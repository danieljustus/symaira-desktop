use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;
use symdesk_index::{
    RetrievalDb, RetrievalDocument, StoredRetrievalChunk, index_location_for_vault,
    open_retrieval_for_vault,
};

static NEXT_ROOT: AtomicU64 = AtomicU64::new(0);

struct TestRoot(PathBuf);

impl TestRoot {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock after Unix epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symdesk-vault-retrieval-migration-{}-{nonce}-{}",
            std::process::id(),
            NEXT_ROOT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir_all(&path).expect("create isolated test root");
        Self(path)
    }
}

impl Drop for TestRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn environment(root: &Path) -> BTreeMap<String, String> {
    let home = root.join("home");
    let data_home = root.join("data");
    fs::create_dir_all(&home).expect("create test home");
    fs::create_dir_all(&data_home).expect("create test data home");
    BTreeMap::from([
        ("HOME".to_owned(), home.to_string_lossy().into_owned()),
        (
            "USERPROFILE".to_owned(),
            home.to_string_lossy().into_owned(),
        ),
        (
            "XDG_DATA_HOME".to_owned(),
            data_home.to_string_lossy().into_owned(),
        ),
    ])
}

fn legacy_path(environment: &BTreeMap<String, String>) -> PathBuf {
    let home = environment
        .get(if cfg!(windows) { "USERPROFILE" } else { "HOME" })
        .expect("test home");
    PathBuf::from(home).join(".local/share/symaira-seek/symseek.db")
}

fn vault_path(root: &Path) -> PathBuf {
    let vault = root.join("vault");
    fs::create_dir_all(&vault).expect("create test vault");
    vault
}

fn vault_index_path(vault: &Path, environment: &BTreeMap<String, String>, root: &Path) -> PathBuf {
    index_location_for_vault(
        &vault.to_string_lossy(),
        environment,
        root,
        &root.join("tmp"),
    )
    .expect("resolve per-vault retrieval index")
}

fn open_vault(
    vault: &Path,
    environment: &BTreeMap<String, String>,
    root: &Path,
) -> Result<RetrievalDb, symdesk_index::SidecarError> {
    open_retrieval_for_vault(
        &vault.to_string_lossy(),
        environment,
        root,
        &root.join("tmp"),
    )
}

fn seed_chunk(path: &Path, content: &str) {
    let database = RetrievalDb::open_at(path).expect("create retrieval index");
    database
        .save_document(&RetrievalDocument {
            path: "legacy.md".to_owned(),
            hash: "document-hash".to_owned(),
            updated_at: "2026-01-02T03:04:05Z".to_owned(),
        })
        .expect("save retrieval document");
    database
        .save_chunks(&[StoredRetrievalChunk {
            id: 0,
            uuid: "legacy-chunk".to_owned(),
            document_path: "legacy.md".to_owned(),
            chunk_index: 0,
            content: content.to_owned(),
            embedding: vec![1.0],
            hash: "chunk-hash".to_owned(),
            norm: 1.0,
            dim: 1,
            model: "local-hash".to_owned(),
            char_start: None,
            char_end: None,
            anchor_kind: String::new(),
            anchor_value: String::new(),
            embedding_pending: false,
        }])
        .expect("save retrieval chunk");
}

fn chunk_content(database: &RetrievalDb) -> String {
    database
        .get_chunks_for_document("legacy.md")
        .expect("read copied chunks")
        .into_iter()
        .next()
        .expect("copied chunk")
        .content
}

#[test]
fn first_vault_open_moves_and_validates_populated_pre_absorption_index() {
    let root = TestRoot::new();
    let environment = environment(&root.0);
    let vault = vault_path(&root.0);
    let legacy = legacy_path(&environment);
    fs::create_dir_all(legacy.parent().expect("legacy parent")).expect("create legacy dir");
    seed_chunk(&legacy, "legacy data survives migration");

    let database = open_vault(&vault, &environment, &root.0).expect("open migrated vault index");
    assert_eq!(database.count_chunks().expect("count migrated chunks"), 1);
    assert_eq!(chunk_content(&database), "legacy data survives migration");
    let destination = vault_index_path(&vault, &environment, &root.0);
    assert!(destination.is_file());
    assert!(!legacy.exists(), "old pre-absorption store is moved");
    assert!(!PathBuf::from(format!("{}-wal", legacy.display())).exists());
    assert!(!PathBuf::from(format!("{}-shm", legacy.display())).exists());
    #[cfg(unix)]
    assert_eq!(
        fs::metadata(destination).unwrap().permissions().mode() & 0o777,
        0o600
    );
}

#[test]
fn existing_vault_index_wins_without_overwriting_or_removing_shared_source() {
    let root = TestRoot::new();
    let environment = environment(&root.0);
    let vault = vault_path(&root.0);
    let legacy = legacy_path(&environment);
    fs::create_dir_all(legacy.parent().expect("legacy parent")).expect("create legacy dir");
    seed_chunk(&legacy, "shared source");
    let destination = vault_index_path(&vault, &environment, &root.0);
    seed_chunk(&destination, "existing per-vault data");

    let database = open_vault(&vault, &environment, &root.0).expect("open existing vault index");
    assert_eq!(chunk_content(&database), "existing per-vault data");
    assert_eq!(database.count_chunks().expect("count existing chunks"), 1);
    assert_eq!(
        chunk_content(&RetrievalDb::open_at(&legacy).expect("open shared source")),
        "shared source"
    );
}

#[test]
fn malformed_legacy_source_is_preserved_and_partial_destination_is_removed() {
    let root = TestRoot::new();
    let environment = environment(&root.0);
    let vault = vault_path(&root.0);
    let legacy = legacy_path(&environment);
    fs::create_dir_all(legacy.parent().expect("legacy parent")).expect("create legacy dir");
    let original = b"not a sqlite database";
    fs::write(&legacy, original).expect("write malformed legacy source");
    let destination = vault_index_path(&vault, &environment, &root.0);

    let error = match open_vault(&vault, &environment, &root.0) {
        Ok(_) => panic!("malformed source unexpectedly opened"),
        Err(error) => error,
    };
    assert!(
        error
            .to_string()
            .starts_with("migrate legacy retrieval index:")
    );
    assert_eq!(fs::read(&legacy).expect("read preserved source"), original);
    assert!(!destination.exists());
}

#[test]
fn validation_failure_rolls_back_only_the_new_copy_and_keeps_source() {
    let root = TestRoot::new();
    let environment = environment(&root.0);
    let vault = vault_path(&root.0);
    let legacy = legacy_path(&environment);
    fs::create_dir_all(legacy.parent().expect("legacy parent")).expect("create legacy dir");
    let connection = Connection::open(&legacy).expect("create incompatible SQLite source");
    connection
        .execute_batch(
            "CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT);
             INSERT INTO schema_migrations (version) VALUES
                ('0001_baseline'), ('0002_meta'), ('0003_index_storage'),
                ('0004_binary_signature'), ('0005_quantized_sidecar'),
                ('0006_folder_contexts'), ('0007_backfill_embedding_dim'),
                ('0008_chunk_spans'), ('0009_extractions'),
                ('0010_embedding_pending'), ('0011_location_anchors'),
                ('0012_german_norm'), ('0013_german_trigram');",
        )
        .expect("mark incompatible schema as migrated");
    drop(connection);
    let destination = vault_index_path(&vault, &environment, &root.0);

    assert!(open_vault(&vault, &environment, &root.0).is_err());
    assert!(
        legacy.is_file(),
        "source survives failed destination validation"
    );
    assert!(
        !destination.exists(),
        "only the new invalid copy is rolled back"
    );
    assert!(!PathBuf::from(format!("{}-wal", destination.display())).exists());
    assert!(!PathBuf::from(format!("{}-shm", destination.display())).exists());
}

#[test]
fn configured_global_override_bypasses_vault_migration() {
    let root = TestRoot::new();
    let environment = environment(&root.0);
    let vault = vault_path(&root.0);
    let legacy = legacy_path(&environment);
    fs::create_dir_all(legacy.parent().expect("legacy parent")).expect("create legacy dir");
    seed_chunk(&legacy, "pre-absorption shared source");

    let home = PathBuf::from(&environment["HOME"]);
    let config_path = home.join(".config/symseek/config.toml");
    fs::create_dir_all(config_path.parent().expect("config parent"))
        .expect("create config directory");
    let override_path = root.0.join("configured/retrieval.db");
    seed_chunk(&override_path, "configured global index");
    fs::write(
        &config_path,
        format!("index_path = {:?}\n", override_path.to_string_lossy()),
    )
    .expect("write global override");

    let database = open_vault(&vault, &environment, &root.0).expect("open global override");
    assert_eq!(chunk_content(&database), "configured global index");
    assert!(legacy.exists(), "override bypasses legacy migration");
}

#[test]
fn snapshot_includes_committed_wal_data_and_preserves_unified_store() {
    let root = TestRoot::new();
    let environment = environment(&root.0);
    let vault = vault_path(&root.0);
    let data_home = PathBuf::from(&environment["XDG_DATA_HOME"]);
    let standalone = data_home.join("symdesk/retrieval.db");
    fs::create_dir_all(standalone.parent().expect("standalone parent"))
        .expect("create standalone dir");
    seed_chunk(&standalone, "before WAL update");

    let writer = Connection::open(&standalone).expect("open standalone writer");
    writer
        .execute_batch(
            "PRAGMA journal_mode=WAL;
             PRAGMA wal_autocheckpoint=0;
             UPDATE chunks SET content='committed WAL data' WHERE document_path='legacy.md';",
        )
        .expect("commit WAL update");
    let wal = PathBuf::from(format!("{}-wal", standalone.display()));
    assert!(fs::metadata(&wal).expect("stat WAL").len() > 0);

    let database = open_vault(&vault, &environment, &root.0).expect("snapshot unified store");
    assert_eq!(chunk_content(&database), "committed WAL data");
    assert!(standalone.exists(), "unified symdesk source is retained");
    drop(database);
    drop(writer);
}

#[cfg(unix)]
use std::os::unix::fs::PermissionsExt;
