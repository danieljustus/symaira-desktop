//! Native Go-compatible file reads and path error presentation.

use std::{
    fs::OpenOptions,
    io::{self, Read},
    path::Path,
};

/// Reads like Go's `os.ReadFile`, retaining the failing operation for diagnostics.
pub fn read(path: &Path) -> Result<Vec<u8>, (&'static str, io::Error)> {
    let mut options = OpenOptions::new();
    options.read(true);
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        // Go can open a directory, then reports its ReadFile error, rather than
        // treating every access-denied open as a directory-read failure.
        const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
        options.custom_flags(FILE_FLAG_BACKUP_SEMANTICS);
    }
    let mut file = options.open(path).map_err(|error| ("open", error))?;
    let mut contents = Vec::new();
    file.read_to_end(&mut contents)
        .map_err(|error| ("read", error))?;
    Ok(contents)
}

/// The text-read boundary preserves Rust's existing invalid-UTF-8 rejection.
pub fn read_to_string(path: &Path) -> Result<String, (&'static str, io::Error)> {
    String::from_utf8(read(path)?).map_err(|_| {
        (
            "read",
            io::Error::new(
                io::ErrorKind::InvalidData,
                "stream did not contain valid UTF-8",
            ),
        )
    })
}

#[must_use]
pub fn path_error(operation: &str, path: &Path, error: &std::io::Error) -> String {
    format!("{operation} {}: {}", path.display(), io_error(error))
}

fn io_error(error: &std::io::Error) -> String {
    match error.kind() {
        std::io::ErrorKind::NotFound => {
            if cfg!(windows) {
                if error.raw_os_error() == Some(3) {
                    "The system cannot find the path specified.".to_owned()
                } else {
                    "The system cannot find the file specified.".to_owned()
                }
            } else {
                "no such file or directory".to_owned()
            }
        }
        std::io::ErrorKind::PermissionDenied => {
            if cfg!(windows) {
                "Access is denied.".to_owned()
            } else {
                "permission denied".to_owned()
            }
        }
        _ => {
            let mut message = error.to_string();
            if let Some(index) = message.rfind(" (os error ") {
                message.truncate(index);
            }
            if !cfg!(windows) {
                let mut chars = message.chars();
                if let Some(first) = chars.next() {
                    message = first.to_lowercase().collect::<String>() + chars.as_str();
                }
            }
            message
        }
    }
}

#[cfg(test)]
mod tests {
    use super::{path_error, read, read_to_string};
    use std::{fs, io};

    #[test]
    fn file_reads_retain_open_read_and_permission_error_distinctions() {
        let root = std::env::temp_dir().join(format!(
            "symdesk-go-read-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir(&root).unwrap();
        let missing = root.join("missing");
        let (operation, error) = read(&missing).unwrap_err();
        assert_eq!(operation, "open");
        assert_eq!(error.kind(), io::ErrorKind::NotFound);
        let regular = root.join("regular");
        fs::write(&regular, b"contents").unwrap();
        assert_eq!(read_to_string(&regular).unwrap(), "contents");
        fs::write(&regular, [0xff]).unwrap();
        let (operation, error) = read_to_string(&regular).unwrap_err();
        assert_eq!(operation, "read");
        assert_eq!(error.kind(), io::ErrorKind::InvalidData);
        let (operation, error) = read(&root).unwrap_err();
        assert_eq!(operation, "read");
        #[cfg(windows)]
        {
            assert_eq!(error.raw_os_error(), Some(1));
            assert_eq!(
                path_error(operation, &root, &error),
                format!("read {}: Incorrect function.", root.display())
            );
            // Synthetic access-denied control, separate from the real directory read.
            assert_eq!(
                path_error("open", &regular, &io::Error::from_raw_os_error(5)),
                format!("open {}: Access is denied.", regular.display())
            );
        }
        #[cfg(not(windows))]
        assert!(path_error(operation, &root, &error).ends_with(": is a directory"));
        fs::remove_dir_all(root).unwrap();
    }
}
