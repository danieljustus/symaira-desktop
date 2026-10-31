//! Small Go-formatting helpers shared by the service layer.

use std::{
    io,
    os::unix::process::ExitStatusExt,
    path::{Component, Path, PathBuf},
    process::ExitStatus,
};

/// Go's `os` error text: lowercase libc message without Rust's errno suffix.
#[must_use]
pub fn os_error_text(error: &io::Error) -> String {
    let text = error.to_string();
    let text = text.split(" (os error ").next().unwrap_or(&text);
    let mut chars = text.chars();
    chars.next().map_or_else(String::new, |first| {
        first.to_lowercase().collect::<String>() + chars.as_str()
    })
}

/// Go's `net/http` transport text for a refused loopback connection. The
/// Rust HTTP stacks report `io: Connection refused`; Go reports
/// `Post "<url>": dial tcp <host:port>: connect: connection refused`.
#[must_use]
pub fn go_transport_error(message: &str, endpoint: &str) -> String {
    const REFUSED: &str = "transport_error: io: Connection refused";
    if !message.contains(REFUSED) {
        return message.to_owned();
    }
    let rest = endpoint
        .split_once("://")
        .map_or(endpoint, |(_, rest)| rest);
    let authority = rest.split('/').next().unwrap_or(rest);
    let scheme = endpoint
        .split_once("://")
        .map_or("http", |(scheme, _)| scheme);
    message.replace(
        REFUSED,
        &format!(
            "transport_error: Post \"{scheme}://{authority}/api/generate\": dial tcp {authority}: connect: connection refused"
        ),
    )
}

/// Go `*PathError` text: `op path: reason`.
#[must_use]
pub fn path_error(op: &str, path: &Path, error: &io::Error) -> String {
    format!("{op} {}: {}", path.display(), os_error_text(error))
}

/// `filepath.Join` for Unix: join with `/` and lexically clean.
#[must_use]
pub fn join(parts: &[&str]) -> PathBuf {
    let joined = parts
        .iter()
        .filter(|part| !part.is_empty())
        .copied()
        .collect::<Vec<_>>()
        .join("/");
    if joined.is_empty() {
        return PathBuf::new();
    }
    let mut clean = PathBuf::new();
    for part in Path::new(&joined).components() {
        match part {
            Component::CurDir => (),
            Component::ParentDir => {
                if clean.file_name().is_some_and(|name| name != "..") {
                    clean.pop();
                } else if !clean.has_root() {
                    clean.push("..");
                }
            }
            other => clean.push(other.as_os_str()),
        }
    }
    if clean.as_os_str().is_empty() {
        PathBuf::from(".")
    } else {
        clean
    }
}

/// Go `*exec.ExitError` text: `exit status N` or `signal: name`.
#[must_use]
pub fn status_text(status: ExitStatus) -> String {
    if let Some(code) = status.code() {
        return format!("exit status {code}");
    }
    let signal = status.signal().unwrap_or_default();
    let name = match signal {
        1 => "hangup",
        2 => "interrupt",
        3 => "quit",
        6 => "aborted",
        9 => "killed",
        11 => "segmentation fault",
        13 => "broken pipe",
        15 => "terminated",
        _ => return format!("signal: {signal}"),
    };
    format!("signal: {name}")
}

/// `encoding/json` string escaping on top of `serde_json` output: Go also
/// escapes `<`, `>`, `&` and U+2028/U+2029.
#[must_use]
pub fn escape_json(rendered: &str) -> String {
    rendered
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn join_cleans_like_filepath_join() {
        assert_eq!(
            join(&["/a", ".symdesk", "runs", "../x"]),
            PathBuf::from("/a/.symdesk/x")
        );
        assert_eq!(join(&["/a/", "", "b//c"]), PathBuf::from("/a/b/c"));
        assert_eq!(join(&[""]), PathBuf::new());
    }

    #[test]
    fn os_error_text_matches_go_wording() {
        let error = io::Error::from_raw_os_error(21);
        assert_eq!(os_error_text(&error), "is a directory");
        assert_eq!(
            path_error("read", Path::new("/v/blocked"), &error),
            "read /v/blocked: is a directory"
        );
    }

    #[test]
    fn escape_json_matches_encoding_json_html_escaping() {
        assert_eq!(escape_json("a<&>\u{2028}"), "a\\u003c\\u0026\\u003e\\u2028");
    }
}
