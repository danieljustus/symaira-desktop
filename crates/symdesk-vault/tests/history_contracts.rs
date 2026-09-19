#![deny(unsafe_code)]

//! Contract tests for symdesk-vault::history comparing against the live Go history oracle.

use std::{
    collections::{BTreeMap, VecDeque},
    fs,
    path::{Path, PathBuf},
    sync::{Arc, Mutex},
};

use serde::{Deserialize, Deserializer, Serialize};
use symdesk_vault::{HistoryError, HistoryStore};
use time::OffsetDateTime;
use time::format_description::well_known::Rfc3339;

const PINNED_SOURCE_HASHES: &[(&str, &str)] = &[
    (
        "go.mod",
        "7a5ca8c06f9e71762c05b7d7b47e74bad9f93bdf8332f0fd3ab16758ccd32bf1",
    ),
    (
        "go.sum",
        "943bc31c96f838bb7cb8378a0f81f8340ddaa9201b1b2a5247a8b6ae28a054a9",
    ),
    (
        "internal/history/checkpoint.go",
        "147f7c08ff4cb00bfaf1271a478dca2631146ef92fb1d54114f7b2a725a31acd",
    ),
    (
        "internal/history/history.go",
        "f86d38d5983d759a95d80f4a55aad8b7c8af572b0ba4ae4e30702b7a301364d4",
    ),
    (
        "internal/history/trash.go",
        "a18e604c203c6cee550078fa8b37acef68c9e79a9f07482288a4b59344f01179",
    ),
];

const EXPECTED_ORACLE_OPERATION_COUNT: usize = 56;
const EXPECTED_ORACLE_COMMIT: &str = "982fe718f2d64629102b4078b1d65a46645c90c5";
const EXPECTED_ORACLE_RELEASE: &str = "unreleased-982fe718";

fn deserialize_option_option<'de, D, T>(deserializer: D) -> Result<Option<Option<T>>, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de>,
{
    Deserialize::deserialize(deserializer).map(Some)
}

#[derive(Deserialize, Serialize, Debug, Clone)]
struct OracleDocument {
    schema_version: u32,
    oracle: OracleMeta,
    #[serde(default)]
    source_hashes: BTreeMap<String, String>,
    initial_files: Vec<FileSpec>,
    operations: Vec<OperationRecord>,
    final_filesystem: Vec<FsEntry>,
}

#[derive(Deserialize, Serialize, Debug, Clone)]
struct OracleMeta {
    commit: String,
    release: String,
}

#[derive(Deserialize, Serialize, Debug, Clone)]
struct FileSpec {
    path: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    content_base64: String,
}

#[derive(Deserialize, Serialize, Debug, Clone)]
struct OperationRecord {
    step: usize,
    op: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    id: String,
    #[serde(default)]
    content: String,
    #[serde(default)]
    content_base64: String,
    #[serde(default)]
    created_timestamps: Vec<String>,
    snapshot_result: Option<EntryDTO>,
    #[serde(default, deserialize_with = "deserialize_option_option")]
    list_result: Option<Option<Vec<EntryDTO>>>,
    #[serde(default)]
    content_result_base64: String,
    restore_result: Option<EntryDTO>,
    #[serde(default)]
    error: String,
    #[serde(default)]
    error_class: String,
}

#[derive(Deserialize, Serialize, Debug, PartialEq, Eq, Clone)]
struct EntryDTO {
    id: String,
    timestamp: String,
    size: i64,
}

#[derive(Deserialize, Serialize, Debug, Clone)]
struct FsEntry {
    path: String,
    mode: u32,
    size: i64,
    #[serde(default)]
    sha256: String,
    #[serde(default)]
    content_base64: String,
    is_dir: bool,
}

fn is_strict_lowercase_hex_64(s: &str) -> bool {
    s.len() == 64
        && s.bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
}

fn validate_oracle_metadata(doc: &OracleDocument) -> Result<(), String> {
    if doc.schema_version != 1 {
        return Err(format!(
            "oracle schema_version must be 1, found {}",
            doc.schema_version
        ));
    }
    if doc.oracle.commit != EXPECTED_ORACLE_COMMIT {
        return Err(format!(
            "oracle commit mismatch: expected {EXPECTED_ORACLE_COMMIT}, found {:?}",
            doc.oracle.commit
        ));
    }
    if doc.oracle.release != EXPECTED_ORACLE_RELEASE {
        return Err(format!(
            "oracle release mismatch: expected {EXPECTED_ORACLE_RELEASE}, found {:?}",
            doc.oracle.release
        ));
    }
    if doc.source_hashes.len() != PINNED_SOURCE_HASHES.len() {
        return Err(format!(
            "source_hashes keyset length mismatch: expected exact {} keys, found {}",
            PINNED_SOURCE_HASHES.len(),
            doc.source_hashes.len()
        ));
    }
    for &(expected_path, expected_hash) in PINNED_SOURCE_HASHES {
        let hash = match doc.source_hashes.get(expected_path) {
            Some(h) => h,
            None => {
                return Err(format!(
                    "missing mandatory source hash entry for {expected_path}"
                ));
            }
        };
        if !is_strict_lowercase_hex_64(hash) {
            return Err(format!(
                "source hash for {expected_path} is not strict 64-character lowercase hex: {hash:?}"
            ));
        }
        if hash != expected_hash {
            return Err(format!(
                "source hash for {expected_path} differs from pinned Git blob: expected {expected_hash}, found {hash}"
            ));
        }
    }
    for key in doc.source_hashes.keys() {
        if !PINNED_SOURCE_HASHES.iter().any(|&(k, _)| k == key.as_str()) {
            return Err(format!("unexpected extra source hash key: {key}"));
        }
    }
    Ok(())
}

fn validate_oracle_operations(operations: &[OperationRecord]) -> Result<(), String> {
    if operations.is_empty() {
        return Err("declared operations must not be empty".to_string());
    }
    if operations.len() != EXPECTED_ORACLE_OPERATION_COUNT {
        return Err(format!(
            "declared operations count mismatch: expected {EXPECTED_ORACLE_OPERATION_COUNT}, found {}",
            operations.len()
        ));
    }
    for (idx, op) in operations.iter().enumerate() {
        let expected_step = idx + 1;
        if op.step != expected_step {
            return Err(format!(
                "step sequence mismatch at index {idx}: expected step {expected_step}, found {}",
                op.step
            ));
        }
        if op.op.trim().is_empty() {
            return Err(format!("step {}: op string must not be empty", op.step));
        }
    }
    Ok(())
}

fn resolve_safe_fixture_path(root: &Path, rel_path: &str) -> Result<PathBuf, String> {
    if rel_path.is_empty() {
        return Err("empty fixture path rejected".to_string());
    }
    let p = Path::new(rel_path);
    if p.is_absolute() {
        return Err(format!("absolute fixture path rejected: {rel_path:?}"));
    }
    let mut normalized = PathBuf::new();
    for comp in p.components() {
        match comp {
            std::path::Component::Prefix(_) | std::path::Component::RootDir => {
                return Err(format!(
                    "root or prefix component in fixture path rejected: {rel_path:?}"
                ));
            }
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                if !normalized.pop() {
                    return Err(format!(
                        "traversal escaping fixture root rejected: {rel_path:?}"
                    ));
                }
            }
            std::path::Component::Normal(c) => {
                normalized.push(c);
            }
        }
    }
    if normalized.as_os_str().is_empty() {
        return Err(format!("fixture path resolves to empty/root: {rel_path:?}"));
    }
    Ok(root.join(normalized))
}

fn path_to_slash(path: &Path) -> String {
    #[cfg(windows)]
    {
        path.to_string_lossy().replace('\\', "/")
    }
    #[cfg(not(windows))]
    {
        path.to_string_lossy().into_owned()
    }
}

fn create_harness_dir_all(path: &Path) -> std::io::Result<()> {
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o750);
    }
    builder.create(path)
}

fn create_vault_root_exclusive(path: &Path) -> std::io::Result<()> {
    let mut builder = fs::DirBuilder::new();
    builder.recursive(false);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder.create(path)
}

#[derive(Debug)]
struct TempVaultGuard {
    path: PathBuf,
}

impl TempVaultGuard {
    fn new(prefix: &str) -> Self {
        let rand_bytes =
            symdesk_vault::history::random_12_bytes().expect("random bytes for temp vault");
        let hex_suffix: String = rand_bytes.iter().map(|b| format!("{b:02x}")).collect();
        let path =
            std::env::temp_dir().join(format!("{prefix}-{}-{hex_suffix}", std::process::id()));
        create_vault_root_exclusive(&path).expect("create unique temp vault root");
        Self { path }
    }

    fn from_path_exclusive(path: PathBuf) -> std::io::Result<Self> {
        create_vault_root_exclusive(&path)?;
        Ok(Self { path })
    }
}

impl Drop for TempVaultGuard {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}

fn decode_base64(input: &str) -> Result<Vec<u8>, String> {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut decode_table = [0xFFu8; 256];
    for (i, &b) in TABLE.iter().enumerate() {
        decode_table[b as usize] = i as u8;
    }

    let clean: Vec<u8> = input
        .bytes()
        .filter(|&b| !b.is_ascii_whitespace())
        .collect();

    if clean.is_empty() {
        return Ok(Vec::new());
    }

    if !clean.len().is_multiple_of(4) {
        return Err(format!("invalid base64 length: {}", clean.len()));
    }

    let mut output = Vec::with_capacity((clean.len() / 4) * 3);
    for chunk in clean.as_chunks::<4>().0 {
        let b0 = chunk[0];
        let b1 = chunk[1];
        let b2 = chunk[2];
        let b3 = chunk[3];

        let v0 = decode_table[b0 as usize];
        let v1 = decode_table[b1 as usize];
        if v0 == 0xFF || v1 == 0xFF {
            return Err("invalid base64 char".to_owned());
        }

        if b2 == b'=' {
            if b3 != b'=' {
                return Err("invalid base64 padding".to_owned());
            }
            output.push((v0 << 2) | (v1 >> 4));
        } else if b3 == b'=' {
            let v2 = decode_table[b2 as usize];
            if v2 == 0xFF {
                return Err("invalid base64 char".to_owned());
            }
            output.push((v0 << 2) | (v1 >> 4));
            output.push((v1 << 4) | (v2 >> 2));
        } else {
            let v2 = decode_table[b2 as usize];
            let v3 = decode_table[b3 as usize];
            if v2 == 0xFF || v3 == 0xFF {
                return Err("invalid base64 char".to_owned());
            }
            output.push((v0 << 2) | (v1 >> 4));
            output.push((v1 << 4) | (v2 >> 2));
            output.push((v2 << 6) | v3);
        }
    }

    Ok(output)
}

struct ActualFsEntry {
    mode: u32,
    size: i64,
    sha256: String,
    bytes: Vec<u8>,
    is_dir: bool,
}

fn collect_actual_fs(root: &Path) -> BTreeMap<String, ActualFsEntry> {
    let mut entries = BTreeMap::new();
    let mut stack = vec![root.to_path_buf()];

    while let Some(current) = stack.pop() {
        let read_dir = fs::read_dir(&current)
            .unwrap_or_else(|err| panic!("read_dir failed for {current:?}: {err}"));

        for entry_res in read_dir {
            let dir_entry = entry_res
                .unwrap_or_else(|err| panic!("dir entry read failed in {current:?}: {err}"));
            let p = dir_entry.path();
            let rel = path_to_slash(
                p.strip_prefix(root)
                    .unwrap_or_else(|err| panic!("strip prefix failed for {p:?}: {err}")),
            );
            let metadata = fs::symlink_metadata(&p)
                .unwrap_or_else(|err| panic!("symlink metadata failed for {p:?}: {err}"));
            let is_dir = metadata.is_dir();
            #[cfg(unix)]
            let mode = {
                use std::os::unix::fs::PermissionsExt;
                metadata.permissions().mode() & 0o777
            };
            #[cfg(windows)]
            let mode = {
                let readonly = metadata.permissions().readonly();
                let mut m = if readonly { 0o444 } else { 0o666 };
                let is_symlink = metadata.file_type().is_symlink();
                if is_dir && !is_symlink {
                    m |= 0o111;
                }
                m
            };
            #[cfg(not(any(unix, windows)))]
            let mode = {
                let readonly = metadata.permissions().readonly();
                let mut m = if readonly { 0o444 } else { 0o666 };
                if is_dir {
                    m |= 0o111;
                }
                m
            };

            if is_dir {
                entries.insert(
                    rel,
                    ActualFsEntry {
                        mode,
                        size: metadata.len() as i64,
                        sha256: String::new(),
                        bytes: Vec::new(),
                        is_dir: true,
                    },
                );
                stack.push(p);
            } else {
                let bytes = fs::read(&p)
                    .unwrap_or_else(|err| panic!("read file bytes failed for {p:?}: {err}"));
                let sha = symdesk_vault::sha256_hex(&bytes);
                entries.insert(
                    rel,
                    ActualFsEntry {
                        mode,
                        size: bytes.len() as i64,
                        sha256: sha,
                        bytes,
                        is_dir: false,
                    },
                );
            }
        }
    }

    entries
}

#[test]
#[ignore = "live differential requires SYMDESK_HISTORY_ORACLE path generated by scripts/rust-port/cmd/historygen"]
fn test_history_live_differential_against_go_oracle() {
    let oracle_path = match std::env::var("SYMDESK_HISTORY_ORACLE") {
        Ok(val) if !val.trim().is_empty() => PathBuf::from(val.trim()),
        _ => panic!(
            "SYMDESK_HISTORY_ORACLE environment variable is not set.\n\
             To run this live differential test:\n  \
               go run ./scripts/rust-port/cmd/historygen --output /tmp/history-oracle.json\n  \
               SYMDESK_HISTORY_ORACLE=/tmp/history-oracle.json cargo test -p symdesk-vault --test history_contracts -- --ignored"
        ),
    };

    let report_data = fs::read_to_string(&oracle_path)
        .unwrap_or_else(|err| panic!("read oracle report at {oracle_path:?}: {err}"));
    let report: OracleDocument = serde_json::from_str(&report_data)
        .unwrap_or_else(|err| panic!("parse oracle report JSON: {err}"));

    validate_oracle_metadata(&report)
        .unwrap_or_else(|err| panic!("validate oracle metadata: {err}"));
    validate_oracle_operations(&report.operations)
        .unwrap_or_else(|err| panic!("validate oracle operations: {err}"));

    let temp_vault = TempVaultGuard::new("symdesk-history-contract-replay");

    // Populate initial files
    for file in &report.initial_files {
        let target = resolve_safe_fixture_path(&temp_vault.path, &file.path)
            .unwrap_or_else(|err| panic!("initial file path {}: {err}", file.path));
        if let Some(parent) = target.parent() {
            create_harness_dir_all(parent).expect("create initial file parent dir");
        }
        let data = if !file.content_base64.is_empty() {
            decode_base64(&file.content_base64)
                .unwrap_or_else(|err| panic!("decode initial file base64: {err}"))
        } else {
            file.content.as_bytes().to_vec()
        };
        fs::write(target, data).expect("write initial file");
    }

    // Prepare injectable deterministic clock with strict queue management (no fallback)
    let clock_queue: Arc<Mutex<VecDeque<OffsetDateTime>>> = Arc::new(Mutex::new(VecDeque::new()));
    let cb_queue = Arc::clone(&clock_queue);
    let store = HistoryStore::with_clock(&temp_vault.path, move || {
        let mut q = cb_queue.lock().unwrap();
        q.pop_front().unwrap_or_else(|| {
            panic!("timestamp clock callback invoked but timestamp queue is empty! Operation was missing explicit created_timestamp.")
        })
    });

    // Replay operations in exact order
    for op in &report.operations {
        // Enqueue explicit newly created timestamps for this operation
        {
            let mut q = clock_queue.lock().unwrap();
            assert!(
                q.is_empty(),
                "step {}: leftover timestamps in queue before operation: {:?}",
                op.step,
                *q
            );
            for ts_str in &op.created_timestamps {
                let dt = OffsetDateTime::parse(ts_str, &Rfc3339).unwrap_or_else(|err| {
                    panic!(
                        "step {}: parse created_timestamp {ts_str:?}: {err}",
                        op.step
                    )
                });
                q.push_back(dt);
            }
        }

        match op.op.as_str() {
            "write_raw" => {
                let target =
                    resolve_safe_fixture_path(&temp_vault.path, &op.path).unwrap_or_else(|err| {
                        panic!("step {}: write_raw path {}: {err}", op.step, op.path)
                    });
                if let Some(parent) = target.parent() {
                    let _ = create_harness_dir_all(parent);
                }
                let data = if !op.content_base64.is_empty() {
                    decode_base64(&op.content_base64).unwrap_or_else(|err| {
                        panic!("step {}: decode write_raw content_base64: {err}", op.step)
                    })
                } else {
                    op.content.as_bytes().to_vec()
                };
                fs::write(target, data)
                    .unwrap_or_else(|err| panic!("step {}: write raw file: {err}", op.step));
            }
            "snapshot" => {
                let res = store.snapshot(&op.path);
                if !op.error_class.is_empty() {
                    let err = res.expect_err(&format!("step {}: expected error", op.step));
                    assert_eq!(
                        classify_rust_error(&err),
                        op.error_class,
                        "step {}: error class mismatch",
                        op.step
                    );
                    assert!(
                        !err.to_string().is_empty(),
                        "step {}: error message must not be empty",
                        op.step
                    );
                    assert!(
                        !op.error.is_empty(),
                        "step {}: raw error string in oracle report must not be empty",
                        op.step
                    );
                } else {
                    let entry = res.unwrap_or_else(|err| {
                        panic!("step {}: snapshot failed unexpectedly: {err}", op.step)
                    });
                    match (entry, &op.snapshot_result) {
                        (Some(e), Some(expected)) => {
                            assert_eq!(e.id, expected.id, "step {}: id match", op.step);
                            assert_eq!(e.size, expected.size, "step {}: size match", op.step);
                            assert_eq!(
                                symdesk_vault::history::format_rfc3339_nano(e.timestamp),
                                expected.timestamp,
                                "step {}: timestamp match",
                                op.step
                            );
                        }
                        (None, None) => {}
                        (actual, expected) => {
                            panic!(
                                "step {}: snapshot mismatch actual={actual:?}, expected={expected:?}",
                                op.step
                            );
                        }
                    }
                }
            }
            "list" => {
                let res = store.list(&op.path);
                if !op.error_class.is_empty() {
                    let err = res.expect_err(&format!("step {}: expected error", op.step));
                    assert_eq!(
                        classify_rust_error(&err),
                        op.error_class,
                        "step {}: error class mismatch",
                        op.step
                    );
                    assert!(
                        !err.to_string().is_empty(),
                        "step {}: error message must not be empty",
                        op.step
                    );
                    assert!(
                        !op.error.is_empty(),
                        "step {}: raw error string in oracle report must not be empty",
                        op.step
                    );
                } else {
                    let actual_opt = res.unwrap_or_else(|err| {
                        panic!("step {}: list failed unexpectedly: {err}", op.step)
                    });
                    match (actual_opt, &op.list_result) {
                        (None, Some(None)) => {
                            // Manifest was absent or JSON null
                        }
                        (Some(actual_entries), Some(Some(expected_entries))) => {
                            assert_eq!(
                                actual_entries.len(),
                                expected_entries.len(),
                                "step {}: list count mismatch",
                                op.step
                            );
                            for (actual, expected) in actual_entries.iter().zip(expected_entries) {
                                assert_eq!(
                                    actual.id, expected.id,
                                    "step {}: list item id",
                                    op.step
                                );
                                assert_eq!(
                                    actual.size, expected.size,
                                    "step {}: list item size",
                                    op.step
                                );
                                assert_eq!(
                                    symdesk_vault::history::format_rfc3339_nano(actual.timestamp),
                                    expected.timestamp,
                                    "step {}: list item timestamp",
                                    op.step
                                );
                            }
                        }
                        (actual, expected) => {
                            panic!(
                                "step {}: list result mismatch actual={actual:?}, expected={expected:?}",
                                op.step
                            );
                        }
                    }
                }
            }
            "content" => {
                let res = store.content(&op.id);
                if !op.error_class.is_empty() {
                    let err = res.expect_err(&format!("step {}: expected error", op.step));
                    assert_eq!(
                        classify_rust_error(&err),
                        op.error_class,
                        "step {}: error class mismatch",
                        op.step
                    );
                    assert!(
                        !err.to_string().is_empty(),
                        "step {}: error message must not be empty",
                        op.step
                    );
                    assert!(
                        !op.error.is_empty(),
                        "step {}: raw error string in oracle report must not be empty",
                        op.step
                    );
                } else {
                    let bytes = res.unwrap_or_else(|err| {
                        panic!("step {}: content failed unexpectedly: {err}", op.step)
                    });
                    let expected_bytes =
                        decode_base64(&op.content_result_base64).unwrap_or_else(|err| {
                            panic!("step {}: decode expected content base64: {err}", op.step)
                        });
                    assert_eq!(bytes, expected_bytes, "step {}: content bytes", op.step);
                }
            }
            "restore" => {
                let id_opt = if op.id.is_empty() {
                    None
                } else {
                    Some(op.id.as_str())
                };
                let res = store.restore(&op.path, id_opt);
                if !op.error_class.is_empty() {
                    let err = res.expect_err(&format!("step {}: expected error", op.step));
                    assert_eq!(
                        classify_rust_error(&err),
                        op.error_class,
                        "step {}: error class mismatch",
                        op.step
                    );
                    assert!(
                        !err.to_string().is_empty(),
                        "step {}: error message must not be empty",
                        op.step
                    );
                    assert!(
                        !op.error.is_empty(),
                        "step {}: raw error string in oracle report must not be empty",
                        op.step
                    );
                } else {
                    let entry = res.unwrap_or_else(|err| {
                        panic!("step {}: restore failed unexpectedly: {err}", op.step)
                    });
                    let expected = op
                        .restore_result
                        .as_ref()
                        .expect("restore result in oracle");
                    assert_eq!(entry.id, expected.id, "step {}: restore id", op.step);
                    assert_eq!(entry.size, expected.size, "step {}: restore size", op.step);
                    assert_eq!(
                        symdesk_vault::history::format_rfc3339_nano(entry.timestamp),
                        expected.timestamp,
                        "step {}: restore timestamp",
                        op.step
                    );
                }
            }
            other => panic!("unknown operation in oracle report: {other}"),
        }

        // Verify that this operation consumed all its scheduled timestamps
        {
            let q = clock_queue.lock().unwrap();
            assert!(
                q.is_empty(),
                "step {}: operation did not consume all scheduled timestamps (leftover: {:?})",
                op.step,
                *q
            );
        }
    }

    // Exact final filesystem inventory comparison (extra/missing paths, sizes, modes, contents)
    let actual_fs = collect_actual_fs(&temp_vault.path);
    let mut expected_fs = BTreeMap::new();
    for entry in &report.final_filesystem {
        expected_fs.insert(entry.path.clone(), entry);
    }

    let actual_paths: Vec<_> = actual_fs.keys().cloned().collect();
    let expected_paths: Vec<_> = expected_fs.keys().cloned().collect();
    assert_eq!(
        actual_paths, expected_paths,
        "Final filesystem inventory mismatch:\nActual:   {actual_paths:?}\nExpected: {expected_paths:?}"
    );

    for (path, actual) in &actual_fs {
        let expected = expected_fs.get(path).unwrap();
        assert_eq!(
            actual.is_dir, expected.is_dir,
            "path {path} is_dir mismatch"
        );
        assert_eq!(
            actual.mode, expected.mode,
            "path {path} mode mismatch (actual={:#o}, expected={:#o})",
            actual.mode, expected.mode
        );

        if !actual.is_dir {
            assert_eq!(actual.size, expected.size, "path {path} size mismatch");
            assert_eq!(
                actual.sha256, expected.sha256,
                "path {path} sha256 mismatch"
            );
            if !expected.content_base64.is_empty() {
                let expected_bytes = decode_base64(&expected.content_base64)
                    .unwrap_or_else(|err| panic!("decode expected base64 for {path}: {err}"));
                assert_eq!(
                    actual.bytes, expected_bytes,
                    "path {path} byte-for-byte mismatch"
                );
            }
        }
    }
}

fn classify_rust_error(err: &HistoryError) -> &'static str {
    match err {
        HistoryError::InvalidPath(..) => "invalid_path",
        HistoryError::InvalidId(..) => "invalid_id",
        HistoryError::NotFound(..) => "not_found",
        HistoryError::NoSnapshots(..) => "no_snapshots",
        HistoryError::AmbiguousPrefix { .. } => "ambiguous_prefix",
        HistoryError::NoSnapshot { .. } => "no_snapshot",
        HistoryError::CorruptManifest(..) => "corrupt_manifest",
        HistoryError::TrashDirectory(..) => "trash_directory",
        HistoryError::RenameFailed { .. } => "rename_failed",
        HistoryError::Io(..) => "other",
    }
}

#[test]
fn test_operation_record_list_result_deserialization() {
    // 1. Missing / absent field => outer None
    let json_missing = r#"{"step": 1, "op": "list"}"#;
    let rec_missing: OperationRecord =
        serde_json::from_str(json_missing).expect("parse missing list_result");
    assert_eq!(rec_missing.list_result, None);

    // 2. Present null => Some(None)
    let json_null = r#"{"step": 2, "op": "list", "list_result": null}"#;
    let rec_null: OperationRecord =
        serde_json::from_str(json_null).expect("parse null list_result");
    assert_eq!(rec_null.list_result, Some(None));

    // 3. Present empty array [] => Some(Some(vec![]))
    let json_empty = r#"{"step": 3, "op": "list", "list_result": []}"#;
    let rec_empty: OperationRecord =
        serde_json::from_str(json_empty).expect("parse empty array list_result");
    assert_eq!(rec_empty.list_result, Some(Some(vec![])));

    // 4. Present one valid item => Some(Some(vec![...]))
    let json_one_item = r#"{
        "step": 4,
        "op": "list",
        "list_result": [
            {
                "id": "e0123456789a",
                "timestamp": "2026-09-15T04:00:00Z",
                "size": 128
            }
        ]
    }"#;
    let rec_one_item: OperationRecord =
        serde_json::from_str(json_one_item).expect("parse one item list_result");
    assert_eq!(
        rec_one_item.list_result,
        Some(Some(vec![EntryDTO {
            id: "e0123456789a".to_string(),
            timestamp: "2026-09-15T04:00:00Z".to_string(),
            size: 128,
        }]))
    );

    // 5. Present zero / omitted-field default item from Go oracle
    let json_default_item = r#"{
        "step": 42,
        "op": "list",
        "list_result": [
            {
                "id": "",
                "timestamp": "0001-01-01T00:00:00Z",
                "size": 0
            }
        ]
    }"#;
    let rec_default_item: OperationRecord =
        serde_json::from_str(json_default_item).expect("parse default item list_result");
    assert_eq!(
        rec_default_item.list_result,
        Some(Some(vec![EntryDTO {
            id: String::new(),
            timestamp: "0001-01-01T00:00:00Z".to_string(),
            size: 0,
        }]))
    );

    // 6. Present duplicate non-null then null item from Go oracle (step 46)
    let json_dup_item = r#"{
        "step": 46,
        "op": "list",
        "list_result": [
            {
                "id": "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
                "timestamp": "2026-09-15T04:00:00Z",
                "size": 128
            }
        ]
    }"#;
    let rec_dup_item: OperationRecord =
        serde_json::from_str(json_dup_item).expect("parse dup item list_result");
    assert_eq!(
        rec_dup_item.list_result,
        Some(Some(vec![EntryDTO {
            id: "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae".to_string(),
            timestamp: "2026-09-15T04:00:00Z".to_string(),
            size: 128,
        }]))
    );

    // 7. Present null entry element item from Go oracle (step 50)
    let json_null_elem = r#"{
        "step": 50,
        "op": "list",
        "list_result": [
            {
                "id": "",
                "timestamp": "0001-01-01T00:00:00Z",
                "size": 0
            }
        ]
    }"#;
    let rec_null_elem: OperationRecord =
        serde_json::from_str(json_null_elem).expect("parse null element list_result");
    assert_eq!(
        rec_null_elem.list_result,
        Some(Some(vec![EntryDTO {
            id: String::new(),
            timestamp: "0001-01-01T00:00:00Z".to_string(),
            size: 0,
        }]))
    );

    // 8. Reject wrong field types
    let invalid_payloads = [
        r#"{"step": 5, "op": "list", "list_result": "invalid_string"}"#,
        r#"{"step": 6, "op": "list", "list_result": 42}"#,
        r#"{"step": 7, "op": "list", "list_result": true}"#,
        r#"{"step": 8, "op": "list", "list_result": {}}"#,
        r#"{"step": 9, "op": "list", "list_result": [123]}"#,
        r#"{"step": 10, "op": "list", "list_result": ["string_item"]}"#,
        r#"{"step": 11, "op": "list", "list_result": [{"id": 123}]}"#,
        r#"{"step": 12, "op": "list", "list_result": [{"id": "abc"}]}"#,
    ];
    for invalid_json in invalid_payloads {
        let res: Result<OperationRecord, _> = serde_json::from_str(invalid_json);
        assert!(
            res.is_err(),
            "expected serde error for invalid payload: {invalid_json}"
        );
    }

    // 9. Present timezone offsets +05:30 and -00:30 from Go oracle
    let json_offsets = r#"{
        "step": 53,
        "op": "list",
        "list_result": [
            {
                "id": "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
                "timestamp": "2026-09-15T15:04:05.123456+05:30",
                "size": 128
            },
            {
                "id": "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
                "timestamp": "2026-09-15T15:04:05.123456-00:30",
                "size": 256
            }
        ]
    }"#;
    let rec_offsets: OperationRecord =
        serde_json::from_str(json_offsets).expect("parse offsets list_result");
    assert_eq!(
        rec_offsets.list_result,
        Some(Some(vec![
            EntryDTO {
                id: "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae".to_string(),
                timestamp: "2026-09-15T15:04:05.123456+05:30".to_string(),
                size: 128,
            },
            EntryDTO {
                id: "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae".to_string(),
                timestamp: "2026-09-15T15:04:05.123456-00:30".to_string(),
                size: 256,
            }
        ]))
    );
}

#[test]
fn test_harness_mkdir_and_temp_vault_guard_collision() {
    // 1. Verify exclusive creation and 0o700 mode of TempVaultGuard root
    let temp_vault = TempVaultGuard::new("test-harness-temp-vault");
    assert!(temp_vault.path.is_dir());
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let root_mode = fs::symlink_metadata(&temp_vault.path)
            .expect("read temp vault root metadata")
            .permissions()
            .mode()
            & 0o777;
        assert_eq!(root_mode, 0o700, "temp vault root mode must be 0o700");
    }

    // 2. Verify nested 0o750 directory creation via create_harness_dir_all
    let nested_path = temp_vault.path.join("a").join("b").join("c");
    create_harness_dir_all(&nested_path).expect("create nested harness directory structure");
    assert!(nested_path.is_dir());
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        for dir in &[
            temp_vault.path.join("a"),
            temp_vault.path.join("a").join("b"),
            nested_path.clone(),
        ] {
            let mode = fs::symlink_metadata(dir)
                .expect("read nested dir metadata")
                .permissions()
                .mode()
                & 0o777;
            assert_eq!(mode, 0o750, "nested dir {:?} mode must be 0o750", dir);
        }
    }

    // 3. Verify root exclusive collision rejection without destructive removal of foreign dir
    let rand_bytes =
        symdesk_vault::history::random_12_bytes().expect("random bytes for foreign dir");
    let hex_suffix: String = rand_bytes.iter().map(|b| format!("{b:02x}")).collect();
    let foreign_dir = std::env::temp_dir().join(format!(
        "symdesk-foreign-preexisting-{}-{hex_suffix}",
        std::process::id()
    ));
    fs::create_dir(&foreign_dir).expect("create foreign preexisting dir");
    let sentinel_file = foreign_dir.join("sentinel.txt");
    let sentinel_data = b"preexisting foreign payload";
    fs::write(&sentinel_file, sentinel_data).expect("write foreign sentinel file");

    // Attempting exclusive create_dir on an existing path must fail with AlreadyExists
    let collision_err = create_vault_root_exclusive(&foreign_dir)
        .expect_err("exclusive create_dir on preexisting path must return error");
    assert_eq!(
        collision_err.kind(),
        std::io::ErrorKind::AlreadyExists,
        "collision error kind must be AlreadyExists"
    );

    // Attempting TempVaultGuard::from_path_exclusive on existing path must fail without constructing/dropping guard
    let guard_err = TempVaultGuard::from_path_exclusive(foreign_dir.clone())
        .expect_err("TempVaultGuard::from_path_exclusive on preexisting path must fail");
    assert_eq!(
        guard_err.kind(),
        std::io::ErrorKind::AlreadyExists,
        "guard error kind must be AlreadyExists"
    );

    // Verify foreign directory and its contents were NOT destroyed or removed
    assert!(foreign_dir.is_dir(), "foreign directory must still exist");
    assert!(sentinel_file.is_file(), "sentinel file must still exist");
    let content = fs::read(&sentinel_file).expect("read foreign sentinel file");
    assert_eq!(
        content, sentinel_data,
        "sentinel file content must remain intact"
    );

    // Clean up foreign test dir
    let _ = fs::remove_dir_all(&foreign_dir);
}

#[test]
fn test_oracle_metadata_positive_and_negative_controls() {
    let mut valid_hashes = BTreeMap::new();
    for &(path, hash) in PINNED_SOURCE_HASHES {
        valid_hashes.insert(path.to_string(), hash.to_string());
    }

    let make_doc =
        |hashes: BTreeMap<String, String>, commit: &str, release: &str, ver: u32| OracleDocument {
            schema_version: ver,
            oracle: OracleMeta {
                commit: commit.to_string(),
                release: release.to_string(),
            },
            source_hashes: hashes,
            initial_files: Vec::new(),
            operations: Vec::new(),
            final_filesystem: Vec::new(),
        };

    // 1. Positive control: valid exact 5-source metadata
    let valid_doc = make_doc(
        valid_hashes.clone(),
        EXPECTED_ORACLE_COMMIT,
        EXPECTED_ORACLE_RELEASE,
        1,
    );
    assert!(
        validate_oracle_metadata(&valid_doc).is_ok(),
        "valid oracle metadata must pass"
    );

    // 2. Negative control: altered valid-hex hash for internal/history/history.go
    let mut altered_hashes = valid_hashes.clone();
    altered_hashes.insert(
        "internal/history/history.go".to_string(),
        "0000000000000000000000000000000000000000000000000000000000000000".to_string(),
    );
    let altered_doc = make_doc(
        altered_hashes,
        EXPECTED_ORACLE_COMMIT,
        EXPECTED_ORACLE_RELEASE,
        1,
    );
    let err = validate_oracle_metadata(&altered_doc)
        .expect_err("altered valid-hex hash must fail validation");
    assert!(
        err.contains("differs from pinned Git blob"),
        "expected diff error, got: {err}"
    );

    // 3. Negative control: uppercase hex hash
    let mut upper_hashes = valid_hashes.clone();
    upper_hashes.insert(
        "go.mod".to_string(),
        "7A5CA8C06F9E71762C05B7D7B47E74BAD9F93BDF8332F0FD3AB16758CCD32BF1".to_string(),
    );
    let upper_doc = make_doc(
        upper_hashes,
        EXPECTED_ORACLE_COMMIT,
        EXPECTED_ORACLE_RELEASE,
        1,
    );
    let err =
        validate_oracle_metadata(&upper_doc).expect_err("uppercase hex hash must fail validation");
    assert!(
        err.contains("not strict 64-character lowercase hex"),
        "expected lowercase hex error, got: {err}"
    );

    // 4. Negative control: non-64 length hex hash
    let mut short_hashes = valid_hashes.clone();
    short_hashes.insert("go.sum".to_string(), "943bc31c".to_string());
    let short_doc = make_doc(
        short_hashes,
        EXPECTED_ORACLE_COMMIT,
        EXPECTED_ORACLE_RELEASE,
        1,
    );
    let err =
        validate_oracle_metadata(&short_doc).expect_err("short hex hash must fail validation");
    assert!(
        err.contains("not strict 64-character lowercase hex"),
        "expected lowercase hex error, got: {err}"
    );

    // 5. Negative control: missing mandatory source key
    let mut missing_hashes = valid_hashes.clone();
    missing_hashes.remove("go.mod");
    let missing_doc = make_doc(
        missing_hashes,
        EXPECTED_ORACLE_COMMIT,
        EXPECTED_ORACLE_RELEASE,
        1,
    );
    let err = validate_oracle_metadata(&missing_doc)
        .expect_err("missing mandatory source must fail validation");
    assert!(
        err.contains("keyset length mismatch"),
        "expected length mismatch error, got: {err}"
    );

    // 6. Negative control: extra foreign source key
    let mut extra_hashes = valid_hashes.clone();
    extra_hashes.insert(
        "foreign/extra.go".to_string(),
        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef".to_string(),
    );
    let extra_doc = make_doc(
        extra_hashes,
        EXPECTED_ORACLE_COMMIT,
        EXPECTED_ORACLE_RELEASE,
        1,
    );
    let err = validate_oracle_metadata(&extra_doc)
        .expect_err("extra foreign source key must fail validation");
    assert!(
        err.contains("keyset length mismatch"),
        "expected keyset length mismatch error, got: {err}"
    );

    // 7. Negative control: altered commit SHA
    let wrong_commit_doc = make_doc(
        valid_hashes.clone(),
        "0000000000000000000000000000000000000000",
        EXPECTED_ORACLE_RELEASE,
        1,
    );
    let err =
        validate_oracle_metadata(&wrong_commit_doc).expect_err("wrong commit must fail validation");
    assert!(
        err.contains("oracle commit mismatch"),
        "expected commit mismatch error, got: {err}"
    );

    // 8. Negative control: altered release label
    let wrong_release_doc = make_doc(valid_hashes.clone(), EXPECTED_ORACLE_COMMIT, "v1.0.0", 1);
    let err = validate_oracle_metadata(&wrong_release_doc)
        .expect_err("wrong release must fail validation");
    assert!(
        err.contains("oracle release mismatch"),
        "expected release mismatch error, got: {err}"
    );

    // 9. Negative control: wrong schema_version
    let wrong_ver_doc = make_doc(
        valid_hashes,
        EXPECTED_ORACLE_COMMIT,
        EXPECTED_ORACLE_RELEASE,
        2,
    );
    let err = validate_oracle_metadata(&wrong_ver_doc)
        .expect_err("wrong schema_version must fail validation");
    assert!(
        err.contains("schema_version must be 1"),
        "expected schema_version error, got: {err}"
    );
}

#[test]
fn test_oracle_operations_validation_and_negative_controls() {
    let make_op = |step: usize, op: &str| OperationRecord {
        step,
        op: op.to_string(),
        path: "test.md".to_string(),
        id: String::new(),
        content: String::new(),
        content_base64: String::new(),
        created_timestamps: Vec::new(),
        snapshot_result: None,
        list_result: None,
        content_result_base64: String::new(),
        restore_result: None,
        error: String::new(),
        error_class: String::new(),
    };

    // 1. Positive control: exactly 56 consecutive steps
    let valid_ops: Vec<_> = (1..=56).map(|s| make_op(s, "snapshot")).collect();
    assert!(
        validate_oracle_operations(&valid_ops).is_ok(),
        "56 consecutive operations must pass"
    );

    // 2. Negative control: truncated operation list (54 operations)
    let truncated_54: Vec<_> = (1..=54).map(|s| make_op(s, "snapshot")).collect();
    let err = validate_oracle_operations(&truncated_54)
        .expect_err("truncated 54-op list must fail validation");
    assert!(
        err.contains("count mismatch: expected 56, found 54"),
        "expected count mismatch error, got: {err}"
    );

    // 3. Negative control: truncated operation list (55 operations)
    let truncated_55: Vec<_> = (1..=55).map(|s| make_op(s, "snapshot")).collect();
    let err = validate_oracle_operations(&truncated_55)
        .expect_err("truncated 55-op list must fail validation");
    assert!(
        err.contains("count mismatch: expected 56, found 55"),
        "expected count mismatch error, got: {err}"
    );

    // 4. Negative control: empty operation list
    let empty_ops: Vec<OperationRecord> = Vec::new();
    let err = validate_oracle_operations(&empty_ops)
        .expect_err("empty operations list must fail validation");
    assert!(
        err.contains("declared operations must not be empty"),
        "expected empty error, got: {err}"
    );

    // 5. Negative control: non-consecutive / gap in step numbers
    let mut gap_ops = valid_ops.clone();
    gap_ops[2].step = 4; // gap at index 2 (expected step 3)
    let err = validate_oracle_operations(&gap_ops)
        .expect_err("non-consecutive step numbers must fail validation");
    assert!(
        err.contains("step sequence mismatch at index 2: expected step 3, found 4"),
        "expected step sequence error, got: {err}"
    );

    // 6. Negative control: empty op name
    let mut empty_op_name = valid_ops.clone();
    empty_op_name[10].op = "  ".to_string();
    let err = validate_oracle_operations(&empty_op_name)
        .expect_err("empty op string must fail validation");
    assert!(
        err.contains("step 11: op string must not be empty"),
        "expected empty op error, got: {err}"
    );
}

#[test]
fn test_resolve_safe_fixture_path_and_traversal_negative_controls() {
    #[cfg(windows)]
    let root = Path::new(r"C:\test\vault\root");
    #[cfg(not(windows))]
    let root = Path::new("/test/vault/root");

    // 1. Negative control: platform-native absolute paths rejected before fs access
    #[cfg(not(windows))]
    {
        let err = resolve_safe_fixture_path(root, "/etc/passwd")
            .expect_err("absolute Unix path must be rejected");
        assert!(
            err.contains("absolute fixture path rejected"),
            "expected absolute error, got: {err}"
        );

        let err = resolve_safe_fixture_path(root, "/abs/evil.md")
            .expect_err("absolute path must be rejected");
        assert!(
            err.contains("absolute fixture path rejected"),
            "expected absolute error, got: {err}"
        );
    }
    #[cfg(windows)]
    {
        let err = resolve_safe_fixture_path(root, r"C:\abs\evil.md")
            .expect_err("absolute Windows drive path with backslash must be rejected");
        assert!(
            err.contains("absolute fixture path rejected"),
            "expected absolute error, got: {err}"
        );

        let err = resolve_safe_fixture_path(root, "C:/abs/evil.md")
            .expect_err("absolute Windows drive path with forward slash must be rejected");
        assert!(
            err.contains("absolute fixture path rejected"),
            "expected absolute error, got: {err}"
        );

        let err = resolve_safe_fixture_path(root, r"\\server\share\evil.md")
            .expect_err("absolute Windows UNC path must be rejected");
        assert!(
            err.contains("absolute fixture path rejected"),
            "expected absolute error, got: {err}"
        );
    }

    // 2. Negative control: Windows rooted / drive-relative / prefix paths rejected before fs access
    #[cfg(windows)]
    {
        // Rooted without drive letter (e.g. /etc/passwd or \abs\evil.md)
        let err = resolve_safe_fixture_path(root, "/etc/passwd")
            .expect_err("rooted Unix-style path on Windows must be rejected via RootDir component");
        assert!(
            err.contains("root or prefix component in fixture path rejected"),
            "expected root/prefix error, got: {err}"
        );

        let err = resolve_safe_fixture_path(root, r"\abs\evil.md").expect_err(
            "rooted backslash path without drive must be rejected via RootDir component",
        );
        assert!(
            err.contains("root or prefix component in fixture path rejected"),
            "expected root/prefix error, got: {err}"
        );

        let err = resolve_safe_fixture_path(root, "/abs/evil.md").expect_err(
            "rooted forward-slash path without drive must be rejected via RootDir component",
        );
        assert!(
            err.contains("root or prefix component in fixture path rejected"),
            "expected root/prefix error, got: {err}"
        );

        // Drive-relative without root directory (e.g. C:notes\evil.md or C:evil.md)
        let err = resolve_safe_fixture_path(root, "C:notes/evil.md")
            .expect_err("drive-relative path must be rejected via Prefix component");
        assert!(
            err.contains("root or prefix component in fixture path rejected"),
            "expected root/prefix error, got: {err}"
        );

        let err = resolve_safe_fixture_path(root, r"C:evil.md")
            .expect_err("drive-relative path must be rejected via Prefix component");
        assert!(
            err.contains("root or prefix component in fixture path rejected"),
            "expected root/prefix error, got: {err}"
        );
    }

    // 3. Negative control: traversal escaping root rejected before fs access
    let err = resolve_safe_fixture_path(root, "../outside.md")
        .expect_err("parent traversal must be rejected");
    assert!(
        err.contains("traversal escaping fixture root rejected"),
        "expected traversal error, got: {err}"
    );

    let err = resolve_safe_fixture_path(root, "notes/../../evil.md")
        .expect_err("nested escaping traversal must be rejected");
    assert!(
        err.contains("traversal escaping fixture root rejected"),
        "expected traversal error, got: {err}"
    );

    #[cfg(windows)]
    {
        let err = resolve_safe_fixture_path(root, r"..\outside.md")
            .expect_err("Windows backslash parent traversal must be rejected");
        assert!(
            err.contains("traversal escaping fixture root rejected"),
            "expected traversal error, got: {err}"
        );

        let err = resolve_safe_fixture_path(root, r"notes\..\..\evil.md")
            .expect_err("Windows backslash nested escaping traversal must be rejected");
        assert!(
            err.contains("traversal escaping fixture root rejected"),
            "expected traversal error, got: {err}"
        );
    }

    // 4. Negative control: empty and dot paths rejected before fs access
    let err = resolve_safe_fixture_path(root, "").expect_err("empty path must be rejected");
    assert!(
        err.contains("empty fixture path rejected"),
        "expected empty error, got: {err}"
    );

    let err =
        resolve_safe_fixture_path(root, ".").expect_err("dot root-resolving path must be rejected");
    assert!(
        err.contains("fixture path resolves to empty/root"),
        "expected root-resolving error, got: {err}"
    );

    let err = resolve_safe_fixture_path(root, "notes/..")
        .expect_err("traversal back to root must be rejected");
    assert!(
        err.contains("fixture path resolves to empty/root"),
        "expected root-resolving error, got: {err}"
    );

    // 5. Positive controls: valid fixture paths resolve safely inside root
    let safe1 = resolve_safe_fixture_path(root, "notes/initial.md").expect("valid relative path");
    assert_eq!(safe1, root.join("notes").join("initial.md"));

    let safe2 = resolve_safe_fixture_path(root, ".symdesk/history/manifest/notes.md.json")
        .expect("valid manifest relative path");
    assert_eq!(
        safe2,
        root.join(".symdesk")
            .join("history")
            .join("manifest")
            .join("notes.md.json")
    );

    let safe3 = resolve_safe_fixture_path(root, "notes/nested/./deep/doc.md")
        .expect("valid normalized relative path");
    assert_eq!(
        safe3,
        root.join("notes")
            .join("nested")
            .join("deep")
            .join("doc.md")
    );

    #[cfg(windows)]
    {
        let safe4 = resolve_safe_fixture_path(root, r"notes\nested\doc.md")
            .expect("valid Windows relative path");
        assert_eq!(safe4, root.join("notes").join("nested").join("doc.md"));
    }
}

#[test]
fn test_path_to_slash_and_literal_unix_backslash_distinction() {
    // 1. Verify path_to_slash behavior
    #[cfg(unix)]
    {
        let literal_backslash_path = Path::new("notes\\backslash.md");
        let formatted = path_to_slash(literal_backslash_path);
        assert_eq!(
            formatted, "notes\\backslash.md",
            "Unix filename with literal backslash must NOT be replaced with forward slash"
        );

        let regular_path = Path::new("notes/regular.md");
        let formatted_reg = path_to_slash(regular_path);
        assert_eq!(
            formatted_reg, "notes/regular.md",
            "Unix regular path must retain forward slashes"
        );
    }

    // 2. Verify collect_actual_fs distinct collection of literal backslash vs nested subdir on Unix
    let temp_vault = TempVaultGuard::new("test-backslash-distinction");

    // Create file in subdirectory notes/backslash.md
    let nested_dir = temp_vault.path.join("notes");
    fs::create_dir_all(&nested_dir).expect("create notes dir");
    fs::write(nested_dir.join("backslash.md"), b"nested content").expect("write nested file");

    // Create file literally named notes\backslash.md in vault root
    let literal_file = temp_vault.path.join("notes\\backslash.md");
    fs::write(&literal_file, b"literal backslash content").expect("write literal backslash file");

    let actual = collect_actual_fs(&temp_vault.path);

    #[cfg(unix)]
    {
        assert!(
            actual.contains_key("notes/backslash.md"),
            "must contain nested path notes/backslash.md"
        );
        assert!(
            actual.contains_key("notes\\backslash.md"),
            "must contain literal backslash path notes\\backslash.md"
        );
        assert_ne!(
            actual.get("notes/backslash.md").unwrap().bytes,
            actual.get("notes\\backslash.md").unwrap().bytes,
            "nested file and literal backslash file must have distinct contents"
        );
    }
    #[cfg(not(unix))]
    {
        assert!(
            actual.contains_key("notes/backslash.md") || actual.contains_key("notes\\backslash.md"),
            "must contain backslash test path"
        );
    }
}
