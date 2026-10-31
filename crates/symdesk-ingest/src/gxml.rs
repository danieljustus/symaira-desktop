//! Port of Go 1.26's strict `encoding/xml` tokenizer (`Decoder.Token`, `Skip`
//! and the string case of `DecodeElement`). The ingest readers surface Go's
//! diagnostics verbatim, so this decoder mirrors Go's byte-at-a-time state
//! machine, line counting, entity handling and name-space bookkeeping instead
//! of using a general XML library whose messages and recovery differ.
use crate::gxml_tables::{FIRST, SECOND};
use std::collections::HashMap;
use std::fmt;

const MAX_UNMARSHAL_DEPTH: usize = 10000;

#[derive(Clone, Debug, PartialEq, Eq, Default)]
pub struct Name {
    pub space: String,
    pub local: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Attr {
    pub name: Name,
    pub value: String,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Token {
    Start { name: Name, attr: Vec<Attr> },
    End(Name),
    CharData(Vec<u8>),
    Comment,
    ProcInst,
    Directive,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum XmlError {
    /// `io.EOF` at a clean end of input.
    Eof,
    Syntax { line: usize, msg: String },
    Other(String),
}

impl fmt::Display for XmlError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            XmlError::Eof => f.write_str("EOF"),
            XmlError::Syntax { line, msg } => {
                write!(f, "XML syntax error on line {line}: {msg}")
            }
            XmlError::Other(m) => f.write_str(m),
        }
    }
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Kind {
    Start,
    Ns,
}

struct Frame {
    kind: Kind,
    name: Name,
    ok: bool,
}

pub struct Decoder<'a> {
    data: &'a [u8],
    pos: usize,
    buf: Vec<u8>,
    stk: Vec<Frame>,
    stk_depth: usize,
    need_close: bool,
    to_close: Name,
    ns: HashMap<String, String>,
    err: Option<XmlError>,
    line: usize,
}

type R<T> = Result<T, XmlError>;

fn is_name_byte(c: u8) -> bool {
    c.is_ascii_alphanumeric() || matches!(c, b'_' | b':' | b'.' | b'-')
}

fn in_table(table: &[(u32, u32, u32)], r: u32) -> bool {
    for &(lo, hi, stride) in table {
        if r < lo {
            return false;
        }
        if r <= hi {
            return stride <= 1 || (r - lo) % stride == 0;
        }
    }
    false
}

/// Go `utf8.DecodeRune`: `(0xFFFD, 1)` for invalid input.
fn decode_rune(b: &[u8]) -> (u32, usize) {
    let Some(&first) = b.first() else {
        return (0xFFFD, 0);
    };
    let want = match first {
        0x00..=0x7f => return (u32::from(first), 1),
        0xc2..=0xdf => 2,
        0xe0..=0xef => 3,
        0xf0..=0xf4 => 4,
        _ => return (0xFFFD, 1),
    };
    if b.len() < want {
        return (0xFFFD, 1);
    }
    match std::str::from_utf8(&b[..want]) {
        Ok(s) => (u32::from(s.chars().next().unwrap()), want),
        Err(_) => (0xFFFD, 1),
    }
}

fn is_name(s: &[u8]) -> bool {
    if s.is_empty() {
        return false;
    }
    let (c, mut n) = decode_rune(s);
    if c == 0xFFFD && n == 1 {
        return false;
    }
    if !in_table(FIRST, c) {
        return false;
    }
    let mut rest = s;
    while n < rest.len() {
        rest = &rest[n..];
        let (c, size) = decode_rune(rest);
        n = size;
        if c == 0xFFFD && n == 1 {
            return false;
        }
        if !in_table(FIRST, c) && !in_table(SECOND, c) {
            return false;
        }
    }
    true
}

fn in_character_range(r: u32) -> bool {
    r == 0x09
        || r == 0x0A
        || r == 0x0D
        || (0x20..=0xD7FF).contains(&r)
        || (0xE000..=0xFFFD).contains(&r)
        || (0x10000..=0x10FFFF).contains(&r)
}

fn entity(name: &[u8]) -> Option<&'static str> {
    Some(match name {
        b"lt" => "<",
        b"gt" => ">",
        b"amp" => "&",
        b"apos" => "'",
        b"quot" => "\"",
        _ => return None,
    })
}

/// Go `strconv.ParseUint(s, base, 64)` for digit-only input.
fn parse_uint(s: &[u8], base: u32) -> Option<u64> {
    if s.is_empty() {
        return None;
    }
    let mut n: u64 = 0;
    for &c in s {
        let d = (c as char).to_digit(base)?;
        n = n.checked_mul(u64::from(base))?.checked_add(u64::from(d))?;
    }
    Some(n)
}

/// Go `procInst`: the `param="..."` value of an xml declaration.
fn proc_inst(param: &str, s: &str) -> String {
    let param = format!("{param}=");
    let lenp = param.len();
    let mut i = 0;
    let mut sep = 0u8;
    while i < s.len() {
        let sub = &s[i..];
        let Some(k) = sub.find(&param) else {
            return String::new();
        };
        if lenp + k >= sub.len() {
            return String::new();
        }
        i += lenp + k + 1;
        let c = sub.as_bytes()[lenp + k];
        if c == b'\'' || c == b'"' {
            sep = c;
            break;
        }
    }
    if sep == 0 {
        return String::new();
    }
    match s.as_bytes()[i..].iter().position(|b| *b == sep) {
        Some(j) => String::from_utf8_lossy(&s.as_bytes()[i..i + j]).into_owned(),
        None => String::new(),
    }
}

impl<'a> Decoder<'a> {
    pub fn new(data: &'a [u8]) -> Self {
        Self {
            data,
            pos: 0,
            buf: Vec::new(),
            stk: Vec::new(),
            stk_depth: 0,
            need_close: false,
            to_close: Name::default(),
            ns: HashMap::new(),
            err: None,
            line: 1,
        }
    }

    fn syntax(&self, msg: impl Into<String>) -> XmlError {
        XmlError::Syntax {
            line: self.line,
            msg: msg.into(),
        }
    }

    fn getc(&mut self) -> Option<u8> {
        if self.err.is_some() {
            return None;
        }
        let Some(&b) = self.data.get(self.pos) else {
            self.err = Some(XmlError::Eof);
            return None;
        };
        self.pos += 1;
        if b == b'\n' {
            self.line += 1;
        }
        Some(b)
    }

    fn mustgetc(&mut self) -> Option<u8> {
        let b = self.getc();
        if b.is_none() && self.err == Some(XmlError::Eof) {
            self.err = Some(self.syntax("unexpected EOF"));
        }
        b
    }

    fn ungetc(&mut self, b: u8) {
        if b == b'\n' {
            self.line -= 1;
        }
        self.pos -= 1;
    }

    fn space(&mut self) {
        while let Some(b) = self.getc() {
            if !matches!(b, b' ' | b'\r' | b'\n' | b'\t') {
                self.ungetc(b);
                return;
            }
        }
    }

    fn fail<T>(&mut self, e: XmlError) -> R<T> {
        self.err = Some(e.clone());
        Err(e)
    }

    fn cur_err(&self) -> XmlError {
        self.err.clone().unwrap_or(XmlError::Eof)
    }

    fn read_name(&mut self) -> bool {
        let Some(b) = self.mustgetc() else {
            return false;
        };
        if b < 0x80 && !is_name_byte(b) {
            self.ungetc(b);
            return false;
        }
        self.buf.push(b);
        loop {
            let Some(b) = self.mustgetc() else {
                return false;
            };
            if b < 0x80 && !is_name_byte(b) {
                self.ungetc(b);
                break;
            }
            self.buf.push(b);
        }
        true
    }

    fn name(&mut self) -> Option<String> {
        self.buf.clear();
        if !self.read_name() {
            return None;
        }
        if !is_name(&self.buf) {
            let msg = format!("invalid XML name: {}", String::from_utf8_lossy(&self.buf));
            self.err = Some(self.syntax(msg));
            return None;
        }
        Some(String::from_utf8_lossy(&self.buf).into_owned())
    }

    fn nsname(&mut self) -> Option<Name> {
        let s = self.name()?;
        let mut name = Name::default();
        if s.matches(':').count() > 1 {
            return None;
        }
        match s.split_once(':') {
            Some((space, local)) if !space.is_empty() && !local.is_empty() => {
                name.space = space.into();
                name.local = local.into();
            }
            _ => name.local = s,
        }
        Some(name)
    }

    /// Go `Decoder.text`. `quote` is `None` outside a quoted attribute value.
    fn text(&mut self, quote: Option<u8>, cdata: bool) -> Option<Vec<u8>> {
        let (mut b0, mut b1) = (0u8, 0u8);
        let mut trunc = 0;
        self.buf.clear();
        loop {
            let Some(mut b) = self.getc() else {
                if cdata {
                    if self.err == Some(XmlError::Eof) {
                        self.err = Some(self.syntax("unexpected EOF in CDATA section"));
                    }
                    return None;
                }
                break;
            };
            if quote.is_none() && b0 == b']' && b1 == b']' && b == b'>' {
                if cdata {
                    trunc = 2;
                    break;
                }
                self.err = Some(self.syntax("unescaped ]]> not in CDATA section"));
                return None;
            }
            if b == b'<' && !cdata {
                if quote.is_some() {
                    self.err = Some(self.syntax("unescaped < inside quoted string"));
                    return None;
                }
                self.ungetc(b'<');
                break;
            }
            if quote == Some(b) {
                break;
            }
            if b == b'&' && !cdata {
                let before = self.buf.len();
                self.buf.push(b'&');
                let mut text = String::new();
                let mut have_text = false;
                b = self.mustgetc()?;
                if b == b'#' {
                    self.buf.push(b);
                    b = self.mustgetc()?;
                    let mut base = 10;
                    if b == b'x' {
                        base = 16;
                        self.buf.push(b);
                        b = self.mustgetc()?;
                    }
                    let start = self.buf.len();
                    while b.is_ascii_digit()
                        || (base == 16 && matches!(b, b'a'..=b'f' | b'A'..=b'F'))
                    {
                        self.buf.push(b);
                        b = self.mustgetc()?;
                    }
                    if b != b';' {
                        self.ungetc(b);
                    } else {
                        let digits = self.buf[start..].to_vec();
                        self.buf.push(b';');
                        if let Some(n) = parse_uint(&digits, base)
                            && n <= 0x10FFFF
                        {
                            text = char::from_u32(n as u32).unwrap_or('\u{FFFD}').to_string();
                            have_text = true;
                        }
                    }
                } else {
                    self.ungetc(b);
                    if !self.read_name() && self.err.is_some() {
                        return None;
                    }
                    b = self.mustgetc()?;
                    if b != b';' {
                        self.ungetc(b);
                    } else {
                        let name = self.buf[before + 1..].to_vec();
                        self.buf.push(b';');
                        if is_name(&name)
                            && let Some(t) = entity(&name)
                        {
                            text = t.into();
                            have_text = true;
                        }
                    }
                }
                if have_text {
                    self.buf.truncate(before);
                    self.buf.extend_from_slice(text.as_bytes());
                    b0 = 0;
                    b1 = 0;
                    continue;
                }
                let mut ent = String::from_utf8_lossy(&self.buf[before..]).into_owned();
                if !ent.ends_with(';') {
                    ent.push_str(" (no semicolon)");
                }
                self.err = Some(self.syntax(format!("invalid character entity {ent}")));
                return None;
            }
            // Unescaped \r and \r\n become \n.
            if b == b'\r' {
                self.buf.push(b'\n');
            } else if b1 == b'\r' && b == b'\n' {
            } else {
                self.buf.push(b);
            }
            b0 = b1;
            b1 = b;
        }
        let data_len = self.buf.len() - trunc;
        let mut i = 0;
        while i < data_len {
            let (r, size) = decode_rune(&self.buf[i..data_len]);
            if r == 0xFFFD && size == 1 {
                self.err = Some(self.syntax("invalid UTF-8"));
                return None;
            }
            i += size;
            if !in_character_range(r) {
                self.err = Some(self.syntax(format!("illegal character code U+{r:04X}")));
                return None;
            }
        }
        Some(self.buf[..data_len].to_vec())
    }

    fn attrval(&mut self) -> Option<Vec<u8>> {
        let b = self.mustgetc()?;
        if b == b'"' || b == b'\'' {
            return self.text(Some(b), false);
        }
        self.err = Some(self.syntax("unquoted or missing attribute value in element"));
        None
    }

    fn raw_token(&mut self) -> R<Token> {
        if let Some(e) = &self.err {
            return Err(e.clone());
        }
        if self.need_close {
            self.need_close = false;
            return Ok(Token::End(self.to_close.clone()));
        }
        let Some(b) = self.getc() else {
            return Err(self.cur_err());
        };
        if b != b'<' {
            self.ungetc(b);
            return match self.text(None, false) {
                Some(d) => Ok(Token::CharData(d)),
                None => Err(self.cur_err()),
            };
        }
        let Some(b) = self.mustgetc() else {
            return Err(self.cur_err());
        };
        macro_rules! must {
            () => {
                match self.mustgetc() {
                    Some(b) => b,
                    None => return Err(self.cur_err()),
                }
            };
        }
        match b {
            b'/' => {
                let Some(name) = self.nsname() else {
                    if self.err.is_none() {
                        self.err = Some(self.syntax("expected element name after </"));
                    }
                    return Err(self.cur_err());
                };
                self.space();
                let b = must!();
                if b != b'>' {
                    let msg = format!("invalid characters between </{} and >", name.local);
                    return self.fail(self.syntax(msg));
                }
                Ok(Token::End(name))
            }
            b'?' => {
                let Some(target) = self.name() else {
                    if self.err.is_none() {
                        self.err = Some(self.syntax("expected target name after <?"));
                    }
                    return Err(self.cur_err());
                };
                self.space();
                self.buf.clear();
                let mut b0 = 0u8;
                loop {
                    let b = must!();
                    self.buf.push(b);
                    if b0 == b'?' && b == b'>' {
                        break;
                    }
                    b0 = b;
                }
                let data = self.buf[..self.buf.len() - 2].to_vec();
                if target == "xml" {
                    let content = String::from_utf8_lossy(&data).into_owned();
                    let ver = proc_inst("version", &content);
                    if !ver.is_empty() && ver != "1.0" {
                        return self.fail(XmlError::Other(format!(
                            "xml: unsupported version {}; only version 1.0 is supported",
                            go_quote(&ver)
                        )));
                    }
                    let enc = proc_inst("encoding", &content);
                    if !enc.is_empty() && !enc.eq_ignore_ascii_case("utf-8") {
                        return self.fail(XmlError::Other(format!(
                            "xml: encoding {} declared but Decoder.CharsetReader is nil",
                            go_quote(&enc)
                        )));
                    }
                }
                Ok(Token::ProcInst)
            }
            b'!' => {
                let mut b = must!();
                match b {
                    b'-' => {
                        let b = must!();
                        if b != b'-' {
                            return self.fail(self.syntax("invalid sequence <!- not part of <!--"));
                        }
                        let (mut b0, mut b1) = (0u8, 0u8);
                        loop {
                            let b = must!();
                            if b0 == b'-' && b1 == b'-' {
                                if b != b'>' {
                                    return self.fail(
                                        self.syntax("invalid sequence \"--\" not allowed in comments"),
                                    );
                                }
                                break;
                            }
                            b0 = b1;
                            b1 = b;
                        }
                        return Ok(Token::Comment);
                    }
                    b'[' => {
                        for expect in b"CDATA[" {
                            let b = must!();
                            if b != *expect {
                                return self.fail(self.syntax("invalid <![ sequence"));
                            }
                        }
                        return match self.text(None, true) {
                            Some(d) => Ok(Token::CharData(d)),
                            None => Err(self.cur_err()),
                        };
                    }
                    _ => {}
                }
                // Directive: <!DOCTYPE ...>. Quoted angle brackets do not nest.
                self.buf.clear();
                self.buf.push(b);
                let mut inquote = 0u8;
                let mut depth = 0i32;
                loop {
                    b = must!();
                    if inquote == 0 && b == b'>' && depth == 0 {
                        break;
                    }
                    'handle: loop {
                        self.buf.push(b);
                        if b == inquote {
                            inquote = 0;
                        } else if inquote != 0 {
                        } else if b == b'\'' || b == b'"' {
                            inquote = b;
                        } else if b == b'>' {
                            depth -= 1;
                        } else if b == b'<' {
                            let s = b"!--";
                            for (i, want) in s.iter().enumerate() {
                                b = must!();
                                if b != *want {
                                    for ch in &s[..i] {
                                        self.buf.push(*ch);
                                    }
                                    depth += 1;
                                    continue 'handle;
                                }
                            }
                            self.buf.pop();
                            let (mut b0, mut b1) = (0u8, 0u8);
                            loop {
                                let b = must!();
                                if b0 == b'-' && b1 == b'-' && b == b'>' {
                                    break;
                                }
                                b0 = b1;
                                b1 = b;
                            }
                            self.buf.push(b' ');
                        }
                        break;
                    }
                }
                Ok(Token::Directive)
            }
            _ => {
                self.ungetc(b);
                let Some(name) = self.nsname() else {
                    if self.err.is_none() {
                        self.err = Some(self.syntax("expected element name after <"));
                    }
                    return Err(self.cur_err());
                };
                let mut attr = Vec::new();
                let mut empty = false;
                loop {
                    self.space();
                    let b = must!();
                    if b == b'/' {
                        empty = true;
                        let b = must!();
                        if b != b'>' {
                            return self.fail(self.syntax("expected /> in element"));
                        }
                        break;
                    }
                    if b == b'>' {
                        break;
                    }
                    self.ungetc(b);
                    let Some(aname) = self.nsname() else {
                        if self.err.is_none() {
                            self.err = Some(self.syntax("expected attribute name in element"));
                        }
                        return Err(self.cur_err());
                    };
                    self.space();
                    let b = must!();
                    if b != b'=' {
                        return self.fail(self.syntax("attribute name without = in element"));
                    }
                    self.space();
                    let Some(data) = self.attrval() else {
                        return Err(self.cur_err());
                    };
                    attr.push(Attr {
                        name: aname,
                        value: String::from_utf8_lossy(&data).into_owned(),
                    });
                }
                if empty {
                    self.need_close = true;
                    self.to_close = name.clone();
                }
                Ok(Token::Start { name, attr })
            }
        }
    }

    fn translate(&self, n: &mut Name, is_element: bool) {
        match () {
            _ if n.space == "xmlns" => return,
            _ if n.space.is_empty() && !is_element => return,
            _ if n.space == "xml" => {
                n.space = "http://www.w3.org/XML/1998/namespace".into();
            }
            _ if n.space.is_empty() && n.local == "xmlns" => return,
            _ => {}
        }
        if let Some(v) = self.ns.get(&n.space) {
            n.space = v.clone();
        }
    }

    /// Go `Decoder.Token`.
    pub fn token(&mut self) -> R<Token> {
        let mut t = match self.raw_token() {
            Ok(t) => t,
            Err(mut e) => {
                if e == XmlError::Eof && !self.stk.is_empty() {
                    e = self.syntax("unexpected EOF");
                }
                return Err(e);
            }
        };
        match &mut t {
            Token::Start { name, attr } => {
                for a in attr.iter() {
                    if a.name.space == "xmlns" {
                        let old = self.ns.get(&a.name.local).cloned();
                        self.push_ns(&a.name.local, old);
                        self.ns.insert(a.name.local.clone(), a.value.clone());
                    }
                    if a.name.space.is_empty() && a.name.local == "xmlns" {
                        let old = self.ns.get("").cloned();
                        self.push_ns("", old);
                        self.ns.insert(String::new(), a.value.clone());
                    }
                }
                self.stk.push(Frame {
                    kind: Kind::Start,
                    name: name.clone(),
                    ok: false,
                });
                self.stk_depth += 1;
                let mut n = name.clone();
                self.translate(&mut n, true);
                *name = n;
                for a in attr.iter_mut() {
                    let mut n = a.name.clone();
                    self.translate(&mut n, false);
                    a.name = n;
                }
            }
            Token::End(name) => {
                if let Err(e) = self.pop_element(name) {
                    self.err = Some(e.clone());
                    return Err(e);
                }
            }
            _ => {}
        }
        Ok(t)
    }

    fn push_ns(&mut self, local: &str, old: Option<String>) {
        self.stk.push(Frame {
            kind: Kind::Ns,
            name: Name {
                local: local.into(),
                space: old.clone().unwrap_or_default(),
            },
            ok: old.is_some(),
        });
    }

    fn pop_element(&mut self, t: &mut Name) -> R<()> {
        let s = self.stk.pop();
        if let Some(f) = &s
            && f.kind == Kind::Start
        {
            self.stk_depth -= 1;
        }
        match s {
            Some(f) if f.kind == Kind::Start => {
                if f.name.local != t.local {
                    return Err(self.syntax(format!(
                        "element <{}> closed by </{}>",
                        f.name.local, t.local
                    )));
                }
                if f.name.space != t.space {
                    let ns = if t.space.is_empty() {
                        "\"\"".to_string()
                    } else {
                        t.space.clone()
                    };
                    return Err(self.syntax(format!(
                        "element <{}> in space {} closed by </{}> in space {}",
                        f.name.local, f.name.space, t.local, ns
                    )));
                }
            }
            _ => {
                return Err(self.syntax(format!("unexpected end element </{}>", t.local)));
            }
        }
        let mut n = t.clone();
        self.translate(&mut n, true);
        *t = n;
        while let Some(top) = self.stk.last() {
            if top.kind == Kind::Start {
                break;
            }
            let s = self.stk.pop().unwrap();
            if s.ok {
                self.ns.insert(s.name.local, s.name.space);
            } else {
                self.ns.remove(&s.name.local);
            }
        }
        Ok(())
    }

    /// Go `Decoder.Skip`: consume the rest of the current element.
    pub fn skip(&mut self) -> R<()> {
        let mut depth: i64 = 0;
        loop {
            match self.token()? {
                Token::Start { .. } => depth += 1,
                Token::End(_) => {
                    if depth == 0 {
                        return Ok(());
                    }
                    depth -= 1;
                }
                _ => {}
            }
        }
    }

    /// Go `DecodeElement(&string, &start)`: concatenated direct character
    /// data; child elements are skipped. `start` must be the token that was
    /// just returned.
    pub fn decode_element_string(&mut self) -> R<String> {
        if self.stk_depth > MAX_UNMARSHAL_DEPTH {
            return Err(XmlError::Other("exceeded max depth".into()));
        }
        let mut data = Vec::new();
        loop {
            match self.token()? {
                Token::Start { .. } => self.skip()?,
                Token::End(_) => break,
                Token::CharData(d) => data.extend_from_slice(&d),
                _ => {}
            }
        }
        // Go's string conversion keeps bytes; the data was validated UTF-8.
        Ok(String::from_utf8_lossy(&data).into_owned())
    }
}

/// Go `%q` for the short ASCII-ish strings printed in declaration errors.
fn go_quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if c.is_control() => out.push_str(&format!("\\x{:02x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
    out
}
