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

#[cfg(unix)]
fn stage_accept_proposal(root: &TempRoot, path: &str, action: &str, status: &str) -> String {
    let file = root.vault().join(path);
    if let Some(parent) = file.parent() {
        fs::create_dir_all(parent).expect("create document directory");
    }
    fs::write(
        &file,
        "---\ntitle: Retention Doc\ndocument_date: \"2024-01-01\"\nstatus: open\n---\nBody\n",
    )
    .expect("write retention document");
    let fingerprint = symdesk_vault::retention_state::retention_state(&root.vault(), path)
        .expect("read authoritative document state")
        .fingerprint;
    let proposal = serde_json::json!({
        "run_id": "ret-safe",
        "rule_name": "rule",
        "created": "2026-01-01T00:00:00Z",
        "items": [{
            "path": path,
            "title": "Retention Doc",
            "reference_date": "2024-01-01",
            "expires_at": "2024-01-31",
            "action": action,
            "rule_name": "rule",
            "fingerprint": fingerprint,
            "status": status,
        }],
        "status": "pending",
    });
    fs::create_dir_all(root.proposal_dir()).expect("create proposal directory");
    fs::write(
        root.proposal_dir().join("ret-safe.json"),
        serde_json::to_vec(&proposal).expect("encode proposal"),
    )
    .expect("write proposal");
    fingerprint
}

#[cfg(unix)]
fn prepare_index(root: &TempRoot) {
    let prepared = run(root, ["--json", "ls"]);
    assert_eq!(
        prepared.status.code(),
        Some(0),
        "stderr: {:?}",
        prepared.stderr
    );
    assert!(
        String::from_utf8_lossy(&prepared.stdout).contains("doc.md"),
        "stdout: {:?}",
        prepared.stdout
    );
}

#[cfg(unix)]
#[test]
fn accept_flag_review_mutates_frontmatter_and_index_and_records_history() {
    let root = TempRoot::new("accept-flag-review");
    stage_accept_proposal(&root, "doc.md", "flag_review", "");
    prepare_index(&root);

    let output = run(&root, ["retention", "accept", "ret-safe", "--json"]);
    assert_eq!(output.status.code(), Some(0), "stderr: {:?}", output.stderr);
    assert!(output.stderr.is_empty());
    let rendered: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("decode result");
    assert_eq!(rendered["status"], "accepted");
    assert_eq!(rendered["run_id"], "ret-safe");
    assert_eq!(rendered["acted"], 1);
    assert_eq!(rendered["failures"], serde_json::Value::Null);
    assert_eq!(rendered["items"][0]["status"], "accepted");
    assert!(rendered["items"][0].get("failure").is_none());

    let bytes = fs::read(root.vault().join("doc.md")).expect("read changed document");
    let document = symdesk_vault::parse_bytes("doc.md", &bytes).expect("parse changed document");
    assert_eq!(document.status, "needs_review");
    let indexed = run(&root, ["--json", "ls"]);
    assert_eq!(indexed.status.code(), Some(0));
    assert!(String::from_utf8_lossy(&indexed.stdout).contains("doc.md"));
    let history = symdesk_vault::retention::load_history(&root.vault()).expect("read history");
    assert_eq!(history.len(), 1);
    assert_eq!(history[0].action_id, "ret-safe:0");
    let journal_dir = root.vault().join(".symdesk/journal");
    let day = fs::read_dir(journal_dir)
        .unwrap()
        .next()
        .unwrap()
        .unwrap()
        .path();
    let event: serde_json::Value = serde_json::from_slice(&fs::read(day).unwrap()).unwrap();
    assert_eq!(event["event"], "status_changed");
    assert_eq!(event["path"], "doc.md");
    assert_eq!(event["title"], "Retention Doc");
    assert_eq!(event["details"], "status set to needs_review");
}

#[cfg(unix)]
#[test]
fn accept_trash_moves_document_and_removes_it_from_index() {
    let root = TempRoot::new("accept-trash");
    stage_accept_proposal(&root, "doc.md", "trash", "");
    prepare_index(&root);

    let output = run(&root, ["retention", "accept", "ret-safe", "--json"]);
    assert_eq!(output.status.code(), Some(0), "stderr: {:?}", output.stderr);
    let rendered: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("decode result");
    assert_eq!(rendered["status"], "accepted");
    assert_eq!(rendered["acted"], 1);
    assert!(!root.vault().join("doc.md").exists());
    let indexed = run(&root, ["--json", "ls"]);
    assert_eq!(indexed.status.code(), Some(0));
    assert_eq!(indexed.stdout, b"null\n");
    let history = symdesk_vault::retention::load_history(&root.vault()).expect("read history");
    assert_eq!(history.len(), 1);
    assert_eq!(history[0].action_id, "ret-safe:0");
    assert!(root.vault().join(".symdesk/trash").exists());
    let journal_dir = root.vault().join(".symdesk/journal");
    let day = fs::read_dir(journal_dir)
        .unwrap()
        .next()
        .unwrap()
        .unwrap()
        .path();
    let event: serde_json::Value = serde_json::from_slice(&fs::read(day).unwrap()).unwrap();
    assert_eq!(event["event"], "file_removed");
    assert_eq!(event["path"], "doc.md");
    assert_eq!(event["title"], "doc.md");
    assert_eq!(event["details"], "moved to trash");
}

#[cfg(unix)]
#[test]
fn accept_rejects_stale_fingerprint_without_mutating_document() {
    let root = TempRoot::new("accept-stale");
    stage_accept_proposal(&root, "doc.md", "flag_review", "");
    prepare_index(&root);
    fs::write(
        root.vault().join("doc.md"),
        "---\ntitle: Changed Doc\ndocument_date: \"2024-01-01\"\nstatus: open\n---\nBody\n",
    )
    .expect("change authoritative document");

    let output = run(&root, ["retention", "accept", "ret-safe", "--json"]);
    assert_eq!(output.status.code(), Some(1));
    assert!(output.stderr.is_empty());
    let lines = output
        .stdout
        .split(|byte| *byte == b'\n')
        .collect::<Vec<_>>();
    let rendered: serde_json::Value = serde_json::from_slice(lines[0]).expect("decode result");
    assert_eq!(
        serde_json::from_slice::<serde_json::Value>(lines[1]).expect("decode error"),
        serde_json::json!({"error":"retention acceptance failed: doc.md: proposal is stale: authoritative fingerprint changed"})
    );
    assert_eq!(rendered["status"], "failed");
    assert_eq!(rendered["acted"], 0);
    assert_eq!(
        rendered["failures"].as_array().expect("failure list").len(),
        1
    );
    assert!(rendered["items"][0].get("status").is_none());
    assert_eq!(
        rendered["items"][0]["failure"],
        "proposal is stale: authoritative fingerprint changed"
    );
    let bytes = fs::read(root.vault().join("doc.md")).expect("read unchanged status");
    let document = symdesk_vault::parse_bytes("doc.md", &bytes).expect("parse unchanged status");
    assert_eq!(document.status, "open");
}

#[cfg(unix)]
#[test]
fn accept_retries_action_completed_item_without_reapplying_and_keeps_acted_zero() {
    let root = TempRoot::new("accept-retry");
    fs::create_dir_all(root.proposal_dir()).expect("create proposal directory");
    fs::write(
        root.proposal_dir().join("ret-safe.json"),
        serde_json::to_vec(&serde_json::json!({
            "run_id": "ret-safe",
            "rule_name": "rule",
            "created": "2026-01-01T00:00:00Z",
            "items": [{
                "path": "missing.md",
                "title": "Retention Doc",
                "reference_date": "2024-01-01",
                "expires_at": "2024-01-31",
                "action": "trash",
                "rule_name": "rule",
                "status": "action_completed",
            }],
            "status": "pending",
        }))
        .expect("encode recovery proposal"),
    )
    .expect("write recovery proposal");
    symdesk_vault::retention::append_history(
        &root.vault(),
        &symdesk_vault::retention::HistoryEntry {
            action_id: "ret-safe:0".to_owned(),
            timestamp: time::OffsetDateTime::now_utc(),
            rule_name: "rule".to_owned(),
            action: "trash".to_owned(),
            path: "missing.md".to_owned(),
            title: "Retention Doc".to_owned(),
        },
    )
    .expect("seed stable history record");
    // Recovery skips both the source reread and mutation, and history append
    // recognizes the stable action ID already written before the crash.
    let output = run(&root, ["retention", "accept", "ret-safe", "--json"]);
    assert_eq!(output.status.code(), Some(0), "stderr: {:?}", output.stderr);
    let rendered: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("decode result");
    assert_eq!(rendered["status"], "accepted");
    assert_eq!(rendered["acted"], 0);
    let history = symdesk_vault::retention::load_history(&root.vault()).expect("read history");
    assert_eq!(history.len(), 1);
    assert_eq!(history[0].action_id, "ret-safe:0");
    assert_eq!(rendered["items"][0]["status"], "accepted");
}

#[cfg(unix)]
#[test]
fn accept_dataset_purge_removes_handle_and_commits_history() {
    let root = TempRoot::new("accept-dataset-purge");
    let dataset = root.vault().join("datasets/orders.md");
    fs::create_dir_all(dataset.parent().expect("dataset parent")).expect("create datasets");
    fs::write(
        &dataset,
        "---\ntype: dataset\ntitle: Orders\ncreated: \"2024-01-02T03:04:05Z\"\ndataset_id: orders\nsource: orders.csv\ncoverage:\n  from: \"2024-01-01\"\n  to: \"2024-12-31\"\nprovenance:\n  imported_at: \"2025-01-01T00:00:00Z\"\n  source_name: orders.csv\n  source_sha256: abc123\nsensitivity: restricted\nretention_rule: rule\n---\n\n# Orders\n",
    )
    .expect("write dataset handle");
    let fingerprint =
        symdesk_vault::retention_state::retention_state(&root.vault(), "datasets/orders.md")
            .expect("read authoritative dataset state")
            .fingerprint;
    fs::create_dir_all(root.proposal_dir()).expect("create proposal directory");
    fs::write(
        root.proposal_dir().join("ret-safe.json"),
        serde_json::to_vec(&serde_json::json!({
            "run_id": "ret-safe",
            "rule_name": "batch",
            "created": "2026-01-01T00:00:00Z",
            "items": [{
                "path": "datasets/orders.md",
                "title": "Orders",
                "reference_date": "2024-01-01",
                "expires_at": "2024-01-31",
                "action": "trash",
                "rule_name": "rule",
                "fingerprint": fingerprint,
            }],
            "status": "pending",
        }))
        .expect("encode dataset proposal"),
    )
    .expect("write proposal");

    let output = run(&root, ["retention", "accept", "ret-safe", "--json"]);
    assert_eq!(output.status.code(), Some(0), "stdout: {:?}", output.stdout);
    assert!(output.stderr.is_empty());
    let result: serde_json::Value = serde_json::from_slice(&output.stdout).expect("decode result");
    assert_eq!(result["status"], "accepted");
    assert_eq!(result["acted"], 1);
    assert!(!dataset.exists());
    assert!(
        !root
            .vault()
            .join(".symdesk/dataset-purge/orders.json")
            .exists()
    );
    let history = symdesk_vault::retention::load_history(&root.vault()).expect("read history");
    assert_eq!(history.len(), 1);
    assert_eq!(history[0].action_id, "ret-safe:0");
}

#[cfg(unix)]
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

#[cfg(windows)]
#[test]
fn reject_reports_directory_sync_error_after_persisting() {
    let root = TempRoot::new("reject-windows");
    let proposal_path = write_proposal(&root);
    let output = run(&root, ["retention", "reject", "ret-safe", "--json"]);
    assert_eq!(output.status.code(), Some(1));
    let error: serde_json::Value = serde_json::from_slice(&output.stdout).expect("error JSON");
    assert!(error["error"].as_str().unwrap_or("").starts_with("sync "));
    let persisted: serde_json::Value = serde_json::from_slice(
        &fs::read(proposal_path).expect("rejected proposal persisted before sync error"),
    )
    .expect("persisted proposal JSON");
    assert_eq!(persisted["status"], "rejected");
}

#[test]
fn reject_reports_missing_proposals_with_the_go_error_envelope() {
    let root = TempRoot::new("reject-missing");

    let output = run(&root, ["retention", "reject", "ret-safe", "--json"]);
    assert_eq!(output.status.code(), Some(1));
    assert!(output.stderr.is_empty());
    let error: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("decode missing proposal error");
    let proposal_path = root.proposal_dir().join("ret-safe.json");
    assert_eq!(
        error["error"],
        format!(
            "open {}: {}",
            proposal_path.display(),
            missing_file_message()
        )
    );
}

#[cfg(unix)]
#[test]
fn go_nil_items_survive_rejection_and_render_as_null() {
    for items_field in [",\"items\":null", ""] {
        let root = TempRoot::new("nil-items");
        fs::create_dir_all(root.proposal_dir()).expect("create proposal directory");
        let path = root.proposal_dir().join("ret-safe.json");
        fs::write(
            &path,
            format!(
                "{{\"run_id\":\"ret-safe\",\"rule_name\":\"batch\",\"created\":\"2026-01-01T00:00:00Z\"{items_field},\"status\":\"pending\"}}"
            ),
        )
        .expect("write Go proposal");

        let output = run(&root, ["retention", "diff", "ret-safe", "--json"]);
        assert_ok(&output, b"null\n");

        let output = run(&root, ["retention", "reject", "ret-safe", "--json"]);
        assert_ok(
            &output,
            b"{\"run_id\":\"ret-safe\",\"status\":\"rejected\"}\n",
        );
        let persisted: serde_json::Value =
            serde_json::from_slice(&fs::read(&path).expect("read rejected proposal"))
                .expect("decode rejected proposal");
        assert_eq!(persisted["items"], serde_json::Value::Null);
        assert_eq!(persisted["status"], "rejected");
    }
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
    #[cfg(not(windows))]
    let expected_time = "2026-01-02 03:04:05".to_owned();
    #[cfg(windows)]
    let expected_time = {
        let utc = time::OffsetDateTime::parse(
            "2026-01-02T03:04:05Z",
            &time::format_description::well_known::Rfc3339,
        )
        .expect("history timestamp");
        let local =
            utc.to_offset(time::UtcOffset::local_offset_at(utc).unwrap_or(time::UtcOffset::UTC));
        let format = time::format_description::parse_borrowed::<2>(
            "[year]-[month]-[day] [hour]:[minute]:[second]",
        )
        .expect("history timestamp format");
        local.format(&format).expect("local history timestamp")
    };
    assert_ok(
        &output,
        format!("{expected_time}  rule<{}  doc<&.md → trash\n", '\u{2028}').as_bytes(),
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

#[cfg(unix)]
#[test]
fn eval_uses_rules_and_authoritative_metadata_to_stage_a_proposal() {
    let root = TempRoot::new("eval");
    fs::create_dir_all(root.vault().join(".symdesk")).expect("create rules directory");
    fs::write(
        root.vault().join(".symdesk/retention-rules.yaml"),
        "name: old-open-memos\nselector:\n  document_type: memo\n  status: open\nperiod_days: 30\naction: flag_review\n",
    )
    .expect("write rules");
    fs::write(
        root.vault().join("expired.md"),
        "---\ntitle: Expired Memo\ndocument_date: \"2024-01-01\"\ndocument_type: memo\nstatus: open\ntags: [finance]\n---\nBody\n",
    )
    .expect("write expired document");
    fs::write(
        root.vault().join("fresh.md"),
        "---\ntitle: Fresh Memo\ndocument_date: \"2099-01-01\"\ndocument_type: memo\nstatus: open\n---\nBody\n",
    )
    .expect("write fresh document");
    fs::write(
        root.vault().join("paid.md"),
        "---\ntitle: Paid Memo\ndocument_date: \"2024-01-01\"\ndocument_type: memo\nstatus: paid\n---\nBody\n",
    )
    .expect("write selector-miss document");

    // Go eval reads only the existing sidecar index; `ls` initializes it for
    // this fresh fixture in both binaries.
    let prepared = run(&root, ["--json", "ls"]);
    assert_eq!(
        prepared.status.code(),
        Some(0),
        "stderr: {:?}",
        prepared.stderr
    );
    assert!(
        String::from_utf8_lossy(&prepared.stdout).contains("expired.md"),
        "ls output: {:?}",
        prepared.stdout
    );
    let state = symdesk_vault::retention_state::retention_state(&root.vault(), "expired.md")
        .expect("read authoritative expired metadata");
    assert_eq!(state.meta.status, "open", "state: {state:?}");
    assert_eq!(state.meta.document_type, "memo", "state: {state:?}");
    assert_eq!(state.meta.document_date, "2024-01-01", "state: {state:?}");

    let output = run(&root, ["retention", "eval", "--json"]);
    assert_eq!(output.status.code(), Some(0), "stderr: {:?}", output.stderr);
    assert!(output.stderr.is_empty(), "stderr: {:?}", output.stderr);
    let rendered: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("decode eval output");
    assert_eq!(rendered["status"], "pending");
    assert_eq!(rendered["item_count"], 1, "eval output: {rendered}");
    assert_eq!(rendered["items"][0]["path"], "expired.md");
    assert_eq!(rendered["items"][0]["title"], "Expired Memo");
    assert_eq!(rendered["items"][0]["reference_date"], "2024-01-01");
    assert_eq!(rendered["items"][0]["expires_at"], "2024-01-31");
    assert_eq!(rendered["items"][0]["action"], "flag_review");
    assert_eq!(rendered["items"][0]["rule_name"], "old-open-memos");
    assert_eq!(
        rendered["items"][0]["fingerprint"].as_str().unwrap().len(),
        64
    );

    let run_id = rendered["run_id"].as_str().expect("run id");
    assert!(run_id.starts_with("ret-"));
    let proposal: serde_json::Value = serde_json::from_slice(
        &fs::read(root.proposal_dir().join(format!("{run_id}.json")))
            .expect("read staged proposal"),
    )
    .expect("decode staged proposal");
    assert_eq!(proposal["run_id"], run_id);
    assert_eq!(proposal["rule_name"], "batch");
    assert_eq!(proposal["status"], "pending");
    assert_eq!(proposal["items"], rendered["items"]);
}

#[test]
fn eval_fails_closed_without_staging_when_authoritative_state_is_invalid() {
    let root = TempRoot::new("eval-fail-closed");
    fs::create_dir_all(root.vault().join(".symdesk")).expect("create rules directory");
    fs::write(
        root.vault().join(".symdesk/retention-rules.yaml"),
        "name: old-documents\nperiod_days: 30\naction: trash\n",
    )
    .expect("write rules");
    let document = root.vault().join("broken.md");
    fs::write(
        &document,
        "---\ntitle: Valid before indexing\ndocument_date: \"2024-01-01\"\n---\nBody\n",
    )
    .expect("write indexable document");
    let prepared = run(&root, ["ls"]);
    assert_eq!(
        prepared.status.code(),
        Some(0),
        "stderr: {:?}",
        prepared.stderr
    );
    fs::write(&document, "---\ntitle: [invalid\n---\nBody\n")
        .expect("break authoritative document");

    let output = run(&root, ["retention", "eval", "--json"]);
    assert_eq!(output.status.code(), Some(1));
    let error: serde_json::Value =
        serde_json::from_slice(&output.stdout).expect("decode fail-closed error");
    assert!(
        error["error"]
            .as_str()
            .expect("error message")
            .starts_with("retention evaluation failed closed: broken.md:")
    );
    assert!(
        !root.proposal_dir().exists(),
        "a proposal must not be staged"
    );
}

fn missing_file_message() -> &'static str {
    if cfg!(windows) {
        "The system cannot find the file specified."
    } else {
        "no such file or directory"
    }
}
