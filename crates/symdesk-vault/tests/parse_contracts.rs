#![deny(unsafe_code)]

use std::collections::BTreeMap;

use noyalib::Value;
use serde::Deserialize;
use symdesk_vault::{Document, VaultError, parse_bytes};
use thiserror as _;
use unicode_general_category as _;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    input: Input,
    document: Option<ExpectedDocument>,
    #[serde(default)]
    error_class: String,
}

#[derive(Deserialize)]
struct Input {
    #[serde(rename = "id")]
    _id: String,
    path: String,
    content: String,
}

#[derive(Deserialize)]
struct ExpectedDocument {
    path: String,
    sha256: String,
    title: String,
    created: String,
    #[serde(default)]
    tags: Vec<String>,
    #[serde(default)]
    aliases: Vec<String>,
    frontmatter: BTreeMap<String, Canonical>,
    body: String,
    #[serde(default)]
    links: Vec<String>,
    size: i64,
    document_date: String,
    person: String,
    status: String,
    due_date: String,
    confidence: i64,
    ocr_json_path: String,
    simhash: String,
    asn: Option<i64>,
    #[serde(rename = "type")]
    document_type: String,
    derived_from: String,
    derived: bool,
}

#[derive(Clone, Debug, Deserialize, PartialEq)]
struct Canonical {
    #[serde(rename = "type")]
    kind: String,
    value: Option<serde_json::Value>,
    #[serde(default)]
    items: Vec<Canonical>,
    #[serde(default, rename = "map")]
    mapping: BTreeMap<String, Canonical>,
}

#[test]
fn parse_bytes_matches_go_vault_fixture() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../testdata/port/vault/parse.json"))
            .expect("decode vault fixture");
    assert_eq!(fixture.schema_version, 1);
    for case in fixture.cases {
        match (
            parse_bytes(&case.input.path, case.input.content.as_bytes()),
            case.document,
        ) {
            (Ok(actual), Some(expected)) => assert_document(&actual, &expected),
            (Err(error), None) => assert_eq!(error_class(&error), case.error_class),
            (Ok(_), None) => panic!("Rust accepted Go-rejected case {}", case.input._id),
            (Err(error), Some(_)) => {
                panic!("Rust rejected Go-accepted case {}: {error}", case.input._id)
            }
        }
    }
}

fn assert_document(actual: &Document, expected: &ExpectedDocument) {
    assert_eq!(actual.path, expected.path);
    assert_eq!(actual.sha256, expected.sha256);
    assert_eq!(actual.title, expected.title);
    assert_eq!(actual.created, expected.created);
    assert_eq!(actual.tags, expected.tags);
    assert_eq!(actual.aliases, expected.aliases);
    assert_eq!(canonical_frontmatter(actual), expected.frontmatter);
    assert_eq!(actual.body, expected.body);
    assert_eq!(actual.links, expected.links);
    assert_eq!(actual.size, expected.size);
    assert_eq!(actual.document_date, expected.document_date);
    assert_eq!(actual.person, expected.person);
    assert_eq!(actual.status, expected.status);
    assert_eq!(actual.due_date, expected.due_date);
    assert_eq!(actual.confidence, expected.confidence);
    assert_eq!(actual.ocr_json_path, expected.ocr_json_path);
    assert_eq!(actual.simhash, expected.simhash);
    assert_eq!(actual.asn, expected.asn);
    assert_eq!(actual.document_type, expected.document_type);
    assert_eq!(actual.derived_from, expected.derived_from);
    assert_eq!(actual.derived, expected.derived);
}

fn error_class(error: &VaultError) -> &'static str {
    match error {
        VaultError::Frontmatter { .. } => "frontmatter",
        VaultError::Asn { .. } => "asn",
    }
}

fn canonical_frontmatter(document: &Document) -> BTreeMap<String, Canonical> {
    document
        .frontmatter
        .iter()
        .map(|(key, value)| {
            if let Some(timestamp) = document.yaml_timestamps.get(key) {
                (key.clone(), scalar("time", Some(timestamp.clone().into())))
            } else {
                (key.clone(), canonical(value))
            }
        })
        .collect()
}

fn canonical(value: &Value) -> Canonical {
    match value {
        Value::Null => scalar("null", None),
        Value::Bool(value) => scalar("bool", Some((*value).into())),
        Value::Number(value) => {
            if let Some(number) = value.as_i64() {
                scalar("int", Some(number.into()))
            } else if let Some(number) = value.as_u64() {
                scalar("int", Some(number.into()))
            } else {
                scalar(
                    "float",
                    Some(
                        serde_json::Number::from_f64(value.as_f64())
                            .expect("finite YAML number")
                            .into(),
                    ),
                )
            }
        }
        Value::String(value) => scalar("string", Some(value.clone().into())),
        Value::Sequence(values) => Canonical {
            kind: "list".to_owned(),
            value: None,
            items: values.iter().map(canonical).collect(),
            mapping: BTreeMap::new(),
        },
        Value::Mapping(values) => Canonical {
            kind: "map".to_owned(),
            value: None,
            items: Vec::new(),
            mapping: values
                .iter()
                .map(|(key, value)| (key.as_str().to_owned(), canonical(value)))
                .collect(),
        },
        Value::Tagged(value) => canonical(value.value()),
    }
}

fn scalar(kind: &str, value: Option<serde_json::Value>) -> Canonical {
    Canonical {
        kind: kind.to_owned(),
        value,
        items: Vec::new(),
        mapping: BTreeMap::new(),
    }
}
