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
    pub expect: Option<Observed>,
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
}

impl Secrets {
    fn new() -> Self {
        let mut values = BTreeMap::new();
        for name in ["ADMIN", "WORKER", "ADMIN2", "WORKER2", "NAMED", "WNAMED"] {
            values.insert(name.to_owned(), random_hex(20));
        }
        for name in ["NAMED", "WNAMED"] {
            let hash = sha256_hex(values[name].as_bytes());
            values.insert(format!("{name}_HASH"), hash);
        }
        Self { values }
    }

    fn order(&self) -> Vec<&String> {
        let mut names: Vec<&String> = self.values.keys().collect();
        names.sort_by(|a, b| b.len().cmp(&a.len()).then_with(|| a.cmp(b)));
        names
    }

    fn expand(&self, text: &str) -> String {
        let mut out = text.to_owned();
        for name in self.order() {
            out = out.replace(&format!("${name}"), &self.values[name]);
        }
        out
    }

    fn redact(&self, text: &str) -> String {
        let mut out = text.to_owned();
        for name in self.order() {
            out = out.replace(&self.values[name], &format!("${name}"));
        }
        out
    }

    fn leaks(&self, text: &str) -> Vec<String> {
        self.order()
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
        hex_run: Regex::new(r"[0-9a-f]{32,}").unwrap(),
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
        let (token, worker) = if self.rotated {
            (&self.secrets.values["ADMIN2"], &self.secrets.values["WORKER2"])
        } else {
            (&self.secrets.values["ADMIN"], &self.secrets.values["WORKER"])
        };
        let mut command = Command::new(&self.binary);
        command
            .arg("serve")
            .arg("--listen")
            .arg(format!("127.0.0.1:{}", self.port))
            .arg("--vault")
            .arg(&self.vault)
            .arg("--token")
            .arg(token);
        if !self.no_worker {
            command.arg("--worker-token").arg(worker);
        }
        let tmp = self.home.join("tmp");
        command
            .current_dir(&self.home)
            .env_clear()
            .env("HOME", &self.home)
            .env("USERPROFILE", &self.home)
            .env("XDG_CONFIG_HOME", self.home.join(".config"))
            .env("XDG_DATA_HOME", self.home.join(".local/share"))
            .env("XDG_CACHE_HOME", self.home.join(".cache"))
            .env("TMPDIR", &tmp)
            .env("TMP", &tmp)
            .env("TEMP", &tmp)
            .env("LANG", "C")
            .env("LC_ALL", "C")
            .env("TZ", "UTC")
            .env("TERM", "dumb")
            .env("NO_COLOR", "1")
            .env("SYMDESK_VAULT", &self.vault)
            .env("SYMDESK_VERSION", "0.12.2")
            .env("PATH", std::env::var_os("PATH").unwrap_or_default())
            .stdout(Stdio::null());
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
        reader: &mut BufReader<Tee<&TcpStream>>,
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
        read_ms: u64,
        mask: &[String],
        volatile_length: bool,
    ) -> Result<Observed, String> {
        let read_ms = if read_ms == 0 { 5000 } else { read_ms };
        let stream = TcpStream::connect_timeout(
            &format!("127.0.0.1:{}", self.port).parse().unwrap(),
            Duration::from_secs(5),
        )
        .map_err(|error| error.to_string())?;
        // Large requests are written concurrently so a server answering early
        // (and closing) cannot deadlock the client.
        let mut writer = stream.try_clone().map_err(|error| error.to_string())?;
        let raw = raw.to_vec();
        let write = thread::spawn(move || {
            let _ = writer.write_all(&raw);
        });
        let _ = stream.set_read_timeout(Some(Duration::from_millis(read_ms)));
        let mut reader = BufReader::with_capacity(
            1 << 20,
            Tee {
                inner: &stream,
                wire: Vec::new(),
            },
        );
        let observed = self.read_response(&stream, &mut reader, head_only, mask, volatile_length)?;
        let wire = String::from_utf8_lossy(&reader.get_ref().wire).into_owned();
        drop(reader);
        let _ = stream.shutdown(std::net::Shutdown::Both);
        let _ = write.join();
        for name in self.secrets.leaks(&wire) {
            self.leaked.insert(name);
        }
        Ok(observed)
    }

    fn tree(&self, include: &[String], mask: &[String]) -> Result<BTreeMap<String, String>, String> {
        let mut out = BTreeMap::new();
        self.walk(&self.vault, include, mask, &mut out)?;
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
        match step.kind.as_str() {
            "fs" => {
                self.apply_fs(step, mtime)?;
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
                self.exchange(raw.as_bytes(), head_only, step.read_ms, &step.mask, step.volatile_length)
            }
            _ => {
                let (raw, head_only) = self.build_request(step);
                self.exchange(&raw, head_only, step.read_ms, &step.mask, step.volatile_length)
            }
        }
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
    let mut server = Server {
        binary: binary.to_path_buf(),
        root: root.clone(),
        vault: vault.clone(),
        home,
        secrets: Secrets::new(),
        port: 0,
        child: None,
        rotated: false,
        no_worker: case.no_worker,
        leaked: BTreeSet::new(),
        patterns: patterns(),
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
        server.start()
    })();
    if let Err(error) = setup {
        problems.push(format!("[{}] setup: {error}", case.name));
        let _ = server.stop();
        let _ = fs::remove_dir_all(&root);
        return problems;
    }
    for step in &case.steps {
        let expected = step.expect.clone().unwrap_or_default();
        match server.observe_step(step, mtime) {
            Ok(actual) => {
                compare(&format!("[{}] {}", case.name, step.label), &expected, &actual, &mut problems);
            }
            Err(error) => problems.push(format!("[{}] {}: {error}", case.name, step.label)),
        }
    }
    let exit = server.stop();
    if exit != "exit 0" && exit != "exit 1" {
        problems.push(format!("[{}] server stop: {exit}", case.name));
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
