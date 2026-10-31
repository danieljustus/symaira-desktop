//! Replays `symdesk ask` with a configured provider (the Hermes CLI backend)
//! against the process fixture recorded from the real Go CLI
//! (`make ask-hermes-fixtures-generate`): retrieval, event stream, prompt
//! construction and degradation, with a loopback shell double as the backend.
#![cfg(unix)]

use std::{
    collections::BTreeMap,
    fs,
    os::unix::fs::PermissionsExt,
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    hermes_script: String,
    files: BTreeMap<String, String>,
    cases: Vec<Case>,
}

#[derive(Deserialize, Clone)]
struct Case {
    name: String,
    args: Vec<String>,
    #[serde(default)]
    mode: String,
    #[serde(default)]
    no_binary: bool,
    #[serde(default)]
    no_index: bool,
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
        "../../../testdata/port/ai/ask-hermes-process.json"
    ))
    .expect("decode Go-owned ask fixture")
}

/// Go's timestamped slog lines are excluded from the compared stderr.
fn without_log_lines(text: &str) -> String {
    text.split_inclusive('\n')
        .filter(|line| !(line.starts_with("time=") && line.contains("level=INFO")))
        .collect()
}

fn replay(case: &Case, fixture: &Fixture) -> Result<(), String> {
    let root = fs::canonicalize(std::env::temp_dir())
        .unwrap()
        .join(format!(
            "symdesk-ask-hermes-{}-{}-{}",
            case.name,
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
    let (home, bin, vault) = (root.join("home"), root.join("bin"), root.join("vault"));
    for dir in [&home, &bin, &vault] {
        fs::create_dir_all(dir).unwrap();
    }
    for (name, content) in &fixture.files {
        let dest = vault.join(name);
        fs::create_dir_all(dest.parent().unwrap()).unwrap();
        fs::write(dest, content).unwrap();
    }
    if !case.no_binary {
        fs::write(bin.join("hermes"), &fixture.hermes_script).unwrap();
        fs::set_permissions(bin.join("hermes"), fs::Permissions::from_mode(0o700)).unwrap();
    }
    let log = root.join("hermes.log");
    let command = |extra: &BTreeMap<String, String>| {
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
            .envs(extra);
        command
    };
    if !case.no_index {
        let setup = command(&BTreeMap::new())
            .arg("--vault")
            .arg(&vault)
            .arg("index")
            .output()
            .unwrap();
        assert!(
            setup.status.success(),
            "{}: index: {}",
            case.name,
            String::from_utf8_lossy(&setup.stderr)
        );
    }
    let _ = fs::remove_file(&log);
    let output = command(&case.env)
        .arg("--vault")
        .arg(&vault)
        .args(&case.args)
        .output()
        .unwrap();
    let root_text = root.to_string_lossy().into_owned();
    let stdout = String::from_utf8_lossy(&output.stdout).replace(&root_text, "<ROOT>");
    let stderr =
        without_log_lines(&String::from_utf8_lossy(&output.stderr)).replace(&root_text, "<ROOT>");
    let transcript = fs::read_to_string(&log)
        .unwrap_or_default()
        .replace(&root_text, "<ROOT>");
    fs::remove_dir_all(&root).unwrap();
    let label = &case.name;
    if output.status.code() != Some(case.exit_code) {
        return Err(format!("{label}: exit {:?}", output.status.code()));
    }
    if stdout != case.stdout {
        return Err(format!(
            "{label}: stdout\nrust: {stdout:?}\ngo:   {:?}",
            case.stdout
        ));
    }
    if stderr != case.stderr {
        return Err(format!("{label}: stderr {stderr:?} != {:?}", case.stderr));
    }
    if transcript != case.log {
        return Err(format!(
            "{label}: hermes transcript\nrust: {transcript:?}\ngo:   {:?}",
            case.log
        ));
    }
    Ok(())
}

#[test]
fn ask_with_hermes_matches_go_process_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 12);
    let failures: Vec<String> = fixture
        .cases
        .iter()
        .filter_map(|case| replay(case, &fixture).err())
        .collect();
    assert!(failures.is_empty(), "{}", failures.join("\n---\n"));
}

#[test]
fn replay_rejects_a_tampered_expectation() {
    let fixture = fixture();
    let mut case = fixture
        .cases
        .iter()
        .find(|case| case.name == "backend_fails")
        .cloned()
        .unwrap();
    case.log.push_str("tamper\n");
    assert!(
        replay(&case, &fixture)
            .unwrap_err()
            .contains("hermes transcript")
    );
}
