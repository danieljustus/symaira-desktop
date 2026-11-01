//! CLI-007: replays the signal and lifecycle contract recorded from the real Go
//! `symdesk` (`make cli-signals-fixtures-generate`). Every case starts the Rust
//! binary in its own process group, waits for the recorded readiness signal,
//! then delivers a signal / closes stdin or stdout and compares the eventual
//! state: exit status or signal, stdout, stderr, whether the process outlived
//! the event, the requests a fake server saw and whether a spawned child
//! survived its parent. Wall-clock durations are not compared.
//!
//! Declared differences (all pinned here, none silent):
//! * Rust `serve` prints a `LISTENING http://ADDR` readiness line on stderr;
//!   Go prints a (filtered) slog line instead.
//! * `events_first_vault_change`: Go ends with `fatal error: sync: unlock of
//!   unlocked mutex` on the first filesystem event; Rust replays the fixture's
//!   `rust` override (it emits the NDJSON event line and keeps running).
//! * Go-only readiness text (inbox/mail watcher logs) is replaced by the
//!   fixture's `rust_settle_ms`.
#![cfg(unix)]

use std::{
    collections::BTreeMap,
    fs,
    io::{Read, Write},
    net::{TcpListener, TcpStream},
    os::unix::process::{CommandExt, ExitStatusExt},
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicUsize, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

use serde::Deserialize;

/// A 1x1 PNG the fake server hands to the worker as job input.
const PNG: [u8; 70] = [
    0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
    0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
    0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41, 0x54, 0x78, 0xda, 0x63, 0xfc, 0xcf, 0xc0, 0x50,
    0x0f, 0x00, 0x04, 0x85, 0x01, 0x80, 0x84, 0xa9, 0x8c, 0x21, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45,
    0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
];

const TOKEN: &str = "signal-test-token-0123456789abcdef";
const SIGNALS_FIXTURE: &str = include_str!("../../../testdata/port/cli/signals-process.json");

#[derive(Deserialize, Clone, Default)]
struct Step {
    op: String,
    #[serde(default)]
    text: String,
    #[serde(default)]
    ms: u64,
    #[serde(default)]
    signal: String,
    #[serde(default)]
    data: String,
    #[serde(default)]
    name: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    count: usize,
    #[serde(default)]
    rust_settle_ms: u64,
}

#[derive(Deserialize, Clone)]
struct RustOverride {
    steps: Vec<Step>,
    exit_code: i32,
    #[serde(default)]
    signal: String,
    stdout: String,
    stderr: String,
}

#[derive(Deserialize, Clone)]
struct Case {
    name: String,
    args: Vec<String>,
    #[serde(default)]
    fake: String,
    #[serde(default)]
    env: Vec<String>,
    stdout_mode: String,
    #[serde(default)]
    setup: Vec<String>,
    steps: Vec<Step>,
    #[serde(default)]
    post: Vec<String>,
    #[serde(default)]
    go_defect: String,
    #[serde(default)]
    rust: Option<RustOverride>,
    exit_code: i32,
    #[serde(default)]
    signal: String,
    stdout: String,
    stderr: String,
    #[serde(default)]
    observations: BTreeMap<String, String>,
    #[serde(default)]
    requests: Vec<String>,
    #[serde(default)]
    post_state: BTreeMap<String, String>,
}

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

fn fixture() -> Fixture {
    let fixture: Fixture =
        serde_json::from_str(SIGNALS_FIXTURE).expect("decode Go signals fixture");
    assert_eq!(fixture.schema_version, 1);
    fixture
}

fn binary() -> PathBuf {
    PathBuf::from(env!("CARGO_BIN_EXE_symdesk"))
}

/// What one run produced; compared field by field with the expectation.
#[derive(Debug, PartialEq, Eq, Default, Clone)]
struct Outcome {
    exit_code: i32,
    signal: String,
    stdout: String,
    stderr: String,
    observations: BTreeMap<String, String>,
    requests: Vec<String>,
    post_state: BTreeMap<String, String>,
}

struct Scratch {
    root: PathBuf,
    home: PathBuf,
    bin: PathBuf,
    vault: PathBuf,
    ext: PathBuf,
    port: u16,
    closed_port: u16,
}

fn free_port() -> u16 {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind a free port");
    listener.local_addr().expect("local address").port()
}

fn write_file(path: &Path, content: &str, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    fs::create_dir_all(path.parent().expect("parent")).expect("create parent");
    fs::write(path, content).expect("write file");
    fs::set_permissions(path, fs::Permissions::from_mode(mode)).expect("chmod");
}

/// Answers the language probe and records its arguments, then either holds
/// until killed or prints a result (mirrors the Go harness).
fn fake_tesseract(root: &Path, action: &str) -> String {
    format!(
        "#!/bin/sh\ncase \"$1\" in --list-langs) printf 'List of available languages (3):\\ndeu\\neng\\nosd\\n'; exit 0;; esac\necho \"$@\" >> {args}\necho $$ > {pid}\n: > {ready}\n{action}\n",
        args = root.join("tesseract.args").display(),
        pid = root.join("tesseract.pid").display(),
        ready = root.join("tesseract.ready").display(),
    )
}

impl Scratch {
    fn new(case: &Case, embed_url: &str) -> Self {
        static COUNTER: AtomicUsize = AtomicUsize::new(0);
        let unique = format!(
            "symdesk-signals-{}-{}",
            std::process::id(),
            COUNTER.fetch_add(1, Ordering::SeqCst)
        );
        let root = std::env::temp_dir().join(unique);
        fs::create_dir_all(&root).expect("create scratch");
        let root = fs::canonicalize(&root).expect("canonical scratch");
        let scratch = Self {
            home: root.join("home"),
            bin: root.join("bin"),
            vault: root.join("vault"),
            ext: root.join("ext"),
            root,
            port: free_port(),
            closed_port: free_port(),
        };
        for dir in [&scratch.home, &scratch.bin, &scratch.vault, &scratch.ext] {
            fs::create_dir_all(dir).expect("create scratch directory");
        }
        write_file(
            &scratch.home.join(".config/symseek/config.toml"),
            &format!(
                "ollama_url = \"{embed_url}/api/embeddings\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = 0\nmodel = \"fixture\"\n"
            ),
            0o600,
        );
        write_file(&scratch.vault.join("a.md"), "# A\nhello quarterly\n", 0o600);
        write_file(&scratch.ext.join("x.md"), "# X\nhello\n", 0o600);
        match case.fake.as_str() {
            "hermes-hold" => write_file(
                &scratch.bin.join("hermes"),
                &format!(
                    "#!/bin/sh\necho $$ > {pid}\n: > {ready}\nexec sleep 30\n",
                    pid = scratch.root.join("hermes.pid").display(),
                    ready = scratch.root.join("hermes.ready").display()
                ),
                0o700,
            ),
            "worker-ocr-hold" => write_file(
                &scratch.bin.join("tesseract"),
                &fake_tesseract(&scratch.root, "exec sleep 300"),
                0o700,
            ),
            "worker-ocr-done" => write_file(
                &scratch.bin.join("tesseract"),
                &fake_tesseract(&scratch.root, r"printf 'hello ocr\n'"),
                0o700,
            ),
            "hermes-ok" => write_file(
                &scratch.bin.join("hermes"),
                &format!(
                    "#!/bin/sh\necho $$ > {pid}\nprintf 'Answer [[a.md]]\\n'\n",
                    pid = scratch.root.join("hermes.pid").display()
                ),
                0o700,
            ),
            _ => {}
        }
        scratch
    }

    fn environment(&self, extra: &[String]) -> Vec<(String, String)> {
        let mut env = vec![
            (
                "PATH".to_owned(),
                format!("{}:/usr/bin:/bin", self.bin.display()),
            ),
            ("HOME".to_owned(), self.home.display().to_string()),
            ("TZ".to_owned(), "UTC".to_owned()),
            (
                "XDG_CONFIG_HOME".to_owned(),
                self.home.join("config").display().to_string(),
            ),
            (
                "XDG_CACHE_HOME".to_owned(),
                self.home.join("cache").display().to_string(),
            ),
            (
                "XDG_DATA_HOME".to_owned(),
                self.home.join("data").display().to_string(),
            ),
            (
                "SYMDESK_OLLAMA_URL".to_owned(),
                "http://127.0.0.1:1".to_owned(),
            ),
        ];
        for pair in extra {
            let (key, value) = pair.split_once('=').expect("KEY=VALUE");
            env.push((key.to_owned(), value.to_owned()));
        }
        env
    }

    fn substitute(&self, value: &str, server: &str) -> String {
        value
            .replace("{ext}", &self.ext.display().to_string())
            .replace("{port}", &self.port.to_string())
            .replace("{closedport}", &self.closed_port.to_string())
            .replace("{server}", server)
    }
}

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.root);
    }
}

/// The fake SymDesk server a worker talks to; mirrors the Go test server.
struct FakeServer {
    address: String,
    requests: Arc<Mutex<Vec<String>>>,
    client_gone: Arc<AtomicBool>,
    stop: Arc<AtomicBool>,
}

impl FakeServer {
    fn start(mode: &str) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind fake server");
        listener.set_nonblocking(true).expect("nonblocking");
        let address = listener.local_addr().expect("address").to_string();
        let requests = Arc::new(Mutex::new(Vec::new()));
        let client_gone = Arc::new(AtomicBool::new(false));
        let stop = Arc::new(AtomicBool::new(false));
        let (thread_requests, thread_gone, thread_stop, mode) = (
            requests.clone(),
            client_gone.clone(),
            stop.clone(),
            mode.to_owned(),
        );
        thread::spawn(move || {
            while !thread_stop.load(Ordering::SeqCst) {
                match listener.accept() {
                    Ok((stream, _)) => {
                        let (requests, gone, stop, mode) = (
                            thread_requests.clone(),
                            thread_gone.clone(),
                            thread_stop.clone(),
                            mode.clone(),
                        );
                        thread::spawn(move || serve_fake(stream, &mode, &requests, &gone, &stop));
                    }
                    Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(5));
                    }
                    Err(_) => break,
                }
            }
        });
        Self {
            address,
            requests,
            client_gone,
            stop,
        }
    }

    fn count(&self) -> usize {
        self.requests.lock().unwrap().len()
    }
}

impl Drop for FakeServer {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
    }
}

fn serve_fake(
    mut stream: TcpStream,
    mode: &str,
    requests: &Mutex<Vec<String>>,
    gone: &AtomicBool,
    stop: &AtomicBool,
) {
    stream.set_nonblocking(false).ok();
    stream.set_read_timeout(Some(Duration::from_secs(10))).ok();
    let mut buffer = Vec::new();
    let mut chunk = [0_u8; 4096];
    let (head_end, content_length) = loop {
        match stream.read(&mut chunk) {
            Ok(0) | Err(_) => return,
            Ok(n) => buffer.extend_from_slice(&chunk[..n]),
        }
        if let Some(at) = buffer.windows(4).position(|window| window == b"\r\n\r\n") {
            let head = String::from_utf8_lossy(&buffer[..at]).to_lowercase();
            let length = head
                .lines()
                .find_map(|line| line.strip_prefix("content-length:"))
                .and_then(|value| value.trim().parse::<usize>().ok())
                .unwrap_or(0);
            break (at + 4, length);
        }
    };
    while buffer.len() < head_end + content_length {
        match stream.read(&mut chunk) {
            Ok(0) | Err(_) => return,
            Ok(n) => buffer.extend_from_slice(&chunk[..n]),
        }
    }
    let head = String::from_utf8_lossy(&buffer[..head_end]).into_owned();
    let body = String::from_utf8_lossy(&buffer[head_end..head_end + content_length]).into_owned();
    let mut lines = head.lines();
    let request_line = lines.next().unwrap_or_default();
    let mut parts = request_line.split(' ');
    let (method, target) = (
        parts.next().unwrap_or_default().to_owned(),
        parts.next().unwrap_or_default().to_owned(),
    );
    let authorized = head
        .lines()
        .any(|line| line.eq_ignore_ascii_case(&format!("authorization: Bearer {TOKEN}")));
    requests.lock().unwrap().push(format!(
        "{method} {target} auth={} body={}",
        if authorized { "bearer" } else { "none" },
        body.trim()
    ));
    let path = target.split('?').next().unwrap_or_default();
    let respond = |stream: &mut TcpStream, status: &str, body: &str| {
        let _ = write!(
            stream,
            "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
            body.len()
        );
    };
    if mode == "embed" {
        let count = serde_json::from_str::<serde_json::Value>(&body)
            .ok()
            .and_then(|value| {
                value
                    .get("input")
                    .and_then(|input| input.as_array().map(Vec::len))
            })
            .unwrap_or(0);
        let data = vec![r#"{"embedding":[1,0,0]}"#; count].join(",");
        respond(&mut stream, "200 OK", &format!(r#"{{"data":[{data}]}}"#));
    } else if method == "POST" && path.ends_with("/lease") {
        match mode {
            "worker-stall" => {
                // Wait until the client hangs up (Go: request context done).
                stream.set_nonblocking(true).ok();
                let deadline = Instant::now() + Duration::from_secs(30);
                while Instant::now() < deadline && !stop.load(Ordering::SeqCst) {
                    let mut probe = [0_u8; 1];
                    match stream.read(&mut probe) {
                        Ok(0) => {
                            gone.store(true, Ordering::SeqCst);
                            return;
                        }
                        Ok(_) => {}
                        Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {}
                        Err(_) => {
                            gone.store(true, Ordering::SeqCst);
                            return;
                        }
                    }
                    thread::sleep(Duration::from_millis(5));
                }
            }
            "worker-ocr-hold" | "worker-ocr-done" => respond(
                &mut stream,
                "200 OK",
                r#"{"id":"job1","original_name":"scan.png","content_type":"image/png","capability":"ocr"}"#,
            ),
            "worker-job404" => respond(
                &mut stream,
                "200 OK",
                r#"{"id":"job1","original_name":"a.png","content_type":"image/png","capability":"ocr"}"#,
            ),
            _ => {
                let _ = stream.write_all(b"HTTP/1.1 204 No Content\r\nConnection: close\r\n\r\n");
            }
        }
    } else if method == "GET" && mode.starts_with("worker-ocr-") {
        let _ = write!(
            stream,
            "HTTP/1.1 200 OK\r\nContent-Type: image/png\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
            PNG.len()
        );
        let _ = stream.write_all(&PNG);
    } else if method == "GET" {
        respond(&mut stream, "404 Not Found", r#"{"error":"input missing"}"#);
    } else {
        respond(&mut stream, "200 OK", "{}");
    }
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

fn wait_for(limit: Duration, mut ok: impl FnMut() -> bool) -> bool {
    let deadline = Instant::now() + limit;
    while Instant::now() < deadline {
        if ok() {
            return true;
        }
        thread::sleep(Duration::from_millis(10));
    }
    ok()
}

fn process_running(pid: u32) -> bool {
    if let Ok(stat) = fs::read_to_string(format!("/proc/{pid}/stat"))
        && let Some(end) = stat.rfind(')')
        && let Some(state) = stat[end + 1..].trim_start().chars().next()
    {
        return state != 'Z';
    }
    Command::new("kill")
        .args(["-0", &pid.to_string()])
        .stderr(Stdio::null())
        .status()
        .is_ok_and(|status| status.success())
}

/// An already-exited process makes `kill` fail; that is a behavioural
/// difference the outcome comparison reports, not a harness error.
fn send_signal(pid: u32, name: &str) {
    let _ = Command::new("kill")
        .arg(format!("-{name}"))
        .arg(pid.to_string())
        .stderr(Stdio::null())
        .status()
        .expect("run kill");
}

fn kill_group(pgid: u32) {
    let _ = Command::new("kill")
        .args(["-KILL", "--", &format!("-{pgid}")])
        .stderr(Stdio::null())
        .status();
}

fn normalize(value: &str, scratch: &Scratch, server: &str) -> String {
    // Go slog lines (and Rust's readiness line for serve) are not compared.
    let mut kept = String::new();
    for line in value.split_inclusive('\n') {
        if line.starts_with("time=") && line.contains(" level=INFO ") {
            continue;
        }
        if line.starts_with("LISTENING http://") {
            continue;
        }
        kept.push_str(line);
    }
    let mut value = kept;
    if let Some(at) = value.find("fatal error: ") {
        let line_end = value[at..]
            .find('\n')
            .map_or(value.len(), |end| at + end + 1);
        value = value[at..line_end].to_owned();
    }
    value = value.replace(&scratch.root.display().to_string(), "<ROOT>");
    if !server.is_empty() {
        value = value.replace(server.trim_start_matches("http://"), "<SERVER>");
    }
    value = value.replace(&format!("127.0.0.1:{}", scratch.port), "127.0.0.1:<PORT>");
    mask_after(
        &mask_after(&value, "\"score\":", "\"<SCORE>\"", true),
        "\"ts\":\"",
        "<TS>\"",
        false,
    )
}

/// Replaces the value following `key` (a JSON number when `number`, else a
/// string up to the closing quote) by `replacement`.
fn mask_after(value: &str, key: &str, replacement: &str, number: bool) -> String {
    let mut out = String::new();
    let mut rest = value;
    while let Some(at) = rest.find(key) {
        out.push_str(&rest[..at + key.len()]);
        let tail = &rest[at + key.len()..];
        let consumed = if number {
            let digits = tail
                .bytes()
                .take_while(|byte| matches!(byte, b'0'..=b'9' | b'.' | b'e' | b'E' | b'+' | b'-'))
                .count();
            if digits == 0 || !tail.as_bytes()[0].is_ascii_digit() {
                out.push_str(&tail[..0]);
                rest = tail;
                continue;
            }
            digits
        } else {
            tail.find('"').map_or(tail.len(), |end| end + 1)
        };
        out.push_str(replacement);
        rest = &tail[consumed..];
    }
    out.push_str(rest);
    out
}

fn framed(body: &str) -> Vec<u8> {
    format!("Content-Length: {}\r\n\r\n{body}", body.len()).into_bytes()
}

fn signal_name(signal: i32) -> String {
    match signal {
        15 => "SIGTERM".to_owned(),
        2 => "SIGINT".to_owned(),
        13 => "SIGPIPE".to_owned(),
        9 => "SIGKILL".to_owned(),
        other => format!("signal {other}"),
    }
}

/// Runs one case against the Rust binary. Errors are harness failures (the
/// process never became ready); behavioural differences come back as Outcome.
fn run_case(case: &Case, rust_steps: bool) -> Result<Outcome, String> {
    // Hermetic embedding provider: no recording depends on a service the host
    // happens to run (the Go harness serves the same constant vectors).
    let embedder = FakeServer::start("embed");
    let scratch = Scratch::new(case, &format!("http://{}", embedder.address));
    let steps = if rust_steps {
        case.rust.as_ref().map_or(&case.steps, |over| &over.steps)
    } else {
        &case.steps
    };
    let fake = case
        .fake
        .starts_with("worker-")
        .then(|| FakeServer::start(&case.fake));
    let server = fake
        .as_ref()
        .map_or_else(String::new, |fake| format!("http://{}", fake.address));
    let environment = scratch.environment(&case.env);
    for setup in &case.setup {
        let argv: Vec<String> = match setup.as_str() {
            "index" => vec![
                "--vault".into(),
                scratch.vault.display().to_string(),
                "index".into(),
            ],
            "sources-add" => vec![
                "--vault".into(),
                scratch.vault.display().to_string(),
                "sources".into(),
                "add".into(),
                scratch.ext.display().to_string(),
            ],
            other => return Err(format!("unknown setup {other:?}")),
        };
        let output = Command::new(binary())
            .args(&argv)
            .current_dir(&scratch.root)
            .env_clear()
            .envs(environment.clone())
            .output()
            .map_err(|error| format!("setup {setup}: {error}"))?;
        if !output.status.success() {
            return Err(format!(
                "setup {setup}: {:?} {}",
                output.status,
                String::from_utf8_lossy(&output.stderr)
            ));
        }
    }

    let mut argv = vec!["--vault".to_owned(), scratch.vault.display().to_string()];
    argv.extend(case.args.iter().map(|arg| scratch.substitute(arg, &server)));
    let mut command = Command::new(binary());
    command
        .args(&argv)
        .current_dir(&scratch.root)
        .env_clear()
        .envs(environment)
        .process_group(0)
        .stdin(Stdio::piped())
        .stderr(Stdio::piped());
    let mut stdout_reader: Option<std::io::PipeReader> = None;
    match case.stdout_mode.as_str() {
        "pipe" => {
            command.stdout(Stdio::piped());
        }
        "broken-pipe" => {
            let (reader, writer) = std::io::pipe().map_err(|error| error.to_string())?;
            stdout_reader = Some(reader);
            command.stdout(Stdio::from(writer));
        }
        other => return Err(format!("unknown stdout mode {other:?}")),
    }
    let mut child: Child = command.spawn().map_err(|error| format!("spawn: {error}"))?;
    // `command` owns the pipe writer; release it so the reader sees EOF.
    drop(command);
    let pid = child.id();
    let group = KillGroup(pid);
    let stdout = child
        .stdout
        .take()
        .map_or_else(|| Arc::new(Mutex::new(Vec::new())), collect);
    let stderr = collect(child.stderr.take().expect("stderr pipe"));
    let mut stdin = child.stdin.take();
    let mut held: Vec<TcpStream> = Vec::new();
    let mut observations = BTreeMap::new();
    let mut status = None;
    let poll_exit = |child: &mut Child, status: &mut Option<std::process::ExitStatus>| {
        if status.is_none() {
            *status = child.try_wait().expect("try_wait");
        }
        status.is_some()
    };
    let label = &case.name;
    for step in steps {
        match step.op.as_str() {
            "settle" => thread::sleep(Duration::from_millis(step.ms)),
            "wait_stderr" | "wait_stdout" => {
                if step.rust_settle_ms > 0 {
                    thread::sleep(Duration::from_millis(step.rust_settle_ms));
                    continue;
                }
                let buffer = if step.op == "wait_stderr" {
                    &stderr
                } else {
                    &stdout
                };
                if !wait_for(Duration::from_secs(30), || {
                    text(buffer).contains(&step.text)
                }) {
                    return Err(format!(
                        "{label}: {} never contained {:?}: {:?}",
                        step.op,
                        step.text,
                        text(buffer)
                    ));
                }
            }
            "wait_port" => {
                if !wait_for(Duration::from_secs(30), || {
                    TcpStream::connect(("127.0.0.1", scratch.port)).is_ok()
                }) {
                    return Err(format!(
                        "{label}: port never accepted; stderr={:?}",
                        text(&stderr)
                    ));
                }
            }
            "wait_file" => {
                if !wait_for(Duration::from_secs(30), || {
                    scratch.root.join(&step.path).exists()
                }) {
                    return Err(format!("{label}: {} never appeared", step.path));
                }
            }
            "wait_requests" => {
                let fake = fake.as_ref().expect("fake server");
                if !wait_for(Duration::from_secs(30), || fake.count() >= step.count) {
                    return Err(format!(
                        "{label}: fake server saw {} requests",
                        fake.count()
                    ));
                }
            }
            "wait_exit" => {
                if !wait_for(Duration::from_secs(60), || {
                    poll_exit(&mut child, &mut status)
                }) {
                    return Err(format!(
                        "{label}: process did not exit; stderr={:?}",
                        text(&stderr)
                    ));
                }
            }
            "signal" => send_signal(pid, &step.signal),
            "close_stdin" => drop(stdin.take()),
            "close_stdout_reader" => drop(stdout_reader.take()),
            "send" => {
                if let Some(stdin) = stdin.as_mut() {
                    let _ = stdin
                        .write_all(&framed(&step.data))
                        .and_then(|()| stdin.flush());
                }
            }
            "send_line" => {
                if let Some(stdin) = stdin.as_mut() {
                    let _ = stdin
                        .write_all(format!("{}\n", step.data).as_bytes())
                        .and_then(|()| stdin.flush());
                }
            }
            "observe" => {
                thread::sleep(Duration::from_millis(step.ms));
                let state = if poll_exit(&mut child, &mut status) {
                    "exited"
                } else {
                    "running"
                };
                observations.insert(step.name.clone(), state.to_owned());
            }
            "connect_idle" => held.push(
                TcpStream::connect(("127.0.0.1", scratch.port))
                    .map_err(|error| error.to_string())?,
            ),
            "keepalive_get" => {
                let mut stream = TcpStream::connect(("127.0.0.1", scratch.port))
                    .map_err(|error| error.to_string())?;
                stream
                    .write_all(format!("GET {} HTTP/1.1\r\nHost: x\r\n\r\n", step.path).as_bytes())
                    .map_err(|error| error.to_string())?;
                stream.set_read_timeout(Some(Duration::from_secs(5))).ok();
                let mut buf = [0_u8; 64];
                let n = stream.read(&mut buf).unwrap_or(0);
                let head = String::from_utf8_lossy(&buf[..n]).into_owned();
                observations.insert(
                    step.name.clone(),
                    head.split("\r\n").next().unwrap_or_default().to_owned(),
                );
                held.push(stream);
            }
            "write_file" => write_file(&scratch.root.join(&step.path), &step.data, 0o600),
            "remove_file" => {
                fs::remove_file(scratch.root.join(&step.path)).map_err(|error| error.to_string())?
            }
            other => return Err(format!("{label}: unknown step {other:?}")),
        }
    }
    // Like the Go harness, stdin stays open until the process has ended.
    let stdin_guard = stdin;
    if !wait_for(Duration::from_secs(60), || {
        poll_exit(&mut child, &mut status)
    }) {
        drop(group);
        let _ = child.wait();
        return Err(format!(
            "{label}: process did not end; stderr={:?}",
            text(&stderr)
        ));
    }
    drop(stdout_reader);
    drop(stdin_guard);
    thread::sleep(Duration::from_millis(150));
    let status = status.expect("exit status");
    let (exit_code, signal) = status
        .signal()
        .map_or((status.code().unwrap_or(-1), String::new()), |signal| {
            (-1, signal_name(signal))
        });
    let mut post_state = BTreeMap::new();
    for post in &case.post {
        if post == "port_closed" {
            let closed = TcpStream::connect_timeout(
                &format!("127.0.0.1:{}", scratch.port).parse().unwrap(),
                Duration::from_millis(300),
            )
            .is_err();
            post_state.insert(post.clone(), closed.to_string());
        } else if post == "server_saw_client_gone" {
            let fake = fake.as_ref().expect("fake server");
            wait_for(Duration::from_secs(5), || {
                fake.client_gone.load(Ordering::SeqCst)
            });
            post_state.insert(
                post.clone(),
                fake.client_gone.load(Ordering::SeqCst).to_string(),
            );
        } else if post == "tesseract_alive_after_parent_exit" {
            let pid = fs::read_to_string(scratch.root.join("tesseract.pid"))
                .ok()
                .and_then(|text| text.trim().parse::<u32>().ok());
            post_state.insert(post.clone(), pid.is_some_and(process_running).to_string());
            if let Some(pid) = pid {
                let _ = Command::new("kill")
                    .args(["-KILL", &pid.to_string()])
                    .stderr(Stdio::null())
                    .status();
            }
        } else if post == "tesseract_args" {
            let args = fs::read_to_string(scratch.root.join("tesseract.args")).unwrap_or_default();
            post_state.insert(
                post.clone(),
                args.lines().next().unwrap_or_default().to_owned(),
            );
        } else if post == "child_alive_after_parent_exit" {
            let pid = fs::read_to_string(scratch.root.join("hermes.pid"))
                .ok()
                .and_then(|text| text.trim().parse::<u32>().ok());
            let alive = pid.is_some_and(process_running);
            post_state.insert(post.clone(), alive.to_string());
            if let Some(pid) = pid {
                let _ = Command::new("kill")
                    .args(["-KILL", &pid.to_string()])
                    .stderr(Stdio::null())
                    .status();
            }
        } else if let Some(query) = post.strip_prefix("search:") {
            let output = Command::new(binary())
                .args([
                    "--vault",
                    &scratch.vault.display().to_string(),
                    "--json",
                    "search",
                    query,
                ])
                .current_dir(&scratch.root)
                .env_clear()
                .envs(scratch.environment(&case.env))
                .output()
                .map_err(|error| error.to_string())?;
            post_state.insert(
                post.clone(),
                normalize(&String::from_utf8_lossy(&output.stdout), &scratch, &server),
            );
        } else if post.starts_with("requests_prefix:") {
            // applied below
        } else {
            return Err(format!("{label}: unknown post check {post:?}"));
        }
    }
    let requests = fake.as_ref().map_or_else(Vec::new, |fake| {
        let mut requests = fake.requests.lock().unwrap().clone();
        if let Some(limit) = case
            .post
            .iter()
            .find_map(|post| post.strip_prefix("requests_prefix:"))
            .and_then(|limit| limit.parse::<usize>().ok())
        {
            requests.truncate(limit);
        }
        requests
            .iter()
            .map(|request| normalize(request, &scratch, &server))
            .collect()
    });
    Ok(Outcome {
        exit_code,
        signal,
        stdout: normalize(&text(&stdout), &scratch, &server),
        stderr: normalize(&text(&stderr), &scratch, &server),
        observations,
        requests,
        post_state,
    })
}

/// Kills the process group on drop so a failed case never leaves children.
struct KillGroup(u32);

impl Drop for KillGroup {
    fn drop(&mut self) {
        kill_group(self.0);
    }
}

/// The outcome the Go fixture (or the declared Rust override) expects.
fn expected(case: &Case) -> Outcome {
    let mut outcome = Outcome {
        exit_code: case.exit_code,
        signal: case.signal.clone(),
        stdout: case.stdout.clone(),
        stderr: case.stderr.clone(),
        observations: case.observations.clone(),
        requests: case.requests.clone(),
        post_state: case.post_state.clone(),
    };
    if let Some(over) = &case.rust {
        assert!(
            !case.go_defect.is_empty(),
            "{}: override without a declared Go defect",
            case.name
        );
        outcome.exit_code = over.exit_code;
        outcome.signal = over.signal.clone();
        outcome.stdout = over.stdout.clone();
        outcome.stderr = over.stderr.clone();
    }
    outcome
}

fn difference(case: &Case, expected: &Outcome, actual: &Outcome) -> Option<String> {
    (expected != actual).then(|| {
        format!(
            "{}:\n  expected {expected:?}\n  actual   {actual:?}",
            case.name
        )
    })
}

#[test]
fn signals_match_go() {
    let only = std::env::var("SYMDESK_SIGNALS_ONLY").unwrap_or_default();
    let fixture = fixture();
    let mut failures = Vec::new();
    let mut ran = 0;
    for case in &fixture.cases {
        if !only.is_empty() && !case.name.contains(&only) {
            continue;
        }
        ran += 1;
        let want = expected(case);
        match run_case(case, true) {
            Ok(actual) => failures.extend(difference(case, &want, &actual)),
            Err(error) => failures.push(error),
        }
    }
    assert!(ran > 0, "no signal case ran");
    assert!(
        failures.is_empty(),
        "{} of {ran} cases differ:\n{}",
        failures.len(),
        failures.join("\n")
    );
}

/// The comparator must notice a changed exit code, signal, stream or
/// observation; otherwise a replay could pass without checking anything.
#[test]
fn comparator_detects_mutations() {
    let fixture = fixture();
    let case = fixture
        .cases
        .iter()
        .find(|case| case.name == "mcp_answer_is_flushed_before_stdin_eof_exit")
        .expect("flush case");
    let want = expected(case);
    let actual = run_case(case, true).expect("run flush case");
    assert!(
        difference(case, &want, &actual).is_none(),
        "baseline must match"
    );
    let mut mutations: Vec<Outcome> = Vec::new();
    for mutate in [
        |outcome: &mut Outcome| outcome.exit_code += 1,
        |outcome: &mut Outcome| outcome.signal = "SIGTERM".into(),
        |outcome: &mut Outcome| outcome.stdout.clear(),
        |outcome: &mut Outcome| outcome.stderr.push('x'),
        |outcome: &mut Outcome| {
            outcome
                .observations
                .insert("running".into(), "exited".into());
        },
    ] {
        let mut mutated = actual.clone();
        mutate(&mut mutated);
        mutations.push(mutated);
    }
    for mutated in mutations {
        assert!(
            difference(case, &want, &mutated).is_some(),
            "mutation went unnoticed"
        );
    }
}

/// Every Go-recorded case is replayed against Go's expectation, except the
/// ones with a declared defect; those must name it.
#[test]
fn declared_differences_are_documented() {
    let fixture = fixture();
    let declared: Vec<&str> = fixture
        .cases
        .iter()
        .filter(|case| case.rust.is_some())
        .map(|case| case.name.as_str())
        .collect();
    assert_eq!(declared, ["events_first_vault_change"]);
    for case in &fixture.cases {
        assert_eq!(
            case.rust.is_some(),
            !case.go_defect.is_empty(),
            "{}",
            case.name
        );
    }
}
