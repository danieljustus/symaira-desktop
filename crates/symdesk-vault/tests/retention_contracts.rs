#![deny(unsafe_code)]

//! Replays the Go-owned retention vectors (contract row VAULT-006) — fixture
//! `testdata/port/vault/retention.json`, written by
//! `internal/retention/port_retention_contract_test.go`.
//!
//! Rule and run-id validation, selector matching, reference dates, expiry
//! evaluation and the proposal/history state files are compared byte for byte;
//! for failures whose wording is language-specific (JSON decoder text, OS errno
//! text) the vectors carry a class instead of a message.

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

use serde::Deserialize;
use symdesk_vault::retention::{
    self, DocMeta, HistoryEntry, Proposal, ProposalItem, RetentionError, Rule, Selector,
};
use symdesk_vault::sha256;
use time::OffsetDateTime;

#[derive(Deserialize)]
struct Fixture {
    schema_version: i64,
    generated_on: String,
    oracle: Oracle,
    source_hashes: BTreeMap<String, String>,
    proposal_dir: String,
    history_path: String,
    rules: Vec<RuleVector>,
    run_ids: Vec<RunIdVector>,
    matches: Vec<MatchVector>,
    references: Vec<ReferenceVector>,
    evaluations: Vec<EvaluationVector>,
    proposals: Vec<FileVector>,
    history: Vec<FileVector>,
    action_ids: Vec<ActionIdVector>,
    notes: Vec<String>,
}

#[derive(Deserialize)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Deserialize)]
struct RuleVector {
    id: String,
    rule: Rule,
    error: String,
    period_days_resolved: i64,
}

#[derive(Deserialize)]
struct RunIdVector {
    id: String,
    run_id: String,
    error: String,
}

#[derive(Deserialize)]
struct MatchVector {
    id: String,
    selector: Selector,
    doc: DocMeta,
    matched: bool,
}

#[derive(Deserialize)]
struct ReferenceVector {
    id: String,
    doc: DocMeta,
    field: String,
    date: String,
    ok: bool,
}

#[derive(Deserialize)]
struct EvaluationVector {
    id: String,
    rule: Rule,
    docs: Vec<DocMeta>,
    now: String,
    items: Vec<ProposalItem>,
}

#[derive(Deserialize)]
struct FileVector {
    id: String,
    paths: Vec<String>,
    #[serde(default)]
    content: String,
    #[serde(default)]
    size: usize,
    #[serde(default)]
    sha256: String,
    #[serde(default)]
    mode: Option<u32>,
    #[serde(default)]
    error: String,
    #[serde(default)]
    error_class: String,
    #[serde(default)]
    loaded: String,
    #[serde(default)]
    platform: String,
    #[serde(default)]
    windows_gap: String,
}

#[derive(Deserialize)]
struct ActionIdVector {
    id: String,
    run_id: String,
    item_index: usize,
    action_id: String,
}

fn fixture_path() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/vault/retention.json")
}

fn load_fixture() -> Fixture {
    let path = fixture_path();
    let data =
        fs::read_to_string(&path).unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
    serde_json::from_str(&data).expect("fixture parses")
}

fn sha256_hex(data: &[u8]) -> String {
    sha256::digest(data)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

/// The fixture records file modes from the platform that generated it.
fn modes_observable(fixture: &Fixture) -> bool {
    let goos = match std::env::consts::OS {
        "macos" => "darwin",
        other => other,
    };
    fixture.generated_on == goos
}

fn class_of(error: &RetentionError) -> String {
    error.class().to_owned()
}

#[test]
fn retention_vectors_match_the_go_oracle() {
    let fixture = load_fixture();
    assert_eq!(fixture.schema_version, 1, "fixture schema version");
    assert!(
        fixture.oracle.commit.len() >= 40 && !fixture.oracle.release.is_empty(),
        "fixture must name the pinned oracle"
    );
    assert!(
        fixture
            .source_hashes
            .contains_key("internal/retention/retention.go"),
        "source digest for the Go engine is missing"
    );
    assert!(
        !fixture.notes.is_empty(),
        "fixture documents its own limits"
    );

    // The recorded state paths are vault-relative; the port builds both of them.
    let vault = Path::new("/vault");
    assert_eq!(
        retention::proposal_dir(vault),
        vault.join(&fixture.proposal_dir),
        "proposal directory"
    );
    assert_eq!(
        retention::history_path(vault),
        vault.join(&fixture.history_path),
        "history path"
    );

    let runnable = fixture.rules.len()
        + fixture.run_ids.len()
        + fixture.matches.len()
        + fixture.references.len()
        + fixture.evaluations.len()
        + fixture.proposals.len()
        + fixture.history.len()
        + fixture.action_ids.len();
    assert!(runnable > 0, "no vectors to replay");

    for vector in &fixture.rules {
        let outcome = retention::validate(&vector.rule);
        match outcome {
            Ok(()) => {
                assert_eq!(vector.error, "", "{}: expected no error", vector.id);
                assert_eq!(
                    retention::period_from_days(vector.rule.period_days),
                    time::Duration::days(vector.period_days_resolved),
                    "{}: resolved period",
                    vector.id
                );
            }
            Err(err) => assert_eq!(err.to_string(), vector.error, "{}: error", vector.id),
        }
    }

    for vector in &fixture.run_ids {
        let outcome = retention::validate_run_id(&vector.run_id);
        match outcome {
            Ok(()) => assert_eq!(vector.error, "", "{}: expected no error", vector.id),
            Err(err) => assert_eq!(err.to_string(), vector.error, "{}: error", vector.id),
        }
    }

    for vector in &fixture.matches {
        assert_eq!(
            retention::matches(&vector.selector, &vector.doc),
            vector.matched,
            "{}: selector result",
            vector.id
        );
    }

    for vector in &fixture.references {
        let parsed = retention::reference_date(&vector.doc, &vector.field);
        assert_eq!(parsed.is_some(), vector.ok, "{}: parse result", vector.id);
        if let Some(value) = parsed {
            assert_eq!(
                symdesk_vault::history::format_rfc3339_nano_utc(value),
                vector.date,
                "{}: parsed date",
                vector.id
            );
        }
    }

    for vector in &fixture.evaluations {
        let now =
            OffsetDateTime::parse(&vector.now, &time::format_description::well_known::Rfc3339)
                .expect("vector time parses");
        let items = retention::evaluate(&vector.rule, &vector.docs, now);
        let got = serde_json::to_value(&items).expect("items encode");
        let want = serde_json::to_value(&vector.items).expect("fixture items encode");
        assert_eq!(got, want, "{}: evaluation items", vector.id);
    }

    for vector in &fixture.action_ids {
        assert_eq!(
            retention::stable_action_id(&vector.run_id, vector.item_index),
            vector.action_id,
            "{}: action id",
            vector.id
        );
    }

    // A Windows gap must always be explained, and it never skips the port: the
    // Go writer is the platform-limited side, not the Rust replay.
    let gapped: Vec<&FileVector> = fixture
        .proposals
        .iter()
        .chain(fixture.history.iter())
        .filter(|vector| vector.platform == "unix")
        .collect();
    for vector in &gapped {
        assert!(
            !vector.windows_gap.is_empty(),
            "{}: a platform mark needs a reason",
            vector.id
        );
    }
    assert!(
        gapped.len() >= 3,
        "the three Go write vectors should carry the Unix mark, found {}",
        gapped.len()
    );

    replay_proposals(&fixture);
    replay_history(&fixture);
}

fn replay_proposals(fixture: &Fixture) {
    let modes = modes_observable(fixture);
    let root = temp_dir("symdesk-port-retention-proposals-");

    let write_vector = vector_by_id(&fixture.proposals, "write-proposal");
    let proposal: Proposal =
        serde_json::from_str(&write_vector.content).expect("the recorded proposal parses");
    retention::write_proposal(&root, &proposal).expect("write proposal");
    let written = root.join(&write_vector.paths[0]);
    compare_file(&written, write_vector, modes, "write-proposal");

    let load_vector = vector_by_id(&fixture.proposals, "load-proposal");
    let loaded = retention::load_proposal(&root, "run-20260918").expect("load proposal");
    assert_eq!(
        serde_json::to_string(&loaded).expect("loaded encodes"),
        load_vector.loaded,
        "load-proposal: loaded document"
    );

    let missing = vector_by_id(&fixture.proposals, "load-missing-proposal");
    let error = retention::load_proposal(&root, "missing-run").expect_err("missing run fails");
    assert_eq!(
        class_of(&error),
        missing.error_class,
        "load-missing-proposal: class"
    );
    assert_eq!(
        missing.error, "",
        "load-missing-proposal: no portable message"
    );

    let invalid = vector_by_id(&fixture.proposals, "write-invalid-run-id");
    let error = retention::write_proposal(
        &root,
        &Proposal {
            run_id: "../escape".to_owned(),
            rule_name: String::new(),
            created: OffsetDateTime::UNIX_EPOCH,
            items: Some(Vec::new()),
            status: retention::PROPOSAL_STATUS_PENDING.to_owned(),
        },
    )
    .expect_err("unsafe run id fails");
    assert_eq!(
        error.to_string(),
        invalid.error,
        "write-invalid-run-id: error"
    );
    assert_eq!(
        class_of(&error),
        invalid.error_class,
        "write-invalid-run-id: class"
    );

    let _ = fs::remove_dir_all(&root);
}

fn replay_history(fixture: &Fixture) {
    let modes = modes_observable(fixture);
    let root = temp_dir("symdesk-port-retention-history-");

    let missing = vector_by_id(&fixture.history, "load-missing-history");
    let entries = retention::load_history(&root).expect("missing history is empty");
    assert!(entries.is_empty(), "load-missing-history: no entries");
    assert_eq!(missing.error, "", "load-missing-history: no error");
    assert_eq!(
        missing.loaded, "null",
        "load-missing-history: Go records nil as null"
    );

    // Replay the same three appends the Go harness performed: one legacy entry
    // without an action id, one modern entry, then a retry of that entry.
    let recorded: Vec<HistoryEntry> =
        serde_json::from_str(&vector_by_id(&fixture.history, "append-and-deduplicate").loaded)
            .expect("recorded entries parse");
    assert_eq!(recorded.len(), 2, "the harness appended two entries");
    let legacy = recorded[0].clone();
    let modern = recorded[1].clone();
    assert!(
        legacy.action_id.is_empty(),
        "the first entry is the legacy form"
    );
    assert!(
        !modern.action_id.is_empty(),
        "the second entry carries an action id"
    );
    retention::append_history(&root, &legacy).expect("append legacy");
    retention::append_history(&root, &modern).expect("append modern");
    let mut retry = modern.clone();
    retry.timestamp = modern.timestamp + time::Duration::minutes(1);
    retention::append_history(&root, &retry).expect("append retry");

    let reopen = vector_by_id(&fixture.history, "append-and-deduplicate");
    let written = root.join(&reopen.paths[0]);
    compare_file(&written, reopen, modes, "append-and-deduplicate");
    let entries = retention::load_history(&root).expect("history loads");
    assert_eq!(
        serde_json::to_string(&entries).expect("entries encode"),
        reopen.loaded,
        "append-and-deduplicate: entries"
    );

    let null_root = temp_dir("symdesk-port-retention-null-");
    fs::create_dir_all(retention::proposal_dir(&null_root)).expect("state dir");
    fs::write(retention::history_path(&null_root), "null").expect("write null history");
    let null_vector = vector_by_id(&fixture.history, "load-null-history");
    let error = retention::load_history(&null_root).expect_err("null history is rejected");
    assert_eq!(
        error.to_string(),
        null_vector.error,
        "load-null-history: error"
    );

    let object_root = temp_dir("symdesk-port-retention-object-");
    fs::create_dir_all(retention::proposal_dir(&object_root)).expect("state dir");
    fs::write(retention::history_path(&object_root), "{}").expect("write object history");
    let object_vector = vector_by_id(&fixture.history, "load-object-history");
    let error = retention::load_history(&object_root).expect_err("object history is rejected");
    assert_eq!(
        class_of(&error),
        object_vector.error_class,
        "load-object-history: class"
    );

    let _ = fs::remove_dir_all(&root);
    let _ = fs::remove_dir_all(&null_root);
    let _ = fs::remove_dir_all(&object_root);
}

#[test]
fn proposal_decoder_preserves_go_zero_values_and_nil_slices() {
    let root = temp_dir("sd-retention-json-shapes-");
    fs::create_dir_all(retention::proposal_dir(&root)).expect("state dir");

    fs::write(
        retention::proposal_dir(&root).join("missing.json"),
        r#"{"run_id":"missing","created":null,"status":"pending"}"#,
    )
    .expect("write missing-items proposal");
    let missing = retention::load_proposal(&root, "missing").expect("missing items load");
    assert!(missing.items.is_none(), "omitted items are Go's nil slice");
    assert_eq!(
        symdesk_vault::history::format_rfc3339_nano_utc(missing.created),
        "0001-01-01T00:00:00Z"
    );

    fs::write(
        retention::proposal_dir(&root).join("empty.json"),
        r#"{"run_id":"empty","items":[],"status":"pending"}"#,
    )
    .expect("write empty-items proposal");
    let empty = retention::load_proposal(&root, "empty").expect("empty items load");
    assert_eq!(empty.items, Some(Vec::new()));

    fs::write(
        retention::proposal_dir(&root).join("duplicate.json"),
        r#"{"run_id":"duplicate","rule_name":"old","rule_name":null,"created":"2026-01-02T03:04:05Z","created":null,"items":[],"items":null}"#,
    )
    .expect("write duplicate proposal");
    let duplicate = retention::load_proposal(&root, "duplicate").expect("duplicate keys load");
    assert_eq!(
        duplicate.rule_name, "old",
        "Go ignores null when decoding into an existing string field"
    );
    assert_eq!(
        symdesk_vault::history::format_rfc3339_nano_utc(duplicate.created),
        "2026-01-02T03:04:05Z",
        "time.Time.UnmarshalJSON also ignores duplicate null"
    );
    assert!(
        duplicate.items.is_none(),
        "last duplicate null clears a slice"
    );

    fs::write(
        retention::proposal_dir(&root).join("folded.json"),
        r#"{"RUN_ID":"upper","Run_Id":"mixed","ſtatus":"pending","items":[]}"#,
    )
    .expect("write folded-field proposal");
    let folded = retention::load_proposal(&root, "folded").expect("folded fields load");
    assert_eq!(folded.run_id, "mixed", "last folded duplicate wins");
    assert_eq!(
        folded.status, "pending",
        "Go Unicode field folding matches long s"
    );

    let _ = fs::remove_dir_all(&root);
}

#[test]
fn proposal_decoder_preserves_go_json_diagnostics() {
    let root = temp_dir("sd-retention-json-errors-");
    fs::create_dir_all(retention::proposal_dir(&root)).expect("state dir");
    for (run_id, data, expected) in [
        ("eof", "{", "unexpected end of JSON input"),
        (
            "key",
            "{ not json",
            "invalid character 'n' looking for beginning of object key string",
        ),
        (
            "type",
            r#"{"run_id":1}"#,
            "json: cannot unmarshal number into Go struct field Proposal.run_id of type string",
        ),
    ] {
        fs::write(
            retention::proposal_dir(&root).join(format!("{run_id}.json")),
            data,
        )
        .expect("write malformed proposal");
        let error = retention::load_proposal(&root, run_id).expect_err("proposal must fail");
        assert_eq!(error.class(), "decode_failed", "{run_id}: error class");
        assert_eq!(error.to_string(), expected, "{run_id}: diagnostic");
    }
    let _ = fs::remove_dir_all(&root);
}

#[test]
fn retention_run_id_diagnostics_use_go_quote_for_controls_and_unicode() {
    for (run_id, expected) in [
        (
            "bad\u{7}",
            r#"retention proposal run ID "bad\a" contains a control character"#,
        ),
        (
            "bad\u{200b}<",
            r#"retention proposal run ID "bad\u200b<" is not platform-safe"#,
        ),
        (
            "bad\u{e0001}<",
            r#"retention proposal run ID "bad\U000e0001<" is not platform-safe"#,
        ),
    ] {
        let error = retention::validate_run_id(run_id).expect_err("unsafe ID must fail");
        assert_eq!(error.to_string(), expected);
    }
}

#[test]
fn retention_json_decoder_preserves_go_order_and_diagnostics() {
    let root = temp_dir("sd-retention-ordered-json-");
    fs::create_dir_all(retention::proposal_dir(&root)).expect("state dir");

    for (run_id, data, expected) in [
        (
            "duplicate-run-id",
            r#"{"run_id":1,"run_id":"duplicate-run-id","created":"2026-01-02T03:04:05Z","items":[],"status":"pending"}"#,
            "json: cannot unmarshal number into Go struct field Proposal.run_id of type string",
        ),
        (
            "duplicate-items",
            r#"{"run_id":"duplicate-items","items":{},"items":[]}"#,
            "json: cannot unmarshal object into Go struct field Proposal.items of type []retention.ProposalItem",
        ),
        (
            "duplicate-item-path",
            r#"{"run_id":"duplicate-item-path","items":[{"path":1,"path":"note.md","action":"trash"}]}"#,
            "json: cannot unmarshal number into Go struct field ProposalItem.items.path of type string",
        ),
        (
            "items-first",
            r#"{"items":{},"run_id":1}"#,
            "json: cannot unmarshal object into Go struct field Proposal.items of type []retention.ProposalItem",
        ),
        (
            "run-id-first",
            r#"{"run_id":1,"items":{}}"#,
            "json: cannot unmarshal number into Go struct field Proposal.run_id of type string",
        ),
        (
            "syntax-before-type",
            r#"{"run_id":1,}"#,
            "invalid character '}' looking for beginning of object key string",
        ),
        (
            "malformed-token",
            r#"{"run_id":truX}"#,
            "invalid character 'X' in literal true (expecting 'e')",
        ),
        (
            "invalid-created",
            r#"{"run_id":"invalid-created","created":"not-a-time","items":[]}"#,
            "parsing time \"not-a-time\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \"not-a-time\" as \"2006\"",
        ),
        (
            "type-before-invalid-created",
            r#"{"run_id":1,"created":"not-a-time","items":[]}"#,
            "parsing time \"not-a-time\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \"not-a-time\" as \"2006\"",
        ),
        (
            "type-before-nonstring-created",
            r#"{"run_id":1,"created":2,"items":[]}"#,
            "Time.UnmarshalJSON: input is not a JSON string",
        ),
        (
            "first-ordinary-type-error",
            r#"{"status":1,"run_id":2,"items":[]}"#,
            "json: cannot unmarshal number into Go struct field Proposal.status of type string",
        ),
    ] {
        fs::write(
            retention::proposal_dir(&root).join(format!("{run_id}.json")),
            data,
        )
        .expect("write proposal case");
        let error = retention::load_proposal(&root, run_id).expect_err("proposal must fail");
        assert_eq!(error.class(), "decode_failed", "{run_id}: error class");
        assert_eq!(error.to_string(), expected, "{run_id}: diagnostic");
    }

    for (data, expected) in [
        (
            r#"[{"timestamp":"2026-01-02T03:04:05Z","rule_name":1,"rule_name":"ok","action":"trash","path":"note.md","title":"Note"}]"#,
            "json: cannot unmarshal number into Go struct field HistoryEntry.rule_name of type string",
        ),
        (
            r#"[{"timestamp":1,"timestamp":"2026-01-02T03:04:05Z"}]"#,
            "Time.UnmarshalJSON: input is not a JSON string",
        ),
        (
            r#"[{"timestamp":"2026-01-02T03:04:05Z"},]"#,
            "invalid character ']' looking for beginning of value",
        ),
        (
            r#"[{"timestamp":"not-a-time"}]"#,
            "parsing time \"not-a-time\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \"not-a-time\" as \"2006\"",
        ),
        (
            r#"[{"rule_name":1,"timestamp":"not-a-time"}]"#,
            "parsing time \"not-a-time\" as \"2006-01-02T15:04:05Z07:00\": cannot parse \"not-a-time\" as \"2006\"",
        ),
    ] {
        fs::write(retention::history_path(&root), data).expect("write history case");
        let error = retention::load_history(&root).expect_err("history must fail");
        assert_eq!(error.class(), "decode_failed");
        assert_eq!(error.to_string(), expected);
    }

    let _ = fs::remove_dir_all(&root);
}

#[cfg(unix)]
#[test]
fn write_proposal_preserves_existing_retention_directory_mode() {
    use std::os::unix::fs::PermissionsExt;

    let root = temp_dir("sd-retention-existing-mode-");
    let dir = retention::proposal_dir(&root);
    fs::create_dir_all(&dir).expect("state dir");
    fs::set_permissions(&dir, fs::Permissions::from_mode(0o700)).expect("set private mode");
    let proposal = Proposal {
        run_id: "mode".to_owned(),
        rule_name: "rule".to_owned(),
        created: OffsetDateTime::UNIX_EPOCH,
        items: Some(Vec::new()),
        status: retention::PROPOSAL_STATUS_PENDING.to_owned(),
    };
    retention::write_proposal(&root, &proposal).expect("write proposal");
    assert_eq!(file_mode(&dir), Some(0o700));
    let _ = fs::remove_dir_all(&root);
}

#[cfg(unix)]
#[test]
fn write_proposal_new_directory_modes_follow_go_mkdirall() {
    use std::process::Command;

    const CHILD: &str = "SYMDESK_RETENTION_MODE_CHILD";
    const ROOT: &str = "SYMDESK_RETENTION_MODE_ROOT";
    if std::env::var_os(CHILD).is_some() {
        let root = PathBuf::from(std::env::var_os(ROOT).expect("child root"));
        let proposal = Proposal {
            run_id: "mode".to_owned(),
            rule_name: "rule".to_owned(),
            created: OffsetDateTime::UNIX_EPOCH,
            items: Some(Vec::new()),
            status: retention::PROPOSAL_STATUS_PENDING.to_owned(),
        };
        retention::write_proposal(&root.join("one").join("vault"), &proposal)
            .expect("child writes proposal");
        return;
    }

    for (umask, expected) in [("000", 0o755), ("077", 0o700)] {
        let root = temp_dir(&format!("sd-retention-umask-{umask}-"));
        let executable = std::env::current_exe().expect("current test executable");
        let status = Command::new("/bin/sh")
            .args([
                "-c",
                "umask \"$CHILD_UMASK\"; exec \"$CHILD_EXE\" --exact write_proposal_new_directory_modes_follow_go_mkdirall --nocapture",
            ])
            .env("CHILD_UMASK", umask)
            .env("CHILD_EXE", &executable)
            .env(CHILD, "1")
            .env(ROOT, &root)
            .status()
            .expect("run mode child");
        assert!(status.success(), "child failed for umask {umask}");
        for relative in [
            Path::new("one"),
            Path::new("one/vault"),
            Path::new("one/vault/.symdesk"),
            Path::new("one/vault/.symdesk/retention"),
        ] {
            assert_eq!(
                file_mode(&root.join(relative)),
                Some(expected),
                "{} under umask {umask}",
                relative.display()
            );
        }
        let _ = fs::remove_dir_all(&root);
    }
}

fn compare_file(path: &Path, vector: &FileVector, modes: bool, label: &str) {
    let data =
        fs::read(path).unwrap_or_else(|err| panic!("{label}: read {}: {err}", path.display()));
    assert_eq!(
        String::from_utf8_lossy(&data),
        vector.content,
        "{label}: file bytes"
    );
    assert_eq!(data.len(), vector.size, "{label}: file size");
    assert_eq!(sha256_hex(&data), vector.sha256, "{label}: file digest");
    if modes {
        assert_eq!(file_mode(path), vector.mode, "{label}: file mode");
    } else {
        let _ = vector.mode;
    }
}

fn vector_by_id<'a>(vectors: &'a [FileVector], id: &str) -> &'a FileVector {
    vectors
        .iter()
        .find(|vector| vector.id == id)
        .unwrap_or_else(|| panic!("fixture vector {id} is missing"))
}

fn temp_dir(prefix: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("{prefix}{}", std::process::id()));
    let _ = fs::remove_dir_all(&dir);
    fs::create_dir_all(&dir).expect("create temp dir");
    dir
}

#[cfg(unix)]
fn file_mode(path: &Path) -> Option<u32> {
    use std::os::unix::fs::PermissionsExt;
    Some(fs::metadata(path).expect("metadata").permissions().mode() & 0o777)
}

#[cfg(not(unix))]
fn file_mode(_path: &Path) -> Option<u32> {
    None
}
