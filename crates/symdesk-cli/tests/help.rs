//! Every frozen Cobra node is exercised through the actual Rust executable.
#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

// The Go recording plus the additions declared after the oracle
// (docs/rust-port/cli-additions.md): the root document is the only one touched.
#[path = "../src/cli_additions.rs"]
#[allow(dead_code)]
mod cli_additions;

use serde::Deserialize;
use std::{
    collections::BTreeMap,
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
struct Documents {
    help: BTreeMap<String, String>,
    root_usage: String,
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
    // Expected text: the Go bytes, with the declared additions applied to the
    // two documents that list the root commands and to nothing else.
    let frozen: Documents = serde_json::from_slice(fixture_bytes).expect("Go documents");
    let (mut help, mut usage) = (frozen.help.clone(), frozen.root_usage.clone());
    cli_additions::apply(&mut help, &mut usage, cli_additions::declared()).expect("additions");
    let mut rewritten = 0;
    let mut expect = |recorded: &str| -> String {
        if recorded == frozen.help[""] {
            rewritten += 1;
            help[""].clone()
        } else if let Some(prefix) = recorded.strip_suffix(frozen.root_usage.as_str()) {
            rewritten += 1;
            format!("{prefix}{usage}")
        } else {
            recorded.to_owned()
        }
    };
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
            .isolated_env()
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
            expect(&case.stdout).as_bytes(),
            "{:?} stdout",
            case.args
        );
        assert_eq!(
            output.stderr,
            expect(&case.stderr).as_bytes(),
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
    // `--help`, `help`, no arguments (stdout) and `help not-a-command` (stderr):
    // every other case is still compared with the Go bytes unchanged.
    assert_eq!(rewritten, 4, "cases whose expectation carries additions");
    fs::remove_dir_all(root).unwrap();
}
