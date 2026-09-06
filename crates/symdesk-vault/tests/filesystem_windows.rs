#![deny(unsafe_code)]
#![cfg(windows)]

use std::fs;

use noyalib as _;
use serde as _;
use serde_json as _;
use symdesk_vault::{SecurePathError, secure_path, walk_all, walk_markdown};
use thiserror as _;
use unicode_general_category as _;

#[test]
fn windows_walk_and_lexical_confinement() {
    let root = std::env::temp_dir().join(format!("symdesk-vault-windows-{}", std::process::id()));
    let _ = fs::remove_dir_all(&root);
    for (path, content) in [
        ("a.md", "a"),
        ("Upper.MD", "upper"),
        ("folder/b.md", "b"),
        ("Vendor/kept.md", "kept"),
        ("vendor/skipped.md", "skip"),
        (".hidden.md", "skip"),
        (".obsidian/skipped.md", "skip"),
    ] {
        let target = root.join(path);
        fs::create_dir_all(target.parent().expect("fixture parent")).expect("create parent");
        fs::write(target, content).expect("write fixture");
    }
    let all: Vec<_> = walk_all(&root)
        .expect("walk all")
        .into_iter()
        .map(|entry| entry.path.to_string_lossy().replace('\\', "/"))
        .collect();
    assert_eq!(all, ["Upper.MD", "Vendor/kept.md", "a.md", "folder/b.md"]);
    let markdown: Vec<_> = walk_markdown(&root)
        .expect("walk markdown")
        .into_iter()
        .map(|path| path.to_string_lossy().replace('\\', "/"))
        .collect();
    assert_eq!(markdown, ["a.md", "folder/b.md"]);
    assert!(secure_path(&root, "folder/new.md").is_ok());
    assert!(matches!(
        secure_path(&root, "../escape.md"),
        Err(SecurePathError::Traversal(_))
    ));
    fs::remove_dir_all(root).expect("remove fixture");
}
