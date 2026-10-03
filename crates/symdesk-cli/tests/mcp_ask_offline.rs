#![deny(unsafe_code)]

use std::{
    fs,
    io::{BufRead, Read, Write},
    net::{TcpListener, TcpStream},
    path::PathBuf,
    process::{Command, Stdio},
    sync::{Arc, Mutex, mpsc},
    thread,
    time::Duration,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use serde_json::{Value, json};
use symdesk_index::{
    RetrievalDb, RetrievalDocument, Sidecar, StoredRetrievalChunk, materialize_chunks,
    parse_markdown_retrieval_sections,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<FixtureCase>,
}

#[derive(Clone, Deserialize)]
struct FixtureCase {
    id: String,
    #[serde(default)]
    embedding_dim: usize,
    documents: Vec<FixtureDocument>,
    expected_tool: Value,
    calls: Vec<FixtureCall>,
    #[serde(default)]
    provider_requests: Vec<FixtureRequest>,
}

#[derive(Clone, Deserialize)]
struct FixtureCall {
    id: String,
    #[serde(default)]
    tool: String,
    arguments_json: String,
    #[serde(default)]
    raw_params_json: String,
    #[serde(default)]
    raw_frame_json: String,
    expected: Value,
}

#[derive(Clone, Deserialize)]
struct FixtureDocument {
    path: String,
    body: String,
    #[serde(default)]
    embedding_marker: String,
    #[serde(default)]
    embedding: Vec<f32>,
}

#[derive(Clone, Debug, PartialEq, Deserialize)]
struct FixtureRequest {
    method: String,
    path: String,
    body: Value,
}

struct RunningAskEmbeddingServer {
    endpoint: String,
    captured: Arc<Mutex<Vec<FixtureRequest>>>,
    stop: mpsc::Sender<()>,
    server: Option<thread::JoinHandle<()>>,
}

impl Drop for RunningAskEmbeddingServer {
    fn drop(&mut self) {
        let _ = self.stop.send(());
        if let Some(server) = self.server.take() {
            let _ = server.join();
        }
    }
}

struct TestRoot(PathBuf);

impl TestRoot {
    fn new(id: &str) -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-mcp-ask-{id}-{}-{nonce}",
            std::process::id()
        ));
        for child in ["home/config", "home/cache", "data", "tmp", "cwd", "vault"] {
            fs::create_dir_all(root.join(child)).expect("create isolated test directory");
        }
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
fn real_mcp_ask_replays_go_handler_envelope() {
    replay_fixture(false);
}

#[cfg(unix)]
#[test]
fn real_mcp_ask_replays_go_envelope_through_symlinked_vault_root() {
    replay_fixture(true);
}

fn replay_fixture(alias_vault: bool) {
    let fixture_path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/mcp/ask-offline.json");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(fixture_path).expect("Go-generated offline Ask MCP fixture"),
    )
    .expect("decode offline Ask MCP fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 3);
    for case in &fixture.cases {
        replay_case(case, alias_vault);
    }
}

fn replay_case(case: &FixtureCase, alias_vault: bool) {
    let root = TestRoot::new(&case.id);
    let vault = root.path("vault").canonicalize().expect("canonical vault");
    #[cfg(unix)]
    let vault = if alias_vault {
        let alias = root.path("vault-alias");
        std::os::unix::fs::symlink(&vault, &alias).expect("create vault root alias");
        alias
    } else {
        vault
    };
    #[cfg(not(unix))]
    let _ = alias_vault;
    for document in &case.documents {
        let path = vault.join(&document.path);
        fs::create_dir_all(path.parent().expect("document parent"))
            .expect("create document parent");
        fs::write(path, &document.body).expect("write source-controlled test document");
    }

    let sidecar_path = root.path("data/sidecar.db");
    let mut sidecar = Sidecar::open(&sidecar_path).expect("open isolated sidecar");
    sidecar
        .refresh_index_for_cli(&vault)
        .expect("index test vault for scoped FTS");
    drop(sidecar);

    let embedding_server = if case.embedding_dim > 0 {
        let server = start_ask_embedding_server(case);
        let index_path = root.path("data/retrieval.db");
        seed_ask_retrieval_index(case, &vault, &index_path);
        let config_dir = root.path("home/.config/symseek");
        fs::create_dir_all(&config_dir).expect("create isolated retrieval config directory");
        fs::write(
            config_dir.join("config.toml"),
            format!(
                "index_path = {:?}\nollama_url = {:?}\nmodel = \"fixture-model\"\nembedding_dim = {}\ntimeout_seconds = 2\nretry_count = 0\n",
                index_path.to_string_lossy(),
                server.endpoint,
                case.embedding_dim
            ),
        )
        .expect("write isolated retrieval config");
        Some(server)
    } else {
        None
    };

    let mut requests = vec![r#"{"jsonrpc":"2.0","id":1,"method":"tools/list"}"#.to_owned()];
    for (index, call) in case.calls.iter().enumerate() {
        requests.push(call_fixture_request(index + 2, call));
    }
    let frames = run_mcp(&root, &vault, &sidecar_path, &requests, None);
    assert_eq!(
        frames.len(),
        case.calls.len() + 1,
        "{} returned unexpected MCP frames",
        case.id
    );
    let actual_provider_requests = embedding_server.as_ref().map_or_else(Vec::new, |server| {
        server.captured.lock().expect("capture lock").clone()
    });
    assert_eq!(
        actual_provider_requests, case.provider_requests,
        "{} actual MCP Ask embedding requests differ from Go",
        case.id
    );
    let by_id = frames
        .iter()
        .filter_map(|frame| frame["id"].as_u64().map(|id| (id, frame)))
        .collect::<std::collections::BTreeMap<_, _>>();
    let list_response = by_id.get(&1).expect("tools/list response id");
    let tools = list_response["result"]["tools"]
        .as_array()
        .expect("tools/list array");
    let ask_tool = tools
        .iter()
        .find(|tool| tool["name"] == "desk_ask")
        .expect("desk_ask catalog entry");
    assert_eq!(
        normalize_value(ask_tool.clone(), &vault),
        case.expected_tool
    );
    for (index, call) in case.calls.iter().enumerate() {
        let response = normalize_value(
            (*by_id
                .get(&((index + 2) as u64))
                .expect("tools/call response id"))
            .clone(),
            &vault,
        );
        assert_eq!(response, call.expected, "{}", call.id);
    }

    if case.id == "notebook-scoped-offline-search" {
        let registry_path = vault.join(".symdesk/search-sources.json");
        fs::create_dir_all(registry_path.parent().expect("registry parent"))
            .expect("create source registry directory");
        fs::write(&registry_path, b"not valid JSON")
            .expect("write invalid unrelated external-source registry");
        let call = &case.calls[0];
        let actual = run_mcp(
            &root,
            &vault,
            &sidecar_path,
            &[call_request(101, &call.arguments_json)],
            None,
        );
        let actual_result = normalize_value(actual[0]["result"].clone(), &vault);
        assert_eq!(actual_result, call.expected["result"]);
    }

    let configured_provider_frames = run_mcp(
        &root,
        &vault,
        &sidecar_path,
        &[call_request(101, r#"{"query":"tag:askscope"}"#)],
        Some("llm_provider = \"openai\"\n"),
    );
    assert!(configured_provider_frames[0]["result"]["isError"] == true);
    assert_eq!(
        configured_provider_frames[0]["result"]["content"][0]["text"],
        "Rust ask provider \"openai\" is not implemented"
    );
}

fn run_mcp(
    root: &TestRoot,
    vault: &std::path::Path,
    sidecar: &std::path::Path,
    requests: &[String],
    config: Option<&str>,
) -> Vec<Value> {
    let config_dir = root.path("home/config/symdesk");
    fs::create_dir_all(&config_dir).expect("create private config directory");
    if let Some(config) = config {
        fs::write(config_dir.join("config.toml"), config).expect("write controlled AI config");
    }
    let mut child = Command::new(env!("CARGO_BIN_EXE_symdesk"))
        .arg("mcp")
        .env_clear()
        .env("PATH", std::env::var("PATH").unwrap_or_default())
        .env(
            "SYSTEMROOT",
            std::env::var("SYSTEMROOT").unwrap_or_default(),
        )
        .env("HOME", root.path("home"))
        .env("USERPROFILE", root.path("home"))
        .env("XDG_CONFIG_HOME", root.path("home/config"))
        .env("XDG_CACHE_HOME", root.path("home/cache"))
        .env("XDG_DATA_HOME", root.path("data"))
        .env("TMPDIR", root.path("tmp"))
        .env("TMP", root.path("tmp"))
        .env("TEMP", root.path("tmp"))
        .env("SYMDESK_VAULT", vault)
        .env("SYMDESK_SIDECAR", sidecar)
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC")
        .current_dir(root.path("cwd"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("launch symdesk MCP process");
    let input = requests.join("\n") + "\n";
    child
        .stdin
        .take()
        .expect("MCP stdin")
        .write_all(input.as_bytes())
        .expect("send MCP requests");
    let output = child.wait_with_output().expect("wait for MCP process");
    assert!(
        output.status.success(),
        "symdesk MCP failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    serde_json::Deserializer::from_slice(&output.stdout)
        .into_iter::<Value>()
        .map(|frame| frame.expect("decode MCP response frame"))
        .collect()
}

fn seed_ask_retrieval_index(
    case: &FixtureCase,
    vault: &std::path::Path,
    index_path: &std::path::Path,
) {
    let index = RetrievalDb::open_at(index_path).expect("open isolated Ask retrieval index");
    for document in &case.documents {
        assert_eq!(
            document.embedding.len(),
            case.embedding_dim,
            "{} embedding fixture dimension",
            document.path
        );
        let path = vault
            .join(&document.path)
            .canonicalize()
            .expect("canonical Ask document path");
        let path_text = path.to_string_lossy().into_owned();
        let sections = parse_markdown_retrieval_sections(&path_text, document.body.as_bytes())
            .expect("parse Ask fixture Markdown");
        let chunks = materialize_chunks(&path_text, &sections);
        index
            .save_document(&RetrievalDocument {
                path: path_text.clone(),
                hash: symdesk_vault::sha256_hex(document.body.as_bytes()),
                updated_at: "2026-09-29T00:00:00Z".to_owned(),
            })
            .expect("save Ask retrieval document");
        let norm = document
            .embedding
            .iter()
            .map(|value| value * value)
            .sum::<f32>()
            .sqrt();
        let stored = chunks
            .into_iter()
            .map(|chunk| {
                let embedding = if chunk.content.contains("__SYMDESK_SEARCH_METADATA_START__") {
                    vec![0.0, 1.0, 0.0]
                } else {
                    document.embedding.clone()
                };
                StoredRetrievalChunk {
                    id: 0,
                    uuid: chunk.uuid,
                    document_path: path_text.clone(),
                    chunk_index: chunk.chunk_index as i64,
                    content: chunk.content,
                    embedding,
                    hash: chunk.hash,
                    norm,
                    dim: case.embedding_dim as i64,
                    model: "fixture-model".to_owned(),
                    char_start: chunk.char_start.map(|value| value as i64),
                    char_end: chunk.char_end.map(|value| value as i64),
                    anchor_kind: chunk.anchor_kind,
                    anchor_value: chunk.anchor_value,
                    embedding_pending: false,
                }
            })
            .collect::<Vec<_>>();
        index
            .save_chunks(&stored)
            .expect("save Ask retrieval chunks");
    }
}

fn start_ask_embedding_server(case: &FixtureCase) -> RunningAskEmbeddingServer {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind fake Ask embedding provider");
    listener
        .set_nonblocking(true)
        .expect("set listener nonblocking");
    let address = listener.local_addr().expect("embedding listener address");
    let endpoint = format!("http://{address}/api/embeddings");
    let captured = Arc::new(Mutex::new(Vec::new()));
    let capture = Arc::clone(&captured);
    let case = case.clone();
    let (stop, stopped) = mpsc::channel();
    let server = thread::spawn(move || {
        loop {
            if stopped.try_recv().is_ok() {
                break;
            }
            match listener.accept() {
                Ok((mut stream, _)) => {
                    let _ = stream.set_nonblocking(false);
                    let _ = stream.set_read_timeout(Some(Duration::from_secs(2)));
                    let Some((method, path, body)) = read_ask_http_request(&mut stream) else {
                        continue;
                    };
                    if let Ok(mut requests) = capture.lock() {
                        requests.push(FixtureRequest {
                            method,
                            path,
                            body: body.clone(),
                        });
                    }
                    let input = body["input"]
                        .as_array()
                        .and_then(|items| items.first())
                        .and_then(Value::as_str)
                        .unwrap_or_default();
                    let vector = if input == "violet cosmic wavelength" {
                        Some(vec![1.0_f32, 0.0, 0.0])
                    } else {
                        case.documents
                            .iter()
                            .find(|document| {
                                !document.embedding_marker.is_empty()
                                    && input.contains(&document.embedding_marker)
                            })
                            .map(|document| document.embedding.clone())
                    };
                    let (status, response) = match vector {
                        Some(vector) if vector.len() == case.embedding_dim => {
                            (200, json!({"data":[{"embedding":vector}]}))
                        }
                        _ => (400, json!({"error":"no configured fixture embedding"})),
                    };
                    let response = serde_json::to_vec(&response).expect("encode fake response");
                    let reason = if status == 200 { "OK" } else { "Bad Request" };
                    let header = format!(
                        "HTTP/1.1 {status} {reason}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
                        response.len()
                    );
                    let _ = stream.write_all(header.as_bytes());
                    let _ = stream.write_all(&response);
                }
                Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(5));
                }
                Err(_) => break,
            }
        }
    });
    RunningAskEmbeddingServer {
        endpoint,
        captured,
        stop,
        server: Some(server),
    }
}

fn read_ask_http_request(stream: &mut TcpStream) -> Option<(String, String, Value)> {
    let mut reader = std::io::BufReader::new(stream);
    let mut first = String::new();
    reader.read_line(&mut first).ok()?;
    let mut parts = first.split_whitespace();
    let method = parts.next()?.to_owned();
    let path = parts.next()?.to_owned();
    let mut content_length = 0_usize;
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).ok()?;
        if line == "\r\n" || line == "\n" {
            break;
        }
        if let Some((name, value)) = line.trim_end().split_once(':')
            && name.eq_ignore_ascii_case("content-length")
        {
            content_length = value.trim().parse().ok()?;
        }
    }
    let mut body = vec![0_u8; content_length];
    reader.read_exact(&mut body).ok()?;
    Some((method, path, serde_json::from_slice(&body).ok()?))
}

fn call_request(id: usize, arguments_json: &str) -> String {
    if arguments_json.is_empty() {
        format!(
            r#"{{"jsonrpc":"2.0","id":{id},"method":"tools/call","params":{{"name":"desk_ask"}}}}"#
        )
    } else {
        format!(
            r#"{{"jsonrpc":"2.0","id":{id},"method":"tools/call","params":{{"name":"desk_ask","arguments":{arguments_json}}}}}"#
        )
    }
}

fn call_fixture_request(id: usize, call: &FixtureCall) -> String {
    if !call.raw_frame_json.is_empty() {
        call.raw_frame_json
            .replace("\"id\":0", &format!("\"id\":{id}"))
    } else if !call.raw_params_json.is_empty() {
        format!(
            r#"{{"jsonrpc":"2.0","id":{id},"method":"tools/call","params":{}}}"#,
            call.raw_params_json
        )
    } else if call.arguments_json.is_empty() {
        let tool = if call.tool.is_empty() {
            "desk_ask"
        } else {
            &call.tool
        };
        format!(
            r#"{{"jsonrpc":"2.0","id":{id},"method":"tools/call","params":{{"name":"{tool}"}}}}"#
        )
    } else {
        let tool = if call.tool.is_empty() {
            "desk_ask"
        } else {
            &call.tool
        };
        format!(
            r#"{{"jsonrpc":"2.0","id":{id},"method":"tools/call","params":{{"name":"{tool}","arguments":{}}}}}"#,
            call.arguments_json
        )
    }
}

fn normalize_value(mut value: Value, vault: &std::path::Path) -> Value {
    if let Some(text) = value
        .pointer("/result/content/0/text")
        .and_then(Value::as_str)
        .map(str::to_owned)
    {
        *value
            .pointer_mut("/result/content/0/text")
            .expect("text field") =
            Value::String(text.replace(&vault.to_string_lossy().to_string(), "$VAULT"));
    }
    value
}
