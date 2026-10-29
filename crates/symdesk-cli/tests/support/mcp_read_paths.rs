use std::path::Path;

// Replace the disposable root only. Never repair actual output separators.
pub fn normalize(text: &str, root: &Path) -> String {
    let root = root.to_string_lossy();
    let escaped = root.replace('\\', "\\\\");
    let mut text = text.to_owned();
    for spelling in [escaped.replace('\\', "\\\\"), escaped, root.into_owned()] {
        text = text.replace(&spelling, "$ROOT");
    }
    text
}

// Platform-adjusted expectation, not separator-insensitive comparison. Only
// absolute host paths following the substituted root change; vault-relative
// Markdown identifiers, escapes and all other bytes remain exact.
pub fn platform_expectation(text: &str, windows: bool) -> String {
    if !windows {
        return text.to_owned();
    }
    let mut out = String::new();
    let mut rest = text;
    while let Some(index) = rest.find("$ROOT") {
        out.push_str(&rest[..index + 5]);
        rest = &rest[index + 5..];
        let end = rest
            .bytes()
            .take_while(|c| c.is_ascii_alphanumeric() || b"._/\\-".contains(c))
            .count();
        let token = &rest[..end];
        let path = token.trim_end_matches('\\');
        let escaping = 2 * (token.len() - path.len() + 1);
        out.push_str(&path.replace('/', &"\\".repeat(escaping)));
        out.push_str(&token[path.len()..]);
        rest = &rest[end..];
    }
    out.push_str(rest);
    out
}

#[test]
fn native_expectation_preserves_relative_identifiers_and_rejects_wrong_output() {
    let root = Path::new(r"C:\Users\runner\fixture");
    let recorded = r#"{"text":"[{\"path\":\"$ROOT/vault/nested/note.md\"},{\"path\":\"notebooks/nested/note.md\"}]"}"#;
    let native = r#"{"text":"[{\"path\":\"$ROOT\\\\vault\\\\nested\\\\note.md\"},{\"path\":\"notebooks/nested/note.md\"}]"}"#;
    assert_eq!(platform_expectation(recorded, true), native);
    assert_eq!(platform_expectation(recorded, false), recorded);
    assert_eq!(
        normalize(
            r#"{"text":"{\"path\":\"C:\\\\Users\\\\runner\\\\fixture\\\\vault\\\\nested\\\\note.md\"}"}"#,
            root
        ),
        r#"{"text":"{\"path\":\"$ROOT\\\\vault\\\\nested\\\\note.md\"}"}"#
    );
    for wrong in [
        native.replacen(r"vault\\\\nested", "vault/nested", 1),
        native.replacen("notebooks/nested", r"notebooks\\\\nested", 1),
    ] {
        assert_ne!(wrong, native, "negative control must mutate bytes");
        assert_ne!(normalize(&wrong, root), native);
    }
    let literal = r#"bad\\literal\n\""#;
    assert_eq!(normalize(literal, root), literal);
}
