//! Byte-exact replay of the Go-owned dataset parser/projection oracle.
//!
//! This is deliberately helper-only coverage. The production CLI/MCP path uses
//! `Service.DatasetSync` in `internal/service/datasets.go`; this crate does not
//! implement that writer, its idempotency rules, or its filesystem side effects.
//! The fixture's full-import records remain evidence only, while Rust replays the
//! shared `ParseCSV` + `replaceDatasetRows` semantics.

use std::collections::BTreeMap;
use std::panic::{AssertUnwindSafe, catch_unwind};

use serde_json::Value;
use symdesk_vault::dataset::{PropertyConfig, parse_csv, project_rows};

/// Compiled in rather than read at run time: a missing oracle must break the
/// build, exactly as the other port fixtures do.
fn fixture() -> Value {
    let raw = include_str!("../../../testdata/port/dataset/sync.json");
    serde_json::from_str(raw).expect("fixture is not valid JSON")
}

fn property_map(value: &Value) -> Result<BTreeMap<String, PropertyConfig>, String> {
    let Some(properties) = value.as_object() else {
        return Ok(BTreeMap::new());
    };
    let mut out = BTreeMap::new();
    for (column, property) in properties {
        out.insert(
            column.clone(),
            PropertyConfig {
                label: property["label"].as_str().unwrap_or_default().to_owned(),
                kind: property["type"].as_str().unwrap_or_default().to_owned(),
            },
        );
    }
    Ok(out)
}

fn validate_projection_case(
    case: &Value,
    slug: &str,
    source_path: &str,
    expected_schema_path: &str,
) -> Result<(), String> {
    let name = case["name"].as_str().unwrap_or_default();
    let csv = case["csv"].as_str().unwrap_or_default();
    let identity = case["identity_field"].as_str().unwrap_or_default();
    let declared = property_map(&case["declared"])?;
    let parsed = catch_unwind(AssertUnwindSafe(|| parse_csv(csv, &declared, identity)))
        .map_err(|_| format!("case {name}: Rust panicked while parsing Go input"))?;
    let (rows, schema) =
        parsed.map_err(|err| format!("case {name}: Go accepted this CSV, Rust did not: {err}"))?;

    let want_columns = case
        .pointer(expected_schema_path)
        .and_then(Value::as_object)
        .ok_or_else(|| format!("case {name}: fixture has no schema at {expected_schema_path}"))?;
    if schema.len() != want_columns.len() {
        return Err(format!(
            "case {name}: schema width {} differs from Go {}",
            schema.len(),
            want_columns.len()
        ));
    }
    for (column, want) in want_columns {
        let got = schema
            .get(column)
            .ok_or_else(|| format!("case {name}: column {column} missing in Rust"))?;
        let want_kind = want["type"].as_str().unwrap_or_default();
        let want_label = want["label"].as_str().unwrap_or_default();
        if got.kind != want_kind || got.label != want_label {
            return Err(format!(
                "case {name}: column {column} = ({:?}, {:?}), want ({want_kind:?}, {want_label:?})",
                got.kind, got.label
            ));
        }
    }

    let projected = project_rows(slug, &rows, source_path)
        .map_err(|err| format!("case {name}: Go projected rows, Rust failed: {err}"))?;
    let want_rows = case["sidecar_rows"]
        .as_array()
        .ok_or_else(|| format!("case {name}: fixture has no sidecar_rows"))?;
    if projected.len() != want_rows.len() {
        return Err(format!(
            "case {name}: projected row count {} differs from Go {}",
            projected.len(),
            want_rows.len()
        ));
    }

    for (index, (got, want)) in projected.iter().zip(want_rows).enumerate() {
        let fields_match = got.dataset_slug == want["dataset_slug"].as_str().unwrap_or_default()
            && got.row_key == want["row_key"].as_str().unwrap_or_default()
            && got.identity == want["identity"].as_str().unwrap_or_default()
            && got.values_json == want["values_json"].as_str().unwrap_or_default()
            && got.source_path == want["source_path"].as_str().unwrap_or_default()
            && got.row_number == want["row_number"].as_u64().unwrap_or_default() as usize;
        if !fields_match {
            return Err(format!(
                "case {name}: projected row {index} differs: got {got:?}, want {want}"
            ));
        }
    }
    Ok(())
}

#[test]
fn import_projection_matches_go_byte_for_byte() {
    let root = fixture();
    assert_eq!(root["schema_version"], 2);
    let cases = root["cases"]
        .as_array()
        .expect("fixture must carry a cases array");
    assert_eq!(cases.len(), 6, "the full-import oracle case count changed");

    for case in cases {
        let slug = case["result"]["slug"].as_str().unwrap_or_default();
        let source_path = case["result"]["raw_path"].as_str().unwrap_or_default();
        validate_projection_case(case, slug, source_path, "/result/columns")
            .unwrap_or_else(|error| panic!("{error}"));
        let projected_count = case["sidecar_rows"].as_array().map_or(0, Vec::len);
        assert_eq!(
            projected_count,
            case["sidecar_count"].as_u64().unwrap_or_default() as usize
        );
        assert_eq!(
            projected_count,
            case["result"]["rows"].as_u64().unwrap_or_default() as usize
        );
    }
}

#[test]
fn focused_csv_parser_and_projection_cases_match_go() {
    let root = fixture();
    let cases = root["csv_cases"]
        .as_array()
        .expect("fixture must carry csv_cases");
    assert_eq!(cases.len(), 4, "the focused CSV oracle case count changed");
    for case in cases {
        validate_projection_case(case, "oracle", "datasets/oracle/source.csv", "/schema")
            .unwrap_or_else(|error| panic!("{error}"));
    }
}

#[test]
fn csv_rejections_match_go_messages_exactly_without_panics() {
    let root = fixture();
    let cases = root["csv_error_cases"]
        .as_array()
        .expect("fixture must carry a csv_error_cases array");
    assert_eq!(cases.len(), 16, "the CSV rejection corpus changed");

    for case in cases {
        let name = case["name"].as_str().unwrap_or_default();
        let csv = case["csv"].as_str().unwrap_or_default();
        let identity = case["identity_field"].as_str().unwrap_or_default();
        let want = case["error"].as_str().unwrap_or_default();

        let mut declared = BTreeMap::new();
        if let Some(column) = case["declared_column"].as_str() {
            declared.insert(
                column.to_owned(),
                PropertyConfig {
                    label: String::new(),
                    kind: case["declared_type"]
                        .as_str()
                        .unwrap_or_default()
                        .to_owned(),
                },
            );
        }

        let parsed = catch_unwind(AssertUnwindSafe(|| parse_csv(csv, &declared, identity)))
            .unwrap_or_else(|_| panic!("case {name}: Rust panicked instead of returning {want:?}"));
        match parsed {
            Ok(_) => panic!("case {name}: Go rejected this CSV, Rust accepted it"),
            Err(err) => assert_eq!(err.to_string(), want, "case {name}"),
        }
    }
}

#[test]
fn nonfinite_projection_errors_match_go_and_partial_writes_stay_recorded() {
    let root = fixture();
    let cases = root["projection_error_cases"]
        .as_array()
        .expect("fixture must carry projection_error_cases");
    assert_eq!(cases.len(), 2, "the projection error corpus changed");

    for case in cases {
        let name = case["name"].as_str().unwrap_or_default();
        let csv = case["csv"].as_str().unwrap_or_default();
        let identity = case["identity_field"].as_str().unwrap_or_default();
        let declared = property_map(&case["declared"]).expect("declared schema");
        let (rows, _) = parse_csv(csv, &declared, identity)
            .unwrap_or_else(|err| panic!("case {name}: Go parsed nonfinite value: {err}"));
        let slug = case["source_name"]
            .as_str()
            .unwrap_or_default()
            .strip_suffix(".csv")
            .unwrap_or_default();
        let source_path = case["raw"]["path"].as_str().unwrap_or_default();
        let err = project_rows(slug, &rows, source_path)
            .expect_err("Go encoding/json rejects nonfinite float projection");
        assert_eq!(
            err.to_string(),
            case["projection_error"].as_str().unwrap_or_default(),
            "case {name}: direct projection error"
        );
        assert_eq!(
            case["error"].as_str().unwrap_or_default(),
            format!("store dataset rows: {err}"),
            "case {name}: service wrapper"
        );
        assert_eq!(case["sidecar_count"], 0, "case {name}: sidecar changed");
        for artifact in ["handle", "raw"] {
            assert!(
                !case[artifact]["path"]
                    .as_str()
                    .unwrap_or_default()
                    .is_empty()
                    && !case[artifact]["sha256"]
                        .as_str()
                        .unwrap_or_default()
                        .is_empty()
                    && case[artifact]["mode"].as_str().unwrap_or_default() == "0600",
                "case {name}: Go partial {artifact} write was not recorded"
            );
        }
    }
}

#[test]
fn corrupted_fixture_is_rejected_by_rust_replay() {
    let mut root = fixture();
    let case = root["csv_cases"][0].clone();
    validate_projection_case(&case, "oracle", "datasets/oracle/source.csv", "/schema")
        .expect("unmodified Go fixture must replay");
    root["csv_cases"][0]["sidecar_rows"][0]["values_json"] =
        Value::String("{\"corrupted\":true}".to_owned());
    let corrupted = &root["csv_cases"][0];
    assert!(
        validate_projection_case(corrupted, "oracle", "datasets/oracle/source.csv", "/schema")
            .is_err(),
        "Rust replay accepted a corrupted expected projection"
    );
}

/// The five generic DatasetImport rejections are ledger evidence only. The two
/// nonfinite cases above replay the parser/projection helper error but still do
/// not implement or certify `Service.DatasetSync` or DatasetImport filesystem
/// writes.
#[test]
fn service_level_rejections_are_recorded_not_replayed() {
    let root = fixture();
    let cases = root["error_cases"]
        .as_array()
        .expect("fixture must carry an error_cases array");
    assert_eq!(cases.len(), 5);
}
