#![deny(unsafe_code)]

use std::{
    ffi::OsStr,
    fs, io,
    path::{Path, PathBuf},
};

const SKIP_DIRECTORIES: &[&str] = &[
    "node_modules",
    "vendor",
    "dist",
    "build",
    "venv",
    ".venv",
    "__pycache__",
];

#[derive(Clone, Debug, Eq, PartialEq)]
pub enum WalkEntryType {
    File,
    Symlink,
    Other,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct WalkEntry {
    pub path: PathBuf,
    pub entry_type: WalkEntryType,
    pub symlink_target: Option<PathBuf>,
}

/// Returns every non-ignored file/symlink in Go-compatible lexical order.
///
/// # Errors
///
/// Propagates directory reads and symlink-target read failures.
pub fn walk_all(root: &Path) -> io::Result<Vec<WalkEntry>> {
    let mut output = Vec::new();
    let root = root.canonicalize()?;
    walk_directory(&root, &root, &mut output)?;
    Ok(output)
}

/// Go's `vault.IsExternalSymlink`: `path` is a symlink whose target is missing
/// or resolves outside the canonical `vault_root`. Contained links, regular
/// files and paths that cannot be inspected report `false`.
#[must_use]
pub fn is_external_symlink(vault_root: &Path, path: &Path) -> bool {
    if !fs::symlink_metadata(path).is_ok_and(|metadata| metadata.file_type().is_symlink()) {
        return false;
    }
    let Ok(canonical_root) = fs::canonicalize(vault_root) else {
        return true;
    };
    fs::canonicalize(path).map_or(true, |target| !target.starts_with(canonical_root))
}

/// Returns only entries whose extension is exactly lowercase `.md`.
///
/// # Errors
///
/// Propagates errors from [`walk_all`].
pub fn walk_markdown(root: &Path) -> io::Result<Vec<PathBuf>> {
    let mut output = Vec::new();
    walk_markdown_with(root, |path| {
        output.push(path.to_path_buf());
        Ok(())
    })?;
    Ok(output)
}

/// Visits lowercase `.md` entries as they are discovered.
///
/// Unlike [`walk_markdown`], this preserves already-visited entries when a
/// later directory or callback operation fails. Callers that queue work can
/// therefore flush that work before returning the error.
///
/// # Errors
///
/// Propagates directory reads, symlink-target read failures, and callback
/// errors.
pub fn walk_markdown_with<F>(root: &Path, mut callback: F) -> io::Result<()>
where
    F: FnMut(&Path) -> io::Result<()>,
{
    let root = root.canonicalize()?;
    walk_directory_visit(&root, &root, &mut |entry| {
        if entry.path.extension() == Some(OsStr::new("md")) {
            callback(&entry.path)?;
        }
        Ok(())
    })
}

fn walk_directory(root: &Path, directory: &Path, output: &mut Vec<WalkEntry>) -> io::Result<()> {
    walk_directory_visit(root, directory, &mut |entry| {
        output.push(entry);
        Ok(())
    })
}

fn walk_directory_visit<F>(root: &Path, directory: &Path, callback: &mut F) -> io::Result<()>
where
    F: FnMut(WalkEntry) -> io::Result<()>,
{
    let directory = directory.canonicalize()?;
    if !directory.starts_with(root) {
        return Err(io::Error::new(
            io::ErrorKind::PermissionDenied,
            "directory resolves outside vault root",
        ));
    }
    let mut entries: Vec<_> = fs::read_dir(&directory)?.collect::<Result<_, _>>()?;
    entries.sort_by_key(fs::DirEntry::file_name);
    for entry in entries {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        let file_type = entry.file_type()?;
        if file_type.is_dir() {
            if name.starts_with('.') || SKIP_DIRECTORIES.contains(&name.as_ref()) {
                continue;
            }
            walk_directory_visit(root, &entry.path(), callback)?;
            continue;
        }
        if name.starts_with('.') {
            continue;
        }
        let relative = entry
            .path()
            .strip_prefix(root)
            .expect("read_dir descendant remains below root")
            .to_path_buf();
        let (entry_type, symlink_target) = if file_type.is_symlink() {
            (WalkEntryType::Symlink, Some(fs::read_link(entry.path())?))
        } else if file_type.is_file() {
            (WalkEntryType::File, None)
        } else {
            (WalkEntryType::Other, None)
        };
        callback(WalkEntry {
            path: relative,
            entry_type,
            symlink_target,
        })?;
    }
    Ok(())
}

#[cfg(all(test, unix))]
mod external_symlink_tests {
    use std::{fs, os::unix::fs::symlink};

    use super::is_external_symlink;

    #[test]
    fn classifies_contained_external_broken_and_regular_paths() {
        let nonce = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("system time")
            .as_nanos();
        let base = std::env::temp_dir().join(format!(
            "symdesk-external-symlink-{}-{nonce}",
            std::process::id()
        ));
        let vault = base.join("vault");
        let outside = base.join("outside");
        fs::create_dir_all(vault.join("sub")).expect("vault directory");
        fs::create_dir_all(&outside).expect("outside directory");
        fs::write(vault.join("sub/target.md"), "# T\n").expect("contained target");
        fs::write(outside.join("secret.md"), "# S\n").expect("outside target");
        symlink("sub/target.md", vault.join("contained.md")).expect("contained link");
        symlink("../outside/secret.md", vault.join("external.md")).expect("external link");
        symlink("missing.md", vault.join("broken.md")).expect("broken link");

        assert!(!is_external_symlink(&vault, &vault.join("sub/target.md")));
        assert!(!is_external_symlink(&vault, &vault.join("contained.md")));
        assert!(is_external_symlink(&vault, &vault.join("external.md")));
        assert!(is_external_symlink(&vault, &vault.join("broken.md")));
        assert!(!is_external_symlink(&vault, &vault.join("absent.md")));
        fs::remove_dir_all(&base).expect("remove fixture");
    }
}
