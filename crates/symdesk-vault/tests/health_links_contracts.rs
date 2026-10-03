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
    markdown_cases: Vec<MarkdownCase>,
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

#[derive(Deserialize)]
struct MarkdownCase {
    name: String,
    body: String,
    files: Vec<String>,
    targets: Vec<MarkdownTarget>,
}
#[derive(Debug, Deserialize, PartialEq)]
struct MarkdownTarget {
    destination: String,
    target: String,
    checked: bool,
    exists: bool,
}

#[test]
fn commonmark_attachment_health_matches_executable_go_cases() {
    use std::{
        fs,
        sync::atomic::{AtomicU64, Ordering},
    };
    use symdesk_vault::{MarkdownLinkResolver, extract_markdown_links, markdown_link_target};
    static SEQUENCE: AtomicU64 = AtomicU64::new(0);
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/vault/health-links.json"
    ))
    .expect("Go health fixture");
    assert_eq!(fixture.markdown_cases.len(), 28);
    for case in fixture.markdown_cases {
        let root = std::env::temp_dir().join(format!(
            "symdesk-markdown-links-{}-{}",
            std::process::id(),
            SEQUENCE.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&root).expect("atomically allocate fresh fixture vault");
        for file in &case.files {
            let path = root.join(file);
            fs::create_dir_all(path.parent().expect("fixture parent")).expect("create parent");
            fs::write(path, b"fixture").expect("create fixture file");
        }
        let resolver = MarkdownLinkResolver::new(&root).expect("vault inventory");
        let actual: Vec<_> = extract_markdown_links(&case.body)
            .into_iter()
            .map(|destination| {
                let target = markdown_link_target(&destination);
                MarkdownTarget {
                    checked: target.is_some(),
                    exists: resolver.check(&destination).unwrap_or(false),
                    target: target.unwrap_or_default(),
                    destination,
                }
            })
            .collect();
        assert_eq!(actual, case.targets, "{}", case.name);
        fs::remove_dir_all(root).expect("remove fixture vault");
    }
}

#[cfg(unix)]
#[test]
fn commonmark_attachment_symlinks_stay_within_the_vault() {
    use std::{fs, os::unix::fs::symlink};
    use symdesk_vault::MarkdownLinkResolver;
    let parent =
        std::env::temp_dir().join(format!("symdesk-markdown-confined-{}", std::process::id()));
    fs::create_dir(&parent).expect("fresh sandbox");
    let root = parent.join("vault");
    fs::create_dir(&root).expect("vault");
    fs::write(parent.join("outside.pdf"), b"outside").expect("outside fixture");
    fs::write(root.join("inside.pdf"), b"inside").expect("inside fixture");
    symlink("../outside.pdf", root.join("escape.pdf")).expect("outgoing symlink");
    symlink("inside.pdf", root.join("internal.pdf")).expect("internal symlink");
    symlink(&root, parent.join("alias")).expect("vault alias");
    for vault in [&root, &parent.join("alias")] {
        let resolver = MarkdownLinkResolver::new(vault).expect("inventory");
        assert_eq!(resolver.check("escape.pdf"), Some(false));
        assert_eq!(resolver.check("internal.pdf"), Some(true));
        assert_eq!(resolver.check("../outside.pdf"), Some(false));
    }
    fs::remove_dir_all(parent).expect("cleanup");
}
