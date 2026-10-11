#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs, io,
    path::{Path, PathBuf},
};

use clap::{Arg, ArgAction, Command};
use serde::Serialize;
use symdesk_core::config::{self, Config};
use symdesk_index::{
    index_location_for_vault, sidecar_path_for_vault, store_retrieval_path_for_vault,
    store_sidecar_path_for_vault,
};

#[derive(Serialize)]
pub struct Paths {
    data_dir: String,
    config_dir: String,
    cache_dir: String,
    sidecar: String,
    retrieval: String,
    ingest: String,
    ingest_archive: String,
    contacts: String,
}

#[derive(Clone, Debug)]
struct IngestConfig {
    vault: String,
    ocr_lang: String,
    db_path: String,
    archive_path: String,
    inbox: String,
    paperless_base_url: String,
    symseek_enabled: bool,
    symseek_binary: String,
    imap_accounts: Vec<()>,
    imap_poll_interval: String,
    ollama_base_url: String,
    ollama_model: String,
}

impl IngestConfig {
    fn defaults(environment: &BTreeMap<String, String>) -> Self {
        let ocr_lang = ["LC_ALL", "LC_MESSAGES", "LANG"]
            .iter()
            .filter_map(|key| environment.get(*key))
            .find(|locale| locale.to_lowercase().starts_with("de"))
            .map_or_else(|| "eng".to_owned(), |_| "deu+eng".to_owned());
        Self {
            vault: String::new(),
            ocr_lang,
            db_path: String::new(),
            archive_path: String::new(),
            inbox: String::new(),
            paperless_base_url: String::new(),
            symseek_enabled: false,
            symseek_binary: String::new(),
            imap_accounts: Vec::new(),
            imap_poll_interval: "5m".to_owned(),
            ollama_base_url: String::new(),
            ollama_model: String::new(),
        }
    }
}

pub fn command() -> Command {
    Command::new("config").subcommand_required(true).subcommand(
        Command::new("paths").arg(Arg::new("extra").num_args(0..).action(ArgAction::Append)),
    )
}

pub fn load_root_config() -> Result<(Config, BTreeMap<String, String>), String> {
    let environment = symdesk_core::config::environment_snapshot()?;
    let path = config::global_path(&environment);
    let content = match fs::read_to_string(&path) {
        Ok(content) => Some(content),
        Err(error) if error.kind() == io::ErrorKind::NotFound => None,
        Err(error) => {
            return Err(format!(
                "failed to read config file: {}",
                go_path_error("read", &path, &error)
            ));
        }
    };
    let loaded = config::load(content.as_deref(), &environment)?;
    Ok((loaded, environment))
}

pub fn run(
    vault_root: &str,
    environment: &BTreeMap<String, String>,
    json: bool,
) -> Result<String, String> {
    let cwd = std::env::current_dir()
        .map_err(|error| format!("failed to get working directory: {error}"))?;
    let temp_root = std::env::temp_dir();
    preflight(vault_root, environment, &cwd, &temp_root)?;

    let sidecar = sidecar_path_for_vault(vault_root, environment, &cwd, &temp_root)
        .map_err(|error| error.to_string())?;
    let retrieval = index_location_for_vault(vault_root, environment, &cwd, &temp_root)
        .map_err(|error| error.to_string())?;
    let ingest_config = load_ingest_config(environment, &cwd)?;
    let effective_vault = first_nonempty(vault_root, &ingest_config.vault);
    let ingest_archive = if !ingest_config.archive_path.is_empty() {
        ingest_config.archive_path
    } else if !effective_vault.is_empty() {
        clean_join(&[effective_vault, "archive", "ingest"])
    } else {
        config::ingest_data_path(environment, "archive")?
    };
    let ingest = if !ingest_config.db_path.is_empty() {
        ingest_config.db_path
    } else {
        config::ingest_data_path(environment, "symingest.db")?
    };

    let contacts = config::contacts_paths(environment).db_path;
    let paths = Paths {
        data_dir: config::data_dir(environment),
        config_dir: config::config_dir(environment),
        cache_dir: config::cache_dir(environment),
        sidecar: sidecar.to_string_lossy().into_owned(),
        retrieval: retrieval.to_string_lossy().into_owned(),
        ingest,
        ingest_archive,
        contacts,
    };
    Ok(render(&paths, json))
}

fn preflight(
    vault_root: &str,
    environment: &BTreeMap<String, String>,
    cwd: &Path,
    temp_root: &Path,
) -> Result<(), String> {
    store_sidecar_path_for_vault(vault_root, environment, cwd, temp_root)
        .map_err(|error| error.to_string())?;
    store_retrieval_path_for_vault(vault_root, environment, cwd, temp_root)
        .map_err(|error| error.to_string())?;
    config::ingest_data_path(environment, "symingest.db")?;
    config::ingest_data_path(environment, "archive")?;
    let _ = config::contacts_paths(environment);
    Ok(())
}

fn load_ingest_config(
    environment: &BTreeMap<String, String>,
    cwd: &Path,
) -> Result<IngestConfig, String> {
    load_ingest_config_inner(environment, cwd)
        .map_err(|error| format!("reload symingest configuration: {error}"))
}

fn load_ingest_config_inner(
    environment: &BTreeMap<String, String>,
    cwd: &Path,
) -> Result<IngestConfig, String> {
    let home = user_home(environment)
        .ok_or_else(|| format!("cannot determine home directory: {}", home_error()))?;
    let mut config = IngestConfig::defaults(environment);
    let global = PathBuf::from(home).join(".config/symingest/config.toml");
    merge_ingest_file(&mut config, &global, "global config error")?;
    let project = cwd.join(".symingest.toml");
    merge_ingest_file(&mut config, &project, "project config error")?;
    apply_ingest_environment(&mut config, environment)?;
    let _unused_schema_fields = (
        &config.ocr_lang,
        &config.inbox,
        &config.paperless_base_url,
        config.symseek_enabled,
        &config.symseek_binary,
        config.imap_accounts.len(),
        &config.imap_poll_interval,
        &config.ollama_base_url,
        &config.ollama_model,
    );
    Ok(config)
}

fn merge_ingest_file(config: &mut IngestConfig, path: &Path, source: &str) -> Result<(), String> {
    match fs::metadata(path) {
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(_) | Ok(_) => {}
    }
    let contents = fs::read_to_string(path).map_err(|error| {
        format!(
            "{source}: failed to parse {}: {}",
            path.display(),
            go_path_error("open", &path.display().to_string(), &error)
        )
    })?;
    let table = toml::from_str::<toml::Table>(&contents)
        .map_err(|error| format!("{source}: failed to parse {}: {error}", path.display()))?;
    apply_ingest_table(config, &table)
        .map_err(|error| format!("{source}: failed to apply {}: {error}", path.display()))
}

fn apply_ingest_table(config: &mut IngestConfig, table: &toml::Table) -> Result<(), String> {
    for (name, target) in [
        ("vault", IngestString::Vault),
        ("ocr_lang", IngestString::OcrLang),
        ("db_path", IngestString::DbPath),
        ("archive_path", IngestString::ArchivePath),
        ("inbox", IngestString::Inbox),
        ("paperless_base_url", IngestString::PaperlessBaseUrl),
    ] {
        if let Some(value) = table.get(name) {
            let value = value.as_str().ok_or_else(|| {
                format!(
                    "field {name:?}: cannot convert {} to string",
                    go_toml_type(value)
                )
            })?;
            set_ingest_string(config, target, value.to_owned());
        }
    }
    if let Some(value) = table.get("symseek_enabled") {
        config.symseek_enabled = go_toml_bool(value)?;
    }
    if let Some(value) = table.get("symseek_binary") {
        config.symseek_binary = value
            .as_str()
            .ok_or_else(|| {
                format!(
                    "field \"symseek_binary\": cannot convert {} to string",
                    go_toml_type(value)
                )
            })?
            .to_owned();
    }
    if let Some(value) = table.get("imap_accounts") {
        match value {
            toml::Value::Array(values) => {
                if let Some((index, _)) = values.iter().enumerate().next() {
                    if values
                        .iter()
                        .all(|value| matches!(value, toml::Value::Table(_)))
                    {
                        return Err(
                            "field \"imap_accounts\": cannot convert []map[string]interface {} to []config.IMAPAccount"
                                .to_owned(),
                        );
                    }
                    return Err(format!(
                        "field \"imap_accounts\": slice element {index}: unsupported field kind struct"
                    ));
                }
                config.imap_accounts.clear();
            }
            other => {
                return Err(format!(
                    "field \"imap_accounts\": cannot convert {} to []config.IMAPAccount",
                    go_toml_type(other)
                ));
            }
        }
    }
    if let Some(value) = table.get("imap_poll_interval") {
        config.imap_poll_interval = value
            .as_str()
            .ok_or_else(|| {
                format!(
                    "field \"imap_poll_interval\": cannot convert {} to string",
                    go_toml_type(value)
                )
            })?
            .to_owned();
    }
    if let Some(value) = table.get("ollama_base_url") {
        config.ollama_base_url = value
            .as_str()
            .ok_or_else(|| {
                format!(
                    "field \"ollama_base_url\": cannot convert {} to string",
                    go_toml_type(value)
                )
            })?
            .to_owned();
    }
    if let Some(value) = table.get("ollama_model") {
        config.ollama_model = value
            .as_str()
            .ok_or_else(|| {
                format!(
                    "field \"ollama_model\": cannot convert {} to string",
                    go_toml_type(value)
                )
            })?
            .to_owned();
    }
    Ok(())
}

#[derive(Clone, Copy)]
enum IngestString {
    Vault,
    OcrLang,
    DbPath,
    ArchivePath,
    Inbox,
    PaperlessBaseUrl,
}

fn set_ingest_string(config: &mut IngestConfig, field: IngestString, value: String) {
    match field {
        IngestString::Vault => config.vault = value,
        IngestString::OcrLang => config.ocr_lang = value,
        IngestString::DbPath => config.db_path = value,
        IngestString::ArchivePath => config.archive_path = value,
        IngestString::Inbox => config.inbox = value,
        IngestString::PaperlessBaseUrl => config.paperless_base_url = value,
    }
}

fn apply_ingest_environment(
    config: &mut IngestConfig,
    environment: &BTreeMap<String, String>,
) -> Result<(), String> {
    for (name, field) in [
        ("SYMINGEST_VAULT", IngestString::Vault),
        ("SYMINGEST_OCR_LANG", IngestString::OcrLang),
        ("SYMINGEST_DB_PATH", IngestString::DbPath),
        ("SYMINGEST_ARCHIVE_PATH", IngestString::ArchivePath),
        ("SYMINGEST_INBOX", IngestString::Inbox),
        (
            "SYMINGEST_PAPERLESS_BASE_URL",
            IngestString::PaperlessBaseUrl,
        ),
    ] {
        if let Some(value) = environment.get(name) {
            set_ingest_string(config, field, value.clone());
        }
    }
    if let Some(value) = environment.get("SYMINGEST_SYMSEEK_ENABLED") {
        let quoted = symdesk_vault::go_quote(value);
        config.symseek_enabled = parse_go_bool(value).map_err(|_| {
            format!(
                "env override error: env SYMINGEST_SYMSEEK_ENABLED: cannot parse {quoted} as bool: strconv.ParseBool: parsing {quoted}: invalid syntax"
            )
        })?;
    }
    if let Some(value) = environment.get("SYMINGEST_SYMSEEK_BINARY") {
        config.symseek_binary.clone_from(value);
    }
    if let Some(value) = environment.get("SYMINGEST_IMAP_ACCOUNTS")
        && !value.is_empty()
    {
        return Err("env override error: env SYMINGEST_IMAP_ACCOUNTS: slice element 0: unsupported field kind struct".to_owned());
    }
    if let Some(value) = environment.get("SYMINGEST_IMAP_POLL_INTERVAL") {
        config.imap_poll_interval.clone_from(value);
    }
    if let Some(value) = environment.get("SYMINGEST_OLLAMA_BASE_URL") {
        config.ollama_base_url.clone_from(value);
    }
    if let Some(value) = environment.get("SYMINGEST_OLLAMA_MODEL") {
        config.ollama_model.clone_from(value);
    }
    Ok(())
}

fn parse_go_bool(value: &str) -> Result<bool, ()> {
    match value {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Ok(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Ok(false),
        _ => Err(()),
    }
}

fn go_toml_bool(value: &toml::Value) -> Result<bool, String> {
    match value {
        toml::Value::Boolean(value) => Ok(*value),
        toml::Value::String(value) => {
            let quoted = symdesk_vault::go_quote(value);
            parse_go_bool(value).map_err(|_| {
                format!(
                    "field \"symseek_enabled\": cannot parse {quoted} as bool: strconv.ParseBool: parsing {quoted}: invalid syntax"
                )
            })
        }
        other => Err(format!(
            "field \"symseek_enabled\": cannot convert {} to bool",
            go_toml_type(other)
        )),
    }
}

fn go_toml_type(value: &toml::Value) -> &'static str {
    match value {
        toml::Value::String(_) => "string",
        toml::Value::Integer(_) => "int64",
        toml::Value::Float(_) => "float64",
        toml::Value::Boolean(_) => "bool",
        toml::Value::Datetime(_) => "toml.LocalDateTime",
        toml::Value::Array(_) => "[]interface {}",
        toml::Value::Table(_) => "map[string]interface {}",
    }
}

fn first_nonempty<'a>(primary: &'a str, fallback: &'a str) -> &'a str {
    if !primary.is_empty() {
        primary
    } else {
        fallback
    }
}

fn clean_join(components: &[&str]) -> String {
    let mut path = PathBuf::new();
    for component in components {
        path.push(component);
    }
    clean_path(&path).to_string_lossy().into_owned()
}

fn clean_path(path: &Path) -> PathBuf {
    let mut result = PathBuf::new();
    for component in path.components() {
        match component {
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => match result.components().next_back() {
                Some(std::path::Component::Normal(_)) => {
                    result.pop();
                }
                Some(std::path::Component::ParentDir) if !result.has_root() => {
                    result.push(component.as_os_str());
                }
                None if !result.has_root() => {
                    result.push(component.as_os_str());
                }
                _ => {}
            },
            other => result.push(other.as_os_str()),
        }
    }
    result
}

fn user_home(environment: &BTreeMap<String, String>) -> Option<&str> {
    #[cfg(windows)]
    let key = "USERPROFILE";
    #[cfg(not(windows))]
    let key = "HOME";
    environment
        .get(key)
        .map(String::as_str)
        .filter(|value| !value.is_empty())
}

fn home_error() -> &'static str {
    #[cfg(windows)]
    {
        "%userprofile% is not defined"
    }
    #[cfg(not(windows))]
    {
        "$HOME is not defined"
    }
}

fn go_path_error(operation: &str, path: &str, error: &io::Error) -> String {
    let kind = match error.kind() {
        io::ErrorKind::NotFound => "no such file or directory",
        io::ErrorKind::PermissionDenied => "permission denied",
        io::ErrorKind::NotADirectory => "not a directory",
        io::ErrorKind::IsADirectory => "is a directory",
        io::ErrorKind::AlreadyExists => "file exists",
        io::ErrorKind::InvalidInput => "invalid argument",
        _ => return format!("{operation} {path}: {error}"),
    };
    format!("{operation} {path}: {kind}")
}

fn render(paths: &Paths, json: bool) -> String {
    if json {
        let rendered = serde_json::to_string_pretty(paths).unwrap_or_default();
        format!("{}\n", go_escape_json(rendered))
    } else {
        format!(
            "data_dir: {}\nconfig_dir: {}\ncache_dir: {}\nsidecar: {}\nretrieval: {}\ningest: {}\ningest_archive: {}\ncontacts: {}\n",
            paths.data_dir,
            paths.config_dir,
            paths.cache_dir,
            paths.sidecar,
            paths.retrieval,
            paths.ingest,
            paths.ingest_archive,
            paths.contacts,
        )
    }
}

fn go_escape_json(value: String) -> String {
    value
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

#[cfg(test)]
mod tests {
    use std::{
        collections::BTreeMap,
        path::{Path, PathBuf},
    };

    use super::{IngestConfig, apply_ingest_environment, clean_path, parse_go_bool};

    #[test]
    fn symingest_environment_empty_values_override_config_strings() {
        let mut config = IngestConfig::defaults(&BTreeMap::new());
        config.vault = "/from-file".to_owned();
        config.db_path = "/from-file/db.sqlite".to_owned();
        let environment = BTreeMap::from([
            ("SYMINGEST_VAULT".to_owned(), String::new()),
            ("SYMINGEST_DB_PATH".to_owned(), String::new()),
        ]);
        apply_ingest_environment(&mut config, &environment).expect("empty string overrides");
        assert!(config.vault.is_empty());
        assert!(config.db_path.is_empty());
    }

    #[test]
    fn symingest_boolean_environment_uses_go_spellings() {
        for value in ["1", "t", "T", "TRUE", "true", "True"] {
            assert_eq!(parse_go_bool(value), Ok(true), "{value}");
        }
        for value in ["0", "f", "F", "FALSE", "false", "False"] {
            assert_eq!(parse_go_bool(value), Ok(false), "{value}");
        }
        assert!(parse_go_bool("yes").is_err());
    }

    #[test]
    fn clean_path_preserves_leading_parent_components() {
        assert_eq!(
            clean_path(Path::new("../../vault/archive/ingest")),
            PathBuf::from("../../vault/archive/ingest")
        );
        assert_eq!(
            clean_path(Path::new("../../alpha/../beta")),
            PathBuf::from("../../beta")
        );
        assert_eq!(
            clean_path(Path::new("/../../vault/archive/../ingest")),
            PathBuf::from("/vault/ingest")
        );
    }
}
