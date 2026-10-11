#![deny(unsafe_code)]

//! Minimal SQLite sidecar index compatible with the Go oracle.

use std::{
    collections::{BTreeMap, HashSet},
    fs,
    io::{self, Read},
    path::{Component, Path, PathBuf},
    sync::{
        atomic::{AtomicBool, Ordering},
        mpsc::{self, RecvTimeoutError},
    },
    time::{Duration, Instant, UNIX_EPOCH},
};

use cap_std::{ambient_authority, fs::Dir};
use notify::{EventKind, RecursiveMode, Watcher};
use noyalib::Value;
use rusqlite::{Connection, OptionalExtension, Transaction, params, params_from_iter};
use serde::Deserialize;
use symdesk_vault::Document;
use thiserror::Error;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

mod backup;
mod dataset_purge;
mod dataset_sync;
mod history_sync;
pub mod index_status;
mod metadata;
mod retrieval;
mod retrieval_config;
mod retrieval_markdown;
pub mod retrieval_quant_sidecar;
mod search_plan;

pub use backup::{backup_database, relocate_database, restore_database};
pub use dataset_purge::{DatasetPurgeError, DatasetPurgeService};
pub use dataset_sync::{
    DatasetImportOptions, DatasetImportResult, DatasetSyncError, DatasetSyncOptions,
    DatasetSyncResult, DatasetSyncRow, DatasetSyncService,
};
pub use history_sync::{HistorySyncError, checkpoint_undo, history_restore};
pub use metadata::{
    METADATA_FILE_NAME, encode_sidecar_metadata, encode_sidecar_metadata_at, open_for_vault,
    record_sidecar_metadata,
};
pub use retrieval::{
    RetrievalAnchor, RetrievalChunk, RetrievalDb, RetrievalDocument, RetrievalEmbeddingSpaceCount,
    RetrievalHybridSearchChunk, RetrievalHybridSearchResponse, RetrievalHybridSearchResult,
    RetrievalSearchChunk, RetrievalSearchResult, RetrievalSection, RetrievalStatusSnapshot,
    RetrievalVectorSearchChunk, RetrievalVectorSearchResult, SearchSource, SourceRegistry,
    StoredRetrievalChunk, go_simple_lowercase, local_hash_embedding, materialize_chunks,
};
pub use retrieval_config::{
    RetrievalEmbeddingConfig, index_location_for_vault, open_retrieval_for_vault,
    relocate_index_for_vault, retrieval_embedding_config, store_retrieval_path_for_vault,
    symseek_config_path,
};
pub use retrieval_markdown::{
    MAX_RETRIEVAL_SOURCE_BYTES, parse_markdown_retrieval_sections, parse_text_retrieval_sections,
};
pub use search_plan::SearchPlanResponse;

const MIGRATIONS: &[(&str, &str)] = &[
    ("001_init", include_str!("../migrations/001_init.sql")),
    (
        "002_doc_metadata",
        include_str!("../migrations/002_doc_metadata.sql"),
    ),
    ("003_asn", include_str!("../migrations/003_asn.sql")),
    (
        "004_file_stat_cache",
        include_str!("../migrations/004_file_stat_cache.sql"),
    ),
    ("005_type", include_str!("../migrations/005_type.sql")),
    (
        "006_links_to_path_index",
        include_str!("../migrations/006_links_to_path_index.sql"),
    ),
    (
        "007_created_at",
        include_str!("../migrations/007_created_at.sql"),
    ),
    (
        "008_index_lifecycle",
        include_str!("../migrations/008_index_lifecycle.sql"),
    ),
    (
        "009_german_norm",
        include_str!("../migrations/009_german_norm.sql"),
    ),
    (
        "010_german_trigram",
        include_str!("../migrations/010_german_trigram.sql"),
    ),
    (
        "011_dataset_rows",
        include_str!("../migrations/011_dataset_rows.sql"),
    ),
];

const MAX_INDEX_BATCH_SIZE: usize = 200;
const MAX_EXTERNAL_TEXT_FILE_SIZE: u64 = 10 << 20;
const FTS_MATCH_JOIN: &str = r#" JOIN (
    SELECT rowid, MAX(rank) AS rank, MAX(snip) AS snip, MAX(body) AS body FROM (
        SELECT rowid, rank, snippet(fts_search, 1, '', '', '...', 64) AS snip, body FROM fts_search WHERE fts_search MATCH ?
        UNION ALL
        SELECT rowid, NULL, NULL, NULL FROM fts_norm WHERE fts_norm MATCH ?
        UNION ALL
        SELECT rowid, NULL, NULL, NULL FROM fts_tri WHERE fts_tri MATCH ?
    ) GROUP BY rowid
) sm ON sm.rowid = f.id"#;

#[derive(Debug, Error)]
pub enum SidecarError {
    #[error(transparent)]
    Sqlite(#[from] rusqlite::Error),
    #[error(transparent)]
    Io(#[from] std::io::Error),
    #[error(transparent)]
    Vault(#[from] symdesk_vault::VaultError),
    #[error(transparent)]
    Path(#[from] symdesk_vault::SecurePathError),
    #[error("sql: database is closed")]
    Closed,
    #[error("{0}")]
    Contract(String),
    #[error("non-UTF-8 {context}: {path:?}")]
    NonUtf8Path {
        context: &'static str,
        path: PathBuf,
    },
    #[error("time conversion failed: {0}")]
    Time(String),
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct IndexedDocument {
    pub path: String,
    pub sha256: String,
    pub title: String,
    pub body: String,
    pub created_at: String,
    pub modified_at: String,
    pub document_type: String,
    pub document_date: Option<String>,
    pub person: Option<String>,
    pub status: Option<String>,
    pub due_date: Option<String>,
    pub confidence: Option<i64>,
    pub ocr_json_path: Option<String>,
    pub simhash: Option<String>,
    pub asn: Option<i64>,
    pub size: Option<i64>,
    pub mtime_ns: Option<i64>,
    pub properties: BTreeMap<String, String>,
    pub links: Vec<String>,
    pub derived: bool,
}

impl IndexedDocument {
    /// Converts the read-only vault document into the persisted sidecar shape.
    ///
    /// # Errors
    /// Returns an error when the supplied nanosecond timestamp is outside the supported range.
    pub fn from_vault(document: &Document, mtime_ns: Option<i64>) -> Result<Self, SidecarError> {
        let modified_at = if let Some(value) = mtime_ns {
            OffsetDateTime::from_unix_timestamp_nanos(i128::from(value))
                .map_err(|error| SidecarError::Time(error.to_string()))?
                .format(&Rfc3339)
                .map_err(|error| SidecarError::Time(error.to_string()))?
        } else if !document.created.is_empty() {
            document.created.clone()
        } else {
            OffsetDateTime::now_utc()
                .format(&Rfc3339)
                .map_err(|error| SidecarError::Time(error.to_string()))?
        };
        let mut properties = BTreeMap::new();
        for (key, value) in &document.frontmatter {
            if key != "tags" && key != "aliases" {
                properties.insert(key.clone(), go_value(value));
            }
        }
        if !document.tags.is_empty() {
            properties.insert("tags".to_owned(), format!("[{}]", document.tags.join(" ")));
        } else if let Some(value) = document.frontmatter.get("tags") {
            properties.insert("tags".to_owned(), go_value(value));
        }
        if !document.aliases.is_empty() {
            properties.insert(
                "aliases".to_owned(),
                document
                    .aliases
                    .iter()
                    .map(|alias| format!("- {alias}"))
                    .collect::<Vec<_>>()
                    .join("\n"),
            );
        } else if let Some(value) = document.frontmatter.get("aliases") {
            properties.insert("aliases".to_owned(), go_value(value));
        }
        let simhash = if document.simhash.is_empty() && !document.body.is_empty() {
            Some(symdesk_core::simhash::compute_hex(&document.body))
        } else {
            optional_string(&document.simhash)
        };
        Ok(Self {
            path: document.path.clone(),
            sha256: document.sha256.clone(),
            title: document.title.clone(),
            body: document.body.clone(),
            created_at: document.created.clone(),
            modified_at,
            document_type: document.document_type.clone(),
            document_date: optional_string(&document.document_date),
            person: optional_string(&document.person),
            status: optional_string(&document.status),
            due_date: optional_string(&document.due_date),
            confidence: (document.confidence != 0).then_some(document.confidence),
            ocr_json_path: optional_string(&document.ocr_json_path),
            simhash,
            asn: document.asn,
            size: Some(document.size),
            mtime_ns,
            properties,
            links: document.links.clone(),
            derived: document.derived || !document.derived_from.is_empty(),
        })
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SearchHit {
    pub path: String,
    pub title: String,
    pub snippet: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ListedDocument {
    pub path: String,
    pub title: String,
    pub modified_at: String,
    pub document_type: String,
}

/// One materialized row in the rebuildable dataset sidecar.
pub type DatasetRow = symdesk_vault::dataset::SidecarRow;

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq)]
pub struct DatasetQueryFilter {
    pub key: String,
    #[serde(default)]
    pub operator: String,
    #[serde(default)]
    pub value: String,
}

pub use symdesk_vault::FilterGroup as DatasetQueryFilterGroup;

#[derive(Clone, Debug, PartialEq)]
pub struct DatasetGroupCountRow {
    pub group_value: serde_json::Value,
    pub count: i64,
}

#[derive(Clone, Debug, PartialEq)]
pub struct DatasetGroupCountResult {
    pub rows: Vec<DatasetGroupCountRow>,
    pub total_groups: usize,
    pub limit: usize,
    pub capped: bool,
}

pub struct Sidecar {
    connection: Connection,
    closed: bool,
}

/// Resolves the per-vault sidecar path used by the Go implementation.
///
/// # Errors
/// Returns an error when the vault path, home directory, or digest input cannot
/// be represented as UTF-8.
pub fn path_for_vault(vault_root: &Path) -> Result<PathBuf, SidecarError> {
    let root = vault_root
        .to_str()
        .ok_or_else(|| SidecarError::NonUtf8Path {
            context: "vault root",
            path: vault_root.to_path_buf(),
        })?;
    let environment =
        symdesk_core::config::environment_snapshot().map_err(SidecarError::Contract)?;
    let cwd = std::env::current_dir()?;
    let temp_root = std::env::temp_dir();
    sidecar_path_for_vault(root, &environment, &cwd, &temp_root)
}

/// Resolves the effective sidecar path without opening or creating it.
/// A nonblank `SYMDESK_SIDECAR` takes precedence; an empty vault resolves to
/// the shared standalone sidecar rather than a hash of the current directory.
pub fn sidecar_path_for_vault(
    vault_root: &str,
    environment: &BTreeMap<String, String>,
    cwd: &Path,
    temp_root: &Path,
) -> Result<PathBuf, SidecarError> {
    if let Some(explicit) = environment
        .get("SYMDESK_SIDECAR")
        .map(String::as_str)
        .map(str::trim)
        .filter(|value| !value.is_empty())
    {
        return Ok(PathBuf::from(explicit));
    }
    store_sidecar_path_for_vault(vault_root, environment, cwd, temp_root)
}

/// Resolves the sidecar path used by the initial unified-store preflight.
/// Unlike [`sidecar_path_for_vault`], this intentionally ignores the legacy
/// sidecar override, matching `config.ResolveStorePaths` in Go.
pub fn store_sidecar_path_for_vault(
    vault_root: &str,
    environment: &BTreeMap<String, String>,
    cwd: &Path,
    temp_root: &Path,
) -> Result<PathBuf, SidecarError> {
    if vault_root.trim().is_empty() {
        let data_home = sidecar_trimmed_environment(environment, "XDG_DATA_HOME")
            .map(str::to_owned)
            .or_else(|| {
                sidecar_user_home(environment).map(|home| {
                    PathBuf::from(home)
                        .join(".local/share")
                        .to_string_lossy()
                        .into_owned()
                })
            })
            .ok_or_else(|| SidecarError::Contract(sidecar_home_error().to_owned()))?;
        return Ok(lexical_clean(
            &Path::new(&data_home).join("symdesk/sidecar.db"),
        ));
    }

    let supplied = sidecar_absolute_clean(Path::new(vault_root), cwd);
    let canonical = fs::canonicalize(&supplied).unwrap_or(supplied);
    let canonical = absolute_non_verbatim(&canonical)?;
    let explicit_data_home =
        sidecar_trimmed_environment(environment, "XDG_DATA_HOME").map(str::to_owned);
    let root = sidecar_storage_root(
        explicit_data_home.as_deref(),
        sidecar_user_home(environment).map(PathBuf::from),
        &canonical,
        temp_root,
    )?;
    let digest = symdesk_vault::sha256_hex(canonical.to_string_lossy().as_bytes());
    Ok(lexical_clean(&root.join(&digest[..16]).join("sidecar.db")))
}

fn dataset_query_filter_where(
    filters: &[DatasetQueryFilter],
    schema: &BTreeMap<String, String>,
) -> Result<(String, Vec<rusqlite::types::Value>), SidecarError> {
    let mut expressions = Vec::with_capacity(filters.len());
    let mut arguments = Vec::new();
    for filter in filters {
        let key = filter.key.trim();
        let pseudo = matches!(key, "identity" | "_identity" | "_key");
        let typ = if pseudo {
            "text"
        } else {
            schema
                .get(key)
                .filter(|value| !value.is_empty())
                .map(String::as_str)
                .ok_or_else(|| {
                    SidecarError::Contract(format!("dataset column {key:?} not found"))
                })?
        };
        let (raw, raw_args) = match key {
            "identity" | "_identity" => ("identity".to_owned(), Vec::new()),
            "_key" => ("row_key".to_owned(), Vec::new()),
            _ => (
                "json_extract(values_json, ?)".to_owned(),
                vec![rusqlite::types::Value::Text(dataset_json_path(key))],
            ),
        };
        let (present, present_args) = match key {
            "identity" | "_identity" => ("identity IS NOT NULL".to_owned(), Vec::new()),
            "_key" => ("row_key IS NOT NULL".to_owned(), Vec::new()),
            _ => (
                "json_type(values_json, ?) IS NOT NULL".to_owned(),
                vec![rusqlite::types::Value::Text(dataset_json_path(key))],
            ),
        };
        let numeric = ["number", "integer", "float"]
            .iter()
            .any(|name| typ.eq_ignore_ascii_case(name));
        let date = ["date", "datetime"]
            .iter()
            .any(|name| typ.eq_ignore_ascii_case(name));
        let typed = if numeric {
            format!("CAST({raw} AS REAL)")
        } else if date {
            format!("julianday({raw})")
        } else {
            format!("LOWER(CAST({raw} AS TEXT))")
        };
        let value = filter.value.trim();
        match filter.operator.trim().to_ascii_lowercase().as_str() {
            "" | "is" | "=" | "==" | "equals" if value.is_empty() => {
                expressions.push(format!(
                    "NOT ({present}) OR {raw} IS NULL OR CAST({raw} AS TEXT) = ''"
                ));
                arguments.extend(present_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
            }
            "" | "is" | "=" | "==" | "equals" => {
                if date {
                    expressions.push(format!("{present} AND julianday({raw}) = julianday(?)"));
                } else if numeric {
                    expressions.push(format!("{present} AND {typed} = CAST(? AS REAL)"));
                } else {
                    expressions.push(format!("{present} AND {typed} = LOWER(?)"));
                }
                arguments.extend(present_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
                arguments.push(rusqlite::types::Value::Text(if numeric || date {
                    value.to_owned()
                } else {
                    value.to_lowercase()
                }));
            }
            "not_equals" | "is_not" | "!=" => {
                if date {
                    expressions.push(format!(
                        "(NOT ({present}) OR NOT (julianday({raw}) = julianday(?)))"
                    ));
                } else if numeric {
                    expressions.push(format!(
                        "(NOT ({present}) OR NOT ({typed} = CAST(? AS REAL)))"
                    ));
                } else {
                    expressions.push(format!("(NOT ({present}) OR NOT ({typed} = LOWER(?)))"));
                }
                arguments.extend(present_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
                arguments.push(rusqlite::types::Value::Text(if numeric || date {
                    value.to_owned()
                } else {
                    value.to_lowercase()
                }));
            }
            "is_empty" | "empty" => {
                expressions.push(format!(
                    "NOT ({present}) OR {raw} IS NULL OR CAST({raw} AS TEXT) = ''"
                ));
                arguments.extend(present_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
            }
            "is_not_empty" | "not_empty" => {
                expressions.push(format!(
                    "{present} AND {raw} IS NOT NULL AND CAST({raw} AS TEXT) <> ''"
                ));
                arguments.extend(present_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
            }
            "contains" | "not_contains" | "starts_with" | "prefix" | "ends_with" | "suffix" => {
                let pattern = match filter.operator.trim().to_ascii_lowercase().as_str() {
                    "starts_with" | "prefix" => format!("{value}%"),
                    "ends_with" | "suffix" => format!("%{value}"),
                    _ => format!("%{value}%"),
                };
                let match_expression = format!("LOWER(CAST({raw} AS TEXT)) LIKE LOWER(?)");
                if filter.operator.trim().eq_ignore_ascii_case("not_contains") {
                    expressions.push(format!("NOT ({present} AND {match_expression})"));
                } else {
                    expressions.push(format!("{present} AND {match_expression}"));
                }
                arguments.extend(present_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
                arguments.push(rusqlite::types::Value::Text(pattern));
            }
            "greater_than" | "gt" | ">" => {
                if date {
                    expressions.push(format!("{present} AND julianday({raw}) > julianday(?)"));
                } else {
                    let cast = if numeric { "REAL" } else { "TEXT" };
                    expressions.push(format!("{present} AND {typed} > CAST(? AS {cast})"));
                }
                arguments.extend(present_args.iter().cloned());
                arguments.extend(raw_args.iter().cloned());
                arguments.push(rusqlite::types::Value::Text(value.to_owned()));
            }
            operator => {
                return Err(SidecarError::Contract(format!(
                    "unsupported dataset filter operator {operator:?}"
                )));
            }
        }
    }
    Ok((
        expressions
            .into_iter()
            .map(|expression| format!("({expression})"))
            .collect::<Vec<_>>()
            .join(" AND "),
        arguments,
    ))
}

fn dataset_json_path(key: &str) -> String {
    format!(r#"$."{}""#, key.replace('"', r#"\""#))
}

fn dataset_sql_value_to_json(value: rusqlite::types::Value) -> serde_json::Value {
    match value {
        rusqlite::types::Value::Null => serde_json::Value::Null,
        rusqlite::types::Value::Integer(value) => serde_json::json!(value),
        rusqlite::types::Value::Real(value) => serde_json::Number::from_f64(value)
            .map(serde_json::Value::Number)
            .unwrap_or(serde_json::Value::Null),
        rusqlite::types::Value::Text(value) => serde_json::Value::String(value),
        rusqlite::types::Value::Blob(value) => {
            serde_json::Value::String(String::from_utf8_lossy(&value).into_owned())
        }
    }
}

fn sidecar_trimmed_environment<'a>(
    environment: &'a BTreeMap<String, String>,
    key: &str,
) -> Option<&'a str> {
    environment
        .get(key)
        .map(|value| value.trim())
        .filter(|value| !value.is_empty())
}

fn sidecar_user_home(environment: &BTreeMap<String, String>) -> Option<&str> {
    #[cfg(windows)]
    let key = "USERPROFILE";
    #[cfg(not(windows))]
    let key = "HOME";
    environment
        .get(key)
        .map(String::as_str)
        .filter(|value| !value.is_empty())
}

fn sidecar_absolute_clean(path: &Path, cwd: &Path) -> PathBuf {
    let absolute = if path.is_absolute() {
        path.to_path_buf()
    } else {
        cwd.join(path)
    };
    lexical_clean(&absolute)
}

fn sidecar_home_error() -> &'static str {
    #[cfg(windows)]
    {
        "user home dir: %userprofile% is not defined"
    }
    #[cfg(not(windows))]
    {
        "user home dir: $HOME is not defined"
    }
}

fn sidecar_storage_root(
    explicit_data_home: Option<&str>,
    home: Option<PathBuf>,
    canonical_vault: &Path,
    temp_root: &Path,
) -> Result<PathBuf, SidecarError> {
    let data_home = explicit_data_home
        .map(PathBuf::from)
        .or_else(|| home.map(|path| path.join(".local/share")))
        .ok_or_else(|| SidecarError::Contract(sidecar_home_error().to_owned()))?;
    let mut root = data_home.join("symdesk/vaults");
    let canonical_temp_root =
        fs::canonicalize(temp_root).unwrap_or_else(|_| temp_root.to_path_buf());
    if explicit_data_home.is_none()
        && canonical_vault.starts_with(&canonical_temp_root)
        && canonical_vault != canonical_temp_root
    {
        root = temp_root.join("symdesk/test-vaults");
    }
    Ok(root)
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct FileStat {
    pub size: i64,
    pub mtime_ns: i64,
}

impl Sidecar {
    /// Opens the SQLite sidecar, applies all Go-compatible migrations and repairs the norm index.
    ///
    /// # Errors
    /// Returns filesystem, SQLite, migration or backfill errors without destructive recovery.
    pub fn open(path: &Path) -> Result<Self, SidecarError> {
        if let Some(parent) = path.parent() {
            create_parent_dir(parent)?;
        }
        let mut connection =
            symaira_core_sqlite::open_with_existing_parent(path).map_err(|error| match error {
                symaira_core_sqlite::Error::Open(err) => SidecarError::Sqlite(err),
                other => SidecarError::Contract(other.to_string()),
            })?;
        migrate(&mut connection)?;
        backfill_norm_index(&mut connection)?;
        Ok(Self {
            connection,
            closed: false,
        })
    }

    /// Runs SQLite's non-destructive integrity check.
    ///
    /// # Errors
    /// Returns the provider error or a non-`ok` integrity result.
    pub fn check_integrity(&self) -> Result<(), SidecarError> {
        if self.closed {
            return Err(SidecarError::Closed);
        }
        let result: String = self
            .connection
            .query_row("PRAGMA integrity_check", [], |row| row.get(0))?;
        if result == "ok" {
            Ok(())
        } else {
            Err(SidecarError::Contract(format!(
                "integrity check failed: {result}"
            )))
        }
    }

    /// Writes an atomic, WAL-consistent SQLite snapshot of this open sidecar.
    ///
    /// # Errors
    /// Returns an error when the sidecar is closed, in-memory, or the snapshot
    /// cannot be created, validated, or atomically installed.
    pub fn backup_to(&self, destination: &Path) -> Result<(), SidecarError> {
        if self.closed {
            return Err(SidecarError::Closed);
        }
        backup_database(&self.connection, destination)
    }

    /// Closes the actual SQLite connection. Subsequent dataset operations fail
    /// with the same stable closed-database diagnostic as the Go sidecar.
    ///
    /// # Errors
    /// Returns the SQLite close error and restores the original connection when
    /// SQLite refuses to close it.
    pub fn close(&mut self) -> Result<(), SidecarError> {
        if self.closed {
            return Ok(());
        }
        let placeholder = Connection::open_in_memory()?;
        let connection = std::mem::replace(&mut self.connection, placeholder);
        match connection.close() {
            Ok(()) => {
                self.closed = true;
                Ok(())
            }
            Err((connection, error)) => {
                self.connection = connection;
                Err(SidecarError::Sqlite(error))
            }
        }
    }

    /// Atomically replaces every derived row for one dataset.
    ///
    /// # Errors
    /// Returns validation, JSON or SQLite errors and rolls back the transaction.
    pub fn replace_dataset_rows(
        &mut self,
        dataset_slug: &str,
        rows: &[DatasetRow],
    ) -> Result<(), SidecarError> {
        let rows = rows
            .iter()
            .map(|row| symdesk_vault::dataset::bytes::SidecarRow {
                dataset_slug: row.dataset_slug.clone(),
                row_key: row.row_key.as_bytes().to_vec(),
                identity: row.identity.as_bytes().to_vec(),
                values_json: row.values_json.clone(),
                source_path: row.source_path.clone(),
                row_number: row.row_number,
            })
            .collect::<Vec<_>>();
        self.replace_dataset_rows_bytes(dataset_slug, &rows)
    }

    /// Replaces derived import rows, preserving Go string bytes as SQLite TEXT.
    ///
    /// # Errors
    /// Returns validation or SQLite errors and rolls back the transaction.
    pub fn replace_dataset_rows_bytes(
        &mut self,
        dataset_slug: &str,
        rows: &[symdesk_vault::dataset::bytes::SidecarRow],
    ) -> Result<(), SidecarError> {
        if self.closed {
            return Err(SidecarError::Closed);
        }
        if dataset_slug.trim().is_empty() {
            return Err(SidecarError::Contract(
                "dataset slug is required".to_owned(),
            ));
        }
        let transaction = self.connection.transaction()?;
        transaction.execute(
            "DELETE FROM dataset_rows WHERE dataset_slug = ?",
            [dataset_slug],
        )?;
        for row in rows {
            let row_slug = if row.dataset_slug.is_empty() {
                dataset_slug
            } else {
                row.dataset_slug.as_str()
            };
            if row_slug != dataset_slug || row.row_key.is_empty() {
                return Err(SidecarError::Contract(
                    "invalid dataset row identity".to_owned(),
                ));
            }
            if serde_json::from_str::<serde_json::Value>(&row.values_json).is_err() {
                return Err(SidecarError::Contract(format!(
                    "dataset row {:?} has invalid values JSON",
                    symdesk_vault::dataset::bytes::text(&row.row_key)
                )));
            }
            let row_number = i64::try_from(row.row_number).map_err(|_| {
                SidecarError::Contract("dataset row number exceeds SQLite integer range".to_owned())
            })?;
            transaction.execute(
                "INSERT INTO dataset_rows(dataset_slug,row_key,identity,values_json,source_path,row_number) VALUES (?,?,?,?,?,?)",
                params![
                    row_slug,
                    rusqlite::types::ToSqlOutput::Borrowed(rusqlite::types::ValueRef::Text(&row.row_key)),
                    (!row.identity.is_empty()).then_some(rusqlite::types::ToSqlOutput::Borrowed(rusqlite::types::ValueRef::Text(&row.identity))),
                    row.values_json,
                    row.source_path,
                    row_number
                ],
            )?;
        }
        transaction.commit()?;
        Ok(())
    }

    /// Returns materialized rows in deterministic key order.
    ///
    /// # Errors
    /// Returns the stable closed-database diagnostic or SQLite query errors.
    pub fn dataset_rows(&self, dataset_slug: &str) -> Result<Vec<DatasetRow>, SidecarError> {
        if self.closed {
            return Err(SidecarError::Closed);
        }
        let mut statement = self.connection.prepare(
            "SELECT dataset_slug,row_key,COALESCE(identity,''),values_json,source_path,row_number FROM dataset_rows WHERE dataset_slug = ? ORDER BY row_key",
        )?;
        let rows = statement.query_map([dataset_slug], |row| {
            let row_number: i64 = row.get(5)?;
            Ok(DatasetRow {
                dataset_slug: row.get(0)?,
                row_key: row.get(1)?,
                identity: row.get(2)?,
                values_json: row.get(3)?,
                source_path: row.get(4)?,
                row_number: usize::try_from(row_number).map_err(|error| {
                    rusqlite::Error::FromSqlConversionFailure(
                        5,
                        rusqlite::types::Type::Integer,
                        Box::new(error),
                    )
                })?,
            })
        })?;
        rows.collect::<Result<Vec<_>, _>>().map_err(Into::into)
    }

    /// Reads raw import keys and identities without lossy decoding or BLOB casts.
    ///
    /// # Errors
    /// Returns the closed-database diagnostic or SQLite query/type errors.
    pub fn dataset_rows_bytes(
        &self,
        dataset_slug: &str,
    ) -> Result<Vec<symdesk_vault::dataset::bytes::SidecarRow>, SidecarError> {
        if self.closed {
            return Err(SidecarError::Closed);
        }
        let mut statement = self.connection.prepare("SELECT dataset_slug,row_key,COALESCE(identity,''),values_json,source_path,row_number FROM dataset_rows WHERE dataset_slug = ? ORDER BY row_key")?;
        let rows = statement.query_map([dataset_slug], |row| {
            let number: i64 = row.get(5)?;
            Ok(symdesk_vault::dataset::bytes::SidecarRow {
                dataset_slug: row.get(0)?,
                row_key: match row.get_ref(1)? {
                    rusqlite::types::ValueRef::Text(bytes) => bytes.to_vec(),
                    other => {
                        return Err(rusqlite::Error::InvalidColumnType(
                            1,
                            "row_key".to_owned(),
                            other.data_type(),
                        ));
                    }
                },
                identity: match row.get_ref(2)? {
                    rusqlite::types::ValueRef::Text(bytes) => bytes.to_vec(),
                    other => {
                        return Err(rusqlite::Error::InvalidColumnType(
                            2,
                            "identity".to_owned(),
                            other.data_type(),
                        ));
                    }
                },
                values_json: row.get(3)?,
                source_path: row.get(4)?,
                row_number: usize::try_from(number).map_err(|error| {
                    rusqlite::Error::FromSqlConversionFailure(
                        5,
                        rusqlite::types::Type::Integer,
                        Box::new(error),
                    )
                })?,
            })
        })?;
        rows.collect::<Result<Vec<_>, _>>().map_err(Into::into)
    }

    /// Returns an ordered, capped grouped row count projection.
    ///
    /// # Errors
    /// Returns a contract error for an empty dataset or group column, the stable
    /// closed-database diagnostic, or SQLite query errors.
    pub fn dataset_group_count(
        &self,
        dataset_slug: &str,
        group_by: &str,
        limit: usize,
    ) -> Result<DatasetGroupCountResult, SidecarError> {
        if self.closed {
            return Err(SidecarError::Closed);
        }
        if dataset_slug.trim().is_empty() {
            return Err(SidecarError::Contract(
                "dataset slug is required".to_owned(),
            ));
        }
        if group_by.trim().is_empty() {
            return Err(SidecarError::Contract(
                "dataset group column is required".to_owned(),
            ));
        }
        let path = dataset_json_path(group_by);
        let expression = "json_extract(values_json, ?)";
        let total: i64 = self.connection.query_row(
            &format!(
                "SELECT COUNT(*) FROM (SELECT {expression} FROM dataset_rows WHERE dataset_slug = ? GROUP BY {expression})"
            ),
            params![path, dataset_slug, path],
            |row| row.get(0),
        )?;
        let limit = if limit == 0 { 10 } else { limit.min(1000) };
        let mut statement = self.connection.prepare(&format!(
            "SELECT {expression}, COUNT(*) FROM dataset_rows WHERE dataset_slug = ? GROUP BY {expression} ORDER BY {expression} ASC LIMIT ?"
        ))?;
        let groups = statement.query_map(
            params![
                path,
                dataset_slug,
                path,
                path,
                i64::try_from(limit).unwrap_or(1000)
            ],
            |row| {
                Ok((
                    row.get::<_, rusqlite::types::Value>(0)?,
                    row.get::<_, i64>(1)?,
                ))
            },
        )?;
        let mut rows = Vec::with_capacity(limit.min(usize::try_from(total).unwrap_or(usize::MAX)));
        for group in groups {
            let (value, count) = group?;
            rows.push(DatasetGroupCountRow {
                group_value: dataset_sql_value_to_json(value),
                count,
            });
        }
        let total_groups = usize::try_from(total).unwrap_or(usize::MAX);
        let capped = rows.len() < total_groups;
        Ok(DatasetGroupCountResult {
            rows,
            total_groups,
            limit,
            capped,
        })
    }

    /// Returns one key-ordered page of dataset rows and the uncapped total.
    ///
    /// # Errors
    /// Returns the stable closed-database diagnostic or SQLite query errors.
    pub fn dataset_query_page(
        &self,
        dataset_slug: &str,
        limit: usize,
    ) -> Result<(usize, Vec<DatasetRow>), SidecarError> {
        self.dataset_query_page_filtered(dataset_slug, &BTreeMap::new(), &[], limit)
    }

    /// Returns a bounded, key-ordered page and total matching structured filters.
    ///
    /// # Errors
    /// Returns a contract error for an unknown column or unsupported operator,
    /// the stable closed-database diagnostic, or SQLite query errors.
    pub fn dataset_query_page_filtered(
        &self,
        dataset_slug: &str,
        schema: &BTreeMap<String, String>,
        filters: &[DatasetQueryFilter],
        limit: usize,
    ) -> Result<(usize, Vec<DatasetRow>), SidecarError> {
        self.dataset_query_page_filtered_with_group(dataset_slug, schema, filters, None, limit)
    }

    /// Returns a bounded page and total matching flat filters and a nested group.
    pub fn dataset_query_page_filtered_with_group(
        &self,
        dataset_slug: &str,
        schema: &BTreeMap<String, String>,
        filters: &[DatasetQueryFilter],
        filter_group: Option<&DatasetQueryFilterGroup>,
        limit: usize,
    ) -> Result<(usize, Vec<DatasetRow>), SidecarError> {
        if self.closed {
            return Err(SidecarError::Closed);
        }
        let (mut where_sql, mut where_args) = dataset_query_filter_where(filters, schema)?;
        if let Some(group) = filter_group {
            let (group_sql, group_args) = dataset_query_filter_group_where(group, schema)?;
            if !where_sql.is_empty() {
                where_sql.push_str(" AND ");
            }
            where_sql.push_str(&group_sql);
            where_args.extend(group_args);
        }
        let where_sql = if where_sql.is_empty() {
            String::new()
        } else {
            format!(" AND {where_sql}")
        };
        let total: i64 = self.connection.query_row(
            &format!("SELECT COUNT(*) FROM dataset_rows WHERE dataset_slug = ?{where_sql}"),
            params_from_iter(
                std::iter::once(rusqlite::types::Value::Text(dataset_slug.to_owned()))
                    .chain(where_args.iter().cloned()),
            ),
            |row| row.get(0),
        )?;
        let limit = i64::try_from(limit.min(1000)).unwrap_or(i64::MAX);
        let mut statement = self.connection.prepare(
            &format!("SELECT dataset_slug,row_key,COALESCE(identity,''),values_json,source_path,row_number FROM dataset_rows WHERE dataset_slug = ?{where_sql} ORDER BY row_key LIMIT ?"),
        )?;
        let rows = statement.query_map(
            params_from_iter(
                std::iter::once(rusqlite::types::Value::Text(dataset_slug.to_owned()))
                    .chain(where_args)
                    .chain(std::iter::once(rusqlite::types::Value::Integer(limit))),
            ),
            |row| {
                let row_number: i64 = row.get(5)?;
                Ok(DatasetRow {
                    dataset_slug: row.get(0)?,
                    row_key: row.get(1)?,
                    identity: row.get(2)?,
                    values_json: row.get(3)?,
                    source_path: row.get(4)?,
                    row_number: usize::try_from(row_number).map_err(|error| {
                        rusqlite::Error::FromSqlConversionFailure(
                            5,
                            rusqlite::types::Type::Integer,
                            Box::new(error),
                        )
                    })?,
                })
            },
        )?;
        Ok((
            usize::try_from(total).unwrap_or(usize::MAX),
            rows.collect::<Result<_, _>>()?,
        ))
    }

    /// Deletes only the rebuildable rows for one dataset.
    ///
    /// # Errors
    /// Returns the stable closed-database diagnostic or SQLite errors.
    pub fn delete_dataset(&self, dataset_slug: &str) -> Result<(), SidecarError> {
        if self.closed {
            return Err(SidecarError::Closed);
        }
        self.connection.execute(
            "DELETE FROM dataset_rows WHERE dataset_slug = ?",
            [dataset_slug],
        )?;
        Ok(())
    }

    /// Indexes one document in its own transaction.
    ///
    /// # Errors
    /// Returns validation or SQLite errors and rolls the transaction back.
    pub fn index_document(&mut self, document: &IndexedDocument) -> Result<(), SidecarError> {
        if document.derived {
            return self.delete_document(&document.path);
        }
        let transaction = self.connection.transaction()?;
        index_document_tx(&transaction, document)?;
        transaction.commit()?;
        Ok(())
    }

    /// Indexes documents in Go-compatible batches of 200.
    ///
    /// # Errors
    /// A failing document commits earlier documents from its current batch and stops processing.
    pub fn index_documents(&mut self, documents: &[IndexedDocument]) -> Result<(), SidecarError> {
        for batch in documents.chunks(MAX_INDEX_BATCH_SIZE) {
            let transaction = self.connection.transaction()?;
            for document in batch {
                let result = if document.derived {
                    delete_document_rows(&transaction, &document.path)
                } else {
                    index_document_tx(&transaction, document)
                };
                if let Err(error) = result {
                    transaction.commit()?;
                    return Err(SidecarError::Contract(format!(
                        "index {}: {error}",
                        document.path
                    )));
                }
            }
            transaction.commit()?;
        }
        Ok(())
    }

    /// Removes one document and all outgoing derived rows. Missing paths are a successful no-op.
    ///
    /// # Errors
    /// Returns SQLite errors and rolls the transaction back.
    pub fn delete_document(&mut self, path: &str) -> Result<(), SidecarError> {
        let transaction = self.connection.transaction()?;
        delete_document_rows(&transaction, path)?;
        transaction.commit()?;
        Ok(())
    }

    /// Indexes Markdown and bounded raw-text files from a canonical source root.
    pub fn refresh_external_source(&mut self, source_root: &Path) -> Result<(), SidecarError> {
        let root = fs::canonicalize(source_root)?;
        if !root.is_dir() {
            return Err(SidecarError::Contract(
                "source path is not a directory".to_owned(),
            ));
        }
        validate_utf8_path(&root, "external source root")?;
        // Database keys deliberately omit Windows' verbatim prefix, so all
        // comparisons against persisted paths must use the same root spelling.
        let key_root = absolute_non_verbatim(&root)?;
        let source_dir = open_vault_dir(&root)?;
        let mut batch = Vec::with_capacity(MAX_INDEX_BATCH_SIZE);
        let mut found = HashSet::new();
        for entry in symdesk_vault::walk_all(&root)? {
            if entry.entry_type != symdesk_vault::WalkEntryType::File {
                continue;
            }
            let relative = entry.path;
            let key = key_root.join(&relative);
            let raw_text = is_external_raw_text(&relative);
            if raw_text && source_dir.metadata(&relative)?.len() > MAX_EXTERNAL_TEXT_FILE_SIZE {
                continue;
            }
            let markdown = relative.extension().and_then(|value| value.to_str()) == Some("md");
            if !raw_text && !markdown {
                continue;
            }
            found.insert(validate_utf8_path(&key, "external source storage key")?.to_owned());
            if raw_text {
                self.refresh_path(
                    &source_dir,
                    &root,
                    &relative,
                    Some(MAX_EXTERNAL_TEXT_FILE_SIZE),
                    true,
                    &mut batch,
                )?;
            } else {
                self.refresh_path(&source_dir, &root, &relative, None, false, &mut batch)?;
            }
        }
        self.flush_refresh_batch(&mut batch)?;
        let indexed: Vec<String> = {
            let mut statement = self.connection.prepare("SELECT path FROM files")?;
            let rows = statement.query_map([], |row| row.get(0))?;
            rows.collect::<Result<_, _>>()?
        };
        for path in indexed {
            if (Path::new(&path).starts_with(&key_root) || Path::new(&path).starts_with(&root))
                && !found.contains(&path)
            {
                self.delete_document(&path)?;
            }
        }
        Ok(())
    }

    /// Indexes a registered external source, then keeps it current until stopped.
    ///
    /// Watcher setup and initial indexing errors are returned. Later event and
    /// refresh errors are reported to stderr while watching continues.
    pub fn watch_external_source(
        &mut self,
        source_root: &Path,
        stop: &AtomicBool,
    ) -> Result<(), SidecarError> {
        let root = fs::canonicalize(source_root)?;
        if !root.is_dir() {
            return Err(SidecarError::Contract(
                "source path is not a directory".to_owned(),
            ));
        }
        validate_utf8_path(&root, "external source root")?;
        let key_root = absolute_non_verbatim(&root)?;
        let (sender, receiver) = mpsc::channel();
        let mut watcher = notify::recommended_watcher(move |event| {
            let _ = sender.send(event);
        })
        .map_err(|error| {
            SidecarError::Contract(format!("failed to create file watcher: {error}"))
        })?;
        watcher
            .watch(&root, RecursiveMode::Recursive)
            .map_err(|error| {
                SidecarError::Contract(format!("failed to setup watchers: {error}"))
            })?;

        eprintln!("Performing initial sync for: {}", root.display());
        self.refresh_external_source(&root)
            .map_err(|error| SidecarError::Contract(format!("initial sync failed: {error}")))?;
        let indexed_paths: Vec<String> = {
            let mut statement = self.connection.prepare("SELECT path FROM files")?;
            let rows = statement.query_map([], |row| row.get(0))?;
            rows.collect::<Result<_, _>>()?
        };
        let file_count = indexed_paths
            .iter()
            .filter(|path| {
                Path::new(path).starts_with(&key_root) || Path::new(path).starts_with(&root)
            })
            .count();
        eprintln!("Watching {file_count} files in {}", root.display());

        let debounce = Duration::from_millis(500);
        let poll_interval = Duration::from_millis(100);
        let mut deadline = None;
        loop {
            if stop.load(Ordering::SeqCst) {
                return Ok(());
            }
            let timeout = deadline
                .map(|at: Instant| {
                    at.saturating_duration_since(Instant::now())
                        .min(poll_interval)
                })
                .unwrap_or(poll_interval);
            match receiver.recv_timeout(timeout) {
                Ok(Ok(event)) => {
                    if matches!(
                        event.kind,
                        EventKind::Create(_) | EventKind::Modify(_) | EventKind::Remove(_)
                    ) {
                        deadline = Some(Instant::now() + debounce);
                    }
                }
                Ok(Err(error)) => eprintln!("Watcher error: {error}"),
                Err(RecvTimeoutError::Timeout) => {
                    if deadline.is_some_and(|at| Instant::now() >= at) {
                        deadline = None;
                        if let Err(error) = self.refresh_external_source(&root) {
                            if fs::metadata(&root).is_err_and(|source_error| {
                                source_error.kind() == io::ErrorKind::NotFound
                            }) {
                                if let Err(prune_error) = self.remove_external_source(&root) {
                                    eprintln!("Incremental re-index error: {prune_error}");
                                }
                            } else {
                                eprintln!("Incremental re-index error: {error}");
                            }
                        }
                    }
                }
                Err(RecvTimeoutError::Disconnected) => {
                    return Err(SidecarError::Contract(
                        "watcher event channel closed".to_owned(),
                    ));
                }
            }
        }
    }

    /// Deletes only index rows rooted under an external source.
    pub fn remove_external_source(&mut self, source_root: &Path) -> Result<usize, SidecarError> {
        let root = fs::canonicalize(source_root).unwrap_or_else(|_| source_root.to_path_buf());
        let key_root = absolute_non_verbatim(&root)?;
        let paths: Vec<String> = {
            let mut statement = self.connection.prepare("SELECT path FROM files")?;
            let rows = statement.query_map([], |row| row.get(0))?;
            rows.collect::<Result<_, _>>()?
        };
        let paths = paths
            .into_iter()
            .filter(|path| {
                Path::new(path).starts_with(&key_root) || Path::new(path).starts_with(&root)
            })
            .collect::<Vec<_>>();
        let removed = paths.len();
        for path in paths {
            self.delete_document(&path)?;
        }
        Ok(removed)
    }

    /// Refreshes the index from lowercase-extension Markdown files under `vault_root`.
    ///
    /// The vault root is opened once as a capability directory. Each file read
    /// uses a capability-opened handle; its metadata and bytes therefore refer
    /// to the same filesystem object. Paths containing non-UTF-8 components are
    /// rejected instead of being lossy-converted into database keys.
    ///
    /// The size/mtime cache is checked before reading each file. A matching
    /// cache entry is an exact no-read/no-write fast path. When the cache is
    /// stale, an equal SHA-256 updates only the cached stat; changed content
    /// is queued for a batched full index. Refresh never removes files that
    /// disappeared from the vault; call [`Self::prune`] explicitly for that.
    ///
    /// # Errors
    /// Returns the first filesystem, parser or SQLite error after flushing
    /// documents already queued before a later walk or parse error.
    pub fn refresh_index(&mut self, vault_root: &Path) -> Result<(), SidecarError> {
        self.refresh_index_inner(vault_root, false)
    }

    /// Refreshes the Markdown index while recording the per-file lifecycle
    /// states emitted by the Go `symdesk index` command.
    ///
    /// # Errors
    /// Returns the first filesystem, parser or SQLite error after flushing
    /// documents already queued before a later walk or parse error.
    pub fn refresh_index_for_cli(&mut self, vault_root: &Path) -> Result<(), SidecarError> {
        self.refresh_index_inner(vault_root, true)
    }

    fn refresh_index_inner(
        &mut self,
        vault_root: &Path,
        record_lifecycle: bool,
    ) -> Result<(), SidecarError> {
        validate_utf8_path(vault_root, "vault root")?;
        let canonical_root = fs::canonicalize(vault_root)?;
        if record_lifecycle {
            for entry in symdesk_vault::walk_all(vault_root)? {
                let Some(extension) = entry.path.extension().and_then(|value| value.to_str())
                else {
                    continue;
                };
                let Some(reason) = unsupported_index_reason(extension) else {
                    continue;
                };
                let key = storage_path(vault_root, &entry.path)?.key_path;
                let key = key.to_str().ok_or_else(|| SidecarError::NonUtf8Path {
                    context: "storage key",
                    path: key.clone(),
                })?;
                self.set_lifecycle_state(key, "unsupported", reason)?;
            }
        }
        let vault_dir = open_vault_dir(vault_root)?;
        let mut batch = Vec::with_capacity(MAX_INDEX_BATCH_SIZE);
        let mut callback_error = None;
        let walk_result = symdesk_vault::walk_markdown_with(vault_root, |relative| {
            let candidate = vault_root.join(relative);
            if fs::symlink_metadata(&candidate)?.file_type().is_symlink()
                && fs::canonicalize(&candidate)
                    .map_or(true, |target| !target.starts_with(&canonical_root))
            {
                // Go's RefreshIndex skips links that cannot be opened through
                // the vault root, while keeping contained symlinks indexable.
                return Ok(());
            }
            let storage_key = match storage_path(vault_root, relative) {
                Ok(path) => path.key_path,
                Err(error) => {
                    callback_error = Some(error);
                    return Err(io::Error::other("refresh index callback failed"));
                }
            };
            let key = storage_key
                .to_str()
                .ok_or_else(|| SidecarError::NonUtf8Path {
                    context: "storage key",
                    path: storage_key.clone(),
                });
            let key = match key {
                Ok(key) => key,
                Err(error) => {
                    callback_error = Some(error);
                    return Err(io::Error::other("refresh index callback failed"));
                }
            };
            if record_lifecycle && let Err(error) = self.set_lifecycle_state(key, "indexing", "") {
                callback_error = Some(error);
                return Err(io::Error::other("refresh index callback failed"));
            }
            let result =
                self.refresh_path(&vault_dir, vault_root, relative, None, false, &mut batch);
            if let Err(error) = result {
                if record_lifecycle {
                    let _ = self.set_lifecycle_state(key, "failed", &error.to_string());
                }
                callback_error = Some(error);
                return Err(io::Error::other("refresh index callback failed"));
            }
            if record_lifecycle && let Err(error) = self.set_lifecycle_state(key, "indexed", "") {
                callback_error = Some(error);
                return Err(io::Error::other("refresh index callback failed"));
            }
            Ok(())
        });

        self.flush_refresh_batch(&mut batch)?;
        if let Some(error) = callback_error {
            return Err(error);
        }
        walk_result.map_err(Into::into)
    }

    fn set_lifecycle_state(
        &self,
        path: &str,
        state: &str,
        reason: &str,
    ) -> Result<(), SidecarError> {
        self.connection.execute(
            "INSERT INTO index_lifecycle(path, state, reason, updated_at) VALUES (?1, ?2, ?3, ?4) ON CONFLICT(path) DO UPDATE SET state=excluded.state, reason=excluded.reason, updated_at=excluded.updated_at",
            params![path, state, reason, OffsetDateTime::now_utc().format(&Rfc3339).map_err(|error| SidecarError::Time(error.to_string()))?],
        )?;
        Ok(())
    }

    fn refresh_path(
        &mut self,
        vault_dir: &Dir,
        vault_root: &Path,
        relative: &Path,
        max_file_size: Option<u64>,
        raw_text: bool,
        batch: &mut Vec<IndexedDocument>,
    ) -> Result<(), SidecarError> {
        let storage_path = storage_path(vault_root, relative)?;
        let path_string =
            storage_path
                .key_path
                .to_str()
                .ok_or_else(|| SidecarError::NonUtf8Path {
                    context: "storage key",
                    path: storage_path.key_path.clone(),
                })?;
        // The fast path needs only capability-scoped metadata; opening the
        // file is deferred until it must be read so unreadable unchanged files
        // remain a no-read/no-write success.
        let metadata = vault_dir.metadata(relative)?;
        if max_file_size.is_some_and(|limit| metadata.len() > limit) {
            return self.delete_document(path_string);
        }
        let mtime_ns = system_time_unix_nanos(metadata.modified()?.into_std())?;
        let file_size = i64::try_from(metadata.len()).unwrap_or(i64::MAX);
        if let Some(cached) = self.stat_cache(path_string)?
            && cached.size == file_size
            && cached.mtime_ns == mtime_ns
        {
            return Ok(());
        }

        // Keep metadata and bytes tied to the same capability-opened handle.
        let mut file = vault_dir.open(relative)?;
        let metadata = file.metadata()?;
        if max_file_size.is_some_and(|limit| metadata.len() > limit) {
            return self.delete_document(path_string);
        }
        let mtime_ns = system_time_unix_nanos(metadata.modified()?.into_std())?;
        let mut bytes = Vec::new();
        if let Some(limit) = max_file_size {
            file.take(limit + 1).read_to_end(&mut bytes)?;
            if bytes.len() as u64 > limit {
                return self.delete_document(path_string);
            }
        } else {
            file.read_to_end(&mut bytes)?;
        }
        let document = if raw_text {
            external_raw_text_document(path_string, &bytes)
        } else {
            symdesk_vault::parse_bytes(path_string, &bytes)?
        };
        if document.derived {
            return self.delete_document(path_string);
        }
        let indexed = self.is_indexed(path_string, &document.sha256)?;
        if indexed {
            self.set_file_stat(path_string, document.size, mtime_ns)?;
            return Ok(());
        }

        let indexed_document = IndexedDocument::from_vault(&document, Some(mtime_ns))?;
        batch.push(indexed_document);
        if batch.len() == MAX_INDEX_BATCH_SIZE {
            self.flush_refresh_batch(batch)?;
        }
        Ok(())
    }

    fn flush_refresh_batch(
        &mut self,
        batch: &mut Vec<IndexedDocument>,
    ) -> Result<(), SidecarError> {
        if batch.is_empty() {
            return Ok(());
        }
        let documents = std::mem::take(batch);
        self.index_documents(&documents)
    }

    /// Returns the cached on-disk size and Unix nanosecond mtime for a path.
    /// `None` means the path is absent or was indexed without a reliable stat.
    ///
    /// # Errors
    /// Returns SQLite query errors.
    pub fn stat_cache(&self, path: &str) -> Result<Option<FileStat>, SidecarError> {
        let value: Option<(Option<i64>, Option<i64>)> = self
            .connection
            .query_row(
                "SELECT size, mtime_ns FROM files WHERE path = ?",
                [path],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .optional()?;
        Ok(value.and_then(|(size, mtime_ns)| match (size, mtime_ns) {
            (Some(size), Some(mtime_ns)) => Some(FileStat { size, mtime_ns }),
            _ => None,
        }))
    }

    /// Reports whether a path is indexed with the supplied SHA-256.
    ///
    /// # Errors
    /// Returns SQLite query errors.
    pub fn is_indexed(&self, path: &str, sha256: &str) -> Result<bool, SidecarError> {
        self.connection
            .query_row("SELECT sha256 FROM files WHERE path = ?", [path], |row| {
                row.get::<_, String>(0)
            })
            .optional()
            .map(|value| value.as_deref() == Some(sha256))
            .map_err(Into::into)
    }

    /// Updates only the cached size and Unix nanosecond mtime for a path.
    ///
    /// # Errors
    /// Returns SQLite update errors.
    pub fn set_file_stat(&self, path: &str, size: i64, mtime_ns: i64) -> Result<(), SidecarError> {
        self.connection.execute(
            "UPDATE files SET size = ?, mtime_ns = ? WHERE path = ?",
            params![size, mtime_ns, path],
        )?;
        Ok(())
    }

    /// Removes indexed documents and lifecycle diagnostics absent from the
    /// vault. It never modifies Markdown sources and is not called by refresh.
    ///
    /// The root and every discovered entry are capability-opened, and
    /// non-UTF-8 root, relative, or storage-key paths are rejected explicitly.
    ///
    /// # Errors
    /// Returns walk or SQLite errors without partially claiming success.
    pub fn prune(&mut self, vault_root: &Path) -> Result<usize, SidecarError> {
        validate_utf8_path(vault_root, "vault root")?;
        let vault_dir = open_vault_dir(vault_root)?;
        let markdown = symdesk_vault::walk_markdown(vault_root)?;
        let mut valid_documents = std::collections::BTreeSet::new();
        for relative in markdown {
            let storage_path = storage_path(vault_root, &relative)?;
            // Open before accepting the key, and parse from that same handle.
            let mut file = vault_dir.open(&relative)?;
            let path_string =
                storage_path
                    .key_path
                    .to_str()
                    .ok_or_else(|| SidecarError::NonUtf8Path {
                        context: "storage key",
                        path: storage_path.key_path.clone(),
                    })?;
            let mut bytes = Vec::new();
            file.read_to_end(&mut bytes)?;
            // Go keeps malformed files valid for pruning; only a successfully
            // parsed derived document is intentionally absent from the set.
            let is_derived = symdesk_vault::parse_bytes(path_string, &bytes)
                .ok()
                .is_some_and(|document| document.derived);
            if !is_derived {
                valid_documents.insert(path_string.to_owned());
            }
        }

        let stale_files = self.stale_paths("SELECT path FROM files", &valid_documents)?;
        let stale_statuses = self.stale_lifecycle_paths(&vault_dir, vault_root)?;
        if stale_files.is_empty() && stale_statuses.is_empty() {
            return Ok(0);
        }
        let transaction = self.connection.transaction()?;
        for path in &stale_files {
            delete_document_rows(&transaction, path)?;
        }
        for path in &stale_statuses {
            transaction.execute("DELETE FROM index_lifecycle WHERE path = ?", [path])?;
        }
        transaction.commit()?;
        Ok(stale_files.len() + stale_statuses.len())
    }

    /// Removes one lifecycle diagnostic row. Missing paths are a no-op.
    ///
    /// # Errors
    /// Returns SQLite errors.
    pub fn delete_index_status(&self, path: &str) -> Result<(), SidecarError> {
        self.connection
            .execute("DELETE FROM index_lifecycle WHERE path = ?", [path])?;
        Ok(())
    }

    fn stale_paths(
        &self,
        query: &str,
        valid: &std::collections::BTreeSet<String>,
    ) -> Result<Vec<String>, SidecarError> {
        let mut statement = self.connection.prepare(query)?;
        let rows = statement.query_map([], |row| row.get::<_, String>(0))?;
        let mut stale = Vec::new();
        for path in rows {
            let path = path?;
            if !valid.contains(&path) {
                stale.push(path);
            }
        }
        Ok(stale)
    }

    fn stale_lifecycle_paths(
        &self,
        vault_dir: &Dir,
        vault_root: &Path,
    ) -> Result<Vec<String>, SidecarError> {
        let entries = symdesk_vault::walk_all(vault_root)?;
        let mut valid = std::collections::BTreeSet::new();
        for entry in entries {
            let storage_path = storage_path(vault_root, &entry.path)?;
            // Do not validate a lifecycle row from a path that was only seen
            // by the ambient directory walker; open the discovered entry via
            // the root capability first.
            let _file = vault_dir.open(&entry.path)?;
            let key = storage_path
                .key_path
                .to_str()
                .ok_or_else(|| SidecarError::NonUtf8Path {
                    context: "storage key",
                    path: storage_path.key_path.clone(),
                })?;
            valid.insert(key.to_owned());
        }
        self.stale_paths("SELECT path FROM index_lifecycle", &valid)
    }

    /// Lists indexed files in path order, optionally restricted to a prefix.
    ///
    /// The stored `files.path` values are absolute, so a caller-supplied
    /// vault-relative prefix (for example `nested`) is resolved against
    /// `vault_root` first. Resolving to the same form the absolute prefix
    /// produces is what the Go oracle does, so both implementations return the
    /// same rows for `--dir nested` and `--dir <vault>/nested`.
    ///
    /// # Errors
    /// Returns SQLite query errors.
    pub fn list_files(
        &self,
        vault_root: &Path,
        dir_prefix: &str,
    ) -> Result<Vec<ListedDocument>, SidecarError> {
        let resolved = resolve_list_prefix(vault_root, dir_prefix);
        let mut sql = String::from(
            "SELECT path, title, COALESCE(modified_at, ''), COALESCE(\"type\", '') FROM files",
        );
        if !resolved.is_empty() {
            sql.push_str(" WHERE path LIKE ?");
        }
        sql.push_str(" ORDER BY path ASC");
        let mut statement = self.connection.prepare(&sql)?;
        if resolved.is_empty() {
            let rows = statement.query_map([], |row| {
                Ok(ListedDocument {
                    path: row.get(0)?,
                    title: row.get(1)?,
                    modified_at: row.get(2)?,
                    document_type: row.get(3)?,
                })
            })?;
            return rows
                .collect::<Result<Vec<_>, rusqlite::Error>>()
                .map_err(Into::into);
        }
        let rows = statement.query_map([format!("{resolved}%")], |row| {
            Ok(ListedDocument {
                path: row.get(0)?,
                title: row.get(1)?,
                modified_at: row.get(2)?,
                document_type: row.get(3)?,
            })
        })?;
        rows.collect::<Result<Vec<_>, rusqlite::Error>>()
            .map_err(Into::into)
    }

    /// Returns an indexed title, preferring the exact stored path and falling
    /// back to the unprefixed key for a canonical Windows path.
    ///
    /// # Errors
    /// Returns an error when no row exists or SQLite cannot execute the query.
    pub fn get_title(&self, path: &str) -> Result<String, SidecarError> {
        let storage_key = strip_verbatim_prefix(path);
        self.connection
            .query_row(
                "SELECT title FROM files WHERE path IN (?1, ?2) ORDER BY (path = ?1) DESC LIMIT 1",
                [path, storage_key.as_str()],
                |row| row.get(0),
            )
            .map_err(Into::into)
    }

    ///
    /// # Errors
    /// Returns SQLite syntax/provider errors.
    pub fn search(&self, query: &str) -> Result<Vec<SearchHit>, SidecarError> {
        self.search_impl(query, None, None)
    }

    /// Executes basic FTS inside an exact path allowlist. An empty scope never widens.
    ///
    /// # Errors
    /// Returns SQLite syntax/provider errors.
    pub fn search_scoped(
        &self,
        query: &str,
        allowed_paths: &[String],
    ) -> Result<Vec<SearchHit>, SidecarError> {
        if allowed_paths.is_empty() {
            return Ok(Vec::new());
        }
        self.search_impl(query, Some(allowed_paths), None)
    }

    /// Executes basic FTS only within the supplied filesystem roots.
    pub fn search_in_roots(
        &self,
        query: &str,
        roots: &[String],
    ) -> Result<Vec<SearchHit>, SidecarError> {
        if roots.is_empty() {
            return Ok(Vec::new());
        }
        self.search_impl(query, None, Some(roots))
    }

    /// Searches the active vault and its registered external roots.
    pub fn search_with_sources(
        &self,
        vault_root: &Path,
        query: &str,
    ) -> Result<Vec<SearchHit>, SidecarError> {
        let roots = self.search_roots(vault_root)?;
        self.search_in_roots(query, &roots)
    }

    fn search_roots(&self, vault_root: &Path) -> Result<Vec<String>, SidecarError> {
        let vault = fs::canonicalize(vault_root)?;
        let registry = SourceRegistry::open(&vault)?;
        let lexical_vault = absolute_non_verbatim(vault_root)?;
        let mut roots = vec![lexical_vault.to_string_lossy().into_owned()];
        if vault != lexical_vault {
            roots.push(vault.to_string_lossy().into_owned());
        }
        roots.extend(registry.list()?.into_iter().map(|source| source.path));
        Ok(roots)
    }

    /// Executes the shared query language against the vault and registered
    /// external roots. Unlike Go's unscoped `DB.SearchPlan`, results are
    /// restricted to this vault's registry allowlist.
    /// Invalid syntax follows Go's plain-text fallback and includes its hint.
    ///
    /// # Errors
    /// Returns SQLite or filesystem errors from searching the sidecar.
    pub fn search_plan(
        &self,
        vault_root: &Path,
        query: &str,
    ) -> Result<SearchPlanResponse, SidecarError> {
        if query.trim().is_empty() {
            return Ok(SearchPlanResponse {
                results: Vec::new(),
                hint: None,
            });
        }
        let roots = self.search_roots(vault_root)?;
        let plan = match symdesk_core::query::parse(query) {
            Ok(plan) => plan,
            Err(_) => {
                return Ok(SearchPlanResponse {
                    results: self.search_in_roots(query, &roots)?,
                    hint: Some(search_plan::INVALID_SYNTAX_HINT),
                });
            }
        };
        search_plan::search_plan(&self.connection, &roots, &plan)
    }

    fn search_impl(
        &self,
        raw_query: &str,
        allowed_paths: Option<&[String]>,
        allowed_roots: Option<&[String]>,
    ) -> Result<Vec<SearchHit>, SidecarError> {
        let query = symdesk_core::german::fts_query(raw_query);
        if query.is_empty() {
            return Ok(Vec::new());
        }
        let trigram = symdesk_core::german::trigram_query(raw_query);
        let mut sql =
            format!("SELECT f.path, f.title, COALESCE(sm.snip, '') FROM files f{FTS_MATCH_JOIN}");
        let mut arguments = vec![query.clone(), query, trigram];
        if let Some(paths) = allowed_paths {
            sql.push_str(" WHERE f.path IN (");
            sql.push_str(&vec!["?"; paths.len()].join(","));
            sql.push(')');
            arguments.extend(paths.iter().cloned());
        }
        if let Some(roots) = allowed_roots {
            sql.push_str(if allowed_paths.is_some() {
                " AND ("
            } else {
                " WHERE ("
            });
            for (index, root) in roots.iter().enumerate() {
                if index != 0 {
                    sql.push_str(" OR ");
                }
                sql.push_str("f.path = ? OR substr(f.path, 1, length(?)) = ?");
                let root_with_separator = format!(
                    "{}{}",
                    root.trim_end_matches(['/', '\\']),
                    std::path::MAIN_SEPARATOR
                );
                arguments.extend([
                    root.clone(),
                    root_with_separator.clone(),
                    root_with_separator,
                ]);
            }
            sql.push(')');
        }
        sql.push_str(" ORDER BY sm.rank IS NULL, sm.rank LIMIT 20");
        let mut statement = self.connection.prepare(&sql)?;
        let rows = statement.query_map(params_from_iter(arguments), |row| {
            Ok(SearchHit {
                path: row.get(0)?,
                title: row.get(1)?,
                snippet: row.get(2)?,
            })
        })?;
        rows.collect::<Result<Vec<_>, _>>().map_err(Into::into)
    }
}

fn dataset_query_filter_group_where(
    group: &DatasetQueryFilterGroup,
    schema: &BTreeMap<String, String>,
) -> Result<(String, Vec<rusqlite::types::Value>), SidecarError> {
    let mut expressions = Vec::with_capacity(group.filters.len() + group.groups.len());
    let mut arguments = Vec::new();
    for filter in &group.filters {
        let filter = DatasetQueryFilter {
            key: filter.key.clone(),
            operator: filter.operator.clone(),
            value: filter.value.clone(),
        };
        let (expression, filter_args) = dataset_query_filter_where(&[filter], schema)?;
        expressions.push(expression);
        arguments.extend(filter_args);
    }
    for child in &group.groups {
        let (expression, child_args) = dataset_query_filter_group_where(child, schema)?;
        expressions.push(expression);
        arguments.extend(child_args);
    }
    if expressions.is_empty() {
        return Ok(("1".to_owned(), arguments));
    }
    let joiner = if group.operator.trim().eq_ignore_ascii_case("any") {
        " OR "
    } else {
        " AND "
    };
    Ok((format!("({})", expressions.join(joiner)), arguments))
}

fn unsupported_index_reason(extension: &str) -> Option<&'static str> {
    match extension.to_ascii_lowercase().as_str() {
        "mobi" => Some("no bundled MOBI parser; DRM status cannot be determined"),
        "azw3" => Some("no bundled AZW3 parser; DRM status cannot be determined"),
        "pages" | "key" | "numbers" => Some("iWork bundle parser is not available"),
        "doc" => Some("legacy binary Office parser is not available"),
        "xls" => Some("legacy binary Office parser is not available"),
        "ppt" => Some("legacy binary Office parser is not available"),
        "djvu" => Some("DjVu parser is not available"),
        "odg" => Some("OpenDocument drawing parser is not available"),
        _ => None,
    }
}

fn is_external_raw_text(path: &Path) -> bool {
    path.extension()
        .and_then(|value| value.to_str())
        .is_some_and(|extension| {
            matches!(
                extension.to_ascii_lowercase().as_str(),
                "txt" | "text" | "go" | "py" | "js" | "ts" | "json" | "yaml" | "yml" | "sh" | "css"
            )
        })
}

fn external_raw_text_document(path: &str, bytes: &[u8]) -> Document {
    Document {
        path: path.to_owned(),
        sha256: symdesk_vault::sha256_hex(bytes),
        title: Path::new(path)
            .file_stem()
            .and_then(|value| value.to_str())
            .unwrap_or(path)
            .to_owned(),
        created: String::new(),
        tags: Vec::new(),
        aliases: Vec::new(),
        frontmatter: BTreeMap::new(),
        yaml_timestamps: BTreeMap::new(),
        body: String::from_utf8_lossy(bytes).into_owned(),
        links: Vec::new(),
        size: i64::try_from(bytes.len()).unwrap_or(i64::MAX),
        document_date: String::new(),
        person: String::new(),
        status: String::new(),
        due_date: String::new(),
        confidence: 0,
        ocr_json_path: String::new(),
        simhash: String::new(),
        asn: None,
        document_type: String::new(),
        derived_from: String::new(),
        derived: false,
    }
}

#[derive(Debug, Eq, PartialEq)]
struct ValidatedStoragePath {
    /// Canonical/verbatim path retained for filesystem operations.
    io_path: PathBuf,
    /// Ordinary absolute path used for database keys and logical document paths.
    key_path: PathBuf,
}

fn open_vault_dir(vault_root: &Path) -> Result<Dir, SidecarError> {
    validate_utf8_path(vault_root, "vault root")?;
    Dir::open_ambient_dir(vault_root, ambient_authority()).map_err(Into::into)
}

/// Resolves a caller-supplied directory prefix into the absolute prefix that
/// `list_files` compares against the stored `files.path` values.
///
/// Mirrors the Go oracle's `Service.listPrefix`: a vault-relative prefix is
/// joined onto the vault root, then cleaned. No canonicalization happens here
/// because this port stores `files.path` in the caller's root spelling (see
/// `storage_path`), so the prefix must use that same spelling to match; the
/// rendered listing is relative either way, so both implementations emit the
/// same rows for `--dir nested` and `--dir <vault>/nested`.
///
/// The flag is documented as a prefix, so no separator is appended: a relative
/// prefix matches exactly what the equivalent absolute prefix already matched.
fn resolve_list_prefix(vault_root: &Path, dir_prefix: &str) -> String {
    let trimmed = dir_prefix.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    let requested = Path::new(trimmed);
    let absolute = if requested.is_absolute() {
        requested.to_path_buf()
    } else {
        vault_root.join(requested)
    };
    let cleaned = lexical_clean(&absolute);
    match cleaned.to_str() {
        Some(value) => strip_verbatim_prefix(value),
        // A non-UTF-8 prefix can never match a stored UTF-8 path, so it
        // resolves to a value that matches nothing rather than widening.
        None => String::new(),
    }
}

fn validate_utf8_path<'a>(path: &'a Path, context: &'static str) -> Result<&'a str, SidecarError> {
    path.to_str().ok_or_else(|| SidecarError::NonUtf8Path {
        context,
        path: path.to_path_buf(),
    })
}

fn storage_path(vault_root: &Path, relative: &Path) -> Result<ValidatedStoragePath, SidecarError> {
    let relative_string = validate_utf8_path(relative, "relative path")?;
    let io_path = symdesk_vault::secure_path(vault_root, relative_string)?;
    // Go filepath.Walk keys are derived from the caller's root, not from a
    // canonicalized root. Keep that spelling, while removing Windows' verbatim
    // prefix so it remains the ordinary storage key expected by the sidecar.
    let key_path = absolute_non_verbatim(vault_root)?.join(relative);
    validate_utf8_path(&key_path, "storage key")?;
    Ok(ValidatedStoragePath { io_path, key_path })
}

/// Builds the ordinary absolute path key used by `refresh_index` for a
/// vault-relative document path.
///
/// This is useful when a caller indexes bytes it has just written: filesystem
/// operations may use a canonical Windows verbatim root (`\\?\C:\...`),
/// while persisted sidecar keys intentionally omit that prefix to match Go's
/// `filepath.Join` keys.
///
/// # Errors
/// Returns an error when the path is outside the vault or the root/path cannot
/// be represented as a UTF-8 sidecar key.
pub fn vault_document_path(vault_root: &Path, relative: &Path) -> Result<PathBuf, SidecarError> {
    if relative.as_os_str().is_empty()
        || relative
            .components()
            .any(|component| !matches!(component, Component::Normal(_)))
    {
        return Err(SidecarError::Contract(
            "document path must contain only vault-relative normal components".to_owned(),
        ));
    }
    let normalized_relative: PathBuf = relative.components().collect();
    Ok(storage_path(vault_root, &normalized_relative)?.key_path)
}

fn absolute_non_verbatim(path: &Path) -> Result<PathBuf, SidecarError> {
    validate_utf8_path(path, "vault root")?;
    let absolute = if path.is_absolute() {
        path.to_path_buf()
    } else {
        std::env::current_dir()?.join(path)
    };
    let cleaned = lexical_clean(&absolute);
    let cleaned = validate_utf8_path(&cleaned, "storage key")?;
    Ok(PathBuf::from(strip_verbatim_prefix(cleaned)))
}

fn strip_verbatim_prefix(path: &str) -> String {
    const VERBATIM_PREFIX: &str = r"\\?";
    const VERBATIM_UNC_PREFIX: &str = r"\\?\UNC";
    if let Some(rest) = path.strip_prefix(VERBATIM_UNC_PREFIX) {
        let rest = rest.strip_prefix('\\').unwrap_or(rest);
        format!("\\\\{rest}")
    } else if let Some(rest) = path.strip_prefix(VERBATIM_PREFIX) {
        rest.strip_prefix('\\').unwrap_or(rest).to_owned()
    } else {
        path.to_owned()
    }
}

fn lexical_clean(path: &Path) -> PathBuf {
    let mut output = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                let _ = output.pop();
            }
            other => output.push(other.as_os_str()),
        }
    }
    output
}

fn create_parent_dir(parent: &Path) -> io::Result<()> {
    if parent.as_os_str().is_empty() {
        return Ok(());
    }

    #[cfg(unix)]
    {
        use std::os::unix::fs::{DirBuilderExt as _, MetadataExt as _};

        // Match sqlitekit.SafeMkdirAll: normalize to an absolute path and
        // inspect every existing component before descending. Root-owned
        // aliases such as /var -> /private/var remain supported; a symlink
        // owned by an ordinary user is never followed. The restrictive mode
        // is supplied at mkdir time, so no later chmod can be redirected by a
        // symlink swap.
        let absolute = if parent.is_absolute() {
            parent.to_path_buf()
        } else {
            std::env::current_dir()?.join(parent)
        };
        let absolute = lexical_clean(&absolute);
        let mut current = PathBuf::from(std::path::MAIN_SEPARATOR_STR);
        for component in absolute.components() {
            match component {
                Component::RootDir | Component::CurDir => continue,
                Component::Normal(name) => current.push(name),
                Component::ParentDir | Component::Prefix(_) => {
                    return Err(io::Error::new(
                        io::ErrorKind::InvalidInput,
                        "parent directory is not absolute after normalization",
                    ));
                }
            }

            loop {
                match fs::symlink_metadata(&current) {
                    Ok(metadata) if metadata.file_type().is_symlink() => {
                        if metadata.uid() != 0 {
                            return Err(io::Error::other(format!(
                                "refusing non-root parent-directory symlink: {}",
                                current.display()
                            )));
                        }
                        current = fs::canonicalize(&current)?;
                        break;
                    }
                    Ok(metadata) if metadata.is_dir() => break,
                    Ok(_) => {
                        return Err(io::Error::new(
                            io::ErrorKind::NotADirectory,
                            format!("parent path is not a directory: {}", current.display()),
                        ));
                    }
                    Err(error) if error.kind() == io::ErrorKind::NotFound => {
                        match fs::DirBuilder::new().mode(0o700).create(&current) {
                            Ok(()) => break,
                            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
                            Err(error) => return Err(error),
                        }
                    }
                    Err(error) => return Err(error),
                }
            }
        }

        Ok(())
    }

    #[cfg(not(unix))]
    {
        fs::create_dir_all(parent)
    }
}

fn system_time_unix_nanos(value: std::time::SystemTime) -> Result<i64, SidecarError> {
    let nanos = match value.duration_since(UNIX_EPOCH) {
        Ok(duration) => i128::try_from(duration.as_nanos()).unwrap_or(i128::MAX),
        Err(error) => -i128::try_from(error.duration().as_nanos()).unwrap_or(i128::MAX),
    };
    i64::try_from(nanos)
        .map_err(|_| SidecarError::Time("mtime is outside i64 nanoseconds".to_owned()))
}

fn migrate(connection: &mut Connection) -> Result<(), SidecarError> {
    connection.execute_batch(
        "CREATE TABLE IF NOT EXISTS schema_migrations (\n\t\tversion TEXT PRIMARY KEY,\n\t\tapplied_at DATETIME DEFAULT CURRENT_TIMESTAMP\n\t)",
    )?;
    for (version, sql) in MIGRATIONS {
        let applied: bool = connection.query_row(
            "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)",
            [version],
            |row| row.get(0),
        )?;
        if applied {
            continue;
        }
        let transaction = connection.transaction()?;
        transaction.execute_batch(sql)?;
        transaction.execute(
            "INSERT INTO schema_migrations(version) VALUES (?)",
            [version],
        )?;
        transaction.commit()?;
    }
    Ok(())
}

fn backfill_norm_index(connection: &mut Connection) -> Result<(), SidecarError> {
    let missing: i64 = connection.query_row(
        "SELECT COUNT(*) FROM fts_search WHERE rowid NOT IN (SELECT rowid FROM fts_norm)",
        [],
        |row| row.get(0),
    )?;
    if missing == 0 {
        return Ok(());
    }
    let pending = {
        let mut statement = connection.prepare(
            "SELECT rowid, title, body FROM fts_search WHERE rowid NOT IN (SELECT rowid FROM fts_norm)",
        )?;
        statement
            .query_map([], |row| {
                Ok((
                    row.get::<_, i64>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, String>(2)?,
                ))
            })?
            .collect::<Result<Vec<_>, _>>()?
    };
    let transaction = connection.transaction()?;
    for (row_id, title, body) in pending {
        transaction.execute(
            "INSERT INTO fts_norm(rowid, norm) VALUES (?, ?)",
            params![
                row_id,
                symdesk_core::german::normalized_text(&format!("{title} {body}"))
            ],
        )?;
    }
    transaction.commit()?;
    Ok(())
}

fn index_document_tx(
    transaction: &Transaction<'_>,
    document: &IndexedDocument,
) -> Result<(), SidecarError> {
    if document.asn.is_some_and(|asn| asn <= 0) {
        return Err(SidecarError::Contract(
            "invalid document ASN: must be a positive integer".to_owned(),
        ));
    }
    let file_id: Option<i64> = transaction
        .query_row(
            "SELECT id FROM files WHERE path = ?",
            [&document.path],
            |row| row.get(0),
        )
        .optional()?;
    let indexed_at = OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .map_err(|error| SidecarError::Time(error.to_string()))?;
    let file_id = if let Some(file_id) = file_id {
        delete_fts(transaction, file_id)?;
        transaction.execute(
            r#"UPDATE files SET sha256=?,title=?,created_at=?,modified_at=?,indexed_at=?,"type"=?,document_date=?,person=?,status=?,due_date=?,confidence=?,ocr_json_path=?,simhash=?,asn=?,size=?,mtime_ns=? WHERE id=?"#,
            params![document.sha256, document.title, document.created_at, document.modified_at, indexed_at, document.document_type, document.document_date, document.person, document.status, document.due_date, document.confidence, document.ocr_json_path, document.simhash, document.asn, document.size, document.mtime_ns, file_id],
        )?;
        transaction.execute("DELETE FROM file_properties WHERE file_id = ?", [file_id])?;
        transaction.execute("DELETE FROM links WHERE from_path = ?", [&document.path])?;
        file_id
    } else {
        transaction.execute(
            r#"INSERT INTO files(path,sha256,title,created_at,modified_at,indexed_at,"type",document_date,person,status,due_date,confidence,ocr_json_path,simhash,asn,size,mtime_ns) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"#,
            params![document.path, document.sha256, document.title, document.created_at, document.modified_at, indexed_at, document.document_type, document.document_date, document.person, document.status, document.due_date, document.confidence, document.ocr_json_path, document.simhash, document.asn, document.size, document.mtime_ns],
        )?;
        transaction.last_insert_rowid()
    };
    let fts_title = document.asn.map_or_else(
        || document.title.clone(),
        |asn| format!("{} ASN {asn}", document.title),
    );
    transaction.execute(
        "INSERT INTO fts_search(rowid,title,body) VALUES (?,?,?)",
        params![file_id, fts_title, document.body],
    )?;
    transaction.execute(
        "INSERT INTO fts_norm(rowid,norm) VALUES (?,?)",
        params![
            file_id,
            symdesk_core::german::normalized_text(&format!("{fts_title} {}", document.body))
        ],
    )?;
    transaction.execute(
        "INSERT INTO fts_tri(rowid,body) VALUES (?,?)",
        params![file_id, document.body],
    )?;
    for (key, value) in &document.properties {
        transaction.execute(
            "INSERT INTO file_properties(file_id,key,value,value_type) VALUES (?,?,?,'string')",
            params![file_id, key, value],
        )?;
    }
    for target in &document.links {
        transaction.execute(
            "INSERT INTO links(from_path,to_path,kind) VALUES (?,?,'wikilink')",
            params![document.path, target],
        )?;
    }
    Ok(())
}

fn delete_document_rows(transaction: &Transaction<'_>, path: &str) -> Result<(), SidecarError> {
    let file_id: Option<i64> = transaction
        .query_row("SELECT id FROM files WHERE path = ?", [path], |row| {
            row.get(0)
        })
        .optional()?;
    let Some(file_id) = file_id else {
        return Ok(());
    };
    delete_fts(transaction, file_id)?;
    transaction.execute("DELETE FROM file_properties WHERE file_id = ?", [file_id])?;
    transaction.execute("DELETE FROM links WHERE from_path = ?", [path])?;
    transaction.execute("DELETE FROM files WHERE id = ?", [file_id])?;
    Ok(())
}

fn delete_fts(transaction: &Transaction<'_>, file_id: i64) -> Result<(), SidecarError> {
    transaction.execute("DELETE FROM fts_search WHERE rowid = ?", [file_id])?;
    transaction.execute("DELETE FROM fts_norm WHERE rowid = ?", [file_id])?;
    transaction.execute("DELETE FROM fts_tri WHERE rowid = ?", [file_id])?;
    Ok(())
}

fn optional_string(value: &str) -> Option<String> {
    (!value.is_empty()).then(|| value.to_owned())
}

fn go_value(value: &Value) -> String {
    match value {
        Value::Null => "<nil>".to_owned(),
        Value::Bool(value) => value.to_string(),
        Value::Number(value) => value.to_string(),
        Value::String(value) => value.clone(),
        Value::Sequence(values) => format!(
            "[{}]",
            values.iter().map(go_value).collect::<Vec<_>>().join(" ")
        ),
        Value::Mapping(values) => {
            let items = values
                .iter()
                .map(|(key, value)| format!("{key}:{}", go_value(value)))
                .collect::<Vec<_>>()
                .join(" ");
            format!("map[{items}]")
        }
        Value::Tagged(value) => value.value().to_string(),
    }
}

#[cfg(test)]
mod contract_tests;

#[cfg(test)]
mod source_tests {
    use std::{
        fs,
        path::{Path, PathBuf},
        sync::{
            Arc,
            atomic::{AtomicBool, Ordering},
        },
        thread,
        time::{Duration, Instant, SystemTime},
    };

    use super::{
        IndexedDocument, MAX_EXTERNAL_TEXT_FILE_SIZE, SearchSource, Sidecar, SourceRegistry,
        absolute_non_verbatim, vault_document_path,
    };

    fn temp_dir(label: &str) -> PathBuf {
        let stamp = SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symdesk-sources-{label}-{}-{stamp}",
            std::process::id()
        ));
        fs::create_dir_all(&path).expect("create temp dir");
        path
    }

    #[test]
    fn indexed_title_accepts_native_canonical_and_storage_key_paths() {
        let root = temp_dir("indexed-canonical-title");
        let source = root.join("external");
        fs::create_dir(&source).expect("external source");
        let note = source.join("guide.md");
        fs::write(
            &note,
            "---\ntitle: Indexed external title\n---\nSearch body.\n",
        )
        .expect("source note");
        let canonical = fs::canonicalize(&note).expect("canonical source note");
        let storage_key = absolute_non_verbatim(&canonical).expect("ordinary storage key");
        let mut sidecar = Sidecar::open(&root.join("sidecar.db")).expect("sidecar");
        sidecar
            .refresh_external_source(&source)
            .expect("index external note");

        for path in [&canonical, &storage_key] {
            assert_eq!(
                sidecar
                    .get_title(path.to_str().expect("UTF-8 fixture path"))
                    .expect("indexed title for either native path spelling"),
                "Indexed external title"
            );
        }

        drop(sidecar);
        fs::remove_dir_all(root).expect("remove temporary source");
    }

    #[test]
    fn written_document_key_uses_refresh_index_root_spelling_and_rejects_traversal() {
        let vault = temp_dir("document-key-vault");
        let canonical_root = fs::canonicalize(&vault).expect("canonical vault root");
        let expected = absolute_non_verbatim(&canonical_root)
            .expect("ordinary vault root")
            .join("nested")
            .join("note.md");

        assert_eq!(
            vault_document_path(&canonical_root, Path::new("nested/note.md"))
                .expect("document key"),
            expected
        );
        assert!(vault_document_path(&canonical_root, Path::new("../outside.md")).is_err());

        let _ = fs::remove_dir_all(vault);
    }

    #[cfg(windows)]
    #[test]
    fn external_source_root_matches_unprefixed_storage_keys() {
        let verbatim = Path::new(r"\\?\C:\sources\external");
        let key_root = absolute_non_verbatim(verbatim).expect("normalize storage root");
        let stored_path = Path::new(r"C:\sources\external\nested\note.md");

        assert!(stored_path.starts_with(&key_root));
    }

    #[cfg(windows)]
    #[test]
    fn written_document_key_matches_refresh_index_verbatim_root_key() {
        let vault = temp_dir("document-key-vault");
        let canonical_root = fs::canonicalize(&vault).expect("canonical vault root");
        let expected = absolute_non_verbatim(&canonical_root)
            .expect("ordinary root")
            .join("nested")
            .join("note.md");

        assert_eq!(
            vault_document_path(&canonical_root, Path::new(r"nested\note.md"))
                .expect("document key"),
            expected
        );
        assert!(vault_document_path(&canonical_root, Path::new(r"nested\..\outside.md")).is_err());

        let _ = fs::remove_dir_all(vault);
    }

    #[cfg(unix)]
    #[test]
    fn refresh_index_skips_external_and_broken_symlinks_but_keeps_contained_links() {
        use std::os::unix::fs::symlink;

        let vault = temp_dir("symlink-refresh-vault");
        let outside = temp_dir("symlink-refresh-outside");
        let marker = "symlink-refresh-unique-marker";
        fs::write(vault.join("inside.md"), marker).expect("write in-vault note");
        fs::write(outside.join("secret.md"), marker).expect("write outside note");
        symlink("inside.md", vault.join("contained.md")).expect("contained symlink");
        symlink(outside.join("secret.md"), vault.join("escape.md")).expect("external symlink");
        symlink("missing.md", vault.join("broken.md")).expect("broken symlink");

        let mut sidecar = Sidecar::open(&vault.join(".symdesk/sidecar.db")).expect("sidecar");
        sidecar
            .refresh_index(&vault)
            .expect("refresh skips uncontained links");
        let hits = sidecar.search(marker).expect("search indexed content");
        assert_eq!(
            hits.iter().map(|hit| hit.path.as_str()).collect::<Vec<_>>(),
            vec![
                vault.join("contained.md").to_str().expect("UTF-8 path"),
                vault.join("inside.md").to_str().expect("UTF-8 path"),
            ]
        );

        let _ = fs::remove_dir_all(vault);
        let _ = fs::remove_dir_all(outside);
    }

    #[test]
    fn registry_canonicalizes_idempotently_and_reopens() {
        let vault = temp_dir("vault");
        let parent = temp_dir("parent");
        let source_path = parent.join("source");
        fs::create_dir(&source_path).expect("source dir");
        #[cfg(unix)]
        let alias = parent.join("alias");
        #[cfg(unix)]
        std::os::unix::fs::symlink(&source_path, &alias).expect("source symlink");

        let registry = SourceRegistry::open(&vault).expect("registry");
        let first = registry.add(&source_path).expect("add canonical");
        #[cfg(windows)]
        assert!(!first.path.starts_with(r"\\?\"));
        #[cfg(unix)]
        assert_eq!(registry.add(&alias).expect("add alias"), first);
        assert_eq!(registry.list().expect("list"), vec![first.clone()]);
        let reopened = SourceRegistry::open(&vault).expect("reopen");
        assert_eq!(reopened.list().expect("reopened list"), vec![first]);
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(vault.join(".symdesk/search-sources.json"))
                .expect("registry stat")
                .permissions()
                .mode();
            assert_eq!(mode & 0o777, 0o600);
        }
        let _ = fs::remove_dir_all(vault);
        let _ = fs::remove_dir_all(parent);
    }

    #[cfg(windows)]
    #[test]
    fn registry_normalizes_legacy_verbatim_source_paths() {
        let vault = temp_dir("legacy-source-registry-vault");
        let source_root = temp_dir("legacy-source-registry-root");
        let verbatim = fs::canonicalize(&source_root)
            .expect("canonical source root")
            .to_string_lossy()
            .into_owned();
        let expected = super::strip_verbatim_prefix(&verbatim);
        let registry_dir = vault.join(".symdesk");
        fs::create_dir_all(&registry_dir).expect("registry dir");
        let registry_path = registry_dir.join("search-sources.json");
        fs::write(
            &registry_path,
            serde_json::json!({
                "version": 1,
                "sources": [{"id": "legacy-id", "path": verbatim}]
            })
            .to_string(),
        )
        .expect("write legacy registry");

        let registry = SourceRegistry::open(&vault).expect("open registry");
        let listed = registry.list().expect("list legacy registry");
        assert_eq!(listed[0].path, expected);
        assert_eq!(
            registry
                .remove(&expected)
                .expect("remove normalized path")
                .id,
            "legacy-id"
        );

        let _ = fs::remove_dir_all(vault);
        let _ = fs::remove_dir_all(source_root);
    }

    #[test]
    fn registry_rejects_vault_subtrees_and_remove_preserves_external_files() {
        let vault = temp_dir("boundary");
        let inside = vault.join("nested");
        fs::create_dir(&inside).expect("nested dir");
        let registry = SourceRegistry::open(&vault).expect("registry");
        assert!(
            registry
                .add(&inside)
                .expect_err("inside vault rejected")
                .to_string()
                .contains("outside the vault")
        );

        #[cfg(unix)]
        {
            let symlink_target = temp_dir("symlink-target");
            let vault_link = vault.join("external-alias");
            std::os::unix::fs::symlink(&symlink_target, &vault_link).expect("vault source symlink");
            assert!(registry.add(&vault_link).is_err());
            let outside_alias = symlink_target.join("inside-alias");
            std::os::unix::fs::symlink(&inside, &outside_alias).expect("alias to vault subtree");
            assert!(registry.add(&outside_alias).is_err());
            let _ = fs::remove_dir_all(symlink_target);
        }

        let external = temp_dir("remove");
        let marker = external.join("keep.md");
        fs::write(&marker, "keep me").expect("marker");
        let source = registry.add(&external).expect("add");
        assert_eq!(registry.remove(&source.id).expect("remove"), source);
        assert_eq!(
            fs::read_to_string(marker).expect("external file survives"),
            "keep me"
        );
        assert!(registry.list().expect("list after remove").is_empty());
        let _ = fs::remove_dir_all(vault);
        let _ = fs::remove_dir_all(external);
    }

    #[test]
    fn search_indexes_registered_external_markdown_and_excludes_unregistered_roots() {
        let vault = temp_dir("search-vault");
        let registered = temp_dir("registered");
        let unregistered = temp_dir("unregistered");
        let needle = "external-source-search-needle";
        let tagged = format!("---\ntags: [keep]\n---\n{needle}");
        fs::write(vault.join("vault.md"), &tagged).expect("vault note");
        fs::write(registered.join("registered.md"), &tagged).expect("registered note");
        fs::write(unregistered.join("unregistered.md"), &tagged).expect("unregistered note");
        let registry = SourceRegistry::open(&vault).expect("registry");
        let source: SearchSource = registry.add(&registered).expect("register source");
        let mut sidecar = Sidecar::open(&vault.join(".symdesk/test-sidecar.db")).expect("sidecar");
        sidecar
            .refresh_external_source(&registered)
            .expect("index registered");
        sidecar
            .refresh_external_source(&unregistered)
            .expect("index other root");
        sidecar.refresh_index(&vault).expect("index vault");
        let hits = sidecar
            .search_with_sources(&vault, needle)
            .expect("scoped search");
        assert_eq!(hits.len(), 2);
        assert!(hits.iter().any(|hit| {
            hit.path
                == PathBuf::from(&source.path)
                    .join("registered.md")
                    .to_string_lossy()
        }));
        assert!(
            hits.iter()
                .any(|hit| hit.path == vault.join("vault.md").to_string_lossy())
        );
        // Rust confines sidecar plan results to this vault's registry roots.
        // Go's SearchPlan SQL has no root predicate, so this is intentionally
        // stricter when unrelated rows happen to share the sidecar database.
        let planned = sidecar
            .search_plan(&vault, "tag:keep")
            .expect("search plan within registered roots");
        assert_eq!(planned.results.len(), 2);
        assert!(planned.results.iter().any(|hit| {
            hit.path
                == PathBuf::from(&source.path)
                    .join("registered.md")
                    .to_string_lossy()
        }));
        assert!(
            planned
                .results
                .iter()
                .any(|hit| hit.path == vault.join("vault.md").to_string_lossy())
        );
        assert!(
            !planned
                .results
                .iter()
                .any(|hit| hit.path.contains("unregistered"))
        );
        fs::remove_file(PathBuf::from(&source.path).join("registered.md"))
            .expect("remove registered note");
        sidecar
            .refresh_external_source(&registered)
            .expect("prune removed external note");
        assert_eq!(
            sidecar
                .search_with_sources(&vault, needle)
                .expect("search after refresh")
                .len(),
            1
        );

        let _ = fs::remove_dir_all(vault);
        let _ = fs::remove_dir_all(registered);
        let _ = fs::remove_dir_all(unregistered);
    }

    #[test]
    fn refresh_external_source_prunes_indexed_unsupported_paths() {
        let vault = temp_dir("unsupported-prune-vault");
        let source_root = fs::canonicalize(temp_dir("unsupported-prune-source"))
            .expect("canonicalize source root");
        let markdown = source_root.join("document.md");
        let unsupported = source_root.join("document.bin");
        let marker = "stale-unsupported-index-marker";
        fs::write(&markdown, marker).expect("write markdown source");

        let mut sidecar = Sidecar::open(&vault.join(".symdesk/test-sidecar.db")).expect("sidecar");
        sidecar
            .refresh_external_source(&source_root)
            .expect("index markdown source");
        fs::rename(&markdown, &unsupported).expect("rename markdown to unsupported path");

        sidecar
            .refresh_external_source(&source_root)
            .expect("refresh unsupported source");
        assert!(
            sidecar
                .search(marker)
                .expect("search after refresh")
                .is_empty(),
            "renamed markdown must be removed when its new extension is unsupported"
        );

        // Model a pre-existing stale row for this unsupported path. The walk
        // must not count it as found merely because a file exists at that path.
        let stale = symdesk_vault::parse_bytes(
            unsupported.to_str().expect("UTF-8 path"),
            marker.as_bytes(),
        )
        .expect("parse stale indexed document");
        let stale = IndexedDocument::from_vault(&stale, None).expect("convert stale document");
        sidecar
            .index_document(&stale)
            .expect("seed stale indexed path");
        sidecar
            .refresh_external_source(&source_root)
            .expect("prune stale unsupported path");
        assert!(
            sidecar
                .search(marker)
                .expect("search after pruning")
                .is_empty(),
            "unsupported existing paths must not keep stale index rows"
        );

        let _ = fs::remove_dir_all(vault);
        let _ = fs::remove_dir_all(source_root);
    }

    #[test]
    fn external_txt_and_go_sources_add_search_refresh_and_remove_in_place() {
        let vault = temp_dir("raw-text-vault");
        let source_root = temp_dir("raw-text-source");
        let txt_path = source_root.join("text-fixture.txt");
        let go_path = source_root.join("code-fixture.go");
        fs::write(
            &txt_path,
            "---\ntitle: parsed markdown title\n---\nplaintextoldmarker",
        )
        .expect("write txt source");
        fs::write(&go_path, "package sample\nconst oldgomarker = true\n").expect("write go source");

        let registry = SourceRegistry::open(&vault).expect("registry");
        let source = registry.add(&source_root).expect("register source");
        let mut sidecar = Sidecar::open(&vault.join(".symdesk/test-sidecar.db")).expect("sidecar");
        sidecar
            .refresh_external_source(&source_root)
            .expect("index raw text files");

        let txt_hits = sidecar
            .search_with_sources(&vault, "plaintextoldmarker")
            .expect("search txt");
        assert_eq!(txt_hits.len(), 1);
        assert_eq!(txt_hits[0].title, "text-fixture");
        assert_eq!(
            txt_hits[0].path,
            PathBuf::from(&source.path)
                .join("text-fixture.txt")
                .to_string_lossy()
        );
        let go_hits = sidecar
            .search_with_sources(&vault, "oldgomarker")
            .expect("search go");
        assert_eq!(go_hits.len(), 1);
        assert_eq!(go_hits[0].title, "code-fixture");
        assert_eq!(
            go_hits[0].path,
            PathBuf::from(&source.path)
                .join("code-fixture.go")
                .to_string_lossy()
        );

        fs::write(&txt_path, "plain text now has plaintextnewmarker").expect("refresh txt source");
        fs::write(&go_path, "package sample\nconst newgomarker = true\n")
            .expect("refresh go source");
        sidecar
            .refresh_external_source(&source_root)
            .expect("refresh raw text files");
        assert!(
            sidecar
                .search_with_sources(&vault, "plaintextoldmarker")
                .unwrap()
                .is_empty()
        );
        assert_eq!(
            sidecar
                .search_with_sources(&vault, "plaintextnewmarker")
                .unwrap()
                .len(),
            1
        );
        assert!(
            sidecar
                .search_with_sources(&vault, "oldgomarker")
                .unwrap()
                .is_empty()
        );
        assert_eq!(
            sidecar
                .search_with_sources(&vault, "newgomarker")
                .unwrap()
                .len(),
            1
        );

        assert_eq!(
            sidecar
                .remove_external_source(Path::new(&source.path))
                .expect("remove indexed source"),
            2
        );
        assert!(
            sidecar
                .search_with_sources(&vault, "plaintextnewmarker")
                .unwrap()
                .is_empty()
        );
        assert_eq!(
            fs::read_to_string(&txt_path).unwrap(),
            "plain text now has plaintextnewmarker"
        );
        assert_eq!(
            fs::read_to_string(&go_path).unwrap(),
            "package sample\nconst newgomarker = true\n"
        );
        assert_eq!(
            registry.remove(&source.id).expect("unregister source"),
            source
        );

        let _ = fs::remove_dir_all(vault);
        let _ = fs::remove_dir_all(source_root);
    }

    #[test]
    fn oversized_external_raw_text_is_skipped_and_pruned() {
        let vault = temp_dir("raw-limit-vault");
        let source_root = temp_dir("raw-limit-source");
        let text_path = source_root.join("oversized.text");
        fs::write(&text_path, "limitmarker").expect("write small source");
        SourceRegistry::open(&vault)
            .expect("registry")
            .add(&source_root)
            .expect("register source");
        let mut sidecar = Sidecar::open(&vault.join(".symdesk/test-sidecar.db")).expect("sidecar");
        sidecar
            .refresh_external_source(&source_root)
            .expect("index small source");
        assert_eq!(
            sidecar
                .search_with_sources(&vault, "limitmarker")
                .unwrap()
                .len(),
            1
        );

        fs::write(
            &text_path,
            vec![b'x'; usize::try_from(MAX_EXTERNAL_TEXT_FILE_SIZE + 1).unwrap()],
        )
        .expect("grow beyond Go's 10 MiB limit");
        sidecar
            .refresh_external_source(&source_root)
            .expect("skip oversized source");
        assert!(
            sidecar
                .search_with_sources(&vault, "limitmarker")
                .unwrap()
                .is_empty()
        );

        let _ = fs::remove_dir_all(vault);
        let _ = fs::remove_dir_all(source_root);
    }

    #[test]
    fn external_source_watch_syncs_nested_create_modify_delete_and_stops() {
        let vault = temp_dir("source-watch-vault");
        let source_root = temp_dir("source-watch-root");
        let nested = source_root.join("nested");
        fs::create_dir_all(&nested).expect("create initial nested directory");
        let existing = nested.join("existing.md");
        fs::write(&existing, "initialwatchmarker").expect("write existing source");
        SourceRegistry::open(&vault)
            .expect("registry")
            .add(&source_root)
            .expect("register source");
        let database = vault.join(".symdesk/watch-sidecar.db");
        let observer = Sidecar::open(&database).expect("observer sidecar");
        let stop = Arc::new(AtomicBool::new(false));
        let worker_stop = Arc::clone(&stop);
        let worker_root = source_root.clone();
        let worker_database = database.clone();
        let worker = thread::spawn(move || {
            Sidecar::open(&worker_database)
                .expect("watch sidecar")
                .watch_external_source(&worker_root, &worker_stop)
        });

        fn wait_for_query(sidecar: &Sidecar, vault: &Path, query: &str, found: bool) {
            let deadline = Instant::now() + Duration::from_secs(8);
            while Instant::now() < deadline {
                let actual = !sidecar
                    .search_with_sources(vault, query)
                    .expect("watch search")
                    .is_empty();
                if actual == found {
                    return;
                }
                thread::sleep(Duration::from_millis(40));
            }
            assert_eq!(
                !sidecar
                    .search_with_sources(vault, query)
                    .expect("final watch search")
                    .is_empty(),
                found,
                "query state did not settle for {query:?}"
            );
        }

        wait_for_query(&observer, &vault, "initialwatchmarker", true);
        fs::write(&existing, "modifiedwatchmarker with a different size")
            .expect("modify existing source");
        wait_for_query(&observer, &vault, "initialwatchmarker", false);
        wait_for_query(&observer, &vault, "modifiedwatchmarker", true);

        let new_dir = source_root.join("new-nested").join("deeper");
        fs::create_dir_all(&new_dir).expect("create watched nested directories");
        let created = new_dir.join("created.md");
        fs::write(&created, "createdwatchmarker").expect("create nested source");
        wait_for_query(&observer, &vault, "createdwatchmarker", true);
        fs::remove_file(&created).expect("delete nested source");
        wait_for_query(&observer, &vault, "createdwatchmarker", false);

        let root_deleted = source_root.join("root-deleted.md");
        fs::write(&root_deleted, "rootdeletedwatchmarker").expect("write root-delete source");
        wait_for_query(&observer, &vault, "rootdeletedwatchmarker", true);
        fs::remove_dir_all(&source_root).expect("delete watched source root");
        wait_for_query(&observer, &vault, "rootdeletedwatchmarker", false);

        stop.store(true, Ordering::SeqCst);
        worker
            .join()
            .expect("watch worker thread")
            .expect("watch stops cleanly");
        let _ = fs::remove_dir_all(vault);
    }
}
