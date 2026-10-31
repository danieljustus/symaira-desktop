//! Replays MCP `desk_ask` with a configured provider (Hermes backend double,
//! loopback Ollama `/api/generate`, loopback Anthropic Messages) and its
//! notebook scope against the process fixture recorded from the real Go CLI
//! (`make ask-provider-mcp-fixtures-generate`). The offline path is pinned by
//! `mcp_ask_offline`; retrieval scores are masked (host-dependent, pinned by
//! the retrieval differentials).
#![cfg(unix)]

use std::{collections::BTreeMap, io::Write, process::Stdio};

use serde::Deserialize;

#[path = "support/provider.rs"]
mod provider;
use provider::{FakeProvider, Turn};
#[path = "support/ask_replay.rs"]
mod ask_replay;
use ask_replay::{
    Request, Scratch, compare_requests, mcp_call_line, normalize, provider_env, select_files,
    without_log_lines,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    hermes_script: String,
    files: BTreeMap<String, String>,
    file_sets: BTreeMap<String, Vec<String>>,
    file_time: String,
    cases: Vec<Case>,
}

#[derive(Deserialize, Clone)]
struct Case {
    name: String,
    file_set: String,
    provider: String,
    #[serde(default)]
    mode: String,
    #[serde(default)]
    env: BTreeMap<String, String>,
    #[serde(default)]
    turns: Vec<Turn>,
    arguments_json: String,
    exit_code: i32,
    stdout: String,
    stderr: String,
    #[serde(default)]
    requests: Vec<Request>,
    #[serde(default)]
    log: String,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/ai/ask-provider-mcp.json"
    ))
    .expect("decode Go-owned MCP ask provider fixture")
}

fn run_replay(case: &Case, fixture: &Fixture) -> Result<(), String> {
    let label = &case.name;
    let scratch = Scratch::new(
        label,
        &select_files(&fixture.files, &fixture.file_sets, &case.file_set),
        &fixture.file_time,
        (case.provider == "hermes").then_some(fixture.hermes_script.as_str()),
    );
    let provider = matches!(case.provider.as_str(), "ollama" | "anthropic")
        .then(|| FakeProvider::start(case.turns.clone()));
    let url = provider.as_ref().map(|p| p.url.clone());
    scratch.index(&case.mode);

    let env = provider_env(&case.provider, url.as_deref(), &case.env);
    let mut child = scratch
        .command(&case.mode, &env)
        .arg("--vault")
        .arg(&scratch.vault)
        .arg("mcp")
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    child
        .stdin
        .take()
        .unwrap()
        .write_all(mcp_call_line(&case.arguments_json).as_bytes())
        .unwrap();
    let output = child.wait_with_output().unwrap();

    let root_text = scratch.root_text();
    let url_text = url.clone().unwrap_or_default();
    let stdout = normalize(
        &String::from_utf8_lossy(&output.stdout),
        &root_text,
        &url_text,
    );
    let stderr = normalize(
        &without_log_lines(&String::from_utf8_lossy(&output.stderr)),
        &root_text,
        &url_text,
    );
    if output.status.code() != Some(case.exit_code) {
        return Err(format!(
            "{label}: exit {:?} != {}\nstdout: {stdout}\nstderr: {stderr}",
            output.status.code(),
            case.exit_code
        ));
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
    let transcript = scratch.transcript();
    if transcript != case.log {
        return Err(format!(
            "{label}: hermes transcript\nrust: {transcript:?}\ngo:   {:?}",
            case.log
        ));
    }
    let seen = provider
        .as_ref()
        .map(FakeProvider::requests)
        .unwrap_or_default();
    compare_requests(label, &case.requests, &seen, &root_text, &url_text)
}

#[test]
fn mcp_desk_ask_with_provider_matches_go_process_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.cases.len(),
        12,
        "every recorded Go case is replayed"
    );
    let failures: Vec<String> = fixture
        .cases
        .iter()
        .filter_map(|case| run_replay(case, &fixture).err())
        .collect();
    assert!(failures.is_empty(), "{}", failures.join("\n---\n"));
}

#[test]
fn replay_rejects_tampered_expectations() {
    let fixture = fixture();
    let case = |name: &str| {
        fixture
            .cases
            .iter()
            .find(|case| case.name == name)
            .cloned()
            .unwrap()
    };
    // A changed answer must be noticed.
    let mut tampered = case("ollama_generate");
    tampered.stdout = tampered.stdout.replace("Budget answer", "Other answer");
    assert!(
        run_replay(&tampered, &fixture)
            .unwrap_err()
            .contains("stdout")
    );
    // A changed provider request must be noticed.
    let mut tampered = case("anthropic_stream");
    tampered.requests[0].body = Some(serde_json::json!({"model": "tampered"}));
    assert!(
        run_replay(&tampered, &fixture)
            .unwrap_err()
            .contains("request 0 body")
    );
    // A changed backend transcript must be noticed.
    let mut tampered = case("hermes_ok");
    tampered.log.push_str("tamper\n");
    assert!(
        run_replay(&tampered, &fixture)
            .unwrap_err()
            .contains("hermes transcript")
    );
}
