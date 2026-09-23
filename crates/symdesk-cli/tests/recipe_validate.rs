use std::{fs, process::Command, time::SystemTime};

use serde::Deserialize;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    recipe: String,
    exit_code: i32,
    stdout: String,
    stderr: String,
    #[serde(default)]
    json: bool,
    #[serde(default = "default_true")]
    exact_stderr: bool,
}

fn default_true() -> bool {
    true
}

#[test]
fn recipe_validate_matches_go_process_contract() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/ai/recipe-validate.json"
    ))
    .expect("decode Go-owned recipe validation fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 10);
    let root = std::env::temp_dir().join(format!(
        "symdesk-recipe-validate-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(SystemTime::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    fs::create_dir(&root).expect("make isolated test directory");
    for case in fixture.cases {
        let recipe = root.join(format!("{}.yml", case.name));
        if case.name != "missing_file" {
            fs::write(&recipe, case.recipe).expect("write recipe input");
        }
        let result = Command::new(env!("CARGO_BIN_EXE_symdesk"))
            .args(if case.json {
                vec!["--json", "recipe", "validate"]
            } else {
                vec!["recipe", "validate"]
            })
            .arg(&recipe)
            .output()
            .expect("run Rust symdesk process");
        assert_eq!(
            result.status.code().unwrap_or(-1),
            case.exit_code,
            "{} exit",
            case.name
        );
        assert_eq!(
            String::from_utf8_lossy(&result.stdout),
            case.stdout,
            "{} stdout",
            case.name
        );
        if case.exact_stderr {
            assert_eq!(
                String::from_utf8_lossy(&result.stderr),
                case.stderr,
                "{} stderr",
                case.name
            );
        } else {
            assert!(
                !result.stderr.is_empty(),
                "{} must report the Go-observed failure",
                case.name
            );
        }
    }
    fs::remove_dir_all(root).expect("remove only this test's temporary directory");
}
