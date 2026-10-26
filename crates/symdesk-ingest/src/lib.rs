#![deny(unsafe_code)]
//! Go-compatible persistent ingest queue. Running jobs have no expiring lease:
//! startup recovery is explicit, and automatic failed-job retry waits ten seconds.
//! Complete/fail/retry intentionally accept any existing status, as Go does.

use rusqlite::{Connection, OptionalExtension, Row, params};
use serde::{Deserialize, Serialize};
use std::path::Path;
mod document;
mod migrations;
pub mod notion;
mod sql;
pub use document::{Document, DocumentPaths};
pub use migrations::MIGRATIONS;
use sql::*;

pub type Result<T> = std::result::Result<T, String>;

#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
pub struct Job {
    pub id: i64,
    pub document_id: i64,
    pub kind: String,
    pub status: String,
    pub attempts: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub last_error: Option<String>,
    pub created_at: String,
    pub updated_at: String,
    pub source_path: String,
}

pub struct Store {
    connection: Connection,
}
fn error(context: &str, e: rusqlite::Error) -> String {
    let text = match e {
        rusqlite::Error::QueryReturnedNoRows => "sql: no rows in result set".to_owned(),
        rusqlite::Error::SqliteFailure(code, Some(_)) if code.extended_code == 787 => {
            "constraint failed: FOREIGN KEY constraint failed (787)".to_owned()
        }
        other => other.to_string(),
    };
    if context.is_empty() {
        text
    } else {
        format!("{context}: {text}")
    }
}
// modernc scans SQLite DATETIME values into time.Time; database/sql then
// formats those string destinations using RFC3339Nano instead of SQLite text.
fn go_timestamp(value: String) -> String {
    if value.len() == 19 && value.as_bytes().get(10) == Some(&b' ') {
        format!("{}T{}Z", &value[..10], &value[11..])
    } else {
        value
    }
}
fn job(row: &Row<'_>) -> rusqlite::Result<Job> {
    Ok(Job {
        id: row.get(0)?,
        document_id: row.get(1)?,
        kind: row.get(2)?,
        status: row.get(3)?,
        attempts: row.get(4)?,
        last_error: row.get(5)?,
        created_at: go_timestamp(row.get(6)?),
        updated_at: go_timestamp(row.get(7)?),
        source_path: if row.as_ref().column_count() > 8 {
            row.get(8)?
        } else {
            String::new()
        },
    })
}
impl Store {
    pub fn open(path: &Path) -> Result<Self> {
        let parent = path
            .parent()
            .filter(|p| !p.as_os_str().is_empty())
            .unwrap_or(Path::new("."));
        let mut options = std::fs::DirBuilder::new();
        options.recursive(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            options.mode(0o700);
        }
        options
            .create(parent)
            .map_err(|e| format!("create store directory: {e}"))?;
        let connection = symaira_core_sqlite::open_with_existing_parent(path)
            .map_err(|e| format!("open store: {e}"))?;
        Self::from_connection(connection)
    }
    /// Configures and migrates an owned connection; also permits deterministic
    /// clock injection in oracle replay without changing production SQL.
    pub fn from_connection(mut connection: Connection) -> Result<Self> {
        connection
            .execute_batch(
                "PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL;",
            )
            .map_err(|e| error("configure store", e))?;
        connection.execute_batch("CREATE TABLE IF NOT EXISTS schema_migrations (\n\t\tversion TEXT PRIMARY KEY,\n\t\tapplied_at DATETIME DEFAULT CURRENT_TIMESTAMP\n\t)").map_err(|e|error("migrate store",e))?;
        for (version, sql) in MIGRATIONS {
            let applied: bool = connection
                .query_row(
                    "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?)",
                    [version],
                    |r| r.get(0),
                )
                .map_err(|e| error("migrate store", e))?;
            if !applied {
                let tx = connection
                    .transaction()
                    .map_err(|e| error("migrate store", e))?;
                tx.execute_batch(sql)
                    .map_err(|e| error("migrate store", e))?;
                tx.execute(
                    "INSERT INTO schema_migrations(version) VALUES (?)",
                    [version],
                )
                .map_err(|e| error("migrate store", e))?;
                tx.commit().map_err(|e| error("migrate store", e))?;
            }
        }
        Ok(Self { connection })
    }
    /// Read-only access for schema inspection and existing store integration.
    pub fn connection(&self) -> &Connection {
        &self.connection
    }

    pub fn enqueue_job(&self, document_id: i64, kind: &str) -> Result<Job> {
        self.connection
            .execute(ENQUEUE_JOB_0, params![document_id, kind])
            .map_err(|e| error("enqueue job", e))?;
        Ok(Job {
            id: self.connection.last_insert_rowid(),
            document_id,
            kind: kind.into(),
            status: "pending".into(),
            ..Job::default()
        })
    }
    pub fn enqueue_skipped_job(&self, document_id: i64, kind: &str, reason: &str) -> Result<Job> {
        self.connection
            .execute(ENQUEUE_SKIPPED_JOB_0, params![document_id, kind, reason])
            .map_err(|e| error("enqueue skipped job", e))?;
        Ok(Job {
            id: self.connection.last_insert_rowid(),
            document_id,
            kind: kind.into(),
            status: "skipped".into(),
            last_error: Some(reason.into()),
            ..Job::default()
        })
    }
    pub fn enqueue_reprocess_job(&mut self, document_id: i64) -> Result<(Job, bool)> {
        let tx = self.connection.transaction().map_err(|e| error("", e))?;
        let found = tx
            .query_row(ENQUEUE_REPROCESS_JOB_0, [document_id], job)
            .optional()
            .map_err(|e| error("find active reprocess job", e))?;
        if let Some(found) = found {
            tx.commit().map_err(|e| error("", e))?;
            return Ok((found, false));
        }
        tx.execute(ENQUEUE_REPROCESS_JOB_1, [document_id])
            .map_err(|e| error("insert reprocess job", e))?;
        let id = tx.last_insert_rowid();
        tx.commit().map_err(|e| error("", e))?;
        Ok((
            Job {
                id,
                document_id,
                kind: "reocr".into(),
                status: "pending".into(),
                ..Job::default()
            },
            true,
        ))
    }
    fn claim(
        &self,
        query: &str,
        args: impl rusqlite::Params,
        context: &str,
    ) -> Result<Option<Job>> {
        let found = self
            .connection
            .query_row(query, args, job)
            .optional()
            .map_err(|e| error(context, e))?;
        if let Some(mut j) = found {
            j.source_path = self
                .connection
                .query_row(
                    "SELECT source_path FROM documents WHERE id = ?",
                    [j.document_id],
                    |r| r.get(0),
                )
                .map_err(|e| error("get document for claimed job", e))?;
            Ok(Some(j))
        } else {
            Ok(None)
        }
    }
    pub fn claim_job(&self) -> Result<Option<Job>> {
        self.claim(CLAIM_JOB_0, [], "claim job")
    }
    pub fn claim_job_by_id(&self, id: i64) -> Result<Option<Job>> {
        self.claim(CLAIM_JOB_BY_I_D_0, [id], "claim job by id")
    }
    pub fn complete_job(&mut self, id: i64) -> Result<()> {
        let tx = self.connection.transaction().map_err(|e| error("", e))?;
        let doc: i64 = tx
            .query_row(COMPLETE_JOB_0, [id], |r| r.get(0))
            .map_err(|e| error("update job status", e))?;
        tx.execute(COMPLETE_JOB_1, [doc])
            .map_err(|e| error("update document status", e))?;
        tx.commit().map_err(|e| error("", e))
    }
    pub fn fail_job(&mut self, id: i64, message: &str) -> Result<()> {
        let tx = self.connection.transaction().map_err(|e| error("", e))?;
        let (doc, attempts): (i64, i64) = tx
            .query_row(FAIL_JOB_0, params![message, id], |r| {
                Ok((r.get(0)?, r.get(1)?))
            })
            .map_err(|e| error("update job status", e))?;
        if attempts >= 3 {
            tx.execute(FAIL_JOB_1, [doc])
                .map_err(|e| error("update document status", e))?;
        }
        tx.commit().map_err(|e| error("", e))
    }
    pub fn retry_job(&mut self, id: i64) -> Result<()> {
        let tx = self.connection.transaction().map_err(|e| error("", e))?;
        let doc: i64 = tx
            .query_row(RETRY_JOB_0, [id], |r| r.get(0))
            .map_err(|e| error("update job status for retry", e))?;
        tx.execute(RETRY_JOB_1, [doc])
            .map_err(|e| error("update document status for retry", e))?;
        tx.commit().map_err(|e| error("", e))
    }
    pub fn reset_running_jobs(&self) -> Result<()> {
        self.connection
            .execute(RESET_RUNNING_JOBS_0, [])
            .map_err(|e| error("reset running jobs", e))?;
        Ok(())
    }
    pub fn list_jobs_page(
        &self,
        vault: &str,
        limit: i64,
        offset: i64,
    ) -> Result<(Option<Vec<Job>>, i64)> {
        let mut args = Vec::<rusqlite::types::Value>::new();
        let where_sql = if vault.is_empty() {
            ""
        } else {
            args.push(vault.to_owned().into());
            args.push(format!("{vault}{}", std::path::MAIN_SEPARATOR).into());
            " WHERE d.vault_root = ? OR (d.vault_root = '' AND instr(d.vault_path, ?) = 1)"
        };
        let total=self.connection.query_row(&format!("SELECT COUNT(*) FROM jobs j JOIN documents d ON j.document_id = d.id{where_sql}"),rusqlite::params_from_iter(args.iter()),|r|r.get(0)).map_err(|e|error("count jobs",e))?;
        let mut query = format!(
            "SELECT j.id,j.document_id,j.kind,j.status,j.attempts,j.last_error,j.created_at,j.updated_at,d.source_path FROM jobs j JOIN documents d ON j.document_id=d.id{where_sql} ORDER BY j.id DESC"
        );
        if limit > 0 {
            query.push_str(" LIMIT ? OFFSET ?");
            args.push(limit.into());
            args.push(offset.max(0).into());
        }
        let mut stmt = self
            .connection
            .prepare(&query)
            .map_err(|e| error("query jobs", e))?;
        let jobs = stmt
            .query_map(rusqlite::params_from_iter(args.iter()), job)
            .map_err(|e| error("query jobs", e))?
            .collect::<std::result::Result<Vec<_>, _>>()
            .map_err(|e| error("scan job", e))?;
        Ok((if jobs.is_empty() { None } else { Some(jobs) }, total))
    }
}
