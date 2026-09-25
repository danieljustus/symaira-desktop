#![deny(unsafe_code)]

use std::{fs, path::PathBuf};

use serde::Deserialize;
use serde_json::Value;
use symdesk_vault::{Base, View, delete_base, delete_view, parse_base, save_base, save_view};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    steps: Vec<Step>,
}

#[derive(Deserialize)]
struct Step {
    operation: String,
    base: Option<Base>,
    view: Option<View>,
    reference: Option<String>,
    output: Option<Value>,
    markdown: Option<String>,
    exists: bool,
    unix_mode: u32,
}

#[test]
fn base_and_view_file_writes_match_go() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/vault/base-view-write.json"
    ))
    .expect("decode Go base/view fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.steps.len(),
        5,
        "Go fixture must cover every file verb"
    );

    let root = temp_vault();
    let path = root.join("bases/invoices.md");
    for step in fixture.steps {
        match step.operation.as_str() {
            "save_base" => {
                save_base(&root, &mut step.base.expect("base input")).expect("Rust SaveBase")
            }
            "save_view" => {
                save_view(&root, step.view.expect("view input")).expect("Rust view save")
            }
            "delete_view" => delete_view(&root, step.reference.as_deref().expect("view reference"))
                .expect("Rust view delete"),
            "delete_base" => delete_base(&root, step.reference.as_deref().expect("base reference"))
                .expect("Rust base delete"),
            other => panic!("unknown Go operation {other}"),
        }

        assert_eq!(path.exists(), step.exists, "{} exists", step.operation);
        if step.exists {
            assert_eq!(
                fs::read_to_string(&path).expect("read Rust base note"),
                step.markdown.expect("Go Markdown oracle"),
                "{} Markdown",
                step.operation
            );
            let parsed = parse_base(
                "bases/invoices.md",
                &fs::read(&path).expect("read Rust base note"),
            )
            .expect("reopen Rust base note");
            assert_eq!(
                serde_json::to_value(parsed).expect("serialize reopened base"),
                step.output.expect("Go reopened base"),
                "{} reopened base",
                step.operation
            );
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                assert_eq!(
                    fs::metadata(&path)
                        .expect("base metadata")
                        .permissions()
                        .mode()
                        & 0o777,
                    step.unix_mode
                );
            }
        }
    }
    fs::remove_dir_all(root).expect("remove test vault");
}

fn temp_vault() -> PathBuf {
    let path = std::env::temp_dir().join(format!(
        "symdesk-base-write-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock after epoch")
            .as_nanos()
    ));
    fs::create_dir_all(&path).expect("create temporary vault");
    path
}
