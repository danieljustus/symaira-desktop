//! Go-shaped filesystem errors.
//!
//! Go reports an OS failure as `<op> <path>: <strerror>` (`*fs.PathError`),
//! with the path exactly as the caller built it through `filepath.Join`
//! (lexically cleaned, so `./journal` is printed `journal`). The `symroom`
//! command line prints these strings verbatim, so the Rust port builds the same
//! text. The wording is the host's: lower-case libc text on Unix, the Windows
//! system message (capitalised, trailing period) on Windows.
//!
//! The helpers return [`io::Error`] values whose [`io::ErrorKind`] is the
//! original kind, so callers that branch on `NotFound` keep working; only the
//! `Display` text changes. Check `raw_os_error` before wrapping.

use std::{
    fs,
    io::{self, Read},
    path::{Component, Path, PathBuf},
};

/// Go `filepath.Clean` for display: no `.` elements, no repeated separators,
/// `..` resolved lexically, and `.` for an empty result.
#[must_use]
pub fn clean(path: &Path) -> String {
    let mut prefix = String::new();
    let mut rooted = false;
    let mut parts: Vec<String> = Vec::new();
    for component in path.components() {
        match component {
            Component::Prefix(value) => prefix = value.as_os_str().to_string_lossy().into_owned(),
            Component::RootDir => rooted = true,
            Component::CurDir => {}
            Component::ParentDir => {
                if parts.last().is_some_and(|last| last != "..") {
                    parts.pop();
                } else if !rooted {
                    parts.push("..".to_owned());
                }
            }
            Component::Normal(name) => parts.push(name.to_string_lossy().into_owned()),
        }
    }
    let separator = std::path::MAIN_SEPARATOR;
    let mut text = prefix;
    if rooted {
        text.push(separator);
    }
    text.push_str(&parts.join(&separator.to_string()));
    if text.is_empty() {
        ".".to_owned()
    } else {
        text
    }
}

/// Go's `syscall.Errno.Error()` text for an OS error. `windows` selects the host
/// family so both wordings are unit-testable on any OS: Windows keeps the
/// `FormatMessage` text, everything else lower-cases libc's.
#[must_use]
pub fn os_message(error: &io::Error, windows: bool) -> String {
    if windows {
        match error.raw_os_error() {
            Some(1) => return "Incorrect function.".to_owned(),
            Some(2) => return "The system cannot find the file specified.".to_owned(),
            Some(3) => return "The system cannot find the path specified.".to_owned(),
            Some(5) => return "Access is denied.".to_owned(),
            _ => {}
        }
    }
    let mut text = error.to_string();
    if let Some(index) = text.rfind(" (os error ") {
        text.truncate(index);
    }
    if windows {
        return text;
    }
    let mut chars = text.chars();
    chars
        .next()
        .map(|first| first.to_lowercase().collect::<String>() + chars.as_str())
        .unwrap_or_default()
}

/// Go `*fs.PathError` text: `<op> <path>: <strerror>`.
#[must_use]
pub fn path_error_text(operation: &str, path: &Path, error: &io::Error) -> String {
    format!(
        "{operation} {}: {}",
        clean(path),
        os_message(error, cfg!(windows))
    )
}

/// Wraps `error` so its `Display` is the Go path error, keeping the kind.
#[must_use]
pub fn path_error(operation: &str, path: &Path, error: &io::Error) -> io::Error {
    io::Error::new(error.kind(), path_error_text(operation, path, error))
}

fn open_options() -> fs::OpenOptions {
    let mut options = fs::OpenOptions::new();
    options.read(true);
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        // Go opens a directory with backup semantics, so reading it fails in
        // `read` rather than `open`.
        const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
        options.custom_flags(FILE_FLAG_BACKUP_SEMANTICS);
    }
    options
}

/// Opens like Go's `os.Open` but returns the raw OS error, for callers that
/// format the failure themselves.
///
/// # Errors
/// Returns the unmodified OS error.
pub fn open_raw(path: &Path) -> io::Result<fs::File> {
    open_options().open(path)
}

/// Go `os.Open`: failures are `open <path>: ...`.
///
/// # Errors
/// Returns the Go-formatted open error.
pub fn open(path: &Path) -> io::Result<fs::File> {
    open_raw(path).map_err(|error| path_error("open", path, &error))
}

/// Go `os.ReadFile`: `open` failures and, for a directory, `read` failures.
/// The returned error is already Go-formatted.
///
/// # Errors
/// Returns the Go-formatted open or read error.
pub fn read_file(path: &Path) -> io::Result<Vec<u8>> {
    let mut file = open(path)?;
    let mut contents = Vec::new();
    file.read_to_end(&mut contents)
        .map_err(|error| path_error("read", path, &error))?;
    Ok(contents)
}

/// Go `os.ReadDir`: failures are `open <path>: ...`.
///
/// # Errors
/// Returns the Go-formatted open error.
pub fn read_dir(path: &Path) -> io::Result<fs::ReadDir> {
    fs::read_dir(path).map_err(|error| path_error("open", path, &error))
}

/// Go `filepath.Abs`: the cleaned working directory joined with a relative
/// path. An unreadable working directory leaves the path unchanged.
#[must_use]
pub fn abs(path: &Path) -> PathBuf {
    if path.is_absolute() {
        return PathBuf::from(clean(path));
    }
    match std::env::current_dir() {
        Ok(cwd) => PathBuf::from(clean(&cwd.join(path))),
        Err(_) => path.to_path_buf(),
    }
}

#[cfg(test)]
mod tests {
    use super::{clean, os_message, path_error_text, read_dir, read_file};
    use std::{io, path::Path};

    #[test]
    fn clean_matches_go_filepath_clean() {
        assert_eq!(clean(Path::new("./journal")), "journal");
        assert_eq!(
            clean(Path::new("a//b/./c/..")),
            "a/b".replace('/', std::path::MAIN_SEPARATOR_STR)
        );
        assert_eq!(clean(Path::new("")), ".");
        assert_eq!(clean(Path::new(".")), ".");
        assert_eq!(
            clean(Path::new("../x")),
            "..".to_owned() + std::path::MAIN_SEPARATOR_STR + "x"
        );
        #[cfg(unix)]
        {
            assert_eq!(clean(Path::new("/a/../..")), "/");
            assert_eq!(clean(Path::new("/a/b/")), "/a/b");
        }
    }

    #[test]
    fn windows_and_unix_wording() {
        let not_found = io::Error::from_raw_os_error(2);
        assert_eq!(
            os_message(&not_found, true),
            "The system cannot find the file specified."
        );
        assert_eq!(
            os_message(&io::Error::from_raw_os_error(3), true),
            "The system cannot find the path specified."
        );
        #[cfg(unix)]
        {
            assert_eq!(os_message(&not_found, false), "no such file or directory");
            assert_eq!(
                os_message(&io::Error::from_raw_os_error(20), false),
                "not a directory"
            );
        }
    }

    #[test]
    fn read_failures_name_the_go_operation_and_keep_the_kind() {
        let root = std::env::temp_dir().join(format!("symroom-goio-{}", std::process::id()));
        std::fs::create_dir_all(&root).unwrap();
        let missing = root.join("missing.txt");
        let error = read_file(&missing).unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::NotFound);
        assert_eq!(
            error.to_string(),
            path_error_text("open", &missing, &io::Error::from_raw_os_error(2))
        );
        let error = read_dir(&missing).err().unwrap();
        assert!(error.to_string().starts_with("open "));
        std::fs::remove_dir_all(&root).unwrap();
    }
}
