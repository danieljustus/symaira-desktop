#![deny(unsafe_code)]

//! Replays the Go-owned note-verb harness for contract row VAULT-004
//! (`internal/service/port_noteops_contract_test.go`, fixture
//! `testdata/port/vault/note-operations.json`): create, edit, move and delete.
//!
//! Compared per case: the resulting Markdown file set (paths, permission bits,
//! sizes, content hashes and normalized bytes), the trash entry a delete
//! produces, and the Go error wrapper/class each verb must reproduce.

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use symdesk_vault::{
    HistoryStore, MutationError, NoteError, create_note, move_note, set_property, sha256,
};
use time::OffsetDateTime;

const CREATED_PLACEHOLDER: &str = "{{CREATED}}";
const DELETED_AT_PLACEHOLDER: &str = "{{DELETED_AT}}";
const ASN_GUARD: &str = "use \"symdesk doc asn <file> <next|N>\" to assign an ASN safely";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    oracle: Oracle,
    source_hashes: BTreeMap<String, String>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    verb: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    from: String,
    #[serde(default)]
    to: String,
    #[serde(default)]
    key: String,
    #[serde(default)]
    value: String,
    #[serde(default)]
    subject: String,
    #[serde(default)]
    setup: Option<Vec<SetupEntry>>,
    vault_mode: u32,
    before: State,
    after: State,
    #[serde(default)]
    error_wrapper: String,
    #[serde(default)]
    error_class: String,
    #[serde(default)]
    result: Option<String>,
    trash: Option<Trash>,
    platform: String,
}

#[derive(Deserialize)]
struct SetupEntry {
    path: String,
    kind: String,
    mode: Option<u32>,
    #[serde(default)]
    content_base64: String,
}

#[derive(Deserialize)]
struct State {
    markdown: Vec<FileRecord>,
}

#[derive(Debug, Deserialize)]
struct FileRecord {
    path: String,
    mode: Option<u32>,
    size: i64,
    sha256: String,
    #[serde(default)]
    content: String,
}

#[derive(Deserialize)]
struct Trash {
    name: String,
    original_path: String,
    size: i64,
    content_sha256: String,
    metadata_base64: String,
    deleted_at_present: bool,
}

#[test]
fn generated_go_note_verbs_match_bytes_modes_paths_and_trash_entries() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/vault/note-operations.json"
    ))
    .expect("decode note operation fixture");
    assert_eq!(fixture.schema_version, 1);
    assert!(!fixture.oracle.commit.is_empty());
    assert!(!fixture.oracle.release.is_empty());
    assert!(!fixture.source_hashes.is_empty());
    assert_eq!(fixture.cases.len(), 16, "fixture lost cases");

    let mut mismatches = Vec::new();
    let mut executed = 0;
    for case in &fixture.cases {
        if case.platform == "unix" && !cfg!(unix) {
            continue;
        }
        mismatches.extend(replay(case));
        executed += 1;
    }
    let runnable = fixture
        .cases
        .iter()
        .filter(|case| case.platform != "unix" || cfg!(unix))
        .count();
    assert_eq!(executed, runnable, "fixture cases were skipped");
    assert!(
        mismatches.is_empty(),
        "note operation parity mismatches:\n{}",
        mismatches.join("\n")
    );
}

fn replay(case: &Case) -> Vec<String> {
    let root = OwnedTempDir::new(&case.id);
    prepare(&root.path, case);

    let mut mismatches = Vec::new();
    compare(
        &mut mismatches,
        &case.id,
        "before markdown",
        &render_markdown(&case.before.markdown),
        &render_markdown(&state_of(&root.path)),
    );

    let mut result = String::new();
    let mut trash_entry: Option<ObservedTrash> = None;
    let failure = match case.verb.as_str() {
        "new" => match create_note(
            &root.path,
            &case.title,
            &case.content,
            OffsetDateTime::now_utc(),
        ) {
            Ok(path) => {
                result = path;
                None
            }
            Err(error) => Some(classify_note(&error)),
        },
        "edit" => match set_property(&root.path, &case.subject, &case.key, &case.value) {
            Ok(()) => None,
            Err(error) => Some(classify_note(&error)),
        },
        "move" => match move_note(&root.path, &case.from, &case.to) {
            Ok(_) => None,
            Err(error) => Some(classify_note(&error)),
        },
        "delete" => match HistoryStore::new(&root.path).trash(&case.subject) {
            Ok(entry) => {
                result = entry.name.clone();
                trash_entry = Some(observe_trash(&root.path, &entry.name));
                None
            }
            Err(error) => Some(classify_history(&error)),
        },
        verb => panic!("unknown verb {verb}"),
    };

    let (class, wrapper) = match &failure {
        None => ("", ""),
        Some(value) => (value.0, value.1),
    };
    compare(
        &mut mismatches,
        &case.id,
        "error_class",
        &case.error_class,
        class,
    );
    compare(
        &mut mismatches,
        &case.id,
        "error_wrapper",
        &case.error_wrapper,
        wrapper,
    );
    compare(
        &mut mismatches,
        &case.id,
        "result",
        case.result.as_deref().unwrap_or_default(),
        &result,
    );
    compare(
        &mut mismatches,
        &case.id,
        "after markdown",
        &render_markdown(&case.after.markdown),
        &render_markdown(&state_of(&root.path)),
    );

    match (&case.trash, &trash_entry) {
        (None, None) => {}
        (Some(expected), Some(actual)) => {
            compare(
                &mut mismatches,
                &case.id,
                "trash name",
                &expected.name,
                &actual.name,
            );
            compare(
                &mut mismatches,
                &case.id,
                "trash original_path",
                &expected.original_path,
                &actual.original_path,
            );
            compare(
                &mut mismatches,
                &case.id,
                "trash size",
                &expected.size.to_string(),
                &actual.size.to_string(),
            );
            compare(
                &mut mismatches,
                &case.id,
                "trash content sha",
                &expected.content_sha256,
                &actual.content_sha256,
            );
            compare(
                &mut mismatches,
                &case.id,
                "trash deleted_at present",
                &expected.deleted_at_present.to_string(),
                &actual.deleted_at_present.to_string(),
            );
            compare(
                &mut mismatches,
                &case.id,
                "trash metadata",
                &expected.metadata_base64,
                &actual.metadata_base64,
            );
        }
        (expected, actual) => mismatches.push(format!(
            "{} trash entry: expected present={}, got present={}",
            case.id,
            expected.is_some(),
            actual.is_some()
        )),
    }
    mismatches
}

struct ObservedTrash {
    name: String,
    original_path: String,
    size: i64,
    content_sha256: String,
    metadata_base64: String,
    deleted_at_present: bool,
}

fn observe_trash(root: &Path, name: &str) -> ObservedTrash {
    let trash_dir = root.join(".symdesk").join("trash");
    let content = fs::read(trash_dir.join(name)).expect("read trashed payload");
    let metadata =
        fs::read(trash_dir.join(format!("{name}.trashinfo.json"))).expect("read trash metadata");
    let parsed: serde_json::Value =
        serde_json::from_slice(&metadata).expect("parse trash metadata JSON");
    let deleted_at = parsed
        .get("deleted_at")
        .and_then(|value| value.as_str())
        .unwrap_or_default()
        .to_owned();
    ObservedTrash {
        name: name.to_owned(),
        original_path: parsed
            .get("original_path")
            .and_then(|value| value.as_str())
            .unwrap_or_default()
            .to_owned(),
        size: parsed
            .get("size")
            .and_then(serde_json::Value::as_i64)
            .unwrap_or(-1),
        content_sha256: hex(&sha256::digest(&content)),
        metadata_base64: base64_encode(&normalise_metadata(&metadata, &deleted_at)),
        deleted_at_present: !deleted_at.is_empty(),
    }
}

/// Replaces the wall-clock deletion stamp with the fixture placeholder, exactly
/// like the Go harness does.
fn normalise_metadata(metadata: &[u8], deleted_at: &str) -> Vec<u8> {
    let text = String::from_utf8_lossy(metadata);
    text.replace(deleted_at, DELETED_AT_PLACEHOLDER)
        .into_bytes()
}

fn prepare(root: &Path, case: &Case) {
    for entry in case.setup.iter().flatten() {
        let path = root.join(entry.path.replace('/', std::path::MAIN_SEPARATOR_STR));
        if entry.kind == "directory" {
            fs::create_dir_all(&path).expect("create setup directory");
            set_mode(&path, entry.mode);
            continue;
        }
        if let Some(parent) = path.parent() {
            fs::create_dir_all(parent).expect("create setup parent");
            set_mode(parent, Some(0o750));
        }
        let content = base64_decode(&entry.content_base64).expect("decode setup content");
        fs::write(&path, content).expect("write setup file");
        set_mode(&path, entry.mode);
    }
    set_mode(root, Some(case.vault_mode));
}

fn state_of(root: &Path) -> Vec<FileRecord> {
    let mut records = Vec::new();
    collect(root, root, &mut records);
    records.sort_by(|left, right| left.path.cmp(&right.path));
    records
}

fn collect(root: &Path, current: &Path, records: &mut Vec<FileRecord>) {
    let Ok(entries) = fs::read_dir(current) else {
        return;
    };
    for entry in entries.filter_map(Result::ok) {
        let path = entry.path();
        if entry.file_type().map(|kind| kind.is_dir()).unwrap_or(false) {
            collect(root, &path, records);
            continue;
        }
        let relative = path
            .strip_prefix(root)
            .unwrap_or(&path)
            .to_string_lossy()
            .replace('\\', "/");
        if !relative.ends_with(".md") {
            continue;
        }
        let Ok(data) = fs::read(&path) else {
            continue;
        };
        let normalised = normalise_created(&data);
        let mut record = FileRecord {
            path: relative,
            mode: mode_of(&path),
            size: i64::try_from(data.len()).unwrap_or(i64::MAX),
            sha256: hex(&sha256::digest(&normalised)),
            content: String::new(),
        };
        if data.len() <= 4096 {
            record.content = String::from_utf8_lossy(&normalised).into_owned();
        }
        records.push(record);
    }
}

/// Replaces the wall-clock creation stamp with the fixture placeholder.
fn normalise_created(data: &[u8]) -> Vec<u8> {
    let text = String::from_utf8_lossy(data);
    let Some(start) = text.find("created: \"") else {
        return data.to_vec();
    };
    let value_start = start + "created: \"".len();
    let Some(offset) = text[value_start..].find('"') else {
        return data.to_vec();
    };
    let value_end = value_start + offset;
    let mut output = String::with_capacity(text.len());
    output.push_str(&text[..value_start]);
    output.push_str(CREATED_PLACEHOLDER);
    output.push_str(&text[value_end..]);
    output.into_bytes()
}

fn render_markdown(records: &[FileRecord]) -> String {
    records
        .iter()
        .map(|record| {
            format!(
                "{} {} size={} sha={} content={}",
                record.path,
                render_mode(record.mode),
                record.size,
                record.sha256,
                record.content
            )
        })
        .collect::<Vec<_>>()
        .join(" | ")
}

/// Permission bits are only part of the contract where the platform reports
/// them; on Windows both sides render the same empty string.
fn render_mode(mode: Option<u32>) -> String {
    if cfg!(unix) {
        format!("mode={mode:?}")
    } else {
        String::new()
    }
}

fn compare(mismatches: &mut Vec<String>, id: &str, label: &str, expected: &str, actual: &str) {
    if expected != actual {
        mismatches.push(format!(
            "{id} {label}: expected {expected:?}, got {actual:?}"
        ));
    }
}

fn classify_note(error: &NoteError) -> (&'static str, &'static str) {
    match error {
        NoteError::AsnGuard => ("asn_guard", ASN_GUARD),
        NoteError::InvalidPath(_) => ("invalid_path", ""),
        NoteError::Create { .. } => ("write_failed", "failed to write file: "),
        NoteError::Move { .. } => ("move_failed", "failed to move file: "),
        NoteError::Mutation(mutation) => classify_mutation(mutation),
    }
}

fn classify_mutation(error: &MutationError) -> (&'static str, &'static str) {
    match error {
        MutationError::Read { source } if source.kind() == std::io::ErrorKind::NotFound => {
            ("not_found", "read file: ")
        }
        MutationError::Read { .. } => ("filesystem", "read file: "),
        MutationError::TempCreate { .. } => ("create_temp", "create temp file: "),
        MutationError::TempRename { .. } => ("rename_temp", "rename temp file: "),
        MutationError::TempWrite { .. } => ("write_temp", "write temp file: "),
        MutationError::TempSync { .. } => ("sync_temp", "sync temp file: "),
        MutationError::Write { .. } => ("write_failed", "write file: "),
        MutationError::Marshal { .. } => ("filesystem", "marshal value: "),
    }
}

fn classify_history(error: &symdesk_vault::HistoryError) -> (&'static str, &'static str) {
    use symdesk_vault::HistoryError as E;
    match error {
        E::TrashDirectory(_) => ("trash_directory", "cannot trash a directory: "),
        E::InvalidPath(_) => ("invalid_path", ""),
        E::Io(source) if source.kind() == std::io::ErrorKind::NotFound => ("not_found", ""),
        _ => ("filesystem", ""),
    }
}

fn mode_of(path: &Path) -> Option<u32> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::metadata(path)
            .ok()
            .map(|metadata| metadata.permissions().mode() & 0o777)
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        None
    }
}

fn set_mode(path: &Path, mode: Option<u32>) {
    #[cfg(unix)]
    if let Some(mode) = mode {
        use std::os::unix::fs::PermissionsExt;
        let _ = fs::set_permissions(path, fs::Permissions::from_mode(mode));
    }
    #[cfg(not(unix))]
    {
        let _ = (path, mode);
    }
}

fn hex(bytes: &[u8]) -> String {
    let mut output = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        output.push_str(&format!("{byte:02x}"));
    }
    output
}

struct OwnedTempDir {
    path: PathBuf,
}

static TEMP_DIR_COUNTER: AtomicU64 = AtomicU64::new(0);

impl OwnedTempDir {
    fn new(id: &str) -> Self {
        let stamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let counter = TEMP_DIR_COUNTER.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "symdesk-note-operation-{id}-{}-{stamp}-{counter}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create private case directory");
        Self { path }
    }
}

impl Drop for OwnedTempDir {
    fn drop(&mut self) {
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let _ = fs::set_permissions(&self.path, fs::Permissions::from_mode(0o750));
            if let Ok(entries) = fs::read_dir(&self.path) {
                for entry in entries.filter_map(Result::ok) {
                    let _ = fs::set_permissions(entry.path(), fs::Permissions::from_mode(0o750));
                }
            }
        }
        let _ = fs::remove_dir_all(&self.path);
    }
}

mod base64 {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

    fn value(byte: u8) -> Option<u8> {
        match byte {
            b'A'..=b'Z' => Some(byte - b'A'),
            b'a'..=b'z' => Some(byte - b'a' + 26),
            b'0'..=b'9' => Some(byte - b'0' + 52),
            b'+' => Some(62),
            b'/' => Some(63),
            _ => None,
        }
    }

    pub(super) fn encode(input: &[u8]) -> String {
        let mut output = String::new();
        for chunk in input.chunks(3) {
            let first = chunk[0];
            output.push(TABLE[(first >> 2) as usize] as char);
            let second = chunk.get(1).copied();
            output.push(TABLE[((first & 3) << 4 | second.unwrap_or(0) >> 4) as usize] as char);
            if let Some(second) = second {
                output.push(
                    TABLE[((second & 15) << 2 | chunk.get(2).copied().unwrap_or(0) >> 6) as usize]
                        as char,
                );
            } else {
                output.push('=');
            }
            if let Some(third) = chunk.get(2) {
                output.push(TABLE[(third & 63) as usize] as char);
            } else {
                output.push('=');
            }
        }
        output
    }

    pub(super) fn decode(input: &str) -> Result<Vec<u8>, ()> {
        let bytes = input.as_bytes();
        if bytes.is_empty() {
            return Ok(Vec::new());
        }
        let (chunks, remainder) = bytes.as_chunks::<4>();
        if !remainder.is_empty() {
            return Err(());
        }
        let mut output = Vec::with_capacity(bytes.len() / 4 * 3);
        for chunk in chunks {
            let a = value(chunk[0]).ok_or(())?;
            let b = value(chunk[1]).ok_or(())?;
            output.push(a << 2 | b >> 4);
            if chunk[2] != b'=' {
                let c = value(chunk[2]).ok_or(())?;
                output.push(b << 4 | c >> 2);
                if chunk[3] != b'=' {
                    let d = value(chunk[3]).ok_or(())?;
                    output.push(c << 6 | d);
                }
            }
        }
        Ok(output)
    }
}

use base64::{decode as base64_decode, encode as base64_encode};
