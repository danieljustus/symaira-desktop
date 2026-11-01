//! `symdesk export --note` (Go `cmd/symdesk/export.go`,
//! `internal/service/export.go`, `dataset_policy.go`): one vault note rendered
//! to HTML or PDF, behind the dataset export policy.
//!
//! `--view` and `--format csv` for a view need the saved-view engine
//! (`ViewsExec` / `ViewsExportCSV`), which lives in the r013-paperviews slice.

use std::{
    fs,
    path::{Path, PathBuf},
    process::ExitCode,
};

use serde::Serialize;
use symdesk_vault::{go_quote, parse_dataset_handle, secure_path, slugify};

use crate::{emit_error, write_go_json, write_stdout};

#[derive(Serialize)]
struct ExportResult {
    format: &'static str,
    path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    profile: String,
    rendered: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    message: String,
}

pub fn run(command: &clap::ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    let value = |name: &str| command.get_one::<String>(name).cloned().unwrap_or_default();
    let (root, mut sidecar) = match crate::relations_cli::service_deps(vault) {
        Ok(deps) => deps,
        Err(error) => return emit_error(error, json_output),
    };
    let outcome = export(
        &root,
        &value("note"),
        &value("local-output"),
        &value("format"),
        &value("profile"),
    );
    if let Err(error) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
    let result = match outcome {
        Ok(result) => result,
        Err(error) => return emit_error(error, json_output),
    };
    if json_output {
        return write_go_json(&result);
    }
    write_stdout(format!(
        "&{{Format:{} Path:{} Profile:{} Rendered:{} Message:{}}}\n",
        result.format, result.path, result.profile, result.rendered, result.message
    ))
}

/// Go `Service.Export` for a note.
fn export(
    root: &Path,
    note: &str,
    output: &str,
    format: &str,
    profile: &str,
) -> Result<ExportResult, String> {
    let format = format.to_lowercase();
    let format: &'static str = match format.as_str() {
        "pdf" => "pdf",
        "html" => "html",
        "csv" => "csv",
        _ => {
            return Err(format!(
                "unsupported export format {}: use pdf, html, or csv",
                go_quote(&format)
            ));
        }
    };
    if !note.is_empty() {
        check_dataset_export_path(root, note)?;
    }
    if format == "csv" {
        return Err("csv export is only supported for views (use --view)".to_owned());
    }
    if note.is_empty() {
        return Err("export requires --note or --view".to_owned());
    }
    let rendered = symdesk_render::export_vault::export_note(root, note, format)?;
    let output = if output.is_empty() {
        default_output(note, format)
    } else {
        output.to_owned()
    };
    let target = absolute_output(&output)?;
    let description = format!("note {note}");
    if format == "pdf" {
        let source_dir = root
            .join(note)
            .parent()
            .map_or_else(|| root.to_path_buf(), Path::to_path_buf);
        return render_pdf(&rendered, &target, profile, &source_dir, &description);
    }
    write_html(&target, &rendered).map_err(|error| {
        format!(
            "failed to write HTML: {}",
            symdesk_vault::go_path_error("open", &target, &error)
        )
    })?;
    Ok(ExportResult {
        format: "html",
        path: target.to_string_lossy().into_owned(),
        profile: String::new(),
        rendered: true,
        message: format!("Exported {description} to HTML"),
    })
}

fn render_pdf(
    markdown: &[u8],
    target: &Path,
    profile: &str,
    source_dir: &Path,
    description: &str,
) -> Result<ExportResult, String> {
    let context = symdesk_render::pdfops::Context::default();
    let info = symdesk_render::press::detect_typst(&context, "");
    if !info.available {
        return Err(format!(
            "PDF export requires a typesetting engine: {}",
            info.hint
        ));
    }
    let request = symdesk_render::press_render::Request {
        source: markdown.to_vec(),
        source_name: target.to_string_lossy().into_owned(),
        source_dir: source_dir.to_string_lossy().into_owned(),
        output_path: target.to_string_lossy().into_owned(),
        profile_override: profile.to_owned(),
        ..Default::default()
    };
    let rendered =
        symdesk_render::press_render::render(&request).map_err(|error| match &error {
            symdesk_render::press_render::PressError::Render(render)
                if render.stage == "engine" =>
            {
                format!("typesetting engine unavailable: {}", render.hint)
            }
            _ => error.to_string(),
        })?;
    Ok(ExportResult {
        format: "pdf",
        path: rendered.output_path.clone(),
        message: format!(
            "Exported {description} to PDF (profile {}, typst {})",
            rendered.profile, rendered.engine_version
        ),
        profile: rendered.profile,
        rendered: true,
    })
}

fn write_html(path: &Path, data: &[u8]) -> std::io::Result<()> {
    use std::io::Write as _;
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o644);
    }
    options.open(path)?.write_all(data)
}

/// Go `defaultOutputPath` for a note.
fn default_output(note: &str, format: &str) -> String {
    let base = note.rsplit('/').next().unwrap_or(note);
    let stem = base.rfind('.').map_or(base, |index| &base[..index]);
    format!("{stem}.{format}")
}

/// `filepath.Clean`, then `filepath.Join(cwd, ..)` for a relative result.
fn absolute_output(output: &str) -> Result<PathBuf, String> {
    let cleaned = clean(output);
    if cleaned.starts_with('/') {
        return Ok(PathBuf::from(cleaned));
    }
    let cwd = std::env::current_dir().map_err(|error| format!("cannot resolve cwd: {error}"))?;
    Ok(PathBuf::from(clean(&format!(
        "{}/{cleaned}",
        cwd.to_string_lossy()
    ))))
}

fn clean(path: &str) -> String {
    let rooted = path.starts_with('/');
    let mut parts: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|last| *last != "..") {
                    parts.pop();
                } else if !rooted {
                    parts.push("..");
                }
            }
            value => parts.push(value),
        }
    }
    let joined = parts.join("/");
    match (rooted, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".to_owned(),
        (false, false) => joined,
    }
}

/// Go `checkDatasetExportPath`.
fn check_dataset_export_path(root: &Path, note: &str) -> Result<(), String> {
    let slashed = note.trim().replace('\\', "/");
    let normalized = slashed.strip_prefix("./").unwrap_or(&slashed);
    let Some(tail) = normalized.strip_prefix("datasets/") else {
        return Ok(());
    };
    let parts: Vec<&str> = tail.split('/').collect();
    if parts[0].trim().is_empty() {
        return Err("dataset export policy: dataset path requires a dataset handle".to_owned());
    }
    let slug = if parts.len() == 1 {
        parts[0].strip_suffix(".md").unwrap_or(parts[0])
    } else {
        parts[0]
    };
    check_dataset_export_slug(root, slug)
}

/// Go `checkDatasetExportSlug`.
fn check_dataset_export_slug(root: &Path, slug: &str) -> Result<(), String> {
    let slug = slug.trim();
    if slug.is_empty() {
        return Err("dataset export requires a dataset slug".to_owned());
    }
    if slug != slugify(slug) {
        return Err(format!(
            "dataset export policy: dataset slug {} is not filesystem-safe",
            go_quote(slug)
        ));
    }
    let rel = format!("datasets/{slug}.md");
    let handle = (|| {
        let path = secure_path(root, &rel).map_err(|error| error.to_string())?;
        let data = symdesk_render::export_vault::read_in_root(root, &path)?;
        parse_dataset_handle(&rel, &data).map_err(|error| error.to_string())
    })()
    .map_err(|error| format!("dataset export policy: {error}"))?;
    if handle.slug != slug {
        return Err(format!(
            "dataset export policy: dataset handle slug {} does not match path slug {}",
            go_quote(&handle.slug),
            go_quote(slug)
        ));
    }
    let configured = crate::config_paths::load_root_config()
        .map(|(config, _)| config.dataset_export_max_sensitivity)
        .unwrap_or_default();
    let max = if configured.trim().is_empty() {
        "internal".to_owned()
    } else {
        configured.trim().to_lowercase()
    };
    if rank(&max) < 0 {
        return Err(format!(
            "dataset export policy: invalid dataset sensitivity {} (valid: public, internal, confidential, restricted)",
            go_quote(&max)
        ));
    }
    if rank(&handle.sensitivity) > rank(&max) {
        return Err(format!(
            "dataset export denied: dataset {} is {}; maximum configured export sensitivity is {max} (set dataset_export_max_sensitivity explicitly to opt in)",
            go_quote(slug),
            handle.sensitivity
        ));
    }
    Ok(())
}

fn rank(value: &str) -> i32 {
    match value {
        "public" => 0,
        "internal" => 1,
        "confidential" => 2,
        "restricted" => 3,
        _ => -1,
    }
}
