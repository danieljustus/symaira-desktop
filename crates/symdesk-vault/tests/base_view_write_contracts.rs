#![deny(unsafe_code)]

use std::{fs, path::PathBuf};

use serde::Deserialize;
use serde_json::Value;
use symdesk_vault::{Base, View, delete_base, delete_view, parse_base, save_base, save_view};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    steps: Vec<Step>,
    migration_cases: Vec<MigrationCase>,
}

#[derive(Deserialize)]
struct MigrationCase {
    id: String,
    legacy_json: String,
    existing_bases: Vec<Base>,
    expected_bases: Vec<Base>,
    expected_created: Vec<bool>,
    legacy_exists: bool,
    legacy_preserved: bool,
    legacy_mode: u32,
    symdesk_mode: u32,
    bases_dir_exists: bool,
    bases_dir_mode: u32,
    base_modes: Vec<u32>,
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

    assert_eq!(fixture.migration_cases.len(), 4);
    for case in fixture.migration_cases {
        let root = temp_vault();
        for mut base in case.existing_bases {
            save_base(&root, &mut base).expect("seed existing base note");
        }
        let legacy = root.join(".symdesk/views.json");
        fs::create_dir_all(legacy.parent().expect("legacy parent"))
            .expect("create legacy directory");
        fs::write(&legacy, case.legacy_json.as_bytes()).expect("write legacy views");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(
                legacy.parent().expect("legacy parent"),
                fs::Permissions::from_mode(0o700),
            )
            .expect("set legacy directory mode");
            fs::set_permissions(&legacy, fs::Permissions::from_mode(0o600))
                .expect("set legacy file mode");
        }

        let error = delete_view(&root, "migration-probe").expect_err("missing probe view");
        assert_eq!(error.to_string(), "view not found", "case {}", case.id);
        assert_eq!(
            legacy.exists(),
            case.legacy_exists,
            "case {} legacy exists",
            case.id
        );
        if case.legacy_exists {
            assert_eq!(
                fs::read(&legacy).expect("read legacy views"),
                case.legacy_json.as_bytes()
            );
            assert!(case.legacy_preserved, "case {} legacy bytes", case.id);
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                assert_eq!(
                    fs::metadata(&legacy)
                        .expect("legacy metadata")
                        .permissions()
                        .mode()
                        & 0o777,
                    case.legacy_mode
                );
                assert_eq!(
                    fs::metadata(legacy.parent().expect("legacy parent"))
                        .expect("legacy directory metadata")
                        .permissions()
                        .mode()
                        & 0o777,
                    case.symdesk_mode
                );
            }
        }

        let bases_dir = root.join("bases");
        assert_eq!(
            bases_dir.exists(),
            case.bases_dir_exists,
            "case {} bases dir",
            case.id
        );
        if case.bases_dir_exists {
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                assert_eq!(
                    fs::metadata(&bases_dir)
                        .expect("bases directory metadata")
                        .permissions()
                        .mode()
                        & 0o777,
                    case.bases_dir_mode
                );
            }
        }
        let mut actual_bases = fs::read_dir(&bases_dir)
            .ok()
            .into_iter()
            .flatten()
            .map(|entry| entry.expect("base directory entry").path())
            .filter(|path| path.extension().and_then(|value| value.to_str()) == Some("md"))
            .collect::<Vec<_>>();
        actual_bases.sort();
        let actual_modes = actual_bases
            .iter()
            .map(|path| {
                #[cfg(unix)]
                {
                    use std::os::unix::fs::PermissionsExt;
                    fs::metadata(path)
                        .expect("base metadata")
                        .permissions()
                        .mode()
                        & 0o777
                }
                #[cfg(not(unix))]
                {
                    0
                }
            })
            .collect::<Vec<_>>();
        let mut actual = actual_bases
            .iter()
            .map(|path| {
                let relative = path.strip_prefix(&root).expect("base under vault root");
                let relative = relative.to_string_lossy().replace('\\', "/");
                parse_base(&relative, &fs::read(path).expect("read migrated base"))
                    .expect("parse migrated base")
            })
            .collect::<Vec<_>>();
        actual.sort_by(|left, right| left.title.to_lowercase().cmp(&right.title.to_lowercase()));
        for (base, has_created) in actual.iter_mut().zip(&case.expected_created) {
            assert!(
                !base.created.is_empty() == *has_created,
                "case {} created timestamp",
                case.id
            );
            if *has_created {
                time::OffsetDateTime::parse(
                    &base.created,
                    &time::format_description::well_known::Rfc3339,
                )
                .expect("migration timestamp is RFC3339");
            }
            base.created.clear();
        }
        assert_eq!(
            actual, case.expected_bases,
            "case {} migrated bases",
            case.id
        );
        #[cfg(unix)]
        assert_eq!(actual_modes, case.base_modes, "case {} base modes", case.id);
        fs::remove_dir_all(root).expect("remove test vault");
    }
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
