//! Service-level history mutations keep the derived sidecar beside the vault.

use std::{fs, io::Read, path::Path};

use symdesk_vault::{
    HistoryStore,
    activity_journal::append_activity,
    history::{Checkpoint, HistoryEntry, HistoryError},
    parse_bytes, secure_path,
};
use thiserror::Error;

use crate::{IndexedDocument, Sidecar, SidecarError, open_vault_dir, system_time_unix_nanos};

#[derive(Debug, Error)]
pub enum HistorySyncError {
    #[error(transparent)]
    Path(#[from] symdesk_vault::SecurePathError),
    #[error(transparent)]
    History(#[from] HistoryError),
    #[error(transparent)]
    Sidecar(#[from] SidecarError),
    #[error("restored file but failed to parse for indexing: {source}")]
    Parse {
        entry: HistoryEntry,
        source: Box<dyn std::error::Error + Send + Sync>,
    },
    #[error("restored file but failed to re-index: {source}")]
    Index {
        entry: HistoryEntry,
        source: SidecarError,
    },
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

/// Restore one snapshot, then update its search row and activity journal.
///
/// # Errors
/// Returns a path, history, parse, or sidecar error. A parse/index error may
/// occur after the authoritative Markdown file has already been restored.
pub fn history_restore(
    vault_root: &Path,
    sidecar: &mut Sidecar,
    rel_path: &str,
    id: Option<&str>,
) -> Result<HistoryEntry, HistorySyncError> {
    let path = secure_path(vault_root, rel_path)?;
    let canonical_root = fs::canonicalize(vault_root)?;
    let rel = path.strip_prefix(&canonical_root).map_err(|error| {
        SidecarError::Contract(format!("restored path is outside vault: {error}"))
    })?;
    let rel = rel.to_str().ok_or_else(|| SidecarError::NonUtf8Path {
        context: "history relative path",
        path: rel.to_path_buf(),
    })?;
    let entry = HistoryStore::new(vault_root).restore(rel, id)?;
    match index_path(vault_root, &path, sidecar) {
        Ok(()) => {}
        Err(HistoryIndexError::Parse(source)) => {
            return Err(HistorySyncError::Parse {
                entry,
                source: Box::new(source),
            });
        }
        Err(HistoryIndexError::Io(source)) => {
            return Err(HistorySyncError::Parse {
                entry,
                source: Box::new(source),
            });
        }
        Err(HistoryIndexError::Index(source)) => {
            return Err(HistorySyncError::Index { entry, source });
        }
    }
    let _ = append_activity(
        vault_root,
        "file_changed",
        rel,
        "",
        &format!("restored snapshot {}", id.unwrap_or("")),
    );
    Ok(entry)
}

/// Undo a task checkpoint and repair the index for its restored/deleted notes.
///
/// # Errors
/// Returns the underlying history error. Like the Go service, individual
/// sidecar repair failures do not hide the checkpoint's partial undo report.
pub fn checkpoint_undo(
    vault_root: &Path,
    sidecar: &mut Sidecar,
    task_id: &str,
) -> Result<Checkpoint, HistorySyncError> {
    let checkpoint = HistoryStore::new(vault_root).undo_checkpoint(task_id)?;
    for file in &checkpoint.files {
        if let Ok(path) = secure_path(vault_root, &file.rel_path)
            && path.extension().is_some_and(|extension| extension == "md")
        {
            let _ = index_path(vault_root, &path, sidecar);
        }
    }
    for rel in &checkpoint.new_files {
        if let Ok(path) = secure_path(vault_root, rel)
            && let Some(key) = path.to_str()
        {
            let _ = sidecar.delete_document(key);
        }
    }
    Ok(checkpoint)
}

enum HistoryIndexError {
    Parse(symdesk_vault::VaultError),
    Index(SidecarError),
    Io(std::io::Error),
}

fn index_path(
    vault_root: &Path,
    path: &Path,
    sidecar: &mut Sidecar,
) -> Result<(), HistoryIndexError> {
    let root = fs::canonicalize(vault_root).map_err(HistoryIndexError::Io)?;
    let relative = path.strip_prefix(&root).map_err(|error| {
        HistoryIndexError::Index(SidecarError::Contract(format!(
            "restored path is outside vault: {error}"
        )))
    })?;
    let vault = open_vault_dir(&root).map_err(HistoryIndexError::Index)?;
    let mut file = vault.open(relative).map_err(HistoryIndexError::Io)?;
    let metadata = file.metadata().map_err(HistoryIndexError::Io)?;
    let mut bytes = Vec::new();
    file.read_to_end(&mut bytes)
        .map_err(HistoryIndexError::Io)?;
    let key = path.to_str().ok_or_else(|| {
        HistoryIndexError::Index(SidecarError::NonUtf8Path {
            context: "history index path",
            path: path.to_path_buf(),
        })
    })?;
    let document = parse_bytes(key, &bytes).map_err(HistoryIndexError::Parse)?;
    let mtime = system_time_unix_nanos(
        metadata
            .modified()
            .map_err(HistoryIndexError::Io)?
            .into_std(),
    )
    .map_err(HistoryIndexError::Index)?;
    let indexed =
        IndexedDocument::from_vault(&document, Some(mtime)).map_err(HistoryIndexError::Index)?;
    sidecar
        .index_document(&indexed)
        .map_err(HistoryIndexError::Index)?;
    Ok(())
}
