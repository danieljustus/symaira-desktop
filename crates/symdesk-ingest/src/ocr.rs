//! OCR adapter: tesseract, pdftoppm and sips driven as child processes.
//!
//! Port of Go `internal/ingest/internal/ocr` (`Runner`). The exact argv,
//! working directory, language validation/cache, German re-run, page order,
//! bounded page concurrency, cancellation (child killed), temp-directory
//! cleanup and Go-formatted diagnostics are replayed against the
//! Go-generated `testdata/port/ingest/ocr.json`.

use crate::extract::normalize_text;
use std::collections::BTreeSet;
use std::io::Read;
use std::os::unix::fs::PermissionsExt;
use std::os::unix::process::ExitStatusExt;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use symdesk_core::textnorm;

/// Maximum number of concurrent per-page tesseract processes (Go `SetLimit(4)`).
const PAGE_CONCURRENCY: usize = 4;

/// Result of an OCR extraction (Go `extract.Result`).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct OcrResult {
    pub text: String,
    pub mime: String,
    pub engine: String,
    pub language: String,
}

/// Source kinds the OCR runner accepts (Go `extract.Kind` MIME strings).
pub const KIND_PNG: &str = "image/png";
pub const KIND_JPEG: &str = "image/jpeg";
pub const KIND_TIFF: &str = "image/tiff";
pub const KIND_WEBP: &str = "image/webp";
pub const KIND_HEIC: &str = "image/heic";
pub const KIND_PDF: &str = "application/pdf";

/// Cancellation/deadline context (Go `context.Context` subset).
#[derive(Clone, Default)]
pub struct Context {
    flags: Vec<Arc<AtomicBool>>,
    deadline: Option<Instant>,
}

impl Context {
    #[must_use]
    pub fn background() -> Self {
        Self::default()
    }

    /// Returns a context that is cancelled by `cancel()` on the returned handle.
    #[must_use]
    pub fn with_cancel(&self) -> (Self, Arc<AtomicBool>) {
        let flag = Arc::new(AtomicBool::new(false));
        let mut child = self.clone();
        child.flags.push(Arc::clone(&flag));
        (child, flag)
    }

    #[must_use]
    pub fn with_timeout(&self, timeout: Duration) -> Self {
        let mut child = self.clone();
        let deadline = Instant::now() + timeout;
        child.deadline = Some(child.deadline.map_or(deadline, |d| d.min(deadline)));
        child
    }

    /// Go `ctx.Err()` text, when done.
    #[must_use]
    pub fn err(&self) -> Option<&'static str> {
        if self.flags.iter().any(|f| f.load(Ordering::SeqCst)) {
            return Some("context canceled");
        }
        if self.deadline.is_some_and(|d| Instant::now() >= d) {
            return Some("context deadline exceeded");
        }
        None
    }
}

#[derive(Default)]
struct LangState {
    /// `langCacheLoaded` / `langCache` (None = list-langs failed, cannot validate).
    available_loaded: bool,
    available: Option<BTreeSet<String>>,
    validated_key: String,
    validated: String,
    validated_err: Option<String>,
    validated_cached: bool,
}

type WarnSink = Arc<dyn Fn(&str) + Send + Sync>;

/// Executes external OCR tools (Go `ocr.Runner`).
pub struct Runner {
    pub tesseract: String,
    pub pdftoppm: String,
    pub sips: String,
    pub ocr_lang: String,
    state: Mutex<LangState>,
    warn: WarnSink,
    temp_root: Option<PathBuf>,
    env: Vec<(String, String)>,
}

fn eprint_sink() -> WarnSink {
    Arc::new(|line: &str| eprint!("{line}"))
}

impl Runner {
    #[must_use]
    pub fn new(tesseract: &str, pdftoppm: &str, sips: &str, ocr_lang: &str) -> Self {
        Self {
            tesseract: tesseract.to_owned(),
            pdftoppm: pdftoppm.to_owned(),
            sips: sips.to_owned(),
            ocr_lang: ocr_lang.to_owned(),
            state: Mutex::new(LangState::default()),
            warn: eprint_sink(),
            temp_root: None,
            env: Vec::new(),
        }
    }

    /// Runner that looks the tools up on `PATH` (Go `DefaultRunner`).
    #[must_use]
    pub fn default_runner(ocr_lang: &str) -> Self {
        Self::new("tesseract", "pdftoppm", "sips", ocr_lang)
    }

    /// Redirects the diagnostics Go writes to stderr (warnings, German re-run).
    #[must_use]
    pub fn with_warn_sink(mut self, sink: impl Fn(&str) + Send + Sync + 'static) -> Self {
        self.warn = Arc::new(sink);
        self
    }

    /// Creates the page/HEIC scratch directories under `root` instead of the
    /// process temporary directory.
    #[must_use]
    pub fn with_temp_root(mut self, root: &Path) -> Self {
        self.temp_root = Some(root.to_path_buf());
        self
    }

    /// Adds an environment variable for every child process.
    #[must_use]
    pub fn with_env(mut self, key: &str, value: &str) -> Self {
        self.env.push((key.to_owned(), value.to_owned()));
        self
    }

    /// Copy with another language; the validation cache is not carried over.
    #[must_use]
    pub fn with_language(&self, ocr_lang: &str) -> Self {
        Self {
            tesseract: self.tesseract.clone(),
            pdftoppm: self.pdftoppm.clone(),
            sips: self.sips.clone(),
            ocr_lang: ocr_lang.to_owned(),
            state: Mutex::new(LangState::default()),
            warn: Arc::clone(&self.warn),
            temp_root: self.temp_root.clone(),
            env: self.env.clone(),
        }
    }

    /// Returns an error if the required tools are missing (images only).
    pub fn available(&self) -> Result<(), String> {
        let path = clean_tool_path("tesseract", &self.tesseract)?;
        look_path(&path)
            .map(drop)
            .map_err(|e| format!("tesseract not found on PATH: {e}"))
    }

    /// Returns an error if the tools for PDF OCR are missing.
    pub fn available_for_pdf(&self) -> Result<(), String> {
        self.available()?;
        let path = clean_tool_path("pdftoppm", &self.pdftoppm)?;
        look_path(&path)
            .map(drop)
            .map_err(|e| format!("pdftoppm not found on PATH: {e}"))
    }

    /// Go `Runner.Extract`.
    pub fn extract(&self, ctx: &Context, path: &str, kind: &str) -> Result<OcrResult, String> {
        match kind {
            KIND_PNG | KIND_JPEG | KIND_TIFF | KIND_WEBP => self.extract_image(ctx, path),
            KIND_HEIC => self.extract_heic(ctx, path),
            KIND_PDF => self.extract_pdf(ctx, path),
            other => Err(format!("ocr: unsupported source kind {}", go_quote(other))),
        }
    }

    fn validate_languages(&self, ctx: &Context) -> Result<String, String> {
        let lang = if self.ocr_lang.is_empty() {
            "eng"
        } else {
            &self.ocr_lang
        };
        self.validate_languages_for(ctx, lang)
    }

    fn validate_languages_for(&self, ctx: &Context, lang: &str) -> Result<String, String> {
        {
            let st = self.state.lock().unwrap();
            if st.validated_cached && st.validated_key == lang {
                return match &st.validated_err {
                    Some(e) => Err(e.clone()),
                    None => Ok(st.validated.clone()),
                };
            }
        }
        let outcome = self.validate_languages_uncached(ctx, lang);
        if ctx.err().is_some() {
            return outcome;
        }
        let mut st = self.state.lock().unwrap();
        st.validated_key = lang.to_owned();
        match &outcome {
            Ok(v) => {
                st.validated = v.clone();
                st.validated_err = None;
            }
            Err(e) => {
                st.validated = String::new();
                st.validated_err = Some(e.clone());
            }
        }
        st.validated_cached = true;
        outcome
    }

    fn validate_languages_uncached(&self, ctx: &Context, lang: &str) -> Result<String, String> {
        let Some(available) = self.available_languages(ctx)? else {
            // list-langs failed: cannot validate, keep the language.
            return Ok(lang.to_owned());
        };
        let mut installed = Vec::new();
        let mut missing = Vec::new();
        for p in lang.split('+') {
            let p = go_trim_space(p);
            if p.is_empty() {
                continue;
            }
            if available.contains(p) {
                installed.push(p.to_owned());
            } else {
                missing.push(p.to_owned());
            }
        }
        if !missing.is_empty() {
            if !installed.is_empty() {
                let fallback = installed.join("+");
                (self.warn)(&format!(
                    "Warning: tesseract language(s) [{}] not installed; falling back to {}\n",
                    missing.join(" "),
                    go_quote(&fallback)
                ));
                return Ok(fallback);
            }
            let list = available.iter().cloned().collect::<Vec<_>>().join(" ");
            return Err(format!(
                "none of the configured OCR languages {} are installed (available: [{list}])",
                go_quote(lang)
            ));
        }
        Ok(lang.to_owned())
    }

    fn available_languages(&self, ctx: &Context) -> Result<Option<BTreeSet<String>>, String> {
        // The lock is held across the process run, as in Go (`langMu`).
        let mut st = self.state.lock().unwrap();
        if st.available_loaded {
            return Ok(st.available.clone());
        }
        let path = clean_tool_path("tesseract", &self.tesseract)?;
        let exec_path =
            look_path(&path).map_err(|e| format!("tesseract not found on PATH: {e}"))?;
        let outcome = run_process(
            ctx,
            &exec_path,
            &["--list-langs"],
            None,
            &self.tesseract,
            &self.env,
        );
        let out = match outcome {
            Ok(o) => o,
            Err(_) => {
                if let Some(e) = ctx.err() {
                    return Err(e.to_owned());
                }
                st.available_loaded = true;
                return Ok(None);
            }
        };
        if !out.success {
            if let Some(e) = ctx.err() {
                return Err(e.to_owned());
            }
            st.available_loaded = true;
            return Ok(None);
        }
        let mut available = BTreeSet::new();
        for line in String::from_utf8_lossy(&out.stdout).split('\n') {
            let line = go_trim_space(line);
            if line.is_empty() || line.contains(':') || line.contains("available") {
                continue;
            }
            available.insert(line.to_owned());
        }
        st.available = Some(available.clone());
        st.available_loaded = true;
        Ok(Some(available))
    }

    fn extract_image(&self, ctx: &Context, path: &str) -> Result<OcrResult, String> {
        self.available()?;
        let mut lang = self.validate_languages(ctx)?;
        // Run from the image directory with a relative name (leptonica quirk).
        let dir = go_dir(path);
        let base = go_base_name(path);
        let out = self
            .run_tool(
                ctx,
                Some(&dir),
                &self.tesseract,
                &["-l", &lang, &base, "stdout"],
            )
            .map_err(|e| format!("tesseract failed: {e}"))?;
        let mut text = normalize_text(&String::from_utf8_lossy(&out));
        if let Some(retry) = self.language_retry(ctx, &lang, &text)
            && let Ok(out2) = self.run_tool(
                ctx,
                Some(&dir),
                &self.tesseract,
                &["-l", &retry, &base, "stdout"],
            )
        {
            text = normalize_text(&String::from_utf8_lossy(&out2));
            lang = retry;
        }
        Ok(OcrResult {
            text,
            mime: "image/ocr".to_owned(),
            engine: "tesseract".to_owned(),
            language: lang,
        })
    }

    /// Language OCR should be retried with when the text is clearly German but
    /// the language has no German model; at most once per document.
    fn language_retry(&self, ctx: &Context, lang: &str, text: &str) -> Option<String> {
        if lang.contains(textnorm::LANG_GERMAN) {
            return None;
        }
        if textnorm::detect_language(text) != textnorm::LANG_GERMAN {
            return None;
        }
        let candidate = format!("{}+{lang}", textnorm::LANG_GERMAN);
        let retry = self.validate_languages_for(ctx, &candidate).ok()?;
        if retry == lang || !retry.contains(textnorm::LANG_GERMAN) {
            return None;
        }
        (self.warn)(&format!(
            "OCR: document language looks German, re-running with {} instead of {}\n",
            go_quote(&retry),
            go_quote(lang)
        ));
        Some(retry)
    }

    fn extract_heic(&self, ctx: &Context, path: &str) -> Result<OcrResult, String> {
        let sips_path = clean_tool_path("sips", &self.sips)?;
        look_path(&sips_path).map_err(|e| {
            format!(
                "sips not found on PATH; HEIC/HEIF OCR requires macOS sips conversion before tesseract: {e}"
            )
        })?;
        let dir = TempDir::new(self.temp_root.as_deref(), "symingest-heic-")?;
        let converted = dir.path().join("converted.png");
        let converted_s = converted.to_string_lossy().into_owned();
        self.run_tool(
            ctx,
            None,
            &self.sips,
            &["-s", "format", "png", path, "--out", &converted_s],
        )
        .map_err(|e| format!("sips HEIC conversion failed: {e}"))?;
        let mut res = self.extract_image(ctx, &converted_s)?;
        "image/heic".clone_into(&mut res.mime);
        "sips+tesseract".clone_into(&mut res.engine);
        Ok(res)
    }

    fn extract_pdf(&self, ctx: &Context, path: &str) -> Result<OcrResult, String> {
        self.available_for_pdf()?;
        let mut lang = self.validate_languages(ctx)?;
        let dir = TempDir::new(self.temp_root.as_deref(), "symingest-pdf-")?;
        let prefix = dir.path().join("page").to_string_lossy().into_owned();
        self.run_tool(
            ctx,
            None,
            &self.pdftoppm,
            &["-png", "-r", "150", path, &prefix],
        )
        .map_err(|e| format!("pdftoppm failed: {e}"))?;
        let entries = std::fs::read_dir(dir.path())
            .map_err(|e| format!("read rendered pages: {}", io_message(&e)))?;
        let mut pages: Vec<String> = entries
            .filter_map(Result::ok)
            .filter(|e| {
                e.file_name()
                    .to_string_lossy()
                    .to_lowercase()
                    .ends_with(".png")
            })
            .map(|e| {
                dir.path()
                    .join(e.file_name())
                    .to_string_lossy()
                    .into_owned()
            })
            .collect();
        pages.sort();
        let raw = self.ocr_pdf_pages(ctx, &pages, &lang)?;
        let mut text = normalize_text(&raw);
        if let Some(retry) = self.language_retry(ctx, &lang, &text)
            && let Ok(raw2) = self.ocr_pdf_pages(ctx, &pages, &retry)
        {
            text = normalize_text(&raw2);
            lang = retry;
        }
        Ok(OcrResult {
            text,
            mime: "application/pdf".to_owned(),
            engine: "pdftoppm+tesseract".to_owned(),
            language: lang,
        })
    }

    /// Runs tesseract over the rendered pages (bounded concurrency, first error
    /// cancels the rest) and joins the page texts in page order.
    fn ocr_pdf_pages(&self, ctx: &Context, pages: &[String], lang: &str) -> Result<String, String> {
        let (group_ctx, cancel) = ctx.with_cancel();
        let next = AtomicU64::new(0);
        let first_err: Mutex<Option<String>> = Mutex::new(None);
        let results: Mutex<Vec<String>> = Mutex::new(vec![String::new(); pages.len()]);
        std::thread::scope(|scope| {
            for _ in 0..PAGE_CONCURRENCY.min(pages.len()) {
                scope.spawn(|| {
                    loop {
                        let i = usize::try_from(next.fetch_add(1, Ordering::SeqCst))
                            .unwrap_or(usize::MAX);
                        let Some(page) = pages.get(i) else { return };
                        match self.run_tool(
                            &group_ctx,
                            None,
                            &self.tesseract,
                            &["-l", lang, page, "stdout"],
                        ) {
                            Ok(out) => {
                                results.lock().unwrap()[i] =
                                    String::from_utf8_lossy(&out).into_owned()
                            }
                            Err(e) => {
                                let mut slot = first_err.lock().unwrap();
                                if slot.is_none() {
                                    *slot = Some(format!(
                                        "tesseract failed for page {}: {e}",
                                        go_base_name(page)
                                    ));
                                }
                                cancel.store(true, Ordering::SeqCst);
                            }
                        }
                    }
                });
            }
        });
        if let Some(e) = first_err.into_inner().unwrap() {
            return Err(e);
        }
        let mut sb = String::new();
        for text in results.into_inner().unwrap() {
            sb.push_str(&text);
            sb.push('\n');
        }
        Ok(sb)
    }

    /// Go `runToolInDir`: captured stdout; error text prefixed with the tool's base name.
    fn run_tool(
        &self,
        ctx: &Context,
        dir: Option<&str>,
        name: &str,
        args: &[&str],
    ) -> Result<Vec<u8>, String> {
        let base = go_base_name(name);
        let exec = if name.contains('/') {
            name.to_owned()
        } else {
            match look_path(name) {
                Ok(p) => p,
                Err(e) => return Err(format!("{base}: {e}")),
            }
        };
        match run_process(ctx, &exec, args, dir, name, &self.env) {
            Ok(o) if o.success => Ok(o.stdout),
            Ok(o) => {
                let msg = String::from_utf8_lossy(&o.stderr).trim().to_owned();
                let msg = if msg.is_empty() { o.status_text } else { msg };
                Err(format!("{base}: {msg}"))
            }
            Err(e) => Err(format!("{base}: {e}")),
        }
    }
}

struct ProcessOutput {
    success: bool,
    status_text: String,
    stdout: Vec<u8>,
    stderr: Vec<u8>,
}

/// Starts `exec` and waits for it; the child is killed when `ctx` is done.
/// `Err` carries the Go-formatted start error (or the context error).
fn run_process(
    ctx: &Context,
    exec: &str,
    args: &[&str],
    dir: Option<&str>,
    orig_name: &str,
    env: &[(String, String)],
) -> Result<ProcessOutput, String> {
    if let Some(e) = ctx.err() {
        return Err(e.to_owned());
    }
    // libc `posix_spawn` falls back to `/bin/sh` for files the kernel rejects
    // with ENOEXEC; Go (and the kernel) report "exec format error" instead.
    if lacks_exec_format(exec) {
        return Err(format!("fork/exec {orig_name}: exec format error"));
    }
    let mut cmd = Command::new(exec);
    cmd.args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    for (k, v) in env {
        cmd.env(k, v);
    }
    if let Some(d) = dir {
        cmd.current_dir(d);
    }
    let mut child = cmd
        .spawn()
        .map_err(|e| format!("fork/exec {orig_name}: {}", io_message(&e)))?;
    let mut so = child.stdout.take().unwrap();
    let mut se = child.stderr.take().unwrap();
    let t_out = std::thread::spawn(move || {
        let mut b = Vec::new();
        let _ = so.read_to_end(&mut b);
        b
    });
    let t_err = std::thread::spawn(move || {
        let mut b = Vec::new();
        let _ = se.read_to_end(&mut b);
        b
    });
    let status = loop {
        match child.try_wait() {
            Ok(Some(s)) => break s,
            Ok(None) => {
                if ctx.err().is_some() {
                    let _ = child.kill();
                    break child.wait().map_err(|e| io_message(&e))?;
                }
                std::thread::sleep(Duration::from_millis(2));
            }
            Err(e) => return Err(io_message(&e)),
        }
    };
    let stdout = t_out.join().unwrap_or_default();
    let stderr = t_err.join().unwrap_or_default();
    Ok(ProcessOutput {
        success: status.success(),
        status_text: exit_status_text(&status),
        stdout,
        stderr,
    })
}

/// True when the file is readable but neither a `#!` script nor a native
/// (ELF/Mach-O) image, i.e. `execve` would fail with ENOEXEC.
fn lacks_exec_format(path: &str) -> bool {
    let mut magic = [0_u8; 4];
    let Ok(mut f) = std::fs::File::open(path) else {
        return false;
    };
    let Ok(n) = f.read(&mut magic) else {
        return false;
    };
    let m = &magic[..n];
    if m.starts_with(b"#!") || m.starts_with(&[0x7f, b'E', b'L', b'F']) {
        return false;
    }
    let word = if n == 4 { u32::from_be_bytes(magic) } else { 0 };
    !matches!(
        word,
        0xfeed_face | 0xfeed_facf | 0xcefa_edfe | 0xcffa_edfe | 0xcafe_babe | 0xbebafeca
    )
}

/// Go `ExitError.Error()`.
fn exit_status_text(status: &std::process::ExitStatus) -> String {
    if let Some(sig) = status.signal() {
        return format!("signal: {}", go_signal_name(sig));
    }
    format!("exit status {}", status.code().unwrap_or(-1))
}

fn go_signal_name(sig: i32) -> String {
    match sig {
        1 => "hangup",
        2 => "interrupt",
        3 => "quit",
        4 => "illegal instruction",
        5 => "trace/breakpoint trap",
        6 => "aborted",
        7 => "bus error",
        8 => "floating point exception",
        9 => "killed",
        10 => "user defined signal 1",
        11 => "segmentation fault",
        12 => "user defined signal 2",
        13 => "broken pipe",
        14 => "alarm clock",
        15 => "terminated",
        other => return format!("signal {other}"),
    }
    .to_owned()
}

/// Go syscall error text for the errnos that process start can produce.
fn io_message(e: &std::io::Error) -> String {
    match e.raw_os_error() {
        Some(1) => "operation not permitted".into(),
        Some(2) => "no such file or directory".into(),
        Some(7) => "argument list too long".into(),
        Some(8) => "exec format error".into(),
        Some(12) => "cannot allocate memory".into(),
        Some(13) => "permission denied".into(),
        Some(20) => "not a directory".into(),
        Some(21) => "is a directory".into(),
        Some(24) => "too many open files".into(),
        Some(26) => "text file busy".into(),
        Some(36) => "file name too long".into(),
        _ => {
            let s = e.to_string();
            match s.find(" (os error") {
                Some(i) => s[..i].to_lowercase(),
                None => s,
            }
        }
    }
}

/// Go `cleanToolPath`.
fn clean_tool_path(name: &str, path: &str) -> Result<String, String> {
    let cleaned = go_clean(path);
    if cleaned.is_empty() || cleaned == "." {
        return Err(format!("{name} command is not configured"));
    }
    Ok(cleaned)
}

/// Go `filepath.Clean` for unix paths.
#[must_use]
pub fn go_clean(path: &str) -> String {
    if path.is_empty() {
        return ".".to_owned();
    }
    let rooted = path.starts_with('/');
    let mut out: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if out.last().is_some_and(|l| *l != "..") {
                    out.pop();
                } else if !rooted {
                    out.push("..");
                }
            }
            p => out.push(p),
        }
    }
    let joined = out.join("/");
    match (rooted, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".to_owned(),
        (false, false) => joined,
    }
}

fn go_dir(path: &str) -> String {
    match path.rfind('/') {
        None => ".".to_owned(),
        Some(i) => go_clean(&path[..=i]),
    }
}

fn go_base_name(path: &str) -> String {
    if path.is_empty() {
        return ".".to_owned();
    }
    let trimmed = path.trim_end_matches('/');
    if trimmed.is_empty() {
        return "/".to_owned();
    }
    trimmed.rsplit('/').next().unwrap_or(trimmed).to_owned()
}

/// Go `strings.TrimSpace` (unicode whitespace).
fn go_trim_space(s: &str) -> &str {
    s.trim_matches(|c: char| c.is_whitespace() || c == '\u{85}')
}

/// Go `%q` for strings (printable runes kept, others escaped).
fn go_quote(s: &str) -> String {
    let mut out = String::from("\"");
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\u{b}' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c as u32 == 0x7f => {
                out.push_str(&format!("\\x{:02x}", c as u32));
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Go `exec.LookPath`: returns the executable path, or the Go error text
/// (`exec: "file": ...`).
pub fn look_path(file: &str) -> Result<String, String> {
    let wrap = |e: &str| format!("exec: {}: {e}", go_quote(file));
    if file.contains('/') {
        return find_executable(file)
            .map(|()| file.to_owned())
            .map_err(|e| wrap(&e));
    }
    let path = std::env::var("PATH").unwrap_or_default();
    for dir in path.split(':') {
        let dir = if dir.is_empty() { "." } else { dir };
        let candidate = format!("{dir}/{file}");
        if find_executable(&candidate).is_ok() {
            if !candidate.starts_with('/') {
                return Err(wrap(
                    "cannot run executable found relative to current directory",
                ));
            }
            return Ok(candidate);
        }
    }
    Err(wrap("executable file not found in $PATH"))
}

fn find_executable(file: &str) -> Result<(), String> {
    let meta = std::fs::metadata(file).map_err(|e| format!("stat {file}: {}", io_message(&e)))?;
    if meta.is_dir() {
        return Err("is a directory".to_owned());
    }
    if meta.permissions().mode() & 0o111 != 0 {
        return Ok(());
    }
    Err("permission denied".to_owned())
}

/// Temporary directory removed on drop (Go `os.MkdirTemp` + `RemoveAll`).
struct TempDir {
    path: PathBuf,
}

static TEMP_COUNTER: AtomicU64 = AtomicU64::new(0);

impl TempDir {
    fn new(root: Option<&Path>, prefix: &str) -> Result<Self, String> {
        let base = root.map_or_else(std::env::temp_dir, Path::to_path_buf);
        for _ in 0..100 {
            let nanos = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .map_or(0, |d| d.subsec_nanos());
            let n = u64::from(nanos)
                .wrapping_mul(2_654_435_761)
                .wrapping_add(TEMP_COUNTER.fetch_add(1, Ordering::SeqCst))
                .wrapping_add(u64::from(std::process::id()) << 20);
            let path = base.join(format!("{prefix}{}", n % 4_000_000_000));
            let mut b = std::fs::DirBuilder::new();
            std::os::unix::fs::DirBuilderExt::mode(&mut b, 0o700);
            match b.create(&path) {
                Ok(()) => return Ok(Self { path }),
                Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => {}
                Err(e) => {
                    return Err(format!(
                        "create temp dir: mkdir {}: {}",
                        path.display(),
                        io_message(&e)
                    ));
                }
            }
        }
        Err("create temp dir: too many attempts".to_owned())
    }

    fn path(&self) -> &Path {
        &self.path
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        if let Err(e) = std::fs::remove_dir_all(&self.path) {
            eprintln!("OCR temporary directory cleanup failed: {e}");
        }
    }
}
