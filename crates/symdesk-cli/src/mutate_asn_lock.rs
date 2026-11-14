//! Matches the Go allocator's cross-process lock.
use std::{
    fs,
    path::{Path, PathBuf},
};
pub(super) struct AsnLock {
    _file: fs::File,
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
}
