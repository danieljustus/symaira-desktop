#![deny(unsafe_code)]

//! Replays `testdata/port/room/mcp-process.json`: the real `symroom mcp`
//! process (MCP-004 concurrency, MCP-005 cancellation and shutdown) as the Go
//! binary behaved when the fixture was generated. Each case drives pipes with
//! timed steps and compares the exit status or signal, stderr and every
//! response frame.
//!
//! One difference is recorded and pinned instead of matched: Go runs every
//! `tools/call` on its own goroutine, Rust on at most 512 worker threads and a
//! queue behind them. A case marks the calls whose answer Go sends at once
//! (`cap_sensitive_ids`); Rust answers them only after a worker is free.

#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

use std::{
    collections::BTreeMap,
    fs,
    io::{Read, Write},
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::{
        Arc, Mutex,
        atomic::{AtomicU64, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use serde::Deserialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};

static TEMP_DIR_COUNTER: AtomicU64 = AtomicU64::new(0);

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hashes: BTreeMap<String, String>,
    identity_key: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Step {
    op: String,
    #[serde(default)]
    data: String,
    #[serde(default)]
    ms: u64,
    #[serde(default)]
    id: i64,
    #[serde(default)]
    method: String,
    #[serde(default)]
    tool: String,
    #[serde(default)]
    args: serde_json::Map<String, Value>,
    #[serde(default)]
    count: i64,
    #[serde(default)]
    cli_args: Vec<String>,
    #[serde(default)]
    signal: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    #[serde(default)]
    unix_only: bool,
    stdout_mode: String,
    #[serde(default)]
    setup: Vec<Vec<String>>,
    steps: Vec<Step>,
    #[serde(default)]
    unordered: bool,
    min_ms: u64,
    max_ms: u64,
    exit_code: i32,
    #[serde(default)]
    signal: String,
    stderr: String,
    first_id: Option<i64>,
    frames: Vec<String>,
    #[serde(default)]
    cap_sensitive_ids: Vec<i64>,
}

struct TempDir(PathBuf);

impl TempDir {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "symroom-mcp-process-{}-{}",
            std::process::id(),
            TEMP_DIR_COUNTER.fetch_add(1, Ordering::SeqCst)
        ));
        fs::create_dir_all(&path).expect("create temp dir");
        Self(path)
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn frame(body: &[u8]) -> Vec<u8> {
    let mut data = format!("Content-Length: {}\r\n\r\n", body.len()).into_bytes();
    data.extend_from_slice(body);
    data
}

fn substitute(value: &str, run_id: &str) -> String {
    value.replace("{run_id}", run_id)
}

fn request_body(id: i64, method: &str, params: Option<Value>) -> Vec<u8> {
    let mut request = json!({"jsonrpc": "2.0", "id": id, "method": method});
    if let Some(params) = params {
        request["params"] = params;
    }
    serde_json::to_vec(&request).expect("request JSON")
}

/// Replaces the JSON string value after `marker` (up to its escaped closing
/// quote) with `mask`.
fn mask_value(body: &mut String, marker: &str, mask: &str) {
    let Some(start) = body.find(marker) else {
        return;
    };
    let value = start + marker.len();
    let end = body[value..]
        .find('\\')
        .map_or(body.len(), |offset| value + offset);
    body.replace_range(value..end, mask);
}

/// Same masks as the Go generator: approval id and expiry depend on the clock.
fn mask_frame(body: &str) -> String {
    let mut body = body.to_owned();
    mask_value(&mut body, "\\\"approval_id\\\":\\\"", "<approval>");
    mask_value(&mut body, "\\\"expires_at\\\":\\\"", "<expires>");
    body
}

fn parse_frames(mut data: &[u8]) -> Vec<String> {
    const PREFIX: &[u8] = b"Content-Length: ";
    let mut frames = Vec::new();
    while !data.is_empty() {
        assert!(data.starts_with(PREFIX), "stdout is not a frame: {data:?}");
        let end = data
            .windows(4)
            .position(|window| window == b"\r\n\r\n")
            .expect("frame header terminated");
        let length: usize = std::str::from_utf8(&data[PREFIX.len()..end])
            .expect("length text")
            .parse()
            .expect("frame length");
        frames.push(
            String::from_utf8(data[end + 4..end + 4 + length].to_vec()).expect("frame is UTF-8"),
        );
        data = &data[end + 4 + length..];
    }
    frames
}

fn frame_id(frame: &str) -> i64 {
    serde_json::from_str::<Value>(frame)
        .ok()
        .and_then(|value| value["id"].as_i64())
        .unwrap_or(-1)
}

struct Outcome {
    exit_code: i32,
    signal: String,
    stderr: String,
    arrival: Vec<String>,
    elapsed: Duration,
}

fn signal_name(number: i32) -> String {
    match number {
        2 => "SIGINT",
        13 => "SIGPIPE",
        15 => "SIGTERM",
        other => return format!("signal {other}"),
    }
    .to_owned()
}

fn environment(root: &Path, key: &str) -> Vec<(String, String)> {
    let home = root.join("home");
    let tmp = root.join("tmp");
    fs::create_dir_all(&home).expect("home");
    fs::create_dir_all(&tmp).expect("tmp");
    let path = |p: &Path| p.to_string_lossy().into_owned();
    vec![
        ("HOME".into(), path(&home)),
        ("USERPROFILE".into(), path(&home)),
        ("XDG_DATA_HOME".into(), path(&root.join("xdg"))),
        ("TMPDIR".into(), path(&tmp)),
        ("TMP".into(), path(&tmp)),
        ("TEMP".into(), path(&tmp)),
        ("TZ".into(), "UTC".into()),
        ("SYMROOM_IDENTITY_KEY".into(), key.to_owned()),
        ("PATH".into(), path(&tmp)),
    ]
}

fn symroom(env: &[(String, String)], dir: Option<&Path>) -> Command {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command.isolated_env().envs(env.iter().cloned());
    if let Some(dir) = dir {
        command.current_dir(dir);
    }
    command
}

fn send_signal(child: &Child, name: &str) {
    let flag = if name == "INT" { "-INT" } else { "-TERM" };
    let status = Command::new("/bin/kill")
        .arg(flag)
        .arg(child.id().to_string())
        .status()
        .expect("kill");
    assert!(status.success(), "kill {flag}");
}

fn run_case(case: &Case, key: &str, root: &Path) -> Outcome {
    let env = environment(root, key);
    let room = root.join("room");
    let init = symroom(&env, None)
        .args(["init", "--name", "T", "--identity", "x"])
        .arg(&room)
        .output()
        .expect("init room");
    assert!(
        init.status.success(),
        "{}: init room: {}",
        case.name,
        String::from_utf8_lossy(&init.stderr)
    );
    let mut run_id = String::new();
    for argv in &case.setup {
        let output = symroom(&env, Some(&room))
            .args(argv)
            .output()
            .expect("setup command");
        assert!(output.status.success(), "{}: setup {argv:?}", case.name);
        if argv.starts_with(&["run".to_owned(), "request".to_owned()]) {
            run_id = format!(
                "run_{}",
                String::from_utf8_lossy(&output.stdout)
                    .trim()
                    .trim_start_matches("ev_")
            );
        }
    }

    let mut command = symroom(&env, None);
    command
        .args(["mcp", "--room"])
        .arg(&room)
        .args(["--identity", "x"])
        .stdin(Stdio::piped())
        .stderr(Stdio::piped());
    let mut broken_reader = None;
    match case.stdout_mode.as_str() {
        "pipe" => {
            command.stdout(Stdio::piped());
        }
        "readonly" => {
            // A descriptor opened for reading: every write fails with EBADF.
            command.stdout(Stdio::from(
                fs::File::open(if cfg!(windows) { "NUL" } else { "/dev/null" }).expect("null"),
            ));
        }
        "broken-pipe" => {
            let (reader, writer) = std::io::pipe().expect("pipe");
            command.stdout(Stdio::from(writer));
            broken_reader = Some(reader);
        }
        other => panic!("unknown stdout mode {other}"),
    }
    let started = Instant::now();
    let mut child = command.spawn().expect("start mcp");
    // `command` still owns the pipe's write end; drop it so the child holds
    // the only one and closing the reader really breaks the pipe.
    drop(command);
    let mut stdin = child.stdin.take();
    let stdout_data = Arc::new(Mutex::new(Vec::<u8>::new()));
    let stdout_thread = child.stdout.take().map(|mut stdout| {
        let data = Arc::clone(&stdout_data);
        thread::spawn(move || {
            let mut chunk = [0_u8; 65536];
            while let Ok(read) = stdout.read(&mut chunk) {
                if read == 0 {
                    break;
                }
                data.lock().unwrap().extend_from_slice(&chunk[..read]);
            }
        })
    });
    let stderr_data = Arc::new(Mutex::new(Vec::<u8>::new()));
    let mut stderr = child.stderr.take().expect("stderr pipe");
    let stderr_thread = {
        let data = Arc::clone(&stderr_data);
        thread::spawn(move || {
            let mut buffer = Vec::new();
            let _ = stderr.read_to_end(&mut buffer);
            data.lock().unwrap().extend_from_slice(&buffer);
        })
    };

    for step in &case.steps {
        match step.op.as_str() {
            "send" => {
                let body = substitute(&step.data, &run_id);
                let data = if body.starts_with("Content-Length:") {
                    body.into_bytes()
                } else {
                    frame(body.as_bytes())
                };
                if let Some(stdin) = stdin.as_mut() {
                    let _ = stdin.write_all(&data);
                    let _ = stdin.flush();
                }
            }
            "request" => {
                let data = frame(&request_body(step.id, &step.method, None));
                if let Some(stdin) = stdin.as_mut() {
                    let _ = stdin.write_all(&data);
                    let _ = stdin.flush();
                }
            }
            "call" | "calls" => {
                let count = if step.op == "calls" { step.count } else { 1 };
                let mut batch = Vec::new();
                for offset in 0..count {
                    let mut args = step.args.clone();
                    for value in args.values_mut() {
                        if let Some(text) = value.as_str() {
                            *value = Value::String(substitute(text, &run_id));
                        }
                    }
                    batch.extend(frame(&request_body(
                        step.id + offset,
                        "tools/call",
                        Some(json!({"name": step.tool, "arguments": args})),
                    )));
                }
                if let Some(stdin) = stdin.as_mut() {
                    let _ = stdin.write_all(&batch);
                    let _ = stdin.flush();
                }
            }
            "sleep" => thread::sleep(Duration::from_millis(step.ms)),
            "close_stdin" => drop(stdin.take()),
            "close_stdout_reader" => drop(broken_reader.take()),
            "signal" => send_signal(&child, &step.signal),
            "cli" => {
                let argv: Vec<String> = step
                    .cli_args
                    .iter()
                    .map(|arg| substitute(arg, &run_id))
                    .collect();
                let output = symroom(&env, Some(&room))
                    .args(&argv)
                    .output()
                    .expect("cli step");
                assert!(output.status.success(), "{}: cli {argv:?}", case.name);
            }
            other => panic!("unknown step {other}"),
        }
    }
    drop(stdin);

    let deadline = Instant::now() + Duration::from_secs(90);
    let status = loop {
        if let Some(status) = child.try_wait().expect("wait") {
            break status;
        }
        assert!(
            Instant::now() < deadline,
            "{}: mcp process did not end",
            case.name
        );
        thread::sleep(Duration::from_millis(10));
    };
    let elapsed = started.elapsed();
    if let Some(thread) = stdout_thread {
        thread.join().expect("stdout thread");
    }
    stderr_thread.join().expect("stderr thread");
    drop(broken_reader);

    #[cfg(unix)]
    let signal = {
        use std::os::unix::process::ExitStatusExt;
        status.signal().map(signal_name).unwrap_or_default()
    };
    #[cfg(not(unix))]
    let signal = String::new();
    let exit_code = if signal.is_empty() {
        status.code().expect("exit code")
    } else {
        -1
    };
    let stdout = stdout_data.lock().unwrap().clone();
    let arrival = parse_frames(&stdout)
        .into_iter()
        .map(|body| mask_frame(&body))
        .collect();
    Outcome {
        exit_code,
        signal,
        stderr: String::from_utf8_lossy(&stderr_data.lock().unwrap()).into_owned(),
        arrival,
        elapsed,
    }
}

#[test]
fn mcp_process_matches_go_for_cancellation_shutdown_and_concurrency() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../");
    let data = fs::read(root.join("testdata/port/room/mcp-process.json"))
        .expect("read Go-generated MCP process fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse MCP process fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.source_hashes.len(), 3);
    for (source, expected) in &fixture.source_hashes {
        let bytes =
            fs::read(root.join(source)).unwrap_or_else(|error| panic!("read {source}: {error}"));
        assert_eq!(
            expected,
            &hex::encode(Sha256::digest(bytes)),
            "Go oracle source hash {source}"
        );
    }
    let temp = TempDir::new();
    let (mut replayed, mut skipped) = (0_usize, 0_usize);
    for (index, case) in fixture.cases.iter().enumerate() {
        if case.unix_only && !cfg!(unix) {
            eprintln!("SKIP {}: needs POSIX descriptors or signals", case.name);
            skipped += 1;
            continue;
        }
        let outcome = run_case(case, &fixture.identity_key, &temp.0.join(index.to_string()));
        replayed += 1;
        let elapsed = u64::try_from(outcome.elapsed.as_millis()).unwrap_or(u64::MAX);
        assert!(
            (case.min_ms..=case.max_ms).contains(&elapsed),
            "{}: elapsed {elapsed}ms outside [{}, {}]",
            case.name,
            case.min_ms,
            case.max_ms
        );
        assert_eq!(
            outcome.exit_code, case.exit_code,
            "{}: exit code",
            case.name
        );
        assert_eq!(outcome.signal, case.signal, "{}: signal", case.name);
        assert_eq!(outcome.stderr, case.stderr, "{}: stderr", case.name);
        let mut frames = outcome.arrival.clone();
        if case.unordered {
            frames.sort_by_key(|body| frame_id(body));
            let first = frame_id(&outcome.arrival[0]);
            assert_eq!(Some(first), case.first_id, "{}: first answer", case.name);
        }
        assert_eq!(frames, case.frames, "{}: response frames", case.name);
        for id in &case.cap_sensitive_ids {
            // Pinned difference (MCP-004): Go answers this call with the first
            // frames; Rust queues it behind the calls beyond its worker cap.
            let position = outcome
                .arrival
                .iter()
                .position(|body| frame_id(body) == *id)
                .expect("cap-sensitive call answered");
            assert!(
                position > 2,
                "{}: call {id} answered at position {position}; the worker cap no longer queues it",
                case.name
            );
        }
    }
    eprintln!(
        "MCP process cases: {} total, {replayed} replayed, {skipped} SKIPPED",
        fixture.cases.len()
    );
    assert!(replayed > 0, "no MCP process case ran");
}
