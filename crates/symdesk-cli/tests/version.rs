#![deny(unsafe_code)]

use std::process::{Command, Output};

fn run(args: &[&str]) -> Output {
    Command::new(env!("CARGO_BIN_EXE_symdesk"))
        .args(args)
        .output()
        .expect("run symdesk")
}

#[test]
fn version_text_and_root_flag_are_exact() {
    let output = run(&["version"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"symdesk devel\n");
    assert!(output.stderr.is_empty());

    let output = run(&["--version"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"symdesk version devel\n");
    assert!(output.stderr.is_empty());
}

#[test]
fn version_json_aliases_are_exact() {
    for args in [
        &["version", "--json"][..],
        &["--json", "version"][..],
        &["version", "--output", "json"][..],
    ] {
        let output = run(args);
        assert_eq!(output.status.code(), Some(0));
        assert_eq!(
            output.stdout,
            b"{\"tool\":\"symdesk\",\"version\":\"devel\",\"schema_version\":1}\n"
        );
        assert!(output.stderr.is_empty());
    }
}

#[test]
fn version_preserves_extra_and_output_error_behavior() {
    let output = run(&["version", "extra"]);
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, b"symdesk devel\n");

    let output = run(&["version", "--output", "badformat"]);
    assert_eq!(output.status.code(), Some(1));
    assert!(output.stdout.is_empty());
    assert_eq!(
        output.stderr,
        b"invalid --output value \"badformat\" (want text|json|yaml)\n"
    );

    let output = run(&["version", "--version"]);
    assert_eq!(output.status.code(), Some(1));
    assert!(output.stdout.is_empty());
    assert_eq!(output.stderr, b"unknown flag: --version\n");
}
