#![deny(unsafe_code)]

use noyalib as _;
use serde::Deserialize;
use serde_json::Value;
use symdesk_vault::{parse_base, parse_dataset_handle};
use thiserror as _;
use unicode_general_category as _;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    base_cases: Vec<Case>,
    dataset_cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    path: String,
    input: String,
    ok: bool,
    output: Option<Value>,
    error: Option<String>,
}

#[test]
fn typed_base_and_dataset_loaders_match_go() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../testdata/port/vault/typed.json"))
            .expect("decode typed vault fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.base_cases.len(), 5);
    assert_eq!(fixture.dataset_cases.len(), 10);

    for case in fixture.base_cases {
        match parse_base(&case.path, case.input.as_bytes()) {
            Ok(value) => {
                assert!(case.ok, "{} unexpectedly parsed", case.name);
                assert_eq!(
                    serde_json::to_value(value).expect("serialize base"),
                    case.output.expect("Go base output"),
                    "{} base output",
                    case.name
                );
            }
            Err(error) => {
                assert!(!case.ok, "{} failed: {error}", case.name);
                if case.name != "malformed-yaml" {
                    assert_eq!(error.to_string(), case.error.expect("Go base error"));
                }
            }
        }
    }

    for case in fixture.dataset_cases {
        match parse_dataset_handle(&case.path, case.input.as_bytes()) {
            Ok(value) => {
                assert!(case.ok, "{} unexpectedly parsed", case.name);
                assert_eq!(
                    serde_json::to_value(value).expect("serialize dataset"),
                    case.output.expect("Go dataset output"),
                    "{} dataset output",
                    case.name
                );
            }
            Err(error) => {
                assert!(!case.ok, "{} failed: {error}", case.name);
                assert_eq!(error.to_string(), case.error.expect("Go dataset error"));
            }
        }
    }
}
