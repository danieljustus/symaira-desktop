//! The absorbed SymRelate contact store. Go remains the production oracle.
#![deny(unsafe_code)]

pub mod contact;
pub mod import;
pub mod keyprovider;
pub mod membership;
pub mod pipeline;
pub mod relationship;
pub mod security;
pub mod vcard;

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

/// Go's `formatTime`: an RFC3339 instant rendered as UTC `RFC3339Nano`
/// (fraction without trailing zeros, `Z` suffix). Text that is not a valid
/// RFC3339 instant is returned unchanged, leaving rejection to the caller.
#[must_use]
pub fn utc_rfc3339nano(text: &str) -> String {
    match parse_rfc3339(text) {
        Some(parsed) => render_rfc3339nano(parsed.to_offset(time::UtcOffset::UTC)),
        None => text.to_owned(),
    }
}

/// The text Go's `json.Marshal` produces for a decoded `time.Time`: the
/// instant keeps its own offset (only the fraction is trimmed).
#[must_use]
pub fn marshal_rfc3339nano(text: &str) -> String {
    parse_rfc3339(text).map_or_else(|| text.to_owned(), render_rfc3339nano)
}

fn parse_rfc3339(text: &str) -> Option<time::OffsetDateTime> {
    time::OffsetDateTime::parse(text, &time::format_description::well_known::Rfc3339).ok()
}

fn render_rfc3339nano(value: time::OffsetDateTime) -> String {
    let mut out = format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}",
        value.year(),
        u8::from(value.month()),
        value.day(),
        value.hour(),
        value.minute(),
        value.second()
    );
    let nanos = value.nanosecond();
    if nanos != 0 {
        out.push('.');
        out.push_str(format!("{nanos:09}").trim_end_matches('0'));
    }
    let offset = value.offset();
    if offset.is_utc() {
        out.push('Z');
    } else {
        let (hours, minutes, _) = offset.as_hms();
        let sign = if offset.whole_seconds() < 0 { '-' } else { '+' };
        out.push_str(&format!("{sign}{:02}:{:02}", hours.abs(), minutes.abs()));
    }
    out
}

#[cfg(test)]
mod tests {
    use super::utc_rfc3339nano;

    #[test]
    fn normalizes_offsets_and_trims_fractions_like_go() {
        assert_eq!(utc_rfc3339nano("2025-06-01T14:00:00+02:00"), "2025-06-01T12:00:00Z");
        assert_eq!(utc_rfc3339nano("2025-06-01T12:00:00.500000000Z"), "2025-06-01T12:00:00.5Z");
        assert_eq!(utc_rfc3339nano("2026-12-31T23:59:59.999999999-05:30"), "2027-01-01T05:29:59.999999999Z");
        assert_eq!(utc_rfc3339nano("not a time"), "not a time");
        assert_eq!(utc_rfc3339nano(""), "");
        assert_eq!(super::marshal_rfc3339nano("2025-06-01T14:00:00.250+02:00"), "2025-06-01T14:00:00.25+02:00");
        assert_eq!(super::marshal_rfc3339nano("2025-06-01T14:00:00-00:00"), "2025-06-01T14:00:00Z");
    }
}
