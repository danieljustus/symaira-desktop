//! Output normalization shared by the CLI-005/006/008 replays. It mirrors
//! `normalizeCLIErrors` in `cmd/symdesk/cli_errors_process_port_test.go` and
//! `remoteDateRE` in `cmd/symdesk/remote_command_process_port_test.go`.
#![allow(dead_code)] // each replay uses a subset

use std::path::Path;

fn is_digit(byte: Option<&u8>) -> bool {
    byte.is_some_and(u8::is_ascii_digit)
}

fn digits(bytes: &[u8], at: usize, count: usize) -> bool {
    (0..count).all(|offset| is_digit(bytes.get(at + offset)))
}

fn lower_hex(bytes: &[u8], at: usize, count: usize) -> bool {
    (0..count).all(|offset| {
        bytes
            .get(at + offset)
            .is_some_and(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(byte))
    })
}

/// Length of an RFC 3339 timestamp (`2026-01-02T03:04:05.123Z`) at `at`.
fn timestamp_len(bytes: &[u8], at: usize) -> Option<usize> {
    let shape = [4, b'-', 2, b'-', 2, b'T', 2, b':', 2, b':', 2];
    let mut cursor = at;
    for part in shape {
        if part > 4 {
            if bytes.get(cursor) != Some(&part) {
                return None;
            }
            cursor += 1;
        } else {
            if !digits(bytes, cursor, usize::from(part)) {
                return None;
            }
            cursor += usize::from(part);
        }
    }
    if bytes.get(cursor) == Some(&b'.') && is_digit(bytes.get(cursor + 1)) {
        cursor += 1;
        while is_digit(bytes.get(cursor)) {
            cursor += 1;
        }
    }
    match bytes.get(cursor) {
        Some(b'Z') => Some(cursor + 1 - at),
        Some(b'+' | b'-')
            if digits(bytes, cursor + 1, 2)
                && bytes.get(cursor + 3) == Some(&b':')
                && digits(bytes, cursor + 4, 2) =>
        {
            Some(cursor + 6 - at)
        }
        _ => None,
    }
}

/// `dddd-dd-dd.md` preceded by a non-word byte.
fn date_md_len(bytes: &[u8], at: usize) -> Option<usize> {
    let word = |byte: &u8| byte.is_ascii_alphanumeric() || *byte == b'_';
    if at > 0 && word(&bytes[at - 1]) {
        return None;
    }
    let matches = digits(bytes, at, 4)
        && bytes.get(at + 4) == Some(&b'-')
        && digits(bytes, at + 5, 2)
        && bytes.get(at + 7) == Some(&b'-')
        && digits(bytes, at + 8, 2)
        && bytes.get(at + 10..at + 13) == Some(b".md");
    matches.then_some(13)
}

/// Mirrors `portJournalToday`: the daily journal of the run's own UTC day has a
/// clock-dependent name; journals of other days keep theirs.
pub fn journal_key(relative: &str) -> String {
    let today = format!(
        ".symdesk/journal/{}.ndjson",
        time::OffsetDateTime::now_utc().date()
    );
    if relative == today {
        ".symdesk/journal/$DATE.ndjson".to_owned()
    } else {
        relative.to_owned()
    }
}

/// Replaces every token the Go recorder replaces with a stable marker.
pub fn normalize(text: &str, raw_root: &Path, canonical_root: &Path, with_date: bool) -> String {
    let mut text: String = text
        .split_inclusive('\n')
        .filter(|line| {
            !(line.starts_with("time=")
                && line.contains(
                    "level=INFO msg=\"seeded per-vault retrieval index from standalone store\"",
                ))
        })
        .collect();
    for root in [canonical_root, raw_root] {
        text = text.replace(&root.to_string_lossy().into_owned(), "$ROOT");
    }
    let bytes = text.as_bytes();
    let mut out = String::with_capacity(text.len());
    let mut at = 0;
    while at < bytes.len() {
        let rest = &text[at..];
        if let Some(length) = timestamp_len(bytes, at) {
            out.push_str("$TIME");
            at += length;
        } else if rest.starts_with("/vaults/") && lower_hex(bytes, at + 8, 16) {
            out.push_str("/vaults/$VAULT_ID");
            at += 8 + 16;
        } else if rest.starts_with("folder-") && lower_hex(bytes, at + 7, 64) {
            out.push_str("folder-$HASH");
            at += 7 + 64;
        } else if rest.starts_with("127.0.0.1:") && is_digit(bytes.get(at + 10)) {
            out.push_str("127.0.0.1:$PORT");
            at += 10;
            while is_digit(bytes.get(at)) {
                at += 1;
            }
        } else if let Some(length) = with_date.then(|| date_md_len(bytes, at)).flatten() {
            out.push_str("$DATE.md");
            at += length;
        } else {
            let character = rest.chars().next().expect("non-empty remainder");
            out.push(character);
            at += character.len_utf8();
        }
    }
    out
}
