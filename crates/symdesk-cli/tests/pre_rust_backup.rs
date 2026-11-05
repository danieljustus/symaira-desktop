//! RUST-018 data safety: the automatic one-time backup of existing state before
//! the first Rust run, its opt-out, failure policy and the verified restore.
//!
//! These tests drive the real `symdesk` binary in disposable HOME/XDG roots with
//! hand-seeded state (including SQLite databases, one with a live WAL). The
//! Go-created state restore exercise lives in `cutover_state.rs`.

#![deny(unsafe_code)]

#[path = "../../test-support/process_env.rs"]
mod process_env;

use std::{
    fs,
    io::Write,
    path::{Path, PathBuf},
    process::{Command, Output, Stdio},
    sync::atomic::{AtomicU32, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use process_env::IsolatedEnvironment;
use rusqlite::Connection;
use sha2::{Digest, Sha256};

const BINARY: &str = env!("CARGO_BIN_EXE_symdesk");
static SEQUENCE: AtomicU32 = AtomicU32::new(0);

struct Env {
    root: PathBuf,
}

impl Env {
    fn new() -> Self {
        let root = std::env::temp_dir().join(format!(
            "symdesk-prerust-{}-{}-{}",
            std::process::id(),
            SEQUENCE.fetch_add(1, Ordering::SeqCst),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        for directory in ["home", "config", "cache", "data", "vault", "cwd"] {
            fs::create_dir_all(root.join(directory)).unwrap();
        }
        Self {
            root: fs::canonicalize(root).unwrap(),
        }
    }

    fn data(&self) -> PathBuf {
        self.root.join("data/symdesk")
    }

    fn run(&self, backup: &str, args: &[&str]) -> Output {
        self.run_with(backup, args, None)
    }

    fn run_with(&self, backup: &str, args: &[&str], stdin: Option<&[u8]>) -> Output {
        let mut command = Command::new(BINARY);
        command.current_dir(self.root.join("cwd")).isolated_env();
        for (key, directory) in [
            ("HOME", "home"),
            ("USERPROFILE", "home"),
            ("XDG_CONFIG_HOME", "config"),
            ("XDG_CACHE_HOME", "cache"),
            ("XDG_DATA_HOME", "data"),
        ] {
            command.env(key, self.root.join(directory));
        }
        command
            .env("TZ", "UTC")
            .env("LANG", "C")
            .env("PATH", "/usr/bin:/bin");
        // `on` enables the backup; `off` is what isolated_env() already set.
        command.env("SYMDESK_PRE_RUST_BACKUP", backup);
        command.args(args);
        match stdin {
            None => command.output().expect("run symdesk"),
            Some(input) => {
                let mut child = command
                    .stdin(Stdio::piped())
                    .stdout(Stdio::piped())
                    .stderr(Stdio::piped())
                    .spawn()
                    .expect("spawn symdesk");
                child.stdin.take().unwrap().write_all(input).unwrap();
                child.wait_with_output().unwrap()
            }
        }
    }

    /// Existing "Go-era" state: databases (rollback-journal and WAL mode), JSON,
    /// config, a legacy fallback directory, an archive and an old backups dir.
    fn seed_state(&self) {
        let data = self.data();
        let vault_dir = data.join("vaults/0123456789abcdef");
        fs::create_dir_all(&vault_dir).unwrap();
        fs::create_dir_all(data.join("archive")).unwrap();
        fs::create_dir_all(data.join("backups")).unwrap();
        fs::create_dir_all(self.root.join("config/symdesk")).unwrap();
        fs::create_dir_all(self.root.join("data/symingest")).unwrap();
        fs::write(
            vault_dir.join("metadata.json"),
            br#"{"vault_path":"/x","last_used":"2026-01-01T00:00:00Z"}"#,
        )
        .unwrap();
        fs::write(data.join("archive/big.bin"), vec![7_u8; 4096]).unwrap();
        fs::write(data.join("backups/old.txt"), b"older backup").unwrap();
        fs::write(
            self.root.join("config/symdesk/config.toml"),
            b"vault = \"/x\"\n",
        )
        .unwrap();
        fs::write(
            self.root.join("data/symingest/legacy.txt"),
            b"legacy ingest state\n",
        )
        .unwrap();
        for database in [data.join("symingest.db"), vault_dir.join("sidecar.db")] {
            let connection = Connection::open(&database).unwrap();
            connection
                .execute_batch(
                    "CREATE TABLE t(id INTEGER PRIMARY KEY, body TEXT);
                     INSERT INTO t(body) VALUES ('one'), ('two'), ('three');",
                )
                .unwrap();
        }
    }
}

impl Drop for Env {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.root);
    }
}

fn sha256(path: &Path) -> String {
    let digest = Sha256::digest(fs::read(path).unwrap());
    digest.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn manifest(env: &Env) -> serde_json::Value {
    let path = env.data().join("backups/pre-rust/manifest.json");
    serde_json::from_slice(&fs::read(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display())))
        .unwrap()
}

/// Every file below `root` except the `backups` directory: path -> sha256.
fn tree(root: &Path) -> std::collections::BTreeMap<String, String> {
    fn walk(root: &Path, dir: &Path, out: &mut std::collections::BTreeMap<String, String>) {
        let Ok(entries) = fs::read_dir(dir) else {
            return;
        };
        for entry in entries.flatten() {
            let path = entry.path();
            let relative = path
                .strip_prefix(root)
                .unwrap()
                .to_string_lossy()
                .into_owned();
            if path.is_dir() {
                walk(root, &path, out);
            } else {
                out.insert(relative, sha256(&path));
            }
        }
    }
    let mut out = std::collections::BTreeMap::new();
    walk(root, root, &mut out);
    out
}

fn text(bytes: &[u8]) -> String {
    String::from_utf8_lossy(bytes).into_owned()
}

#[test]
fn first_run_creates_backup_with_correct_hashes_and_second_run_does_nothing() {
    let env = Env::new();
    env.seed_state();
    let before = tree(&env.root);

    let off = env.run("off", &["config", "paths"]);
    assert_eq!(off.status.code(), Some(0), "{}", text(&off.stderr));
    assert!(
        !env.data().join("backups/pre-rust").exists(),
        "opt-out must not create a backup"
    );
    assert!(off.stderr.is_empty());

    let first = env.run("on", &["config", "paths"]);
    assert_eq!(first.status.code(), Some(0), "{}", text(&first.stderr));
    // stdout is identical with and without the backup: it stays reserved for the command.
    assert_eq!(first.stdout, off.stdout);
    let notice = text(&first.stderr);
    assert_eq!(
        notice.matches("backed up once").count(),
        1,
        "exactly one notice: {notice}"
    );
    assert!(notice.contains("backup restore-pre-rust"));

    let manifest = manifest(&env);
    assert_eq!(manifest["schema_version"], 1);
    assert_eq!(manifest["tool"], "symdesk");
    assert!(manifest["created_at"].as_str().unwrap().ends_with('Z'));
    assert!(!manifest["symdesk_version"].as_str().unwrap().is_empty());
    assert!(
        manifest["sources"]
            .as_array()
            .unwrap()
            .iter()
            .any(|s| s["path"].as_str().unwrap().ends_with("data/symingest"))
    );
    let files = manifest["files"].as_array().unwrap();
    let sources: Vec<&str> = files
        .iter()
        .map(|f| f["source"].as_str().unwrap())
        .collect();
    for expected in [
        "data/symdesk/symingest.db",
        "data/symdesk/vaults/0123456789abcdef/sidecar.db",
        "data/symdesk/vaults/0123456789abcdef/metadata.json",
        "config/symdesk/config.toml",
        "data/symingest/legacy.txt",
    ] {
        assert!(
            sources.iter().any(|s| s.ends_with(expected)),
            "{expected} missing from {sources:?}"
        );
    }
    assert!(
        !sources
            .iter()
            .any(|s| s.contains("/archive/") || s.contains("/backups/")),
        "{sources:?}"
    );
    let backup_dir = env.data().join("backups/pre-rust");
    for file in files {
        let copy = backup_dir.join(file["backup"].as_str().unwrap());
        assert_eq!(
            sha256(&copy),
            file["sha256"].as_str().unwrap(),
            "{}",
            file["source"]
        );
        assert_eq!(
            fs::metadata(&copy).unwrap().len(),
            file["size"].as_u64().unwrap()
        );
        // Quiescent files are copied byte for byte: the digest is the original's.
        assert_eq!(file["method"], "copy");
        assert_eq!(
            sha256(Path::new(file["source"].as_str().unwrap())),
            file["sha256"].as_str().unwrap()
        );
    }
    // State is read, never modified, by the backup.
    let after: std::collections::BTreeMap<_, _> = tree(&env.root)
        .into_iter()
        .filter(|(path, _)| !path.starts_with("data/symdesk/backups/") || path.ends_with("old.txt"))
        .collect();
    assert_eq!(after, before);

    let manifest_bytes = fs::read(backup_dir.join("manifest.json")).unwrap();
    let second = env.run("on", &["config", "paths"]);
    assert_eq!(second.status.code(), Some(0));
    assert!(
        second.stderr.is_empty(),
        "second run must be silent: {}",
        text(&second.stderr)
    );
    assert_eq!(
        fs::read(backup_dir.join("manifest.json")).unwrap(),
        manifest_bytes
    );
}

#[test]
fn no_state_creates_no_backup_and_later_rust_state_is_not_backed_up_as_pre_rust() {
    let env = Env::new();
    let run = env.run("on", &["config", "paths"]);
    assert_eq!(run.status.code(), Some(0), "{}", text(&run.stderr));
    assert!(run.stderr.is_empty());
    assert!(!env.data().join("backups/pre-rust").exists());
    let marker: serde_json::Value =
        serde_json::from_slice(&fs::read(env.data().join("backups/pre-rust.done.json")).unwrap())
            .unwrap();
    assert_eq!(marker["result"], "no-state");
    // State the Rust build creates afterwards is not "pre-Rust" state.
    env.seed_state();
    let later = env.run("on", &["config", "paths"]);
    assert!(later.stderr.is_empty(), "{}", text(&later.stderr));
    assert!(!env.data().join("backups/pre-rust").exists());
}

#[test]
fn opt_out_values_disable_the_backup() {
    for value in ["off", "0", "false", "no", "OFF"] {
        let env = Env::new();
        env.seed_state();
        let run = env.run(value, &["config", "paths"]);
        assert_eq!(run.status.code(), Some(0));
        assert!(run.stderr.is_empty());
        assert!(!env.data().join("backups/pre-rust").exists(), "{value}");
        assert!(
            !env.data().join("backups/pre-rust.done.json").exists(),
            "{value}"
        );
    }
}

#[test]
fn live_wal_database_is_snapshotted_consistently_and_its_sidecars_are_folded() {
    let env = Env::new();
    env.seed_state();
    let database = env.data().join("live.db");
    let connection = Connection::open(&database).unwrap();
    connection
        .execute_batch(
            "PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
             CREATE TABLE t(id INTEGER PRIMARY KEY, body TEXT);
             INSERT INTO t(body) VALUES ('committed-in-wal-1'), ('committed-in-wal-2');",
        )
        .unwrap();
    assert!(
        fs::metadata(format!("{}-wal", database.display()))
            .unwrap()
            .len()
            > 0,
        "WAL must be live"
    );

    let run = env.run("on", &["config", "paths"]);
    assert_eq!(run.status.code(), Some(0), "{}", text(&run.stderr));
    let manifest = manifest(&env);
    let entry = manifest["files"]
        .as_array()
        .unwrap()
        .iter()
        .find(|f| {
            f["source"]
                .as_str()
                .unwrap()
                .ends_with("data/symdesk/live.db")
        })
        .unwrap();
    assert_eq!(entry["method"], "sqlite-backup");
    let snapshot = env
        .data()
        .join("backups/pre-rust")
        .join(entry["backup"].as_str().unwrap());
    let restored = Connection::open(&snapshot).unwrap();
    let rows: i64 = restored
        .query_row("SELECT count(*) FROM t", [], |row| row.get(0))
        .unwrap();
    assert_eq!(rows, 2, "WAL content must be inside the snapshot");
    let integrity: String = restored
        .query_row("PRAGMA integrity_check", [], |row| row.get(0))
        .unwrap();
    assert_eq!(integrity, "ok");
    let files = manifest["files"].as_array().unwrap();
    assert!(
        !files
            .iter()
            .any(|f| f["source"].as_str().unwrap().ends_with("live.db-wal")),
        "WAL must be folded, not copied"
    );
    assert!(
        manifest["skipped"]
            .as_array()
            .unwrap()
            .iter()
            .any(|s| s["source"].as_str().unwrap().ends_with("live.db-wal"))
    );
    drop(connection);
}

#[test]
fn failed_backup_aborts_the_command_and_leaves_state_untouched() {
    let env = Env::new();
    env.seed_state();
    // A regular file where the backups directory must be created.
    fs::remove_dir_all(env.data().join("backups")).unwrap();
    fs::write(env.data().join("backups"), b"in the way").unwrap();
    let before = tree(&env.root);
    let run = env.run("on", &["config", "paths"]);
    assert_ne!(run.status.code(), Some(0));
    assert!(run.stdout.is_empty(), "no stdout on a refused command");
    let message = text(&run.stderr);
    assert!(
        message.contains("cannot create the one-time pre-Rust state backup"),
        "{message}"
    );
    assert!(message.contains("SYMDESK_PRE_RUST_BACKUP=off"), "{message}");
    assert_eq!(tree(&env.root), before, "state must be untouched");
    // The documented escape hatch still lets the user continue.
    let escaped = env.run("off", &["config", "paths"]);
    assert_eq!(escaped.status.code(), Some(0));
}

#[test]
fn mcp_stdout_carries_only_json_rpc_frames_while_the_backup_runs() {
    let env = Env::new();
    env.seed_state();
    let initialize = br#"{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}
"#;
    let vault = env.root.join("vault");
    let output = env.run_with(
        "on",
        &["--vault", vault.to_str().unwrap(), "mcp"],
        Some(initialize),
    );
    assert!(
        env.data().join("backups/pre-rust/manifest.json").is_file(),
        "backup missing: {}",
        text(&output.stderr)
    );
    assert!(text(&output.stderr).contains("backed up once"));
    let stdout = text(&output.stdout);
    assert!(
        !stdout.trim().is_empty(),
        "initialize must be answered: {}",
        text(&output.stderr)
    );
    for line in stdout.lines() {
        let frame: serde_json::Value = serde_json::from_str(line)
            .unwrap_or_else(|e| panic!("non-JSON on stdout {line:?}: {e}"));
        assert_eq!(frame["jsonrpc"], "2.0", "{line}");
    }
}

#[test]
fn restore_verifies_moves_current_state_aside_and_returns_the_original_files() {
    let env = Env::new();
    env.seed_state();
    let original = tree(&env.root);
    let run = env.run("on", &["config", "paths"]);
    assert_eq!(run.status.code(), Some(0));

    // Rust-era changes: edit, delete, add, and a new store.
    fs::write(
        env.data().join("vaults/0123456789abcdef/metadata.json"),
        b"{\"changed\":true}",
    )
    .unwrap();
    fs::remove_file(env.root.join("data/symingest/legacy.txt")).unwrap();
    fs::write(env.data().join("rust-created.db"), b"new").unwrap();
    Connection::open(env.data().join("symingest.db"))
        .unwrap()
        .execute("INSERT INTO t(body) VALUES ('rust')", [])
        .unwrap();
    assert_ne!(tree(&env.root), original);

    // A dry run verifies and reports but changes nothing.
    let changed = tree(&env.root);
    let dry = env.run("on", &["backup", "restore-pre-rust", "--dry-run"]);
    assert_eq!(dry.status.code(), Some(0), "{}", text(&dry.stderr));
    assert!(text(&dry.stdout).contains("Dry run: nothing was changed"));
    assert_eq!(tree(&env.root), changed);

    let restored = env.run("on", &["backup", "restore-pre-rust"]);
    assert_eq!(
        restored.status.code(),
        Some(0),
        "{}",
        text(&restored.stderr)
    );
    assert!(text(&restored.stdout).contains("all digests verified"));
    let without_backups: std::collections::BTreeMap<_, _> = tree(&env.root)
        .into_iter()
        .filter(|(path, _)| !path.starts_with("data/symdesk/backups/"))
        .collect();
    let expected: std::collections::BTreeMap<_, _> = original
        .into_iter()
        .filter(|(path, _)| {
            !path.starts_with("data/symdesk/backups/") && !path.starts_with("data/symdesk/archive/")
        })
        .collect();
    let restored_tree: std::collections::BTreeMap<_, _> = without_backups
        .into_iter()
        .filter(|(path, _)| !path.starts_with("data/symdesk/archive/"))
        .collect();
    assert_eq!(
        restored_tree, expected,
        "restore must give back the original bytes and drop Rust-era files"
    );
    // Nothing is deleted: the Rust-era state sits in pre-restore-*.
    let aside: Vec<_> = fs::read_dir(env.data().join("backups"))
        .unwrap()
        .flatten()
        .filter(|entry| {
            entry
                .file_name()
                .to_string_lossy()
                .starts_with("pre-restore-")
        })
        .collect();
    assert_eq!(aside.len(), 1);
    assert!(
        tree(&aside[0].path())
            .keys()
            .any(|path| path.ends_with("rust-created.db"))
    );
}

#[test]
fn restore_refuses_a_tampered_backup_and_changes_nothing() {
    let env = Env::new();
    env.seed_state();
    assert_eq!(env.run("on", &["config", "paths"]).status.code(), Some(0));
    let manifest = manifest(&env);
    let victim = manifest["files"][0]["backup"].as_str().unwrap().to_owned();
    let victim_path = env.data().join("backups/pre-rust").join(&victim);
    let mut bytes = fs::read(&victim_path).unwrap();
    bytes.push(0);
    fs::write(&victim_path, bytes).unwrap();
    fs::write(env.data().join("rust-created.db"), b"new").unwrap();
    let before = tree(&env.root);
    for args in [
        &["backup", "restore-pre-rust"][..],
        &["backup", "restore-pre-rust", "--dry-run"][..],
    ] {
        let run = env.run("on", args);
        assert_eq!(run.status.code(), Some(1));
        assert!(
            text(&run.stderr).contains("failed verification"),
            "{}",
            text(&run.stderr)
        );
        assert_eq!(tree(&env.root), before);
    }
    let missing = env.run("on", &["backup", "restore-pre-rust", "--bogus"]);
    assert_eq!(missing.status.code(), Some(2));
}

#[test]
fn restore_without_a_backup_explains_itself() {
    let env = Env::new();
    let run = env.run("off", &["backup", "restore-pre-rust"]);
    assert_eq!(run.status.code(), Some(1));
    assert!(
        text(&run.stderr).contains("no pre-Rust backup found"),
        "{}",
        text(&run.stderr)
    );
}

#[test]
fn restore_command_is_hidden_from_help_and_other_backup_words_stay_unknown() {
    let env = Env::new();
    let help = env.run("on", &["--help"]);
    assert_eq!(help.status.code(), Some(0));
    assert!(!text(&help.stdout).contains("restore-pre-rust"));
    assert!(!text(&help.stdout).contains(" backup "));
    let unknown = env.run("on", &["backup"]);
    assert_eq!(unknown.status.code(), Some(1));
    assert!(
        text(&unknown.stderr).contains("unknown command \"backup\""),
        "{}",
        text(&unknown.stderr)
    );
}
