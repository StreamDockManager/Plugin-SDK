//! Current-envelope plugin-owned JSON state with atomic replacement and fail-closed reads
use crate::{error, Result};
use serde_json::Value;
use std::{
    fs,
    io::Read,
    path::{Path, PathBuf},
};
pub const MAXIMUM_BYTES: usize = 8 << 20;
#[cfg(test)]
use std::sync::atomic::{AtomicU64, Ordering};
#[cfg(test)]
static SEQUENCE: AtomicU64 = AtomicU64::new(0);

// Windows may report NotFound when an ancestor is a regular file. Confirm a
// genuinely absent path before treating it as fresh state or clearing a guard
fn missing_path(path: &Path) -> std::io::Result<bool> {
    for (index, ancestor) in path.ancestors().enumerate() {
        let ancestor = if ancestor.as_os_str().is_empty() {
            Path::new(".")
        } else {
            ancestor
        };
        match fs::symlink_metadata(ancestor) {
            Ok(_) => {
                if index == 0 {
                    return Ok(false);
                }
                // Follow every directory alias, including Windows junctions,
                // and propagate inaccessible or dangling-target failures
                let metadata = fs::metadata(ancestor)?;
                if !metadata.is_dir() {
                    return Err(std::io::Error::new(
                        std::io::ErrorKind::NotADirectory,
                        "state path ancestor is not a directory",
                    ));
                }
                return Ok(true);
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(error) => return Err(error),
        }
    }
    Ok(false)
}
// Write data through the owned transport or state backend for local state persistence
pub fn write(path: &Path, value: &Value) -> Result<Vec<u8>> {
    let data = serde_json::to_vec(value)?;
    write_encoded(path, &data)?;
    Ok(data)
}
// Atomically replace the persisted encoded state
fn write_encoded(path: &Path, data: &[u8]) -> Result<()> {
    if data.len() > MAXIMUM_BYTES {
        return Err(error("state exceeds 8 MiB"));
    }
    let parent = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    let mut directories = fs::DirBuilder::new();
    directories.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        directories.mode(0o700);
    }
    directories.create(parent)?;
    crate::atomicfile::write(path, data, crate::atomicfile::Policy::Durable)?;
    Ok(())
}

pub struct Store {
    pub path: PathBuf,
    pub version: u32,
    pub value: Value,
    pub due: Option<u64>,
    pub dirty: bool,
    written: Vec<u8>,
    load_blocked: bool,
}
impl Store {
    // Initialize the instance and its owned state for local state persistence
    pub fn new(path: impl Into<PathBuf>, version: u32) -> Self {
        Self {
            path: path.into(),
            version,
            value: Value::Null,
            due: None,
            dirty: false,
            written: vec![],
            load_blocked: false,
        }
    }
    // Read and validate persisted state before exposing it
    pub fn load(&mut self) -> Result<Option<Value>> {
        self.load_blocked = true;
        if self.version != 1 {
            return Err(error("unsupported state version: current version is 1"));
        }
        let file = match fs::File::open(&self.path) {
            Ok(f) => f,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => return self.not_found(e),
            Err(e) => return Err(e.into()),
        };
        let mut data = vec![];
        file.take((MAXIMUM_BYTES + 1) as u64)
            .read_to_end(&mut data)?;
        if data.len() > MAXIMUM_BYTES {
            self.load_blocked = true;
            return Err(error("state exceeds 8 MiB"));
        }
        let envelope: Value = serde_json::from_slice(&data)?;
        if envelope.get("version").and_then(Value::as_u64) != Some(1)
            || envelope.get("data").is_none()
        {
            return Err(error(
                "unsupported state format: version 1 envelope required",
            ));
        }
        let value = envelope["data"].clone();
        self.load_blocked = false;
        self.written = data;
        self.value = value.clone();
        Ok(Some(value))
    }
    // Handle confirmed missing state without hiding access errors
    fn not_found(&mut self, original: std::io::Error) -> Result<Option<Value>> {
        if !missing_path(&self.path)? {
            return Err(original.into());
        }
        if self.load_blocked {
            self.written.clear();
        }
        self.load_blocked = false;
        Ok(None)
    }
    // Update local state and mark it for persistence
    pub fn change(&mut self, value: Value, now: u64) {
        self.value = value;
        self.dirty = true;
        if self.due.is_none() {
            self.due = Some(now.saturating_add(250))
        }
    }
    // Write pending state or bytes to the owned destination
    pub fn flush(&mut self, now: u64, force: bool) -> Result<bool> {
        if self.load_blocked {
            return Err(error(
                "saved state is protected: repair or move it and reload before writing",
            ));
        }
        if self.version != 1 {
            return Err(error("unsupported state version: current version is 1"));
        }
        if !self.dirty || !force && self.due.is_some_and(|due| now < due) {
            return Ok(false);
        }
        struct Envelope<'a> {
            data: &'a Value,
            version: u32,
        }
        impl serde::Serialize for Envelope<'_> {
            // Encode the current state for a single flush
            fn serialize<S: serde::Serializer>(
                &self,
                serializer: S,
            ) -> std::result::Result<S::Ok, S::Error> {
                use serde::ser::SerializeMap;
                // Match the original json! envelope under either serde_json map
                // implementation. Downstream feature unification can enable
                // preserve_order even when this crate does not request it
                let mut keys = serde_json::Map::new();
                keys.insert("version".into(), Value::Null);
                keys.insert("data".into(), Value::Null);
                let mut map = serializer.serialize_map(Some(2))?;
                for key in keys.keys() {
                    if key == "version" {
                        map.serialize_entry(key, &self.version)?;
                    } else {
                        map.serialize_entry(key, self.data)?;
                    }
                }
                map.end()
            }
        }
        let data = serde_json::to_vec(&Envelope {
            data: &self.value,
            version: self.version,
        })?;
        if data.len() > MAXIMUM_BYTES {
            return Err(error("state exceeds 8 MiB"));
        }
        if data != self.written {
            write_encoded(&self.path, &data)?;
            self.written = data;
        };
        self.dirty = false;
        self.due = None;
        Ok(true)
    }
    // Close the owned session and release pending work for local state persistence
    pub fn close(&mut self) -> Result<bool> {
        self.flush(0, true)
    }
}

// Resolve the local storage filename for this identity
pub fn path_for(root: &Path, identity: &str) -> Result<PathBuf> {
    if identity.is_empty()
        || identity.len() > 180
        || identity.contains("..")
        || !identity
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
    {
        return Err(error("invalid plugin state identity"));
    }
    Ok(root
        .join("stream-dock-manager-plugins")
        .join(identity)
        .join("state.json"))
}
// Select the normal local storage directory
pub fn default_path(identity: &str) -> Result<PathBuf> {
    let root = if let Some(root) = std::env::var_os("STREAM_DOCK_MANAGER_PLUGIN_STATE_DIR") {
        PathBuf::from(root)
    } else if cfg!(windows) {
        PathBuf::from(
            std::env::var_os("APPDATA")
                .ok_or_else(|| error("user configuration directory unavailable"))?,
        )
    } else {
        let home = PathBuf::from(
            std::env::var_os("HOME")
                .ok_or_else(|| error("user configuration directory unavailable"))?,
        );
        if cfg!(target_os = "macos") {
            home.join("Library/Application Support")
        } else {
            std::env::var_os("XDG_CONFIG_HOME")
                .map(PathBuf::from)
                .unwrap_or_else(|| home.join(".config"))
        }
    };
    path_for(&root, identity)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    // Verify ambiguous not found does not clear protected state
    fn ambiguous_not_found_does_not_clear_protected_state() {
        let root = std::env::temp_dir().join(format!(
            "stream-dock-manager-storage-missing-{}-{}",
            std::process::id(),
            SEQUENCE.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&root).unwrap();
        fs::write(root.join("blocked"), b"keep").unwrap();
        let mut store = Store::new(root.join("blocked/missing/nested/state.json"), 1);
        store.load_blocked = true;
        store.written = b"previous bytes".to_vec();
        store.change(serde_json::json!({"previous": true}), 0);
        let missing = || std::io::Error::new(std::io::ErrorKind::NotFound, "ambiguous missing");
        assert!(store.not_found(missing()).is_err());
        assert!(store.load_blocked && store.dirty);
        assert_eq!(store.due, Some(250));
        assert_eq!(store.written, b"previous bytes");
        assert!(store.close().unwrap_err().to_string().contains("protected"));
        assert_eq!(fs::read(root.join("blocked")).unwrap(), b"keep");
        store.path = root.join("absent/nested/state.json");
        assert_eq!(store.not_found(missing()).unwrap(), None);
        assert!(!store.load_blocked);
        assert!(store.written.is_empty());
        fs::remove_dir_all(root).unwrap();
    }
}
