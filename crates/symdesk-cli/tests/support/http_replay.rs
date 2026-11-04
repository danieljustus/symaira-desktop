//! Replay engine for the Go-recorded HTTP process fixtures
//! (`cmd/symdesk/http_process_port_test.go`). The Rust `symdesk serve` process
//! is started in a disposable HOME with a disposable vault and probed over a
//! raw socket exactly like the Go server was, then the observed status line,
//! headers, body, connection behavior, vault tree and exit outcome are compared
//! with the Go recording after the same normalization.
#![allow(dead_code)]

use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    io::{BufRead, BufReader, Read, Write},
    net::{TcpListener, TcpStream},
    os::unix::{
        fs::{PermissionsExt, symlink},
        process::ExitStatusExt,
    },
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    sync::{
        Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use regex::Regex;
use serde::Deserialize;

pub use ext::Conn;

#[derive(Deserialize, Clone)]
pub struct Repeat {
    pub text: String,
    pub count: usize,
}

#[derive(Deserialize, Clone, Default, PartialEq, Debug)]
pub struct Observed {
    #[serde(default)]
    pub status: u16,
    #[serde(default)]
    pub reason: String,
    #[serde(default)]
    pub headers: Vec<(String, String)>,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub body_b64: String,
    #[serde(default)]
    pub body_len: usize,
    #[serde(default)]
    pub body_sha256: String,
    #[serde(default)]
    pub continues: usize,
    #[serde(default)]
    pub closed: bool,
    #[serde(default)]
    pub silent: bool,
    #[serde(default)]
    pub tree: BTreeMap<String, String>,
    #[serde(default)]
    pub exit: String,
    #[serde(default)]
    pub early: String,
    #[serde(default)]
    pub tls: String,
}

#[derive(Deserialize, Clone, Default)]
pub struct Step {
    pub label: String,
    #[serde(default)]
    pub kind: String,
    #[serde(default)]
    pub method: String,
    #[serde(default)]
    pub target: String,
    #[serde(default)]
    pub version: String,
    #[serde(default)]
    pub headers: Vec<(String, String)>,
    #[serde(default)]
    pub no_host: bool,
    #[serde(default)]
    pub no_close: bool,
    #[serde(default)]
    pub no_length: bool,
    #[serde(default)]
    pub body: String,
    #[serde(default)]
    pub body_repeat: Option<Repeat>,
    #[serde(default)]
    pub raw: String,
    #[serde(default)]
    pub raw_fill: usize,
    #[serde(default)]
    pub read_ms: u64,
    #[serde(default)]
    pub mask: Vec<String>,
    #[serde(default)]
    pub volatile_length: bool,
    #[serde(default)]
    pub ignore_closed: bool,
    #[serde(default)]
    pub with_stdout: bool,
    #[serde(default)]
    pub redacts: Vec<String>,
    #[serde(default)]
    pub op: String,
    #[serde(default)]
    pub path: String,
    #[serde(default)]
    pub content: String,
    #[serde(default)]
    pub mtime: i64,
    #[serde(default)]
    pub mode: u32,
    #[serde(default)]
    pub rotate: bool,
    #[serde(default)]
    pub tree_include: Vec<String>,
    #[serde(default)]
    pub stall: String,
    #[serde(default)]
    pub capture: BTreeMap<String, String>,
    #[serde(default)]
    pub count: usize,
    #[serde(default)]
    pub stream_lines: usize,
    #[serde(default)]
    pub stream_until: String,
    #[serde(default)]
    pub release: bool,
    #[serde(default)]
    pub abort: bool,
    #[serde(default)]
    pub args: Vec<String>,
    #[serde(default)]
    pub plain: bool,
    #[serde(default)]
    pub tls_version: String,
    #[serde(default)]
    pub time_class: bool,
    #[serde(default)]
    pub wait_ms: u64,
    pub expect: Option<Observed>,
}

#[derive(Deserialize, Clone, Default)]
pub struct Serve {
    #[serde(default)]
    pub args: Vec<String>,
    #[serde(default)]
    pub env: BTreeMap<String, String>,
    #[serde(default)]
    pub options: String,
}

#[derive(Deserialize, Clone)]
pub struct Case {
    pub name: String,
    #[serde(default)]
    pub files: BTreeMap<String, String>,
    #[serde(default)]
    pub symlinks: BTreeMap<String, String>,
    #[serde(default)]
    pub mtime: i64,
    #[serde(default)]
    pub no_worker: bool,
    #[serde(default)]
    pub serve: Option<Serve>,
    #[serde(default)]
    pub env: BTreeMap<String, String>,
    #[serde(default)]
    pub tls: bool,
    #[serde(default)]
    pub provider: bool,
    #[serde(default)]
    pub empty_path: bool,
    #[serde(default)]
    pub no_server: bool,
    pub steps: Vec<Step>,
    #[serde(default)]
    pub stderr_leaks: Vec<String>,
}

#[derive(Deserialize)]
pub struct Fixture {
    pub schema_version: u32,
    pub cases: Vec<Case>,
}

pub struct Secrets {
    values: BTreeMap<String, String>,
    /// Captured response values (share ids/tokens): expanded and redacted like
    /// secrets, but expected in responses, so never leak-scanned.
    vars: Mutex<BTreeMap<String, String>>,
}

impl Secrets {
    pub fn new() -> Self {
        let mut values = BTreeMap::new();
        for name in [
            "ADMIN", "WORKER", "ADMIN2", "WORKER2", "NAMED", "WNAMED", "NAMEDADM", "SHARE",
            "SHARE2", "PROVKEY",
        ] {
            values.insert(name.to_owned(), random_hex(20));
        }
        for name in ["NAMED", "WNAMED", "NAMEDADM", "SHARE", "SHARE2"] {
            let hash = sha256_hex(values[name].as_bytes());
            values.insert(format!("{name}_HASH"), hash);
        }
        Self {
            values,
            vars: Mutex::new(BTreeMap::new()),
        }
    }

    fn set_var(&self, name: &str, value: String) {
        self.vars.lock().unwrap().insert(name.to_owned(), value);
    }

    pub fn lookup(&self, name: &str) -> String {
        self.values
            .get(name)
            .cloned()
            .or_else(|| self.vars.lock().unwrap().get(name).cloned())
            .unwrap_or_default()
    }

    fn order(&self) -> Vec<String> {
        let mut names: Vec<String> = self.values.keys().cloned().collect();
        names.extend(self.vars.lock().unwrap().keys().cloned());
        names.sort_by(|a, b| b.len().cmp(&a.len()).then_with(|| a.cmp(b)));
        names
    }

    fn expand(&self, text: &str) -> String {
        let mut out = text.to_owned();
        for name in self.order() {
            out = out.replace(&format!("${name}"), &self.lookup(&name));
        }
        out
    }

    fn redact(&self, text: &str) -> String {
        let mut out = text.to_owned();
        for name in self.order() {
            let value = self.lookup(&name);
            if !value.is_empty() {
                out = out.replace(&value, &format!("${name}"));
            }
        }
        out
    }

    pub fn leaks(&self, text: &str) -> Vec<String> {
        let mut names: Vec<&String> = self.values.keys().collect();
        names.sort_by(|a, b| b.len().cmp(&a.len()).then_with(|| a.cmp(b)));
        names
            .into_iter()
            .filter(|name| text.contains(&self.values[*name]))
            .cloned()
            .collect()
    }
}

fn random_hex(bytes: usize) -> String {
    let mut buffer = vec![0_u8; bytes];
    fs::File::open("/dev/urandom")
        .and_then(|mut file| file.read_exact(&mut buffer))
        .expect("read /dev/urandom");
    buffer.iter().map(|byte| format!("{byte:02x}")).collect()
}

pub fn sha256_hex(data: &[u8]) -> String {
    use sha2::{Digest, Sha256};
    Sha256::digest(data)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect()
}

fn base64_std(data: &[u8]) -> String {
    const ALPHABET: &[u8; 64] =
        b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for chunk in data.chunks(3) {
        let b = [
            chunk[0],
            chunk.get(1).copied().unwrap_or(0),
            chunk.get(2).copied().unwrap_or(0),
        ];
        out.push(ALPHABET[(b[0] >> 2) as usize] as char);
        out.push(ALPHABET[(((b[0] & 3) << 4) | (b[1] >> 4)) as usize] as char);
        out.push(if chunk.len() > 1 {
            ALPHABET[(((b[1] & 15) << 2) | (b[2] >> 6)) as usize] as char
        } else {
            '='
        });
        out.push(if chunk.len() > 2 {
            ALPHABET[(b[2] & 63) as usize] as char
        } else {
            '='
        });
    }
    out
}

struct Patterns {
    rfc3339: Regex,
    hex_run: Regex,
    boundary: Regex,
    version: Regex,
    date: Regex,
}

fn patterns() -> Patterns {
    Patterns {
        rfc3339: Regex::new(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})")
            .unwrap(),
        hex_run: Regex::new(r"[0-9a-f]{24,}").unwrap(),
        boundary: Regex::new(r"boundary=[0-9a-f]+").unwrap(),
        version: Regex::new(r#""version":"[^"]*""#).unwrap(),
        date: Regex::new(r"[A-Z][a-z]{2}, \d{2} [A-Z][a-z]{2} \d{4} \d{2}:\d{2}:\d{2} GMT")
            .unwrap(),
    }
}

struct Server {
    binary: PathBuf,
    root: PathBuf,
    vault: PathBuf,
    home: PathBuf,
    secrets: Secrets,
    port: u16,
    child: Option<Child>,
    rotated: bool,
    no_worker: bool,
    leaked: BTreeSet<String>,
    patterns: Patterns,
    case: Case,
    vault2: PathBuf,
    provider: Option<provider::Provider>,
    tls_certificate: Option<tls::Material>,
    capture: Mutex<BTreeMap<String, String>>,
}

fn free_port() -> u16 {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind probe port");
    listener.local_addr().unwrap().port()
}

fn describe_exit(code: Option<i32>) -> String {
    format!("exit {}", code.unwrap_or(-1))
}

/// Reads through a buffer while keeping every byte for the secret scan.
struct Tee<R> {
    inner: R,
    wire: Vec<u8>,
}

impl<R: Read> Read for Tee<R> {
    fn read(&mut self, buf: &mut [u8]) -> std::io::Result<usize> {
        let n = self.inner.read(buf)?;
        self.wire.extend_from_slice(&buf[..n]);
        Ok(n)
    }
}

fn read_line(reader: &mut impl BufRead) -> (String, bool, bool) {
    // (line including newline, hit_eof, other_error)
    let mut bytes = Vec::new();
    match reader.read_until(b'\n', &mut bytes) {
        Ok(0) => (String::new(), true, false),
        Ok(_) => (
            String::from_utf8_lossy(&bytes).into_owned(),
            !bytes.ends_with(b"\n"),
            false,
        ),
        Err(_) => (String::from_utf8_lossy(&bytes).into_owned(), false, true),
    }
}

fn read_chunked(reader: &mut impl BufRead) -> Vec<u8> {
    let mut out = Vec::new();
    loop {
        let (line, eof, error) = read_line(reader);
        if eof && line.is_empty() || error {
            return out;
        }
        let trimmed = line.trim();
        let size_text = trimmed.split(';').next().unwrap_or("");
        let Ok(size) = usize::from_str_radix(size_text, 16) else {
            return out;
        };
        if size == 0 {
            loop {
                let (trailer, eof, error) = read_line(reader);
                if eof || error || trailer.trim_end_matches(['\r', '\n']).is_empty() {
                    return out;
                }
            }
        }
        let mut chunk = vec![0_u8; size];
        let mut filled = 0;
        while filled < size {
            match reader.read(&mut chunk[filled..]) {
                Ok(0) | Err(_) => break,
                Ok(n) => filled += n,
            }
        }
        out.extend_from_slice(&chunk[..filled]);
        let _ = read_line(reader);
    }
}

impl Server {
    fn start(&mut self) -> Result<(), String> {
        self.port = free_port();
        let (args, env) = self.serve_command()?;
        let mut command = Command::new(&self.binary);
        command.args(&args).current_dir(&self.home).env_clear().envs(env).stdout(Stdio::null());
        let stderr = fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(self.root.join("stderr.log"))
            .map_err(|error| error.to_string())?;
        command.stderr(stderr);
        let mut child = command.spawn().map_err(|error| error.to_string())?;
        let deadline = Instant::now() + Duration::from_secs(60);
        while Instant::now() < deadline {
            if TcpStream::connect_timeout(
                &format!("127.0.0.1:{}", self.port).parse().unwrap(),
                Duration::from_millis(200),
            )
            .is_ok()
            {
                self.child = Some(child);
                return Ok(());
            }
            if let Ok(Some(status)) = child.try_wait() {
                return Err(format!("server exited during start: {status}"));
            }
            thread::sleep(Duration::from_millis(25));
        }
        let _ = child.kill();
        let _ = child.wait();
        Err("server did not start".to_owned())
    }

    fn stop(&mut self) -> String {
        let Some(mut child) = self.child.take() else {
            return "not running".to_owned();
        };
        // SAFETY: the pid belongs to the child this server owns.
        unsafe {
            libc::kill(child.id() as libc::pid_t, libc::SIGTERM);
        }
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            match child.try_wait() {
                Ok(Some(status)) => {
                    return if status.signal().is_some() {
                        "exit -1".to_owned()
                    } else {
                        describe_exit(status.code())
                    };
                }
                Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(10)),
                _ => {
                    let _ = child.kill();
                    let _ = child.wait();
                    return "killed after 30s".to_owned();
                }
            }
        }
    }

    fn mask_text(&self, text: &str, mask: &[String]) -> String {
        let mut out = self.secrets.redact(text);
        out = out.replace(&format!("127.0.0.1:{}", self.port), "127.0.0.1:$PORT");
        out = out.replace(&self.vault.to_string_lossy().into_owned(), "$VAULT");
        out = out.replace(&self.root.to_string_lossy().into_owned(), "$ROOT");
        for class in mask {
            match class.as_str() {
                "time" => out = self.patterns.rfc3339.replace_all(&out, "$$TIME").into_owned(),
                "hex" => out = self.patterns.hex_run.replace_all(&out, "$$HEX").into_owned(),
                "version" => {
                    out = self
                        .patterns
                        .version
                        .replace_all(&out, r#""version":"$$VERSION""#)
                        .into_owned();
                }
                "date" => out = self.patterns.date.replace_all(&out, "$$DATE").into_owned(),
                _ => {}
            }
        }
        out
    }

    fn normalize_headers(
        &self,
        headers: &[(String, String)],
        mask: &[String],
        volatile_length: bool,
    ) -> Vec<(String, String)> {
        let mut out = Vec::new();
        for (name, value) in headers {
            let mut value = value.clone();
            match name.as_str() {
                "date" => continue,
                "content-length" if volatile_length => continue,
                "retry-after" => value = "$RETRY".to_owned(),
                "content-type" => {
                    value = self
                        .patterns
                        .boundary
                        .replace_all(&value, "boundary=$$BOUNDARY")
                        .into_owned();
                }
                _ => {}
            }
            out.push((name.clone(), self.mask_text(&value, mask)));
        }
        out.sort();
        out
    }

    fn record_body(&self, observed: &mut Observed, body: &[u8], mask: &[String]) {
        if mask.iter().any(|class| class == "nobody") {
            return;
        }
        observed.body_len = body.len();
        if body.len() > 16 << 10 {
            observed.body_sha256 = sha256_hex(body);
            return;
        }
        if body.is_empty() {
            return;
        }
        let Ok(text) = std::str::from_utf8(body) else {
            observed.body_b64 = base64_std(body);
            return;
        };
        observed.body = self.mask_text(text, mask);
        observed.body_len = observed.body.len();
        if observed.body_len != body.len() {
            observed.body_len = 0;
        }
    }

    fn read_response(
        &self,
        stream: &TcpStream,
        reader: &mut BufReader<Tee<Conn>>,
        head_only: bool,
        mask: &[String],
        volatile_length: bool,
    ) -> Result<Observed, String> {
        let mut observed = Observed::default();
        loop {
            let (line, eof, error) = read_line(reader);
            if line.is_empty() {
                observed.silent = true;
                observed.closed = eof && !error;
                return Ok(observed);
            }
            let line = line.trim_end_matches(['\r', '\n']);
            let mut parts = line.splitn(3, ' ');
            let version = parts.next().unwrap_or("");
            let code = parts.next().unwrap_or("");
            if !version.starts_with("HTTP/") || code.is_empty() {
                if line.starts_with("\u{15}\u{3}") {
                    // A TLS alert record: the alert bytes differ between TLS stacks.
                    observed.exit = "tls alert".to_owned();
                    observed.closed = true;
                    return Ok(observed);
                }
                return Err(format!("unparsable status line {line:?}"));
            }
            observed.status = code
                .parse()
                .map_err(|_| format!("status line {line:?}: bad code"))?;
            observed.reason = parts.next().unwrap_or("").to_owned();
            let mut headers = Vec::new();
            loop {
                let (header, eof, error) = read_line(reader);
                if eof && header.is_empty() || error {
                    return Err("read headers: unexpected end".to_owned());
                }
                let header = header.trim_end_matches(['\r', '\n']);
                if header.is_empty() {
                    break;
                }
                let (name, value) = header.split_once(':').unwrap_or((header, ""));
                headers.push((name.trim().to_lowercase(), value.trim().to_owned()));
            }
            if observed.status == 100 {
                observed.continues += 1;
                continue;
            }
            observed.headers = self.normalize_headers(&headers, mask, volatile_length);
            let mut chunked = false;
            let mut length: Option<usize> = None;
            for (name, value) in &headers {
                match name.as_str() {
                    "transfer-encoding" if value.eq_ignore_ascii_case("chunked") => {
                        chunked = true;
                    }
                    "content-length" => {
                        if let Ok(n) = value.parse::<usize>() {
                            length = Some(n);
                        }
                    }
                    _ => {}
                }
            }
            let no_body = head_only
                || matches!(observed.status, 204 | 304)
                || (100..200).contains(&observed.status);
            let body = if no_body {
                Vec::new()
            } else if chunked {
                read_chunked(reader)
            } else if let Some(length) = length {
                let mut body = vec![0_u8; length];
                let mut filled = 0;
                while filled < length {
                    match reader.read(&mut body[filled..]) {
                        Ok(0) | Err(_) => break,
                        Ok(n) => filled += n,
                    }
                }
                body.truncate(filled);
                body
            } else {
                let mut body = Vec::new();
                let mut chunk = [0_u8; 8192];
                loop {
                    match reader.read(&mut chunk) {
                        Ok(0) | Err(_) => break,
                        Ok(n) => body.extend_from_slice(&chunk[..n]),
                    }
                }
                body
            };
            self.apply_capture(&body, &headers);
            self.record_body(&mut observed, &body, mask);
            let _ = stream.set_read_timeout(Some(Duration::from_millis(400)));
            let mut probe = [0_u8; 1];
            observed.closed = matches!(reader.read(&mut probe), Ok(0));
            return Ok(observed);
        }
    }

    fn exchange(
        &mut self,
        raw: &[u8],
        head_only: bool,
        step: &Step,
        mask: &[String],
        volatile_length: bool,
    ) -> Result<Observed, String> {
        let read_ms = if step.read_ms == 0 { 5000 } else { step.read_ms };
        let tcp = TcpStream::connect_timeout(
            &format!("127.0.0.1:{}", self.port).parse().unwrap(),
            Duration::from_secs(5),
        )
        .map_err(|error| error.to_string())?;
        let mut write_thread = None;
        let (conn, label) = if self.case.tls && !step.plain {
            match self.tls_connect(&tcp, &step.tls_version) {
                Ok((mut session, label)) => {
                    session.write_all(raw).map_err(|error| error.to_string())?;
                    (Conn::Tls(Box::new(session)), label)
                }
                Err(failure) => {
                    return Ok(Observed {
                        exit: failure,
                        ..Observed::default()
                    });
                }
            }
        } else {
            // Large requests are written concurrently so a server answering
            // early (and closing) cannot deadlock the client.
            let mut writer = tcp.try_clone().map_err(|error| error.to_string())?;
            let raw = raw.to_vec();
            write_thread = Some(thread::spawn(move || {
                let _ = writer.write_all(&raw);
            }));
            (
                Conn::Plain(tcp.try_clone().map_err(|error| error.to_string())?),
                String::new(),
            )
        };
        let _ = tcp.set_read_timeout(Some(Duration::from_millis(read_ms)));
        let mut reader = BufReader::with_capacity(
            1 << 20,
            Tee {
                inner: conn,
                wire: Vec::new(),
            },
        );
        let mut observed = self.read_response(&tcp, &mut reader, head_only, mask, volatile_length)?;
        if step.ignore_closed {
            observed.closed = false;
        }
        observed.tls = label;
        let wire = String::from_utf8_lossy(&reader.get_ref().wire).into_owned();
        drop(reader);
        let _ = tcp.shutdown(std::net::Shutdown::Both);
        if let Some(handle) = write_thread {
            let _ = handle.join();
        }
        for name in self.secrets.leaks(&wire) {
            self.leaked.insert(name);
        }
        Ok(observed)
    }

    fn tree(&self, include: &[String], mask: &[String]) -> Result<BTreeMap<String, String>, String> {
        let mut out = BTreeMap::new();
        // users.json carries per-run random token hashes: always compare it masked.
        let mut include = include.to_vec();
        include.push(".symdesk/users.json".to_owned());
        self.walk(&self.vault, &include, mask, &mut out)?;
        Ok(out)
    }

    fn walk(
        &self,
        dir: &Path,
        include: &[String],
        mask: &[String],
        out: &mut BTreeMap<String, String>,
    ) -> Result<(), String> {
        let mut entries: Vec<_> = fs::read_dir(dir)
            .map_err(|error| format!("{}: {error}", dir.display()))?
            .collect::<Result<_, _>>()
            .map_err(|error| error.to_string())?;
        entries.sort_by_key(fs::DirEntry::file_name);
        for entry in entries {
            let path = entry.path();
            let rel = path
                .strip_prefix(&self.vault)
                .unwrap()
                .to_string_lossy()
                .replace('\\', "/");
            let kind = entry.file_type().map_err(|error| error.to_string())?;
            if rel.starts_with(".http-harness")
                || rel.starts_with(".symdesk/server/sidecar.db")
                || rel.starts_with(".symdesk/server/state.db")
                || rel.ends_with(".tmp")
                || rel.starts_with(".symdesk/server/.")
            {
                continue;
            }
            let meta = fs::symlink_metadata(&path).map_err(|error| error.to_string())?;
            if kind.is_symlink() {
                let target = fs::read_link(&path).map_err(|error| error.to_string())?;
                out.insert(
                    rel,
                    format!("-> {}", self.mask_text(&target.to_string_lossy(), &[])),
                );
                continue;
            }
            if kind.is_dir() {
                out.insert(format!("{rel}/"), format!("dir {:04o}", meta.permissions().mode() & 0o777));
                self.walk(&path, include, mask, out)?;
                continue;
            }
            let data = fs::read(&path).map_err(|error| format!("{}: {error}", path.display()))?;
            let mode = format!("{:04o}", meta.permissions().mode() & 0o777);
            let mut described = None;
            if let Ok(text) = std::str::from_utf8(&data) {
                for prefix in include {
                    if rel.starts_with(prefix.as_str()) {
                        described = Some(format!("{mode} {}", self.mask_text(text, mask)));
                        break;
                    }
                }
            }
            out.insert(
                rel,
                described.unwrap_or_else(|| format!("{mode} sha256:{}", sha256_hex(&data))),
            );
        }
        Ok(())
    }

    fn build_request(&self, step: &Step) -> (Vec<u8>, bool) {
        let method = if step.method.is_empty() { "GET" } else { &step.method };
        let version = if step.version.is_empty() { "HTTP/1.1" } else { &step.version };
        let mut body = self.secrets.expand(&step.body);
        if let Some(repeat) = &step.body_repeat {
            body.push_str(&repeat.text.repeat(repeat.count));
        }
        let mut head = format!("{method} {} {version}\r\n", self.secrets.expand(&step.target));
        let (mut has_host, mut has_length) = (false, false);
        for (name, value) in &step.headers {
            match name.to_lowercase().as_str() {
                "host" => has_host = true,
                "content-length" | "transfer-encoding" => has_length = true,
                _ => {}
            }
            head.push_str(&format!("{name}: {}\r\n", self.secrets.expand(value)));
        }
        if !has_host && !step.no_host {
            head.push_str(&format!("Host: 127.0.0.1:{}\r\n", self.port));
        }
        if !step.no_close {
            head.push_str("Connection: close\r\n");
        }
        if !body.is_empty() && !has_length && !step.no_length {
            head.push_str(&format!("Content-Length: {}\r\n", body.len()));
        }
        head.push_str("\r\n");
        head.push_str(&body);
        (head.into_bytes(), method == "HEAD")
    }

    fn apply_fs(&self, step: &Step, default_mtime: i64) -> Result<(), String> {
        let target = self.vault.join(&step.path);
        match step.op.as_str() {
            "write" => {
                make_dirs(target.parent().unwrap())?;
                write_file(&target, self.secrets.expand(&step.content).as_bytes(), 0o600)?;
                let mtime = if step.mtime == 0 { default_mtime } else { step.mtime };
                set_mtime(&target, mtime)?;
            }
            "symlink" => {
                make_dirs(target.parent().unwrap())?;
                symlink(&step.content, &target).map_err(|e| e.to_string())?;
            }
            "remove" => {
                if target.is_dir() && !fs::symlink_metadata(&target).unwrap().file_type().is_symlink() {
                    fs::remove_dir_all(&target).map_err(|e| e.to_string())?;
                } else if fs::symlink_metadata(&target).is_ok() {
                    fs::remove_file(&target).map_err(|e| e.to_string())?;
                }
            }
            "mkdir" => make_dirs(&target)?,
            "chmod" => fs::set_permissions(&target, fs::Permissions::from_mode(step.mode))
                .map_err(|e| e.to_string())?,
            other => return Err(format!("unknown fs op {other:?}")),
        }
        Ok(())
    }

    fn observe_shutdown(&mut self, step: &Step) -> Result<Observed, String> {
        let mut stream = TcpStream::connect_timeout(
            &format!("127.0.0.1:{}", self.port).parse().unwrap(),
            Duration::from_secs(5),
        )
        .map_err(|error| error.to_string())?;
        stream
            .write_all(self.secrets.expand(&step.stall).as_bytes())
            .map_err(|error| error.to_string())?;
        thread::sleep(Duration::from_millis(300));
        let started = Instant::now();
        let exit = self.stop();
        let class = if started.elapsed() > Duration::from_secs(5) {
            "at the deadline"
        } else {
            "immediately"
        };
        drop(stream);
        Ok(Observed {
            exit: format!("{exit} {class}"),
            ..Observed::default()
        })
    }

    fn observe_step(&mut self, step: &Step, mtime: i64) -> Result<Observed, String> {
        *self.capture.lock().unwrap() = step.capture.clone();
        let started = Instant::now();
        let mut observed = self.observe_step_inner(step, mtime)?;
        if step.time_class {
            let elapsed = started.elapsed();
            observed.exit = if elapsed < Duration::from_secs(3) {
                "fast"
            } else if (Duration::from_secs(9)..=Duration::from_secs(14)).contains(&elapsed) {
                "after the 10 s header timeout"
            } else {
                "slow"
            }
            .to_owned();
        }
        self.capture.lock().unwrap().clear();
        Ok(observed)
    }

    fn observe_step_inner(&mut self, step: &Step, mtime: i64) -> Result<Observed, String> {
        match step.kind.as_str() {
            "sleep" => {
                thread::sleep(Duration::from_millis(step.wait_ms));
                Ok(Observed::default())
            }
            "wait_jobs" => self.wait_jobs(step),
            "provider" => self.observe_provider(step),
            "launch" => self.launch(step),
            "stream" => self.stream(step),
            "fs" => {
                self.apply_fs(step, mtime)?;
                // Let the vault watcher (FSEvents/inotify) deliver the change:
                // the snapshot cache trusts notifications, not metadata.
                std::thread::sleep(Duration::from_millis(500));
                Ok(Observed::default())
            }
            "tree" => Ok(Observed {
                tree: self.tree(&step.tree_include, &step.mask)?,
                ..Observed::default()
            }),
            "restart" => {
                let exit = self.stop();
                if step.rotate {
                    self.rotated = true;
                }
                self.start()?;
                Ok(Observed {
                    exit,
                    ..Observed::default()
                })
            }
            "shutdown" => self.observe_shutdown(step),
            "raw" => {
                let mut raw = self.secrets.expand(&step.raw);
                raw = raw.replace("{{FILL}}", &"a".repeat(step.raw_fill));
                raw = raw.replace("{{PORT}}", &self.port.to_string());
                let head_only = raw.split(' ').next() == Some("HEAD");
                self.exchange(raw.as_bytes(), head_only, step, &step.mask, step.volatile_length)
            }
            _ => {
                let (raw, head_only) = self.build_request(step);
                self.exchange(&raw, head_only, step, &step.mask, step.volatile_length)
            }
        }
    }
}

impl Server {
    fn expand_paths(&self, text: &str) -> String {
        let tls_dir = self.root.join("tls");
        let text = text
            .replace("$PORT", &self.port.to_string())
            .replace("$VAULT2", &self.vault2.to_string_lossy())
            .replace("$VAULT", &self.vault.to_string_lossy())
            .replace("$ROOT", &self.root.to_string_lossy())
            .replace("$TLSCERT", &tls_dir.join("cert.pem").to_string_lossy())
            .replace("$TLSKEY", &tls_dir.join("key.pem").to_string_lossy());
        self.secrets.expand(&text)
    }

    fn base_env(&self) -> Vec<(String, String)> {
        let home = self.home.to_string_lossy().into_owned();
        let tmp = self.home.join("tmp").to_string_lossy().into_owned();
        let path = if self.case.empty_path {
            self.root.join("emptybin").to_string_lossy().into_owned()
        } else {
            std::env::var("PATH").unwrap_or_default()
        };
        let pair = |key: &str, value: String| (key.to_owned(), value);
        vec![
            pair("HOME", home.clone()),
            pair("USERPROFILE", home.clone()),
            pair("XDG_CONFIG_HOME", format!("{home}/.config")),
            pair("XDG_DATA_HOME", format!("{home}/.local/share")),
            pair("XDG_CACHE_HOME", format!("{home}/.cache")),
            pair("TMPDIR", tmp.clone()),
            pair("TMP", tmp.clone()),
            pair("TEMP", tmp),
            pair("LANG", "C".to_owned()),
            pair("LC_ALL", "C".to_owned()),
            pair("TZ", "UTC".to_owned()),
            pair("TERM", "dumb".to_owned()),
            pair("NO_COLOR", "1".to_owned()),
            pair("SYMDESK_VAULT", self.vault.to_string_lossy().into_owned()),
            pair("SYMDESK_VERSION", "0.12.2".to_owned()),
            pair("PATH", path),
        ]
    }

    /// Mirrors the Go harness: arguments (including `serve`) and environment.
    fn serve_command(&self) -> Result<(Vec<String>, Vec<(String, String)>), String> {
        let mut env = self.base_env();
        let mut args: Vec<String> = Vec::new();
        if let Some(serve) = &self.case.serve {
            // The internal, environment-free options path for the HA file.
            let options = self.root.join("ha-options.json");
            args.push(format!("--internal-ha-options={}", options.display()));
            args.push("serve".to_owned());
            args.extend(serve.args.iter().map(|arg| self.expand_paths(arg)));
            for (name, value) in &serve.env {
                env.push((name.clone(), self.expand_paths(value)));
            }
            if serve.options.is_empty() {
                let _ = fs::remove_file(&options);
            } else {
                fs::write(&options, self.expand_paths(&serve.options))
                    .map_err(|error| error.to_string())?;
            }
        } else {
            let (token, worker) = if self.rotated {
                (&self.secrets.values["ADMIN2"], &self.secrets.values["WORKER2"])
            } else {
                (&self.secrets.values["ADMIN"], &self.secrets.values["WORKER"])
            };
            args.extend(["serve".to_owned(), "--listen".to_owned(), format!("127.0.0.1:{}", self.port)]);
            args.extend(["--vault".to_owned(), self.vault.to_string_lossy().into_owned()]);
            args.extend(["--token".to_owned(), token.clone()]);
            if !self.no_worker {
                args.extend(["--worker-token".to_owned(), worker.clone()]);
            }
            if self.case.tls {
                let dir = self.root.join("tls");
                args.extend(["--tls-cert".to_owned(), dir.join("cert.pem").to_string_lossy().into_owned()]);
                args.extend(["--tls-key".to_owned(), dir.join("key.pem").to_string_lossy().into_owned()]);
            }
        }
        for (name, value) in &self.case.env {
            env.push((name.clone(), self.expand_paths(value)));
        }
        if let Some(provider) = &self.provider {
            env.push(("SYMDESK_LLM_PROVIDER".to_owned(), "ollama".to_owned()));
            env.push(("SYMDESK_OLLAMA_URL".to_owned(), provider.url.clone()));
            env.push(("SYMDESK_OLLAMA_MODEL".to_owned(), "fixture-model".to_owned()));
        }
        Ok((args, env))
    }

    fn tls_connect(
        &self,
        tcp: &TcpStream,
        version: &str,
    ) -> Result<(rustls::StreamOwned<rustls::ClientConnection, TcpStream>, String), String> {
        let material = self.tls_certificate.as_ref().ok_or("no TLS material")?;
        let config = tls::client_config(material, version);
        let name = rustls::pki_types::ServerName::try_from("127.0.0.1")
            .map_err(|error| error.to_string())?
            .to_owned();
        let connection = rustls::ClientConnection::new(config, name).map_err(|error| error.to_string())?;
        let socket = tcp.try_clone().map_err(|error| error.to_string())?;
        let _ = socket.set_read_timeout(Some(Duration::from_secs(5)));
        let _ = socket.set_write_timeout(Some(Duration::from_secs(5)));
        let mut stream = rustls::StreamOwned::new(connection, socket);
        while stream.conn.is_handshaking() {
            if stream.conn.complete_io(&mut stream.sock).is_err() {
                return Err("tls handshake failed".to_owned());
            }
        }
        let protocol = match stream.conn.protocol_version() {
            Some(rustls::ProtocolVersion::TLSv1_3) => "TLS1.3",
            Some(rustls::ProtocolVersion::TLSv1_2) => "TLS1.2",
            _ => "TLS?",
        };
        let alpn = stream
            .conn
            .alpn_protocol()
            .map(|bytes| String::from_utf8_lossy(bytes).into_owned())
            .unwrap_or_default();
        Ok((stream, format!("{protocol} {alpn}")))
    }

    /// Stores `header:<name>` response headers and top-level string members of
    /// a JSON body as variables.
    fn apply_capture(&self, body: &[u8], headers: &[(String, String)]) {
        let capture = self.capture.lock().unwrap().clone();
        if capture.is_empty() {
            return;
        }
        let object = match serde_json::from_slice::<serde_json::Value>(body) {
            Ok(serde_json::Value::Object(object)) => object,
            _ => serde_json::Map::new(),
        };
        for (name, key) in capture {
            if let Some(header) = key.strip_prefix("header:") {
                for (field, value) in headers {
                    if field == header {
                        self.secrets.set_var(&name, value.clone());
                    }
                }
            } else if let Some(serde_json::Value::String(value)) = object.get(&key) {
                self.secrets.set_var(&name, value.clone());
            }
        }
    }

    /// Polls the job list until nothing is pending or processing.
    fn wait_jobs(&mut self, step: &Step) -> Result<Observed, String> {
        let token = self.secrets.expand(&step.body);
        let request = Step {
            method: "GET".to_owned(),
            target: "/api/v1/jobs".to_owned(),
            headers: vec![("Authorization".to_owned(), format!("Bearer {token}"))],
            ..Step::default()
        };
        let deadline = Instant::now() + Duration::from_secs(40);
        while Instant::now() < deadline {
            let (raw, head_only) = self.build_request(&request);
            if let Ok(observed) = self.exchange(&raw, head_only, &request, &[], true)
                && let Ok(serde_json::Value::Array(jobs)) = serde_json::from_str::<serde_json::Value>(&observed.body)
                && !jobs.is_empty()
                && !jobs.iter().any(|job| {
                    matches!(job["status"].as_str(), Some("pending" | "processing"))
                })
            {
                return Ok(Observed {
                    exit: "settled".to_owned(),
                    ..Observed::default()
                });
            }
            thread::sleep(Duration::from_millis(200));
        }
        Ok(Observed {
            exit: "timeout".to_owned(),
            ..Observed::default()
        })
    }

    fn observe_provider(&self, step: &Step) -> Result<Observed, String> {
        let provider = self.provider.as_ref().ok_or("provider step without a provider")?;
        let deadline = Instant::now() + Duration::from_secs(4);
        while Instant::now() < deadline && provider.cancelled() < step.count {
            thread::sleep(Duration::from_millis(20));
        }
        Ok(Observed {
            body: self.mask_text(&provider.report(), &step.mask),
            ..Observed::default()
        })
    }

    /// Runs `serve <args>` to completion and records its exit and stderr.
    fn launch(&mut self, step: &Step) -> Result<Observed, String> {
        self.port = free_port();
        let mut args = Vec::new();
        if let Some(serve) = &self.case.serve {
            let options = self.root.join("ha-options.json");
            args.push(format!("--internal-ha-options={}", options.display()));
            if serve.options.is_empty() {
                let _ = fs::remove_file(&options);
            } else {
                fs::write(&options, self.expand_paths(&serve.options))
                    .map_err(|error| error.to_string())?;
            }
        }
        args.push("serve".to_owned());
        args.extend(step.args.iter().map(|arg| self.expand_paths(arg)));
        let log = self.root.join("launch.log");
        let stderr = fs::File::create(&log).map_err(|error| error.to_string())?;
        let stdout_log = self.root.join("launch.out");
        let stdout = if step.with_stdout {
            Stdio::from(fs::File::create(&stdout_log).map_err(|error| error.to_string())?)
        } else {
            Stdio::null()
        };
        let mut command = Command::new(&self.binary);
        command
            .args(&args)
            .current_dir(&self.home)
            .env_clear()
            .envs(self.base_env())
            .stdout(stdout)
            .stderr(stderr);
        for (name, value) in &self.case.env {
            command.env(name, self.expand_paths(value));
        }
        let mut child = command.spawn().map_err(|error| error.to_string())?;
        let deadline = Instant::now() + Duration::from_secs(8);
        let exit = loop {
            match child.try_wait() {
                Ok(Some(status)) => {
                    break if status.signal().is_some() {
                        "exit -1".to_owned()
                    } else {
                        describe_exit(status.code())
                    };
                }
                Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(20)),
                _ => {
                    let _ = child.kill();
                    let _ = child.wait();
                    break "still running after 8s".to_owned();
                }
            }
        };
        let text = fs::read_to_string(&log).unwrap_or_default();
        let kept: Vec<&str> = text
            .lines()
            .filter(|line| !line.is_empty() && !line.starts_with("time=") && !line.starts_with("LISTENING"))
            .collect();
        for name in self.secrets.leaks(&text) {
            self.leaked.insert(name);
        }
        let mut body = self.mask_text(&kept.join("\n"), &step.mask);
        if step.with_stdout {
            let printed = fs::read_to_string(&stdout_log).unwrap_or_default();
            body = format!(
                "{}{body}",
                self.mask_text(&format!("stdout:\n{printed}stderr:\n"), &step.mask)
            );
        }
        Ok(Observed {
            exit,
            body,
            ..Observed::default()
        })
    }

    /// Two-phase read of a streamed response (see the Go `stream`).
    fn stream(&mut self, step: &Step) -> Result<Observed, String> {
        let (raw, _) = self.build_request(step);
        let tcp = TcpStream::connect_timeout(
            &format!("127.0.0.1:{}", self.port).parse().unwrap(),
            Duration::from_secs(5),
        )
        .map_err(|error| error.to_string())?;
        let (mut conn, label) = if self.case.tls && !step.plain {
            match self.tls_connect(&tcp, &step.tls_version) {
                Ok((session, label)) => (Conn::Tls(Box::new(session)), label),
                Err(failure) => {
                    return Ok(Observed {
                        exit: failure,
                        ..Observed::default()
                    });
                }
            }
        } else {
            (Conn::Plain(tcp.try_clone().map_err(|error| error.to_string())?), String::new())
        };
        conn.write_all(&raw).map_err(|error| error.to_string())?;
        let read_ms = if step.read_ms == 0 { 8000 } else { step.read_ms };
        let _ = tcp.set_read_timeout(Some(Duration::from_millis(read_ms)));
        let mut reader = BufReader::with_capacity(1 << 20, Tee { inner: conn, wire: Vec::new() });
        let mut observed = Observed {
            tls: label,
            ..Observed::default()
        };
        let (line, _, _) = read_line(&mut reader);
        let line = line.trim_end_matches(['\r', '\n']).to_owned();
        let mut parts = line.splitn(3, ' ');
        let _ = parts.next();
        observed.status = parts.next().and_then(|code| code.parse().ok()).ok_or("bad status line")?;
        observed.reason = parts.next().unwrap_or("").to_owned();
        let mut headers = Vec::new();
        let mut chunked = false;
        loop {
            let (header, eof, error) = read_line(&mut reader);
            if eof && header.is_empty() || error {
                return Err("stream: read headers".to_owned());
            }
            let header = header.trim_end_matches(['\r', '\n']);
            if header.is_empty() {
                break;
            }
            let (name, value) = header.split_once(':').unwrap_or((header, ""));
            let (name, value) = (name.trim().to_lowercase(), value.trim().to_owned());
            if name == "transfer-encoding" && value.eq_ignore_ascii_case("chunked") {
                chunked = true;
            }
            headers.push((name, value));
        }
        observed.headers = self.normalize_headers(&headers, &step.mask, true);
        let body: Box<dyn Read + '_> = if chunked {
            Box::new(ext::ChunkedReader::new(&mut reader))
        } else {
            Box::new(&mut reader)
        };
        let mut lines = BufReader::with_capacity(8 << 20, body);
        let mut early = String::new();
        let mut index = 0;
        while index < step.stream_lines || !step.stream_until.is_empty() {
            let mut text = Vec::new();
            let read = lines.read_until(b'\n', &mut text).unwrap_or(0);
            let text = String::from_utf8_lossy(&text).into_owned();
            early.push_str(&text);
            index += 1;
            if read == 0
                || (!step.stream_until.is_empty() && text.contains(&step.stream_until))
                || index > 200
            {
                break;
            }
        }
        observed.early = self.mask_text(&early, &step.mask);
        if step.abort {
            drop(lines);
            let _ = tcp.shutdown(std::net::Shutdown::Both);
            observed.closed = true;
            return Ok(observed);
        }
        if step.release
            && let Some(provider) = &self.provider
        {
            provider.release();
        }
        let mut rest = Vec::new();
        let _ = lines.read_to_end(&mut rest);
        let mut full = early.into_bytes();
        full.extend_from_slice(&rest);
        drop(lines);
        let wire = String::from_utf8_lossy(&reader.get_ref().wire).into_owned();
        for name in self.secrets.leaks(&wire) {
            self.leaked.insert(name);
        }
        self.apply_capture(&full, &headers);
        self.record_body(&mut observed, &full, &step.mask);
        observed.closed = true;
        Ok(observed)
    }
}


mod ext {
    use std::io::{self, BufRead, Read, Write};
    use std::net::TcpStream;

    /// A plaintext or TLS client connection.
    pub enum Conn {
        Plain(TcpStream),
        Tls(Box<rustls::StreamOwned<rustls::ClientConnection, TcpStream>>),
    }

    impl Read for Conn {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            match self {
                Self::Plain(stream) => stream.read(buf),
                // A peer that closes without close_notify is a closed connection.
                Self::Tls(stream) => match stream.read(buf) {
                    Err(error) if error.kind() == io::ErrorKind::UnexpectedEof => Ok(0),
                    other => other,
                },
            }
        }
    }

    impl Write for Conn {
        fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
            match self {
                Self::Plain(stream) => stream.write(buf),
                Self::Tls(stream) => stream.write(buf),
            }
        }

        fn flush(&mut self) -> io::Result<()> {
            match self {
                Self::Plain(stream) => stream.flush(),
                Self::Tls(stream) => stream.flush(),
            }
        }
    }

    /// Incremental HTTP/1.1 chunked-body decoder.
    pub struct ChunkedReader<R> {
        inner: R,
        remaining: usize,
        done: bool,
    }

    impl<R: BufRead> ChunkedReader<R> {
        pub fn new(inner: R) -> Self {
            Self {
                inner,
                remaining: 0,
                done: false,
            }
        }
    }

    impl<R: BufRead> Read for ChunkedReader<R> {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            if self.done || buf.is_empty() {
                return Ok(0);
            }
            if self.remaining == 0 {
                let mut line = String::new();
                if self.inner.read_line(&mut line)? == 0 {
                    return Ok(0);
                }
                let size_text = line.trim().split(';').next().unwrap_or("");
                let Ok(size) = usize::from_str_radix(size_text, 16) else {
                    self.done = true;
                    return Ok(0);
                };
                if size == 0 {
                    self.done = true;
                    return Ok(0);
                }
                self.remaining = size;
            }
            let want = buf.len().min(self.remaining);
            let read = self.inner.read(&mut buf[..want])?;
            self.remaining -= read;
            if self.remaining == 0 {
                let mut crlf = String::new();
                let _ = self.inner.read_line(&mut crlf);
            }
            Ok(read)
        }
    }
}

mod tls {
    use std::sync::Arc;

    use rustls::{ClientConfig, RootCertStore, crypto::ring, pki_types::CertificateDer};

    pub struct Material {
        pub cert_pem: String,
        pub key_pem: String,
        pub cert_der: Vec<u8>,
    }

    /// Mints a fresh self-signed pair for 127.0.0.1 / localhost (per run).
    pub fn mint() -> Material {
        let certified = rcgen::generate_simple_self_signed(vec![
            "127.0.0.1".to_owned(),
            "localhost".to_owned(),
        ])
        .expect("mint test certificate");
        Material {
            cert_pem: certified.cert.pem(),
            key_pem: certified.signing_key.serialize_pem(),
            cert_der: certified.cert.der().to_vec(),
        }
    }

    pub fn client_config(material: &Material, version: &str) -> Arc<ClientConfig> {
        let mut roots = RootCertStore::empty();
        roots
            .add(CertificateDer::from(material.cert_der.clone()))
            .expect("trust the minted certificate");
        let provider = Arc::new(ring::default_provider());
        let builder = match version {
            "1.2" => ClientConfig::builder_with_provider(provider)
                .with_protocol_versions(&[&rustls::version::TLS12])
                .expect("TLS 1.2 only"),
            "1.3" => ClientConfig::builder_with_provider(provider)
                .with_protocol_versions(&[&rustls::version::TLS13])
                .expect("TLS 1.3 only"),
            _ => ClientConfig::builder_with_provider(provider)
                .with_safe_default_protocol_versions()
                .expect("default protocol versions"),
        };
        let mut config = builder.with_root_certificates(roots).with_no_client_auth();
        config.alpn_protocols = vec![b"http/1.1".to_vec()];
        Arc::new(config)
    }
}

/// The fake loopback Ollama endpoint; the Go harness implements the same script
/// (`httpProvider` in cmd/symdesk/http_process_ext_port_test.go).
mod provider {
    use std::{
        io::{ErrorKind, Read, Write},
        net::{TcpListener, TcpStream},
        sync::{
            Arc, Condvar, Mutex,
            atomic::{AtomicBool, Ordering},
        },
        thread,
        time::{Duration, Instant},
    };

    #[derive(Default)]
    struct State {
        /// The planted provider API key the "key echo" failure reflects.
        plant: String,
        requests: Mutex<Vec<String>>,
        cancellations: Mutex<usize>,
        gate: (Mutex<usize>, Condvar),
        stop: AtomicBool,
    }

    pub struct Provider {
        pub url: String,
        state: Arc<State>,
    }

    impl Provider {
        pub fn start(plant: &str) -> Self {
            let listener = TcpListener::bind("127.0.0.1:0").expect("bind provider");
            let url = format!("http://{}", listener.local_addr().unwrap());
            let state = Arc::new(State {
                plant: plant.to_owned(),
                ..State::default()
            });
            let accept_state = Arc::clone(&state);
            thread::spawn(move || {
                for stream in listener.incoming() {
                    if accept_state.stop.load(Ordering::SeqCst) {
                        break;
                    }
                    let Ok(stream) = stream else { continue };
                    let state = Arc::clone(&accept_state);
                    thread::spawn(move || {
                        let _ = handle(stream, &state);
                    });
                }
            });
            Self { url, state }
        }

        pub fn release(&self) {
            *self.state.gate.0.lock().unwrap() += 1;
            self.state.gate.1.notify_all();
        }

        pub fn cancelled(&self) -> usize {
            *self.state.cancellations.lock().unwrap()
        }

        pub fn report(&self) -> String {
            let mut out = String::new();
            for line in self.state.requests.lock().unwrap().iter() {
                out.push_str(line);
                out.push('\n');
            }
            out.push_str(&format!("cancellations={}", self.cancelled()));
            out
        }
    }

    impl Drop for Provider {
        fn drop(&mut self) {
            self.state.stop.store(true, Ordering::SeqCst);
            let _ = TcpStream::connect(self.url.trim_start_matches("http://"));
        }
    }

    fn read_request(stream: &mut TcpStream) -> Option<(String, String, Vec<u8>)> {
        let mut data = Vec::new();
        let mut chunk = [0_u8; 8192];
        let head_end = loop {
            let read = stream.read(&mut chunk).ok()?;
            if read == 0 {
                return None;
            }
            data.extend_from_slice(&chunk[..read]);
            if let Some(position) = data.windows(4).position(|window| window == b"\r\n\r\n") {
                break position + 4;
            }
        };
        let head = String::from_utf8_lossy(&data[..head_end]).into_owned();
        let mut lines = head.lines();
        let mut request_line = lines.next()?.split(' ');
        let method = request_line.next()?.to_owned();
        let path = request_line.next()?.to_owned();
        let mut length = 0_usize;
        let mut chunked = false;
        for line in lines {
            let lower = line.to_ascii_lowercase();
            if let Some(value) = lower.strip_prefix("content-length:") {
                length = value.trim().parse().unwrap_or(0);
            } else if lower.starts_with("transfer-encoding:") && lower.contains("chunked") {
                chunked = true;
            }
        }
        let mut body = data[head_end..].to_vec();
        if chunked {
            // Decode chunk framing until the terminating chunk.
            let mut decoded = Vec::new();
            let mut rest = body;
            loop {
                if let Some(line_end) = rest.windows(2).position(|window| window == b"\r\n") {
                    let size = usize::from_str_radix(
                        String::from_utf8_lossy(&rest[..line_end]).trim().split(';').next().unwrap_or("0"),
                        16,
                    )
                    .ok()?;
                    if size == 0 {
                        break;
                    }
                    while rest.len() < line_end + 2 + size + 2 {
                        let read = stream.read(&mut chunk).ok()?;
                        if read == 0 {
                            return None;
                        }
                        rest.extend_from_slice(&chunk[..read]);
                    }
                    decoded.extend_from_slice(&rest[line_end + 2..line_end + 2 + size]);
                    rest.drain(..line_end + 2 + size + 2);
                } else {
                    let read = stream.read(&mut chunk).ok()?;
                    if read == 0 {
                        return None;
                    }
                    rest.extend_from_slice(&chunk[..read]);
                }
            }
            body = decoded;
        } else {
            while body.len() < length {
                let read = stream.read(&mut chunk).ok()?;
                if read == 0 {
                    return None;
                }
                body.extend_from_slice(&chunk[..read]);
            }
        }
        Some((method, path, body))
    }

    fn chunk(stream: &mut TcpStream, bytes: &[u8]) -> std::io::Result<()> {
        stream.write_all(format!("{:x}\r\n", bytes.len()).as_bytes())?;
        stream.write_all(bytes)?;
        stream.write_all(b"\r\n")?;
        stream.flush()
    }

    /// Waits until the peer closes its end (the request context ends in Go).
    fn wait_closed(stream: &mut TcpStream, until: Option<&dyn Fn() -> bool>) -> bool {
        let _ = stream.set_nonblocking(true);
        let deadline = Instant::now() + Duration::from_secs(25);
        let mut sink = [0_u8; 64];
        while Instant::now() < deadline {
            if let Some(done) = until
                && done()
            {
                return false;
            }
            match stream.read(&mut sink) {
                Ok(0) => return true,
                Ok(_) => {}
                Err(error) if error.kind() == ErrorKind::WouldBlock => {
                    thread::sleep(Duration::from_millis(10));
                }
                Err(_) => return true,
            }
        }
        false
    }

    fn handle(mut stream: TcpStream, state: &State) -> std::io::Result<()> {
        let Some((method, path, body)) = read_request(&mut stream) else {
            return Ok(());
        };
        if method != "POST" || path != "/api/generate" {
            return stream.write_all(
                b"HTTP/1.1 404 Not Found\r\nContent-Length: 24\r\nConnection: close\r\n\r\nunexpected provider route",
            );
        }
        let Ok(request) = serde_json::from_slice::<serde_json::Value>(&body) else {
            return stream.write_all(
                b"HTTP/1.1 400 Bad Request\r\nContent-Length: 23\r\nConnection: close\r\n\r\ninvalid provider request",
            );
        };
        let prompt = request["prompt"].as_str().unwrap_or("").to_owned();
        let recorded = serde_json::json!({
            "model": request["model"].as_str().unwrap_or(""),
            "prompt": prompt,
            "stream": request["stream"].as_bool().unwrap_or(false),
        });
        state
            .requests
            .lock()
            .unwrap()
            .push(format!("POST /api/generate {recorded}"));
        let cancelled = || *state.cancellations.lock().unwrap() += 1;
        let ndjson = b"HTTP/1.1 200 OK\r\nContent-Type: application/x-ndjson\r\nTransfer-Encoding: chunked\r\n\r\n";
        if prompt.contains("key echo provider input") {
            let body = format!("rejected credential {} for this model", state.plant);
            return stream.write_all(
                format!(
                    "HTTP/1.1 401 Unauthorized\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                )
                .as_bytes(),
            );
        }
        if prompt.contains("provider error input") {
            return stream.write_all(
                b"HTTP/1.1 500 Internal Server Error\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: 24\r\nConnection: close\r\n\r\nfixture provider failure",
            );
        }
        if prompt.contains("disconnect provider input") {
            stream.write_all(ndjson)?;
            chunk(&mut stream, b"{\"response\":\"partial\",\"done\":false}\n")?;
            if wait_closed(&mut stream, None) {
                cancelled();
            }
            return Ok(());
        }
        if prompt.contains("oversized provider input") {
            stream.write_all(ndjson)?;
            chunk(&mut stream, b"{\"response\":\"")?;
            let block = vec![b'x'; 1 << 20];
            for _ in 0..4 {
                chunk(&mut stream, &block)?;
            }
            chunk(&mut stream, b"\"}\n")?;
            return stream.write_all(b"0\r\n\r\n");
        }
        if prompt.contains("split provider input") {
            stream.write_all(ndjson)?;
            chunk(&mut stream, b"{\"response\":\"spl")?;
            thread::sleep(Duration::from_millis(150));
            chunk(
                &mut stream,
                b"it\",\"done\":false}\n{\"response\":\"\",\"done\":true}\n",
            )?;
            return stream.write_all(b"0\r\n\r\n");
        }
        if prompt.contains("hang provider input") {
            stream.write_all(ndjson)?;
            if wait_closed(&mut stream, None) {
                cancelled();
            }
            return Ok(());
        }
        stream.write_all(ndjson)?;
        chunk(&mut stream, b"{\"response\":\"first\",\"done\":false}\n")?;
        if prompt.contains("gated provider input") {
            let deadline = Instant::now() + Duration::from_secs(20);
            let released = || {
                let mut count = state.gate.0.lock().unwrap();
                if *count > 0 {
                    *count -= 1;
                    true
                } else {
                    false
                }
            };
            let _ = stream.set_nonblocking(true);
            let mut sink = [0_u8; 64];
            loop {
                if released() {
                    break;
                }
                match stream.read(&mut sink) {
                    Ok(0) => {
                        cancelled();
                        return Ok(());
                    }
                    Err(error) if error.kind() == ErrorKind::WouldBlock => {}
                    Err(_) => {
                        cancelled();
                        return Ok(());
                    }
                    Ok(_) => {}
                }
                if Instant::now() >= deadline {
                    break;
                }
                thread::sleep(Duration::from_millis(10));
            }
            let _ = stream.set_nonblocking(false);
        }
        chunk(&mut stream, b"{\"response\":\" second\",\"done\":false}\n")?;
        chunk(&mut stream, b"{\"response\":\"\",\"done\":true}\n")?;
        stream.write_all(b"0\r\n\r\n")
    }
}

fn make_dirs(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::DirBuilderExt;
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o750)
        .create(path)
        .map_err(|error| format!("{}: {error}", path.display()))
}

fn write_file(path: &Path, data: &[u8], mode: u32) -> Result<(), String> {
    fs::write(path, data).map_err(|error| format!("{}: {error}", path.display()))?;
    fs::set_permissions(path, fs::Permissions::from_mode(mode)).map_err(|error| error.to_string())
}

fn set_mtime(path: &Path, seconds: i64) -> Result<(), String> {
    let file = fs::OpenOptions::new()
        .write(true)
        .open(path)
        .map_err(|error| error.to_string())?;
    let when = if seconds >= 0 {
        UNIX_EPOCH + Duration::from_secs(seconds as u64)
    } else {
        UNIX_EPOCH - Duration::from_secs(seconds.unsigned_abs())
    };
    file.set_modified(when).map_err(|error| error.to_string())
}

fn compare(label: &str, expected: &Observed, actual: &Observed, problems: &mut Vec<String>) {
    fn clip(text: &str) -> String {
        if text.len() > 400 {
            format!("{}…({} bytes)", &text[..text.floor_char_boundary(400)], text.len())
        } else {
            text.to_owned()
        }
    }
    if expected == actual {
        return;
    }
    let mut diffs = Vec::new();
    macro_rules! field {
        ($name:ident) => {
            if expected.$name != actual.$name {
                diffs.push(format!(
                    "  {}: go={} rust={}",
                    stringify!($name),
                    clip(&format!("{:?}", expected.$name)),
                    clip(&format!("{:?}", actual.$name))
                ));
            }
        };
    }
    field!(status);
    field!(reason);
    field!(headers);
    field!(body);
    field!(body_b64);
    field!(body_len);
    field!(body_sha256);
    field!(continues);
    field!(closed);
    field!(silent);
    field!(exit);
    field!(early);
    field!(tls);
    if expected.tree != actual.tree {
        for key in expected.tree.keys().chain(actual.tree.keys()).collect::<BTreeSet<_>>() {
            if expected.tree.get(key) != actual.tree.get(key) {
                diffs.push(format!(
                    "  tree[{key}]: go={:?} rust={:?}",
                    expected.tree.get(key).map(|v| clip(v)),
                    actual.tree.get(key).map(|v| clip(v))
                ));
            }
        }
    }
    problems.push(format!("{label}\n{}", diffs.join("\n")));
}

fn run_case(binary: &Path, case: &Case) -> Vec<String> {
    let mut problems = Vec::new();
    let base = std::env::temp_dir().join(format!(
        "http-replay-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    fs::create_dir_all(&base).unwrap();
    let root = fs::canonicalize(&base).unwrap();
    let vault = root.join("vault");
    let home = root.join("home");
    for dir in [vault.clone(), home.join("tmp")] {
        fs::create_dir_all(dir).unwrap();
    }
    let secrets = Secrets::new();
    let planted_key = secrets.lookup("PROVKEY");
    let mut server = Server {
        binary: binary.to_path_buf(),
        root: root.clone(),
        vault: vault.clone(),
        home,
        secrets,
        port: 0,
        child: None,
        rotated: false,
        no_worker: case.no_worker,
        leaked: BTreeSet::new(),
        patterns: patterns(),
        case: case.clone(),
        vault2: root.join("vault2"),
        provider: case.provider.then(|| provider::Provider::start(&planted_key)),
        tls_certificate: (case.tls || case.no_server).then(tls::mint),
        capture: Mutex::new(BTreeMap::new()),
    };
    let mtime = if case.mtime == 0 { 1_767_323_045 } else { case.mtime };
    let setup = (|| -> Result<(), String> {
        for (name, content) in &case.files {
            let step = Step {
                op: "write".to_owned(),
                path: name.clone(),
                content: content.clone(),
                mtime,
                ..Step::default()
            };
            server.apply_fs(&step, mtime)?;
        }
        for (name, target) in &case.symlinks {
            let target = target
                .strip_prefix("$ROOT/")
                .map_or_else(|| target.clone(), |rest| root.join(rest).to_string_lossy().into_owned());
            let step = Step {
                op: "symlink".to_owned(),
                path: name.clone(),
                content: target,
                ..Step::default()
            };
            server.apply_fs(&step, mtime)?;
        }
        write_file(&root.join("outside.md"), b"outside", 0o600)?;
        make_dirs(&server.vault2)?;
        write_file(
            &server.vault2.join("Second.md"),
            b"---\ntitle: Second\n---\nsecond vault",
            0o600,
        )?;
        if case.empty_path {
            make_dirs(&root.join("emptybin"))?;
        }
        if let Some(material) = &server.tls_certificate {
            let dir = root.join("tls");
            make_dirs(&dir)?;
            write_file(&dir.join("cert.pem"), material.cert_pem.as_bytes(), 0o600)?;
            write_file(&dir.join("key.pem"), material.key_pem.as_bytes(), 0o600)?;
        }
        if case.no_server { Ok(()) } else { server.start() }
    })();
    if let Err(error) = setup {
        problems.push(format!("[{}] setup: {error}", case.name));
        let _ = server.stop();
        let _ = fs::remove_dir_all(&root);
        return problems;
    }
    for step in &case.steps {
        let mut expected = step.expect.clone().unwrap_or_default();
        // A secret Go reflects (SEC-002): Rust must have replaced it.
        for name in &step.redacts {
            expected.body = expected.body.replace(&format!("${name}"), "[redacted]");
        }
        match server.observe_step(step, mtime) {
            Ok(mut actual) => {
                if !step.redacts.is_empty() {
                    // The length recorded by Go includes the reflected secret.
                    actual.body_len = 0;
                }
                compare(&format!("[{}] {}", case.name, step.label), &expected, &actual, &mut problems);
            }
            Err(error) => problems.push(format!("[{}] {}: {error}", case.name, step.label)),
        }
    }
    if !case.no_server {
        let exit = server.stop();
        if exit != "exit 0" && exit != "exit 1" && exit != "not running" {
            problems.push(format!("[{}] server stop: {exit}", case.name));
        }
    }
    let log = fs::read(root.join("stderr.log")).unwrap_or_default();
    for name in server.secrets.leaks(&String::from_utf8_lossy(&log)) {
        server.leaked.insert(name);
    }
    let leaked: Vec<String> = server.leaked.iter().cloned().collect();
    if !leaked.is_empty() || !case.stderr_leaks.is_empty() {
        problems.push(format!(
            "[{}] credentials leaked: rust={leaked:?} go={:?}",
            case.name, case.stderr_leaks
        ));
    }
    let _ = fs::remove_dir_all(&root);
    problems
}

/// Replays every case of `fixture_json` against `binary`. The recorded case
/// and step totals are asserted so a changed or skipped fixture cannot pass.
pub fn replay(binary: &str, fixture_json: &str, expected_cases: usize, expected_steps: usize) {
    let fixture: Fixture = serde_json::from_str(fixture_json).expect("decode Go-owned HTTP fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), expected_cases, "recorded case count");
    let steps: usize = fixture.cases.iter().map(|case| case.steps.len()).sum();
    assert_eq!(steps, expected_steps, "recorded step count");
    let binary = PathBuf::from(binary);
    let next = AtomicUsize::new(0);
    let problems = Mutex::new(Vec::new());
    let executed = AtomicUsize::new(0);
    thread::scope(|scope| {
        for _ in 0..4 {
            scope.spawn(|| {
                loop {
                    let index = next.fetch_add(1, Ordering::SeqCst);
                    let Some(case) = fixture.cases.get(index) else {
                        break;
                    };
                    let found = run_case(&binary, case);
                    executed.fetch_add(case.steps.len(), Ordering::SeqCst);
                    problems.lock().unwrap().extend(found);
                }
            });
        }
    });
    assert_eq!(executed.load(Ordering::SeqCst), expected_steps, "executed steps");
    let problems = problems.into_inner().unwrap();
    assert!(
        problems.is_empty(),
        "{} divergences from the Go recording:\n{}",
        problems.len(),
        problems.join("\n")
    );
}
