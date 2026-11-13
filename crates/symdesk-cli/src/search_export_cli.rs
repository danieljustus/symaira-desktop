//! `symdesk search export` and `symdesk search notebook` (Go
//! `cmd/symdesk/search.go`, `internal/service/search_export.go`,
//! `internal/export/search_results.go`): a search result set rendered to a
//! vault note / PDF, or promoted to a Markdown-backed notebook.
use std::{
    fmt::Write as _,
    path::{Path, PathBuf},
    process::ExitCode,
};

use clap::{Arg, ArgMatches, Command};
use symdesk_index::Sidecar;
use symdesk_vault::{
    add_notebook_source, go_quote, new_notebook_with_query, notebook_slug,
    render_go_yaml_double_quoted, render_go_yaml_string, secure_path,
};

use crate::{
    mutate_cli::{parse, reindex, snapshot},
    search_cli::CliSearchHit,
    service_cli::with_deps,
    write_go_json, write_stdout,
};

/// Cobra's command-local `--output` of `search export` shadows the persistent
/// one; Clap names it apart from the global flag.
pub fn rewrite_output_flag(args: &mut [std::ffi::OsString]) {
    rewrite_export_output_flag(args);
    let Some(at) = args.iter().position(|arg| arg == "search") else {
        return;
    };
    if !args
        .get(at + 1)
        .is_some_and(|arg| arg == "export" || arg == "save-note")
    {
        return;
    }
    for argument in args.iter_mut().skip(at + 2) {
        if argument == "--output" {
            *argument = std::ffi::OsString::from("--local-output");
        } else if let Some(value) = argument.to_str().and_then(|v| v.strip_prefix("--output=")) {
            *argument = std::ffi::OsString::from(format!("--local-output={value}"));
        }
    }
}

/// The top-level `export` command shadows `--output` the same way.
fn rewrite_export_output_flag(args: &mut [std::ffi::OsString]) {
    let Some(at) = args.iter().position(|arg| arg == "export") else {
        return;
    };
    if at > 0 && args[at - 1] == "search" {
        return;
    }
    for argument in args.iter_mut().skip(at + 1) {
        if argument == "--output" {
            *argument = std::ffi::OsString::from("--local-output");
        } else if let Some(value) = argument.to_str().and_then(|v| v.strip_prefix("--output=")) {
            *argument = std::ffi::OsString::from(format!("--local-output={value}"));
        }
    }
}

pub fn export_cli() -> Command {
    Command::new("export")
        .visible_alias("save-note")
        .arg(Arg::new("args").num_args(0..))
        .arg(Arg::new("format").long("format").default_value("markdown"))
        .arg(Arg::new("title").long("title").default_value(""))
        // Cobra's local `--output` shadows the persistent switch; the argv
        // rewrite in `main` renames it for Clap.
        .arg(
            Arg::new("local-output")
                .long("local-output")
                .default_value(""),
        )
}

pub fn notebook_cli() -> Command {
    Command::new("notebook")
        .visible_alias("keep")
        .visible_alias("save")
        .arg(Arg::new("args").num_args(0..))
        .arg(Arg::new("title").long("title").default_value(""))
}

#[derive(serde::Serialize)]
struct ExportResult {
    format: String,
    path: String,
    query: String,
    title: String,
    count: usize,
}

pub fn run(name: &str, command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let query = command
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .next()
        .cloned()
        .unwrap_or_default();
    let title = command
        .get_one::<String>("title")
        .cloned()
        .unwrap_or_default();
    with_deps(vault, output_json, |root, sidecar| {
        let query = query.trim().to_owned();
        if query.is_empty() {
            return Err("search query is required".to_owned());
        }
        let canonical = std::fs::canonicalize(root).map_err(|error| error.to_string())?;
        let hits = crate::search_hits(root, sidecar, &query)?;
        if name == "notebook" {
            return search_notebook(&canonical, sidecar, &query, &title, &hits, output_json);
        }
        let format = command
            .get_one::<String>("format")
            .cloned()
            .unwrap_or_default();
        let output = command
            .get_one::<String>("local-output")
            .cloned()
            .unwrap_or_default();
        export(
            &canonical,
            sidecar,
            &query,
            &title,
            &output,
            &format,
            &hits,
            output_json,
        )
    })
}

fn search_notebook(
    root: &Path,
    sidecar: &mut Sidecar,
    query: &str,
    title: &str,
    hits: &[CliSearchHit],
    output_json: bool,
) -> Result<ExitCode, String> {
    let title = if title.trim().is_empty() {
        format!("Search: {query}")
    } else {
        title.to_owned()
    };
    let mut notebook = new_notebook_with_query(
        root,
        &title,
        &format!("Promoted from search query: {query}"),
        query,
    )
    .map_err(|error| error.to_string())?;
    let mut seen = std::collections::HashSet::new();
    for hit in hits {
        let path = hit.path.replace('\\', "/").trim().to_owned();
        if path.is_empty() || !seen.insert(path.clone()) {
            continue;
        }
        notebook =
            add_notebook_source(root, &notebook.path, &path).map_err(|error| error.to_string())?;
    }
    let path = secure_path(root, &notebook.path).map_err(|error| error.to_string())?;
    parse(&path)
        .map_err(|error| format!("wrote notebook but failed to parse for indexing: {error}"))?;
    reindex(sidecar, &path)
        .map_err(|error| format!("wrote notebook but failed to index: {error}"))?;
    Ok(if output_json {
        write_go_json(&notebook)
    } else {
        write_stdout(format!(
            "&{{ID:{} Path:{} Title:{} Description:{} Created:{} Sources:[{}] Query:{}}}\n",
            notebook.id,
            notebook.path,
            notebook.title,
            notebook.description,
            notebook.created,
            notebook.sources.join(" "),
            notebook.query
        ))
    })
}

#[allow(clippy::too_many_arguments)]
fn export(
    root: &Path,
    sidecar: &mut Sidecar,
    query: &str,
    title: &str,
    output: &str,
    format: &str,
    hits: &[CliSearchHit],
    output_json: bool,
) -> Result<ExitCode, String> {
    let format = match format.trim().to_lowercase().as_str() {
        "md" => "markdown".to_owned(),
        other => other.to_owned(),
    };
    if format != "markdown" && format != "pdf" {
        return Err(format!(
            "unsupported search export format {}: use markdown or pdf",
            go_quote(&format)
        ));
    }
    let title = if title.trim().is_empty() {
        format!("Search results: {query}")
    } else {
        title.to_owned()
    };
    let created = symdesk_vault::notes::format_rfc3339(time::OffsetDateTime::now_utc());
    let content = search_results_markdown(query, &title, &created, hits, &format)?;
    let result = if format == "markdown" {
        let (relative, absolute) = if output.is_empty() {
            note_path(root, &title, ".md")?
        } else {
            vault_output_path(root, output, ".md")?
        };
        snapshot(root, &absolute);
        write_note(&absolute, content.as_bytes())?;
        index_written(sidecar, &absolute)?;
        ExportResult {
            format,
            path: relative,
            query: query.to_owned(),
            title,
            count: hits.len(),
        }
    } else {
        let target = if output.is_empty() {
            note_path(root, &title, ".pdf")?.1
        } else {
            absolute_output(output)?
        };
        render_pdf(root, &content, &target)?;
        ExportResult {
            format,
            path: target.to_string_lossy().into_owned(),
            query: query.to_owned(),
            title,
            count: hits.len(),
        }
    };
    Ok(if output_json {
        write_go_json(&result)
    } else {
        write_stdout(format!(
            "&{{Format:{} Path:{} Query:{} Title:{} Count:{}}}\n",
            result.format, result.path, result.query, result.title, result.count
        ))
    })
}

fn render_pdf(root: &Path, markdown: &str, target: &Path) -> Result<(), String> {
    let context = symdesk_render::pdfops::Context::default();
    let info = symdesk_render::press::detect_typst(&context, "");
    if !info.available {
        return Err(format!(
            "PDF export requires a typesetting engine: {}",
            info.hint
        ));
    }
    let request = symdesk_render::press_render::Request {
        source: markdown.as_bytes().to_vec(),
        source_name: target.to_string_lossy().into_owned(),
        source_dir: root.to_string_lossy().into_owned(),
        output_path: target.to_string_lossy().into_owned(),
        ..Default::default()
    };
    symdesk_render::press_render::render(&request)
        .map(|_| ())
        .map_err(|error| error.to_string())
}

fn write_note(path: &Path, content: &[u8]) -> Result<(), String> {
    use std::io::Write as _;
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options
        .open(path)
        .and_then(|mut file| file.write_all(content))
        .map_err(|error| format!("write search result note: {error}"))
}

fn index_written(sidecar: &mut Sidecar, path: &Path) -> Result<(), String> {
    parse(path).map_err(|error| {
        format!("wrote search result note but failed to parse for indexing: {error}")
    })?;
    reindex(sidecar, path)
        .map(|_| ())
        .map_err(|error| format!("wrote search result note but failed to index: {error}"))
}

fn note_path(root: &Path, title: &str, extension: &str) -> Result<(String, PathBuf), String> {
    let slug = notebook_slug(title);
    let mut attempt = 1;
    loop {
        let name = if attempt == 1 {
            format!("{slug}{extension}")
        } else {
            format!("{slug}-{attempt}{extension}")
        };
        let (relative, absolute) =
            vault_output_path(root, &format!("search-results/{name}"), extension)?;
        if !absolute.exists() {
            return Ok((relative, absolute));
        }
        attempt += 1;
    }
}

fn vault_output_path(
    root: &Path,
    output: &str,
    extension: &str,
) -> Result<(String, PathBuf), String> {
    if output.is_empty() {
        return Err("output path is required".to_owned());
    }
    let mut output = output.to_owned();
    if go_ext(&output).is_empty() {
        output.push_str(extension);
    }
    let cleaned = if Path::new(&output).is_absolute() {
        let relative = pathdiff(root, &lexical_clean(Path::new(&output)));
        relative.ok_or_else(|| "Rel: can't make path relative to vault".to_owned())?
    } else {
        output
    };
    let relative = lexical_clean_str(&cleaned);
    if relative == "."
        || relative == ".."
        || relative.starts_with("../")
        || relative == ".symdesk"
        || relative.starts_with(".symdesk/")
    {
        return Err("search export path must stay inside the vault".to_owned());
    }
    let absolute = secure_path(root, &relative).map_err(|error| error.to_string())?;
    if let Some(parent) = absolute.parent() {
        create_dir_all_0750(parent)
            .map_err(|error| format!("create search export directory: {error}"))?;
    }
    Ok((relative, absolute))
}

/// Go `filepath.Ext`: the suffix from the final dot of the last element.
fn go_ext(path: &str) -> &str {
    for (index, byte) in path.bytes().enumerate().rev() {
        if byte == b'/' {
            break;
        }
        if byte == b'.' {
            return &path[index..];
        }
    }
    ""
}

fn create_dir_all_0750(path: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o750)
            .create(path)
    }
    #[cfg(not(unix))]
    {
        std::fs::create_dir_all(path)
    }
}

fn absolute_output(output: &str) -> Result<PathBuf, String> {
    let cleaned = lexical_clean(Path::new(output));
    if cleaned.is_absolute() {
        return Ok(cleaned);
    }
    let cwd = std::env::current_dir().map_err(|error| format!("cannot resolve cwd: {error}"))?;
    Ok(lexical_clean(&cwd.join(cleaned)))
}

/// Go `filepath.Clean` on a slash-separated string.
fn lexical_clean_str(path: &str) -> String {
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
            other => parts.push(other),
        }
    }
    let joined = parts.join("/");
    match (rooted, joined.is_empty()) {
        (true, _) => format!("/{joined}"),
        (false, true) => ".".to_owned(),
        (false, false) => joined,
    }
}

fn lexical_clean(path: &Path) -> PathBuf {
    PathBuf::from(lexical_clean_str(&path.to_string_lossy()))
}

/// Go `filepath.Rel(root, target)` for clean absolute paths.
fn pathdiff(root: &Path, target: &Path) -> Option<String> {
    let root: Vec<_> = root.components().collect();
    let target: Vec<_> = target.components().collect();
    let common = root.iter().zip(&target).take_while(|(a, b)| a == b).count();
    let mut parts: Vec<String> = Vec::new();
    for _ in common..root.len() {
        parts.push("..".to_owned());
    }
    for component in &target[common..] {
        parts.push(component.as_os_str().to_string_lossy().into_owned());
    }
    if parts.is_empty() {
        Some(".".to_owned())
    } else {
        Some(parts.join("/"))
    }
}

// ---- internal/export/search_results.go -------------------------------------

fn search_results_markdown(
    query: &str,
    title: &str,
    created: &str,
    hits: &[CliSearchHit],
    format: &str,
) -> Result<String, String> {
    let title = if title.trim().is_empty() {
        "Search results"
    } else {
        title
    };
    let mut out = String::from("---\n");
    // yaml.Marshal of a map: keys sorted.
    let scalar = |value: &str| {
        render_go_yaml_string(value, 4, false)
            .map_err(|error| format!("encode search result metadata: {error}"))
    };
    let _ = writeln!(out, "created: {}", scalar(created)?.trim_end_matches('\n'));
    let _ = writeln!(out, "query: {}", render_go_yaml_double_quoted(query));
    let _ = writeln!(out, "result_count: {}", hits.len());
    let _ = writeln!(out, "title: {}", scalar(title)?.trim_end_matches('\n'));
    out.push_str("type: search_result_set\n");
    out.push_str("---\n\n");
    if format != "pdf" {
        out.push_str("# ");
        out.push_str(&heading_text(title));
        out.push_str("\n\n");
    }
    out.push_str("Query: `");
    out.push_str(&code_text(query));
    out.push_str("`\n\n");
    let _ = write!(
        out,
        "**{} result{}**\n\n",
        hits.len(),
        if hits.len() == 1 { "" } else { "s" }
    );
    if hits.is_empty() {
        out.push_str("_No matching passages._\n");
        return Ok(out);
    }
    for (index, hit) in hits.iter().enumerate() {
        let _ = write!(out, "## {}. {}\n\n", index + 1, heading_text(&hit.title));
        out.push_str("**Source:** [[");
        out.push_str(&wiki_target(hit));
        out.push_str("]] (`");
        out.push_str(&code_text(&hit.path));
        out.push_str("`)\n");
        match &hit.anchor {
            Some(anchor) if !anchor.kind.is_empty() && !anchor.value.is_empty() => {
                out.push_str("**Location:** ");
                out.push_str(&inline_text(&anchor.kind));
                out.push_str(": ");
                out.push_str(&inline_text(&anchor.value));
                out.push('\n');
            }
            _ => out.push_str("**Location:** Not available\n"),
        }
        let _ = writeln!(out, "**Score:** {:.6}", hit.score);
        if !hit.metadata_matches.is_empty() {
            out.push_str("**Metadata matches:** ");
            for (position, field) in hit.metadata_matches.iter().enumerate() {
                if position > 0 {
                    out.push_str(", ");
                }
                out.push_str(&inline_text(field));
            }
            out.push('\n');
        }
        out.push('\n');
        out.push_str(&blockquote(&hit.snippet));
        out.push_str("\n\n");
    }
    Ok(out)
}

fn wiki_target(hit: &CliSearchHit) -> String {
    let mut target = hit
        .path
        .split('/')
        .map(escape_wiki_part)
        .collect::<Vec<_>>()
        .join("/");
    if let Some(anchor) = &hit.anchor
        && !anchor.kind.is_empty()
        && !anchor.value.is_empty()
    {
        target.push('#');
        target.push_str(&escape_wiki_part(&anchor.kind));
        target.push(':');
        target.push_str(&escape_wiki_part(&anchor.value));
    }
    target
}

/// `url.PathEscape` followed by `&` -> `%26`.
fn escape_wiki_part(value: &str) -> String {
    let mut out = String::new();
    for byte in value.bytes() {
        let unreserved = byte.is_ascii_alphanumeric() || matches!(byte, b'-' | b'_' | b'.' | b'~');
        let path_safe = matches!(byte, b'$' | b'+' | b'=' | b':' | b'@');
        if unreserved || path_safe {
            out.push(byte as char);
        } else {
            let _ = write!(out, "%{byte:02X}");
        }
    }
    out
}

fn heading_text(value: &str) -> String {
    let value = value.trim().replace(['\r', '\n'], " ");
    let value = value.trim_start_matches('#').to_owned();
    if value.is_empty() {
        "Untitled result".to_owned()
    } else {
        value
    }
}

fn inline_text(value: &str) -> String {
    value.trim().replace(['\r', '\n'], " ").replace('`', "'")
}

fn code_text(value: &str) -> String {
    value.replace(['\r', '\n'], " ").replace('`', "'")
}

fn blockquote(value: &str) -> String {
    value
        .replace("\r\n", "\n")
        .split('\n')
        .map(|line| format!("> {line}"))
        .collect::<Vec<_>>()
        .join("\n")
}
