use std::{
    collections::BTreeMap,
    fs::{self, OpenOptions},
    io::Write,
    path::{Component, Path, PathBuf},
};

use rusqlite::Connection;
use serde::{Deserialize, Serialize};

use crate::{SidecarError, relocate_database};

/// Resolves the standalone symseek config file, which intentionally ignores
/// `XDG_CONFIG_HOME` just like the Go config package.
#[must_use]
pub fn symseek_config_path(environment: &BTreeMap<String, String>, cwd: &Path) -> PathBuf {
    let home = user_home(environment).map(PathBuf::from);
    let path = home
        .map(|home| home.join(".config/symseek/config.toml"))
        .unwrap_or_else(|| PathBuf::from(".config/symseek/config.toml"));
    absolute_clean(&path, cwd)
}

/// Resolves the Go retrieval index path without opening or creating the DB.
/// A configured `index_path` wins for standalone and vault-scoped requests.
pub fn index_location_for_vault(
    vault_root: &str,
    environment: &BTreeMap<String, String>,
    cwd: &Path,
    temp_root: &Path,
) -> Result<PathBuf, SidecarError> {
    let config_path = symseek_config_path(environment, cwd);
    let config = load_config(&config_path)?;
    if !config.index_path.trim().is_empty() {
        return Ok(absolute_clean(Path::new(&config.index_path), cwd));
    }
    if !vault_root.trim().is_empty() {
        return vault_retrieval_path(vault_root, environment, cwd, temp_root);
    }
    standalone_retrieval_path(environment, cwd)
}

/// Snapshots the effective standalone retrieval index and persists its new
/// location. Vault-scoped relocation is rejected because `index_path` is a
/// global override in the Go contract.
pub fn relocate_index_for_vault(
    vault_root: &str,
    destination: &Path,
    environment: &BTreeMap<String, String>,
    cwd: &Path,
    temp_root: &Path,
) -> Result<PathBuf, SidecarError> {
    if !vault_root.is_empty() {
        return Err(SidecarError::Contract(
            "cannot relocate a vault-scoped retrieval index; use backup/restore or run relocate without --vault for a deliberate global index_path override".to_owned(),
        ));
    }
    let source = index_location_for_vault("", environment, cwd, temp_root)?;
    let destination = absolute_clean(destination, cwd);
    let source_info = fs::metadata(&source)
        .map_err(|error| SidecarError::Contract(format!("stat retrieval index: {error}")))?;
    if !source_info.is_file() {
        return Err(SidecarError::Contract(format!(
            "retrieval index is not a regular file: {}",
            source.display()
        )));
    }
    let connection = Connection::open(&source)?;
    let relocated = relocate_database(&connection, &destination)?;

    let config_path = symseek_config_path(environment, cwd);
    let mut config = load_config(&config_path)?;
    config.index_path = relocated.to_string_lossy().into_owned();
    save_config(&config_path, &config)?;
    Ok(relocated)
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(default = "default_symseek_config")]
struct SymseekConfig {
    ollama_url: String,
    model: String,
    embedding_dim: i64,
    timeout_seconds: i64,
    retry_count: i64,
    retry_backoff_ms: i64,
    index_cooldown_seconds: i64,
    vector_backend: String,
    index_path: String,
    vector_quantization: String,
    vector_quant_bits: i64,
    vector_quantized_shortlist: i64,
    vector_exact_rerank: bool,
    rerank_query: bool,
    rerank_model: String,
    rerank_timeout_seconds: i64,
    expand_query: bool,
    expand_model: String,
    expand_timeout_seconds: i64,
}

fn default_symseek_config() -> SymseekConfig {
    SymseekConfig {
        ollama_url: "http://localhost:11434/api/embeddings".to_owned(),
        model: "qwen3-embedding:0.6b".to_owned(),
        embedding_dim: 768,
        timeout_seconds: 120,
        retry_count: 2,
        retry_backoff_ms: 500,
        index_cooldown_seconds: 5,
        vector_backend: "sqlite".to_owned(),
        index_path: String::new(),
        vector_quantization: "off".to_owned(),
        vector_quant_bits: 4,
        vector_quantized_shortlist: 200,
        vector_exact_rerank: true,
        rerank_query: false,
        rerank_model: String::new(),
        rerank_timeout_seconds: 120,
        expand_query: false,
        expand_model: String::new(),
        expand_timeout_seconds: 120,
    }
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct LegacyJsonConfig {
    ollama_url: String,
    model: String,
    embedding_dim: i64,
    timeout_seconds: i64,
    retry_count: i64,
    retry_backoff_ms: i64,
    index_cooldown_seconds: i64,
    vector_backend: String,
    index_path: String,
    vector_quantization: String,
    vector_quant_bits: i64,
    vector_quantized_shortlist: i64,
    vector_exact_rerank: bool,
    rerank_query: bool,
    rerank_model: String,
    rerank_timeout_seconds: i64,
    expand_query: bool,
    expand_model: String,
    expand_timeout_seconds: i64,
}

impl From<LegacyJsonConfig> for SymseekConfig {
    fn from(config: LegacyJsonConfig) -> Self {
        Self {
            ollama_url: config.ollama_url,
            model: config.model,
            embedding_dim: config.embedding_dim,
            timeout_seconds: config.timeout_seconds,
            retry_count: config.retry_count,
            retry_backoff_ms: config.retry_backoff_ms,
            index_cooldown_seconds: config.index_cooldown_seconds,
            vector_backend: config.vector_backend,
            index_path: config.index_path,
            vector_quantization: config.vector_quantization,
            vector_quant_bits: config.vector_quant_bits,
            vector_quantized_shortlist: config.vector_quantized_shortlist,
            vector_exact_rerank: config.vector_exact_rerank,
            rerank_query: config.rerank_query,
            rerank_model: config.rerank_model,
            rerank_timeout_seconds: config.rerank_timeout_seconds,
            expand_query: config.expand_query,
            expand_model: config.expand_model,
            expand_timeout_seconds: config.expand_timeout_seconds,
        }
    }
}

fn load_config(path: &Path) -> Result<SymseekConfig, SidecarError> {
    match fs::read_to_string(path) {
        Ok(contents) => toml::from_str(&contents).map_err(|error| {
            SidecarError::Contract(format!("failed to decode config file: {error}"))
        }),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            let legacy_path = path.with_file_name("config.json");
            match fs::read(&legacy_path) {
                Ok(contents) => match serde_json::from_slice::<LegacyJsonConfig>(&contents) {
                    Ok(legacy) => {
                        let config = SymseekConfig::from(legacy);
                        save_config(path, &config)?;
                        Ok(config)
                    }
                    Err(_) => Ok(default_symseek_config()),
                },
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                    Ok(default_symseek_config())
                }
                Err(error) => Err(SidecarError::Contract(format!(
                    "read legacy JSON config: {error}"
                ))),
            }
        }
        Err(error) => Err(SidecarError::Contract(format!(
            "failed to read config file: {error}"
        ))),
    }
}

fn save_config(path: &Path, config: &SymseekConfig) -> Result<(), SidecarError> {
    let parent = path
        .parent()
        .filter(|parent| !parent.as_os_str().is_empty())
        .unwrap_or_else(|| Path::new("."));
    create_private_dir_all(parent).map_err(|error| {
        SidecarError::Contract(format!("failed to create config directory: {error}"))
    })?;
    let contents = toml::to_string(config)
        .map_err(|error| SidecarError::Contract(format!("failed to encode config: {error}")))?;
    let mut options = OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(path).map_err(|error| {
        SidecarError::Contract(format!("failed to create config file: {error}"))
    })?;
    file.write_all(contents.as_bytes())
        .map_err(|error| SidecarError::Contract(format!("failed to encode config: {error}")))?;
    file.sync_all()
        .map_err(|error| SidecarError::Contract(format!("failed to close config file: {error}")))
}

#[cfg(unix)]
fn create_private_dir_all(path: &Path) -> std::io::Result<()> {
    use std::os::unix::fs::DirBuilderExt;
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true).mode(0o700).create(path)
}

#[cfg(not(unix))]
fn create_private_dir_all(path: &Path) -> std::io::Result<()> {
    fs::create_dir_all(path)
}

fn standalone_retrieval_path(
    environment: &BTreeMap<String, String>,
    cwd: &Path,
) -> Result<PathBuf, SidecarError> {
    let data_home = data_home(environment)?;
    let primary = data_home.join("symdesk/retrieval.db");
    let old_primary = data_home.join("symdesk/symseek.db");
    let home = user_home(environment).ok_or_else(|| {
        SidecarError::Contract("user home dir: cannot determine home directory".to_owned())
    })?;
    let legacy = PathBuf::from(home).join(".local/share/symaira-seek/symseek.db");
    for candidate in [&primary, &old_primary, &legacy] {
        if absolute_clean(candidate, cwd).exists() {
            return Ok(candidate.to_path_buf());
        }
    }
    Ok(primary)
}

fn vault_retrieval_path(
    vault_root: &str,
    environment: &BTreeMap<String, String>,
    cwd: &Path,
    temp_root: &Path,
) -> Result<PathBuf, SidecarError> {
    let supplied = absolute_clean(Path::new(vault_root), cwd);
    let canonical = fs::canonicalize(&supplied).unwrap_or_else(|_| supplied.clone());
    let mut root = data_home(environment)?.join("symdesk/vaults");
    let canonical_temp =
        fs::canonicalize(temp_root).unwrap_or_else(|_| absolute_clean(temp_root, cwd));
    if nonempty_environment(environment, "XDG_DATA_HOME").is_none()
        && canonical.starts_with(&canonical_temp)
        && canonical != canonical_temp
    {
        root = absolute_clean(temp_root, cwd).join("symdesk/test-vaults");
    }
    let canonical_text = canonical.to_string_lossy();
    let digest = symdesk_vault::sha256_hex(canonical_text.as_bytes());
    Ok(root.join(&digest[..16]).join("retrieval.db"))
}

fn data_home(environment: &BTreeMap<String, String>) -> Result<PathBuf, SidecarError> {
    if let Some(value) = nonempty_environment(environment, "XDG_DATA_HOME") {
        return Ok(PathBuf::from(value));
    }
    let home = user_home(environment).ok_or_else(|| {
        SidecarError::Contract("user home dir: cannot determine home directory".to_owned())
    })?;
    Ok(PathBuf::from(home).join(".local/share"))
}

fn user_home(environment: &BTreeMap<String, String>) -> Option<&str> {
    #[cfg(windows)]
    let key = "USERPROFILE";
    #[cfg(not(windows))]
    let key = "HOME";
    nonempty_environment(environment, key)
}

fn nonempty_environment<'a>(
    environment: &'a BTreeMap<String, String>,
    key: &str,
) -> Option<&'a str> {
    environment
        .get(key)
        .map(String::as_str)
        .filter(|value| !value.trim().is_empty())
}

fn absolute_clean(path: &Path, cwd: &Path) -> PathBuf {
    let path = if path.is_absolute() {
        path.to_path_buf()
    } else {
        cwd.join(path)
    };
    lexical_clean(&path)
}

fn lexical_clean(path: &Path) -> PathBuf {
    let mut cleaned = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                if !cleaned.pop() {
                    cleaned.push(component.as_os_str());
                }
            }
            other => cleaned.push(other.as_os_str()),
        }
    }
    cleaned
}
