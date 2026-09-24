#![deny(unsafe_code)]

//! Replays Go `HistoryStore.Prune` policy and side effects.

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
};

use serde::Deserialize;
use serde_json::Value;
use symdesk_vault::history::{HistoryError, HistoryRetentionPolicy, HistoryStore};
use time::{Duration, OffsetDateTime, format_description::well_known::Rfc3339};

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
    files: Vec<FileSpec>,
    steps: Vec<Step>,
    policy: Policy,
    removed: usize,
    #[serde(default)]
    error: String,
    #[serde(default)]
    error_class: String,
    after: Vec<FileRecord>,
    objects: Vec<ObjectRecord>,
}

#[derive(Clone, Deserialize)]
struct FileSpec {
    path: String,
    #[serde(default)]
    content: String,
}

#[derive(Clone, Deserialize)]
struct Step {
    operation: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    task_id: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    index: usize,
    #[serde(default)]
    age_seconds: i64,
}

#[derive(Clone, Copy, Deserialize)]
struct Policy {
    max_per_file: i64,
    max_age_seconds: i64,
    max_checkpoint_age_seconds: i64,
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
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/vault/history-prune.json")
}

#[test]
fn go_history_prune_contracts_replay() {
    let fixture: Fixture =
        serde_json::from_slice(&fs::read(fixture_path()).expect("read Go history prune fixture"))
            .expect("decode Go history prune fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle.commit,
        "38891d35eb8ceb6c348eca9a78b3fb2873677e3d"
    );
    assert_eq!(fixture.oracle.release, "post-v0.13.0-dependency-refresh");
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
    assert_eq!(fixture.cases.len(), 4);

    for case in fixture.cases {
        let scenario = Scenario::new(&case);
        let result = execute(&scenario, &case);
        match result {
            Ok(removed) => {
                assert!(
                    case.error.is_empty(),
                    "{}: Rust succeeded but Go failed",
                    case.id
                );
                assert_eq!(removed, case.removed, "{} removed count", case.id);
            }
            Err(error) => {
                assert!(
                    error.to_string().starts_with(&case.error),
                    "{} error: Rust {:?}, Go prefix {:?}",
                    case.id,
                    error.to_string(),
                    case.error
                );
                assert_eq!(
                    error_class(&error),
                    case.error_class,
                    "{} error class",
                    case.id
                );
                assert_eq!(
                    error.removed(),
                    case.removed,
                    "{} partial removed count",
                    case.id
                );
            }
        }
        let mut expected = case.after.clone();
        if !cfg!(unix) {
            for file in &mut expected {
                file.mode = None;
            }
        }
        assert_eq!(state_of(scenario.path()), expected, "{} files", case.id);
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
    _guard: TempVault,
    store: HistoryStore,
}

impl Scenario {
    fn new(case: &Case) -> Self {
        let root = std::env::temp_dir().join(format!(
            "symdesk-history-prune-{}-{}",
            std::process::id(),
            unique_suffix()
        ));
        fs::create_dir_all(&root).expect("create temporary vault");
        for file in &case.files {
            let path = root.join(&file.path);
            if let Some(parent) = path.parent() {
                fs::create_dir_all(parent).expect("create fixture parent");
            }
            fs::write(path, &file.content).expect("write fixture file");
        }
        Self {
            _guard: TempVault(root.clone()),
            store: HistoryStore::new(root),
        }
    }

    fn path(&self) -> &Path {
        self._guard.0.as_path()
    }
}

fn unique_suffix() -> String {
    use std::sync::atomic::{AtomicU64, Ordering};
    static NEXT: AtomicU64 = AtomicU64::new(0);
    NEXT.fetch_add(1, Ordering::Relaxed).to_string()
}

fn execute(
    scenario: &Scenario,
    case: &Case,
) -> Result<usize, symdesk_vault::history::HistoryPruneError> {
    for step in &case.steps {
        match step.operation.as_str() {
            "write" => {
                let path = scenario.path().join(&step.path);
                if let Some(parent) = path.parent() {
                    fs::create_dir_all(parent).expect("create write parent");
                }
                fs::write(path, &step.content).expect("write step content");
            }
            "snapshot" => {
                scenario.store.snapshot(&step.path).expect("snapshot step");
            }
            "checkpoint" => {
                scenario
                    .store
                    .checkpoint_file(&step.task_id, &step.path)
                    .expect("checkpoint step");
            }
            "age_snapshot" => {
                let path = scenario
                    .path()
                    .join(".symdesk/history/manifest")
                    .join(format!("{}.json", step.path));
                let mut manifest: Value =
                    serde_json::from_slice(&fs::read(&path).expect("read manifest to age"))
                        .expect("decode manifest to age");
                let timestamp = age_timestamp(step.age_seconds);
                manifest[step.index]["timestamp"] = Value::String(timestamp);
                fs::write(
                    path,
                    serde_json::to_vec_pretty(&manifest).expect("encode aged manifest"),
                )
                .expect("write aged manifest");
            }
            "age_checkpoint" => {
                let path = scenario
                    .path()
                    .join(".symdesk/history/checkpoints")
                    .join(format!("{}.json", step.task_id));
                let mut checkpoint: Value =
                    serde_json::from_slice(&fs::read(&path).expect("read checkpoint to age"))
                        .expect("decode checkpoint to age");
                checkpoint["timestamp"] = Value::String(age_timestamp(step.age_seconds));
                fs::write(
                    path,
                    serde_json::to_vec_pretty(&checkpoint).expect("encode aged checkpoint"),
                )
                .expect("write aged checkpoint");
            }
            "corrupt_manifest" => {
                let path = scenario
                    .path()
                    .join(".symdesk/history/manifest")
                    .join(format!("{}.json", step.path));
                fs::write(path, &step.content).expect("write corrupt manifest");
            }
            other => panic!("unknown history prune step {other:?}"),
        }
    }

    scenario.store.prune(HistoryRetentionPolicy {
        max_per_file: case.policy.max_per_file,
        max_age: Duration::seconds(case.policy.max_age_seconds),
        max_checkpoint_age: Duration::seconds(case.policy.max_checkpoint_age_seconds),
    })
}

fn age_timestamp(seconds: i64) -> String {
    (OffsetDateTime::now_utc() - Duration::seconds(seconds))
        .format(&Rfc3339)
        .expect("format timestamp")
}

fn error_class(error: &symdesk_vault::history::HistoryPruneError) -> &'static str {
    match error.source_error() {
        HistoryError::CorruptManifest(_, _) => "corrupt_manifest",
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
            let relative = path.strip_prefix(root).expect("file under root");
            let relative = relative.to_string_lossy().replace('\\', "/");
            if relative.starts_with(".symdesk/history/objects/") {
                continue;
            }
            let data = fs::read(&path).expect("read resulting file");
            if let Some(text) = String::from_utf8(data.clone())
                .ok()
                .filter(|value| !value.contains('\u{fffd}'))
            {
                let content = normalize_text(&text);
                files.push(FileRecord {
                    path: relative,
                    mode: permission_bits(&metadata),
                    size: i64::try_from(content.len()).unwrap_or(i64::MAX),
                    sha256: String::new(),
                    content,
                });
            } else {
                files.push(FileRecord {
                    path: relative,
                    mode: permission_bits(&metadata),
                    size: i64::try_from(data.len()).unwrap_or(i64::MAX),
                    sha256: sha256_hex(&data),
                    content: String::new(),
                });
            }
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
        if trimmed.starts_with("\"timestamp\": \"") {
            output.push_str(&format!("{indent}\"timestamp\": \"{{{{timestamp}}}}\","));
        } else {
            output.push_str(line);
        }
    }
    if text.ends_with('\n') {
        output.push('\n');
    }
    output
}

fn sha256_hex(data: &[u8]) -> String {
    symdesk_vault::sha256::digest(data)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}
