#![deny(unsafe_code)]

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::Path;
use std::sync::atomic::{AtomicU64, Ordering};

use serde::Deserialize;
use serde_json::value::RawValue;
use sha2::{Digest, Sha256};
use symroom_core::{
    event::Event,
    journal,
    runs::{self, Run},
};

const FIXTURE: &str = include_str!("../../../testdata/port/room/run-projection.json");

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: BTreeMap<String, String>,
    events: Vec<Box<RawValue>>,
    records: Vec<String>,
    checkpoint_records: Vec<String>,
    journal_queries: JournalQueries,
}

#[derive(Deserialize)]
struct JournalQueries {
    journal_files: Vec<JournalFile>,
    signers: BTreeMap<String, String>,
    merged_event_ids: Vec<String>,
    list_all: Vec<String>,
    list_pending: Vec<String>,
    gets: Vec<GetVector>,
    checkpoint_records: Vec<String>,
    checkpoint_array_normalization: String,
}

#[derive(Deserialize)]
struct JournalFile {
    name: String,
    content: String,
}

#[derive(Deserialize)]
struct GetVector {
    run_id: String,
    record: String,
    error: String,
}

#[test]
fn go_run_projection_and_journal_queries_match_byte_for_byte() {
    let fixture: Fixture = serde_json::from_str(FIXTURE).expect("Go-generated fixture parses");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle_revision,
        "a80da93e3ec02801c73aa5b2318dc06de3efd3fa"
    );
    assert_eq!(fixture.records.len(), 8, "nonzero projected records");
    assert_eq!(fixture.events.len(), 32, "fixture exercises all edge paths");
    assert_eq!(fixture.checkpoint_records.len(), 2);
    for source in [
        "internal/room/run/run.go",
        "internal/room/run/checkpoint.go",
        "internal/room/event/event.go",
        "internal/room/journal/journal.go",
        "internal/room/journal/merge.go",
    ] {
        let path = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../..")
            .join(source);
        let bytes = fs::read(&path).unwrap_or_else(|err| panic!("read {}: {err}", path.display()));
        assert_eq!(
            fixture.source_hashes.get(source),
            Some(&hex::encode(Sha256::digest(bytes))),
            "oracle source hash {source}"
        );
    }

    let events = fixture
        .events
        .iter()
        .map(|raw| Event::unmarshal_json_line(raw.get().as_bytes()).expect("fixture event parses"))
        .collect::<Vec<_>>();
    let kinds = events
        .iter()
        .map(|event| event.kind.as_str())
        .collect::<BTreeSet<_>>();
    for kind in [
        "run.requested",
        "run.approved",
        "run.denied",
        "run.started",
        "run.finished",
        "run.failed",
        "run.cancelled",
        "run.retried",
        "checkpoint.requested",
        "checkpoint.resolved",
    ] {
        assert!(kinds.contains(kind), "fixture is missing event kind {kind}");
    }
    for id in [
        "malformed",
        "empty-request",
        "unknown",
        "unmatched",
        "bad-request",
        "key-order-upper-lower",
        "key-order-lower-upper",
        "key-order-interleaved",
        "ignored-deep",
        "ignored-huge-number",
        "checkpoint-orphan-resolve",
        "checkpoint-first-request",
        "checkpoint-first-resolve",
        "checkpoint-repeat-request",
        "checkpoint-final-resolve",
        "checkpoint-null-request",
        "checkpoint-null-resolve",
        "checkpoint-unmatched-resolve",
        "checkpoint-bad-request",
        "checkpoint-bad-resolve",
        "checkpoint-empty-request",
    ] {
        assert!(
            events.iter().any(|event| event.id == id),
            "fixture is missing edge case {id}"
        );
    }
    assert!(matches_go_records(&events, &fixture.records));
    let checkpoints = runs::project_checkpoints(&events);
    let actual_checkpoints = checkpoints
        .values()
        .map(|checkpoint| serde_json::to_vec(checkpoint).expect("checkpoint serializes"))
        .collect::<Vec<_>>();
    let expected_checkpoints = fixture
        .checkpoint_records
        .iter()
        .map(String::as_bytes)
        .collect::<Vec<_>>();
    assert_eq!(
        actual_checkpoints, expected_checkpoints,
        "Go/Rust checkpoint records"
    );

    // Negative control: changing the final, case-insensitive run_id value must
    // make the same byte comparator reject the projection.
    let mut changed = events.clone();
    let event = changed
        .iter_mut()
        .find(|event| event.id == "key-order-interleaved")
        .expect("negative-control event");
    event.body = RawValue::from_string(
        r#"{"run_id":"run-a","RUN_ID":"run-b","run_id":"negative-control-id","title":"interleaved duplicates"}"#.into(),
    )
    .expect("valid negative-control body");
    assert!(!matches_go_records(&changed, &fixture.records));

    replay_journal_queries(&fixture.journal_queries);
}

fn replay_journal_queries(fixture: &JournalQueries) {
    assert_eq!(
        fixture.checkpoint_array_normalization,
        "sort only each run.checkpoints array by checkpoint id; Go ProjectRuns ranges a map"
    );
    static NEXT_TEMP: AtomicU64 = AtomicU64::new(0);
    let root = std::env::temp_dir().join(format!(
        "symroom-run-query-{}-{}",
        std::process::id(),
        NEXT_TEMP.fetch_add(1, Ordering::Relaxed)
    ));
    let journal_dir = root.join("journal");
    fs::create_dir_all(&journal_dir).expect("create fixture journal");
    for file in &fixture.journal_files {
        fs::write(journal_dir.join(&file.name), file.content.as_bytes())
            .expect("write Go-produced journal segment");
    }

    let merged = journal::merge_all(&root).expect("merge Go-produced journal segments");
    let merged_ids = merged
        .iter()
        .map(|event| event.id.clone())
        .collect::<Vec<_>>();
    assert_eq!(
        merged_ids, fixture.merged_event_ids,
        "Go/Rust journal merge order"
    );
    for event in &merged {
        let public_key = fixture
            .signers
            .get(&event.author)
            .expect("fixture signer for every author");
        event
            .verify_signature(&hex::decode(public_key).expect("signer key hex"))
            .unwrap_or_else(|error| panic!("Go fixture signature for {}: {error}", event.id));
    }

    let all = runs::list(&root, false).expect("Go-compatible List all");
    assert_eq!(
        encode_runs(&all, true),
        fixture.list_all,
        "Go/Rust List all"
    );
    let pending = runs::list(&root, true).expect("Go-compatible pending List");
    assert_eq!(
        encode_runs(&pending, true),
        fixture.list_pending,
        "Go/Rust pending List"
    );

    for vector in &fixture.gets {
        match runs::get(&root, &vector.run_id) {
            Ok(run) => {
                assert!(
                    vector.error.is_empty(),
                    "unexpected Go error for {}",
                    vector.run_id
                );
                assert_eq!(
                    encode_run(&run, true),
                    vector.record,
                    "Go/Rust Get {}",
                    vector.run_id
                );
            }
            Err(error) => {
                assert!(
                    vector.record.is_empty(),
                    "unexpected Go record for {}",
                    vector.run_id
                );
                assert_eq!(
                    error.to_string(),
                    vector.error,
                    "Go/Rust Get error {}",
                    vector.run_id
                );
            }
        }
    }

    let checkpoints = runs::project_checkpoints(&merged);
    let actual = checkpoints
        .values()
        .map(|checkpoint| serde_json::to_string(checkpoint).expect("checkpoint serializes"))
        .collect::<Vec<_>>();
    assert_eq!(
        actual, fixture.checkpoint_records,
        "Go/Rust journal checkpoints"
    );
    fs::remove_dir_all(&root).expect("remove only this test's temporary journal");
}

fn encode_runs(runs: &[Run], normalize_checkpoints: bool) -> Vec<String> {
    runs.iter()
        .map(|run| encode_run(run, normalize_checkpoints))
        .collect()
}

fn encode_run(run: &Run, normalize_checkpoints: bool) -> String {
    let mut run = run.clone();
    if normalize_checkpoints && let Some(checkpoints) = &mut run.checkpoints {
        checkpoints.sort_by(|left, right| left.id.cmp(&right.id));
    }
    serde_json::to_string(&run).expect("run serializes")
}

fn matches_go_records(events: &[Event], records: &[String]) -> bool {
    let projected: BTreeMap<String, Run> = runs::project_runs(events);
    if projected.len() != records.len() {
        return false;
    }
    let actual = projected
        .values()
        .map(|run| serde_json::to_vec(run).expect("run serializes"))
        .collect::<Vec<_>>();
    let expected = records.iter().map(String::as_bytes).collect::<Vec<_>>();
    actual.iter().map(Vec::as_slice).eq(expected)
}
