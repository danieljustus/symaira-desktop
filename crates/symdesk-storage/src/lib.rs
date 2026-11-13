#![deny(unsafe_code)]

//! Port of Go `internal/storage` PostgreSQL key/value store (contract row
//! PG-001): the `storage_entries` table that backs the self-host server's
//! persistent state.
//!
//! The Go-recorded fixture `testdata/port/storage/postgres.json` (written by
//! `scripts/rust-port/cmd/pggen` against a real PostgreSQL server) pins the
//! DDL, results, byte ordering, error classes with SQLSTATE and server text,
//! pooling and lock behavior. `tests/postgres_contracts.rs` replays all of it
//! (`make postgres-integration`).
//!
//! Driver: `tokio-postgres`, a pure-Rust client (SCRAM-SHA-256 included), so
//! neither gate host needs libpq. The pool is a small purpose-built one
//! because the contract needs two things generic pools hide: a deadline that
//! also aborts the statement server-side (pgx cancels the query and discards
//! the connection) and `pool_max_conns`/queueing semantics identical to
//! pgxpool.
//!
//! Declared differences from Go (none is exercised by the fixture):
//! - TLS is not implemented: `sslmode=require|verify-ca|verify-full` fails to
//!   connect like Go against a server without TLS, and a server that demands
//!   TLS is unreachable. A `rustls` connector is the follow-up.
//! - Only `pool_max_conns` of the pgxpool `pool_*` DSN parameters is
//!   supported; the others are rejected as DSN errors instead of applied.
//! - There is no `context.Context`: cancellation is by dropping the future
//!   (which cancels the statement) or by [`PostgresBackend::bounded`].
//! - Keys are `&str`, so Go's invalid-UTF-8 keys cannot be expressed.
//! - After a server-side connection kill (`pg_terminate_backend`) Go
//!   surfaces the dead connection's error to the next caller; this port
//!   discards connections it knows are closed and reconnects transparently.

mod dsn;
mod postgres;

use std::fmt;

#[path = "../../symroom-cli/src/go_isprint.rs"]
mod go_isprint;

pub use postgres::{Bounded, PostgresBackend};

/// One key/value pair returned by [`PostgresBackend::query`].
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Entry {
    pub key: String,
    pub value: Vec<u8>,
}

/// Failure of a store operation. [`fmt::Display`] reproduces the Go error
/// text where the Go text is deterministic (server errors, not-found, invalid
/// key, empty DSN); connection and DSN details are driver-specific.
#[derive(Debug, Eq, PartialEq)]
pub enum StorageError {
    /// `storage: key not found: "<key>"` (`ErrNotFound`).
    NotFound(String),
    /// `storage: key must not be empty` (`ErrInvalidKey`).
    InvalidKey,
    /// `storage: PostgreSQL DSN must not be empty`.
    EmptyDsn,
    /// `parse PostgreSQL DSN: <detail>`.
    Dsn(String),
    /// `connect to PostgreSQL: <detail>`; the server's SQLSTATE and primary
    /// message when it rejected the startup (authentication, missing
    /// database, ...).
    Connect {
        detail: String,
        sqlstate: Option<String>,
        server_message: Option<String>,
    },
    /// An error the server reported for a statement:
    /// `<context>: ERROR: <message> (SQLSTATE <code>)`.
    Server {
        context: String,
        severity: String,
        message: String,
        sqlstate: String,
    },
    /// A client-side failure without a SQLSTATE (I/O, protocol, decoding).
    Client { context: String, detail: String },
    /// The deadline passed (`context deadline exceeded`).
    Timeout,
    /// The store was closed (`closed pool`).
    Closed,
}

impl fmt::Display for StorageError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::NotFound(key) => write!(f, "storage: key not found: {}", go_quote(key)),
            Self::InvalidKey => f.write_str("storage: key must not be empty"),
            Self::EmptyDsn => f.write_str("storage: PostgreSQL DSN must not be empty"),
            Self::Dsn(detail) => write!(f, "parse PostgreSQL DSN: {detail}"),
            Self::Connect { detail, .. } => write!(f, "connect to PostgreSQL: {detail}"),
            Self::Server {
                context,
                severity,
                message,
                sqlstate,
            } => write!(f, "{context}: {severity}: {message} (SQLSTATE {sqlstate})"),
            Self::Client { context, detail } => write!(f, "{context}: {detail}"),
            Self::Timeout => f.write_str("context deadline exceeded"),
            Self::Closed => f.write_str("closed pool"),
        }
    }
}

impl std::error::Error for StorageError {}

/// Go `%q` of a string (`strconv.Quote`), used in the Go error texts.
pub(crate) fn go_quote(text: &str) -> String {
    let mut out = String::with_capacity(text.len() + 2);
    out.push('"');
    for ch in text.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            ch if (ch as u32) < 0x20 || ch as u32 == 0x7f => {
                out.push_str(&format!("\\x{:02x}", ch as u32));
            }
            ch if !go_isprint::is_print(ch) => {
                if (ch as u32) < 0x10000 {
                    out.push_str(&format!("\\u{:04x}", ch as u32));
                } else {
                    out.push_str(&format!("\\U{:08x}", ch as u32));
                }
            }
            ch => out.push(ch),
        }
    }
    out.push('"');
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn quotes_like_go() {
        assert_eq!(go_quote("a\x00b"), "\"a\\x00b\"");
        assert_eq!(go_quote("\ttab\nnewline"), "\"\\ttab\\nnewline\"");
        assert_eq!(go_quote("ünï-日本-😀"), "\"ünï-日本-😀\"");
        assert_eq!(go_quote("q\"\\"), "\"q\\\"\\\\\"");
        assert_eq!(go_quote("nb\u{a0}sp"), "\"nb\\u00a0sp\"");
    }

    #[test]
    fn display_matches_go_texts() {
        assert_eq!(
            StorageError::NotFound("k\n".into()).to_string(),
            "storage: key not found: \"k\\n\""
        );
        assert_eq!(
            StorageError::InvalidKey.to_string(),
            "storage: key must not be empty"
        );
        assert_eq!(
            StorageError::Server {
                context: "get storage key \"a\"".into(),
                severity: "ERROR".into(),
                message: "column \"storage_key\" does not exist".into(),
                sqlstate: "42703".into(),
            }
            .to_string(),
            "get storage key \"a\": ERROR: column \"storage_key\" does not exist (SQLSTATE 42703)"
        );
    }
}
