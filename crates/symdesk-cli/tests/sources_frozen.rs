#![cfg(unix)]
#![deny(unsafe_code)]

//! Frozen replay of the former `source-differential` and
//! `source-watch-differential` gates, which compared a live Go and a live Rust
//! `symdesk sources` binary (`scripts/rust-port/source-differential.py`).
//!
//! `scripts/rust-port/source_record.py` ran the real Go binary through both
//! scenarios once and committed what it observed:
//! `testdata/port/cli/source-registry.json` (list/add/remove, external search,
//! the registry file) and `testdata/port/cli/source-watch.json` (ready line and
//! the indexed content hash after create, update and delete). This test runs
//! the same scenarios against the Rust binary and compares; it never starts Go.
//!
//! Variable text is normalized exactly as the recorder does: canonical and
//! literal source/vault roots become `<SOURCE>`/`<VAULT>` and the path-derived
//! source ID `folder-<64 hex>` becomes `<ID>`.

use std::{
    fs,
    io::{BufRead, BufReader},
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::{
        atomic::{AtomicU64, Ordering},
        mpsc::{self, RecvTimeoutError},
    },
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use rusqlite::{Connection, OpenFlags};
use serde_json::Value;

const BINARY: &str = env!("CARGO_BIN_EXE_symdesk");
const REGISTRY: &str = include_str!("../../../testdata/port/cli/source-registry.json");
const WATCH: &str = include_str!("../../../testdata/port/cli/source-watch.json");
const GO_ORACLE: &str = "5c666cbf6051eba7b8e659d7d3f16d051c3404b9";
const EXPECTED_REGISTRY_STEPS: usize = 12;
const EXPECTED_WATCH_STATES: usize = 3;

static NEXT: AtomicU64 = AtomicU64::new(0);

fn scratch(prefix: &str) -> PathBuf {
    let tick = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock")
        .as_nanos();
    let root = std::env::temp_dir().join(format!(
        "{prefix}-{}-{tick}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    ));
    fs::create_dir_all(&root).expect("scratch root");
    // The recorder resolves its root too: the source ID hashes the canonical path.
    fs::canonicalize(&root).expect("canonical scratch root")
}

fn document(text: &str, kind: &str) -> Value {
    let document: Value = serde_json::from_str(text).expect("decode Go recording");
    assert_eq!(document["schema_version"], 1);
    assert_eq!(document["kind"], kind);
    assert_eq!(document["oracle"]["commit"], GO_ORACLE);
    document
}

fn originals(document: &Value, source: &Path) -> Vec<(String, String)> {
    fs::create_dir_all(source).expect("source directory");
    let mut files = Vec::new();
    for (name, text) in document["originals"].as_object().expect("originals") {
        let text = text.as_str().expect("original text").to_owned();
        fs::write(source.join(name), &text).expect("write original");
        files.push((name.clone(), text));
    }
    assert_eq!(files.len(), 3);
    files
}

fn command(home: &Path, vault: &Path) -> Command {
    let mut command = Command::new(BINARY);
    command.current_dir(home);
    for (key, sub) in [
        ("HOME", "home"),
        ("USERPROFILE", "userprofile"),
        ("XDG_DATA_HOME", "xdg_data_home"),
        ("XDG_CONFIG_HOME", "xdg_config_home"),
    ] {
        let directory = home.join(sub);
        fs::create_dir_all(&directory).expect("isolated home");
        command.env(key, directory);
    }
    command
        .env_remove("SYMDESK_VAULT")
        .env_remove("SYMDESK_SIDECAR")
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC")
        .env("NO_COLOR", "1");
    let _ = vault;
    command
}

/// Canonical roots first: on macOS the literal `/var/...` root is a suffix of
/// the canonical `/private/var/...` root.
fn normalizer(source: &Path, vault: &Path) -> impl Fn(&str) -> String {
    let mut pairs: Vec<(String, &'static str)> = Vec::new();
    for (path, token) in [(source, "<SOURCE>"), (vault, "<VAULT>")] {
        pairs.push((
            fs::canonicalize(path)
                .unwrap_or_else(|_| path.to_path_buf())
                .to_string_lossy()
                .into_owned(),
            token,
        ));
        pairs.push((path.to_string_lossy().into_owned(), token));
    }
    pairs.sort_by_key(|(needle, _)| std::cmp::Reverse(needle.len()));
    move |text: &str| {
        let mut text = text.to_owned();
        for (needle, token) in &pairs {
            text = text.replace(needle.as_str(), token);
        }
        replace_source_ids(&text)
    }
}

/// `folder-[0-9a-f]{64}` -> `<ID>`.
fn replace_source_ids(text: &str) -> String {
    const PREFIX: &str = "folder-";
    let bytes = text.as_bytes();
    let mut result = String::with_capacity(text.len());
    let mut index = 0;
    while index < bytes.len() {
        if text[index..].starts_with(PREFIX) {
            let digits = &bytes[index + PREFIX.len()..];
            let hex = digits
                .iter()
                .take_while(|byte| matches!(byte, b'0'..=b'9' | b'a'..=b'f'))
                .count();
            if hex >= 64 {
                result.push_str("<ID>");
                index += PREFIX.len() + 64;
                continue;
            }
        }
        let character = text[index..].chars().next().expect("char boundary");
        result.push(character);
        index += character.len_utf8();
    }
    result
}

fn run_json(home: &Path, vault: &Path, args: &[String]) -> String {
    let output = command(home, vault)
        .args(["--json", "--vault"])
        .arg(vault)
        .args(args)
        .output()
        .expect("run symdesk");
    assert!(
        output.status.success(),
        "symdesk {args:?}: {}{}",
        String::from_utf8_lossy(&output.stderr),
        String::from_utf8_lossy(&output.stdout)
    );
    String::from_utf8(output.stdout).expect("UTF-8 JSON")
}

#[test]
fn source_registry_and_external_search_match_go_recording() {
    let recording = document(REGISTRY, "registry");
    let root = scratch("symdesk-source-registry");
    let source = root.join("external");
    let originals = originals(&recording, &source);
    let (vault, home) = (root.join("vault"), root.join("home"));
    fs::create_dir_all(&vault).expect("vault");
    fs::create_dir_all(&home).expect("home");
    let normalize = normalizer(&source, &vault);
    let queries = recording["queries"].as_object().expect("queries");
    let steps = recording["steps"].as_array().expect("steps");
    assert_eq!(steps.len(), EXPECTED_REGISTRY_STEPS);

    let mut source_id = String::new();
    for (index, step) in steps.iter().enumerate() {
        let args: Vec<String> = step["args"]
            .as_array()
            .expect("step args")
            .iter()
            .map(|arg| {
                arg.as_str()
                    .expect("arg")
                    .replace("<SOURCE>", &source.to_string_lossy())
                    .replace("<ID>", &source_id)
            })
            .collect();
        let stdout = run_json(&home, &vault, &args);
        let parsed: Value = serde_json::from_str(&stdout).expect("JSON stdout");
        if args[..2] == ["sources".to_owned(), "add".to_owned()] && source_id.is_empty() {
            source_id = parsed["source"]["id"]
                .as_str()
                .expect("source id")
                .to_owned();
            assert!(source_id.starts_with("folder-") && source_id.len() == 71);
        }
        let observed = match step["output"].get("target_hit") {
            Some(_) => {
                let query = args[1].as_str();
                let filename = queries[query].as_str().expect("query file");
                let candidates = [
                    fs::canonicalize(&source)
                        .expect("canonical source")
                        .join(filename),
                    source.join(filename),
                ];
                let hits: Vec<&Value> = parsed["results"]
                    .as_array()
                    .expect("results")
                    .iter()
                    .filter(|hit| {
                        candidates
                            .iter()
                            .any(|candidate| hit["path"].as_str() == candidate.to_str())
                    })
                    .collect();
                assert_eq!(hits.len(), 1, "step {index} {query}: {parsed}");
                let hit = hits[0];
                assert!(hit["snippet"].as_str().expect("snippet").contains(query));
                serde_json::json!({ "target_hit": {
                    "path": normalize(hit["path"].as_str().expect("path")),
                    "title": hit["title"].as_str().expect("title").trim(),
                    "snippet": hit["snippet"].as_str().expect("snippet").trim(),
                    "source_type": hit["source_type"],
                    "read_only": hit["read_only"],
                }})
            }
            None => serde_json::from_str(&normalize(&stdout)).expect("normalized JSON"),
        };
        assert_eq!(observed, step["output"], "step {index}: {args:?}");
    }

    let registry =
        fs::read_to_string(vault.join(".symdesk/search-sources.json")).expect("registry file");
    let observed: Value = serde_json::from_str(&normalize(&registry)).expect("registry JSON");
    assert_eq!(observed, recording["registry"], "registry file");
    for (name, text) in originals {
        assert_eq!(
            fs::read_to_string(source.join(&name)).expect("original"),
            text,
            "external source {name} was modified"
        );
    }
    fs::remove_dir_all(&root).expect("cleanup");
}

fn indexed_hash(home: &Path, note: &Path) -> Option<String> {
    fn databases(dir: &Path, found: &mut Vec<PathBuf>) {
        let Ok(entries) = fs::read_dir(dir) else {
            return;
        };
        for entry in entries.flatten() {
            let path = entry.path();
            match entry.file_type() {
                Ok(kind) if kind.is_dir() => databases(&path, found),
                Ok(kind) if kind.is_file() && path.extension().is_some_and(|ext| ext == "db") => {
                    found.push(path);
                }
                _ => {}
            }
        }
    }
    let mut found = Vec::new();
    databases(home, &mut found);
    for database in found {
        let Ok(connection) =
            Connection::open_with_flags(&database, OpenFlags::SQLITE_OPEN_READ_ONLY)
        else {
            continue;
        };
        let _ = connection.busy_timeout(Duration::from_secs(1));
        let has = |table: &str| {
            connection
                .query_row(
                    "SELECT 1 FROM sqlite_master WHERE type='table' AND name=?1",
                    [table],
                    |_| Ok(()),
                )
                .is_ok()
        };
        let sql = if has("documents") {
            "SELECT hash FROM documents WHERE path=?1"
        } else if has("files") {
            "SELECT sha256 FROM files WHERE path=?1"
        } else {
            continue;
        };
        if let Ok(hash) = connection.query_row(sql, [note.to_string_lossy().as_ref()], |row| {
            row.get::<_, String>(0)
        }) {
            return Some(hash);
        }
    }
    None
}

struct Watcher(Child);

impl Drop for Watcher {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = self.0.wait();
    }
}

#[test]
fn source_watch_lifecycle_matches_go_recording() {
    let recording = document(WATCH, "watch");
    let root = scratch("symdesk-source-watch");
    let source = root.join("external");
    originals(&recording, &source);
    let (vault, home) = (root.join("vault"), root.join("home"));
    fs::create_dir_all(&vault).expect("vault");
    fs::create_dir_all(&home).expect("home");
    let normalize = normalizer(&source, &vault);

    let added = run_json(
        &home,
        &vault,
        &[
            "sources".to_owned(),
            "add".to_owned(),
            source.to_string_lossy().into_owned(),
        ],
    );
    let source_id = serde_json::from_str::<Value>(&added).expect("add JSON")["source"]["id"]
        .as_str()
        .expect("source id")
        .to_owned();
    let mut child = command(&home, &vault)
        .arg("--vault")
        .arg(&vault)
        .args(["sources", "watch", &source_id])
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .expect("start sources watch");
    let stderr = child.stderr.take().expect("stderr pipe");
    let _watcher = Watcher(child);
    let (sender, lines) = mpsc::channel();
    thread::spawn(move || {
        for line in BufReader::new(stderr).lines().map_while(Result::ok) {
            if sender.send(line).is_err() {
                break;
            }
        }
    });

    let deadline = Instant::now() + Duration::from_secs(120);
    let ready = loop {
        let remaining = deadline.saturating_duration_since(Instant::now());
        match lines.recv_timeout(remaining.min(Duration::from_millis(200))) {
            Ok(line)
                if line.starts_with("Watching ") && line.contains(source.to_str().unwrap()) =>
            {
                break normalize(&line);
            }
            Ok(_) => {}
            Err(RecvTimeoutError::Timeout) if Instant::now() < deadline => {}
            Err(error) => panic!("sources watch never became ready: {error}"),
        }
    };
    assert_eq!(ready, recording["ready_line"].as_str().expect("ready line"));

    let nested = source.join("nested");
    fs::create_dir(&nested).expect("nested directory");
    // Wait until the watcher has registered the new directory: a probe note must reach the
    // index and leave it again before the recorded steps start. A fixed sleep loses the first
    // event on a saturated host.
    let probe = nested.join("probe.md");
    let probe_hash = Some(symdesk_vault::sha256_hex(b"# probe\n"));
    let ready_end = Instant::now() + Duration::from_secs(120);
    let mut rewritten = Instant::now();
    fs::write(&probe, "# probe\n").expect("write probe note");
    while indexed_hash(&home, &probe) != probe_hash {
        assert!(
            Instant::now() < ready_end,
            "nested directory never registered"
        );
        if rewritten.elapsed() > Duration::from_secs(5) {
            fs::write(&probe, "# probe\n").expect("rewrite probe note");
            rewritten = Instant::now();
        }
        thread::sleep(Duration::from_millis(200));
    }
    fs::remove_file(&probe).expect("delete probe note");
    while indexed_hash(&home, &probe).is_some() {
        assert!(Instant::now() < ready_end, "probe deletion never indexed");
        thread::sleep(Duration::from_millis(200));
    }
    let note = nested.join("watched.md");
    let observations = recording["observations"].as_array().expect("observations");
    assert_eq!(observations.len(), EXPECTED_WATCH_STATES);
    for observation in observations {
        let expected = match observation["action"].as_str().expect("action") {
            "write" => {
                let content = observation["content"].as_str().expect("content");
                fs::write(&note, content).expect("write watched note");
                Some(symdesk_vault::sha256_hex(content.as_bytes()))
            }
            "delete" => {
                fs::remove_file(&note).expect("delete watched note");
                None
            }
            other => panic!("unknown recorded action {other}"),
        };
        assert_eq!(
            expected.as_deref(),
            observation["indexed_sha256"].as_str(),
            "recorded Go hash is the content hash"
        );
        let end = Instant::now() + Duration::from_secs(120);
        let mut rewritten = Instant::now();
        while indexed_hash(&home, &note) != expected {
            assert!(
                Instant::now() < end,
                "watch index hash did not reach {expected:?}"
            );
            // A write event the OS never delivered is replayed with the same bytes; deletions
            // have nothing to replay.
            if observation["action"] == "write" && rewritten.elapsed() > Duration::from_secs(10) {
                let content = observation["content"].as_str().expect("content");
                fs::write(&note, content).expect("rewrite watched note");
                rewritten = Instant::now();
            }
            thread::sleep(Duration::from_millis(200));
        }
    }
    fs::remove_dir(&nested).expect("remove nested directory");
    drop(_watcher);
    fs::remove_dir_all(&root).expect("cleanup");
}
