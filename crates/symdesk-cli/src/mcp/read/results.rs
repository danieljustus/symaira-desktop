//! Externalized tool-result reads; storage is outside the authoritative vault.
use std::path::{Component, Path, PathBuf};

pub(super) fn root(vault: &Path) -> Result<PathBuf, String> {
    let canonical = std::fs::canonicalize(vault).unwrap_or_else(|_| vault.to_owned());
    // Go EvalSymlinks returns ordinary drive/UNC paths. Rust canonicalize
    // returns a verbatim path on Windows; that prefix is not part of Go's key.
    let name = go_canonical_name(&canonical.to_string_lossy(), cfg!(windows));
    let hash = symdesk_vault::sha256_hex(name.as_bytes());
    let data = std::env::var_os("XDG_DATA_HOME")
        .filter(|v| !v.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            std::env::var_os("HOME")
                .filter(|v| !v.is_empty())
                .map(PathBuf::from)
                .unwrap_or_else(std::env::temp_dir)
                .join(".local/share")
        });
    Ok(data.join("symdesk/agent-results").join(&hash[..16]))
}

fn go_canonical_name(path: &str, windows: bool) -> String {
    if windows {
        if let Some(rest) = path.strip_prefix(r"\\?\UNC\") {
            return format!(r"\\{rest}");
        }
        if let Some(rest) = path.strip_prefix(r"\\?\") {
            return rest.to_owned();
        }
    }
    path.to_owned()
}

pub(super) fn read(vault: &Path, handle: &str) -> Result<serde_json::Value, String> {
    let invalid = || {
        format!(
            "invalid externalized-result handle: {}",
            symdesk_vault::go_quote(handle)
        )
    };
    let mut clean = PathBuf::new();
    for component in Path::new(handle).components() {
        match component {
            Component::Normal(part) => clean.push(part),
            Component::CurDir => {}
            Component::ParentDir => {
                if !clean.pop() {
                    return Err(invalid());
                }
            }
            _ => return Err(invalid()),
        }
    }
    if clean.as_os_str().is_empty() {
        return Err(invalid());
    }
    let path = root(vault)?.join(clean);
    let bytes = symdesk_vault::go_read(&path).map_err(|(op, error)| {
        format!(
            "externalized result {} not found: {}",
            symdesk_vault::go_quote(handle),
            symdesk_vault::go_path_error(op, &path, &error)
        )
    })?;
    Ok(serde_json::Value::String(
        String::from_utf8_lossy(&bytes).into_owned(),
    ))
}

#[cfg(test)]
mod tests {
    use super::go_canonical_name;

    #[test]
    fn result_keys_use_go_drive_and_unc_spelling() {
        for (native, go) in [
            (r"\\?\C:\Users\runner\vault", r"C:\Users\runner\vault"),
            (r"\\?\UNC\server\share\vault", r"\\server\share\vault"),
            (r"C:\Users\runner\vault", r"C:\Users\runner\vault"),
            ("/fixture/vault", "/fixture/vault"),
        ] {
            let name = go_canonical_name(native, true);
            assert_eq!(name, go);
            assert_eq!(
                symdesk_vault::sha256_hex(name.as_bytes()),
                symdesk_vault::sha256_hex(go.as_bytes())
            );
            assert_eq!(go_canonical_name(native, false), native);
        }
    }
}
