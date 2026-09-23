#![deny(unsafe_code)]

//! Replays the Go-owned destructive history purge fixture.

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

use serde::Deserialize;
use symdesk_vault::history::{HistoryError, HistoryStore};
use time::{Duration, OffsetDateTime};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle: Oracle,
    source_hashes: BTreeMap<String, String>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Clone, Deserialize)]
struct Case {
    id: String,
    description: String,
    operation: String,
    #[serde(default)]
    files: Vec<FileSpec>,
    #[serde(default)]
    paths: Vec<String>,
    #[serde(default)]
    result: String,
    #[serde(default)]
    error: String,
    #[serde(default)]
    error_class: String,
    #[serde(default)]
    after: Vec<FileRecord>,
    #[serde(default)]
    objects: Vec<ObjectRecord>,
}

#[derive(Clone, Deserialize)]
struct FileSpec {
    path: String,
    #[serde(default)]
    content: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
struct FileRecord {
    path: String,
    #[serde(default)]
    mode: Option<u32>,
    size: i64,
    #[serde(default)]
    sha256: String,
    #[serde(default)]
    content: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq, Eq)]
struct ObjectRecord {
    name: String,
    size: i64,
    sha256: String,
}

fn fixture_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/vault/history-purge.json")
}

#[test]
fn go_history_purge_contracts_replay() {
    let fixture: Fixture =
        serde_json::from_slice(&fs::read(fixture_path()).expect("read Go history purge fixture"))
            .expect("decode Go history purge fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle.commit,
        "d78e40d4083eefbda54aee53b771d5da6136c905"
    );
    assert_eq!(fixture.oracle.release, "post-v0.12.2-security-880");
    assert!(
        fixture
            .source_hashes
            .contains_key("internal/history/history.go")
    );
    assert!(
        fixture
            .source_hashes
            .contains_key("internal/history/checkpoint.go")
    );
    assert_eq!(fixture.cases.len(), 6);

    for case in fixture.cases {
        let operation = if case.id.starts_with("preflight-") {
            "preflight_purge_paths"
        } else {
            "purge_paths"
        };
        assert_eq!(case.operation, operation, "{} operation", case.id);
        let scenario = Scenario::new(&case);
        let execution = execute(&scenario, &case);
        match execution {
            Ok(result) => {
                assert!(
                    case.error.is_empty(),
                    "{}: Rust succeeded but Go failed",
                    case.id
                );
                assert_eq!(result, case.result, "{} result", case.id);
            }
            Err(error) => {
                assert_eq!(error.to_string(), case.error, "{} error", case.id);
                assert_eq!(
                    error_class(&error),
                    case.error_class,
                    "{} error class",
                    case.id
                );
            }
        }
        assert_eq!(state_of(scenario.path()), case.after, "{} files", case.id);
        assert_eq!(
            objects_of(&scenario.path().join(".symdesk/history/objects")),
            case.objects,
            "{} objects",
            case.id
        );
        assert!(!case.description.is_empty());
    }
}

struct TempVault(PathBuf);

impl Drop for TempVault {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

struct Scenario {
    guard: TempVault,
    store: HistoryStore,
}

impl Scenario {
    fn new(case: &Case) -> Self {
        let guard = temp_root(&case.id);
        for file in &case.files {
            let path = guard.0.join(&file.path);
            fs::create_dir_all(path.parent().expect("file parent")).expect("create parent");
            fs::write(path, file.content.as_bytes()).expect("write initial file");
        }
        let store = HistoryStore::with_clock(&guard.0, monotonic_clock());
        Self { guard, store }
    }

    fn path(&self) -> &Path {
        &self.guard.0
    }
}

fn temp_root(name: &str) -> TempVault {
    use std::sync::atomic::{AtomicU64, Ordering};
    static COUNTER: AtomicU64 = AtomicU64::new(0);
    let root = std::env::temp_dir().join(format!(
        "symdesk-history-purge-{name}-{}-{}",
        std::process::id(),
        COUNTER.fetch_add(1, Ordering::SeqCst)
    ));
    fs::create_dir_all(&root).expect("create temp vault");
    TempVault(root)
}

fn monotonic_clock() -> impl Fn() -> OffsetDateTime + Send + Sync + 'static {
    use std::sync::atomic::{AtomicI64, Ordering};
    let base = OffsetDateTime::from_unix_timestamp(1_767_225_600).expect("fixed base timestamp");
    let offset = AtomicI64::new(0);
    move || base + Duration::seconds(offset.fetch_add(1, Ordering::SeqCst))
}

fn execute(scenario: &Scenario, case: &Case) -> Result<String, HistoryError> {
    let store = &scenario.store;
    let root = scenario.path();
    match case.id.as_str() {
        "purge-success-filters-checkpoints-and-gc" => {
            for path in ["target.md", "keep.md", "shared.md"] {
                store.snapshot(path)?;
            }
            for path in [
                "target.md",
                "keep.md",
                "target-new.md",
                "target-skip/child.md",
            ] {
                store.checkpoint_file("mixed", path)?;
            }
            store.checkpoint_file("empty", "target-only-new.md")?;
            let orphan_id = "f".repeat(64);
            let objects = root.join(".symdesk/history/objects");
            fs::write(objects.join(orphan_id), b"orphan").expect("write orphan object");
            store.purge_paths(&case.paths)?;
            Ok(purge_result(scenario))
        }
        "preflight-valid-inventory-is-read-only" => {
            for path in ["target.md", "keep.md"] {
                store.snapshot(path)?;
            }
            store.checkpoint_file("task", "target.md")?;
            store.preflight_purge_paths(&case.paths)?;
            Ok(purge_result(scenario))
        }
        "purge-corrupt-survivor-preserves-target" => {
            for path in ["target.md", "keep.md"] {
                store.snapshot(path)?;
            }
            store.checkpoint_file("task", "target.md")?;
            fs::write(root.join(".symdesk/history/manifest/keep.md.json"), b"null")
                .expect("replace survivor manifest with JSON null");
            store.purge_paths(&case.paths)?;
            Ok("purge accepted a corrupt survivor".to_owned())
        }
        "purge-corrupt-checkpoint-preserves-target" => {
            store.snapshot("target.md")?;
            store.checkpoint_file("task", "target.md")?;
            fs::write(root.join(".symdesk/history/checkpoints/task.json"), b"null")
                .expect("replace checkpoint with JSON null");
            store.purge_paths(&case.paths)?;
            Ok("purge accepted a corrupt checkpoint".to_owned())
        }
        "purge-replaced-object-fails-before-mutation" => {
            store.snapshot("target.md")?;
            let keep = store.snapshot("keep.md")?.expect("keep snapshot");
            fs::write(
                root.join(format!(".symdesk/history/objects/{}", keep.id)),
                b"replaced",
            )
            .expect("replace snapshot bytes");
            store.purge_paths(&case.paths)?;
            Ok("purge accepted replaced object bytes".to_owned())
        }
        "preflight-traversal-target-is-rejected" => {
            store.preflight_purge_paths(&case.paths)?;
            Ok("preflight accepted traversal".to_owned())
        }
        other => Err(HistoryError::Io(std::io::Error::other(format!(
            "unknown fixture case {other}"
        )))),
    }
}

fn purge_result(scenario: &Scenario) -> String {
    let target = scenario
        .store
        .list("target.md")
        .expect("read target history")
        .unwrap_or_default();
    let keep = scenario
        .store
        .list("keep.md")
        .expect("read keep history")
        .unwrap_or_default();
    let checkpoints = scenario.store.list_checkpoints().expect("read checkpoints");
    format!(
        "target={} keep={} checkpoints={} objects={}",
        target.len(),
        keep.len(),
        checkpoints.len(),
        objects_of(&scenario.path().join(".symdesk/history/objects"))
            .iter()
            .map(|object| object.name.as_str())
            .collect::<Vec<_>>()
            .join(",")
    )
}

fn error_class(error: &HistoryError) -> &'static str {
    match error {
        HistoryError::InvalidPath(_) => "invalid_path",
        _ => "other",
    }
}

fn objects_of(directory: &Path) -> Vec<ObjectRecord> {
    let Ok(entries) = fs::read_dir(directory) else {
        return Vec::new();
    };
    let mut objects = entries
        .flatten()
        .filter_map(|entry| {
            let data = fs::read(entry.path()).ok()?;
            Some(ObjectRecord {
                name: entry.file_name().to_string_lossy().into_owned(),
                size: i64::try_from(data.len()).unwrap_or(i64::MAX),
                sha256: sha256_hex(&data),
            })
        })
        .collect::<Vec<_>>();
    objects.sort_by(|left, right| left.name.cmp(&right.name));
    objects
}

fn state_of(root: &Path) -> Vec<FileRecord> {
    fn visit(root: &Path, directory: &Path, files: &mut Vec<FileRecord>) {
        let Ok(entries) = fs::read_dir(directory) else {
            return;
        };
        for entry in entries.flatten() {
            let path = entry.path();
            let Ok(metadata) = fs::symlink_metadata(&path) else {
                continue;
            };
            if metadata.is_dir() {
                visit(root, &path, files);
                continue;
            }
            let relative = path.strip_prefix(root).expect("under root");
            let relative = relative.to_string_lossy().replace('\\', "/");
            if relative.starts_with(".symdesk/history/objects/") {
                continue;
            }
            let data = fs::read(&path).expect("read file state");
            let text = String::from_utf8(data.clone()).ok();
            let record = if let Some(text) = text.filter(|text| !text.contains('\u{fffd}')) {
                let content = normalize_text(&text);
                FileRecord {
                    path: relative,
                    mode: permission_bits(&metadata),
                    size: i64::try_from(content.len()).unwrap_or(i64::MAX),
                    sha256: String::new(),
                    content,
                }
            } else {
                FileRecord {
                    path: relative,
                    mode: permission_bits(&metadata),
                    size: i64::try_from(data.len()).unwrap_or(i64::MAX),
                    sha256: sha256_hex(&data),
                    content: String::new(),
                }
            };
            files.push(record);
        }
    }
    let mut files = Vec::new();
    visit(root, root, &mut files);
    files.sort_by(|left, right| left.path.cmp(&right.path));
    files
}

#[cfg(unix)]
fn permission_bits(metadata: &fs::Metadata) -> Option<u32> {
    use std::os::unix::fs::PermissionsExt;
    Some(metadata.permissions().mode() & 0o777)
}

#[cfg(not(unix))]
fn permission_bits(_metadata: &fs::Metadata) -> Option<u32> {
    None
}

fn normalize_text(text: &str) -> String {
    let mut output = String::with_capacity(text.len());
    for (index, line) in text.lines().enumerate() {
        if index > 0 {
            output.push('\n');
        }
        let trimmed = line.trim_start();
        let indent = &line[..line.len() - trimmed.len()];
        let line = if trimmed.starts_with("\"timestamp\": \"") {
            format!("{indent}\"timestamp\": \"{{{{timestamp}}}}\",")
        } else {
            line.to_owned()
        };
        output.push_str(&line);
    }
    if text.ends_with('\n') {
        output.push('\n');
    }
    output
}

fn sha256_hex(data: &[u8]) -> String {
    let digest = symdesk_vault::sha256::digest(data);
    digest.iter().map(|byte| format!("{byte:02x}")).collect()
}
