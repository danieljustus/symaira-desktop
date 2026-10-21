#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    ffi::OsString,
    fs,
    io::{self, BufRead, Write},
    panic::{self, AssertUnwindSafe},
    path::{Path, PathBuf},
    process::ExitCode,
    sync::{
        Arc, Mutex,
        atomic::{AtomicBool, AtomicU64, Ordering},
        mpsc,
    },
    thread,
    time::{Duration, Instant},
};

use serde::{
    Deserialize, Serialize,
    de::{Deserializer, MapAccess, Visitor},
};
use serde_json::{Value, json, value::RawValue};
use sha2::{Digest, Sha256};

const INSTRUCTIONS: &str = "Use room_* tools to inspect and record the signed room work record. There is no approval-granting tool in this server.";
const MCP_USAGE: &str = "Usage of mcp:\n  -artifact-root string\n    \tArtifact root directory\n  -identity string\n    \tSigning identity name\n  -room string\n    \tRoom directory (default \".\")\n";
const MAX_FRAME_BYTES: usize = 1 << 20;
const MAX_HEADER_BYTES: usize = 64 << 10;
const MAX_HEADER_LINES: usize = 100;
/// Go's `json.Unmarshal` error for a `tools/call` params object that is not an
/// object, with the anonymous struct type spelled exactly as `reflect` prints it.
const GO_CALL_PARAMS_TYPE: &str = "struct { Name string \"json:\\\"name\\\"\"; Arguments json.RawMessage \"json:\\\"arguments\\\"\"; Meta map[string]interface {} \"json:\\\"_meta,omitempty\\\"\" }";

// Mirrors Go `flag.ExitOnError` for the mcp FlagSet: -h/-help prints usage to
// stderr and exits 0; an unknown flag is reported before its value is read;
// the first positional argument ends flag parsing. The identity resolver is
// injected so this module stays independent of the CLI config layer.
pub fn run_cli(
    args: &[OsString],
    resolve_identity: impl FnOnce(&str) -> Result<symroom_core::identity::Identity, ExitCode>,
) -> ExitCode {
    let mut room = PathBuf::from(".");
    let mut artifact_root = PathBuf::new();
    let mut identity_name = String::new();
    let mut i = 0;
    while i < args.len() {
        let raw = args[i].to_string_lossy();
        if raw == "--" {
            break;
        }
        if !raw.starts_with('-') || raw == "-" {
            break;
        }
        let flag = raw.trim_start_matches('-');
        let (name, inline_value) = flag
            .split_once('=')
            .map_or((flag, None), |(name, value)| (name, Some(value.to_owned())));
        i += 1;
        if name == "h" || name == "help" {
            eprint!("{MCP_USAGE}");
            return ExitCode::SUCCESS;
        }
        if !matches!(name, "room" | "artifact-root" | "identity") {
            eprintln!("flag provided but not defined: -{name}");
            eprint!("{MCP_USAGE}");
            return ExitCode::from(2);
        }
        let value = if let Some(value) = inline_value {
            OsString::from(value)
        } else if let Some(value) = args.get(i) {
            i += 1;
            value.clone()
        } else {
            eprintln!("flag needs an argument: -{name}");
            eprint!("{MCP_USAGE}");
            return ExitCode::from(2);
        };
        match name {
            "room" => room = PathBuf::from(value),
            "artifact-root" => artifact_root = PathBuf::from(value),
            _ => identity_name = value.to_string_lossy().into_owned(),
        }
    }
    if artifact_root.as_os_str().is_empty() {
        artifact_root.clone_from(&room);
    }
    // Go `resolveIdentity` always runs, so a missing identity fails before serving.
    let identity = match resolve_identity(&identity_name) {
        Ok(identity) => identity,
        Err(code) => return code,
    };
    let stdin = io::stdin();
    let stdout = io::stdout();
    match serve_io_with_identity(stdin.lock(), stdout, &room, &artifact_root, Some(&identity)) {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("{error}");
            ExitCode::FAILURE
        }
    }
}

fn tools() -> Value {
    json!([
        {"name":"room_status","description":"Return room metadata and journal statistics","inputSchema":{"type":"object","properties":{}}},
        {"name":"room_journal_tail","description":"Return the merged signed journal tail","inputSchema":{"type":"object","properties":{"limit":{"type":"integer"}}}},
        {"name":"room_note_post","description":"Post a signed note to the room journal","inputSchema":{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}},
        {"name":"room_artifact_list","description":"List linked room artifacts","inputSchema":{"type":"object","properties":{}}},
        {"name":"room_artifact_link","description":"Link and hash an artifact, recording a signed event","inputSchema":{"type":"object","required":["path"],"properties":{"path":{"type":"string"},"title":{"type":"string"}}}},
        {"name":"room_run_request","description":"Request a run with a signed journal event","inputSchema":{"type":"object","required":["title"],"properties":{"title":{"type":"string"},"plan_file":{"type":"string"},"adapter":{"type":"string"}}}},
        {"name":"room_run_wait","description":"Wait for a run approval decision","inputSchema":{"type":"object","required":["run_id"],"properties":{"run_id":{"type":"string"},"timeout_seconds":{"type":"number"}}}},
        {"name":"room_checkpoint_request","description":"Request a signed human checkpoint","inputSchema":{"type":"object","required":["run_id","question"],"properties":{"run_id":{"type":"string"},"question":{"type":"string"}}}}
    ])
}

// ---------------------------------------------------------------------------
// Go `encoding/json` compatibility: syntax errors, string escaping, field
// matching. The JSON-RPC layer is byte-compared against Go, including the
// text of `Parse error:` messages.
// ---------------------------------------------------------------------------

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
                        false
                    }
                    b'[' => {
                        self.step = BeginValueOrEmpty;
                        self.stack.push(ParseState::ArrayValue);
                        false
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
fn go_check_valid(data: &[u8]) -> Result<(), String> {
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

/// Go `strconv.Quote` for the `%q` verb in `invalid Content-Length: %q`.
fn go_quote(text: &str) -> String {
    let mut out = String::from("\"");
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
    out.push('"');
    out
}

/// Go `encoding/json` string encoding (HTML-safe, U+2028/U+2029 escaped).
fn go_write_string(out: &mut String, text: &str) {
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
fn go_marshal(value: &Value, out: &mut String) {
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

fn go_marshal_string(value: &Value) -> String {
    let mut out = String::new();
    go_marshal(value, &mut out);
    out
}

/// Re-escape serde output the way `json.Marshal` does (HTML-safe).
fn go_html_escape(rendered: &str) -> String {
    rendered
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

fn json_kind(value: &Value) -> &'static str {
    match value {
        Value::Null => "null",
        Value::Bool(_) => "bool",
        Value::Number(_) => "number",
        Value::String(_) => "string",
        Value::Array(_) => "array",
        Value::Object(_) => "object",
    }
}

/// Ordered object members (duplicates kept) as raw JSON, so Go's
/// case-insensitive "last match wins" field decoding can be replayed.
struct Members(Vec<(String, Box<RawValue>)>);

impl<'de> Deserialize<'de> for Members {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct MembersVisitor;
        impl<'de> Visitor<'de> for MembersVisitor {
            type Value = Members;
            fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                formatter.write_str("a JSON object")
            }
            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Members, A::Error> {
                let mut members = Vec::new();
                while let Some(entry) = map.next_entry::<String, Box<RawValue>>()? {
                    members.push(entry);
                }
                Ok(Members(members))
            }
        }
        deserializer.deserialize_map(MembersVisitor)
    }
}

/// Decoded JSON object with Go struct-decoding semantics: fields match
/// case-insensitively, the last duplicate wins, JSON `null` leaves a field
/// unchanged and any type mismatch marks the whole decode as failed while the
/// remaining fields still decode.
struct GoObject {
    failed: bool,
    members: Vec<(String, Value)>,
}

impl GoObject {
    /// `json.Unmarshal(raw, &struct)` where `raw` may be absent (nil).
    fn decode(raw: Option<&str>) -> Self {
        let mut object = Self {
            failed: false,
            members: Vec::new(),
        };
        let Some(raw) = raw else {
            object.failed = true;
            return object;
        };
        match serde_json::from_str::<Value>(raw) {
            Ok(Value::Null) => {}
            Ok(Value::Object(_)) => match serde_json::from_str::<Members>(raw) {
                Ok(Members(members)) => {
                    object.members = members
                        .into_iter()
                        .map(|(key, raw)| {
                            // A literal serde cannot represent (for example an
                            // overflowing exponent) is a Go decode error for
                            // any typed field; a non-string sentinel models it.
                            let value =
                                serde_json::from_str(raw.get()).unwrap_or(Value::Bool(false));
                            (key, value)
                        })
                        .collect();
                }
                Err(_) => object.failed = true,
            },
            _ => object.failed = true,
        }
        object
    }

    fn string(&mut self, name: &str) -> String {
        let mut out = String::new();
        for (key, value) in &self.members {
            if key.eq_ignore_ascii_case(name) {
                match value {
                    Value::String(text) => out.clone_from(text),
                    Value::Null => {}
                    _ => self.failed = true,
                }
            }
        }
        out
    }

    fn int(&mut self, name: &str) -> i64 {
        let mut out = 0;
        for (key, value) in &self.members {
            if key.eq_ignore_ascii_case(name) {
                match value {
                    Value::Number(number) => {
                        if let Some(n) = number.as_i64() {
                            out = n;
                        } else {
                            self.failed = true;
                        }
                    }
                    Value::Null => {}
                    _ => self.failed = true,
                }
            }
        }
        out
    }

    fn float(&mut self, name: &str) -> f64 {
        let mut out = 0.0;
        for (key, value) in &self.members {
            if key.eq_ignore_ascii_case(name) {
                match value {
                    Value::Number(number) => out = number.as_f64().unwrap_or(0.0),
                    Value::Null => {}
                    _ => self.failed = true,
                }
            }
        }
        out
    }
}

// ---------------------------------------------------------------------------
// Framing and JSON-RPC envelope (Go `corekit/mcpserver`).
// ---------------------------------------------------------------------------

#[derive(Clone, Copy, PartialEq, Eq)]
enum Mode {
    Framed,
    Line,
}

struct Request {
    has_id: bool,
    /// JSON text echoed in the response `id` member (`null` when absent).
    id: String,
    method: String,
    params: Option<String>,
}

enum ReadError {
    Parse(String, Mode),
    InvalidRequest(Mode),
    Fatal(io::Error),
}

fn fatal(kind: io::ErrorKind, message: impl Into<String>) -> ReadError {
    ReadError::Fatal(io::Error::new(kind, message.into()))
}

/// Go `readLineLimited`: one `\n`-terminated line (kept), `eof` set when the
/// stream ended first.
fn read_line_limited<R: BufRead>(input: &mut R) -> io::Result<(Vec<u8>, bool)> {
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

fn go_trim_space(text: &str) -> &str {
    text.trim_matches(|c: char| c.is_whitespace())
}

fn looks_like_json_line(line: &str) -> bool {
    if serde_json::from_str::<Value>(line).is_ok() && go_check_valid(line.as_bytes()).is_ok() {
        return true;
    }
    match line.as_bytes().first() {
        None => false,
        Some(b'{' | b'[' | b'"' | b'-' | b't' | b'f' | b'n') => true,
        Some(byte) => byte.is_ascii_digit(),
    }
}

fn content_length_value(rest: &str) -> Result<i64, ReadError> {
    let value = go_trim_space(rest);
    value.parse::<i64>().map_err(|_| {
        fatal(
            io::ErrorKind::InvalidData,
            format!("invalid Content-Length: {}", go_quote(value)),
        )
    })
}

/// Go `readRequest`: a line of JSON or a `Content-Length` frame. `Ok(None)` is
/// a clean end of stream.
fn read_request<R: BufRead>(input: &mut R) -> Result<Option<(Request, Mode)>, ReadError> {
    let mut total_bytes = 0;
    let line = loop {
        let (raw, eof) = read_line_limited(input).map_err(ReadError::Fatal)?;
        total_bytes += raw.len();
        let text = String::from_utf8_lossy(&raw);
        let trimmed = go_trim_space(&text);
        if eof {
            if raw.is_empty() {
                return Ok(None);
            }
            break trimmed.to_owned();
        }
        if !trimmed.is_empty() {
            break trimmed.to_owned();
        }
    };

    if looks_like_json_line(&line) {
        return parse_json_request(line.as_bytes(), Mode::Line).map(Some);
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
    if let Some(rest) = line.strip_prefix("Content-Length:") {
        content_length = Some(content_length_value(rest)?);
    }
    loop {
        let (raw, eof) = read_line_limited(input).map_err(ReadError::Fatal)?;
        if eof {
            return Ok(None);
        }
        let text = String::from_utf8_lossy(&raw);
        let trimmed = text.trim_end_matches(['\r', '\n']);
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
        if let Some(rest) = trimmed.strip_prefix("Content-Length:") {
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
fn parse_json_request(data: &[u8], mode: Mode) -> Result<(Request, Mode), ReadError> {
    if let Err(message) = go_check_valid(data) {
        return Err(ReadError::Parse(message, mode));
    }
    let Ok(text) = std::str::from_utf8(data) else {
        return Err(ReadError::InvalidRequest(mode));
    };
    let Ok(Members(members)) = serde_json::from_str::<Members>(text) else {
        return Err(ReadError::InvalidRequest(mode));
    };
    let mut jsonrpc = String::new();
    let mut method = String::new();
    let mut id_raw: Option<String> = None;
    let mut params: Option<String> = None;
    let mut type_error = false;
    let mut has_id = false;
    let mut has_params = false;
    for (key, raw) in &members {
        has_id |= key == "id";
        has_params |= key == "params";
        let lower = key.to_ascii_lowercase();
        match lower.as_str() {
            "jsonrpc" | "method" => match serde_json::from_str::<Value>(raw.get()) {
                Ok(Value::String(value)) => {
                    if lower == "jsonrpc" {
                        jsonrpc = value;
                    } else {
                        method = value;
                    }
                }
                Ok(Value::Null) => {}
                _ => type_error = true,
            },
            "id" => id_raw = Some(raw.get().to_owned()),
            "params" => params = Some(raw.get().to_owned()),
            _ => {}
        }
    }
    let id_text = id_raw.unwrap_or_else(|| "null".to_owned());
    let id_valid = match serde_json::from_str::<Value>(&id_text) {
        Ok(Value::Null | Value::String(_) | Value::Number(_)) => true,
        // Literals serde cannot hold (e.g. huge exponents) are still numbers.
        Err(_) => id_text.starts_with(|c: char| c == '-' || c.is_ascii_digit()),
        Ok(_) => false,
    };
    if type_error
        || jsonrpc != "2.0"
        || method.is_empty()
        || (has_id && !id_valid)
        || (has_params && !params.as_deref().is_some_and(valid_params))
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
            params,
        },
        mode,
    ))
}

fn render_result(id: &str, result: &Value) -> String {
    format!(
        "{{\"jsonrpc\":\"2.0\",\"id\":{id},\"result\":{}}}",
        go_marshal_string(result)
    )
}

fn render_error(id: &str, code: i64, message: &str) -> String {
    let error = go_marshal_string(&json!({"code": code, "message": message}));
    format!("{{\"jsonrpc\":\"2.0\",\"id\":{id},\"error\":{error}}}")
}

fn write_frame<W: Write>(output: &mut W, mode: Mode, body: &str) -> io::Result<()> {
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

type SharedOutput<W> = Arc<Mutex<W>>;

fn write_shared<W: Write>(output: &SharedOutput<W>, mode: Mode, body: &str) -> io::Result<()> {
    let mut output = output
        .lock()
        .map_err(|_| io::Error::other("MCP output lock poisoned"))?;
    write_frame(&mut *output, mode, body)
}

pub fn serve_io_with_identity<R: BufRead, W: Write + Send>(
    mut input: R,
    output: W,
    room_dir: &Path,
    artifact_root: &Path,
    identity: Option<&symroom_core::identity::Identity>,
) -> io::Result<()> {
    let output: SharedOutput<W> = Arc::new(Mutex::new(output));
    let cancelled = Arc::new(AtomicBool::new(false));
    let (errors, worker_errors) = mpsc::channel::<io::Error>();

    thread::scope(|scope| {
        let mut in_flight = Vec::new();
        let join_all = |in_flight: &mut Vec<thread::ScopedJoinHandle<'_, ()>>| {
            for handle in in_flight.drain(..) {
                let _ = handle.join();
            }
        };
        let outcome = (|| -> io::Result<()> {
            loop {
                if let Ok(error) = worker_errors.try_recv() {
                    return Err(error);
                }
                match read_request(&mut input) {
                    Ok(None) => return Ok(()),
                    Err(ReadError::Parse(message, mode)) => {
                        join_all(&mut in_flight);
                        let body = render_error("null", -32700, &format!("Parse error: {message}"));
                        write_shared(&output, mode, &body)?;
                    }
                    Err(ReadError::InvalidRequest(mode)) => {
                        join_all(&mut in_flight);
                        let body = render_error("null", -32600, "Invalid Request");
                        write_shared(&output, mode, &body)?;
                    }
                    Err(ReadError::Fatal(error)) => {
                        return Err(io::Error::new(
                            error.kind(),
                            format!("mcpserver: read error: {error}"),
                        ));
                    }
                    Ok(Some((request, mode))) => {
                        if request.method == "tools/call" {
                            in_flight.retain(|handle| !handle.is_finished());
                            let output = Arc::clone(&output);
                            let errors = errors.clone();
                            let cancelled = Arc::clone(&cancelled);
                            in_flight.push(scope.spawn(move || {
                                let response = guarded_call(
                                    &request,
                                    room_dir,
                                    artifact_root,
                                    identity,
                                    &cancelled,
                                );
                                if request.has_id
                                    && let Err(error) = write_shared(&output, mode, &response)
                                {
                                    let _ = errors.send(error);
                                }
                            }));
                        } else {
                            let response = handle_sync(&request);
                            if request.has_id {
                                write_shared(&output, mode, &response)?;
                            }
                        }
                    }
                }
            }
        })();
        // Go cancels the serve context on an aborted stream so waiting tools
        // return `context canceled`; a clean EOF lets in-flight tools finish.
        if outcome.is_err() {
            cancelled.store(true, Ordering::SeqCst);
        }
        join_all(&mut in_flight);
        drop(errors);
        match outcome {
            Ok(()) => worker_errors.try_recv().map_or(Ok(()), Err),
            Err(error) => Err(error),
        }
    })
}

fn handle_sync(request: &Request) -> String {
    let id = request.id.as_str();
    match request.method.as_str() {
        "initialize" => render_result(
            id,
            &json!({
                "protocolVersion": "2024-11-05",
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "symroom", "version": "0.1.0"},
                "instructions": INSTRUCTIONS,
            }),
        ),
        "ping" => render_result(id, &json!({})),
        "tools/list" => render_result(id, &json!({"tools": tools()})),
        method => render_error(id, -32601, &format!("Method not found: {method}")),
    }
}

/// Go `handleRequest` recovers handler panics into `-32603`.
fn guarded_call(
    request: &Request,
    room_dir: &Path,
    artifact_root: &Path,
    identity: Option<&symroom_core::identity::Identity>,
    cancelled: &AtomicBool,
) -> String {
    let outcome = panic::catch_unwind(AssertUnwindSafe(|| {
        call(request, room_dir, artifact_root, identity, cancelled)
    }));
    outcome
        .unwrap_or_else(|_| render_error(&request.id, -32603, "Internal error: handler panicked"))
}

/// A failing tool handler: either a tool error result or a Go-style panic.
enum Failure {
    Tool(String),
    Panic,
}

impl From<String> for Failure {
    fn from(message: String) -> Self {
        Self::Tool(message)
    }
}

impl From<&str> for Failure {
    fn from(message: &str) -> Self {
        Self::Tool(message.to_owned())
    }
}

fn tool_result(id: &str, text: String) -> String {
    render_result(
        id,
        &json!({"content":[{"type":"text","text":text}],"isError":false}),
    )
}

fn tool_error(id: &str, text: String) -> String {
    render_result(
        id,
        &json!({"content":[{"type":"text","text":text}],"isError":true}),
    )
}

/// Go `handleToolsCall` parameter decoding. Returns the tool name and the raw
/// `arguments` member.
fn decode_call_params(params: Option<&str>) -> Result<(String, Option<String>), String> {
    let Some(params) = params else {
        return Err("unexpected end of JSON input".to_owned());
    };
    let Ok(value) = serde_json::from_str::<Value>(params) else {
        return Err("unexpected end of JSON input".to_owned());
    };
    match &value {
        Value::Null => return Ok((String::new(), None)),
        Value::Object(_) => {}
        other => {
            return Err(format!(
                "json: cannot unmarshal {} into Go value of type {GO_CALL_PARAMS_TYPE}",
                json_kind(other)
            ));
        }
    }
    let Ok(Members(members)) = serde_json::from_str::<Members>(params) else {
        return Err("unexpected end of JSON input".to_owned());
    };
    let mut name = String::new();
    let mut arguments = None;
    let mut first_error: Option<String> = None;
    for (key, raw) in &members {
        let value: Value = serde_json::from_str(raw.get()).unwrap_or(Value::Bool(false));
        if key.eq_ignore_ascii_case("name") {
            match value {
                Value::String(text) => name = text,
                Value::Null => {}
                other => {
                    first_error.get_or_insert_with(|| {
                        format!(
                            "json: cannot unmarshal {} into Go struct field .name of type string",
                            json_kind(&other)
                        )
                    });
                }
            }
        } else if key.eq_ignore_ascii_case("arguments") {
            arguments = Some(raw.get().to_owned());
        } else if key.eq_ignore_ascii_case("_meta") {
            match value {
                Value::Object(_) | Value::Null => {}
                other => {
                    first_error.get_or_insert_with(|| {
                        format!(
                            "json: cannot unmarshal {} into Go struct field ._meta of type map[string]interface {{}}",
                            json_kind(&other)
                        )
                    });
                }
            }
        }
    }
    first_error.map_or(Ok((name, arguments)), Err)
}

fn call(
    request: &Request,
    room_dir: &Path,
    artifact_root: &Path,
    identity: Option<&symroom_core::identity::Identity>,
    cancelled: &AtomicBool,
) -> String {
    let id = request.id.as_str();
    let (name, arguments) = match decode_call_params(request.params.as_deref()) {
        Ok(decoded) => decoded,
        Err(message) => {
            return render_error(id, -32602, &format!("Invalid params: {message}"));
        }
    };
    if !tools()
        .as_array()
        .is_some_and(|items| items.iter().any(|item| item["name"] == name.as_str()))
    {
        return render_error(id, -32601, &format!("Unknown tool: {name}"));
    }
    let mut args = GoObject::decode(arguments.as_deref());
    let outcome: Result<String, Failure> = match name.as_str() {
        "room_status" => room_status(room_dir).map_err(Failure::Tool),
        "room_journal_tail" => journal_tail(room_dir, &mut args),
        "room_artifact_list" => artifact_list(room_dir, artifact_root).map_err(Failure::Tool),
        "room_run_wait" => run_wait(room_dir, &mut args, cancelled),
        "room_note_post" => mutate_note(room_dir, &mut args, identity),
        "room_artifact_link" => mutate_artifact(room_dir, artifact_root, &mut args, identity),
        "room_run_request" => mutate_run(room_dir, &mut args, identity),
        _ => mutate_checkpoint(room_dir, &mut args, identity),
    };
    match outcome {
        Ok(text) => tool_result(id, text),
        Err(Failure::Tool(message)) => tool_error(id, message),
        Err(Failure::Panic) => render_error(id, -32603, "Internal error: handler panicked"),
    }
}

fn journal_tail(room_dir: &Path, args: &mut GoObject) -> Result<String, Failure> {
    let mut limit = args.int("limit");
    if limit <= 0 {
        limit = 20;
    }
    let limit = usize::try_from(limit).unwrap_or(usize::MAX);
    let mut events =
        symroom_core::journal::merge_all(room_dir).map_err(|error| error.to_string())?;
    if events.len() > limit {
        events.drain(..events.len() - limit);
    }
    if events.is_empty() {
        Ok("null".to_owned())
    } else {
        Ok(go_html_escape(
            &serde_json::to_string(&events).expect("journal events serialize"),
        ))
    }
}

/// Go `run.Wait` inside a per-call timeout context, polling in short slices so
/// a cancelled stream stops waiting like Go's `ctx.Done()`.
fn run_wait(
    room_dir: &Path,
    args: &mut GoObject,
    cancelled: &AtomicBool,
) -> Result<String, Failure> {
    let run_id = args.string("run_id");
    let mut seconds = args.float("timeout_seconds");
    if args.failed || run_id.is_empty() {
        return Err("run_id is required".into());
    }
    if seconds <= 0.0 {
        seconds = 30.0;
    }
    let timeout =
        Duration::try_from_secs_f64(seconds).unwrap_or(Duration::from_secs(u32::MAX.into()));
    let started = Instant::now();
    loop {
        let remaining = timeout.saturating_sub(started.elapsed());
        let slice = remaining.min(Duration::from_millis(100));
        match symroom_core::runs::wait(room_dir, &run_id, slice) {
            Ok(run) => {
                return Ok(go_html_escape(
                    &serde_json::to_string(&run).expect("run serializes"),
                ));
            }
            Err(symroom_core::runs::RunWaitError::Cancelled) => {
                return Err("run was denied: run cancelled".into());
            }
            Err(symroom_core::runs::RunWaitError::Denied) => {
                let error = symroom_core::runs::get(room_dir, &run_id)
                    .map(|run| run.error.unwrap_or_default())
                    .unwrap_or_default();
                return Err(format!("run was denied: {error}").into());
            }
            Err(symroom_core::runs::RunWaitError::Timeout) => {
                if cancelled.load(Ordering::SeqCst) {
                    return Err("context canceled".into());
                }
                if started.elapsed() >= timeout {
                    return Err("wait timed out before approval decision".into());
                }
            }
        }
    }
}

/// Go formats an OS error as `<op> <path>: <lower-case strerror>`.
fn go_io_error(operation: &str, path: &Path, error: &io::Error) -> String {
    let mut text = error.to_string();
    if let Some(index) = text.rfind(" (os error ") {
        text.truncate(index);
    }
    let mut chars = text.chars();
    let message = chars
        .next()
        .map(|first| first.to_lowercase().collect::<String>() + chars.as_str())
        .unwrap_or_default();
    let operation = if error.raw_os_error() == Some(21) {
        "read"
    } else {
        operation
    };
    format!("{operation} {}: {message}", path.display())
}

struct RoomConfig {
    id: String,
    created: String,
    root_pubkey: String,
    root_event: String,
}

/// Go `room.ReadRoomConfig`: line-based `key = value` parsing.
fn read_room_config(room_dir: &Path) -> Result<RoomConfig, String> {
    let path = room_dir.join("room.toml");
    let content = fs::read_to_string(&path)
        .or_else(|error| {
            // Go reads bytes, so invalid UTF-8 is not an error.
            if error.kind() == io::ErrorKind::InvalidData {
                fs::read(&path).map(|bytes| String::from_utf8_lossy(&bytes).into_owned())
            } else {
                Err(error)
            }
        })
        .map_err(|error| format!("read room.toml: {}", go_io_error("open", &path, &error)))?;
    let mut config = RoomConfig {
        id: String::new(),
        created: String::new(),
        root_pubkey: String::new(),
        root_event: String::new(),
    };
    for line in content.split('\n') {
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        let value = go_trim_space(value).trim_matches('"').to_owned();
        match go_trim_space(key) {
            "id" => config.id = value,
            "created" => config.created = value,
            "root_pubkey" => config.root_pubkey = value,
            "root_event" => config.root_event = value,
            _ => {}
        }
    }
    Ok(config)
}

fn mutate_note(
    room_dir: &Path,
    args: &mut GoObject,
    identity: Option<&symroom_core::identity::Identity>,
) -> Result<String, Failure> {
    let text = args.string("text");
    if args.failed || text.is_empty() {
        return Err("text is required".into());
    }
    let room = read_room_config(room_dir)?.id;
    let stats =
        symroom_core::journal::read_journal_stats(room_dir).map_err(|error| error.to_string())?;
    let identity = identity.ok_or(Failure::Panic)?;
    if stats
        .member_state
        .members
        .get(&identity.member_id)
        .is_some_and(|member| member.role == "observer")
    {
        return Err("observer role has read-only access".into());
    }
    let author = symroom_core::journal::author_stats(room_dir, &identity.member_id)
        .map_err(|error| error.to_string())?;
    let body = go_body(&json!({"text": text}))?;
    let event = append_signed(
        room_dir,
        identity,
        room,
        unique_event_id(),
        "note.posted",
        body,
        author.seq + 1,
        author.prev,
        stats.max_lamport + 1,
    )?;
    Ok(event_text(&event))
}

/// Go `MakeRelativePath`: absolute, lexically cleaned, relative to the root.
fn make_relative_path(target: &Path, root: &Path) -> Result<String, String> {
    let absolute = |path: &Path| -> Result<PathBuf, String> {
        if path.is_absolute() {
            Ok(lexical_normalize(path))
        } else {
            std::env::current_dir()
                .map(|cwd| lexical_normalize(&cwd.join(path)))
                .map_err(|error| error.to_string())
        }
    };
    let target = absolute(target)?;
    let root = absolute(root)?;
    let rel = target
        .strip_prefix(&root)
        .map_err(|_| "path is outside artifact root".to_owned())?;
    let rel = if rel.as_os_str().is_empty() {
        ".".to_owned()
    } else {
        rel.to_string_lossy().into_owned()
    };
    if rel.starts_with("..") {
        return Err("path is outside artifact root".to_owned());
    }
    Ok(rel)
}

/// Go `filepath.Join(root, relative)`: relative parts never escape via a
/// leading separator.
fn join_under(root: &Path, relative: &str) -> PathBuf {
    lexical_normalize(&root.join(relative.trim_start_matches('/')))
}

fn mutate_artifact(
    room_dir: &Path,
    artifact_root: &Path,
    args: &mut GoObject,
    identity: Option<&symroom_core::identity::Identity>,
) -> Result<String, Failure> {
    let path = args.string("path");
    let title = args.string("title");
    if args.failed || path.is_empty() {
        return Err("path is required".into());
    }
    let artifact_root = if artifact_root.as_os_str().is_empty() {
        room_dir
    } else {
        artifact_root
    };
    let rel = make_relative_path(Path::new(&path), artifact_root)?;
    let absolute = join_under(artifact_root, &rel);
    let contents = fs::read(&absolute)
        .map_err(|error| format!("compute sha256: {}", go_io_error("open", &absolute, &error)))?;
    let hash = hex::encode(Sha256::digest(contents));
    let digest = hex::encode(Sha256::digest(format!("{rel}{hash}").as_bytes()));
    let artifact_id = format!("art_{}", &digest[..16]);
    let title = if title.is_empty() {
        Path::new(&rel)
            .file_name()
            .unwrap_or_default()
            .to_string_lossy()
            .into_owned()
    } else {
        title
    };
    let identity = identity.ok_or(Failure::Panic)?;
    let body = go_body(
        &json!({"artifact_id":artifact_id,"path":rel,"sha256":hash,"title":title,"symdesk_id":""}),
    )?;
    let event = append_signed(
        room_dir,
        identity,
        "rm_test".to_owned(),
        format!("ev_{}", &artifact_id[4..]),
        "artifact.linked",
        body,
        0,
        String::new(),
        0,
    )?;
    Ok(event_text(&event))
}

fn mutate_run(
    room_dir: &Path,
    args: &mut GoObject,
    identity: Option<&symroom_core::identity::Identity>,
) -> Result<String, Failure> {
    let title = args.string("title");
    let plan_file = args.string("plan_file");
    let adapter = args.string("adapter");
    if args.failed || title.is_empty() {
        return Err("title is required".into());
    }
    read_room_config(room_dir)?;
    let identity = identity.ok_or(Failure::Panic)?;
    let event = symroom_core::runs::request(room_dir, &title, &plan_file, &adapter, identity)
        .map_err(|error| error.to_string())?;
    Ok(event_text(&event))
}

fn mutate_checkpoint(
    room_dir: &Path,
    args: &mut GoObject,
    identity: Option<&symroom_core::identity::Identity>,
) -> Result<String, Failure> {
    let run_id = args.string("run_id");
    let question = args.string("question");
    if args.failed || run_id.is_empty() || question.is_empty() {
        return Err("run_id and question are required".into());
    }
    let room_id = read_room_config(room_dir)?.id;
    let identity = identity.ok_or(Failure::Panic)?;
    let checkpoint_id = format!("chk_{}", &unique_event_id()[3..19]);
    let body =
        go_body(&json!({"checkpoint_id":checkpoint_id,"run_id":run_id,"question":question}))?;
    let event = append_signed(
        room_dir,
        identity,
        room_id,
        format!("ev_{}", &checkpoint_id[4..]),
        "checkpoint.requested",
        body,
        0,
        String::new(),
        0,
    )?;
    Ok(event_text(&event))
}

fn go_body(value: &Value) -> Result<Box<RawValue>, String> {
    // `json.Marshal(map[string]string{...})`: sorted keys, HTML-safe strings.
    RawValue::from_string(go_marshal_string(value)).map_err(|error| error.to_string())
}

fn event_text(event: &symroom_core::event::Event) -> String {
    go_html_escape(&serde_json::to_string(event).expect("event serializes"))
}

#[expect(
    clippy::too_many_arguments,
    reason = "Go event fields are explicit at each tool"
)]
fn append_signed(
    room_dir: &Path,
    identity: &symroom_core::identity::Identity,
    room: String,
    id: String,
    kind: &str,
    body: Box<serde_json::value::RawValue>,
    seq: u64,
    prev: String,
    lamport: u64,
) -> Result<symroom_core::event::Event, String> {
    let mut event = symroom_core::event::Event {
        v: symroom_core::event::CURRENT_VERSION,
        id,
        room,
        author: identity.member_id.clone(),
        seq,
        prev,
        lamport,
        ts: symroom_core::event::format_timestamp(time::OffsetDateTime::now_utc()),
        kind: kind.to_owned(),
        body,
        sig: None,
    };
    if seq == 0 {
        let author = symroom_core::journal::author_stats(room_dir, &identity.member_id)
            .map_err(|error| error.to_string())?;
        event.seq = author.seq + 1;
        event.prev = author.prev;
        let stats = symroom_core::journal::read_journal_stats(room_dir)
            .map_err(|error| error.to_string())?;
        event.lamport = stats.max_lamport + 1;
    }
    event.sign(identity).map_err(|error| error.to_string())?;
    symroom_core::journal::append_event(room_dir, &event).map_err(|error| error.to_string())?;
    Ok(event)
}

fn lexical_normalize(path: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for component in path.components() {
        match component {
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                out.pop();
            }
            other => out.push(other.as_os_str()),
        }
    }
    out
}

fn unique_event_id() -> String {
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let nonce = NEXT.fetch_add(1, Ordering::Relaxed);
    let stamp = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    let digest = Sha256::digest(format!("{stamp}:{}:{nonce}", std::process::id()).as_bytes());
    format!("ev_{}", &hex::encode(digest)[..20])
}

#[derive(Serialize)]
struct RoomConfigView {
    schema_version: i32,
    id: String,
    created: String,
    root_pubkey: String,
    root_event: String,
}

#[derive(Serialize)]
struct RoomStatus {
    max_lamport: u64,
    room: RoomConfigView,
}

fn room_status(room_dir: &Path) -> Result<String, String> {
    let config = read_room_config(room_dir)?;
    let stats =
        symroom_core::journal::read_journal_stats(room_dir).map_err(|error| error.to_string())?;
    let rendered = serde_json::to_string(&RoomStatus {
        max_lamport: stats.max_lamport,
        room: RoomConfigView {
            schema_version: 0,
            id: config.id,
            created: config.created,
            root_pubkey: config.root_pubkey,
            root_event: config.root_event,
        },
    })
    .map_err(|error| error.to_string())?;
    Ok(go_html_escape(&rendered))
}

#[derive(Serialize)]
struct ArtifactRef {
    id: String,
    path: String,
    sha256: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    symdesk_id: String,
    status: String,
}

/// Go `json.Unmarshal(ev.Body, &struct{string fields})`; `None` when Go would
/// report an error (and the event is therefore skipped).
fn decode_body_strings(body: &str, names: &[&str]) -> Option<Vec<String>> {
    let mut object = GoObject::decode(Some(body));
    let values = names.iter().map(|name| object.string(name)).collect();
    (!object.failed).then_some(values)
}

fn artifact_list(room_dir: &Path, artifact_root: &Path) -> Result<String, String> {
    let artifact_root = if artifact_root.as_os_str().is_empty() {
        room_dir
    } else {
        artifact_root
    };
    let events = symroom_core::journal::merge_all(room_dir).map_err(|error| error.to_string())?;
    let mut active = BTreeMap::<String, (String, String, String, String)>::new();
    for event in events {
        match event.kind.as_str() {
            "artifact.linked" => {
                if let Some(fields) = decode_body_strings(
                    event.body.get(),
                    &["artifact_id", "path", "sha256", "title", "symdesk_id"],
                ) && let [id, path, hash, title, symdesk_id] = &fields[..]
                {
                    active.insert(
                        id.clone(),
                        (
                            path.clone(),
                            hash.clone(),
                            title.clone(),
                            symdesk_id.clone(),
                        ),
                    );
                }
            }
            "artifact.unlinked" => {
                if let Some(fields) = decode_body_strings(event.body.get(), &["artifact_id"]) {
                    active.remove(&fields[0]);
                }
            }
            _ => {}
        }
    }
    if active.is_empty() {
        return Ok("null".to_owned());
    }
    let refs = active
        .into_iter()
        .map(|(id, (path, expected_hash, title, symdesk_id))| {
            let file_path = join_under(artifact_root, &path);
            let status = match fs::read(&file_path) {
                Ok(bytes) => {
                    if format!("{:x}", Sha256::digest(&bytes)) == expected_hash {
                        "ok"
                    } else {
                        "modified"
                    }
                }
                Err(error) if error.kind() == io::ErrorKind::NotFound => "missing",
                Err(_) => "error",
            };
            ArtifactRef {
                id,
                path,
                sha256: expected_hash,
                title,
                symdesk_id,
                status: status.to_owned(),
            }
        })
        .collect::<Vec<_>>();
    Ok(go_html_escape(
        &serde_json::to_string(&refs).expect("artifact refs serialize"),
    ))
}
