use std::{
    fs::{self, DirBuilder, File, OpenOptions},
    io,
    path::{Component, Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

use rusqlite::Connection;

use crate::SidecarError;

static TEMP_SEQUENCE: AtomicU64 = AtomicU64::new(0);

/// Creates a private, validated, atomic SQLite snapshot from an open database.
/// The source path is read from the connection for Go-compatible same-path
/// detection; committed WAL pages are read from that same open connection.
///
/// # Errors
/// Returns an error if the source and destination are the same path, directory
/// creation or SQL snapshotting fails, or the output is not a SQLite database.
pub fn backup_database(connection: &Connection, destination: &Path) -> Result<(), SidecarError> {
    let source = connection
        .path()
        .ok_or_else(|| SidecarError::Contract("cannot snapshot an in-memory sidecar".to_owned()))?;
    let source = Path::new(source);
    if same_file_path(source, destination) {
        return Err(SidecarError::Contract(
            "source and destination are the same index file".to_owned(),
        ));
    }
    let source_info = fs::metadata(source)
        .map_err(|error| SidecarError::Contract(format!("stat retrieval index: {error}")))?;
    if !source_info.is_file() {
        return Err(SidecarError::Contract(format!(
            "retrieval index is not a regular file: {}",
            source.display()
        )));
    }

    let parent = destination
        .parent()
        .filter(|path| !path.as_os_str().is_empty())
        .unwrap_or_else(|| Path::new("."));
    create_private_dir_all(parent)
        .map_err(|error| SidecarError::Contract(format!("create index directory: {error}")))?;

    let (temporary, placeholder) = create_private_temp(parent)?;
    drop(placeholder);
    fs::remove_file(&temporary)
        .map_err(|error| SidecarError::Contract(format!("prepare snapshot path: {error}")))?;

    let result = backup_at(connection, &temporary, destination);
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

/// Creates a WAL-consistent relocation snapshot and returns its absolute path.
/// The source remains intact; callers persist the returned path only after
/// this operation succeeds.
pub fn relocate_database(
    connection: &Connection,
    destination: &Path,
) -> Result<PathBuf, SidecarError> {
    let destination = if destination.is_absolute() {
        destination.to_path_buf()
    } else {
        std::env::current_dir()
            .map_err(|error| {
                SidecarError::Contract(format!("absolute index relocation path: {error}"))
            })?
            .join(destination)
    };
    let destination = clean_path(&destination);
    backup_database(connection, &destination)?;
    Ok(destination)
}

/// Validates and atomically restores a SQLite backup without modifying it.
/// Callers close long-lived destination connections before replacement.
pub fn restore_database(source: &Path, destination: &Path) -> Result<(), SidecarError> {
    let info = fs::metadata(source)
        .map_err(|error| SidecarError::Contract(format!("stat index backup: {error}")))?;
    if !info.is_file() {
        return Err(SidecarError::Contract(format!(
            "index backup is not a regular file: {}",
            source.display()
        )));
    }
    validate_sqlite_header(source)?;
    if same_file_path(source, destination) {
        return Err(SidecarError::Contract(
            "source and destination are the same index file".to_owned(),
        ));
    }
    let parent = destination
        .parent()
        .filter(|path| !path.as_os_str().is_empty())
        .unwrap_or_else(|| Path::new("."));
    create_private_dir_all(parent)
        .map_err(|error| SidecarError::Contract(format!("create index directory: {error}")))?;
    let mut input = File::open(source)
        .map_err(|error| SidecarError::Contract(format!("open index file: {error}")))?;
    let (temporary, mut output) = create_private_temp(parent)?;
    let result = (|| {
        let copied = io::copy(&mut input, &mut output)
            .map_err(|error| SidecarError::Contract(format!("copy index file: {error}")))?;
        if copied != info.len() {
            return Err(SidecarError::Contract(format!(
                "copy index file: copied {copied} bytes, want {}",
                info.len()
            )));
        }
        output
            .sync_all()
            .map_err(|error| SidecarError::Contract(format!("sync index file: {error}")))?;
        drop(output);
        fs::rename(&temporary, destination)
            .map_err(|error| SidecarError::Contract(format!("replace retrieval index: {error}")))
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

fn backup_at(
    connection: &Connection,
    temporary: &Path,
    destination: &Path,
) -> Result<(), SidecarError> {
    let path = temporary
        .to_str()
        .ok_or_else(|| SidecarError::Contract("snapshot path is not valid UTF-8".to_owned()))?;
    let quoted = path.replace('\'', "''");
    connection
        .execute_batch(&format!("VACUUM INTO '{quoted}'"))
        .map_err(|error| SidecarError::Contract(format!("snapshot retrieval index: {error}")))?;

    let file = OpenOptions::new()
        .write(true)
        .open(temporary)
        .map_err(|error| SidecarError::Contract(format!("protect retrieval snapshot: {error}")))?;
    set_private_mode(&file)
        .map_err(|error| SidecarError::Contract(format!("protect retrieval snapshot: {error}")))?;
    drop(file);

    validate_sqlite_header(temporary)?;
    fs::rename(temporary, destination)
        .map_err(|error| SidecarError::Contract(format!("replace retrieval snapshot: {error}")))
}

fn create_private_temp(parent: &Path) -> Result<(PathBuf, File), SidecarError> {
    for _ in 0..100 {
        let sequence = TEMP_SEQUENCE.fetch_add(1, Ordering::Relaxed);
        let path = parent.join(format!(
            ".symdesk-snapshot-{}-{sequence}.db",
            std::process::id()
        ));
        let mut options = OpenOptions::new();
        options.write(true).read(true).create_new(true);
        set_create_mode(&mut options);
        match options.open(&path) {
            Ok(file) => return Ok((path, file)),
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(error) => {
                return Err(SidecarError::Contract(format!(
                    "create snapshot path: {error}"
                )));
            }
        }
    }
    Err(SidecarError::Contract(
        "create snapshot path: unable to allocate a unique temporary path".to_owned(),
    ))
}

fn validate_sqlite_header(path: &Path) -> Result<(), SidecarError> {
    let mut file = File::open(path)
        .map_err(|error| SidecarError::Contract(format!("open index backup: {error}")))?;
    let mut header = [0; 16];
    std::io::Read::read_exact(&mut file, &mut header)
        .map_err(|error| SidecarError::Contract(format!("read index backup header: {error}")))?;
    if &header != b"SQLite format 3\0" {
        return Err(SidecarError::Contract(format!(
            "index backup is not a SQLite database: {}",
            path.display()
        )));
    }
    Ok(())
}

fn clean_path(path: &Path) -> PathBuf {
    let mut cleaned = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                if !cleaned.pop() {
                    cleaned.push(component.as_os_str());
                }
            }
            other => cleaned.push(other.as_os_str()),
        }
    }
    if cleaned.as_os_str().is_empty() {
        PathBuf::from(".")
    } else {
        cleaned
    }
}

fn same_file_path(source: &Path, destination: &Path) -> bool {
    let source = clean_path(source);
    let destination = clean_path(destination);
    source == destination
        || matches!(
            (fs::canonicalize(&source), fs::canonicalize(&destination)),
            (Ok(source), Ok(destination)) if source == destination
        )
}

#[cfg(unix)]
fn set_create_mode(options: &mut OpenOptions) {
    use std::os::unix::fs::OpenOptionsExt;
    options.mode(0o600);
}

#[cfg(unix)]
fn create_private_dir_all(path: &Path) -> std::io::Result<()> {
    use std::os::unix::fs::DirBuilderExt;
    let mut builder = DirBuilder::new();
    builder.recursive(true).mode(0o700).create(path)
}

#[cfg(not(unix))]
fn create_private_dir_all(path: &Path) -> std::io::Result<()> {
    let mut builder = DirBuilder::new();
    builder.recursive(true).create(path)
}

#[cfg(not(unix))]
fn set_create_mode(_options: &mut OpenOptions) {}

#[cfg(unix)]
fn set_private_mode(file: &File) -> std::io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    file.set_permissions(fs::Permissions::from_mode(0o600))
}

#[cfg(not(unix))]
#[expect(
    clippy::permissions_set_readonly_false,
    reason = "Windows clears only the read-only attribute"
)]
fn set_private_mode(file: &File) -> std::io::Result<()> {
    let mut permissions = file.metadata()?.permissions();
    permissions.set_readonly(false);
    file.set_permissions(permissions)
}
