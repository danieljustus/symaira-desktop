#![deny(unsafe_code)]

//! Replays the Go `symdesk index` / `symdesk index retry` process fixture
//! (`testdata/port/cli/index-retry-process.json`) against the Rust CLI with the
//! same deterministic local embedding provider.

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

/// A lifecycle row the Go oracle wrote through its sidecar API before the step.
/// No Go CLI path leaves a failed row for a non-Markdown extension, so the row
/// is seeded instead of produced by an earlier process.
#[derive(Debug, Deserialize)]
struct Seed {
    path: String,
    state: String,
    reason: String,
}

#[derive(Debug, Deserialize)]
struct Step {
    id: String,
    #[serde(default)]
    dirs: Vec<String>,
    #[serde(default)]
    seeds: Vec<Seed>,
    #[serde(default)]
    writes: Vec<FixtureFile>,
    #[serde(default)]
    removes: Vec<String>,
    args: Vec<String>,
    result: ProcessResult,
    requests: Vec<Request>,
    state: State,
}

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    config: String,
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
            "symdesk-index-retry-{}-{nonce}",
            std::process::id()
        ));
        for name in ["home/.config/symseek", "cwd", "data", "tmp", "vault"] {
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

/// Mirrors the Go fixture provider: one constant 3-dimensional vector per input.
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
    let count = body["input"].as_array().map_or(0, Vec::len);
    requests.lock().expect("request capture").push(Request {
        method,
        path,
        content_type,
        body,
    });
    let data = (0..count)
        .map(|_| json!({"embedding": [0.25_f32, -0.5_f32, 1.0_f32]}))
        .collect::<Vec<_>>();
    let response = json!({"data": data}).to_string();
    write!(
        stream,
        "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{response}",
        response.len()
    )
    .expect("write fake embedding response");
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/cli/index-retry-process.json");
    let mut fixture: Fixture =
        serde_json::from_slice(&fs::read(path).expect("Go-generated index retry fixture"))
            .expect("valid fixture");
    if cfg!(windows) {
        // The frozen fixture records Unix stat wording. The native Windows Go
        // oracle reports the same missing file through GetFileAttributesEx.
        for step in &mut fixture.steps {
            for reason in step
                .state
                .lifecycle_reasons
                .values_mut()
                .chain(std::iter::once(&mut step.result.stdout))
            {
                for name in ["gone.md", "missing.txt"] {
                    *reason = reason.replace(
                        &format!("lstat $ROOT/vault/{name}: no such file or directory"),
                        &format!(
                            "GetFileAttributesEx $ROOT/vault/{name}: The system cannot find the file specified."
                        ),
                    );
                }
            }
        }
    }
    fixture
}

fn normalize(text: &str, root: &Path) -> String {
    let mut value = text.to_owned();
    let raw = root.to_string_lossy().into_owned();
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
    roots.push(raw);
    for root in roots {
        value = value
            .replace(&root.replace('\\', "\\\\"), "$ROOT")
            .replace(&root, "$ROOT");
    }
    if cfg!(windows) {
        value = slash_root_paths(&value);
    }
    value
}

/// Rewrites the separators of every `$ROOT` path token to `/`, as Go's
/// process normalizer does on Windows. Only path tokens are touched, so JSON
/// escapes such as `\n` elsewhere in the text keep their meaning.
fn slash_root_paths(text: &str) -> String {
    let mut output = String::with_capacity(text.len());
    let mut rest = text;
    while let Some(index) = rest.find("$ROOT") {
        output.push_str(&rest[..index + "$ROOT".len()]);
        rest = &rest[index + "$ROOT".len()..];
        let bytes = rest.as_bytes();
        let mut consumed = 0;
        while consumed < bytes.len() {
            match bytes[consumed] {
                b'\\' | b'/' => {
                    output.push('/');
                    consumed += 1;
                    if consumed < bytes.len() && bytes[consumed] == b'\\' {
                        consumed += 1;
                    }
                }
                byte if byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b'-') => {
                    output.push(char::from(byte));
                    consumed += 1;
                }
                _ => break,
            }
        }
        rest = &rest[consumed..];
    }
    output.push_str(rest);
    output
}

fn run(root: &TempRoot, args: &[String]) -> ProcessResult {
    let args = args
        .iter()
        .map(|arg| arg.replace("$ROOT", &root.0.to_string_lossy()))
        .collect::<Vec<_>>();
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command.args(&args).current_dir(root.path("cwd"));
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

/// Writes seeded lifecycle rows to the sidecar the CLI opens for this vault.
/// The row key is the vault path as the CLI receives it, like a vault walk.
fn seed_lifecycle(root: &TempRoot, seeds: &[Seed]) {
    if seeds.is_empty() {
        return;
    }
    let mut sidecar =
        symdesk_index::Sidecar::open(&sidecar_path(root)).expect("open seeded sidecar");
    for seed in seeds {
        let path = root.path("vault").join(&seed.path);
        sidecar
            .set_index_status(&path.to_string_lossy(), &seed.state, &seed.reason)
            .expect("seed lifecycle row");
    }
    sidecar.close().expect("close seeded sidecar");
}

fn read_lifecycle(root: &TempRoot) -> (BTreeMap<String, String>, BTreeMap<String, String>) {
    let sidecar = Connection::open(sidecar_path(root)).expect("open sidecar");
    let mut lifecycle = BTreeMap::new();
    let mut reasons = BTreeMap::new();
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
            reasons.insert(name, normalize(&reason, &root.0));
        }
    }
    (lifecycle, reasons)
}

fn read_state(root: &TempRoot) -> State {
    let (lifecycle, lifecycle_reasons) = read_lifecycle(root);
    let retrieval_path = root.path("data/retrieval.db");
    if !retrieval_path.exists() {
        // Go opens the hybrid index only once a document is written to it.
        return State {
            documents: Vec::new(),
            chunks: Vec::new(),
            generation: 0,
            index_storage: 0,
            lifecycle,
            lifecycle_reasons,
        };
    }
    let db = Connection::open(retrieval_path).expect("open retrieval db");
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
fn index_retry_replays_go_process_fixture() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    let root = TempRoot::new();
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
        for name in &step.dirs {
            fs::create_dir_all(root.path("vault").join(name)).expect("apply step directory");
        }
        for file in &step.writes {
            fs::write(root.path("vault").join(&file.path), &file.body).expect("apply step write");
        }
        seed_lifecycle(&root, &step.seeds);
        for name in &step.removes {
            fs::remove_file(root.path("vault").join(name)).expect("apply step removal");
        }
        let result = run(&root, &step.args);
        assert_eq!(result, step.result, "step {} process output", step.id);
        assert_eq!(provider.take(), step.requests, "step {} requests", step.id);
        assert_eq!(read_state(&root), step.state, "step {} state", step.id);
    }
}

#[test]
fn root_path_separators_normalize_without_touching_json_escapes() {
    assert_eq!(
        slash_root_paths(r#"failed to parse $ROOT\vault\bad.md: x\n"#),
        "failed to parse $ROOT/vault/bad.md: x\\n"
    );
    assert_eq!(
        slash_root_paths(r#"{"path":"$ROOT\\vault\\bad.md","reason":"a\nb"}"#),
        r#"{"path":"$ROOT/vault/bad.md","reason":"a\nb"}"#
    );
}
