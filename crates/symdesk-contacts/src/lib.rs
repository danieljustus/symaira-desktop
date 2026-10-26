//! The absorbed SymRelate contact store. Go remains the production oracle.
#![deny(unsafe_code)]

use rusqlite::Connection;
use std::path::Path;
use std::time::Duration;

/// Byte-identical copies of the Go embedded migrations, in version order.
pub const MIGRATIONS: &[(i64, &str, &str)] = &[
    (1, "init", include_str!("../migrations/0001_init.sql")),
    (
        2,
        "contacts",
        include_str!("../migrations/0002_contacts.sql"),
    ),
    (
        3,
        "relationships",
        include_str!("../migrations/0003_relationships.sql"),
    ),
    (
        4,
        "security",
        include_str!("../migrations/0004_security.sql"),
    ),
    (5, "import", include_str!("../migrations/0005_import.sql")),
    (
        6,
        "memory_links",
        include_str!("../migrations/0006_memory_links.sql"),
    ),
    (
        7,
        "meeting_idempotency",
        include_str!("../migrations/0007_meeting_idempotency.sql"),
    ),
];

/// Open a local store with the Go store's timeout, WAL and foreign-key policy.
pub fn open(path: impl AsRef<Path>) -> rusqlite::Result<Connection> {
    let mut db = Connection::open(path)?;
    db.busy_timeout(Duration::from_secs(5))?;
    db.execute_batch(
        "PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA foreign_keys=ON;",
    )?;
    migrate(&mut db)?;
    Ok(db)
}

/// An independent in-memory store, with the same schema and foreign keys.
pub fn open_memory() -> rusqlite::Result<Connection> {
    let mut db = Connection::open_in_memory()?;
    db.execute_batch("PRAGMA foreign_keys=ON")?;
    migrate(&mut db)?;
    Ok(db)
}

/// Apply each missing version in its own transaction. Already recorded versions
/// are left untouched, including their original applied_at timestamp.
pub fn migrate(db: &mut Connection) -> rusqlite::Result<()> {
    db.execute_batch("\n\t\tCREATE TABLE IF NOT EXISTS schema_migrations (\n\t\t\tversion    INTEGER PRIMARY KEY,\n\t\t\tname       TEXT NOT NULL,\n\t\t\tapplied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))\n\t\t)")?;
    let applied: Vec<i64> = db
        .prepare("SELECT version FROM schema_migrations")?
        .query_map([], |r| r.get(0))?
        .collect::<rusqlite::Result<_>>()?;
    for &(version, name, sql) in MIGRATIONS {
        if applied.contains(&version) {
            continue;
        }
        let tx = db.transaction()?;
        // Match Go's deliberately simple comment stripping and statement split,
        // rather than passing an entire script to SQLite's more powerful parser.
        let stripped = sql
            .split('\n')
            .map(|line| line.split_once("--").map_or(line, |(head, _)| head))
            .collect::<Vec<_>>()
            .join("\n")
            + "\n";
        for stmt in stripped.split(';') {
            if !stmt.trim().is_empty() {
                tx.execute_batch(stmt)?;
            }
        }
        tx.execute(
            "INSERT INTO schema_migrations (version, name) VALUES (?, ?)",
            (version, name),
        )?;
        tx.commit()?;
    }
    Ok(())
}
