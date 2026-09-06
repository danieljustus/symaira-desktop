#![deny(unsafe_code)]

use regex as _;
use serde::Deserialize;
use symdesk_core::query;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use toml as _;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    cases: Cases,
}

#[derive(Deserialize)]
struct Cases {
    queries: Vec<QueryCase>,
    dates: Vec<DateCase>,
}

#[derive(Deserialize)]
struct QueryCase {
    #[serde(rename = "id")]
    _id: String,
    input: String,
    #[serde(default)]
    filters: Vec<Filter>,
    #[serde(default)]
    terms: Vec<Term>,
    #[serde(default)]
    regexes: Vec<RegexCase>,
    #[serde(default)]
    requires_sidecar: bool,
    #[serde(default)]
    error: String,
    #[serde(default)]
    error_class: String,
}

#[derive(Deserialize)]
struct Filter {
    field: String,
    value: String,
    #[serde(default)]
    negated: bool,
}

#[derive(Deserialize)]
struct Term {
    value: String,
    #[serde(default)]
    phrase: bool,
    #[serde(default)]
    negated: bool,
}

#[derive(Deserialize)]
struct RegexCase {
    pattern: String,
    #[serde(default)]
    negated: bool,
    probe: String,
    probe_matches: bool,
}

#[derive(Deserialize)]
struct DateCase {
    #[serde(rename = "id")]
    _id: String,
    value: String,
    reference: String,
    from_unix_nanos: Option<i64>,
    to_unix_nanos: Option<i64>,
    #[serde(default)]
    error: String,
    #[serde(default)]
    validate_error: String,
}

#[test]
fn query_parser_matches_go_fixture() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/core/search-query.json"
    ))
    .expect("decode query fixture");
    assert_eq!(fixture.schema_version, 1);
    for case in fixture.cases.queries {
        match query::parse(&case.input) {
            Ok(plan) => {
                assert!(case.error.is_empty(), "Go rejected {:?}", case.input);
                assert_eq!(plan.filters.len(), case.filters.len());
                for (actual, expected) in plan.filters.iter().zip(case.filters) {
                    assert_eq!(actual.field.as_str(), expected.field);
                    assert_eq!(actual.value, expected.value);
                    assert_eq!(actual.negated, expected.negated);
                }
                assert_eq!(plan.terms.len(), case.terms.len());
                for (actual, expected) in plan.terms.iter().zip(case.terms) {
                    assert_eq!(actual.value, expected.value);
                    assert_eq!(actual.phrase, expected.phrase);
                    assert_eq!(actual.negated, expected.negated);
                }
                assert_eq!(plan.regexes.len(), case.regexes.len());
                for (actual, expected) in plan.regexes.iter().zip(case.regexes) {
                    assert_eq!(actual.pattern, expected.pattern);
                    assert_eq!(actual.negated, expected.negated);
                    assert_eq!(actual.matches(&expected.probe), expected.probe_matches);
                }
                assert_eq!(plan.requires_sidecar(), case.requires_sidecar);
            }
            Err(error) => {
                assert!(
                    !case.error.is_empty(),
                    "Rust rejected {:?}: {error}",
                    case.input
                );
                assert_eq!(error.class, case.error_class);
                if error.class != "invalid regular expression" {
                    assert_eq!(error.message, case.error);
                }
            }
        }
    }
}

#[test]
fn date_parser_matches_go_fixture() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/core/search-query.json"
    ))
    .expect("decode query fixture");
    for case in fixture.cases.dates {
        let reference =
            OffsetDateTime::parse(&case.reference, &Rfc3339).expect("fixture reference is RFC3339");
        match query::parse_date_value(&case.value, reference) {
            Ok(actual) => {
                assert!(case.error.is_empty(), "Go rejected {:?}", case.value);
                assert_eq!(
                    actual.from.unix_timestamp_nanos(),
                    i128::from(case.from_unix_nanos.expect("fixture from"))
                );
                assert_eq!(
                    actual.to.unix_timestamp_nanos(),
                    i128::from(case.to_unix_nanos.expect("fixture to"))
                );
            }
            Err(error) => assert_eq!(error.message, case.error),
        }
        let validation = query::validate_date_value(&case.value)
            .err()
            .map_or_else(String::new, |error| error.message);
        assert_eq!(validation, case.validate_error);
    }
}
