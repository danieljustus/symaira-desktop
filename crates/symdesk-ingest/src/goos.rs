//! Small Go `os`/`path/filepath` behaviours the ingest pipeline relies on, with
//! Go's error text (`op PATH: reason`).

use std::fs;
use std::io;
use std::path::Path;
use symdesk_vault::go_path_error;

/// Go `os.MkdirAll(path, mode)`: an existing non-directory in the way is
/// `mkdir PATH: not a directory`; a race that created the directory is fine.
pub fn mkdir_all(path: &str, mode: u32) -> Result<(), String> {
    if let Ok(meta) = fs::metadata(path) {
        return if meta.is_dir() {
            Ok(())
        } else {
            Err(format!("mkdir {path}: not a directory"))
        };
    }
    let trimmed = path.trim_end_matches('/');
    if let Some(i) = trimmed.rfind('/') {
        let parent = if i == 0 { "/" } else { &trimmed[..i] };
        mkdir_all(parent, mode)?;
    }
    let mut builder = fs::DirBuilder::new();
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(mode);
    }
    #[cfg(not(unix))]
    let _ = mode;
    match builder.create(path) {
        Ok(()) => Ok(()),
        Err(e) => {
            if fs::symlink_metadata(path).is_ok_and(|m| m.is_dir()) {
                Ok(())
            } else {
                Err(go_path_error("mkdir", Path::new(path), &e))
            }
        }
    }
}

/// `op PATH: reason` for an I/O failure.
#[must_use]
pub fn path_error(op: &str, path: &str, e: &io::Error) -> String {
    go_path_error(op, Path::new(path), e)
}

/// Go `filepath.Ext`.
#[must_use]
pub fn ext(path: &str) -> &str {
    for (i, c) in path.char_indices().rev() {
        if c == '/' {
            break;
        }
        if c == '.' {
            return &path[i..];
        }
    }
    ""
}

/// Go `filepath.Base`.
#[must_use]
pub fn base(path: &str) -> String {
    if path.is_empty() {
        return ".".into();
    }
    let trimmed = path.trim_end_matches('/');
    if trimmed.is_empty() {
        return "/".into();
    }
    trimmed.rsplit('/').next().unwrap_or(trimmed).to_owned()
}

/// Go `filepath.Dir`.
#[must_use]
pub fn dir(path: &str) -> String {
    let split = path.rfind('/').map_or(0, |i| i + 1);
    let head = &path[..split];
    let cleaned = crate::ocr::go_clean(head);
    if split == 0 { ".".into() } else { cleaned }
}

/// Go `filepath.Join` for unix paths: empty elements are ignored, the result is
/// cleaned; no non-empty element yields the empty string.
#[must_use]
pub fn join(parts: &[&str]) -> String {
    let joined: Vec<&str> = parts.iter().copied().filter(|p| !p.is_empty()).collect();
    if joined.is_empty() {
        return String::new();
    }
    crate::ocr::go_clean(&joined.join("/"))
}

/// Go `filepath.Abs`.
pub fn abs(path: &str) -> Result<String, String> {
    if path.starts_with('/') {
        return Ok(crate::ocr::go_clean(path));
    }
    let cwd = std::env::current_dir().map_err(|e| format!("getwd: {e}"))?;
    Ok(join(&[&cwd.to_string_lossy(), path]))
}

/// Go `filepath.Rel` for cleaned absolute unix paths.
#[must_use]
pub fn rel(base: &str, target: &str) -> Option<String> {
    let b = crate::ocr::go_clean(base);
    let t = crate::ocr::go_clean(target);
    if b == t {
        return Some(".".into());
    }
    if b.starts_with('/') != t.starts_with('/') {
        return None;
    }
    let bs: Vec<&str> = b.split('/').filter(|s| !s.is_empty()).collect();
    let ts: Vec<&str> = t.split('/').filter(|s| !s.is_empty()).collect();
    let common = bs.iter().zip(&ts).take_while(|(x, y)| x == y).count();
    let mut out: Vec<&str> = vec![".."; bs.len() - common];
    out.extend(&ts[common..]);
    Some(out.join("/"))
}

/// Go's text for an I/O error without the `op PATH:` prefix.
#[must_use]
pub fn io_text(e: &io::Error) -> String {
    go_path_error("x", Path::new(""), e)
        .strip_prefix("x : ")
        .unwrap_or_default()
        .to_owned()
}
