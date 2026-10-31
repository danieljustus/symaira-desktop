//! Port of the text extraction path of `github.com/ledongthuc/pdf`
//! (`Page.GetPlainText`, revision `5959a4027728`).
//!
//! Object access and cross-reference parsing stay with lopdf, but everything
//! that decides *which text* comes out follows the Go library: its open-time
//! header/`%%EOF`/`startxref` checks, page tree walk (`Count` based), resource
//! inheritance, content stream lexer and `Interpret` loop (including the
//! PostScript-style helpers cmaps use), font encoder selection (`WinAnsi`,
//! `MacRoman`, `Identity-H`/`ToUnicode`, `Differences`, PDFDoc fallback), the
//! cmap reader and the Go-specific stream filters. Any condition on which Go
//! panics makes the page fail, exactly like the `recover` in `GetPlainText`.
//!
//! Known, intentional deviations: ASCII85 and every filter other than
//! `FlateDecode` (predictor 12 only) fail the page like Go's `unknown filter`
//! panic (Go's ASCII85 path additionally corrupts real streams); content-stream
//! lexer states in which Go loops forever (an unterminated hex string, array or
//! cmap count) return a page error instead; xref/trailer syntax errors beyond
//! the `startxref` checks report lopdf's wording.

use std::{cell::RefCell, collections::BTreeMap, io::Read, rc::Rc};

use super::retrieval_extract::contract;
use crate::pdf_go_tables::{MAC_ROMAN, NAME_TO_RUNE, PDF_DOC, WIN_ANSI};
use crate::{RetrievalAnchor, RetrievalByteSection, SidecarError};
use lopdf::{Dictionary, Document, Object};

const NO_TEXT: &str = "PDF contains no extractable text (may be image-only)";
const DECOMPRESSED_LIMIT: usize = 100 << 20;
/// Upper bound for loops that Go would run without bound on hostile input.
const LOOP_BOUND: usize = 1 << 20;
const NO_RUNE: char = '\u{FFFD}';

/// Go panics (recovered per page) map to this marker.
#[derive(Debug)]
struct PageError;
type PageResult<T> = Result<T, PageError>;

// ---------------------------------------------------------------- values

type SharedDict = Rc<RefCell<BTreeMap<Vec<u8>, Val>>>;

#[derive(Clone, Debug)]
enum Val {
    Null,
    Bool,
    Int(i64),
    Real,
    Str(Vec<u8>),
    Name(Vec<u8>),
    Array(Vec<Val>),
    Dict(SharedDict),
}

impl Val {
    fn raw_string(&self) -> &[u8] {
        match self {
            Val::Str(bytes) => bytes,
            _ => &[],
        }
    }

    fn int64(&self) -> i64 {
        match self {
            Val::Int(value) => *value,
            _ => 0,
        }
    }

    fn name(&self) -> &[u8] {
        match self {
            Val::Name(bytes) => bytes,
            _ => &[],
        }
    }
}

// ---------------------------------------------------------------- lexer

#[derive(Clone, Debug)]
enum Tok {
    Eof,
    Kw(Vec<u8>),
    Str(Vec<u8>),
    Name(Vec<u8>),
    Bool,
    Int(i64),
    Real,
}

fn is_space(byte: u8) -> bool {
    matches!(byte, 0 | b'\t' | b'\n' | 0x0c | b'\r' | b' ')
}

fn is_delim(byte: u8) -> bool {
    matches!(
        byte,
        b'<' | b'>' | b'(' | b')' | b'[' | b']' | b'{' | b'}' | b'/' | b'%'
    )
}

fn unhex(byte: u8) -> i32 {
    match byte {
        b'0'..=b'9' => i32::from(byte - b'0'),
        b'a'..=b'f' => i32::from(byte - b'a') + 10,
        b'A'..=b'F' => i32::from(byte - b'A') + 10,
        _ => -1,
    }
}

/// Go's `buffer` over an in-memory stream with `allowEOF` set.
struct Lexer<'a> {
    data: &'a [u8],
    pos: usize,
    eof: bool,
    synthetic: bool,
    unread: Vec<Tok>,
}

impl<'a> Lexer<'a> {
    fn new(data: &'a [u8]) -> Self {
        Self {
            data,
            pos: 0,
            eof: false,
            synthetic: false,
            unread: Vec::new(),
        }
    }

    /// Past the end Go's `readByte` returns a newline and latches `eof`.
    fn read_byte(&mut self) -> u8 {
        if self.pos >= self.data.len() {
            self.eof = true;
            self.synthetic = true;
            return b'\n';
        }
        self.synthetic = false;
        let byte = self.data[self.pos];
        self.pos += 1;
        byte
    }

    fn unread_byte(&mut self) {
        if !self.synthetic && self.pos > 0 {
            self.pos -= 1;
        }
    }

    fn read_token(&mut self) -> PageResult<Tok> {
        if let Some(token) = self.unread.pop() {
            return Ok(token);
        }
        let mut c = self.read_byte();
        loop {
            if is_space(c) {
                if self.eof {
                    return Ok(Tok::Eof);
                }
                c = self.read_byte();
            } else if c == b'%' {
                while c != b'\r' && c != b'\n' {
                    c = self.read_byte();
                }
            } else {
                break;
            }
        }
        match c {
            b'<' => {
                if self.read_byte() == b'<' {
                    return Ok(Tok::Kw(b"<<".to_vec()));
                }
                self.unread_byte();
                self.read_hex_string()
            }
            b'(' => self.read_literal_string(),
            b'[' | b']' | b'{' | b'}' => Ok(Tok::Kw(vec![c])),
            b'/' => Ok(self.read_name()?),
            b'>' => {
                if self.read_byte() == b'>' {
                    return Ok(Tok::Kw(b">>".to_vec()));
                }
                self.unread_byte();
                Err(PageError)
            }
            _ => {
                if is_delim(c) {
                    return Err(PageError);
                }
                self.unread_byte();
                self.read_keyword()
            }
        }
    }

    fn read_hex_string(&mut self) -> PageResult<Tok> {
        let mut out = Vec::new();
        loop {
            let c = loop {
                let c = self.read_byte();
                if c == b'>' || !is_space(c) {
                    break c;
                }
                // Go spins forever on an unterminated string.
                if self.eof {
                    return Err(PageError);
                }
            };
            if c == b'>' {
                break;
            }
            let c2 = loop {
                let c2 = self.read_byte();
                if !is_space(c2) {
                    break c2;
                }
                if self.eof {
                    return Err(PageError);
                }
            };
            let value = unhex(c) << 4 | unhex(c2);
            if value < 0 {
                return Err(PageError);
            }
            out.push(value as u8);
        }
        Ok(Tok::Str(out))
    }

    fn read_literal_string(&mut self) -> PageResult<Tok> {
        let mut out = Vec::new();
        let mut depth = 1;
        while !self.eof {
            let c = self.read_byte();
            match c {
                b'(' => {
                    depth += 1;
                    out.push(c);
                }
                b')' => {
                    depth -= 1;
                    if depth == 0 {
                        break;
                    }
                    out.push(c);
                }
                b'\\' => match self.read_byte() {
                    b'n' => out.push(b'\n'),
                    b'r' => out.push(b'\r'),
                    b'b' => out.push(8),
                    b't' => out.push(b'\t'),
                    b'f' => out.push(0x0c),
                    escaped @ (b'(' | b')' | b'\\') => out.push(escaped),
                    b'\r' => {
                        if self.read_byte() != b'\n' {
                            self.unread_byte();
                        }
                    }
                    b'\n' => {}
                    digit @ b'0'..=b'7' => {
                        let mut value = u32::from(digit - b'0');
                        for _ in 0..2 {
                            let next = self.read_byte();
                            if !(b'0'..=b'7').contains(&next) {
                                self.unread_byte();
                                break;
                            }
                            value = value * 8 + u32::from(next - b'0');
                        }
                        if value > 255 {
                            return Err(PageError);
                        }
                        out.push(value as u8);
                    }
                    _ => return Err(PageError),
                },
                _ => out.push(c),
            }
        }
        Ok(Tok::Str(out))
    }

    fn read_name(&mut self) -> PageResult<Tok> {
        let mut out = Vec::new();
        loop {
            let c = self.read_byte();
            if is_delim(c) || is_space(c) {
                self.unread_byte();
                break;
            }
            if c == b'#' {
                let first = unhex(self.read_byte());
                let second = unhex(self.read_byte());
                let value = first << 4 | second;
                if value < 0 {
                    return Err(PageError);
                }
                out.push(value as u8);
                continue;
            }
            out.push(c);
        }
        Ok(Tok::Name(out))
    }

    fn read_keyword(&mut self) -> PageResult<Tok> {
        let mut out = Vec::new();
        loop {
            let c = self.read_byte();
            if is_delim(c) || is_space(c) {
                self.unread_byte();
                break;
            }
            out.push(c);
        }
        let text = String::from_utf8_lossy(&out).into_owned();
        if out == b"true" {
            return Ok(Tok::Bool);
        }
        if out == b"false" {
            return Ok(Tok::Bool);
        }
        let digits = out
            .strip_prefix(b"+")
            .or_else(|| out.strip_prefix(b"-"))
            .unwrap_or(&out);
        if !digits.is_empty() && digits.iter().all(u8::is_ascii_digit) {
            return text.parse::<i64>().map(Tok::Int).map_err(|_| PageError);
        }
        let dots = digits.iter().filter(|b| **b == b'.').count();
        if !digits.is_empty()
            && dots == 1
            && digits.iter().all(|b| b.is_ascii_digit() || *b == b'.')
        {
            return text
                .parse::<f64>()
                .map(|_| Tok::Real)
                .map_err(|_| PageError);
        }
        Ok(Tok::Kw(out))
    }

    /// Go's `readObject` with `allowObjptr` and `allowStream` off.
    fn read_object(&mut self) -> PageResult<Val> {
        let token = self.read_token()?;
        match token {
            Tok::Kw(keyword) => match keyword.as_slice() {
                b"null" | b">>" => Ok(Val::Null),
                b"<<" => self.read_dict(),
                b"[" => self.read_array(),
                _ => Err(PageError),
            },
            Tok::Eof => Err(PageError),
            Tok::Str(bytes) => Ok(Val::Str(bytes)),
            Tok::Name(bytes) => Ok(Val::Name(bytes)),
            Tok::Bool => Ok(Val::Bool),
            Tok::Int(value) => Ok(Val::Int(value)),
            Tok::Real => Ok(Val::Real),
        }
    }

    fn read_array(&mut self) -> PageResult<Val> {
        let mut items = Vec::new();
        loop {
            let token = self.read_token()?;
            match &token {
                // Go appends io.EOF forever here.
                Tok::Eof => return Err(PageError),
                Tok::Kw(keyword) if keyword == b"]" => break,
                _ => {}
            }
            self.unread.push(token);
            items.push(self.read_object()?);
            if items.len() > LOOP_BOUND {
                return Err(PageError);
            }
        }
        Ok(Val::Array(items))
    }

    fn read_dict(&mut self) -> PageResult<Val> {
        let mut map = BTreeMap::new();
        loop {
            let token = self.read_token()?;
            match token {
                Tok::Kw(keyword) if keyword == b">>" => break,
                Tok::Eof => {
                    self.read_token()?;
                    break;
                }
                Tok::Name(key) => {
                    let value = self.read_object()?;
                    map.insert(key, value);
                }
                _ => return Err(PageError),
            }
        }
        Ok(Val::Dict(Rc::new(RefCell::new(map))))
    }
}

/// Go's `Interpret` over already decoded stream bytes (one stack and one
/// dictionary stack are shared across the streams of a `Contents` array).
fn interpret(
    streams: &[Vec<u8>],
    mut handler: impl FnMut(&mut Vec<Val>, &[u8]) -> PageResult<()>,
) -> PageResult<()> {
    let mut stack: Vec<Val> = Vec::new();
    let mut dicts: Vec<SharedDict> = Vec::new();
    for data in streams {
        let mut lexer = Lexer::new(data);
        'reading: loop {
            let token = lexer.read_token()?;
            if matches!(token, Tok::Eof) {
                break;
            }
            if let Tok::Kw(keyword) = &token {
                match keyword.as_slice() {
                    b"null" | b"[" | b"]" | b"<<" | b">>" => {}
                    b"dict" => {
                        stack.pop();
                        stack.push(Val::Dict(Rc::default()));
                        continue;
                    }
                    b"currentdict" => {
                        let Some(top) = dicts.last() else {
                            return Err(PageError);
                        };
                        stack.push(Val::Dict(Rc::clone(top)));
                        continue;
                    }
                    b"begin" => {
                        match stack.pop() {
                            Some(Val::Dict(dict)) => dicts.push(dict),
                            _ => return Err(PageError),
                        }
                        continue;
                    }
                    b"end" => {
                        if dicts.pop().is_none() {
                            return Err(PageError);
                        }
                        continue;
                    }
                    b"def" => {
                        let Some(top) = dicts.last() else {
                            return Err(PageError);
                        };
                        let value = stack.pop().unwrap_or(Val::Null);
                        if let Some(Val::Name(key)) = stack.pop() {
                            top.borrow_mut().insert(key, value);
                        }
                        continue;
                    }
                    b"pop" => {
                        stack.pop();
                        continue;
                    }
                    other => {
                        for dict in dicts.iter().rev() {
                            if let Some(value) = dict.borrow().get(other) {
                                stack.push(value.clone());
                                continue 'reading;
                            }
                        }
                        handler(&mut stack, other)?;
                        continue;
                    }
                }
            }
            lexer.unread.push(token);
            let object = lexer.read_object()?;
            stack.push(object);
            if stack.len() > LOOP_BOUND {
                return Err(PageError);
            }
        }
    }
    Ok(())
}

// ------------------------------------------------------------- encoders

enum DiffEntry {
    Int(i64),
    Name(Vec<u8>),
    Other,
}

struct Cmap {
    space: [Vec<(Vec<u8>, Vec<u8>)>; 4],
    bfrange: Vec<(Vec<u8>, Vec<u8>, Val)>,
    bfchar: Vec<(Vec<u8>, Vec<u8>)>,
}

enum Encoder {
    Nop,
    Byte(&'static [u32; 256]),
    Dict(Vec<DiffEntry>),
    Cmap(Cmap),
}

fn utf16_decode(bytes: &[u8]) -> PageResult<Vec<char>> {
    let mut units = Vec::new();
    let mut index = 0;
    while index < bytes.len() {
        // Go indexes `s[i+1]` unconditionally: an odd length panics.
        let Some(low) = bytes.get(index + 1) else {
            return Err(PageError);
        };
        units.push(u16::from(bytes[index]) << 8 | u16::from(*low));
        index += 2;
    }
    Ok(char::decode_utf16(units)
        .map(|unit| unit.unwrap_or(NO_RUNE))
        .collect())
}

fn glyph_rune(name: &[u8]) -> Option<char> {
    let name = std::str::from_utf8(name).ok()?;
    let index = NAME_TO_RUNE
        .binary_search_by(|entry| entry.0.cmp(name))
        .ok()?;
    char::from_u32(NAME_TO_RUNE[index].1)
}

impl Encoder {
    /// Go's `Decode`, returning the Go string as bytes.
    fn decode(&self, raw: &[u8]) -> PageResult<Vec<u8>> {
        let mut out = String::new();
        match self {
            Encoder::Nop => return Ok(raw.to_vec()),
            Encoder::Byte(table) => {
                for byte in raw {
                    out.push(char::from_u32(table[usize::from(*byte)]).unwrap_or(NO_RUNE));
                }
            }
            Encoder::Dict(entries) => {
                for byte in raw {
                    let mut ch = char::from(*byte);
                    let mut n: i64 = -1;
                    for entry in entries {
                        match entry {
                            DiffEntry::Int(value) => n = *value,
                            DiffEntry::Name(name) => {
                                if i64::from(*byte) == n
                                    && let Some(found) = glyph_rune(name)
                                    && found != '\0'
                                {
                                    ch = found;
                                    break;
                                }
                                n += 1;
                            }
                            DiffEntry::Other => {}
                        }
                    }
                    out.push(ch);
                }
            }
            Encoder::Cmap(map) => return map.decode(raw),
        }
        Ok(out.into_bytes())
    }
}

impl Cmap {
    fn decode(&self, mut raw: &[u8]) -> PageResult<Vec<u8>> {
        let mut out = String::new();
        'parse: while !raw.is_empty() {
            for n in 1..=4.min(raw.len()) {
                for (low, high) in &self.space[n - 1] {
                    if low.as_slice() <= &raw[..n] && &raw[..n] <= high.as_slice() {
                        let text = &raw[..n];
                        raw = &raw[n..];
                        for (orig, repl) in &self.bfchar {
                            if orig.len() == n && orig == text {
                                out.extend(utf16_decode(repl)?);
                                continue 'parse;
                            }
                        }
                        for (lo, hi, dst) in &self.bfrange {
                            if lo.len() == n && lo.as_slice() <= text && text <= hi.as_slice() {
                                match dst {
                                    Val::Str(source) => {
                                        let mut bytes = source.clone();
                                        if lo.as_slice() != text {
                                            let Some(last) = bytes.last_mut() else {
                                                return Err(PageError);
                                            };
                                            *last = last.wrapping_add(
                                                text[text.len() - 1].wrapping_sub(lo[lo.len() - 1]),
                                            );
                                        }
                                        out.extend(utf16_decode(&bytes)?);
                                        continue 'parse;
                                    }
                                    Val::Array(items) => {
                                        let offset =
                                            text[text.len() - 1].wrapping_sub(lo[lo.len() - 1]);
                                        if let Some(Val::Str(item)) = items.get(usize::from(offset))
                                        {
                                            out.extend(utf16_decode(item)?);
                                            continue 'parse;
                                        }
                                    }
                                    _ => {}
                                }
                                out.push(NO_RUNE);
                                continue 'parse;
                            }
                        }
                        out.push(NO_RUNE);
                        continue 'parse;
                    }
                }
            }
            out.push(NO_RUNE);
            raw = &raw[1..];
        }
        Ok(out.into_bytes())
    }
}

/// Go's `readCmap` over the decoded `ToUnicode` stream.
fn read_cmap(data: &[u8]) -> PageResult<Option<Cmap>> {
    let mut count: i64 = -1;
    let mut map = Cmap {
        space: Default::default(),
        bfrange: Vec::new(),
        bfchar: Vec::new(),
    };
    let mut ok = true;
    interpret(&[data.to_vec()], |stack, operator| {
        if !ok {
            return Ok(());
        }
        let pop = |stack: &mut Vec<Val>| stack.pop().unwrap_or(Val::Null);
        let bound = |count: i64| -> PageResult<usize> {
            usize::try_from(count)
                .ok()
                .filter(|count| *count <= LOOP_BOUND)
                .ok_or(PageError)
        };
        match operator {
            b"findresource" => {
                pop(stack);
                pop(stack);
                stack.push(Val::Dict(Rc::default()));
            }
            b"begincmap" => stack.push(Val::Dict(Rc::default())),
            b"endcmap" => {
                pop(stack);
            }
            b"begincodespacerange" => count = pop(stack).int64(),
            b"endcodespacerange" => {
                if count < 0 {
                    ok = false;
                    return Ok(());
                }
                for _ in 0..bound(count)? {
                    let high = pop(stack).raw_string().to_vec();
                    let low = pop(stack).raw_string().to_vec();
                    if low.is_empty() || low.len() != high.len() {
                        ok = false;
                        return Ok(());
                    }
                    // Go indexes `space[len(lo)-1]` of a four element array.
                    let slot = map.space.get_mut(low.len() - 1).ok_or(PageError)?;
                    slot.push((low, high));
                }
                count = -1;
            }
            b"beginbfchar" => count = pop(stack).int64(),
            b"endbfchar" => {
                if count < 0 {
                    return Err(PageError);
                }
                for _ in 0..bound(count)? {
                    let repl = pop(stack).raw_string().to_vec();
                    let orig = pop(stack).raw_string().to_vec();
                    map.bfchar.push((orig, repl));
                }
            }
            b"beginbfrange" => count = pop(stack).int64(),
            b"endbfrange" => {
                if count < 0 {
                    return Err(PageError);
                }
                for _ in 0..bound(count)? {
                    let dst = pop(stack);
                    let high = pop(stack).raw_string().to_vec();
                    let low = pop(stack).raw_string().to_vec();
                    map.bfrange.push((low, high, dst));
                }
            }
            b"defineresource" => {
                pop(stack);
                let value = pop(stack);
                pop(stack);
                stack.push(value);
            }
            _ => {}
        }
        Ok(())
    })?;
    Ok(ok.then_some(map))
}

// ------------------------------------------------------------- documents

static NULL: Object = Object::Null;

struct Pdf<'a> {
    doc: &'a Document,
    limit: usize,
}

impl<'a> Pdf<'a> {
    /// Go's `resolve`: follow references, unknown objects are null.
    fn resolve(&self, object: &'a Object) -> &'a Object {
        self.doc
            .dereference(object)
            .map_or(&NULL, |(_, resolved)| resolved)
    }

    fn dict_of(object: &'a Object) -> Option<&'a Dictionary> {
        match object {
            Object::Dictionary(dict) => Some(dict),
            Object::Stream(stream) => Some(&stream.dict),
            _ => None,
        }
    }

    /// Go's `Value.Key` (dictionaries and stream headers).
    fn key(&self, object: &'a Object, key: &[u8]) -> &'a Object {
        Self::dict_of(self.resolve(object))
            .and_then(|dict| dict.get(key).ok())
            .map_or(&NULL, |value| self.resolve(value))
    }

    fn name_is(&self, object: &'a Object, expected: &[u8]) -> bool {
        matches!(self.resolve(object), Object::Name(name) if name == expected)
    }

    fn int(&self, object: &'a Object) -> i64 {
        match self.resolve(object) {
            Object::Integer(value) => *value,
            _ => 0,
        }
    }

    /// Go's `Reader.Page`.
    fn page(&self, number: i64, pages_root: &'a Object) -> Option<&'a Object> {
        let mut num = number - 1;
        let mut page = pages_root;
        'search: while self.name_is(self.key(page, b"Type"), b"Pages") {
            if self.int(self.key(page, b"Count")) < num {
                return None;
            }
            let kids = self.resolve(self.key(page, b"Kids"));
            let kids: &[Object] = match kids {
                Object::Array(items) => items,
                _ => &[],
            };
            for kid in kids {
                let kid = self.resolve(kid);
                if self.name_is(self.key(kid, b"Type"), b"Pages") {
                    let count = self.int(self.key(kid, b"Count"));
                    if num < count {
                        page = kid;
                        continue 'search;
                    }
                    num -= count;
                    continue;
                }
                if self.name_is(self.key(kid, b"Type"), b"Page") {
                    if num == 0 {
                        return Some(kid);
                    }
                    num -= 1;
                }
            }
            break;
        }
        None
    }

    fn find_inherited(&self, page: &'a Object, key: &[u8]) -> &'a Object {
        let mut current = self.resolve(page);
        for _ in 0..LOOP_BOUND {
            if matches!(current, Object::Null) {
                break;
            }
            let found = self.key(current, key);
            if !matches!(found, Object::Null) {
                return found;
            }
            current = self.key(current, b"Parent");
        }
        &NULL
    }

    /// Go's `Value.Reader`: raw bytes through the Go filter chain.
    fn stream_bytes(&self, object: &'a Object) -> PageResult<Vec<u8>> {
        let Object::Stream(stream) = self.resolve(object) else {
            return Err(PageError);
        };
        let filter = self.key(object, b"Filter");
        let params = self.key(object, b"DecodeParms");
        let mut bytes = stream.content.clone();
        match filter {
            Object::Null => {}
            Object::Name(name) => bytes = self.apply_filter(bytes, name, params)?,
            Object::Array(items) => {
                for (index, item) in items.iter().enumerate() {
                    let name = match self.resolve(item) {
                        Object::Name(name) => name.as_slice(),
                        _ => &[],
                    };
                    let param = match params {
                        Object::Array(list) => list.get(index).map_or(&NULL, |p| self.resolve(p)),
                        _ => &NULL,
                    };
                    bytes = self.apply_filter(bytes, name, param)?;
                }
            }
            _ => return Err(PageError),
        }
        Ok(bytes)
    }

    fn apply_filter(&self, input: Vec<u8>, name: &[u8], param: &'a Object) -> PageResult<Vec<u8>> {
        if name != b"FlateDecode" {
            return Err(PageError);
        }
        let mut decoded = Vec::new();
        let mut reader =
            flate2::read::ZlibDecoder::new(input.as_slice()).take(self.limit as u64 + 1);
        reader.read_to_end(&mut decoded).map_err(|_| PageError)?;
        if decoded.len() > self.limit {
            return Err(PageError);
        }
        let predictor = self.key(param, b"Predictor");
        if matches!(predictor, Object::Null) {
            return Ok(decoded);
        }
        if self.int(predictor) != 12 {
            return Err(PageError);
        }
        let columns =
            usize::try_from(self.int(self.key(param, b"Columns"))).map_err(|_| PageError)?;
        let row = columns.checked_add(1).ok_or(PageError)?;
        let mut history = vec![0_u8; row];
        let mut out = Vec::new();
        let mut chunks = decoded.chunks_exact(row);
        for chunk in &mut chunks {
            if chunk[0] != 2 {
                return Err(PageError);
            }
            for (slot, byte) in history.iter_mut().zip(chunk) {
                *slot = slot.wrapping_add(*byte);
            }
            out.extend_from_slice(&history[1..]);
        }
        if !chunks.remainder().is_empty() {
            return Err(PageError);
        }
        Ok(out)
    }

    /// Go's `Font.Encoder`.
    fn encoder(&self, font: &'a Object) -> PageResult<Encoder> {
        match self.key(font, b"Encoding") {
            Object::Name(name) => match name.as_slice() {
                b"WinAnsiEncoding" => Ok(Encoder::Byte(&WIN_ANSI)),
                b"MacRomanEncoding" => Ok(Encoder::Byte(&MAC_ROMAN)),
                b"Identity-H" => self.charmap_encoding(font),
                _ => Ok(Encoder::Nop),
            },
            encoding @ Object::Dictionary(_) => {
                let mut entries = Vec::new();
                if let Object::Array(items) = self.key(encoding, b"Differences") {
                    for item in items {
                        entries.push(match self.resolve(item) {
                            Object::Integer(value) => DiffEntry::Int(*value),
                            Object::Name(name) => DiffEntry::Name(name.clone()),
                            _ => DiffEntry::Other,
                        });
                    }
                }
                Ok(Encoder::Dict(entries))
            }
            Object::Null => self.charmap_encoding(font),
            _ => Ok(Encoder::Nop),
        }
    }

    fn charmap_encoding(&self, font: &'a Object) -> PageResult<Encoder> {
        let to_unicode = self.key(font, b"ToUnicode");
        if matches!(to_unicode, Object::Stream(_)) {
            let bytes = self.stream_bytes(to_unicode)?;
            return Ok(read_cmap(&bytes)?.map_or(Encoder::Nop, Encoder::Cmap));
        }
        Ok(Encoder::Byte(&PDF_DOC))
    }

    /// Go's `Page.GetPlainText(nil)`.
    fn page_text(&self, page: &'a Object) -> PageResult<String> {
        let contents = self.key(page, b"Contents");
        if matches!(contents, Object::Null) {
            return Ok(String::new());
        }
        let mut streams = Vec::new();
        match contents {
            Object::Array(items) => {
                for item in items {
                    streams.push(self.stream_bytes(item)?);
                }
            }
            single => streams.push(self.stream_bytes(single)?),
        }
        let resources = self.find_inherited(page, b"Resources");
        let font_dict = self.key(resources, b"Font");
        let mut fonts: BTreeMap<&[u8], &'a Object> = BTreeMap::new();
        if let Some(dict) = Self::dict_of(font_dict) {
            for (name, value) in dict.iter() {
                fonts.insert(name.as_slice(), self.resolve(value));
            }
        }
        let mut encoder = Encoder::Nop;
        let mut text = String::new();
        let show = |text: &mut String, decoded: &[u8]| push_go_runes(text, decoded);
        interpret(&streams, |stack, operator| {
            let args = std::mem::take(stack);
            match operator {
                b"BT" => text.push('\n'),
                b"T*" => show(&mut text, &encoder.decode(b"\n")?),
                b"Tf" => {
                    if args.len() != 2 {
                        return Err(PageError);
                    }
                    encoder = match fonts.get(args[0].name()) {
                        Some(font) => self.encoder(font)?,
                        None => Encoder::Nop,
                    };
                }
                b"\"" => return Err(PageError),
                b"'" | b"Tj" => {
                    if args.len() != 1 {
                        return Err(PageError);
                    }
                    show(&mut text, &encoder.decode(args[0].raw_string())?);
                }
                b"TJ" => {
                    // Go indexes `args[0]`: no operands panics.
                    let Some(first) = args.first() else {
                        return Err(PageError);
                    };
                    if let Val::Array(items) = first {
                        for item in items {
                            if let Val::Str(bytes) = item {
                                show(&mut text, &encoder.decode(bytes)?);
                            }
                        }
                    }
                }
                _ => {}
            }
            Ok(())
        })?;
        Ok(text)
    }
}

/// `for _, ch := range s { WriteRune(ch) }`: invalid UTF-8 becomes U+FFFD per
/// byte.
fn push_go_runes(out: &mut String, bytes: &[u8]) {
    let mut rest = bytes;
    while !rest.is_empty() {
        match std::str::from_utf8(rest) {
            Ok(valid) => {
                out.push_str(valid);
                return;
            }
            Err(error) => {
                let (valid, after) = rest.split_at(error.valid_up_to());
                out.push_str(std::str::from_utf8(valid).unwrap_or_default());
                out.push(NO_RUNE);
                rest = &after[1..];
            }
        }
    }
}

/// Go's `pdf.NewReaderEncrypted` checks that precede object parsing.
fn go_open_checks(bytes: &[u8]) -> Result<(), &'static str> {
    let mut head = [0_u8; 10];
    let count = bytes.len().min(10);
    head[..count].copy_from_slice(&bytes[..count]);
    if !head.starts_with(b"%PDF-1.")
        || !(b'0'..=b'7').contains(&head[7])
        || (head[8] != b'\r' && head[8] != b'\n')
    {
        return Err("not a PDF file: invalid header");
    }
    const END_CHUNK: usize = 100;
    let mut tail = if bytes.len() >= END_CHUNK {
        bytes[bytes.len() - END_CHUNK..].to_vec()
    } else {
        vec![0; END_CHUNK]
    };
    // `len(buf) > 0 && last == '\n' || last == '\r'`
    while tail
        .last()
        .is_some_and(|byte| *byte == b'\n' || *byte == b'\r')
    {
        tail.pop();
    }
    while tail
        .last()
        .is_some_and(|byte| matches!(byte, b'\r' | b'\n' | b'\t' | b' '))
    {
        tail.pop();
    }
    if !tail.ends_with(b"%%EOF") {
        return Err("not a PDF file: missing %%EOF");
    }
    let Some(index) = find_last_line(&tail, b"startxref") else {
        return Err("malformed PDF file: missing final startxref");
    };
    let mut lexer = Lexer::new(&tail[index..]);
    let keyword = lexer.read_token();
    if !matches!(&keyword, Ok(Tok::Kw(word)) if word == b"startxref") {
        return Err("malformed PDF file: missing startxref");
    }
    if !matches!(lexer.read_token(), Ok(Tok::Int(_))) {
        return Err("malformed PDF file: startxref not followed by integer");
    }
    Ok(())
}

fn find_last_line(buf: &[u8], needle: &[u8]) -> Option<usize> {
    let mut max = buf.len();
    loop {
        let index = buf[..max]
            .windows(needle.len())
            .rposition(|window| window == needle)?;
        if index == 0 || index + needle.len() >= buf.len() {
            return None;
        }
        let before = buf[index - 1];
        let after = buf[index + needle.len()];
        if (before == b'\n' || before == b'\r') && (after == b'\n' || after == b'\r') {
            return Some(index);
        }
        max = index;
    }
}

fn pages(bytes: &[u8]) -> Result<Vec<(u32, String)>, SidecarError> {
    pages_with_limit(bytes, DECOMPRESSED_LIMIT)
}

fn pages_with_limit(bytes: &[u8], limit: usize) -> Result<Vec<(u32, String)>, SidecarError> {
    go_open_checks(bytes).map_err(|reason| contract(format!("failed to open PDF: {reason}")))?;
    let doc =
        Document::load_mem(bytes).map_err(|e| contract(format!("failed to open PDF: {e}")))?;
    let pdf = Pdf { doc: &doc, limit };
    let trailer = Object::Dictionary(doc.trailer.clone());
    let root = pdf.key(&trailer, b"Root");
    let pages_root = pdf.key(root, b"Pages");
    let total = pdf.int(pdf.key(pages_root, b"Count"));
    let mut result = Vec::new();
    for number in 1..=total.min(LOOP_BOUND as i64) {
        let Some(page) = pdf.page(number, pages_root) else {
            continue;
        };
        if let Ok(text) = pdf.page_text(page) {
            result.push((number as u32, text));
        }
    }
    Ok(result)
}

pub(super) fn text(bytes: &[u8]) -> Result<Vec<u8>, SidecarError> {
    let mut text = String::new();
    for (_, page) in pages(bytes)? {
        text.push_str(&page);
        text.push('\n');
    }
    let text = text.trim();
    if text.is_empty() {
        return Err(contract(NO_TEXT));
    }
    Ok(symdesk_core::textnorm::dehyphenate(text).into_bytes())
}
pub(super) fn sections(bytes: &[u8]) -> Result<Vec<RetrievalByteSection>, SidecarError> {
    let mut sections = Vec::new();
    let mut start = 0;
    for (page, text) in pages(bytes)? {
        let text = text.trim();
        if text.is_empty() {
            continue;
        }
        sections.push(RetrievalByteSection {
            text: text.as_bytes().to_vec(),
            start,
            anchor: RetrievalAnchor {
                kind: "page".into(),
                value: page.to_string(),
            },
            synthetic: false,
        });
        start += text.len() + 2;
    }
    if sections.is_empty() {
        return Err(contract(NO_TEXT));
    }
    Ok(sections)
}

#[cfg(test)]
mod tests {
    use base64::{Engine, engine::general_purpose::STANDARD};
    #[test]
    fn retrieval_extract_pdf_decompression_safety_divergence() {
        let fixture: serde_json::Value = serde_json::from_slice(
            &std::fs::read(
                std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
                    .join("../../testdata/port/retrieval/document-extraction.json"),
            )
            .unwrap(),
        )
        .unwrap();
        let case = fixture["cases"]
            .as_array()
            .unwrap()
            .iter()
            .find(|c| c["id"] == "simple.pdf")
            .unwrap();
        let bytes = STANDARD
            .decode(case["input_base64"].as_str().unwrap())
            .unwrap();
        let mut doc = lopdf::Document::load_mem(&bytes).unwrap();
        for object in doc.objects.values_mut() {
            if let lopdf::Object::Stream(stream) = object {
                stream.set_plain_content("BT /F1 12 Tf (x) Tj ET\n".repeat(100).into_bytes());
                stream.compress().unwrap();
            }
        }
        let mut compressed = Vec::new();
        doc.save_to(&mut compressed).unwrap();
        assert_eq!(super::DECOMPRESSED_LIMIT, 100 << 20);
        assert!(
            !super::pages_with_limit(&compressed, super::DECOMPRESSED_LIMIT)
                .unwrap()
                .is_empty()
        );
        // Go has no equivalent cap. Exercise the exact production limit path
        // at a small bound rather than allocating a 100-MiB test stream.
        assert!(super::pages_with_limit(&compressed, 32).unwrap().is_empty());
    }
}
