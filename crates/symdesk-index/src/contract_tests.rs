#[cfg(unix)]
use std::process::Command;
use std::{
    collections::HashMap,
    fs,
    io::Write,
    path::Path,
    time::{Duration, UNIX_EPOCH},
};

use rusqlite::{Connection, types::ValueRef};
use serde::Deserialize;
use serde_json::{Value, json};

#[cfg(windows)]
use std::path::PathBuf;

use super::{IndexedDocument, MIGRATIONS, SearchHit, Sidecar, storage_path, strip_verbatim_prefix};

const GO_MIGRATIONS: &[(&str, &str)] = &[
    (
        "001_init",
        include_str!("../../../internal/sidecar/migrations/001_init.sql"),
    ),
    (
        "002_doc_metadata",
        include_str!("../../../internal/sidecar/migrations/002_doc_metadata.sql"),
    ),
    (
        "003_asn",
        include_str!("../../../internal/sidecar/migrations/003_asn.sql"),
    ),
    (
        "004_file_stat_cache",
        include_str!("../../../internal/sidecar/migrations/004_file_stat_cache.sql"),
    ),
    (
        "005_type",
        include_str!("../../../internal/sidecar/migrations/005_type.sql"),
    ),
    (
        "006_links_to_path_index",
        include_str!("../../../internal/sidecar/migrations/006_links_to_path_index.sql"),
    ),
    (
        "007_created_at",
        include_str!("../../../internal/sidecar/migrations/007_created_at.sql"),
    ),
    (
        "008_index_lifecycle",
        include_str!("../../../internal/sidecar/migrations/008_index_lifecycle.sql"),
    ),
    (
        "009_german_norm",
        include_str!("../../../internal/sidecar/migrations/009_german_norm.sql"),
    ),
    (
        "010_german_trigram",
        include_str!("../../../internal/sidecar/migrations/010_german_trigram.sql"),
    ),
    (
        "011_dataset_rows",
        include_str!("../../../internal/sidecar/migrations/011_dataset_rows.sql"),
    ),
];

#[test]
fn rust_migration_bytes_match_go_oracle() {
    assert_eq!(MIGRATIONS, GO_MIGRATIONS);
}

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    documents: Vec<DocumentInput>,
    updated_input: DocumentInput,
    batch_inputs: Vec<DocumentInput>,
    pragmas: HashMap<String, String>,
    migrations: Vec<String>,
    schema: Vec<SchemaObject>,
    initial: Value,
    searches: Vec<SearchCase>,
    updated: Value,
    deleted: Value,
    batch_error: String,
    after_batch_error: Value,
}

#[derive(Clone, Deserialize)]
struct DocumentInput {
    path: String,
    markdown: String,
    mtime_ns: i64,
}

#[derive(Deserialize)]
struct SchemaObject {
    #[serde(rename = "type")]
    object_type: String,
    name: String,
    sql: String,
}

#[derive(Deserialize)]
struct SearchCase {
    query: String,
    scoped: bool,
    #[serde(default)]
    allowed: Vec<String>,
    hits: Vec<SearchHitFixture>,
}

#[derive(Deserialize)]
struct SearchHitFixture {
    path: String,
    title: String,
    snippet: String,
}

#[test]
fn go_sidecar_contract_matches_rust() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/sidecar/contracts.json"
    ))
    .expect("decode sidecar fixture");
    assert_eq!(fixture.schema_version, 1);
    let directory = std::env::temp_dir().join(format!("symdesk-index-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&directory);
    let path = directory.join("sidecar.db");
    let mut sidecar = Sidecar::open(&path).expect("open sidecar");
    assert_eq!(pragmas(&sidecar.connection), fixture.pragmas);
    assert_eq!(migrations(&sidecar.connection), fixture.migrations);

    let actual_schema = schema(&sidecar.connection);
    assert_eq!(
        actual_schema
            .iter()
            .map(|entry| (&entry.object_type, &entry.name))
            .collect::<Vec<_>>(),
        fixture
            .schema
            .iter()
            .map(|entry| (&entry.object_type, &entry.name))
            .collect::<Vec<_>>()
    );
    for expected in fixture.schema.iter().filter(|entry| {
        !entry.name.starts_with("fts_search_")
            && !entry.name.starts_with("fts_norm_")
            && !entry.name.starts_with("fts_tri_")
    }) {
        let actual = actual_schema
            .iter()
            .find(|entry| entry.name == expected.name)
            .expect("schema object exists");
        assert_eq!(actual.sql, expected.sql, "schema SQL for {}", expected.name);
    }

    let initial = fixture.documents.iter().map(indexed).collect::<Vec<_>>();
    sidecar.index_documents(&initial).expect("initial index");
    assert_eq!(snapshot(&sidecar.connection), fixture.initial);

    for search in fixture.searches {
        let hits = if search.scoped {
            sidecar
                .search_scoped(&search.query, &search.allowed)
                .expect("scoped search")
        } else {
            sidecar.search(&search.query).expect("search")
        };
        assert_eq!(
            hits,
            search
                .hits
                .into_iter()
                .map(|hit| SearchHit {
                    path: hit.path,
                    title: hit.title,
                    snippet: hit.snippet,
                })
                .collect::<Vec<_>>(),
            "search {:?}",
            search.query
        );
    }

    sidecar
        .index_document(&indexed(&fixture.updated_input))
        .expect("update document");
    assert_eq!(snapshot(&sidecar.connection), fixture.updated);
    sidecar
        .delete_document("vault/link.md")
        .expect("delete document");
    assert_eq!(snapshot(&sidecar.connection), fixture.deleted);

    let mut batch = fixture.batch_inputs.iter().map(indexed).collect::<Vec<_>>();
    batch[1].asn = Some(0);
    let error = sidecar
        .index_documents(&batch)
        .expect_err("invalid ASN must fail");
    assert_eq!(error.to_string(), fixture.batch_error);
    assert_eq!(snapshot(&sidecar.connection), fixture.after_batch_error);
    sidecar.check_integrity().expect("integrity check");
    drop(sidecar);
    let _ = std::fs::remove_dir_all(directory);
}

#[derive(Deserialize)]
struct LifecycleFixture {
    schema_version: u8,
    oracle: LifecycleOracle,
    inputs: Vec<LifecycleInput>,
    initial: Value,
    fast: Value,
    stat_only: Value,
    same_size: Value,
    derived_transition: Value,
    missing_after_refresh: Value,
    after_prune: Value,
    lifecycle_after_prune: Vec<LifecycleStatus>,
    prune_removed: usize,
    fast_indexed_at_same: bool,
    stat_indexed_at_same: bool,
    same_size_length: bool,
    uppercase_ignored: bool,
}

#[derive(Deserialize)]
struct LifecycleOracle {
    commit: String,
    release: String,
}

#[derive(Deserialize)]
struct LifecycleInput {
    path: String,
    initial: String,
    stat_refresh: Option<String>,
    edit: Option<String>,
    derived: Option<String>,
    mtime_ns: i64,
    refresh_mtime_ns: Option<i64>,
    edit_mtime_ns: Option<i64>,
    derived_mtime_ns: Option<i64>,
}

#[derive(Deserialize, Debug, PartialEq)]
struct LifecycleStatus {
    path: String,
    state: String,
    reason: String,
    updated_at: String,
}

#[test]
fn go_refresh_stat_prune_lifecycle_matches_rust() {
    let fixture: LifecycleFixture = serde_json::from_str(include_str!(
        "../../../testdata/port/sidecar/lifecycle.json"
    ))
    .expect("decode lifecycle fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle.commit,
        "ae86331930fdfa2b128b68ae5af7437091b9949a"
    );
    assert_eq!(fixture.oracle.release, "v0.12.2");
    assert!(fixture.same_size_length);
    assert!(fixture.uppercase_ignored);

    let root = std::env::temp_dir().join(format!("symdesk-index-lifecycle-{}", std::process::id()));
    let _ = fs::remove_dir_all(&root);
    fs::create_dir_all(&root).expect("create vault root");
    let root = fs::canonicalize(&root).expect("canonicalize vault root");
    let db_path = root.join("sidecar.db");
    let mut sidecar = Sidecar::open(&db_path).expect("open sidecar");
    for input in &fixture.inputs {
        write_lifecycle_file(&root, &input.path, &input.initial, input.mtime_ns);
    }

    sidecar.refresh_index(&root).expect("initial refresh");
    assert_snapshot(&sidecar, &root, &fixture.initial);
    let note_path = storage_path(&root, Path::new("note.md"))
        .expect("note storage path")
        .key_path;
    let initial_indexed_at = indexed_at(&sidecar, &note_path);

    sidecar.refresh_index(&root).expect("fast refresh");
    assert_snapshot(&sidecar, &root, &fixture.fast);
    assert_eq!(initial_indexed_at, indexed_at(&sidecar, &note_path));
    assert!(fixture.fast_indexed_at_same);

    let stat = fixture
        .inputs
        .iter()
        .find(|input| input.path == "stat.md")
        .expect("stat input");
    write_lifecycle_file(
        &root,
        &stat.path,
        stat.stat_refresh.as_deref().expect("stat content"),
        stat.refresh_mtime_ns.expect("stat mtime"),
    );
    let stat_path = storage_path(&root, Path::new("stat.md"))
        .expect("stat storage path")
        .key_path;
    let stat_indexed_at = indexed_at(&sidecar, &stat_path);
    sidecar.refresh_index(&root).expect("stat-only refresh");
    assert_snapshot(&sidecar, &root, &fixture.stat_only);
    assert_eq!(stat_indexed_at, indexed_at(&sidecar, &stat_path));
    assert!(fixture.stat_indexed_at_same);

    let note = fixture
        .inputs
        .iter()
        .find(|input| input.path == "note.md")
        .expect("note input");
    write_lifecycle_file(
        &root,
        &note.path,
        note.edit.as_deref().expect("edit content"),
        note.edit_mtime_ns.expect("edit mtime"),
    );
    sidecar.refresh_index(&root).expect("same-size refresh");
    assert_snapshot(&sidecar, &root, &fixture.same_size);

    let derived = fixture
        .inputs
        .iter()
        .find(|input| input.path == "derived.md")
        .expect("derived input");
    write_lifecycle_file(
        &root,
        &derived.path,
        derived.derived.as_deref().expect("derived content"),
        derived.derived_mtime_ns.expect("derived mtime"),
    );
    sidecar
        .refresh_index(&root)
        .expect("derived transition refresh");
    assert_snapshot(&sidecar, &root, &fixture.derived_transition);

    fs::remove_file(root.join("remove.md")).expect("remove source");
    sidecar
        .refresh_index(&root)
        .expect("refresh with missing source");
    assert_snapshot(&sidecar, &root, &fixture.missing_after_refresh);

    sidecar
        .connection
        .execute(
            "INSERT INTO index_lifecycle(path, state, reason, updated_at) VALUES (?, ?, ?, ?)",
            rusqlite::params![
                storage_path(&root, Path::new("stale-status.md"))
                    .expect("stale status storage path")
                    .key_path
                    .to_string_lossy(),
                "failed",
                "missing",
                "2026-01-15T12:00:00Z"
            ],
        )
        .expect("insert stale status");
    sidecar
        .connection
        .execute(
            "INSERT INTO index_lifecycle(path, state, reason, updated_at) VALUES (?, ?, ?, ?)",
            rusqlite::params![
                note_path.to_string_lossy(),
                "indexed",
                "",
                "2026-01-15T12:00:00Z"
            ],
        )
        .expect("insert kept status");
    let before_absent_delete = snapshot(&sidecar.connection);
    sidecar
        .delete_document(
            &storage_path(&root, Path::new("never-indexed.md"))
                .expect("absent storage path")
                .key_path
                .to_string_lossy(),
        )
        .expect("absent delete");
    assert_eq!(before_absent_delete, snapshot(&sidecar.connection));

    assert_eq!(sidecar.prune(&root).expect("prune"), fixture.prune_removed);
    assert_snapshot(&sidecar, &root, &fixture.after_prune);
    assert_eq!(
        fixture.lifecycle_after_prune,
        lifecycle_statuses(&sidecar, &root)
    );
    drop(sidecar);
    let _ = fs::remove_dir_all(root);
}

#[test]
fn storage_key_normalizes_windows_verbatim_forms_cross_platform() {
    let root = Path::new(r"C:\vault");
    let verbatim = r"\\?\C:\vault\nested\note.md";
    assert_eq!(strip_verbatim_prefix(verbatim), r"C:\vault\nested\note.md");
    assert_eq!(relative_storage_path(root, verbatim), "nested/note.md");
    assert_eq!(
        relative_storage_path(
            Path::new(r"\\server\share\vault"),
            r"\\?\UNC\server\share\vault\note.md"
        ),
        "note.md"
    );
}

#[test]
fn validated_storage_path_separates_io_path_from_storage_key() {
    let base =
        std::env::temp_dir().join(format!("symdesk-index-storage-key-{}", std::process::id()));
    let actual = base.join("actual");
    let root = base.join("root");
    let _ = fs::remove_dir_all(&base);
    fs::create_dir_all(&actual).expect("create actual root");
    #[cfg(unix)]
    let canonical_actual = fs::canonicalize(&actual).expect("canonicalize actual root");
    #[cfg(unix)]
    std::os::unix::fs::symlink(&actual, &root).expect("create root symlink");
    #[cfg(not(unix))]
    let root = actual.clone();

    let storage = storage_path(&root, Path::new("nested/note.md")).expect("storage path");
    assert_eq!(storage.key_path, root.join("nested/note.md"));
    assert!(!storage.key_path.to_string_lossy().contains("actual/nested"));
    #[cfg(unix)]
    {
        assert_eq!(storage.io_path, canonical_actual.join("nested/note.md"));
        assert_ne!(storage.io_path, storage.key_path);
    }
    #[cfg(windows)]
    assert!(storage.io_path.ends_with(Path::new(r"nested\note.md")));
    assert!(storage_path(&root, Path::new("../escape.md")).is_err());
    let _ = fs::remove_dir_all(base);
}

#[cfg(unix)]
#[test]
fn unchanged_refresh_skips_unreadable_file() {
    use std::os::unix::fs::PermissionsExt as _;

    let uid = Command::new("id")
        .arg("-u")
        .output()
        .expect("query effective uid");
    if uid.status.success() && uid.stdout == b"0\n" {
        return;
    }
    let root =
        std::env::temp_dir().join(format!("symdesk-index-unreadable-{}", std::process::id()));
    let _ = fs::remove_dir_all(&root);
    fs::create_dir_all(&root).expect("create root");
    let root = fs::canonicalize(&root).expect("canonicalize root");
    let storage = storage_path(&root, Path::new("note.md")).expect("note storage path");
    let path = storage.key_path;
    let io_path = storage.io_path;
    write_lifecycle_file(
        &root,
        "note.md",
        "---\ntitle: Note\n---\nbody\n",
        1_800_000_000_000_000_000,
    );
    let mut sidecar = Sidecar::open(&root.join("sidecar.db")).expect("open sidecar");
    sidecar.refresh_index(&root).expect("initial refresh");
    let before = indexed_at(&sidecar, &path);
    fs::set_permissions(&io_path, fs::Permissions::from_mode(0o000)).expect("make unreadable");
    let result = sidecar.refresh_index(&root);
    fs::set_permissions(&io_path, fs::Permissions::from_mode(0o600)).expect("restore permissions");
    result.expect("fast refresh should not read file");
    assert_eq!(before, indexed_at(&sidecar, &path));
    drop(sidecar);
    let _ = fs::remove_dir_all(root);
}

#[test]
fn refresh_flushes_queued_documents_before_later_parse_error() {
    let root = std::env::temp_dir().join(format!(
        "symdesk-index-refresh-error-{}",
        std::process::id()
    ));
    let _ = fs::remove_dir_all(&root);
    fs::create_dir_all(&root).expect("create root");
    let root = fs::canonicalize(&root).expect("canonicalize root");
    write_lifecycle_file(
        &root,
        "a-valid.md",
        "---\ntitle: Valid\n---\nbody\n",
        1_800_000_000_000_000_000,
    );
    write_lifecycle_file(
        &root,
        "b-invalid.md",
        "---\ntitle: [\n---\nbroken\n",
        1_800_000_001_000_000_000,
    );
    let mut sidecar = Sidecar::open(&root.join("sidecar.db")).expect("open sidecar");

    assert!(sidecar.refresh_index(&root).is_err());
    let indexed: i64 = sidecar
        .connection
        .query_row(
            "SELECT COUNT(*) FROM files WHERE path = ?",
            [storage_path(&root, Path::new("a-valid.md"))
                .expect("valid storage path")
                .key_path
                .to_string_lossy()
                .as_ref()],
            |row| row.get(0),
        )
        .expect("count flushed document");
    assert_eq!(indexed, 1);
    drop(sidecar);
    let _ = fs::remove_dir_all(root);
}

#[cfg(unix)]
#[test]
fn open_preserves_existing_parent_permissions() {
    use std::os::unix::fs::PermissionsExt as _;

    let root = std::env::temp_dir().join(format!(
        "symdesk-index-existing-parent-{}",
        std::process::id()
    ));
    let _ = fs::remove_dir_all(&root);
    fs::create_dir_all(&root).expect("create parent");
    fs::set_permissions(&root, fs::Permissions::from_mode(0o750)).expect("set parent mode");
    let _sidecar = Sidecar::open(&root.join("sidecar.db")).expect("open sidecar");
    let mode = fs::metadata(&root)
        .expect("stat parent")
        .permissions()
        .mode()
        & 0o777;
    assert_eq!(mode, 0o750);
    drop(_sidecar);
    let _ = fs::remove_dir_all(root);
}

#[test]
fn open_creates_a_usable_parent_on_all_platforms() {
    let root = std::env::temp_dir().join(format!(
        "symdesk-index-created-parent-{}",
        std::process::id()
    ));
    let _ = fs::remove_dir_all(&root);
    let _sidecar = Sidecar::open(&root.join("nested").join("sidecar.db")).expect("open sidecar");
    assert!(root.join("nested").is_dir());
    drop(_sidecar);
    let _ = fs::remove_dir_all(root);
}

#[cfg(windows)]
#[test]
fn long_path_refresh_uses_verbatim_io_path_and_ordinary_key() {
    let ordinary_root =
        std::env::temp_dir().join(format!("symdesk-index-long-path-{}", std::process::id()));
    let mut long_root = ordinary_root.clone();
    for index in 0..16 {
        long_root = long_root.join(format!("segment-{index:02}-{}", "x".repeat(16)));
    }
    let root = PathBuf::from(format!(r"\\?\{}", long_root.display()));
    let _ = fs::remove_dir_all(&root);
    if let Err(error) = fs::create_dir_all(&root) {
        if error.raw_os_error() == Some(206) {
            eprintln!("skipping long-path regression: Windows long paths are disabled");
            return;
        }
        panic!("create long vault root: {error}");
    }
    let relative = Path::new("note.md");
    let content = b"---\ntitle: Long path\n---\nbody\n";
    fs::write(root.join(relative), content).expect("write long-path document");

    let storage = storage_path(&root, relative).expect("validated long-path storage");
    assert_eq!(storage.key_path, long_root.join(relative));
    assert!(storage.io_path.to_string_lossy().starts_with(r#"\\?\"#));
    assert_eq!(
        fs::read(&storage.io_path).expect("read verbatim path"),
        content
    );

    let db_path = std::env::temp_dir().join(format!(
        "symdesk-index-long-path-db-{}.db",
        std::process::id()
    ));
    let _ = fs::remove_file(&db_path);
    let mut sidecar = Sidecar::open(&db_path).expect("open sidecar");
    let mut batch = Vec::new();
    sidecar
        .refresh_path(&root, relative, &mut batch)
        .expect("refresh long-path document");
    sidecar
        .flush_refresh_batch(&mut batch)
        .expect("flush long-path document");
    let key = storage.key_path.to_string_lossy().into_owned();
    let stored_path: String = sidecar
        .connection
        .query_row("SELECT path FROM files WHERE path = ?", [&key], |row| {
            row.get(0)
        })
        .expect("stored ordinary path key");
    assert_eq!(stored_path, key);
    drop(sidecar);
    let _ = fs::remove_file(db_path);
    let _ = fs::remove_dir_all(root);
}

fn write_lifecycle_file(root: &Path, relative: &str, content: &str, mtime_ns: i64) {
    let path = root.join(relative);
    let mut file = fs::File::create(path).expect("create lifecycle file");
    file.write_all(content.as_bytes())
        .expect("write lifecycle file");
    file.set_modified(UNIX_EPOCH + Duration::from_nanos(mtime_ns as u64))
        .expect("set lifecycle mtime");
}

fn assert_snapshot(sidecar: &Sidecar, root: &Path, expected: &Value) {
    let mut actual = snapshot(&sidecar.connection);
    normalize_snapshot_paths(&mut actual, root);
    assert_eq!(&actual, expected);
}

fn normalize_snapshot_paths(value: &mut Value, root: &Path) {
    match value {
        Value::Array(values) => values
            .iter_mut()
            .for_each(|value| normalize_snapshot_paths(value, root)),
        Value::Object(object) => {
            for (key, value) in object.iter_mut() {
                if matches!(key.as_str(), "path" | "from" | "to")
                    && let Value::String(path) = value
                {
                    *path = relative_storage_path(root, path);
                }
                normalize_snapshot_paths(value, root);
            }
        }
        _ => {}
    }
}

fn relative_storage_path(root: &Path, path: &str) -> String {
    let root = slash_path(&strip_verbatim_prefix(&root.to_string_lossy()));
    let path = slash_path(&strip_verbatim_prefix(path));
    let prefix = format!("{}/", root.trim_end_matches('/'));
    path.strip_prefix(&prefix).unwrap_or(&path).to_owned()
}

fn slash_path(path: &str) -> String {
    path.replace('\\', "/")
}

fn indexed_at(sidecar: &Sidecar, path: &Path) -> String {
    sidecar
        .connection
        .query_row(
            "SELECT indexed_at FROM files WHERE path = ?",
            [path.to_string_lossy().as_ref()],
            |row| row.get(0),
        )
        .expect("indexed_at")
}

fn lifecycle_statuses(sidecar: &Sidecar, root: &Path) -> Vec<LifecycleStatus> {
    let mut statement = sidecar
        .connection
        .prepare("SELECT path, state, reason, updated_at FROM index_lifecycle ORDER BY path")
        .expect("prepare lifecycle rows");
    statement
        .query_map([], |row| {
            let path: String = row.get(0)?;
            Ok(LifecycleStatus {
                path: relative_storage_path(root, &path),
                state: row.get(1)?,
                reason: row.get(2)?,
                updated_at: row.get(3)?,
            })
        })
        .expect("query lifecycle rows")
        .collect::<Result<Vec<_>, _>>()
        .expect("collect lifecycle rows")
}

fn indexed(input: &DocumentInput) -> IndexedDocument {
    let document = symdesk_vault::parse_bytes(&input.path, input.markdown.as_bytes())
        .expect("parse fixture document");
    IndexedDocument::from_vault(&document, Some(input.mtime_ns)).expect("convert document")
}

fn pragmas(connection: &Connection) -> HashMap<String, String> {
    [
        "journal_mode",
        "foreign_keys",
        "busy_timeout",
        "integrity_check",
    ]
    .into_iter()
    .map(|name| {
        let value = connection
            .query_row(&format!("PRAGMA {name}"), [], |row| {
                Ok(match row.get_ref(0)? {
                    ValueRef::Integer(value) => value.to_string(),
                    ValueRef::Text(value) => String::from_utf8_lossy(value).into_owned(),
                    value => format!("{value:?}"),
                })
            })
            .expect("pragma");
        (name.to_owned(), value)
    })
    .collect()
}

fn migrations(connection: &Connection) -> Vec<String> {
    let mut statement = connection
        .prepare("SELECT version FROM schema_migrations ORDER BY version")
        .expect("prepare migrations");
    statement
        .query_map([], |row| row.get(0))
        .expect("query migrations")
        .collect::<Result<_, _>>()
        .expect("collect migrations")
}

fn schema(connection: &Connection) -> Vec<SchemaObject> {
    let mut statement = connection
        .prepare("SELECT type,name,COALESCE(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name")
        .expect("prepare schema");
    statement
        .query_map([], |row| {
            Ok(SchemaObject {
                object_type: row.get(0)?,
                name: row.get(1)?,
                sql: row.get(2)?,
            })
        })
        .expect("query schema")
        .collect::<Result<_, _>>()
        .expect("collect schema")
}

fn snapshot(connection: &Connection) -> Value {
    let files = query_values(
        connection,
        r#"SELECT id,path,sha256,title,created_at,modified_at,"type",document_date,person,status,due_date,confidence,ocr_json_path,simhash,asn,size,mtime_ns FROM files ORDER BY id"#,
        |row| {
            Ok(json!({
                "id": row.get::<_, i64>(0)?, "path": row.get::<_, String>(1)?,
                "sha256": row.get::<_, String>(2)?, "title": row.get::<_, String>(3)?,
                "created_at": row.get::<_, String>(4)?, "modified_at": row.get::<_, String>(5)?,
                "type": row.get::<_, String>(6)?, "document_date": row.get::<_, Option<String>>(7)?,
                "person": row.get::<_, Option<String>>(8)?, "status": row.get::<_, Option<String>>(9)?,
                "due_date": row.get::<_, Option<String>>(10)?, "confidence": row.get::<_, Option<i64>>(11)?,
                "ocr_json_path": row.get::<_, Option<String>>(12)?, "simhash": row.get::<_, Option<String>>(13)?,
                "asn": row.get::<_, Option<i64>>(14)?, "size": row.get::<_, Option<i64>>(15)?,
                "mtime_ns": row.get::<_, Option<i64>>(16)?
            }))
        },
    );
    let properties = query_values(
        connection,
        "SELECT file_id,key,value,value_type FROM file_properties ORDER BY file_id,key,value",
        |row| {
            Ok(
                json!({"file_id":row.get::<_,i64>(0)?,"key":row.get::<_,String>(1)?,"value":row.get::<_,Option<String>>(2)?,"value_type":row.get::<_,String>(3)?}),
            )
        },
    );
    let links = query_values(
        connection,
        "SELECT from_path,to_path,kind FROM links ORDER BY from_path,to_path,kind",
        |row| {
            Ok(
                json!({"from":row.get::<_,String>(0)?,"to":row.get::<_,String>(1)?,"kind":row.get::<_,String>(2)?}),
            )
        },
    );
    let fts_search = query_values(
        connection,
        "SELECT rowid,title,body FROM fts_search ORDER BY rowid",
        |row| {
            Ok(
                json!({"rowid":row.get::<_,i64>(0)?,"title":row.get::<_,String>(1)?,"body":row.get::<_,String>(2)?}),
            )
        },
    );
    let fts_norm = query_values(
        connection,
        "SELECT rowid,norm FROM fts_norm ORDER BY rowid",
        |row| Ok(json!({"rowid":row.get::<_,i64>(0)?,"norm":row.get::<_,String>(1)?})),
    );
    let fts_tri = query_values(
        connection,
        "SELECT rowid,body FROM fts_tri ORDER BY rowid",
        |row| Ok(json!({"rowid":row.get::<_,i64>(0)?,"body":row.get::<_,String>(1)?})),
    );
    json!({"files":files,"properties":properties,"links":links,"fts_search":fts_search,"fts_norm":fts_norm,"fts_tri":fts_tri})
}

fn query_values<F>(connection: &Connection, sql: &str, mut mapper: F) -> Vec<Value>
where
    F: FnMut(&rusqlite::Row<'_>) -> rusqlite::Result<Value>,
{
    let mut statement = connection.prepare(sql).expect("prepare snapshot");
    statement
        .query_map([], |row| mapper(row))
        .expect("query snapshot")
        .collect::<Result<_, _>>()
        .expect("collect snapshot")
}
