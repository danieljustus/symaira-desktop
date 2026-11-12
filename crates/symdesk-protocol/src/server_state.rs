//! Startup state of the self-host server, mirroring `selfhost.NewServer`:
//! the private `.symdesk` directories, the job store directory, the persistent
//! state key/value store (`storage_entries`, SQLite at
//! `.symdesk/server/state.db`) and the one-time legacy-token admin migration.
//!
//! Go's `serve` command never sets `ServerConfig.StateDSN`, so the PostgreSQL
//! backend of the state store (`symdesk-storage`, PG-001) is unreachable from
//! the server process and the SQLite path is the only one wired here.

use std::{
    fs,
    path::{Path, PathBuf},
};

use rusqlite::OptionalExtension;
use symdesk_vault::permissions::{Manager, hash_token};

/// Key the legacy-token admin migration records once it ran.
const LEGACY_ADMIN_MIGRATED: &str = "state:legacy_admin_migrated";

fn make_private_dirs(path: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(path)
    }
    #[cfg(not(unix))]
    {
        fs::create_dir_all(path)
    }
}

/// Go: `os.MkdirAll(dir, 0700)` for the job store directory.
pub(crate) fn create_job_store(vault_root: &Path) -> Result<PathBuf, String> {
    let directory = vault_root.join(".symdesk/server/jobs");
    make_private_dirs(&directory).map_err(|error| format!("create job store: {error}"))?;
    Ok(directory)
}

/// `OpenServerState` + `SetIfAbsent`: records `key` unless it exists and
/// reports whether it wrote.
fn set_state_if_absent(vault_root: &Path, key: &str, value: &[u8]) -> Result<bool, String> {
    let directory = vault_root.join(".symdesk/server");
    make_private_dirs(&directory).map_err(|error| format!("server state directory: {error}"))?;
    let path = directory.join("state.db");
    // Go `sqlitekit.Open` (storage.OpenSQLite): WAL, foreign keys and the
    // five-second busy timeout through CoreKit; the directory exists already.
    let connection =
        symaira_core_sqlite::open_with_existing_parent(&path).map_err(|error| match error {
            symaira_core_sqlite::Error::Open(error) => {
                format!("server state (sqlite): open {}: {error}", path.display())
            }
            other => format!("server state (sqlite): open {}: {other}", path.display()),
        })?;
    connection
        .execute_batch(
            "CREATE TABLE IF NOT EXISTS storage_entries (
                storage_key TEXT PRIMARY KEY NOT NULL COLLATE BINARY,
                value BLOB NOT NULL
            )",
        )
        .map_err(|error| format!("server state (sqlite): {error}"))?;
    let exists = connection
        .query_row(
            "SELECT 1 FROM storage_entries WHERE storage_key = ?1",
            [key],
            |_| Ok(()),
        )
        .optional()
        .map_err(|error| format!("server state (sqlite): {error}"))?
        .is_some();
    if exists {
        return Ok(false);
    }
    connection
        .execute(
            "INSERT INTO storage_entries (storage_key, value) VALUES (?1, ?2)",
            rusqlite::params![key, value],
        )
        .map_err(|error| format!("server state (sqlite): {error}"))?;
    Ok(true)
}

/// The permissions manager initialisation and the one-time legacy-token admin
/// migration of `NewServer`: with no users yet, the admin token becomes the
/// token hash of an `admin` user (roles `admin`, `user`), once. A later
/// restart with other tokens keeps accepting the migrated token.
pub(crate) fn initialise_permissions(vault_root: &Path, token: &str) -> Result<(), String> {
    let manager = Manager::new(vault_root.join(".symdesk"))
        .map_err(|error| format!("permissions: {error}"))?;
    if token.is_empty() {
        return Ok(());
    }
    let users = manager
        .user_list()
        .map_err(|error| format!("permissions: list users: {error}"))?;
    if users.is_some_and(|users| !users.is_empty()) {
        return Ok(());
    }
    let stamp = time::OffsetDateTime::now_utc()
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|error| error.to_string())?;
    let migrated = set_state_if_absent(vault_root, LEGACY_ADMIN_MIGRATED, stamp.as_bytes())
        .map_err(|error| format!("server state: record legacy admin migration: {error}"))?;
    if !migrated {
        eprintln!("legacy-token admin migration already ran; not recreating admin user");
        return Ok(());
    }
    manager
        .user_add("admin", &["admin".to_owned(), "user".to_owned()])
        .map_err(|error| format!("permissions: create admin user: {error}"))?;
    manager
        .set_token_hash("admin", &hash_token(token))
        .map_err(|error| format!("permissions: set admin token: {error}"))?;
    Ok(())
}
