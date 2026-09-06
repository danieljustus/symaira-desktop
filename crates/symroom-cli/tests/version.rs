#![deny(unsafe_code)]

use std::process::{Command, Output};

fn run(args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_symroom"))
        .args(args)
        .output()
        .expect("run symroom")
}

#[test]
fn version_outputs_are_exact() {
    let output = run(&["version"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"symroom dev\n");
    assert!(output.stderr.is_empty());

    let output = run(&["version", "--json"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(
        output.stdout,
        b"{\"tool\":\"symroom\",\"version\":\"dev\",\"schema_version\":1}\n"
    );
    assert!(output.stderr.is_empty());
}

#[test]
fn version_preserves_extra_and_unknown_flag_behavior() {
    let output = run(&["version", "extra"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"symroom dev\n");

    let output = run(&["version", "--version"]);
    assert_eq!(output.status.code(), Some(2));
    assert!(output.stdout.is_empty());
    assert_eq!(output.stderr, b"flag provided but not defined: -version\nUsage of version:\n  -json\n    \tEmit version info in JSON format\n");
}
