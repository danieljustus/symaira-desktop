//! Port of the Go `symaira-corekit/mcpserver` stdio transport: line and
//! `Content-Length` framing, the `encoding/json` syntax scanner (so `Parse
//! error:` texts are Go's), the JSON-RPC request envelope and response
//! rendering. The module is pure (no process, filesystem or global state) so
//! the same code is exercised by the server, the unit tests and the
//! `mcp_frame` fuzz target.

use std::{
    borrow::Cow,
    collections::BTreeMap,
    io::{self, BufRead, Write},
};

use serde_json::{Value, json};

pub const MAX_FRAME_BYTES: usize = 1 << 20;
const MAX_HEADER_BYTES: usize = 64 << 10;
const MAX_HEADER_LINES: usize = 100;
/// Go `encoding/json` `maxNestingDepth`: a scanner error past this many open
/// containers.
const MAX_NESTING_DEPTH: usize = 10_000;

fn is_space(c: u8) -> bool {
    matches!(c, b' ' | b'\t' | b'\r' | b'\n')
}

/// Go `quoteChar` from `encoding/json/scanner.go`.
fn go_quote_char(c: u8) -> String {
    match c {
        b'\'' => "'\\''".to_owned(),
        b'"' => "'\"'".to_owned(),
        7 => "'\\a'".to_owned(),
        8 => "'\\b'".to_owned(),
        9 => "'\\t'".to_owned(),
        10 => "'\\n'".to_owned(),
        11 => "'\\v'".to_owned(),
        12 => "'\\f'".to_owned(),
        13 => "'\\r'".to_owned(),
        b'\\' => "'\\\\'".to_owned(),
        0x20..=0x7e => format!("'{}'", char::from(c)),
        0xa1..=0xff if c != 0xad => format!("'{}'", char::from(c)),
        0..=0x1f | 0x7f => format!("'\\x{c:02x}'"),
        _ => format!("'\\u{c:04x}'"),
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum ScanState {
    BeginValueOrEmpty,
    BeginValue,
    BeginStringOrEmpty,
    BeginString,
    EndValue,
    EndTop,
    InString,
    InStringEsc,
    InStringEscU,
    InStringEscU1,
    InStringEscU2,
    InStringEscU3,
    Neg,
    One,
    Zero,
    Dot,
    Dot0,
    Exp,
    ExpSign,
    Exp0,
    T,
    Tr,
    Tru,
    F,
    Fa,
    Fal,
    Fals,
    N,
    Nu,
    Nul,
    Error,
}

#[derive(Clone, Copy)]
enum ParseState {
    ObjectKey,
    ObjectValue,
    ArrayValue,
}

/// Port of Go's `encoding/json` validity scanner (`checkValid`), reproducing
/// its `SyntaxError` text byte for byte.
struct GoScanner {
    step: ScanState,
    end_top: bool,
    stack: Vec<ParseState>,
    error: Option<String>,
}

impl GoScanner {
    fn new() -> Self {
        Self {
            step: ScanState::BeginValue,
            end_top: false,
            stack: Vec::new(),
            error: None,
        }
    }

    fn fail(&mut self, c: u8, context: &str) -> bool {
        self.step = ScanState::Error;
        self.error = Some(format!("invalid character {} {context}", go_quote_char(c)));
        true
    }

    /// Go `pushParseState`: more than `maxNestingDepth` open containers is a
    /// syntax error at the opening byte.
    fn check_depth(&mut self, c: u8) -> bool {
        if self.stack.len() > MAX_NESTING_DEPTH {
            self.fail(c, "exceeded max depth")
        } else {
            false
        }
    }

    fn pop(&mut self) {
        self.stack.pop();
        if self.stack.is_empty() {
            self.step = ScanState::EndTop;
            self.end_top = true;
        } else {
            self.step = ScanState::EndValue;
        }
    }

    fn feed(&mut self, c: u8) -> bool {
        let state = self.step;
        self.run(state, c)
    }

    fn literal(&mut self, c: u8, expect: u8, next: ScanState, what: &str) -> bool {
        if c == expect {
            self.step = next;
            false
        } else {
            self.fail(
                c,
                &format!("in literal {what} (expecting '{}')", char::from(expect)),
            )
        }
    }

    #[expect(clippy::too_many_lines, reason = "direct port of the Go state machine")]
    fn run(&mut self, state: ScanState, c: u8) -> bool {
        use ScanState::{
            BeginString, BeginStringOrEmpty, BeginValue, BeginValueOrEmpty, Dot, Dot0, EndTop,
            EndValue, Error, Exp, Exp0, ExpSign, F, Fa, Fal, Fals, InString, InStringEsc,
            InStringEscU, InStringEscU1, InStringEscU2, InStringEscU3, N, Neg, Nu, Nul, One, T, Tr,
            Tru, Zero,
        };
        match state {
            BeginValueOrEmpty => {
                if is_space(c) {
                    return false;
                }
                if c == b']' {
                    return self.run(EndValue, c);
                }
                self.run(BeginValue, c)
            }
            BeginValue => {
                if is_space(c) {
                    return false;
                }
                match c {
                    b'{' => {
                        self.step = BeginStringOrEmpty;
                        self.stack.push(ParseState::ObjectKey);
                        self.check_depth(c)
                    }
                    b'[' => {
                        self.step = BeginValueOrEmpty;
                        self.stack.push(ParseState::ArrayValue);
                        self.check_depth(c)
                    }
                    b'"' => {
                        self.step = InString;
                        false
                    }
                    b'-' => {
                        self.step = Neg;
                        false
                    }
                    b'0' => {
                        self.step = Zero;
                        false
                    }
                    b't' => {
                        self.step = T;
                        false
                    }
                    b'f' => {
                        self.step = F;
                        false
                    }
                    b'n' => {
                        self.step = N;
                        false
                    }
                    b'1'..=b'9' => {
                        self.step = One;
                        false
                    }
                    _ => self.fail(c, "looking for beginning of value"),
                }
            }
            BeginStringOrEmpty => {
                if is_space(c) {
                    return false;
                }
                if c == b'}' {
                    if let Some(top) = self.stack.last_mut() {
                        *top = ParseState::ObjectValue;
                    }
                    return self.run(EndValue, c);
                }
                self.run(BeginString, c)
            }
            BeginString => {
                if is_space(c) {
                    return false;
                }
                if c == b'"' {
                    self.step = InString;
                    return false;
                }
                self.fail(c, "looking for beginning of object key string")
            }
            EndValue => {
                let Some(top) = self.stack.last().copied() else {
                    self.step = EndTop;
                    self.end_top = true;
                    return self.run(EndTop, c);
                };
                if is_space(c) {
                    self.step = EndValue;
                    return false;
                }
                match top {
                    ParseState::ObjectKey => {
                        if c == b':' {
                            if let Some(top) = self.stack.last_mut() {
                                *top = ParseState::ObjectValue;
                            }
                            self.step = BeginValue;
                            return false;
                        }
                        self.fail(c, "after object key")
                    }
                    ParseState::ObjectValue => {
                        if c == b',' {
                            if let Some(top) = self.stack.last_mut() {
                                *top = ParseState::ObjectKey;
                            }
                            self.step = BeginString;
                            return false;
                        }
                        if c == b'}' {
                            self.pop();
                            return false;
                        }
                        self.fail(c, "after object key:value pair")
                    }
                    ParseState::ArrayValue => {
                        if c == b',' {
                            self.step = BeginValue;
                            return false;
                        }
                        if c == b']' {
                            self.pop();
                            return false;
                        }
                        self.fail(c, "after array element")
                    }
                }
            }
            EndTop => {
                if !is_space(c) {
                    // Go records the error but reports scanEnd for this byte;
                    // the error surfaces on the next byte or at end of input.
                    self.fail(c, "after top-level value");
                }
                false
            }
            InString => {
                match c {
                    b'"' => self.step = EndValue,
                    b'\\' => self.step = InStringEsc,
                    0..=0x1f => return self.fail(c, "in string literal"),
                    _ => {}
                }
                false
            }
            InStringEsc => match c {
                b'b' | b'f' | b'n' | b'r' | b't' | b'\\' | b'/' | b'"' => {
                    self.step = InString;
                    false
                }
                b'u' => {
                    self.step = InStringEscU;
                    false
                }
                _ => self.fail(c, "in string escape code"),
            },
            InStringEscU | InStringEscU1 | InStringEscU2 | InStringEscU3 => {
                if c.is_ascii_hexdigit() {
                    self.step = match state {
                        InStringEscU => InStringEscU1,
                        InStringEscU1 => InStringEscU2,
                        InStringEscU2 => InStringEscU3,
                        _ => InString,
                    };
                    false
                } else {
                    self.fail(c, "in \\u hexadecimal character escape")
                }
            }
            Neg => match c {
                b'0' => {
                    self.step = Zero;
                    false
                }
                b'1'..=b'9' => {
                    self.step = One;
                    false
                }
                _ => self.fail(c, "in numeric literal"),
            },
            One => {
                if c.is_ascii_digit() {
                    self.step = One;
                    return false;
                }
                self.run(Zero, c)
            }
            Zero => match c {
                b'.' => {
                    self.step = Dot;
                    false
                }
                b'e' | b'E' => {
                    self.step = Exp;
                    false
                }
                _ => self.run(EndValue, c),
            },
            Dot => {
                if c.is_ascii_digit() {
                    self.step = Dot0;
                    return false;
                }
                self.fail(c, "after decimal point in numeric literal")
            }
            Dot0 => match c {
                b'0'..=b'9' => false,
                b'e' | b'E' => {
                    self.step = Exp;
                    false
                }
                _ => self.run(EndValue, c),
            },
            Exp => {
                if c == b'+' || c == b'-' {
                    self.step = ExpSign;
                    return false;
                }
                self.run(ExpSign, c)
            }
            ExpSign => {
                if c.is_ascii_digit() {
                    self.step = Exp0;
                    return false;
                }
                self.fail(c, "in exponent of numeric literal")
            }
            Exp0 => {
                if c.is_ascii_digit() {
                    return false;
                }
                self.run(EndValue, c)
            }
            T => self.literal(c, b'r', Tr, "true"),
            Tr => self.literal(c, b'u', Tru, "true"),
            Tru => self.literal(c, b'e', EndValue, "true"),
            F => self.literal(c, b'a', Fa, "false"),
            Fa => self.literal(c, b'l', Fal, "false"),
            Fal => self.literal(c, b's', Fals, "false"),
            Fals => self.literal(c, b'e', EndValue, "false"),
            N => self.literal(c, b'u', Nu, "null"),
            Nu => self.literal(c, b'l', Nul, "null"),
            Nul => self.literal(c, b'l', EndValue, "null"),
            Error => true,
        }
    }

    fn eof(&mut self) -> bool {
        if self.error.is_some() {
            return true;
        }
        if self.end_top {
            return false;
        }
        let state = self.step;
        self.run(state, b' ');
        if self.end_top {
            return false;
        }
        if self.error.is_none() {
            self.error = Some("unexpected end of JSON input".to_owned());
        }
        true
    }
}

/// Go `json.Valid` with the `SyntaxError` text of `json.Unmarshal`.
pub fn go_check_valid(data: &[u8]) -> Result<(), String> {
    let mut scanner = GoScanner::new();
    for &c in data {
        if scanner.feed(c) {
            return Err(scanner.error.unwrap_or_default());
        }
    }
    if scanner.eof() {
        return Err(scanner.error.unwrap_or_default());
    }
    Ok(())
}

/// Go `strconv.Quote` for the `%q` verb in `invalid Content-Length: %q`; a
/// byte that is not valid UTF-8 prints as `\xNN`.
pub fn go_quote(bytes: &[u8]) -> String {
    let mut out = String::from("\"");
    let mut rest = bytes;
    loop {
        let (text, invalid) = match std::str::from_utf8(rest) {
            Ok(text) => (text, None),
            Err(error) => {
                let (valid, after) = rest.split_at(error.valid_up_to());
                let width = error.error_len().unwrap_or(after.len());
                (
                    std::str::from_utf8(valid).unwrap_or_default(),
                    Some((width, after)),
                )
            }
        };
        for ch in text.chars() {
            match ch {
                '"' => out.push_str("\\\""),
                '\\' => out.push_str("\\\\"),
                '\n' => out.push_str("\\n"),
                '\r' => out.push_str("\\r"),
                '\t' => out.push_str("\\t"),
                '\u{7}' => out.push_str("\\a"),
                '\u{8}' => out.push_str("\\b"),
                '\u{b}' => out.push_str("\\v"),
                '\u{c}' => out.push_str("\\f"),
                c if (c as u32) < 0x20 || c as u32 == 0x7f => {
                    out.push_str(&format!("\\x{:02x}", c as u32));
                }
                c => out.push(c),
            }
        }
        let Some((width, after)) = invalid else {
            break;
        };
        for byte in &after[..width] {
            out.push_str(&format!("\\x{byte:02x}"));
        }
        rest = &after[width..];
    }
    out.push('"');
    out
}

/// Go `encoding/json` string encoding (HTML-safe, U+2028/U+2029 escaped).
pub fn go_write_string(out: &mut String, text: &str) {
    out.push('"');
    for ch in text.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                out.push_str(&format!("\\u{:04x}", ch as u32));
            }
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

/// Go `json.Marshal` of a generic value: object keys sorted, HTML-safe strings.
pub fn go_marshal(value: &Value, out: &mut String) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(flag) => out.push_str(if *flag { "true" } else { "false" }),
        Value::Number(number) => out.push_str(&number.to_string()),
        Value::String(text) => go_write_string(out, text),
        Value::Array(items) => {
            out.push('[');
            for (index, item) in items.iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                go_marshal(item, out);
            }
            out.push(']');
        }
        Value::Object(map) => {
            let sorted: BTreeMap<&String, &Value> = map.iter().collect();
            out.push('{');
            for (index, (key, item)) in sorted.into_iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                go_write_string(out, key);
                out.push(':');
                go_marshal(item, out);
            }
            out.push('}');
        }
    }
}

pub fn go_marshal_string(value: &Value) -> String {
    let mut out = String::new();
    go_marshal(value, &mut out);
    out
}

/// Re-escape serde output the way `json.Marshal` does (HTML-safe).
pub fn go_html_escape(rendered: &str) -> String {
    rendered
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

/// Go's decoder turns every invalid UTF-8 byte inside a JSON string into one
/// U+FFFD (it decodes rune by rune), unlike `String::from_utf8_lossy`, which
/// collapses a truncated sequence into a single replacement character.
pub fn go_lossy_utf8(bytes: &[u8]) -> Cow<'_, str> {
    if let Ok(text) = std::str::from_utf8(bytes) {
        return Cow::Borrowed(text);
    }
    let mut out = String::with_capacity(bytes.len() + 8);
    let mut rest = bytes;
    loop {
        match std::str::from_utf8(rest) {
            Ok(text) => {
                out.push_str(text);
                return Cow::Owned(out);
            }
            Err(error) => {
                let (valid, after) = rest.split_at(error.valid_up_to());
                out.push_str(std::str::from_utf8(valid).unwrap_or_default());
                let invalid = error.error_len().unwrap_or(after.len());
                for _ in 0..invalid {
                    out.push('\u{fffd}');
                }
                rest = &after[invalid..];
            }
        }
    }
}

fn hex4(bytes: &[u8]) -> Option<u16> {
    u16::from_str_radix(std::str::from_utf8(bytes.get(..4)?).ok()?, 16).ok()
}

/// Go `unquote` replaces an unpaired `\uD800`-`\uDFFF` escape with U+FFFD and
/// consumes only that escape; serde_json rejects the same input. Rewrite such
/// escapes to `\uFFFD` in already-validated JSON text before handing it to
/// serde_json.
pub fn go_sanitize_surrogates(text: &str) -> Cow<'_, str> {
    let bytes = text.as_bytes();
    let mut out: Option<String> = None;
    let mut copied = 0;
    let mut index = 0;
    let mut in_string = false;
    while index < bytes.len() {
        let byte = bytes[index];
        if !in_string {
            in_string = byte == b'"';
            index += 1;
            continue;
        }
        match byte {
            b'"' => {
                in_string = false;
                index += 1;
            }
            b'\\' if bytes.get(index + 1) == Some(&b'u') => {
                let code = hex4(&bytes[index + 2..]);
                let paired = matches!(code, Some(0xd800..=0xdbff))
                    && bytes.get(index + 6) == Some(&b'\\')
                    && bytes.get(index + 7) == Some(&b'u')
                    && matches!(hex4(&bytes[index + 8..]), Some(0xdc00..=0xdfff));
                if paired {
                    index += 12;
                } else {
                    if matches!(code, Some(0xd800..=0xdfff)) {
                        let buffer = out.get_or_insert_with(String::new);
                        buffer.push_str(&text[copied..index]);
                        buffer.push_str("\\ufffd");
                        copied = index + 6;
                    }
                    index += 6;
                }
            }
            b'\\' => index += 2,
            _ => index += 1,
        }
    }
    match out {
        Some(mut buffer) => {
            buffer.push_str(&text[copied..]);
            Cow::Owned(buffer)
        }
        None => Cow::Borrowed(text),
    }
}

/// The JSON kind of a raw, already validated value, named as Go's
/// `UnmarshalTypeError` names it. Looks only at the first byte, so nesting
/// depth never matters.
pub fn raw_kind(raw: &str) -> &'static str {
    match raw.trim_start().as_bytes().first() {
        Some(b'{') => "object",
        Some(b'[') => "array",
        Some(b'"') => "string",
        Some(b't' | b'f') => "bool",
        Some(b'n') => "null",
        _ => "number",
    }
}

fn skip_space(bytes: &[u8], mut index: usize) -> usize {
    while bytes.get(index).is_some_and(|byte| is_space(*byte)) {
        index += 1;
    }
    index
}

/// Index just past the string literal that starts at `start`.
fn string_end(bytes: &[u8], start: usize) -> Option<usize> {
    let mut index = start + 1;
    while let Some(byte) = bytes.get(index) {
        match byte {
            b'"' => return Some(index + 1),
            b'\\' => index += 2,
            _ => index += 1,
        }
    }
    None
}

/// Index just past the JSON value that starts at `start`.
fn value_end(bytes: &[u8], start: usize) -> Option<usize> {
    match bytes.get(start)? {
        b'"' => string_end(bytes, start),
        b'{' | b'[' => {
            let mut depth = 0_usize;
            let mut index = start;
            while let Some(byte) = bytes.get(index) {
                match byte {
                    b'"' => index = string_end(bytes, index)?,
                    b'{' | b'[' => {
                        depth += 1;
                        index += 1;
                    }
                    b'}' | b']' => {
                        depth -= 1;
                        index += 1;
                        if depth == 0 {
                            return Some(index);
                        }
                    }
                    _ => index += 1,
                }
            }
            None
        }
        _ => {
            let mut index = start;
            while bytes
                .get(index)
                .is_some_and(|byte| !is_space(*byte) && !matches!(byte, b',' | b'}' | b']'))
            {
                index += 1;
            }
            Some(index)
        }
    }
}

/// Ordered members of a JSON object (duplicates kept) with each value as raw
/// text, so Go's case-insensitive "last match wins" decoding can be replayed.
/// It is a byte scanner over text that already passed `go_check_valid`, so
/// unlike serde_json it has no recursion limit: Go accepts nesting up to
/// `MAX_NESTING_DEPTH`.
fn split_members(text: &str) -> Option<Vec<(String, &str)>> {
    let bytes = text.as_bytes();
    let mut index = skip_space(bytes, 0);
    if bytes.get(index) != Some(&b'{') {
        return None;
    }
    index += 1;
    let mut members = Vec::new();
    loop {
        index = skip_space(bytes, index);
        match bytes.get(index)? {
            b'}' => return Some(members),
            b',' => {
                index += 1;
                continue;
            }
            b'"' => {}
            _ => return None,
        }
        let key_end = string_end(bytes, index)?;
        let key: String = serde_json::from_str(&text[index..key_end]).ok()?;
        index = skip_space(bytes, key_end);
        if bytes.get(index) != Some(&b':') {
            return None;
        }
        index = skip_space(bytes, index + 1);
        let end = value_end(bytes, index)?;
        members.push((key, &text[index..end]));
        index = end;
    }
}

// ---------------------------------------------------------------------------
// Framing and JSON-RPC envelope (Go `corekit/mcpserver`).
// ---------------------------------------------------------------------------

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Mode {
    Framed,
    Line,
}

#[derive(Debug)]
pub struct Request {
    pub has_id: bool,
    /// JSON text echoed in the response `id` member (`null` when absent).
    pub id: String,
    pub method: String,
    pub params: Option<String>,
}

#[derive(Debug)]
pub enum ReadError {
    Parse(String, Mode),
    InvalidRequest(Mode),
    Fatal(io::Error),
}

pub fn fatal(kind: io::ErrorKind, message: impl Into<String>) -> ReadError {
    ReadError::Fatal(io::Error::new(kind, message.into()))
}

/// Go `readLineLimited`: one `\n`-terminated line (kept), `eof` set when the
/// stream ended first.
pub fn read_line_limited<R: BufRead>(input: &mut R) -> io::Result<(Vec<u8>, bool)> {
    let mut line = Vec::new();
    loop {
        let buffer = input.fill_buf()?;
        if buffer.is_empty() {
            return Ok((line, true));
        }
        let (take, found) = buffer
            .iter()
            .position(|byte| *byte == b'\n')
            .map_or((buffer.len(), false), |index| (index + 1, true));
        if line.len() + take > MAX_FRAME_BYTES {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("line exceeds {MAX_FRAME_BYTES} bytes"),
            ));
        }
        line.extend_from_slice(&buffer[..take]);
        input.consume(take);
        if found {
            return Ok((line, false));
        }
    }
}

/// The first character of `bytes` when it starts with a complete, valid UTF-8
/// sequence (Go's `utf8.DecodeRune` reports an invalid byte as RuneError,
/// which is not a space).
fn first_char(bytes: &[u8]) -> Option<(char, usize)> {
    (1..=bytes.len().min(4)).find_map(|width| {
        std::str::from_utf8(&bytes[..width])
            .ok()
            .and_then(|text| text.chars().next())
            .map(|ch| (ch, width))
    })
}

fn last_char(bytes: &[u8]) -> Option<(char, usize)> {
    (1..=bytes.len().min(4)).find_map(|width| {
        std::str::from_utf8(&bytes[bytes.len() - width..])
            .ok()
            .and_then(|text| text.chars().next_back())
            .filter(|ch| ch.len_utf8() == width)
            .map(|ch| (ch, width))
    })
}

/// Go `strings.TrimSpace` over bytes that may not be valid UTF-8, so a line is
/// trimmed without first replacing its invalid bytes.
fn go_trim_space_bytes(mut bytes: &[u8]) -> &[u8] {
    while let Some((ch, width)) = first_char(bytes) {
        if !ch.is_whitespace() {
            break;
        }
        bytes = &bytes[width..];
    }
    while let Some((ch, width)) = last_char(bytes) {
        if !ch.is_whitespace() {
            break;
        }
        bytes = &bytes[..bytes.len() - width];
    }
    bytes
}

/// Go `looksLikeJSONLine`. `json.Valid` implies the first-byte test below, so
/// the first byte alone decides.
fn looks_like_json_line(line: &[u8]) -> bool {
    match line.first() {
        None => false,
        Some(b'{' | b'[' | b'"' | b'-' | b't' | b'f' | b'n') => true,
        Some(byte) => byte.is_ascii_digit(),
    }
}

fn content_length_value(rest: &[u8]) -> Result<i64, ReadError> {
    let value = go_trim_space_bytes(rest);
    std::str::from_utf8(value)
        .ok()
        .and_then(|text| text.parse::<i64>().ok())
        .ok_or_else(|| {
            fatal(
                io::ErrorKind::InvalidData,
                format!("invalid Content-Length: {}", go_quote(value)),
            )
        })
}

/// Go `readRequest`: a line of JSON or a `Content-Length` frame. `Ok(None)` is
/// a clean end of stream.
pub fn read_request<R: BufRead>(input: &mut R) -> Result<Option<(Request, Mode)>, ReadError> {
    let mut total_bytes = 0;
    let line = loop {
        let (raw, eof) = read_line_limited(input).map_err(ReadError::Fatal)?;
        total_bytes += raw.len();
        let trimmed = go_trim_space_bytes(&raw);
        if eof {
            if raw.is_empty() {
                return Ok(None);
            }
            break trimmed.to_vec();
        }
        if !trimmed.is_empty() {
            break trimmed.to_vec();
        }
    };

    if looks_like_json_line(&line) {
        return parse_json_request(&line, Mode::Line).map(Some);
    }

    let mut header_bytes = total_bytes;
    let mut header_lines = 1;
    if header_bytes > MAX_HEADER_BYTES || header_lines > MAX_HEADER_LINES {
        return Err(fatal(
            io::ErrorKind::InvalidData,
            "framed header limits exceeded",
        ));
    }
    let mut content_length = None;
    if let Some(rest) = line.strip_prefix(b"Content-Length:") {
        content_length = Some(content_length_value(rest)?);
    }
    loop {
        let (raw, eof) = read_line_limited(input).map_err(ReadError::Fatal)?;
        if eof {
            return Ok(None);
        }
        let mut trimmed = raw.as_slice();
        while let Some(stripped) = trimmed
            .strip_suffix(b"\n")
            .or_else(|| trimmed.strip_suffix(b"\r"))
        {
            trimmed = stripped;
        }
        if trimmed.is_empty() {
            break;
        }
        header_bytes += raw.len();
        header_lines += 1;
        if header_bytes > MAX_HEADER_BYTES || header_lines > MAX_HEADER_LINES {
            return Err(fatal(
                io::ErrorKind::InvalidData,
                "framed header limits exceeded",
            ));
        }
        if let Some(rest) = trimmed.strip_prefix(b"Content-Length:") {
            content_length = Some(content_length_value(rest)?);
        }
    }
    let Some(length) = content_length else {
        return Err(fatal(
            io::ErrorKind::InvalidData,
            "missing Content-Length header",
        ));
    };
    if length <= 0 || length > MAX_FRAME_BYTES as i64 {
        return Err(fatal(
            io::ErrorKind::InvalidData,
            format!("invalid Content-Length: {length}"),
        ));
    }
    let mut body = vec![0; length as usize];
    let mut filled = 0;
    while filled < body.len() {
        match input.read(&mut body[filled..]) {
            Ok(0) => {
                let reason = if filled == 0 { "EOF" } else { "unexpected EOF" };
                return Err(fatal(
                    io::ErrorKind::UnexpectedEof,
                    format!("read body: {reason}"),
                ));
            }
            Ok(read) => filled += read,
            Err(error) if error.kind() == io::ErrorKind::Interrupted => {}
            Err(error) => return Err(fatal(error.kind(), format!("read body: {error}"))),
        }
    }
    parse_json_request(&body, Mode::Framed).map(Some)
}

fn valid_params(text: &str) -> bool {
    matches!(text.trim_start().as_bytes().first(), Some(b'{' | b'['))
}

/// Go `parseJSONRequest`: syntax errors are parse errors; every shape error is
/// an invalid request.
pub fn parse_json_request(data: &[u8], mode: Mode) -> Result<(Request, Mode), ReadError> {
    if let Err(message) = go_check_valid(data) {
        return Err(ReadError::Parse(message, mode));
    }
    // The scanner has accepted the bytes, so what remains is Go's decode:
    // invalid UTF-8 and unpaired surrogate escapes inside strings become
    // U+FFFD instead of failing.
    let text = go_lossy_utf8(data);
    let text = go_sanitize_surrogates(&text);
    let Some(members) = split_members(&text) else {
        return Err(ReadError::InvalidRequest(mode));
    };
    let mut jsonrpc = String::new();
    let mut method = String::new();
    let mut id_raw: Option<&str> = None;
    let mut params: Option<&str> = None;
    let mut type_error = false;
    let mut has_id = false;
    let mut has_params = false;
    for (key, raw) in &members {
        has_id |= key == "id";
        has_params |= key == "params";
        let lower = key.to_ascii_lowercase();
        match lower.as_str() {
            "jsonrpc" | "method" => match raw_kind(raw) {
                "string" => {
                    let value: String = serde_json::from_str(raw).unwrap_or_default();
                    if lower == "jsonrpc" {
                        jsonrpc = value;
                    } else {
                        method = value;
                    }
                }
                "null" => {}
                _ => type_error = true,
            },
            "id" => id_raw = Some(*raw),
            "params" => params = Some(*raw),
            _ => {}
        }
    }
    let id_text = id_raw.unwrap_or("null").to_owned();
    let id_valid = matches!(raw_kind(&id_text), "null" | "string" | "number");
    if type_error
        || jsonrpc != "2.0"
        || method.is_empty()
        || (has_id && !id_valid)
        || (has_params && !params.is_some_and(valid_params))
    {
        return Err(ReadError::InvalidRequest(mode));
    }
    let id = match serde_json::from_str::<Value>(&id_text) {
        Ok(Value::String(value)) => go_marshal_string(&Value::String(value)),
        _ => id_text,
    };
    Ok((
        Request {
            has_id,
            id,
            method,
            params: params.map(str::to_owned),
        },
        mode,
    ))
}

pub fn render_result(id: &str, result: &Value) -> String {
    format!(
        "{{\"jsonrpc\":\"2.0\",\"id\":{id},\"result\":{}}}",
        go_marshal_string(result)
    )
}

pub fn render_error(id: &str, code: i64, message: &str) -> String {
    let error = go_marshal_string(&json!({"code": code, "message": message}));
    format!("{{\"jsonrpc\":\"2.0\",\"id\":{id},\"error\":{error}}}")
}

pub fn write_frame<W: Write>(output: &mut W, mode: Mode, body: &str) -> io::Result<()> {
    let mut frame = Vec::with_capacity(body.len() + 32);
    if mode == Mode::Line {
        frame.extend_from_slice(body.as_bytes());
        frame.push(b'\n');
    } else {
        frame.extend_from_slice(format!("Content-Length: {}\r\n\r\n", body.len()).as_bytes());
        frame.extend_from_slice(body.as_bytes());
    }
    output.write_all(&frame)?;
    output.flush()
}

/// What the server's read loop does with one input stream, minus tool
/// dispatch: every request is answered with the `ping` result (a notification
/// with nothing), every parse/shape error with the Go error response, and the
/// first fatal read error ends the stream. Returns the exact stdout bytes and
/// the fatal error text (Go `mcpserver: read error: ...`). Used by the unit
/// tests and the `mcp_frame` fuzz target; tool dispatch is not involved.
pub fn drive_stream(input: &[u8]) -> (Vec<u8>, Option<String>) {
    let mut reader = io::BufReader::new(input);
    let mut out = Vec::new();
    loop {
        match read_request(&mut reader) {
            Ok(None) => return (out, None),
            Ok(Some((request, mode))) => {
                if request.has_id {
                    let body = render_result(&request.id, &json!({}));
                    let _ = write_frame(&mut out, mode, &body);
                }
            }
            Err(ReadError::Parse(message, mode)) => {
                let body = render_error("null", -32700, &format!("Parse error: {message}"));
                let _ = write_frame(&mut out, mode, &body);
            }
            Err(ReadError::InvalidRequest(mode)) => {
                let body = render_error("null", -32600, "Invalid Request");
                let _ = write_frame(&mut out, mode, &body);
            }
            Err(ReadError::Fatal(error)) => {
                return (out, Some(format!("mcpserver: read error: {error}")));
            }
        }
    }
}

/// Checks that `out` is a concatenation of well-formed response messages: each
/// is either a `Content-Length` frame whose body has exactly that many bytes
/// or a single line, and every body is one JSON-RPC 2.0 object. Anything else
/// on the transport is stdio pollution.
pub fn check_output_frames(out: &[u8]) -> Result<usize, String> {
    let mut rest = out;
    let mut count = 0;
    while !rest.is_empty() {
        let body: &[u8] = if let Some(after) = rest.strip_prefix(b"Content-Length: ") {
            let header_end = after
                .windows(4)
                .position(|window| window == b"\r\n\r\n")
                .ok_or("frame header is not terminated")?;
            let length: usize = std::str::from_utf8(&after[..header_end])
                .ok()
                .and_then(|text| text.parse().ok())
                .ok_or("frame length is not a number")?;
            let start = header_end + 4;
            let body = after
                .get(start..start + length)
                .ok_or("frame body is shorter than its Content-Length")?;
            rest = &after[start + length..];
            body
        } else {
            let end = rest
                .iter()
                .position(|byte| *byte == b'\n')
                .ok_or("line response is not newline-terminated")?;
            let body = &rest[..end];
            rest = &rest[end + 1..];
            body
        };
        let value: Value = serde_json::from_slice(body)
            .map_err(|error| format!("response is not JSON: {error}"))?;
        let object = value.as_object().ok_or("response is not an object")?;
        if object.get("jsonrpc") != Some(&json!("2.0")) || !object.contains_key("id") {
            return Err("response is not a JSON-RPC 2.0 message".to_owned());
        }
        count += 1;
    }
    Ok(count)
}
