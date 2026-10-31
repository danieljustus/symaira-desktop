//! A minimal IMAP4rev1 client over TLS: the exact command subset the mail poller
//! drives (`internal/ingest/internal/ingest/mail.go` through `go-imap/v2`).
//! Command spellings, set formatting, mailbox quoting/UTF-7 and the error text
//! (`imap: NO [CODE] text`) follow go-imap v2.0.0-beta.8; the oracle fixture
//! (`testdata/port/ingest/mail.json`) pins every command the Go client sent.

use rustls::ClientConfig;
use rustls::pki_types::ServerName;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpStream, ToSocketAddrs};
use std::sync::Arc;
use std::time::Duration;

/// `imapMessage.Envelope`: only the Message-ID is used.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ImapEnvelope {
    pub message_id: String,
}

/// `imapMessage`.
#[derive(Clone, Debug, Default)]
pub struct ImapMessage {
    pub seq_num: u32,
    pub uid: u32,
    pub envelope: Option<ImapEnvelope>,
    pub body: Vec<u8>,
}

/// `mailboxStatus`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct MailboxStatus {
    pub uid_validity: u32,
    pub uid_next: u32,
}

/// The `imapClient` interface the poller is written against.
pub trait ImapClient: Send {
    fn login(&mut self, username: &str, password: &str) -> Result<(), String>;
    fn select(&mut self, folder: &str) -> Result<MailboxStatus, String>;
    /// `UID SEARCH UID <start>:* [UNSEEN]`.
    fn search_uid(&mut self, start_uid: u32, unseen: bool) -> Result<Vec<u32>, String>;
    fn fetch_envelopes_uid(&mut self, uids: &[u32]) -> Result<Vec<ImapMessage>, String>;
    fn fetch_uid(&mut self, uids: &[u32]) -> Result<Vec<ImapMessage>, String>;
    fn store_seen(&mut self, seq: u32) -> Result<(), String>;
    fn move_to(&mut self, seq: u32, dest: &str) -> Result<(), String>;
    fn close(&mut self) -> Result<(), String>;
}

// ------------------------------------------------------------- value parser

#[derive(Clone, Debug, PartialEq)]
enum Val {
    Nil,
    Atom(String),
    Num(u64),
    Str(Vec<u8>),
    List(Vec<Val>),
}

struct Parser<'a> {
    b: &'a [u8],
    i: usize,
    /// Literals follow the line that announced them.
    literals: std::vec::IntoIter<Vec<u8>>,
}

impl<'a> Parser<'a> {
    fn skip_sp(&mut self) {
        while self.b.get(self.i) == Some(&b' ') {
            self.i += 1;
        }
    }

    fn value(&mut self) -> Result<Val, String> {
        self.skip_sp();
        match self.b.get(self.i) {
            None => Err("unexpected end of response".into()),
            Some(b'(') => {
                self.i += 1;
                let mut items = Vec::new();
                loop {
                    self.skip_sp();
                    match self.b.get(self.i) {
                        Some(b')') => {
                            self.i += 1;
                            return Ok(Val::List(items));
                        }
                        None => return Err("unterminated list".into()),
                        _ => items.push(self.value()?),
                    }
                }
            }
            Some(b'"') => {
                self.i += 1;
                let mut out = Vec::new();
                while let Some(&c) = self.b.get(self.i) {
                    self.i += 1;
                    match c {
                        b'\\' => {
                            if let Some(&n) = self.b.get(self.i) {
                                out.push(n);
                                self.i += 1;
                            }
                        }
                        b'"' => return Ok(Val::Str(out)),
                        c => out.push(c),
                    }
                }
                Err("unterminated string".into())
            }
            Some(b'{') => {
                let close = self.b[self.i..]
                    .iter()
                    .position(|c| *c == b'}')
                    .ok_or("bad literal")?;
                self.i += close + 1;
                self.literals
                    .next()
                    .map(Val::Str)
                    .ok_or_else(|| "missing literal".to_owned())
            }
            Some(_) => {
                let start = self.i;
                let mut depth = 0i32;
                while let Some(&c) = self.b.get(self.i) {
                    match c {
                        b'[' => depth += 1,
                        b']' => depth -= 1,
                        b' ' | b'(' | b')' if depth <= 0 => break,
                        _ => {}
                    }
                    self.i += 1;
                }
                let text = String::from_utf8_lossy(&self.b[start..self.i]).into_owned();
                if text.eq_ignore_ascii_case("NIL") {
                    Ok(Val::Nil)
                } else if !text.is_empty() && text.bytes().all(|c| c.is_ascii_digit()) {
                    Ok(Val::Num(text.parse().unwrap_or(0)))
                } else {
                    Ok(Val::Atom(text))
                }
            }
        }
    }
}

// ------------------------------------------------------------------ errors

/// `imap.Error.Error()`.
fn imap_error(status: &str, code: &str, text: &str) -> String {
    let mut out = format!("imap: {status}");
    if !code.is_empty() {
        out.push_str(&format!(" [{code}]"));
    }
    let text = if text.is_empty() {
        "(no response text)"
    } else {
        text
    };
    out.push(' ');
    out.push_str(text);
    out
}

/// Go's `net.OpError` wording for the dial errors a poll can see.
fn dial_error(addr: &str, e: &std::io::Error) -> String {
    use std::io::ErrorKind;
    let reason = match e.kind() {
        ErrorKind::ConnectionRefused => "connection refused".to_owned(),
        ErrorKind::TimedOut | ErrorKind::WouldBlock => {
            return format!("dial tcp {addr}: i/o timeout");
        }
        ErrorKind::ConnectionReset => "connection reset by peer".to_owned(),
        ErrorKind::HostUnreachable => "no route to host".to_owned(),
        ErrorKind::NetworkUnreachable => "network is unreachable".to_owned(),
        _ => {
            let text = e.to_string();
            let text = text.split(" (os error").next().unwrap_or(&text);
            let mut chars = text.chars();
            match chars.next() {
                Some(c) => c.to_lowercase().chain(chars).collect(),
                None => text.to_owned(),
            }
        }
    };
    format!("dial tcp {addr}: connect: {reason}")
}

// ------------------------------------------------------------------ client

enum Conn {
    Tls(Box<rustls::StreamOwned<rustls::ClientConnection, TcpStream>>),
}

impl Read for Conn {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        match self {
            Conn::Tls(s) => s.read(buf),
        }
    }
}

impl Write for Conn {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        match self {
            Conn::Tls(s) => s.write(buf),
        }
    }
    fn flush(&mut self) -> std::io::Result<()> {
        match self {
            Conn::Tls(s) => s.flush(),
        }
    }
}

/// One tagged-command exchange: the untagged lines (literals attached) and the
/// tagged completion.
struct Response {
    untagged: Vec<(String, Vec<Vec<u8>>)>,
    status: String,
    code: String,
    code_args: String,
    text: String,
}

const IO_TIMEOUT: Duration = Duration::from_secs(120);

pub struct TlsImapClient {
    reader: BufReader<Conn>,
    next_tag: u32,
    /// `Some` once the connection failed; commands then report this text.
    dead: Option<String>,
    caps: Option<Vec<String>>,
}

/// A rustls client configuration trusting the platform roots (Go: system roots).
pub fn default_tls_config() -> Result<Arc<ClientConfig>, String> {
    let mut roots = rustls::RootCertStore::empty();
    let loaded = rustls_native_certs::load_native_certs();
    for cert in loaded.certs {
        let _ = roots.add(cert);
    }
    if roots.is_empty() {
        return Err("no system root certificates available".into());
    }
    tls_config_with_roots(roots)
}

/// A client configuration trusting exactly `roots`.
pub fn tls_config_with_roots(roots: rustls::RootCertStore) -> Result<Arc<ClientConfig>, String> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let mut config = ClientConfig::builder_with_provider(provider)
        .with_safe_default_protocol_versions()
        .map_err(|e| e.to_string())?
        .with_root_certificates(roots)
        .with_no_client_auth();
    config.alpn_protocols = vec![b"imap".to_vec()];
    Ok(Arc::new(config))
}

impl TlsImapClient {
    /// `imapclient.DialTLS(addr, ...)` followed by the greeting.
    pub fn dial_tls(addr: &str, host: &str, config: Arc<ClientConfig>) -> Result<Self, String> {
        let sock_addr = addr
            .to_socket_addrs()
            .map_err(|e| format!("dial tcp: lookup {host}: {e}"))?
            .next()
            .ok_or_else(|| format!("dial tcp: lookup {host}: no such host"))?;
        let tcp = TcpStream::connect_timeout(&sock_addr, Duration::from_secs(30))
            .map_err(|e| dial_error(addr, &e))?;
        let _ = tcp.set_read_timeout(Some(IO_TIMEOUT));
        let _ = tcp.set_write_timeout(Some(IO_TIMEOUT));
        let name = ServerName::try_from(host.to_owned())
            .map_err(|e| format!("tls: invalid server name {host:?}: {e}"))?;
        let conn = rustls::ClientConnection::new(config, name).map_err(|e| format!("tls: {e}"))?;
        let mut stream = rustls::StreamOwned::new(conn, tcp);
        // Complete the handshake eagerly so certificate errors surface as dial errors.
        while stream.conn.is_handshaking() {
            stream
                .conn
                .complete_io(&mut stream.sock)
                .map_err(|e| format!("tls: {e}"))?;
        }
        let mut client = Self {
            reader: BufReader::new(Conn::Tls(Box::new(stream))),
            next_tag: 0,
            dead: None,
            caps: None,
        };
        let (line, _) = client.read_response_line()?;
        let upper = line.to_uppercase();
        if !upper.starts_with("* OK") && !upper.starts_with("* PREAUTH") {
            return Err(format!("imap: unexpected greeting: {line}"));
        }
        client.note_caps(&line);
        Ok(client)
    }

    fn fail<T>(&mut self, text: impl Into<String>) -> Result<T, String> {
        let text = text.into();
        self.dead = Some(text.clone());
        Err(text)
    }

    fn io_fail<T>(&mut self, e: &std::io::Error) -> Result<T, String> {
        let text = match e.kind() {
            std::io::ErrorKind::UnexpectedEof => "unexpected EOF".to_owned(),
            std::io::ErrorKind::TimedOut | std::io::ErrorKind::WouldBlock => {
                "i/o timeout".to_owned()
            }
            _ => e.to_string(),
        };
        self.fail(text)
    }

    fn note_caps(&mut self, line: &str) {
        if let Some(i) = line.to_uppercase().find("[CAPABILITY ") {
            let rest = &line[i + "[CAPABILITY ".len()..];
            if let Some(j) = rest.find(']') {
                self.caps = Some(
                    rest[..j]
                        .split_whitespace()
                        .map(|s| s.to_uppercase())
                        .collect(),
                );
            }
        }
    }

    /// Reads one logical response line; literals announced by `{n}` are
    /// returned separately and the line keeps its `{n}` marker.
    fn read_response_line(&mut self) -> Result<(String, Vec<Vec<u8>>), String> {
        let mut line = Vec::new();
        let mut literals = Vec::new();
        loop {
            let mut part = Vec::new();
            match self.reader.read_until(b'\n', &mut part) {
                Ok(0) => return self.fail("unexpected EOF"),
                Ok(_) => {}
                Err(e) => return self.io_fail(&e),
            }
            if part.last() != Some(&b'\n') {
                return self.fail("unexpected EOF");
            }
            part.pop();
            if part.last() == Some(&b'\r') {
                part.pop();
            }
            line.extend_from_slice(&part);
            // A line ending in {n} is followed by n literal bytes, then the rest.
            if line.ends_with(b"}")
                && let Some(open) = line.iter().rposition(|c| *c == b'{')
                && let Ok(n) = std::str::from_utf8(&line[open + 1..line.len() - 1])
                    .unwrap_or("")
                    .trim_end_matches('+')
                    .parse::<usize>()
            {
                let mut lit = vec![0u8; n];
                if let Err(e) = self.reader.read_exact(&mut lit) {
                    return self.io_fail(&e);
                }
                literals.push(lit);
                continue;
            }
            break;
        }
        Ok((String::from_utf8_lossy(&line).into_owned(), literals))
    }

    fn send_line(&mut self, line: &str) -> Result<(), String> {
        let w = self.reader.get_mut();
        if let Err(e) = w.write_all(line.as_bytes()).and_then(|_| w.flush()) {
            return self.io_fail(&e);
        }
        Ok(())
    }

    /// Sends `args` (already atom/quoted/literal encoded; literals as
    /// `Arg::Literal`) and reads until the tagged completion.
    fn command(&mut self, name: &str, args: &[Arg]) -> Result<Response, String> {
        if let Some(dead) = &self.dead {
            return Err(dead.clone());
        }
        self.next_tag += 1;
        let tag = format!("T{}", self.next_tag);
        let mut line = format!("{tag} {name}");
        for arg in args {
            line.push(' ');
            match arg {
                Arg::Raw(s) => line.push_str(s),
                Arg::Literal(bytes) => {
                    line.push_str(&format!("{{{}}}\r\n", bytes.len()));
                    self.send_line(&line)?;
                    // Wait for the continuation request.
                    let (cont, _) = self.read_response_line()?;
                    if !cont.starts_with('+') {
                        return Err(format!("imap: expected continuation, got {cont:?}"));
                    }
                    let w = self.reader.get_mut();
                    if let Err(e) = w.write_all(bytes).and_then(|_| w.flush()) {
                        return self.io_fail(&e);
                    }
                    line = String::new();
                }
            }
        }
        line.push_str("\r\n");
        self.send_line(&line)?;
        let mut untagged = Vec::new();
        loop {
            let (text, literals) = self.read_response_line()?;
            if let Some(rest) = text.strip_prefix(&format!("{tag} ")) {
                let (status, rest) = rest.split_once(' ').unwrap_or((rest, ""));
                let (code, code_args, text) = split_code(rest);
                self.note_caps(&format!("{status} {rest}"));
                return Ok(Response {
                    untagged,
                    status: status.to_uppercase(),
                    code,
                    code_args,
                    text,
                });
            }
            if text.starts_with("* BYE") {
                untagged.push((text, literals));
                continue;
            }
            untagged.push((text, literals));
        }
    }

    fn ok(resp: &Response) -> Result<(), String> {
        if resp.status == "OK" {
            Ok(())
        } else {
            Err(imap_error(&resp.status, &resp.code, &resp.text))
        }
    }

    fn ensure_caps(&mut self) -> Result<Vec<String>, String> {
        if let Some(c) = &self.caps {
            return Ok(c.clone());
        }
        let resp = self.command("CAPABILITY", &[])?;
        Self::ok(&resp)?;
        for (line, _) in &resp.untagged {
            if let Some(rest) = line.strip_prefix("* CAPABILITY ") {
                self.caps = Some(rest.split_whitespace().map(|s| s.to_uppercase()).collect());
            }
        }
        Ok(self.caps.clone().unwrap_or_default())
    }

    fn parse_fetch(line: &str, literals: Vec<Vec<u8>>) -> Option<Result<ImapMessage, String>> {
        let rest = line.strip_prefix("* ")?;
        let (num, rest) = rest.split_once(' ')?;
        let rest = rest
            .strip_prefix("FETCH ")
            .or_else(|| rest.strip_prefix("fetch "))?;
        let seq = num.parse().ok()?;
        let mut p = Parser {
            b: rest.as_bytes(),
            i: 0,
            literals: literals.into_iter(),
        };
        let items = match p.value() {
            Ok(Val::List(items)) => items,
            Ok(_) => return Some(Err("fetch data is not a list".into())),
            Err(e) => return Some(Err(e)),
        };
        let mut msg = ImapMessage {
            seq_num: seq,
            ..ImapMessage::default()
        };
        let mut it = items.into_iter();
        while let (Some(Val::Atom(key)), Some(value)) = (it.next(), it.next()) {
            let key = key.to_uppercase();
            match (key.as_str(), value) {
                ("UID", Val::Num(n)) => msg.uid = n as u32,
                ("ENVELOPE", Val::List(fields)) => {
                    let id = match fields.get(9) {
                        Some(Val::Str(s)) => {
                            parse_msg_id(&String::from_utf8_lossy(s)).unwrap_or_default()
                        }
                        _ => String::new(),
                    };
                    msg.envelope = Some(ImapEnvelope { message_id: id });
                }
                ("BODY[]", Val::Str(s)) => msg.body = s,
                _ => {}
            }
        }
        Some(Ok(msg))
    }

    fn fetch(&mut self, uids: &[u32], items: &str) -> Result<Vec<ImapMessage>, String> {
        if uids.is_empty() {
            return Ok(Vec::new());
        }
        let set = uid_set(uids);
        let resp = self.command("UID", &[Arg::Raw(format!("FETCH {set} {items}"))])?;
        let mut out = Vec::new();
        for (line, literals) in &resp.untagged {
            // go-imap logs and skips messages it fails to collect.
            if let Some(Ok(msg)) = Self::parse_fetch(line, literals.clone()) {
                out.push(msg);
            }
        }
        Self::ok(&resp)?;
        Ok(out)
    }
}

/// `mail.Header.MessageID` on a single `Message-Id` value (go-message
/// `headerParser.parseMsgID`): the identifier without angle brackets; `None`
/// when the value is not a well-formed msg-id.
pub fn parse_msg_id(value: &str) -> Option<String> {
    struct P<'a>(&'a str);
    impl P<'_> {
        fn skip_space(&mut self) {
            self.0 = self.0.trim_start_matches([' ', '\t']);
        }
        fn consume(&mut self, c: char) -> bool {
            match self.0.strip_prefix(c) {
                Some(rest) => {
                    self.0 = rest;
                    true
                }
                None => false,
            }
        }
        fn skip_cfws(&mut self) -> bool {
            self.skip_space();
            while self.consume('(') {
                let mut depth = 1;
                let mut chars = self.0.char_indices();
                let mut end = self.0.len();
                let mut closed = false;
                while let Some((i, c)) = chars.next() {
                    match c {
                        '\\' if i + 1 < self.0.len() => {
                            chars.next();
                        }
                        '(' => depth += 1,
                        ')' => {
                            depth -= 1;
                            if depth == 0 {
                                end = i + 1;
                                closed = true;
                                break;
                            }
                        }
                        _ => {}
                    }
                }
                if !closed {
                    return false;
                }
                self.0 = &self.0[end..];
                self.skip_space();
            }
            true
        }
        fn atext(&mut self, dot: bool) -> Option<&str> {
            let end = self
                .0
                .char_indices()
                .find(|(_, c)| !is_atext(*c, dot))
                .map_or(self.0.len(), |(i, _)| i);
            if end == 0 {
                return None;
            }
            let (atom, rest) = self.0.split_at(end);
            self.0 = rest;
            Some(atom)
        }
    }
    fn is_vchar(c: char) -> bool {
        ('!'..='~').contains(&c) || !c.is_ascii()
    }
    fn is_atext(c: char, dot: bool) -> bool {
        match c {
            '.' => dot,
            '(' | ')' | '[' | ']' | ';' | '@' | '\\' | ',' | '<' | '>' | '"' | ':' => false,
            c => is_vchar(c),
        }
    }
    let mut p = P(value);
    if !p.skip_cfws() || !p.consume('<') {
        return None;
    }
    let left = p.atext(true)?.to_owned();
    if !p.consume('@') {
        return None;
    }
    let right = if p.0.starts_with('[') {
        p.consume('[');
        let end =
            p.0.char_indices()
                .find(|(_, c)| matches!(c, '[' | ']' | '\\') || !is_vchar(*c))
                .map_or(p.0.len(), |(i, _)| i);
        let lit = p.0[..end].to_owned();
        p.0 = &p.0[end..];
        if !p.consume(']') {
            return None;
        }
        format!("[{lit}]")
    } else {
        p.atext(true)?.to_owned()
    };
    if !p.consume('>') || !p.skip_cfws() {
        return None;
    }
    Some(format!("{left}@{right}"))
}

fn split_code(rest: &str) -> (String, String, String) {
    if let Some(inner) = rest.strip_prefix('[')
        && let Some(end) = inner.find(']')
    {
        let code_full = &inner[..end];
        let (code, args) = code_full.split_once(' ').unwrap_or((code_full, ""));
        return (
            code.to_uppercase(),
            args.to_owned(),
            inner[end + 1..].trim_start().to_owned(),
        );
    }
    (String::new(), String::new(), rest.to_owned())
}

enum Arg {
    Raw(String),
    Literal(Vec<u8>),
}

/// `imapwire.Encoder.String`/`Mailbox`: quoted unless it needs a literal.
fn quoted_or_literal(s: &[u8]) -> Arg {
    let safe = s.iter().all(|c| (0x20..=0x7e).contains(c));
    if !safe {
        return Arg::Literal(s.to_vec());
    }
    let mut out = String::from("\"");
    for &c in s {
        if c == b'"' || c == b'\\' {
            out.push('\\');
        }
        out.push(c as char);
    }
    out.push('"');
    Arg::Raw(out)
}

/// `imapwire.Encoder.Mailbox`: `INBOX` bare, everything else modified UTF-7
/// encoded and quoted.
fn mailbox_arg(name: &str) -> Arg {
    if name.eq_ignore_ascii_case("INBOX") {
        return Arg::Raw("INBOX".into());
    }
    quoted_or_literal(utf7_encode(name).as_bytes())
}

/// `utf7.Encode`: IMAP modified UTF-7.
pub fn utf7_encode(s: &str) -> String {
    let mut out = String::new();
    let mut pending: Vec<u16> = Vec::new();
    let flush = |out: &mut String, pending: &mut Vec<u16>| {
        if pending.is_empty() {
            return;
        }
        let bytes: Vec<u8> = pending.iter().flat_map(|u| u.to_be_bytes()).collect();
        let table = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+,";
        out.push('&');
        for chunk in bytes.chunks(3) {
            let mut acc = 0u32;
            for (i, b) in chunk.iter().enumerate() {
                acc |= u32::from(*b) << (16 - 8 * i);
            }
            let chars = match chunk.len() {
                1 => 2,
                2 => 3,
                _ => 4,
            };
            for k in 0..chars {
                out.push(table[((acc >> (18 - 6 * k)) & 0x3f) as usize] as char);
            }
        }
        out.push('-');
        pending.clear();
    };
    for c in s.chars() {
        if ('\u{20}'..='\u{7e}').contains(&c) {
            flush(&mut out, &mut pending);
            if c == '&' {
                out.push_str("&-");
            } else {
                out.push(c);
            }
        } else {
            let mut buf = [0u16; 2];
            pending.extend_from_slice(c.encode_utf16(&mut buf));
        }
    }
    flush(&mut out, &mut pending);
    out
}

/// `imap.UIDSetNum(...).String()`: sorted, merged ranges.
pub fn uid_set(uids: &[u32]) -> String {
    let mut sorted: Vec<u32> = uids.to_vec();
    sorted.sort_unstable();
    sorted.dedup();
    let mut ranges: Vec<(u32, u32)> = Vec::new();
    for u in sorted {
        match ranges.last_mut() {
            Some((_, hi)) if *hi + 1 == u => *hi = u,
            _ => ranges.push((u, u)),
        }
    }
    ranges
        .iter()
        .map(|(a, b)| {
            if a == b {
                a.to_string()
            } else {
                format!("{a}:{b}")
            }
        })
        .collect::<Vec<_>>()
        .join(",")
}

impl ImapClient for TlsImapClient {
    fn login(&mut self, username: &str, password: &str) -> Result<(), String> {
        let resp = self.command(
            "LOGIN",
            &[
                quoted_or_literal(username.as_bytes()),
                quoted_or_literal(password.as_bytes()),
            ],
        )?;
        Self::ok(&resp)
    }

    fn select(&mut self, folder: &str) -> Result<MailboxStatus, String> {
        let resp = self.command("SELECT", &[mailbox_arg(folder)])?;
        let mut status = MailboxStatus::default();
        for (line, _) in &resp.untagged {
            let upper = line.to_uppercase();
            for (key, slot) in [
                ("[UIDVALIDITY ", &mut status.uid_validity),
                ("[UIDNEXT ", &mut status.uid_next),
            ] {
                if let Some(i) = upper.find(key) {
                    let rest = &upper[i + key.len()..];
                    if let Some(j) = rest.find(']') {
                        *slot = rest[..j].trim().parse().unwrap_or(0);
                    }
                }
            }
        }
        Self::ok(&resp)?;
        Ok(status)
    }

    fn search_uid(&mut self, start_uid: u32, unseen: bool) -> Result<Vec<u32>, String> {
        let criteria = if unseen {
            format!("SEARCH UID {start_uid}:* UNSEEN")
        } else {
            format!("SEARCH UID {start_uid}:*")
        };
        let resp = self.command("UID", &[Arg::Raw(criteria)])?;
        let mut uids = Vec::new();
        for (line, _) in &resp.untagged {
            if let Some(rest) = line.strip_prefix("* SEARCH") {
                uids.extend(
                    rest.split_whitespace()
                        .filter_map(|n| n.parse::<u32>().ok()),
                );
            }
        }
        Self::ok(&resp)?;
        Ok(uids)
    }

    fn fetch_envelopes_uid(&mut self, uids: &[u32]) -> Result<Vec<ImapMessage>, String> {
        self.fetch(uids, "(UID ENVELOPE)")
    }

    fn fetch_uid(&mut self, uids: &[u32]) -> Result<Vec<ImapMessage>, String> {
        self.fetch(uids, "(UID ENVELOPE BODY.PEEK[])")
    }

    fn store_seen(&mut self, seq: u32) -> Result<(), String> {
        let resp = self.command("STORE", &[Arg::Raw(format!("{seq} +FLAGS (\\Seen)"))])?;
        Self::ok(&resp)
    }

    fn move_to(&mut self, seq: u32, dest: &str) -> Result<(), String> {
        let caps = self.ensure_caps()?;
        if caps.iter().any(|c| c == "MOVE") {
            let resp = self.command("MOVE", &[Arg::Raw(seq.to_string()), mailbox_arg(dest)])?;
            return Self::ok(&resp);
        }
        // go-imap fallback: COPY, STORE +FLAGS.SILENT \Deleted, EXPUNGE.
        let copy = self.command("COPY", &[Arg::Raw(seq.to_string()), mailbox_arg(dest)])?;
        let store = self.command(
            "STORE",
            &[Arg::Raw(format!("{seq} +FLAGS.SILENT (\\Deleted)"))],
        )?;
        let expunge = self.command("EXPUNGE", &[])?;
        Self::ok(&copy)?;
        Self::ok(&store)?;
        Self::ok(&expunge)
    }

    fn close(&mut self) -> Result<(), String> {
        if self.dead.is_some() {
            // The transport already failed; Go reports the closed connection.
            return Err("use of closed network connection".into());
        }
        let resp = self.command("LOGOUT", &[])?;
        let _ = (&resp.code, &resp.code_args);
        Self::ok(&resp)?;
        if let Some(Conn::Tls(s)) = Some(self.reader.get_mut()) {
            s.conn.send_close_notify();
            let _ = s.flush();
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn uid_sets_merge_like_go_imap() {
        assert_eq!(uid_set(&[1]), "1");
        assert_eq!(uid_set(&[1, 2]), "1:2");
        assert_eq!(uid_set(&[5, 1, 3, 2, 3]), "1:3,5");
        assert_eq!(uid_set(&[1, 3, 5]), "1,3,5");
    }

    #[test]
    fn utf7_matches_rfc3501() {
        assert_eq!(utf7_encode("Entwürfe"), "Entw&APw-rfe");
        assert_eq!(utf7_encode("A \"b\" & c"), "A \"b\" &- c");
        assert_eq!(
            utf7_encode("~peter/mail/台北/日本語"),
            "~peter/mail/&U,BTFw-/&ZeVnLIqe-"
        );
    }

    #[test]
    fn message_ids_follow_go_message() {
        assert_eq!(parse_msg_id("<a1@x>").as_deref(), Some("a1@x"));
        assert_eq!(
            parse_msg_id(" (c) <a.b@x.y> (t)").as_deref(),
            Some("a.b@x.y")
        );
        assert_eq!(
            parse_msg_id("<a@[1.2.3.4]>").as_deref(),
            Some("a@[1.2.3.4]")
        );
        assert_eq!(parse_msg_id("a@x"), None);
        assert_eq!(parse_msg_id("<a b@x>"), None);
        assert_eq!(parse_msg_id("<nohost>"), None);
        assert_eq!(parse_msg_id("<a@x"), None);
    }

    #[test]
    fn error_text_matches_go_imap() {
        assert_eq!(imap_error("NO", "", "boom"), "imap: NO boom");
        assert_eq!(
            imap_error("NO", "NONEXISTENT", "no such mailbox"),
            "imap: NO [NONEXISTENT] no such mailbox"
        );
        assert_eq!(imap_error("BAD", "", ""), "imap: BAD (no response text)");
    }
}
