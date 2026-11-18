//! `symroom` is folded into `symdesk`: `symdesk room <args>` and a `symroom`
//! name (also through a symlink) behave like the former `symroom` binary.
#![deny(unsafe_code)]
#![cfg(unix)]

use std::{
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
};

const SYMDESK: &str = env!("CARGO_BIN_EXE_symdesk");
const VERSION_JSON: &[u8] = b"{\"tool\":\"symroom\",\"version\":\"dev\",\"schema_version\":1}\n";

fn run(program: &Path, args: &[&str]) -> Output {
    Command::new(program)
        .env("SYMDESK_PRE_RUST_BACKUP", "off")
        .args(args)
        .output()
        .expect("run binary")
}

struct SymroomLink(PathBuf);

impl SymroomLink {
    fn new(label: &str) -> Self {
        let dir =
            std::env::temp_dir().join(format!("symdesk-room-fold-{label}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).expect("create link dir");
        let link = dir.join("symroom");
        std::os::unix::fs::symlink(SYMDESK, &link).expect("symlink symroom -> symdesk");
        Self(link)
    }
}

impl Drop for SymroomLink {
    fn drop(&mut self) {
        if let Some(dir) = self.0.parent() {
            let _ = fs::remove_dir_all(dir);
        }
    }
}

#[test]
fn room_subcommand_reports_the_symroom_version() {
    let output = run(Path::new(SYMDESK), &["room", "version", "--json"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, VERSION_JSON);
    assert!(output.stderr.is_empty());

    let output = run(Path::new(SYMDESK), &["room", "version"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"symroom dev\n");
}

#[test]
fn symroom_symlink_behaves_like_symroom() {
    let link = SymroomLink::new("version");
    let output = run(&link.0, &["version", "--json"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, VERSION_JSON);
    assert!(output.stderr.is_empty());

    let output = run(&link.0, &[]);
    assert_eq!(output.status.code(), Some(2));
    assert!(output.stdout.is_empty());
    assert!(
        output
            .stderr
            .starts_with(b"symroom - room management and coordination tool")
    );
}

#[test]
fn room_arguments_bypass_symdesk_flag_parsing() {
    // symdesk would reject `--version` after `version`; symroom's Go flag
    // grammar answers with its own error and exit code 2.
    let output = run(Path::new(SYMDESK), &["room", "version", "--version"]);
    assert_eq!(output.status.code(), Some(2));
    assert!(output.stdout.is_empty());
    assert_eq!(
        output.stderr,
        b"flag provided but not defined: -version\nUsage of version:\n  -json\n    \tEmit version info in JSON format\n"
    );

    let output = run(Path::new(SYMDESK), &["room", "--help"]);
    assert_eq!(output.status.code(), Some(0));
    assert!(
        output
            .stdout
            .starts_with(b"symroom - room management and coordination tool")
    );

    let output = run(Path::new(SYMDESK), &["room"]);
    assert_eq!(output.status.code(), Some(2));
    assert!(
        output
            .stderr
            .starts_with(b"symroom - room management and coordination tool")
    );
}

#[test]
fn symdesk_without_room_is_unchanged() {
    let output = run(Path::new(SYMDESK), &["version", "--json"]);
    assert_eq!(output.status.code(), Some(0));
    assert!(String::from_utf8_lossy(&output.stdout).contains("\"tool\":\"symdesk\""));
}
