#![deny(unsafe_code)]

//! Replays the Go-owned history lifecycle harness (contract row VAULT-006,
//! `internal/history/port_lifecycle_contract_test.go`, fixture
//! `testdata/port/vault/history-lifecycle.json`) against the Rust port.
//!
//! Every case records what the real Go history engine did: the operation, its
//! rendered result or exact error text, an error class, and the resulting vault
//! file set (modes, sizes, normalised content). The Rust store must reproduce
//! all of it; wall-clock stamps are compared as placeholders, so the replay is
//! deterministic and does not need a live oracle process.

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

use serde::Deserialize;
use symdesk_vault::history::{HistoryError, HistoryStore, TrashEntry};
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

/// Deserialises a JSON array, mapping JSON `null` to an empty vector the way
/// Go's `json.Unmarshal` does for a nil slice.
fn de_vec_or_null<'de, D, T>(deserializer: D) -> Result<Vec<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de>,
{
    Ok(Option::<Vec<T>>::deserialize(deserializer)?.unwrap_or_default())
}

#[derive(Clone, Deserialize)]
struct Case {
    id: String,
    description: String,
    operation: String,
    platform: String,
    #[serde(deserialize_with = "de_vec_or_null", default)]
    files: Vec<FileSpec>,
    call: Call,
    #[serde(default)]
    result: String,
    #[serde(default)]
    error: String,
    #[serde(default)]
    error_class: String,
    #[serde(deserialize_with = "de_vec_or_null", default)]
    after: Vec<FileRecord>,
}

#[derive(Clone, Deserialize)]
struct FileSpec {
    path: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    content_base64: String,
    #[serde(default)]
    mode: Option<u32>,
}

#[derive(Clone, Default, Deserialize)]
struct Call {
    #[serde(default)]
    task_id: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    name: String,
    #[serde(default)]
    max_age_seconds: i64,
    #[serde(default)]
    write_content: String,
    #[serde(default)]
    create_path: String,
    #[serde(default)]
    create_content: String,
    #[serde(default)]
    extra_task_id: String,
    #[serde(default)]
    extra_trash_path: String,
    #[serde(default)]
    purge_meta_age_seconds: i64,
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
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/vault/history-lifecycle.json")
}

/// Replaces wall-clock stamps exactly like the Go harness does, so recorded
/// artifacts compare as placeholders.
fn normalise_text(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    for (index, line) in text.lines().enumerate() {
        if index > 0 {
            out.push('\n');
        }
        let trimmed = line.trim_start();
        let indent = &line[..line.len() - trimmed.len()];
        let stamp = trimmed
            .strip_prefix("\"timestamp\": \"")
            .and_then(|rest| rest.strip_suffix("\","))
            .map(|_| "\"timestamp\": \"{{timestamp}}\",");
        let deletion = trimmed
            .strip_prefix("\"deleted_at\": \"")
            .and_then(|rest| rest.strip_suffix("\","))
            .map(|_| "\"deleted_at\": \"{{deleted_at}}\",");
        if let Some(replacement) = stamp.or(deletion) {
            out.push_str(indent);
            out.push_str(replacement);
            continue;
        }
        out.push_str(line);
    }
    if text.ends_with('\n') {
        out.push('\n');
    }
    out
}

struct Scenario {
    guard: TempVault,
    store: HistoryStore,
}

impl Scenario {
    fn path(&self) -> &Path {
        &self.guard.0
    }
}

struct TempVault(PathBuf);

impl Drop for TempVault {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn bench_root(name: &str) -> TempVault {
    let mut base = std::env::temp_dir();
    base.push(format!(
        "symdesk-history-lifecycle-{name}-{}-{}",
        std::process::id(),
        unique_counter()
    ));
    fs::create_dir_all(&base).expect("create temp vault root");
    TempVault(base)
}

fn unique_counter() -> u64 {
    use std::sync::atomic::{AtomicU64, Ordering};
    static COUNTER: AtomicU64 = AtomicU64::new(0);
    COUNTER.fetch_add(1, Ordering::SeqCst)
}

impl Scenario {
    fn new(case: &Case) -> Self {
        let guard = bench_root(&case.id);
        let root = &guard;
        for spec in &case.files {
            let target = root.0.join(Path::new(&spec.path));
            if let Some(parent) = target.parent() {
                fs::create_dir_all(parent).expect("create parent dir");
            }
            let data = if spec.content_base64.is_empty() {
                spec.content.as_bytes().to_vec()
            } else {
                decode_hex(&spec.content_base64)
            };
            fs::write(&target, data).expect("write fixture file");
            if let Some(mode) = spec.mode {
                set_mode(&target, mode);
            }
        }
        let store = HistoryStore::with_clock(&guard.0, monotonic_clock());
        Self { guard, store }
    }
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    let _ = fs::set_permissions(path, fs::Permissions::from_mode(mode));
}

/// Permission bits are a Unix concept; the Go harness records no mode on
/// Windows and neither does the replay.
#[cfg(not(unix))]
fn set_mode(_path: &Path, _mode: u32) {}

/// Returns an always-increasing clock so ordering results are deterministic and
/// match the Go run's real-time ordering without sleeping.
fn monotonic_clock() -> impl Fn() -> OffsetDateTime + Send + Sync + 'static {
    use std::sync::atomic::{AtomicI64, Ordering};
    let base = OffsetDateTime::from_unix_timestamp(1_767_225_600).expect("fixed base timestamp");
    let offset = AtomicI64::new(0);
    move || base + Duration::seconds(offset.fetch_add(1, Ordering::SeqCst))
}

fn decode_hex(text: &str) -> Vec<u8> {
    let mut out = Vec::with_capacity(text.len() / 2);
    let bytes = text.as_bytes();
    let mut index = 0;
    while index + 1 < bytes.len() {
        let high = (bytes[index] as char).to_digit(16).expect("hex digit");
        let low = (bytes[index + 1] as char).to_digit(16).expect("hex digit");
        out.push((high * 16 + low) as u8);
        index += 2;
    }
    out
}

fn state_of(root: &Path) -> Vec<FileRecord> {
    let mut out = Vec::new();
    collect(root, root, &mut out);
    out.sort_by(|left, right| left.path.cmp(&right.path));
    out
}

fn collect(root: &Path, dir: &Path, out: &mut Vec<FileRecord>) {
    let entries = match fs::read_dir(dir) {
        Ok(entries) => entries,
        Err(_) => return,
    };
    for entry in entries.flatten() {
        let path = entry.path();
        let relative = path
            .strip_prefix(root)
            .expect("path under root")
            .to_string_lossy()
            .replace('\\', "/");
        if relative.starts_with(".symdesk/history/objects/") {
            continue;
        }
        let metadata = match fs::symlink_metadata(&path) {
            Ok(metadata) => metadata,
            Err(_) => continue,
        };
        if metadata.is_dir() {
            collect(root, &path, out);
            continue;
        }
        let data = fs::read(&path).expect("read recorded file");
        let mut record = FileRecord {
            path: relative,
            mode: permission_bits(&metadata),
            size: i64::try_from(data.len()).unwrap_or(i64::MAX),
            sha256: sha256_hex(&data),
            content: String::new(),
        };
        let text = String::from_utf8(data).ok();
        if let Some(text) = text.filter(|text| !text.contains('\u{fffd}')) {
            record.content = normalise_text(&text);
            record.sha256 = String::new();
            record.size = i64::try_from(record.content.len()).unwrap_or(i64::MAX);
        }
        out.push(record);
    }
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

fn sha256_hex(data: &[u8]) -> String {
    let digest = symdesk_vault::sha256::digest(data);
    let mut out = String::with_capacity(64);
    for byte in digest {
        out.push_str(&format!("{byte:02x}"));
    }
    out
}

fn error_class(err: &HistoryError) -> String {
    match err {
        HistoryError::TaskIdRequired => "task_id_required",
        HistoryError::InvalidTaskId(_) => "invalid_task_id",
        HistoryError::CorruptCheckpoint(_, _) => "corrupt_checkpoint",
        HistoryError::TrashDirectory(_) => "trash_directory",
        HistoryError::TrashNameInvalid(_) => "invalid_trash_name",
        HistoryError::TrashItemNotFound { .. } => "trash_not_found",
        HistoryError::CorruptTrashMetadata(_, _) => "corrupt_trash_metadata",
        HistoryError::TrashInventory(_) => "trash_inventory",
        HistoryError::TrashRestoreConflict { .. } => "trash_restore_conflict",
        HistoryError::TrashMetadataNameMismatch { .. }
        | HistoryError::TrashMetadataPathMismatch { .. }
        | HistoryError::TrashMetadataInvalid(_)
        | HistoryError::TrashPayloadSizeMismatch(_) => "trash_metadata_mismatch",
        HistoryError::InvalidPath(_) => "invalid_path",
        _ => "other",
    }
    .to_owned()
}

/// Mirrors `historyErrorText` in the Go harness: OS-produced tails and
/// third-party decoder wording are dropped and covered by the class instead.
fn error_text(err: &HistoryError) -> String {
    let message = err.to_string();
    match err {
        HistoryError::TrashItemNotFound { .. } => {
            if let Some(index) = message.find(" not found: ") {
                return message[..index + " not found: ".len()].to_owned();
            }
        }
        HistoryError::CorruptTrashMetadata(_, _) | HistoryError::CorruptCheckpoint(_, _) => {
            if let Some(index) = message.find(": ") {
                return message[..index + 2].to_owned();
            }
        }
        _ => {}
    }
    message
}

fn render_checkpoint(checkpoint: &symdesk_vault::history::Checkpoint) -> String {
    let files = checkpoint
        .files
        .iter()
        .map(|file| format!("{}@{}", file.rel_path, file.entry.id))
        .collect::<Vec<_>>()
        .join(" ");
    format!(
        "task={} files=[{}] new=[{}] skipped=[{}] partial={}",
        checkpoint.task_id,
        files,
        checkpoint.new_files.join(" "),
        checkpoint.skipped.join(" "),
        checkpoint.partial()
    )
}

fn checkpoints_dir(root: &Path) -> PathBuf {
    root.join(".symdesk/history/checkpoints")
}

fn trash_dir(root: &Path) -> PathBuf {
    root.join(".symdesk/trash")
}

fn execute(scenario: &Scenario, case: &Case) -> Result<String, HistoryError> {
    let store = &scenario.store;
    let root = scenario.path();
    match case.id.as_str() {
        "checkpoint-begin" => {
            let checkpoint = store.begin_checkpoint(&case.call.task_id)?;
            Ok(render_checkpoint(&checkpoint))
        }
        "checkpoint-begin-idempotent" => {
            let first = store.begin_checkpoint(&case.call.task_id)?;
            let second = store.begin_checkpoint(&case.call.task_id)?;
            Ok(format!(
                "same_timestamp={} same_files={}",
                first.timestamp == second.timestamp,
                second.files.is_empty()
            ))
        }
        "checkpoint-file-existing" => {
            store.checkpoint_file(&case.call.task_id, &case.call.path)?;
            fs::write(root.join("notes/a.md"), b"rewritten\n").expect("rewrite note");
            let checkpoint = store.checkpoint_file(&case.call.task_id, &case.call.path)?;
            let blob = store.content(&checkpoint.files[0].entry.id)?;
            Ok(format!(
                "files={} new={} skipped={} blob={:?}",
                checkpoint.files.len(),
                checkpoint.new_files.len(),
                checkpoint.skipped.len(),
                String::from_utf8_lossy(&blob)
            ))
        }
        "checkpoint-file-new" => {
            let checkpoint = store.checkpoint_file(&case.call.task_id, &case.call.path)?;
            Ok(render_checkpoint(&checkpoint))
        }
        "checkpoint-undo" => {
            store.checkpoint_file(&case.call.task_id, &case.call.path)?;
            store.checkpoint_file(&case.call.task_id, &case.call.create_path)?;
            fs::write(root.join("notes/a.md"), case.call.write_content.as_bytes())
                .expect("write task edit");
            fs::write(
                root.join("notes/created.md"),
                case.call.create_content.as_bytes(),
            )
            .expect("write task creation");
            let checkpoint = store.undo_checkpoint(&case.call.task_id)?;
            Ok(render_checkpoint(&checkpoint))
        }
        "checkpoint-invalid-task-id" => {
            let mut rejected = Vec::new();
            for task_id in ["", "../escape", ".hidden", "a/b", "a:b", ".", ".."] {
                if let Err(err) = store.begin_checkpoint(task_id) {
                    rejected.push(format!("{task_id:?}={}", error_class(&err)));
                }
            }
            if rejected.len() != 7 {
                return Err(HistoryError::Io(std::io::Error::other(format!(
                    "only {} of 7 invalid task ids were rejected",
                    rejected.len()
                ))));
            }
            Ok(rejected.join(" "))
        }
        "checkpoint-list" => {
            let first = store.begin_checkpoint(&case.call.task_id)?;
            let second = store.begin_checkpoint(&case.call.extra_task_id)?;
            fs::write(checkpoints_dir(root).join("broken.json"), b"{not json")
                .expect("write corrupt manifest");
            let checkpoints = store.list_checkpoints()?;
            Ok(format!(
                "count={} first={} second={} newer={}",
                checkpoints.len(),
                checkpoints[0].task_id,
                checkpoints[1].task_id,
                second.timestamp > first.timestamp
            ))
        }
        "trash-list-empty" => {
            let entries = store.trash_list()?;
            let strict = store.trash_list_strict()?;
            Ok(format!("list={} strict={}", entries.len(), strict.len()))
        }
        "trash-list-order" => {
            store.trash(&case.call.path)?;
            store.trash(&case.call.extra_trash_path)?;
            let entries = store.trash_list()?;
            let strict = store.trash_list_strict()?;
            Ok(format!(
                "count={} strict={} first={} second={}",
                entries.len(),
                strict.len(),
                entries[0].original_path,
                entries[1].original_path
            ))
        }
        "trash-list-strict-corrupt-metadata" => {
            let entry = store.trash(&case.call.path)?;
            let lenient = store.trash_list()?;
            assert_eq!(lenient.len(), 1, "lenient listing should still see it");
            fs::write(
                trash_dir(root).join(format!("{}{}", entry.name, ".trashinfo.json")),
                b"{not json",
            )
            .expect("corrupt metadata");
            store.trash_list_strict()?;
            Ok("strict accepted corrupt metadata".to_owned())
        }
        "trash-list-strict-orphan-payload" => {
            store.trash(&case.call.path)?;
            fs::write(trash_dir(root).join("orphan.md"), b"orphan\n")
                .expect("write orphan payload");
            store.trash_list_strict()?;
            Ok("strict accepted an orphan payload".to_owned())
        }
        "trash-restore" => {
            let entry = store.trash(&case.call.path)?;
            let restored = store.trash_restore(&entry.name)?;
            Ok(format!(
                "name={} original={} size={}",
                restored.name, restored.original_path, restored.size
            ))
        }
        "trash-restore-conflict" => {
            let entry = store.trash(&case.call.path)?;
            fs::write(root.join("notes/a.md"), b"replacement\n").expect("occupying file");
            store.trash_restore(&entry.name)?;
            Ok("restore overwrote the occupied path".to_owned())
        }
        "trash-restore-missing" => {
            store.trash_list_strict()?;
            store.trash_restore(&case.call.name)?;
            Ok("restore accepted an unknown item".to_owned())
        }
        "trash-restore-invalid-name" => {
            for name in ["../escape", "a/b", ".", ".."] {
                if store.trash_restore(name).is_ok() {
                    return Ok(format!("restore accepted name {name:?}"));
                }
            }
            store.trash_restore("../escape")?;
            Ok(String::new())
        }
        "trash-purge-all" => {
            store.trash(&case.call.path)?;
            store.trash(&case.call.extra_trash_path)?;
            let purged = store.trash_purge(Duration::seconds(case.call.max_age_seconds))?;
            let remaining = store.trash_list_strict()?.len();
            Ok(format!("purged={purged} remaining={remaining}"))
        }
        "trash-purge-by-age" => {
            let old = store.trash(&case.call.path)?;
            store.trash("notes/fresh.md")?;
            let aged = TrashEntry {
                deleted_at: old.deleted_at - Duration::seconds(case.call.purge_meta_age_seconds),
                ..old.clone()
            };
            let payload = serde_json::to_vec_pretty(&aged).expect("encode aged metadata");
            fs::write(
                trash_dir(root).join(format!("{}{}", aged.name, ".trashinfo.json")),
                payload,
            )
            .expect("write aged metadata");
            let purged = store.trash_purge(Duration::seconds(case.call.max_age_seconds))?;
            let entries = store.trash_list_strict()?;
            Ok(format!(
                "purged={purged} remaining={} kept={}",
                entries.len(),
                entries[0].original_path
            ))
        }
        "trash-purge-refuses-corrupt" => {
            let entry = store.trash(&case.call.path)?;
            fs::write(
                trash_dir(root).join(format!("{}{}", entry.name, ".trashinfo.json")),
                b"{}",
            )
            .expect("write empty metadata");
            store.trash_purge(Duration::seconds(case.call.max_age_seconds))?;
            Ok("purge accepted a corrupt inventory".to_owned())
        }
        other => panic!("case {other} has no Rust replay step"),
    }
}

#[test]
fn generated_go_history_lifecycle_matches_rust_bytes_and_side_effects() {
    let path = fixture_path();
    let data = fs::read(&path).unwrap_or_else(|err| panic!("read {path:?}: {err}"));
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse fixture");
    assert_eq!(fixture.schema_version, 1, "unexpected fixture schema");
    assert!(
        fixture.oracle.commit.len() == 40,
        "oracle commit must be pinned"
    );
    assert!(
        fixture
            .source_hashes
            .contains_key("internal/history/trash.go"),
        "fixture must hash the ported Go sources"
    );
    assert!(
        !fixture.oracle.release.is_empty(),
        "oracle block must pin the Go release"
    );
    for case in &fixture.cases {
        assert!(
            !case.description.is_empty(),
            "case {} must state the contract it pins",
            case.id
        );
        assert!(
            !case.operation.is_empty(),
            "case {} must name the ported entry point",
            case.id
        );
    }

    let mut mismatches: Vec<String> = Vec::new();
    let mut executed = 0usize;
    for case in &fixture.cases {
        if case.platform == "unix" && !cfg!(unix) {
            continue;
        }
        executed += 1;
        let scenario = Scenario::new(case);
        let outcome = execute(&scenario, case);
        match (outcome, case.error.is_empty()) {
            (Ok(result), true) if result == case.result => {}
            (Ok(result), true) => mismatches.push(format!(
                "{}: result mismatch\n  go:   {:?}\n  rust: {:?}",
                case.id, case.result, result
            )),
            (Ok(result), false) => mismatches.push(format!(
                "{}: Go failed with {:?} but Rust returned {:?}",
                case.id, case.error, result
            )),
            (Err(err), true) => mismatches.push(format!(
                "{}: Rust failed with {err:?} but Go succeeded with {:?}",
                case.id, case.result
            )),
            (Err(err), false) => {
                if error_text(&err) != case.error {
                    mismatches.push(format!(
                        "{}: error mismatch\n  go:   {:?}\n  rust: {:?}\n  raw:  {}",
                        case.id,
                        case.error,
                        error_text(&err),
                        err
                    ));
                }
                if error_class(&err) != case.error_class {
                    mismatches.push(format!(
                        "{}: error class mismatch\n  go:   {:?}\n  rust: {:?}",
                        case.id,
                        case.error_class,
                        error_class(&err)
                    ));
                }
            }
        }

        // The recorded file set is compared for every case, success or not,
        // on the very run that produced the outcome above.
        let expected: Vec<FileRecord> = case
            .after
            .clone()
            .into_iter()
            .map(normalise_record)
            .collect();
        let actual = state_of(scenario.path());
        if expected != actual {
            mismatches.push(format!(
                "{}: file set mismatch\n  go:   {expected:#?}\n  rust: {actual:#?}",
                case.id
            ));
        }
        drop(scenario);
    }

    let runnable = fixture
        .cases
        .iter()
        .filter(|case| case.platform != "unix" || cfg!(unix))
        .count();
    assert_eq!(executed, runnable, "fixture cases were skipped");
    assert!(
        mismatches.is_empty(),
        "history lifecycle parity mismatches ({} of {} cases):\n{}",
        mismatches.len(),
        executed,
        mismatches.join("\n")
    );
    println!("history lifecycle parity: {executed} cases matched the Go oracle");
}

fn normalise_record(mut record: FileRecord) -> FileRecord {
    if !cfg!(unix) {
        record.mode = None;
    }
    record.content = normalise_text(&record.content);
    record
}
