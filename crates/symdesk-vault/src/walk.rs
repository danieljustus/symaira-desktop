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
    walk_directory(root, root, &mut output)?;
    Ok(output)
}

/// Returns only entries whose extension is exactly lowercase `.md`.
///
/// # Errors
///
/// Propagates errors from [`walk_all`].
pub fn walk_markdown(root: &Path) -> io::Result<Vec<PathBuf>> {
    Ok(walk_all(root)?
        .into_iter()
        .filter(|entry| entry.path.extension() == Some(OsStr::new("md")))
        .map(|entry| entry.path)
        .collect())
}

fn walk_directory(root: &Path, directory: &Path, output: &mut Vec<WalkEntry>) -> io::Result<()> {
    let mut entries: Vec<_> = fs::read_dir(directory)?.collect::<Result<_, _>>()?;
    entries.sort_by_key(fs::DirEntry::file_name);
    for entry in entries {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        let file_type = entry.file_type()?;
        if file_type.is_dir() {
            if name.starts_with('.') || SKIP_DIRECTORIES.contains(&name.as_ref()) {
                continue;
            }
            walk_directory(root, &entry.path(), output)?;
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
        output.push(WalkEntry {
            path: relative,
            entry_type,
            symlink_target,
        });
    }
    Ok(())
}
