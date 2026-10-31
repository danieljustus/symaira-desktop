//! Port of `github.com/emersion/go-message` (`v0.18.2`) as `MailPoller` uses it:
//! `mail.CreateReader` / `Reader.NextPart` over an in-memory RFC 5322 message,
//! `message.Read`'s lenient header reader, the `mime.WordDecoder` based
//! parameter decoding, the transfer-encoding/charset layers and the old
//! `mime/multipart` copy in `go-message/textproto`.
//!
//! Like `gmime` the data is processed as whole byte slices with an explicit
//! end-of-stream status. No charset converter is registered (the Go binary does
//! not import `go-message/charset`), so only UTF-8 and US-ASCII text parts are
//! readable; everything else is `UnknownCharsetError`, exactly as in Go.

use crate::gmime::{End, Stream, base64_stream, go_quote, parse_media_type, quoted_printable};
use std::borrow::Cow;
use std::collections::HashMap;

/// `defaultMaxHeaderBytes`.
const MAX_HEADER_BYTES: usize = 1 << 20;
const PEEK_BUFFER: usize = 4096;
/// A multipart reader that fails on every call makes Go's poller loop forever;
/// the Rust poller gives up after this many consecutive part errors.
pub const MAX_CONSECUTIVE_PART_ERRORS: usize = 1000;

// ------------------------------------------------------------------ headers

#[derive(Clone, Debug, Default)]
pub struct Header {
    fields: Vec<(Vec<u8>, Vec<u8>)>,
}

impl Header {
    /// `Header.Get`: the first value for the canonical key, `""` if absent.
    pub fn get(&self, key: &str) -> Vec<u8> {
        let key = canonical_key(key.as_bytes());
        self.fields
            .iter()
            .find(|(k, _)| *k == key)
            .map(|(_, v)| v.clone())
            .unwrap_or_default()
    }

    #[cfg(test)]
    fn has(&self, key: &str) -> bool {
        let key = canonical_key(key.as_bytes());
        self.fields.iter().any(|(k, _)| *k == key)
    }
}

fn is_token_byte(c: u8) -> bool {
    matches!(c,
        b'a'..=b'z' | b'A'..=b'Z' | b'0'..=b'9'
        | b'!' | b'#' | b'$' | b'%' | b'&' | b'\'' | b'*' | b'+' | b'-' | b'.'
        | b'^' | b'_' | b'`' | b'|' | b'~')
}

/// `textproto.CanonicalMIMEHeaderKey`: keys with a space or a non-token byte
/// are returned unchanged.
fn canonical_key(s: &[u8]) -> Vec<u8> {
    if s.iter().any(|c| *c == b' ' || !is_token_byte(*c)) {
        return s.to_vec();
    }
    let mut upper = true;
    s.iter()
        .map(|&c| {
            let out = if upper {
                c.to_ascii_uppercase()
            } else {
                c.to_ascii_lowercase()
            };
            upper = c == b'-';
            out
        })
        .collect()
}

fn is_sp_tab(c: u8) -> bool {
    c == b' ' || c == b'\t'
}

fn trim_sp_tab(mut s: &[u8]) -> &[u8] {
    while let [first, rest @ ..] = s {
        if is_sp_tab(*first) {
            s = rest;
        } else {
            break;
        }
    }
    while let [rest @ .., last] = s {
        if is_sp_tab(*last) {
            s = rest;
        } else {
            break;
        }
    }
    s
}

/// A `bufio.Reader`-like cursor over the data of a stream.
struct Cursor<'a> {
    data: &'a [u8],
    pos: usize,
    end: &'a End,
}

enum LineErr {
    Eof,
    Other(String),
}

impl<'a> Cursor<'a> {
    fn end_err(&self) -> LineErr {
        match self.end {
            End::Eof => LineErr::Eof,
            End::Err(e) => LineErr::Other(e.clone()),
        }
    }

    /// `bufio.Reader.ReadLine` loop of `readLineSlice`: the line without its
    /// `\n` / `\r\n`; at the end of the data the stream's end condition.
    fn read_line(&mut self) -> Result<Vec<u8>, LineErr> {
        let rest = &self.data[self.pos..];
        if rest.is_empty() {
            return Err(self.end_err());
        }
        let (mut line, adv) = match rest.iter().position(|b| *b == b'\n') {
            Some(i) => (rest[..i].to_vec(), i + 1),
            None => (rest.to_vec(), rest.len()),
        };
        self.pos += adv;
        if line.last() == Some(&b'\r') && adv > line.len() {
            line.pop();
        }
        Ok(line)
    }

    fn peek_byte(&self) -> Option<u8> {
        self.data.get(self.pos).copied()
    }
}

/// `readContinuedLineSlice`: `Ok(None)` is "header without a body".
fn read_continued_line(c: &mut Cursor<'_>) -> Result<Option<Vec<u8>>, String> {
    let mut line = match c.read_line() {
        Ok(l) => l,
        Err(LineErr::Eof) => return Ok(None),
        Err(LineErr::Other(e)) => return Err(e),
    };
    if line.is_empty() {
        return Ok(Some(line));
    }
    line.extend_from_slice(b"\r\n");
    while c.peek_byte().is_some_and(is_sp_tab) {
        match c.read_line() {
            Ok(l) => {
                line.extend_from_slice(&l);
                line.extend_from_slice(b"\r\n");
            }
            Err(_) => break,
        }
    }
    Ok(Some(line))
}

/// `trimAroundNewlines`.
fn trim_around_newlines(v: &[u8]) -> Vec<u8> {
    let mut out: Vec<u8> = Vec::new();
    let mut write = |seg: &[u8]| {
        let seg = seg.strip_suffix(b"\r").unwrap_or(seg);
        let seg = trim_sp_tab(seg);
        if seg.is_empty() {
            return;
        }
        if !out.is_empty() {
            out.push(b' ');
        }
        out.extend_from_slice(seg);
    };
    let mut rest = v;
    loop {
        match rest.iter().position(|b| *b == b'\n') {
            Some(i) => {
                write(&rest[..i]);
                rest = &rest[i + 1..];
            }
            None => {
                write(rest);
                break;
            }
        }
    }
    out
}

/// `textproto.ReadHeader`; the header read so far and the error, if any.
fn read_header(c: &mut Cursor<'_>) -> (Header, Option<String>) {
    let mut fields: Vec<(Vec<u8>, Vec<u8>)> = Vec::new();
    if c.peek_byte().is_some_and(is_sp_tab) {
        return match c.read_line() {
            Err(LineErr::Eof) => (Header { fields }, Some("EOF".into())),
            Err(LineErr::Other(e)) => (Header { fields }, Some(e)),
            Ok(line) => (
                Header { fields },
                Some(format!(
                    "message: malformed MIME header initial line: {}",
                    String::from_utf8_lossy(&line)
                )),
            ),
        };
    }
    loop {
        let kv = match read_continued_line(c) {
            Ok(Some(kv)) => kv,
            Ok(None) => return (Header { fields }, None),
            Err(e) => return (Header { fields }, Some(e)),
        };
        if kv.is_empty() {
            return (Header { fields }, None);
        }
        let Some(i) = kv.iter().position(|b| *b == b':') else {
            return (
                Header { fields },
                Some(format!(
                    "message: malformed MIME header line: {}",
                    String::from_utf8_lossy(&kv)
                )),
            );
        };
        let key = trim_sp_tab(&kv[..i]);
        if key.iter().any(|c| !(33..=126).contains(c) || *c == b':') {
            return (
                Header { fields },
                Some(format!(
                    "message: malformed MIME header key: {}",
                    String::from_utf8_lossy(key)
                )),
            );
        }
        let key = canonical_key(key);
        if key.is_empty() {
            continue;
        }
        fields.push((key, trim_around_newlines(&kv[i + 1..])));
    }
}

// ------------------------------------------------------- mime.WordDecoder

fn hex_val(c: u8) -> Option<u8> {
    match c {
        b'0'..=b'9' => Some(c - b'0'),
        b'a'..=b'f' => Some(c - b'a' + 10),
        b'A'..=b'F' => Some(c - b'A' + 10),
        _ => None,
    }
}

fn q_decode(s: &[u8]) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(s.len());
    let mut i = 0;
    while i < s.len() {
        let c = s[i];
        match c {
            b'_' => out.push(b' '),
            b'=' => {
                if i + 2 >= s.len() {
                    return None;
                }
                let (h, l) = (hex_val(s[i + 1])?, hex_val(s[i + 2])?);
                out.push(h << 4 | l);
                i += 2;
            }
            c if (b' '..=b'~').contains(&c) || c == b'\n' || c == b'\r' || c == b'\t' => {
                out.push(c);
            }
            _ => return None,
        }
        i += 1;
    }
    Some(out)
}

/// `base64.StdEncoding.DecodeString` (padding required, `\r`/`\n` ignored).
fn std_base64(s: &[u8]) -> Option<Vec<u8>> {
    let clean: Vec<u8> = s
        .iter()
        .copied()
        .filter(|c| *c != b'\r' && *c != b'\n')
        .collect();
    if clean.len() % 4 != 0 {
        return None;
    }
    let val = |c: u8| -> Option<u32> {
        match c {
            b'A'..=b'Z' => Some(u32::from(c - b'A')),
            b'a'..=b'z' => Some(u32::from(c - b'a') + 26),
            b'0'..=b'9' => Some(u32::from(c - b'0') + 52),
            b'+' => Some(62),
            b'/' => Some(63),
            _ => None,
        }
    };
    let mut out = Vec::with_capacity(clean.len() / 4 * 3);
    let quanta = clean.chunks(4).count();
    for (n, q) in clean.chunks(4).enumerate() {
        let last = n + 1 == quanta;
        let pad = q.iter().rev().take_while(|c| **c == b'=').count();
        if pad > 2 || (pad > 0 && !last) {
            return None;
        }
        let mut acc = 0u32;
        for &c in &q[..4 - pad] {
            acc = acc << 6 | val(c)?;
        }
        acc <<= 6 * pad as u32;
        let bytes = [(acc >> 16) as u8, (acc >> 8) as u8, acc as u8];
        out.extend_from_slice(&bytes[..3 - pad]);
    }
    Some(out)
}

/// `WordDecoder.convert` with go-message's `charsetReader`.
fn convert_charset(out: &mut Vec<u8>, charset: &[u8], content: &[u8]) -> bool {
    let lower = charset.to_ascii_lowercase();
    match lower.as_slice() {
        b"utf-8" => out.extend_from_slice(content),
        b"iso-8859-1" => {
            for &c in content {
                let mut buf = [0u8; 4];
                out.extend_from_slice(char::from(c).encode_utf8(&mut buf).as_bytes());
            }
        }
        b"us-ascii" => {
            for &c in content {
                if c >= 0x80 {
                    out.extend_from_slice("\u{fffd}".as_bytes());
                } else {
                    out.push(c);
                }
            }
        }
        _ => return false,
    }
    true
}

fn find(hay: &[u8], needle: &[u8]) -> Option<usize> {
    if needle.is_empty() {
        return Some(0);
    }
    hay.windows(needle.len()).position(|w| w == needle)
}

fn has_non_whitespace(s: &[u8]) -> bool {
    s.iter().any(|c| !matches!(c, b' ' | b'\t' | b'\r' | b'\n'))
}

/// go-message `decodeHeader` (`mime.WordDecoder.DecodeHeader`): the decoded
/// text, or the input unchanged when decoding fails.
pub fn decode_header(input: &[u8]) -> Vec<u8> {
    let Some(i) = find(input, b"=?") else {
        return input.to_vec();
    };
    let mut buf: Vec<u8> = input[..i].to_vec();
    let mut header = &input[i..];
    let mut between_words = false;
    loop {
        let Some(start) = find(header, b"=?") else {
            break;
        };
        let mut cur = start + 2;
        let Some(i) = header[cur..].iter().position(|b| *b == b'?') else {
            break;
        };
        let charset = &header[cur..cur + i];
        cur += i + 1;
        if header.len() < cur + 4 {
            break;
        }
        let encoding = header[cur];
        cur += 1;
        if header[cur] != b'?' {
            break;
        }
        cur += 1;
        let Some(j) = find(&header[cur..], b"?=") else {
            break;
        };
        let text = &header[cur..cur + j];
        let end = cur + j + 2;
        let content = match encoding {
            b'B' | b'b' => std_base64(text),
            b'Q' | b'q' => q_decode(text),
            _ => None,
        };
        let Some(content) = content else {
            between_words = false;
            buf.extend_from_slice(&header[..start + 2]);
            header = &header[start + 2..];
            continue;
        };
        if start > 0 && (!between_words || has_non_whitespace(&header[..start])) {
            buf.extend_from_slice(&header[..start]);
        }
        if !convert_charset(&mut buf, charset, &content) {
            return input.to_vec();
        }
        header = &header[end..];
        between_words = true;
    }
    buf.extend_from_slice(header);
    buf
}

// -------------------------------------------------- Content-Type & friends

/// `parseHeaderWithParams`: on a parse error the raw value and no parameters.
fn parse_with_params(value: &[u8]) -> (String, HashMap<String, String>, bool) {
    let mt = parse_media_type(value);
    if mt.err {
        return (String::from_utf8_lossy(value).into_owned(), HashMap::new(), true);
    }
    let params = mt
        .params
        .into_iter()
        .map(|(k, v)| (k, String::from_utf8_lossy(&decode_header(&v)).into_owned()))
        .collect();
    (mt.media_type, params, false)
}

impl Header {
    /// `Header.ContentType`: `text/plain` when absent.
    pub fn content_type(&self) -> (String, HashMap<String, String>, bool) {
        let v = self.get("Content-Type");
        if v.is_empty() {
            return ("text/plain".into(), HashMap::new(), false);
        }
        parse_with_params(&v)
    }

    /// `Header.ContentDisposition`.
    pub fn content_disposition(&self) -> (String, HashMap<String, String>, bool) {
        parse_with_params(&self.get("Content-Disposition"))
    }

    /// `AttachmentHeader.Filename`.
    pub fn filename(&self) -> String {
        let (_, params, _) = self.content_disposition();
        match params.get("filename") {
            Some(f) => f.clone(),
            None => {
                let (_, params, _) = self.content_type();
                params.get("name").cloned().unwrap_or_default()
            }
        }
    }
}

// ----------------------------------------------------------------- entities

/// An entity-level problem that still leaves a readable body.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum EntityError {
    /// `UnknownEncodingError`.
    Encoding(String),
    /// `UnknownCharsetError`.
    Charset(String),
}

impl EntityError {
    pub fn message(&self) -> String {
        match self {
            EntityError::Encoding(enc) => {
                format!("encoding error: unhandled encoding {}", go_quote(enc.as_bytes()))
            }
            EntityError::Charset(cs) => format!(
                "unknown charset: unknown charset: message: unhandled charset {}",
                go_quote(cs.as_bytes())
            ),
        }
    }
}

pub struct Entity {
    pub header: Header,
    pub body: Stream<'static>,
    media_type: String,
    media_params: HashMap<String, String>,
    pub err: Option<EntityError>,
}

impl Entity {
    fn is_multipart(&self) -> bool {
        self.media_type.starts_with("multipart/")
    }
}

/// `whitespaceReplacingReader` + `base64.NewDecoder`.
fn decode_base64(raw: &Stream<'_>) -> Stream<'static> {
    let replaced: Vec<u8> = raw
        .data
        .iter()
        .map(|&c| if c == b' ' || c == b'\t' { b'\n' } else { c })
        .collect();
    base64_stream(&Stream {
        data: Cow::Owned(replaced),
        end: raw.end.clone(),
    })
}

/// `message.New`.
fn new_entity(header: Header, raw: Stream<'static>) -> Entity {
    let (media_type, media_params, _) = header.content_type();
    let mut err = None;
    let mut body = raw;
    if !media_type.starts_with("multipart/") {
        let enc = String::from_utf8_lossy(&header.get("Content-Transfer-Encoding")).into_owned();
        match enc.to_lowercase().as_str() {
            "quoted-printable" => body = quoted_printable(&body),
            "base64" => body = decode_base64(&body),
            "7bit" | "8bit" | "binary" | "" => {}
            _ => err = Some(EntityError::Encoding(enc.to_lowercase())),
        }
    }
    if media_type.starts_with("text/")
        && let Some(charset) = media_params.get("charset")
    {
        let lower = charset.to_lowercase();
        if lower != "utf-8" && lower != "us-ascii" {
            err = Some(EntityError::Charset(lower));
        }
    }
    Entity {
        header,
        body,
        media_type,
        media_params,
        err,
    }
}

// ---------------------------------------------------------------- multipart

/// `matchAfterPrefix` of the old `mime/multipart` copy with the whole stream
/// available (`readErr != nil`).
fn match_after_prefix(buf: &[u8], prefix_len: usize) -> i32 {
    if buf.len() == prefix_len {
        return 1;
    }
    match buf[prefix_len] {
        b' ' | b'\t' | b'\r' | b'\n' | b'-' => 1,
        _ => -1,
    }
}

struct Multipart {
    data: Vec<u8>,
    end: End,
    pos: usize,
    nl: &'static [u8],
    dash_boundary: Vec<u8>,
    parts_read: usize,
    /// End of the current part's body; consumed on the next `next_part`.
    current_end: Option<usize>,
}

impl Multipart {
    fn new(body: Stream<'static>, boundary: &str) -> Self {
        let mut dash = b"--".to_vec();
        dash.extend_from_slice(boundary.as_bytes());
        Self {
            data: body.data.into_owned(),
            end: body.end,
            pos: 0,
            nl: b"\r\n",
            dash_boundary: dash,
            parts_read: 0,
            current_end: None,
        }
    }

    fn skip_lwsp(b: &[u8]) -> &[u8] {
        let n = b.iter().take_while(|c| is_sp_tab(**c)).count();
        &b[n..]
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

    /// `MultipartReader.NextPart`: `Ok(None)` is `io.EOF`.
    fn next_part(&mut self) -> Result<Option<(Header, Stream<'static>)>, String> {
        if let Some(e) = self.current_end.take() {
            self.pos = self.pos.max(e);
        }
        if self.dash_boundary == b"--" {
            return Err("multipart: boundary is empty".into());
        }
        let mut expect_new = false;
        loop {
            let rest = &self.data[self.pos..];
            // ReadSlice('\n') with a 4096-byte buffer.
            let (line, err): (Vec<u8>, Option<String>) =
                match rest.iter().position(|b| *b == b'\n') {
                    Some(i) if i < PEEK_BUFFER => (rest[..=i].to_vec(), None),
                    _ if rest.len() >= PEEK_BUFFER => {
                        (rest[..PEEK_BUFFER].to_vec(), Some("bufio: buffer full".into()))
                    }
                    _ => (
                        rest.to_vec(),
                        Some(self.end.error().unwrap_or("EOF").to_owned()),
                    ),
                };
            self.pos += line.len();
            if err.as_deref() == Some("EOF") && self.is_final_boundary(&line) {
                return Ok(None);
            }
            if let Some(e) = err {
                return Err(format!("multipart: NextPart: {e}"));
            }
            if self.is_boundary_delimiter_line(&line) {
                self.parts_read += 1;
                return self.new_part().map(Some);
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

    fn new_part(&mut self) -> Result<(Header, Stream<'static>), String> {
        let end = self.end.clone();
        let mut cur = Cursor {
            data: &self.data,
            pos: self.pos,
            end: &end,
        };
        let (header, err) = read_header(&mut cur);
        self.pos = cur.pos;
        if let Some(e) = err {
            return Err(e);
        }
        let body = self.scan_body();
        Ok((header, body))
    }

    /// Whole-data `partReader.Read` / `scanUntilBoundary`.
    fn scan_body(&mut self) -> Stream<'static> {
        let start = self.pos;
        let buf = &self.data[start..];
        let mut nl_dash = self.nl.to_vec();
        nl_dash.extend_from_slice(&self.dash_boundary);
        let stream_end = match self.end.error() {
            Some(e) => End::Err(e.into()),
            None => End::Err("unexpected EOF".into()),
        };
        let finish = |me: &mut Self, len: usize, end: End, consumed: usize| {
            me.current_end = Some(start + consumed);
            Stream {
                data: Cow::Owned(me.data[start..start + len].to_vec()),
                end,
            }
        };
        let mut cur = 0usize;
        if buf.starts_with(&self.dash_boundary) {
            if match_after_prefix(buf, self.dash_boundary.len()) == -1 {
                cur = self.dash_boundary.len();
            } else {
                return finish(self, 0, End::Eof, 0);
            }
        } else if self.dash_boundary.starts_with(buf) {
            return finish(self, 0, stream_end, 0);
        }
        loop {
            let rest = &buf[cur..];
            if let Some(i) = find(rest, &nl_dash) {
                if match_after_prefix(&rest[i..], nl_dash.len()) == -1 {
                    cur += i + nl_dash.len();
                    continue;
                }
                return finish(self, cur + i, End::Eof, cur + i);
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

// ------------------------------------------------------------------- reader

/// `mail.PartHeader` kind.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum PartKind {
    Inline,
    Attachment,
}

pub struct Part {
    pub kind: PartKind,
    pub header: Header,
    pub body: Stream<'static>,
}

/// Outcome of [`Reader::next_part`] (`(*Part, error)`).
pub enum Next {
    /// `io.EOF`.
    Eof,
    /// A usable part, possibly together with `UnknownCharsetError`.
    Part(Box<Part>, Option<String>),
    /// An error without a part.
    Error(String),
}

enum Source {
    Multipart(Box<Multipart>),
    /// The artificial `multipart/mixed` wrapper around a single entity.
    Single(Option<Entity>),
}

/// `mail.Reader`.
pub struct Reader {
    /// The message header (`Reader.Header`).
    pub header: Header,
    readers: Vec<Source>,
}

impl Reader {
    /// `mail.CreateReader` over a fully buffered message. Any error, including
    /// `UnknownCharsetError`, is an error here: the poller treats `err != nil`
    /// as failure.
    pub fn create(data: &[u8]) -> Result<Self, String> {
        let end = End::Eof;
        let mut cur = Cursor {
            data,
            pos: 0,
            end: &end,
        };
        let (header, err) = read_header(&mut cur);
        if let Some(e) = err {
            return Err(e);
        }
        if cur.pos > MAX_HEADER_BYTES {
            return Err("message: header exceeds maximum size".into());
        }
        let body = Stream {
            data: Cow::Owned(data[cur.pos..].to_vec()),
            end: End::Eof,
        };
        let entity = new_entity(header.clone(), body);
        if let Some(e) = &entity.err {
            return Err(e.message());
        }
        let source = if entity.is_multipart() {
            let boundary = entity.media_params.get("boundary").cloned().unwrap_or_default();
            Source::Multipart(Box::new(Multipart::new(entity.body, &boundary)))
        } else {
            Source::Single(Some(entity))
        };
        Ok(Self {
            header,
            readers: vec![source],
        })
    }

    /// `Reader.NextPart`.
    pub fn next_part(&mut self) -> Next {
        while let Some(source) = self.readers.last_mut() {
            let entity = match source {
                Source::Single(slot) => match slot.take() {
                    Some(entity) => entity,
                    None => {
                        self.readers.pop();
                        continue;
                    }
                },
                Source::Multipart(mr) => match mr.next_part() {
                    Ok(None) => {
                        self.readers.pop();
                        continue;
                    }
                    Err(e) => return Next::Error(e),
                    Ok(Some((header, raw))) => {
                        let entity = new_entity(header, raw);
                        if let Some(err @ EntityError::Encoding(_)) = &entity.err {
                            return Next::Error(err.message());
                        }
                        entity
                    }
                },
            };
            if entity.is_multipart() {
                let boundary = entity.media_params.get("boundary").cloned().unwrap_or_default();
                self.readers
                    .push(Source::Multipart(Box::new(Multipart::new(entity.body, &boundary))));
                continue;
            }
            let (t, _, _) = entity.header.content_type();
            let (disp, _, _) = entity.header.content_disposition();
            let kind = if disp == "inline" || (disp != "attachment" && t.starts_with("text/")) {
                PartKind::Inline
            } else {
                PartKind::Attachment
            };
            let err = entity.err.as_ref().map(EntityError::message);
            return Next::Part(
                Box::new(Part {
                    kind,
                    header: entity.header,
                    body: entity.body,
                }),
                err,
            );
        }
        Next::Eof
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn word_decoder_matches_go_rules() {
        let d = |s: &str| String::from_utf8(decode_header(s.as_bytes())).unwrap();
        assert_eq!(d("=?UTF-8?B?w6RiYy5wZGY=?="), "äbc.pdf");
        assert_eq!(d("=?utf-8?q?a_b=2Epdf?= =?utf-8?q?c?="), "a b.pdfc");
        assert_eq!(d("x=?UTF-8?Q?y?=z.pdf"), "xyz.pdf");
        assert_eq!(d("=?UTF-8?Q?bad=ZZ?=.pdf"), "=?UTF-8?Q?bad=ZZ?=.pdf");
        assert_eq!(d("=?UTF-16?Q?a?=.pdf"), "=?UTF-16?Q?a?=.pdf");
        assert_eq!(d("=?ISO-8859-1?Q?r=E9sum=E9.pdf?="), "résumé.pdf");
    }

    #[test]
    fn header_values_fold_like_go_message() {
        let data = b"A:  one\r\n two\r\n\tthree\r\nB: x\r\n\r\nbody";
        let end = End::Eof;
        let mut c = Cursor { data, pos: 0, end: &end };
        let (h, err) = read_header(&mut c);
        assert!(err.is_none());
        assert_eq!(h.get("a"), b"one two three");
        assert!(h.has("B"));
        assert_eq!(&data[c.pos..], b"body");
    }

    #[test]
    fn malformed_header_line_error_text() {
        assert_eq!(
            Reader::create(b"no colon\r\n\r\nbody").err().unwrap(),
            "message: malformed MIME header line: no colon\r\n"
        );
    }
}
