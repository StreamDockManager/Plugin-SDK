//! Atomic replacement mechanics; file-sync/permissions remain explicit caller policies
use std::{
    fs::{self, OpenOptions},
    io::{self, Write},
    path::Path,
    sync::atomic::{AtomicU64, Ordering},
};
static SEQUENCE: AtomicU64 = AtomicU64::new(0);
pub(crate) enum Policy {
    Cache,
    Durable,
}
// Write data through the owned transport or state backend for atomic file replacement
pub(crate) fn write(path: &Path, data: &[u8], policy: Policy) -> io::Result<()> {
    let parent = path
        .parent()
        .filter(|p| !p.as_os_str().is_empty())
        .unwrap_or(Path::new("."));
    // create_new protects files left by earlier processes and concurrent writers
    let (temporary, mut file) = loop {
        let temporary = parent.join(format!(
            ".sdk-{}-{}.tmp",
            std::process::id(),
            SEQUENCE.fetch_add(1, Ordering::Relaxed)
        ));
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(if matches!(policy, Policy::Durable) {
                0o600
            } else {
                0o666
            });
        }
        match options.open(&temporary) {
            Ok(file) => break (temporary, file),
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(error) => return Err(error),
        }
    };
    let result = (|| {
        file.write_all(data)?;
        if matches!(policy, Policy::Durable) {
            file.sync_all()?;
        }
        drop(file);
        fs::rename(&temporary, path)
    })();
    let _ = fs::remove_file(temporary);
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    // Verify collision cleanup and concurrent replacement
    fn collision_cleanup_and_concurrent_replacement() {
        let root = std::env::temp_dir().join(format!("sdk-atomic-{}", std::process::id()));
        fs::create_dir_all(&root).unwrap();
        // No production test shares this directory. Preexisting candidates must survive
        let sentinel = root.join(format!(
            ".sdk-{}-{}.tmp",
            std::process::id(),
            SEQUENCE.load(Ordering::Relaxed)
        ));
        fs::write(&sentinel, b"unrelated").unwrap();
        let destination = root.join("value");
        write(&destination, b"initial", Policy::Durable).unwrap();
        assert_eq!(fs::read(&sentinel).unwrap(), b"unrelated");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(&destination).unwrap().permissions().mode() & 0o777,
                0o600
            );
        }
        let directory = root.join("directory");
        fs::create_dir(&directory).unwrap();
        assert!(write(&directory, b"failure", Policy::Cache).is_err());
        assert_eq!(fs::read_dir(&root).unwrap().count(), 3);
        std::thread::scope(|scope| {
            for i in 0..32 {
                let destination = &destination;
                scope.spawn(move || {
                    let bytes = vec![i; 10000];
                    write(
                        destination,
                        &bytes,
                        if i % 2 == 0 {
                            Policy::Cache
                        } else {
                            Policy::Durable
                        },
                    )
                    .unwrap();
                });
            }
        });
        let bytes = fs::read(&destination).unwrap();
        assert_eq!(bytes.len(), 10000);
        assert!(bytes.iter().all(|byte| *byte == bytes[0]));
        assert_eq!(fs::read_dir(&root).unwrap().count(), 3);
        fs::remove_dir_all(root).unwrap();
    }
}
