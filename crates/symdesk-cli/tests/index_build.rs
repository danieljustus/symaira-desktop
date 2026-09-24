#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::Path,
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::Connection;
use serde::Deserialize;

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct ProcessResult {
    exit_code: i32,
    stdout: String,
    stderr: String,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct IndexedFile {
    path: String,
    title: String,
    body: String,
}

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    default_first: ProcessResult,
    default_again: ProcessResult,
    reembed: ProcessResult,
    reembed_no_network: bool,
    prune: ProcessResult,
    explicit: ProcessResult,
    missing: ProcessResult,
    default_files: Vec<IndexedFile>,
    explicit_files: Vec<IndexedFile>,
    lifecycle: BTreeMap<String, String>,
    lifecycle_reasons: BTreeMap<String, String>,
    metadata: bool,
}

struct TempRoot(std::path::PathBuf);

impl TempRoot {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system time")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-index-build-{}-{nonce}",
            std::process::id()
        ));
        for name in ["home", "cwd", "data", "tmp", "vault", "explicit"] {
            fs::create_dir_all(root.join(name)).expect("create fixture directory");
        }
        Self(root)
    }

    fn path(&self, child: &str) -> std::path::PathBuf {
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
        .join("../../testdata/port/cli/index-build-process.json");
    serde_json::from_slice(&fs::read(path).expect("Go-generated index build fixture"))
        .expect("valid fixture")
}

fn run(root: &TempRoot, args: &[&str]) -> ProcessResult {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command.args(args).current_dir(root.path("cwd"));
    for (key, value) in [
        ("HOME", root.path("home")),
        ("USERPROFILE", root.path("home")),
        ("XDG_DATA_HOME", root.path("data")),
        ("TMPDIR", root.path("tmp")),
        ("TMP", root.path("tmp")),
        ("TEMP", root.path("tmp")),
    ] {
        command.env(key, value);
    }
    for key in ["SYMDESK_SIDECAR", "SYMDESK_VAULT", "XDG_CONFIG_HOME"] {
        command.env_remove(key);
    }
    command
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC")
        .env("TERM", "dumb")
        .env("NO_COLOR", "1");
    result(command.output().expect("run Rust CLI process"), &root.0)
}

fn result(output: Output, root: &Path) -> ProcessResult {
    ProcessResult {
        exit_code: output.status.code().unwrap_or(-1),
        stdout: normalize(&String::from_utf8_lossy(&output.stdout), root),
        stderr: normalize(&String::from_utf8_lossy(&output.stderr), root),
    }
}

fn normalize(text: &str, root: &Path) -> String {
    let root = root.to_string_lossy();
    text.replace(&format!("{}\\\\", root.replace('\\', "\\\\")), "$ROOT/")
        .replace(&format!("{}\\", root), "$ROOT/")
        .replace(root.as_ref(), "$ROOT")
}

#[test]
fn normalizes_json_escaped_windows_root() {
    let root = Path::new(r"C:\temp\fixture");
    assert_eq!(
        normalize(r#"{"error":"stat C:\\temp\\fixture\\missing"}"#, root),
        r#"{"error":"stat $ROOT/missing"}"#
    );
}

#[test]
fn index_build_cli_replays_go_process_fixture() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    let root = TempRoot::new();
    fs::write(
        root.path("vault/first.md"),
        "# First\n\nfirst oracle document.",
    )
    .expect("first note");
    fs::create_dir_all(root.path("vault/nested")).expect("nested vault");
    fs::write(
        root.path("vault/nested/second.md"),
        "# Second\n\nsecond oracle document.",
    )
    .expect("second note");
    fs::write(
        root.path("explicit/only.md"),
        "# Explicit\n\nexplicit oracle document.",
    )
    .expect("explicit note");
    fs::write(root.path("vault/draft.DOC"), "legacy office document").expect("legacy document");
    fs::write(root.path("vault/book.mobi"), "ebook document").expect("ebook document");

    let config_directory = root.path("home/.config/symseek");
    fs::create_dir_all(&config_directory).expect("create isolated symseek config directory");
    fs::write(
        config_directory.join("config.toml"),
        format!(
            "index_path = {:?}\n",
            root.path("data/retrieval.db").to_string_lossy()
        ),
    )
    .expect("configure isolated standalone retrieval database");
    let retrieval = Connection::open(root.path("data/retrieval.db")).expect("create retrieval db");
    retrieval
        .execute_batch("CREATE TABLE chunks (document_path TEXT NOT NULL, embedding_pending INTEGER NOT NULL DEFAULT 0);")
        .expect("create no-pending retrieval schema");

    let default_vault = root.path("vault").to_string_lossy().into_owned();
    let explicit_vault = root.path("explicit").to_string_lossy().into_owned();
    let missing_vault = root.path("missing").to_string_lossy().into_owned();
    assert_eq!(
        run(&root, &["--json", "--vault", &default_vault, "index"]),
        fixture.default_first
    );
    assert_eq!(
        run(&root, &["--json", "--vault", &default_vault, "index"]),
        fixture.default_again
    );
    assert_eq!(
        run(&root, &["--vault", &default_vault, "index", "--re-embed"]),
        fixture.reembed
    );
    assert!(
        fixture.reembed_no_network,
        "Go no-pending re-embed made no local fake-provider request"
    );
    fs::remove_file(root.path("vault/nested/second.md")).expect("remove stale indexed note");
    assert_eq!(
        run(
            &root,
            &["--json", "--vault", &default_vault, "index", "--prune"]
        ),
        fixture.prune
    );
    assert_eq!(
        run(
            &root,
            &[
                "--json",
                "--vault",
                &default_vault,
                "index",
                &explicit_vault
            ]
        ),
        fixture.explicit
    );
    assert_eq!(
        run(&root, &["--json", "--vault", &missing_vault, "index"]),
        fixture.missing
    );

    let default_path = fixture_sidecar_path(&root, "vault");
    let default_db = Connection::open(&default_path).expect("open default sidecar");
    let default_files = read_files(&default_db, &root.0);
    assert_eq!(default_files, fixture.default_files);
    let lifecycle = read_lifecycle(&default_db);
    assert_eq!(lifecycle, fixture.lifecycle);
    let lifecycle_reasons = read_lifecycle_reasons(&default_db);
    assert_eq!(lifecycle_reasons, fixture.lifecycle_reasons);
    let explicit_db =
        Connection::open(fixture_sidecar_path(&root, "explicit")).expect("open explicit sidecar");
    assert_eq!(read_files(&explicit_db, &root.0), fixture.explicit_files);
    assert_eq!(
        fixture.metadata,
        default_path
            .parent()
            .expect("sidecar parent")
            .join("metadata.json")
            .is_file()
    );
}

fn fixture_sidecar_path(root: &TempRoot, vault: &str) -> std::path::PathBuf {
    let canonical = fs::canonicalize(root.path(vault)).expect("canonical fixture vault");
    let digest = symdesk_vault::sha256_hex(canonical.to_string_lossy().as_bytes());
    root.path("data")
        .join("symdesk/vaults")
        .join(&digest[..16])
        .join("sidecar.db")
}

fn read_files(connection: &Connection, root: &Path) -> Vec<IndexedFile> {
    let mut statement = connection.prepare("SELECT files.path,files.title,fts_search.body FROM files JOIN fts_search ON fts_search.rowid=files.id ORDER BY files.path").expect("prepare files query");
    statement
        .query_map([], |row| {
            Ok(IndexedFile {
                path: row.get::<_, String>(0)?,
                title: row.get(1)?,
                body: row.get(2)?,
            })
        })
        .expect("query files")
        .map(|row| {
            let mut file = row.expect("file row");
            file.path = normalize(&file.path, root);
            file
        })
        .collect()
}

fn read_lifecycle(connection: &Connection) -> BTreeMap<String, String> {
    let mut statement = connection
        .prepare("SELECT path,state FROM index_lifecycle ORDER BY path")
        .expect("prepare lifecycle query");
    statement
        .query_map([], |row| {
            Ok((
                Path::new(&row.get::<_, String>(0)?)
                    .file_name()
                    .expect("filename")
                    .to_string_lossy()
                    .into_owned(),
                row.get(1)?,
            ))
        })
        .expect("query lifecycle")
        .map(|row| {
            let (path, state) = row.expect("lifecycle row");
            (path, state)
        })
        .collect()
}

fn read_lifecycle_reasons(connection: &Connection) -> BTreeMap<String, String> {
    let mut statement = connection
        .prepare("SELECT path,reason FROM index_lifecycle WHERE reason <> '' ORDER BY path")
        .expect("prepare lifecycle reasons query");
    statement
        .query_map([], |row| {
            Ok((
                Path::new(&row.get::<_, String>(0)?)
                    .file_name()
                    .expect("filename")
                    .to_string_lossy()
                    .into_owned(),
                row.get(1)?,
            ))
        })
        .expect("query lifecycle reasons")
        .map(|row| {
            let (path, reason) = row.expect("lifecycle reason row");
            (path, reason)
        })
        .collect()
}
