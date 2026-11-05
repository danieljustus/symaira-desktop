//! `symdesk worker`: Go `cmd/symdesk/selfhost.go` + `internal/selfhost/worker.go`.
//! Polls a SymDesk server for OCR jobs, downloads the input, extracts text
//! and reports the result.
//!
//! Lifecycle (CLI-007): SIGINT/SIGTERM cancel the context: the in-flight HTTP
//! request is aborted (the server observes the disconnect), a running OCR
//! child is stopped, the temporary input directory is removed and the command
//! exits 0. A failed poll or job is retried after `--poll` until a signal
//! arrives. Closing stdin does not stop the worker.
//!
//! Declared differences from Go:
//! * Only the Tesseract engine is implemented. `--engine ollama` jobs fail
//!   with an explicit error; the plausibility guard retry is not ported
//!   (HTTP worker routes belong to RUST-015).
//! * Request errors carry the `ureq` transport text, not Go's `net/http` text.

use std::{
    fs,
    io::{Read, Write},
    path::{Path, PathBuf},
    process::ExitCode,
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicUsize, Ordering},
    },
    time::Duration,
};

use clap::{Arg, ArgAction, ArgMatches, Command};
use serde_json::{Value, json};
use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_core::cancel::CancelToken;
use symdesk_ingest::ocr;

use crate::lifecycle;

const MAX_UPLOAD_BYTES: u64 = 100 << 20;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(15 * 60);

pub(crate) fn cli() -> Command {
    Command::new("worker")
        .arg(Arg::new("server").long("server").num_args(1))
        .arg(Arg::new("token").long("token").num_args(1))
        .arg(Arg::new("worker-id").long("worker-id").num_args(1))
        .arg(Arg::new("engine").long("engine").num_args(1))
        .arg(Arg::new("ollama-url").long("ollama-url").num_args(1))
        .arg(Arg::new("ollama-model").long("ollama-model").num_args(1))
        .arg(Arg::new("ocr-language").long("ocr-language").num_args(1))
        .arg(Arg::new("poll").long("poll").num_args(1))
        .arg(Arg::new("once").long("once").action(ArgAction::SetTrue))
}

struct Config {
    server_url: String,
    token: String,
    worker_id: String,
    engine: String,
    ollama_model: String,
    ocr_language: String,
    poll: Duration,
    once: bool,
}

fn environment_or(key: &str, fallback: &str) -> String {
    std::env::var(key)
        .ok()
        .filter(|value| !value.is_empty())
        .unwrap_or_else(|| fallback.to_owned())
}

fn flag(matches: &ArgMatches, name: &str, environment: &str, fallback: &str) -> String {
    matches
        .get_one::<String>(name)
        .cloned()
        .unwrap_or_else(|| environment_or(environment, fallback))
}

/// Go `isBareHTTPURL` over the `strings.TrimRight(.., "/")`-ed value.
fn bare_http_url(raw: &str) -> Option<String> {
    let trimmed = raw.trim_end_matches('/');
    let (scheme, rest) = trimmed.split_once("://")?;
    if !matches!(scheme, "http" | "https") || rest.is_empty() {
        return None;
    }
    if rest.contains(['/', '?', '#', '@']) || rest.chars().any(char::is_whitespace) {
        return None;
    }
    Some(trimmed.to_owned())
}

fn goos() -> &'static str {
    match std::env::consts::OS {
        "macos" => "darwin",
        other => other,
    }
}

fn goarch() -> &'static str {
    match std::env::consts::ARCH {
        "aarch64" => "arm64",
        "x86_64" => "amd64",
        other => other,
    }
}

/// Go `selfhost.NewWorker` validation and defaults.
fn configure(matches: &ArgMatches) -> Result<Config, String> {
    let server = flag(matches, "server", "SYMDESK_SERVER_URL", "");
    let server_url = bare_http_url(&server)
        .ok_or_else(|| "server must be a bare http(s) base URL".to_owned())?;
    let token = flag(matches, "token", "SYMDESK_SERVER_TOKEN", "");
    if token.len() < 32 {
        return Err("server token must contain at least 32 characters".to_owned());
    }
    let mut worker_id = flag(matches, "worker-id", "SYMDESK_WORKER_ID", "");
    if worker_id.is_empty() {
        worker_id = format!("{}-{}-{}", lifecycle::hostname(), goos(), goarch());
    }
    let engine = flag(matches, "engine", "SYMDESK_WORKER_ENGINE", "auto");
    let ollama = flag(
        matches,
        "ollama-url",
        "OLLAMA_HOST",
        "http://127.0.0.1:11434",
    );
    if bare_http_url(&ollama).is_none() {
        return Err("ollama host must be a bare http(s) base URL".to_owned());
    }
    let ocr_language = flag(matches, "ocr-language", "SYMDESK_OCR_LANGUAGE", "deu+eng");
    let poll = match matches.get_one::<String>("poll") {
        Some(text) => parse_duration(text).ok_or_else(|| {
            format!(
                "invalid argument {text:?} for \"--poll\" flag: time: invalid duration {text:?}"
            )
        })?,
        None => Duration::from_secs(5),
    };
    Ok(Config {
        server_url,
        token,
        worker_id,
        engine,
        ollama_model: flag(matches, "ollama-model", "SYMDESK_OLLAMA_MODEL", ""),
        ocr_language,
        poll: if poll.is_zero() {
            Duration::from_secs(5)
        } else {
            poll
        },
        once: matches.get_flag("once"),
    })
}

/// Go `time.ParseDuration` for the `--poll` flag; negative values clamp to zero.
fn parse_duration(input: &str) -> Option<Duration> {
    let (negative, mut rest) = match input.as_bytes().first()? {
        b'-' => (true, &input[1..]),
        b'+' => (false, &input[1..]),
        _ => (false, input),
    };
    if rest == "0" {
        return Some(Duration::ZERO);
    }
    if rest.is_empty() {
        return None;
    }
    let mut total = 0.0_f64;
    while !rest.is_empty() {
        let number_end = rest
            .find(|c: char| !(c.is_ascii_digit() || c == '.'))
            .unwrap_or(rest.len());
        let number: f64 = rest[..number_end].parse().ok()?;
        rest = &rest[number_end..];
        let unit_end = rest
            .find(|c: char| c.is_ascii_digit() || c == '.')
            .unwrap_or(rest.len());
        let scale = match &rest[..unit_end] {
            "ns" => 1.0,
            "us" | "\u{b5}s" | "\u{3bc}s" => 1e3,
            "ms" => 1e6,
            "s" => 1e9,
            "m" => 60e9,
            "h" => 3600e9,
            _ => return None,
        };
        total += number * scale;
        rest = &rest[unit_end..];
    }
    if negative || !total.is_finite() {
        return Some(Duration::ZERO);
    }
    Some(Duration::from_nanos(total as u64))
}

struct Job {
    id: String,
    original_name: String,
    content_type: String,
}

enum Lease {
    Job(Job),
    None,
}

struct Worker {
    config: Config,
    agent: ureq::Agent,
    cancel: CancelToken,
    context: ocr::Context,
}

fn response_error(status: u16, body: &[u8]) -> String {
    if let Ok(Value::Object(object)) = serde_json::from_slice::<Value>(body)
        && let Some(Value::String(message)) = object.get("error")
        && !message.is_empty()
    {
        return message.clone();
    }
    format!("HTTP {status}: {}", String::from_utf8_lossy(body).trim())
}

impl Worker {
    fn request(
        &self,
        method: &str,
        path: &str,
        body: Option<&Value>,
    ) -> Result<(u16, ureq::Body), String> {
        let url = format!("{}{path}", self.config.server_url);
        let bearer = format!("Bearer {}", self.config.token);
        let result = if let Some(body) = body {
            self.agent
                .post(&url)
                .header("Authorization", &bearer)
                .header("Content-Type", "application/json")
                .send(body.to_string().as_bytes())
        } else {
            self.agent.get(&url).header("Authorization", &bearer).call()
        };
        match result {
            Ok(response) => {
                let (parts, body) = response.into_parts();
                Ok((parts.status.as_u16(), body))
            }
            Err(error) => {
                if self.cancel.is_cancelled() {
                    return Err("context canceled".to_owned());
                }
                Err(format!("{method} {url:?}: {error}"))
            }
        }
    }

    fn read_all(mut body: ureq::Body, limit: u64) -> Result<Vec<u8>, String> {
        let mut data = Vec::new();
        body.as_reader()
            .take(limit)
            .read_to_end(&mut data)
            .map_err(|error| error.to_string())?;
        Ok(data)
    }

    fn lease(&self) -> Result<Lease, String> {
        let body = json!({"capabilities": ["ocr"], "worker_id": self.config.worker_id});
        let (status, response) = self.request("POST", "/api/v1/worker/lease", Some(&body))?;
        match status {
            204 => Ok(Lease::None),
            200 => {
                let data = Self::read_all(response, 1 << 20)?;
                let value: Value =
                    serde_json::from_slice(&data).map_err(|error| error.to_string())?;
                let text = |key: &str| {
                    value
                        .get(key)
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_owned()
                };
                Ok(Lease::Job(Job {
                    id: text("id"),
                    original_name: text("original_name"),
                    content_type: text("content_type"),
                }))
            }
            other => Err(response_error(other, &Self::read_all(response, 1 << 20)?)),
        }
    }

    fn download(&self, job: &Job) -> Result<(PathBuf, TempDir), String> {
        let path = format!("/api/v1/worker/input?id={}", query_escape(&job.id));
        let (status, response) = self.request("GET", &path, None)?;
        if status != 200 {
            return Err(response_error(status, &Self::read_all(response, 1 << 20)?));
        }
        let directory = TempDir::create()?;
        let extension = Path::new(&job.original_name)
            .extension()
            .map(|ext| format!(".{}", ext.to_string_lossy()))
            .or_else(|| extension_for(&job.content_type))
            .unwrap_or_default();
        let input = directory.path.join(format!("input{extension}"));
        let mut file = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&input)
            .map_err(|error| error.to_string())?;
        let mut body = response;
        let copied = std::io::copy(&mut body.as_reader().take(MAX_UPLOAD_BYTES + 1), &mut file)
            .map_err(|error| error.to_string())?;
        file.flush().map_err(|error| error.to_string())?;
        if copied > MAX_UPLOAD_BYTES {
            return Err("worker input exceeds 100 MiB".to_owned());
        }
        Ok((input, directory))
    }

    fn engine_mode(&self) -> Result<&'static str, String> {
        let mode = self.config.engine.trim().to_lowercase();
        match mode.as_str() {
            "" | "auto" => Ok(if self.config.ollama_model.is_empty() {
                "tesseract"
            } else {
                "ollama"
            }),
            "tesseract" => Ok("tesseract"),
            "ollama" if self.config.ollama_model.is_empty() => {
                Err("--ollama-model is required for the Ollama engine".to_owned())
            }
            "ollama" => Ok("ollama"),
            _ => Err(format!(
                "unsupported OCR engine {:?}; supported engines are tesseract and ollama",
                self.config.engine
            )),
        }
    }

    fn process(&self, input: &Path) -> Result<(String, String, String), String> {
        let mode = self.engine_mode()?;
        if mode == "ollama" {
            return Err(
                "OCR failed: the Ollama OCR engine is not available in this build".to_owned(),
            );
        }
        let kind = match input
            .extension()
            .map(|ext| ext.to_string_lossy().to_lowercase())
            .as_deref()
        {
            Some("png") => ocr::KIND_PNG,
            Some("jpg" | "jpeg") => ocr::KIND_JPEG,
            Some("tif" | "tiff") => ocr::KIND_TIFF,
            Some("webp") => ocr::KIND_WEBP,
            Some("heic") => ocr::KIND_HEIC,
            Some("pdf") => ocr::KIND_PDF,
            _ => "",
        };
        let runner = ocr::Runner::default_runner(&self.config.ocr_language);
        let result = runner
            .extract(&self.context, &input.to_string_lossy(), kind)
            .map_err(|error| format!("OCR failed: extract text: {error}"))?;
        let engine = result.engine.trim();
        let engine = if engine.is_empty() { mode } else { engine };
        Ok((result.text, engine.to_owned(), String::new()))
    }

    fn report(&self, path: &str, body: &Value) -> Result<(), String> {
        let (status, response) = self.request("POST", path, Some(body))?;
        if status != 200 {
            return Err(response_error(status, &Self::read_all(response, 1 << 20)?));
        }
        Ok(())
    }

    /// Go `RunOnce`: lease one job and process it. `Ok(false)` means no job.
    fn run_once(&self) -> Result<bool, String> {
        let job = match self.lease()? {
            Lease::None => return Ok(false),
            Lease::Job(job) => job,
        };
        let id = &self.config.worker_id;
        let (input, _directory) = match self.download(&job) {
            Ok(downloaded) => downloaded,
            Err(error) => {
                let _ = self.report(
                    "/api/v1/worker/fail",
                    &json!({"error": error, "job_id": job.id, "retry": true, "worker_id": id}),
                );
                return Err(error);
            }
        };
        let (text, engine, model) = match self.process(&input) {
            Ok(processed) => processed,
            Err(error) => {
                let _ = self.report(
                    "/api/v1/worker/fail",
                    &json!({"error": error, "job_id": job.id, "retry": false, "worker_id": id}),
                );
                return Err(error);
            }
        };
        self.report(
            "/api/v1/worker/complete",
            &json!({"engine": engine, "job_id": job.id, "model": model, "text": text, "worker_id": id}),
        )?;
        Ok(true)
    }

    /// Go `Run`: poll until a signal arrives (exit 0) or, with `--once`, one
    /// attempt has finished.
    fn run(&self) -> Result<(), String> {
        loop {
            if lifecycle::shutdown_requested() {
                return Ok(());
            }
            match self.run_once() {
                Err(error) => {
                    if lifecycle::shutdown_requested() {
                        return Ok(());
                    }
                    if self.config.once {
                        return Err(error);
                    }
                }
                Ok(processed) => {
                    if self.config.once {
                        return Ok(());
                    }
                    if processed {
                        continue;
                    }
                }
            }
            if lifecycle::wait_for_shutdown(self.config.poll) {
                return Ok(());
            }
        }
    }
}

/// A temporary directory removed on drop (Go `os.RemoveAll(dir)` cleanup).
struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn create() -> Result<Self, String> {
        static COUNTER: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "symdesk-worker-{}-{}",
            std::process::id(),
            COUNTER.fetch_add(1, Ordering::SeqCst)
        ));
        let mut builder = fs::DirBuilder::new();
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            builder.mode(0o700);
        }
        builder.create(&path).map_err(|error| error.to_string())?;
        Ok(Self { path })
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}

fn extension_for(content_type: &str) -> Option<String> {
    let essence = content_type.split(';').next().unwrap_or_default().trim();
    let extension = match essence {
        "image/png" => ".png",
        "image/jpeg" => ".jpg",
        "image/tiff" => ".tiff",
        "image/webp" => ".webp",
        "image/heic" => ".heic",
        "application/pdf" => ".pdf",
        _ => return None,
    };
    Some(extension.to_owned())
}

/// Go `url.QueryEscape`.
fn query_escape(value: &str) -> String {
    let mut out = String::new();
    for byte in value.bytes() {
        match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(char::from(byte));
            }
            b' ' => out.push('+'),
            other => out.push_str(&format!("%{other:02X}")),
        }
    }
    out
}

pub(crate) fn run(matches: &ArgMatches) -> ExitCode {
    let config = match configure(matches) {
        Ok(config) => config,
        Err(error) => return crate::emit_error(error, false),
    };
    if let Err(error) = lifecycle::install_shutdown_handlers() {
        return crate::emit_error(error.to_string(), false);
    }
    let cancel = CancelToken::new();
    let (context, ocr_cancel) = ocr::Context::background().with_cancel();
    let agent_config = ureq::Agent::config_builder()
        .http_status_as_error(false)
        .timeout_global(Some(REQUEST_TIMEOUT))
        // One connection per request: a poll every few seconds gains nothing
        // from reuse and a server-closed idle connection must not fail a job.
        .max_idle_connections(0)
        .build();
    let worker = Worker {
        agent: cancel.agent(agent_config),
        cancel: cancel.clone(),
        context,
        config,
    };
    // Go cancels the signal context: abort the in-flight request and stop a
    // running OCR child.
    std::thread::spawn(move || {
        while !lifecycle::wait_for_shutdown(Duration::from_secs(3600)) {}
        cancel.cancel();
        ocr_cancel.store(true, Ordering::SeqCst);
    });
    match worker.run() {
        Ok(()) => crate::process_exit(CoreExitCode::Ok),
        Err(error) => crate::emit_error(format!("worker: {error}"), false),
    }
}

/// Inputs of the worker `serve --local-worker` runs inside the server process
/// (Go `selfhost.WorkerConfig` with `WorkerID: "server-local-worker"`).
pub(crate) struct LocalWorkerSettings {
    pub server_url: String,
    pub token: String,
    pub engine: String,
    pub ollama_url: String,
    pub ollama_model: String,
    pub ocr_language: String,
}

/// A running in-process worker; [`LocalWorker::stop`] mirrors cancelling Go's
/// signal context.
pub(crate) struct LocalWorker {
    stop: Arc<AtomicBool>,
    cancel: CancelToken,
    ocr_cancel: Arc<AtomicBool>,
}

impl LocalWorker {
    pub(crate) fn stop(&self) {
        self.stop.store(true, Ordering::SeqCst);
        self.cancel.cancel();
        self.ocr_cancel.store(true, Ordering::SeqCst);
    }
}

/// Go `selfhost.NewWorker` validation followed by `worker.Run` on a thread.
pub(crate) fn spawn_local(settings: LocalWorkerSettings) -> Result<LocalWorker, String> {
    let server_url = bare_http_url(&settings.server_url)
        .ok_or_else(|| "server must be a bare http(s) base URL".to_owned())?;
    if settings.token.len() < 32 {
        return Err("server token must contain at least 32 characters".to_owned());
    }
    let ollama = if settings.ollama_url.is_empty() {
        "http://127.0.0.1:11434"
    } else {
        settings.ollama_url.as_str()
    };
    if bare_http_url(ollama).is_none() {
        return Err("ollama host must be a bare http(s) base URL".to_owned());
    }
    let config = Config {
        server_url,
        token: settings.token,
        worker_id: "server-local-worker".to_owned(),
        engine: if settings.engine.is_empty() {
            "auto".to_owned()
        } else {
            settings.engine
        },
        ollama_model: settings.ollama_model,
        ocr_language: if settings.ocr_language.is_empty() {
            "deu+eng".to_owned()
        } else {
            settings.ocr_language
        },
        poll: Duration::from_secs(5),
        once: false,
    };
    let cancel = CancelToken::new();
    let (context, ocr_cancel) = ocr::Context::background().with_cancel();
    let agent_config = ureq::Agent::config_builder()
        .http_status_as_error(false)
        .timeout_global(Some(REQUEST_TIMEOUT))
        .max_idle_connections(0)
        .build();
    let worker = Worker {
        agent: cancel.agent(agent_config),
        cancel: cancel.clone(),
        context,
        config,
    };
    let stop = Arc::new(AtomicBool::new(false));
    let handle = LocalWorker {
        stop: Arc::clone(&stop),
        cancel,
        ocr_cancel,
    };
    std::thread::spawn(move || {
        while !stop.load(Ordering::SeqCst) {
            if matches!(worker.run_once(), Ok(true)) {
                continue;
            }
            // Go waits PollEvery after an empty poll or a failure.
            let mut waited = Duration::ZERO;
            while waited < worker.config.poll && !stop.load(Ordering::SeqCst) {
                std::thread::sleep(Duration::from_millis(50));
                waited += Duration::from_millis(50);
            }
        }
    });
    Ok(handle)
}
