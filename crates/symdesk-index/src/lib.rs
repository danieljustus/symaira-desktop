#![deny(unsafe_code)]

//! Minimal SQLite sidecar index compatible with the Go oracle.

use std::{
    collections::BTreeMap,
    fs, io,
    path::{Component, Path, PathBuf},
    time::{Duration, UNIX_EPOCH},
};

use noyalib::Value;
use rusqlite::{Connection, OptionalExtension, Transaction, params, params_from_iter};
use symdesk_vault::Document;
use thiserror::Error;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

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
    #[error("{0}")]
    Contract(String),
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

pub struct Sidecar {
    connection: Connection,
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
        let mut connection = Connection::open(path)?;
        connection.busy_timeout(Duration::from_millis(5000))?;
        connection.pragma_update(None, "foreign_keys", true)?;
        connection.pragma_update(None, "journal_mode", "WAL")?;
        migrate(&mut connection)?;
        backfill_norm_index(&mut connection)?;
        Ok(Self { connection })
    }

    /// Runs SQLite's non-destructive integrity check.
    ///
    /// # Errors
    /// Returns the provider error or a non-`ok` integrity result.
    pub fn check_integrity(&self) -> Result<(), SidecarError> {
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

    /// Refreshes the index from lowercase-extension Markdown files under `vault_root`.
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
        let mut batch = Vec::with_capacity(MAX_INDEX_BATCH_SIZE);
        let mut callback_error = None;
        let walk_result = symdesk_vault::walk_markdown_with(vault_root, |relative| {
            let result = self.refresh_path(vault_root, relative, &mut batch);
            if let Err(error) = result {
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

    fn refresh_path(
        &mut self,
        vault_root: &Path,
        relative: &Path,
        batch: &mut Vec<IndexedDocument>,
    ) -> Result<(), SidecarError> {
        let path = storage_path(vault_root, relative)?;
        let metadata = fs::metadata(&path)?;
        let mtime_ns = system_time_unix_nanos(metadata.modified()?)?;
        let path_string = path.to_string_lossy().into_owned();
        let file_size = i64::try_from(metadata.len()).unwrap_or(i64::MAX);
        if let Some(cached) = self.stat_cache(&path_string)?
            && cached.size == file_size
            && cached.mtime_ns == mtime_ns
        {
            return Ok(());
        }

        let bytes = fs::read(&path)?;
        let document = symdesk_vault::parse_bytes(&path_string, &bytes)?;
        if document.derived {
            return self.delete_document(&path_string);
        }
        let indexed = self.is_indexed(&path_string, &document.sha256)?;
        if indexed {
            self.set_file_stat(&path_string, document.size, mtime_ns)?;
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
    /// # Errors
    /// Returns walk or SQLite errors without partially claiming success.
    pub fn prune(&mut self, vault_root: &Path) -> Result<usize, SidecarError> {
        let markdown = symdesk_vault::walk_markdown(vault_root)?;
        let mut valid_documents = std::collections::BTreeSet::new();
        for relative in markdown {
            let path = storage_path(vault_root, &relative)?;
            let path_string = path.to_string_lossy().into_owned();
            // Go keeps malformed files valid for pruning; only a successfully
            // parsed derived document is intentionally absent from the set.
            let is_derived = fs::read(&path)
                .ok()
                .and_then(|bytes| symdesk_vault::parse_bytes(&path_string, &bytes).ok())
                .is_some_and(|document| document.derived);
            if !is_derived {
                valid_documents.insert(path_string);
            }
        }

        let stale_files = self.stale_paths("SELECT path FROM files", &valid_documents)?;
        let stale_statuses = self.stale_lifecycle_paths(vault_root)?;
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

    fn stale_lifecycle_paths(&self, vault_root: &Path) -> Result<Vec<String>, SidecarError> {
        let entries = symdesk_vault::walk_all(vault_root)?;
        let valid = entries
            .into_iter()
            .map(|entry| storage_path(vault_root, &entry.path))
            .map(|path| path.map(|path| path.to_string_lossy().into_owned()))
            .collect::<Result<std::collections::BTreeSet<_>, _>>()?;
        self.stale_paths("SELECT path FROM index_lifecycle", &valid)
    }

    ///
    /// # Errors
    /// Returns SQLite syntax/provider errors.
    pub fn search(&self, query: &str) -> Result<Vec<SearchHit>, SidecarError> {
        self.search_impl(query, None)
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
        self.search_impl(query, Some(allowed_paths))
    }

    fn search_impl(
        &self,
        raw_query: &str,
        allowed_paths: Option<&[String]>,
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

fn storage_path(vault_root: &Path, relative: &Path) -> Result<PathBuf, SidecarError> {
    // Validate the walk result against the canonical vault tree, but keep the
    // ordinary absolute path produced from the caller's root as the DB key.
    // `secure_path` may return a Windows `\\\\?\\` path or resolve macOS
    // `/var` through `/private`; neither matches Go filepath.Walk keys.
    let _ = symdesk_vault::secure_path(vault_root, &relative.to_string_lossy())?;
    Ok(absolute_non_verbatim(vault_root)?.join(relative))
}

fn absolute_non_verbatim(path: &Path) -> Result<PathBuf, SidecarError> {
    let absolute = if path.is_absolute() {
        path.to_path_buf()
    } else {
        std::env::current_dir()?.join(path)
    };
    let cleaned = lexical_clean(&absolute);
    Ok(PathBuf::from(strip_verbatim_prefix(
        &cleaned.to_string_lossy(),
    )))
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
    #[cfg(unix)]
    let mut missing = Vec::<PathBuf>::new();
    #[cfg(unix)]
    {
        let mut current = parent;
        while !current.exists() {
            missing.push(current.to_path_buf());
            let Some(next) = current.parent() else {
                break;
            };
            if next == current {
                break;
            }
            current = next;
        }
    }
    fs::create_dir_all(parent)?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        for directory in missing {
            fs::set_permissions(directory, fs::Permissions::from_mode(0o700))?;
        }
    }
    Ok(())
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
