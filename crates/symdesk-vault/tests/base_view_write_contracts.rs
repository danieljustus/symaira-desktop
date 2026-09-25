#![deny(unsafe_code)]

use std::{fs, path::PathBuf};

use serde::Deserialize;
use serde_json::Value;
use symdesk_vault::{
    Base, View, delete_base, delete_base_with_snapshot, delete_view, delete_view_with_snapshot,
    parse_base, save_base, save_base_with_snapshot, save_view, save_view_with_snapshot,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    steps: Vec<Step>,
    migration_cases: Vec<MigrationCase>,
    snapshot_cases: Vec<SnapshotCase>,
}

#[derive(Deserialize)]
struct SnapshotEvent {
    path: String,
    exists: bool,
    markdown: Option<String>,
}

#[derive(Deserialize)]
struct SnapshotCase {
    id: String,
    operation: String,
    existing_base: Option<Base>,
    base: Option<Base>,
    view: Option<View>,
    reference: Option<String>,
    events: Vec<String>,
    snapshots: Vec<SnapshotEvent>,
    exists: bool,
    markdown: Option<String>,
    error: bool,
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
    #[cfg(unix)]
    legacy_mode: u32,
    #[cfg(unix)]
    symdesk_mode: u32,
    bases_dir_exists: bool,
    #[cfg(unix)]
    bases_dir_mode: u32,
    #[cfg(unix)]
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
    #[cfg(unix)]
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
        #[cfg(unix)]
        let actual_modes = actual_bases
            .iter()
            .map(|path| {
                use std::os::unix::fs::PermissionsExt;
                fs::metadata(path)
                    .expect("base metadata")
                    .permissions()
                    .mode()
                    & 0o777
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
        actual.sort_by_key(|base| base.title.to_lowercase());
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

    assert_eq!(fixture.snapshot_cases.len(), 6);
    for case in fixture.snapshot_cases {
        let root = temp_vault();
        if let Some(mut base) = case.existing_base {
            save_base(&root, &mut base).expect("seed snapshot case base");
        }
        let mut events = Vec::new();
        let mut snapshots = Vec::new();
        let mut snapshot_fn = |path: &std::path::Path| {
            events.push("snapshot".to_owned());
            let relative = path
                .strip_prefix(&root)
                .expect("snapshot path under vault root");
            let relative = relative.to_string_lossy().replace('\\', "/");
            let (exists, markdown) = match fs::read_to_string(path) {
                Ok(markdown) => (true, Some(markdown)),
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => (false, None),
                Err(error) => panic!("read pre-write snapshot: {error}"),
            };
            snapshots.push((relative, exists, markdown));
        };
        let result = match case.operation.as_str() {
            "create_base" | "save_base" => save_base_with_snapshot(
                &root,
                &mut case.base.expect("base snapshot input"),
                &mut snapshot_fn,
            ),
            "save_view" => save_view_with_snapshot(
                &root,
                case.view.expect("view snapshot input"),
                &mut snapshot_fn,
            ),
            "delete_view" => delete_view_with_snapshot(
                &root,
                case.reference.as_deref().expect("view snapshot reference"),
                &mut snapshot_fn,
            ),
            "delete_base" => delete_base_with_snapshot(
                &root,
                case.reference.as_deref().expect("base snapshot reference"),
                &mut snapshot_fn,
            ),
            "missing_delete_view" => delete_view_with_snapshot(&root, "missing", &mut snapshot_fn),
            other => panic!("unknown snapshot operation {other}"),
        };
        events.push("operation".to_owned());
        assert_eq!(result.is_err(), case.error, "case {} error", case.id);
        assert_eq!(events, case.events, "case {} event order", case.id);
        assert_eq!(
            snapshots.len(),
            case.snapshots.len(),
            "case {} snapshots",
            case.id
        );
        for (actual, expected) in snapshots.iter().zip(&case.snapshots) {
            assert_eq!(actual.0, expected.path, "case {} callback path", case.id);
            assert_eq!(
                actual.1, expected.exists,
                "case {} pre-write exists",
                case.id
            );
            assert_eq!(
                actual.2.as_deref(),
                expected.markdown.as_deref(),
                "case {} pre-write bytes",
                case.id
            );
        }
        let path = root.join("bases/invoices.md");
        assert_eq!(path.exists(), case.exists, "case {} final exists", case.id);
        if case.exists {
            assert_eq!(
                fs::read_to_string(path).expect("read final snapshot case base"),
                case.markdown.expect("Go final Markdown"),
                "case {} final Markdown",
                case.id
            );
        }
        fs::remove_dir_all(root).expect("remove snapshot test vault");
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
