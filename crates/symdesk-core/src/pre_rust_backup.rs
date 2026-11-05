//! Automatic one-time backup of existing (Go-created) state before the first
//! Rust run, and the verified restore of it (RUST-018).
//!
//! The stable release ships Rust only, so there is no Go binary to fall back
//! to. Data safety is therefore a gate: before a Rust process touches state that
//! earlier Go builds created, it copies that state into
//! `<data>/backups/pre-rust/` (temporary directory, then an atomic rename;
//! `manifest.json` is written last and is the completion marker) and records a
//! marker so later runs do nothing. The backup never writes to stdout, which
//! stays reserved for MCP JSON-RPC frames; the caller prints one stderr notice.
//!
//! Failure policy: when state exists and the backup cannot be completed the
//! caller must abort the command and leave state untouched. The documented
//! escape hatch is `SYMDESK_PRE_RUST_BACKUP=off`.
//!
//! SQLite files are copied byte for byte when quiescent (no live WAL or
//! rollback journal and unchanged while copied), which keeps the restore
//! byte-identical to the original. A database with a live WAL/journal, or one
//! that changes during the copy, is snapshotted with SQLite's own `VACUUM INTO`
//! (WAL-consistent) and its `-wal`/`-journal` sidecars are folded into that
//! snapshot instead of being copied.

use std::{
    collections::BTreeSet,
    fs::{self, File},
    io::{self, Read, Write},
    path::{Component, Path, PathBuf},
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use rusqlite::{Connection, OpenFlags};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};

/// Environment switch: `off`, `0`, `false` or `no` disables the automatic
/// backup (tests and operators who manage their own).
pub const OPT_OUT_ENV: &str = "SYMDESK_PRE_RUST_BACKUP";
pub const SCHEMA_VERSION: u32 = 1;
const BACKUP_NAME: &str = "pre-rust";
const MARKER_NAME: &str = "pre-rust.done.json";
const LOCK_NAME: &str = ".pre-rust.lock";
const SQLITE_MAGIC: &[u8; 16] = b"SQLite format 3\0";
/// Top-level directories of a state root that are never backed up: the ingest
/// archive can be large and is reproducible from the vault, and `backups/` is
/// where the backup itself lives.
const SKIPPED_TOP_LEVEL: [&str; 2] = ["backups", "archive"];

/// What to back up for one tool.
#[derive(Clone, Debug)]
pub struct Spec {
    /// `symdesk` or `symroom`; recorded in the manifest and notices.
    pub tool: String,
    pub version: String,
    /// Directory holding `pre-rust/` and the marker, normally `<data>/backups`.
    pub backups_dir: PathBuf,
    /// State directories (data, config, legacy fallback directories).
    pub roots: Vec<PathBuf>,
    /// Individual state files that may live outside the roots (configured
    /// database locations).
    pub extra_files: Vec<PathBuf>,
}

#[derive(Debug, Eq, PartialEq)]
pub enum Outcome {
    /// `SYMDESK_PRE_RUST_BACKUP=off`.
    Disabled,
    /// A marker or a completed backup already exists.
    AlreadyDone,
    /// No existing state: nothing to back up, and a marker now records that so
    /// state the Rust build itself creates is never mistaken for Go state.
    NoState,
    Created {
        dir: PathBuf,
        files: usize,
    },
}

#[derive(Debug, Deserialize, Serialize)]
pub struct Manifest {
    pub schema_version: u32,
    pub created_at: String,
    pub symdesk_version: String,
    pub tool: String,
    pub sources: Vec<SourceRoot>,
    pub dirs: Vec<DirEntry>,
    pub files: Vec<FileEntry>,
    #[serde(default)]
    pub skipped: Vec<Skipped>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct SourceRoot {
    pub path: String,
    pub exists: bool,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct DirEntry {
    pub source: String,
    pub mode: u32,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct FileEntry {
    pub source: String,
    /// Path of the copy, relative to `pre-rust/`.
    pub backup: String,
    pub sha256: String,
    pub size: u64,
    pub mode: u32,
    pub mtime_ns: i64,
    /// `copy` (byte-identical) or `sqlite-backup` (`VACUUM INTO`).
    pub method: String,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct Skipped {
    pub source: String,
    pub reason: String,
}

#[derive(Debug, Deserialize, Serialize)]
struct Marker {
    schema_version: u32,
    result: String,
    created_at: String,
    symdesk_version: String,
}

#[derive(Debug, Default, Eq, PartialEq)]
pub struct RestoreReport {
    pub dry_run: bool,
    pub backup_dir: PathBuf,
    pub files: usize,
    pub verified_files: usize,
    pub set_aside: Vec<String>,
    pub aside_dir: Option<PathBuf>,
}

#[must_use]
pub fn opted_out() -> bool {
    std::env::var(OPT_OUT_ENV).is_ok_and(|value| {
        matches!(
            value.trim().to_ascii_lowercase().as_str(),
            "off" | "0" | "false" | "no"
        )
    })
}

#[must_use]
pub fn backup_dir(spec: &Spec) -> PathBuf {
    spec.backups_dir.join(BACKUP_NAME)
}

fn marker_path(spec: &Spec) -> PathBuf {
    spec.backups_dir.join(MARKER_NAME)
}

fn now() -> String {
    use time::format_description::well_known::Rfc3339;
    time::OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .unwrap_or_default()
}

fn hex(bytes: &[u8]) -> String {
    use std::fmt::Write as _;
    let mut text = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        let _ = write!(text, "{byte:02x}");
    }
    text
}

fn sha256_file(path: &Path) -> io::Result<(String, u64)> {
    let mut file = File::open(path)?;
    let mut hasher = Sha256::new();
    let mut buffer = vec![0_u8; 1 << 16];
    let mut total = 0_u64;
    loop {
        let read = file.read(&mut buffer)?;
        if read == 0 {
            break;
        }
        hasher.update(&buffer[..read]);
        total += read as u64;
    }
    Ok((hex(&hasher.finalize()), total))
}

fn mtime_ns(metadata: &fs::Metadata) -> i64 {
    metadata
        .modified()
        .ok()
        .and_then(|time| match time.duration_since(UNIX_EPOCH) {
            Ok(after) => i64::try_from(after.as_nanos()).ok(),
            Err(before) => i64::try_from(before.duration().as_nanos()).ok().map(|n| -n),
        })
        .unwrap_or(0)
}

#[cfg(unix)]
fn mode_of(metadata: &fs::Metadata) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    metadata.permissions().mode() & 0o7777
}

#[cfg(not(unix))]
fn mode_of(_: &fs::Metadata) -> u32 {
    0o644
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) -> io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(mode))
}

#[cfg(not(unix))]
fn set_mode(_: &Path, _: u32) -> io::Result<()> {
    Ok(())
}

fn is_sqlite(path: &Path) -> bool {
    let mut header = [0_u8; 16];
    File::open(path)
        .and_then(|mut file| file.read_exact(&mut header))
        .is_ok()
        && &header == SQLITE_MAGIC
}

fn sidecar(path: &Path, suffix: &str) -> PathBuf {
    let mut name = path.as_os_str().to_owned();
    name.push(suffix);
    PathBuf::from(name)
}

fn non_empty(path: &Path) -> bool {
    fs::metadata(path).is_ok_and(|metadata| metadata.len() > 0)
}

/// A walked state file.
struct Found {
    path: PathBuf,
    metadata: fs::Metadata,
}

#[derive(Default)]
struct Inventory {
    dirs: Vec<(PathBuf, u32)>,
    files: Vec<Found>,
    skipped: Vec<Skipped>,
}

fn walk(directory: &Path, inventory: &mut Inventory, top_level: bool) -> io::Result<()> {
    let mut entries: Vec<_> = fs::read_dir(directory)?.collect::<Result<_, _>>()?;
    entries.sort_by_key(fs::DirEntry::file_name);
    for entry in entries {
        let path = entry.path();
        let name = entry.file_name().to_string_lossy().into_owned();
        if top_level && SKIPPED_TOP_LEVEL.contains(&name.as_str()) {
            continue;
        }
        let metadata = fs::symlink_metadata(&path)?;
        if metadata.is_dir() {
            inventory.dirs.push((path.clone(), mode_of(&metadata)));
            walk(&path, inventory, false)?;
        } else if metadata.is_file() {
            if name.ends_with("-shm") {
                inventory.skipped.push(Skipped {
                    source: path.to_string_lossy().into_owned(),
                    reason: "ephemeral SQLite shared-memory file".to_owned(),
                });
            } else {
                inventory.files.push(Found { path, metadata });
            }
        } else {
            inventory.skipped.push(Skipped {
                source: path.to_string_lossy().into_owned(),
                reason: "symbolic link or special file".to_owned(),
            });
        }
    }
    Ok(())
}

fn inventory(spec: &Spec) -> Result<(Inventory, Vec<SourceRoot>), String> {
    let mut found = Inventory::default();
    let mut sources = Vec::new();
    let mut seen = BTreeSet::new();
    for root in &spec.roots {
        if !seen.insert(root.clone()) {
            continue;
        }
        let exists = root.is_dir();
        sources.push(SourceRoot {
            path: root.to_string_lossy().into_owned(),
            exists,
        });
        if !exists {
            continue;
        }
        let metadata =
            fs::symlink_metadata(root).map_err(|e| format!("stat {}: {e}", root.display()))?;
        found.dirs.push((root.clone(), mode_of(&metadata)));
        walk(root, &mut found, true).map_err(|e| format!("scan {}: {e}", root.display()))?;
    }
    for file in &spec.extra_files {
        if found.files.iter().any(|existing| &existing.path == file) {
            continue;
        }
        match fs::symlink_metadata(file) {
            Ok(metadata) if metadata.is_file() => {
                // Keep a configured database's WAL sidecar with it.
                found.files.push(Found {
                    path: file.clone(),
                    metadata,
                });
                for suffix in ["-wal", "-journal"] {
                    let side = sidecar(file, suffix);
                    if let Ok(side_metadata) = fs::symlink_metadata(&side)
                        && side_metadata.is_file()
                        && !found.files.iter().any(|existing| existing.path == side)
                    {
                        found.files.push(Found {
                            path: side,
                            metadata: side_metadata,
                        });
                    }
                }
            }
            Ok(_) => found.skipped.push(Skipped {
                source: file.to_string_lossy().into_owned(),
                reason: "not a regular file".to_owned(),
            }),
            Err(error) if error.kind() == io::ErrorKind::NotFound => {}
            Err(error) => return Err(format!("stat {}: {error}", file.display())),
        }
    }
    Ok((found, sources))
}

/// Ensures the pre-Rust backup exists. `Ok(Created)` means the caller should
/// print the single stderr notice; an `Err` means state exists but is NOT
/// backed up and the command must be aborted.
///
/// # Errors
/// Returns a human-readable reason when the backup could not be completed.
pub fn ensure(spec: &Spec) -> Result<Outcome, String> {
    if opted_out() {
        return Ok(Outcome::Disabled);
    }
    if marker_path(spec).is_file() {
        return Ok(Outcome::AlreadyDone);
    }
    let (found, sources) = inventory(spec)?;
    let has_state = !found.files.is_empty();
    let completed = backup_dir(spec).join("manifest.json").is_file();
    if !has_state && !completed {
        // A fresh install has nothing to back up; record that so state created
        // by Rust itself is not treated as pre-Rust state later. Best effort:
        // a read-only location simply leaves the decision to the next run.
        let _ = write_marker(spec, "no-state");
        return Ok(Outcome::NoState);
    }
    fs::create_dir_all(&spec.backups_dir)
        .map_err(|e| format!("create {}: {e}", spec.backups_dir.display()))?;
    let lock = File::options()
        .create(true)
        .truncate(false)
        .write(true)
        .open(spec.backups_dir.join(LOCK_NAME))
        .map_err(|e| format!("open backup lock: {e}"))?;
    lock.lock()
        .map_err(|e| format!("lock backup directory: {e}"))?;
    // Another first run may have finished while this one waited.
    if marker_path(spec).is_file() {
        return Ok(Outcome::AlreadyDone);
    }
    if backup_dir(spec).join("manifest.json").is_file() {
        write_marker(spec, "backed-up")?;
        return Ok(Outcome::AlreadyDone);
    }
    // Re-scan under the lock: the state may have changed while waiting.
    let (found, sources_locked) = inventory(spec)?;
    drop(sources);
    let outcome = create(spec, &found, sources_locked);
    drop(lock);
    outcome
}

fn write_marker(spec: &Spec, result: &str) -> Result<(), String> {
    fs::create_dir_all(&spec.backups_dir)
        .map_err(|e| format!("create {}: {e}", spec.backups_dir.display()))?;
    let marker = Marker {
        schema_version: SCHEMA_VERSION,
        result: result.to_owned(),
        created_at: now(),
        symdesk_version: spec.version.clone(),
    };
    let encoded = serde_json::to_vec_pretty(&marker).map_err(|e| e.to_string())?;
    atomic_write(&marker_path(spec), &encoded).map_err(|e| format!("write backup marker: {e}"))
}

fn atomic_write(path: &Path, bytes: &[u8]) -> io::Result<()> {
    let temporary = sidecar(path, &format!(".tmp-{}", std::process::id()));
    let mut file = File::create(&temporary)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    drop(file);
    fs::rename(&temporary, path).inspect_err(|_| {
        let _ = fs::remove_file(&temporary);
    })
}

fn create(spec: &Spec, found: &Inventory, sources: Vec<SourceRoot>) -> Result<Outcome, String> {
    let stamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap_or(Duration::ZERO)
        .as_nanos();
    let temporary = spec
        .backups_dir
        .join(format!(".{BACKUP_NAME}.tmp-{}-{stamp}", std::process::id()));
    // Stale temporary directories of crashed runs are not reused.
    if let Ok(entries) = fs::read_dir(&spec.backups_dir) {
        for entry in entries.flatten() {
            let name = entry.file_name().to_string_lossy().into_owned();
            if name.starts_with(&format!(".{BACKUP_NAME}.tmp-")) && entry.path() != temporary {
                let _ = fs::remove_dir_all(entry.path());
            }
        }
    }
    let result = populate(spec, found, sources, &temporary);
    match result {
        Ok(files) => {
            let target = backup_dir(spec);
            fs::rename(&temporary, &target).map_err(|e| {
                let _ = fs::remove_dir_all(&temporary);
                format!("publish backup {}: {e}", target.display())
            })?;
            write_marker(spec, "backed-up")?;
            Ok(Outcome::Created { dir: target, files })
        }
        Err(error) => {
            let _ = fs::remove_dir_all(&temporary);
            Err(error)
        }
    }
}

fn populate(
    spec: &Spec,
    found: &Inventory,
    sources: Vec<SourceRoot>,
    temporary: &Path,
) -> Result<usize, String> {
    let files_dir = temporary.join("files");
    fs::create_dir_all(&files_dir).map_err(|e| format!("create {}: {e}", files_dir.display()))?;
    let databases: BTreeSet<&Path> = found
        .files
        .iter()
        .filter(|file| is_sqlite(&file.path))
        .map(|file| file.path.as_path())
        .collect();
    let mut folded: BTreeSet<PathBuf> = BTreeSet::new();
    let mut skipped = found
        .skipped
        .iter()
        .map(|item| Skipped {
            source: item.source.clone(),
            reason: item.reason.clone(),
        })
        .collect::<Vec<_>>();
    let mut copier = Copier {
        files_dir: &files_dir,
        index: 0,
        entries: Vec::new(),
    };
    // Databases first: a snapshot folds the WAL/journal, so those sidecars must
    // not also be copied (a stale WAL against a different main file corrupts).
    for file in found
        .files
        .iter()
        .filter(|file| databases.contains(file.path.as_path()))
    {
        let method = copier.copy(file, true)?;
        if method == "sqlite-backup" {
            folded.insert(sidecar(&file.path, "-wal"));
            folded.insert(sidecar(&file.path, "-journal"));
        }
    }
    for file in found
        .files
        .iter()
        .filter(|file| !databases.contains(file.path.as_path()))
    {
        if folded.contains(&file.path) {
            skipped.push(Skipped {
                source: file.path.to_string_lossy().into_owned(),
                reason: "folded into the SQLite snapshot of its database".to_owned(),
            });
            continue;
        }
        copier.copy(file, false)?;
    }
    let mut entries = copier.entries;
    entries.sort_by(|left, right| left.source.cmp(&right.source));
    let mut dirs: Vec<DirEntry> = found
        .dirs
        .iter()
        .map(|(path, mode)| DirEntry {
            source: path.to_string_lossy().into_owned(),
            mode: *mode,
        })
        .collect();
    dirs.sort_by(|left, right| left.source.cmp(&right.source));
    let count = entries.len();
    let manifest = Manifest {
        schema_version: SCHEMA_VERSION,
        created_at: now(),
        symdesk_version: spec.version.clone(),
        tool: spec.tool.clone(),
        sources,
        dirs,
        files: entries,
        skipped,
    };
    let encoded = serde_json::to_vec_pretty(&manifest).map_err(|e| e.to_string())?;
    // The manifest is written last: its presence marks a complete backup.
    atomic_write(&temporary.join("manifest.json"), &encoded)
        .map_err(|e| format!("write manifest: {e}"))?;
    Ok(count)
}

struct Copier<'a> {
    files_dir: &'a Path,
    index: usize,
    entries: Vec<FileEntry>,
}

impl Copier<'_> {
    fn copy(&mut self, found: &Found, database: bool) -> Result<String, String> {
        let relative = format!("{:05}-{}", self.index, file_label(&found.path));
        self.index += 1;
        let destination = self.files_dir.join(&relative);
        let method = if database {
            copy_database(&found.path, &destination)?
        } else {
            fs::copy(&found.path, &destination)
                .map_err(|e| format!("copy {}: {e}", found.path.display()))?;
            "copy".to_owned()
        };
        let (sha256, size) = sha256_file(&destination)
            .map_err(|e| format!("hash {}: {e}", destination.display()))?;
        self.entries.push(FileEntry {
            source: found.path.to_string_lossy().into_owned(),
            backup: format!("files/{relative}"),
            sha256,
            size,
            mode: mode_of(&found.metadata),
            mtime_ns: mtime_ns(&found.metadata),
            method: method.clone(),
        });
        Ok(method)
    }
}

fn file_label(path: &Path) -> String {
    path.file_name().map_or_else(
        || "file".to_owned(),
        |name| {
            name.to_string_lossy()
                .chars()
                .map(|c| {
                    if c.is_ascii_alphanumeric() || matches!(c, '.' | '-' | '_') {
                        c
                    } else {
                        '_'
                    }
                })
                .collect()
        },
    )
}

fn header_counter(path: &Path) -> Option<[u8; 4]> {
    let mut header = [0_u8; 28];
    File::open(path).ok()?.read_exact(&mut header).ok()?;
    header[24..28].try_into().ok()
}

fn quiescent(path: &Path) -> bool {
    !non_empty(&sidecar(path, "-wal")) && !non_empty(&sidecar(path, "-journal"))
}

/// Copies one SQLite database and reports the method used.
fn copy_database(source: &Path, destination: &Path) -> Result<String, String> {
    for _ in 0..3 {
        if !quiescent(source) {
            break;
        }
        let before = fs::metadata(source).map_err(|e| format!("stat {}: {e}", source.display()))?;
        let counter = header_counter(source);
        fs::copy(source, destination).map_err(|e| format!("copy {}: {e}", source.display()))?;
        let after = fs::metadata(source).map_err(|e| format!("stat {}: {e}", source.display()))?;
        if quiescent(source)
            && before.len() == after.len()
            && mtime_ns(&before) == mtime_ns(&after)
            && counter == header_counter(source)
        {
            return Ok("copy".to_owned());
        }
        let _ = fs::remove_file(destination);
    }
    let connection = Connection::open_with_flags(
        source,
        OpenFlags::SQLITE_OPEN_READ_ONLY | OpenFlags::SQLITE_OPEN_NO_MUTEX,
    )
    .map_err(|e| format!("open {} for snapshot: {e}", source.display()))?;
    connection
        .busy_timeout(Duration::from_secs(10))
        .map_err(|e| e.to_string())?;
    let _ = fs::remove_file(destination);
    connection
        .execute("VACUUM INTO ?1", [destination.to_string_lossy().as_ref()])
        .map_err(|e| format!("snapshot {}: {e}", source.display()))?;
    drop(connection);
    let check = Connection::open_with_flags(destination, OpenFlags::SQLITE_OPEN_READ_ONLY)
        .and_then(|c| c.query_row("PRAGMA integrity_check", [], |row| row.get::<_, String>(0)))
        .map_err(|e| format!("verify snapshot of {}: {e}", source.display()))?;
    if check != "ok" {
        return Err(format!(
            "snapshot of {} failed its integrity check: {check}",
            source.display()
        ));
    }
    Ok("sqlite-backup".to_owned())
}

fn safe_relative(path: &str) -> bool {
    let candidate = Path::new(path);
    !path.is_empty()
        && candidate.is_relative()
        && candidate
            .components()
            .all(|component| matches!(component, Component::Normal(_)))
}

fn load_manifest(dir: &Path) -> Result<Manifest, String> {
    let path = dir.join("manifest.json");
    let bytes = fs::read(&path).map_err(|e| {
        if e.kind() == io::ErrorKind::NotFound {
            format!(
                "no pre-Rust backup found: {} does not exist (it is created on the first Rust run that finds existing state)",
                path.display()
            )
        } else {
            format!("read {}: {e}", path.display())
        }
    })?;
    let manifest: Manifest =
        serde_json::from_slice(&bytes).map_err(|e| format!("decode {}: {e}", path.display()))?;
    if manifest.schema_version != SCHEMA_VERSION {
        return Err(format!(
            "unsupported backup schema_version {} (this build reads {SCHEMA_VERSION})",
            manifest.schema_version
        ));
    }
    Ok(manifest)
}

/// Verifies every file of the backup against its manifest.
///
/// # Errors
/// Lists every missing or corrupted file; nothing is modified.
pub fn verify(spec: &Spec) -> Result<usize, String> {
    let dir = backup_dir(spec);
    let manifest = load_manifest(&dir)?;
    verify_files(&dir, &manifest)
}

fn verify_files(dir: &Path, manifest: &Manifest) -> Result<usize, String> {
    let mut problems = Vec::new();
    for file in &manifest.files {
        if !safe_relative(&file.backup) {
            problems.push(format!(
                "{}: unsafe backup path {:?}",
                file.source, file.backup
            ));
            continue;
        }
        match sha256_file(&dir.join(&file.backup)) {
            Ok((digest, size)) if digest == file.sha256 && size == file.size => {}
            Ok((digest, size)) => problems.push(format!(
                "{}: backup copy changed (sha256 {digest}/{size} bytes, manifest {}/{})",
                file.source, file.sha256, file.size
            )),
            Err(error) => {
                problems.push(format!("{}: backup copy unreadable: {error}", file.source))
            }
        }
    }
    if problems.is_empty() {
        Ok(manifest.files.len())
    } else {
        Err(format!(
            "the pre-Rust backup failed verification; nothing was restored:\n  {}",
            problems.join("\n  ")
        ))
    }
}

/// Restores the pre-Rust backup. The backup is verified first, the current
/// state of every backed-up root is then moved aside (never deleted), and the
/// backed-up files are placed back atomically one by one and re-verified.
///
/// # Errors
/// Returns the reason; on a failure after state was moved aside it is moved
/// back before returning.
pub fn restore(spec: &Spec, dry_run: bool) -> Result<RestoreReport, String> {
    let dir = backup_dir(spec);
    let manifest = load_manifest(&dir)?;
    let verified = verify_files(&dir, &manifest)?;
    let mut report = RestoreReport {
        dry_run,
        backup_dir: dir.clone(),
        files: manifest.files.len(),
        verified_files: verified,
        ..RestoreReport::default()
    };
    let mut moves: Vec<(PathBuf, String)> = Vec::new();
    for (position, root) in manifest.sources.iter().enumerate() {
        let root_path = Path::new(&root.path);
        if !root_path.is_dir() {
            continue;
        }
        let mut names: Vec<_> = fs::read_dir(root_path)
            .map_err(|e| format!("read {}: {e}", root.path))?
            .flatten()
            .map(|entry| entry.file_name().to_string_lossy().into_owned())
            .filter(|name| !SKIPPED_TOP_LEVEL.contains(&name.as_str()))
            .collect();
        names.sort();
        for name in names {
            moves.push((root_path.join(&name), format!("root-{position}/{name}")));
        }
    }
    let root_paths: BTreeSet<&str> = manifest
        .sources
        .iter()
        .map(|root| root.path.as_str())
        .collect();
    for file in &manifest.files {
        let in_root = root_paths
            .iter()
            .any(|root| Path::new(&file.source).starts_with(root));
        if !in_root && Path::new(&file.source).exists() {
            moves.push((
                PathBuf::from(&file.source),
                format!("extra/{}", file_label(Path::new(&file.source))),
            ));
        }
        // Sidecars of an out-of-root database go aside with it.
        if !in_root {
            for suffix in ["-wal", "-shm", "-journal"] {
                let side = sidecar(Path::new(&file.source), suffix);
                if side.exists() {
                    moves.push((side.clone(), format!("extra/{}", file_label(&side))));
                }
            }
        }
    }
    report.set_aside = moves
        .iter()
        .map(|(path, _)| path.to_string_lossy().into_owned())
        .collect();
    if dry_run {
        return Ok(report);
    }
    let stamp = time::OffsetDateTime::now_utc();
    let aside = spec.backups_dir.join(format!(
        "pre-restore-{:04}{:02}{:02}T{:02}{:02}{:02}Z-{}",
        stamp.year(),
        u8::from(stamp.month()),
        stamp.day(),
        stamp.hour(),
        stamp.minute(),
        stamp.second(),
        std::process::id()
    ));
    fs::create_dir_all(&aside).map_err(|e| format!("create {}: {e}", aside.display()))?;
    report.aside_dir = Some(aside.clone());
    let mut moved: Vec<(PathBuf, PathBuf)> = Vec::new();
    let outcome = (|| -> Result<(), String> {
        for (source, label) in &moves {
            let target = aside.join(label);
            fs::create_dir_all(target.parent().unwrap_or(&aside))
                .map_err(|e| format!("create {}: {e}", target.display()))?;
            fs::rename(source, &target)
                .map_err(|e| format!("move {} aside: {e}", source.display()))?;
            moved.push((source.clone(), target));
        }
        place(&dir, &manifest)
    })();
    if let Err(error) = outcome {
        // Put the previous state back so a failed restore changes nothing.
        for file in &manifest.files {
            let _ = fs::remove_file(&file.source);
        }
        for (source, target) in moved.iter().rev() {
            let _ = fs::remove_dir_all(source).or_else(|_| fs::remove_file(source));
            let _ = fs::rename(target, source);
        }
        return Err(format!(
            "restore failed and the previous state was put back: {error}"
        ));
    }
    Ok(report)
}

fn place(dir: &Path, manifest: &Manifest) -> Result<(), String> {
    for entry in &manifest.dirs {
        fs::create_dir_all(&entry.source).map_err(|e| format!("create {}: {e}", entry.source))?;
    }
    for file in &manifest.files {
        let target = Path::new(&file.source);
        if let Some(parent) = target.parent() {
            fs::create_dir_all(parent).map_err(|e| format!("create {}: {e}", parent.display()))?;
        }
        let temporary = sidecar(target, ".restore-tmp");
        fs::copy(dir.join(&file.backup), &temporary)
            .map_err(|e| format!("copy backup of {}: {e}", file.source))?;
        set_mode(&temporary, file.mode).map_err(|e| format!("set mode of {}: {e}", file.source))?;
        let stamp = if file.mtime_ns >= 0 {
            UNIX_EPOCH + Duration::from_nanos(file.mtime_ns.unsigned_abs())
        } else {
            UNIX_EPOCH - Duration::from_nanos(file.mtime_ns.unsigned_abs())
        };
        File::options()
            .write(true)
            .open(&temporary)
            .and_then(|f| f.set_modified(stamp))
            .map_err(|e| format!("restore mtime of {}: {e}", file.source))?;
        fs::rename(&temporary, target).map_err(|e| format!("place {}: {e}", file.source))?;
    }
    for entry in manifest.dirs.iter().rev() {
        set_mode(Path::new(&entry.source), entry.mode)
            .map_err(|e| format!("set mode of {}: {e}", entry.source))?;
    }
    for file in &manifest.files {
        let (digest, size) = sha256_file(Path::new(&file.source))
            .map_err(|e| format!("verify restored {}: {e}", file.source))?;
        if digest != file.sha256 || size != file.size {
            return Err(format!(
                "restored {} does not match its manifest digest",
                file.source
            ));
        }
    }
    Ok(())
}

/// Result of a hidden `backup restore-pre-rust` invocation, so both CLIs share
/// one implementation and only forward the bytes.
#[derive(Debug, Eq, PartialEq)]
pub struct CommandResult {
    pub code: u8,
    pub stdout: String,
    pub stderr: String,
}

/// The single stderr notice printed after the automatic backup was created.
#[must_use]
pub fn created_notice(tool: &str, dir: &Path, files: usize) -> String {
    format!(
        "{tool}: existing state was backed up once before the first Rust run ({files} files): {}\n\
         {tool}: restore it with `{tool} backup restore-pre-rust` (preview with --dry-run); set {OPT_OUT_ENV}=off to skip this backup.\n",
        dir.display()
    )
}

/// Message for a backup that could not be completed. State is untouched.
#[must_use]
pub fn failure_message(tool: &str, error: &str) -> String {
    format!(
        "{tool}: cannot create the one-time pre-Rust state backup: {error}\n\
         {tool}: no state was changed and the command was not run. Fix the problem above, or set {OPT_OUT_ENV}=off to continue without a backup.\n"
    )
}

/// `backup restore-pre-rust [--dry-run]`; `args` are the words after
/// `restore-pre-rust`.
#[must_use]
pub fn restore_command(tool: &str, spec: &Spec, args: &[String]) -> CommandResult {
    let usage = format!(
        "Usage: {tool} backup restore-pre-rust [--dry-run]\n\n\
         Restores the state saved by the automatic pre-Rust backup. The backup is verified\n\
         against its manifest first; the current state is moved aside (not deleted) before\n\
         the saved files are placed back and re-verified.\n"
    );
    let mut dry_run = false;
    for arg in args {
        match arg.as_str() {
            "--dry-run" | "-dry-run" => dry_run = true,
            "-h" | "--help" | "help" => {
                return CommandResult {
                    code: 0,
                    stdout: usage,
                    stderr: String::new(),
                };
            }
            other => {
                return CommandResult {
                    code: 2,
                    stdout: String::new(),
                    stderr: format!("{tool}: unknown argument {other:?}\n\n{usage}"),
                };
            }
        }
    }
    match restore(spec, dry_run) {
        Ok(report) if dry_run => CommandResult {
            code: 0,
            stdout: format!(
                "Backup {} verified ({} files). Dry run: nothing was changed.\n\
                 Would move {} current entries aside and restore {} files.\n",
                report.backup_dir.display(),
                report.verified_files,
                report.set_aside.len(),
                report.files
            ),
            stderr: String::new(),
        },
        Ok(report) => CommandResult {
            code: 0,
            stdout: format!(
                "Restored {} files from {} (all digests verified).\nPrevious state moved to {}\n",
                report.files,
                report.backup_dir.display(),
                report
                    .aside_dir
                    .as_deref()
                    .map_or_else(String::new, |p| p.display().to_string())
            ),
            stderr: String::new(),
        },
        Err(error) => CommandResult {
            code: 1,
            stdout: String::new(),
            stderr: format!("{tool}: {error}\n"),
        },
    }
}
