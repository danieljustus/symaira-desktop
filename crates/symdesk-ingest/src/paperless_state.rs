//! Paperless resume state keyed by source *and* concrete destination. Legacy
//! latest-row reads are retained, but callers with a target must use exact reads.
use crate::{Result, Store, error, go_timestamp};
use rusqlite::{OptionalExtension, Row, params};
use serde::{Deserialize, Serialize};
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PaperlessImportState {
    pub id: i64,
    pub base_url: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub target_vault: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub target_archive: String,
    pub paperless_document_id: i64,
    pub status: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub vault_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub archive_path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub sha256: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub last_error: String,
    pub created_at: String,
    pub updated_at: String,
}
/// A write has no ID/clock fields: those belong to SQLite, just as in Go.
#[derive(Clone, Debug, Default)]
pub struct PaperlessStateWrite<'a> {
    pub base_url: &'a str,
    pub target_vault: &'a str,
    pub target_archive: &'a str,
    pub document_id: i64,
    pub status: &'a str,
    pub last_error: &'a str,
    pub vault_path: &'a str,
    pub archive_path: &'a str,
    pub sha256: &'a str,
}
const FIELDS: &str = "id,base_url,target_vault,target_archive,paperless_document_id,status,last_error,vault_path,archive_path,sha256,created_at,updated_at";
fn scan(r: &Row<'_>) -> rusqlite::Result<PaperlessImportState> {
    Ok(PaperlessImportState {
        id: r.get(0)?,
        base_url: r.get(1)?,
        target_vault: r.get(2)?,
        target_archive: r.get(3)?,
        paperless_document_id: r.get(4)?,
        status: r.get(5)?,
        last_error: r.get::<_, Option<String>>(6)?.unwrap_or_default(),
        vault_path: r.get::<_, Option<String>>(7)?.unwrap_or_default(),
        archive_path: r.get::<_, Option<String>>(8)?.unwrap_or_default(),
        sha256: r.get::<_, Option<String>>(9)?.unwrap_or_default(),
        created_at: go_timestamp(r.get(10)?),
        updated_at: go_timestamp(r.get(11)?),
    })
}
fn nullable(s: &str) -> Option<&str> {
    if s.is_empty() { None } else { Some(s) }
}
impl Store {
    pub fn upsert_paperless_import_state_for_target(
        &self,
        v: &PaperlessStateWrite<'_>,
    ) -> Result<()> {
        if !["pending", "imported", "skipped", "failed"].contains(&v.status) {
            return Err(format!(
                "invalid paperless import state status: {}",
                symdesk_vault::go_quote(v.status)
            ));
        }
        self.connection.execute("INSERT INTO paperless_import_state (base_url,target_vault,target_archive,paperless_document_id,status,last_error,vault_path,archive_path,sha256) VALUES (?,?,?,?,?,?,?,?,?) ON CONFLICT(base_url,target_vault,target_archive,paperless_document_id) DO UPDATE SET target_vault=excluded.target_vault,target_archive=excluded.target_archive,status=excluded.status,last_error=excluded.last_error,vault_path=excluded.vault_path,archive_path=excluded.archive_path,sha256=excluded.sha256,updated_at=CURRENT_TIMESTAMP",params![v.base_url,v.target_vault,v.target_archive,v.document_id,v.status,nullable(v.last_error),nullable(v.vault_path),nullable(v.archive_path),nullable(v.sha256)]).map_err(|e|error("upsert paperless import state",e))?;
        Ok(())
    }
    pub fn upsert_paperless_import_state(
        &self,
        base_url: &str,
        document_id: i64,
        status: &str,
        last_error: &str,
    ) -> Result<()> {
        self.upsert_paperless_import_state_for_target(&PaperlessStateWrite {
            base_url,
            document_id,
            status,
            last_error,
            ..PaperlessStateWrite::default()
        })
    }
    pub fn paperless_import_status(
        &self,
        base_url: &str,
        document_id: i64,
    ) -> Result<Option<String>> {
        self.connection.query_row("SELECT status FROM paperless_import_state WHERE base_url=? AND paperless_document_id=? ORDER BY updated_at DESC,id DESC LIMIT 1",params![base_url,document_id],|r|r.get(0)).optional().map_err(|e|error("get paperless import state",e))
    }
    pub fn paperless_import_status_for_target(
        &self,
        base_url: &str,
        vault: &str,
        archive: &str,
        document_id: i64,
    ) -> Result<Option<String>> {
        self.connection.query_row("SELECT status FROM paperless_import_state WHERE base_url=? AND target_vault=? AND target_archive=? AND paperless_document_id=?",params![base_url,vault,archive,document_id],|r|r.get(0)).optional().map_err(|e|error("get paperless import state",e))
    }
    pub fn paperless_import_state_by_id(
        &self,
        base_url: &str,
        document_id: i64,
    ) -> Result<PaperlessImportState> {
        self.connection.query_row(&format!("SELECT {FIELDS} FROM paperless_import_state WHERE base_url=? AND paperless_document_id=? ORDER BY updated_at DESC,id DESC LIMIT 1"),params![base_url,document_id],scan).map_err(|e|error("",e))
    }
    pub fn paperless_import_state_for_target(
        &self,
        base_url: &str,
        vault: &str,
        archive: &str,
        document_id: i64,
    ) -> Result<PaperlessImportState> {
        self.connection.query_row(&format!("SELECT {FIELDS} FROM paperless_import_state WHERE base_url=? AND target_vault=? AND target_archive=? AND paperless_document_id=?"),params![base_url,vault,archive,document_id],scan).map_err(|e|error("",e))
    }
    pub fn list_paperless_import_state(
        &self,
        base_url: &str,
        status: &str,
    ) -> Result<Option<Vec<PaperlessImportState>>> {
        let filter = if status.is_empty() {
            ""
        } else {
            " AND status=?"
        };
        let query = format!(
            "SELECT {FIELDS} FROM paperless_import_state WHERE base_url=?{filter} ORDER BY paperless_document_id ASC,target_vault ASC,target_archive ASC,id ASC"
        );
        let args = if status.is_empty() {
            vec![base_url]
        } else {
            vec![base_url, status]
        };
        let mut stmt = self
            .connection
            .prepare(&query)
            .map_err(|e| error("list paperless import state", e))?;
        let states = stmt
            .query_map(rusqlite::params_from_iter(args), scan)
            .map_err(|e| error("list paperless import state", e))?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(|e| error("scan paperless import state", e))?;
        Ok(if states.is_empty() {
            None
        } else {
            Some(states)
        })
    }
}
