#![deny(unsafe_code)]

//! Replays the Go-generated `testdata/port/core/template-path.json`
//! (`scripts/rust-port/cmd/templatepathgen`) against
//! `symdesk_core::template_path`.

use std::collections::{BTreeMap, HashSet};

use regex as _;
use serde::Deserialize;
use symdesk_core::template_path::{Template, disambiguate};
use time as _;
use toml as _;

const FIXTURE: &str = include_str!("../../../testdata/port/core/template-path.json");

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    nondeterministic_skipped: usize,
    cases: Cases,
}

#[derive(Deserialize)]
struct Cases {
    compile: Vec<CompileCase>,
    eval: Vec<EvalCase>,
    disambiguate: Vec<DisambiguateCase>,
}

#[derive(Deserialize)]
struct CompileCase {
    id: String,
    raw: String,
    #[serde(default)]
    error: Option<String>,
    raw_out: String,
    is_empty: bool,
}

#[derive(Deserialize)]
struct EvalCase {
    id: String,
    template: String,
    fields: BTreeMap<String, String>,
    output: String,
}

#[derive(Deserialize)]
struct DisambiguateCase {
    id: String,
    target: String,
    known: Vec<String>,
    output: String,
}

fn fixture() -> Fixture {
    let fixture: Fixture = serde_json::from_str(FIXTURE).expect("decode template-path fixture");
    assert_eq!(fixture.schema_version, 1);
    fixture
}

#[test]
fn compile_matches_go() {
    let fixture = fixture();
    let mut errors = 0;
    for case in &fixture.cases.compile {
        match Template::compile(&case.raw) {
            Ok(template) => {
                assert!(
                    case.error.is_none(),
                    "{}: Go rejected, Rust accepted",
                    case.id
                );
                assert_eq!(template.raw(), case.raw_out, "{}", case.id);
                assert_eq!(template.is_empty(), case.is_empty, "{}", case.id);
            }
            Err(error) => {
                errors += 1;
                assert_eq!(
                    Some(error.to_string()),
                    case.error,
                    "{}: error text",
                    case.id
                );
            }
        }
    }
    assert_eq!(fixture.cases.compile.len(), 82, "recorded compile cases");
    assert!(errors >= 15, "fixture must exercise rejection paths");
}

#[test]
fn eval_matches_go() {
    let fixture = fixture();
    for case in &fixture.cases.eval {
        let template = Template::compile(&case.template)
            .unwrap_or_else(|error| panic!("{}: {error}", case.id));
        assert_eq!(template.eval(&case.fields), case.output, "{}", case.id);
    }
    // 1154 base cases plus the order-sensitive `%%a`-style formats that Go
    // cannot answer deterministically and the generator withholds.
    assert!(fixture.cases.eval.len() >= 1150, "recorded eval cases");
    assert!(
        fixture.nondeterministic_skipped > 0,
        "the fixture must prove Go's map-order strftime cases were detected and withheld"
    );
}

#[test]
fn disambiguate_matches_go() {
    let fixture = fixture();
    for case in &fixture.cases.disambiguate {
        let known: HashSet<String> = case.known.iter().cloned().collect();
        assert_eq!(
            disambiguate(&case.target, &known),
            case.output,
            "{}",
            case.id
        );
    }
    assert_eq!(
        fixture.cases.disambiguate.len(),
        24,
        "recorded disambiguate cases"
    );
}
