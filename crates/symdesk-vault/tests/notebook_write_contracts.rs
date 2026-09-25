#![deny(unsafe_code)]

use std::{fs, path::PathBuf};

use serde::Deserialize;
use serde_json::Value;
use symdesk_vault::{Notebook, add_notebook_source, remove_notebook_source};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    path: String,
    initial: String,
    steps: Vec<Step>,
}

#[derive(Deserialize)]
struct Step {
    operation: String,
    source: String,
    output: Value,
    markdown: String,
    unix_mode: u32,
}

#[test]
fn notebook_source_writes_match_go() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/vault/notebook-write.json"
    ))
    .expect("decode Go notebook write fixture");
    assert_eq!(fixture.schema_version, 1);

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
