//! Match observeDuplicatesCase: resolve the case root before deriving children.
//! Go EvalSymlinks expands macOS /var aliases. SQLite storage keys retain this
//! spelling.
use std::path::{Path, PathBuf};

pub fn canonical_root(root: &Path) -> PathBuf {
    std::fs::canonicalize(root).expect("canonical process case root")
}

/// Materialize only language-neutral root-marked paths, preserving literal argv.
/// Go conflict resolve retains an absolute argument as-is for exact DB deletion;
/// injecting a slash suffix after the root creates a different key.
#[allow(dead_code)] // MCP replay imports canonical_root but has no root-marked argv.
pub fn expand_argument(arg: &str, root: &str) -> String {
    if arg.contains("$ROOT") {
        arg.replace("$ROOT", root)
            .replace('/', std::path::MAIN_SEPARATOR_STR)
    } else {
        arg.to_owned()
    }
}
