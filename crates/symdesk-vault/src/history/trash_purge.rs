//! Selected, fail-closed trash entry removal.

use std::path::Path;

use super::{HistoryError, HistoryStore, TRASH_META_SUFFIX, TrashEntry, trash_rel_dir};

impl HistoryStore {
    /// Permanently removes exactly the selected trash entries.
    ///
    /// The complete inventory is validated before any removal. Entries already
    /// absent are ignored so a retry after a successful purge is idempotent.
    /// A selected entry whose original path changed is rejected before
    /// mutation. Unselected entries are never removed.
    pub fn purge_trash_entries(&self, wanted: &[TrashEntry]) -> Result<usize, HistoryError> {
        let current = self.trash_list_strict()?;
        let by_name: std::collections::BTreeMap<_, _> = current
            .iter()
            .map(|entry| (entry.name.as_str(), entry))
            .collect();

        for entry in wanted {
            if let Some(actual) = by_name.get(entry.name.as_str())
                && actual.original_path != entry.original_path
            {
                return Err(HistoryError::TrashEntryOriginalPathChanged(
                    entry.name.clone(),
                ));
            }
        }

        let root = self.open_root()?;
        let mut removed = 0;
        for entry in wanted {
            if !by_name.contains_key(entry.name.as_str()) {
                continue;
            }
            for path in [
                format!("{}/{name}", trash_rel_dir(), name = entry.name),
                format!(
                    "{}/{name}{TRASH_META_SUFFIX}",
                    trash_rel_dir(),
                    name = entry.name
                ),
            ] {
                match root.remove_file(Path::new(&path)) {
                    Ok(()) => {}
                    Err(err) if err.kind() == std::io::ErrorKind::NotFound => {}
                    Err(err) => return Err(HistoryError::Io(err)),
                }
            }
            removed += 1;
        }
        Ok(removed)
    }
}
