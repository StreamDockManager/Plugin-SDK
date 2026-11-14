//! Fingerprints and verified cache records; no build or registry access
use crate::Result;
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::{collections::BTreeMap, fs, io::Read, path::Path};
// Hash the file contents for cache validation
fn file_hash(path: &Path) -> Result<String> {
    let mut file = fs::File::open(path)?;
    let mut hash = Sha256::new();
    let mut buffer = [0; 64 << 10];
    loop {
        let size = match file.read(&mut buffer) {
            Err(error) if error.kind() == std::io::ErrorKind::Interrupted => continue,
            result => result?,
        };
        if size == 0 {
            break;
        }
        hash.update(&buffer[..size]);
    }
    Ok(hash.finalize().iter().map(|b| format!("{b:02x}")).collect())
}
// Hash the declared build inputs into a cache identity
pub fn fingerprint(inputs: &BTreeMap<String, Vec<u8>>, configuration: &Value) -> Result<String> {
    let mut h = Sha256::new();
    h.update(serde_json::to_vec(configuration)?);
    for (name, data) in inputs {
        h.update(format!("{name}\0{}\0", data.len()));
        h.update(data);
    }
    Ok(h.finalize().iter().map(|b| format!("{b:02x}")).collect())
}
// Validate and retrieve a previously cached build artifact
pub fn cached(path: &Path, key: &str) -> bool {
    let check = || -> Result<bool> {
        let record: Value =
            serde_json::from_slice(&fs::read(format!("{}.build.json", path.display()))?)?;
        Ok(record["input"] == key && record["output"] == file_hash(path)?)
    };
    check().unwrap_or(false)
}
// Record the artifact and its validated build identity
pub fn remember(path: &Path, key: &str) -> Result<()> {
    let destination = format!("{}.build.json", path.display());
    let data = serde_json::to_vec(&json!({"input":key,"output":file_hash(path)?}))?;
    crate::atomicfile::write(
        Path::new(&destination),
        &data,
        crate::atomicfile::Policy::Cache,
    )?;
    Ok(())
}

// Run independent build jobs with bounded concurrency
pub fn parallel<T: Send>(
    items: Vec<T>,
    jobs: usize,
    cancel: std::sync::Arc<std::sync::atomic::AtomicBool>,
    work: impl Fn(T) -> Result<()> + Sync,
) -> Result<()> {
    if !(1..=128).contains(&jobs) {
        return Err(crate::error("jobs must be between 1 and 128"));
    }
    let queue = std::sync::Mutex::new(items.into_iter());
    let errors = std::sync::Mutex::new(Vec::new());
    std::thread::scope(|scope| {
        for _ in 0..jobs {
            let work = &work;
            let queue = &queue;
            let errors = &errors;
            let cancel = &cancel;
            scope.spawn(move || loop {
                let item = queue.lock().unwrap().next();
                let Some(item) = item else { break };
                let result = if cancel.load(std::sync::atomic::Ordering::Acquire) {
                    Err(crate::error("build cancelled"))
                } else {
                    work(item)
                };
                if let Err(error) = result {
                    errors.lock().unwrap().push(error);
                }
            });
        }
    });
    if let Some(error) = errors.into_inner().unwrap().pop() {
        Err(error)
    } else {
        Ok(())
    }
}
