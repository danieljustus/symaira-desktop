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

#[test]
fn query_cli_projects_identity_keys_and_caps_the_default_ordered_page() {
    let root = TempRoot::new("query-page");
    let seeded = run(
        &root,
        [
            "dataset",
            "sync",
            "orders",
            "--rows",
            r#"[{"identity":"b","values":{"id":"b","amount":2}},{"identity":"a","values":{"id":"a","amount":1}}]"#,
            "--provenance",
            r#"{"source_name":"fixture","source_sha256":"sha","imported_at":"2026-04-03T10:00:00Z"}"#,
            "--identity-field",
            "id",
            "--json",
        ],
    );
    assert_eq!(seeded.status.code(), Some(0), "stderr: {:?}", seeded.stderr);

    let queried = run(
        &root,
        [
            "dataset",
            "query",
            "orders",
            "--columns",
            "_key,identity,id",
            "--limit",
            "1",
            "--json",
        ],
    );
    assert_eq!(
        queried.status.code(),
        Some(0),
        "stderr: {:?}",
        queried.stderr
    );
    assert_eq!(queried.stdout, b"{\"dataset\":\"orders\",\"columns\":[\"_key\",\"identity\",\"id\"],\"rows\":[{\"_key\":\"identity:a\",\"id\":\"a\",\"identity\":\"a\"}],\"total_rows\":2,\"returned_rows\":1,\"limit\":1,\"capped\":true}\n");
    let default_page = run(&root, ["dataset", "query", "orders", "--json"]);
    assert_eq!(
        default_page.status.code(),
        Some(0),
        "stderr: {:?}",
        default_page.stderr
    );
    assert_eq!(default_page.stdout, b"{\"dataset\":\"orders\",\"columns\":[\"amount\",\"id\"],\"rows\":[{\"amount\":1,\"id\":\"a\"},{\"amount\":2,\"id\":\"b\"}],\"total_rows\":2,\"returned_rows\":2,\"limit\":10,\"capped\":false}\n");
}

#[test]
fn query_cli_rejects_unknown_columns_and_missing_datasets() {
    let root = TempRoot::new("query-invalid");
    let seeded = run(
        &root,
        [
            "dataset",
            "sync",
            "orders",
            "--rows",
            r#"[{"identity":"a","values":{"id":"a"}}]"#,
            "--provenance",
            r#"{"source_name":"fixture","source_sha256":"sha","imported_at":"2026-04-03T10:00:00Z"}"#,
            "--identity-field",
            "id",
            "--json",
        ],
    );
    assert_eq!(seeded.status.code(), Some(0), "stderr: {:?}", seeded.stderr);
    let unknown = run(
        &root,
        [
            "dataset",
            "query",
            "orders",
            "--columns",
            "absent",
            "--json",
        ],
    );
    assert_ne!(unknown.status.code(), Some(0));
    let unknown_error: serde_json::Value =
        serde_json::from_slice(&unknown.stdout).expect("JSON error");
    assert_eq!(
        unknown_error["error"],
        "dataset column \"absent\" not found"
    );
    let missing = run(&root, ["dataset", "query", "missing", "--json"]);
    assert_ne!(missing.status.code(), Some(0));
    let missing_error: serde_json::Value =
        serde_json::from_slice(&missing.stdout).expect("JSON error");
    assert_eq!(missing_error["error"], "dataset \"missing\" not found");
}

#[test]
fn query_cli_applies_flat_scalar_filters_with_go_null_semantics() {
    let root = TempRoot::new("query-filters");
    let seeded = run(
        &root,
        [
            "dataset",
            "sync",
            "orders",
            "--rows",
            r#"[{"identity":"a","values":{"id":"a","amount":10,"status":"open"}},{"identity":"b","values":{"id":"b","amount":20,"status":null}},{"identity":"c","values":{"id":"c","amount":30}},{"identity":"d","values":{"id":"d","amount":40,"status":""}},{"identity":"e","values":{"id":"e","amount":50,"status":"paid"}}]"#,
            "--provenance",
            r#"{"source_name":"fixture","source_sha256":"sha","imported_at":"2026-04-03T10:00:00Z"}"#,
            "--identity-field",
            "id",
            "--schema",
            r#"{"amount":{"type":"number"},"status":{"type":"text"}}"#,
            "--json",
        ],
    );
    assert_eq!(seeded.status.code(), Some(0), "stderr: {:?}", seeded.stderr);

    let cases = [
        (
            r#"[{"key":"status","operator":"equals","value":"OPEN"}]"#,
            vec!["a"],
            1,
        ),
        (
            r#"[{"key":"amount","operator":"equals","value":"10.0"}]"#,
            vec!["a"],
            1,
        ),
        (
            r#"[{"key":"status","operator":"not_equals","value":"open"}]"#,
            vec!["b", "c", "d", "e"],
            4,
        ),
        (
            r#"[{"key":"status","operator":"is_empty","value":""}]"#,
            vec!["b", "c", "d"],
            3,
        ),
    ];
    for (filters, expected_ids, expected_total) in cases {
        let queried = run(
            &root,
            ["dataset", "query", "orders", "--filters", filters, "--json"],
        );
        assert_eq!(
            queried.status.code(),
            Some(0),
            "stderr: {:?}",
            queried.stderr
        );
        let output: serde_json::Value =
            serde_json::from_slice(&queried.stdout).expect("query JSON");
        assert_eq!(output["total_rows"], expected_total);
        let ids = output["rows"]
            .as_array()
            .expect("rows")
            .iter()
            .map(|row| row["id"].as_str().expect("id"))
            .collect::<Vec<_>>();
        assert_eq!(ids, expected_ids);
    }

    let combined = run(
        &root,
        [
            "dataset",
            "query",
            "orders",
            "--filters",
            r#"[{"key":"status","operator":"is_empty"},{"key":"amount","operator":"not_equals","value":"20"}]"#,
            "--json",
        ],
    );
    assert_eq!(
        combined.status.code(),
        Some(0),
        "stderr: {:?}",
        combined.stderr
    );
    let output: serde_json::Value = serde_json::from_slice(&combined.stdout).expect("query JSON");
    assert_eq!(output["total_rows"], 2);
    let ids = output["rows"]
        .as_array()
        .expect("rows")
        .iter()
        .map(|row| row["id"].as_str().expect("id"))
        .collect::<Vec<_>>();
    assert_eq!(ids, vec!["c", "d"]);

    let unknown = run(
        &root,
        [
            "dataset",
            "query",
            "orders",
            "--filters",
            r#"[{"key":"absent","operator":"equals","value":"x"}]"#,
            "--json",
        ],
    );
    assert_ne!(unknown.status.code(), Some(0));
    let error: serde_json::Value = serde_json::from_slice(&unknown.stdout).expect("JSON error");
    assert_eq!(error["error"], "dataset column \"absent\" not found");
}

#[test]
fn query_cli_caps_large_pages_at_one_thousand_rows() {
    let root = TempRoot::new("query-cap");
    let rows = (0..1001)
        .map(|index| {
            format!(r#"{{"identity":"row-{index:04}","values":{{"id":"row-{index:04}"}}}}"#)
        })
        .collect::<Vec<_>>()
        .join(",");
    let row_input = format!("[{rows}]");
    let rows_path = root.0.join("rows.json");
    fs::write(&rows_path, row_input).expect("write large rows fixture");
    let seeded = run(
        &root,
        [
            "dataset",
            "sync",
            "orders",
            "--rows",
            rows_path.to_str().expect("UTF-8 rows path"),
            "--provenance",
            r#"{"source_name":"fixture","source_sha256":"sha","imported_at":"2026-04-03T10:00:00Z"}"#,
            "--identity-field",
            "id",
            "--json",
        ],
    );
    assert_eq!(seeded.status.code(), Some(0), "stderr: {:?}", seeded.stderr);
    let queried = run(
        &root,
        ["dataset", "query", "orders", "--limit", "5000", "--json"],
    );
    assert_eq!(
        queried.status.code(),
        Some(0),
        "stderr: {:?}",
        queried.stderr
    );
    let result: serde_json::Value = serde_json::from_slice(&queried.stdout).expect("JSON result");
    assert_eq!(result["limit"], 1000);
    assert_eq!(result["total_rows"], 1001);
    assert_eq!(result["returned_rows"], 1000);
    assert_eq!(result["capped"], true);
    assert_eq!(result["rows"].as_array().map(Vec::len), Some(1000));
}
