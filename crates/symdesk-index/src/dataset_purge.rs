#![deny(unsafe_code)]

//! Acceptance-bound, resumable removal of one retention-managed dataset.

use std::{
    io::{self, Read, Write},
    path::Path,
    sync::atomic::{AtomicU64, Ordering},
};

use cap_std::{
    ambient_authority,
    fs::{Dir, OpenOptions},
};
use serde::{Deserialize, Serialize};
use symdesk_vault::{HistoryError, HistoryStore, TRASH_META_SUFFIX, parse_dataset_handle};
use thiserror::Error;

use crate::{Sidecar, SidecarError};

const JOURNAL_DIR: &str = ".symdesk/dataset-purge";
const TRASH_DIR: &str = ".symdesk/trash";
static TEMP_COUNTER: AtomicU64 = AtomicU64::new(0);

#[derive(Debug, Error)]
pub enum DatasetPurgeError {
    #[error("{0}")]
    Contract(String),
    #[error(transparent)]
    Sidecar(#[from] SidecarError),
    #[error(transparent)]
    Io(#[from] io::Error),
    #[error(transparent)]
    History(#[from] HistoryError),
    #[error(transparent)]
    Retention(#[from] symdesk_vault::retention_state::RetentionStateError),
}

#[derive(Clone, Debug, Serialize, Deserialize)]
struct PathRecord {
    path: String,
    kind: String,
    identity: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    content_hash: String,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
struct TrashRecord {
    name: String,
    original_path: String,
    payload_identity: String,
    payload_hash: String,
    metadata_identity: String,
    metadata_hash: String,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
struct Journal {
    version: u32,
    slug: String,
    accepted_rule: String,
    fingerprint: String,
    paths: Vec<PathRecord>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    trash: Vec<TrashRecord>,
    phase: String,
    status: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    last_error: String,
}

/// Executes Go-compatible `Service.DatasetPurgeWithFingerprint` semantics.
pub struct DatasetPurgeService<'a> {
    vault_root: &'a Path,
    sidecar: &'a mut Sidecar,
}

impl<'a> DatasetPurgeService<'a> {
    #[must_use]
    pub fn new(vault_root: &'a Path, sidecar: &'a mut Sidecar) -> Self {
        Self {
            vault_root,
            sidecar,
        }
    }

    /// Removes the dataset handle/raw directory after validating the accepted rule and fingerprint.
    /// Progress is journaled before sidecar or vault mutation and can be resumed by repeating the call.
    pub fn purge(
        &mut self,
        slug: &str,
        accepted_rule: &str,
        fingerprint: &str,
    ) -> Result<(), DatasetPurgeError> {
        let slug = slug.trim();
        let rule = accepted_rule.trim();
        if slug.is_empty() || slugify(slug) != slug || rule.is_empty() {
            return Err(DatasetPurgeError::Contract(
                "dataset purge requires a filesystem-safe slug and accepted retention rule".into(),
            ));
        }
        let root = Dir::open_ambient_dir(self.vault_root, ambient_authority())?;
        let journal_name = format!("{JOURNAL_DIR}/{slug}.json");
        let mut journal = match read_optional(&root, &journal_name)? {
            Some(bytes) => {
                let journal: Journal = serde_json::from_slice(&bytes).map_err(|e| {
                    DatasetPurgeError::Contract(format!("corrupt dataset purge journal: {e}"))
                })?;
                validate_journal(&journal, slug)?;
                if journal.accepted_rule != rule
                    || (!fingerprint.is_empty() && journal.fingerprint != fingerprint)
                {
                    return Err(DatasetPurgeError::Contract("dataset purge journal does not match requested dataset, rule, or fingerprint".into()));
                }
                if journal.status == "completed" || journal.phase == "complete" {
                    remove_file_idempotent(&root, &journal_name)?;
                    return Ok(());
                }
                journal
            }
            None => {
                let plan = preflight(self.vault_root, &root, slug, rule, fingerprint)?;
                write_journal(&root, &plan)?;
                plan
            }
        };
        self.resume(&root, &mut journal)
    }

    fn resume(&mut self, root: &Dir, journal: &mut Journal) -> Result<(), DatasetPurgeError> {
        let result = (|| {
            if journal.phase == "sidecar" {
                self.sidecar.delete_dataset(&journal.slug)?;
                self.sidecar
                    .delete_document(&format!("datasets/{}.md", journal.slug))?;
                journal.phase = "active".into();
                journal.status = "in_progress".into();
                journal.last_error.clear();
                write_journal(root, journal)?;
            }
            if journal.phase == "active" {
                for record in &journal.paths {
                    remove_recorded(root, record)?;
                }
                journal.phase = "recovery".into();
                journal.status = "in_progress".into();
                journal.last_error.clear();
                write_journal(root, journal)?;
            }
            if journal.phase == "recovery" {
                let store = HistoryStore::new(self.vault_root);
                let paths: Vec<String> = journal
                    .paths
                    .iter()
                    .map(|p| p.path.clone())
                    .chain(journal.trash.iter().map(|t| t.original_path.clone()))
                    .collect();
                validate_trash(root, &journal.trash)?;
                store.purge_paths(&paths)?;
                let current_trash = store.trash_list_strict()?;
                let mut wanted = Vec::with_capacity(journal.trash.len());
                for record in &journal.trash {
                    if let Some(entry) =
                        current_trash.iter().find(|entry| entry.name == record.name)
                    {
                        if entry.original_path != record.original_path {
                            return Err(DatasetPurgeError::Contract(format!(
                                "trash entry {:?} original path changed",
                                record.name
                            )));
                        }
                        wanted.push(entry.clone());
                    }
                }
                store.purge_trash_entries(&wanted)?;
                journal.phase = "complete".into();
                journal.status = "completed".into();
                journal.last_error.clear();
                write_journal(root, journal)?;
            }
            if journal.phase != "complete" {
                return Err(DatasetPurgeError::Contract(format!(
                    "invalid dataset purge phase {:?}",
                    journal.phase
                )));
            }
            remove_file_idempotent(root, &format!("{JOURNAL_DIR}/{}.json", journal.slug))?;
            Ok(())
        })();
        if let Err(error) = result {
            journal.status = "failed".into();
            journal.last_error = error.to_string();
            if let Err(persist) = write_journal(root, journal) {
                return Err(DatasetPurgeError::Contract(format!(
                    "{error} (also failed to persist purge journal: {persist})"
                )));
            }
            return Err(error);
        }
        result
    }
}

fn preflight(
    vault: &Path,
    root: &Dir,
    slug: &str,
    rule: &str,
    accepted_fp: &str,
) -> Result<Journal, DatasetPurgeError> {
    let handle_path = format!("datasets/{slug}.md");
    let handle = read_regular(root, &handle_path)?;
    let parsed = parse_dataset_handle(&handle_path, &handle)
        .map_err(|e| DatasetPurgeError::Contract(e.to_string()))?;
    if parsed.slug != slug || parsed.retention_rule != rule {
        return Err(DatasetPurgeError::Contract(format!(
            "dataset handle {handle_path} does not match accepted slug/rule"
        )));
    }
    let state = symdesk_vault::retention_state::retention_state(vault, &handle_path)?;
    if !accepted_fp.is_empty() && state.fingerprint != accepted_fp {
        return Err(DatasetPurgeError::Contract(
            "dataset purge proposal is stale: authoritative fingerprint changed".into(),
        ));
    }
    let mut paths = vec![file_record(root, &handle_path, &handle)?];
    let raw_dir = format!("datasets/{slug}");
    match root.symlink_metadata(Path::new(&raw_dir)) {
        Ok(meta) if meta.is_dir() && !meta.file_type().is_symlink() => {
            for entry in root.read_dir(Path::new(&raw_dir))? {
                let entry = entry?;
                let name = entry.file_name().into_string().map_err(|_| {
                    DatasetPurgeError::Contract(
                        "dataset raw path contains a non-UTF-8 entry".into(),
                    )
                })?;
                let rel = format!("{raw_dir}/{name}");
                let meta = root.symlink_metadata(Path::new(&rel))?;
                if !meta.is_file() || meta.file_type().is_symlink() {
                    return Err(DatasetPurgeError::Contract(format!(
                        "dataset raw directory contains non-regular entry {name:?}"
                    )));
                }
                let data = read_regular(root, &rel)?;
                paths.push(file_record(root, &rel, &data)?);
            }
            paths.push(PathRecord {
                path: raw_dir.clone(),
                kind: "dir".into(),
                identity: identity(&root.symlink_metadata(Path::new(&raw_dir))?),
                content_hash: String::new(),
            });
        }
        Ok(_) => {
            return Err(DatasetPurgeError::Contract(format!(
                "dataset raw path {raw_dir} must be a directory, not a symlink"
            )));
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(error.into()),
    }
    paths.sort_by(|a, b| {
        if a.kind == "dir" {
            std::cmp::Ordering::Greater
        } else if b.kind == "dir" {
            std::cmp::Ordering::Less
        } else {
            a.path.cmp(&b.path)
        }
    });

    let history = HistoryStore::new(vault);
    let entries = history.trash_list_strict()?;
    let prefix = format!("{raw_dir}/");
    let mut trash = Vec::new();
    for entry in entries {
        if entry.original_path != handle_path && !entry.original_path.starts_with(&prefix) {
            continue;
        }
        let payload = format!("{TRASH_DIR}/{}", entry.name);
        let metadata = format!("{TRASH_DIR}/{}{}", entry.name, TRASH_META_SUFFIX);
        let payload_bytes = read_regular(root, &payload)?;
        let metadata_bytes = read_regular(root, &metadata)?;
        trash.push(TrashRecord {
            name: entry.name,
            original_path: entry.original_path,
            payload_identity: identity(&root.symlink_metadata(Path::new(&payload))?),
            payload_hash: hash(&payload_bytes),
            metadata_identity: identity(&root.symlink_metadata(Path::new(&metadata))?),
            metadata_hash: hash(&metadata_bytes),
        });
    }
    let all: Vec<String> = paths
        .iter()
        .map(|p| p.path.clone())
        .chain(trash.iter().map(|t| t.original_path.clone()))
        .collect();
    history.preflight_purge_paths(&all)?;
    Ok(Journal {
        version: 1,
        slug: slug.into(),
        accepted_rule: rule.into(),
        fingerprint: state.fingerprint,
        paths,
        trash,
        phase: "sidecar".into(),
        status: "in_progress".into(),
        last_error: String::new(),
    })
}

fn validate_journal(j: &Journal, slug: &str) -> Result<(), DatasetPurgeError> {
    if j.version != 1
        || j.slug != slug
        || j.accepted_rule.is_empty()
        || !valid_hash(&j.fingerprint)
        || j.paths.is_empty()
        || !["sidecar", "active", "recovery", "complete"].contains(&j.phase.as_str())
        || !["in_progress", "failed", "completed"].contains(&j.status.as_str())
    {
        return Err(DatasetPurgeError::Contract(
            "invalid dataset purge journal".into(),
        ));
    }
    let handle = format!("datasets/{slug}.md");
    let raw = format!("datasets/{slug}");
    let prefix = format!("{raw}/");
    let mut seen = std::collections::BTreeSet::new();
    let mut found_handle = false;
    for p in &j.paths {
        if p.identity.is_empty() || !seen.insert(&p.path) || !clean_dataset_path(&p.path) {
            return Err(DatasetPurgeError::Contract(format!(
                "invalid dataset purge path {:?}",
                p.path
            )));
        }
        if p.path == handle {
            found_handle = p.kind == "file" && valid_hash(&p.content_hash);
        } else if p.path == raw {
            if p.kind != "dir" || !p.content_hash.is_empty() {
                return Err(DatasetPurgeError::Contract(
                    "invalid dataset raw directory record".into(),
                ));
            }
        } else if p.path.starts_with(&prefix) {
            let name = &p.path[prefix.len()..];
            if p.kind != "file"
                || name.is_empty()
                || name.contains('/')
                || !valid_hash(&p.content_hash)
            {
                return Err(DatasetPurgeError::Contract(format!(
                    "invalid dataset raw path {:?}",
                    p.path
                )));
            }
        } else {
            return Err(DatasetPurgeError::Contract(format!(
                "dataset purge path {:?} is outside dataset",
                p.path
            )));
        }
    }
    if !found_handle {
        return Err(DatasetPurgeError::Contract(
            "dataset purge journal does not record its handle".into(),
        ));
    }
    for t in &j.trash {
        let in_dataset = t.original_path == handle
            || (t.original_path.starts_with(&prefix)
                && clean_dataset_path(&t.original_path)
                && !t.original_path[prefix.len()..].contains('/'));
        if t.name.is_empty()
            || t.name.contains(['/', '\\'])
            || t.original_path.contains('\\')
            || !in_dataset
            || !valid_hash(&t.payload_hash)
            || !valid_hash(&t.metadata_hash)
            || t.payload_identity.is_empty()
            || t.metadata_identity.is_empty()
        {
            return Err(DatasetPurgeError::Contract(
                "invalid dataset purge trash record".into(),
            ));
        }
    }
    Ok(())
}

fn remove_recorded(root: &Dir, record: &PathRecord) -> Result<(), DatasetPurgeError> {
    let path = Path::new(&record.path);
    let metadata = match root.symlink_metadata(path) {
        Ok(m) => m,
        Err(e) if e.kind() == io::ErrorKind::NotFound => return Ok(()),
        Err(e) => return Err(e.into()),
    };
    let valid_type = !metadata.file_type().is_symlink()
        && match record.kind.as_str() {
            "file" => metadata.is_file(),
            "dir" => metadata.is_dir(),
            _ => false,
        };
    if !valid_type || identity(&metadata) != record.identity {
        return Err(DatasetPurgeError::Contract(format!(
            "purge path {} was replaced or changed type",
            record.path
        )));
    }
    if record.kind == "file" && hash(&read_regular(root, &record.path)?) != record.content_hash {
        return Err(DatasetPurgeError::Contract(format!(
            "purge path {} content changed",
            record.path
        )));
    }
    if record.kind == "dir" {
        root.remove_dir(path)?;
    } else {
        root.remove_file(path)?;
    }
    Ok(())
}

fn validate_trash(root: &Dir, trash: &[TrashRecord]) -> Result<(), DatasetPurgeError> {
    for t in trash {
        for (path, expected_id, expected_hash) in [
            (
                format!("{TRASH_DIR}/{}", t.name),
                &t.payload_identity,
                &t.payload_hash,
            ),
            (
                format!("{TRASH_DIR}/{}{}", t.name, TRASH_META_SUFFIX),
                &t.metadata_identity,
                &t.metadata_hash,
            ),
        ] {
            let metadata = match root.symlink_metadata(Path::new(&path)) {
                Ok(m) => m,
                Err(e) if e.kind() == io::ErrorKind::NotFound => continue,
                Err(e) => return Err(e.into()),
            };
            if !metadata.is_file()
                || metadata.file_type().is_symlink()
                || identity(&metadata) != *expected_id
                || hash(&read_regular(root, &path)?) != *expected_hash
            {
                return Err(DatasetPurgeError::Contract(format!(
                    "dataset trash {} changed",
                    t.name
                )));
            }
        }
    }
    Ok(())
}

fn read_regular(root: &Dir, path: &str) -> Result<Vec<u8>, DatasetPurgeError> {
    let metadata = root.symlink_metadata(Path::new(path))?;
    if !metadata.is_file() || metadata.file_type().is_symlink() {
        return Err(DatasetPurgeError::Contract(format!(
            "expected regular file: {path}"
        )));
    }
    let mut file = root.open(Path::new(path))?;
    let opened = file.metadata()?;
    if identity(&metadata) != identity(&opened) {
        return Err(DatasetPurgeError::Contract(format!(
            "file changed during read: {path}"
        )));
    }
    let mut data = Vec::new();
    file.read_to_end(&mut data)?;
    Ok(data)
}

fn file_record(root: &Dir, path: &str, data: &[u8]) -> Result<PathRecord, DatasetPurgeError> {
    let metadata = root.symlink_metadata(Path::new(path))?;
    Ok(PathRecord {
        path: path.into(),
        kind: "file".into(),
        identity: identity(&metadata),
        content_hash: hash(data),
    })
}

fn identity(meta: &cap_std::fs::Metadata) -> String {
    #[cfg(unix)]
    {
        use cap_std::fs::MetadataExt;
        format!("{}:{}", meta.dev(), meta.ino())
    }
    #[cfg(not(unix))]
    {
        format!(
            "{}:{}:{}",
            meta.len(),
            meta.modified()
                .ok()
                .and_then(|v| v.duration_since(std::time::UNIX_EPOCH).ok())
                .map_or(0, |v| v.as_nanos()),
            meta.is_dir()
        )
    }
}

fn hash(data: &[u8]) -> String {
    symdesk_vault::sha256_hex(data)
}
fn valid_hash(value: &str) -> bool {
    value.len() == 64 && value.bytes().all(|b| b.is_ascii_hexdigit())
}
fn clean_dataset_path(value: &str) -> bool {
    !value.is_empty()
        && !value.starts_with('/')
        && !value.contains('\\')
        && value
            .split('/')
            .all(|part| !part.is_empty() && part != "." && part != "..")
}
fn slugify(value: &str) -> String {
    let mut result = String::new();
    let mut separator = false;
    for character in symdesk_vault::go_lowercase(value.trim()).chars() {
        if character.is_ascii_lowercase() || character.is_ascii_digit() {
            if separator && !result.is_empty() {
                result.push('-');
            }
            result.push(character);
            separator = false;
        } else if !result.is_empty() {
            separator = true;
        }
    }
    if result.is_empty() {
        "base".into()
    } else {
        result
    }
}

fn read_optional(root: &Dir, path: &str) -> Result<Option<Vec<u8>>, DatasetPurgeError> {
    match root.open(Path::new(path)) {
        Ok(mut f) => {
            let mut bytes = Vec::new();
            f.read_to_end(&mut bytes)?;
            Ok(Some(bytes))
        }
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(None),
        Err(e) => Err(e.into()),
    }
}

fn write_journal(root: &Dir, journal: &Journal) -> Result<(), DatasetPurgeError> {
    root.create_dir_all(Path::new(JOURNAL_DIR))?;
    let bytes = serde_json::to_vec_pretty(journal)
        .map_err(|e| DatasetPurgeError::Contract(e.to_string()))?;
    for _ in 0..100 {
        let counter = TEMP_COUNTER.fetch_add(1, Ordering::Relaxed);
        let temp = format!(
            "{JOURNAL_DIR}/.journal-{}-{counter}.tmp",
            std::process::id()
        );
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use cap_std::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        let mut file = match root.open_with(&temp, &options) {
            Ok(f) => f,
            Err(e) if e.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(e) => return Err(e.into()),
        };
        let write_result = (|| -> Result<(), io::Error> {
            file.write_all(&bytes)?;
            file.sync_all()?;
            drop(file);
            root.rename(
                Path::new(&temp),
                root,
                Path::new(&format!("{JOURNAL_DIR}/{}.json", journal.slug)),
            )?;
            Ok(())
        })();
        if let Err(error) = write_result {
            let _ = root.remove_file(Path::new(&temp));
            return Err(error.into());
        }
        return Ok(());
    }
    Err(DatasetPurgeError::Io(io::Error::new(
        io::ErrorKind::AlreadyExists,
        "journal temporary name space exhausted",
    )))
}

fn remove_file_idempotent(root: &Dir, path: &str) -> Result<(), DatasetPurgeError> {
    match root.remove_file(Path::new(path)) {
        Ok(()) => Ok(()),
        Err(e) if e.kind() == io::ErrorKind::NotFound => Ok(()),
        Err(e) => Err(e.into()),
    }
}

#[cfg(test)]
mod tests {
    use std::{
        collections::BTreeMap,
        fs,
        path::PathBuf,
        sync::atomic::{AtomicU64, Ordering},
    };

    use cap_std::{ambient_authority, fs::Dir};
    use serde_json::json;
    use symdesk_vault::Provenance;

    use crate::{DatasetSyncOptions, DatasetSyncRow, DatasetSyncService, Sidecar};

    use super::{DatasetPurgeService, preflight, write_journal};

    static COUNTER: AtomicU64 = AtomicU64::new(0);

    #[test]
    fn replacement_after_journal_is_never_removed_on_retry() {
        let mut sandbox = Sandbox::new();
        sandbox.setup();
        let handle = "datasets/orders.md";
        let state = symdesk_vault::retention_state::retention_state(&sandbox.root, handle)
            .expect("retention state");
        let root = Dir::open_ambient_dir(&sandbox.root, ambient_authority()).expect("root cap");
        let journal = preflight(
            &sandbox.root,
            &root,
            "orders",
            "default",
            &state.fingerprint,
        )
        .expect("preflight plan");
        write_journal(&root, &journal).expect("persist plan");
        sandbox.db.close().expect("close sidecar");
        assert!(
            DatasetPurgeService::new(&sandbox.root, &mut sandbox.db)
                .purge("orders", "default", &state.fingerprint)
                .is_err()
        );

        let raw = sandbox.root.join("datasets/orders/2026-01-04.csv");
        fs::remove_file(&raw).expect("remove original raw file");
        fs::write(&raw, b"replacement").expect("write replacement raw file");
        sandbox.db = Sidecar::open(&sandbox.parent.join("sidecar.db")).expect("reopen sidecar");
        let error = DatasetPurgeService::new(&sandbox.root, &mut sandbox.db)
            .purge("orders", "default", &state.fingerprint)
            .expect_err("replacement must fail closed");
        assert!(
            error.to_string().contains("replaced") || error.to_string().contains("content changed")
        );
        assert_eq!(
            fs::read(raw).expect("read retained replacement"),
            b"replacement"
        );
        assert!(!sandbox.root.join("datasets/orders.md").exists());
        assert!(
            sandbox
                .root
                .join(".symdesk/dataset-purge/orders.json")
                .exists()
        );
    }

    #[test]
    fn replaced_trash_payload_survives_purge_retry() {
        let mut sandbox = Sandbox::new();
        sandbox.setup();
        let raw = sandbox.root.join("datasets/orders/2026-01-04.csv");
        let raw_bytes = fs::read(&raw).expect("read original raw source");
        let history = symdesk_vault::HistoryStore::new(&sandbox.root);
        let entry = history
            .trash("datasets/orders/2026-01-04.csv")
            .expect("trash raw source");
        fs::write(&raw, &raw_bytes).expect("restore active source copy");
        let state =
            symdesk_vault::retention_state::retention_state(&sandbox.root, "datasets/orders.md")
                .expect("retention state");
        let root = Dir::open_ambient_dir(&sandbox.root, ambient_authority()).expect("root cap");
        let journal = preflight(
            &sandbox.root,
            &root,
            "orders",
            "default",
            &state.fingerprint,
        )
        .expect("preflight plan");
        write_journal(&root, &journal).expect("persist plan");
        sandbox.db.close().expect("close sidecar");
        assert!(
            DatasetPurgeService::new(&sandbox.root, &mut sandbox.db)
                .purge("orders", "default", &state.fingerprint)
                .is_err()
        );

        let payload = sandbox.root.join(".symdesk/trash").join(&entry.name);
        fs::write(&payload, b"replacement trash bytes").expect("replace trash payload bytes");
        sandbox.db = Sidecar::open(&sandbox.parent.join("sidecar.db")).expect("reopen sidecar");
        assert!(
            DatasetPurgeService::new(&sandbox.root, &mut sandbox.db)
                .purge("orders", "default", &state.fingerprint)
                .is_err()
        );
        assert_eq!(
            fs::read(payload).expect("read retained replacement trash"),
            b"replacement trash bytes"
        );
        assert!(
            sandbox
                .root
                .join(".symdesk/dataset-purge/orders.json")
                .exists()
        );
    }

    struct Sandbox {
        parent: PathBuf,
        root: PathBuf,
        db: Sidecar,
    }

    impl Sandbox {
        fn new() -> Self {
            let n = COUNTER.fetch_add(1, Ordering::Relaxed);
            let parent = std::env::temp_dir()
                .join(format!("symdesk-purge-retry-{}-{n}", std::process::id()));
            let root = parent.join("vault");
            fs::create_dir_all(&root).expect("create vault");
            let db = Sidecar::open(&parent.join("sidecar.db")).expect("open sidecar");
            Self { parent, root, db }
        }

        fn setup(&mut self) {
            let options = DatasetSyncOptions {
                title: "Orders".into(),
                slug: "orders".into(),
                identity_field: "id".into(),
                provenance: Provenance {
                    imported_at: "2026-01-04T00:00:00Z".into(),
                    source_name: "feed".into(),
                    source_sha256: "policy-sha".into(),
                },
                sensitivity: "restricted".into(),
                retention_rule: "default".into(),
                rows: vec![DatasetSyncRow {
                    identity: "o1".into(),
                    values: BTreeMap::from([
                        ("amount".into(), json!(12.5)),
                        ("id".into(), json!("o1")),
                    ]),
                }],
                ..DatasetSyncOptions::default()
            };
            DatasetSyncService::new(&self.root, &mut self.db)
                .sync(options)
                .expect("seed dataset");
        }
    }

    impl Drop for Sandbox {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.parent);
        }
    }
}
