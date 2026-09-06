#![deny(unsafe_code)]
#![cfg(not(windows))]

use std::{
    fs,
    path::{Path, PathBuf},
};

use noyalib as _;
use serde::Deserialize;
use symdesk_vault::{SecurePathError, WalkEntryType, secure_path, walk_all, walk_markdown};
use thiserror as _;
use unicode_general_category as _;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    tree: Vec<TreeFile>,
    walk_all: Vec<ExpectedWalk>,
    walk_markdown: Vec<String>,
    secure_paths: Vec<SecureCase>,
}

#[derive(Deserialize)]
struct TreeFile {
    path: String,
    #[serde(default)]
    content: String,
    symlink_to: Option<String>,
    #[serde(default)]
    directory: bool,
}

#[derive(Deserialize, Debug, PartialEq)]
struct ExpectedWalk {
    path: String,
    #[serde(rename = "type")]
    entry_type: String,
    #[serde(default)]
    symlink: String,
}

#[derive(Deserialize)]
struct SecureCase {
    #[serde(rename = "id")]
    _id: String,
    input: String,
    #[serde(default)]
    result: String,
    #[serde(default)]
    error_class: String,
}

#[test]
fn walking_and_secure_paths_match_go_fixture() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../testdata/port/vault/filesystem.json"))
            .expect("decode vault filesystem fixture");
    assert_eq!(fixture.schema_version, 1);
    let base = std::env::temp_dir().join(format!("symdesk-rust-vault-fs-{}", std::process::id()));
    let root = base.join("root");
    let outside = base.join("outside");
    let _ = fs::remove_dir_all(&base);
    fs::create_dir_all(&root).expect("create root");
    fs::create_dir_all(&outside).expect("create outside");
    fs::write(outside.join("outside.md"), b"outside").expect("write outside");

    for entry in &fixture.tree {
        let path = root.join(&entry.path);
        if let Some(target) = &entry.symlink_to {
            let target = expand(target, &root, &outside);
            if entry.directory {
                fs::create_dir_all(&target).expect("create symlink directory target");
            }
            std::os::unix::fs::symlink(target, path).expect("create symlink");
        } else {
            fs::create_dir_all(path.parent().expect("tree parent")).expect("create parent");
            fs::write(path, entry.content.as_bytes()).expect("write tree file");
        }
    }

    let actual: Vec<_> = walk_all(&root)
        .expect("walk all")
        .into_iter()
        .map(|entry| ExpectedWalk {
            path: slash(&entry.path),
            entry_type: match entry.entry_type {
                WalkEntryType::File => "file",
                WalkEntryType::Symlink => "symlink",
                WalkEntryType::Other => "other",
            }
            .to_owned(),
            symlink: entry
                .symlink_target
                .map_or_else(String::new, |target| normalize(&target, &root, &outside)),
        })
        .collect();
    assert_eq!(actual, fixture.walk_all);

    let markdown: Vec<_> = walk_markdown(&root)
        .expect("walk markdown")
        .into_iter()
        .map(|path| slash(&path))
        .collect();
    assert_eq!(markdown, fixture.walk_markdown);

    for case in fixture.secure_paths {
        match secure_path(&root, &case.input) {
            Ok(path) => {
                assert!(case.error_class.is_empty());
                assert_eq!(normalize(&path, &root, &outside), case.result);
            }
            Err(error) => assert_eq!(secure_error_class(&error), case.error_class),
        }
    }
    fs::remove_dir_all(&base).expect("remove fixture tree");
}

fn expand(value: &str, root: &Path, outside: &Path) -> PathBuf {
    if let Some(rest) = value.strip_prefix("<OUTSIDE>") {
        outside.join(rest.trim_start_matches('/'))
    } else if let Some(rest) = value.strip_prefix("<ROOT>") {
        root.join(rest.trim_start_matches('/'))
    } else {
        root.join(value)
    }
}

fn normalize(value: &Path, root: &Path, outside: &Path) -> String {
    let value = value.canonicalize().unwrap_or_else(|_| value.to_path_buf());
    for (base, marker) in [(root, "<ROOT>"), (outside, "<OUTSIDE>")] {
        let canonical = base.canonicalize().unwrap_or_else(|_| base.to_path_buf());
        if let Ok(rest) = value.strip_prefix(&canonical) {
            let suffix = slash(rest);
            return if suffix.is_empty() {
                marker.to_owned()
            } else {
                format!("{marker}/{suffix}")
            };
        }
    }
    slash(&value)
}

fn slash(path: &Path) -> String {
    path.to_string_lossy().replace('\\', "/")
}

fn secure_error_class(error: &SecurePathError) -> &'static str {
    match error {
        SecurePathError::Traversal(_) => "traversal",
        SecurePathError::SymlinkEscape(_) => "symlink_escape",
        _ => "other",
    }
}
