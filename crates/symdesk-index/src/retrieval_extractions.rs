//! Extraction-sidecar import performed by Go's retrieval engine while it
//! indexes a Markdown document (`internal/retrieval/internal/engine/
//! extractions.go`).
//!
//! The engine looks at the text it is handed -- for `symdesk index` the
//! document *body*, after the vault parser stripped the document's own
//! frontmatter -- and, when that text starts with a frontmatter block holding a
//! `sha256` key, imports `.symaira/extractions/<sha256>.jsonl` found by walking
//! up from the document's directory. The import replaces the document's
//! `extractions` rows with the sidecar's lines. Every observable detail below
//! (the regular expressions, the nearest-directory search, the `bufio.Scanner`
//! line limit, `encoding/json` error wording, the chunk-linking arithmetic)
//! follows the Go source and is replayed against the Go process fixture.

use std::{
    fs,
    path::{Path, PathBuf},
    sync::OnceLock,
};

use regex::Regex;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::{
    SidecarError,
    retrieval::{ExtractionRow, RetrievalDb},
};

/// Vault-relative directory the sidecars are written to.
const SIDECAR_DIR: &str = ".symaira/extractions";
/// `bufio.Scanner`'s maximum token size configured by Go's importer.
const MAX_LINE_BYTES: usize = 1 << 20;
const PRODUCER: &str = "symingest/annotate";

/// Go's `\s` is `[\t\n\f\r ]`, narrower than the regex crate's Unicode `\s`.
const GO_SPACE: &str = r"[\t\n\f\r ]";

fn block_regex() -> &'static Regex {
    static BLOCK: OnceLock<Regex> = OnceLock::new();
    BLOCK.get_or_init(|| {
        Regex::new(&format!(
            r"(?s)^---{GO_SPACE}*\n(.*?)\n---{GO_SPACE}*(\n|$)"
        ))
        .expect("frontmatter block pattern")
    })
}

fn sha256_regex() -> &'static Regex {
    static SHA: OnceLock<Regex> = OnceLock::new();
    SHA.get_or_init(|| {
        Regex::new(&format!(
            r#"(?m)^sha256:{GO_SPACE}*"?([0-9a-fA-F]{{64}})"?{GO_SPACE}*$"#
        ))
        .expect("frontmatter sha256 pattern")
    })
}

/// Go's `frontmatterSHA256`: the lower-cased `sha256` value of the frontmatter
/// block that starts `content`.
fn frontmatter_sha256(content: &str) -> Option<String> {
    let block = block_regex().captures(content)?;
    let digest = sha256_regex().captures(block.get(1)?.as_str())?;
    Some(digest[1].to_ascii_lowercase())
}

/// Go's `findSidecarPath`: walks up from the document's directory, without
/// stopping at the vault root, to the first directory holding a regular file
/// `.symaira/extractions/<sha256>.jsonl`.
fn find_sidecar_path(doc_path: &str, sha256: &str) -> Option<PathBuf> {
    let mut directory = Some(Path::new(doc_path).parent().unwrap_or(Path::new("")));
    while let Some(dir) = directory {
        let candidate = dir.join(SIDECAR_DIR).join(format!("{sha256}.jsonl"));
        if fs::metadata(&candidate).is_ok_and(|metadata| !metadata.is_dir()) {
            return Some(candidate);
        }
        directory = dir.parent();
    }
    None
}

/// Go's `detectSidecarPath`: `None` unless the path has a (case-insensitive)
/// `.md` extension, `content` starts with a frontmatter block carrying a
/// `sha256`, and a matching sidecar file exists.
#[must_use]
pub fn detect_sidecar_path(path: &str, content: &str) -> Option<PathBuf> {
    if !go_extension(path).eq_ignore_ascii_case(".md") {
        return None;
    }
    find_sidecar_path(path, &frontmatter_sha256(content)?)
}

/// `filepath.Ext`: the suffix from the final dot of the last path element.
fn go_extension(path: &str) -> &str {
    for (index, byte) in path.bytes().enumerate().rev() {
        if byte == b'/' || (cfg!(windows) && byte == b'\\') {
            break;
        }
        if byte == b'.' {
            return &path[index..];
        }
    }
    ""
}

/// Go's `ImportExtractionSidecar`: replaces the extraction rows of `doc_path`
/// with the lines of the sidecar at `sidecar`. A malformed sidecar leaves the
/// rows untouched; the error text is what Go prints after the "Warning:
/// failed to import extraction sidecar ..." prefix.
///
/// # Errors
/// Returns Go's error wording for an unreadable sidecar, an over-long line or
/// an invalid JSON line.
pub fn import_extraction_sidecar(
    database: &RetrievalDb,
    doc_path: &str,
    sidecar: &Path,
) -> Result<(), String> {
    let shown = sidecar.to_string_lossy();
    let bytes = fs::read(sidecar).map_err(|error| {
        format!(
            "open sidecar {shown}: open {shown}: {}",
            go_io_error_text(&error)
        )
    })?;
    let chunks = database
        .chunk_spans_for_document(doc_path)
        .map_err(|error| format!("load chunk spans for {doc_path}: {error}"))?;

    let now = OffsetDateTime::now_utc()
        .format(&Rfc3339)
        .unwrap_or_default();
    let mut rows = Vec::new();
    for (index, line) in scan_lines(&bytes).enumerate() {
        let line_number = index + 1;
        let line = line.map_err(|reason| format!("read sidecar {shown}: {reason}"))?;
        let line = String::from_utf8_lossy(line);
        let line = line.trim();
        if line.is_empty() {
            continue;
        }
        let extraction = decode_line(line)
            .map_err(|reason| format!("{shown}: line {line_number}: invalid JSON: {reason}"))?;
        let class = if extraction.field.is_empty() {
            extraction.kind
        } else {
            extraction.field
        };
        let mut row = ExtractionRow {
            document_path: doc_path.to_owned(),
            chunk_id: None,
            class,
            value: extraction.value,
            evidence_text: String::new(),
            span_start: None,
            span_end: None,
            matched: extraction.matched,
            producer: PRODUCER.to_owned(),
            source_ref: shown.clone().into_owned(),
            created_at: now.clone(),
        };
        if let Some(span) = extraction.span {
            row.span_start = Some(span.start);
            row.span_end = Some(span.end);
            row.evidence_text = span.snippet;
            row.chunk_id = best_matching_chunk_id(&chunks, span.start, span.end);
        }
        rows.push(row);
    }
    database
        .replace_extractions(doc_path, &rows)
        .map_err(|error: SidecarError| error.to_string())
}

/// The part of Go's `*os.PathError` text after the second colon.
fn go_io_error_text(error: &std::io::Error) -> String {
    match error.kind() {
        std::io::ErrorKind::NotFound => "no such file or directory".to_owned(),
        std::io::ErrorKind::PermissionDenied => "permission denied".to_owned(),
        _ => error.to_string(),
    }
}

/// `bufio.ScanLines` over a 1 MiB buffer: lines end at `\n`, lose one
/// trailing `\r`, and a line that does not fit the buffer ends the scan with
/// `bufio.Scanner: token too long`.
fn scan_lines(bytes: &[u8]) -> impl Iterator<Item = Result<&[u8], &'static str>> {
    let mut rest = bytes;
    let mut failed = false;
    std::iter::from_fn(move || {
        if failed || rest.is_empty() {
            return None;
        }
        let end = rest.iter().position(|byte| *byte == b'\n');
        let length = end.unwrap_or(rest.len());
        if length >= MAX_LINE_BYTES {
            failed = true;
            return Some(Err("bufio.Scanner: token too long"));
        }
        let mut line = &rest[..length];
        rest = end.map_or(&[][..], |position| &rest[position + 1..]);
        if line.last() == Some(&b'\r') {
            line = &line[..line.len() - 1];
        }
        Some(Ok(line))
    })
}

/// Go's `bestMatchingChunkID`: the first chunk whose span contains the span
/// midpoint, else the chunk with the nearest edge. Go's `int` arithmetic wraps.
fn best_matching_chunk_id(
    chunks: &[(i64, Option<i64>, Option<i64>)],
    span_start: i64,
    span_end: i64,
) -> Option<i64> {
    let mid = span_start.wrapping_add(span_end) / 2;
    let mut best: Option<i64> = None;
    let mut best_distance: i64 = -1;
    for (id, start, end) in chunks {
        let (Some(start), Some(end)) = (start, end) else {
            continue;
        };
        if mid >= *start && mid < *end {
            return Some(*id);
        }
        let distance = if mid < *start {
            start.wrapping_sub(mid)
        } else {
            mid.wrapping_sub(*end)
        };
        if best_distance == -1 || distance < best_distance {
            best_distance = distance;
            best = Some(*id);
        }
    }
    best
}

// ---------------------------------------------------------------------------
// encoding/json: Unmarshal of one line into Go's sidecarExtraction

#[derive(Default)]
struct Span {
    start: i64,
    end: i64,
    snippet: String,
}

#[derive(Default)]
struct DecodedExtraction {
    field: String,
    kind: String,
    value: String,
    span: Option<Span>,
    matched: bool,
}

const MAX_NESTING_DEPTH: usize = 10_000;

enum Json {
    Null,
    Bool(bool),
    Num(String),
    Str(String),
    Arr,
    Obj(Vec<(String, Json)>),
}

impl Drop for Json {
    /// Iterative: dropping a 10 000-deep object chain must not recurse.
    fn drop(&mut self) {
        let mut stack = Vec::new();
        if let Self::Obj(entries) = self {
            stack.extend(std::mem::take(entries).into_iter().map(|entry| entry.1));
        }
        while let Some(mut value) = stack.pop() {
            if let Self::Obj(entries) = &mut value {
                stack.extend(std::mem::take(entries).into_iter().map(|entry| entry.1));
            }
        }
    }
}

impl Json {
    fn kind(&self) -> &'static str {
        match self {
            Self::Null => "null",
            Self::Bool(_) => "bool",
            Self::Num(_) => "number",
            Self::Str(_) => "string",
            Self::Arr => "array",
            Self::Obj(_) => "object",
        }
    }
}

/// `json.Unmarshal(line, &sidecarExtraction{})`: the scanner's syntax errors
/// first (over the whole line), then the first type mismatch.
fn decode_line(line: &str) -> Result<DecodedExtraction, String> {
    let value = Scanner::new(line.as_bytes()).document()?;
    decode_extraction(&value)
}

struct Scanner<'a> {
    data: &'a [u8],
    pos: usize,
}

enum Frame {
    Obj(Vec<(String, Json)>, Option<String>),
    Arr,
}

const EOF_TEXT: &str = "unexpected end of JSON input";

fn is_space(byte: u8) -> bool {
    matches!(byte, b' ' | b'\t' | b'\r' | b'\n')
}

/// Go's `quoteChar`: the scanner byte as a Latin-1 rune, `strconv.Quote`d.
fn quote_byte(byte: u8) -> String {
    match byte {
        b'\'' => "'\\''".to_owned(),
        b'"' => "'\"'".to_owned(),
        b'\\' => "'\\\\'".to_owned(),
        0x07 => "'\\a'".to_owned(),
        0x08 => "'\\b'".to_owned(),
        0x0c => "'\\f'".to_owned(),
        b'\n' => "'\\n'".to_owned(),
        b'\r' => "'\\r'".to_owned(),
        b'\t' => "'\\t'".to_owned(),
        0x0b => "'\\v'".to_owned(),
        0x20..=0x7e => format!("'{}'", char::from(byte)),
        // Latin-1 supplement: printable except U+0080..U+00A0 and U+00AD.
        0xa1..=0xff if byte != 0xad => format!("'{}'", char::from(byte)),
        0x80.. => format!("'\\u{byte:04x}'"),
        _ => format!("'\\x{byte:02x}'"),
    }
}

impl<'a> Scanner<'a> {
    fn new(data: &'a [u8]) -> Self {
        Self { data, pos: 0 }
    }

    fn syntax(byte: u8, context: &str) -> String {
        format!("invalid character {} {context}", quote_byte(byte))
    }

    fn next(&mut self) -> Result<u8, String> {
        let byte = self
            .data
            .get(self.pos)
            .copied()
            .ok_or_else(|| EOF_TEXT.to_owned())?;
        self.pos += 1;
        Ok(byte)
    }

    fn next_non_space(&mut self) -> Result<u8, String> {
        loop {
            let byte = self.next()?;
            if !is_space(byte) {
                return Ok(byte);
            }
        }
    }

    /// One complete value followed only by whitespace (`checkValid`).
    fn document(&mut self) -> Result<Json, String> {
        let value = self.first_value()?;
        while let Some(byte) = self.data.get(self.pos).copied() {
            self.pos += 1;
            if !is_space(byte) {
                return Err(Self::syntax(byte, "after top-level value"));
            }
        }
        Ok(value)
    }

    fn first_value(&mut self) -> Result<Json, String> {
        let mut stack: Vec<Frame> = Vec::new();
        'outer: loop {
            let byte = self.next_non_space()?;
            let mut completed = match byte {
                b'{' => {
                    if stack.len() + 1 > MAX_NESTING_DEPTH {
                        return Err(Self::syntax(byte, "exceeded max depth"));
                    }
                    let first = self.next_non_space()?;
                    if first == b'}' {
                        Json::Obj(Vec::new())
                    } else {
                        let key = self.read_key(first)?;
                        stack.push(Frame::Obj(Vec::new(), Some(key)));
                        continue 'outer;
                    }
                }
                b'[' => {
                    if stack.len() + 1 > MAX_NESTING_DEPTH {
                        return Err(Self::syntax(byte, "exceeded max depth"));
                    }
                    self.skip_space();
                    if self.data.get(self.pos) == Some(&b']') {
                        self.pos += 1;
                        Json::Arr
                    } else {
                        if self.pos >= self.data.len() {
                            return Err(EOF_TEXT.to_owned());
                        }
                        stack.push(Frame::Arr);
                        continue 'outer;
                    }
                }
                b'"' => Json::Str(self.string_body()?),
                b'-' | b'0'..=b'9' => self.number(byte)?,
                b't' => self.literal("true", Json::Bool(true))?,
                b'f' => self.literal("false", Json::Bool(false))?,
                b'n' => self.literal("null", Json::Null)?,
                _ => return Err(Self::syntax(byte, "looking for beginning of value")),
            };
            loop {
                match stack.last_mut() {
                    None => return Ok(completed),
                    Some(Frame::Obj(entries, key)) => {
                        entries.push((key.take().unwrap_or_default(), completed));
                        let byte = self.next_non_space()?;
                        match byte {
                            b',' => {
                                let first = self.next_non_space()?;
                                *key = Some(self.read_key(first)?);
                                continue 'outer;
                            }
                            b'}' => {
                                let Some(Frame::Obj(entries, _)) = stack.pop() else {
                                    unreachable!("object frame on top of the stack")
                                };
                                completed = Json::Obj(entries);
                            }
                            _ => return Err(Self::syntax(byte, "after object key:value pair")),
                        }
                    }
                    Some(Frame::Arr) => {
                        let byte = self.next_non_space()?;
                        match byte {
                            b',' => continue 'outer,
                            b']' => {
                                stack.pop();
                                completed = Json::Arr;
                            }
                            _ => return Err(Self::syntax(byte, "after array element")),
                        }
                    }
                }
            }
        }
    }

    fn skip_space(&mut self) {
        while self.data.get(self.pos).is_some_and(|byte| is_space(*byte)) {
            self.pos += 1;
        }
    }

    /// An object key whose first non-space byte `first` was consumed, then the
    /// colon.
    fn read_key(&mut self, first: u8) -> Result<String, String> {
        if first != b'"' {
            return Err(Self::syntax(
                first,
                "looking for beginning of object key string",
            ));
        }
        let key = self.string_body()?;
        let colon = self.next_non_space()?;
        if colon != b':' {
            return Err(Self::syntax(colon, "after object key"));
        }
        Ok(key)
    }

    /// A string after its opening quote, unquoted like `json.unquote`.
    fn string_body(&mut self) -> Result<String, String> {
        let mut raw: Vec<u8> = Vec::new();
        loop {
            let byte = self.next()?;
            match byte {
                b'"' => break,
                b'\\' => {
                    let escape = self.next()?;
                    match escape {
                        b'b' => raw.push(8),
                        b'f' => raw.push(12),
                        b'n' => raw.push(b'\n'),
                        b'r' => raw.push(b'\r'),
                        b't' => raw.push(b'\t'),
                        b'\\' | b'/' | b'"' => raw.push(escape),
                        b'u' => {
                            let rune = self.unicode_escape()?;
                            let mut buffer = [0u8; 4];
                            raw.extend_from_slice(rune.encode_utf8(&mut buffer).as_bytes());
                        }
                        _ => return Err(Self::syntax(escape, "in string escape code")),
                    }
                }
                byte if byte < 0x20 => return Err(Self::syntax(byte, "in string literal")),
                byte => raw.push(byte),
            }
        }
        Ok(String::from_utf8_lossy(&raw).into_owned())
    }

    /// The code point of a `\uXXXX` escape (the `\u` already consumed), joining
    /// a following low-surrogate escape and replacing unpaired surrogates.
    fn unicode_escape(&mut self) -> Result<char, String> {
        let first = self.hex4()?;
        if (0xD800..0xDC00).contains(&first) {
            let saved = self.pos;
            if self.data.get(self.pos) == Some(&b'\\') && self.data.get(self.pos + 1) == Some(&b'u')
            {
                self.pos += 2;
                let second = self.hex4()?;
                if (0xDC00..0xE000).contains(&second) {
                    let combined = 0x10000 + ((first - 0xD800) << 10) + (second - 0xDC00);
                    return Ok(char::from_u32(combined).unwrap_or('\u{FFFD}'));
                }
                self.pos = saved;
            }
            return Ok('\u{FFFD}');
        }
        if (0xDC00..0xE000).contains(&first) {
            return Ok('\u{FFFD}');
        }
        Ok(char::from_u32(first).unwrap_or('\u{FFFD}'))
    }

    fn hex4(&mut self) -> Result<u32, String> {
        let mut value = 0u32;
        for _ in 0..4 {
            let byte = self.next()?;
            let digit = match byte {
                b'0'..=b'9' => byte - b'0',
                b'a'..=b'f' => byte - b'a' + 10,
                b'A'..=b'F' => byte - b'A' + 10,
                _ => return Err(Self::syntax(byte, "in \\u hexadecimal character escape")),
            };
            value = value * 16 + u32::from(digit);
        }
        Ok(value)
    }

    fn digits(&mut self) {
        while self.data.get(self.pos).is_some_and(u8::is_ascii_digit) {
            self.pos += 1;
        }
    }

    fn number(&mut self, first: u8) -> Result<Json, String> {
        let start = self.pos - 1;
        let mut byte = first;
        if byte == b'-' {
            byte = self.next()?;
            if !byte.is_ascii_digit() {
                return Err(Self::syntax(byte, "in numeric literal"));
            }
        }
        if byte != b'0' {
            self.digits();
        }
        if self.data.get(self.pos) == Some(&b'.') {
            self.pos += 1;
            let digit = self.next()?;
            if !digit.is_ascii_digit() {
                return Err(Self::syntax(
                    digit,
                    "after decimal point in numeric literal",
                ));
            }
            self.digits();
        }
        if matches!(self.data.get(self.pos), Some(b'e' | b'E')) {
            self.pos += 1;
            let mut digit = self.next()?;
            if digit == b'+' || digit == b'-' {
                digit = self.next()?;
            }
            if !digit.is_ascii_digit() {
                return Err(Self::syntax(digit, "in exponent of numeric literal"));
            }
            self.digits();
        }
        Ok(Json::Num(
            String::from_utf8_lossy(&self.data[start..self.pos]).into_owned(),
        ))
    }

    fn literal(&mut self, word: &'static str, value: Json) -> Result<Json, String> {
        for expected in word.bytes().skip(1) {
            let byte = self.next()?;
            if byte != expected {
                return Err(Self::syntax(
                    byte,
                    &format!("in literal {word} (expecting {})", quote_byte(expected)),
                ));
            }
        }
        Ok(value)
    }
}

/// Go's case-insensitive key folding (`foldName`): ASCII upper-casing plus the
/// two non-ASCII code points that fold into ASCII letters.
fn fold_key(key: &str) -> String {
    key.chars()
        .map(|c| match c {
            '\u{212A}' => 'K',
            '\u{17F}' => 'S',
            c => c.to_ascii_uppercase(),
        })
        .collect()
}

/// Struct field lookup: the exact JSON name first, then the folded name.
fn lookup(names: &[&'static str], key: &str) -> Option<&'static str> {
    if let Some(name) = names.iter().find(|name| **name == key) {
        return Some(name);
    }
    let folded = fold_key(key);
    names.iter().find(|name| fold_key(name) == folded).copied()
}

fn struct_error(value: &str, structure: &str, field: &str, ty: &str) -> String {
    format!("json: cannot unmarshal {value} into Go struct field {structure}.{field} of type {ty}")
}

fn string_field(
    value: &Json,
    out: &mut String,
    structure: &str,
    field: &str,
) -> Result<(), String> {
    match value {
        Json::Null => Ok(()),
        Json::Str(text) => {
            out.clone_from(text);
            Ok(())
        }
        other => Err(struct_error(other.kind(), structure, field, "string")),
    }
}

fn decode_extraction(value: &Json) -> Result<DecodedExtraction, String> {
    const NAMES: &[&str] = &["field", "type", "value", "span", "matched"];
    let mut out = DecodedExtraction::default();
    let entries = match value {
        Json::Null => return Ok(out),
        Json::Obj(entries) => entries,
        other => {
            return Err(format!(
                "json: cannot unmarshal {} into Go value of type engine.sidecarExtraction",
                other.kind()
            ));
        }
    };
    let structure = "sidecarExtraction";
    for (key, value) in entries {
        match lookup(NAMES, key) {
            Some("field") => string_field(value, &mut out.field, structure, "field")?,
            Some("type") => string_field(value, &mut out.kind, structure, "type")?,
            Some("value") => string_field(value, &mut out.value, structure, "value")?,
            Some("matched") => match value {
                Json::Null => {}
                Json::Bool(flag) => out.matched = *flag,
                other => {
                    return Err(struct_error(other.kind(), structure, "matched", "bool"));
                }
            },
            Some("span") => match value {
                Json::Null => out.span = None,
                Json::Obj(fields) => {
                    // Go decodes into the pointer it already holds, so a
                    // repeated "span" key merges into the earlier object.
                    let span = out.span.get_or_insert_with(Span::default);
                    decode_span(fields, span)?;
                }
                other => {
                    return Err(struct_error(
                        other.kind(),
                        structure,
                        "span",
                        "engine.sidecarSpan",
                    ));
                }
            },
            _ => {}
        }
    }
    Ok(out)
}

fn decode_span(entries: &[(String, Json)], span: &mut Span) -> Result<(), String> {
    const NAMES: &[&str] = &["start", "end", "snippet"];
    for (key, value) in entries {
        match lookup(NAMES, key) {
            Some(name @ ("start" | "end")) => {
                let target = if name == "start" {
                    &mut span.start
                } else {
                    &mut span.end
                };
                match value {
                    Json::Null => {}
                    Json::Num(raw) => match raw.parse::<i64>() {
                        Ok(number) => *target = number,
                        Err(_) => {
                            return Err(struct_error(
                                &format!("number {raw}"),
                                "sidecarSpan",
                                &format!("span.{name}"),
                                "int",
                            ));
                        }
                    },
                    other => {
                        return Err(struct_error(
                            other.kind(),
                            "sidecarSpan",
                            &format!("span.{name}"),
                            "int",
                        ));
                    }
                }
            }
            Some("snippet") => {
                string_field(value, &mut span.snippet, "sidecarSpan", "span.snippet")?
            }
            _ => {}
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn digest_is_read_only_from_the_leading_block() {
        let digest = "ab".repeat(32);
        let leading = format!("---\nsha256: {}\n---\nbody", digest.to_uppercase());
        assert_eq!(frontmatter_sha256(&leading), Some(digest.clone()));
        assert_eq!(
            frontmatter_sha256(&format!("text\n---\nsha256: {digest}\n---\n")),
            None
        );
        assert_eq!(
            frontmatter_sha256(&format!("---\n  sha256: {digest}\n---\n")),
            None
        );
        assert_eq!(frontmatter_sha256("---\nsha256: abc\n---\n"), None);
    }

    #[test]
    fn extension_gate_matches_filepath_ext() {
        assert_eq!(go_extension("/a.b/c"), "");
        assert_eq!(go_extension("/a/c.MD"), ".MD");
        assert_eq!(go_extension("/a/.md"), ".md");
        assert!(detect_sidecar_path("/a/notes.txt", "---\nsha256: x\n---\n").is_none());
    }

    #[test]
    fn scanner_limit_follows_buffer_size() {
        let under = vec![b'x'; MAX_LINE_BYTES - 1];
        assert!(scan_lines(&under).all(|line| line.is_ok()));
        let at_limit = vec![b'x'; MAX_LINE_BYTES];
        assert_eq!(
            scan_lines(&at_limit).next(),
            Some(Err("bufio.Scanner: token too long"))
        );
        let lines: Vec<_> = scan_lines(b"a\r\n\nb")
            .map(|line| line.map(|bytes| bytes.to_vec()))
            .collect();
        assert_eq!(
            lines,
            vec![Ok(b"a".to_vec()), Ok(Vec::new()), Ok(b"b".to_vec())]
        );
    }

    #[test]
    fn chunk_link_wraps_like_go_ints() {
        let chunks = [(1, Some(0), Some(10)), (2, Some(100), Some(200))];
        assert_eq!(best_matching_chunk_id(&chunks, 5, 7), Some(1));
        assert_eq!(best_matching_chunk_id(&chunks, 150, 150), Some(2));
        assert_eq!(best_matching_chunk_id(&chunks, 1000, 1000), Some(2));
        assert_eq!(best_matching_chunk_id(&chunks, i64::MAX, i64::MAX), Some(1));
        assert_eq!(best_matching_chunk_id(&[(1, None, None)], 1, 2), None);
    }

    #[test]
    fn decoder_reports_go_wording() {
        let error = |line: &str| match decode_line(line) {
            Err(error) => error,
            Ok(_) => panic!("{line} decoded"),
        };
        assert_eq!(error("{"), "unexpected end of JSON input");
        assert_eq!(
            error("{\"a\":1} x"),
            "invalid character 'x' after top-level value"
        );
        assert_eq!(
            error("{\"span\":{\"start\":1.5}}"),
            "json: cannot unmarshal number 1.5 into Go struct field sidecarSpan.span.start of type int"
        );
        assert_eq!(
            error("[1]"),
            "json: cannot unmarshal array into Go value of type engine.sidecarExtraction"
        );
        let merged = decode_line(r#"{"SPAN":{"Start":1,"end":2},"span":{"start":3}}"#)
            .expect("duplicate span keys merge");
        let span = merged.span.expect("span");
        assert_eq!((span.start, span.end), (3, 2));
    }
}
