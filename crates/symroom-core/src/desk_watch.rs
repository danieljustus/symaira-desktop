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
        // encoding/json replaces invalid UTF-8 and unpaired surrogate escapes
        // inside strings with U+FFFD.
        let line = String::from_utf8_lossy(&line);
        let normalized;
        let line = if line.contains("\\uD") || line.contains("\\ud") {
            normalized = replace_unpaired_surrogates(&line);
            normalized.as_str()
        } else {
            line.as_ref()
        };
        let item = serde_json::from_str::<EventStreamItem>(line);
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

fn replace_unpaired_surrogates(input: &str) -> String {
    fn unit(bytes: &[u8], start: usize) -> Option<u16> {
        let digits = std::str::from_utf8(bytes.get(start..start + 4)?).ok()?;
        u16::from_str_radix(digits, 16).ok()
    }

    let bytes = input.as_bytes();
    let mut output = Vec::with_capacity(bytes.len());
    let mut index = 0;
    let mut in_string = false;
    while index < bytes.len() {
        match bytes[index] {
            b'"' => {
                in_string = !in_string;
                output.push(bytes[index]);
                index += 1;
            }
            b'\\' if in_string => {
                if bytes.get(index + 1) == Some(&b'u')
                    && let Some(value) = unit(bytes, index + 2)
                    && (0xd800..=0xdfff).contains(&value)
                {
                    let pair = (0xd800..=0xdbff).contains(&value)
                        && bytes.get(index + 6..index + 8) == Some(b"\\u")
                        && unit(bytes, index + 8)
                            .is_some_and(|next| (0xdc00..=0xdfff).contains(&next));
                    if pair {
                        output.extend_from_slice(&bytes[index..index + 12]);
                        index += 12;
                    } else {
                        output.extend_from_slice(b"\\uFFFD");
                        index += 6;
                    }
                } else if index + 1 < bytes.len() {
                    output.extend_from_slice(&bytes[index..index + 2]);
                    index += 2;
                } else {
                    output.push(bytes[index]);
                    index += 1;
                }
            }
            _ => {
                output.push(bytes[index]);
                index += 1;
            }
        }
    }
    String::from_utf8(output).expect("input was valid UTF-8")
}
