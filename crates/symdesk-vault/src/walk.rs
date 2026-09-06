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
    walk_directory_visit(root, root, &mut |entry| {
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
