//! Normalization shared by the RUST-013 ingest-bodies replay: the Go
//! recorder's `normalizeBodiesText(normalizeCLIErrors(..))` plus the TOML
//! error rule of `ingest_bodies_process_port_test.go`.
use std::path::Path;

use super::cli_normalize;

fn is_hex(byte: u8) -> bool {
    byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte)
}

/// Go: `regexp.MustCompile("[0-9a-f]{24}")` replaced by `$RAND24` (leftmost,
/// non-overlapping 24-digit windows of every lowercase hex run).
pub fn normalize_random(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = String::with_capacity(text.len());
    let mut at = 0;
    while at < bytes.len() {
        if is_hex(bytes[at]) {
            let mut end = at;
            while end < bytes.len() && is_hex(bytes[end]) {
                end += 1;
            }
            let length = end - at;
            for _ in 0..length / 24 {
                out.push_str("$RAND24");
            }
            out.push_str(&text[at + (length / 24) * 24..end]);
            at = end;
        } else {
            let character = text[at..].chars().next().expect("non-empty remainder");
            out.push(character);
            at += character.len_utf8();
        }
    }
    out
}

/// Replaces every match of a fixed digit/literal shape (digit-run widths
/// are the values <= 4, literals the rest) like the Go regexp would.
fn normalize_shape(text: &str, shape: &[u8], replacement: &str, fraction_and_zone: bool) -> String {
    let bytes = text.as_bytes();
    let matches_at = |at: usize| {
        let mut cursor = at;
        for &part in shape {
            if part > 4 {
                if bytes.get(cursor) != Some(&part) {
                    return None;
                }
                cursor += 1;
            } else {
                let width = usize::from(part);
                if !(0..width)
                    .all(|offset| bytes.get(cursor + offset).is_some_and(u8::is_ascii_digit))
                {
                    return None;
                }
                cursor += width;
            }
        }
        if fraction_and_zone {
            if bytes.get(cursor) == Some(&b'.') {
                let digits = bytes[cursor + 1..]
                    .iter()
                    .take_while(|byte| byte.is_ascii_digit())
                    .count();
                if digits == 0 {
                    return None;
                }
                cursor += 1 + digits;
            }
            if bytes.get(cursor) != Some(&b'Z') {
                return None;
            }
            cursor += 1;
        }
        Some(cursor - at)
    };
    let mut out = String::with_capacity(text.len());
    let mut at = 0;
    while at < bytes.len() {
        if let Some(length) = matches_at(at) {
            out.push_str(replacement);
            at += length;
        } else {
            let character = text[at..].chars().next().expect("non-empty remainder");
            out.push(character);
            at += character.len_utf8();
        }
    }
    out
}

/// Go: `\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}` replaced by `$LOCALTIME`.
pub fn normalize_local_time(text: &str) -> String {
    normalize_shape(
        text,
        &[4, b'-', 2, b'-', 2, b' ', 2, b':', 2, b':', 2],
        "$LOCALTIME",
        false,
    )
}

/// Go: `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z` replaced by `$RFC3339`.
pub fn normalize_rfc3339(text: &str) -> String {
    normalize_shape(
        text,
        &[4, b'-', 2, b'-', 2, b'T', 2, b':', 2, b':', 2],
        "$RFC3339",
        true,
    )
}

/// Go `regexp.MustCompile("toml: line \\d+[^\\n]*")` replaced by
/// `toml: line $TOML`: the TOML parsers word their syntax errors differently
/// (declared limit), only the failing line is compared.
pub fn normalize_toml(text: &str) -> String {
    let marker = "toml: line ";
    let mut out = String::with_capacity(text.len());
    let mut rest = text;
    while let Some(at) = rest.find(marker) {
        let after = &rest[at + marker.len()..];
        let digits = after.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 {
            out.push_str(&rest[..at + marker.len()]);
            rest = after;
            continue;
        }
        out.push_str(&rest[..at]);
        out.push_str("toml: line $TOML");
        let tail = &after[digits..];
        rest = tail.find('\n').map_or("", |newline| &tail[newline..]);
    }
    out.push_str(rest);
    out
}

/// Go `(?m)^(symmeet_published_facts: \[)(.*)(\])$`: the ledger hash order
/// comes from Go map iteration, so the items are compared sorted.
pub fn sort_published_facts(text: &str) -> String {
    let mut out = String::with_capacity(text.len());
    for (index, line) in text.split('\n').enumerate() {
        if index > 0 {
            out.push('\n');
        }
        let sorted = line
            .strip_prefix("symmeet_published_facts: [")
            .and_then(|rest| rest.strip_suffix(']'))
            .map(|items| {
                let mut items: Vec<&str> = items.split(", ").collect();
                items.sort_unstable();
                format!("symmeet_published_facts: [{}]", items.join(", "))
            });
        out.push_str(sorted.as_deref().unwrap_or(line));
    }
    out
}

/// Go `\$LOCALTIME\.\d+` replaced by `$LOCALTIME`.
pub fn normalize_local_fraction(text: &str) -> String {
    let marker = "$LOCALTIME.";
    let mut out = String::with_capacity(text.len());
    let mut rest = text;
    while let Some(at) = rest.find(marker) {
        let after = &rest[at + marker.len()..];
        let digits = after.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 {
            out.push_str(&rest[..at + marker.len()]);
            rest = after;
            continue;
        }
        out.push_str(&rest[..at]);
        out.push_str("$LOCALTIME");
        rest = &after[digits..];
    }
    out.push_str(rest);
    out
}

/// The recorder's `normalizeIngestText`.
pub fn normalize(text: &str, root: &Path, canonical: &Path) -> String {
    let text = sort_published_facts(&cli_normalize::normalize(text, root, canonical, false));
    normalize_toml(&normalize_local_fraction(&normalize_rfc3339(
        &normalize_local_time(&normalize_random(&text)),
    )))
}
