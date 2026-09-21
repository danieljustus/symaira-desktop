#![deny(unsafe_code)]

//! Replays the Go-owned retention-rules fixture
//! `testdata/port/vault/retention-rules.json`, written by
//! `internal/retention/port_retention_rules_contract_test.go`. It covers the two
//! deliberately unported entry points `retention.LoadRules` and
//! `retention.DocMetaFromDocument` (contract row VAULT-006 / work item RUST-007).
//!
//! Every expectation in the fixture was produced by executing the real Go
//! implementation; this test replays those cases, including the negative ones,
//! and never invents an expected value of its own.

use std::fs;
use std::path::{Path, PathBuf};

use serde::Deserialize;

use symdesk_vault::retention::{self, DocMeta, RetentionError, Rule};

#[derive(Deserialize, Debug)]
struct Fixture {
    load_rules: Vec<LoadRulesVector>,
    doc_meta: Vec<DocMetaVector>,
}

#[derive(Deserialize, Debug)]
struct LoadRulesVector {
    id: String,
    description: String,
    yaml_content: String,
    #[serde(default)]
    rules: Vec<Rule>,
    #[serde(default)]
    error: String,
    #[serde(default)]
    error_class: String,
}

#[derive(Deserialize, Debug)]
struct DocMetaVector {
    id: String,
    description: String,
    /// The exact Markdown the Go parser read; `document` in the fixture is the
    /// Go-side parse result and is deliberately not deserialized here.
    markdown: String,
    doc_meta: DocMeta,
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/vault/retention-rules.json");
    let data = fs::read(&path)
        .unwrap_or_else(|err| panic!("read {}: {err} (run PORT_GENERATE=1 in Go)", path.display()));
    serde_json::from_slice(&data).unwrap_or_else(|err| panic!("decode fixture: {err}"))
}

fn scratch_dir() -> PathBuf {
    let dir = Path::new(env!("CARGO_TARGET_TMPDIR")).join("retention-rules-contracts");
    fs::create_dir_all(&dir).expect("create scratch dir");
    dir
}

fn write_rules_file(name: &str, content: &str) -> PathBuf {
    let path = scratch_dir().join(name);
    fs::write(&path, content).expect("write rules file");
    path
}

/// Replays one recorded `LoadRules` case, success or failure.
fn replay_load_rules(vector: &LoadRulesVector) {
    let context = format!("{} ({})", vector.id, vector.description);
    let path = if vector.error_class == "read_failed" {
        // A read failure is only observable for a path that does not exist, so
        // the recorded content must not be written first.
        scratch_dir().join(format!("{}-absent.yaml", vector.id))
    } else {
        write_rules_file(&format!("{}.yaml", vector.id), &vector.yaml_content)
    };

    match retention::load_rules(&path) {
        Ok(rules) => {
            assert!(
                vector.error.is_empty(),
                "{context}: Go recorded {:?} but the port returned {} rule(s)",
                vector.error,
                rules.len()
            );
            assert_eq!(rules, vector.rules, "{context}: rule vectors differ");
        }
        Err(error) => {
            assert!(
                !vector.error.is_empty(),
                "{context}: Go recorded {} rule(s) but the port failed with {error}",
                vector.rules.len()
            );
            match vector.error_class.as_str() {
                // Rule validation messages are language-neutral and are
                // therefore compared exactly.
                "validation" => {
                    assert_eq!(error.class(), "validation", "{context}: {error}");
                    assert_eq!(
                        error.to_string(),
                        vector.error,
                        "{context}: message differs"
                    );
                }
                // The inner YAML parser text is language-specific; the Go
                // wrapper and the class are the contract.
                "decode_failed" => {
                    assert!(
                        error.to_string().starts_with("parse retention rule: "),
                        "{context}: {error}"
                    );
                }
                "read_failed" => {
                    assert_eq!(error.class(), "read_failed", "{context}: {error}");
                }
                other => panic!("{context}: unhandled fixture error class {other:?}"),
            }
        }
    }
}

/// Replays one recorded `DocMetaFromDocument` case through the real Rust parser.
fn replay_doc_meta(vector: &DocMetaVector) {
    let context = format!("{} ({})", vector.id, vector.description);
    let document = symdesk_vault::parse_bytes("test.md", vector.markdown.as_bytes())
        .unwrap_or_else(|err| panic!("{context}: parse markdown: {err}"));
    assert_eq!(
        retention::doc_meta_from_document(&document),
        vector.doc_meta,
        "{context}: DocMeta differs"
    );
}

#[test]
fn retention_rules_vectors_match_the_go_oracle() {
    let fixture = fixture();
    assert!(
        !fixture.load_rules.is_empty() && !fixture.doc_meta.is_empty(),
        "fixture carries no vectors; regenerate it from the Go implementation"
    );
    for vector in &fixture.load_rules {
        replay_load_rules(vector);
    }
    for vector in &fixture.doc_meta {
        replay_doc_meta(vector);
    }
}

/// A control that only runs the positive path would pass on a port that accepts
/// everything, so the two negative outcomes are asserted directly.
#[test]
fn retention_rules_negative_controls_stay_negative() {
    let absent = scratch_dir().join("negative-control-absent.yaml");
    let _ = fs::remove_file(&absent);
    assert_eq!(
        retention::load_rules(&absent)
            .err()
            .map(|error| error.class().to_owned()),
        Some("read_failed".to_owned())
    );

    let invalid = write_rules_file(
        "negative-control-invalid.yaml",
        "name: \"\"\nperiod_days: 1\naction: trash\n",
    );
    let error = match retention::load_rules(&invalid) {
        Ok(rules) => panic!("an empty rule name must fail validation, got {rules:?}"),
        Err(error) => error,
    };
    assert_eq!(error.class(), "validation");
    assert_eq!(
        error.to_string(),
        "invalid rule \"\": rule name is required"
    );

    // The fixture's own validation cases must keep matching Go verbatim.
    assert_eq!(
        RetentionError::ReadFailed.class(),
        "read_failed",
        "the error class labels are part of the replay contract"
    );
}
