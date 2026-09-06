#![deny(unsafe_code)]

//! Unified SymDesk configuration semantics frozen from the Go loader.

use std::{collections::BTreeMap, fmt};

use serde::{Deserialize, Serialize};

#[derive(Clone, Default, Deserialize, Serialize)]
#[serde(transparent)]
pub struct SecretValue(String);

impl SecretValue {
    #[must_use]
    pub fn is_configured(&self) -> bool {
        !self.0.is_empty()
    }
}

impl fmt::Debug for SecretValue {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(if self.is_configured() {
            "SecretValue(***)"
        } else {
            "SecretValue(empty)"
        })
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(default)]
pub struct Config {
    pub vault: String,
    pub inbox: String,
    pub review_threshold: i64,
    pub llm_provider: String,
    llm_api_key: SecretValue,
    pub llm_model: String,
    pub ollama_url: String,
    pub recipe_runner: String,
    pub hermes_session: String,
    pub language: String,
    pub max_tokens: i64,
    pub agent_max_iterations: i64,
    pub history_max_per_file: i64,
    pub history_max_age_days: i64,
    pub history_checkpoint_max_age_days: i64,
    pub trash_retention_days: i64,
    pub results_max_age_days: i64,
    pub results_max_per_task: i64,
    pub dataset_export_max_sensitivity: String,
    pub storage_path_template: String,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            vault: String::new(),
            inbox: String::new(),
            review_threshold: 85,
            llm_provider: "ollama".to_owned(),
            llm_api_key: SecretValue::default(),
            llm_model: "claude-sonnet-5".to_owned(),
            ollama_url: String::new(),
            recipe_runner: String::new(),
            hermes_session: String::new(),
            language: String::new(),
            max_tokens: 8192,
            agent_max_iterations: 0,
            history_max_per_file: 20,
            history_max_age_days: 90,
            history_checkpoint_max_age_days: 30,
            trash_retention_days: 30,
            results_max_age_days: 30,
            results_max_per_task: 20,
            dataset_export_max_sensitivity: "internal".to_owned(),
            storage_path_template: String::new(),
        }
    }
}

impl Config {
    #[must_use]
    pub fn has_api_key(&self) -> bool {
        self.llm_api_key.is_configured()
    }

    /// Applies the manual Go environment allowlist. Tagged fields omitted by
    /// Go remain intentionally ignored until issue #854 changes both oracles.
    pub fn apply_environment(&mut self, environment: &BTreeMap<String, String>) {
        apply_string(environment, "SYMDESK_VAULT", &mut self.vault);
        apply_string(environment, "SYMDESK_INBOX", &mut self.inbox);
        if let Some(value) = parse_integer(environment, "SYMDESK_REVIEW_THRESHOLD")
            && (0..=100).contains(&value)
        {
            self.review_threshold = value;
        }
        apply_string(environment, "SYMDESK_LLM_PROVIDER", &mut self.llm_provider);
        if let Some(value) = nonempty(environment, "SYMDESK_LLM_API_KEY") {
            self.llm_api_key = SecretValue(value.to_owned());
        }
        apply_string(environment, "SYMDESK_LLM_MODEL", &mut self.llm_model);
        apply_string(
            environment,
            "SYMDESK_HERMES_SESSION",
            &mut self.hermes_session,
        );
        apply_string(environment, "SYMDESK_LANG", &mut self.language);
        if let Some(value) = parse_integer(environment, "SYMDESK_MAX_TOKENS")
            && value > 0
        {
            self.max_tokens = value;
        }
        for (key, target) in [
            (
                "SYMDESK_HISTORY_MAX_PER_FILE",
                &mut self.history_max_per_file,
            ),
            (
                "SYMDESK_HISTORY_MAX_AGE_DAYS",
                &mut self.history_max_age_days,
            ),
            (
                "SYMDESK_HISTORY_CHECKPOINT_MAX_AGE_DAYS",
                &mut self.history_checkpoint_max_age_days,
            ),
            (
                "SYMDESK_TRASH_RETENTION_DAYS",
                &mut self.trash_retention_days,
            ),
            (
                "SYMDESK_RESULTS_MAX_AGE_DAYS",
                &mut self.results_max_age_days,
            ),
            (
                "SYMDESK_RESULTS_MAX_PER_TASK",
                &mut self.results_max_per_task,
            ),
        ] {
            if let Some(value) = parse_integer(environment, key)
                && value >= 0
            {
                *target = value;
            }
        }
        if let Some(value) = nonempty(environment, "SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY") {
            self.dataset_export_max_sensitivity = value.trim().to_lowercase();
        }
    }

    #[must_use]
    pub fn validate_values(&self) -> Vec<Finding> {
        self.validate_with_path_exists(|_| true)
    }

    #[must_use]
    pub fn validate_with_path_exists(&self, path_exists: impl Fn(&str) -> bool) -> Vec<Finding> {
        let mut findings = Vec::new();
        if !(0..=100).contains(&self.review_threshold) {
            findings.push(Finding::fatal(
                "review_threshold",
                format!(
                    "review_threshold must be 0–100, got {}",
                    self.review_threshold
                ),
            ));
        }
        if self.max_tokens <= 0 {
            findings.push(Finding::fatal(
                "max_tokens",
                format!("max_tokens must be > 0, got {}", self.max_tokens),
            ));
        }
        warning_if_negative(
            &mut findings,
            "history_max_age_days",
            self.history_max_age_days,
        );
        warning_if_negative(
            &mut findings,
            "history_checkpoint_max_age_days",
            self.history_checkpoint_max_age_days,
        );
        warning_if_negative(
            &mut findings,
            "trash_retention_days",
            self.trash_retention_days,
        );
        warning_if_negative(
            &mut findings,
            "history_max_per_file",
            self.history_max_per_file,
        );
        warning_if_negative(
            &mut findings,
            "results_max_age_days",
            self.results_max_age_days,
        );
        warning_if_negative(
            &mut findings,
            "results_max_per_task",
            self.results_max_per_task,
        );
        if !matches!(
            self.dataset_export_max_sensitivity.as_str(),
            "public" | "internal" | "confidential" | "restricted"
        ) {
            findings.push(Finding::warning(
                "dataset_export_max_sensitivity",
                format!(
                    "dataset_export_max_sensitivity must be public, internal, confidential, or restricted, got {:?}",
                    self.dataset_export_max_sensitivity
                ),
            ));
        }
        if !self.vault.is_empty() && !path_exists(&self.vault) {
            findings.push(Finding::fatal(
                "vault",
                format!("vault path does not exist: {}", self.vault),
            ));
        }
        if !self.inbox.is_empty() && !path_exists(&self.inbox) {
            findings.push(Finding::warning(
                "inbox",
                format!("inbox path does not exist: {}", self.inbox),
            ));
        }
        if !matches!(
            self.llm_provider.as_str(),
            "" | "ollama" | "anthropic" | "openai" | "hermes"
        ) {
            findings.push(Finding::warning(
                "llm_provider",
                format!(
                    "unsupported llm_provider {:?} — expected one of: ollama, anthropic, openai, hermes",
                    self.llm_provider
                ),
            ));
        }
        if !matches!(self.language.as_str(), "" | "en" | "de") {
            findings.push(Finding::warning(
                "language",
                format!(
                    "unsupported language {:?} — expected en or de",
                    self.language
                ),
            ));
        }
        findings
    }
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Severity {
    Fatal,
    Warning,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Finding {
    pub severity: Severity,
    pub field: &'static str,
    pub message: String,
}

impl Finding {
    fn fatal(field: &'static str, message: String) -> Self {
        Self {
            severity: Severity::Fatal,
            field,
            message,
        }
    }

    fn warning(field: &'static str, message: String) -> Self {
        Self {
            severity: Severity::Warning,
            field,
            message,
        }
    }
}

/// Loads defaults, optional TOML, then the current Go environment allowlist.
///
/// # Errors
///
/// Returns a stable prefix followed by the TOML decoder detail.
pub fn load(
    toml_input: Option<&str>,
    environment: &BTreeMap<String, String>,
) -> Result<Config, String> {
    let mut config = match toml_input {
        Some(input) => toml::from_str::<Config>(input)
            .map_err(|error| format!("failed to decode config file: {error}"))?,
        None => Config::default(),
    };
    config.apply_environment(environment);
    Ok(config)
}

/// Encodes the current complete configuration in field order.
///
/// # Errors
///
/// Returns the TOML serializer error.
pub fn render_toml(config: &Config) -> Result<String, String> {
    toml::to_string(config).map_err(|error| format!("failed to encode config: {error}"))
}

#[must_use]
pub fn resolve_data_home(environment: &BTreeMap<String, String>) -> String {
    resolve_home(environment, "XDG_DATA_HOME", ".local/share")
}

#[must_use]
pub fn resolve_config_home(environment: &BTreeMap<String, String>) -> String {
    resolve_home(environment, "XDG_CONFIG_HOME", ".config")
}

#[must_use]
pub fn resolve_cache_home(environment: &BTreeMap<String, String>) -> String {
    resolve_home(environment, "XDG_CACHE_HOME", ".cache")
}

#[must_use]
pub fn data_dir(environment: &BTreeMap<String, String>) -> String {
    join(&resolve_data_home(environment), "symdesk")
}

#[must_use]
pub fn config_dir(environment: &BTreeMap<String, String>) -> String {
    join(&resolve_config_home(environment), "symdesk")
}

#[must_use]
pub fn cache_dir(environment: &BTreeMap<String, String>) -> String {
    join(&resolve_cache_home(environment), "symdesk")
}

/// Mirrors configkit's important distinction: only an absolute XDG config
/// home affects the global file path; relative values fall back to HOME.
#[must_use]
pub fn global_path(environment: &BTreeMap<String, String>) -> String {
    let base = nonempty(environment, "XDG_CONFIG_HOME")
        .map(str::trim)
        .filter(|value| portable_absolute(value))
        .map_or_else(|| join(&home(environment), ".config"), str::to_owned);
    join(&join(&base, "symdesk"), "config.toml")
}

fn warning_if_negative(findings: &mut Vec<Finding>, field: &'static str, value: i64) {
    if value < 0 {
        findings.push(Finding::warning(
            field,
            format!("{field} must be >= 0, got {value}"),
        ));
    }
}

fn apply_string(environment: &BTreeMap<String, String>, key: &str, target: &mut String) {
    if let Some(value) = nonempty(environment, key) {
        target.clone_from(&value.to_owned());
    }
}

fn parse_integer(environment: &BTreeMap<String, String>, key: &str) -> Option<i64> {
    nonempty(environment, key)?.parse().ok()
}

fn nonempty<'a>(environment: &'a BTreeMap<String, String>, key: &str) -> Option<&'a str> {
    environment
        .get(key)
        .map(String::as_str)
        .filter(|value| !value.is_empty())
}

fn resolve_home(environment: &BTreeMap<String, String>, key: &str, suffix: &str) -> String {
    nonempty(environment, key)
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map_or_else(|| join(&home(environment), suffix), str::to_owned)
}

fn home(environment: &BTreeMap<String, String>) -> String {
    nonempty(environment, "HOME")
        .or_else(|| nonempty(environment, "USERPROFILE"))
        .unwrap_or(".")
        .to_owned()
}

fn portable_absolute(value: &str) -> bool {
    value.starts_with('/') || value.starts_with('\\') || value.as_bytes().get(1) == Some(&b':')
}

fn join(left: &str, right: &str) -> String {
    if left.is_empty() || left == "." {
        format!("./{}", right.trim_start_matches(['/', '\\']))
    } else {
        format!(
            "{}/{}",
            left.trim_end_matches(['/', '\\']),
            right.trim_start_matches(['/', '\\'])
        )
    }
}
