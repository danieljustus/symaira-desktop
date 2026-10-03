use std::{
    collections::BTreeMap,
    fs,
    io::{BufRead, BufReader, Read, Write},
    net::{TcpListener, TcpStream},
    path::PathBuf,
    process::Command,
    sync::{Arc, Mutex, mpsc},
    thread,
    time::Duration,
};

#[path = "support/isolated_root.rs"]
mod isolated_root;

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

#[derive(Deserialize)]
struct FixtureCase {
    id: String,
    query: String,
    index_documents: bool,
    #[serde(default, deserialize_with = "null_default")]
    documents: Vec<FixtureDocument>,
    #[serde(default)]
    sources: Vec<String>,
    expected: Vec<Value>,
    requests: Vec<FixtureRequest>,
}

fn null_default<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

#[derive(Deserialize)]
struct FixtureDocument {
    path: String,
    body: String,
    #[serde(default)]
    source: String,
}

#[derive(Deserialize)]
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

type RunningAskEmbeddingServer = (
    String,
    Arc<Mutex<Vec<CapturedRequest>>>,
    mpsc::Sender<()>,
    thread::JoinHandle<()>,
);

struct TempRoot(PathBuf);

impl TempRoot {
    fn new() -> Self {
        let root = isolated_root::create("symdesk-ask-offline");
        for child in [
            "home",
            "home/config",
            "home/cache",
            "cwd",
            "data",
            "tmp",
            "vault",
            "outside",
        ] {
            fs::create_dir_all(root.join(child)).expect("create private test directory");
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

#[test]
fn real_ask_cli_replays_go_service_oracle() {
    replay_fixture(false);
}

#[cfg(unix)]
#[test]
fn real_ask_cli_replays_go_service_oracle_through_symlinked_vault_root() {
    replay_fixture(true);
}

fn replay_fixture(alias_vault: bool) {
    let path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/cli/ask-offline.json");
    let fixture: Fixture =
        serde_json::from_slice(&fs::read(path).expect("Go-generated Ask fixture"))
            .expect("decode Ask fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 7);
    for case in &fixture.cases {
        replay_case(case, alias_vault);
    }
}

fn replay_case(case: &FixtureCase, alias_vault: bool) {
    let root = TempRoot::new();
    let vault = root
        .path("vault")
        .canonicalize()
        .expect("canonical test vault");
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
    let (endpoint, requests, stop, server) = start_embedding_server(case);

    let mut source_paths = BTreeMap::new();
    for source in &case.sources {
        let path = root.path("outside").join(source);
        fs::create_dir_all(&path).expect("create registered source");
        source_paths.insert(
            source.clone(),
            path.canonicalize().expect("canonical source root"),
        );
    }
    let mut documents = Vec::new();
    for document in &case.documents {
        let base = if document.source.is_empty() {
            vault.clone()
        } else {
            source_paths[&document.source].clone()
        };
        let path = base.join(&document.path);
        fs::create_dir_all(path.parent().expect("document parent"))
            .expect("create document parent");
        fs::write(&path, &document.body).expect("write fixture document");
        documents.push((path, document));
    }
    if case.index_documents {
        let db = RetrievalDb::open_at(&index_path).expect("open fixture retrieval index");
        for (path, document) in &documents {
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
                    model: "ask-fixture-model".to_owned(),
                    char_start: chunk.char_start.map(|n| n as i64),
                    char_end: chunk.char_end.map(|n| n as i64),
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
        SourceRegistry::open(&vault)
            .expect("open source registry")
            .add(source)
            .expect("register fixture source");
    }

    let canonical_vault = vault.canonicalize().expect("canonical test vault");
    let digest = symdesk_vault::sha256_hex(canonical_vault.to_string_lossy().as_bytes());
    let sidecar_path = root.path("data").join(&digest[..16]).join("sidecar.db");
    let mut sidecar = Sidecar::open(&sidecar_path).expect("open isolated sidecar");
    sidecar
        .refresh_index_for_cli(&vault)
        .expect("refresh FTS index");
    drop(sidecar);

    let config_dir = home.join(".config/symseek");
    fs::create_dir_all(&config_dir).expect("create isolated retrieval config");
    let config = format!(
        "index_path = {:?}\nollama_url = {:?}\nmodel = \"ask-fixture-model\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = 0\nvector_backend = \"sqlite\"\nvector_quantization = \"off\"\n",
        index_path.to_string_lossy(),
        endpoint
    );
    fs::write(config_dir.join("config.toml"), config).expect("write private retrieval config");

    let output = Command::new(env!("CARGO_BIN_EXE_symdesk"))
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
            "ask",
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
        .env_remove("SYMDESK_VAULT")
        .env_remove("SYMDESK_OLLAMA_URL")
        .output()
        .expect("run actual symdesk ask CLI");
    let _ = stop.send(());
    server.join().expect("join fake local embedding server");
    assert_eq!(
        output.status.code(),
        Some(0),
        "{} stderr: {}",
        case.id,
        String::from_utf8_lossy(&output.stderr)
    );

    let actual_requests = requests.lock().expect("captured requests").clone();
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
        actual_requests,
        expected_requests,
        "{} local embedding requests; stderr: {}",
        case.id,
        String::from_utf8_lossy(&output.stderr)
    );
    if case.index_documents {
        assert!(
            !actual_requests.is_empty(),
            "{} should use hybrid retrieval",
            case.id
        );
    }
    if case.id == "empty-query-zero-provider-requests"
        || case.id == "tag-plan-zero-embedding-requests"
    {
        assert!(
            actual_requests.is_empty(),
            "{} must not contact the embedding model",
            case.id
        );
    }
    assert!(
        actual_requests
            .iter()
            .all(|request| request.path != "/api/chat"),
        "Ask must not make an AI chat request"
    );

    let stdout = String::from_utf8(output.stdout).expect("UTF-8 Ask event stream");
    let mut actual = stdout
        .lines()
        .map(|line| serde_json::from_str::<Value>(line).expect("Ask JSON event"))
        .collect::<Vec<_>>();
    normalize_events(&mut actual, &vault, &source_paths);
    assert_eq!(actual, case.expected, "{} streamed Ask events", case.id);
}

fn normalize_events(
    events: &mut [Value],
    vault: &std::path::Path,
    sources: &BTreeMap<String, PathBuf>,
) {
    for event in events {
        if let Some(path) = event
            .get_mut("path")
            .and_then(|value| value.as_str())
            .map(str::to_owned)
            && PathBuf::from(&path).is_absolute()
        {
            if let Some((source, root)) = sources
                .iter()
                .find(|(_, root)| PathBuf::from(&path).starts_with(root))
            {
                let path_buf = PathBuf::from(&path);
                let relative = path_buf.strip_prefix(root).expect("source relative path");
                *event.get_mut("path").expect("path field") = json!(format!(
                    "@source/{source}/{}",
                    relative.to_string_lossy().replace('\\', "/")
                ));
            } else if let Ok(relative) = PathBuf::from(&path).strip_prefix(vault) {
                *event.get_mut("path").expect("path field") = json!(format!(
                    "@vault/{}",
                    relative.to_string_lossy().replace('\\', "/")
                ));
            }
        }
        if let Some(text) = event
            .get_mut("text")
            .and_then(|value| value.as_str())
            .map(str::to_owned)
        {
            let mut normalized = text;
            for (source, root) in sources {
                normalized = normalized.replace(
                    &format!("{}{}", root.to_string_lossy(), std::path::MAIN_SEPARATOR),
                    &format!("@source/{source}/"),
                );
                normalized = normalized.replace(
                    &root.to_string_lossy().to_string(),
                    &format!("@source/{source}"),
                );
            }
            *event.get_mut("text").expect("text field") = json!(normalized);
        }
    }
}

fn start_embedding_server(case: &FixtureCase) -> RunningAskEmbeddingServer {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind loopback fake embedding server");
    listener
        .set_nonblocking(true)
        .expect("set listener nonblocking");
    let address = listener.local_addr().expect("fake server address");
    let endpoint = format!("http://{address}/api/embeddings");
    let captured = Arc::new(Mutex::new(Vec::new()));
    let captured_thread = Arc::clone(&captured);
    let (stop, stopped) = mpsc::channel();
    let id = case.id.clone();
    let server = thread::spawn(move || {
        loop {
            if stopped.try_recv().is_ok() {
                break;
            }
            match listener.accept() {
                Ok((mut stream, _)) => {
                    stream
                        .set_nonblocking(false)
                        .expect("set accepted stream blocking");
                    stream
                        .set_read_timeout(Some(Duration::from_secs(2)))
                        .expect("bound fake request read");
                    if let Err(error) = serve_embedding(&mut stream, &captured_thread) {
                        panic!("{id} fake embedding server: {error}");
                    }
                }
                Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(5))
                }
                Err(error) => panic!("{id} fake embedding accept: {error}"),
            }
        }
    });
    (endpoint, captured, stop, server)
}

fn serve_embedding(
    stream: &mut TcpStream,
    captured: &Arc<Mutex<Vec<CapturedRequest>>>,
) -> std::io::Result<()> {
    let mut reader = BufReader::new(stream.try_clone()?);
    let mut request_line = String::new();
    reader.read_line(&mut request_line)?;
    let mut content_length = 0usize;
    loop {
        let mut line = String::new();
        reader.read_line(&mut line)?;
        if line == "\r\n" || line.is_empty() {
            break;
        }
        if let Some((name, value)) = line.split_once(':')
            && name.eq_ignore_ascii_case("content-length")
        {
            content_length = value.trim().parse().unwrap_or(0);
        }
    }
    let mut body = vec![0u8; content_length];
    reader.read_exact(&mut body)?;
    let mut pieces = request_line.split_whitespace();
    let method = pieces.next().unwrap_or_default().to_owned();
    let path = pieces.next().unwrap_or_default().to_owned();
    let body_value: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
    captured
        .lock()
        .expect("capture mutex")
        .push(CapturedRequest {
            method,
            path,
            body: body_value,
        });
    let response = json!({"data":[{"embedding":[1.0,0.0,0.0]}]});
    let bytes = serde_json::to_vec(&response).expect("encode embedding response");
    write!(
        stream,
        "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        bytes.len()
    )?;
    stream.write_all(&bytes)?;
    stream.flush()
}
