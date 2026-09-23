//! Snapshot and checkpoint retention with content-addressed object collection.

use std::{
    collections::BTreeSet,
    error::Error,
    fmt, io,
    path::{Path, PathBuf},
};

use cap_std::fs::{Dir, DirEntry};
use time::{Date, Duration, OffsetDateTime};

use super::{HistoryError, HistoryStore, objects_rel_dir};

/// Retention limits for per-file snapshots and task checkpoints.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct HistoryRetentionPolicy {
    /// Keep at most this many snapshots per file; non-positive means unlimited.
    pub max_per_file: i64,
    /// Drop snapshots older than this duration; non-positive means unlimited.
    /// The newest snapshot for each file is always retained.
    pub max_age: Duration,
    /// Drop task checkpoints older than this duration; non-positive means unlimited.
    pub max_checkpoint_age: Duration,
}

/// Failure from [`HistoryStore::prune`], including Go's partial removed count.
#[derive(Debug)]
pub struct HistoryPruneError {
    removed: usize,
    source: HistoryError,
}

impl HistoryPruneError {
    /// Number of snapshots and checkpoints removed before the failure.
    #[must_use]
    pub const fn removed(&self) -> usize {
        self.removed
    }

    /// The underlying history or filesystem failure.
    #[must_use]
    pub const fn source_error(&self) -> &HistoryError {
        &self.source
    }
}

impl fmt::Display for HistoryPruneError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        self.source.fmt(formatter)
    }
}

impl Error for HistoryPruneError {
    fn source(&self) -> Option<&(dyn Error + 'static)> {
        Some(&self.source)
    }
}

fn failed(removed: usize, source: HistoryError) -> HistoryPruneError {
    HistoryPruneError { removed, source }
}

impl HistoryStore {
    /// Applies retention to all snapshot manifests, prunes aged checkpoints,
    /// and removes unreferenced content-addressed objects.
    ///
    /// This follows Go `HistoryStore.Prune`: old checkpoints lose blob
    /// protection before snapshot manifests are processed; the newest snapshot
    /// for each file survives age-based pruning; and garbage collection runs
    /// only after every manifest has been processed. On failure, the error
    /// exposes any already-removed count and leaves later steps unapplied.
    pub fn prune(&self, policy: HistoryRetentionPolicy) -> Result<usize, HistoryPruneError> {
        let mut removed = 0;
        let root = self.open_root().map_err(|error| failed(removed, error))?;
        let cutoff = (policy.max_age > Duration::ZERO).then(|| {
            self.now()
                .checked_sub(policy.max_age)
                .unwrap_or(Date::MIN.midnight().assume_utc())
        });

        if policy.max_checkpoint_age > Duration::ZERO {
            let checkpoint_cutoff = self
                .now()
                .checked_sub(policy.max_checkpoint_age)
                .unwrap_or(Date::MIN.midnight().assume_utc());
            let checkpoints = self
                .list_checkpoints()
                .map_err(|error| failed(removed, error))?;
            for checkpoint in checkpoints {
                if checkpoint.timestamp >= checkpoint_cutoff {
                    continue;
                }
                let path = super::checkpoint::checkpoint_rel_path(&checkpoint.task_id)
                    .map_err(|error| failed(removed, error))?;
                match root.remove_file(Path::new(&path)) {
                    Ok(()) => removed += 1,
                    Err(error) if error.kind() == io::ErrorKind::NotFound => removed += 1,
                    Err(error) => return Err(failed(removed, HistoryError::Io(error))),
                }
            }
        }

        let checkpoints = self
            .list_checkpoints()
            .map_err(|error| failed(removed, error))?;
        let mut referenced = BTreeSet::new();
        for checkpoint in checkpoints {
            referenced.extend(checkpoint.files.into_iter().map(|file| file.entry.id));
        }

        let manifest_root = format!("{}/manifest", super::history_rel_dir());
        visit_manifests(root, &manifest_root, &mut |path| {
            prune_manifest(
                self,
                root,
                &manifest_root,
                path,
                policy,
                cutoff,
                &mut removed,
                &mut referenced,
            )
        })
        .map_err(|error| failed(removed, error))?;

        let mut objects = match root.read_dir(Path::new(objects_rel_dir())) {
            Ok(entries) => entries
                .map(|entry| entry.map_err(HistoryError::Io))
                .collect::<Result<Vec<_>, _>>()
                .map_err(|error| failed(removed, error))?,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(removed),
            Err(error) => return Err(failed(removed, HistoryError::Io(error))),
        };
        objects.sort_by_key(DirEntry::file_name);
        for object in objects {
            if object
                .file_type()
                .map_err(|error| failed(removed, HistoryError::Io(error)))?
                .is_dir()
            {
                continue;
            }
            let name = object.file_name().into_string().map_err(|_| {
                failed(
                    removed,
                    HistoryError::Io(io::Error::new(
                        io::ErrorKind::InvalidData,
                        "non-UTF-8 history object name",
                    )),
                )
            })?;
            if !referenced.contains(&name) {
                root.remove_file(PathBuf::from(objects_rel_dir()).join(name))
                    .map_err(|error| failed(removed, HistoryError::Io(error)))?;
            }
        }
        Ok(removed)
    }
}

fn visit_manifests(
    root: &Dir,
    directory: &str,
    action: &mut impl FnMut(&str) -> Result<(), HistoryError>,
) -> Result<(), HistoryError> {
    fn visit(
        root: &Dir,
        directory: &str,
        action: &mut impl FnMut(&str) -> Result<(), HistoryError>,
    ) -> Result<(), HistoryError> {
        let mut entries = match root.read_dir(Path::new(directory)) {
            Ok(entries) => entries
                .map(|entry| entry.map_err(HistoryError::Io))
                .collect::<Result<Vec<_>, _>>()?,
            Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(()),
            Err(error) => return Err(HistoryError::Io(error)),
        };
        entries.sort_by_key(DirEntry::file_name);
        for entry in entries {
            let name = entry.file_name().into_string().map_err(|_| {
                HistoryError::Io(io::Error::new(
                    io::ErrorKind::InvalidData,
                    "non-UTF-8 history manifest path",
                ))
            })?;
            let path = format!("{directory}/{name}");
            if entry.file_type().map_err(HistoryError::Io)?.is_dir() {
                visit(root, &path, action)?;
            } else if name.ends_with(".json") {
                action(&path)?;
            }
        }
        Ok(())
    }

    visit(root, directory, action)
}

#[expect(
    clippy::too_many_arguments,
    reason = "retention state is shared across manifests"
)]
fn prune_manifest(
    store: &HistoryStore,
    root: &Dir,
    manifest_root: &str,
    path: &str,
    policy: HistoryRetentionPolicy,
    cutoff: Option<OffsetDateTime>,
    removed: &mut usize,
    referenced: &mut BTreeSet<String>,
) -> Result<(), HistoryError> {
    let relative = path
        .strip_prefix(&format!("{manifest_root}/"))
        .and_then(|value| value.strip_suffix(".json"))
        .ok_or_else(|| {
            HistoryError::Io(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("invalid history manifest path {path:?}"),
            ))
        })?;
    let entries = store.list(relative)?.unwrap_or_default();
    let original_len = entries.len();

    let mut kept = Vec::with_capacity(entries.len());
    for (index, entry) in entries.into_iter().enumerate() {
        let at_limit = policy.max_per_file > 0
            && i64::try_from(kept.len()).unwrap_or(i64::MAX) >= policy.max_per_file;
        let expired = index > 0 && cutoff.is_some_and(|cutoff| entry.timestamp < cutoff);
        if at_limit || expired {
            *removed += 1;
        } else {
            referenced.insert(entry.id.clone());
            kept.push(entry);
        }
    }

    if kept.len() == original_len {
        return Ok(());
    }
    if kept.is_empty() {
        root.remove_file(Path::new(path))
            .map_err(HistoryError::Io)?;
    } else {
        store.write_manifest(relative, &kept)?;
    }
    Ok(())
}
