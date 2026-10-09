use super::{Sidecar, SidecarError};

/// A persisted document-level indexing state used by `symdesk index status`.
/// Text bytes remain diagnostic data, never normalized filesystem identities.
#[derive(Clone, Debug, PartialEq)]
pub struct IndexStatus {
    pub path: Vec<u8>,
    pub state: Vec<u8>,
    pub reason: Vec<u8>,
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
                path: row.get_ref(0)?.as_bytes()?.to_vec(),
                state: row.get_ref(1)?.as_bytes()?.to_vec(),
                reason: row.get_ref(2)?.as_bytes()?.to_vec(),
                updated_at: row.get(3)?,
            })
        })?;
        rows.collect::<Result<Vec<_>, _>>().map_err(Into::into)
    }
}
