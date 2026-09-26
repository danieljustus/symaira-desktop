//! Replays the Go-generated sidecar metadata fixture (issue #1006).
//!
//! The encoding cases are compared byte-for-byte; the filesystem cases are
//! structural because a real open records the current instant, which no port
//! can reproduce. The real filesystem modes are asserted natively on Unix
//! only, where Go observes them; the mode strings the Go oracle recorded are
//! asserted on every platform so non-Unix runs exercise the contract too.

use std::{
    fs,
    path::{Path, PathBuf},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use symdesk_index::{
    METADATA_FILE_NAME, encode_sidecar_metadata_at, open_for_vault, path_for_vault,
};

#[derive(Deserialize)]
struct Fixture {
    metadata_file_name: String,
    directory_mode: String,
    file_mode: String,
    key_order: Vec<String>,
    encoding_cases: Vec<EncodingCase>,
    open_for_vault: FilesystemCase,
    reopen: FilesystemCase,
    explicit_override: FilesystemCase,
}

#[derive(Deserialize)]
struct EncodingCase {
    name: String,
    vault_path: String,
    unix_sec: i64,
    nanos: i64,
    encoded: String,
}

#[derive(Deserialize)]
struct FilesystemCase {
    entries: Vec<String>,
    metadata_written: bool,
    vault_path_is_canonical: bool,
    last_used_is_utc: bool,
    temp_leftovers: usize,
    last_used_advances: bool,
}

fn fixture() -> Fixture {
    let path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/sidecar/metadata.json");
    let raw = fs::read_to_string(&path).expect("read sidecar metadata fixture");
    serde_json::from_str(&raw).expect("decode sidecar metadata fixture")
}

#[test]
fn encodes_every_recorded_case_byte_for_byte() {
    let fixture = fixture();
    assert_eq!(fixture.metadata_file_name, METADATA_FILE_NAME);
    assert_eq!(fixture.key_order, vec!["vault_path", "last_used"]);
    assert!(
        fixture.encoding_cases.len() >= 10,
        "fixture lost encoding coverage"
    );
    for case in &fixture.encoding_cases {
        let nanos = u32::try_from(case.nanos).expect("recorded nanoseconds are non-negative");
        let encoded = encode_sidecar_metadata_at(&case.vault_path, case.unix_sec, nanos);
        assert_eq!(encoded, case.encoded, "encoding case {}", case.name);
    }
}

#[test]
fn open_for_vault_records_metadata_like_go() {
    let scratch = scratch_root("open");
    let data_home = scratch.join("data");
    let vault = scratch.join("vault");
    fs::create_dir_all(&vault).expect("create vault");
    fs::create_dir_all(&data_home).expect("create data home");

    let fixture = fixture();
    let expected = &fixture.open_for_vault;
    let (first_entries, first_recorded) = with_env(&data_home, None, &vault, || {
        let sidecar = open_for_vault(&vault).expect("open for vault");
        drop(sidecar);
        let directory = sidecar_directory(&vault);
        (durable_entries(&directory), read_metadata(&directory))
    });

    assert_eq!(first_entries, expected.entries);
    assert!(expected.metadata_written);
    assert!(expected.vault_path_is_canonical);
    assert_eq!(
        first_recorded.vault_path,
        canonical(&vault).to_string_lossy()
    );
    #[cfg(windows)]
    assert!(!first_recorded.vault_path.starts_with(r"\\?\"));
    assert!(expected.last_used_is_utc);
    assert!(
        first_recorded.last_used.ends_with('Z'),
        "recorded instant is not the Go UTC rendering: {}",
        first_recorded.last_used
    );
    assert_eq!(
        temp_leftovers(&with_env(&data_home, None, &vault, || sidecar_directory(
            &vault
        ))),
        expected.temp_leftovers
    );

    // Reopen: the record is rewritten and the instant advances, as Go's fixture
    // recorded it.
    let reopen: &FilesystemCase = &fixture.reopen;
    std::thread::sleep(Duration::from_millis(2));
    let (second_entries, second_recorded) = with_env(&data_home, None, &vault, || {
        let sidecar = open_for_vault(&vault).expect("reopen for vault");
        drop(sidecar);
        let directory = sidecar_directory(&vault);
        (durable_entries(&directory), read_metadata(&directory))
    });
    assert_eq!(second_entries, reopen.entries);
    assert!(reopen.last_used_advances);
    assert!(
        second_recorded.last_used > first_recorded.last_used,
        "last_used did not advance: {} -> {}",
        first_recorded.last_used,
        second_recorded.last_used
    );
}

#[test]
fn explicit_override_writes_no_metadata_like_go() {
    let scratch = scratch_root("explicit");
    let data_home = scratch.join("data");
    let vault = scratch.join("vault");
    let explicit_dir = scratch.join("explicit");
    fs::create_dir_all(&vault).expect("create vault");
    fs::create_dir_all(&data_home).expect("create data home");
    fs::create_dir_all(&explicit_dir).expect("create explicit dir");
    let explicit = explicit_dir.join("explicit.db");

    let fixture = fixture();
    let expected: &FilesystemCase = &fixture.explicit_override;
    let entries = with_env(&data_home, Some(&explicit), &vault, || {
        let sidecar = open_for_vault(&vault).expect("open with explicit override");
        drop(sidecar);
        durable_entries(&explicit_dir)
    });
    assert_eq!(entries, expected.entries);
    assert!(!expected.metadata_written);
    assert!(!explicit_dir.join(METADATA_FILE_NAME).exists());
    assert_eq!(temp_leftovers(&explicit_dir), expected.temp_leftovers);
}

/// The Go oracle records `0700` for the sidecar directory and `0600` for
/// `metadata.json`. The test below compares those strings against the real
/// filesystem, which Windows cannot observe — reading them here keeps the
/// contract exercised (and the struct fields used) on every platform.
#[test]
fn fixture_records_go_private_modes() {
    let fixture = fixture();
    assert_eq!(fixture.directory_mode, "0700", "sidecar directory mode");
    assert_eq!(fixture.file_mode, "0600", "metadata file mode");
}

#[cfg(unix)]
#[test]
fn records_the_recorded_posix_modes() {
    use std::os::unix::fs::PermissionsExt;

    let fixture = fixture();
    let scratch = scratch_root("modes");
    let data_home = scratch.join("data");
    let vault = scratch.join("vault");
    fs::create_dir_all(&vault).expect("create vault");
    fs::create_dir_all(&data_home).expect("create data home");

    let directory = with_env(&data_home, None, &vault, || {
        let sidecar = open_for_vault(&vault).expect("open for vault");
        drop(sidecar);
        sidecar_directory(&vault)
    });

    let directory_mode = fs::metadata(&directory)
        .expect("stat sidecar directory")
        .permissions()
        .mode()
        & 0o777;
    let file_mode = fs::metadata(directory.join(METADATA_FILE_NAME))
        .expect("stat metadata")
        .permissions()
        .mode()
        & 0o777;
    assert_eq!(
        format!("{directory_mode:04o}"),
        fixture.directory_mode,
        "sidecar directory mode"
    );
    assert_eq!(
        format!("{file_mode:04o}"),
        fixture.file_mode,
        "metadata file mode"
    );
}

struct Recorded {
    vault_path: String,
    last_used: String,
}

fn read_metadata(directory: &Path) -> Recorded {
    let raw =
        fs::read_to_string(directory.join(METADATA_FILE_NAME)).expect("read recorded metadata");
    let value: serde_json::Value = serde_json::from_str(&raw).expect("decode recorded metadata");
    let keys: Vec<String> = value
        .as_object()
        .expect("metadata is an object")
        .keys()
        .cloned()
        .collect();
    assert!(
        raw.find("\"vault_path\"").expect("vault_path key present")
            < raw.find("\"last_used\"").expect("last_used key present"),
        "recorded key order differs from Go"
    );
    assert_eq!(keys.len(), 2, "unexpected metadata keys: {keys:?}");
    Recorded {
        vault_path: value["vault_path"].as_str().expect("vault_path").to_owned(),
        last_used: value["last_used"].as_str().expect("last_used").to_owned(),
    }
}

fn durable_entries(directory: &Path) -> Vec<String> {
    let mut names: Vec<String> = fs::read_dir(directory)
        .expect("read sidecar directory")
        .map(|entry| entry.expect("directory entry").file_name())
        .map(|name| name.to_string_lossy().into_owned())
        .filter(|name| !name.ends_with("-wal") && !name.ends_with("-shm"))
        .collect();
    names.sort();
    names
}

fn temp_leftovers(directory: &Path) -> usize {
    fs::read_dir(directory)
        .expect("read sidecar directory")
        .map(|entry| entry.expect("directory entry").file_name())
        .filter(|name| {
            let name = name.to_string_lossy();
            name.starts_with(".metadata-") && name.ends_with(".tmp")
        })
        .count()
}

/// Resolves the per-vault sidecar directory. The caller already holds the
/// environment lock, so this must not take it again.
fn sidecar_directory(vault: &Path) -> PathBuf {
    path_for_vault(vault)
        .expect("resolve sidecar path")
        .parent()
        .expect("sidecar path has a parent")
        .to_path_buf()
}

/// Creates a unique scratch root under the process temp directory. Nothing in
/// this suite may write into the operator's home.
fn scratch_root(label: &str) -> PathBuf {
    let unique = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("after epoch")
        .as_nanos();
    let root = std::env::temp_dir().join(format!("symdesk-index-metadata-{label}-{unique}"));
    fs::create_dir_all(&root).expect("create scratch root");
    root
}

fn canonical(path: &Path) -> PathBuf {
    let resolved = fs::canonicalize(path).unwrap_or_else(|_| path.to_path_buf());
    #[cfg(windows)]
    if let Some(ordinary) = resolved.to_string_lossy().strip_prefix(r"\\?\") {
        return PathBuf::from(ordinary);
    }
    resolved
}

/// Runs `body` with the isolated data root, so no test touches the operator's
/// real `~/.local/share/symdesk`. Environment mutation is process-wide, so the
/// suite runs single-threaded through the `SYMDESK_INDEX_ENV_LOCK`.
fn with_env<T>(
    data_home: &Path,
    explicit: Option<&Path>,
    _vault: &Path,
    body: impl FnOnce() -> T,
) -> T {
    static LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());
    let guard = LOCK
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner);
    // SAFETY-equivalent contract: the lock serialises every environment
    // mutation in this test binary.
    unsafe {
        std::env::set_var("XDG_DATA_HOME", data_home);
        match explicit {
            Some(path) => std::env::set_var("SYMDESK_SIDECAR", path),
            None => std::env::remove_var("SYMDESK_SIDECAR"),
        }
    }
    let result = body();
    unsafe {
        std::env::remove_var("SYMDESK_SIDECAR");
    }
    drop(guard);
    result
}

#[test]
fn recorded_instants_round_trip_through_the_go_layout() {
    // The Go layout trims trailing zeros, so a naive nanosecond renderer would
    // disagree exactly here.
    let base = UNIX_EPOCH + Duration::from_secs(1_767_229_445);
    for (nanos, expected_suffix) in [
        (0u32, "05Z"),
        (500_000_000, "05.5Z"),
        (120_000_000, "05.12Z"),
        (1, "05.000000001Z"),
    ] {
        let instant = base + Duration::from_nanos(u64::from(nanos));
        let seconds = i64::try_from(
            instant
                .duration_since(UNIX_EPOCH)
                .expect("after epoch")
                .as_secs(),
        )
        .expect("representable");
        let encoded = encode_sidecar_metadata_at("/vaults/plain", seconds, nanos);
        assert!(
            encoded.contains(expected_suffix),
            "{nanos} rendered as {encoded}"
        );
    }
    let _ = SystemTime::now();
}
