#![deny(unsafe_code)]

//! Replays the Go `symdesk index` hybrid-retrieval process fixture
//! (`testdata/port/cli/index-retrieval-process.json`) against the Rust CLI
//! with the same deterministic local embedding provider.

use std::{
    collections::BTreeMap,
    fs,
    io::{BufRead, BufReader, Read, Write},
    net::{TcpListener, TcpStream},
    path::{Path, PathBuf},
    process::Command,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, Ordering},
    },
    thread,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;
use serde::Deserialize;
use serde_json::{Value, json};

const FAIL_MARKER: &str = "provider-fail";

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct ProcessResult {
    exit_code: i32,
    stdout: String,
    stderr: String,
}

#[derive(Debug, Deserialize)]
struct FixtureFile {
    path: String,
    body: String,
}

#[derive(Debug, Deserialize, PartialEq)]
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
struct State {
    documents: Vec<Document>,
    chunks: Vec<Chunk>,
    generation: i64,
    index_storage: i64,
    lifecycle: BTreeMap<String, String>,
    lifecycle_reasons: BTreeMap<String, String>,
}

#[derive(Debug, Deserialize)]
struct Step {
    id: String,
    #[serde(default)]
    writes: Vec<FixtureFile>,
    args: Vec<String>,
    result: ProcessResult,
    requests: Vec<Request>,
    state: State,
}

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    config: String,
    files: Vec<FixtureFile>,
    large_file: String,
    large_bytes: usize,
    steps: Vec<Step>,
}

struct TempRoot(PathBuf);

impl TempRoot {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system time")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-index-retrieval-{}-{nonce}",
            std::process::id()
        ));
        for name in ["home/.config/symseek", "cwd", "data", "tmp", "vault/nested"] {
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

/// Mirrors the Go fixture provider: a deterministic vector per input text,
/// and a 500 for any request whose inputs contain the failure marker.
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

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/cli/index-retrieval-process.json");
    serde_json::from_slice(&fs::read(path).expect("Go-generated index retrieval fixture"))
        .expect("valid fixture")
}

fn roots(root: &Path) -> Vec<String> {
    let mut roots = Vec::new();
    if let Ok(canonical) = fs::canonicalize(root) {
        let canonical = canonical.to_string_lossy().into_owned();
        roots.push(
            canonical
                .strip_prefix(r"\\?\")
                .unwrap_or(&canonical)
                .to_owned(),
        );
    }
    roots.push(root.to_string_lossy().into_owned());
    roots
}

fn normalize(text: &str, root: &Path) -> String {
    let mut value = text.to_owned();
    for root in roots(root) {
        value = value
            .replace(&root.replace('\\', "\\\\"), "$ROOT")
            .replace(&root, "$ROOT");
    }
    if cfg!(windows) {
        value = value.replace("\\\\", "/").replace('\\', "/");
    }
    value
}

/// Provider retry diagnostics carry transport-specific error text in both
/// stacks; the contract is every other stderr line, in order.
fn without_provider_diagnostics(stderr: &str) -> String {
    stderr
        .split_inclusive('\n')
        .filter(|line| !line.starts_with("engine: "))
        .collect()
}

fn run(root: &TempRoot, args: &[String]) -> ProcessResult {
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
    let output = command.output().expect("run Rust CLI process");
    ProcessResult {
        exit_code: output.status.code().unwrap_or(-1),
        stdout: normalize(&String::from_utf8_lossy(&output.stdout), &root.0),
        stderr: normalize(&String::from_utf8_lossy(&output.stderr), &root.0),
    }
}

fn sidecar_path(root: &TempRoot) -> PathBuf {
    let canonical = fs::canonicalize(root.path("vault")).expect("canonical vault");
    let canonical = canonical.to_string_lossy();
    let canonical = canonical.strip_prefix(r"\\?\").unwrap_or(&canonical);
    let digest = symdesk_vault::sha256_hex(canonical.as_bytes());
    root.path("data")
        .join("symdesk/vaults")
        .join(&digest[..16])
        .join("sidecar.db")
}

fn read_state(root: &TempRoot) -> State {
    let db = Connection::open(root.path("data/retrieval.db")).expect("open retrieval db");
    let documents = db
        .prepare("SELECT path, hash FROM documents ORDER BY path")
        .expect("documents query")
        .query_map([], |row| {
            Ok(Document {
                path: normalize(&row.get::<_, String>(0)?, &root.0),
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
                document_path: normalize(&row.get::<_, String>(0)?, &root.0),
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
    let sidecar = Connection::open(sidecar_path(root)).expect("open sidecar");
    let mut lifecycle = BTreeMap::new();
    let mut lifecycle_reasons = BTreeMap::new();
    let mut statement = sidecar
        .prepare("SELECT path, state, reason FROM index_lifecycle ORDER BY path")
        .expect("lifecycle query");
    let rows = statement
        .query_map([], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
            ))
        })
        .expect("lifecycle");
    for row in rows {
        let (path, state, reason) = row.expect("lifecycle row");
        let name = Path::new(&path)
            .file_name()
            .expect("file name")
            .to_string_lossy()
            .into_owned();
        lifecycle.insert(name.clone(), state);
        if !reason.is_empty() {
            lifecycle_reasons.insert(name, normalize(&reason, &root.0));
        }
    }
    State {
        documents,
        chunks,
        generation,
        index_storage,
        lifecycle,
        lifecycle_reasons,
    }
}

#[test]
fn index_writes_hybrid_retrieval_like_go_process_fixture() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    let root = TempRoot::new();
    for file in &fixture.files {
        fs::write(root.path("vault").join(&file.path), &file.body).expect("write vault file");
    }
    let mut large = b"# Big\n\n".to_vec();
    large.resize(fixture.large_bytes, b'x');
    fs::write(root.path("vault").join(&fixture.large_file), large).expect("write large file");
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

    for step in &fixture.steps {
        for file in &step.writes {
            fs::write(root.path("vault").join(&file.path), &file.body).expect("apply step write");
        }
        let mut result = run(&root, &step.args);
        result.stderr = without_provider_diagnostics(&result.stderr);
        let expected = ProcessResult {
            exit_code: step.result.exit_code,
            stdout: step.result.stdout.clone(),
            stderr: without_provider_diagnostics(&step.result.stderr),
        };
        assert_eq!(result, expected, "step {} process output", step.id);
        assert_eq!(provider.take(), step.requests, "step {} requests", step.id);
        assert_eq!(
            read_state(&root),
            step.state,
            "step {} retrieval state",
            step.id
        );
    }
}

#[test]
fn provider_diagnostics_filter_keeps_contract_lines() {
    assert_eq!(
        without_provider_diagnostics("engine: retry\nIndexed: a (1 chunks)\nengine: x\n"),
        "Indexed: a (1 chunks)\n"
    );
}
