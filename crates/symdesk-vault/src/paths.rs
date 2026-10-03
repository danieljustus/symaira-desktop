#![deny(unsafe_code)]

use std::{
    fs, io,
    path::{Component, Path, PathBuf},
};

use thiserror::Error;

#[derive(Debug, Error)]
pub enum SecurePathError {
    #[error("path traversal denied: {0} is outside vault")]
    Traversal(String),
    #[error("cannot resolve vault root: {0}")]
    Vault(io::Error),
    #[error("cannot resolve path: {0}")]
    Path(io::Error),
    #[error("cannot resolve parent directory: {0}")]
    Parent(io::Error),
    #[error("symlink escape denied: {0} resolves outside vault")]
    SymlinkEscape(String),
    #[error("resolve absolute path: {0}")]
    Absolute(io::Error),
}

/// Resolves a path with Go `filepath.Join` compatibility and symlink confinement.
///
/// # Errors
///
/// Rejects lexical traversal and canonical symlink escape.
pub fn secure_path(vault_root: &Path, requested: &str) -> Result<PathBuf, SecurePathError> {
    if cfg!(windows) && has_windows_volume(requested) {
        return Err(SecurePathError::Traversal(requested.to_owned()));
    }
    let absolute_vault = absolute(vault_root).map_err(SecurePathError::Absolute)?;
    // Go filepath.Join(root, "/etc/passwd") keeps the root on the pinned
    // Unix oracle. Strip root components before joining to preserve it.
    let relative_request: PathBuf = Path::new(requested)
        .components()
        .filter(|component| !matches!(component, Component::RootDir))
        .collect();
    let absolute_target = lexical_clean(&absolute_vault.join(relative_request));
    if !under(&absolute_target, &absolute_vault) {
        return Err(SecurePathError::Traversal(requested.to_owned()));
    }
    let canonical_vault = fs::canonicalize(&absolute_vault).map_err(SecurePathError::Vault)?;
    let canonical_target = canonicalize_missing(&absolute_target)?;
    if !under(&canonical_target, &canonical_vault) {
        return Err(SecurePathError::SymlinkEscape(requested.to_owned()));
    }
    Ok(canonical_target)
}

// Match the nonempty-volume decision of Go 1.26.6 filepath.VolumeName.
// Rust's Path prefix parser excludes numeric drives and incomplete UNC roots.
fn has_windows_volume(requested: &str) -> bool {
    let bytes = requested.as_bytes();
    let separator = |byte: u8| byte == b'/' || byte == b'\\';
    if bytes.len() >= 2 && bytes[1] == b':' {
        return true;
    }
    if bytes.first().is_none_or(|&byte| !separator(byte)) {
        return false;
    }
    if bytes.get(1).is_some_and(|&byte| separator(byte)) {
        return true;
    }
    bytes.get(1..3) == Some(b"??")
        && (bytes.len() == 3 || bytes.get(3).is_some_and(|&byte| separator(byte)))
}

fn absolute(path: &Path) -> io::Result<PathBuf> {
    if path.is_absolute() {
        Ok(lexical_clean(path))
    } else {
        Ok(lexical_clean(&std::env::current_dir()?.join(path)))
    }
}

fn canonicalize_missing(path: &Path) -> Result<PathBuf, SecurePathError> {
    if fs::metadata(path).is_ok() {
        return fs::canonicalize(path).map_err(SecurePathError::Path);
    }
    let mut parent = path.to_path_buf();
    while fs::metadata(&parent).is_err() {
        let Some(next) = parent.parent() else {
            break;
        };
        if next == parent {
            break;
        }
        parent = next.to_path_buf();
    }
    let resolved = fs::canonicalize(&parent).map_err(SecurePathError::Parent)?;
    let remaining = path.strip_prefix(&parent).unwrap_or(Path::new(""));
    Ok(lexical_clean(&resolved.join(remaining)))
}

fn under(path: &Path, root: &Path) -> bool {
    path == root || path.starts_with(root)
}

fn lexical_clean(path: &Path) -> PathBuf {
    let mut output = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                let _ = output.pop();
            }
            other => output.push(other.as_os_str()),
        }
    }
    output
}

#[cfg(test)]
mod tests {
    use super::*;

    struct TestRoot(PathBuf);
    impl Drop for TestRoot {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }

    #[test]
    fn native_volume_coordinates_follow_go_confinement() {
        let root = TestRoot(std::env::temp_dir().join(format!(
            "symdesk-native-volume-paths-{}-{}",
            std::process::id(),
            getrandom::u64().unwrap()
        )));
        fs::create_dir(&root.0).unwrap();
        for requested in [
            r"C:\outside\note.md",
            r"C:outside.md",
            r"1:outside.md",
            r"::outside.md",
            r"\\server\share\note.md",
            r"\\server",
            r"\??\C:\outside.md",
            r"\??",
            #[cfg(windows)]
            "//server/share/note.md",
            #[cfg(windows)]
            r"/??/C:/outside.md",
            #[cfg(windows)]
            r"\/server/share/note.md",
        ] {
            let result = secure_path(&root.0, requested);
            #[cfg(windows)]
            {
                let error = result.unwrap_err();
                assert!(matches!(error, SecurePathError::Traversal(_)));
                assert_eq!(
                    error.to_string(),
                    format!("path traversal denied: {requested} is outside vault")
                );
            }
            #[cfg(unix)]
            assert_eq!(
                result.unwrap(),
                fs::canonicalize(&root.0).unwrap().join(requested)
            );
        }
        fs::remove_dir_all(&root.0).unwrap();
    }
}
