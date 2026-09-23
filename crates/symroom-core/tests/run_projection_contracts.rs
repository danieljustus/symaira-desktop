#![deny(unsafe_code)]

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::Path;

use serde::Deserialize;
use serde_json::value::RawValue;
use sha2::{Digest, Sha256};
use symroom_core::{
    event::Event,
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
}

#[test]
fn go_run_projection_records_match_byte_for_byte() {
    let fixture: Fixture = serde_json::from_str(FIXTURE).expect("Go-generated fixture parses");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle_revision,
        "a80da93e3ec02801c73aa5b2318dc06de3efd3fa"
    );
    assert_eq!(fixture.records.len(), 4, "nonzero projected records");
    assert_eq!(fixture.events.len(), 16, "fixture exercises all edge paths");
    for source in ["internal/room/run/run.go", "internal/room/event/event.go"] {
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
    ] {
        assert!(kinds.contains(kind), "fixture is missing event kind {kind}");
    }
    for id in [
        "malformed",
        "empty-request",
        "unknown",
        "unmatched",
        "bad-request",
    ] {
        assert!(
            events.iter().any(|event| event.id == id),
            "fixture is missing edge case {id}"
        );
    }
    let projected: BTreeMap<String, Run> = runs::project_runs(&events);
    assert_eq!(
        projected.len(),
        fixture.records.len(),
        "projected run count"
    );
    let actual = projected
        .values()
        .map(|run| serde_json::to_vec(run).expect("run serializes"))
        .collect::<Vec<_>>();
    let expected = fixture
        .records
        .iter()
        .map(String::as_bytes)
        .collect::<Vec<_>>();
    assert_eq!(actual, expected, "serialized Go/Rust projection records");
}
