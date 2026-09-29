#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    io::{Read, Write},
    net::{TcpListener, TcpStream},
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::{Arc, Mutex, mpsc},
    thread,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use serde_json::{Value, json};
use symdesk_index::{
    IndexedDocument, RetrievalDb, RetrievalDocument, Sidecar, SourceRegistry, StoredRetrievalChunk,
    materialize_chunks, parse_markdown_retrieval_sections,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<FixtureCase>,
}

#[derive(Clone, Deserialize)]
struct FixtureCase {
    id: String,
    query: String,
    embedding_dim: usize,
    provider_status: u16,
    provider_dimension: usize,
    #[serde(default)]
    expand_query: bool,
    #[serde(default)]
    rerank_query: bool,
    #[serde(default)]
    vector_backend: String,
    #[serde(default)]
    vector_quantization: String,
    #[serde(default)]
    expand_model: String,
    #[serde(default)]
    expanded_text: String,
    #[serde(default)]
    chat_response: String,
    #[serde(default)]
    chat_error_body: String,
    #[serde(default)]
    chat_status: u16,
    documents: Vec<FixtureDocument>,
    #[serde(default)]
    external_documents: Vec<FixtureDocument>,
    #[serde(default)]
    unregistered_documents: Vec<FixtureDocument>,
    requests: Vec<FixtureRequest>,
    expected: Value,
}

#[derive(Clone, Deserialize)]
struct FixtureDocument {
    path: String,
    body: String,
}

#[derive(Clone, Debug, PartialEq, Deserialize)]
struct FixtureRequest {
    method: String,
    path: String,
    body: Value,
}

struct TestRoot(PathBuf);
type RunningEmbeddingServer = (
    String,
    Arc<Mutex<Vec<FixtureRequest>>>,
    mpsc::Sender<()>,
    thread::JoinHandle<()>,
);

impl TestRoot {
    fn new() -> Self {
        let root = std::env::temp_dir().join(format!(
            "symdesk-mcp-search-{}-{}",
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .expect("system clock")
                .as_nanos()
        ));
        for name in [
            "home",
            "home/config",
            "home/cache",
            "vault",
            "tmp",
            "data",
            "cwd",
        ] {
            fs::create_dir_all(root.join(name)).expect("create private test directory");
        }
        #[cfg(unix)]
        fs::set_permissions(&root, std::os::unix::fs::PermissionsExt::from_mode(0o700))
            .expect("restrict fixture root permissions");
        Self(root)
    }

    fn path(&self, child: &str) -> PathBuf {
        self.0.join(child)
    }
}

impl Drop for TestRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

#[test]
fn real_mcp_search_replays_go_handler_envelope() {
    let path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/mcp/search-hybrid.json");
    let fixture: Fixture =
        serde_json::from_slice(&fs::read(path).expect("Go-generated MCP fixture"))
            .expect("valid Go MCP fixture");
    assert_eq!(fixture.schema_version, 1);
    assert!(fixture.cases.len() >= 6);
    for case in fixture.cases {
        replay_case(&case);
    }
}

fn replay_case(case: &FixtureCase) {
    let root = TestRoot::new();
    let vault = root.path("vault");
    let home = root.path("home");
    let index_path = root.path("data/retrieval.db");
    let sidecar_path = root.path("data/sidecar.db");
    let external_root = root.path("external");
    let unregistered_root = root.path("unregistered");
    let (endpoint, captured, stop, server) = start_embedding_server(case);

    let mut documents = Vec::new();
    for document in &case.documents {
        let path = vault.join(&document.path);
        fs::create_dir_all(path.parent().expect("document parent")).expect("create document dir");
        fs::write(&path, &document.body).expect("write fixture document");
        documents.push((path, document));
    }
    let mut external_documents = Vec::new();
    for document in &case.external_documents {
        let path = external_root.join(&document.path);
        fs::create_dir_all(path.parent().expect("external document parent"))
            .expect("create external document dir");
        fs::write(&path, &document.body).expect("write external fixture document");
        external_documents.push((path, document));
    }
    let mut unregistered_documents = Vec::new();
    for document in &case.unregistered_documents {
        let path = unregistered_root.join(&document.path);
        fs::create_dir_all(path.parent().expect("unregistered document parent"))
            .expect("create unregistered document dir");
        fs::write(&path, &document.body).expect("write unregistered fixture document");
        unregistered_documents.push((path, document));
    }

    let mut sidecar = Sidecar::open(&sidecar_path).expect("open sidecar");
    sidecar
        .refresh_index_for_cli(&vault)
        .expect("refresh sidecar");
    if !external_documents.is_empty() {
        SourceRegistry::open(&vault)
            .expect("open source registry")
            .add(&external_root)
            .expect("register fixture source");
        sidecar
            .refresh_external_source(&external_root)
            .expect("index registered external source");
    }
    if !unregistered_documents.is_empty() {
        sidecar
            .refresh_external_source(&unregistered_root)
            .expect("index unregistered boundary control");
    }
    if case.id == "scoped-path-and-negative-term" {
        index_outside_candidate(&mut sidecar, &root);
    }
    drop(sidecar);

    let index = RetrievalDb::open_at(&index_path).expect("open retrieval database");
    for (path, document) in &documents {
        let path = path.canonicalize().expect("canonical document path");
        let path_text = path.to_string_lossy().into_owned();
        let sections = parse_markdown_retrieval_sections(&path_text, document.body.as_bytes())
            .expect("parse fixture Markdown");
        let chunks = materialize_chunks(&path_text, &sections);
        index
            .save_document(&RetrievalDocument {
                path: path_text.clone(),
                hash: symdesk_vault::sha256_hex(document.body.as_bytes()),
                updated_at: "2026-09-29T00:00:00Z".to_owned(),
            })
            .expect("save retrieval document");
        let stored = chunks
            .into_iter()
            .map(|chunk| StoredRetrievalChunk {
                id: 0,
                uuid: chunk.uuid,
                document_path: path_text.clone(),
                chunk_index: chunk.chunk_index as i64,
                content: chunk.content,
                embedding: vec![1.0; case.embedding_dim],
                hash: chunk.hash,
                norm: (case.embedding_dim as f32).sqrt(),
                dim: case.embedding_dim as i64,
                model: "fixture-model".to_owned(),
                char_start: chunk.char_start.map(|value| value as i64),
                char_end: chunk.char_end.map(|value| value as i64),
                anchor_kind: chunk.anchor_kind,
                anchor_value: chunk.anchor_value,
                embedding_pending: false,
            })
            .collect::<Vec<_>>();
        index.save_chunks(&stored).expect("save retrieval chunks");
    }
    for (path, document) in external_documents.iter().chain(&unregistered_documents) {
        let path = path
            .canonicalize()
            .expect("canonical external document path");
        let path_text = path.to_string_lossy().into_owned();
        let sections = parse_markdown_retrieval_sections(&path_text, document.body.as_bytes())
            .expect("parse external fixture Markdown");
        let chunks = materialize_chunks(&path_text, &sections);
        index
            .save_document(&RetrievalDocument {
                path: path_text.clone(),
                hash: symdesk_vault::sha256_hex(document.body.as_bytes()),
                updated_at: "2026-09-29T00:00:00Z".to_owned(),
            })
            .expect("save external retrieval document");
        let stored = chunks
            .into_iter()
            .map(|chunk| StoredRetrievalChunk {
                id: 0,
                uuid: chunk.uuid,
                document_path: path_text.clone(),
                chunk_index: chunk.chunk_index as i64,
                content: chunk.content,
                embedding: vec![1.0; case.embedding_dim],
                hash: chunk.hash,
                norm: (case.embedding_dim as f32).sqrt(),
                dim: case.embedding_dim as i64,
                model: "fixture-model".to_owned(),
                char_start: chunk.char_start.map(|value| value as i64),
                char_end: chunk.char_end.map(|value| value as i64),
                anchor_kind: chunk.anchor_kind,
                anchor_value: chunk.anchor_value,
                embedding_pending: false,
            })
            .collect::<Vec<_>>();
        index
            .save_chunks(&stored)
            .expect("save external retrieval chunks");
    }
    drop(index);

    let config_dir = home.join(".config/symseek");
    fs::create_dir_all(&config_dir).expect("create isolated config");
    let vector_backend = if case.vector_backend.is_empty() {
        "sqlite"
    } else {
        &case.vector_backend
    };
    let vector_quantization = if case.vector_quantization.is_empty() {
        "off"
    } else {
        &case.vector_quantization
    };
    let config = format!(
        "index_path = {:?}\nollama_url = {:?}\nmodel = \"fixture-model\"\nembedding_dim = {}\ntimeout_seconds = 2\nretry_count = 0\nvector_backend = {:?}\nvector_quantization = {:?}\nexpand_query = {}\nexpand_model = {:?}\nexpand_timeout_seconds = 5\nrerank_query = {}\n",
        index_path.to_string_lossy(),
        endpoint,
        case.embedding_dim,
        vector_backend,
        vector_quantization,
        case.expand_query,
        case.expand_model,
        case.rerank_query
    );
    fs::write(config_dir.join("config.toml"), config).expect("write isolated config");

    let request = json!({
        "jsonrpc": "2.0", "id": 1, "method": "tools/call",
        "params": {"name": "desk_search", "arguments": {"query": case.query}}
    });
    let mut child = Command::new(env!("CARGO_BIN_EXE_symdesk"))
        .args(["mcp"])
        .env_clear()
        .env("PATH", std::env::var("PATH").unwrap_or_default())
        .env("HOME", &home)
        .env("USERPROFILE", &home)
        .env("XDG_CONFIG_HOME", root.path("home/config"))
        .env("XDG_CACHE_HOME", root.path("home/cache"))
        .env("XDG_DATA_HOME", root.path("data"))
        .env("TMPDIR", root.path("tmp"))
        .env("TMP", root.path("tmp"))
        .env("TEMP", root.path("tmp"))
        .env("SYMDESK_VAULT", &vault)
        .env("SYMDESK_SIDECAR", &sidecar_path)
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC")
        .env(
            "SYSTEMROOT",
            std::env::var("SYSTEMROOT").unwrap_or_default(),
        )
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .current_dir(root.path("cwd"))
        .spawn()
        .expect("launch symdesk MCP");
    let input = format!("{request}\n");
    child
        .stdin
        .take()
        .expect("stdin")
        .write_all(input.as_bytes())
        .expect("send tools/call");
    let output = child.wait_with_output().expect("wait for symdesk MCP");
    let _ = stop.send(());
    server.join().expect("join fake provider");
    assert!(
        output.status.success(),
        "{} stderr: {}",
        case.id,
        String::from_utf8_lossy(&output.stderr)
    );
    if case.chat_status >= 300 {
        let prefix = case.chat_error_body.chars().take(512).collect::<String>();
        let expected_error = format!(
            "engine: HyDE expansion failed (hyde expansion failed: ollama returned HTTP {}: {prefix})",
            case.chat_status
        );
        let stderr = String::from_utf8_lossy(&output.stderr);
        assert!(
            stderr.contains(&expected_error),
            "{} stderr: {stderr}",
            case.id
        );
        assert!(
            !stderr.contains("TAIL_MARKER"),
            "{} included bytes beyond Go's 512-byte error prefix",
            case.id
        );
    }

    let mut actual_requests = captured.lock().expect("request capture").clone();
    let mut expected_requests = case.requests.clone();
    actual_requests.sort_by(|left, right| left.path.cmp(&right.path));
    expected_requests.sort_by(|left, right| left.path.cmp(&right.path));
    assert_eq!(
        actual_requests, expected_requests,
        "{} provider requests",
        case.id
    );

    let actual: Value = serde_json::from_slice(&output.stdout).unwrap_or_else(|error| {
        panic!(
            "{} MCP stdout is not JSON: {error}: {}",
            case.id,
            String::from_utf8_lossy(&output.stdout)
        )
    });
    let actual = replace_root(actual, &external_root, "$EXTERNAL");
    let actual = replace_root(actual, &unregistered_root, "$UNREGISTERED");
    let expected = replace_vault(case.expected.clone(), &vault);
    let expected = replace_root(expected, &external_root, "$EXTERNAL");
    let expected = replace_root(expected, &unregistered_root, "$UNREGISTERED");
    assert_eq!(
        normalize_response(actual),
        normalize_response(expected),
        "{} Go MCP response",
        case.id
    );
}

fn index_outside_candidate(sidecar: &mut Sidecar, root: &TestRoot) {
    let path = root.path("outside/projects/escape.md");
    fs::create_dir_all(path.parent().expect("outside parent")).expect("create outside parent");
    fs::write(
        &path,
        "# Outside\n\nAn outside needle must stay out of this vault.",
    )
    .expect("write outside indexed source");
    let path = path.canonicalize().expect("canonical outside path");
    let body = "# Outside\n\nAn outside needle must stay out of this vault.";
    sidecar
        .index_document(&IndexedDocument {
            path: path.to_string_lossy().into_owned(),
            sha256: symdesk_vault::sha256_hex(body.as_bytes()),
            title: "Outside".to_owned(),
            body: body.to_owned(),
            created_at: "2026-09-29T00:00:00Z".to_owned(),
            modified_at: "2026-09-29T00:00:00Z".to_owned(),
            document_type: "note".to_owned(),
            document_date: None,
            person: None,
            status: None,
            due_date: None,
            confidence: None,
            ocr_json_path: None,
            simhash: None,
            asn: None,
            size: Some(body.len() as i64),
            mtime_ns: None,
            properties: BTreeMap::new(),
            links: Vec::new(),
            derived: false,
        })
        .expect("index outside path as a boundary control");
}

fn start_embedding_server(case: &FixtureCase) -> RunningEmbeddingServer {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind local fake provider");
    listener
        .set_nonblocking(true)
        .expect("nonblocking listener");
    let endpoint = format!(
        "http://{}/api/embeddings",
        listener.local_addr().expect("listener address")
    );
    let captured = Arc::new(Mutex::new(Vec::new()));
    let capture = Arc::clone(&captured);
    let (stop, stopped) = mpsc::channel();
    let case = case.clone();
    let server = thread::spawn(move || {
        loop {
            if stopped.try_recv().is_ok() {
                break;
            }
            match listener.accept() {
                Ok((mut stream, _)) => {
                    let _ = stream.set_nonblocking(false);
                    let _ = stream.set_read_timeout(Some(Duration::from_secs(2)));
                    let request = read_request(&mut stream);
                    if let Some((method, path, body)) = request {
                        let is_chat = path == "/api/chat";
                        if let Ok(mut requests) = capture.lock() {
                            requests.push(FixtureRequest {
                                method,
                                path,
                                body: body.clone(),
                            });
                        }
                        let (status, response) = if is_chat {
                            let status = if case.chat_status == 0 {
                                200
                            } else {
                                case.chat_status
                            };
                            let response = if status >= 400 {
                                json!({"error":"fixture chat failure"})
                            } else {
                                json!({"message":{"content":case.expanded_text}})
                            };
                            let response_body = if case.chat_response.is_empty() {
                                serde_json::to_vec(&response).expect("encode chat response")
                            } else {
                                case.chat_response.as_bytes().to_vec()
                            };
                            let response_body = if status >= 400 && !case.chat_error_body.is_empty()
                            {
                                case.chat_error_body.as_bytes().to_vec()
                            } else {
                                response_body
                            };
                            (status, response_body)
                        } else {
                            let input = body["input"]
                                .as_array()
                                .and_then(|items| items.first())
                                .and_then(Value::as_str)
                                .unwrap_or_default();
                            let (status, dimension) = if input == case.query {
                                (case.provider_status, case.provider_dimension)
                            } else {
                                (200, case.embedding_dim)
                            };
                            let mut vector = vec![0.0_f32; dimension];
                            if input == case.expanded_text && vector.len() > 1 {
                                vector[1] = 1.0;
                            } else if let Some(first) = vector.first_mut() {
                                *first = 1.0;
                            }
                            (
                                status,
                                serde_json::to_vec(&json!({"data":[{"embedding":vector}]}))
                                    .expect("encode embedding response"),
                            )
                        };
                        let reason = if status >= 400 {
                            "Service Unavailable"
                        } else {
                            "OK"
                        };
                        let header = format!(
                            "HTTP/1.1 {status} {reason}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                            response.len()
                        );
                        let _ = stream.write_all(header.as_bytes());
                        let _ = stream.write_all(&response);
                    }
                }
                Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(5))
                }
                Err(_) => break,
            }
        }
    });
    (endpoint, captured, stop, server)
}

fn read_request(stream: &mut TcpStream) -> Option<(String, String, Value)> {
    let mut bytes = Vec::new();
    let mut buffer = [0; 2048];
    let header_end = loop {
        let count = stream.read(&mut buffer).ok()?;
        if count == 0 {
            return None;
        }
        bytes.extend_from_slice(&buffer[..count]);
        if let Some(index) = bytes.windows(4).position(|window| window == b"\r\n\r\n") {
            break index + 4;
        }
    };
    let headers = String::from_utf8_lossy(&bytes[..header_end]).into_owned();
    let content_length = headers
        .lines()
        .find_map(|line| {
            line.to_ascii_lowercase()
                .strip_prefix("content-length:")
                .and_then(|value| value.trim().parse::<usize>().ok())
        })
        .unwrap_or(0);
    while bytes.len() < header_end + content_length {
        let count = stream.read(&mut buffer).ok()?;
        if count == 0 {
            return None;
        }
        bytes.extend_from_slice(&buffer[..count]);
    }
    let first = headers.lines().next()?;
    let mut parts = first.split_whitespace();
    let method = parts.next()?.to_owned();
    let path = parts.next()?.to_owned();
    let body = serde_json::from_slice(&bytes[header_end..header_end + content_length]).ok()?;
    Some((method, path, body))
}

fn replace_vault(value: Value, vault: &Path) -> Value {
    replace_root(value, vault, "$VAULT")
}

fn replace_root(value: Value, root: &Path, token: &str) -> Value {
    match value {
        Value::String(text) => {
            Value::String(text.replace(&root.to_string_lossy().to_string(), token))
        }
        Value::Array(items) => Value::Array(
            items
                .into_iter()
                .map(|item| replace_root(item, root, token))
                .collect(),
        ),
        Value::Object(items) => Value::Object(
            items
                .into_iter()
                .map(|(key, item)| (key, replace_root(item, root, token)))
                .collect(),
        ),
        other => other,
    }
}

fn canonicalize(value: Value) -> Value {
    match value {
        Value::Array(items) => Value::Array(items.into_iter().map(canonicalize).collect()),
        Value::Object(items) => Value::Object(
            items
                .into_iter()
                .map(|(key, item)| (key, canonicalize(item)))
                .collect(),
        ),
        other => other,
    }
}

fn normalize_response(mut response: Value) -> Value {
    let Some(content) = response.pointer_mut("/result/content/0/text") else {
        return canonicalize(response);
    };
    let Some(text) = content.as_str() else {
        return canonicalize(response);
    };
    let Ok(mut result) = serde_json::from_str::<Value>(text) else {
        return canonicalize(response);
    };
    if let Some(items) = result.get_mut("results").and_then(Value::as_array_mut) {
        for item in items {
            if let Some(score) = item.get("score").and_then(Value::as_f64)
                && let Some(number) = serde_json::Number::from_f64(score)
            {
                item["score"] = Value::Number(number);
            }
        }
    }
    *content =
        Value::String(serde_json::to_string(&canonicalize(result)).expect("serialize MCP payload"));
    canonicalize(response)
}
