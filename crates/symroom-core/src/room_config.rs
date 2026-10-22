//! Go `room.ReadRoomConfig`: the lenient `key = value` scanner for `room.toml`.
//!
//! Go does not parse TOML. It splits the file on `\n`, splits each line once on
//! `=`, trims the key and value (Unicode space) and strips every leading and
//! trailing `"` from the value. A line without `=`, an unknown key, a section
//! header or malformed TOML is ignored, a later duplicate key wins, and a
//! missing `id` is an empty string. Only a failure to read the file is an
//! error, reported as `read room.toml: <op> <path>: <strerror>`.

use std::path::Path;

use crate::goio;

/// The four keys Go's scanner keeps.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RoomConfig {
    pub id: String,
    pub created: String,
    pub root_pubkey: String,
    pub root_event: String,
}

/// Go `strings.TrimSpace`: trims Unicode `White_Space`.
fn trim_space(text: &str) -> &str {
    text.trim_matches(char::is_whitespace)
}

/// Scans `content` like Go's `ReadRoomConfig` (no I/O).
#[must_use]
pub fn parse(content: &str) -> RoomConfig {
    let mut config = RoomConfig::default();
    for line in content.split('\n') {
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        let value = trim_space(value).trim_matches('"').to_owned();
        match trim_space(key) {
            "id" => config.id = value,
            "created" => config.created = value,
            "root_pubkey" => config.root_pubkey = value,
            "root_event" => config.root_event = value,
            _ => {}
        }
    }
    config
}

/// Go `room.ReadRoomConfig`. The error text is already `read room.toml: ...`.
///
/// # Errors
/// Returns the Go-formatted read error when `room.toml` cannot be read.
pub fn read(room_dir: &Path) -> Result<RoomConfig, String> {
    // Go reads bytes, so invalid UTF-8 is not an error.
    goio::read_file(&room_dir.join("room.toml"))
        .map(|bytes| parse(&String::from_utf8_lossy(&bytes)))
        .map_err(|error| format!("read room.toml: {error}"))
}

#[cfg(test)]
mod tests {
    use super::{RoomConfig, parse};

    #[test]
    fn malformed_and_id_less_files_are_not_errors() {
        assert_eq!(parse("this is not toml at all\n"), RoomConfig::default());
        assert_eq!(parse("[[[ =\n"), RoomConfig::default());
        let config = parse("created = \"2026-09-23T10:00:00.000Z\"\n");
        assert_eq!(config.id, "");
        assert_eq!(config.created, "2026-09-23T10:00:00.000Z");
    }

    #[test]
    fn last_duplicate_wins_and_values_keep_inner_text() {
        let config = parse("id = \"rm_a\"\n[x]\n  id\t= \"\"rm_b # c\"\"  \r\nroot_event=ev_1=2\n");
        assert_eq!(config.id, "rm_b # c");
        assert_eq!(config.root_event, "ev_1=2");
    }
}
