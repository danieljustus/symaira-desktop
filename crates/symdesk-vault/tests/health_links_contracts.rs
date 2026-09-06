#![deny(unsafe_code)]

use noyalib as _;
use serde::Deserialize;
use symdesk_vault::{HealthLinkResolver, LinkInventory, normalize_health_link_target};
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
    raw: String,
    inventory: Inventory,
    normalized: String,
    checked: bool,
    exists: bool,
}

#[derive(Deserialize)]
struct Inventory {
    paths: Vec<String>,
    titles: Vec<String>,
    aliases: Vec<String>,
    attachments: Vec<String>,
}

#[test]
fn health_link_and_attachment_resolution_matches_go() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/vault/health-links.json"
    ))
    .expect("decode health link fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 18);
    for case in fixture.cases {
        let normalized = normalize_health_link_target(&case.raw);
        assert_eq!(
            normalized.as_deref().unwrap_or(""),
            case.normalized,
            "{} normalization",
            case.name
        );
        assert_eq!(normalized.is_some(), case.checked, "{} checked", case.name);
        let resolver = HealthLinkResolver::new(&LinkInventory {
            paths: case.inventory.paths,
            titles: case.inventory.titles,
            aliases: case.inventory.aliases,
            attachments: case.inventory.attachments,
        });
        assert_eq!(
            resolver.check(&case.raw),
            case.checked.then_some(case.exists),
            "{} existence",
            case.name
        );
    }
}
