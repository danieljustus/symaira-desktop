//! Matches the Go allocator's cross-process lock on each platform.
use std::{
    fs,
    path::{Path, PathBuf},
};
pub(super) struct AsnLock {
    #[cfg(not(windows))]
    _file: fs::File,
    #[cfg(windows)]
    path: PathBuf,
}
impl AsnLock {
    pub(super) fn acquire(root: &Path) -> Result<Self, String> {
        let dir = root.join(".symdesk");
        let mut builder = fs::DirBuilder::new();
        builder.recursive(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            builder.mode(0o700);
        }
        builder
            .create(&dir)
            .map_err(|e| format!("create ASN lock directory: {e}"))?;
        Self::open(dir.join("asn.lock"))
    }
    #[cfg(not(windows))]
    fn open(path: PathBuf) -> Result<Self, String> {
        let mut options = fs::OpenOptions::new();
        options.create(true).truncate(false).read(true).write(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        let file = options
            .open(path)
            .map_err(|e| format!("open ASN lock: {e}"))?;
        file.lock()
            .map_err(|e| format!("lock ASN allocation: {e}"))?;
        Ok(Self { _file: file })
    }
    #[cfg(windows)]
    fn open(path: PathBuf) -> Result<Self, String> {
        let deadline = std::time::Instant::now() + std::time::Duration::from_secs(30);
        loop {
            match fs::OpenOptions::new()
                .create_new(true)
                .read(true)
                .write(true)
                .open(&path)
            {
                Ok(file) => {
                    drop(file);
                    return Ok(Self { path });
                }
                Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {
                    if std::time::Instant::now() > deadline {
                        return Err("ASN allocation is already in progress".into());
                    }
                    std::thread::sleep(std::time::Duration::from_millis(50));
                }
                Err(e) => return Err(format!("create ASN lock: {e}")),
            }
        }
    }
}
#[cfg(windows)]
impl Drop for AsnLock {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.path);
    }
}
