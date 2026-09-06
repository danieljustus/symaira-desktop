#![deny(unsafe_code)]

use std::collections::{BTreeMap, HashMap};

use noyalib::Value as YamlValue;
use serde::Deserialize;
use serde_json::Value as JsonValue;
use symdesk_vault::{
    Document, SearchMetadata, SearchMetadataField, format_search_metadata, metadata_matches,
    search_metadata_from_document, strip_search_metadata,
};
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
    document: Option<GoDocument>,
    #[serde(default)]
    fields: Vec<GoField>,
    formatted: String,
    queries: BTreeMap<String, Option<Vec<String>>>,
    stripped: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "PascalCase")]
struct GoField {
    name: String,
    value: String,
    weight: i32,
}

#[derive(Deserialize)]
#[serde(rename_all = "PascalCase")]
struct GoDocument {
    title: String,
    created: String,
    tags: Vec<String>,
    aliases: Vec<String>,
    frontmatter: BTreeMap<String, JsonValue>,
    document_date: String,
    person: String,
    status: String,
    due_date: String,
    #[serde(rename = "OcrJSONPath")]
    ocr_json_path: String,
    simhash: String,
    #[serde(rename = "ASN")]
    asn: Option<i64>,
    #[serde(rename = "Type")]
    document_type: String,
}

#[test]
fn hybrid_metadata_representation_matches_go() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../testdata/port/vault/metadata.json"))
            .expect("decode metadata fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 3);
    for case in fixture.cases {
        let metadata = if let Some(document) = case.document {
            search_metadata_from_document(&to_document(document))
        } else {
            SearchMetadata {
                fields: case.fields.iter().map(to_field).collect(),
            }
        };
        if !case.fields.is_empty() {
            assert_eq!(
                metadata.fields,
                case.fields.iter().map(to_field).collect::<Vec<_>>(),
                "{} fields",
                case.name
            );
        }
        let formatted = format_search_metadata(&metadata);
        assert_eq!(formatted, case.formatted, "{} formatted", case.name);
        let content = if formatted.is_empty() {
            "BODY".to_owned()
        } else {
            format!("{formatted}\nBODY")
        };
        for (query, expected) in case.queries {
            assert_eq!(
                metadata_matches(&query, &content),
                expected.unwrap_or_default(),
                "{} query {query}",
                case.name
            );
        }
        assert_eq!(
            strip_search_metadata(&content),
            case.stripped,
            "{} stripped",
            case.name
        );
    }
}

fn to_field(field: &GoField) -> SearchMetadataField {
    SearchMetadataField {
        name: field.name.clone(),
        value: field.value.clone(),
        weight: field.weight,
    }
}

fn to_document(input: GoDocument) -> Document {
    Document {
        path: String::new(),
        sha256: String::new(),
        title: input.title,
        created: input.created,
        tags: input.tags,
        aliases: input.aliases,
        frontmatter: input
            .frontmatter
            .into_iter()
            .map(|(key, value)| (key, json_to_yaml(value)))
            .collect(),
        yaml_timestamps: BTreeMap::new(),
        body: String::new(),
        links: Vec::new(),
        size: 0,
        document_date: input.document_date,
        person: input.person,
        status: input.status,
        due_date: input.due_date,
        confidence: 0,
        ocr_json_path: input.ocr_json_path,
        simhash: input.simhash,
        asn: input.asn,
        document_type: input.document_type,
        derived_from: String::new(),
        derived: false,
    }
}

fn json_to_yaml(value: JsonValue) -> YamlValue {
    match value {
        JsonValue::Null => YamlValue::Null,
        JsonValue::Bool(value) => YamlValue::Bool(value),
        JsonValue::Number(value) => {
            if let Some(integer) = value.as_i64() {
                YamlValue::from(integer)
            } else {
                YamlValue::from(value.as_f64().expect("JSON number"))
            }
        }
        JsonValue::String(value) => YamlValue::String(value),
        JsonValue::Array(values) => {
            YamlValue::Sequence(values.into_iter().map(json_to_yaml).collect())
        }
        JsonValue::Object(values) => {
            let map: HashMap<String, JsonValue> = values.into_iter().collect();
            let encoded = serde_json::to_vec(&map).expect("encode map");
            noyalib::from_slice(&encoded).expect("decode JSON-compatible YAML")
        }
    }
}
