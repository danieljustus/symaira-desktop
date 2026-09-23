//! Fail-closed preflight and explicit removal of history recovery state.

use std::{collections::BTreeSet, io, path::Path};

use cap_std::fs::{Dir, DirEntry};
use serde_json::Value;

use super::checkpoint::checkpoints_rel_dir;
use super::{
    Checkpoint, HistoryEntry, HistoryError, HistoryStore, clean_rel, go_zero_time, history_rel_dir,
    is_hex_id, objects_rel_dir,
};

struct Manifest {
    path: String,
    rel_path: String,
    entries: Vec<HistoryEntry>,
}

struct PurgeCheckpoint {
    path: String,
    checkpoint: Checkpoint,
    matched: bool,
}

impl HistoryStore {
    /// Validates every stored history manifest, referenced blob, checkpoint,
    /// and object-store entry without changing vault state.
    ///
    /// `rel_paths` are vault-relative and are validated for parity with the Go
    /// API even though preflight checks the complete recovery inventory.
    ///
    /// # Errors
    /// Returns [`HistoryError::InvalidPath`] for unsafe requested paths,
    /// [`HistoryError::Purge`] for invalid recovery state, or [`HistoryError::Io`]
    /// for filesystem failures.
    pub fn preflight_purge_paths(&self, rel_paths: &[String]) -> Result<(), HistoryError> {
        for path in rel_paths {
            clean_rel(path)?;
        }
        let root = self.open_root()?;
        let manifest_paths = manifest_paths(root, true)?;
        for path in manifest_paths {
            let entries = decode_manifest(&root.read(Path::new(&path)).map_err(HistoryError::Io)?)
                .map_err(|detail| invalid_manifest(&path, detail))?;
            for entry in &entries {
                verify_entry(root, entry).map_err(|detail| invalid_manifest(&path, detail))?;
            }
        }

        for (name, path, file_type) in checkpoint_entries(root)? {
            if file_type.is_dir() || !name.ends_with(".json") {
                return Err(purge_error(format!("invalid checkpoint entry {name:?}")));
            }
            let checkpoint = read_checkpoint(root, &name, &path)?;
            for file in &checkpoint.files {
                verify_entry(root, &file.entry)
                    .map_err(|detail| invalid_checkpoint(&name, detail))?;
            }
        }

        for entry in entries_or_empty(root, objects_rel_dir())? {
            let name = entry_name(&entry)?;
            let file_type = entry.file_type().map_err(HistoryError::Io)?;
            if file_type.is_dir() || file_type.is_symlink() {
                return Err(purge_error(format!("invalid history object {name:?}")));
            }
        }
        Ok(())
    }

    /// Removes manifests and checkpoint references for the requested
    /// vault-relative paths, then deletes history blobs no longer referenced by
    /// any surviving manifest or checkpoint.
    ///
    /// Every manifest, checkpoint, and referenced object is validated before
    /// the first mutation so a corrupt survivor cannot make recovery damage
    /// permanent through garbage collection.
    ///
    /// # Errors
    /// Returns [`HistoryError::InvalidPath`] for unsafe requested paths,
    /// [`HistoryError::Purge`] for invalid recovery state, or [`HistoryError::Io`]
    /// for filesystem failures.
    pub fn purge_paths(&self, rel_paths: &[String]) -> Result<(), HistoryError> {
        let mut targets = BTreeSet::new();
        for path in rel_paths {
            targets.insert(clean_rel(path)?.replace('\\', "/"));
        }
        let root = self.open_root()?;

        let manifests = manifest_paths(root, false)?
            .into_iter()
            .map(|path| {
                let entries =
                    decode_manifest(&root.read(Path::new(&path)).map_err(HistoryError::Io)?)
                        .map_err(|detail| invalid_manifest(&path, detail))?;
                for entry in &entries {
                    verify_entry(root, entry).map_err(|detail| invalid_manifest(&path, detail))?;
                }
                let prefix = format!("{}/", manifest_dir());
                let rel_path = path
                    .strip_prefix(&prefix)
                    .and_then(|value| value.strip_suffix(".json"))
                    .ok_or_else(|| purge_error(format!("invalid history manifest path {path:?}")))?
                    .to_owned();
                Ok(Manifest {
                    path,
                    rel_path,
                    entries,
                })
            })
            .collect::<Result<Vec<_>, HistoryError>>()?;

        let checkpoints = checkpoint_entries(root)?
            .into_iter()
            .filter(|(name, _, file_type)| !file_type.is_dir() && name.ends_with(".json"))
            .map(|(name, path, _)| {
                read_checkpoint(root, &name, &path).map(|checkpoint| (path, checkpoint))
            })
            .collect::<Result<Vec<_>, _>>()?;

        let mut surviving_checkpoints = Vec::with_capacity(checkpoints.len());
        for (path, mut checkpoint) in checkpoints {
            let original_len =
                checkpoint.files.len() + checkpoint.new_files.len() + checkpoint.skipped.len();
            checkpoint
                .files
                .retain(|file| !targets.contains(&file.rel_path));
            checkpoint.new_files.retain(|file| !targets.contains(file));
            checkpoint.skipped.retain(|file| !targets.contains(file));
            let remaining_len =
                checkpoint.files.len() + checkpoint.new_files.len() + checkpoint.skipped.len();
            surviving_checkpoints.push(PurgeCheckpoint {
                path,
                checkpoint,
                matched: original_len != remaining_len,
            });
        }

        for manifest in &manifests {
            if targets.contains(&manifest.rel_path) {
                remove_file(root, &manifest.path)?;
            }
        }
        for record in &surviving_checkpoints {
            if !record.matched {
                continue;
            }
            let checkpoint = &record.checkpoint;
            if checkpoint.files.is_empty()
                && checkpoint.new_files.is_empty()
                && checkpoint.skipped.is_empty()
            {
                remove_file(root, &record.path)?;
            } else {
                let data = serde_json::to_vec_pretty(checkpoint)
                    .map_err(|err| HistoryError::Io(io::Error::other(err)))?;
                self.write_file_atomic_root(&record.path, &data, 0o644)?;
            }
        }

        let mut referenced = BTreeSet::new();
        for manifest in &manifests {
            if !targets.contains(&manifest.rel_path) {
                referenced.extend(manifest.entries.iter().map(|entry| entry.id.as_str()));
            }
        }
        for record in &surviving_checkpoints {
            referenced.extend(
                record
                    .checkpoint
                    .files
                    .iter()
                    .map(|file| file.entry.id.as_str()),
            );
        }
        for entry in entries_or_empty(root, objects_rel_dir())? {
            let name = entry_name(&entry)?;
            if entry.file_type().map_err(HistoryError::Io)?.is_dir()
                || referenced.contains(name.as_str())
            {
                continue;
            }
            remove_file(root, &format!("{}/{name}", objects_rel_dir()))?;
        }
        Ok(())
    }
}

fn manifest_dir() -> String {
    format!("{}/manifest", history_rel_dir())
}

fn manifest_paths(root: &Dir, strict: bool) -> Result<Vec<String>, HistoryError> {
    fn visit(
        root: &Dir,
        directory: &str,
        strict: bool,
        found: &mut Vec<String>,
    ) -> Result<(), HistoryError> {
        let mut entries = entries_or_empty(root, directory)?;
        entries.sort_by_key(|entry| entry.file_name());
        for entry in entries {
            let name = entry_name(&entry)?;
            let path = format!("{directory}/{name}");
            let file_type = entry.file_type().map_err(HistoryError::Io)?;
            if file_type.is_dir() {
                visit(root, &path, strict, found)?;
            } else if name.ends_with(".json") {
                found.push(path);
            } else if strict {
                return Err(purge_error(format!(
                    "invalid history manifest entry {path:?}"
                )));
            }
        }
        Ok(())
    }

    let mut found = Vec::new();
    visit(root, &manifest_dir(), strict, &mut found)?;
    Ok(found)
}

fn checkpoint_entries(
    root: &Dir,
) -> Result<Vec<(String, String, cap_std::fs::FileType)>, HistoryError> {
    let mut entries = entries_or_empty(root, checkpoints_rel_dir())?;
    entries.sort_by_key(|entry| entry.file_name());
    entries
        .into_iter()
        .map(|entry| {
            let name = entry_name(&entry)?;
            let file_type = entry.file_type().map_err(HistoryError::Io)?;
            let path = format!("{}/{name}", checkpoints_rel_dir());
            Ok((name, path, file_type))
        })
        .collect()
}

fn entries_or_empty(root: &Dir, path: &str) -> Result<Vec<DirEntry>, HistoryError> {
    match root.read_dir(Path::new(path)) {
        Ok(entries) => entries
            .map(|entry| entry.map_err(HistoryError::Io))
            .collect(),
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(Vec::new()),
        Err(err) => Err(HistoryError::Io(err)),
    }
}

fn entry_name(entry: &DirEntry) -> Result<String, HistoryError> {
    entry.file_name().into_string().map_err(|_| {
        HistoryError::Io(io::Error::new(
            io::ErrorKind::InvalidData,
            "non-UTF-8 history path",
        ))
    })
}

fn decode_manifest(data: &[u8]) -> Result<Vec<HistoryEntry>, String> {
    let entries: Option<Vec<HistoryEntry>> =
        serde_json::from_slice(data).map_err(|err| err.to_string())?;
    entries.ok_or_else(|| "manifest must be a non-null array".to_owned())
}

fn read_checkpoint(root: &Dir, name: &str, path: &str) -> Result<Checkpoint, HistoryError> {
    let data = root.read(Path::new(path)).map_err(HistoryError::Io)?;
    let invalid =
        |detail: String| purge_error(format!("invalid checkpoint manifest {name:?}: {detail}"));
    let value: Value = serde_json::from_slice(&data).map_err(|err| invalid(err.to_string()))?;
    let object = value
        .as_object()
        .ok_or_else(|| invalid("checkpoint must be a non-null object".to_owned()))?;
    for key in ["files", "new_files", "skipped"] {
        if object.get(key).is_some_and(Value::is_null) {
            return Err(invalid(format!("{key} must be a non-null array")));
        }
    }
    let checkpoint: Checkpoint =
        serde_json::from_value(value).map_err(|err| invalid(err.to_string()))?;
    if checkpoint.task_id.is_empty() || checkpoint.timestamp == go_zero_time() {
        return Err(invalid("task_id and timestamp are required".to_owned()));
    }
    super::checkpoint::validate_task_id(&checkpoint.task_id)
        .map_err(|err| invalid(err.to_string()))?;
    if name.strip_suffix(".json") != Some(checkpoint.task_id.as_str()) {
        return Err(invalid(format!(
            "task_id {:?} does not match filename",
            checkpoint.task_id
        )));
    }
    let mut seen = BTreeSet::new();
    for file in &checkpoint.files {
        validate_checkpoint_path(&mut seen, &file.rel_path)?;
    }
    for path in checkpoint.new_files.iter().chain(&checkpoint.skipped) {
        validate_checkpoint_path(&mut seen, path)?;
    }
    Ok(checkpoint)
}

fn validate_checkpoint_path(seen: &mut BTreeSet<String>, path: &str) -> Result<(), HistoryError> {
    let rel =
        clean_rel(path).map_err(|_| purge_error(format!("invalid checkpoint path {path:?}")))?;
    if rel.replace('\\', "/") != path {
        return Err(purge_error(format!("invalid checkpoint path {path:?}")));
    }
    if !seen.insert(path.to_owned()) {
        return Err(purge_error(format!("duplicate checkpoint path {path:?}")));
    }
    Ok(())
}

fn verify_entry(root: &Dir, entry: &HistoryEntry) -> Result<(), String> {
    if !is_hex_id(&entry.id) {
        return Err(format!("invalid referenced object id {:?}", entry.id));
    }
    if entry.timestamp == go_zero_time() || entry.size < 0 {
        return Err(format!("invalid snapshot entry {:?}", entry.id));
    }
    let path = format!("{}/{id}", objects_rel_dir(), id = entry.id);
    let metadata = root
        .symlink_metadata(Path::new(&path))
        .map_err(|err| format!("referenced object {} is unavailable: {err}", entry.id))?;
    if metadata.file_type().is_symlink() || !metadata.is_file() {
        return Err(format!(
            "referenced object {} is not a regular file",
            entry.id
        ));
    }
    let data = root
        .read(Path::new(&path))
        .map_err(|err| format!("read referenced object {}: {err}", entry.id))?;
    if i64::try_from(data.len()).unwrap_or(i64::MAX) != entry.size {
        return Err(format!(
            "referenced object {} size does not match manifest",
            entry.id
        ));
    }
    let digest = super::super::sha256::digest(&data);
    let mut hash = String::with_capacity(64);
    for byte in digest {
        use std::fmt::Write as _;
        let _ = write!(hash, "{byte:02x}");
    }
    if hash != entry.id {
        return Err(format!(
            "referenced object {} content hash does not match id",
            entry.id
        ));
    }
    Ok(())
}

fn invalid_manifest(path: &str, detail: String) -> HistoryError {
    purge_error(format!("invalid history manifest {path:?}: {detail}"))
}

fn invalid_checkpoint(name: &str, detail: String) -> HistoryError {
    purge_error(format!("invalid checkpoint manifest {name:?}: {detail}"))
}

fn purge_error(message: String) -> HistoryError {
    HistoryError::Purge(message)
}

fn remove_file(root: &Dir, path: &str) -> Result<(), HistoryError> {
    match root.remove_file(Path::new(path)) {
        Ok(()) => Ok(()),
        Err(err) if err.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(err) => Err(HistoryError::Io(err)),
    }
}
