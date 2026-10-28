//! Match observeDuplicatesCase: resolve the case root before deriving children.
//! Go EvalSymlinks expands macOS /var and Windows 8.3 aliases without emitting
//! Rust's Windows verbatim prefix. SQLite storage keys retain this spelling.
use std::path::{Path, PathBuf};

pub fn canonical_root(root: &Path) -> PathBuf {
    let root = std::fs::canonicalize(root).expect("canonical process case root");
    #[cfg(windows)]
    {
        let text = root.to_str().expect("UTF-8 process case root");
        if let Some(rest) = text.strip_prefix(r"\\?\UNC\") {
            return PathBuf::from(format!(r"\\{rest}"));
        }
        if let Some(rest) = text.strip_prefix(r"\\?\") {
            return PathBuf::from(rest);
        }
    }
    root
}

/// Materialize only language-neutral root-marked paths, preserving literal argv.
/// Go conflict resolve retains an absolute argument as-is for exact DB deletion;
/// injecting a slash suffix after a native Windows root creates a different key.
pub fn expand_argument(arg: &str, root: &str) -> String {
    if arg.contains("$ROOT") {
        arg.replace("$ROOT", root)
            .replace('/', std::path::MAIN_SEPARATOR_STR)
    } else {
        arg.to_owned()
    }
}

#[test]
fn root_markers_use_native_separators_without_cleaning_literal_arguments() {
    let root = if cfg!(windows) {
        r"C:\temp\case"
    } else {
        "/private/case"
    };
    let expected =
        format!("{root}/vault/note conflicted copy.md").replace('/', std::path::MAIN_SEPARATOR_STR);
    assert_eq!(
        expand_argument("$ROOT/vault/note conflicted copy.md", root),
        expected
    );
    assert_eq!(expand_argument("../literal/path", root), "../literal/path");
    assert_eq!(
        expand_argument("https://example.invalid/path", root),
        "https://example.invalid/path"
    );
}
