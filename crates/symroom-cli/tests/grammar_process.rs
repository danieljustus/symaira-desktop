#![deny(unsafe_code)]

use std::{
    fs,
    path::{Path, PathBuf},
    process::Command,
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

static TEMP_DIR_COUNTER: AtomicU64 = AtomicU64::new(0);

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    args: Vec<String>,
    exit_code: i32,
    stdout: String,
    stderr: String,
}

// Replays Go-owned usage, help and unknown-flag process results for every
// command and nested action in the parser grammar, in disposable HOME/XDG/TMP
// roots with a cleared environment.
#[test]
fn grammar_process_matches_go_usage_help_and_flag_contract() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/cli/symroom-grammar-process.json");
    let fixture: Fixture = serde_json::from_slice(&fs::read(&path).expect("read fixture"))
        .expect("parse grammar process fixture");
    assert_eq!(fixture.schema_version, 1);
    assert!(!fixture.oracle_revision.is_empty());
    assert_eq!(fixture.cases.len(), 122, "fixture case count changed");

    let mut mismatches = Vec::new();
    for case in &fixture.cases {
        let temp = TempDir::new();
        let work = temp.path.join("work");
        for dir in ["work", "home", "data", "config", "tmp"] {
            create_private_dir(&temp.path.join(dir));
        }
        // Windows resolves the home directory from USERPROFILE, so both home
        // variables point at the disposable root on every platform.
        let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
        #[cfg(windows)]
        command.env(
            "SYSTEMROOT",
            std::env::var_os("SYSTEMROOT").unwrap_or_default(),
        );
        let output = command
            .args(&case.args)
            .current_dir(&work)
            .env_clear()
            .env("HOME", temp.path.join("home"))
            .env("USERPROFILE", temp.path.join("home"))
            .env("XDG_DATA_HOME", temp.path.join("data"))
            .env("XDG_CONFIG_HOME", temp.path.join("config"))
            .env("TMPDIR", temp.path.join("tmp"))
            .env("PATH", temp.path.join("tmp"))
            .env("TZ", "UTC")
            .env("LC_ALL", "C")
            .env("LANG", "C")
            .output()
            .expect("run Rust symroom");
        let stdout = String::from_utf8(output.stdout).expect("stdout is UTF-8");
        let stderr = String::from_utf8(output.stderr).expect("stderr is UTF-8");
        let code = output.status.code();
        if code != Some(case.exit_code) || stdout != case.stdout || stderr != case.stderr {
            mismatches.push(format!(
                "{}: exit {:?} (want {}), stdout {:?} (want {:?}), stderr {:?} (want {:?})",
                case.name, code, case.exit_code, stdout, case.stdout, stderr, case.stderr
            ));
        }
    }
    assert!(
        mismatches.is_empty(),
        "{} of {} grammar process cases differ from Go:\n{}",
        mismatches.len(),
        fixture.cases.len(),
        mismatches.join("\n")
    );
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock after epoch")
            .as_nanos();
        let nonce = format!(
            "{nonce}-{}",
            TEMP_DIR_COUNTER.fetch_add(1, Ordering::Relaxed)
        );
        let path = std::env::temp_dir().join(format!(
            "symroom-grammar-process-{}-{nonce}",
            std::process::id()
        ));
        create_private_dir(&path);
        Self { path }
    }
}

// The SQLite state check rejects group- or world-accessible ancestors, so every
// disposable root is owner-only, as in the Go oracle run.
fn create_private_dir(path: &Path) {
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder
        .create(path)
        .expect("create temporary test directory");
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
