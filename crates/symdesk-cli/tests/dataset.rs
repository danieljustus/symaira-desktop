#![deny(unsafe_code)]

use std::{
    ffi::OsStr,
    fs,
    path::PathBuf,
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

struct TempRoot(PathBuf);

impl TempRoot {
    fn new(label: &str) -> Self {
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-dataset-cli-{label}-{}-{nanos}",
            std::process::id()
        ));
        fs::create_dir_all(root.join("home")).expect("create home");
        fs::create_dir_all(root.join("vault")).expect("create vault");
        Self(root)
    }

    fn vault(&self) -> PathBuf {
        self.0.join("vault")
    }
}

impl Drop for TempRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn run(root: &TempRoot, args: impl IntoIterator<Item = impl AsRef<OsStr>>) -> Output {
    let home = root.0.join("home");
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command
        .env_clear()
        .env("HOME", &home)
        .env("USERPROFILE", &home)
        .env("XDG_CONFIG_HOME", home.join(".config"))
        .env("XDG_DATA_HOME", home.join(".local/share"))
        .env("XDG_CACHE_HOME", home.join(".cache"))
        .env("XDG_STATE_HOME", home.join(".local/state"))
        .env("TMPDIR", root.0.join("tmp"))
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC")
        .env("TERM", "dumb")
        .env("NO_COLOR", "1")
        .args(args)
        .args(["--vault", root.vault().to_str().expect("UTF-8 vault path")]);
    command.output().expect("run symdesk dataset command")
}

#[test]
fn sync_cli_preserves_go_float64_rounding_and_reuses_provenance() {
    let root = TempRoot::new("sync-number");
    let first = run(
        &root,
        [
            "dataset",
            "sync",
            "rounded",
            "--rows",
            r#"[{"identity":"one","values":{"id":"one","amount":9007199254740993}}]"#,
            "--provenance",
            r#"{"source_name":"feed","source_sha256":"sha-1","imported_at":"2026-04-03T10:00:00Z"}"#,
            "--identity-field",
            "id",
            "--json",
        ],
    );
    assert_eq!(first.status.code(), Some(0), "stderr: {:?}", first.stderr);
    assert_eq!(
        first.stdout,
        b"{\"slug\":\"rounded\",\"rows\":1,\"imported_rows\":1,\"raw_path\":\"datasets/rounded/2026-04-03.csv\",\"handle_path\":\"datasets/rounded.md\",\"idempotent\":false}\n"
    );
    assert_eq!(
        fs::read(root.vault().join("datasets/rounded/2026-04-03.csv")).expect("read CSV"),
        b"amount,id\n9007199254740992,one\n"
    );

    let second = run(
        &root,
        [
            "dataset",
            "sync",
            "rounded",
            "--rows",
            r#"[{"identity":"one","values":{"id":"one","amount":9}}]"#,
            "--source-name",
            "feed",
            "--source-sha256",
            "sha-1",
            "--imported-at",
            "2026-04-03T10:00:00Z",
            "--identity-field",
            "id",
            "--json",
        ],
    );
    assert_eq!(second.status.code(), Some(0), "stderr: {:?}", second.stderr);
    assert_eq!(
        second.stdout,
        b"{\"slug\":\"rounded\",\"rows\":1,\"imported_rows\":1,\"raw_path\":\"datasets/rounded/2026-04-03.csv\",\"handle_path\":\"datasets/rounded.md\",\"idempotent\":true}\n"
    );
    assert_eq!(
        fs::read(root.vault().join("datasets/rounded/2026-04-03.csv")).expect("CSV remains"),
        b"amount,id\n9007199254740992,one\n"
    );
}
