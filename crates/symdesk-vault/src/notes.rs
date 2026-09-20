#![deny(unsafe_code)]

//! Note-level file verbs of the vault write stack.
//!
//! These functions are the filesystem half of the Go service verbs recorded by
//! contract row VAULT-004 (harness: `internal/service/port_noteops_contract_test.go`):
//!
//! | Rust | Go |
//! |---|---|
//! | [`create_note`] | `Service.NoteNew` — `internal/service/service.go:551` |
//! | [`set_property`] | `Service.PropsEdit` — `internal/service/service.go:798` |
//! | [`move_note`] | `Service.NoteMove` — `internal/service/service.go:772` |
//!
//! The index/sidecar updates that the Go verbs perform around these filesystem
//! steps live in the index and service layers and are deliberately not part of
//! this crate.

use std::{
    fs::OpenOptions,
    io::Write as _,
    path::{Path, PathBuf},
};

use thiserror::Error;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::{
    mutations::{MutationError, set_frontmatter_value},
    paths::{SecurePathError, secure_path},
};

/// Errors returned by note operations.
#[derive(Debug, Error)]
pub enum NoteError {
    /// The Go `asn` guard: ASNs may only be assigned through the dedicated
    /// command, never through a generic property edit.
    #[error("use \"symdesk doc asn <file> <next|N>\" to assign an ASN safely")]
    AsnGuard,
    /// The vault-relative path is unsafe (traversal or symlink escape).
    #[error("{0}")]
    InvalidPath(#[from] SecurePathError),
    /// Writing the note file failed (Go: `failed to write file: %w`).
    #[error("failed to write file: {source}")]
    Create {
        /// Underlying filesystem error.
        #[source]
        source: std::io::Error,
    },
    /// Moving the note failed (Go: `failed to move file: %w`).
    #[error("failed to move file: {source}")]
    Move {
        /// Underlying filesystem error.
        #[source]
        source: std::io::Error,
    },
    /// A frontmatter edit failed.
    #[error("{0}")]
    Mutation(#[from] MutationError),
}

/// Returns the vault-relative file name a Go `note new` title maps to.
///
/// Mirrors `strings.ReplaceAll(title, " ", "_") + ".md"`; the title is
/// otherwise used verbatim, exactly like the Go implementation.
#[must_use]
pub fn note_file_name(title: &str) -> String {
    format!("{}.md", title.replace(' ', "_"))
}

/// Renders the compact frontmatter document a Go `note new` writes.
#[must_use]
pub fn note_document(title: &str, created: &str, content: &str) -> String {
    format!("---\ntitle: \"{title}\"\ncreated: \"{created}\"\ntags: []\n---\n\n{content}")
}

/// Writes a new note and returns its vault-relative path.
///
/// Timestamps use Go's `time.RFC3339` layout truncated to whole seconds.
///
/// # Errors
///
/// Returns [`NoteError`] when the derived path is unsafe or cannot be written.
pub fn create_note(
    vault_root: impl AsRef<Path>,
    title: &str,
    content: &str,
    created: OffsetDateTime,
) -> Result<String, NoteError> {
    let file_name = note_file_name(title);
    let path = secure_path(vault_root.as_ref(), &file_name)?;
    let document = note_document(title, &format_rfc3339(created), content);
    write_direct(&path, document.as_bytes())?;
    Ok(file_name)
}

/// Applies a scalar frontmatter edit to an existing note.
///
/// # Errors
///
/// Returns [`NoteError::AsnGuard`] for the reserved `asn` key and [`NoteError`]
/// when the path is unsafe or the atomic rewrite fails.
pub fn set_property(
    vault_root: impl AsRef<Path>,
    rel_path: &str,
    key: &str,
    value: &str,
) -> Result<(), NoteError> {
    if key == "asn" {
        return Err(NoteError::AsnGuard);
    }
    let path = secure_path(vault_root.as_ref(), rel_path)?;
    set_frontmatter_value(&path, key, &noyalib::Value::from(value.to_owned()))?;
    Ok(())
}

/// Renames a note inside the vault.
///
/// # Errors
///
/// Returns [`NoteError`] when either path is unsafe or the rename fails.
pub fn move_note(
    vault_root: impl AsRef<Path>,
    from_rel: &str,
    to_rel: &str,
) -> Result<PathBuf, NoteError> {
    let from = secure_path(vault_root.as_ref(), from_rel)?;
    let to = secure_path(vault_root.as_ref(), to_rel)?;
    std::fs::rename(&from, &to).map_err(|source| NoteError::Move { source })?;
    Ok(to)
}

/// Formats a timestamp with Go's `time.RFC3339` layout.
#[must_use]
pub fn format_rfc3339(value: OffsetDateTime) -> String {
    value
        .truncate_to_second()
        .format(&Rfc3339)
        .unwrap_or_default()
}

/// Mirrors Go's `os.WriteFile(path, data, 0o644)`: an existing file keeps its
/// mode, a new file is created with `0o644` minus the process umask.
fn write_direct(path: &Path, data: &[u8]) -> Result<(), NoteError> {
    let mut options = OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o644);
    }
    let mut file = options
        .open(path)
        .map_err(|source| NoteError::Create { source })?;
    file.write_all(data)
        .map_err(|source| NoteError::Create { source })
}
