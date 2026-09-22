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
            "symdesk-retention-cli-{label}-{}-{nanos}",
            std::process::id()
        ));
        fs::create_dir_all(root.join("home")).expect("create home");
        fs::create_dir_all(root.join("vault")).expect("create vault");
        Self(root)
    }

    fn vault(&self) -> PathBuf {
        self.0.join("vault")
    }

    fn proposal_dir(&self) -> PathBuf {
        self.vault().join(".symdesk").join("retention")
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
    command.output().expect("run symdesk retention command")
}

fn write_proposal(root: &TempRoot) -> PathBuf {
    let dir = root.proposal_dir();
    fs::create_dir_all(&dir).expect("create proposal directory");
    let path = dir.join("ret-safe.json");
    fs::write(
        &path,
        r#"{"run_id":"ret-safe","rule_name":"rule","created":"2026-01-01T00:00:00Z","items":[{"path":"doc.md","title":"Doc","reference_date":"2026-01-01","expires_at":"2026-01-02","action":"trash","rule_name":"rule"}],"status":"pending"}"#,
    )
    .expect("write proposal");
    path
}

fn assert_ok(output: &Output, stdout: &[u8]) {
    assert_eq!(output.status.code(), Some(0));
    assert_eq!(output.stdout, stdout);
    assert!(output.stderr.is_empty(), "stderr: {:?}", output.stderr);
}

fn assert_error(output: &Output, stdout: &[u8], stderr: &[u8]) {
    assert_eq!(output.status.code(), Some(1));
    assert_eq!(output.stdout, stdout);
    assert_eq!(output.stderr, stderr);
}

#[test]
fn reject_persists_and_diff_reads_back_the_proposal() {
    let root = TempRoot::new("reject");
    let proposal_path = write_proposal(&root);

    let output = run(&root, ["retention", "reject", "ret-safe", "--json"]);
    assert_ok(
        &output,
        b"{\"run_id\":\"ret-safe\",\"status\":\"rejected\"}\n",
    );

    let persisted: serde_json::Value = serde_json::from_slice(
        &fs::read(&proposal_path).expect("read persisted rejected proposal"),
    )
    .expect("decode persisted rejected proposal");
    assert_eq!(persisted["status"], "rejected");
    assert_eq!(persisted["items"][0]["path"], "doc.md");

    let output = run(&root, ["retention", "diff", "ret-safe", "--json"]);
    assert_ok(
        &output,
        b"[{\"path\":\"doc.md\",\"title\":\"Doc\",\"reference_date\":\"2026-01-01\",\"expires_at\":\"2026-01-02\",\"action\":\"trash\",\"rule_name\":\"rule\"}]\n",
    );
}

#[test]
fn diff_preserves_go_json_escaping_and_human_struct_layout() {
    let root = TempRoot::new("diff");
    fs::create_dir_all(root.proposal_dir()).expect("create proposal directory");
    let proposal = format!(
        "{{\"run_id\":\"ret-safe\",\"rule_name\":\"rule\",\"created\":\"2026-01-01T00:00:00Z\",\"items\":[{{\"path\":\"doc<&.md\",\"title\":\"Title > & {}\",\"reference_date\":\"2026-01-01\",\"expires_at\":\"2026-01-02\",\"action\":\"trash\",\"rule_name\":\"rule<{}\"}}],\"status\":\"pending\"}}",
        '\u{2029}', '\u{2028}'
    );
    fs::write(root.proposal_dir().join("ret-safe.json"), proposal).expect("write proposal");

    let output = run(&root, ["retention", "diff", "ret-safe", "--json"]);
    assert_ok(
        &output,
        b"[{\"path\":\"doc\\u003c\\u0026.md\",\"title\":\"Title \\u003e \\u0026 \\u2029\",\"reference_date\":\"2026-01-01\",\"expires_at\":\"2026-01-02\",\"action\":\"trash\",\"rule_name\":\"rule\\u003c\\u2028\"}]\n",
    );

    let output = run(&root, ["retention", "diff", "ret-safe"]);
    assert_ok(
        &output,
        format!(
            "[{{Path:doc<&.md Title:Title > & {} ReferenceDate:2026-01-01 ExpiresAt:2026-01-02 Action:trash RuleName:rule<{} Fingerprint: Status: Failure:}}]\n",
            '\u{2029}', '\u{2028}'
        )
        .as_bytes(),
    );
}

#[test]
fn reject_and_diff_match_go_argument_and_run_id_errors() {
    let root = TempRoot::new("errors");

    let output = run(&root, ["retention", "reject"]);
    assert_error(&output, b"", b"accepts 1 arg(s), received 0\n");

    let output = run(&root, ["retention", "diff", "one", "two"]);
    assert_error(&output, b"", b"accepts 1 arg(s), received 2\n");

    let output = run(&root, ["retention", "reject", "../escape", "--json"]);
    assert_error(
        &output,
        b"{\"error\":\"retention proposal run ID \\\"../escape\\\" is not a single safe filename component\"}\n",
        b"",
    );

    let output = run(&root, ["retention", "diff", "CON"]);
    assert_error(
        &output,
        b"",
        b"retention proposal run ID \"CON\" is a reserved platform name\n",
    );

    let output = run(&root, ["retention", "diff", "safe-missing"]);
    let path = root.proposal_dir().join("safe-missing.json");
    let expected = format!("open {}: {}\n", path.display(), missing_file_message());
    assert_error(&output, b"", expected.as_bytes());
}

#[test]
fn history_distinguishes_missing_empty_and_nonempty_logs() {
    let root = TempRoot::new("history");

    let output = run(&root, ["retention", "history", "--json"]);
    assert_ok(&output, b"null\n");

    fs::create_dir_all(root.proposal_dir()).expect("create proposal directory");
    fs::write(root.proposal_dir().join("history.json"), b"[]").expect("write empty history");
    let output = run(&root, ["retention", "history", "--json"]);
    assert_ok(&output, b"[]\n");

    let history = format!(
        "[{{\"action_id\":\"ret-safe:0\",\"timestamp\":\"2026-01-02T03:04:05Z\",\"rule_name\":\"rule<{}\",\"action\":\"trash\",\"path\":\"doc<&.md\",\"title\":\"Title > & {}\"}}]",
        '\u{2028}', '\u{2029}'
    );
    fs::write(root.proposal_dir().join("history.json"), history).expect("write history");

    let output = run(&root, ["retention", "history", "--json"]);
    assert_ok(
        &output,
        b"[{\"action_id\":\"ret-safe:0\",\"timestamp\":\"2026-01-02T03:04:05Z\",\"rule_name\":\"rule\\u003c\\u2028\",\"action\":\"trash\",\"path\":\"doc\\u003c\\u0026.md\",\"title\":\"Title \\u003e \\u0026 \\u2029\"}]\n",
    );

    let output = run(&root, ["retention", "history"]);
    assert_ok(
        &output,
        format!(
            "2026-01-02 03:04:05  rule<{}  doc<&.md → trash\n",
            '\u{2028}'
        )
        .as_bytes(),
    );
}

#[test]
fn history_rejects_arguments_and_null_state() {
    let root = TempRoot::new("history-errors");

    let output = run(&root, ["retention", "history", "extra", "--json"]);
    assert_error(
        &output,
        b"{\"error\":\"unknown command \\\"extra\\\" for \\\"symdesk retention history\\\"\"}\n",
        b"",
    );

    fs::create_dir_all(root.proposal_dir()).expect("create proposal directory");
    fs::write(root.proposal_dir().join("history.json"), b"null").expect("write null history");
    let output = run(&root, ["retention", "history"]);
    assert_error(
        &output,
        b"",
        b"retention history must be a non-null array\n",
    );
}

fn missing_file_message() -> &'static str {
    if cfg!(windows) {
        "The system cannot find the file specified."
    } else {
        "no such file or directory"
    }
}
