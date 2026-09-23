use std::{
    fs,
    io::Read,
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::{Connection, params};
use serde::Deserialize;
use symdesk_index::relocate_database;

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    input_rows: Vec<Row>,
    source_rows_after: Vec<Row>,
    relocated_rows: Vec<Row>,
    wal_was_nonempty: bool,
    persisted_index_path: String,
    header: String,
    mode: Option<String>,
    destination_replaced: bool,
    same_path_error: String,
    rename_conflict_error: String,
    conflict_marker_unchanged: bool,
    blocked_parent_error: String,
    blocked_marker_unchanged: bool,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Row {
    id: i64,
    body: String,
}

struct TestDir(PathBuf);

impl TestDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock before Unix epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symdesk-index-relocate-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create isolated test directory");
        Self(path)
    }
}

impl Drop for TestDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join("testdata/port/retrieval/index-relocate.json");
    let bytes = fs::read(&path).expect("Go-generated index relocation fixture");
    serde_json::from_slice(&bytes).expect("valid relocation fixture")
}

#[test]
fn relocate_replays_go_wal_snapshot_and_rename_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert!(fixture.wal_was_nonempty, "Go oracle observed an empty WAL");
    assert!(
        fixture.destination_replaced,
        "Go oracle did not replace destination"
    );
    assert!(fixture.conflict_marker_unchanged);
    assert!(fixture.blocked_marker_unchanged);

    let root = TestDir::new();
    let source = root.0.join("source.db");
    let connection = Connection::open(&source).expect("open WAL source database");
    connection
        .execute_batch(
            "PRAGMA journal_mode=WAL;
             PRAGMA wal_autocheckpoint=0;
             CREATE TABLE relocate_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL);",
        )
        .expect("create source schema");
    for row in &fixture.input_rows {
        connection
            .execute(
                "INSERT INTO relocate_rows (id, body) VALUES (?1, ?2)",
                params![row.id, row.body],
            )
            .expect("insert source row");
    }
    let wal = fs::metadata(format!("{}-wal", source.display())).expect("source WAL exists");
    assert!(wal.len() > 0, "source WAL is empty before relocation");

    let destination = root.0.join("relocated/retrieval.db");
    fs::create_dir_all(destination.parent().expect("destination parent"))
        .expect("create destination directory");
    fs::write(&destination, b"old destination").expect("seed old destination");
    let actual_path = relocate_database(&connection, &destination).expect("relocate snapshot");
    let expected_path = fixture
        .persisted_index_path
        .replace("$ROOT", &root.0.to_string_lossy());
    assert_eq!(actual_path, PathBuf::from(expected_path));

    assert_eq!(read_rows(&connection), fixture.source_rows_after);
    let relocated = Connection::open(&destination).expect("open relocated snapshot");
    assert_eq!(read_rows(&relocated), fixture.relocated_rows);
    drop(relocated);

    let mut header = [0; 16];
    fs::File::open(&destination)
        .expect("open relocated file")
        .read_exact(&mut header)
        .expect("read relocated header");
    assert_eq!(String::from_utf8_lossy(&header), fixture.header);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = format!(
            "{:04o}",
            fs::metadata(&destination)
                .expect("stat relocated file")
                .permissions()
                .mode()
                & 0o777
        );
        assert_eq!(Some(mode), fixture.mode);
    }

    let same_path =
        relocate_database(&connection, &source).expect_err("same-file relocation must fail");
    assert_eq!(same_path.to_string(), fixture.same_path_error);

    let conflict = root.0.join("occupied.db");
    fs::create_dir(&conflict).expect("create conflicting directory target");
    let marker = b"preserve the existing destination";
    fs::write(conflict.join("marker"), marker).expect("seed conflict marker");
    let conflict_error =
        relocate_database(&connection, &conflict).expect_err("directory target must fail rename");
    assert!(
        conflict_error
            .to_string()
            .starts_with(&fixture.rename_conflict_error),
        "rename conflict error {:?} did not start with {:?}",
        conflict_error.to_string(),
        fixture.rename_conflict_error
    );
    assert_eq!(
        fs::read(conflict.join("marker")).expect("read conflict marker"),
        marker
    );

    let blocked_parent = root.0.join("ordinary-file");
    fs::write(&blocked_parent, b"keep this marker").expect("seed blocked parent");
    let blocked = relocate_database(&connection, &blocked_parent.join("retrieval.db"))
        .expect_err("non-directory parent must fail");
    assert!(
        blocked
            .to_string()
            .starts_with(&fixture.blocked_parent_error),
        "blocked-parent error {:?} did not start with {:?}",
        blocked.to_string(),
        fixture.blocked_parent_error
    );
    assert_eq!(
        fs::read(&blocked_parent).expect("read blocked parent marker"),
        b"keep this marker"
    );
}

fn read_rows(connection: &Connection) -> Vec<Row> {
    let mut statement = connection
        .prepare("SELECT id, body FROM relocate_rows ORDER BY id")
        .expect("prepare source rows query");
    statement
        .query_map([], |row| {
            Ok(Row {
                id: row.get(0)?,
                body: row.get(1)?,
            })
        })
        .expect("query rows")
        .collect::<Result<Vec<_>, _>>()
        .expect("read rows")
}
