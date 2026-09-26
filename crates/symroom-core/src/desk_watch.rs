//! `symdesk events` line parser used by SymRoom watch.

use std::io::BufRead;

use serde::Deserialize;

use crate::journal::read_scanner_line;

#[derive(Debug, Clone, Default, Deserialize, Eq, PartialEq)]
#[serde(default)]
pub struct EventStreamItem {
    pub event: String,
    pub path: String,
}

/// Replays Go `desk.WatchStream`: skip blank/malformed/pathless lines and stop
/// at the first handler, cancellation or Scanner error.
///
/// # Errors
/// Returns the Go-compatible error message from the handler, cancellation or
/// line scanner.
pub fn watch_stream(
    reader: &mut impl BufRead,
    cancelled: impl Fn() -> bool,
    mut handler: impl FnMut(&EventStreamItem) -> Result<(), String>,
) -> Result<(), String> {
    while let Some(line) = read_scanner_line(reader).map_err(|error| error.to_string())? {
        if cancelled() {
            return Err("context canceled".to_owned());
        }
        if line.is_empty() {
            continue;
        }
        // encoding/json replaces invalid UTF-8 inside strings with U+FFFD.
        let item = match std::str::from_utf8(&line) {
            Ok(line) => serde_json::from_str::<EventStreamItem>(line),
            Err(_) => serde_json::from_str::<EventStreamItem>(&String::from_utf8_lossy(&line)),
        };
        let Ok(item) = item else {
            continue;
        };
        if item.path.is_empty() {
            continue;
        }
        handler(&item)?;
    }
    Ok(())
}
