use super::*;

struct TestRoot(PathBuf);

impl TestRoot {
    fn new(name: &str) -> Self {
        let path =
            std::env::temp_dir().join(format!("symdesk-index-open-{name}-{}", std::process::id()));
        fs::create_dir(&path).expect("create isolated root");
        Self(path)
    }
}

impl Drop for TestRoot {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.0).expect("remove isolated root");
    }
}

fn sqlite_failure(path: &Path) -> rusqlite::Error {
    match Sidecar::open(path) {
        Err(SidecarError::Sqlite(error)) => error,
        Err(other) => panic!("expected structured SQLite error, got {other:?}"),
        Ok(_) => panic!("expected open failure"),
    }
}

// Independent baseline for the connection policy used before the shared opener.
fn open_with_previous_policy(path: &Path) -> rusqlite::Result<Connection> {
    let connection = Connection::open(path)?;
    connection.busy_timeout(std::time::Duration::from_millis(5000))?;
    connection.pragma_update(None, "foreign_keys", true)?;
    connection.pragma_update(None, "journal_mode", "WAL")?;
    Ok(connection)
}

#[test]
fn open_failures_preserve_sqlite_payload_and_existing_data() {
    let root = TestRoot::new("failures");
    let directory = root.0.join("directory.db");
    fs::create_dir(&directory).unwrap();
    assert_eq!(
        sqlite_failure(&directory).sqlite_error_code(),
        Some(rusqlite::ErrorCode::CannotOpen)
    );
    assert_eq!(fs::read_dir(&directory).unwrap().count(), 0);

    let corrupt = root.0.join("corrupt.db");
    let bytes = [b'x'; 4096];
    fs::write(&corrupt, bytes).unwrap();
    let expected = open_with_previous_policy(&corrupt).unwrap_err();
    let actual = sqlite_failure(&corrupt);
    assert_eq!(actual, expected);
    assert_eq!(
        actual.sqlite_error_code(),
        Some(rusqlite::ErrorCode::NotADatabase)
    );
    assert_eq!(fs::read(&corrupt).unwrap(), bytes);
}

#[test]
fn parent_failure_stays_io_and_does_not_replace_file() {
    let root = TestRoot::new("parent-failure");
    let parent = root.0.join("file");
    fs::write(&parent, b"keep").unwrap();
    let expected = fs::create_dir_all(&parent).unwrap_err();
    match Sidecar::open(&parent.join("index.db")) {
        Err(SidecarError::Io(error)) => {
            assert_eq!(error.kind(), expected.kind());
            assert_eq!(error.raw_os_error(), expected.raw_os_error());
        }
        _ => panic!("expected parent IO failure"),
    }
    assert_eq!(fs::read(parent).unwrap(), b"keep");
}

#[cfg(unix)]
#[test]
fn legacy_symlink_parent_and_new_directory_modes_survive_shared_open() {
    use std::os::unix::fs::{PermissionsExt, symlink};

    let root = TestRoot::new("symlink");
    let actual = root.0.join("actual");
    fs::create_dir(&actual).unwrap();
    fs::set_permissions(&actual, fs::Permissions::from_mode(0o750)).unwrap();
    let legacy = root.0.join("legacy");
    symlink(&actual, &legacy).unwrap();
    let path = legacy.join("new/nested/index.db");
    let sidecar = Sidecar::open(&path).expect("legacy symlink is accepted");
    assert!(actual.join("new/nested/index.db").is_file());
    assert_eq!(
        fs::metadata(&actual).unwrap().permissions().mode() & 0o777,
        0o750
    );
    for relative in ["new", "new/nested"] {
        assert_eq!(
            fs::metadata(actual.join(relative))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o700
        );
    }
    assert!(
        fs::symlink_metadata(legacy)
            .unwrap()
            .file_type()
            .is_symlink()
    );
    drop(sidecar);
}

#[test]
fn invalid_filename_retains_sqlite_error_after_parent_creation() {
    let root = TestRoot::new("invalid-path");
    let parent = root.0.join("created");
    let path = parent.join("invalid-\0.db");
    let actual = sqlite_failure(&path);
    assert_eq!(actual, open_with_previous_policy(&path).unwrap_err());
    assert!(matches!(actual, rusqlite::Error::NulError(_)));
    assert!(parent.is_dir());
    assert_eq!(fs::read_dir(parent).unwrap().count(), 0);
}

#[cfg(target_os = "macos")]
#[test]
fn non_utf8_filename_retains_macos_sqlite_failure_after_parent_creation() {
    use std::os::unix::ffi::OsStringExt;

    let root = TestRoot::new("non-utf8-path");
    let parent = root.0.join("created");
    let path = parent.join(std::ffi::OsString::from_vec(b"invalid-\xff.db".to_vec()));
    let actual = sqlite_failure(&path);
    assert_eq!(actual, open_with_previous_policy(&path).unwrap_err());
    assert_eq!(
        actual.sqlite_error_code(),
        Some(rusqlite::ErrorCode::CannotOpen)
    );
    assert!(parent.is_dir());
    assert_eq!(fs::read_dir(parent).unwrap().count(), 0);
}

#[test]
fn reopen_keeps_product_migrations_and_repairs_missing_norm_rows() {
    let root = TestRoot::new("backfill");
    let path = root.0.join("index.db");
    let sidecar = Sidecar::open(&path).unwrap();
    sidecar
        .connection
        .execute(
            "INSERT INTO fts_search(rowid,title,body) VALUES (42,'Müller','Straße')",
            [],
        )
        .unwrap();
    sidecar
        .connection
        .execute(
            "UPDATE schema_migrations SET applied_at = '2000-01-01 00:00:00'",
            [],
        )
        .unwrap();
    let applied: Vec<(String, String)> = sidecar
        .connection
        .prepare("SELECT version, applied_at FROM schema_migrations ORDER BY version")
        .unwrap()
        .query_map([], |row| Ok((row.get(0)?, row.get(1)?)))
        .unwrap()
        .collect::<Result<_, _>>()
        .unwrap();
    assert_eq!(applied.len(), MIGRATIONS.len());
    drop(sidecar);
    for _ in 0..2 {
        let reopened = Sidecar::open(&path).unwrap();
        let norm: String = reopened
            .connection
            .query_row("SELECT norm FROM fts_norm WHERE rowid=42", [], |row| {
                row.get(0)
            })
            .unwrap();
        assert_eq!(norm, symdesk_core::german::normalized_text("Müller Straße"));
        for (version, timestamp) in &applied {
            let actual: String = reopened
                .connection
                .query_row(
                    "SELECT applied_at FROM schema_migrations WHERE version=?",
                    [version],
                    |row| row.get(0),
                )
                .unwrap();
            assert_eq!(&actual, timestamp);
        }
        assert_eq!(
            reopened
                .connection
                .query_row("PRAGMA journal_mode", [], |row| row.get::<_, String>(0))
                .unwrap(),
            "wal"
        );
        assert_eq!(
            reopened
                .connection
                .query_row("PRAGMA foreign_keys", [], |row| row.get::<_, i64>(0))
                .unwrap(),
            1
        );
        assert_eq!(
            reopened
                .connection
                .query_row("PRAGMA busy_timeout", [], |row| row.get::<_, i64>(0))
                .unwrap(),
            5000
        );
    }
}
