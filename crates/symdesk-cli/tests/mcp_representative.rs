#![deny(unsafe_code)]
//! Go-free replay of the raw-process representative MCP corpus
//! (`testdata/port/mcp/representative.json`, MCP-001 and the early surface).
//!
//! The Go responses were recorded once by
//! `go run ./scripts/rust-port/cmd/mcpdiff --record` into
//! `representative-recording.json` (`make mcp-representative-recording-generate`);
//! `make mcp-differential` re-checks that recording against a fresh Go run while
//! Go exists, and this replay compares the Rust server with it without Go.

#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

#[path = "support/isolated_root.rs"]
mod isolated_root;

use std::{
    fs,
    io::Write,
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::mpsc,
    thread,
    time::{Duration, SystemTime},
};

use serde::Deserialize;
use serde_json::Value;

#[derive(Deserialize)]
struct Fixture {
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    request: String,
    #[serde(default)]
    raw_input: String,
    #[serde(default)]
    framed: bool,
    #[serde(default)]
    empty_vault: bool,
    #[serde(default)]
    instructions_absent: bool,
}

#[derive(Deserialize)]
struct Recording {
    schema_version: u32,
    cases: Vec<Observation>,
}

#[derive(Deserialize)]
struct Observation {
    id: String,
    exit_code: i32,
    frames: Vec<Value>,
}

struct Output {
    stdout: Vec<u8>,
    exit_code: i32,
}

fn testdata(name: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/mcp")
        .join(name)
}

fn run(vault: &Path, db: &Path, case_input: String) -> Output {
    let home = vault.join(".harness-home");
    fs::create_dir_all(home.join("tmp")).expect("harness home");
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command.arg("mcp").isolated_env();
    for (name, value) in [
        ("HOME", home.clone()),
        ("USERPROFILE", home.clone()),
        ("XDG_CONFIG_HOME", home.join(".config")),
        ("XDG_DATA_HOME", home.join(".local/share")),
        ("XDG_CACHE_HOME", home.join(".cache")),
        ("TMPDIR", home.join("tmp")),
        ("TMP", home.join("tmp")),
        ("TEMP", home.join("tmp")),
        ("SYMDESK_VAULT", vault.to_path_buf()),
        ("SYMDESK_SIDECAR", db.to_path_buf()),
    ] {
        command.env(name, value);
    }
    for (name, value) in [
        ("LANG", "C"),
        ("LC_ALL", "C"),
        ("TZ", "UTC"),
        ("TERM", "dumb"),
        ("NO_COLOR", "1"),
    ] {
        command.env(name, value);
    }
    for name in ["PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"] {
        if let Some(value) = std::env::var_os(name) {
            command.env(name, value);
        }
    }
    let mut child = command
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("launch symdesk mcp");
    let mut stdin = child.stdin.take().expect("stdin pipe");
    // A write error means the server already exited (clean EOF cases); ignore it.
    let _ = stdin.write_all(case_input.as_bytes());
    drop(stdin);
    let (done, wait) = mpsc::channel();
    thread::spawn(move || {
        let _ = done.send(child.wait_with_output());
    });
    let output = wait
        .recv_timeout(Duration::from_secs(60))
        .expect("MCP process timed out")
        .expect("wait for symdesk mcp");
    Output {
        stdout: output.stdout,
        exit_code: output.status.code().unwrap_or(-1),
    }
}

fn input_of(case: &Case) -> String {
    if !case.raw_input.is_empty() {
        case.raw_input.clone()
    } else if case.framed {
        format!(
            "Content-Length: {}\r\n\r\n{}",
            case.request.len(),
            case.request
        )
    } else {
        format!("{}\n", case.request)
    }
}

fn decode_frames(data: &[u8], framed: bool) -> Result<Vec<Value>, String> {
    let mut frames = Vec::new();
    if !framed {
        for line in data.split(|byte| *byte == b'\n') {
            if line.iter().all(u8::is_ascii_whitespace) {
                continue;
            }
            frames.push(serde_json::from_slice(line).map_err(|error| error.to_string())?);
        }
        return Ok(frames);
    }
    let mut rest = data;
    while !rest.iter().all(u8::is_ascii_whitespace) {
        let line_end = rest
            .iter()
            .position(|byte| *byte == b'\n')
            .ok_or("unexpected EOF")?;
        let header = String::from_utf8_lossy(&rest[..line_end]).trim().to_owned();
        rest = &rest[line_end + 1..];
        while matches!(rest.first(), Some(b'\r' | b'\n')) {
            rest = &rest[1..];
        }
        let length: usize = header
            .strip_prefix("Content-Length: ")
            .and_then(|value| value.parse().ok())
            .filter(|length| (1..=1 << 20).contains(length))
            .ok_or_else(|| format!("bad header {header:?}"))?;
        if rest.len() < length {
            return Err("unexpected EOF".to_owned());
        }
        frames.push(serde_json::from_slice(&rest[..length]).map_err(|error| error.to_string())?);
        rest = &rest[length..];
    }
    Ok(frames)
}

/// Replay every case whose id starts with `prefix`; returns how many ran.
fn replay(prefix: &str) -> usize {
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(testdata("representative.json")).expect("request fixture"),
    )
    .expect("decode request fixture");
    let recording: Recording = serde_json::from_slice(
        &fs::read(testdata("representative-recording.json")).expect("Go recording"),
    )
    .expect("decode Go recording");
    assert_eq!(recording.schema_version, 1);
    assert_eq!(
        fixture
            .cases
            .iter()
            .map(|case| case.id.as_str())
            .collect::<Vec<_>>(),
        recording
            .cases
            .iter()
            .map(|case| case.id.as_str())
            .collect::<Vec<_>>(),
        "the Go recording must cover exactly the fixture requests, in order"
    );

    let root = isolated_root::create("symdesk-mcp-representative");
    fs::write(
        root.join("alpha.md"),
        "---\ntitle: Alpha Note\ncreated: 2026-01-02T03:04:05Z\n---\nneedle alpha body\n",
    )
    .expect("alpha.md");
    fs::create_dir(root.join("nested")).expect("nested dir");
    fs::write(
        root.join("nested/beta.md"),
        "---\ntitle: Nested Beta\n---\nneedle beta body\n",
    )
    .expect("beta.md");
    // desk_ls reports modification times; the Go recording pinned them to this instant.
    let pinned = SystemTime::UNIX_EPOCH + Duration::from_secs(1_767_323_045); // 2026-01-02T03:04:05Z
    for name in ["alpha.md", "nested/beta.md"] {
        fs::File::options()
            .write(true)
            .open(root.join(name))
            .and_then(|file| file.set_modified(pinned))
            .expect("pin mtime");
    }
    let root_text = root.to_string_lossy().into_owned();

    let mut ran = 0;
    for (case, golden) in fixture.cases.iter().zip(&recording.cases) {
        if !case.id.starts_with(prefix) {
            continue;
        }
        let (vault, db) = if case.empty_vault {
            let vault = root.join("empty");
            fs::create_dir_all(&vault).expect("empty vault");
            let db = vault.join("rust.db");
            (vault, db)
        } else {
            (root.clone(), root.join("rust.db"))
        };
        if case.id == "search-call" {
            let prepared = run(
                &vault,
                &db,
                "{\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"tools/call\",\"params\":{\"name\":\"desk_ls\",\"arguments\":{}}}\n"
                    .to_owned(),
            );
            assert_eq!(prepared.exit_code, 0, "{}: prepare", case.id);
        }
        let output = run(&vault, &db, input_of(case));
        let masked = String::from_utf8_lossy(&output.stdout).replace(&root_text, "<ROOT>");
        let frames = decode_frames(masked.as_bytes(), case.framed)
            .unwrap_or_else(|error| panic!("{}: Rust output {error}: {masked:?}", case.id));
        assert_eq!(output.exit_code, golden.exit_code, "{}: exit code", case.id);
        if case.instructions_absent {
            assert_eq!(frames.len(), 1, "{}: one initialize response", case.id);
            assert!(
                frames[0]["result"].get("instructions").is_none(),
                "{}: instructions",
                case.id
            );
        }
        assert_eq!(
            frames, golden.frames,
            "{}: response mismatch\nGo: {:?}\nRust: {:?}",
            case.id, golden.frames, frames
        );
        ran += 1;
    }
    fs::remove_dir_all(&root).expect("remove replay root");
    ran
}

/// MCP-001: initialize/ping/invalid-request framing cases (`make mcp-initialize-differential`).
#[test]
fn mcp001_cases_replay_the_go_recording() {
    assert_eq!(
        replay("mcp001-"),
        7,
        "the recording must retain all MCP-001 cases"
    );
}

/// The early raw-process surface: initialize, catalog, status/ls/search calls,
/// notifications, malformed input and Content-Length framing.
#[test]
fn surface_cases_replay_the_go_recording() {
    assert_eq!(
        replay(""),
        23,
        "the recording must retain every representative case"
    );
}
