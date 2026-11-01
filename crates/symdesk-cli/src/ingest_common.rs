//! Shared plumbing of the ingest command family (Go `internal/ingest`
//! facade `Options.resolve`, `openStore` and Go path helpers): the symingest
//! configuration, the document store location and `filepath` semantics.
use std::path::Path;

use symdesk_core::config;
use symdesk_ingest::Store;

use crate::config_paths::{self, load_root_config};

/// Go `ingest.SchemaVersion`.
pub const SCHEMA_VERSION: u32 = 1;

/// Go `ingest.resolved`: every location filled in from the configuration and
/// the documented defaults.
pub struct Resolved {
    pub vault: String,
    pub archive: String,
    pub db_path: String,
    pub ocr_lang: String,
    pub ollama_base_url: String,
    pub ollama_model: String,
}

/// The root (symdesk) configuration with the global `--vault` flag applied:
/// Go's `cfg.Vault` after `PersistentPreRun`.
pub fn configured_vault(flag: Option<&str>) -> Result<String, String> {
    let (mut root, _) =
        load_root_config().map_err(|error| format!("failed to load config: {error}"))?;
    if let Some(flag) = flag.filter(|value| !value.is_empty()) {
        root.vault = flag.to_owned();
    }
    Ok(root.vault)
}

/// Go's `toml: line N: ...` decode errors carry a parser-specific tail; keep the
/// prefix and the line so both implementations agree on where the file breaks.
pub fn go_toml_error_text(text: &str) -> String {
    let Some(start) = text.find("TOML parse error at line ") else {
        return text.to_owned();
    };
    let rest = &text[start + "TOML parse error at line ".len()..];
    let digits: String = rest.chars().take_while(char::is_ascii_digit).collect();
    format!("{}toml: line {digits}", &text[..start])
}

/// Go `ingest.Options.resolve` for `Options{Vault: vault}`.
pub fn resolve(vault: &str) -> Result<Resolved, String> {
    let environment = config::environment_snapshot()?;
    let cwd = std::env::current_dir().unwrap_or_default();
    let loaded = config_paths::load_ingest_config_inner(&environment, &cwd).map_err(|error| {
        format!(
            "load symingest configuration: {}",
            go_toml_error_text(&error)
        )
    })?;
    let first = |a: &str, b: &str| {
        if a.is_empty() {
            b.to_owned()
        } else {
            a.to_owned()
        }
    };
    let vault = first(vault, &loaded.vault);
    let mut archive = loaded.archive_path.clone();
    if archive.is_empty() {
        archive = if vault.is_empty() {
            config::ingest_data_path(&environment, "archive")?
        } else {
            go_join(&[&vault, "archive", "ingest"])
        };
    }
    let db_path = if loaded.db_path.is_empty() {
        config::ingest_data_path(&environment, "symingest.db")?
    } else {
        loaded.db_path.clone()
    };
    let ocr_lang = first(&loaded.ocr_lang, "eng");
    Ok(Resolved {
        vault,
        archive,
        db_path,
        ocr_lang,
        ollama_base_url: loaded.ollama_base_url,
        ollama_model: loaded.ollama_model,
    })
}

/// Go `ingest.openStore`.
pub fn open_store(resolved: &Resolved) -> Result<Store, String> {
    // Go `os.MkdirAll` names the first existing ancestor that is not a directory.
    let parent = Path::new(&resolved.db_path)
        .parent()
        .unwrap_or(Path::new("."));
    for ancestor in parent.ancestors() {
        match std::fs::metadata(ancestor) {
            Ok(info) if info.is_dir() => break,
            Ok(_) => {
                return Err(format!(
                    "open document store: create store directory: mkdir {}: not a directory",
                    ancestor.display()
                ));
            }
            Err(error)
                if matches!(
                    error.kind(),
                    std::io::ErrorKind::NotFound | std::io::ErrorKind::NotADirectory
                ) => {}
            Err(_) => break,
        }
    }
    Store::open(Path::new(&resolved.db_path))
        .map_err(|error| format!("open document store: {error}"))
}

/// Go `filepath.Clean` (Unix).
pub fn go_clean(path: &str) -> String {
    if path.is_empty() {
        return ".".to_owned();
    }
    let rooted = path.starts_with('/');
    let mut parts: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|last| *last != "..") {
                    parts.pop();
                } else if !rooted {
                    parts.push("..");
                }
            }
            other => parts.push(other),
        }
    }
    let joined = parts.join("/");
    match (rooted, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".to_owned(),
        (false, false) => joined,
    }
}

/// Go `filepath.Join`.
pub fn go_join(parts: &[&str]) -> String {
    let joined = parts
        .iter()
        .filter(|part| !part.is_empty())
        .copied()
        .collect::<Vec<_>>()
        .join("/");
    if joined.is_empty() {
        String::new()
    } else {
        go_clean(&joined)
    }
}

/// Go `filepath.Abs`.
pub fn go_abs(path: &str) -> Result<String, String> {
    if path.starts_with('/') {
        return Ok(go_clean(path));
    }
    let cwd = std::env::current_dir()
        .map_err(|error| format!("getwd: {error}"))?
        .to_string_lossy()
        .into_owned();
    Ok(go_join(&[&cwd, path]))
}

/// Go `filepath.Base`.
pub fn go_base(path: &str) -> String {
    if path.is_empty() {
        return ".".to_owned();
    }
    let trimmed = path.trim_end_matches('/');
    if trimmed.is_empty() {
        return "/".to_owned();
    }
    trimmed.rsplit('/').next().unwrap_or(trimmed).to_owned()
}

/// Go `filepath.Ext`.
pub fn go_ext(path: &str) -> String {
    let base = path.rsplit('/').next().unwrap_or(path);
    base.rfind('.')
        .map_or_else(String::new, |at| base[at..].to_owned())
}
