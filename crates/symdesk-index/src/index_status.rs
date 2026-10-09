use serde::Serialize;

use super::{Sidecar, SidecarError};

/// A persisted document-level indexing state used by `symdesk index status`.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct IndexStatus {
    #[serde(rename = "path")]
    pub path: String,
    #[serde(rename = "index_state")]
    pub state: String,
    #[serde(
        rename = "index_failure_reason",
        skip_serializing_if = "String::is_empty"
    )]
    pub reason: String,
    #[serde(rename = "index_updated_at")]
    pub updated_at: String,
}

impl Sidecar {
    /// Returns lifecycle rows in path order, matching Go's status listing.
    pub fn list_index_statuses(&self) -> Result<Vec<IndexStatus>, SidecarError> {
        let mut statement = self
            .connection
            .prepare("SELECT path, state, reason, updated_at FROM index_lifecycle ORDER BY path")?;
        let rows = statement.query_map([], |row| {
            Ok(IndexStatus {
                path: row.get(0)?,
                state: row.get(1)?,
                reason: row.get(2)?,
                updated_at: row.get(3)?,
            })
        })?;
        rows.collect::<Result<Vec<_>, _>>().map_err(Into::into)
    }
}
