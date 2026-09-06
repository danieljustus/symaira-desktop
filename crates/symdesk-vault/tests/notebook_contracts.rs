#![deny(unsafe_code)]

use noyalib as _;
use serde::Deserialize;
use serde_json::Value;
use symdesk_vault::parse_notebook;
use thiserror as _;
use unicode_general_category as _;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    cases: Vec<Case>,
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
fn typed_notebook_loader_matches_go() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../testdata/port/vault/notebook.json"))
            .expect("decode notebook fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 6);
    for case in fixture.cases {
        match parse_notebook(&case.path, case.input.as_bytes()) {
            Ok(value) => {
                assert!(case.ok, "{} unexpectedly parsed", case.name);
                assert_eq!(
                    serde_json::to_value(value).expect("serialize notebook"),
                    case.output.expect("Go notebook output"),
                    "{} notebook output",
                    case.name
                );
            }
            Err(error) => {
                assert!(!case.ok, "{} failed: {error}", case.name);
                if case.name != "malformed" {
                    assert_eq!(error.to_string(), case.error.expect("Go notebook error"));
                }
            }
        }
    }
}
