//! Go normalizeRetryText / slashRootPaths: only temporary-root path tokens.
#[cfg(not(windows))]
pub fn slash_root_paths(text: &str) -> String {
    text.to_owned()
}

/// Rewrites the separators of every `$ROOT` path token to `/`; a doubled
/// backslash inside a token is one separator (Go `slashRootPaths`).
#[cfg(windows)]
pub fn slash_root_paths(text: &str) -> String {
    let mut out = String::new();
    let mut rest = text;
    while let Some(index) = rest.find("$ROOT") {
        out.push_str(&rest[..index + "$ROOT".len()]);
        rest = &rest[index + "$ROOT".len()..];
        let bytes = rest.as_bytes();
        let mut consumed = 0;
        while consumed < bytes.len() {
            let c = bytes[consumed];
            if c == b'\\' || c == b'/' {
                out.push('/');
                consumed += 1;
                if consumed < bytes.len() && bytes[consumed] == b'\\' {
                    consumed += 1;
                }
            } else if c == b'.' || c == b'_' || c == b'-' || c.is_ascii_alphanumeric() {
                out.push(char::from(c));
                consumed += 1;
            } else {
                break;
            }
        }
        rest = &rest[consumed..];
    }
    out.push_str(rest);
    out
}
