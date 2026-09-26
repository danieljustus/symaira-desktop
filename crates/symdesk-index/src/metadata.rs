//! Per-vault sidecar metadata, byte-compatible with Go's `recordSidecarMetadata`.
//!
//! Go writes `metadata.json` next to `sidecar.db` whenever a vault sidecar is
//! opened without an explicit `SYMDESK_SIDECAR` override (issue #1006). The
//! record is produced by `json.Marshal` over a struct with a `time.Time` field,
//! so two Go-specific encodings have to be reproduced exactly:
//!
//! * `encoding/json` escapes `<`, `>`, `&`, U+2028 and U+2029 even though they
//!   are legal JSON string content, which `serde_json` does not do.
//! * `time.Time` marshals as RFC3339 with a nanosecond fraction whose trailing
//!   zeros are removed, dropping the fraction entirely at a whole second.

use std::{
    fs,
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

use crate::{Sidecar, SidecarError, path_for_vault};

pub const METADATA_FILE_NAME: &str = "metadata.json";

/// Opens the per-vault sidecar and records `metadata.json` beside it.
///
/// Mirrors Go's `sidecar.OpenForVault`: an explicit `SYMDESK_SIDECAR` override
/// suppresses the metadata record, and a failure to write it closes the
/// database and propagates the error instead of leaving a half-open sidecar.
///
/// # Errors
/// Returns the sidecar open error, or the filesystem error of the metadata
/// write.
pub fn open_for_vault(vault_root: &Path) -> Result<Sidecar, SidecarError> {
    let explicit = std::env::var("SYMDESK_SIDECAR")
        .ok()
        .map(|value| value.trim().to_owned())
        .filter(|value| !value.is_empty());
    let path = path_for_vault(vault_root)?;
    let sidecar = Sidecar::open(&path)?;
    if explicit.is_some() {
        return Ok(sidecar);
    }
    let canonical = canonical_vault_path(vault_root)?;
    let directory = path.parent().unwrap_or_else(|| Path::new("."));
    record_sidecar_metadata(directory, &canonical, SystemTime::now())?;
    Ok(sidecar)
}

/// Resolves the vault path the way Go records it: absolute, then symlink
/// resolved when that succeeds.
fn canonical_vault_path(vault_root: &Path) -> Result<PathBuf, SidecarError> {
    let absolute = if vault_root.is_absolute() {
        vault_root.to_path_buf()
    } else {
        std::env::current_dir()?.join(vault_root)
    };
    crate::absolute_non_verbatim(&fs::canonicalize(&absolute).unwrap_or(absolute))
}

/// Writes `metadata.json` atomically with Go's directory and file modes.
///
/// # Errors
/// Returns the underlying filesystem error, matching Go's failure points
/// (directory creation, temporary file creation, write, rename).
pub fn record_sidecar_metadata(
    directory: &Path,
    vault_path: &Path,
    last_used: SystemTime,
) -> Result<(), SidecarError> {
    let vault_path = vault_path
        .to_str()
        .ok_or_else(|| SidecarError::NonUtf8Path {
            context: "sidecar metadata vault path",
            path: vault_path.to_path_buf(),
        })?;
    let payload = encode_sidecar_metadata(vault_path, last_used)?;
    create_private_dir(directory)?;
    let temporary = directory.join(format!(".metadata-{}.tmp", temporary_suffix(last_used)));
    write_private_file(&temporary, payload.as_bytes())?;
    if let Err(error) = fs::rename(&temporary, directory.join(METADATA_FILE_NAME)) {
        let _ = fs::remove_file(&temporary);
        return Err(SidecarError::Io(error));
    }
    Ok(())
}

fn temporary_suffix(last_used: SystemTime) -> u128 {
    last_used
        .duration_since(UNIX_EPOCH)
        .map_or(0, |elapsed| elapsed.as_nanos())
}

#[cfg(unix)]
fn create_private_dir(directory: &Path) -> Result<(), SidecarError> {
    use std::os::unix::fs::DirBuilderExt;
    if directory.is_dir() {
        return Ok(());
    }
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(directory)?;
    Ok(())
}

#[cfg(not(unix))]
fn create_private_dir(directory: &Path) -> Result<(), SidecarError> {
    fs::create_dir_all(directory)?;
    Ok(())
}

#[cfg(unix)]
fn write_private_file(path: &Path, bytes: &[u8]) -> Result<(), SidecarError> {
    use std::{io::Write, os::unix::fs::OpenOptionsExt};
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)?;
    file.write_all(bytes)?;
    file.sync_all()?;
    Ok(())
}

#[cfg(not(unix))]
fn write_private_file(path: &Path, bytes: &[u8]) -> Result<(), SidecarError> {
    fs::write(path, bytes)?;
    Ok(())
}

/// Encodes the metadata record exactly as Go's `json.Marshal` does.
///
/// # Errors
/// Returns [`SidecarError::Time`] when the instant predates the Unix epoch by
/// more than the representable range.
pub fn encode_sidecar_metadata(
    vault_path: &str,
    last_used: SystemTime,
) -> Result<String, SidecarError> {
    let (seconds, nanos) = unix_parts(last_used)?;
    Ok(encode_sidecar_metadata_at(vault_path, seconds, nanos))
}

fn unix_parts(instant: SystemTime) -> Result<(i64, u32), SidecarError> {
    match instant.duration_since(UNIX_EPOCH) {
        Ok(elapsed) => {
            let seconds = i64::try_from(elapsed.as_secs())
                .map_err(|_| SidecarError::Time("timestamp out of range".to_owned()))?;
            Ok((seconds, elapsed.subsec_nanos()))
        }
        Err(error) => {
            let elapsed = error.duration();
            let seconds = i64::try_from(elapsed.as_secs())
                .map_err(|_| SidecarError::Time("timestamp out of range".to_owned()))?;
            if elapsed.subsec_nanos() == 0 {
                Ok((-seconds, 0))
            } else {
                Ok((-seconds - 1, 1_000_000_000 - elapsed.subsec_nanos()))
            }
        }
    }
}

/// Encodes the record from an explicit Unix instant. Kept public so the port
/// contract test can replay the Go-generated fixture cases byte-for-byte.
#[must_use]
pub fn encode_sidecar_metadata_at(vault_path: &str, seconds: i64, nanos: u32) -> String {
    let mut encoded = String::from("{\"vault_path\":");
    encode_go_json_string(vault_path, &mut encoded);
    encoded.push_str(",\"last_used\":");
    encode_go_json_string(&format_go_rfc3339_utc(seconds, nanos), &mut encoded);
    encoded.push('}');
    encoded
}

/// Renders `time.Time.MarshalJSON`'s UTC layout: RFC3339 whose fractional part
/// drops trailing zeros and disappears completely at a whole second.
fn format_go_rfc3339_utc(seconds: i64, nanos: u32) -> String {
    let instant = time::OffsetDateTime::from_unix_timestamp(seconds)
        .unwrap_or(time::OffsetDateTime::UNIX_EPOCH);
    let date = instant.date();
    let time_of_day = instant.time();
    let mut rendered = format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}",
        date.year(),
        u8::from(date.month()),
        date.day(),
        time_of_day.hour(),
        time_of_day.minute(),
        time_of_day.second()
    );
    if nanos > 0 {
        let fraction = format!("{nanos:09}");
        let trimmed = fraction.trim_end_matches('0');
        rendered.push('.');
        rendered.push_str(trimmed);
    }
    rendered.push('Z');
    rendered
}

/// Applies Go's `encoding/json` string escaping, including the HTML escapes
/// (`<`, `>`, `&`) and the line/paragraph separators that `serde_json` leaves
/// literal.
fn encode_go_json_string(value: &str, out: &mut String) {
    out.push('"');
    for character in value.chars() {
        match character {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            control if (control as u32) < 0x20 => {
                out.push_str(&format!("\\u{:04x}", control as u32));
            }
            other => out.push(other),
        }
    }
    out.push('"');
}
