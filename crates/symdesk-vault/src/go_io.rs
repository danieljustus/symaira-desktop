//! Native Go-compatible path error presentation shared by vault services.

use std::path::Path;

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
