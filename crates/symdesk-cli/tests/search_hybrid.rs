use std::{
    collections::BTreeMap,
    fs,
    io::{BufRead, BufReader, Read, Write},
    net::{TcpListener, TcpStream},
    path::{Path, PathBuf},
    process::Command,
    sync::{Arc, Mutex, mpsc},
    thread,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use serde_json::{Value, json};
use symdesk_index::{
    RetrievalDb, RetrievalDocument, Sidecar, SourceRegistry, StoredRetrievalChunk,
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
    index_documents: bool,
    documents: Vec<FixtureDocument>,
    #[serde(default, deserialize_with = "null_default")]
    sources: Vec<String>,
    requests: Vec<FixtureRequest>,
    expected: Value,
}

fn null_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

#[derive(Clone, Deserialize)]
struct FixtureDocument {
    path: String,
    body: String,
    #[serde(default)]
    source: String,
    #[serde(default)]
    index_model: String,
}

#[derive(Clone, Deserialize)]
struct FixtureRequest {
    method: String,
    path: String,
    body: Value,
}

#[derive(Clone, Debug, PartialEq)]
struct CapturedRequest {
    method: String,
    path: String,
    body: Value,
}

struct TempRoot(PathBuf);

impl TempRoot {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-search-hybrid-{}-{nonce}",
            std::process::id()
        ));
        for name in [
            "home",
            "home/config",
            "home/cache",
            "cwd",
            "data",
            "tmp",
            "vault",
            "outside",
        ] {
            fs::create_dir_all(root.join(name)).expect("create private test directory");
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

fn fixture() -> Fixture {
    let path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/cli/search-hybrid.json");
    serde_json::from_slice(&fs::read(path).expect("Go-generated hybrid search fixture"))
        .expect("decode hybrid search fixture")
}

#[test]
fn real_search_cli_replays_go_service_oracle() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 17);
    for case in fixture.cases {
        replay_case(&case, false);
    }
}

#[cfg(unix)]
#[test]
fn search_alias_root_preserves_hybrid_titles_and_lexical_fallback() {
    for case in fixture().cases {
        if matches!(
            case.id.as_str(),
            "multi-root-success-metadata-anchor-snippet" | "empty-retrieval-falls-back-to-sidecar"
        ) {
            replay_case(&case, true);
        }
    }
}

fn replay_case(case: &FixtureCase, alias_vault: bool) {
    let root = TempRoot::new();
    let vault = root.path("vault");
    #[cfg(unix)]
    let vault = if alias_vault {
        let alias = root.path("vault-alias");
        std::os::unix::fs::symlink(&vault, &alias).expect("create vault-root alias");
        alias
    } else {
        vault
    };
    #[cfg(not(unix))]
    let _ = alias_vault;
    let home = root.path("home");
    let cwd = root.path("cwd");
    let index_path = root.path("data/retrieval.db");
    let (endpoint, captured, stop_server, server) = start_embedding_server(case);

    let mut source_paths = BTreeMap::new();
    for source in &case.sources {
        let path = root.path("outside").join(source);
        fs::create_dir_all(&path).expect("create external source root");
        source_paths.insert(
            source.clone(),
            path.canonicalize()
                .expect("canonical registered source identity"),
        );
    }

    let mut stored_documents = Vec::new();
    for document in &case.documents {
        let document_root = if document.source.is_empty() {
            vault.clone()
        } else {
            source_paths[&document.source].clone()
        };
        let path = document_root.join(&document.path);
        fs::create_dir_all(path.parent().expect("document parent"))
            .expect("create document parent");
        fs::write(&path, &document.body).expect("write fixture document");
        stored_documents.push((path, document));
    }

    if case.index_documents {
        let db = RetrievalDb::open_at(&index_path).expect("open fixture retrieval index");
        for (path, document) in &stored_documents {
            let path = path.canonicalize().expect("canonical document path");
            let path_text = path.to_string_lossy().into_owned();
            let sections = parse_markdown_retrieval_sections(&path_text, document.body.as_bytes())
                .expect("parse fixture Markdown");
            let chunks = materialize_chunks(&path_text, &sections);
            let stored = chunks
                .into_iter()
                .map(|chunk| StoredRetrievalChunk {
                    id: 0,
                    uuid: chunk.uuid,
                    document_path: path_text.clone(),
                    chunk_index: chunk.chunk_index as i64,
                    content: chunk.content,
                    embedding: vec![1.0, 0.0, 0.0],
                    hash: chunk.hash,
                    norm: 1.0,
                    dim: 3,
                    model: if document.index_model.is_empty() {
                        "fixture-model".to_owned()
                    } else {
                        document.index_model.clone()
                    },
                    char_start: chunk.char_start.map(|value| value as i64),
                    char_end: chunk.char_end.map(|value| value as i64),
                    anchor_kind: chunk.anchor_kind,
                    anchor_value: chunk.anchor_value,
                    embedding_pending: false,
                })
                .collect::<Vec<_>>();
            db.save_document(&RetrievalDocument {
                path: path_text,
                hash: symdesk_vault::sha256_hex(document.body.as_bytes()),
                updated_at: "2026-09-29T00:00:00Z".to_owned(),
            })
            .expect("save retrieval document");
            db.save_chunks(&stored).expect("save retrieval chunks");
        }
    }
    for source in source_paths.values() {
        let registry = SourceRegistry::open(&vault).expect("open source registry");
        registry.add(source).expect("register fixture source");
    }

    let canonical_vault = vault.canonicalize().expect("canonical test vault");
    let digest = symdesk_vault::sha256_hex(canonical_vault.to_string_lossy().as_bytes());
    let sidecar_path = root.path("data").join(&digest[..16]).join("sidecar.db");
    let mut sidecar = Sidecar::open(&sidecar_path).expect("open isolated sidecar");
    sidecar
        .refresh_index_for_cli(&vault)
        .expect("refresh sidecar index");
    drop(sidecar);

    let config_dir = home.join(".config/symseek");
    fs::create_dir_all(&config_dir).expect("create isolated retrieval config directory");
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
    fs::write(config_dir.join("config.toml"), config).expect("write private retrieval config");

    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command
        .env_clear()
        .env("PATH", std::env::var("PATH").unwrap_or_default())
        .env(
            "SYSTEMROOT",
            std::env::var("SYSTEMROOT").unwrap_or_default(),
        )
        .args([
            "--vault",
            vault.to_str().expect("UTF-8 vault"),
            "--output",
            "json",
            "search",
            &case.query,
        ])
        .current_dir(&cwd)
        .env("HOME", &home)
        .env("USERPROFILE", &home)
        .env("XDG_CONFIG_HOME", root.path("home/config"))
        .env("XDG_CACHE_HOME", root.path("home/cache"))
        .env("XDG_DATA_HOME", root.path("data"))
        .env("SYMDESK_SIDECAR", &sidecar_path)
        .env("TMPDIR", root.path("tmp"))
        .env("TMP", root.path("tmp"))
        .env("TEMP", root.path("tmp"))
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC")
        .env("TERM", "dumb")
        .env("NO_COLOR", "1")
        .env_remove("SYMDESK_VAULT");
    let output = command.output().expect("run actual symdesk search CLI");
    let _ = stop_server.send(());
    server.join().expect("join local fake provider");
    assert_eq!(
        output.status.code(),
        Some(0),
        "{} stderr: {}",
        case.id,
        String::from_utf8_lossy(&output.stderr)
    );

    let actual_requests = captured.lock().expect("captured requests").clone();
    let expected_requests = case
        .requests
        .iter()
        .map(|request| CapturedRequest {
            method: request.method.clone(),
            path: request.path.clone(),
            body: request.body.clone(),
        })
        .collect::<Vec<_>>();
    assert_eq!(
        actual_requests, expected_requests,
        "{} provider requests",
        case.id
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

    let actual: Value = serde_json::from_slice(&output.stdout).unwrap_or_else(|error| {
        panic!(
            "{} stdout is not JSON: {error}: {}",
            case.id,
            String::from_utf8_lossy(&output.stdout)
        )
    });
    let actual = normalize_response(actual, &vault, &source_paths);
    let expected = normalize_response(case.expected.clone(), &vault, &source_paths);
    assert_eq!(
        canonicalize_response(actual),
        canonicalize_response(expected),
        "{} Go search response; stderr: {}",
        case.id,
        String::from_utf8_lossy(&output.stderr)
    );
}

type EmbeddingServer = (
    String,
    Arc<Mutex<Vec<CapturedRequest>>>,
    mpsc::Sender<()>,
    thread::JoinHandle<()>,
);

fn start_embedding_server(case: &FixtureCase) -> EmbeddingServer {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind loopback fixture server");
    listener
        .set_nonblocking(true)
        .expect("make listener nonblocking");
    let endpoint = format!(
        "http://{}/api/embeddings",
        listener.local_addr().expect("address")
    );
    let captured = Arc::new(Mutex::new(Vec::new()));
    let thread_capture = Arc::clone(&captured);
    let case = case.clone();
    let (stop, stopped) = mpsc::channel();
    let worker = thread::spawn(move || {
        loop {
            if stopped.try_recv().is_ok() {
                break;
            }
            match listener.accept() {
                Ok((stream, _)) => {
                    let request = serve_fixture_provider_request(stream, &case);
                    thread_capture
                        .lock()
                        .expect("capture HTTP request")
                        .push(request);
                }
                Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(5));
                }
                Err(error) => panic!("accept local fake provider request: {error}"),
            }
        }
    });
    (endpoint, captured, stop, worker)
}

fn serve_fixture_provider_request(mut stream: TcpStream, case: &FixtureCase) -> CapturedRequest {
    stream
        .set_nonblocking(false)
        .expect("make accepted test stream blocking");
    let mut reader = BufReader::new(stream.try_clone().expect("clone test HTTP stream"));
    let mut request_line = String::new();
    reader
        .read_line(&mut request_line)
        .expect("read request line");
    let mut content_length = 0usize;
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).expect("read request headers");
        if line == "\r\n" || line.is_empty() {
            break;
        }
        if let Some((name, value)) = line.split_once(':')
            && name.eq_ignore_ascii_case("content-length")
        {
            content_length = value.trim().parse().expect("content length");
        }
    }
    let mut bytes = vec![0; content_length];
    reader.read_exact(&mut bytes).expect("read request body");
    let body: Value = serde_json::from_slice(&bytes).expect("decode request JSON");
    let mut parts = request_line.split_whitespace();
    let request = CapturedRequest {
        method: parts.next().unwrap_or_default().to_owned(),
        path: parts.next().unwrap_or_default().to_owned(),
        body,
    };
    let (_, status_line, response_body) = if request.path == "/api/chat" {
        let status = if case.chat_status == 0 {
            200
        } else {
            case.chat_status
        };
        let response = if status >= 300 {
            json!({"error":"fixture chat failure"})
        } else {
            json!({"message":{"content":case.expanded_text}})
        };
        let response_body = if case.chat_response.is_empty() {
            serde_json::to_vec(&response).expect("encode chat response")
        } else {
            case.chat_response.as_bytes().to_vec()
        };
        let response_body = if status >= 300 && !case.chat_error_body.is_empty() {
            case.chat_error_body.as_bytes().to_vec()
        } else {
            response_body
        };
        (status, format!("HTTP/1.1 {status} Fixture"), response_body)
    } else {
        let input = request.body["input"][0].as_str().unwrap_or_default();
        let status = if input == case.query {
            case.provider_status
        } else {
            200
        };
        let dimension = if input == case.query || case.embedding_dim == 0 {
            case.provider_dimension
        } else {
            case.embedding_dim
        };
        let mut vector = vec![0.0_f32; dimension];
        if input == case.expanded_text && vector.len() > 1 {
            vector[1] = 1.0;
        } else if let Some(first) = vector.first_mut() {
            *first = 1.0;
        }
        let response = json!({"data":[{"embedding":vector}]});
        (
            status,
            format!("HTTP/1.1 {status} Fixture"),
            if status >= 300 {
                br#"{"error":"fixture provider failure"}"#.to_vec()
            } else {
                serde_json::to_vec(&response).expect("encode embedding response")
            },
        )
    };
    write!(
        stream,
        "{status_line}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        response_body.len()
    )
    .expect("write fixture response headers");
    stream
        .write_all(&response_body)
        .expect("write fixture response");
    request
}

fn normalize_response(
    mut response: Value,
    vault: &Path,
    sources: &BTreeMap<String, PathBuf>,
) -> Value {
    let Some(results) = response.get_mut("results").and_then(Value::as_array_mut) else {
        return response;
    };
    for result in results.iter_mut() {
        let Some(path) = result
            .get("path")
            .and_then(Value::as_str)
            .map(str::to_owned)
        else {
            continue;
        };
        let path_buf = PathBuf::from(&path);
        let normalized = if path.starts_with("@vault/") || path.starts_with("@source/") {
            path
        } else if !path_buf.is_absolute() {
            format!("@vault/{}", path.replace('\\', "/"))
        } else if let Ok(relative) = path_buf.strip_prefix(vault) {
            format!("@vault/{}", relative.to_string_lossy().replace('\\', "/"))
        } else if let Some((source, root)) =
            sources.iter().find(|(_, root)| path_buf.starts_with(root))
        {
            let relative = path_buf.strip_prefix(root).unwrap_or(Path::new(""));
            format!(
                "@source/{source}/{}",
                relative.to_string_lossy().replace('\\', "/")
            )
        } else {
            path.replace('\\', "/")
        };
        result["path"] = Value::String(normalized);
    }
    response
}

fn canonicalize_response(mut response: Value) -> Value {
    if let Some(results) = response.get_mut("results").and_then(Value::as_array_mut) {
        results.sort_by(|left, right| {
            let left_score = left
                .get("score")
                .and_then(Value::as_f64)
                .unwrap_or_default();
            let right_score = right
                .get("score")
                .and_then(Value::as_f64)
                .unwrap_or_default();
            right_score.total_cmp(&left_score).then_with(|| {
                left.get("path")
                    .and_then(Value::as_str)
                    .cmp(&right.get("path").and_then(Value::as_str))
            })
        });
    }
    response
}
