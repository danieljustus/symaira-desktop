use std::{
    fs,
    io::{BufRead, BufReader, Read, Write},
    net::{TcpListener, TcpStream},
    path::{Path, PathBuf},
    process::{Command, Output},
    sync::{Arc, Mutex},
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;
use serde::Deserialize;
use serde_json::{Value, json};
use symdesk_index::{RetrievalDb, RetrievalDocument, StoredRetrievalChunk};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<FixtureCase>,
}

#[derive(Clone, Deserialize)]
struct FixtureCase {
    id: String,
    #[serde(default)]
    document_path: String,
    document_body: String,
    response_status: u16,
    embedding_dim: usize,
    response_dimension: usize,
    #[serde(default)]
    transient_once: bool,
    requests: Vec<FixtureRequest>,
    pending_chunks: Vec<FixtureChunk>,
    resolved_chunks: Vec<FixtureChunk>,
    document_hash: String,
    reembedded_documents: usize,
    remaining_pending: usize,
    generation: i64,
}

#[derive(Clone, Deserialize)]
struct FixtureRequest {
    method: String,
    path: String,
    content_type: String,
    accept: String,
    body: Value,
}

#[derive(Clone, Deserialize)]
struct FixtureChunk {
    uuid: String,
    document_path: String,
    chunk_index: i64,
    content: String,
    embedding: Vec<f32>,
    hash: String,
    dim: i64,
    #[serde(rename = "embedding_model")]
    model: String,
    char_start: Option<i64>,
    char_end: Option<i64>,
    anchor_kind: String,
    anchor_value: String,
    embedding_pending: bool,
}

#[derive(Clone, Debug, PartialEq)]
struct CapturedRequest {
    method: String,
    path: String,
    content_type: String,
    accept: String,
    body: Value,
}

struct TempRoot(PathBuf);

impl TempRoot {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-reembed-http-{}-{nonce}",
            std::process::id()
        ));
        for directory in ["home", "cwd", "data", "tmp", "vault"] {
            fs::create_dir_all(root.join(directory)).expect("create isolated directory");
        }
        Self(root)
    }

    fn path(&self, name: &str) -> PathBuf {
        self.0.join(name)
    }
}

impl Drop for TempRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/retrieval/reembed-http-cli.json");
    serde_json::from_slice(&fs::read(path).expect("Go-generated HTTP re-embed fixture"))
        .expect("decode HTTP re-embed fixture")
}

fn run(root: &TempRoot, args: &[&str]) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command.args(args).current_dir(root.path("cwd"));
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
        .env("NO_COLOR", "1")
        .output()
        .expect("run Rust CLI process")
}

fn start_server(
    status: u16,
    transient_once: bool,
    response_dimension: usize,
    expected_requests: usize,
) -> (
    u16,
    Arc<Mutex<Vec<CapturedRequest>>>,
    thread::JoinHandle<()>,
) {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind local embedding server");
    listener
        .set_nonblocking(true)
        .expect("set listener nonblocking");
    let port = listener.local_addr().expect("read local address").port();
    let captured = Arc::new(Mutex::new(Vec::new()));
    let thread_capture = Arc::clone(&captured);
    let worker = thread::spawn(move || {
        let deadline = Instant::now() + Duration::from_secs(10);
        while thread_capture.lock().expect("request capture").len() < expected_requests
            && Instant::now() < deadline
        {
            let (stream, _) = match listener.accept() {
                Ok(accepted) => accepted,
                Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(5));
                    continue;
                }
                Err(error) => panic!("accept fake embedding request: {error}"),
            };
            serve_one(
                stream,
                status,
                transient_once,
                response_dimension,
                &thread_capture,
            );
        }
    });
    (port, captured, worker)
}

fn serve_one(
    mut stream: TcpStream,
    status: u16,
    transient_once: bool,
    response_dimension: usize,
    captured: &Arc<Mutex<Vec<CapturedRequest>>>,
) {
    stream
        .set_nonblocking(false)
        .expect("make accepted fake-provider stream blocking");
    stream
        .set_read_timeout(Some(Duration::from_secs(3)))
        .expect("set fake server read timeout");
    let mut reader = BufReader::new(stream.try_clone().expect("clone stream for request"));
    let mut request_line = String::new();
    reader
        .read_line(&mut request_line)
        .expect("read request line");
    let mut content_length = 0usize;
    let mut content_type = String::new();
    let mut accept = String::new();
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).expect("read request header");
        if line == "\r\n" || line.is_empty() {
            break;
        }
        if let Some((name, value)) = line.split_once(':') {
            match name.trim().to_ascii_lowercase().as_str() {
                "content-length" => content_length = value.trim().parse().expect("content length"),
                "content-type" => content_type = value.trim().to_owned(),
                "accept" => accept = value.trim().to_owned(),
                _ => {}
            }
        }
    }
    let mut body = vec![0; content_length];
    reader.read_exact(&mut body).expect("read request body");
    let body: Value = serde_json::from_slice(&body).expect("decode request body");
    let mut line_parts = request_line.split_whitespace();
    let method = line_parts.next().unwrap_or_default().to_owned();
    let path = line_parts.next().unwrap_or_default().to_owned();
    let request_index = {
        let mut captured = captured.lock().expect("request capture");
        let index = captured.len();
        captured.push(CapturedRequest {
            method,
            path,
            content_type,
            accept,
            body: body.clone(),
        });
        index
    };
    let status = if transient_once && request_index == 0 {
        503
    } else {
        status
    };
    let body = if status == 404 {
        json!({"error":"model not found"}).to_string()
    } else if status >= 500 {
        json!({"error":"temporary local failure"}).to_string()
    } else {
        let count = body["input"].as_array().map_or(0, Vec::len);
        let dimension = if response_dimension == 0 {
            3
        } else {
            response_dimension
        };
        let data = (0..count)
            .map(|_| {
                json!({
                    "embedding": (0..dimension)
                        .map(|index| [0.25, -0.5, 1.0][index % 3])
                        .collect::<Vec<_>>()
                })
            })
            .collect::<Vec<_>>();
        json!({"data":data}).to_string()
    };
    let reason = match status {
        404 => "Not Found",
        503 => "Service Unavailable",
        _ => "OK",
    };
    write!(
        stream,
        "HTTP/1.1 {status} {reason}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
        body.len()
    )
    .expect("write fake embedding response");
}

fn stored(path: &str, chunk: &FixtureChunk) -> StoredRetrievalChunk {
    StoredRetrievalChunk {
        id: 0,
        uuid: chunk.uuid.clone(),
        document_path: path.to_owned(),
        chunk_index: chunk.chunk_index,
        content: chunk.content.clone(),
        embedding: chunk.embedding.clone(),
        hash: chunk.hash.clone(),
        norm: 0.0,
        dim: chunk.dim,
        model: chunk.model.clone(),
        char_start: chunk.char_start,
        char_end: chunk.char_end,
        anchor_kind: chunk.anchor_kind.clone(),
        anchor_value: chunk.anchor_value.clone(),
        embedding_pending: chunk.embedding_pending,
    }
}

#[test]
fn reembed_cli_replays_go_http_success_and_failure_cases() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 6);
    for case in fixture.cases {
        let root = TempRoot::new();
        let document_path = if case.document_path.is_empty() {
            "doc.md"
        } else {
            case.document_path.as_str()
        };
        fs::write(root.path("cwd").join(document_path), &case.document_body)
            .expect("write source document");
        let config_dir = root.path("home/.config/symseek");
        fs::create_dir_all(&config_dir).expect("create existing symseek config directory");
        let retrieval_path = root.path("data/retrieval.db");
        let (port, requests, server) = start_server(
            case.response_status,
            case.transient_once,
            case.response_dimension,
            case.requests.len(),
        );
        fs::write(
            config_dir.join("config.toml"),
            format!(
                "index_path = {:?}\nollama_url = \"http://localhost:{port}/api/embeddings\"\nmodel = \"fixture-model\"\nembedding_dim = {}\ntimeout_seconds = 2\nretry_count = 1\nretry_backoff_ms = 1\n",
                retrieval_path.to_string_lossy(), case.embedding_dim
            ),
        )
        .expect("configure local fake provider");
        let database = RetrievalDb::open_at(&retrieval_path).expect("create legacy retrieval DB");
        database
            .save_document(&RetrievalDocument {
                path: document_path.to_owned(),
                hash: "old-pending-hash".to_owned(),
                updated_at: "2026-09-01T00:00:00Z".to_owned(),
            })
            .expect("seed pending document");
        let chunks = case
            .pending_chunks
            .iter()
            .map(|chunk| stored(document_path, chunk))
            .collect::<Vec<_>>();
        database.save_chunks(&chunks).expect("seed pending chunks");
        drop(database);

        let vault = root.path("vault").to_string_lossy().into_owned();
        let output =
            if case.remaining_pending == 0 && (case.transient_once || case.embedding_dim == 0) {
                run(
                    &root,
                    &["--output=json", "--vault", &vault, "index", "--re-embed"],
                )
            } else if case.remaining_pending == 0 {
                run(&root, &["--vault", &vault, "index", "--re-embed"])
            } else {
                run(&root, &["--json", "--vault", &vault, "index", "--re-embed"])
            };
        server.join().expect("fake HTTP server finishes");
        assert_eq!(output.status.code(), Some(0), "{}", case.id);
        let stdout = String::from_utf8_lossy(&output.stdout);
        let stderr = String::from_utf8_lossy(&output.stderr);
        if case.remaining_pending == 0 {
            if case.transient_once || case.embedding_dim == 0 {
                let json_output: Value = serde_json::from_slice(&output.stdout)
                    .expect("--output=json emits only structured build output");
                assert_eq!(json_output["status"], "ok", "{} status", case.id);
                assert_eq!(
                    json_output["reembedded_documents"], case.reembedded_documents,
                    "{} resolved documents",
                    case.id
                );
                assert!(
                    !stdout.contains("Re-embedded "),
                    "{} progress must not corrupt JSON output: {stdout}",
                    case.id
                );
            } else {
                assert!(
                    stdout.contains(&format!(
                        "Re-embedded {} document(s) with pending chunks.",
                        case.reembedded_documents
                    )),
                    "{} stdout: {stdout}",
                    case.id
                );
            }
        } else {
            let json_output: Value = serde_json::from_slice(&output.stdout)
                .expect("provider failure returns an explicit JSON result");
            assert!(
                !stdout.contains("Re-embedded "),
                "{} must not report pending provider failures as resolved: {stdout}",
                case.id
            );
            assert_eq!(json_output["status"], "incomplete", "{} status", case.id);
            assert_eq!(
                json_output["reembedded_documents"], 0,
                "{} resolved",
                case.id
            );
            assert_eq!(
                json_output["reembed_pending_documents"], 1,
                "{} remaining documents",
                case.id
            );
            if case.response_status == 404 {
                assert!(
                    stderr.contains("model_not_found"),
                    "{} stderr: {stderr}",
                    case.id
                );
            }
        }
        let actual_requests = requests.lock().expect("request capture").clone();
        let expected_requests = case
            .requests
            .iter()
            .map(|request| CapturedRequest {
                method: request.method.clone(),
                path: request.path.clone(),
                content_type: request.content_type.clone(),
                accept: request.accept.clone(),
                body: request.body.clone(),
            })
            .collect::<Vec<_>>();
        assert_eq!(actual_requests, expected_requests, "{} requests", case.id);

        let database = Connection::open(&retrieval_path).expect("open rebuilt retrieval DB");
        let mut statement = database
            .prepare(
                "SELECT uuid, chunk_index, content, embedding, hash, embedding_dim, embedding_model,
                        char_start, char_end, anchor_kind, anchor_value, embedding_pending
                 FROM chunks WHERE document_path = ? ORDER BY chunk_index",
            )
            .expect("prepare rebuilt chunks");
        let actual = statement
            .query_map([document_path], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, i64>(1)?,
                    row.get::<_, String>(2)?,
                    row.get::<_, Vec<u8>>(3)?,
                    row.get::<_, String>(4)?,
                    row.get::<_, i64>(5)?,
                    row.get::<_, String>(6)?,
                    row.get::<_, Option<i64>>(7)?,
                    row.get::<_, Option<i64>>(8)?,
                    row.get::<_, Option<String>>(9)?.unwrap_or_default(),
                    row.get::<_, Option<String>>(10)?.unwrap_or_default(),
                    row.get::<_, i64>(11)? != 0,
                ))
            })
            .expect("query rebuilt chunks")
            .collect::<Result<Vec<_>, _>>()
            .expect("read rebuilt chunks");
        let expected = &case.resolved_chunks;
        assert_eq!(actual.len(), expected.len(), "{} chunks", case.id);
        for (actual, expected) in actual.iter().zip(expected) {
            assert_eq!(expected.document_path, "$DOC", "{} source marker", case.id);
            let expected_embedding = expected
                .embedding
                .iter()
                .flat_map(|value| value.to_le_bytes())
                .collect::<Vec<_>>();
            assert_eq!(actual.0, expected.uuid, "{} uuid", case.id);
            assert_eq!(actual.1, expected.chunk_index, "{} chunk index", case.id);
            assert_eq!(actual.2, expected.content, "{} content", case.id);
            assert_eq!(actual.3, expected_embedding, "{} embedding", case.id);
            assert_eq!(actual.4, expected.hash, "{} hash", case.id);
            assert_eq!(actual.5, expected.dim, "{} dim", case.id);
            assert_eq!(actual.6, expected.model, "{} model", case.id);
            assert_eq!(actual.7, expected.char_start, "{} start", case.id);
            assert_eq!(actual.8, expected.char_end, "{} end", case.id);
            assert_eq!(actual.9, expected.anchor_kind, "{} anchor kind", case.id);
            assert_eq!(actual.10, expected.anchor_value, "{} anchor", case.id);
            assert_eq!(actual.11, expected.embedding_pending, "{} pending", case.id);
        }
        let hash: String = database
            .query_row(
                "SELECT hash FROM documents WHERE path=?",
                [document_path],
                |row| row.get(0),
            )
            .expect("read rebuilt document hash");
        assert_eq!(hash, case.document_hash, "{} document hash", case.id);
        let pending: i64 = database
            .query_row(
                "SELECT COUNT(*) FROM chunks WHERE embedding_pending=1",
                [],
                |row| row.get(0),
            )
            .expect("count remaining pending chunks");
        assert_eq!(
            pending as usize, case.remaining_pending,
            "{} pending",
            case.id
        );
        let generation: i64 = database
            .query_row(
                "SELECT value FROM index_meta WHERE key='generation'",
                [],
                |row| row.get(0),
            )
            .expect("read generation");
        assert_eq!(generation, case.generation, "{} generation", case.id);
        let old_fts: i64 = database
            .query_row(
                "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'old'",
                [],
                |row| row.get(0),
            )
            .expect("search removed pending text");
        assert_eq!(old_fts, 0, "{} old FTS", case.id);
        let new_fts: i64 = database
            .query_row(
                "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'contract'",
                [],
                |row| row.get(0),
            )
            .expect("search rebuilt text");
        assert!(new_fts > 0, "{} new FTS hit", case.id);
    }
}
