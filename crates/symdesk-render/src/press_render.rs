//! The press render pipeline (Go `internal/pdf/internal/press/{render,typst,
//! assets,overwrite}.go`): contract check, profile and option resolution, the
//! per-render work directory (templates, fonts, vendored packages, `meta.json`,
//! `body.md`, referenced images, `main.typ`) and the `typst compile` call.
//!
//! The work directory holds real copies of the embedded assets where Go
//! symlinks into a persistent assets cache; the files and the argv handed to
//! Typst are the same, so the PDF is the same.

use std::{
    fs,
    io::Read,
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use regex::Regex;

use crate::{
    pdfops::Context,
    press::{self, EngineInfo},
    press_front::{self, ContractError},
};

const CMARKER_VERSION: &str = "0.1.9";
const MITEX_VERSION: &str = "0.2.7";

macro_rules! asset {
    ($path:literal) => {
        include_bytes!(concat!("../assets/pdf/", $path)) as &[u8]
    };
}

const TEMPLATES: &[(&str, &[u8])] = &[
    ("behoerde.typ", asset!("templates/behoerde.typ")),
    ("brief.typ", asset!("templates/brief.typ")),
    ("meeting.typ", asset!("templates/meeting.typ")),
    ("rechnung.typ", asset!("templates/rechnung.typ")),
    ("report.typ", asset!("templates/report.typ")),
];

const PACKAGES: &[(&str, &[u8])] = &[
    (
        "preview/cmarker/0.1.9/typst.toml",
        asset!("packages/preview/cmarker/0.1.9/typst.toml"),
    ),
    (
        "preview/cmarker/0.1.9/lib.typ",
        asset!("packages/preview/cmarker/0.1.9/lib.typ"),
    ),
    (
        "preview/cmarker/0.1.9/plugin.wasm",
        asset!("packages/preview/cmarker/0.1.9/plugin.wasm"),
    ),
    (
        "preview/mitex/0.2.7/typst.toml",
        asset!("packages/preview/mitex/0.2.7/typst.toml"),
    ),
    (
        "preview/mitex/0.2.7/LICENSE",
        asset!("packages/preview/mitex/0.2.7/LICENSE"),
    ),
    (
        "preview/mitex/0.2.7/CHANGELOG.md",
        asset!("packages/preview/mitex/0.2.7/CHANGELOG.md"),
    ),
    (
        "preview/mitex/0.2.7/mitex.wasm",
        asset!("packages/preview/mitex/0.2.7/mitex.wasm"),
    ),
    (
        "preview/mitex/0.2.7/README.md",
        asset!("packages/preview/mitex/0.2.7/README.md"),
    ),
    (
        "preview/mitex/0.2.7/lib.typ",
        asset!("packages/preview/mitex/0.2.7/lib.typ"),
    ),
    (
        "preview/mitex/0.2.7/mitex.typ",
        asset!("packages/preview/mitex/0.2.7/mitex.typ"),
    ),
    (
        "preview/mitex/0.2.7/specs/README.md",
        asset!("packages/preview/mitex/0.2.7/specs/README.md"),
    ),
    (
        "preview/mitex/0.2.7/specs/mod.typ",
        asset!("packages/preview/mitex/0.2.7/specs/mod.typ"),
    ),
    (
        "preview/mitex/0.2.7/specs/prelude.typ",
        asset!("packages/preview/mitex/0.2.7/specs/prelude.typ"),
    ),
    (
        "preview/mitex/0.2.7/specs/latex/standard.typ",
        asset!("packages/preview/mitex/0.2.7/specs/latex/standard.typ"),
    ),
];

const FONTS: &[(&str, &[u8])] = &[
    (
        "Inter-Bold.ttf",
        include_bytes!("../assets/fonts/Inter-Bold.ttf"),
    ),
    (
        "Inter-Regular.ttf",
        include_bytes!("../assets/fonts/Inter-Regular.ttf"),
    ),
];

#[derive(Clone, Debug, Default)]
pub struct EngineConfig {
    pub typst_bin: String,
    pub font_paths: Vec<String>,
    pub ignore_system_fonts: bool,
    pub timeout: Option<Duration>,
}

#[derive(Clone, Debug, Default)]
pub struct Request {
    pub source: Vec<u8>,
    pub source_name: String,
    /// Empty = no filesystem base for local image references.
    pub source_dir: String,
    pub output_path: String,
    pub profile_override: String,
    pub standard_override: Vec<String>,
    pub reproducible: Option<bool>,
    pub default_profile: String,
    pub short_diagnostics: bool,
    pub engine: EngineConfig,
}

#[derive(Clone, Debug, PartialEq)]
pub struct RenderResult {
    pub output_path: String,
    pub profile: String,
    pub engine: &'static str,
    pub engine_version: String,
    pub pdf_standard: Vec<String>,
    pub reproducible: bool,
    pub bytes: u64,
    pub duration_ms: u64,
}

#[derive(Clone, Debug, Default, PartialEq)]
pub struct RenderError {
    pub stage: String,
    pub message: String,
    pub detail: String,
    pub hint: String,
}

impl std::fmt::Display for RenderError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.message)?;
        if !self.detail.is_empty() {
            write!(f, "\n{}", self.detail)?;
        }
        Ok(())
    }
}

#[derive(Clone, Debug, PartialEq)]
pub enum PressError {
    Contract(ContractError),
    Render(RenderError),
}

impl std::fmt::Display for PressError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Contract(e) => e.fmt(f),
            Self::Render(e) => e.fmt(f),
        }
    }
}

fn render_err(stage: &str, message: impl Into<String>) -> PressError {
    PressError::Render(RenderError {
        stage: stage.into(),
        message: message.into(),
        ..Default::default()
    })
}

/// Go `%q` for a string (printable runes stay as they are).
pub fn go_quote(s: &str) -> String {
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
                out.push_str(&format!("\\x{:02x}", c as u32))
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// The single overwrite policy (Go `CheckOverwrite`).
pub fn check_overwrite(path: &str, overwrite: bool) -> Result<(), String> {
    let Ok(meta) = fs::metadata(path) else {
        return Ok(());
    };
    if meta.is_dir() {
        return Err(format!("output path {} is a directory", go_quote(path)));
    }
    let is_pdf = match fs::File::open(path) {
        Err(e) => {
            return Err(format!(
                "could not read existing file: {}",
                go_open_error(path, &e)
            ));
        }
        Ok(mut f) => {
            let mut buf = [0u8; 4];
            match f.read(&mut buf) {
                Ok(4) => &buf == b"%PDF",
                _ => false,
            }
        }
    };
    if !is_pdf && !overwrite {
        return Err(format!(
            "refusing to overwrite existing non-PDF file {}; pass overwrite to force",
            go_quote(path)
        ));
    }
    Ok(())
}

fn go_open_error(path: &str, e: &std::io::Error) -> String {
    let text = match e.kind() {
        std::io::ErrorKind::PermissionDenied => "permission denied".to_owned(),
        std::io::ErrorKind::NotFound => "no such file or directory".to_owned(),
        _ => {
            let mut m = e.to_string();
            if let Some(i) = m.rfind(" (os error ") {
                m.truncate(i);
            }
            let mut c = m.chars();
            c.next().map_or(m.clone(), |f| {
                f.to_lowercase().collect::<String>() + c.as_str()
            })
        }
    };
    format!("open {path}: {text}")
}

/// The entry document typst compiles (Go `mainTyp`).
pub fn main_typ(template: &str) -> String {
    format!(
        "// Generated by symprint — do not edit.\n#import \"@preview/cmarker:{CMARKER_VERSION}\"\n#import \"@preview/mitex:{MITEX_VERSION}\": mitex\n#import \"/templates/{template}\" as profile\n#let meta = json(\"/meta.json\")\n#show: profile.apply.with(meta)\n#cmarker.render(read(\"/body.md\"),\n  math: (source, block: false) => mitex(source, block: block, alt: source),\n  scope: (\n    image: (source, ..args) => image(source, ..args),\n  ),\n)\n"
    )
}

/// Keep typst's location-pointing lines, bounded to 40 (Go `cleanTypstError`).
pub fn clean_typst_error(s: &str) -> String {
    let s = s.trim_matches(|c: char| c.is_whitespace());
    if s.is_empty() {
        return String::new();
    }
    let lines: Vec<&str> = s.split('\n').collect();
    if lines.len() > 40 {
        let mut kept: Vec<String> = lines[..40].iter().map(|l| (*l).to_owned()).collect();
        kept.push("  … (truncated)".into());
        return kept.join("\n");
    }
    lines.join("\n")
}

fn go_duration(d: Duration) -> String {
    let ns = d.as_nanos();
    if ns == 0 {
        return "0s".into();
    }
    if ns < 1_000 {
        return format!("{ns}ns");
    }
    if ns < 1_000_000 {
        return trim_frac(ns as f64 / 1e3, "µs");
    }
    if ns < 1_000_000_000 {
        return trim_frac(ns as f64 / 1e6, "ms");
    }
    let secs = d.as_secs();
    let (h, m, s) = (secs / 3600, (secs % 3600) / 60, secs % 60);
    let frac = d.subsec_nanos();
    let mut out = String::new();
    if h > 0 {
        out.push_str(&format!("{h}h"));
    }
    if h > 0 || m > 0 {
        out.push_str(&format!("{m}m"));
    }
    if frac == 0 {
        out.push_str(&format!("{s}s"));
    } else {
        out.push_str(format!("{s}.{frac:09}").trim_end_matches('0'));
        out.push('s');
    }
    out
}

fn trim_frac(v: f64, unit: &str) -> String {
    let s = format!("{v:.3}");
    let s = s.trim_end_matches('0').trim_end_matches('.');
    format!("{s}{unit}")
}

static NONCE: AtomicU64 = AtomicU64::new(0);

struct WorkDir(PathBuf);
impl WorkDir {
    fn create() -> Result<Self, std::io::Error> {
        let base = std::env::temp_dir();
        for _ in 0..100 {
            let n = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .map_or(0, |d| d.subsec_nanos() as u64)
                ^ (u64::from(std::process::id()) << 20)
                ^ NONCE
                    .fetch_add(0x9E37, Ordering::Relaxed)
                    .wrapping_mul(2_654_435_761);
            let dir = base.join(format!("symprint-{}", n % 4_000_000_000 + 1_000_000));
            let mut b = fs::DirBuilder::new();
            #[cfg(unix)]
            {
                use std::os::unix::fs::DirBuilderExt;
                b.mode(0o700);
            }
            match b.create(&dir) {
                Ok(()) => return Ok(Self(dir)),
                Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => continue,
                Err(e) => return Err(e),
            }
        }
        Err(std::io::Error::other("could not create a unique work dir"))
    }
}
impl Drop for WorkDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    fs::write(path, data)
}

fn write_assets(work: &Path) -> Result<(), PressError> {
    for (name, data) in TEMPLATES {
        write_private(&work.join("templates").join(name), data)
            .map_err(|_| render_err("write", "could not materialize templates"))?;
    }
    for (name, data) in FONTS {
        write_private(&work.join("fonts").join(name), data)
            .map_err(|_| render_err("write", "could not materialize fonts"))?;
    }
    for (name, data) in PACKAGES {
        write_private(&work.join("packages").join(name), data)
            .map_err(|_| render_err("write", "could not materialize vendored packages"))?;
    }
    Ok(())
}

fn image_re() -> Regex {
    Regex::new(r#"!\[[^\]]*\]\([\t\n\x0c\r ]*<?([^)\t\n\x0c\r >]+)>?(?:[\t\n\x0c\r ]+"[^"]*")?[\t\n\x0c\r ]*\)"#).expect("image regex")
}

fn is_remote_ref(r: &str) -> bool {
    let l = r.to_lowercase();
    l.starts_with("http://") || l.starts_with("https://") || l.starts_with("data:")
}

/// Go `filepath.Clean` for slash paths.
fn go_clean(p: &str) -> String {
    if p.is_empty() {
        return ".".into();
    }
    let rooted = p.starts_with('/');
    let mut out: Vec<&str> = Vec::new();
    for part in p.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if out.last().is_some_and(|l| *l != "..") {
                    out.pop();
                } else if !rooted {
                    out.push("..");
                }
            }
            x => out.push(x),
        }
    }
    let joined = out.join("/");
    match (rooted, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".into(),
        _ => joined,
    }
}

fn collect_assets(src_dir: &str, dst: &Path, body: &[u8]) -> Result<(), PressError> {
    let body = String::from_utf8_lossy(body);
    let refs: Vec<String> = image_re()
        .captures_iter(&body)
        .map(|c| c[1].to_owned())
        .filter(|r| !is_remote_ref(r))
        .collect();
    if refs.is_empty() {
        return Ok(());
    }
    if src_dir.is_empty() {
        return Err(PressError::Render(RenderError {
            stage: "assets".into(),
            message: format!("image reference {} cannot be resolved: the input has no source directory", go_quote(&refs[0])),
            hint: "render from a Markdown file on disk (symprint render <file.md>) so relative image paths have a base directory".into(),
            ..Default::default()
        }));
    }
    let src_root = fs::canonicalize(src_dir)
        .map_err(|_| render_err("assets", "could not resolve source directory"))?;
    for r in &refs {
        copy_asset(&src_root, src_dir, dst, r)?;
    }
    Ok(())
}

fn copy_asset(src_root: &Path, src_dir: &str, dst: &Path, r: &str) -> Result<(), PressError> {
    let asset_err = |message: String, hint: &str| {
        PressError::Render(RenderError {
            stage: "assets".into(),
            message,
            hint: hint.into(),
            ..Default::default()
        })
    };
    if r.starts_with('/') {
        return Err(asset_err(
            format!("image reference {} is an absolute path", go_quote(r)),
            "reference images relative to the Markdown file (e.g. ![alt](images/logo.png))",
        ));
    }
    let clean = go_clean(r);
    if clean == ".." || clean.starts_with("../") {
        return Err(asset_err(
            format!(
                "image reference {} escapes the source directory (path traversal is not allowed)",
                go_quote(r)
            ),
            "keep referenced assets inside the document's directory tree",
        ));
    }
    let src_path = go_clean(&format!("{src_dir}/{clean}"));
    let resolved = match fs::canonicalize(&src_path) {
        Ok(p) => p,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            return Err(asset_err(
                format!(
                    "referenced image {} not found next to the input document",
                    go_quote(r)
                ),
                "check the path — it is resolved relative to the Markdown file",
            ));
        }
        Err(_) => {
            return Err(render_err(
                "assets",
                format!("could not resolve image reference {}", go_quote(r)),
            ));
        }
    };
    if resolved != src_root && !resolved.starts_with(src_root) {
        return Err(asset_err(
            format!(
                "image reference {} resolves outside the source directory (symlink escape)",
                go_quote(r)
            ),
            "copy the asset into the document's directory tree instead of linking to it",
        ));
    }
    let info = fs::metadata(&resolved)
        .map_err(|_| render_err("assets", format!("could not stat image {}", go_quote(r))))?;
    if info.is_dir() {
        return Err(render_err(
            "assets",
            format!("image reference {} points at a directory", go_quote(r)),
        ));
    }
    let dst_path = dst.join(&clean);
    if let Some(parent) = dst_path.parent() {
        fs::create_dir_all(parent)
            .map_err(|_| render_err("write", "could not create asset directory in work dir"))?;
    }
    fs::copy(&resolved, &dst_path).map_err(|_| {
        render_err(
            "write",
            format!("could not copy image {} into the work dir", go_quote(r)),
        )
    })?;
    Ok(())
}

fn absolute(path: &str) -> Result<String, std::io::Error> {
    if path.starts_with('/') {
        return Ok(go_clean(path));
    }
    let cwd = std::env::current_dir()?;
    Ok(go_clean(&format!("{}/{path}", cwd.to_string_lossy())))
}

/// Run the full pipeline.
pub fn render(req: &Request) -> Result<RenderResult, PressError> {
    let doc = press_front::parse(&req.source).map_err(PressError::Contract)?;
    let mut front = doc.front.clone();

    let mut name = req.profile_override.clone();
    if name.is_empty() {
        name = front.profile.clone();
    }
    if name.is_empty() {
        name = req.default_profile.clone();
    }
    if name.is_empty() {
        return Err(PressError::Render(RenderError {
            stage: "contract".into(),
            message: "no profile selected".into(),
            hint: "set `profile:` in the frontmatter or pass --profile (see `symprint profiles`)"
                .into(),
            ..Default::default()
        }));
    }
    let Some(prof) = press::lookup(&name) else {
        return Err(PressError::Render(RenderError {
            stage: "contract".into(),
            message: format!("unknown profile: {name}"),
            hint: "run `symprint profiles` to list the built-in profiles".into(),
            ..Default::default()
        }));
    };
    if front.form.is_empty() {
        front.form = prof.form.to_owned();
    }
    front.profile = prof.name.to_owned();
    let checked = press_front::Document {
        front: front.clone(),
        body: doc.body.clone(),
    };
    let issues = checked.validate(prof);
    if issues.iter().any(|i| i.severity == "error") {
        return Err(PressError::Contract(ContractError {
            issues,
            ..Default::default()
        }));
    }

    let mut std_list = req.standard_override.clone();
    if std_list.is_empty() {
        std_list = front.pdf.standard.clone();
    }
    if std_list.is_empty() {
        std_list = prof.pdf_standard.iter().map(|s| (*s).to_owned()).collect();
    }
    let mut repro = prof.reproducible;
    if let Some(r) = front.pdf.reproducible {
        repro = r;
    }
    if let Some(r) = req.reproducible {
        repro = r;
    }

    let eng: EngineInfo = press::detect_typst(&Context::default(), &req.engine.typst_bin);
    if !eng.available {
        return Err(PressError::Render(RenderError {
            stage: "engine".into(),
            message: "rendering engine not available".into(),
            hint: eng.hint,
            ..Default::default()
        }));
    }
    render_typst(&eng, prof, &front, &doc.body, req, &std_list, repro)
}

fn render_typst(
    eng: &EngineInfo,
    prof: &press::Profile,
    front: &press_front::Frontmatter,
    body: &[u8],
    req: &Request,
    std_list: &[String],
    repro: bool,
) -> Result<RenderResult, PressError> {
    let start = Instant::now();
    let work = WorkDir::create().map_err(|_| render_err("write", "could not create work dir"))?;
    let w = &work.0;
    write_assets(w)?;
    let meta = press_front::meta_json(front)
        .map_err(|_| render_err("write", "could not encode metadata"))?;
    write_private(&w.join("meta.json"), meta.as_bytes())
        .map_err(|_| render_err("write", "could not write metadata"))?;
    write_private(&w.join("body.md"), body)
        .map_err(|_| render_err("write", "could not write body"))?;
    collect_assets(&req.source_dir, w, body)?;
    write_private(&w.join("main.typ"), main_typ(prof.template).as_bytes())
        .map_err(|_| render_err("write", "could not write entry"))?;

    let out = absolute(&req.output_path).map_err(|_| render_err("write", "invalid output path"))?;
    let out_dir = Path::new(&out)
        .parent()
        .unwrap_or(Path::new("/"))
        .to_path_buf();
    {
        let mut b = fs::DirBuilder::new();
        b.recursive(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            b.mode(0o700);
        }
        b.create(&out_dir)
            .map_err(|_| render_err("write", "could not create output dir"))?;
    }

    let work_s = w.to_string_lossy().into_owned();
    let mut args: Vec<String> = vec!["compile".into(), "--root".into(), work_s.clone()];
    args.extend([
        "--package-path".into(),
        format!("{work_s}/packages"),
        "--package-cache-path".into(),
        format!("{work_s}/package-cache"),
    ]);
    if req.short_diagnostics {
        args.extend(["--diagnostic-format".into(), "short".into()]);
    }
    args.extend(["--font-path".into(), format!("{work_s}/fonts")]);
    for fp in &req.engine.font_paths {
        args.extend(["--font-path".into(), fp.clone()]);
    }
    if req.engine.ignore_system_fonts || req.engine.font_paths.is_empty() {
        args.push("--ignore-system-fonts".into());
    }
    if !std_list.is_empty() {
        args.extend(["--pdf-standard".into(), std_list.join(",")]);
    }
    args.extend([format!("{work_s}/main.typ"), out.clone()]);

    let timeout = req
        .engine
        .timeout
        .filter(|t| !t.is_zero())
        .unwrap_or(Duration::from_secs(60));
    let mut cmd = Command::new(&eng.path);
    cmd.args(&args)
        .current_dir(w)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::piped());
    if repro {
        cmd.env("SOURCE_DATE_EPOCH", "0");
    }
    let compile_failed = |detail: String| {
        PressError::Render(RenderError {
            stage: "compile".into(),
            message: "typst could not render the document".into(),
            detail: clean_typst_error(&detail),
            hint: "fix the source/template, then re-run; full engine output is above".into(),
        })
    };
    let mut child = cmd.spawn().map_err(|e| compile_failed(e.to_string()))?;
    let mut stderr = child.stderr.take().expect("piped stderr");
    let reader = std::thread::spawn(move || {
        let mut buf = Vec::new();
        let _ = stderr.read_to_end(&mut buf);
        buf
    });
    let deadline = Instant::now() + timeout;
    let status = loop {
        match child.try_wait() {
            Ok(Some(s)) => break Some(s),
            Ok(None) if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                break None;
            }
            Ok(None) => std::thread::sleep(Duration::from_millis(2)),
            Err(e) => return Err(compile_failed(e.to_string())),
        }
    };
    let stderr_text = String::from_utf8_lossy(&reader.join().unwrap_or_default()).into_owned();
    match status {
        None => {
            return Err(PressError::Render(RenderError {
                stage: "compile".into(),
                message: format!("typst compile timed out after {}", go_duration(timeout)),
                hint: "raise engine.timeout_seconds in the config".into(),
                ..Default::default()
            }));
        }
        Some(s) if !s.success() => return Err(compile_failed(stderr_text)),
        Some(_) => {}
    }
    let bytes = fs::metadata(&out).map_or(0, |m| m.len());
    Ok(RenderResult {
        output_path: out,
        profile: prof.name.to_owned(),
        engine: "typst",
        engine_version: eng.version.clone(),
        pdf_standard: std_list.to_vec(),
        reproducible: repro,
        bytes,
        duration_ms: start.elapsed().as_millis() as u64,
    })
}
