//! The scripted loopback IMAP server shared with the Go oracle
//! (`mail_port_test.go`'s `fakeIMAP`): same script format, same replies, TLS with
//! the throw-away certificate in `testdata/port/ingest/imap-tls`. It records
//! every command the client sent (tag stripped, password masked).
#![allow(dead_code)]

use base64::Engine as _;
use rustls::pki_types::pem::PemObject;
use rustls::pki_types::{CertificateDer, PrivateKeyDer};
use serde_json::Value;
use std::collections::BTreeMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{Shutdown, TcpListener, TcpStream};
use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::thread::JoinHandle;
use symdesk_ingest::gmime::go_quote;

#[derive(Clone, Debug, Default)]
pub struct Msg {
    pub uid: u32,
    pub flags: Vec<String>,
    pub message_id: String,
    pub no_envelope: bool,
    pub raw: Vec<u8>,
}

#[derive(Clone, Debug, Default)]
pub struct Folder {
    pub uid_validity: u32,
    pub uid_next: u32,
    pub no_uid_next: bool,
    pub messages: Vec<Msg>,
}

#[derive(Clone, Debug, Default)]
pub struct Spec {
    pub password: String,
    pub caps: String,
    pub folders: BTreeMap<String, Folder>,
    pub fail: BTreeMap<String, String>,
    pub drop: String,
    pub no_cap_codes: bool,
    pub omit_body: bool,
}

pub fn parse_msg(v: &Value) -> Msg {
    Msg {
        uid: v["uid"].as_u64().unwrap_or(0) as u32,
        flags: v["flags"]
            .as_array()
            .map(|a| a.iter().map(|f| f.as_str().unwrap().to_owned()).collect())
            .unwrap_or_default(),
        message_id: v["message_id"].as_str().unwrap_or("").to_owned(),
        no_envelope: v["no_envelope"].as_bool().unwrap_or(false),
        raw: base64::engine::general_purpose::STANDARD
            .decode(v["raw"].as_str().unwrap_or(""))
            .unwrap(),
    }
}

pub fn parse_fail(v: &Value) -> BTreeMap<String, String> {
    v.as_object()
        .map(|o| {
            o.iter()
                .map(|(k, v)| (k.clone(), v.as_str().unwrap().to_owned()))
                .collect()
        })
        .unwrap_or_default()
}

pub fn parse_spec(v: &Value) -> Spec {
    let mut folders = BTreeMap::new();
    if let Some(o) = v["folders"].as_object() {
        for (name, f) in o {
            folders.insert(
                name.clone(),
                Folder {
                    uid_validity: f["uid_validity"].as_u64().unwrap_or(0) as u32,
                    uid_next: f["uid_next"].as_u64().unwrap_or(0) as u32,
                    no_uid_next: f["no_uid_next"].as_bool().unwrap_or(false),
                    messages: f["messages"]
                        .as_array()
                        .map(|a| a.iter().map(parse_msg).collect())
                        .unwrap_or_default(),
                },
            );
        }
    }
    Spec {
        password: v["password"].as_str().unwrap_or("").to_owned(),
        caps: v["caps"].as_str().unwrap_or("").to_owned(),
        folders,
        fail: parse_fail(&v["fail"]),
        drop: v["drop"].as_str().unwrap_or("").to_owned(),
        no_cap_codes: v["no_cap_codes"].as_bool().unwrap_or(false),
        omit_body: v["omit_body"].as_bool().unwrap_or(false),
    }
}

pub struct Server {
    pub addr: std::net::SocketAddr,
    pub spec: Arc<Mutex<Spec>>,
    pub conns: Arc<Mutex<Vec<Vec<String>>>>,
    stop: Arc<AtomicBool>,
    accept: Option<JoinHandle<()>>,
    workers: Arc<Mutex<Vec<JoinHandle<()>>>>,
}

pub fn tls_dir() -> std::path::PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/ingest/imap-tls")
}

fn server_config() -> Arc<rustls::ServerConfig> {
    let dir = tls_dir();
    let certs: Vec<CertificateDer<'static>> = CertificateDer::pem_file_iter(dir.join("server.pem"))
        .unwrap()
        .map(|c| c.unwrap())
        .collect();
    let key = PrivateKeyDer::from_pem_file(dir.join("server.key")).unwrap();
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    Arc::new(
        rustls::ServerConfig::builder_with_provider(provider)
            .with_safe_default_protocol_versions()
            .unwrap()
            .with_no_client_auth()
            .with_single_cert(certs, key)
            .unwrap(),
    )
}

pub fn client_config() -> Arc<rustls::ClientConfig> {
    let mut roots = rustls::RootCertStore::empty();
    for c in CertificateDer::pem_file_iter(tls_dir().join("ca.pem")).unwrap() {
        roots.add(c.unwrap()).unwrap();
    }
    symdesk_ingest::imap::tls_config_with_roots(roots).unwrap()
}

impl Server {
    pub fn start(spec: Spec) -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        listener.set_nonblocking(true).unwrap();
        let addr = listener.local_addr().unwrap();
        let spec = Arc::new(Mutex::new(spec));
        let conns: Arc<Mutex<Vec<Vec<String>>>> = Arc::new(Mutex::new(Vec::new()));
        let stop = Arc::new(AtomicBool::new(false));
        let workers: Arc<Mutex<Vec<JoinHandle<()>>>> = Arc::new(Mutex::new(Vec::new()));
        let config = server_config();
        let accept = {
            let (spec, conns, stop, workers) = (
                Arc::clone(&spec),
                Arc::clone(&conns),
                Arc::clone(&stop),
                Arc::clone(&workers),
            );
            std::thread::spawn(move || {
                while !stop.load(Ordering::SeqCst) {
                    match listener.accept() {
                        Ok((sock, _)) => {
                            let _ = sock.set_nonblocking(false);
                            let idx = {
                                let mut c = conns.lock().unwrap();
                                c.push(Vec::new());
                                c.len() - 1
                            };
                            let (spec, conns, config) =
                                (Arc::clone(&spec), Arc::clone(&conns), Arc::clone(&config));
                            workers.lock().unwrap().push(std::thread::spawn(move || {
                                serve(sock, config, spec, conns, idx)
                            }));
                        }
                        Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                            std::thread::sleep(std::time::Duration::from_millis(2));
                        }
                        Err(_) => break,
                    }
                }
            })
        };
        Self {
            addr,
            spec,
            conns,
            stop,
            accept: Some(accept),
            workers,
        }
    }

    /// Stops accepting and waits for the connection threads.
    pub fn close(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
        if let Some(h) = self.accept.take() {
            let _ = h.join();
        }
        let handles: Vec<_> = self.workers.lock().unwrap().drain(..).collect();
        for h in handles {
            let _ = h.join();
        }
    }
}

impl Drop for Server {
    fn drop(&mut self) {
        self.close();
    }
}

struct Conn {
    reader: BufReader<rustls::StreamOwned<rustls::ServerConnection, TcpStream>>,
}

impl Conn {
    fn write(&mut self, s: &str) {
        let w = self.reader.get_mut();
        let _ = w.write_all(s.as_bytes());
    }

    fn line(&mut self, s: &str) {
        self.write(&format!("{s}\r\n"));
    }

    fn flush(&mut self) {
        let _ = self.reader.get_mut().flush();
    }

    /// `readCommand`: one command, following synchronizing literals.
    fn read_command(&mut self) -> Option<String> {
        let mut sb = String::new();
        loop {
            let mut raw = Vec::new();
            match self.reader.read_until(b'\n', &mut raw) {
                Ok(0) | Err(_) => return None,
                Ok(_) => {}
            }
            let line = String::from_utf8_lossy(&raw)
                .trim_end_matches(['\r', '\n'])
                .to_owned();
            sb.push_str(&line);
            if let Some(i) = line.rfind('{')
                && line.ends_with('}')
                && let Ok(n) = line[i + 1..line.len() - 1]
                    .trim_end_matches('+')
                    .parse::<usize>()
            {
                if !line.ends_with("+}") {
                    self.line("+ ready");
                    self.flush();
                }
                let mut buf = vec![0u8; n];
                if self.reader.read_exact(&mut buf).is_err() {
                    return None;
                }
                sb.push_str(&format!("<{n} bytes>"));
                continue;
            }
            return Some(sb);
        }
    }
}

fn has_flag(flags: &[String], f: &str) -> bool {
    flags.iter().any(|x| x.eq_ignore_ascii_case(f))
}

fn next_uid(f: &Folder) -> u32 {
    if f.uid_next != 0 {
        return f.uid_next;
    }
    f.messages.iter().map(|m| m.uid).max().unwrap_or(0) + 1
}

fn cap_line(spec: &Spec) -> String {
    if spec.caps.is_empty() {
        "IMAP4rev1".into()
    } else {
        format!("IMAP4rev1 {}", spec.caps)
    }
}

pub fn snapshot(spec: &Spec) -> Value {
    let mut out = serde_json::Map::new();
    for (name, f) in &spec.folders {
        let messages: Vec<Value> = f
            .messages
            .iter()
            .map(|m| {
                let mut fl = m.flags.clone();
                fl.sort();
                serde_json::json!({"uid": m.uid, "flags": fl})
            })
            .collect();
        out.insert(
            name.clone(),
            serde_json::json!({"uid_validity": f.uid_validity, "uid_next": next_uid(f), "messages": messages}),
        );
    }
    Value::Object(out)
}

fn serve(
    sock: TcpStream,
    config: Arc<rustls::ServerConfig>,
    spec: Arc<Mutex<Spec>>,
    conns: Arc<Mutex<Vec<Vec<String>>>>,
    idx: usize,
) {
    let _ = sock.set_read_timeout(Some(std::time::Duration::from_secs(30)));
    let Ok(sc) = rustls::ServerConnection::new(config) else {
        return;
    };
    let shutdown_sock = sock.try_clone().ok();
    let mut c = Conn {
        reader: BufReader::new(rustls::StreamOwned::new(sc, sock)),
    };
    let finish = |c: &mut Conn| {
        c.flush();
        if let Some(s) = &shutdown_sock {
            let _ = s.shutdown(Shutdown::Both);
        }
    };
    {
        let s = spec.lock().unwrap();
        if s.no_cap_codes {
            c.line("* OK fake ready");
        } else {
            c.line(&format!("* OK [CAPABILITY {}] fake ready", cap_line(&s)));
        }
    }
    c.flush();
    let mut selected = String::new();
    let mut dropped: Vec<String> = Vec::new();
    loop {
        let Some(cmd) = c.read_command() else {
            finish(&mut c);
            return;
        };
        let (tag, rest) = cmd.split_once(' ').unwrap_or((&cmd, ""));
        let (verb, args) = rest.split_once(' ').unwrap_or((rest, ""));
        let upper = verb.to_uppercase();
        let mut full = upper.clone();
        let mut args = args.to_owned();
        if upper == "UID" {
            let (sub, a2) = args.split_once(' ').unwrap_or((&args, ""));
            full = format!("UID {}", sub.to_uppercase());
            args = a2.to_owned();
        }
        let mut logged = full.clone();
        if !args.is_empty() {
            logged.push(' ');
            logged.push_str(&args);
        }
        if upper == "LOGIN" {
            let user = args.split(' ').next().unwrap_or("");
            logged = format!("LOGIN {user} <PASSWORD>");
        }
        conns.lock().unwrap()[idx].push(logged);
        let (fail_line, drop) = {
            let s = spec.lock().unwrap();
            (
                s.fail.get(&full).cloned(),
                s.drop == full && !dropped.contains(&full),
            )
        };
        if drop {
            dropped.push(full);
            finish(&mut c);
            return;
        }
        if let Some(line) = fail_line
            && upper != "LOGOUT"
        {
            c.line(&format!("{tag} {line}"));
            c.flush();
            continue;
        }
        match upper.as_str() {
            "CAPABILITY" => {
                let s = spec.lock().unwrap();
                c.line(&format!("* CAPABILITY {}", cap_line(&s)));
                c.line(&format!("{tag} OK CAPABILITY completed"));
            }
            "LOGIN" => {
                let pw = args.split_once(' ').map_or("", |(_, p)| p);
                let s = spec.lock().unwrap();
                if pw.trim_matches('"') != s.password {
                    c.line(&format!(
                        "{tag} NO [AUTHENTICATIONFAILED] invalid credentials"
                    ));
                } else if s.no_cap_codes {
                    c.line(&format!("{tag} OK LOGIN completed"));
                } else {
                    c.line(&format!(
                        "{tag} OK [CAPABILITY {}] LOGIN completed",
                        cap_line(&s)
                    ));
                }
            }
            "SELECT" => {
                let name = args.trim_matches('"').to_owned();
                let s = spec.lock().unwrap();
                match s.folders.get(&name) {
                    None => c.line(&format!("{tag} NO [NONEXISTENT] no such mailbox")),
                    Some(f) => {
                        selected = name;
                        c.line(&format!("* {} EXISTS", f.messages.len()));
                        c.line("* 0 RECENT");
                        c.line("* FLAGS (\\Seen \\Answered \\Flagged \\Deleted \\Draft)");
                        c.line("* OK [PERMANENTFLAGS (\\Seen \\Answered \\Flagged \\Deleted \\Draft \\*)] flags");
                        c.line(&format!("* OK [UIDVALIDITY {}] uids valid", f.uid_validity));
                        if !f.no_uid_next {
                            c.line(&format!("* OK [UIDNEXT {}] predicted", next_uid(f)));
                        }
                        c.line(&format!("{tag} OK [READ-WRITE] SELECT completed"));
                    }
                }
            }
            "UID" => {
                let s = spec.lock().unwrap();
                let f = s.folders.get(&selected).cloned().unwrap_or_default();
                match full.as_str() {
                    "UID SEARCH" => {
                        let toks: Vec<&str> = args.split_whitespace().collect();
                        let mut start = 1u32;
                        let mut unseen = false;
                        let mut i = 0;
                        while i < toks.len() {
                            match toks[i].to_uppercase().as_str() {
                                "UID" => {
                                    if i + 1 < toks.len() {
                                        let a = toks[i + 1].split(':').next().unwrap_or("");
                                        start = a.parse().unwrap_or(0);
                                        i += 1;
                                    }
                                }
                                "UNSEEN" => unseen = true,
                                _ => {}
                            }
                            i += 1;
                        }
                        let max = f.messages.iter().map(|m| m.uid).max().unwrap_or(0);
                        let mut out = Vec::new();
                        for m in &f.messages {
                            let mut inc = m.uid >= start || (start > max && m.uid == max);
                            if unseen && has_flag(&m.flags, "\\Seen") {
                                inc = false;
                            }
                            if inc {
                                out.push(m.uid.to_string());
                            }
                        }
                        if out.is_empty() {
                            c.line("* SEARCH");
                        } else {
                            c.line(&format!("* SEARCH {}", out.join(" ")));
                        }
                        c.line(&format!("{tag} OK SEARCH completed"));
                    }
                    "UID FETCH" => {
                        let (set, items) = args.split_once(' ').unwrap_or((&args, ""));
                        let mut want = std::collections::BTreeSet::new();
                        for part in set.split(',') {
                            let (a, b) = part
                                .split_once(':')
                                .map_or((part, None), |(a, b)| (a, Some(b)));
                            let lo: u64 = a.parse().unwrap_or(0);
                            let hi: u64 = match b {
                                None => lo,
                                Some("*") => u32::MAX as u64,
                                Some(b) => b.parse().unwrap_or(0),
                            };
                            for m in &f.messages {
                                if u64::from(m.uid) >= lo && u64::from(m.uid) <= hi {
                                    want.insert(m.uid);
                                }
                            }
                        }
                        let iu = items.to_uppercase();
                        let want_env = iu.contains("ENVELOPE");
                        let want_body = iu.contains("BODY.PEEK[]") || iu.contains("BODY[]");
                        for (i, m) in f.messages.iter().enumerate() {
                            if !want.contains(&m.uid) {
                                continue;
                            }
                            let mut parts = vec![format!("UID {}", m.uid)];
                            if want_env && !m.no_envelope {
                                let mid = if m.message_id.is_empty() {
                                    "NIL".to_owned()
                                } else {
                                    go_quote(m.message_id.as_bytes())
                                };
                                parts.push(format!(
                                    "ENVELOPE (NIL NIL NIL NIL NIL NIL NIL NIL NIL {mid})"
                                ));
                            }
                            if want_body && !s.omit_body {
                                parts.push(format!(
                                    "BODY[] {{{}}}\r\n{}",
                                    m.raw.len(),
                                    String::from_utf8_lossy(&m.raw)
                                ));
                            }
                            // The body is raw bytes; write them unmodified.
                            if want_body && !s.omit_body {
                                let head = format!("* {} FETCH (", i + 1);
                                let pre: Vec<String> = parts[..parts.len() - 1].to_vec();
                                let mut out = head;
                                for p in &pre {
                                    out.push_str(p);
                                    out.push(' ');
                                }
                                out.push_str(&format!("BODY[] {{{}}}\r\n", m.raw.len()));
                                c.write(&out);
                                let w = c.reader.get_mut();
                                let _ = w.write_all(&m.raw);
                                c.write(")\r\n");
                            } else {
                                c.write(&format!("* {} FETCH ({})\r\n", i + 1, parts.join(" ")));
                            }
                        }
                        c.line(&format!("{tag} OK FETCH completed"));
                    }
                    "UID EXPUNGE" => c.line(&format!("{tag} OK EXPUNGE completed")),
                    _ => c.line(&format!("{tag} BAD unsupported")),
                }
            }
            "STORE" => {
                let (seq_s, spec_s) = args.split_once(' ').unwrap_or((&args, ""));
                let seq: usize = seq_s.parse().unwrap_or(0);
                let mut s = spec.lock().unwrap();
                let silent = spec_s.to_uppercase().contains(".SILENT");
                let flags_part = match (spec_s.find('('), spec_s.rfind(')')) {
                    (Some(a), Some(b)) if a < b => &spec_s[a + 1..b],
                    _ => "",
                };
                let f = s.folders.entry(selected.clone()).or_default();
                if seq < 1 || seq > f.messages.len() {
                    c.line(&format!("{tag} BAD invalid message sequence number"));
                } else {
                    let m = &mut f.messages[seq - 1];
                    for fl in flags_part.split_whitespace() {
                        if !has_flag(&m.flags, fl) {
                            m.flags.push(fl.to_owned());
                        }
                    }
                    if !silent {
                        c.line(&format!("* {seq} FETCH (FLAGS ({}))", m.flags.join(" ")));
                    }
                    c.line(&format!("{tag} OK STORE completed"));
                }
            }
            "MOVE" | "COPY" => {
                let (seq_s, dest) = args.split_once(' ').unwrap_or((&args, ""));
                let dest = dest.trim_matches('"').to_owned();
                let seq: usize = seq_s.parse().unwrap_or(0);
                let mut s = spec.lock().unwrap();
                if !s.folders.contains_key(&dest) {
                    c.line(&format!("{tag} NO [TRYCREATE] no such mailbox"));
                } else {
                    let len = s.folders.get(&selected).map_or(0, |f| f.messages.len());
                    if seq < 1 || seq > len {
                        c.line(&format!("{tag} BAD invalid message sequence number"));
                    } else {
                        let m = s.folders[&selected].messages[seq - 1].clone();
                        let df = s.folders.get_mut(&dest).unwrap();
                        let nu = next_uid(df);
                        df.messages.push(Msg { uid: nu, ..m });
                        df.uid_next = nu + 1;
                        if upper == "MOVE" {
                            s.folders
                                .get_mut(&selected)
                                .unwrap()
                                .messages
                                .remove(seq - 1);
                            c.line(&format!("* {seq} EXPUNGE"));
                        }
                        c.line(&format!("{tag} OK {upper} completed"));
                    }
                }
            }
            "EXPUNGE" => {
                let mut s = spec.lock().unwrap();
                let f = s.folders.entry(selected.clone()).or_default();
                let mut keep = Vec::new();
                let mut removed = 0usize;
                for (i, m) in f.messages.clone().into_iter().enumerate() {
                    if has_flag(&m.flags, "\\Deleted") {
                        c.line(&format!("* {} EXPUNGE", i + 1 - removed));
                        removed += 1;
                        continue;
                    }
                    keep.push(m);
                }
                f.messages = keep;
                c.line(&format!("{tag} OK EXPUNGE completed"));
            }
            "LOGOUT" => {
                c.line("* BYE logging out");
                c.line(&format!("{tag} OK LOGOUT completed"));
                finish(&mut c);
                return;
            }
            "NOOP" | "CLOSE" | "UNSELECT" => c.line(&format!("{tag} OK {upper} completed")),
            _ => c.line(&format!("{tag} BAD unsupported {upper}")),
        }
        c.flush();
    }
}
