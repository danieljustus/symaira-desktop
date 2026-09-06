#![deny(unsafe_code)]

use noyalib as _;
use serde::Deserialize;
use thiserror as _;
use unicode_general_category as _;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    filename: String,
    document: String,
}

#[test]
fn actual_swift_mobile_writer_output_parses_in_rust() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/vault/mobile-writer.json"
    ))
    .expect("decode Swift mobile fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.filename, "Käse___日本.md");
    let document = symdesk_vault::parse_bytes(&fixture.filename, fixture.document.as_bytes())
        .expect("parse Swift writer output");
    assert_eq!(document.title, "Käse \"Crème\" \\ 日本");
    assert_eq!(document.created, "2025-07-15T17:20:00Z");
    assert!(document.tags.is_empty());
    assert_eq!(document.body, "\n## Einkauf\n\nMilch und 日本語");
}
