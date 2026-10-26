//! Every frozen Cobra node is exercised through the actual Rust executable.
use serde::Deserialize;
use std::{
    fs,
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    commands: usize,
    cases: Vec<Case>,
}
#[derive(Deserialize)]
struct Case {
    args: Vec<String>,
    exit_code: i32,
    stdout: String,
    stderr: String,
}

#[test]
fn every_command_help_and_completion_match_go_process() {
    let fixture_bytes = include_bytes!("../../../testdata/port/cli/help-process.json");
    assert_eq!(
        fixture_bytes.as_slice(),
        include_bytes!("../../../testdata/port/cli/cobra-help.json").as_slice(),
        "embedded release asset drift"
    );
    let fixture: Fixture = serde_json::from_slice(fixture_bytes).expect("Go process fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.commands, 207);
    assert_eq!(fixture.cases.len(), 424);
    let root = std::env::temp_dir().join(format!(
        "symdesk-help-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    fs::create_dir_all(&root).unwrap();
    let mut executed = 0;
    for case in fixture.cases {
        let output = Command::new(env!("CARGO_BIN_EXE_symdesk"))
            .current_dir(&root)
            .env_clear()
            .env("HOME", &root)
            .env("USERPROFILE", &root)
            .env("XDG_CONFIG_HOME", root.join("config"))
            .env("XDG_DATA_HOME", root.join("data"))
            .env("XDG_CACHE_HOME", root.join("cache"))
            .env("TZ", "UTC")
            .env("LANG", "C")
            .env("LC_ALL", "C")
            .env("PATH", &root)
            .args(&case.args)
            .output()
            .unwrap();
        assert_eq!(
            output.stdout,
            case.stdout.as_bytes(),
            "{:?} stdout",
            case.args
        );
        assert_eq!(
            output.stderr,
            case.stderr.as_bytes(),
            "{:?} stderr",
            case.args
        );
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "{:?} exit",
            case.args
        );
        executed += 1;
    }
    assert_eq!(executed, 424);
    fs::remove_dir_all(root).unwrap();
}
