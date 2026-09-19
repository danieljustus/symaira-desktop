#![deny(unsafe_code)]

//! Task checkpoints: group the pre-task content of several files under one task
//! id so an entire agent run can be rejected as a unit.
//!
//! This is a direct port of `internal/history/checkpoint.go` (Go oracle). The
//! checkpoint reuses the existing content-addressed blob store; only the
//! grouping, the manifest format and the undo semantics are new, so manifests
//! written by Go and by Rust are interchangeable.

use std::io;
use std::path::Path;

use serde::{Deserialize, Serialize};
use time::OffsetDateTime;

use super::{clean_rel, mkdir_all_0750, HistoryEntry, HistoryError, HistoryStore};

/// Relative directory holding task checkpoint manifests.
#[must_use]
pub fn checkpoints_rel_dir() -> &'static str {
    ".symdesk/history/checkpoints"
}

/// One file grouped into a task checkpoint.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct CheckpointFile {
    /// Vault-relative path of the file.
    pub rel_path: String,
    /// Snapshot taken before the task's first write to that file.
    pub entry: HistoryEntry,
}

/// Deserialises a JSON array, mapping JSON `null` to an empty vector the way
/// Go's `json.Unmarshal` does for a nil slice.
fn de_vec_or_null<'de, D, T>(deserializer: D) -> Result<Vec<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de>,
{
    Ok(Option::<Vec<T>>::deserialize(deserializer)?.unwrap_or_default())
}

/// Groups content-addressed blobs under one task id.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct Checkpoint {
    /// Identifies the agent run (or any caller-chosen task).
    pub task_id: String,
    /// When the checkpoint was created (UTC).
    #[serde(with = "super::rfc3339_nano")]
    pub timestamp: OffsetDateTime,
    /// Files that existed before the task and were snapshotted.
    #[serde(deserialize_with = "de_vec_or_null", default)]
    pub files: Vec<CheckpointFile>,
    /// Paths that did not exist when the checkpoint was taken.
    #[serde(deserialize_with = "de_vec_or_null", default)]
    pub new_files: Vec<String>,
    /// Paths that could not be snapshotted; a non-empty list is a partial
    /// checkpoint and is reported as such instead of implying completeness.
    #[serde(deserialize_with = "de_vec_or_null", default)]
    pub skipped: Vec<String>,
}

impl Checkpoint {
    /// Reports whether the checkpoint has skipped entries.
    #[must_use]
    pub fn partial(&self) -> bool {
        !self.skipped.is_empty()
    }

    fn has(&self, rel: &str) -> bool {
        self.files.iter().any(|file| file.rel_path == rel)
            || self.new_files.iter().any(|path| path == rel)
            || self.skipped.iter().any(|path| path == rel)
    }
}

/// Rejects task ids that could escape the checkpoints directory or collide
/// with dot-files. Mirrors `validateTaskID` in the Go oracle.
///
/// # Errors
///
/// Returns [`HistoryError::TaskIdRequired`] for an empty id and
/// [`HistoryError::InvalidTaskId`] for separators, dot-files, `..`/`.` or
/// a colon.
pub fn validate_task_id(task_id: &str) -> Result<(), HistoryError> {
    if task_id.is_empty() {
        return Err(HistoryError::TaskIdRequired);
    }
    if task_id.contains(['/', '\\'])
        || task_id == "."
        || task_id == ".."
        || task_id.starts_with('.')
        || task_id.contains(':')
    {
        return Err(HistoryError::InvalidTaskId(task_id.to_owned()));
    }
    Ok(())
}

fn checkpoint_rel_path(task_id: &str) -> Result<String, HistoryError> {
    validate_task_id(task_id)?;
    Ok(format!("{}/{task_id}.json", checkpoints_rel_dir()))
}

impl HistoryStore {
    /// Starts (or resumes) a task checkpoint.
    ///
    /// It is idempotent: calling it twice for the same task keeps the first
    /// checkpoint's files and timestamp, so lazy callers can invoke it before
    /// every write.
    ///
    /// # Errors
    ///
    /// Returns an error for invalid task ids or on I/O failure.
    pub fn begin_checkpoint(&self, task_id: &str) -> Result<Checkpoint, HistoryError> {
        match self.load_checkpoint(task_id) {
            Ok(checkpoint) => Ok(checkpoint),
            Err(HistoryError::Io(err)) if err.kind() == io::ErrorKind::NotFound => {
                let checkpoint = Checkpoint {
                    task_id: task_id.to_owned(),
                    timestamp: self.now(),
                    files: Vec::new(),
                    new_files: Vec::new(),
                    skipped: Vec::new(),
                };
                self.save_checkpoint(&checkpoint)?;
                Ok(checkpoint)
            }
            Err(err) => Err(err),
        }
    }

    /// Records the current content of `rel_path` under `task_id`, lazily,
    /// before the file's first write of the task.
    ///
    /// It is a no-op when the file is already recorded: the pre-task state must
    /// never be overwritten by a later call in the same task. A file that does
    /// not exist is recorded as new so undo can delete it; a snapshot failure
    /// is recorded as skipped.
    ///
    /// # Errors
    ///
    /// Returns an error for invalid paths or on I/O failure.
    pub fn checkpoint_file(
        &self,
        task_id: &str,
        rel_path: &str,
    ) -> Result<Checkpoint, HistoryError> {
        let mut checkpoint = self.begin_checkpoint(task_id)?;
        let rel = clean_rel(rel_path)?;

        if checkpoint.has(&rel) {
            return Ok(checkpoint);
        }

        match self.snapshot(&rel) {
            Err(_) => checkpoint.skipped.push(rel),
            Ok(None) => checkpoint.new_files.push(rel),
            Ok(Some(entry)) => checkpoint.files.push(CheckpointFile {
                rel_path: rel,
                entry,
            }),
        }
        self.save_checkpoint(&checkpoint)?;
        Ok(checkpoint)
    }

    /// Restores the pre-task state as one unit: every recorded file is restored
    /// from its blob and every new file created by the task is deleted.
    ///
    /// Returns the checkpoint with any restore failures appended to `skipped` —
    /// a partial undo is reported, never silently claimed complete.
    ///
    /// # Errors
    ///
    /// Returns an error for an unknown or invalid task id, or on I/O failure.
    pub fn undo_checkpoint(&self, task_id: &str) -> Result<Checkpoint, HistoryError> {
        let mut checkpoint = self.load_checkpoint(task_id)?;
        if checkpoint.files.is_empty() && checkpoint.new_files.is_empty() {
            return Ok(checkpoint);
        }

        let mut failed: Vec<String> = Vec::new();
        for file in &checkpoint.files {
            if self
                .restore(&file.rel_path, Some(file.entry.id.as_str()))
                .is_err()
            {
                failed.push(file.rel_path.clone());
            }
        }

        let root = self.open_root()?;
        for rel in &checkpoint.new_files {
            match root.remove_file(Path::new(rel)) {
                Ok(()) => {}
                Err(err) if err.kind() == io::ErrorKind::NotFound => {}
                Err(_) => failed.push(rel.clone()),
            }
        }

        checkpoint.skipped.extend(failed);
        self.save_checkpoint(&checkpoint)?;
        Ok(checkpoint)
    }

    /// Returns all task checkpoints, newest first.
    ///
    /// Corrupt manifests are skipped rather than failing the listing, matching
    /// the Go oracle.
    ///
    /// # Errors
    ///
    /// Returns an error on I/O failure.
    pub fn list_checkpoints(&self) -> Result<Vec<Checkpoint>, HistoryError> {
        let root = self.open_root()?;
        let entries = match root.read_dir(Path::new(checkpoints_rel_dir())) {
            Ok(entries) => entries,
            Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(err) => return Err(HistoryError::Io(err)),
        };

        let mut names: Vec<String> = Vec::new();
        for entry in entries {
            let entry = entry.map_err(HistoryError::Io)?;
            let file_type = entry.file_type().map_err(HistoryError::Io)?;
            if file_type.is_dir() {
                continue;
            }
            let name = entry.file_name().to_string_lossy().into_owned();
            if name.ends_with(".json") {
                names.push(name);
            }
        }
        names.sort();

        let mut out = Vec::with_capacity(names.len());
        for name in names {
            let rel = format!("{}/{name}", checkpoints_rel_dir());
            let data = root.read(Path::new(&rel)).map_err(HistoryError::Io)?;
            if let Ok(checkpoint) = serde_json::from_slice::<Checkpoint>(&data) {
                out.push(checkpoint);
            }
        }
        out.sort_by_key(|checkpoint| std::cmp::Reverse(checkpoint.timestamp));
        Ok(out)
    }

    fn load_checkpoint(&self, task_id: &str) -> Result<Checkpoint, HistoryError> {
        let rel = checkpoint_rel_path(task_id)?;
        let root = self.open_root()?;
        let data = root.read(Path::new(&rel)).map_err(HistoryError::Io)?;
        serde_json::from_slice(&data).map_err(|err| {
            HistoryError::CorruptCheckpoint(task_id.to_owned(), err.to_string())
        })
    }

    fn save_checkpoint(&self, checkpoint: &Checkpoint) -> Result<(), HistoryError> {
        let root = self.open_root()?;
        mkdir_all_0750(root, Path::new(checkpoints_rel_dir())).map_err(HistoryError::Io)?;
        let rel = checkpoint_rel_path(&checkpoint.task_id)?;
        // Persist arrays as [] rather than null so a strict recovery preflight
        // can distinguish a valid empty list from malformed JSON null.
        let data = serde_json::to_vec_pretty(checkpoint)
            .map_err(|err| HistoryError::Io(io::Error::other(err)))?;
        self.write_file_atomic_root(&rel, &data, 0o644)
    }
}
