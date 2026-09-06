#![deny(unsafe_code)]

use std::collections::HashSet;

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct TagSpan {
    pub start: usize,
    pub end: usize,
    pub tag: String,
}

#[must_use]
pub fn extract_inline_tags(body: &str) -> Vec<String> {
    let mut seen = HashSet::new();
    let mut result = Vec::new();
    for span in find_inline_tag_spans(body) {
        let lower = span.tag.to_lowercase();
        if seen.insert(lower) {
            result.push(span.tag);
        }
    }
    result
}

#[must_use]
pub fn find_inline_tag_spans(body: &str) -> Vec<TagSpan> {
    let bytes = body.as_bytes();
    let mut spans = Vec::new();
    let mut index = 0;
    let mut in_fence = false;
    let mut fence_character = 0_u8;
    let mut fence_length = 0;
    while index < bytes.len() {
        let line_start = index;
        let mut temporary = index;
        let mut spaces = 0;
        while temporary < bytes.len() && matches!(bytes[temporary], b' ' | b'\t') && spaces < 3 {
            spaces += if bytes[temporary] == b' ' { 1 } else { 4 };
            temporary += 1;
        }
        if temporary < bytes.len() && matches!(bytes[temporary], b'`' | b'~') {
            let character = bytes[temporary];
            let mut count = 0;
            while temporary < bytes.len() && bytes[temporary] == character {
                count += 1;
                temporary += 1;
            }
            if count >= 3 {
                if !in_fence {
                    in_fence = true;
                    fence_character = character;
                    fence_length = count;
                    index = skip_line(bytes, temporary);
                    continue;
                }
                if character == fence_character
                    && count >= fence_length
                    && bytes[temporary..line_end(bytes, temporary)]
                        .iter()
                        .all(|byte| matches!(byte, b' ' | b'\t' | b'\r'))
                {
                    in_fence = false;
                    index = skip_line(bytes, temporary);
                    continue;
                }
            }
        }
        if in_fence {
            index = skip_line(bytes, index);
            continue;
        }

        index = line_start;
        temporary = index;
        spaces = 0;
        while temporary < bytes.len() && bytes[temporary] == b' ' && spaces < 3 {
            spaces += 1;
            temporary += 1;
        }
        let mut hashes = 0;
        while temporary < bytes.len() && bytes[temporary] == b'#' {
            hashes += 1;
            temporary += 1;
        }
        if (1..=6).contains(&hashes)
            && (temporary == bytes.len()
                || matches!(bytes[temporary], b' ' | b'\t' | b'\r' | b'\n'))
        {
            index = temporary;
            while index < bytes.len() && matches!(bytes[index], b' ' | b'\t') {
                index += 1;
            }
        }

        while index < bytes.len() && bytes[index] != b'\n' {
            if bytes[index] == b'`' {
                let start = index;
                while index < bytes.len() && bytes[index] == b'`' {
                    index += 1;
                }
                let count = index - start;
                if let Some(close) = closing_backticks(bytes, index, count) {
                    index = close + count;
                }
                continue;
            }
            if index + 1 < bytes.len() && bytes[index..].starts_with(b"[[") {
                if let Some(offset) = find_bytes(&bytes[index + 2..], b"]]") {
                    index += 2 + offset + 2;
                } else {
                    index += 2;
                }
                continue;
            }
            if index + 1 < bytes.len() && bytes[index..].starts_with(b"](") {
                if let Some(offset) = bytes[index + 2..].iter().position(|byte| *byte == b')') {
                    index += 2 + offset + 1;
                } else {
                    index += 2;
                }
                continue;
            }
            if bytes[index] == b'<'
                && ["http://", "https://", "mailto:", "ftp://"]
                    .iter()
                    .any(|prefix| starts_ascii_case_insensitive(&bytes[index + 1..], prefix))
                && let Some(offset) = bytes[index + 1..].iter().position(|byte| *byte == b'>')
            {
                index += 1 + offset + 1;
                continue;
            }
            if ["http://", "https://", "ftp://", "file://"]
                .iter()
                .any(|prefix| starts_ascii_case_insensitive(&bytes[index..], prefix))
            {
                while index < bytes.len() && !url_terminator(bytes[index]) {
                    index += 1;
                }
                continue;
            }
            if bytes[index] == b'#' {
                let preceding = index == 0 || valid_preceding(bytes[index - 1]);
                if !preceding
                    || index + 1 >= bytes.len()
                    || matches!(bytes[index + 1], b' ' | b'\t' | b'\r' | b'\n' | b'#' | b'/')
                    || punctuation(bytes[index + 1])
                {
                    index += 1;
                    continue;
                }
                let tag_start = index + 1;
                let mut tag_end = tag_start;
                while tag_end < bytes.len() && tag_character(bytes[tag_end]) {
                    tag_end += 1;
                }
                let mut raw_end = tag_end;
                if let Some(offset) = find_bytes(&bytes[tag_start..tag_end], b"//") {
                    raw_end = tag_start + offset;
                }
                while raw_end > tag_start && matches!(bytes[raw_end - 1], b'/' | b'-' | b'_') {
                    raw_end -= 1;
                }
                let raw = &body[tag_start..raw_end];
                if raw.is_empty() || digits_only(raw.as_bytes()) {
                    index += 1;
                    continue;
                }
                spans.push(TagSpan {
                    start: index,
                    end: raw_end,
                    tag: raw.to_owned(),
                });
                index = raw_end;
                continue;
            }
            index += 1;
        }
        if index < bytes.len() {
            index += 1;
        }
    }
    spans
}

fn valid_preceding(value: u8) -> bool {
    matches!(
        value,
        b' ' | b'\t'
            | b'\n'
            | b'\r'
            | b'('
            | b'['
            | b'{'
            | b'<'
            | b'"'
            | b'\''
            | b'`'
            | b','
            | b';'
            | b'*'
            | b'_'
            | b'~'
    )
}

fn punctuation(value: u8) -> bool {
    matches!(
        value,
        b'.' | b',' | b';' | b':' | b'!' | b'?' | b')' | b']' | b'}' | b'>' | b'"' | b'\''
    )
}

fn tag_character(value: u8) -> bool {
    value.is_ascii_alphanumeric() || matches!(value, b'_' | b'-' | b'/')
}

fn digits_only(value: &[u8]) -> bool {
    !value
        .iter()
        .any(|byte| byte.is_ascii_alphabetic() || matches!(byte, b'_' | b'-'))
}

fn url_terminator(value: u8) -> bool {
    matches!(
        value,
        b' ' | b'\t' | b'\r' | b'\n' | b'<' | b'>' | b'"' | b'\'' | b')' | b']'
    )
}

fn starts_ascii_case_insensitive(value: &[u8], prefix: &str) -> bool {
    value
        .get(..prefix.len())
        .is_some_and(|candidate| candidate.eq_ignore_ascii_case(prefix.as_bytes()))
}

fn find_bytes(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}

fn closing_backticks(bytes: &[u8], mut index: usize, count: usize) -> Option<usize> {
    while index < bytes.len() && bytes[index] != b'\n' {
        if bytes[index] == b'`' {
            let start = index;
            while index < bytes.len() && bytes[index] == b'`' {
                index += 1;
            }
            if index - start == count {
                return Some(start);
            }
        } else {
            index += 1;
        }
    }
    None
}

fn line_end(bytes: &[u8], start: usize) -> usize {
    bytes[start..]
        .iter()
        .position(|byte| *byte == b'\n')
        .map_or(bytes.len(), |offset| start + offset)
}

fn skip_line(bytes: &[u8], start: usize) -> usize {
    let end = line_end(bytes, start);
    if end < bytes.len() { end + 1 } else { end }
}
