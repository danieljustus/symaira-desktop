#![deny(unsafe_code)]

use std::collections::BTreeMap;

use regex as _;
use serde::Deserialize;
use symdesk_core::config::{self, Config, Severity};
use time as _;
use toml as _;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    cases: Cases,
}

#[derive(Deserialize)]
struct Cases {
    defaults: SafeConfig,
    loads: Vec<LoadCase>,
    validation: Vec<ValidationCase>,
    save: SaveCase,
    paths: Vec<PathCase>,
}

#[derive(Clone, Deserialize)]
struct SafeConfig {
    vault: String,
    inbox: String,
    review_threshold: i64,
    llm_provider: String,
    has_api_key: bool,
    llm_model: String,
    ollama_url: String,
    recipe_runner: String,
    hermes_session: String,
    language: String,
    max_tokens: i64,
    agent_max_iterations: i64,
    history_max_per_file: i64,
    history_max_age_days: i64,
    history_checkpoint_max_age_days: i64,
    trash_retention_days: i64,
    results_max_age_days: i64,
    results_max_per_task: i64,
    dataset_export_max_sensitivity: String,
    storage_path_template: String,
}

#[derive(Deserialize)]
struct LoadCase {
    #[serde(rename = "id")]
    _id: String,
    toml: Option<String>,
    #[serde(default)]
    missing: bool,
    #[serde(default)]
    environment: BTreeMap<String, String>,
    config: Option<SafeConfig>,
    error_prefix: Option<String>,
}

#[derive(Deserialize)]
struct ValidationCase {
    #[serde(rename = "id")]
    _id: String,
    config: SafeConfig,
    paths_exist: bool,
    findings: Vec<Finding>,
}

#[derive(Deserialize)]
struct Finding {
    severity: String,
    field: String,
    message: String,
}

#[derive(Deserialize)]
struct SaveCase {
    config: SafeConfig,
    toml: String,
}

#[derive(Deserialize)]
struct PathCase {
    #[serde(rename = "id")]
    _id: String,
    environment: BTreeMap<String, String>,
    data_home: String,
    config_home: String,
    cache_home: String,
    data_dir: String,
    config_dir: String,
    cache_dir: String,
    global_path: String,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!("../../../testdata/port/core/config.json"))
        .expect("decode config fixture")
}

#[test]
fn defaults_loads_and_ignored_environment_match_go() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_config(&Config::default(), &fixture.cases.defaults);
    for case in fixture.cases.loads {
        let input = if case.missing {
            None
        } else {
            case.toml.as_deref()
        };
        match config::load(input, &case.environment) {
            Ok(actual) => {
                assert!(case.error_prefix.is_none());
                assert_config(&actual, case.config.as_ref().expect("fixture config"));
            }
            Err(error) => assert!(
                error.starts_with(case.error_prefix.as_deref().expect("fixture error prefix")),
                "unexpected error: {error}"
            ),
        }
    }
}

#[test]
fn validation_order_and_messages_match_go() {
    for case in fixture().cases.validation {
        let config = from_safe(&case.config);
        let actual = config.validate_with_path_exists(|_| case.paths_exist);
        assert_eq!(actual.len(), case.findings.len());
        for (actual, expected) in actual.iter().zip(case.findings) {
            assert_eq!(
                match actual.severity {
                    Severity::Fatal => "fatal",
                    Severity::Warning => "warning",
                },
                expected.severity
            );
            assert_eq!(actual.field, expected.field);
            assert_eq!(actual.message, expected.message);
        }
    }
}

#[test]
fn canonical_toml_bytes_match_go_encoder() {
    let case = fixture().cases.save;
    let actual = config::render_toml(&from_safe(&case.config)).expect("encode config");
    assert_eq!(actual, case.toml);
}

#[test]
fn base_and_global_paths_match_go_contract() {
    for case in fixture().cases.paths {
        assert_eq!(config::resolve_data_home(&case.environment), case.data_home);
        assert_eq!(
            config::resolve_config_home(&case.environment),
            case.config_home
        );
        assert_eq!(
            config::resolve_cache_home(&case.environment),
            case.cache_home
        );
        assert_eq!(config::data_dir(&case.environment), case.data_dir);
        assert_eq!(config::config_dir(&case.environment), case.config_dir);
        assert_eq!(config::cache_dir(&case.environment), case.cache_dir);
        assert_eq!(config::global_path(&case.environment), case.global_path);
    }
}

fn assert_config(actual: &Config, expected: &SafeConfig) {
    assert_eq!(actual.vault, expected.vault);
    assert_eq!(actual.inbox, expected.inbox);
    assert_eq!(actual.review_threshold, expected.review_threshold);
    assert_eq!(actual.llm_provider, expected.llm_provider);
    assert_eq!(actual.has_api_key(), expected.has_api_key);
    assert_eq!(actual.llm_model, expected.llm_model);
    assert_eq!(actual.ollama_url, expected.ollama_url);
    assert_eq!(actual.recipe_runner, expected.recipe_runner);
    assert_eq!(actual.hermes_session, expected.hermes_session);
    assert_eq!(actual.language, expected.language);
    assert_eq!(actual.max_tokens, expected.max_tokens);
    assert_eq!(actual.agent_max_iterations, expected.agent_max_iterations);
    assert_eq!(actual.history_max_per_file, expected.history_max_per_file);
    assert_eq!(actual.history_max_age_days, expected.history_max_age_days);
    assert_eq!(
        actual.history_checkpoint_max_age_days,
        expected.history_checkpoint_max_age_days
    );
    assert_eq!(actual.trash_retention_days, expected.trash_retention_days);
    assert_eq!(actual.results_max_age_days, expected.results_max_age_days);
    assert_eq!(actual.results_max_per_task, expected.results_max_per_task);
    assert_eq!(
        actual.dataset_export_max_sensitivity,
        expected.dataset_export_max_sensitivity
    );
    assert_eq!(actual.storage_path_template, expected.storage_path_template);
}

fn from_safe(value: &SafeConfig) -> Config {
    let mut config = Config::default();
    config.vault.clone_from(&value.vault);
    config.inbox.clone_from(&value.inbox);
    config.review_threshold = value.review_threshold;
    config.llm_provider.clone_from(&value.llm_provider);
    config.llm_model.clone_from(&value.llm_model);
    config.ollama_url.clone_from(&value.ollama_url);
    config.recipe_runner.clone_from(&value.recipe_runner);
    config.hermes_session.clone_from(&value.hermes_session);
    config.language.clone_from(&value.language);
    config.max_tokens = value.max_tokens;
    config.agent_max_iterations = value.agent_max_iterations;
    config.history_max_per_file = value.history_max_per_file;
    config.history_max_age_days = value.history_max_age_days;
    config.history_checkpoint_max_age_days = value.history_checkpoint_max_age_days;
    config.trash_retention_days = value.trash_retention_days;
    config.results_max_age_days = value.results_max_age_days;
    config.results_max_per_task = value.results_max_per_task;
    config
        .dataset_export_max_sensitivity
        .clone_from(&value.dataset_export_max_sensitivity);
    config
        .storage_path_template
        .clone_from(&value.storage_path_template);
    config
}
