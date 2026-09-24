#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    ffi::OsStr,
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::{Connection, params};
use serde::Deserialize;

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct ProcessResult {
    exit_code: i32,
    stdout: String,
    stderr: String,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Row {
    id: i64,
    body: String,
}

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    location: ProcessResult,
    location_text: ProcessResult,
    backup: ProcessResult,
    backup_rows: Vec<Row>,
    restore: ProcessResult,
    source_rows_after_restore: Vec<Row>,
    relocate: ProcessResult,
    location_after_relocate: ProcessResult,
    source_rows_after_relocate: Vec<Row>,
    relocated_rows: Vec<Row>,
    vault_relocate_rejected: ProcessResult,
    backup_missing_output: ProcessResult,
    source_preserved_after_relocation: bool,
    destination_replaced: bool,
}

struct TempRoot(PathBuf);

impl TempRoot {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-index-maintenance-{}-{nonce}",
            std::process::id()
        ));
        for directory in ["home", "cwd", "data", "tmp", "vault"] {
            fs::create_dir_all(root.join(directory)).expect("create isolated fixture directory");
        }
        Self(root)
    }

    fn path(&self, child: &str) -> PathBuf {
        self.0.join(child)
    }
}

impl Drop for TempRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/cli/index-maintenance-process.json");
    serde_json::from_slice(&fs::read(path).expect("Go-generated process fixture"))
        .expect("valid Go process fixture")
}

#[test]
fn index_maintenance_cli_replays_go_process_fixture() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);

    let root = TempRoot::new();
    let home = root.path("home");
    let cwd = root.path("cwd");
    let data_home = root.path("data");
    let temp_root = root.path("tmp");
    let source = data_home.join("retrieval.db");
    let backup = root.path("backup.db");
    let destination = root.path("relocated/retrieval.db");
    let rejected = root.path("rejected.db");
    let vault = root.path("vault");
    let config = home.join(".config/symseek/config.toml");
    fs::create_dir_all(config.parent().expect("config parent")).expect("create config parent");
    fs::write(
        &config,
        format!(
            "index_path = {:?}\nmodel = \"process-fixture\"\n",
            source.to_str().expect("UTF-8 fixture path")
        ),
    )
    .expect("seed isolated retrieval config");

    let connection = Connection::open(&source).expect("create source index");
    connection
        .pragma_update(None, "journal_mode", "WAL")
        .expect("enable WAL mode");
    connection
        .pragma_update(None, "wal_autocheckpoint", 0)
        .expect("disable automatic WAL checkpoint");
    connection
        .execute_batch("CREATE TABLE process_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL);")
        .expect("create fixture table");
    connection
        .execute(
            "INSERT INTO process_rows (id, body) VALUES (?1, ?2)",
            params![1, "committed only to WAL"],
        )
        .expect("insert WAL-only row");

    let environment = BTreeMap::from([
        ("HOME".to_owned(), home.to_string_lossy().into_owned()),
        (
            "USERPROFILE".to_owned(),
            home.to_string_lossy().into_owned(),
        ),
        (
            "XDG_CONFIG_HOME".to_owned(),
            home.join("wrong-config").to_string_lossy().into_owned(),
        ),
        (
            "XDG_DATA_HOME".to_owned(),
            data_home.to_string_lossy().into_owned(),
        ),
        (
            "TMPDIR".to_owned(),
            temp_root.to_string_lossy().into_owned(),
        ),
        ("TMP".to_owned(), temp_root.to_string_lossy().into_owned()),
        ("TEMP".to_owned(), temp_root.to_string_lossy().into_owned()),
        ("LANG".to_owned(), "C".to_owned()),
        ("LC_ALL".to_owned(), "C".to_owned()),
        ("TZ".to_owned(), "UTC".to_owned()),
        ("TERM".to_owned(), "dumb".to_owned()),
        ("NO_COLOR".to_owned(), "1".to_owned()),
    ]);
    let run = |arguments: &[&OsStr]| {
        let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        command
            .env_clear()
            .current_dir(&cwd)
            .envs(&environment)
            .args(arguments);
        normalize_output(command.output().expect("run Rust CLI process"), &root.0)
    };

    let actual_location = run(&strings(["--json", "index", "maintenance", "location"]));
    assert_eq!(actual_location, fixture.location);
    let actual_location_text = run(&strings(["index", "maintenance", "location"]));
    assert_eq!(actual_location_text, fixture.location_text);

    let actual_backup = run(&strings([
        "--output",
        "json",
        "index",
        "maintenance",
        "backup",
        "--output",
        backup.to_str().expect("UTF-8 backup path"),
    ]));
    assert_eq!(actual_backup, fixture.backup);
    assert_eq!(read_rows(&backup), fixture.backup_rows);

    connection
        .execute(
            "INSERT INTO process_rows (id, body) VALUES (?1, ?2)",
            params![2, "later write must be removed by restore"],
        )
        .expect("write post-backup source row");
    drop(connection);

    let actual_restore = run(&strings([
        "--output",
        "json",
        "index",
        "maintenance",
        "restore",
        "--input",
        backup.to_str().expect("UTF-8 backup path"),
    ]));
    assert_eq!(actual_restore, fixture.restore);
    assert_eq!(read_rows(&source), fixture.source_rows_after_restore);

    fs::create_dir_all(destination.parent().expect("destination parent"))
        .expect("create destination directory");
    fs::write(&destination, b"old destination").expect("seed replacement target");
    let actual_relocate = run(&strings([
        "--output",
        "json",
        "index",
        "maintenance",
        "relocate",
        "--output",
        destination.to_str().expect("UTF-8 destination path"),
    ]));
    assert_eq!(actual_relocate, fixture.relocate);
    let actual_location_after_relocate = run(&strings([
        "--output",
        "json",
        "index",
        "maintenance",
        "location",
    ]));
    assert_eq!(
        actual_location_after_relocate,
        fixture.location_after_relocate
    );
    assert_eq!(read_rows(&source), fixture.source_rows_after_relocate);
    assert_eq!(read_rows(&destination), fixture.relocated_rows);

    let actual_vault_reject = run(&strings([
        "--output",
        "json",
        "--vault",
        vault.to_str().expect("UTF-8 vault path"),
        "index",
        "maintenance",
        "relocate",
        "--output",
        rejected.to_str().expect("UTF-8 rejected path"),
    ]));
    assert_eq!(actual_vault_reject, fixture.vault_relocate_rejected);
    assert!(!rejected.exists());

    let actual_missing_output = run(&strings(["--json", "index", "maintenance", "backup"]));
    assert_eq!(actual_missing_output, fixture.backup_missing_output);
    assert!(fixture.source_preserved_after_relocation);
    assert!(fixture.destination_replaced);
}

fn strings<const N: usize>(values: [&str; N]) -> [&OsStr; N] {
    values.map(OsStr::new)
}

fn normalize_output(output: Output, root: &Path) -> ProcessResult {
    let stdout = String::from_utf8(output.stdout).expect("UTF-8 CLI stdout");
    let stderr = String::from_utf8(output.stderr).expect("UTF-8 CLI stderr");
    ProcessResult {
        exit_code: output.status.code().unwrap_or(-1),
        stdout: normalize_path_output(stdout, root),
        stderr: normalize_path_output(stderr, root),
    }
}

fn normalize_path_output(value: String, root: &Path) -> String {
    let root = root.to_string_lossy();
    value
        .replace(&root.replace('\\', r"\\"), "$ROOT")
        .replace(root.as_ref(), "$ROOT")
        .replace(r"\\", "/")
        .replace('\\', "/")
}

#[test]
fn normalizes_json_escaped_windows_index_path() {
    let root = Path::new(r"C:\Users\runner\Temp\fixture");
    assert_eq!(
        normalize_path_output(
            r#"{"index_location":"C:\\Users\\runner\\Temp\\fixture\\data\\retrieval.db"}"#
                .to_owned(),
            root,
        ),
        r#"{"index_location":"$ROOT/data/retrieval.db"}"#
    );
}

fn read_rows(path: &Path) -> Vec<Row> {
    let connection = Connection::open(path).expect("open index rows");
    let mut statement = connection
        .prepare("SELECT id, body FROM process_rows ORDER BY id")
        .expect("prepare row query");
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
