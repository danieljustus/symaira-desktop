use std::path::Path;

pub fn normalize(text: &str, root: &Path) -> String {
    let root = root.to_string_lossy();
    let escaped = root.replace('\\', "\\\\");
    let mut text = text.to_owned();
    for spelling in [escaped.replace('\\', "\\\\"), escaped, root.into_owned()] {
        text = text.replace(&spelling, "$ROOT");
    }
    let mut out = String::new();
    let mut rest = text.as_str();
    while let Some(index) = rest.find("$ROOT") {
        out.push_str(&rest[..index + 5]);
        rest = &rest[index + 5..];
        let end = rest
            .bytes()
            .take_while(|c| c.is_ascii_alphanumeric() || b"._/\\-".contains(c))
            .count();
        let token = &rest[..end];
        let path = token.trim_end_matches('\\');
        let mut previous_separator = false;
        for c in path.chars() {
            if c == '\\' {
                if !previous_separator {
                    out.push('/');
                }
                previous_separator = true;
            } else {
                out.push(c);
                previous_separator = false;
            }
        }
        out.push_str(&token[path.len()..]);
        rest = &rest[end..];
    }
    out.push_str(rest);
    out.replace(r"notebooks\\", "notebooks/")
        .replace(r"notebooks\", "notebooks/")
}

#[test]
fn nested_windows_paths_preserve_json_escapes() {
    let root = Path::new(r"C:\Users\runner\fixture");
    assert_eq!(
        normalize(
            r#"{"text":"{\"path\":\"C:\\\\Users\\\\runner\\\\fixture\\\\vault\\\\note.md\"}"}"#,
            root
        ),
        r#"{"text":"{\"path\":\"$ROOT/vault/note.md\"}"}"#
    );
    assert_eq!(
        normalize(r#"bad\\literal\n\""#, root),
        r#"bad\\literal\n\""#
    );
}
