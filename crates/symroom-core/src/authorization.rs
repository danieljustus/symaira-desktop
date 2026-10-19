//! Room authorization transaction ported from Go `internal/room/authorization`
//! (#1043): approval decisions and membership mutations hold one OS file lock
//! across replay, authorization, signing and append, shared by independent
//! processes and root aliases. `std::fs::File::lock` uses `flock` on Unix and
//! `LockFileEx` on Windows, so Go and Rust writers exclude each other.

use std::fs::{self, File, OpenOptions, TryLockError};
use std::io;
use std::path::Path;

/// Held room transaction; dropping it releases the kernel lock.
#[derive(Debug)]
pub struct Guard(#[allow(dead_code)] File);

/// Run `operation` while holding the room transaction, waiting for other writers.
pub fn with_room<T, E: From<io::Error>>(
    room_dir: &Path,
    operation: impl FnOnce() -> Result<T, E>,
) -> Result<T, E> {
    let file = open(room_dir)?;
    file.lock()?;
    let _guard = Guard(file);
    operation()
}

/// Try to take the room transaction without waiting. `Ok(None)` means another
/// writer currently owns it (Go `ErrBusy`).
pub fn try_acquire(room_dir: &Path) -> io::Result<Option<Guard>> {
    let file = open(room_dir)?;
    match file.try_lock() {
        Ok(()) => Ok(Some(Guard(file))),
        Err(TryLockError::WouldBlock) => Ok(None),
        Err(TryLockError::Error(error)) => Err(error),
    }
}

// The non-authoritative lock lives in ignored local .symroom state. Never
// remove the file: another process may already be waiting on its inode.
// Go confines this through os.Root; Rust refuses a symlinked state directory
// or lock file so the lock can never be created outside the room.
fn open(room_dir: &Path) -> io::Result<File> {
    let state = room_dir.join(".symroom");
    let mut builder = fs::DirBuilder::new();
    #[cfg(unix)]
    std::os::unix::fs::DirBuilderExt::mode(&mut builder, 0o700);
    match builder.create(&state) {
        Ok(()) => {}
        Err(error) if error.kind() == io::ErrorKind::AlreadyExists => {}
        Err(error) => return Err(error),
    }
    if !fs::symlink_metadata(&state)?.is_dir() {
        return Err(io::Error::other(
            "room authorization state is not a directory",
        ));
    }
    let path = state.join("authorization.lock");
    if fs::symlink_metadata(&path).is_ok_and(|meta| !meta.is_file()) {
        return Err(io::Error::other(
            "room authorization lock is not a regular file",
        ));
    }
    let mut options = OpenOptions::new();
    options.create(true).read(true).write(true).truncate(false);
    #[cfg(unix)]
    std::os::unix::fs::OpenOptionsExt::mode(&mut options, 0o600);
    options.open(path)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn temp_dir(label: &str) -> std::path::PathBuf {
        let mut nonce = [0_u8; 8];
        getrandom::fill(&mut nonce).expect("nonce");
        let path = std::env::temp_dir().join(format!(
            "symroom-authz-{label}-{}-{}",
            std::process::id(),
            hex::encode(nonce)
        ));
        fs::create_dir_all(&path).expect("temp dir");
        path
    }

    #[test]
    fn transaction_preserves_error_and_releases_on_error() {
        let root = temp_dir("release");
        let result: Result<(), io::Error> =
            with_room(&root, || Err(io::Error::other("operation rejected")));
        assert_eq!(result.unwrap_err().to_string(), "operation rejected");
        let guard = try_acquire(&root).expect("acquire");
        assert!(guard.is_some(), "failed operation retained its lock");
        drop(guard);
        assert!(try_acquire(&root).expect("reacquire").is_some());
        fs::remove_dir_all(root).ok();
    }

    #[cfg(unix)]
    #[test]
    fn root_alias_shares_kernel_lock() {
        let parent = temp_dir("alias");
        let root = parent.join("room");
        fs::create_dir(&root).expect("room");
        let alias = parent.join("alias");
        std::os::unix::fs::symlink(&root, &alias).expect("alias");
        with_room(&root, || {
            assert!(
                try_acquire(&alias)?.is_none(),
                "root alias bypassed held kernel lock"
            );
            Ok::<_, io::Error>(())
        })
        .expect("transaction");
        fs::remove_dir_all(parent).ok();
    }

    #[cfg(unix)]
    #[test]
    fn local_state_cannot_escape_root() {
        let root = temp_dir("escape-root");
        let outside = temp_dir("escape-outside");
        std::os::unix::fs::symlink(&outside, root.join(".symroom")).expect("symlink");
        let mut called = false;
        let result = with_room(&root, || {
            called = true;
            Ok::<_, io::Error>(())
        });
        assert!(result.is_err() && !called, "external state accepted");
        assert!(!outside.join("authorization.lock").exists());
        fs::remove_dir_all(root).ok();
        fs::remove_dir_all(outside).ok();
    }
}
