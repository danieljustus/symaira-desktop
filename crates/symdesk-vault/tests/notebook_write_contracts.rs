#![deny(unsafe_code)]

use std::{fs, path::PathBuf};

use serde::Deserialize;
use serde_json::Value;
use symdesk_vault::{
    NotebookWriteError, add_notebook_source, new_notebook, new_notebook_with_query,
    remove_notebook_source,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    path: String,
    initial: String,
    steps: Vec<Step>,
    creations: Vec<Creation>,
    rejected: Vec<Rejected>,
}

#[derive(Deserialize)]
struct Step {
    operation: String,
    source: String,
    output: Value,
    markdown: String,
    unix_mode: u32,
}

#[derive(Deserialize)]
struct Creation {
    name: String,
    operation: String,
    title: String,
    description: String,
    query: Option<String>,
    existing: Option<Vec<String>>,
    output: Value,
    markdown: String,
    unix_mode: u32,
    notebooks_dir_mode: u32,
}

#[derive(Deserialize)]
struct Rejected {
    name: String,
    operation: String,
    title: String,
    root: Option<String>,
    error: String,
}

#[test]
fn notebook_source_writes_match_go() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/vault/notebook-write.json"
    ))
    .expect("decode Go notebook write fixture");
    assert_eq!(fixture.schema_version, 2);

    let root = temp_vault();
    let path = root.join(&fixture.path);
    fs::create_dir_all(path.parent().expect("notebook parent")).expect("create notebook directory");
    fs::write(&path, fixture.initial).expect("write notebook fixture");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&path, fs::Permissions::from_mode(0o600))
            .expect("set Go fixture file mode");
    }

    for step in fixture.steps {
        let got = match step.operation.as_str() {
            "add" => add_notebook_source(&root, &fixture.path, &step.source),
            "remove" => remove_notebook_source(&root, &fixture.path, &step.source),
            other => panic!("unknown Go operation {other}"),
        }
        .expect("Rust notebook write");
        assert_eq!(
            serde_json::to_value(got).expect("serialize notebook"),
            step.output
        );
        assert_eq!(
            fs::read_to_string(&path).expect("read rendered notebook"),
            step.markdown
        );
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(&path)
                    .expect("notebook metadata")
                    .permissions()
                    .mode()
                    & 0o777,
                step.unix_mode
            );
        }
    }

    fs::remove_dir_all(root).expect("remove test vault");
}

#[test]
fn notebook_creation_matches_go() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/vault/notebook-write.json"
    ))
    .expect("decode Go notebook creation fixture");
    for case in fixture.creations {
        let root = temp_vault();
        if let Some(existing) = &case.existing {
            let dir = root.join("notebooks");
            fs::create_dir_all(&dir).expect("create existing notebook directory");
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                fs::set_permissions(&dir, fs::Permissions::from_mode(case.notebooks_dir_mode))
                    .expect("set Go notebook directory mode");
            }
            for name in existing {
                fs::write(dir.join(name), "occupied").expect("write colliding notebook");
            }
        }
        let got = match case.operation.as_str() {
            "new" => new_notebook(&root, &case.title, &case.description),
            "new_with_query" => new_notebook_with_query(
                &root,
                &case.title,
                &case.description,
                case.query.as_deref().unwrap_or_default(),
            ),
            other => panic!("unknown Go operation {other} in {}", case.name),
        }
        .unwrap_or_else(|error| panic!("Rust creation {}: {error}", case.name));

        let oracle_created = case.output["created"]
            .as_str()
            .expect("Go creation timestamp")
            .to_owned();
        let mut expected = case.output;
        expected["created"] = serde_json::Value::String(got.created.clone());
        let mut actual = serde_json::to_value(&got).expect("serialize notebook");
        actual["path"] = serde_json::Value::String(got.path.replace('\\', "/"));
        assert_eq!(actual, expected);
        assert!(
            time::OffsetDateTime::parse(
                &got.created,
                &time::format_description::well_known::Rfc3339
            )
            .is_ok(),
            "invalid creation timestamp in {}",
            case.name
        );

        let path = root.join(&got.path);
        let markdown = case.markdown.replace(&oracle_created, &got.created);
        assert_eq!(
            fs::read_to_string(&path).expect("read created notebook"),
            markdown
        );
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(&path)
                    .expect("created notebook metadata")
                    .permissions()
                    .mode()
                    & 0o777,
                case.unix_mode,
                "file mode in {}",
                case.name
            );
            assert_eq!(
                fs::metadata(root.join("notebooks"))
                    .expect("notebook directory metadata")
                    .permissions()
                    .mode()
                    & 0o777,
                case.notebooks_dir_mode,
                "directory mode in {}",
                case.name
            );
        }
        if let Some(existing) = case.existing {
            for name in existing {
                assert_eq!(
                    fs::read_to_string(root.join("notebooks").join(name))
                        .expect("read collision sentinel"),
                    "occupied"
                );
            }
        }
        fs::remove_dir_all(root).expect("remove test vault");
    }

    for rejected in fixture.rejected {
        assert!(
            !rejected.error.is_empty(),
            "Go rejection {} had no error",
            rejected.name
        );
        let root = temp_vault();
        let root = rejected
            .root
            .as_deref()
            .map_or_else(|| root.clone(), |relative| root.join(relative));
        let result = match rejected.operation.as_str() {
            "new" => new_notebook(&root, &rejected.title, ""),
            other => panic!(
                "unknown Go rejection operation {other} in {}",
                rejected.name
            ),
        };
        let error = result.expect_err("Rust accepted a rejected Go creation case");
        if rejected.root.is_some() {
            assert!(
                matches!(&error, NotebookWriteError::Path(_)),
                "invalid vault root returned the wrong error: {error}"
            );
        } else {
            assert_eq!(error.to_string(), rejected.error);
        }
        let cleanup_root = rejected
            .root
            .as_deref()
            .map_or_else(|| root.clone(), |_| root.parent().unwrap().to_path_buf());
        fs::remove_dir_all(cleanup_root).expect("remove rejected test vault");
    }
}

#[test]
fn notebook_source_write_rejects_self_and_escape() {
    let root = temp_vault();
    let path = root.join("notebooks/research.md");
    fs::create_dir_all(path.parent().expect("notebook parent")).expect("create notebook directory");
    fs::write(&path, "---\ntype: notebook\ntitle: Research\ncreated: 2026-01-02T03:04:05Z\nnotebook_id: research\nsources: []\n---\n").expect("write notebook");

    assert!(add_notebook_source(&root, "notebooks/research.md", "notebooks/research.md").is_err());
    assert!(add_notebook_source(&root, "notebooks/research.md", "../../outside.md").is_err());
    fs::remove_dir_all(root).expect("remove test vault");
}

fn temp_vault() -> PathBuf {
    let path = std::env::temp_dir().join(format!(
        "symdesk-notebook-write-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock after epoch")
            .as_nanos()
    ));
    fs::create_dir_all(&path).expect("create temporary vault");
    path
}
