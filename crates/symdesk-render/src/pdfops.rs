//! Page-level Poppler/qpdf adapter, matching `internal/ingest/internal/pdfops`.
//! Tools are resolved per operation. No shell is involved. Inputs and outputs
//! are caller-selected local paths; this is not a vault-confinement boundary.
use std::{
    collections::BTreeSet,
    fs::{self, File},
    io::{self, Read},
    path::{Component, Path, PathBuf},
    process::{Command, Stdio},
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

/// Cooperative cancellation shared by all subprocesses in one PDF operation.
#[derive(Clone, Default)]
pub struct Context {
    cancelled: Arc<AtomicBool>,
    pub deadline: Option<Instant>,
}
impl Context {
    pub fn cancel(&self) {
        self.cancelled.store(true, Ordering::Release);
    }
    fn error(&self) -> Option<&'static str> {
        if self.cancelled.load(Ordering::Acquire) {
            Some("context canceled")
        } else if self.deadline.is_some_and(|at| Instant::now() >= at) {
            Some("context deadline exceeded")
        } else {
            None
        }
    }
}

#[derive(Clone, Debug)]
pub struct Tools {
    pub pdfinfo: String,
    pub pdfseparate: String,
    pub pdfunite: String,
    pub qpdf: String,
}
impl Default for Tools {
    fn default() -> Self {
        Self {
            pdfinfo: "pdfinfo".into(),
            pdfseparate: "pdfseparate".into(),
            pdfunite: "pdfunite".into(),
            qpdf: "qpdf".into(),
        }
    }
}

/// Go's sorted, duplicate-rejecting one-based selector.
pub fn parse_pages(spec: &str) -> Result<Vec<i64>, String> {
    if spec.trim().is_empty() {
        return Err("page selector is empty".into());
    }
    let mut pages = BTreeSet::new();
    for raw in spec.split(',') {
        let part = raw.trim();
        if part.is_empty() {
            return Err(format!("invalid empty page selector in {}", quote(spec)));
        }
        let bounds: Vec<_> = part.split('-').collect();
        if bounds.len() > 2 {
            return Err(format!("invalid page range {}", quote(part)));
        }
        let start =
            positive(bounds[0]).ok_or_else(|| format!("invalid page number {}", quote(part)))?;
        let end = if bounds.len() == 2 {
            positive(bounds[1])
                .filter(|end| *end >= start)
                .ok_or_else(|| format!("invalid page range {}", quote(part)))?
        } else {
            start
        };
        // RENDER-PDF-LIMIT-1: fail closed instead of allocating an attacker-sized
        // selector (or wrapping Go's MaxInt loop). Ordinary selectors are exact.
        if end - start >= 1_000_000 || pages.len() as i64 + end - start + 1 > 1_000_000 {
            return Err("page selector exceeds 1000000 pages".into());
        }
        for page in start..=end {
            if !pages.insert(page) {
                return Err(format!("page {page} appears more than once"));
            }
        }
    }
    Ok(pages.into_iter().collect())
}
fn positive(value: &str) -> Option<i64> {
    value.trim().parse::<i64>().ok().filter(|n| *n > 0)
}

impl Tools {
    pub fn split(
        &self,
        ctx: &Context,
        input: &str,
        at: &str,
        output_dir: &str,
    ) -> Result<Vec<String>, String> {
        let pages = self.page_count(ctx, input)?;
        let boundaries = parse_pages(at)?;
        for boundary in &boundaries {
            if *boundary >= pages {
                return Err(format!(
                    "split page {boundary} is outside the document; document has {pages} pages"
                ));
            }
        }
        mkdir_all(Path::new(output_dir))
            .map_err(|e| format!("create split output directory: {e}"))?;
        let temp = Workspace::new("symingest-pdf-split-")
            .map_err(|e| format!("create PDF split workspace: {e}"))?;
        let mut page_paths = Vec::new();
        for page in 1..=pages {
            let pattern = text(&temp.0.join("page-%d.pdf"));
            self.run(
                ctx,
                &self.pdfseparate,
                &[
                    "-f".into(),
                    page.to_string(),
                    "-l".into(),
                    page.to_string(),
                    input.into(),
                    pattern,
                ],
            )
            .map_err(|e| format!("split page {page}: {e}"))?;
            let path = text(&temp.0.join(format!("page-{page}.pdf")));
            fs::metadata(&path).map_err(|e| {
                format!(
                    "split page {page} did not produce a PDF: {}",
                    path_error("stat", &path, e)
                )
            })?;
            page_paths.push(path);
        }
        let mut start = 0;
        let mut outputs = Vec::new();
        for (index, end) in boundaries.into_iter().chain([pages]).enumerate() {
            let output = text(&clean(
                Path::new(output_dir).join(format!("part-{:03}.pdf", index + 1)),
            ));
            let mut args = page_paths[start..end as usize].to_vec();
            args.push(output.clone());
            self.run(ctx, &self.pdfunite, &args)
                .map_err(|e| format!("merge split part {}: {e}", index + 1))?;
            outputs.push(output);
            start = end as usize;
        }
        Ok(outputs)
    }
    pub fn merge(&self, ctx: &Context, inputs: &[String], output: &str) -> Result<(), String> {
        if inputs.len() < 2 {
            return Err("merge requires at least two input PDFs".into());
        }
        for input in inputs {
            require_file(input).map_err(|e| format!("merge input {input}: {e}"))?;
        }
        mkdir_all(&parent(output)).map_err(|e| format!("create merge output directory: {e}"))?;
        let mut args = inputs.to_vec();
        args.push(output.into());
        self.run(ctx, &self.pdfunite, &args)
            .map_err(|e| format!("merge PDFs: {e}"))?;
        require_file(output)
    }
    pub fn rotate(
        &self,
        ctx: &Context,
        input: &str,
        output: &str,
        degrees: i64,
        pages: &str,
    ) -> Result<(), String> {
        require_file(input).map_err(|e| format!("rotate input: {e}"))?;
        if ![-270, -180, -90, 90, 180, 270].contains(&degrees) {
            return Err("rotation must be one of -270, -180, -90, 90, 180, 270".into());
        }
        if !pages.is_empty() {
            parse_pages(pages)?;
        }
        mkdir_all(&parent(output)).map_err(|e| format!("create rotation output directory: {e}"))?;
        let selector = if pages.is_empty() {
            format!("{degrees:+}")
        } else {
            format!("{degrees:+}:{pages}")
        };
        self.run(
            ctx,
            &self.qpdf,
            &[
                "--warning-exit-0".into(),
                input.into(),
                output.into(),
                format!("--rotate={selector}"),
            ],
        )
        .map_err(|e| format!("rotate PDF (qpdf is required): {e}"))?;
        require_file(output)
    }
    fn page_count(&self, ctx: &Context, input: &str) -> Result<i64, String> {
        require_file(input).map_err(|e| format!("split input: {e}"))?;
        let path = tool_path(&self.pdfinfo, "pdfinfo")?;
        let stdout = execute(ctx, &path, &[input.into()])?;
        for line in stdout.split('\n') {
            if let Some((key, value)) = line.split_once(':')
                && key.trim() == "Pages"
                && let Some(pages) = positive(value)
            {
                return Ok(pages);
            }
        }
        Err(format!(
            "pdfinfo did not report a positive page count for {input}"
        ))
    }
    fn run(&self, ctx: &Context, name: &str, args: &[String]) -> Result<(), String> {
        let path = tool_path(name, &base(name))?;
        execute(ctx, &path, args).map(|_| ())
    }
}

/// Public facade mirrors ingest's lexical absolute-path conversion, without
/// resolving symlinks or requiring outputs to exist.
pub fn split_pdf_at_spec(
    ctx: &Context,
    tools: &Tools,
    input: &str,
    at: &str,
    output: &str,
) -> Result<Vec<String>, String> {
    let input = absolute(input, "split input")?;
    let output = absolute(output, "split output directory")?;
    tools.split(ctx, &input, at, &output)
}
pub fn merge_pdfs(
    ctx: &Context,
    tools: &Tools,
    inputs: &[String],
    output: &str,
) -> Result<(), String> {
    let inputs = inputs
        .iter()
        .map(|p| absolute(p, "merge input"))
        .collect::<Result<Vec<_>, _>>()?;
    tools.merge(ctx, &inputs, &absolute(output, "merge output")?)
}
pub fn rotate_pdf(
    ctx: &Context,
    tools: &Tools,
    input: &str,
    output: &str,
    degrees: i64,
    pages: &str,
) -> Result<(), String> {
    let input = absolute(input, "rotate input")?;
    tools.rotate(
        ctx,
        &input,
        &absolute(output, "rotate output")?,
        degrees,
        pages,
    )
}
fn absolute(path: &str, label: &str) -> Result<String, String> {
    let path_buf = Path::new(path);
    let joined = if path_buf.is_absolute() {
        path_buf.to_path_buf()
    } else {
        std::env::current_dir()
            .map_err(|e| format!("invalid {label} {}: {e}", quote(path)))?
            .join(path_buf)
    };
    Ok(text(&clean(joined)))
}
fn clean(path: PathBuf) -> PathBuf {
    let mut result = PathBuf::new();
    for part in path.components() {
        match part {
            Component::CurDir => {}
            Component::ParentDir if result.file_name().is_some_and(|n| n != "..") => {
                result.pop();
            }
            Component::ParentDir if result.has_root() => {}
            part => result.push(part.as_os_str()),
        }
    }
    if result.as_os_str().is_empty() {
        result.push(".");
    }
    result
}
fn text(path: &Path) -> String {
    path.to_string_lossy().into_owned()
}
fn parent(path: &str) -> PathBuf {
    clean(
        Path::new(path)
            .parent()
            .filter(|p| !p.as_os_str().is_empty())
            .unwrap_or(Path::new("."))
            .to_path_buf(),
    )
}
fn base(path: &str) -> String {
    clean(PathBuf::from(path))
        .file_name()
        .map_or_else(|| ".".into(), |n| n.to_string_lossy().into_owned())
}
fn require_file(path: &str) -> Result<(), String> {
    let metadata = fs::metadata(path).map_err(|e| path_error("stat", path, e))?;
    if metadata.is_dir() {
        return Err(format!("{path} is a directory"));
    }
    Ok(())
}
fn path_error(op: &str, path: &str, error: io::Error) -> String {
    let message = match error.kind() {
        io::ErrorKind::NotFound => "no such file or directory".into(),
        io::ErrorKind::PermissionDenied => "permission denied".into(),
        io::ErrorKind::AlreadyExists => "file exists".into(),
        io::ErrorKind::NotADirectory => "not a directory".into(),
        _ => error.to_string(),
    };
    format!("{op} {path}: {message}")
}
fn mkdir_all(path: &Path) -> Result<(), String> {
    if let Ok(metadata) = fs::metadata(path) {
        return if metadata.is_dir() {
            Ok(())
        } else {
            Err(format!("mkdir {}: not a directory", text(path)))
        };
    }
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder
        .create(path)
        .map_err(|e| path_error("mkdir", &text(path), e))
}
fn executable(path: &Path) -> bool {
    fs::metadata(path).is_ok_and(|m| {
        if m.is_dir() {
            return false;
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            m.permissions().mode() & 0o111 != 0
        }
        #[cfg(not(unix))]
        {
            true
        }
    })
}
pub(crate) fn tool_path(name: &str, label: &str) -> Result<PathBuf, String> {
    if name.trim().is_empty() {
        return Err(format!(
            "{label} is not configured; install the required PDF tool"
        ));
    }
    let missing =
        || format!("{label} not found in PATH; install {label} to use this PDF operation");
    if name.contains(std::path::MAIN_SEPARATOR) || name.contains('/') {
        return if executable(Path::new(name)) {
            Ok(PathBuf::from(name))
        } else {
            Err(missing())
        };
    }
    for directory in std::env::split_paths(&std::env::var_os("PATH").unwrap_or_default()) {
        let path = directory.join(name);
        if executable(&path) {
            // Go exec.LookPath rejects executables found through relative PATH.
            return if path.is_absolute() {
                Ok(path)
            } else {
                Err(missing())
            };
        }
        #[cfg(windows)]
        for extension in std::env::var("PATHEXT")
            .unwrap_or_else(|_| ".COM;.EXE;.BAT;.CMD".into())
            .split(';')
        {
            let path = directory.join(format!("{name}{extension}"));
            if executable(&path) {
                return if path.is_absolute() {
                    Ok(path)
                } else {
                    Err(missing())
                };
            }
        }
    }
    Err(missing())
}

static NONCE: AtomicU64 = AtomicU64::new(0);
struct Workspace(PathBuf);
impl Workspace {
    fn new(prefix: &str) -> Result<Self, String> {
        for _ in 0..32 {
            let stamp = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap_or_default()
                .as_nanos();
            let path = std::env::temp_dir().join(format!(
                "{prefix}{}-{stamp}-{}",
                std::process::id(),
                NONCE.fetch_add(1, Ordering::Relaxed)
            ));
            let mut builder = fs::DirBuilder::new();
            #[cfg(unix)]
            {
                use std::os::unix::fs::DirBuilderExt;
                builder.mode(0o700);
            }
            match builder.create(&path) {
                Ok(()) => return Ok(Self(path)),
                Err(e) if e.kind() == io::ErrorKind::AlreadyExists => continue,
                Err(e) => return Err(path_error("mkdir", &text(&path), e)),
            }
        }
        Err("could not allocate PDF workspace".into())
    }
}
impl Drop for Workspace {
    fn drop(&mut self) {
        if let Err(error) = fs::remove_dir_all(&self.0) {
            eprintln!("PDF workspace cleanup failed: {error}");
        }
    }
}
pub(crate) fn execute(ctx: &Context, path: &Path, args: &[String]) -> Result<String, String> {
    let label = base(&text(path));
    if let Some(error) = ctx.error() {
        return Err(format!("{label}: {error}"));
    }
    // File-backed capture avoids waiting for EOF from inherited descendant pipes.
    // The Go adapter kills only its direct child; no tree-cleanup claim is made.
    let workspace = Workspace::new("symdesk-pdf-capture-")?;
    let out_path = workspace.0.join("stdout");
    let err_path = workspace.0.join("stderr");
    let stdout = File::create(&out_path).map_err(|e| e.to_string())?;
    let stderr = File::create(&err_path).map_err(|e| e.to_string())?;
    let mut child = Command::new(path)
        .args(args)
        .stdin(Stdio::null())
        .stdout(stdout)
        .stderr(stderr)
        .spawn()
        .map_err(|e| format!("{label}: {}", path_error("fork/exec", &text(path), e)))?;
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) => {}
            Err(error) => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(format!("{label}: {error}"));
            }
        }
        if ctx.error().is_some() {
            let _ = child.kill();
            break child.wait().map_err(|e| format!("{label}: {e}"))?;
        }
        thread::sleep(Duration::from_millis(5));
    };
    let read = |path: &Path| -> Result<String, String> {
        let mut bytes = Vec::new();
        File::open(path)
            .map_err(|e| e.to_string())?
            .take(16 * 1024 * 1024 + 1)
            .read_to_end(&mut bytes)
            .map_err(|e| e.to_string())?;
        if bytes.len() > 16 * 1024 * 1024 {
            return Err("PDF tool output exceeds 16 MiB".into());
        }
        Ok(String::from_utf8_lossy(&bytes).into_owned())
    };
    if !status.success() {
        let message = read(&err_path)?;
        let message = message.trim();
        let fallback = status.code().map_or_else(
            || "signal: killed".into(),
            |code| format!("exit status {code}"),
        );
        return Err(format!(
            "{label}: {}",
            if message.is_empty() {
                &fallback
            } else {
                message
            }
        ));
    }
    read(&out_path)
}
fn quote(value: &str) -> String {
    let mut result = String::from("\"");
    for c in value.chars() {
        match c {
            '"' => result.push_str("\\\""),
            '\\' => result.push_str("\\\\"),
            '\n' => result.push_str("\\n"),
            '\r' => result.push_str("\\r"),
            '\t' => result.push_str("\\t"),
            '\u{7}' => result.push_str("\\a"),
            '\u{8}' => result.push_str("\\b"),
            '\u{b}' => result.push_str("\\v"),
            '\u{c}' => result.push_str("\\f"),
            c if c < ' ' || c == '\u{7f}' => result.push_str(&format!("\\x{:02x}", c as u32)),
            c if c.is_control() || (c.is_whitespace() && c != ' ') => {
                result.push_str(&format!("\\u{:04x}", c as u32))
            }
            c => result.push(c),
        }
    }
    result.push('"');
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn bounded_selector_and_lexical_path() {
        assert_eq!(parse_pages("5,+1 - 3").unwrap(), [1, 2, 3, 5]);
        assert_eq!(
            parse_pages("1-2,2").unwrap_err(),
            "page 2 appears more than once"
        );
        assert_eq!(
            parse_pages("1-9223372036854775807").unwrap_err(),
            "page selector exceeds 1000000 pages"
        );
        assert_eq!(clean(PathBuf::from("a/../b/./c")), Path::new("b/c"));
    }
}
