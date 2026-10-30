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
    host_dependent_skipped: usize,
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
    // 1206 candidate cases minus 71 whose output starts with a colon element
    // (Windows `filepath.Clean` spells those with a leading `./`, so the
    // generator withholds them) and 22 order-sensitive `%%a`-style formats
    // that Go cannot answer deterministically.
    assert!(fixture.cases.eval.len() >= 1100, "recorded eval cases");
    assert!(
        fixture.host_dependent_skipped > 0,
        "the fixture must prove the Windows-dependent colon-led outputs were detected and withheld"
    );
    for case in &fixture.cases.eval {
        let first = case.output.split('/').next().unwrap_or_default();
        let volume = first.as_bytes().get(1) == Some(&b':');
        assert!(
            volume || !first.contains(':'),
            "{}: host-dependent output {:?} must not be recorded",
            case.id,
            case.output
        );
    }
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
