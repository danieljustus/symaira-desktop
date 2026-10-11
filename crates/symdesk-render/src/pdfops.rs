//! Page-level Poppler/qpdf adapter, matching `internal/ingest/internal/pdfops`.
//! Tools are resolved per operation. No shell is involved. Inputs and outputs
//! are caller-selected local paths; this is not a vault-confinement boundary.
use std::{
    collections::BTreeSet,
    fs, io,
    path::{Component, Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicU64, Ordering},
    },
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
    fn bounded(&self) -> Self {
        let deadline = Instant::now() + Duration::from_secs(60);
        Self {
            cancelled: self.cancelled.clone(),
            deadline: Some(self.deadline.map_or(deadline, |at| at.min(deadline))),
        }
    }
    pub(crate) fn error(&self) -> Option<&'static str> {
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
        let ctx = &ctx.bounded();
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
        let ends: Vec<_> = boundaries.into_iter().chain([pages]).collect();
        let outputs: Vec<_> = (1..=ends.len())
            .map(|index| {
                text(&clean(
                    Path::new(output_dir).join(format!("part-{index:03}.pdf")),
                ))
            })
            .collect();
        new_outputs(&[input.into()], &outputs)?;
        let temp = Workspace::new(Path::new(output_dir))
            .map_err(|e| format!("create PDF split workspace: {e}"))?;
        let mut start = 1;
        for (index, end) in ends.into_iter().enumerate() {
            let staged = text(&temp.0.join(format!("part-{:03}.pdf", index + 1)));
            self.run(
                ctx,
                &self.qpdf,
                &[
                    input.into(),
                    "--pages".into(),
                    ".".into(),
                    format!("{start}-{end}"),
                    "--".into(),
                    staged.clone(),
                ],
            )
            .map_err(|e| format!("split part {}: {e}", index + 1))?;
            self.validate(ctx, &staged)?;
            start = end + 1;
        }
        for output in &outputs {
            publish(ctx, &text(&temp.0.join(base(output))), output)?;
        }
        Ok(outputs)
    }
    pub fn merge(&self, ctx: &Context, inputs: &[String], output: &str) -> Result<(), String> {
        let ctx = &ctx.bounded();
        if inputs.len() < 2 {
            return Err("merge requires at least two input PDFs".into());
        }
        for input in inputs {
            require_file(input).map_err(|e| format!("merge input {input}: {e}"))?;
        }
        mkdir_all(&parent(output)).map_err(|e| format!("create merge output directory: {e}"))?;
        self.write_output(ctx, inputs, output, |staged| {
            let mut args = vec![
                inputs[0].clone(),
                "--pages".into(),
                ".".into(),
                "1-z".into(),
            ];
            for input in &inputs[1..] {
                args.extend([input.clone(), "1-z".into()]);
            }
            args.extend(["--".into(), staged.into()]);
            self.run(ctx, &self.qpdf, &args)
                .map_err(|e| format!("merge PDFs: {e}"))
        })
    }
    pub fn rotate(
        &self,
        ctx: &Context,
        input: &str,
        output: &str,
        degrees: i64,
        pages: &str,
    ) -> Result<(), String> {
        let ctx = &ctx.bounded();
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
        self.write_output(ctx, &[input.into()], output, |staged| {
            self.run(
                ctx,
                &self.qpdf,
                &[input.into(), staged.into(), format!("--rotate={selector}")],
            )
            .map_err(|e| format!("rotate PDF (qpdf is required): {e}"))
        })
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
                if pages > 10_000 {
                    return Err("PDF page count exceeds 10000 pages".into());
                }
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
    fn write_output(
        &self,
        ctx: &Context,
        inputs: &[String],
        output: &str,
        write: impl FnOnce(&str) -> Result<(), String>,
    ) -> Result<(), String> {
        new_outputs(inputs, &[output.into()])?;
        let temp = Workspace::new(&parent(output))
            .map_err(|e| format!("create PDF output workspace: {e}"))?;
        let staged = text(&temp.0.join("result.pdf"));
        write(&staged)?;
        self.validate(ctx, &staged)?;
        publish(ctx, &staged, output)
    }
    fn validate(&self, ctx: &Context, staged: &str) -> Result<(), String> {
        let info = fs::symlink_metadata(staged).map_err(|e| path_error("lstat", staged, e))?;
        if !info.file_type().is_file() {
            return Err("PDF tool output is not a regular file".into());
        }
        self.run(ctx, &self.qpdf, &["--check".into(), staged.into()])
            .map_err(|e| format!("validate PDF output: {e}"))?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            fs::set_permissions(staged, fs::Permissions::from_mode(0o600))
                .map_err(|e| e.to_string())?;
        }
        Ok(())
    }
}

fn new_outputs(inputs: &[String], outputs: &[String]) -> Result<(), String> {
    for output in outputs {
        if let Ok(info) = fs::metadata(output) {
            for input in inputs {
                let original = fs::metadata(input).map_err(|e| path_error("stat", input, e))?;
                #[cfg(unix)]
                {
                    use std::os::unix::fs::MetadataExt;
                    if info.dev() == original.dev() && info.ino() == original.ino() {
                        return Err(format!("PDF output aliases input: {output}"));
                    }
                }
                #[cfg(not(unix))]
                let _ = (&info, original);
            }
        }
        match fs::symlink_metadata(output) {
            Ok(_) => return Err(format!("PDF output already exists: {output}")),
            Err(e) if e.kind() == io::ErrorKind::NotFound => {}
            Err(e) => return Err(path_error("lstat", output, e)),
        }
    }
    Ok(())
}

fn publish(ctx: &Context, staged: &str, output: &str) -> Result<(), String> {
    if let Some(error) = ctx.error() {
        return Err(error.into());
    }
    // Atomic create-if-absent; never overwrite an alias or a raced-in path.
    // A multi-part publication failure may retain already-published new parts.
    fs::hard_link(staged, output).map_err(|e| {
        format!(
            "publish PDF output: {}",
            path_error("link", &format!("{staged} {output}"), e)
        )
    })
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
    if !metadata.is_file() {
        return Err(format!("{path} is not a regular file"));
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
    fn new(directory: &Path) -> Result<Self, String> {
        for _ in 0..32 {
            let stamp = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap_or_default()
                .as_nanos();
            let path = directory.join(format!(
                ".symdesk-pdf-{}-{stamp}-{}",
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
    crate::pdfprocess::run(ctx, path, args)
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
            c if !crate::go_print::is_print(c) => {
                if c as u32 <= 0xffff {
                    result.push_str(&format!("\\u{:04x}", c as u32));
                } else {
                    result.push_str(&format!("\\U{:08x}", c as u32));
                }
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

    #[test]
    fn go_unicode_error_quoting() {
        assert_eq!(
            parse_pages("1,\u{200b}").unwrap_err(),
            "invalid page number \"\\u200b\""
        );
        assert_eq!(
            quote("é東\u{301}\u{200b}\u{e000}\u{e0001}\u{1cc00}"),
            "\"é東\u{301}\\u200b\\ue000\\U000e0001\\U0001cc00\""
        );
    }
}
