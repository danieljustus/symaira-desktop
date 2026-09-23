#![deny(unsafe_code)]

//! Content-addressed local version snapshot and restore engine for vault files.
//!
//! Snapshots are immutable, content-addressed blobs stored under
//! `<vault_root>/.symdesk/history/objects/<sha256>`. Per-file history manifests
//! are stored under `<vault_root>/.symdesk/history/manifest/<relpath>.json`.
//! All filesystem access is strictly confined within the vault capability root
//! using [`cap_std::fs::Dir`].

pub mod checkpoint;
mod prune;
mod purge;
mod trash_purge;

pub use checkpoint::{Checkpoint, CheckpointFile};
pub use prune::{HistoryPruneError, HistoryRetentionPolicy};

use std::{
    fmt::Write as _,
    io,
    path::{Component, Path, PathBuf},
    sync::{Arc, OnceLock},
};

use cap_std::fs::Dir;
use serde::{Deserialize, Serialize};
use time::OffsetDateTime;

/// Describes one stored snapshot of a vault file.
#[derive(Clone, Debug, Serialize, PartialEq, Eq)]
pub struct HistoryEntry {
    /// ID is the lowercase hex SHA-256 of the snapshot content.
    pub id: String,
    /// Timestamp is when the snapshot was taken (UTC, RFC 3339 nano).
    #[serde(with = "rfc3339_nano")]
    pub timestamp: OffsetDateTime,
    /// Size is the content size in bytes.
    pub size: i64,
}

/// Returns the Go zero time `0001-01-01T00:00:00Z`.
#[must_use]
pub fn go_zero_time() -> OffsetDateTime {
    time::Date::from_calendar_date(1, time::Month::January, 1)
        .expect("valid Go zero date 0001-01-01")
        .midnight()
        .assume_utc()
}

impl Default for HistoryEntry {
    fn default() -> Self {
        Self {
            id: String::new(),
            timestamp: go_zero_time(),
            size: 0,
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum EntryField {
    Id,
    Timestamp,
    Size,
    Unknown,
}

fn match_entry_field(key: &str) -> EntryField {
    if is_fold_match(key, "id") {
        EntryField::Id
    } else if is_fold_match(key, "timestamp") {
        EntryField::Timestamp
    } else if is_fold_match(key, "size") {
        EntryField::Size
    } else {
        EntryField::Unknown
    }
}

fn is_fold_match(key: &str, target: &str) -> bool {
    let mut key_chars = key.chars();
    let mut target_chars = target.chars();

    loop {
        match (key_chars.next(), target_chars.next()) {
            (None, None) => return true,
            (Some(k), Some(t)) => {
                let k_norm = if k == 'ſ' {
                    's'
                } else {
                    k.to_ascii_lowercase()
                };
                let t_norm = if t == 'ſ' {
                    's'
                } else {
                    t.to_ascii_lowercase()
                };
                if k_norm != t_norm {
                    return false;
                }
            }
            _ => return false,
        }
    }
}

impl<'de> Deserialize<'de> for HistoryEntry {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        struct HistoryEntryVisitor;

        impl<'de> serde::de::Visitor<'de> for HistoryEntryVisitor {
            type Value = HistoryEntry;

            fn expecting(&self, formatter: &mut std::fmt::Formatter) -> std::fmt::Result {
                formatter.write_str("a history entry object or null")
            }

            fn visit_unit<E>(self) -> Result<Self::Value, E>
            where
                E: serde::de::Error,
            {
                Ok(HistoryEntry::default())
            }

            fn visit_none<E>(self) -> Result<Self::Value, E>
            where
                E: serde::de::Error,
            {
                Ok(HistoryEntry::default())
            }

            fn visit_map<M>(self, mut access: M) -> Result<Self::Value, M::Error>
            where
                M: serde::de::MapAccess<'de>,
            {
                let mut entry = HistoryEntry::default();

                while let Some(key) = access.next_key::<String>()? {
                    match match_entry_field(&key) {
                        EntryField::Id => {
                            let val: serde_json::Value = access.next_value()?;
                            match val {
                                serde_json::Value::String(s) => {
                                    entry.id = s;
                                }
                                serde_json::Value::Null => {}
                                other => {
                                    return Err(serde::de::Error::invalid_type(
                                        json_value_to_unexpected(&other),
                                        &"a string or null",
                                    ));
                                }
                            }
                        }
                        EntryField::Timestamp => {
                            let val: serde_json::Value = access.next_value()?;
                            match val {
                                serde_json::Value::String(s) => {
                                    let dt = OffsetDateTime::parse(
                                        &s,
                                        &time::format_description::well_known::Rfc3339,
                                    )
                                    .map_err(serde::de::Error::custom)?;
                                    entry.timestamp = dt;
                                }
                                serde_json::Value::Null => {}
                                other => {
                                    return Err(serde::de::Error::invalid_type(
                                        json_value_to_unexpected(&other),
                                        &"an RFC 3339 string or null",
                                    ));
                                }
                            }
                        }
                        EntryField::Size => {
                            let val: serde_json::Value = access.next_value()?;
                            match val {
                                serde_json::Value::Number(num) => {
                                    if let Some(n) = num.as_i64() {
                                        entry.size = n;
                                    } else if let Some(u) = num.as_u64() {
                                        if u <= i64::MAX as u64 {
                                            entry.size = u as i64;
                                        } else {
                                            return Err(serde::de::Error::custom(
                                                "size integer value out of range for i64",
                                            ));
                                        }
                                    } else {
                                        return Err(serde::de::Error::invalid_type(
                                            serde::de::Unexpected::Float(
                                                num.as_f64().unwrap_or(0.0),
                                            ),
                                            &"an integer or null",
                                        ));
                                    }
                                }
                                serde_json::Value::Null => {}
                                other => {
                                    return Err(serde::de::Error::invalid_type(
                                        json_value_to_unexpected(&other),
                                        &"an integer or null",
                                    ));
                                }
                            }
                        }
                        EntryField::Unknown => {
                            let _ignored: serde::de::IgnoredAny = access.next_value()?;
                        }
                    }
                }

                Ok(entry)
            }
        }

        deserializer.deserialize_any(HistoryEntryVisitor)
    }
}

fn json_value_to_unexpected(val: &serde_json::Value) -> serde::de::Unexpected<'_> {
    match val {
        serde_json::Value::Null => serde::de::Unexpected::Unit,
        serde_json::Value::Bool(b) => serde::de::Unexpected::Bool(*b),
        serde_json::Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                serde::de::Unexpected::Signed(i)
            } else if let Some(u) = n.as_u64() {
                serde::de::Unexpected::Unsigned(u)
            } else if let Some(f) = n.as_f64() {
                serde::de::Unexpected::Float(f)
            } else {
                serde::de::Unexpected::Other("number")
            }
        }
        serde_json::Value::String(s) => serde::de::Unexpected::Str(s),
        serde_json::Value::Array(_) => serde::de::Unexpected::Seq,
        serde_json::Value::Object(_) => serde::de::Unexpected::Map,
    }
}

/// Errors returned by the history snapshot engine.
#[derive(Debug, thiserror::Error)]
pub enum HistoryError {
    /// Vault-relative path is invalid or attempts traversal outside the vault.
    #[error("invalid vault-relative path: {0:?}")]
    InvalidPath(String),

    /// Snapshot identifier is not a valid 64-character lowercase hex SHA-256.
    #[error("invalid snapshot id: {0:?}")]
    InvalidId(String),

    /// Snapshot object blob was not found in the object store.
    #[error("snapshot object {0} not found")]
    NotFound(String),

    /// No snapshots have been recorded for the requested file.
    #[error("no snapshots recorded for {0}")]
    NoSnapshots(String),

    /// The snapshot ID prefix matched multiple snapshots for the file.
    #[error("snapshot id prefix {prefix:?} is ambiguous for {path}")]
    AmbiguousPrefix {
        /// The ambiguous search prefix.
        prefix: String,
        /// The vault-relative target path.
        path: String,
    },

    /// No snapshot matched the requested identifier or prefix for the file.
    #[error("no snapshot {id:?} for {path}")]
    NoSnapshot {
        /// The requested snapshot identifier or prefix.
        id: String,
        /// The vault-relative target path.
        path: String,
    },

    /// History manifest JSON file is corrupt or unparseable.
    #[error("corrupt history manifest for {0}: {1}")]
    CorruptManifest(String, String),

    /// The trash operation was refused because the target is a directory.
    #[error("cannot trash a directory: {0}")]
    TrashDirectory(String),

    /// The trash rename failed and cleaning up the metadata sidecar failed too.
    #[error("rename failed: {source} (cleanup also failed: {cleanup})")]
    RenameFailed {
        /// The failing rename.
        #[source]
        source: std::io::Error,
        /// Cleanup failure text.
        cleanup: String,
    },

    /// A task id is required for checkpoint operations.
    #[error("task id is required")]
    TaskIdRequired,

    /// Checkpoint task id is invalid (separators, leading dot, colon).
    #[error("invalid task id: {0:?}")]
    InvalidTaskId(String),

    /// Checkpoint manifest JSON file is corrupt or unparseable.
    #[error("corrupt checkpoint manifest for {0}: {1}")]
    CorruptCheckpoint(String, String),

    /// Invalid recovery metadata or history object inventory during purge.
    #[error("{0}")]
    Purge(String),

    /// A selected trash item's original path no longer matches its metadata.
    #[error("trash entry {0:?} original path changed")]
    TrashEntryOriginalPathChanged(String),

    /// Trash item name is invalid (separators or a bare dot segment).
    #[error("invalid trash item name: {0:?}")]
    TrashNameInvalid(String),

    /// Trash item metadata could not be found.
    #[error("trash item {name:?} not found: {source}")]
    TrashItemNotFound {
        /// Requested trash item name.
        name: String,
        /// Underlying I/O failure.
        #[source]
        source: std::io::Error,
    },

    /// Trash metadata is corrupt or inconsistent with its payload.
    #[error("corrupt trash metadata for {0:?}: {1}")]
    CorruptTrashMetadata(String, String),

    /// A strict trash inventory check rejected the trash directory.
    #[error("invalid trash inventory: {0}")]
    TrashInventory(String),

    /// Trash metadata declares a different item name than its file name.
    #[error("trash metadata name mismatch: file {name:?} declares {declared:?}")]
    TrashMetadataNameMismatch {
        /// Trash item name from the file name.
        name: String,
        /// Name the metadata declares.
        declared: String,
    },

    /// Trash metadata declares an original path that is not a clean vault path.
    #[error("trash metadata path mismatch for {name:?}: {path:?}")]
    TrashMetadataPathMismatch {
        /// Trash item name.
        name: String,
        /// Declared original path.
        path: String,
    },

    /// Trash metadata is structurally invalid (zero timestamp, negative size).
    #[error("invalid trash metadata for {0:?}")]
    TrashMetadataInvalid(String),

    /// Trash payload size does not match the recorded metadata.
    #[error("trash payload size mismatch for {0:?}")]
    TrashPayloadSizeMismatch(String),

    /// The trash item cannot be restored because its original path is taken.
    #[error("cannot restore {name}: {path} already exists")]
    TrashRestoreConflict {
        /// Trash item name.
        name: String,
        /// Vault-relative original path.
        path: String,
    },

    /// Underlying filesystem I/O error.
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),
}

/// Describes one soft-deleted vault file.
///
/// The field order and JSON shape are the Go contract
/// (`internal/history/trash.go`), including the RFC 3339 nano timestamp.
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
pub struct TrashEntry {
    /// Unique name of the item inside the trash directory.
    #[serde(default)]
    pub name: String,
    /// Vault-relative path the file was deleted from.
    #[serde(default)]
    pub original_path: String,
    /// When the file was moved to the trash (UTC). Missing fields default to
    /// the Go zero time so a hand-damaged metadata file is reported as invalid
    /// metadata instead of a parse error, matching the Go oracle.
    #[serde(with = "rfc3339_nano", default = "go_zero_time")]
    pub deleted_at: OffsetDateTime,
    /// File size in bytes at deletion time.
    #[serde(default)]
    pub size: i64,
}

/// Thread-safe clock callback for injecting deterministic timestamps during replay.
pub type HistoryClock = Arc<dyn Fn() -> OffsetDateTime + Send + Sync>;

/// Manages content-addressed snapshots and manifests for a single vault root.
pub struct HistoryStore {
    vault_root: PathBuf,
    clock: Option<HistoryClock>,
    root: OnceLock<Result<Dir, (io::ErrorKind, String)>>,
}

impl HistoryStore {
    /// Creates a `HistoryStore` rooted at `vault_root`.
    ///
    /// No directories or capabilities are opened until the first access.
    pub fn new(vault_root: impl AsRef<Path>) -> Self {
        Self {
            vault_root: vault_root.as_ref().to_path_buf(),
            clock: None,
            root: OnceLock::new(),
        }
    }

    /// Creates a `HistoryStore` with an injectable thread-safe clock.
    pub fn with_clock(
        vault_root: impl AsRef<Path>,
        clock: impl Fn() -> OffsetDateTime + Send + Sync + 'static,
    ) -> Self {
        Self {
            vault_root: vault_root.as_ref().to_path_buf(),
            clock: Some(Arc::new(clock)),
            root: OnceLock::new(),
        }
    }

    /// Returns the current timestamp from the clock callback or real UTC time.
    fn now(&self) -> OffsetDateTime {
        match &self.clock {
            Some(clock) => clock(),
            None => OffsetDateTime::now_utc(),
        }
    }

    /// Lazily opens and caches the root directory capability.
    fn open_root(&self) -> Result<&Dir, HistoryError> {
        let cached = self.root.get_or_init(|| {
            Dir::open_ambient_dir(&self.vault_root, cap_std::ambient_authority())
                .map_err(|err| (err.kind(), err.to_string()))
        });
        match cached {
            Ok(dir) => Ok(dir),
            Err((kind, msg)) => Err(HistoryError::Io(io::Error::new(*kind, msg.clone()))),
        }
    }

    /// Stores the current content of the vault file at `rel_path`.
    ///
    /// It is a no-op returning `Ok(None)` when the file does not exist, and a
    /// no-op returning `Ok(Some(entry))` when its content equals the most recent
    /// snapshot, so callers can invoke it unconditionally before any write.
    ///
    /// # Errors
    ///
    /// Returns an error on invalid path, permission denial, or I/O failure.
    pub fn snapshot(&self, rel_path: &str) -> Result<Option<HistoryEntry>, HistoryError> {
        let rel = clean_rel(rel_path)?;
        let root = self.open_root()?;

        let data = match root.read(Path::new(&rel)) {
            Ok(bytes) => bytes,
            Err(err) if err.kind() == io::ErrorKind::NotFound => {
                return Ok(None);
            }
            Err(err) => return Err(HistoryError::Io(err)),
        };

        let digest = crate::sha256::digest(&data);
        let mut id = String::with_capacity(64);
        for byte in digest {
            let _ = write!(id, "{byte:02x}");
        }

        let mut entries = self.list(&rel)?.unwrap_or_default();
        if !entries.is_empty() && entries[0].id == id {
            return Ok(Some(entries[0].clone()));
        }

        let objects_rel = objects_rel_dir();
        mkdir_all_0750(root, Path::new(objects_rel)).map_err(HistoryError::Io)?;

        let obj_rel = format!("{objects_rel}/{id}");
        match root.metadata(Path::new(&obj_rel)) {
            Ok(_) => {
                // Content-addressed object already exists; immutable.
            }
            Err(err) if err.kind() == io::ErrorKind::NotFound => {
                self.write_file_atomic_root(&obj_rel, &data, 0o644)?;
            }
            Err(err) => return Err(HistoryError::Io(err)),
        }

        let entry = HistoryEntry {
            id,
            timestamp: self.now(),
            size: i64::try_from(data.len()).unwrap_or(i64::MAX),
        };

        entries.insert(0, entry.clone());
        self.write_manifest(&rel, &entries)?;

        Ok(Some(entry))
    }

    /// Returns the snapshots recorded for `rel_path`, newest first.
    ///
    /// Returns `Ok(None)` if no manifest exists or if the manifest is JSON null.
    /// Returns `Ok(Some(vec![]))` if the manifest is empty.
    ///
    /// # Errors
    ///
    /// Returns an error on invalid path, corrupt manifest JSON, or I/O failure.
    pub fn list(&self, rel_path: &str) -> Result<Option<Vec<HistoryEntry>>, HistoryError> {
        let rel_mp = manifest_rel_path(rel_path)?;
        let root = self.open_root()?;

        let data = match root.read(Path::new(&rel_mp)) {
            Ok(bytes) => bytes,
            Err(err) if err.kind() == io::ErrorKind::NotFound => {
                return Ok(None);
            }
            Err(err) => return Err(HistoryError::Io(err)),
        };

        let entries: Option<Vec<HistoryEntry>> = serde_json::from_slice(&data)
            .map_err(|err| HistoryError::CorruptManifest(rel_path.to_owned(), err.to_string()))?;

        match entries {
            Some(mut list) => {
                list.sort_by_key(|a| std::cmp::Reverse(a.timestamp));
                Ok(Some(list))
            }
            None => Ok(None),
        }
    }

    /// Returns the stored content bytes of a snapshot object by its 64-char hex ID.
    ///
    /// # Errors
    ///
    /// Returns `HistoryError::InvalidId` for invalid hex, or `HistoryError::NotFound`
    /// if the object blob is missing.
    pub fn content(&self, id: &str) -> Result<Vec<u8>, HistoryError> {
        if !is_hex_id(id) {
            return Err(HistoryError::InvalidId(id.to_owned()));
        }

        let root = self.open_root()?;
        let obj_rel = format!("{}/{id}", objects_rel_dir());
        match root.read(Path::new(&obj_rel)) {
            Ok(bytes) => Ok(bytes),
            Err(err) if err.kind() == io::ErrorKind::NotFound => {
                Err(HistoryError::NotFound(id.to_owned()))
            }
            Err(err) => Err(HistoryError::Io(err)),
        }
    }

    /// Writes the snapshot identified by `id` (or, if `None` / empty, the most
    /// recent snapshot) back to the vault file at `rel_path`.
    ///
    /// The current file content is snapshotted first, so a restore is itself undoable.
    /// `id` may be a unique prefix of the full hash.
    ///
    /// # Errors
    ///
    /// Returns an error on invalid path, no snapshots recorded, ambiguous prefix,
    /// or I/O failure.
    pub fn restore(&self, rel_path: &str, id: Option<&str>) -> Result<HistoryEntry, HistoryError> {
        let rel = clean_rel(rel_path)?;
        let entries = self.list(&rel)?.unwrap_or_default();
        if entries.is_empty() {
            return Err(HistoryError::NoSnapshots(rel_path.to_owned()));
        }

        let target = match id {
            None | Some("") => &entries[0],
            Some(prefix) => {
                let mut candidate: Option<&HistoryEntry> = None;
                for entry in &entries {
                    if entry.id.starts_with(prefix) {
                        if candidate.is_some() {
                            return Err(HistoryError::AmbiguousPrefix {
                                prefix: prefix.to_owned(),
                                path: rel_path.to_owned(),
                            });
                        }
                        candidate = Some(entry);
                    }
                }
                match candidate {
                    Some(entry) => entry,
                    None => {
                        return Err(HistoryError::NoSnapshot {
                            id: prefix.to_owned(),
                            path: rel_path.to_owned(),
                        });
                    }
                }
            }
        };

        let target_entry = target.clone();
        let data = self.content(&target_entry.id)?;

        // Preserve the pre-restore state as its own snapshot.
        self.snapshot(&rel)?;

        let root = self.open_root()?;
        if let Some(parent) = Path::new(&rel).parent()
            && !parent.as_os_str().is_empty()
        {
            mkdir_all_0750(root, parent).map_err(HistoryError::Io)?;
        }

        self.write_file_atomic_root(&rel, &data, 0o644)?;
        Ok(target_entry)
    }

    /// Writes entries formatted as JSON manifest atomically to the manifest file.
    fn write_manifest(&self, rel: &str, entries: &[HistoryEntry]) -> Result<(), HistoryError> {
        let rel_mp = manifest_rel_path(rel)?;
        let root = self.open_root()?;
        if let Some(parent) = Path::new(&rel_mp).parent()
            && !parent.as_os_str().is_empty()
        {
            mkdir_all_0750(root, parent).map_err(HistoryError::Io)?;
        }
        let data = serde_json::to_vec_pretty(entries)
            .map_err(|err| HistoryError::Io(io::Error::other(err)))?;
        self.write_file_atomic_root(&rel_mp, &data, 0o644)
    }

    /// Moves the vault file at `rel_path` into the trash instead of deleting it.
    ///
    /// Mirrors `Store.Trash` in `internal/history/trash.go`: a snapshot of the
    /// final content is taken first (so even a purged item stays recoverable
    /// until history retention drops it), the file's relative path is flattened
    /// into a unique trash name, the metadata sidecar is written atomically and
    /// the file is renamed into the trash directory. A failed rename removes the
    /// metadata again.
    ///
    /// # Errors
    ///
    /// Returns [`HistoryError::TrashDirectory`] for directories,
    /// [`HistoryError::InvalidPath`] for unsafe paths, and [`HistoryError::Io`]
    /// for missing files or filesystem failures.
    pub fn trash(&self, rel_path: &str) -> Result<TrashEntry, HistoryError> {
        let rel = clean_rel(rel_path)?;
        let root = self.open_root()?;
        let info = root.metadata(Path::new(&rel))?;
        if info.is_dir() {
            return Err(HistoryError::TrashDirectory(rel_path.to_owned()));
        }

        self.snapshot(&rel)?;

        let dir = trash_rel_dir();
        mkdir_all_0750(root, Path::new(dir)).map_err(HistoryError::Io)?;

        // Flatten the relative path into a unique trash name.
        let base = rel.replace('/', "__");
        let mut name = base.clone();
        let mut counter = 1;
        while root.metadata(Path::new(&format!("{dir}/{name}"))).is_ok() {
            name = format!("{base}.{counter}");
            counter += 1;
        }

        let entry = TrashEntry {
            name: name.clone(),
            original_path: rel.clone(),
            deleted_at: self.now(),
            size: i64::try_from(info.len()).unwrap_or(i64::MAX),
        };
        let metadata = serde_json::to_vec_pretty(&entry)
            .map_err(|err| HistoryError::Io(io::Error::other(err)))?;
        let trash_rel = format!("{dir}/{name}");
        let meta_rel = format!("{trash_rel}{TRASH_META_SUFFIX}");
        self.write_file_atomic_root(&meta_rel, &metadata, 0o644)?;

        if let Err(err) = root.rename(Path::new(&rel), root, Path::new(&trash_rel)) {
            return match root.remove_file(Path::new(&meta_rel)) {
                Ok(()) => Err(HistoryError::Io(err)),
                Err(cleanup) => Err(HistoryError::RenameFailed {
                    source: err,
                    cleanup: cleanup.to_string(),
                }),
            };
        }
        Ok(entry)
    }

    /// Returns all trash entries, newest deletion first.
    ///
    /// Corrupt metadata is skipped rather than failing the listing, matching the
    /// Go oracle.
    ///
    /// # Errors
    ///
    /// Returns an error on I/O failure.
    pub fn trash_list(&self) -> Result<Vec<TrashEntry>, HistoryError> {
        let root = self.open_root()?;
        let dir = trash_rel_dir();
        let entries = match root.read_dir(Path::new(dir)) {
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
            if name.ends_with(TRASH_META_SUFFIX) {
                names.push(name);
            }
        }
        names.sort();

        let mut out = Vec::new();
        for name in names {
            let data = root
                .read(Path::new(&format!("{dir}/{name}")))
                .map_err(HistoryError::Io)?;
            if let Ok(entry) = serde_json::from_slice::<TrashEntry>(&data) {
                out.push(entry);
            }
        }
        out.sort_by_key(|entry| std::cmp::Reverse(entry.deleted_at));
        Ok(out)
    }

    /// Returns a complete, validated trash inventory for destructive callers.
    ///
    /// Unlike [`Self::trash_list`], it fails closed on malformed metadata and
    /// verifies that every payload has exactly one matching metadata file (and
    /// vice versa).
    ///
    /// # Errors
    ///
    /// Returns [`HistoryError::TrashInventory`] for any inventory defect.
    pub fn trash_list_strict(&self) -> Result<Vec<TrashEntry>, HistoryError> {
        let root = self.open_root()?;
        let dir = trash_rel_dir();
        let entries = match root.read_dir(Path::new(dir)) {
            Ok(entries) => entries,
            Err(err) if err.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(err) => return Err(HistoryError::Io(err)),
        };

        let mut payloads: Vec<String> = Vec::new();
        let mut metadata: Vec<String> = Vec::new();
        for entry in entries {
            let entry = entry.map_err(HistoryError::Io)?;
            let name = entry.file_name().to_string_lossy().into_owned();
            if entry.file_type().map_err(HistoryError::Io)?.is_dir() {
                return Err(HistoryError::TrashInventory(format!("directory {name:?}")));
            }
            if let Some(stripped) = name.strip_suffix(TRASH_META_SUFFIX) {
                if stripped.is_empty() {
                    return Err(HistoryError::TrashInventory(format!(
                        "invalid trash metadata name {name:?}"
                    )));
                }
                metadata.push(stripped.to_owned());
                continue;
            }
            let info = root
                .symlink_metadata(Path::new(&format!("{dir}/{name}")))
                .map_err(|err| {
                    HistoryError::TrashInventory(format!("stat trash payload {name:?}: {err}"))
                })?;
            if !info.is_file() {
                return Err(HistoryError::TrashInventory(format!(
                    "invalid trash payload {name:?}: not a regular file"
                )));
            }
            payloads.push(name);
        }

        if payloads.len() != metadata.len() {
            return Err(HistoryError::TrashInventory(
                "payload/metadata count mismatch".to_owned(),
            ));
        }
        for name in &payloads {
            if !metadata.iter().any(|other| other == name) {
                return Err(HistoryError::TrashInventory(format!(
                    "payload {name:?} has no metadata"
                )));
            }
        }
        for name in &metadata {
            if !payloads.iter().any(|other| other == name) {
                return Err(HistoryError::TrashInventory(format!(
                    "metadata {name:?} has no payload"
                )));
            }
        }

        let mut entries = Vec::with_capacity(payloads.len());
        for name in &payloads {
            let meta_rel = format!("{dir}/{name}{TRASH_META_SUFFIX}");
            let data = root
                .read(Path::new(&meta_rel))
                .map_err(|err| HistoryError::CorruptTrashMetadata(name.clone(), err.to_string()))?;
            if serde_json::from_slice::<serde_json::Value>(&data)
                .map(|value| value.is_null())
                .unwrap_or(false)
            {
                return Err(HistoryError::CorruptTrashMetadata(
                    name.clone(),
                    "must be a non-null object".to_owned(),
                ));
            }
            let entry: TrashEntry = serde_json::from_slice(&data)
                .map_err(|err| HistoryError::CorruptTrashMetadata(name.clone(), err.to_string()))?;
            if entry.name != *name {
                return Err(HistoryError::TrashMetadataNameMismatch {
                    name: name.clone(),
                    declared: entry.name.clone(),
                });
            }
            let rel = clean_rel(&entry.original_path).unwrap_or_default();
            if rel != entry.original_path {
                return Err(HistoryError::TrashMetadataPathMismatch {
                    name: name.clone(),
                    path: entry.original_path.clone(),
                });
            }
            if !entry_is_valid(&entry) {
                return Err(HistoryError::TrashMetadataInvalid(name.clone()));
            }
            let payload = root
                .symlink_metadata(Path::new(&format!("{dir}/{name}")))
                .map_err(|err| HistoryError::CorruptTrashMetadata(name.clone(), err.to_string()))?;
            if i64::try_from(payload.len()).unwrap_or(i64::MAX) != entry.size {
                return Err(HistoryError::TrashPayloadSizeMismatch(name.clone()));
            }
            entries.push(entry);
        }
        entries.sort_by_key(|entry| std::cmp::Reverse(entry.deleted_at));
        Ok(entries)
    }

    /// Moves a trash item back to its original vault path.
    ///
    /// If the original path is occupied the restore fails and the trash item is
    /// kept.
    ///
    /// # Errors
    ///
    /// Returns [`HistoryError::TrashItemNotFound`],
    /// [`HistoryError::TrashRestoreConflict`] or an I/O failure.
    pub fn trash_restore(&self, name: &str) -> Result<TrashEntry, HistoryError> {
        let entry = self.trash_entry(name)?;
        let rel = clean_rel(&entry.original_path)?;
        let root = self.open_root()?;
        if root.metadata(Path::new(&rel)).is_ok() {
            return Err(HistoryError::TrashRestoreConflict {
                name: name.to_owned(),
                path: entry.original_path.clone(),
            });
        }
        let parent = Path::new(&rel)
            .parent()
            .filter(|dir| !dir.as_os_str().is_empty());
        if let Some(dir) = parent {
            mkdir_all_0750(root, dir).map_err(HistoryError::Io)?;
        }
        let source = format!("{}/{name}", trash_rel_dir());
        root.rename(Path::new(&source), root, Path::new(&rel))
            .map_err(HistoryError::Io)?;
        let meta = format!("{source}{TRASH_META_SUFFIX}");
        match root.remove_file(Path::new(&meta)) {
            Ok(()) => {}
            Err(err) if err.kind() == io::ErrorKind::NotFound => {}
            Err(err) => return Err(HistoryError::Io(err)),
        }
        Ok(entry)
    }

    /// Permanently removes trash items deleted more than `max_age` ago.
    ///
    /// A non-positive `max_age` purges everything. Returns the number of purged
    /// items. The full strict inventory is validated first, so a corrupt trash
    /// directory refuses the purge instead of dropping data.
    ///
    /// # Errors
    ///
    /// Returns an error when the inventory is invalid or on I/O failure.
    pub fn trash_purge(&self, max_age: time::Duration) -> Result<usize, HistoryError> {
        let entries = self.trash_list_strict()?;
        let root = self.open_root()?;
        let cutoff = self.now() - max_age;
        let mut purged = 0usize;
        for entry in entries {
            if max_age.is_positive() && entry.deleted_at > cutoff {
                continue;
            }
            for name in [
                format!("{}/{}", trash_rel_dir(), entry.name),
                format!("{}/{}{}", trash_rel_dir(), entry.name, TRASH_META_SUFFIX),
            ] {
                match root.remove_file(Path::new(&name)) {
                    Ok(()) => {}
                    Err(err) if err.kind() == io::ErrorKind::NotFound => {}
                    Err(err) => return Err(HistoryError::Io(err)),
                }
            }
            purged += 1;
        }
        Ok(purged)
    }

    /// Loads one trash entry by its item name.
    ///
    /// # Errors
    ///
    /// Returns [`HistoryError::TrashNameInvalid`] for a name with separators,
    /// [`HistoryError::TrashItemNotFound`] when the metadata is missing, and
    /// [`HistoryError::CorruptTrashMetadata`] when it does not parse.
    pub fn trash_entry(&self, name: &str) -> Result<TrashEntry, HistoryError> {
        if name.contains(['/', '\\']) || name == "." || name == ".." {
            return Err(HistoryError::TrashNameInvalid(name.to_owned()));
        }
        let root = self.open_root()?;
        let meta = format!("{}/{name}{TRASH_META_SUFFIX}", trash_rel_dir());
        let data =
            root.read(Path::new(&meta))
                .map_err(|source| HistoryError::TrashItemNotFound {
                    name: name.to_owned(),
                    source,
                })?;
        let mut entry: TrashEntry = serde_json::from_slice(&data)
            .map_err(|err| HistoryError::CorruptTrashMetadata(name.to_owned(), err.to_string()))?;
        entry.name = name.to_owned();
        Ok(entry)
    }

    /// Atomically writes data to `name` (relative to root) via a temporary file
    /// created alongside it and renamed into place. Every step is confined to `root`.
    fn write_file_atomic_root(
        &self,
        name: &str,
        data: &[u8],
        perm: u32,
    ) -> Result<(), HistoryError> {
        let root = self.open_root()?;
        let (tmp_file, tmp_name) = self.create_root_temp(name, ".symdesk-history-")?;

        let write_res = (|| -> Result<(), io::Error> {
            let mut tmp = tmp_file;
            use std::io::Write;
            tmp.write_all(data)?;
            tmp.sync_all()?;
            drop(tmp);

            #[cfg(unix)]
            {
                use cap_std::fs::PermissionsExt;
                let permissions = cap_std::fs::Permissions::from_mode(perm);
                root.set_permissions(Path::new(&tmp_name), permissions)?;
            }
            #[cfg(not(unix))]
            {
                let _ = perm;
            }

            root.rename(Path::new(&tmp_name), root, Path::new(name))?;
            Ok(())
        })();

        if let Err(err) = write_res {
            let _ = root.remove_file(Path::new(&tmp_name));
            return Err(HistoryError::Io(err));
        }

        Ok(())
    }

    /// Creates a uniquely-named temporary file inside the target's directory relative to root.
    fn create_root_temp(
        &self,
        target_name: &str,
        prefix: &str,
    ) -> Result<(cap_std::fs::File, String), HistoryError> {
        let root = self.open_root()?;
        let parent = Path::new(target_name).parent().unwrap_or(Path::new(""));

        for _ in 0..100 {
            let rand_bytes = random_12_bytes()?;
            let mut hex_suffix = String::with_capacity(24);
            for b in rand_bytes {
                let _ = write!(hex_suffix, "{b:02x}");
            }
            let file_name = format!("{prefix}{hex_suffix}.tmp");
            let rel_tmp = if parent.as_os_str().is_empty() {
                file_name
            } else {
                format!(
                    "{}/{file_name}",
                    parent.to_string_lossy().replace('\\', "/")
                )
            };

            let mut opts = cap_std::fs::OpenOptions::new();
            opts.write(true).create_new(true);
            #[cfg(unix)]
            {
                use cap_std::fs::OpenOptionsExt;
                opts.mode(0o600);
            }

            match root.open_with(Path::new(&rel_tmp), &opts) {
                Ok(file) => return Ok((file, rel_tmp)),
                Err(err) if err.kind() == io::ErrorKind::AlreadyExists => {
                    continue;
                }
                Err(err) => return Err(HistoryError::Io(err)),
            }
        }

        Err(HistoryError::Io(io::Error::other(
            "create temporary file: too many collisions",
        )))
    }
}

/// Recursively creates a directory and all parent components with mode `0o750` on Unix.
fn mkdir_all_0750(root: &Dir, path: &Path) -> io::Result<()> {
    let mut builder = cap_std::fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use cap_std::fs::DirBuilderExt;
        builder.mode(0o750);
    }
    root.create_dir_with(path, &builder)
}

/// Generates 12 cryptographically secure random bytes using getrandom.
pub fn random_12_bytes() -> Result<[u8; 12], HistoryError> {
    let mut buf = [0u8; 12];
    getrandom::fill(&mut buf).map_err(|err| HistoryError::Io(io::Error::other(err)))?;
    Ok(buf)
}

/// Relative directory where soft-deleted files are kept.
#[must_use]
pub fn trash_rel_dir() -> &'static str {
    ".symdesk/trash"
}

/// Reports whether a trash entry carries a usable timestamp and size, matching
/// the Go oracle's `entry.DeletedAt.IsZero() || entry.Size < 0` check.
fn entry_is_valid(entry: &TrashEntry) -> bool {
    entry.deleted_at != go_zero_time() && entry.size >= 0
}

/// Suffix of the per-item trash metadata sidecar.
pub const TRASH_META_SUFFIX: &str = ".trashinfo.json";

/// Relative directory where history artifacts are stored.
#[must_use]
pub fn history_rel_dir() -> &'static str {
    ".symdesk/history"
}

/// Relative directory where content-addressed objects are stored.
#[must_use]
pub fn objects_rel_dir() -> &'static str {
    ".symdesk/history/objects"
}

/// Computes the vault-relative manifest path for a given file path.
///
/// # Errors
///
/// Returns `HistoryError::InvalidPath` if `rel_path` is invalid or attempts traversal.
pub fn manifest_rel_path(rel_path: &str) -> Result<String, HistoryError> {
    let clean = clean_rel(rel_path)?;
    Ok(format!("{}/manifest/{clean}.json", history_rel_dir()))
}

/// Normalizes a vault-relative path and rejects traversal outside the vault.
///
/// # Errors
///
/// Returns `HistoryError::InvalidPath` for empty, absolute, or traversing paths.
pub fn clean_rel(rel_path: &str) -> Result<String, HistoryError> {
    if rel_path.is_empty() {
        return Err(HistoryError::InvalidPath(rel_path.to_owned()));
    }

    let path = Path::new(rel_path);
    if path.is_absolute() {
        return Err(HistoryError::InvalidPath(rel_path.to_owned()));
    }

    let mut components = Vec::new();
    for comp in path.components() {
        match comp {
            Component::Prefix(_) | Component::RootDir => {
                return Err(HistoryError::InvalidPath(rel_path.to_owned()));
            }
            Component::CurDir => {
                // skip "."
            }
            Component::ParentDir => {
                if components.pop().is_none() {
                    return Err(HistoryError::InvalidPath(rel_path.to_owned()));
                }
            }
            Component::Normal(c) => {
                let s = c
                    .to_str()
                    .ok_or_else(|| HistoryError::InvalidPath(rel_path.to_owned()))?;
                components.push(s);
            }
        }
    }

    if components.is_empty() {
        return Err(HistoryError::InvalidPath(rel_path.to_owned()));
    }

    Ok(components.join("/"))
}

/// Returns true if `id` is a 64-character lowercase hex SHA-256 string.
#[must_use]
pub fn is_hex_id(id: &str) -> bool {
    id.len() == 64 && id.bytes().all(|b| b.is_ascii_hexdigit())
}

/// Formats an [`OffsetDateTime`] into Go-compatible RFC 3339 nano format, preserving the stored timezone offset.
///
/// If nanoseconds are zero, no fractional dot is emitted (e.g. `2026-09-15T05:27:35Z` or `2026-09-15T15:04:05+05:30`).
/// Trailing zeros in the nanosecond fraction are stripped (e.g. `.123Z` or `.123456+05:30`).
/// If the timezone offset is UTC / zero, `Z` is emitted as the offset suffix.
#[must_use]
pub fn format_rfc3339_nano(dt: OffsetDateTime) -> String {
    let year = dt.year();
    let month = dt.month() as u8;
    let day = dt.day();
    let hour = dt.hour();
    let minute = dt.minute();
    let second = dt.second();
    let nanos = dt.nanosecond();

    let total_offset_secs = dt.offset().whole_seconds();
    let offset_str = if total_offset_secs == 0 {
        "Z".to_string()
    } else {
        let sign = if total_offset_secs < 0 { '-' } else { '+' };
        let abs_secs = total_offset_secs.unsigned_abs();
        let hours = abs_secs / 3600;
        let minutes = (abs_secs % 3600) / 60;
        format!("{sign}{hours:02}:{minutes:02}")
    };

    if nanos == 0 {
        format!("{year:04}-{month:02}-{day:02}T{hour:02}:{minute:02}:{second:02}{offset_str}")
    } else {
        let nanos_str = format!("{nanos:09}");
        let trimmed = nanos_str.trim_end_matches('0');
        format!(
            "{year:04}-{month:02}-{day:02}T{hour:02}:{minute:02}:{second:02}.{trimmed}{offset_str}"
        )
    }
}

/// Formats an [`OffsetDateTime`] into Go-compatible RFC 3339 nano UTC format.
///
/// If nanoseconds are zero, no fractional dot is emitted (e.g. `2026-09-15T05:27:35Z`).
/// Trailing zeros in the nanosecond fraction are stripped (e.g. `.123Z`).
#[must_use]
pub fn format_rfc3339_nano_utc(dt: OffsetDateTime) -> String {
    format_rfc3339_nano(dt.to_offset(time::UtcOffset::UTC))
}

/// Custom serde serializer/deserializer matching exact Go `time.RFC3339Nano`.
pub mod rfc3339_nano {
    use serde::{Deserialize, Deserializer, Serializer};
    use time::OffsetDateTime;
    use time::format_description::well_known::Rfc3339;

    /// Serializes an [`OffsetDateTime`] using Go-compatible RFC 3339 nano formatting
    /// preserving the stored timezone offset.
    ///
    /// # Errors
    ///
    /// Returns a serializer error if serialization fails.
    pub fn serialize<S>(date: &OffsetDateTime, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: Serializer,
    {
        let s = super::format_rfc3339_nano(*date);
        serializer.serialize_str(&s)
    }

    /// Deserializes an [`OffsetDateTime`] from an RFC 3339 formatted string.
    ///
    /// # Errors
    ///
    /// Returns a deserializer error if the input string is not valid RFC 3339.
    pub fn deserialize<'de, D>(deserializer: D) -> Result<OffsetDateTime, D::Error>
    where
        D: Deserializer<'de>,
    {
        let s = String::deserialize(deserializer)?;
        OffsetDateTime::parse(&s, &Rfc3339).map_err(serde::de::Error::custom)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::fs;

    fn new_test_vault() -> (PathBuf, HistoryStore) {
        let rand_bytes = random_12_bytes().expect("random bytes");
        let hex_suffix: String = rand_bytes.iter().map(|b| format!("{b:02x}")).collect();
        let dir = std::env::temp_dir().join(format!(
            "symdesk-history-test-{}-{hex_suffix}",
            std::process::id()
        ));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).expect("create test vault root");
        let store = HistoryStore::new(&dir);
        (dir, store)
    }

    fn write_test_file(root: &Path, rel: &str, content: &[u8]) {
        let p = root.join(rel);
        if let Some(parent) = p.parent() {
            fs::create_dir_all(parent).expect("create parent dir");
        }
        fs::write(p, content).expect("write test file");
    }

    fn read_test_file(root: &Path, rel: &str) -> Vec<u8> {
        fs::read(root.join(rel)).expect("read test file")
    }

    #[test]
    fn test_clean_rel() {
        assert_eq!(clean_rel("notes/a.md").unwrap(), "notes/a.md");
        assert_eq!(clean_rel("a/b/../c.md").unwrap(), "a/c.md");
        assert_eq!(clean_rel("a/./b.md").unwrap(), "a/b.md");

        assert!(clean_rel("").is_err());
        assert!(clean_rel(".").is_err());
        assert!(clean_rel("..").is_err());
        assert!(clean_rel("../evil.md").is_err());
        assert!(clean_rel("/abs/evil.md").is_err());
        assert!(clean_rel("notes/../../evil.md").is_err());
    }

    #[test]
    fn test_is_hex_id() {
        assert!(is_hex_id(
            "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"
        ));
        assert!(is_hex_id(
            "0000000000000000000000000000000000000000000000000000000000000000"
        ));
        assert!(!is_hex_id("short"));
        assert!(!is_hex_id(
            "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ag"
        ));
    }

    #[test]
    fn test_format_rfc3339_nano_and_utc() {
        let dt = OffsetDateTime::from_unix_timestamp(1_700_000_000).unwrap();
        assert_eq!(format_rfc3339_nano_utc(dt), "2023-11-14T22:13:20Z");
        assert_eq!(format_rfc3339_nano(dt), "2023-11-14T22:13:20Z");

        let dt_nanos = dt
            .replace_nanosecond(123_456_789)
            .expect("valid nanoseconds");
        assert_eq!(
            format_rfc3339_nano_utc(dt_nanos),
            "2023-11-14T22:13:20.123456789Z"
        );
        assert_eq!(
            format_rfc3339_nano(dt_nanos),
            "2023-11-14T22:13:20.123456789Z"
        );

        let dt_trailing = dt
            .replace_nanosecond(100_000_000)
            .expect("valid nanoseconds");
        assert_eq!(
            format_rfc3339_nano_utc(dt_trailing),
            "2023-11-14T22:13:20.1Z"
        );
        assert_eq!(format_rfc3339_nano(dt_trailing), "2023-11-14T22:13:20.1Z");

        // Positive fractional offset +05:30
        let dt_plus = OffsetDateTime::parse(
            "2026-09-15T15:04:05.123456+05:30",
            &time::format_description::well_known::Rfc3339,
        )
        .unwrap();
        assert_eq!(
            format_rfc3339_nano(dt_plus),
            "2026-09-15T15:04:05.123456+05:30"
        );
        assert_eq!(
            format_rfc3339_nano_utc(dt_plus),
            "2026-09-15T09:34:05.123456Z"
        );

        // Negative fractional offset -00:30
        let dt_minus = OffsetDateTime::parse(
            "2026-09-15T15:04:05.123456-00:30",
            &time::format_description::well_known::Rfc3339,
        )
        .unwrap();
        assert_eq!(
            format_rfc3339_nano(dt_minus),
            "2026-09-15T15:04:05.123456-00:30"
        );
        assert_eq!(
            format_rfc3339_nano_utc(dt_minus),
            "2026-09-15T15:34:05.123456Z"
        );
    }

    #[test]
    fn test_snapshot_and_restore() {
        let (root, store) = new_test_vault();
        write_test_file(&root, "notes/a.md", b"v1");

        let e1 = store.snapshot("notes/a.md").unwrap().expect("snapshot v1");
        assert_eq!(e1.size, 2);

        write_test_file(&root, "notes/a.md", b"v2");
        let e2 = store.snapshot("notes/a.md").unwrap().expect("snapshot v2");
        assert_eq!(e2.size, 2);
        assert_ne!(e1.id, e2.id);

        let list = store.list("notes/a.md").unwrap().expect("list found");
        assert_eq!(list.len(), 2);
        assert_eq!(list[0].id, e2.id);
        assert_eq!(list[1].id, e1.id);

        // Restore oldest (v1) by full ID.
        let restored = store.restore("notes/a.md", Some(&e1.id)).unwrap();
        assert_eq!(restored.id, e1.id);
        assert_eq!(read_test_file(&root, "notes/a.md"), b"v1");

        // Pre-restore state (v2) was snapshotted before restore.
        let list_after = store.list("notes/a.md").unwrap().expect("list after");
        assert_eq!(list_after.len(), 2);

        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_snapshot_dedup_and_missing_file() {
        let (root, store) = new_test_vault();
        write_test_file(&root, "a.md", b"same content");

        let e1 = store.snapshot("a.md").unwrap().expect("first snapshot");
        let e2 = store.snapshot("a.md").unwrap().expect("second snapshot");
        assert_eq!(e1.id, e2.id);
        assert_eq!(e1.timestamp, e2.timestamp);

        let list = store.list("a.md").unwrap().expect("list found");
        assert_eq!(list.len(), 1);

        let missing = store.snapshot("missing.md").unwrap();
        assert!(missing.is_none());

        let missing_list = store.list("missing.md").unwrap();
        assert!(missing_list.is_none());

        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_restore_by_prefix_and_latest() {
        let (root, store) = new_test_vault();
        write_test_file(&root, "a.md", b"v1");
        let e1 = store.snapshot("a.md").unwrap().unwrap();

        write_test_file(&root, "a.md", b"v2");
        let e2 = store.snapshot("a.md").unwrap().unwrap();

        write_test_file(&root, "a.md", b"working copy");

        // Restore latest (empty or None) restores v2 (and snapshots working copy).
        let restored_latest = store.restore("a.md", None).unwrap();
        assert_eq!(restored_latest.id, e2.id);
        assert_eq!(read_test_file(&root, "a.md"), b"v2");

        // Restore by prefix (8 chars) of v1.
        let prefix = &e1.id[0..8];
        let restored_prefix = store.restore("a.md", Some(prefix)).unwrap();
        assert_eq!(restored_prefix.id, e1.id);
        assert_eq!(read_test_file(&root, "a.md"), b"v1");

        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_path_traversal_rejected() {
        let (root, store) = new_test_vault();
        for bad in ["../evil.md", "/abs/evil.md", "..", ".", ""] {
            assert!(store.snapshot(bad).is_err());
            assert!(store.list(bad).is_err());
            assert!(store.restore(bad, None).is_err());
        }
        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    #[cfg(unix)]
    fn test_snapshot_cannot_escape_vault_via_symlink() {
        let (root, store) = new_test_vault();
        let rand_bytes = random_12_bytes().expect("random bytes");
        let hex_suffix: String = rand_bytes.iter().map(|b| format!("{b:02x}")).collect();
        let outside = std::env::temp_dir().join(format!("symdesk-outside-sentinel-{hex_suffix}"));
        let _ = fs::remove_dir_all(&outside);
        fs::create_dir_all(&outside).expect("create outside sentinel dir");
        fs::create_dir_all(root.join(".symdesk/history")).expect("create history dir");

        let symlink_path = root.join(".symdesk/history/objects");
        std::os::unix::fs::symlink(&outside, &symlink_path).expect("create symlink to outside");

        write_test_file(&root, "a.md", b"content");
        let result = store.snapshot("a.md");
        assert!(
            result.is_err(),
            "Snapshot must reject symlink escaping vault root"
        );

        let entries = fs::read_dir(&outside).expect("read outside");
        assert_eq!(
            entries.count(),
            0,
            "No object blob may be written outside the vault"
        );

        let _ = fs::remove_dir_all(&root);
        let _ = fs::remove_dir_all(&outside);
    }

    #[test]
    fn test_missing_vault_root_lazy_error() {
        let missing_path = std::env::temp_dir().join("symdesk-missing-root-nowhere-12345");
        let store = HistoryStore::new(&missing_path);
        let result = store.snapshot("a.md");
        assert!(result.is_err());
    }

    #[test]
    fn test_opaque_binary_roundtrip() {
        let (root, store) = new_test_vault();
        let binary_data = vec![0x00, 0xFF, 0xFE, 0x01, 0x80, 0xAA, 0x55, 0x00];
        write_test_file(&root, "binary.dat", &binary_data);

        let entry = store.snapshot("binary.dat").unwrap().unwrap();
        assert_eq!(entry.size, binary_data.len() as i64);

        let blob = store.content(&entry.id).unwrap();
        assert_eq!(blob, binary_data);

        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_corrupt_manifest() {
        let (root, store) = new_test_vault();
        write_test_file(
            &root,
            ".symdesk/history/manifest/doc.md.json",
            b"corrupt json {",
        );

        let err = store.list("doc.md").unwrap_err();
        assert!(matches!(err, HistoryError::CorruptManifest(..)));

        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_null_and_empty_manifest() {
        let (root, store) = new_test_vault();
        write_test_file(&root, ".symdesk/history/manifest/null_doc.md.json", b"null");
        let null_res = store.list("null_doc.md").unwrap();
        assert!(null_res.is_none());

        write_test_file(&root, ".symdesk/history/manifest/empty_doc.md.json", b"[]");
        let empty_res = store.list("empty_doc.md").unwrap();
        assert_eq!(empty_res, Some(vec![]));

        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_invalid_and_missing_object_ids() {
        let (root, store) = new_test_vault();
        assert!(matches!(
            store.content("short_id").unwrap_err(),
            HistoryError::InvalidId(..)
        ));
        assert!(matches!(
            store
                .content("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
                .unwrap_err(),
            HistoryError::NotFound(..)
        ));
        let _ = fs::remove_dir_all(&root);
    }

    #[test]
    fn test_go_zero_time_and_defaults() {
        let zero = go_zero_time();
        assert_eq!(format_rfc3339_nano_utc(zero), "0001-01-01T00:00:00Z");

        let default_entry = HistoryEntry::default();
        assert_eq!(default_entry.id, "");
        assert_eq!(default_entry.size, 0);
        assert_eq!(default_entry.timestamp, zero);
    }

    #[test]
    fn test_history_entry_deserialize_omitted_and_nulls() {
        // 1. Omitted fields in object
        let entry: HistoryEntry = serde_json::from_str("{}").unwrap();
        assert_eq!(entry, HistoryEntry::default());

        // 2. Explicit null fields in object
        let entry_nulls: HistoryEntry =
            serde_json::from_str(r#"{"id": null, "timestamp": null, "size": null}"#).unwrap();
        assert_eq!(entry_nulls, HistoryEntry::default());

        // 3. Null object itself
        let entry_direct_null: HistoryEntry = serde_json::from_str("null").unwrap();
        assert_eq!(entry_direct_null, HistoryEntry::default());

        // 4. Array containing null element and empty object
        let entries: Vec<HistoryEntry> = serde_json::from_str("[null, {}]").unwrap();
        assert_eq!(entries.len(), 2);
        assert_eq!(entries[0], HistoryEntry::default());
        assert_eq!(entries[1], HistoryEntry::default());
    }

    #[test]
    fn test_history_entry_deserialize_duplicate_fields_later_null() {
        let json = r#"{
            "id": "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
            "id": null,
            "timestamp": "2026-09-15T04:00:00Z",
            "timestamp": null,
            "size": 128,
            "size": null
        }"#;
        let entry: HistoryEntry = serde_json::from_str(json).unwrap();
        assert_eq!(
            entry.id,
            "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"
        );
        assert_eq!(
            format_rfc3339_nano_utc(entry.timestamp),
            "2026-09-15T04:00:00Z"
        );
        assert_eq!(entry.size, 128);
    }

    #[test]
    fn test_history_entry_deserialize_case_folding_and_unicode_long_s() {
        let json = r#"{
            "ID": "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
            "TimeStamp": "2026-09-15T04:00:00Z",
            "ſize": 128
        }"#;
        let entry: HistoryEntry = serde_json::from_str(json).unwrap();
        assert_eq!(
            entry.id,
            "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"
        );
        assert_eq!(
            format_rfc3339_nano_utc(entry.timestamp),
            "2026-09-15T04:00:00Z"
        );
        assert_eq!(entry.size, 128);

        let json_long_s_ts = r#"{"timeſtamp": "2026-09-15T04:00:00.123Z", "SIZE": 42}"#;
        let entry_ts: HistoryEntry = serde_json::from_str(json_long_s_ts).unwrap();
        assert_eq!(
            format_rfc3339_nano_utc(entry_ts.timestamp),
            "2026-09-15T04:00:00.123Z"
        );
        assert_eq!(entry_ts.size, 42);
    }

    #[test]
    fn test_history_entry_deserialize_unknown_fields_and_invalid_types() {
        // Unknown fields ignored
        let json_unknown = r#"{
            "unknown_field": "some_value",
            "nested": {"a": [1, 2, 3]},
            "id": "my_id"
        }"#;
        let entry: HistoryEntry = serde_json::from_str(json_unknown).unwrap();
        assert_eq!(entry.id, "my_id");

        // Invalid types rejected
        assert!(serde_json::from_str::<HistoryEntry>(r#"{"id": 123}"#).is_err());
        assert!(serde_json::from_str::<HistoryEntry>(r#"{"id": true}"#).is_err());
        assert!(serde_json::from_str::<HistoryEntry>(r#"{"id": []}"#).is_err());
        assert!(serde_json::from_str::<HistoryEntry>(r#"{"timestamp": 123}"#).is_err());
        assert!(serde_json::from_str::<HistoryEntry>(r#"{"timestamp": "not-a-date"}"#).is_err());
        assert!(serde_json::from_str::<HistoryEntry>(r#"{"size": "not-a-number"}"#).is_err());
        assert!(serde_json::from_str::<HistoryEntry>(r#"{"size": 12.34}"#).is_err());
        assert!(serde_json::from_str::<HistoryEntry>("123").is_err());
        assert!(serde_json::from_str::<HistoryEntry>("\"string\"").is_err());
    }

    #[test]
    fn test_history_entry_serialization_roundtrip() {
        let entry = HistoryEntry {
            id: "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae".to_string(),
            timestamp: OffsetDateTime::parse(
                "2026-09-15T04:00:00Z",
                &time::format_description::well_known::Rfc3339,
            )
            .unwrap(),
            size: 128,
        };
        let json_str = serde_json::to_string(&entry).unwrap();
        assert!(json_str.contains(
            r#""id":"2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae""#
        ));
        assert!(json_str.contains(r#""timestamp":"2026-09-15T04:00:00Z""#));
        assert!(json_str.contains(r#""size":128"#));

        let deserialized: HistoryEntry = serde_json::from_str(&json_str).unwrap();
        assert_eq!(deserialized, entry);

        // Positive fractional offset +05:30 serialization roundtrip
        let entry_plus = HistoryEntry {
            id: "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae".to_string(),
            timestamp: OffsetDateTime::parse(
                "2026-09-15T15:04:05.123456+05:30",
                &time::format_description::well_known::Rfc3339,
            )
            .unwrap(),
            size: 128,
        };
        let json_plus = serde_json::to_string(&entry_plus).unwrap();
        assert!(json_plus.contains(r#""timestamp":"2026-09-15T15:04:05.123456+05:30""#));
        let de_plus: HistoryEntry = serde_json::from_str(&json_plus).unwrap();
        assert_eq!(de_plus, entry_plus);

        // Negative fractional offset -00:30 serialization roundtrip
        let entry_minus = HistoryEntry {
            id: "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae".to_string(),
            timestamp: OffsetDateTime::parse(
                "2026-09-15T15:04:05.123456-00:30",
                &time::format_description::well_known::Rfc3339,
            )
            .unwrap(),
            size: 128,
        };
        let json_minus = serde_json::to_string(&entry_minus).unwrap();
        assert!(json_minus.contains(r#""timestamp":"2026-09-15T15:04:05.123456-00:30""#));
        let de_minus: HistoryEntry = serde_json::from_str(&json_minus).unwrap();
        assert_eq!(de_minus, entry_minus);
    }

    #[test]
    fn test_manifest_rewrite_preserves_stored_offset() {
        let (root, _) = new_test_vault();
        let store = HistoryStore::with_clock(&root, || {
            OffsetDateTime::parse(
                "2026-09-15T16:00:00Z",
                &time::format_description::well_known::Rfc3339,
            )
            .unwrap()
        });
        write_test_file(&root, "doc.md", b"v2 content");
        let manifest_content = r#"[
  {
    "id": "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
    "timestamp": "2026-09-15T15:04:05.123456+05:30",
    "size": 10
  }
]"#;
        write_test_file(
            &root,
            ".symdesk/history/manifest/doc.md.json",
            manifest_content.as_bytes(),
        );
        write_test_file(
            &root,
            ".symdesk/history/objects/2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae",
            b"v1 content",
        );

        let snapshot_res = store.snapshot("doc.md").unwrap().expect("new snapshot");
        assert_ne!(
            snapshot_res.id,
            "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"
        );

        let entries = store.list("doc.md").unwrap().expect("entries");
        assert_eq!(entries.len(), 2);
        assert_eq!(
            format_rfc3339_nano(entries[1].timestamp),
            "2026-09-15T15:04:05.123456+05:30"
        );

        let manifest_bytes = fs::read(root.join(".symdesk/history/manifest/doc.md.json")).unwrap();
        let manifest_str = String::from_utf8(manifest_bytes).unwrap();
        assert!(manifest_str.contains("2026-09-15T15:04:05.123456+05:30"));

        let _ = fs::remove_dir_all(&root);
    }
}
