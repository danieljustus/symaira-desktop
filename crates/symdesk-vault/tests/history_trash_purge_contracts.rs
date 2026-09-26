#![deny(unsafe_code)]

//! Replays the Go-owned selected trash purge fixture.

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
    #[serde(default)]
    files: Vec<FileSpec>,
    #[serde(default)]
    result: String,
    #[serde(default)]
    error: String,
    #[serde(default)]
    error_class: String,
    #[serde(default)]
    after: Vec<FileRecord>,
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

fn fixture_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/vault/history-trash-purge.json")
}

#[test]
fn go_selected_trash_purge_contracts_replay() {
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(fixture_path()).expect("read selected trash purge fixture"),
    )
    .expect("decode selected trash purge fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle.commit,
        "e0364e835c03672178db936a2263fba1fb1ec2ab"
    );
    assert_eq!(fixture.oracle.release, "post-v0.12.2-security-880");
    assert!(
        fixture
            .source_hashes
            .contains_key("internal/history/trash.go")
    );
    assert!(
        fixture
            .source_hashes
            .contains_key("internal/history/history.go")
    );
    assert_eq!(fixture.cases.len(), 6);

    for case in fixture.cases {
        let scenario = Scenario::new(&case);
        match execute(&scenario, &case) {
            Ok(result) => {
                assert!(
                    case.error.is_empty(),
                    "{}: Rust succeeded but Go failed",
                    case.id
                );
                assert_eq!(result, case.result, "{} result", case.id);
            }
            Err(error) => {
                assert_eq!(error_text(&error), case.error, "{} error", case.id);
                assert_eq!(
                    error_class(&error),
                    case.error_class,
                    "{} error class",
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
        assert!(!case.description.is_empty());
    }
}

#[test]
fn mixed_stale_selection_rejects_before_any_deletion() {
    let guard = temp_root("mixed-selector");
    fs::write(guard.0.join("a.md"), b"alpha").unwrap();
    fs::write(guard.0.join("b.md"), b"bravo").unwrap();
    let store = HistoryStore::with_clock(&guard.0, monotonic_clock());
    let first = store.trash("a.md").unwrap();
    let second = store.trash("b.md").unwrap();
    let mut stale = second.clone();
    stale.original_path = "elsewhere.md".to_owned();
    assert!(matches!(
        store.purge_trash_entries(&[first.clone(), stale]),
        Err(HistoryError::TrashEntryOriginalPathChanged(_))
    ));
    assert!(
        guard
            .0
            .join(format!(".symdesk/trash/{}", first.name))
            .exists()
    );
    assert!(
        guard
            .0
            .join(format!(".symdesk/trash/{}", second.name))
            .exists()
    );
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
        "symdesk-selected-trash-purge-{name}-{}-{}",
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
        "selected-purge-keeps-unselected-and-retries-idempotently" => {
            let selected = store.trash("a.md")?;
            store.trash("b.md")?;
            let first = store.purge_trash_entries(std::slice::from_ref(&selected))?;
            let retry = store.purge_trash_entries(std::slice::from_ref(&selected))?;
            let mut names: Vec<_> = store
                .trash_list_strict()?
                .into_iter()
                .map(|entry| entry.name)
                .collect();
            names.sort();
            Ok(format!(
                "first={first} retry={retry} remaining={}",
                names.join(",")
            ))
        }
        "selected-purge-corrupt-unselected-metadata-preserves-all" => {
            let selected = store.trash("a.md")?;
            let other = store.trash("b.md")?;
            fs::write(
                root.join(format!(".symdesk/trash/{}.trashinfo.json", other.name)),
                b"null",
            )
            .expect("replace unselected metadata with null");
            store.purge_trash_entries(std::slice::from_ref(&selected))?;
            Ok("purge accepted corrupt unselected inventory".to_owned())
        }
        "selected-purge-replaced-metadata-path-preserves-all" => {
            let selected = store.trash("a.md")?;
            let mut replaced = selected.clone();
            replaced.original_path = "elsewhere.md".to_owned();
            fs::write(
                root.join(format!(".symdesk/trash/{}.trashinfo.json", selected.name)),
                serde_json::to_vec_pretty(&replaced).expect("encode replacement metadata"),
            )
            .expect("replace metadata");
            store.purge_trash_entries(std::slice::from_ref(&selected))?;
            Ok("purge accepted changed metadata path".to_owned())
        }
        "selected-purge-replaced-payload-preserves-all" => {
            let selected = store.trash("a.md")?;
            fs::write(
                root.join(format!(".symdesk/trash/{}", selected.name)),
                b"replacement payload",
            )
            .expect("replace payload");
            store.purge_trash_entries(std::slice::from_ref(&selected))?;
            Ok("purge accepted replaced payload".to_owned())
        }
        "selected-purge-missing-metadata-preserves-all" => {
            let selected = store.trash("a.md")?;
            fs::remove_file(root.join(format!(".symdesk/trash/{}.trashinfo.json", selected.name)))
                .expect("remove metadata");
            store.purge_trash_entries(std::slice::from_ref(&selected))?;
            Ok("purge accepted payload without metadata".to_owned())
        }
        "selected-purge-missing-payload-preserves-all" => {
            let selected = store.trash("a.md")?;
            fs::remove_file(root.join(format!(".symdesk/trash/{}", selected.name)))
                .expect("remove payload");
            store.purge_trash_entries(std::slice::from_ref(&selected))?;
            Ok("purge accepted metadata without payload".to_owned())
        }
        other => Err(HistoryError::Io(std::io::Error::other(format!(
            "unknown fixture case {other}"
        )))),
    }
}

fn state_of(root: &Path) -> Vec<FileRecord> {
    fn visit(root: &Path, path: &Path, records: &mut Vec<FileRecord>) {
        let mut entries: Vec<_> = fs::read_dir(path)
            .expect("read vault state")
            .map(Result::unwrap)
            .collect();
        entries.sort_by_key(|entry| entry.file_name());
        for entry in entries {
            let path = entry.path();
            let metadata = fs::symlink_metadata(&path).expect("stat vault state");
            if metadata.is_dir() {
                visit(root, &path, records);
                continue;
            }
            let relative = path.strip_prefix(root).expect("vault-relative state path");
            let relative = relative.to_string_lossy().replace('\\', "/");
            if relative.starts_with(".symdesk/history/objects/") {
                continue;
            }
            let data = fs::read(&path).expect("read vault state file");
            let mut record = FileRecord {
                path: relative,
                mode: file_mode(&metadata),
                size: i64::try_from(data.len()).unwrap_or(i64::MAX),
                sha256: sha256_hex(&data),
                content: String::new(),
            };
            if let Ok(text) = String::from_utf8(data)
                && !text.contains('\u{fffd}')
            {
                record.content = normalise_text(&text);
                record.sha256.clear();
                record.size = i64::try_from(record.content.len()).unwrap_or(i64::MAX);
            }
            records.push(record);
        }
    }
    let mut out = Vec::new();
    visit(root, root, &mut out);
    out.sort_by(|left, right| left.path.cmp(&right.path));
    out
}

fn normalise_text(text: &str) -> String {
    let mut out = String::new();
    for (index, line) in text.lines().enumerate() {
        if index > 0 {
            out.push('\n');
        }
        let trimmed = line.trim_start();
        let indent = &line[..line.len() - trimmed.len()];
        let replacement = [
            ("\"timestamp\": \"", "\"timestamp\": \"{{timestamp}}\","),
            ("\"deleted_at\": \"", "\"deleted_at\": \"{{deleted_at}}\","),
        ]
        .into_iter()
        .find_map(|(prefix, value)| {
            trimmed
                .strip_prefix(prefix)
                .and_then(|rest| rest.strip_suffix("\",").map(|_| value))
        });
        if let Some(replacement) = replacement {
            out.push_str(indent);
            out.push_str(replacement);
        } else {
            out.push_str(line);
        }
    }
    if text.ends_with('\n') {
        out.push('\n');
    }
    out
}

#[cfg(unix)]
fn file_mode(metadata: &fs::Metadata) -> Option<u32> {
    use std::os::unix::fs::PermissionsExt;
    Some(metadata.permissions().mode() & 0o777)
}

#[cfg(not(unix))]
fn file_mode(_metadata: &fs::Metadata) -> Option<u32> {
    None
}

fn sha256_hex(data: &[u8]) -> String {
    let mut output = String::new();
    for byte in symdesk_vault::sha256::digest(data) {
        use std::fmt::Write as _;
        let _ = write!(output, "{byte:02x}");
    }
    output
}

fn error_class(error: &HistoryError) -> String {
    match error {
        HistoryError::CorruptTrashMetadata(_, _) => "corrupt_trash_metadata",
        HistoryError::TrashInventory(_) => "trash_inventory",
        HistoryError::TrashMetadataNameMismatch { .. }
        | HistoryError::TrashMetadataPathMismatch { .. }
        | HistoryError::TrashMetadataInvalid(_)
        | HistoryError::TrashPayloadSizeMismatch(_) => "trash_metadata_mismatch",
        HistoryError::TrashEntryOriginalPathChanged(_) => "other",
        _ => "other",
    }
    .to_owned()
}

fn error_text(error: &HistoryError) -> String {
    let text = error.to_string();
    if let HistoryError::CorruptTrashMetadata(_, _) = error
        && let Some(index) = text.find(": ")
    {
        return text[..index + 2].to_owned();
    }
    text
}
