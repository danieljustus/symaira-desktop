use std::{
    fs,
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;
use serde::Deserialize;
use symdesk_index::backup_database;

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    input_rows: Vec<Row>,
    observed_rows: Vec<Row>,
    wal_was_nonempty: bool,
    header: String,
    #[cfg_attr(not(unix), allow(dead_code))]
    mode: Option<String>,
    #[cfg_attr(not(unix), allow(dead_code))]
    directory_mode: Option<String>,
    same_path_error: String,
    blocked_parent_error: String,
    replacement_okay: bool,
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
            "symdesk-index-backup-{}-{nonce}",
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
        .join("testdata/port/retrieval/index-backup.json");
    let bytes = fs::read(&path).expect("Go-generated index backup fixture");
    serde_json::from_slice(&bytes).expect("valid index backup fixture")
}

#[test]
fn backup_replays_go_wal_snapshot_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert!(
        fixture.wal_was_nonempty,
        "Go oracle observed no committed WAL"
    );
    assert!(
        fixture.replacement_okay,
        "Go oracle did not replace destination"
    );

    let root = TestDir::new();
    let source = root.0.join("source.db");
    let connection = Connection::open(&source).expect("open source database");
    connection
        .execute_batch(
            "PRAGMA journal_mode=WAL;
             PRAGMA wal_autocheckpoint=0;
             CREATE TABLE backup_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL);",
        )
        .expect("create WAL source schema");
    for row in &fixture.input_rows {
        connection
            .execute(
                "INSERT INTO backup_rows (id, body) VALUES (?1, ?2)",
                (&row.id, &row.body),
            )
            .expect("insert source row");
    }
    let wal = fs::metadata(format!("{}-wal", source.display())).expect("source WAL exists");
    assert!(wal.len() > 0, "source WAL is empty before backup");

    let destination = root.0.join("nested/backup.db");
    fs::create_dir_all(destination.parent().expect("destination parent"))
        .expect("create destination directory");
    fs::write(&destination, b"old destination").expect("seed old destination");
    backup_database(&connection, &destination).expect("backup open database");

    let mut header = [0; 16];
    use std::io::Read;
    fs::File::open(&destination)
        .expect("open backup")
        .read_exact(&mut header)
        .expect("read backup header");
    assert_eq!(String::from_utf8_lossy(&header), fixture.header);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = format!(
            "{:04o}",
            fs::metadata(&destination).unwrap().permissions().mode() & 0o777
        );
        assert_eq!(Some(mode), fixture.mode);
    }
    let created_destination = root.0.join("created/deeper/backup.db");
    backup_database(&connection, &created_destination)
        .expect("create destination directories and snapshot");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = format!(
            "{:04o}",
            fs::metadata(created_destination.parent().unwrap())
                .unwrap()
                .permissions()
                .mode()
                & 0o777
        );
        assert_eq!(Some(mode), fixture.directory_mode);
    }
    let backup = Connection::open(&destination).expect("open backup database");
    let mut statement = backup
        .prepare("SELECT id, body FROM backup_rows ORDER BY id")
        .expect("prepare backup row query");
    let observed = statement
        .query_map([], |row| {
            Ok(Row {
                id: row.get(0)?,
                body: row.get(1)?,
            })
        })
        .expect("query backup rows")
        .collect::<Result<Vec<_>, _>>()
        .expect("read backup rows");
    assert_eq!(observed, fixture.observed_rows);

    let same_path =
        backup_database(&connection, &source.join(".")).expect_err("same-path snapshot must fail");
    assert_eq!(same_path.to_string(), fixture.same_path_error);

    let blocked_parent = root.0.join("ordinary-file");
    fs::write(&blocked_parent, b"file").expect("seed non-directory parent");
    let blocked = backup_database(&connection, &blocked_parent.join("backup.db"))
        .expect_err("non-directory parent must fail");
    assert!(
        blocked
            .to_string()
            .starts_with(&fixture.blocked_parent_error),
        "error {:?} did not start with oracle prefix {:?}",
        blocked.to_string(),
        fixture.blocked_parent_error
    );
}
