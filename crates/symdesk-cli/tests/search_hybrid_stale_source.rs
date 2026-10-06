use std::{
    fs,
    io::{BufRead, BufReader, Read, Write},
    net::{TcpListener, TcpStream},
    path::PathBuf,
    process::Command,
    sync::mpsc,
    thread,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use serde_json::{Value, json};
use symdesk_index::{
    RetrievalDb, RetrievalDocument, SourceRegistry, StoredRetrievalChunk, materialize_chunks,
    parse_markdown_retrieval_sections,
};

struct TempRoot(PathBuf);

impl TempRoot {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symdesk-stale-source-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir_all(&path).expect("create test root");
        Self(path)
    }

    fn path(&self, relative: &str) -> PathBuf {
        self.0.join(relative)
    }
}

impl Drop for TempRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

#[test]
fn hybrid_search_migrates_populated_legacy_index_and_skips_deleted_source() {
    let root = TempRoot::new();
    let vault = root.path("vault");
    let source = root.path("external");
    let home = root.path("home");
    let cwd = root.path("cwd");
    let index_path = root.path("data/symdesk/retrieval.db");
    fs::create_dir_all(&vault).expect("create vault");
    fs::create_dir_all(&source).expect("create external source");
    fs::create_dir_all(&cwd).expect("create cwd");
    fs::create_dir_all(home.join(".config/symseek")).expect("create config directory");

    let document_path = vault.join("needle.md");
    let content = "---\ntitle: Vault result\n---\n\nneedle stale source parity";
    fs::write(&document_path, content).expect("write vault document");
    let document_path = document_path.canonicalize().expect("canonical document");
    let document_path_text = document_path.to_string_lossy().into_owned();
    let sections = parse_markdown_retrieval_sections(&document_path_text, content.as_bytes())
        .expect("parse document");
    let chunks = materialize_chunks(&document_path_text, &sections)
        .into_iter()
        .map(|chunk| StoredRetrievalChunk {
            id: 0,
            uuid: chunk.uuid,
            document_path: document_path_text.clone(),
            chunk_index: chunk.chunk_index as i64,
            content: chunk.content,
            embedding: vec![1.0, 0.0, 0.0],
            hash: chunk.hash,
            norm: 1.0,
            dim: 3,
            model: "fixture-model".to_owned(),
            char_start: chunk.char_start.map(|value| value as i64),
            char_end: chunk.char_end.map(|value| value as i64),
            anchor_kind: chunk.anchor_kind,
            anchor_value: chunk.anchor_value,
            embedding_pending: false,
        })
        .collect::<Vec<_>>();
    let index = RetrievalDb::open_at(&index_path).expect("open retrieval index");
    index
        .save_document(&RetrievalDocument {
            path: document_path_text,
            hash: symdesk_vault::sha256_hex(content.as_bytes()),
            updated_at: "2026-10-01T00:00:00Z".to_owned(),
        })
        .expect("save document");
    index.save_chunks(&chunks).expect("save chunks");

    let registry = SourceRegistry::open(&vault).expect("open source registry");
    registry.add(&source).expect("register external source");
    fs::remove_dir_all(&source).expect("remove registered source directory");

    let (endpoint, stop_server, server) = start_embedding_server();
    fs::write(
        home.join(".config/symseek/config.toml"),
        format!(
            "ollama_url = {:?}\nmodel = \"fixture-model\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = 0\nvector_backend = \"sqlite\"\nvector_quantization = \"off\"\n",
            endpoint
        ),
    )
    .expect("write retrieval config");

    let output = Command::new(env!("CARGO_BIN_EXE_symdesk"))
        .env_clear()
        .env("PATH", std::env::var("PATH").unwrap_or_default())
        .args([
            "--vault",
            vault.to_str().expect("UTF-8 vault"),
            "--output",
            "json",
            "search",
            "needle",
        ])
        .current_dir(&cwd)
        .env("HOME", &home)
        .env("USERPROFILE", &home)
        .env("XDG_DATA_HOME", root.path("data"))
        .env("TMPDIR", root.path("tmp"))
        .env("TMP", root.path("tmp"))
        .env("TEMP", root.path("tmp"))
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC")
        .env_remove("SYMDESK_VAULT")
        .output()
        .expect("run symdesk search");
    let _ = stop_server.send(());
    server.join().expect("join embedding server");

    assert_eq!(
        output.status.code(),
        Some(0),
        "search failed: stdout={} stderr={}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    let response: Value = serde_json::from_slice(&output.stdout).expect("decode search JSON");
    let results = response["results"].as_array().expect("results array");
    assert!(
        results.iter().any(|result| result["path"] == "needle.md"),
        "vault hit missing from {response}"
    );
    let standalone =
        RetrievalDb::open_at(&index_path).expect("legacy shared index remains available");
    assert!(standalone.count_chunks().expect("count shared chunks") > 0);
    let migrated = find_migrated_vault_index(&root.path("data/symdesk/vaults"))
        .expect("per-vault index seeded from legacy shared index");
    assert!(migrated.count_chunks().expect("count migrated chunks") > 0);
}

fn find_migrated_vault_index(root: &std::path::Path) -> Option<RetrievalDb> {
    let entries = fs::read_dir(root).ok()?;
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            if let Some(database) = find_migrated_vault_index(&path) {
                return Some(database);
            }
        } else if path.file_name().is_some_and(|name| name == "retrieval.db")
            && let Ok(database) = RetrievalDb::open_at(path)
            && database.count_chunks().is_ok_and(|count| count > 0)
        {
            return Some(database);
        }
    }
    None
}

fn start_embedding_server() -> (String, mpsc::Sender<()>, thread::JoinHandle<()>) {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind local embedding server");
    listener
        .set_nonblocking(true)
        .expect("set listener nonblocking");
    let endpoint = format!(
        "http://{}/api/embeddings",
        listener.local_addr().expect("listener address")
    );
    let (stop, stopped) = mpsc::channel();
    let worker = thread::spawn(move || {
        loop {
            if stopped.try_recv().is_ok() {
                break;
            }
            match listener.accept() {
                Ok((stream, _)) => respond_embedding_request(stream),
                Err(error) if error.kind() == std::io::ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(5));
                }
                Err(error) => panic!("accept embedding request: {error}"),
            }
        }
    });
    (endpoint, stop, worker)
}

fn respond_embedding_request(stream: TcpStream) {
    stream
        .set_nonblocking(false)
        .expect("make accepted test stream blocking");
    let mut reader = BufReader::new(stream.try_clone().expect("clone HTTP stream"));
    let mut request_line = String::new();
    reader
        .read_line(&mut request_line)
        .expect("read request line");
    let mut content_length = 0usize;
    loop {
        let mut line = String::new();
        reader.read_line(&mut line).expect("read request header");
        if line == "\r\n" || line.is_empty() {
            break;
        }
        if let Some((name, value)) = line.split_once(':')
            && name.eq_ignore_ascii_case("content-length")
        {
            content_length = value.trim().parse().expect("content length");
        }
    }
    let mut body = vec![0; content_length];
    reader.read_exact(&mut body).expect("read request body");
    let _request: Value = serde_json::from_slice(&body).expect("decode request JSON");
    let response = json!({"data": [{"embedding": [1.0, 0.0, 0.0]}]}).to_string();
    let mut stream = reader.into_inner();
    write!(
        stream,
        "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
        response.len(),
        response
    )
    .expect("write embedding response");
}
