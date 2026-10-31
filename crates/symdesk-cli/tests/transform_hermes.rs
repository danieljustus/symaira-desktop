//! Replays `symdesk transform` with the Hermes CLI backend against the process
//! fixture recorded from the real Go CLI (`make transform-hermes-fixtures-generate`).
#![cfg(unix)]

use std::{
    collections::BTreeMap,
    fs,
    io::Write,
    os::unix::fs::PermissionsExt,
    process::{Command, Stdio},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

/// Two replays in one test binary can share a clock tick; the counter keeps
/// their scratch roots (and Hermes transcripts) distinct.
static ROOT_SEQUENCE: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    hermes_script: String,
    cases: Vec<Case>,
}

#[derive(Deserialize, Clone)]
struct Case {
    name: String,
    args: Vec<String>,
    #[serde(default)]
    stdin: String,
    #[serde(default)]
    mode: String,
    #[serde(default)]
    no_binary: bool,
    #[serde(default)]
    env: BTreeMap<String, String>,
    exit_code: i32,
    stdout: String,
    stderr: String,
    #[serde(default)]
    log: String,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/ai/transform-hermes-process.json"
    ))
    .expect("decode Go-owned transform fixture")
}

fn replay(case: &Case, script: &str) -> Result<(), String> {
    let root = fs::canonicalize(std::env::temp_dir())
        .unwrap()
        .join(format!(
            "symdesk-transform-hermes-{}-{}-{}-{}",
            case.name,
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos(),
            ROOT_SEQUENCE.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
        ));
    let (home, bin) = (root.join("home"), root.join("bin"));
    fs::create_dir_all(&home).unwrap();
    fs::create_dir_all(&bin).unwrap();
    if !case.no_binary {
        fs::write(bin.join("hermes"), script).unwrap();
        fs::set_permissions(bin.join("hermes"), fs::Permissions::from_mode(0o700)).unwrap();
    }
    let log = root.join("hermes.log");
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command
        .current_dir(&root)
        .env_clear()
        .env("PATH", format!("{}:/usr/bin:/bin", bin.display()))
        .env("HOME", &home)
        .env("TZ", "UTC")
        .env("XDG_CONFIG_HOME", home.join("config"))
        .env("XDG_CACHE_HOME", home.join("cache"))
        .env("XDG_DATA_HOME", home.join("data"))
        .env("FAKE_HERMES_LOG", &log)
        .env("FAKE_HERMES_MODE", &case.mode)
        .envs(&case.env)
        .args(&case.args)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let mut child = command.spawn().expect("spawn Rust symdesk");
    child
        .stdin
        .take()
        .unwrap()
        .write_all(case.stdin.as_bytes())
        .unwrap();
    let output = child.wait_with_output().unwrap();
    let root_text = root.to_string_lossy();
    let stdout = String::from_utf8_lossy(&output.stdout).replace(root_text.as_ref(), "<ROOT>");
    let stderr = String::from_utf8_lossy(&output.stderr).replace(root_text.as_ref(), "<ROOT>");
    let transcript = fs::read_to_string(&log).unwrap_or_default();
    fs::remove_dir_all(&root).unwrap();
    let label = &case.name;
    if output.status.code() != Some(case.exit_code) {
        return Err(format!("{label}: exit {:?}", output.status.code()));
    }
    if stdout != case.stdout {
        return Err(format!("{label}: stdout {stdout:?} != {:?}", case.stdout));
    }
    if stderr != case.stderr {
        return Err(format!("{label}: stderr {stderr:?} != {:?}", case.stderr));
    }
    if transcript != case.log {
        return Err(format!(
            "{label}: hermes transcript {transcript:?} != {:?}",
            case.log
        ));
    }
    Ok(())
}

#[test]
fn transform_hermes_matches_go_process_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 13);
    for case in &fixture.cases {
        replay(case, &fixture.hermes_script).unwrap_or_else(|error| panic!("{error}"));
    }
}

#[test]
fn replay_rejects_a_tampered_expectation() {
    let fixture = fixture();
    let mut case = fixture
        .cases
        .iter()
        .find(|case| case.name == "text_ok")
        .cloned()
        .unwrap();
    case.stdout = "{something else}\n".to_owned();
    assert!(
        replay(&case, &fixture.hermes_script)
            .unwrap_err()
            .contains("stdout")
    );
}
