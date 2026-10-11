#![deny(unsafe_code)]

//! Unified SymDesk configuration semantics frozen from the Go loader.

use std::{collections::BTreeMap, ffi::OsString, fmt, fs, io::Write, path::Path};

use serde::{Deserialize, Serialize};

/// Collects Unicode environment settings used by the configuration loaders.
/// Unrelated variables with non-Unicode values are ignored; a relevant
/// configuration value fails explicitly instead of being silently defaulted.
pub fn environment_snapshot() -> Result<BTreeMap<String, String>, String> {
    collect_environment(std::env::vars_os())
}

/// Reads a setting when its caller actually consumes it, preserving absence and empty values.
///
/// # Errors
/// Rejects a non-Unicode value without exposing its bytes or defaulting it.
pub fn environment_value(name: &str) -> Result<Option<String>, String> {
    decode_environment_value(name, std::env::var_os(name))
}

fn decode_environment_value(name: &str, value: Option<OsString>) -> Result<Option<String>, String> {
    value
        .map(|value| {
            value
                .into_string()
                .map_err(|_| format!("environment variable {name} is not valid UTF-8"))
        })
        .transpose()
}

fn collect_environment(
    variables: impl IntoIterator<Item = (OsString, OsString)>,
) -> Result<BTreeMap<String, String>, String> {
    let mut environment = BTreeMap::new();
    for (name, value) in variables {
        let Some(name) = name.to_str() else {
            continue;
        };
        match value.into_string() {
            Ok(value) => {
                environment.insert(name.to_owned(), value);
            }
            Err(_) if is_configuration_environment_name(name) => {
                return Err(format!("environment variable {name} is not valid UTF-8"));
            }
            Err(_) => {}
        }
    }
    Ok(environment)
}

fn is_configuration_environment_name(name: &str) -> bool {
    // Match the keys consumed by these loaders, not whole namespaces. Keep
    // this exact list aligned with the configuration and path lookups below.
    matches!(
        name,
        "HOME"
            | "USERPROFILE"
            | "TMPDIR"
            | "TMP"
            | "TEMP"
            | "LANG"
            | "LC_ALL"
            | "LC_MESSAGES"
            | "XDG_DATA_HOME"
            | "XDG_CONFIG_HOME"
            | "XDG_CACHE_HOME"
            | "SYMDESK_VAULT"
            | "SYMDESK_INBOX"
            | "SYMDESK_SIDECAR"
            | "SYMDESK_REVIEW_THRESHOLD"
            | "SYMDESK_LLM_PROVIDER"
            | "SYMDESK_LLM_API_KEY"
            | "SYMDESK_LLM_MODEL"
            | "SYMDESK_OLLAMA_URL"
            | "SYMDESK_RECIPE_RUNNER"
            | "SYMDESK_HERMES_SESSION"
            | "SYMDESK_LANG"
            | "SYMDESK_MAX_TOKENS"
            | "SYMDESK_HISTORY_MAX_PER_FILE"
            | "SYMDESK_HISTORY_MAX_AGE_DAYS"
            | "SYMDESK_HISTORY_CHECKPOINT_MAX_AGE_DAYS"
            | "SYMDESK_TRASH_RETENTION_DAYS"
            | "SYMDESK_RESULTS_MAX_AGE_DAYS"
            | "SYMDESK_RESULTS_MAX_PER_TASK"
            | "SYMDESK_AGENT_MAX_ITERATIONS"
            | "SYMDESK_DATASET_EXPORT_MAX_SENSITIVITY"
            | "SYMDESK_STORAGE_PATH_TEMPLATE"
            | "SYMRELATE_CONFIG_HOME"
            | "SYMRELATE_DATA_HOME"
            | "SYMRELATE_CACHE_HOME"
            | "SYMINGEST_VAULT"
            | "SYMINGEST_OCR_LANG"
            | "SYMINGEST_DB_PATH"
            | "SYMINGEST_ARCHIVE_PATH"
            | "SYMINGEST_INBOX"
            | "SYMINGEST_PAPERLESS_BASE_URL"
            | "SYMINGEST_SYMSEEK_ENABLED"
            | "SYMINGEST_SYMSEEK_BINARY"
            | "SYMINGEST_IMAP_ACCOUNTS"
            | "SYMINGEST_IMAP_POLL_INTERVAL"
            | "SYMINGEST_OLLAMA_BASE_URL"
            | "SYMINGEST_OLLAMA_MODEL"
    )
}

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

    /// Returns the configured API key or secret reference for resolution by a consumer.
    ///
    /// The value remains wrapped internally so derived `Debug` output stays redacted.
    #[must_use]
    pub fn api_key_reference(&self) -> &str {
        &self.llm_api_key.0
    }

    /// Applies the manual Go environment allowlist, including every
    /// documented `SYMDESK_*` configuration override.
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
        apply_string(environment, "SYMDESK_OLLAMA_URL", &mut self.ollama_url);
        apply_string(
            environment,
            "SYMDESK_RECIPE_RUNNER",
            &mut self.recipe_runner,
        );
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
            (
                "SYMDESK_AGENT_MAX_ITERATIONS",
                &mut self.agent_max_iterations,
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
        apply_string(
            environment,
            "SYMDESK_STORAGE_PATH_TEMPLATE",
            &mut self.storage_path_template,
        );
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
        Some(input) => toml::from_str::<Config>(input).map_err(|error| {
            format!(
                "failed to decode config file: {}",
                go_toml_error(input, &error)
            )
        })?,
        None => Config::default(),
    };
    config.apply_environment(environment);
    Ok(config)
}

fn go_toml_error(input: &str, error: &toml::de::Error) -> String {
    let detail = error.to_string();
    if !detail.contains("unclosed array") || !input.trim_end().ends_with('[') {
        return detail;
    }
    let last_key = input
        .lines()
        .rev()
        .find_map(|line| {
            let line = line.split('#').next()?.trim();
            let (key, _) = line.split_once('=')?;
            Some(key.trim().trim_matches('"').to_owned())
        })
        .unwrap_or_default();
    let line = input.lines().count().max(1);
    format!("toml: line {line} (last key {last_key:?}): unexpected EOF; expected value")
}

/// Encodes the current complete configuration in field order.
///
/// # Errors
///
/// Returns the TOML serializer error.
pub fn render_toml(config: &Config) -> Result<String, String> {
    toml::to_string(config).map_err(|error| format!("failed to encode config: {error}"))
}

/// Every directory below `directory` that does not exist yet, ordered from the
/// outermost to the innermost, mirroring the directories `os.MkdirAll` creates.
#[cfg(unix)]
fn missing_ancestors(directory: &Path) -> Vec<&Path> {
    let mut missing = Vec::new();
    let mut current = directory;
    while !current.exists() {
        match current.parent() {
            Some(parent) if !parent.as_os_str().is_empty() => {
                missing.push(current);
                current = parent;
            }
            _ => break,
        }
    }
    missing.reverse();
    missing
}

/// Writes the configuration exactly like the Go oracle's `config.Save`
/// (`internal/config/config.go:211`).
///
/// Go semantics, reproduced one for one:
///
/// * `os.MkdirAll(filepath.Dir(path), 0700)` — *every* missing ancestor is
///   created with mode `0700`, an existing directory keeps its mode;
/// * `os.OpenFile(path, O_CREATE|O_WRONLY|O_TRUNC, 0600)` — the file is created
///   with mode `0600` only when it does not exist, an existing file keeps its
///   mode and is truncated;
/// * the written bytes are exactly [`render_toml`], already proven byte-equal to
///   the Go encoder by `canonical_toml_bytes_match_go_encoder`;
/// * every failure carries the Go wrapper prefix: `failed to create config
///   directory: `, `failed to create config file: `, `failed to encode config: `
///   or `failed to close config file: `.
///
/// One deliberate difference: Go reports the deferred `f.Close()` error, Rust has
/// no fallible close, so `sync_all()` (fsync) is the closest call that can still
/// fail after a successful write.
///
/// # Errors
///
/// Returns the wrapped message described above.
pub fn save(path: &str, config: &Config) -> Result<(), String> {
    let target = Path::new(path);
    let parent = match target.parent() {
        Some(dir) if !dir.as_os_str().is_empty() => dir,
        _ => Path::new("."),
    };
    // Go applies the directory mode to every directory it creates, so the set
    // of missing ancestors is captured before `create_dir_all`. On Windows Go
    // ignores the mode entirely, which is why both the binding and its use are
    // unix-only.
    #[cfg(unix)]
    let created: Vec<&Path> = missing_ancestors(parent);
    fs::create_dir_all(parent)
        .map_err(|error| format!("failed to create config directory: {error}"))?;
    #[cfg(unix)]
    for directory in created {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(directory, fs::Permissions::from_mode(0o700))
            .map_err(|error| format!("failed to create config directory: {error}"))?;
    }
    let body = render_toml(config)?;
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options
        .open(target)
        .map_err(|error| format!("failed to create config file: {error}"))?;
    file.write_all(body.as_bytes())
        .map_err(|error| format!("failed to encode config: {error}"))?;
    file.sync_all()
        .map_err(|error| format!("failed to close config file: {error}"))?;
    Ok(())
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

/// Read-only contacts directory/database resolution, including legacy overrides.
#[derive(Debug, Eq, PartialEq)]
pub struct ContactsPaths {
    pub config_dir: String,
    pub data_dir: String,
    pub cache_dir: String,
    pub db_path: String,
}

#[must_use]
pub fn contacts_paths(environment: &BTreeMap<String, String>) -> ContactsPaths {
    let primary_dir = store_join(
        &store_base(environment, "XDG_DATA_HOME", ".local/share"),
        "symdesk",
    );
    let legacy_dir = store_join(
        &store_base(environment, "XDG_DATA_HOME", ".local/share"),
        "symrelate",
    );
    let override_dir = trimmed(environment, "SYMRELATE_DATA_HOME");
    let db_path = override_dir.map_or_else(
        || {
            legacy_fallback(
                store_join(&primary_dir, "symrelate.db"),
                store_join(&legacy_dir, "symrelate.db"),
            )
        },
        |directory| store_join(directory, "symrelate.db"),
    );
    let data_dir = override_dir.map_or_else(
        || {
            if db_path == store_join(&legacy_dir, "symrelate.db") {
                legacy_dir
            } else {
                primary_dir
            }
        },
        str::to_owned,
    );
    ContactsPaths {
        config_dir: trimmed(environment, "SYMRELATE_CONFIG_HOME").map_or_else(
            || {
                store_join(
                    &store_base(environment, "XDG_CONFIG_HOME", ".config"),
                    "symdesk",
                )
            },
            str::to_owned,
        ),
        data_dir,
        cache_dir: trimmed(environment, "SYMRELATE_CACHE_HOME").map_or_else(
            || {
                store_join(
                    &store_base(environment, "XDG_CACHE_HOME", ".cache"),
                    "symdesk",
                )
            },
            str::to_owned,
        ),
        db_path,
    }
}

/// Resolves one ingest artifact without creating or migrating state.
///
/// # Errors
/// Returns the Go validation/home-directory diagnostic for invalid inputs.
pub fn ingest_data_path(
    environment: &BTreeMap<String, String>,
    name: &str,
) -> Result<String, String> {
    let clean = clean_store_path(Path::new(name.trim()));
    let mut components = clean.components();
    if !matches!(components.next(), Some(std::path::Component::Normal(_)))
        || components.next().is_some()
    {
        return Err(format!(
            "ingest data artifact must be a single relative name: {}",
            quote_artifact(name)
        ));
    }
    let base = match trimmed(environment, "XDG_DATA_HOME") {
        Some(value) => value.to_owned(),
        None => store_join(
            user_home(environment).map_err(|error| {
                format!("cannot determine home directory; set {name} explicitly: {error}")
            })?,
            ".local/share",
        ),
    };
    let name = clean.to_str().expect("cleaning UTF-8 preserves UTF-8");
    Ok(legacy_fallback(
        store_join(&store_join(&base, "symdesk"), name),
        store_join(&store_join(&base, "symingest"), name),
    ))
}

fn trimmed<'a>(environment: &'a BTreeMap<String, String>, key: &str) -> Option<&'a str> {
    nonempty(environment, key)
        .map(str::trim)
        .filter(|value| !value.is_empty())
}

fn user_home(environment: &BTreeMap<String, String>) -> Result<&str, &'static str> {
    #[cfg(windows)]
    let (key, error) = ("USERPROFILE", "user home dir: %userprofile% is not defined");
    #[cfg(not(windows))]
    let (key, error) = ("HOME", "user home dir: $HOME is not defined");
    nonempty(environment, key).ok_or(error)
}

fn store_base(environment: &BTreeMap<String, String>, key: &str, suffix: &str) -> String {
    trimmed(environment, key).map_or_else(
        || store_join(user_home(environment).unwrap_or("."), suffix),
        str::to_owned,
    )
}

fn legacy_fallback(primary: String, legacy: String) -> String {
    // Go os.Stat accepts directories and follows symlinks, unlike is_file().
    if Path::new(&primary).exists() || !Path::new(&legacy).exists() {
        primary
    } else {
        legacy
    }
}

fn store_join(left: &str, right: &str) -> String {
    #[cfg(windows)]
    if is_windows_verbatim(left) {
        return join_windows_verbatim(left, right);
    }
    clean_store_path(&Path::new(left).join(right))
        .to_string_lossy()
        .into_owned()
}

fn clean_store_path(path: &Path) -> std::path::PathBuf {
    use std::path::{Component, PathBuf};
    let mut clean = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => (),
            Component::ParentDir => {
                if clean.file_name().is_some_and(|name| name != "..") {
                    clean.pop();
                } else if !clean.has_root() {
                    clean.push("..");
                }
            }
            _ => clean.push(component.as_os_str()),
        }
    }
    if clean.as_os_str().is_empty() {
        clean.push(".");
    }
    clean
}

// strconv.Quote, not Rust Debug or JSON: control escapes differ in diagnostics.
fn quote_artifact(value: &str) -> String {
    use std::{fmt::Write as _, sync::LazyLock};
    static PRINTABLE: LazyLock<regex::Regex> = LazyLock::new(|| {
        regex::Regex::new(r"^[\pL\pM\pN\pP\pS]$").expect("static Unicode categories")
    });
    let mut quoted = String::from("\"");
    for character in value.chars() {
        match character {
            '"' => quoted.push_str("\\\""),
            '\\' => quoted.push_str("\\\\"),
            '\u{7}' => quoted.push_str("\\a"),
            '\u{8}' => quoted.push_str("\\b"),
            '\u{c}' => quoted.push_str("\\f"),
            '\n' => quoted.push_str("\\n"),
            '\r' => quoted.push_str("\\r"),
            '\t' => quoted.push_str("\\t"),
            '\u{b}' => quoted.push_str("\\v"),
            c if c == ' ' || PRINTABLE.is_match(c.encode_utf8(&mut [0; 4])) => quoted.push(c),
            c => {
                let code = u32::from(c);
                if code < 128 {
                    write!(quoted, "\\x{code:02x}").expect("write String");
                } else if code < 65536 {
                    write!(quoted, "\\u{code:04x}").expect("write String");
                } else {
                    write!(quoted, "\\U{code:08x}").expect("write String");
                }
            }
        }
    }
    quoted.push('"');
    quoted
}

/// Mirrors configkit's important distinction: only an absolute XDG config
/// home affects the global file path; relative values fall back to HOME.
#[must_use]
pub fn global_path(environment: &BTreeMap<String, String>) -> String {
    let base = nonempty(environment, "XDG_CONFIG_HOME")
        .filter(|value| Path::new(value).is_absolute())
        .map_or_else(
            || join(user_home(environment).unwrap_or("."), ".config"),
            str::to_owned,
        );
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

fn join(left: &str, right: &str) -> String {
    store_join(left, right)
}

#[cfg(any(windows, test))]
fn is_windows_verbatim(value: &str) -> bool {
    matches!(
        value.as_bytes(),
        [b'\\' | b'/', b'\\' | b'/', b'?', b'\\' | b'/', ..]
    )
}

// Go 1.26 treats the first component after `\\?\` as the device volume,
// including `UNC`, not Rust's indivisible UNC host/share prefix. Clean the
// remaining rooted components without stripping the verbatim I/O prefix.
#[cfg(any(windows, test))]
fn join_windows_verbatim(left: &str, right: &str) -> String {
    let joined = format!(r"{}\{}", left.trim_end_matches(['/', '\\']), right).replace('/', r"\");
    let volume_end = joined[4..]
        .find('\\')
        .map_or(joined.len(), |index| index + 4);
    let (volume, tail) = joined.split_at(volume_end);
    if tail.is_empty() {
        return joined;
    }
    let mut components = Vec::new();
    for component in tail.split('\\') {
        match component {
            "" | "." => (),
            ".." => {
                components.pop();
            }
            _ => components.push(component),
        }
    }
    format!(r"{}\{}", volume, components.join(r"\"))
}

#[cfg(test)]
mod windows_verbatim_join_tests {
    use super::{is_windows_verbatim, join_windows_verbatim};

    #[test]
    fn separator_variants_dispatch_to_verbatim_cleaner() {
        for base in [r"\\?\C:\root", r"\\?\UNC\server\share\root"] {
            let canonical = join_windows_verbatim(base, "symdesk");
            for first in ['\\', '/'] {
                for second in ['\\', '/'] {
                    for last in ['\\', '/'] {
                        let variant = format!("{first}{second}?{last}{}", &base[4..]);
                        assert!(is_windows_verbatim(&variant), "{variant}");
                        assert_eq!(join_windows_verbatim(&variant, "symdesk"), canonical);
                    }
                }
            }
        }
        for ordinary in ["", "/", "//?", "//x/", r"\?\", "é/root", r"C:\root"] {
            assert!(!is_windows_verbatim(ordinary), "{ordinary}");
        }
    }

    #[test]
    fn captured_native_go_verbatim_paths_match_on_every_host() {
        let fixture: serde_json::Value = serde_json::from_str(include_str!(
            "../../../testdata/port/config/windows-verbatim-paths.json"
        ))
        .expect("actual native Windows Go capture");
        assert_eq!(fixture["goos"], "windows");
        assert_eq!(fixture["go_version"], "go1.26.6");
        let cases = fixture["cases"].as_array().expect("captured cases");
        assert_eq!(cases.len(), 11);
        let mut ids = std::collections::BTreeSet::new();
        for case in cases {
            assert!(ids.insert(case["id"].as_str().expect("case ID")));
            let base = case["input"].as_str().expect("captured input");
            assert!(is_windows_verbatim(base));
            let actual =
                join_windows_verbatim(&join_windows_verbatim(base, "symdesk"), "config.toml");
            assert_eq!(actual, case["expected"], "{}", case["id"]);
        }
    }
}

#[cfg(all(test, unix))]
mod environment_snapshot_tests {
    use super::{collect_environment, decode_environment_value};
    use std::{ffi::OsString, os::unix::ffi::OsStringExt};

    #[test]
    fn unrelated_non_unicode_environment_value_is_ignored_without_lossy_conversion() {
        let environment = collect_environment([
            (
                OsString::from("XDG_DATA_HOME"),
                OsString::from("/safe/data"),
            ),
            (
                OsString::from("UNRELATED_BINARY_ENV"),
                OsString::from_vec(vec![0xff]),
            ),
        ])
        .expect("unrelated non-Unicode variable is not an error");
        assert_eq!(
            environment.get("XDG_DATA_HOME").map(String::as_str),
            Some("/safe/data")
        );
        assert!(!environment.contains_key("UNRELATED_BINARY_ENV"));
        for name in [
            "XDG_UNUSED_BINARY",
            "SYMDESK_UNUSED_BINARY",
            "SYMINGEST_UNUSED_BINARY",
            "SYMRELATE_UNUSED_BINARY",
            "SYMSEEK_UNUSED_BINARY",
            "OLLAMA_UNUSED_BINARY",
            "SYMDESK_VAULT_EXTRA",
            "xdg_data_home",
            "SYMDESK_ANTHROPIC_URL",
            "SYMDESK_OLLAMA_MODEL",
            "SYMDESK_SERVER_TOKEN",
            "SYMDESK_WORKER_TOKEN",
            "SYMDESK_SERVER_LISTEN",
            "TZ",
        ] {
            let environment =
                collect_environment([(OsString::from(name), OsString::from_vec(vec![0xff]))])
                    .expect("unknown keys are not configuration, regardless of prefix");
            assert!(!environment.contains_key(name));
        }
    }

    #[test]
    fn non_unicode_configuration_value_is_reported_instead_of_defaulted() {
        let error = collect_environment([(
            OsString::from("XDG_DATA_HOME"),
            OsString::from_vec(vec![0xff]),
        )])
        .expect_err("relevant non-Unicode configuration is not silently dropped");
        assert_eq!(
            error,
            "environment variable XDG_DATA_HOME is not valid UTF-8"
        );
    }

    #[test]
    fn consumed_environment_values_preserve_absence_empty_and_unicode_but_reject_raw_bytes() {
        for name in [
            "SYMDESK_ANTHROPIC_URL",
            "SYMDESK_OLLAMA_MODEL",
            "SYMDESK_SERVER_TOKEN",
            "SYMDESK_WORKER_TOKEN",
            "SYMDESK_SERVER_LISTEN",
        ] {
            assert_eq!(decode_environment_value(name, None), Ok(None));
            for value in ["", " padded é value "] {
                assert_eq!(
                    decode_environment_value(name, Some(OsString::from(value))),
                    Ok(Some(value.to_owned()))
                );
            }
            assert_eq!(
                decode_environment_value(name, Some(OsString::from_vec(vec![0xff]))),
                Err(format!("environment variable {name} is not valid UTF-8"))
            );
        }
    }
}

#[cfg(test)]
mod secret_reference_tests {
    use super::{Config, load, render_toml};
    use std::collections::BTreeMap;

    #[test]
    fn api_key_reference_survives_toml_and_environment_loading() {
        let from_toml = load(Some("llm_api_key = \"toml-ref\"\n"), &BTreeMap::new())
            .expect("load TOML secret reference");
        assert_eq!(from_toml.api_key_reference(), "toml-ref");
        let serialized = render_toml(&from_toml).expect("serialize config");
        let roundtrip = load(Some(&serialized), &BTreeMap::new()).expect("reload config");
        assert_eq!(roundtrip.api_key_reference(), "toml-ref");

        let environment = BTreeMap::from([("SYMDESK_LLM_API_KEY".into(), "env-ref".into())]);
        let from_environment = load(None, &environment).expect("load environment secret");
        assert_eq!(from_environment.api_key_reference(), "env-ref");
        assert_eq!(Config::default().api_key_reference(), "");
    }

    #[test]
    fn config_debug_output_keeps_api_key_redacted() {
        let config = load(
            Some("llm_api_key = \"secret-that-must-not-appear\"\n"),
            &BTreeMap::new(),
        )
        .expect("load secret config");
        let debug = format!("{config:?}");
        assert!(debug.contains("SecretValue(***)"));
        assert!(!debug.contains("secret-that-must-not-appear"));
        assert_eq!(config.api_key_reference(), "secret-that-must-not-appear");
    }
}
