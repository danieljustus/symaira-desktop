#![deny(unsafe_code)]

use std::collections::HashSet;

#[must_use]
pub fn extract_wikilinks(body: &str) -> Vec<String> {
    let clean = strip_code_blocks_and_spans(body);
    let bytes = clean.as_bytes();
    let mut result = Vec::new();
    let mut seen = HashSet::new();
    let mut index = 0;
    while index + 1 < bytes.len() {
        if bytes[index] == b'[' && bytes[index + 1] == b'[' {
            let content_start = index + 2;
            if let Some(offset) = clean[content_start..].find("]]") {
                let end = content_start + offset;
                let raw = &clean[content_start..end];
                let target = raw.split_once('|').map_or(raw, |(target, _)| target);
                let target = target
                    .split_once('#')
                    .map_or(target, |(target, _)| target)
                    .trim();
                if !target.is_empty() && seen.insert(target.to_owned()) {
                    result.push(target.to_owned());
                }
                index = end + 2;
                continue;
            }
        }
        index += 1;
    }
    result
}

fn strip_code_blocks_and_spans(body: &str) -> String {
    let bytes = body.as_bytes();
    let mut output = Vec::with_capacity(bytes.len());
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
                    output.push(b'\n');
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
                    output.push(b'\n');
                    continue;
                }
            }
        }
        if in_fence {
            index = skip_line(bytes, index);
            output.push(b'\n');
            continue;
        }

        index = line_start;
        while index < bytes.len() && bytes[index] != b'\n' {
            if bytes[index] == b'`' {
                let start = index;
                while index < bytes.len() && bytes[index] == b'`' {
                    index += 1;
                }
                let count = index - start;
                if let Some(close) = closing_backticks(bytes, index, count) {
                    index = close + count;
                    output.push(b' ');
                    continue;
                }
                output.extend(std::iter::repeat_n(b'`', count));
                continue;
            }
            output.push(bytes[index]);
            index += 1;
        }
        if index < bytes.len() {
            output.push(b'\n');
            index += 1;
        }
    }
    String::from_utf8_lossy(&output).into_owned()
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
