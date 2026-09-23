#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;
use serde::Deserialize;
use symdesk_index::restore_database;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    source_hashes: BTreeMap<String, String>,
    restored_rows: Vec<Row>,
    header: String,
    mode: String,
    directory_mode: String,
    source_unchanged: bool,
    same_path_error: String,
    invalid_prefix: String,
    directory_prefix: String,
    missing_prefix: String,
}
#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Row {
    id: i64,
    body: String,
}

#[test]
fn restore_replays_go_validation_atomic_replace_and_modes() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(root.join("testdata/port/retrieval/index-restore.json"))
            .expect("Go restore fixture"),
    )
    .expect("parse fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.source_hashes.len(), 2);
    assert!(fixture.source_hashes.values().all(|hash| hash.len() == 64));
    let temp = TempDir::new();
    let source = temp.path.join("backup.db");
    let destination = temp.path.join("nested/index.db");
    fs::create_dir_all(destination.parent().expect("destination parent"))
        .expect("create destination parent");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(
            destination.parent().expect("destination parent"),
            fs::Permissions::from_mode(0o700),
        )
        .expect("private destination parent");
    }
    for (path, body) in [(&source, "restored row"), (&destination, "stale row")] {
        let db = Connection::open(path).expect("open fixture DB");
        db.execute_batch("CREATE TABLE restore_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL)")
            .expect("create table");
        db.execute("INSERT INTO restore_rows VALUES (1, ?1)", [body])
            .expect("insert row");
    }
    let before = fs::read(&source).expect("source before restore");
    restore_database(&source, &destination).expect("restore valid backup");
    assert_eq!(fs::read(&source).expect("source after restore"), before);
    assert!(fixture.source_unchanged);
    let bytes = fs::read(&destination).expect("restored DB");
    assert_eq!(&bytes[..16], fixture.header.as_bytes());
    let db = Connection::open(&destination).expect("open restored DB");
    let mut query = db
        .prepare("SELECT id, body FROM restore_rows ORDER BY id")
        .expect("query rows");
    let rows = query
        .query_map([], |row| {
            Ok(Row {
                id: row.get(0)?,
                body: row.get(1)?,
            })
        })
        .expect("read rows")
        .collect::<Result<Vec<_>, _>>()
        .expect("collect rows");
    assert_eq!(rows, fixture.restored_rows);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            format!(
                "{:04o}",
                fs::metadata(&destination).unwrap().permissions().mode() & 0o777
            ),
            fixture.mode
        );
        assert_eq!(
            format!(
                "{:04o}",
                fs::metadata(destination.parent().unwrap())
                    .unwrap()
                    .permissions()
                    .mode()
                    & 0o777
            ),
            fixture.directory_mode
        );
    }
    let same = restore_database(&destination, &destination).expect_err("same path");
    assert_eq!(same.to_string(), fixture.same_path_error);
    let invalid = temp.path.join("invalid.db");
    fs::write(&invalid, b"not sqlite").expect("write invalid backup");
    assert!(
        restore_database(&invalid, &destination)
            .expect_err("invalid backup")
            .to_string()
            .starts_with(&fixture.invalid_prefix)
    );
    assert!(
        restore_database(&temp.path, &destination)
            .expect_err("directory backup")
            .to_string()
            .starts_with(&fixture.directory_prefix)
    );
    assert!(
        restore_database(&temp.path.join("missing.db"), &destination)
            .expect_err("missing backup")
            .to_string()
            .starts_with(&fixture.missing_prefix)
    );
    assert_eq!(
        fs::read(&destination).expect("destination after rejects"),
        bytes
    );
}

struct TempDir {
    path: PathBuf,
}
impl TempDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symdesk-index-restore-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create scratch directory");
        Self { path }
    }
}
impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove scratch directory");
    }
}
