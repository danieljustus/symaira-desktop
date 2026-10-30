//! A YAML reader for the press frontmatter contract that keeps what `yaml.v3`
//! keeps and the typed readers drop: the original scalar text, the node line,
//! the scalar style and the explicit tag. `yaml.v3` decodes into string fields
//! from that text (`title: 0x1F` is the string `0x1F`) and reports errors by
//! line, so the port needs the same view of the document.
//!
//! Supported: block and flow mappings and sequences, plain, single and double
//! quoted and `|`/`>` block scalars, comments, tags, anchors, aliases and
//! the `<<` merge key. Documents the reader cannot parse are reported as a
//! syntax error; the `yaml.v3` scanner wording for those is not reproduced
//! (declared limit, pinned by the fixture's `syntax_error` flag).

use std::collections::HashMap;

#[derive(Clone, Debug, PartialEq)]
pub enum Style {
    Plain,
    Quoted,
    Block,
}

#[derive(Clone, Debug)]
pub enum Kind {
    Scalar { text: String, style: Style },
    Seq(Vec<Node>),
    Map(Vec<(Node, Node)>),
}

#[derive(Clone, Debug)]
pub struct Node {
    pub kind: Kind,
    pub tag: Option<String>,
    pub line: usize,
}

#[derive(Debug)]
pub struct SyntaxError {
    pub line: usize,
    pub message: String,
}

type Res<T> = Result<T, SyntaxError>;

struct Line {
    no: usize,
    indent: usize,
    text: String,
}

struct Parser {
    lines: Vec<Line>,
    at: usize,
    anchors: HashMap<String, Node>,
}

fn err<T>(line: usize, message: &str) -> Res<T> {
    Err(SyntaxError {
        line,
        message: message.to_owned(),
    })
}

/// Parse the first document of `src`. An empty or comment-only document is `None`.
pub fn parse(src: &str) -> Res<Option<Node>> {
    let mut lines = Vec::new();
    for (i, raw) in src.split('\n').enumerate() {
        let trimmed_end = raw.trim_end_matches('\r');
        let indent = trimmed_end.len() - trimmed_end.trim_start_matches(' ').len();
        let rest = &trimmed_end[indent..];
        if rest.starts_with('\t') && !rest.trim().is_empty() {
            return err(i + 1, "found character that cannot start any token");
        }
        lines.push(Line {
            no: i + 1,
            indent,
            text: trimmed_end.to_owned(),
        });
    }
    let mut p = Parser {
        lines,
        at: 0,
        anchors: HashMap::new(),
    };
    p.skip_blank();
    if p.at >= p.lines.len() {
        return Ok(None);
    }
    let indent = p.lines[p.at].indent;
    let node = p.block(indent)?;
    p.skip_blank();
    if p.at < p.lines.len() {
        return err(p.lines[p.at].no, "did not find expected <document start>");
    }
    Ok(Some(node))
}

fn is_blank(text: &str) -> bool {
    let t = text.trim_start_matches(' ');
    t.is_empty() || t.starts_with('#')
}

impl Parser {
    fn skip_blank(&mut self) {
        while self.at < self.lines.len() && is_blank(&self.lines[self.at].text) {
            self.at += 1;
        }
    }

    /// Parse the block node whose first line has the given indent.
    fn block(&mut self, indent: usize) -> Res<Node> {
        let line = &self.lines[self.at];
        let rest = &line.text[line.indent..];
        if rest == "-" || rest.starts_with("- ") {
            return self.block_seq(indent);
        }
        if find_map_colon(rest).is_some() {
            return self.block_map(indent);
        }
        // A scalar (or flow collection, anchor/tag prefixed node) on its own line.
        let no = line.no;
        let text = rest.to_owned();
        self.at += 1;
        self.value_text(&text, no, indent, true)
    }

    fn block_seq(&mut self, indent: usize) -> Res<Node> {
        let first = self.lines[self.at].no;
        let mut items = Vec::new();
        loop {
            self.skip_blank();
            if self.at >= self.lines.len() {
                break;
            }
            let (ind, no) = (self.lines[self.at].indent, self.lines[self.at].no);
            if ind < indent {
                break;
            }
            let rest = self.lines[self.at].text[ind..].to_owned();
            if ind > indent {
                return err(no, "did not find expected '-' indicator");
            }
            if !(rest == "-" || rest.starts_with("- ")) {
                break;
            }
            let after = rest[1..].trim_start_matches(' ');
            let consumed = rest.len() - after.len();
            if after.is_empty() || after.starts_with('#') {
                self.at += 1;
                self.skip_blank();
                if self.at < self.lines.len() && self.lines[self.at].indent > indent {
                    let i = self.lines[self.at].indent;
                    items.push(self.block(i)?);
                } else {
                    items.push(null_node(no));
                }
            } else if find_map_colon(after).is_some() && !after.starts_with(['[', '{', '"', '\'']) {
                // Compact mapping: re-read the rest of the line as a mapping line.
                let new_indent = indent + consumed;
                self.lines[self.at] = Line {
                    no,
                    indent: new_indent,
                    text: format!("{}{}", " ".repeat(new_indent), after),
                };
                items.push(self.block_map(new_indent)?);
            } else if after.starts_with("- ") || after == "-" {
                let new_indent = indent + consumed;
                self.lines[self.at] = Line {
                    no,
                    indent: new_indent,
                    text: format!("{}{}", " ".repeat(new_indent), after),
                };
                items.push(self.block_seq(new_indent)?);
            } else {
                self.at += 1;
                items.push(self.value_text(after, no, indent, false)?);
            }
        }
        Ok(Node {
            kind: Kind::Seq(items),
            tag: None,
            line: first,
        })
    }

    fn block_map(&mut self, indent: usize) -> Res<Node> {
        let first = self.lines[self.at].no;
        let mut pairs: Vec<(Node, Node)> = Vec::new();
        loop {
            self.skip_blank();
            if self.at >= self.lines.len() {
                break;
            }
            let (ind, no) = (self.lines[self.at].indent, self.lines[self.at].no);
            if ind < indent {
                break;
            }
            if ind > indent {
                return err(no, "mapping values are not allowed in this context");
            }
            let rest = self.lines[self.at].text[ind..].to_owned();
            if rest == "-" || rest.starts_with("- ") {
                break;
            }
            let Some(colon) = find_map_colon(&rest) else {
                return err(no, "could not find expected ':'");
            };
            let key_text = rest[..colon].trim_end();
            let after = rest[colon + 1..].trim_start_matches(' ');
            let key = scalar_from_key(key_text, no)?;
            self.at += 1;
            let value = if after.is_empty() || after.starts_with('#') {
                self.skip_blank();
                if self.at < self.lines.len()
                    && (self.lines[self.at].indent > indent || self.is_seq_at(indent))
                {
                    let i = self.lines[self.at].indent;
                    self.block(i)?
                } else {
                    null_node(no)
                }
            } else {
                self.value_text(after, no, indent, false)?
            };
            pairs.push((key, value));
        }
        Ok(Node {
            kind: Kind::Map(pairs),
            tag: None,
            line: first,
        })
    }

    fn is_seq_at(&self, indent: usize) -> bool {
        let l = &self.lines[self.at];
        l.indent == indent && (l.text[l.indent..] == *"-" || l.text[l.indent..].starts_with("- "))
    }

    /// Parse a value that starts inline on `no`. `self.at` is already past `no`.
    fn value_text(&mut self, text: &str, no: usize, indent: usize, _top: bool) -> Res<Node> {
        let mut text = strip_comment(text).trim_end().to_owned();
        let mut tag = None;
        let mut anchor = None;
        loop {
            let t = text.trim_start();
            if t.starts_with('!') {
                let end = t.find(' ').unwrap_or(t.len());
                tag = Some(t[..end].to_owned());
                text = t[end..].trim_start().to_owned();
            } else if t.starts_with('&') {
                let end = t.find(' ').unwrap_or(t.len());
                anchor = Some(t[1..end].to_owned());
                text = t[end..].trim_start().to_owned();
            } else {
                break;
            }
        }
        let text = text.trim();
        if let Some(name) = text.strip_prefix('*') {
            return match self.anchors.get(name.trim()) {
                Some(n) => Ok(n.clone()),
                None => err(no, "unknown anchor referenced"),
            };
        }
        let mut node = if text.is_empty() {
            // Tag/anchor with the content on the following lines.
            self.skip_blank();
            if self.at < self.lines.len() && self.lines[self.at].indent > indent {
                let i = self.lines[self.at].indent;
                self.block(i)?
            } else {
                null_node(no)
            }
        } else if text.starts_with('[') || text.starts_with('{') {
            let mut buf = text.to_owned();
            while !flow_balanced(&buf) {
                if self.at >= self.lines.len() {
                    return err(no, "did not find expected ',' or ']'");
                }
                buf.push(' ');
                buf.push_str(strip_comment(self.lines[self.at].text.trim()));
                self.at += 1;
            }
            let mut fp = Flow {
                s: buf.as_bytes(),
                i: 0,
                line: no,
                anchors: &mut self.anchors,
            };
            let n = fp.node()?;
            fp.ws();
            if fp.i < fp.s.len() {
                return err(no, "did not find expected <document start>");
            }
            n
        } else if text.starts_with('|') || text.starts_with('>') {
            self.block_scalar(text, no, indent)?
        } else if text.starts_with('"') || text.starts_with('\'') {
            let mut buf = text.to_owned();
            while !quote_closed(&buf) {
                if self.at >= self.lines.len() {
                    return err(
                        no,
                        "found unexpected end of stream while scanning a quoted scalar",
                    );
                }
                buf.push('\n');
                buf.push_str(self.lines[self.at].text.trim());
                self.at += 1;
            }
            let (s, used) = parse_quoted(&buf, no)?;
            if !buf[used..].trim().is_empty() && !buf[used..].trim_start().starts_with('#') {
                return err(no, "did not find expected key");
            }
            Node {
                kind: Kind::Scalar {
                    text: s,
                    style: Style::Quoted,
                },
                tag: None,
                line: no,
            }
        } else {
            // Plain scalar, possibly continued on more-indented lines. A `: `
            // inside one would start a mapping, which is not allowed here.
            if has_plain_colon(text) {
                return err(no, "mapping values are not allowed in this context");
            }
            let mut s = text.to_owned();
            while self.at < self.lines.len()
                && !is_blank(&self.lines[self.at].text)
                && self.lines[self.at].indent > indent
            {
                let more = strip_comment(self.lines[self.at].text.trim())
                    .trim_end()
                    .to_owned();
                if more.is_empty() {
                    break;
                }
                if has_plain_colon(&more) {
                    return err(
                        self.lines[self.at].no,
                        "mapping values are not allowed in this context",
                    );
                }
                s.push(' ');
                s.push_str(&more);
                self.at += 1;
            }
            Node {
                kind: Kind::Scalar {
                    text: s,
                    style: Style::Plain,
                },
                tag: None,
                line: no,
            }
        };
        if tag.is_some() {
            node.tag = tag;
        }
        if let Some(a) = anchor {
            self.anchors.insert(a, node.clone());
        }
        Ok(node)
    }

    fn block_scalar(&mut self, header: &str, no: usize, indent: usize) -> Res<Node> {
        let folded = header.starts_with('>');
        let mut chomp = 'c';
        for ch in header[1..].chars() {
            match ch {
                '-' => chomp = '-',
                '+' => chomp = '+',
                d if d.is_ascii_digit() => {}
                ' ' => break,
                _ => return err(no, "did not find expected comment or line break"),
            }
        }
        let mut body: Vec<String> = Vec::new();
        let mut block_indent: Option<usize> = None;
        while self.at < self.lines.len() {
            let l = &self.lines[self.at];
            let blank = l.text.trim().is_empty();
            if !blank {
                let bi = *block_indent.get_or_insert(l.indent);
                if l.indent < bi || l.indent <= indent {
                    break;
                }
                body.push(l.text[bi.min(l.text.len())..].to_owned());
            } else {
                body.push(match block_indent {
                    Some(bi) if l.text.len() > bi => l.text[bi..].to_owned(),
                    _ => String::new(),
                });
            }
            self.at += 1;
        }
        // Trailing blank lines belong to the chomping decision.
        let mut trailing = 0;
        while body.last().is_some_and(String::is_empty) {
            body.pop();
            trailing += 1;
        }
        let mut text = String::new();
        if folded {
            let mut prev_more = false;
            for (i, l) in body.iter().enumerate() {
                let more = l.starts_with(' ') || l.starts_with('\t');
                if i > 0 {
                    if l.is_empty() {
                        text.push('\n');
                    } else if !body[i - 1].is_empty() && !more && !prev_more {
                        text.push(' ');
                    } else if !body[i - 1].is_empty() {
                        text.push('\n');
                    }
                }
                text.push_str(l);
                prev_more = more;
            }
        } else {
            text = body.join("\n");
        }
        // A block that runs to the end of a stream without a final line break
        // has no trailing newline to clip or keep.
        let no_final_break = self.at >= self.lines.len() && trailing == 0;
        if !body.is_empty() && !no_final_break {
            match chomp {
                '-' => {}
                '+' => {
                    text.push('\n');
                    for _ in 0..trailing {
                        text.push('\n');
                    }
                }
                _ => text.push('\n'),
            }
        }
        Ok(Node {
            kind: Kind::Scalar {
                text,
                style: Style::Block,
            },
            tag: None,
            line: no,
        })
    }
}

fn has_plain_colon(s: &str) -> bool {
    let b = s.as_bytes();
    (0..b.len()).any(|i| b[i] == b':' && (i + 1 == b.len() || b[i + 1] == b' '))
}

fn null_node(line: usize) -> Node {
    Node {
        kind: Kind::Scalar {
            text: String::new(),
            style: Style::Plain,
        },
        tag: None,
        line,
    }
}

/// Index of the `:` that separates a block mapping key from its value, if the
/// line is a mapping entry.
fn find_map_colon(s: &str) -> Option<usize> {
    let b = s.as_bytes();
    let mut i = 0;
    if b.is_empty() {
        return None;
    }
    if b[0] == b'"' || b[0] == b'\'' {
        let q = b[0];
        i = 1;
        while i < b.len() {
            if q == b'"' && b[i] == b'\\' {
                i += 2;
                continue;
            }
            if b[i] == q {
                if q == b'\'' && b.get(i + 1) == Some(&b'\'') {
                    i += 2;
                    continue;
                }
                i += 1;
                break;
            }
            i += 1;
        }
        let rest = &s[i.min(s.len())..];
        let t = rest.trim_start_matches(' ');
        return if t.starts_with(':') && (t.len() == 1 || t.as_bytes()[1] == b' ') {
            Some(i + (rest.len() - t.len()))
        } else {
            None
        };
    }
    if b[0] == b'[' || b[0] == b'{' || b[0] == b'#' {
        return None;
    }
    while i < b.len() {
        if b[i] == b':' && (i + 1 == b.len() || b[i + 1] == b' ') {
            return Some(i);
        }
        if b[i] == b' ' && b.get(i + 1) == Some(&b'#') {
            return None;
        }
        i += 1;
    }
    None
}

fn scalar_from_key(text: &str, line: usize) -> Res<Node> {
    let mut tag = None;
    let mut t = text.trim();
    while t.starts_with('!') {
        let end = t.find(' ').unwrap_or(t.len());
        tag = Some(t[..end].to_owned());
        t = t[end..].trim_start();
    }
    if t.starts_with('"') || t.starts_with('\'') {
        let (s, _) = parse_quoted(t, line)?;
        return Ok(Node {
            kind: Kind::Scalar {
                text: s,
                style: Style::Quoted,
            },
            tag,
            line,
        });
    }
    Ok(Node {
        kind: Kind::Scalar {
            text: t.to_owned(),
            style: Style::Plain,
        },
        tag,
        line,
    })
}

fn strip_comment(s: &str) -> &str {
    let b = s.as_bytes();
    let (mut sq, mut dq) = (false, false);
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'\\' if dq => i += 1,
            b'"' if !sq => dq = !dq,
            b'\'' if !dq => sq = !sq,
            b'#' if !sq && !dq && (i == 0 || b[i - 1] == b' ' || b[i - 1] == b'\t') => {
                return &s[..i];
            }
            _ => {}
        }
        i += 1;
    }
    s
}

fn flow_balanced(s: &str) -> bool {
    let (mut depth, mut sq, mut dq) = (0i32, false, false);
    let b = s.as_bytes();
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'\\' if dq => i += 1,
            b'"' if !sq => dq = !dq,
            b'\'' if !dq => sq = !sq,
            b'[' | b'{' if !sq && !dq => depth += 1,
            b']' | b'}' if !sq && !dq => depth -= 1,
            _ => {}
        }
        i += 1;
    }
    depth <= 0 && !sq && !dq
}

fn quote_closed(s: &str) -> bool {
    parse_quoted(s, 0).is_ok()
}

/// Parse a quoted scalar at the start of `s`; returns the value and the bytes used.
fn parse_quoted(s: &str, line: usize) -> Res<(String, usize)> {
    let b = s.as_bytes();
    let q = b[0];
    let mut out = String::new();
    let mut i = 1;
    while i < b.len() {
        let c = b[i];
        if q == b'\'' {
            if c == b'\'' {
                if b.get(i + 1) == Some(&b'\'') {
                    out.push('\'');
                    i += 2;
                    continue;
                }
                return Ok((out, i + 1));
            }
        } else {
            if c == b'"' {
                return Ok((out, i + 1));
            }
            if c == b'\\' {
                i += 1;
                let Some(&e) = b.get(i) else { break };
                match e {
                    b'n' => out.push('\n'),
                    b't' => out.push('\t'),
                    b'r' => out.push('\r'),
                    b'0' => out.push('\0'),
                    b'a' => out.push('\x07'),
                    b'b' => out.push('\x08'),
                    b'e' => out.push('\x1b'),
                    b'f' => out.push('\x0c'),
                    b'v' => out.push('\x0b'),
                    b' ' => out.push(' '),
                    b'/' => out.push('/'),
                    b'"' => out.push('"'),
                    b'\\' => out.push('\\'),
                    b'N' => out.push('\u{85}'),
                    b'_' => out.push('\u{a0}'),
                    b'L' => out.push('\u{2028}'),
                    b'P' => out.push('\u{2029}'),
                    b'x' | b'u' | b'U' => {
                        let n = match e {
                            b'x' => 2,
                            b'u' => 4,
                            _ => 8,
                        };
                        let hex = s.get(i + 1..i + 1 + n).unwrap_or("");
                        let Ok(v) = u32::from_str_radix(hex, 16) else {
                            return err(line, "found unknown escape character");
                        };
                        let Some(ch) = char::from_u32(v) else {
                            return err(line, "found invalid Unicode character escape code");
                        };
                        out.push(ch);
                        i += n;
                    }
                    b'\n' => {
                        i += 1;
                        while b.get(i) == Some(&b' ') {
                            i += 1;
                        }
                        continue;
                    }
                    _ => return err(line, "found unknown escape character"),
                }
                i += 1;
                continue;
            }
        }
        if c == b'\n' {
            // Line folding inside quoted scalars: a break becomes a space.
            while out.ends_with(' ') {
                out.pop();
            }
            let mut blank = 0;
            let mut j = i + 1;
            while j < b.len() && (b[j] == b' ' || b[j] == b'\n') {
                if b[j] == b'\n' {
                    blank += 1;
                }
                j += 1;
            }
            if blank == 0 {
                out.push(' ');
            } else {
                for _ in 0..blank {
                    out.push('\n');
                }
            }
            i = j;
            continue;
        }
        let ch_len = s[i..].chars().next().map_or(1, char::len_utf8);
        out.push_str(&s[i..i + ch_len]);
        i += ch_len;
    }
    err(
        line,
        "found unexpected end of stream while scanning a quoted scalar",
    )
}

struct Flow<'a, 'b> {
    s: &'a [u8],
    i: usize,
    line: usize,
    anchors: &'b mut HashMap<String, Node>,
}

impl Flow<'_, '_> {
    fn ws(&mut self) {
        while self.i < self.s.len() && (self.s[self.i] == b' ' || self.s[self.i] == b'\t') {
            self.i += 1;
        }
    }

    fn text(&self, from: usize, to: usize) -> &str {
        std::str::from_utf8(&self.s[from..to]).unwrap_or("")
    }

    fn node(&mut self) -> Res<Node> {
        self.ws();
        let line = self.line;
        let mut tag = None;
        let mut anchor = None;
        loop {
            match self.s.get(self.i) {
                Some(b'!') => {
                    let st = self.i;
                    while self.i < self.s.len()
                        && !matches!(self.s[self.i], b' ' | b',' | b']' | b'}')
                    {
                        self.i += 1;
                    }
                    tag = Some(self.text(st, self.i).to_owned());
                    self.ws();
                }
                Some(b'&') => {
                    let st = self.i + 1;
                    while self.i < self.s.len()
                        && !matches!(self.s[self.i], b' ' | b',' | b']' | b'}')
                    {
                        self.i += 1;
                    }
                    anchor = Some(self.text(st, self.i).to_owned());
                    self.ws();
                }
                _ => break,
            }
        }
        let mut node = match self.s.get(self.i) {
            None => return err(line, "did not find expected node content"),
            Some(b'[') => {
                self.i += 1;
                let mut items = Vec::new();
                loop {
                    self.ws();
                    match self.s.get(self.i) {
                        Some(b']') => {
                            self.i += 1;
                            break;
                        }
                        None => return err(line, "did not find expected ',' or ']'"),
                        _ => {}
                    }
                    let item = self.node()?;
                    self.ws();
                    // `[a: b]` single-pair mapping inside a sequence.
                    if self.s.get(self.i) == Some(&b':')
                        && matches!(self.s.get(self.i + 1), Some(b' ') | None)
                    {
                        self.i += 1;
                        let v = self.node()?;
                        items.push(Node {
                            kind: Kind::Map(vec![(item, v)]),
                            tag: None,
                            line,
                        });
                    } else {
                        items.push(item);
                    }
                    self.ws();
                    match self.s.get(self.i) {
                        Some(b',') => self.i += 1,
                        Some(b']') => {}
                        _ => return err(line, "did not find expected ',' or ']'"),
                    }
                }
                Node {
                    kind: Kind::Seq(items),
                    tag: None,
                    line,
                }
            }
            Some(b'{') => {
                self.i += 1;
                let mut pairs = Vec::new();
                loop {
                    self.ws();
                    match self.s.get(self.i) {
                        Some(b'}') => {
                            self.i += 1;
                            break;
                        }
                        None => return err(line, "did not find expected ',' or '}'"),
                        _ => {}
                    }
                    let k = self.node()?;
                    self.ws();
                    let v = if self.s.get(self.i) == Some(&b':') {
                        self.i += 1;
                        self.ws();
                        if matches!(self.s.get(self.i), Some(b',') | Some(b'}')) {
                            null_node(line)
                        } else {
                            self.node()?
                        }
                    } else {
                        null_node(line)
                    };
                    pairs.push((k, v));
                    self.ws();
                    match self.s.get(self.i) {
                        Some(b',') => self.i += 1,
                        Some(b'}') => {}
                        _ => return err(line, "did not find expected ',' or '}'"),
                    }
                }
                Node {
                    kind: Kind::Map(pairs),
                    tag: None,
                    line,
                }
            }
            Some(b'"') | Some(b'\'') => {
                let rest = std::str::from_utf8(&self.s[self.i..]).unwrap_or("");
                let (s, used) = parse_quoted(rest, line)?;
                self.i += used;
                Node {
                    kind: Kind::Scalar {
                        text: s,
                        style: Style::Quoted,
                    },
                    tag: None,
                    line,
                }
            }
            Some(b'*') => {
                let st = self.i + 1;
                while self.i < self.s.len() && !matches!(self.s[self.i], b' ' | b',' | b']' | b'}')
                {
                    self.i += 1;
                }
                let name = self.text(st, self.i).to_owned();
                match self.anchors.get(&name) {
                    Some(n) => n.clone(),
                    None => return err(line, "unknown anchor referenced"),
                }
            }
            Some(_) => {
                let st = self.i;
                while self.i < self.s.len() {
                    let c = self.s[self.i];
                    if matches!(c, b',' | b']' | b'}') {
                        break;
                    }
                    if c == b':'
                        && matches!(
                            self.s.get(self.i + 1),
                            Some(b' ') | Some(b',') | Some(b'}') | Some(b']') | None
                        )
                    {
                        break;
                    }
                    self.i += 1;
                }
                let t = self.text(st, self.i).trim().to_owned();
                Node {
                    kind: Kind::Scalar {
                        text: t,
                        style: Style::Plain,
                    },
                    tag: None,
                    line,
                }
            }
        };
        if tag.is_some() {
            node.tag = tag;
        }
        if let Some(a) = anchor {
            self.anchors.insert(a, node.clone());
        }
        Ok(node)
    }
}
