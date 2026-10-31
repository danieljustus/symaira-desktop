#![cfg(unix)]
#![deny(unsafe_code)]

//! Replays the Go `symdesk sources add|list|remove|watch` process fixture
//! (`testdata/port/cli/sources-process.json`) against the Rust CLI with the
//! same loopback embedding provider, comparing output, exit status, provider
//! requests, the registry file and the shared retrieval database per step.
//!
//! Declared comparison limits (mirrored in the Go generator):
//! - stderr of non-watch steps is compared as a sorted list of lines: Go
//!   prepares one directory's files in parallel and prints their `Indexed:`
//!   lines in completion order. Provider retry diagnostics (`engine: ...`)
//!   carry transport-specific text and are dropped on both sides.
//! - provider requests are compared as a multiset sorted by body, for the same
//!   reason.
//! - `extractions.id` follows commit order and `created_at` is wall-clock.
//! - `watch` is bounded by the harness: it waits for the `Watching` line,
//!   applies the recorded changes, waits for each `Watch:` summary line, and
//!   then sends SIGINT (the Go CLI installs no handler either, so both exit by
//!   signal and report exit status -1).

use std::{
    collections::BTreeMap,
    fs,
    io::{BufRead, BufReader, Read, Write},
    net::{TcpListener, TcpStream},
    os::unix::fs::{PermissionsExt, symlink},
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;
use serde::Deserialize;
use serde_json::{Value, json};

const FAIL_MARKER: &str = "provider-fail";
const EXTRACTION_TERMS: &[&str] = &["invoice", "grounded", "cafe"];

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct ProcessResult {
    exit_code: i32,
    stdout: String,
    stderr: String,
}

#[derive(Clone, Debug, Deserialize)]
struct FixtureFile {
    path: String,
    #[serde(default)]
    body: String,
    #[serde(default)]
    base64: String,
}

#[derive(Debug, Deserialize)]
struct Link {
    path: String,
    target: String,
}

#[derive(Debug, Deserialize)]
struct Fill {
    path: String,
    prefix: String,
    bytes: usize,
}

#[derive(Debug, Deserialize)]
struct WatchAction {
    #[serde(default)]
    writes: Vec<FixtureFile>,
    #[serde(default)]
    removes: Vec<String>,
    wait: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq)]
struct Request {
    method: String,
    path: String,
    content_type: String,
    body: Value,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Document {
    path: String,
    hash: String,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Chunk {
    document_path: String,
    chunk_index: i64,
    content: String,
    hash: String,
    embedding: String,
    norm: String,
    binary_signature: String,
    binary_signature_type: String,
    dim: i64,
    model: String,
    char_start: Option<i64>,
    char_end: Option<i64>,
    embedding_pending: i64,
    anchor_kind: String,
    anchor_value: String,
    content_norm: String,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Extraction {
    id: i64,
    document_path: String,
    chunk_index: Option<i64>,
    class: String,
    value: String,
    evidence_text: String,
    span_start: Option<i64>,
    span_end: Option<i64>,
    matched: i64,
    producer: String,
    source_ref: String,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct State {
    exists: bool,
    documents: Vec<Document>,
    chunks: Vec<Chunk>,
    #[serde(default)]
    extractions: Vec<Extraction>,
    #[serde(default)]
    extraction_fts: BTreeMap<String, Vec<i64>>,
    #[serde(default)]
    generation: i64,
    #[serde(default)]
    index_storage: i64,
}

#[derive(Debug, Deserialize)]
struct Step {
    id: String,
    #[serde(default)]
    writes: Vec<FixtureFile>,
    #[serde(default)]
    fills: Vec<Fill>,
    #[serde(default)]
    removes: Vec<String>,
    #[serde(default)]
    links: Vec<Link>,
    args: Vec<String>,
    #[serde(default)]
    watch: Option<Vec<WatchAction>>,
    result: ProcessResult,
    requests: Vec<Request>,
    state: State,
    #[serde(default)]
    registry: String,
    #[serde(default)]
    registry_mode: String,
}

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    config: String,
    vault: Vec<FixtureFile>,
    sources: Vec<String>,
    sorted_stderr: bool,
    steps: Vec<Step>,
}

struct TempRoot(PathBuf);

impl TempRoot {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system time")
            .as_nanos();
        let root = fs::canonicalize(std::env::temp_dir())
            .expect("canonical temp dir")
            .join(format!("symdesk-sources-{}-{nonce}", std::process::id()));
        for name in ["home/.config/symseek", "cwd", "data", "tmp", "vault/inner"] {
            fs::create_dir_all(root.join(name)).expect("create fixture directory");
        }
        Self(root)
    }

    fn path(&self, child: &str) -> PathBuf {
        self.0.join(child)
    }
}

impl Drop for TempRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

struct Provider {
    port: u16,
    requests: Arc<Mutex<Vec<Request>>>,
    stop: Arc<AtomicBool>,
    worker: Option<thread::JoinHandle<()>>,
}

impl Provider {
    fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").expect("bind local embedding server");
        listener
            .set_nonblocking(true)
            .expect("set listener nonblocking");
        let port = listener.local_addr().expect("local address").port();
        let requests = Arc::new(Mutex::new(Vec::new()));
        let stop = Arc::new(AtomicBool::new(false));
        let (thread_requests, thread_stop) = (Arc::clone(&requests), Arc::clone(&stop));
        let worker = thread::spawn(move || {
            while !thread_stop.load(Ordering::Relaxed) {
                match listener.accept() {
                    Ok((stream, _)) => serve(stream, &thread_requests),
                    Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                        thread::sleep(Duration::from_millis(5));
                    }
                    Err(error) => panic!("accept fake embedding request: {error}"),
                }
            }
        });
        Self {
            port,
            requests,
            stop,
            worker: Some(worker),
        }
    }

    fn take(&self) -> Vec<Request> {
        std::mem::take(&mut *self.requests.lock().expect("request capture"))
    }
}

impl Drop for Provider {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::Relaxed);
        if let Some(worker) = self.worker.take() {
            let _ = worker.join();
        }
    }
}

/// Mirrors the Go fixture provider: a deterministic vector per input text, and
/// a 500 for any request whose inputs contain the failure marker.
fn serve(mut stream: TcpStream, requests: &Mutex<Vec<Request>>) {
    stream.set_nonblocking(false).expect("blocking stream");
    stream
        .set_read_timeout(Some(Duration::from_secs(3)))
        .expect("read timeout");
    let mut reader = BufReader::new(stream.try_clone().expect("clone stream"));
    let mut request_line = String::new();
    reader.read_line(&mut request_line).expect("request line");
    let (mut length, mut content_type) = (0usize, String::new());
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).expect("request header");
        if line == "\r\n" || line.is_empty() {
            break;
        }
        if let Some((name, value)) = line.split_once(':') {
            match name.trim().to_ascii_lowercase().as_str() {
                "content-length" => length = value.trim().parse().expect("content length"),
                "content-type" => value.trim().clone_into(&mut content_type),
                _ => {}
            }
        }
    }
    let mut body = vec![0; length];
    reader.read_exact(&mut body).expect("request body");
    let body: Value = serde_json::from_slice(&body).expect("request JSON");
    let mut parts = request_line.split_whitespace();
    let method = parts.next().unwrap_or_default().to_owned();
    let path = parts.next().unwrap_or_default().to_owned();
    let inputs = body["input"]
        .as_array()
        .map(|values| {
            values
                .iter()
                .map(|value| value.as_str().unwrap_or_default().to_owned())
                .collect::<Vec<_>>()
        })
        .unwrap_or_default();
    requests.lock().expect("request capture").push(Request {
        method,
        path,
        content_type,
        body,
    });
    let (status, response) = if inputs.iter().any(|input| input.contains(FAIL_MARKER)) {
        (
            "500 Internal Server Error",
            json!({"error": "fixture provider failure"}),
        )
    } else {
        let data = inputs
            .iter()
            .map(|input| {
                let first = (input.len() % 97) as f32 / 97.0;
                json!({"embedding": [first, 0.5_f32, -0.25_f32]})
            })
            .collect::<Vec<_>>();
        ("200 OK", json!({"data": data}))
    };
    let response = response.to_string();
    write!(
        stream,
        "HTTP/1.1 {status}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{response}",
        response.len()
    )
    .expect("write fake embedding response");
}

fn decode_base64(text: &str) -> Vec<u8> {
    const ALPHABET: &[u8] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut output = Vec::new();
    let (mut accumulator, mut bits) = (0u32, 0u32);
    for byte in text.bytes().filter(|byte| *byte != b'=') {
        let value = ALPHABET
            .iter()
            .position(|candidate| *candidate == byte)
            .expect("base64 character") as u32;
        accumulator = (accumulator << 6) | value;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            output.push((accumulator >> bits) as u8);
            accumulator &= (1 << bits) - 1;
        }
    }
    output
}

fn write_file(base: &Path, file: &FixtureFile) {
    let target = base.join(&file.path);
    fs::create_dir_all(target.parent().expect("parent")).expect("create fixture parent");
    let body = if file.base64.is_empty() {
        file.body.clone().into_bytes()
    } else {
        decode_base64(&file.base64)
    };
    fs::write(target, body).expect("write fixture file");
}

fn fixture() -> Fixture {
    let path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/cli/sources-process.json");
    serde_json::from_slice(&fs::read(path).expect("Go-generated sources fixture"))
        .expect("valid fixture")
}

struct Masks {
    root: PathBuf,
    ids: Vec<(String, String)>,
}

impl Masks {
    fn new(root: &Path, sources: &[String]) -> Self {
        let ids = sources
            .iter()
            .map(|relative| (id_for(root, relative), format!("$ID:{relative}")))
            .collect();
        Self {
            root: root.to_path_buf(),
            ids,
        }
    }

    fn normalize(&self, text: &str) -> String {
        let mut value = text.to_owned();
        for (id, name) in &self.ids {
            value = value.replace(id, name);
        }
        value.replace(&*self.root.to_string_lossy(), "$ROOT")
    }

    fn expand(&self, argument: &str) -> String {
        let mut value = argument.to_owned();
        while let Some(start) = value.find("$ID:") {
            let end = value[start + 4..]
                .find(|character: char| {
                    !(character.is_ascii_alphanumeric() || "_./-".contains(character))
                })
                .map_or(value.len(), |offset| start + 4 + offset);
            let id = id_for(&self.root, &value[start + 4..end]);
            value.replace_range(start..end, &id);
        }
        value.replace("$ROOT", &self.root.to_string_lossy())
    }
}

fn id_for(root: &Path, relative: &str) -> String {
    format!(
        "folder-{}",
        symdesk_vault::sha256_hex(root.join(relative).to_string_lossy().as_bytes())
    )
}

fn without_provider_diagnostics(stderr: &str) -> String {
    stderr
        .split_inclusive('\n')
        .filter(|line| !line.starts_with("engine: "))
        .collect()
}

fn sorted_lines(text: &str) -> String {
    let text = without_provider_diagnostics(text);
    let mut lines = text
        .split_inclusive('\n')
        .map(|line| line.trim_end_matches('\n').to_owned())
        .collect::<Vec<_>>();
    lines.sort();
    lines.iter().map(|line| format!("{line}\n")).collect()
}

fn command(root: &TempRoot, args: &[String]) -> Command {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command
        .arg("--vault")
        .arg(root.path("vault"))
        .args(args)
        .current_dir(root.path("cwd"));
    for (key, value) in [
        ("HOME", root.path("home")),
        ("USERPROFILE", root.path("home")),
        ("XDG_DATA_HOME", root.path("data")),
        ("TMPDIR", root.path("tmp")),
        ("TMP", root.path("tmp")),
        ("TEMP", root.path("tmp")),
    ] {
        command.env(key, value);
    }
    for key in ["SYMDESK_SIDECAR", "SYMDESK_VAULT", "XDG_CONFIG_HOME"] {
        command.env_remove(key);
    }
    command
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC")
        .env("TERM", "dumb")
        .env("NO_COLOR", "1");
    command
}

fn wait_for(buffer: &Mutex<String>, needle: &str) {
    let deadline = Instant::now() + Duration::from_secs(30);
    while Instant::now() < deadline {
        if buffer.lock().expect("stderr buffer").contains(needle) {
            return;
        }
        thread::sleep(Duration::from_millis(20));
    }
    panic!(
        "timed out waiting for {needle:?} in stderr:\n{}",
        buffer.lock().expect("stderr buffer")
    );
}

fn run_process(root: &TempRoot, args: &[String]) -> (i32, String, String) {
    let output = command(root, args).output().expect("run Rust CLI process");
    (
        output.status.code().unwrap_or(-1),
        String::from_utf8_lossy(&output.stdout).into_owned(),
        String::from_utf8_lossy(&output.stderr).into_owned(),
    )
}

fn run_watch(root: &TempRoot, args: &[String], actions: &[WatchAction]) -> (i32, String, String) {
    let mut child = command(root, args)
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("start Rust CLI process");
    let buffer = Arc::new(Mutex::new(String::new()));
    let mut stderr = child.stderr.take().expect("child stderr");
    let reader_buffer = Arc::clone(&buffer);
    let reader = thread::spawn(move || {
        let mut chunk = [0u8; 4096];
        while let Ok(read) = stderr.read(&mut chunk) {
            if read == 0 {
                break;
            }
            reader_buffer
                .lock()
                .expect("stderr buffer")
                .push_str(&String::from_utf8_lossy(&chunk[..read]));
        }
    });
    wait_for(&buffer, "Watching ");
    for action in actions {
        for file in &action.writes {
            write_file(&root.0, file);
        }
        for name in &action.removes {
            let target = root.0.join(name);
            if target.is_dir() {
                fs::remove_dir_all(target).expect("remove watched directory");
            } else {
                fs::remove_file(target).expect("remove watched file");
            }
        }
        wait_for(&buffer, &action.wait);
    }
    let status = Command::new("kill")
        .args(["-INT", &child.id().to_string()])
        .status()
        .expect("send SIGINT");
    assert!(status.success(), "kill -INT failed");
    let status = child.wait().expect("wait for Rust CLI process");
    reader.join().expect("stderr reader");
    let mut stdout = String::new();
    if let Some(mut pipe) = child.stdout.take() {
        let _ = pipe.read_to_string(&mut stdout);
    }
    let stderr = buffer.lock().expect("stderr buffer").clone();
    (status.code().unwrap_or(-1), stdout, stderr)
}

fn read_state(root: &TempRoot, masks: &Masks) -> State {
    let path = root.path("data/retrieval.db");
    if !path.exists() {
        return State {
            exists: false,
            documents: Vec::new(),
            chunks: Vec::new(),
            extractions: Vec::new(),
            extraction_fts: BTreeMap::new(),
            generation: 0,
            index_storage: 0,
        };
    }
    let db = Connection::open(path).expect("open retrieval db");
    let documents = db
        .prepare("SELECT path, hash FROM documents ORDER BY path")
        .expect("documents query")
        .query_map([], |row| {
            Ok(Document {
                path: masks.normalize(&row.get::<_, String>(0)?),
                hash: row.get(1)?,
            })
        })
        .expect("documents")
        .collect::<Result<Vec<_>, _>>()
        .expect("document rows");
    let chunks = db
        .prepare(
            "SELECT document_path, chunk_index, content, hash, embedding, norm, binary_signature,
             typeof(binary_signature), embedding_dim, embedding_model, char_start, char_end,
             embedding_pending, anchor_kind, anchor_value, content_norm
             FROM chunks ORDER BY document_path, chunk_index",
        )
        .expect("chunks query")
        .query_map([], |row| {
            let hex = |bytes: Option<Vec<u8>>| {
                bytes
                    .unwrap_or_default()
                    .iter()
                    .map(|byte| format!("{byte:02x}"))
                    .collect::<String>()
            };
            Ok(Chunk {
                document_path: masks.normalize(&row.get::<_, String>(0)?),
                chunk_index: row.get(1)?,
                content: row.get(2)?,
                hash: row.get(3)?,
                embedding: hex(row.get(4)?),
                norm: format!("{:x}", row.get::<_, f64>(5)?.to_bits()),
                binary_signature: hex(row.get(6)?),
                binary_signature_type: row.get(7)?,
                dim: row.get(8)?,
                model: row.get(9)?,
                char_start: row.get(10)?,
                char_end: row.get(11)?,
                embedding_pending: row.get(12)?,
                anchor_kind: row.get(13)?,
                anchor_value: row.get(14)?,
                content_norm: row.get(15)?,
            })
        })
        .expect("chunks")
        .collect::<Result<Vec<_>, _>>()
        .expect("chunk rows");
    let generation = db
        .query_row(
            "SELECT value FROM index_meta WHERE key='generation'",
            [],
            |row| row.get(0),
        )
        .expect("generation");
    let index_storage = db
        .query_row("SELECT COUNT(*) FROM index_storage", [], |row| row.get(0))
        .expect("index storage");
    let extractions = db
        .prepare(
            "SELECT e.id, e.document_path, c.chunk_index, e.class, e.value, e.evidence_text,
             e.span_start, e.span_end, e.matched, e.producer, e.source_ref
             FROM extractions e LEFT JOIN chunks c ON c.id = e.chunk_id
             ORDER BY e.document_path, e.id",
        )
        .expect("extractions query")
        .query_map([], |row| {
            Ok(Extraction {
                id: 0,
                document_path: masks.normalize(&row.get::<_, String>(1)?),
                chunk_index: row.get(2)?,
                class: row.get(3)?,
                value: row.get(4)?,
                evidence_text: row.get(5)?,
                span_start: row.get(6)?,
                span_end: row.get(7)?,
                matched: row.get(8)?,
                producer: row.get(9)?,
                source_ref: masks.normalize(&row.get::<_, String>(10)?),
            })
        })
        .expect("extractions")
        .collect::<Result<Vec<_>, _>>()
        .expect("extraction rows");
    let mut extraction_fts = BTreeMap::new();
    for term in EXTRACTION_TERMS {
        let count: i64 = db
            .query_row(
                "SELECT COUNT(*) FROM extractions_fts WHERE extractions_fts MATCH ?1",
                [term],
                |row| row.get(0),
            )
            .expect("fts count");
        extraction_fts.insert((*term).to_owned(), vec![count]);
    }
    State {
        exists: true,
        documents,
        chunks,
        extractions,
        extraction_fts,
        generation,
        index_storage,
    }
}

fn sort_requests(mut requests: Vec<Request>) -> Vec<Request> {
    requests.sort_by_key(|request| request.body.to_string());
    requests
}

#[test]
fn sources_match_go_process_fixture() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert!(fixture.sorted_stderr);
    assert!(fixture.steps.len() >= 30, "fixture lost steps");
    let root = TempRoot::new();
    let masks = Masks::new(&root.0, &fixture.sources);
    for file in &fixture.vault {
        write_file(&root.path("vault"), file);
    }
    let provider = Provider::start();
    let index_path = root
        .path("data/retrieval.db")
        .to_string_lossy()
        .into_owned();
    let config = fixture
        .config
        .replace("$INDEX_PATH", &format!("{index_path:?}"))
        .replace("$SERVER", &format!("http://127.0.0.1:{}", provider.port));
    fs::write(root.path("home/.config/symseek/config.toml"), config).expect("write config");

    let mut watched = 0;
    for step in &fixture.steps {
        for file in &step.writes {
            write_file(&root.0, file);
        }
        for fill in &step.fills {
            let mut bytes = fill.prefix.clone().into_bytes();
            bytes.resize(fill.bytes, b'x');
            let target = root.0.join(&fill.path);
            fs::create_dir_all(target.parent().expect("parent")).expect("fill parent");
            fs::write(target, bytes).expect("write fill");
        }
        for name in &step.removes {
            let target = root.0.join(name);
            if target.is_dir() {
                fs::remove_dir_all(target).expect("apply step directory removal");
            } else if target.exists() {
                fs::remove_file(target).expect("apply step removal");
            }
        }
        for link in &step.links {
            symlink(&link.target, root.0.join(&link.path)).expect("create step symlink");
        }
        provider.take();
        let args = step
            .args
            .iter()
            .map(|argument| masks.expand(argument))
            .collect::<Vec<_>>();
        let (code, stdout, stderr) = match &step.watch {
            Some(actions) => {
                watched += 1;
                run_watch(&root, &args, actions)
            }
            None => run_process(&root, &args),
        };
        let stderr = if step.watch.is_some() {
            without_provider_diagnostics(&masks.normalize(&stderr))
        } else {
            sorted_lines(&masks.normalize(&stderr))
        };
        let actual = ProcessResult {
            exit_code: code,
            stdout: masks.normalize(&stdout),
            stderr,
        };
        let expected = ProcessResult {
            exit_code: step.result.exit_code,
            stdout: step.result.stdout.clone(),
            stderr: if step.watch.is_some() {
                without_provider_diagnostics(&step.result.stderr)
            } else {
                sorted_lines(&step.result.stderr)
            },
        };
        assert_eq!(actual, expected, "step {} process output", step.id);
        assert_eq!(
            sort_requests(provider.take()),
            sort_requests(step.requests.clone()),
            "step {} requests",
            step.id
        );
        assert_eq!(
            read_state(&root, &masks),
            step.state,
            "step {} retrieval state",
            step.id
        );
        let registry_path = root.path("vault/.symdesk/search-sources.json");
        let (registry, mode) = match fs::read_to_string(&registry_path) {
            Ok(text) => (
                masks.normalize(&text),
                format!(
                    "-{}",
                    mode_string(
                        fs::metadata(&registry_path)
                            .expect("registry metadata")
                            .permissions()
                            .mode()
                    )
                ),
            ),
            Err(_) => (String::new(), String::new()),
        };
        assert_eq!(registry, step.registry, "step {} registry file", step.id);
        assert_eq!(mode, step.registry_mode, "step {} registry mode", step.id);
    }
    assert_eq!(watched, 1, "the fixture carries one bounded watch step");
}

/// Go's `FileMode.Perm().String()` for a regular file: `-rw-------`.
fn mode_string(mode: u32) -> String {
    let mut text = String::new();
    for shift in [6, 3, 0] {
        let bits = (mode >> shift) & 0o7;
        text.push(if bits & 4 != 0 { 'r' } else { '-' });
        text.push(if bits & 2 != 0 { 'w' } else { '-' });
        text.push(if bits & 1 != 0 { 'x' } else { '-' });
    }
    text
}
