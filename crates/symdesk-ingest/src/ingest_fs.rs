//! Lexical `path`/`path/filepath` helpers (Unix flavour) the mail poller and the
//! directory watcher share: `Clean`, `Join`, `Base`, `Rel`-based containment,
//! `cleanOptionalDir`.

/// `path.Clean` on Unix paths.
pub fn go_clean(path: &str) -> String {
    if path.is_empty() {
        return ".".into();
    }
    let b = path.as_bytes();
    let rooted = b[0] == b'/';
    let n = b.len();
    let mut out: Vec<u8> = Vec::with_capacity(n);
    let mut r = 0;
    let mut dotdot = 0;
    if rooted {
        out.push(b'/');
        r = 1;
        dotdot = 1;
    }
    while r < n {
        if b[r] == b'/' {
            r += 1;
        } else if b[r] == b'.' && (r + 1 == n || b[r + 1] == b'/') {
            r += 1;
        } else if b[r] == b'.' && r + 1 < n && b[r + 1] == b'.' && (r + 2 == n || b[r + 2] == b'/')
        {
            r += 2;
            if out.len() > dotdot {
                let mut w = out.len() - 1;
                while w > dotdot && out[w] != b'/' {
                    w -= 1;
                }
                out.truncate(w);
            } else if !rooted {
                if !out.is_empty() {
                    out.push(b'/');
                }
                out.extend_from_slice(b"..");
                dotdot = out.len();
            }
        } else {
            if (rooted && out.len() != 1) || (!rooted && !out.is_empty()) {
                out.push(b'/');
            }
            while r < n && b[r] != b'/' {
                out.push(b[r]);
                r += 1;
            }
        }
    }
    if out.is_empty() {
        return ".".into();
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// `filepath.Join(dir, elem)` for two non-empty elements.
pub fn go_join(dir: &str, elem: &str) -> String {
    go_clean(&format!("{dir}/{elem}"))
}

/// `filepath.Base`.
pub fn go_base(path: &str) -> String {
    if path.is_empty() {
        return ".".into();
    }
    let trimmed = path.trim_end_matches('/');
    if trimmed.is_empty() {
        return "/".into();
    }
    match trimmed.rfind('/') {
        Some(i) => trimmed[i + 1..].to_owned(),
        None => trimmed.to_owned(),
    }
}

/// `filepath.Dir`.
pub fn go_dir(path: &str) -> String {
    match path.rfind('/') {
        Some(i) => go_clean(&path[..=i]),
        None => ".".into(),
    }
}

/// `filepath.Ext`.
pub fn go_ext(path: &str) -> &str {
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

/// `isPathWithin`: `filepath.Rel(Clean(dir), Clean(path))` is `.` or does not
/// start with `..` and is not absolute.
pub fn is_path_within(path: &str, dir: &str) -> bool {
    let base = go_clean(dir);
    let targ = go_clean(path);
    if base.starts_with('/') != targ.starts_with('/') {
        return false;
    }
    if base == targ {
        return true;
    }
    let prefix = if base == "/" {
        "/".to_owned()
    } else {
        format!("{base}/")
    };
    match targ.strip_prefix(&prefix) {
        Some(rest) => !rest.starts_with(".."),
        None => false,
    }
}

/// `cleanOptionalDir`.
pub fn clean_optional_dir(path: &str) -> Result<String, String> {
    if path.is_empty() {
        return Ok(String::new());
    }
    if path.starts_with('/') {
        return Ok(go_clean(path));
    }
    let pwd = std::env::current_dir().map_err(|e| e.to_string())?;
    Ok(go_clean(&format!("{}/{path}", pwd.to_string_lossy())))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn clean_matches_go() {
        for (input, want) in [
            ("", "."),
            ("a//b", "a/b"),
            ("/a/./b/../c/", "/a/c"),
            ("../x", "../x"),
            ("/../x", "/x"),
            ("a/../..", ".."),
            ("/proc/id-/", "/proc/id-"),
        ] {
            assert_eq!(go_clean(input), want, "{input}");
        }
    }

    #[test]
    fn base_and_within() {
        assert_eq!(go_base("/a/b.pdf"), "b.pdf");
        assert_eq!(go_base("a/"), "a");
        assert_eq!(go_base("///"), "/");
        assert_eq!(go_base(""), ".");
        assert!(is_path_within("/p/a", "/p"));
        assert!(!is_path_within("/p/..a", "/p"));
        assert!(!is_path_within("/pa/b", "/p"));
        assert!(is_path_within("/p", "/p"));
    }
}
