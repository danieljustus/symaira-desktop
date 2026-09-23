#![deny(unsafe_code)]

use std::collections::BTreeMap;

use serde::Deserialize;
use symdesk_core::config::{self, Config};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    oracle_commit: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    toml: Option<String>,
    #[serde(default)]
    missing: bool,
    #[serde(default)]
    environment: BTreeMap<String, String>,
    config: Expected,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Expected {
    ollama_url: String,
    recipe_runner: String,
    agent_max_iterations: i64,
    storage_path_template: String,
}

fn fixture() -> Fixture {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join("testdata/port/core/config-precedence.json");
    let data = std::fs::read_to_string(path).expect("read Go-owned config precedence fixture");
    serde_json::from_str(&data).expect("decode Go-owned config precedence fixture")
}

#[test]
fn four_environment_overrides_match_the_go_loader() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_commit, "e023816a9db2b3d71514049195886fe1b9766a5a");
    assert_eq!(fixture.cases.len(), 10);

    for case in fixture.cases {
        let input = if case.missing {
            None
        } else {
            Some(case.toml.as_deref().expect("non-missing case TOML"))
        };
        let actual = config::load(input, &case.environment)
            .unwrap_or_else(|error| panic!("case {} failed to load: {error}", case.id));
        assert_eq!(
            Expected {
                ollama_url: actual.ollama_url,
                recipe_runner: actual.recipe_runner,
                agent_max_iterations: actual.agent_max_iterations,
                storage_path_template: actual.storage_path_template,
            },
            case.config,
            "case {}",
            case.id
        );
    }
}

#[test]
fn environment_application_does_not_change_other_default_fields() {
    let mut config = Config::default();
    config.apply_environment(&BTreeMap::from([
        ("SYMDESK_OLLAMA_URL".into(), "http://env.example".into()),
        ("SYMDESK_RECIPE_RUNNER".into(), "env-runner".into()),
        ("SYMDESK_AGENT_MAX_ITERATIONS".into(), "12".into()),
        ("SYMDESK_STORAGE_PATH_TEMPLATE".into(), "env/{title}".into()),
    ]));
    assert_eq!(config.llm_provider, "ollama");
    assert_eq!(config.review_threshold, 85);
    assert_eq!(config.max_tokens, 8192);
}
