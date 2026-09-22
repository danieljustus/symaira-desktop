//! Byte-exact replay of the Go-owned dataset sync oracle.
//!
//! Reads `testdata/port/dataset/sync.json` read-only: for every recorded
//! import case Rust re-parses the CSV Go captured and must reproduce the
//! sidecar projection field for field, and for every recorded CSV rejection it
//! must fail with Go's exact message.
//!
//! ponytail: the fixture also records the Go-written handle bytes and the
//! service-level rejections, but neither is replayed here — the YAML writer
//! behind the handle and the `internal/service` import path are not ported
//! (see the DATA-001 row in docs/rust-port/contract-matrix.md).

use std::collections::BTreeMap;

use serde_json::Value;
use symdesk_vault::dataset::{PropertyConfig, parse_csv, project_rows};

/// Compiled in rather than read at run time: a missing oracle must break the
/// build, exactly as the other port fixtures do.
fn fixture() -> Value {
    let raw = include_str!("../../../testdata/port/dataset/sync.json");
    serde_json::from_str(raw).expect("fixture is not valid JSON")
}

#[test]
fn sidecar_projection_matches_go_byte_for_byte() {
    let root = fixture();
    let cases = root["cases"]
        .as_array()
        .expect("fixture must carry a cases array");
    assert!(!cases.is_empty(), "fixture must record at least one case");

    for case in cases {
        let name = case["name"].as_str().unwrap_or_default();
        let csv = case["csv"].as_str().unwrap_or_default();
        let identity = case["identity_field"].as_str().unwrap_or_default();
        let slug = case["result"]["slug"].as_str().unwrap_or_default();
        let source_path = case["result"]["raw_path"].as_str().unwrap_or_default();
        let declared: BTreeMap<String, PropertyConfig> = BTreeMap::new();

        let (rows, schema) = parse_csv(csv, &declared, identity)
            .unwrap_or_else(|err| panic!("case {name}: Go accepted this CSV, Rust did not: {err}"));

        // The inferred schema is part of the handle Go writes, so a different
        // inference would change the manifest bytes.
        let want_columns = case["result"]["columns"]
            .as_object()
            .expect("result must carry columns");
        assert_eq!(
            schema.len(),
            want_columns.len(),
            "case {name}: schema width differs from Go"
        );
        for (column, want) in want_columns {
            let got = schema
                .get(column)
                .unwrap_or_else(|| panic!("case {name}: column {column} missing in Rust"));
            assert_eq!(
                got.kind.as_str(),
                want["type"].as_str().unwrap_or_default(),
                "case {name}: column {column} type differs from Go"
            );
            assert_eq!(
                got.label.as_str(),
                want["label"].as_str().unwrap_or_default(),
                "case {name}: column {column} label differs from Go"
            );
        }

        let projected = project_rows(slug, &rows, source_path);

        let want_rows = case["sidecar_rows"]
            .as_array()
            .expect("case must carry sidecar_rows");
        assert_eq!(
            projected.len(),
            want_rows.len(),
            "case {name}: projected row count differs from Go"
        );
        assert_eq!(
            projected.len(),
            case["sidecar_count"].as_u64().unwrap_or_default() as usize,
            "case {name}: sidecar_count disagrees with the projection"
        );
        assert_eq!(
            projected.len(),
            case["result"]["rows"].as_u64().unwrap_or_default() as usize,
            "case {name}: result.rows (unique row identity) disagrees"
        );

        for (index, (got, want)) in projected.iter().zip(want_rows).enumerate() {
            assert_eq!(
                got.dataset_slug, want["dataset_slug"],
                "case {name}: row {index} slug"
            );
            assert_eq!(got.row_key, want["row_key"], "case {name}: row {index} key");
            assert_eq!(
                got.identity, want["identity"],
                "case {name}: row {index} identity"
            );
            assert_eq!(
                got.values_json,
                want["values_json"].as_str().unwrap_or_default(),
                "case {name}: row {index} values_json differs from Go's encoding/json output"
            );
            assert_eq!(
                got.source_path, want["source_path"],
                "case {name}: row {index} source"
            );
            assert_eq!(
                got.row_number,
                want["row_number"].as_i64().unwrap_or_default() as usize,
                "case {name}: row {index} row_number"
            );
        }
    }
}

#[test]
fn csv_rejections_match_go_messages_exactly() {
    let root = fixture();
    let cases = root["csv_error_cases"]
        .as_array()
        .expect("fixture must carry a csv_error_cases array");
    assert!(!cases.is_empty(), "fixture must record csv rejections");

    for case in cases {
        let name = case["name"].as_str().unwrap_or_default();
        let csv = case["csv"].as_str().unwrap_or_default();
        let identity = case["identity_field"].as_str().unwrap_or_default();
        let want = case["error"].as_str().unwrap_or_default();

        let mut declared: BTreeMap<String, PropertyConfig> = BTreeMap::new();
        if let Some(column) = case["declared_column"].as_str() {
            declared.insert(
                column.to_string(),
                PropertyConfig {
                    label: String::new(),
                    kind: case["declared_type"]
                        .as_str()
                        .unwrap_or_default()
                        .to_string(),
                },
            );
        }

        match parse_csv(csv, &declared, identity) {
            Ok(_) => panic!("case {name}: Go rejected this CSV, Rust accepted it"),
            Err(err) => assert_eq!(
                err.to_string(),
                want,
                "case {name}: rejection text differs from Go"
            ),
        }
    }
}

/// The service-level rejections are recorded for the ledger but cannot be
/// replayed until the `internal/service` import path is ported.
#[test]
fn service_level_rejections_are_recorded_not_replayed() {
    let root = fixture();
    let cases = root["error_cases"]
        .as_array()
        .expect("fixture must carry an error_cases array");
    assert_eq!(
        cases.len(),
        5,
        "the recorded service-level rejections changed; update DATA-001's scope note"
    );
}
