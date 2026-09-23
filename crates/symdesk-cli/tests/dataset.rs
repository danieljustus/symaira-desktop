#![deny(unsafe_code)]

use std::{
    ffi::OsStr,
    fs,
    path::PathBuf,
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use symdesk_index::Sidecar;

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

#[test]
fn import_cli_persists_exact_source_and_authoritative_handle() {
    let root = TempRoot::new("import");
    let source = root.0.join("supplier.csv");
    let source_bytes = b"id,amount\nrow-1,12.5\nrow-2,7\n";
    fs::write(&source, source_bytes).expect("write source CSV");
    let output = run(
        &root,
        [
            "dataset",
            "import",
            source.to_str().expect("UTF-8 source path"),
            "--title",
            "Supplier Ledger",
            "--identity-field",
            "id",
            "--imported-at",
            "2026-04-04T12:30:00Z",
            "--json",
        ],
    );
    assert_eq!(output.status.code(), Some(0), "stderr: {:?}", output.stderr);
    assert_eq!(
        output.stdout,
        b"{\"handle_path\":\"datasets/supplier-ledger.md\",\"raw_path\":\"datasets/supplier-ledger/2026-04-04.csv\",\"slug\":\"supplier-ledger\",\"rows\":2,\"columns\":{\"amount\":{\"type\":\"number\",\"label\":\"amount\"},\"id\":{\"type\":\"text\",\"label\":\"id\"}},\"source_sha256\":\"324af029f20bb0265e2e729f60e57e291df9c56e367907a5a25c89dc2e37bed4\",\"sensitivity\":\"restricted\",\"retention_rule\":\"default\"}\n"
    );
    let raw_path = root.vault().join("datasets/supplier-ledger/2026-04-04.csv");
    assert_eq!(fs::read(raw_path).expect("read raw source"), source_bytes);
    let handle_path = root.vault().join("datasets/supplier-ledger.md");
    let handle = fs::read_to_string(handle_path).expect("read Markdown manifest");
    assert!(handle.contains("source: datasets/supplier-ledger/2026-04-04.csv"));
    assert!(handle.contains("source_name: supplier.csv"));
    assert!(handle.contains(
        "source_sha256: 324af029f20bb0265e2e729f60e57e291df9c56e367907a5a25c89dc2e37bed4"
    ));

    let digest = symdesk_vault::sha256_hex(
        root.vault()
            .canonicalize()
            .expect("canonical vault")
            .to_str()
            .expect("UTF-8 vault")
            .as_bytes(),
    );
    let sidecar_path = root
        .0
        .join("home/.local/share/symdesk/vaults")
        .join(&digest[..16])
        .join("sidecar.db");
    let sidecar = Sidecar::open(&sidecar_path).expect("open persisted sidecar");
    let rows = sidecar
        .dataset_rows("supplier-ledger")
        .expect("read projected rows");
    assert_eq!(rows.len(), 2);
    assert_eq!(rows[0].identity, "row-1");
    assert_eq!(rows[1].identity, "row-2");

    let text = run(
        &root,
        [
            "dataset",
            "import",
            source.to_str().expect("UTF-8 source path"),
            "--slug",
            "text-ledger",
            "--identity-field",
            "id",
            "--imported-at",
            "2026-04-04T12:30:00Z",
            "--output",
            "text",
        ],
    );
    assert_eq!(text.status.code(), Some(0), "stderr: {:?}", text.stderr);
    assert_eq!(
        text.stdout,
        b"&{HandlePath:datasets/text-ledger.md RawPath:datasets/text-ledger/2026-04-04.csv Slug:text-ledger Rows:2 Columns:map[amount:{Type:number Label:amount Options:[] Description: Default:} id:{Type:text Label:id Options:[] Description: Default:}] SourceSHA256:324af029f20bb0265e2e729f60e57e291df9c56e367907a5a25c89dc2e37bed4 Sensitivity:restricted RetentionRule:default}\n"
    );
}
