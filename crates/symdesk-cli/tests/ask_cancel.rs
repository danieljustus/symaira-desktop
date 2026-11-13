//! Replays cancellation of an in-flight provider call against the process
//! fixture recorded from the real Go CLI (`make ask-cancel-fixtures-generate`):
//! SIGTERM while `ask`, `ask --agent` and MCP `desk_ask` wait on a stalled
//! provider ends the process by the signal and closes the provider connection;
//! closing MCP stdin does not cancel an in-flight ask (the late answer is still
//! delivered). In-process cancellation (a failed output write) is unit-tested
//! in `symdesk-cli` and `symdesk-core::cancel`.
#![cfg(unix)]

use std::{
    collections::BTreeMap,
    io::{Read, Write},
    os::unix::process::ExitStatusExt,
    process::Stdio,
    sync::{Arc, Mutex},
    thread,
    time::{Duration, Instant},
};

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

const SIGTERM: i32 = 15;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    files: BTreeMap<String, String>,
    file_sets: BTreeMap<String, Vec<String>>,
    file_time: String,
    cases: Vec<Case>,
}

#[derive(Deserialize, Clone)]
struct Case {
    name: String,
    kind: String,
    provider: String,
    #[serde(default)]
    args: Vec<String>,
    #[serde(default)]
    mcp_arguments_json: String,
    #[serde(default)]
    env: BTreeMap<String, String>,
    turns: Vec<Turn>,
    #[serde(default)]
    wait_for: String,
    #[serde(default)]
    signal: String,
    exit_code: i32,
    stdout: String,
    stderr: String,
    provider_saw_client_gone: bool,
    #[serde(default)]
    requests: Vec<Request>,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/ai/ask-cancel-process.json"
    ))
    .expect("decode Go-owned ask cancel fixture")
}

fn collect(mut pipe: impl Read + Send + 'static) -> Arc<Mutex<Vec<u8>>> {
    let buffer = Arc::new(Mutex::new(Vec::new()));
    let sink = buffer.clone();
    thread::spawn(move || {
        let mut chunk = [0_u8; 4096];
        while let Ok(n) = pipe.read(&mut chunk) {
            if n == 0 {
                break;
            }
            sink.lock().unwrap().extend_from_slice(&chunk[..n]);
        }
    });
    buffer
}

fn text(buffer: &Arc<Mutex<Vec<u8>>>) -> String {
    String::from_utf8_lossy(&buffer.lock().unwrap()).into_owned()
}

fn wait_for_text(buffer: &Arc<Mutex<Vec<u8>>>, needle: &str, limit: Duration) -> bool {
    let deadline = Instant::now() + limit;
    while Instant::now() < deadline {
        if text(buffer).contains(needle) {
            return true;
        }
        thread::sleep(Duration::from_millis(10));
    }
    text(buffer).contains(needle)
}

fn run_replay(case: &Case, fixture: &Fixture) -> Result<(), String> {
    let label = &case.name;
    let scratch = Scratch::new(
        label,
        &select_files(&fixture.files, &fixture.file_sets, "single"),
        &fixture.file_time,
        None,
    );
    let provider = FakeProvider::start(case.turns.clone());
    let url = provider.url.clone();
    scratch.index("");
    let env = provider_env(&case.provider, Some(&url), &case.env);
    let mut command = scratch.command("", &env);
    command.arg("--vault").arg(&scratch.vault);
    if case.kind == "sigterm" {
        command.args(&case.args);
    } else {
        command.arg("mcp");
    }
    let mut child = command
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let stdout_buffer = collect(child.stdout.take().unwrap());
    let stderr_buffer = collect(child.stderr.take().unwrap());
    let mut stdin = child.stdin.take().unwrap();
    if case.kind != "sigterm" {
        stdin
            .write_all(mcp_call_line(&case.mcp_arguments_json).as_bytes())
            .unwrap();
    }
    if case.kind == "mcp_eof" {
        drop(stdin);
    } else {
        if !provider.wait_started(Duration::from_secs(20)) {
            let _ = child.kill();
            let _ = child.wait();
            return Err(format!("{label}: the provider never received a request"));
        }
        if !case.wait_for.is_empty()
            && !wait_for_text(&stdout_buffer, &case.wait_for, Duration::from_secs(20))
        {
            let _ = child.kill();
            let _ = child.wait();
            return Err(format!(
                "{label}: CLI never printed {:?}; stdout={:?}",
                case.wait_for,
                text(&stdout_buffer)
            ));
        }
        let signalled = std::process::Command::new("kill")
            .args(["-TERM", &child.id().to_string()])
            .status()
            .unwrap();
        assert!(signalled.success());
    }
    let status = child.wait().unwrap();
    // Let the copier threads drain what the dying process wrote.
    thread::sleep(Duration::from_millis(100));

    let expected_signal = match case.signal.as_str() {
        "terminated" => Some(SIGTERM),
        "" => None,
        other => return Err(format!("{label}: unknown signal {other:?} in fixture")),
    };
    if status.signal() != expected_signal {
        return Err(format!(
            "{label}: signal {:?} != {expected_signal:?} (exit {:?})",
            status.signal(),
            status.code()
        ));
    }
    // Go reports a signalled child as exit code -1.
    let exit = status.code().unwrap_or(-1);
    if exit != case.exit_code {
        return Err(format!("{label}: exit {exit} != {}", case.exit_code));
    }
    if case.provider_saw_client_gone && !provider.wait_disconnected(Duration::from_secs(10)) {
        return Err(format!(
            "{label}: the provider did not observe the client disconnect"
        ));
    }
    let root_text = scratch.root_text();
    let stdout = normalize(&text(&stdout_buffer), &root_text, &url);
    let stderr = normalize(&without_log_lines(&text(&stderr_buffer)), &root_text, &url);
    if stdout != case.stdout {
        return Err(format!(
            "{label}: stdout\nrust: {stdout:?}\ngo:   {:?}",
            case.stdout
        ));
    }
    if stderr != case.stderr {
        return Err(format!("{label}: stderr {stderr:?} != {:?}", case.stderr));
    }
    compare_requests(
        label,
        &case.requests,
        &provider.requests(),
        &root_text,
        &url,
    )
}

#[test]
fn ask_cancellation_matches_go_process_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 5, "every recorded Go case is replayed");
    let kinds: Vec<&str> = fixture.cases.iter().map(|c| c.kind.as_str()).collect();
    assert_eq!(
        kinds.iter().filter(|kind| **kind == "sigterm").count(),
        3,
        "ask, agent/ollama, agent/anthropic"
    );
    assert!(kinds.contains(&"mcp_sigterm") && kinds.contains(&"mcp_eof"));
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
    // A different termination signal must be noticed.
    let mut tampered = case("mcp_desk_ask_sigterm");
    tampered.signal = String::new();
    tampered.exit_code = 0;
    assert!(
        run_replay(&tampered, &fixture)
            .unwrap_err()
            .contains("signal")
    );
    // An EOF that cancelled the ask would lose the late answer.
    let mut tampered = case("mcp_stdin_eof_lets_in_flight_ask_finish");
    tampered.stdout = String::new();
    assert!(
        run_replay(&tampered, &fixture)
            .unwrap_err()
            .contains("stdout")
    );
    // A changed request must be noticed.
    let mut tampered = case("ask_agent_anthropic_sigterm");
    tampered.requests[0].body = Some(serde_json::json!({"model": "tampered"}));
    assert!(
        run_replay(&tampered, &fixture)
            .unwrap_err()
            .contains("request 0 body")
    );
}
