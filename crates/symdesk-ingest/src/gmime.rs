//! Ports of the Go standard-library pieces `readEML` depends on:
//! `net/mail.ReadMessage` (over `net/textproto` headers), `mime.ParseMediaType`,
//! `mime/multipart.Reader`, `mime/quotedprintable.Reader` and the
//! `encoding/base64` stream decoder. Data is processed as whole byte slices
//! with an explicit end-of-stream status instead of Go's incremental readers;
//! the Go readers are written to be buffer-independent, so the logical result
//! is the same. Documented limits are listed in `docs/rust-port`.
use std::borrow::Cow;
use std::collections::HashMap;

pub const MAX_NESTING: usize = 64;
const MAX_MIME_HEADER_SIZE: i64 = 10 << 20;
const MAX_MIME_HEADERS: i64 = 10000;
const PEEK_BUFFER: usize = 4096;

/// How a stream ends after its last byte.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum End {
    Eof,
    Err(String),
}

impl End {
    fn unexpected() -> Self {
        End::Err("unexpected EOF".into())
    }
    pub fn error(&self) -> Option<&str> {
        match self {
            End::Eof => None,
            End::Err(e) => Some(e),
        }
    }
}

#[derive(Clone, Debug)]
pub struct Stream<'a> {
    pub data: Cow<'a, [u8]>,
    pub end: End,
}

// ---------------------------------------------------------------- quoting

/// Approximation of Go's `%q` for byte strings.
pub fn go_quote(b: &[u8]) -> String {
    let mut out = String::from("\"");
    let mut i = 0;
    while i < b.len() {
        let c = b[i];
        if c < 0x80 {
            i += 1;
            match c {
                b'"' => out.push_str("\\\""),
                b'\\' => out.push_str("\\\\"),
                0x07 => out.push_str("\\a"),
                0x08 => out.push_str("\\b"),
                0x0c => out.push_str("\\f"),
                b'\n' => out.push_str("\\n"),
                b'\r' => out.push_str("\\r"),
                b'\t' => out.push_str("\\t"),
                0x0b => out.push_str("\\v"),
                0x20..=0x7e => out.push(c as char),
                _ => out.push_str(&format!("\\x{c:02x}")),
            }
            continue;
        }
        let want = match c {
            0xc2..=0xdf => 2,
            0xe0..=0xef => 3,
            0xf0..=0xf4 => 4,
            _ => 0,
        };
        let ch = (want > 0 && i + want <= b.len())
            .then(|| std::str::from_utf8(&b[i..i + want]).ok())
            .flatten()
            .and_then(|s| s.chars().next());
        match ch {
            Some(ch) => {
                i += want;
                if ch.is_alphanumeric() || (!ch.is_control() && !ch.is_whitespace()) {
                    out.push(ch);
                } else if (ch as u32) < 0x10000 {
                    out.push_str(&format!("\\u{:04x}", ch as u32));
                } else {
                    out.push_str(&format!("\\U{:08x}", ch as u32));
                }
            }
            None => {
                i += 1;
                out.push_str(&format!("\\x{c:02x}"));
            }
        }
    }
    out.push('"');
    out
}

// -------------------------------------------------------------- byte reader

struct Rd<'a> {
    data: &'a [u8],
    end: &'a End,
    pos: usize,
}

impl<'a> Rd<'a> {
    fn new(data: &'a [u8], end: &'a End) -> Self {
        Self { data, end, pos: 0 }
    }
    fn end_error(&self) -> String {
        self.end.error().unwrap_or("EOF").to_string()
    }
    /// `bufio.Reader.ReadLine` semantics: a final unterminated line is
    /// returned without error; the stream error follows on the next call.
    fn read_line(&mut self) -> Result<Vec<u8>, String> {
        if self.pos >= self.data.len() {
            return Err(self.end_error());
        }
        let rest = &self.data[self.pos..];
        let (line, adv) = match rest.iter().position(|b| *b == b'\n') {
            Some(i) => {
                let mut l = &rest[..i];
                if l.last() == Some(&b'\r') {
                    l = &l[..l.len() - 1];
                }
                (l, i + 1)
            }
            None => (rest, rest.len()),
        };
        self.pos += adv;
        Ok(line.to_vec())
    }
    fn peek(&self) -> Option<u8> {
        self.data.get(self.pos).copied()
    }
}

fn trim_sp_tab(s: &[u8]) -> &[u8] {
    let mut a = 0;
    while a < s.len() && (s[a] == b' ' || s[a] == b'\t') {
        a += 1;
    }
    let mut z = s.len();
    while z > a && (s[z - 1] == b' ' || s[z - 1] == b'\t') {
        z -= 1;
    }
    &s[a..z]
}

/// `textproto.Reader.readContinuedLineSlice`. `lim < 0` is unlimited.
fn read_continued_line(r: &mut Rd<'_>, mut lim: i64, must_colon: bool) -> Result<Vec<u8>, String> {
    let line = r.read_line()?;
    if lim >= 0 && line.len() as i64 > lim {
        return Err("message too large".into());
    }
    if line.is_empty() {
        return Ok(line);
    }
    if must_colon && !line.contains(&b':') {
        return Err(format!(
            "malformed MIME header: missing colon: {}",
            go_quote(&line)
        ));
    }
    let mut buf = trim_sp_tab(&line).to_vec();
    if lim < 0 {
        lim = i64::MAX;
    } else {
        lim -= buf.len() as i64;
    }
    loop {
        let mut n = 0;
        while let Some(c) = r.peek() {
            if c != b' ' && c != b'\t' {
                break;
            }
            r.pos += 1;
            n += 1;
        }
        if n == 0 {
            break;
        }
        buf.push(b' ');
        if buf.len() as i64 >= lim {
            return Err("message too large".into());
        }
        let line = r.read_line()?;
        let remaining = lim - buf.len() as i64;
        if line.len() as i64 > remaining {
            return Err("message too large".into());
        }
        buf.extend_from_slice(trim_sp_tab(&line));
    }
    Ok(buf)
}

fn valid_header_field_byte(c: u8) -> bool {
    c.is_ascii_alphanumeric() || b"!#$%&'*+-.^_`|~".contains(&c)
}

fn valid_header_value_byte(c: u8) -> bool {
    // textproto: 0x20..=0x7e, tab, and any byte >= 0x80 (obs-text).
    (c >= 0x20 && c != 0x7f) || c == b'\t'
}

fn canonical_key_loose(s: &[u8]) -> Vec<u8> {
    // textproto.CanonicalMIMEHeaderKey: unchanged if any byte is invalid.
    if !s.iter().all(|c| valid_header_field_byte(*c)) {
        return s.to_vec();
    }
    canon(s)
}

fn canon(s: &[u8]) -> Vec<u8> {
    let mut upper = true;
    s.iter()
        .map(|&c| {
            let o = if upper && c.is_ascii_lowercase() {
                c.to_ascii_uppercase()
            } else if !upper && c.is_ascii_uppercase() {
                c.to_ascii_lowercase()
            } else {
                c
            };
            upper = o == b'-';
            o
        })
        .collect()
}

/// `textproto.canonicalMIMEHeaderKey` (strict variant used by multipart).
fn canonical_key_strict(a: &[u8]) -> Option<Vec<u8>> {
    if a.is_empty() {
        return None;
    }
    let mut no_canon = false;
    for &c in a {
        if valid_header_field_byte(c) {
            continue;
        }
        if c == b' ' {
            no_canon = true;
            continue;
        }
        return None;
    }
    Some(if no_canon { a.to_vec() } else { canon(a) })
}

#[derive(Clone, Debug, Default)]
pub struct Header(HashMap<Vec<u8>, Vec<Vec<u8>>>);

impl Header {
    pub fn get(&self, key: &str) -> Vec<u8> {
        let k = canonical_key_loose(key.as_bytes());
        self.0
            .get(&k)
            .and_then(|v| v.first())
            .cloned()
            .unwrap_or_default()
    }
    fn del(&mut self, key: &str) {
        let k = canonical_key_loose(key.as_bytes());
        self.0.remove(&k);
    }
    fn push(&mut self, k: Vec<u8>, v: Vec<u8>) {
        self.0.entry(k).or_default().push(v);
    }
    fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
}

/// `net/mail.ReadMessage`: returns the header and the body offset.
pub fn read_message(data: &[u8]) -> Result<(Header, usize), String> {
    let end = End::Eof;
    let mut r = Rd::new(data, &end);
    let mut h = Header::default();
    let err: Option<String>;
    if matches!(r.peek(), Some(b' ' | b'\t')) {
        let line = r.read_line()?;
        return Err(format!(
            "malformed initial line: {}",
            String::from_utf8_lossy(&line)
        ));
    }
    loop {
        let kv = read_continued_line(&mut r, -1, false);
        let (kv, e) = match kv {
            Ok(kv) => (kv, None),
            Err(e) => (Vec::new(), Some(e)),
        };
        if kv.is_empty() {
            err = e;
            break;
        }
        let Some(i) = kv.iter().position(|b| *b == b':') else {
            return Err(format!(
                "malformed header line: {}",
                String::from_utf8_lossy(&kv)
            ));
        };
        let key = canonical_key_loose(&kv[..i]);
        if key.is_empty() {
            continue;
        }
        let mut v = &kv[i + 1..];
        while let Some((&c, rest)) = v.split_first() {
            if c == b' ' || c == b'\t' {
                v = rest;
            } else {
                break;
            }
        }
        h.push(key, v.to_vec());
    }
    if let Some(e) = err
        && (e != "EOF" || h.is_empty())
    {
        return Err(e);
    }
    Ok((h, r.pos))
}

/// `textproto.readMIMEHeader` as used for multipart parts.
fn read_mime_header(r: &mut Rd<'_>) -> Result<Header, String> {
    let mut max_memory = MAX_MIME_HEADER_SIZE;
    let mut max_headers = MAX_MIME_HEADERS;
    let mut m = Header::default();
    if matches!(r.peek(), Some(b' ' | b'\t')) {
        let line = r.read_line()?;
        if line.len() > 80 {
            return Err("message too large".into());
        }
        return Err(format!(
            "malformed MIME header initial line: {}",
            go_quote(&line)
        ));
    }
    loop {
        let kv = read_continued_line(r, max_memory, true)?;
        if kv.is_empty() {
            return Ok(m);
        }
        let bad = || format!("malformed MIME header line: {}", go_quote(&kv));
        let Some(i) = kv.iter().position(|b| *b == b':') else {
            return Err(bad());
        };
        let Some(key) = canonical_key_strict(&kv[..i]) else {
            return Err(bad());
        };
        let v = &kv[i + 1..];
        if !v.iter().all(|c| valid_header_value_byte(*c)) {
            return Err(bad());
        }
        max_headers -= 1;
        if max_headers < 0 {
            return Err("message too large".into());
        }
        let mut value = v;
        while let Some((&c, rest)) = value.split_first() {
            if c == b' ' || c == b'\t' {
                value = rest;
            } else {
                break;
            }
        }
        if !m.0.contains_key(&key) {
            max_memory -= key.len() as i64 + 200;
        }
        max_memory -= value.len() as i64;
        if max_memory < 0 {
            return Err("message too large".into());
        }
        m.push(key, value.to_vec());
    }
}

// -------------------------------------------------------------- media types

fn is_token_char(c: u8) -> bool {
    c.is_ascii_alphanumeric() || b"!#$%&'*+-.^_`{|}~".contains(&c)
}

fn is_tspecial(c: u8) -> bool {
    b"()<>@,;:\\\"/[]?=".contains(&c)
}

fn consume_token(v: &[u8]) -> (&[u8], &[u8]) {
    for (i, c) in v.iter().enumerate() {
        if !is_token_char(*c) {
            return (&v[..i], &v[i..]);
        }
    }
    (v, &[])
}

/// Go `unicode.IsSpace` over a UTF-8 prefix; returns its byte length.
fn space_len(b: &[u8]) -> usize {
    let Some(&c) = b.first() else {
        return 0;
    };
    if c < 0x80 {
        return usize::from(matches!(c, b'\t' | b'\n' | 0x0b | 0x0c | b'\r' | b' '));
    }
    let want = match c {
        0xc2..=0xdf => 2,
        0xe0..=0xef => 3,
        0xf0..=0xf4 => 4,
        _ => return 0,
    };
    if b.len() < want {
        return 0;
    }
    match std::str::from_utf8(&b[..want]) {
        Ok(s) if s.chars().next().is_some_and(char::is_whitespace) => want,
        _ => 0,
    }
}

fn trim_left_space(mut v: &[u8]) -> &[u8] {
    loop {
        let n = space_len(v);
        if n == 0 {
            return v;
        }
        v = &v[n..];
    }
}

fn trim_space(v: &[u8]) -> &[u8] {
    let mut v = trim_left_space(v);
    // trailing spaces: decode backwards by trying each possible width
    'outer: loop {
        for w in 1..=4.min(v.len()) {
            let tail = &v[v.len() - w..];
            if space_len(tail) == w {
                v = &v[..v.len() - w];
                continue 'outer;
            }
        }
        return v;
    }
}

fn consume_value(v: &[u8]) -> (Vec<u8>, &[u8]) {
    if v.is_empty() {
        return (Vec::new(), v);
    }
    if v[0] != b'"' {
        let (t, rest) = consume_token(v);
        return (t.to_vec(), rest);
    }
    let mut buf = Vec::new();
    let mut i = 1;
    while i < v.len() {
        let r = v[i];
        if r == b'"' {
            return (buf, &v[i + 1..]);
        }
        if r == b'\\' && i + 1 < v.len() && is_tspecial(v[i + 1]) {
            buf.push(v[i + 1]);
            i += 2;
            continue;
        }
        if r == b'\r' || r == b'\n' {
            return (Vec::new(), v);
        }
        buf.push(v[i]);
        i += 1;
    }
    (Vec::new(), v)
}

type Param<'a> = (Vec<u8>, Vec<u8>, &'a [u8]);

fn consume_media_param(v: &[u8]) -> Param<'_> {
    let rest = trim_left_space(v);
    let Some(rest) = rest.strip_prefix(b";") else {
        return (Vec::new(), Vec::new(), v);
    };
    let rest = trim_left_space(rest);
    let (param, rest) = consume_token(rest);
    let param = param.to_ascii_lowercase();
    if param.is_empty() {
        return (Vec::new(), Vec::new(), v);
    }
    let rest = trim_left_space(rest);
    let Some(rest) = rest.strip_prefix(b"=") else {
        return (Vec::new(), Vec::new(), v);
    };
    let rest = trim_left_space(rest);
    let (value, rest2) = consume_value(rest);
    if value.is_empty() && rest2.len() == rest.len() {
        return (Vec::new(), Vec::new(), v);
    }
    (param, value, rest2)
}

fn ishex(c: u8) -> bool {
    c.is_ascii_hexdigit()
}

fn percent_hex_unescape(s: &[u8]) -> Option<Vec<u8>> {
    let mut i = 0;
    let mut percents = 0;
    while i < s.len() {
        if s[i] != b'%' {
            i += 1;
            continue;
        }
        percents += 1;
        if i + 2 >= s.len() || !ishex(s[i + 1]) || !ishex(s[i + 2]) {
            return None;
        }
        i += 3;
    }
    if percents == 0 {
        return Some(s.to_vec());
    }
    let mut out = Vec::new();
    let mut i = 0;
    while i < s.len() {
        if s[i] == b'%' {
            let h = |c: u8| (c as char).to_digit(16).unwrap() as u8;
            out.push(h(s[i + 1]) << 4 | h(s[i + 2]));
            i += 3;
        } else {
            out.push(s[i]);
            i += 1;
        }
    }
    Some(out)
}

fn decode_2231_enc(v: &[u8]) -> Option<Vec<u8>> {
    let i = v.iter().position(|b| *b == b'\'')?;
    let charset = v[..i].to_ascii_lowercase();
    let rest = &v[i + 1..];
    let j = rest.iter().position(|b| *b == b'\'')?;
    let other = &rest[j + 1..];
    if charset != b"us-ascii" && charset != b"utf-8" {
        return None;
    }
    percent_hex_unescape(other)
}

#[derive(Debug, PartialEq, Eq)]
pub struct MediaType {
    pub media_type: String,
    pub params: HashMap<String, Vec<u8>>,
    pub err: bool,
}

fn go_lower(s: &[u8]) -> Vec<u8> {
    match std::str::from_utf8(s) {
        Ok(t) => t
            .chars()
            .flat_map(|c| {
                if c == '\u{130}' {
                    vec!['i']
                } else {
                    let l: Vec<char> = c.to_lowercase().collect();
                    if l.len() == 1 { l } else { vec![c] }
                }
            })
            .collect::<String>()
            .into_bytes(),
        Err(_) => "\u{fffd}".as_bytes().to_vec(),
    }
}

fn check_media_type_disposition(s: &[u8]) -> bool {
    let (typ, rest) = consume_token(s);
    if typ.is_empty() {
        return false;
    }
    if rest.is_empty() {
        return true;
    }
    let Some(rest) = rest.strip_prefix(b"/") else {
        return false;
    };
    let (sub, rest) = consume_token(rest);
    !sub.is_empty() && rest.is_empty()
}

/// `mime.ParseMediaType`. On error the Go function still returns a media
/// type only for `ErrInvalidMediaParameter`; callers here ignore the error.
pub fn parse_media_type(v: &[u8]) -> MediaType {
    let semi = v.iter().position(|b| *b == b';');
    let base = &v[..semi.unwrap_or(v.len())];
    let media = trim_space(&go_lower(base)).to_vec();
    let fail = |m: &[u8]| MediaType {
        media_type: String::from_utf8_lossy(m).into_owned(),
        params: HashMap::new(),
        err: true,
    };
    if !check_media_type_disposition(&media) {
        return fail(b"");
    }
    let media_type = String::from_utf8_lossy(&media).into_owned();
    let mut params: HashMap<String, Vec<u8>> = HashMap::new();
    let mut continuation: HashMap<String, HashMap<String, Vec<u8>>> = HashMap::new();
    let mut v = &v[base.len()..];
    while !v.is_empty() {
        v = trim_left_space(v);
        if v.is_empty() {
            break;
        }
        let (key, value, rest) = consume_media_param(v);
        if key.is_empty() {
            if trim_space(rest) == b";" {
                break;
            }
            return MediaType {
                media_type,
                params: HashMap::new(),
                err: true,
            };
        }
        let key = String::from_utf8_lossy(&key).into_owned();
        let pmap = match key.split_once('*') {
            Some((base_name, _)) => continuation.entry(base_name.to_string()).or_default(),
            None => &mut params,
        };
        if let Some(old) = pmap.get(&key)
            && *old != value
        {
            return fail(b"");
        }
        pmap.insert(key, value);
        v = rest;
    }
    for (key, pieces) in continuation {
        let single = format!("{key}*");
        if let Some(v) = pieces.get(&single) {
            if let Some(d) = decode_2231_enc(v) {
                params.insert(key, d);
            }
            continue;
        }
        let mut buf = Vec::new();
        let mut valid = false;
        for n in 0.. {
            let simple = format!("{key}*{n}");
            if let Some(v) = pieces.get(&simple) {
                valid = true;
                buf.extend_from_slice(v);
                continue;
            }
            let Some(v) = pieces.get(&format!("{simple}*")) else {
                break;
            };
            valid = true;
            if n == 0 {
                if let Some(d) = decode_2231_enc(v) {
                    buf.extend_from_slice(&d);
                }
            } else {
                buf.extend_from_slice(&percent_hex_unescape(v).unwrap_or_default());
            }
        }
        if valid {
            params.insert(key, buf);
        }
    }
    MediaType {
        media_type,
        params,
        err: false,
    }
}

// -------------------------------------------------------- quoted-printable

fn qp_is_discard_ws(c: u8) -> bool {
    matches!(c, b'\n' | b'\r' | b' ' | b'\t')
}

/// `quotedprintable.Reader` + `io.ReadAll`: the decoded bytes and the first
/// error. Lines are read with a 4096-byte `bufio.ReadSlice`.
pub fn quoted_printable(input: &Stream<'_>) -> Stream<'static> {
    let data: &[u8] = &input.data;
    let mut out = Vec::new();
    let mut pos = 0;
    let mut rerr: Option<String> = None; // sticky stream error (None = still reading)
    let finish = |out: Vec<u8>, e: String| Stream {
        data: Cow::Owned(out),
        end: if e == "EOF" { End::Eof } else { End::Err(e) },
    };
    loop {
        // refill: ReadSlice('\n') over the 4096-byte buffer
        if let Some(e) = rerr.take() {
            return finish(out, e);
        }
        let rest = &data[pos..];
        let (mut line, e): (&[u8], Option<String>) = match rest.iter().position(|b| *b == b'\n') {
            Some(i) if i < PEEK_BUFFER => (&rest[..=i], None),
            _ if rest.len() >= PEEK_BUFFER => {
                (&rest[..PEEK_BUFFER], Some("bufio: buffer full".into()))
            }
            _ => (rest, Some(input.end.error().unwrap_or("EOF").to_string())),
        };
        pos += line.len();
        let whole = line;
        let has_lf = whole.ends_with(b"\n");
        let has_cr = whole.ends_with(b"\r\n");
        let mut z = whole.len();
        while z > 0 && qp_is_discard_ws(whole[z - 1]) {
            z -= 1;
        }
        let trimmed = &whole[..z];
        let mut cur: Vec<u8> = trimmed.to_vec();
        rerr = e;
        if trimmed.ends_with(b"=") {
            let tail = &whole[z..];
            let mut s = 0;
            while s < tail.len() && (tail[s] == b' ' || tail[s] == b'\t') {
                s += 1;
            }
            let right = &tail[s..];
            cur.pop();
            if !right.starts_with(b"\n")
                && !right.starts_with(b"\r\n")
                && !(right.is_empty() && !cur.is_empty() && rerr.as_deref() == Some("EOF"))
            {
                rerr = Some(format!(
                    "quotedprintable: invalid bytes after =: {}",
                    go_quote(right)
                ));
            }
        } else if has_lf {
            if has_cr {
                cur.extend_from_slice(b"\r\n");
            } else {
                cur.push(b'\n');
            }
        }
        line = &[];
        let _ = line;
        // consume cur
        let mut i = 0;
        while i < cur.len() {
            let mut b = cur[i];
            match b {
                b'=' => match read_hex_byte(&cur[i + 1..]) {
                    Ok(h) => {
                        b = h;
                        i += 2;
                    }
                    Err(e) => {
                        if cur.len() - i >= 2 && cur[i + 1] != b'\r' && cur[i + 1] != b'\n' {
                            b = b'=';
                        } else {
                            return finish(out, e);
                        }
                    }
                },
                b'\t' | b'\r' | b'\n' => {}
                0x80..=0xff => {}
                _ if !(b' '..=b'~').contains(&b) => {
                    return finish(
                        out,
                        format!("quotedprintable: invalid unescaped byte 0x{b:02x} in body"),
                    );
                }
                _ => {}
            }
            out.push(b);
            i += 1;
        }
    }
}

fn read_hex_byte(v: &[u8]) -> Result<u8, String> {
    if v.len() < 2 {
        return Err("unexpected EOF".into());
    }
    let hx = |b: u8| {
        (b as char)
            .to_digit(16)
            .map(|d| d as u8)
            .ok_or_else(|| format!("quotedprintable: invalid hex byte 0x{b:02x}"))
    };
    Ok(hx(v[0])? << 4 | hx(v[1])?)
}

// ------------------------------------------------------------------ base64

fn b64(c: u8) -> Option<u8> {
    match c {
        b'A'..=b'Z' => Some(c - b'A'),
        b'a'..=b'z' => Some(c - b'a' + 26),
        b'0'..=b'9' => Some(c - b'0' + 52),
        b'+' => Some(62),
        b'/' => Some(63),
        _ => None,
    }
}

/// `enc.Decode` over one chunk: `(decoded, error)` with Go's offsets.
fn b64_decode_chunk(src: &[u8], out: &mut Vec<u8>) -> Result<(), String> {
    let corrupt = |n: i64| format!("illegal base64 data at input byte {n}");
    let mut si = 0usize;
    while si < src.len() {
        // decodeQuantum
        let mut dbuf = [0u8; 4];
        let mut dlen = 4;
        let mut j = 0usize;
        let mut err: Option<String> = None;
        let mut jj = 0isize;
        while jj < 4 {
            j = jj as usize;
            if si == src.len() {
                match j {
                    0 => return Ok(()),
                    _ => return Err(corrupt(si as i64 - j as i64)),
                }
            }
            let c = src[si];
            si += 1;
            if let Some(v) = b64(c) {
                dbuf[j] = v;
                jj += 1;
                continue;
            }
            if c == b'\n' || c == b'\r' {
                continue;
            }
            if c != b'=' {
                return Err(corrupt(si as i64 - 1));
            }
            match j {
                0 | 1 => return Err(corrupt(si as i64 - 1)),
                2 => {
                    while si < src.len() && (src[si] == b'\n' || src[si] == b'\r') {
                        si += 1;
                    }
                    if si == src.len() {
                        return Err(corrupt(src.len() as i64));
                    }
                    if src[si] != b'=' {
                        return Err(corrupt(si as i64 - 1));
                    }
                    si += 1;
                }
                _ => {}
            }
            while si < src.len() && (src[si] == b'\n' || src[si] == b'\r') {
                si += 1;
            }
            if si < src.len() {
                err = Some(corrupt(si as i64));
            }
            dlen = j;
            break;
        }
        let val = (u32::from(dbuf[0]) << 18)
            | (u32::from(dbuf[1]) << 12)
            | (u32::from(dbuf[2]) << 6)
            | u32::from(dbuf[3]);
        let d = [(val >> 16) as u8, (val >> 8) as u8, val as u8];
        let _ = j;
        match dlen {
            4 => out.extend_from_slice(&d),
            3 => out.extend_from_slice(&d[..2]),
            2 => out.push(d[0]),
            _ => {}
        }
        if let Some(e) = err {
            return Err(e);
        }
    }
    Ok(())
}

const SIZE_CLASSES: &[usize] = &[
    8, 16, 24, 32, 48, 64, 80, 96, 112, 128, 144, 160, 176, 192, 208, 224, 240, 256, 288, 320, 352,
    384, 416, 448, 480, 512, 576, 640, 704, 768, 896, 1024, 1152, 1280, 1408, 1536, 1792, 2048,
    2304, 2688, 3072, 3200, 3456, 4096, 4864, 5376, 6144, 6528, 6784, 6912, 8192, 9472, 9728,
    10240, 10880, 12288, 13568, 14336, 16384, 18432, 19072, 20480, 21760, 24576, 27264, 28672,
    32768,
];

/// Go runtime `roundupsize` for a pointer-free allocation.
fn roundup_size(n: usize) -> usize {
    if n <= 32768 {
        return *SIZE_CLASSES.iter().find(|c| **c >= n).unwrap();
    }
    n.div_ceil(8192) * 8192
}

/// `base64.decoder` driven by `io.ReadAll` (Go 1.26 chunked variant) over a
/// reader that returns `min(len(p), remaining)` bytes.
struct B64Reader<'a> {
    raw: &'a [u8],
    raw_pos: usize,
    end: End,
    buf: Vec<u8>,
    out: Vec<u8>,
    err: Option<String>,
    read_err: Option<String>,
}

impl B64Reader<'_> {
    /// `newlineFilteringReader.Read` over the raw stream.
    fn src_read(&mut self, want: usize) -> (Vec<u8>, Option<String>) {
        loop {
            let remaining = self.raw.len() - self.raw_pos;
            if remaining == 0 {
                return (
                    Vec::new(),
                    Some(self.end.error().unwrap_or("EOF").to_string()),
                );
            }
            let n = want.min(remaining);
            let chunk = &self.raw[self.raw_pos..self.raw_pos + n];
            self.raw_pos += n;
            let kept: Vec<u8> = chunk
                .iter()
                .copied()
                .filter(|b| *b != b'\r' && *b != b'\n')
                .collect();
            if !kept.is_empty() {
                return (kept, None);
            }
        }
    }

    fn read(&mut self, plen: usize) -> (Vec<u8>, Option<String>) {
        if !self.out.is_empty() {
            let n = plen.min(self.out.len());
            let got: Vec<u8> = self.out.drain(..n).collect();
            return (got, None);
        }
        if let Some(e) = &self.err {
            return (Vec::new(), Some(e.clone()));
        }
        while self.buf.len() < 4 && self.read_err.is_none() {
            let nn = (plen / 3 * 4).clamp(4, 1024);
            let (got, e) = self.src_read(nn - self.buf.len());
            self.buf.extend_from_slice(&got);
            self.read_err = e;
        }
        if self.buf.len() < 4 {
            let mut e = self.read_err.clone();
            if e.as_deref() == Some("EOF") && !self.buf.is_empty() {
                e = Some("unexpected EOF".into());
            }
            self.err = e.clone();
            return (Vec::new(), e);
        }
        let nr = self.buf.len() / 4 * 4;
        let nw = self.buf.len() / 4 * 3;
        let mut dec = Vec::new();
        let r = b64_decode_chunk(&self.buf[..nr], &mut dec);
        self.err = r.err();
        let got = if nw > plen {
            let n = plen.min(dec.len());
            self.out = dec[n..].to_vec();
            dec[..n].to_vec()
        } else {
            dec
        };
        self.buf.drain(..nr);
        (got, self.err.clone())
    }
}

/// `base64.NewDecoder(StdEncoding, r)` + `io.ReadAll`. The simulation models
/// Go's read sizes (512-byte first read, chunked growth) so that error
/// offsets, which are relative to the decoder's chunk, match; the underlying
/// reader is assumed to return everything requested (bufio window effects of
/// messages above 4096 bytes are a documented limit).
pub fn base64_stream(input: &Stream<'_>) -> Stream<'static> {
    let mut rd = B64Reader {
        raw: &input.data,
        raw_pos: 0,
        end: input.end.clone(),
        buf: Vec::new(),
        out: Vec::new(),
        err: None,
        read_err: None,
    };
    let mut all = Vec::new();
    let mut cap = 512usize;
    let mut len = 0usize;
    let mut next = 256usize;
    loop {
        let (got, err) = rd.read(cap - len);
        len += got.len();
        all.extend_from_slice(&got);
        if let Some(e) = err {
            return Stream {
                data: Cow::Owned(all),
                end: if e == "EOF" { End::Eof } else { End::Err(e) },
            };
        }
        if cap - len < cap / 16 {
            len = 0;
            cap = roundup_size(next);
            next += next / 2;
        }
    }
}

// --------------------------------------------------------------- multipart

pub struct Multipart<'a> {
    data: &'a [u8],
    end: &'a End,
    pos: usize,
    nl: &'static [u8],
    dash_boundary: Vec<u8>,
    parts_read: usize,
    boundary_empty: bool,
    /// End of the current part's body, valid until the next `next_part`.
    current_end: Option<usize>,
}

pub struct Part<'a> {
    pub header: Header,
    pub body: Stream<'a>,
}

impl<'a> Multipart<'a> {
    pub fn new(stream: &'a Stream<'a>, boundary: &[u8]) -> Self {
        let mut dash = b"--".to_vec();
        dash.extend_from_slice(boundary);
        Self {
            data: &stream.data,
            end: &stream.end,
            pos: 0,
            nl: b"\r\n",
            boundary_empty: dash == b"--",
            dash_boundary: dash,
            parts_read: 0,
            current_end: None,
        }
    }

    fn nl_dash_boundary(&self) -> Vec<u8> {
        let mut v = self.nl.to_vec();
        v.extend_from_slice(&self.dash_boundary);
        v
    }

    fn skip_lwsp(b: &[u8]) -> &[u8] {
        let mut i = 0;
        while i < b.len() && (b[i] == b' ' || b[i] == b'\t') {
            i += 1;
        }
        &b[i..]
    }

    fn is_final_boundary(&self, line: &[u8]) -> bool {
        let mut dd = self.dash_boundary.clone();
        dd.extend_from_slice(b"--");
        if !line.starts_with(&dd) {
            return false;
        }
        let rest = Self::skip_lwsp(&line[dd.len()..]);
        rest.is_empty() || rest == self.nl
    }

    fn is_boundary_delimiter_line(&mut self, line: &[u8]) -> bool {
        if !line.starts_with(&self.dash_boundary) {
            return false;
        }
        let rest = Self::skip_lwsp(&line[self.dash_boundary.len()..]);
        if self.parts_read == 0 && rest == b"\n" {
            self.nl = &self.nl[1..];
        }
        rest == self.nl
    }

    /// `Reader.NextPart`. `Ok(None)` is `io.EOF`.
    pub fn next_part(&mut self) -> Result<Option<Part<'a>>, String> {
        if let Some(e) = self.current_end.take() {
            self.pos = self.pos.max(e);
        }
        if self.boundary_empty {
            return Err("multipart: boundary is empty".into());
        }
        let mut expect_new = false;
        loop {
            // ReadSlice('\n') with a 4096-byte buffer.
            let rest = &self.data[self.pos..];
            let (line, err): (&[u8], Option<String>) = match rest.iter().position(|b| *b == b'\n') {
                Some(i) if i < PEEK_BUFFER => (&rest[..=i], None),
                _ if rest.len() >= PEEK_BUFFER => {
                    (&rest[..PEEK_BUFFER], Some("bufio: buffer full".into()))
                }
                _ => (rest, Some(self.end.error().unwrap_or("EOF").to_string())),
            };
            let line = line.to_vec();
            self.pos += line.len();
            if err.as_deref() == Some("EOF") && self.is_final_boundary(&line) {
                return Ok(None);
            }
            if let Some(e) = err {
                return Err(format!("multipart: NextPart: {e}"));
            }
            if self.is_boundary_delimiter_line(&line) {
                self.parts_read += 1;
                return self
                    .new_part()
                    .map(Some)
                    .or_else(|e| if e == "EOF" { Ok(None) } else { Err(e) });
            }
            if self.is_final_boundary(&line) {
                return Ok(None);
            }
            if expect_new {
                return Err(format!(
                    "multipart: expecting a new Part; got line {}",
                    go_quote(&line)
                ));
            }
            if self.parts_read == 0 {
                continue;
            }
            if line == self.nl {
                expect_new = true;
                continue;
            }
            return Err(format!(
                "multipart: unexpected line in Next(): {}",
                go_quote(&line)
            ));
        }
    }

    fn new_part(&mut self) -> Result<Part<'a>, String> {
        let mut rd = Rd {
            data: self.data,
            end: self.end,
            pos: self.pos,
        };
        let mut header = read_mime_header(&mut rd).map_err(|e| {
            if e == "message too large" {
                "multipart: message too large".to_string()
            } else {
                e
            }
        })?;
        self.pos = rd.pos;
        let body = self.scan_body();
        let qp = {
            let cte = header.get("Content-Transfer-Encoding");
            cte.eq_ignore_ascii_case(b"quoted-printable")
        };
        let mut part = Part {
            header: Header::default(),
            body,
        };
        if qp {
            header.del("Content-Transfer-Encoding");
            part.body = quoted_printable(&part.body);
        }
        part.header = header;
        Ok(part)
    }

    /// Whole-data version of `partReader.Read`/`scanUntilBoundary`.
    fn scan_body(&mut self) -> Stream<'a> {
        let start = self.pos;
        let buf = &self.data[start..];
        let nl_dash = self.nl_dash_boundary();
        let unexpected = End::unexpected();
        // the stream's own end condition replaces ErrUnexpectedEOF
        let stream_end = match self.end.error() {
            Some(e) => End::Err(e.into()),
            None => unexpected,
        };
        let finish = |me: &mut Self, len: usize, end: End, consumed: usize| {
            me.current_end = Some(start + consumed);
            Stream {
                data: Cow::Borrowed(&me.data[start..start + len]),
                end,
            }
        };
        // total == 0 check
        if buf.starts_with(&self.dash_boundary) {
            match match_after_prefix(buf, self.dash_boundary.len()) {
                -1 => { /* body starts with a false boundary: fall through to general scan */ }
                _ => return finish(self, 0, End::Eof, 0),
            }
        } else if self.dash_boundary.starts_with(buf) {
            return finish(self, 0, stream_end, 0);
        }
        let mut cur = 0usize;
        loop {
            let rest = &buf[cur..];
            if let Some(i) = find(rest, &nl_dash) {
                match match_after_prefix(&rest[i..], nl_dash.len()) {
                    -1 => {
                        cur += i + nl_dash.len();
                        continue;
                    }
                    _ => return finish(self, cur + i, End::Eof, cur + i),
                }
            }
            if nl_dash.starts_with(rest) {
                return finish(self, cur, stream_end, buf.len());
            }
            if let Some(i) = rest.iter().rposition(|b| *b == nl_dash[0])
                && nl_dash.starts_with(&rest[i..])
            {
                return finish(self, cur + i, stream_end, buf.len());
            }
            return finish(self, buf.len(), stream_end, buf.len());
        }
    }
}

fn find(hay: &[u8], needle: &[u8]) -> Option<usize> {
    if needle.is_empty() {
        return Some(0);
    }
    hay.windows(needle.len()).position(|w| w == needle)
}

/// `matchAfterPrefix` with the stream fully available (`readErr != nil`).
fn match_after_prefix(buf: &[u8], prefix_len: usize) -> i32 {
    if buf.len() == prefix_len {
        return 1;
    }
    let c = buf[prefix_len];
    if matches!(c, b' ' | b'\t' | b'\r' | b'\n') {
        return 1;
    }
    if c == b'-' {
        if buf.len() == prefix_len + 1 {
            return -1;
        }
        if buf[prefix_len + 1] == b'-' {
            return 1;
        }
    }
    -1
}
