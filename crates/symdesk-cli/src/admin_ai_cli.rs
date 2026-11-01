//! `symdesk ai autofill` and `symdesk notebook generate` (Go `cmd/symdesk/ai.go`,
//! `notebook.go`, `internal/service/autofill.go`, `notebook_generate.go`): the
//! two one-shot LLM commands that write back into the vault.

use std::{collections::BTreeMap, fmt::Write as _, fs, path::Path, process::ExitCode};

use clap::{Arg, ArgAction, ArgMatches, Command};
use serde::Serialize;
use symdesk_index::Sidecar;
use symdesk_service::agent::{CitationWarning, check_citation_warnings};
use symdesk_vault::{go_quote, parse_bytes, render_go_yaml_string, secure_path};

use crate::{
    service_cli::with_deps,
    views_exec_cli::{exec_view, find_view},
    write_go_json, write_stdout,
};

pub fn ai_cli() -> Command {
    Command::new("ai").subcommand(
        Command::new("autofill")
            .arg(Arg::new("extra").num_args(0..).hide(true))
            .arg(Arg::new("view").long("view"))
            .arg(Arg::new("property").long("property"))
            .arg(Arg::new("prompt").long("prompt"))
            .arg(
                Arg::new("dry-run")
                    .long("dry-run")
                    .action(ArgAction::SetTrue),
            ),
    )
}

pub fn generate_cli() -> Command {
    Command::new("generate")
        .arg(Arg::new("args").num_args(0..))
        .arg(Arg::new("kind").long("kind"))
        .arg(
            Arg::new("dry-run")
                .long("dry-run")
                .action(ArgAction::SetTrue),
        )
}

// ---- ai autofill ------------------------------------------------------------

#[derive(Serialize)]
struct AutofillChange {
    path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    old_value: String,
    new_value: String,
    status: &'static str,
}

#[derive(Serialize)]
struct AutofillResult {
    view: String,
    property: String,
    total: usize,
    filled: usize,
    skipped: usize,
    failed: usize,
    dry_run: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    changes: Vec<AutofillChange>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    errors: Vec<BTreeMap<&'static str, String>>,
}

pub fn run_ai(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let Some(("autofill", args)) = command.subcommand() else {
        return crate::process_exit(crate::CoreExitCode::Ok);
    };
    let value = |name: &str| args.get_one::<String>(name).cloned().unwrap_or_default();
    let (view, property, prompt) = (value("view"), value("property"), value("prompt"));
    let dry_run = args.get_flag("dry-run");
    with_deps(vault, json, |root, db| {
        let result = autofill(root, db, &view, &property, &prompt, dry_run)?;
        Ok(if json {
            write_go_json(&result)
        } else {
            write_stdout(autofill_text(&result))
        })
    })
}

fn autofill_text(result: &AutofillResult) -> String {
    let changes = result
        .changes
        .iter()
        .map(|change| {
            format!(
                "{{Path:{} Title:{} OldValue:{} NewValue:{} Status:{}}}",
                change.path, change.title, change.old_value, change.new_value, change.status
            )
        })
        .collect::<Vec<_>>()
        .join(" ");
    let errors = result
        .errors
        .iter()
        .map(|entry| {
            format!(
                "map[{}]",
                entry
                    .iter()
                    .map(|(key, value)| format!("{key}:{value}"))
                    .collect::<Vec<_>>()
                    .join(" ")
            )
        })
        .collect::<Vec<_>>()
        .join(" ");
    format!(
        "&{{View:{} Property:{} Total:{} Filled:{} Skipped:{} Failed:{} DryRun:{} Changes:[{changes}] Errors:[{errors}]}}\n",
        result.view,
        result.property,
        result.total,
        result.filled,
        result.skipped,
        result.failed,
        result.dry_run
    )
}

/// Go `Service.Autofill`.
fn autofill(
    root: &Path,
    db: &mut Sidecar,
    view_id: &str,
    property: &str,
    extra_prompt: &str,
    dry_run: bool,
) -> Result<AutofillResult, String> {
    let property = property.trim();
    if property.is_empty() {
        return Err("property must not be empty".to_owned());
    }
    let view = find_view(root, view_id).map_err(|error| format!("view not found: {error}"))?;
    let source = view.source.trim();
    if source.starts_with("dataset:") {
        return Err(format!(
            "unsupported dataset source {}: generic view AI operations only support note-backed views",
            go_quote(source)
        ));
    }
    let rows = exec_view(root, db, &view)
        .map_err(|error| format!("failed to evaluate view: {error}"))?
        .unwrap_or_default();
    let mut result = AutofillResult {
        view: view.name.clone(),
        property: property.to_owned(),
        total: rows.len(),
        filled: 0,
        skipped: 0,
        failed: 0,
        dry_run,
        changes: Vec::new(),
        errors: Vec::new(),
    };
    let prefix = format!("{}/", root.to_string_lossy());
    for row in &rows {
        let path = row
            .get("_path")
            .and_then(serde_json::Value::as_str)
            .unwrap_or_default();
        if path.is_empty() {
            result.skipped += 1;
            continue;
        }
        let relative = path.strip_prefix(&prefix).unwrap_or(path).to_owned();
        let title = row
            .get("_title")
            .and_then(serde_json::Value::as_str)
            .unwrap_or_default()
            .to_owned();
        let existing = match row.get(property) {
            Some(value) if !value.is_null() => crate::dataset::go_debug_value(value),
            _ => String::new(),
        };
        if !existing.trim().is_empty() && existing != "<nil>" {
            result.skipped += 1;
            result.changes.push(AutofillChange {
                path: relative,
                title,
                old_value: String::new(),
                new_value: String::new(),
                status: "skipped",
            });
            continue;
        }
        let fail = |result: &mut AutofillResult, error: String| {
            result.failed += 1;
            result.errors.push(BTreeMap::from([
                ("error", error),
                ("path", relative.clone()),
            ]));
        };
        let document = match symdesk_render::export_vault::parse_file_in_root(root, Path::new(path))
        {
            Ok(document) => document,
            Err(error) => {
                fail(&mut result, error);
                continue;
            }
        };
        let mut instruction = format!(
            "Extract the value for the property {} from the note below. ",
            go_quote(property)
        );
        if !extra_prompt.is_empty() {
            let _ = write!(instruction, "{extra_prompt} ");
        }
        instruction.push_str(
            "Return only the value, no markdown, no quotes, no explanation. \
             If the note does not contain the information, return an empty line.\n\n",
        );
        if !document.title.is_empty() {
            let _ = writeln!(instruction, "Title: {}", document.title);
        }
        instruction.push_str(&document.body);
        instruction.push('\n');
        let value = match crate::ai_cli::prompt_one(&instruction) {
            Ok(value) => value,
            Err(error) => {
                fail(&mut result, error);
                continue;
            }
        };
        if value.is_empty() {
            result.skipped += 1;
            result.changes.push(AutofillChange {
                path: relative,
                title,
                old_value: String::new(),
                new_value: String::new(),
                status: "skipped",
            });
            continue;
        }
        let mut change = AutofillChange {
            path: relative.clone(),
            title,
            old_value: existing,
            new_value: value.clone(),
            status: "pending",
        };
        if dry_run {
            result.filled += 1;
        } else {
            if let Err(error) = symdesk_vault::set_frontmatter_value(
                Path::new(path),
                property,
                &noyalib::Value::from(value),
            ) {
                fail(&mut result, error.to_string());
                continue;
            }
            change.status = "applied";
            result.filled += 1;
        }
        result.changes.push(change);
    }
    Ok(result)
}

// ---- notebook generate ------------------------------------------------------

#[derive(Serialize)]
struct GenerateResult {
    path: String,
    kind: String,
    content: String,
    sources: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    citation_warnings: Vec<CitationWarning>,
    dry_run: bool,
}

const BUILTIN_KINDS: [(&str, &str); 4] = [
    (
        "briefing",
        "Write a concise briefing document. Summarize the key points, decisions and open questions across the sources below. Use clear Markdown headings and keep it skimmable.",
    ),
    (
        "faq",
        "Write a FAQ covering the sources below: the 8-12 most important questions a reader would have, each followed by a concise answer.",
    ),
    (
        "study-guide",
        "Write a study guide covering the sources below: the key concepts and definitions a reader must know, followed by a short quiz of 5 questions with answers underneath.",
    ),
    (
        "timeline",
        "Write a chronological timeline of the events, dates and milestones mentioned in the sources below. If the sources contain no dates, say so explicitly instead of inventing one.",
    ),
];

/// Go `Service.NotebookGenerate`, then the command's output.
pub fn notebook_generate(
    root: &Path,
    db: &mut Sidecar,
    reference: &str,
    command: &ArgMatches,
    json: bool,
) -> Result<ExitCode, String> {
    let kind = command
        .get_one::<String>("kind")
        .cloned()
        .unwrap_or_default();
    let dry_run = command.get_flag("dry-run");
    let result = generate(root, db, reference, &kind, dry_run)?;
    Ok(if json {
        write_go_json(&result)
    } else {
        let warnings = result
            .citation_warnings
            .iter()
            .map(|warning| format!("{{Path:{} Line:{}}}", warning.path, warning.line))
            .collect::<Vec<_>>()
            .join(" ");
        write_stdout(format!(
            "&{{Path:{} Kind:{} Content:{} Sources:[{}] CitationWarnings:[{warnings}] DryRun:{}}}\n",
            result.path,
            result.kind,
            result.content,
            result.sources.join(" "),
            result.dry_run
        ))
    })
}

fn generate(
    root: &Path,
    db: &mut Sidecar,
    reference: &str,
    kind: &str,
    dry_run: bool,
) -> Result<GenerateResult, String> {
    let kind = kind.trim();
    if kind.is_empty() {
        return Err("kind is required".to_owned());
    }
    let notebook = symdesk_vault::load_notebook(root, reference)?;
    let instruction = artifact_instruction(root, kind)?;
    let (hits, scoped) = symdesk_protocol::search_notebook_ask_sources(root, reference, "", db)?;
    if hits.is_empty() {
        return Err(format!(
            "notebook {} has no existing sources to generate from",
            go_quote(&notebook.id)
        ));
    }
    let mut prompt = format!(
        "{instruction}\n\nAnswer using only the sources below. Cite them as [[path]] under a \"## Sources\" heading at the end. If the sources do not contain enough information for part of the request, say so explicitly rather than inventing content.\n\n"
    );
    for hit in &hits {
        let snippet = if hit.snippet.len() > 3000 {
            String::from_utf8_lossy(&hit.snippet.as_bytes()[..3000]).into_owned()
        } else {
            hit.snippet.clone()
        };
        let _ = write!(
            prompt,
            "--- Source [[{}]] ({}) ---\n{snippet}\n\n",
            hit.path, hit.title
        );
    }
    let content =
        crate::ai_cli::prompt_one(&prompt).map_err(|error| format!("generate {kind}: {error}"))?;
    if content.trim().is_empty() {
        return Err(format!("generate {kind}: model returned an empty response"));
    }
    let warnings = check_citation_warnings(&content, &scoped);
    let relative = clean_slash(&format!("notebooks/{}/{kind}.md", notebook.id));
    let result = GenerateResult {
        path: relative.clone(),
        kind: kind.to_owned(),
        content: content.clone(),
        sources: scoped.clone(),
        citation_warnings: warnings,
        dry_run,
    };
    if dry_run {
        return Ok(result);
    }
    write_artifact(root, db, &notebook, kind, &relative, &content, &scoped)?;
    Ok(result)
}

/// Go `resolveArtifactInstruction`: a vault template wins over a built-in kind.
fn artifact_instruction(root: &Path, kind: &str) -> Result<String, String> {
    let template = clean_slash(&format!("templates/notebook-{kind}.md"));
    if let Ok(path) = secure_path(root, &template)
        && let Ok(data) = symdesk_render::export_vault::read_in_root(root, &path)
    {
        if let Ok(document) = parse_bytes(&path.to_string_lossy(), &data)
            && !document.body.trim().is_empty()
        {
            return Ok(document.body.trim().to_owned());
        }
        let body = String::from_utf8_lossy(&data).trim().to_owned();
        if !body.is_empty() {
            return Ok(body);
        }
    }
    if let Some((_, instruction)) = BUILTIN_KINDS.iter().find(|(name, _)| *name == kind) {
        return Ok((*instruction).to_owned());
    }
    let known = BUILTIN_KINDS.map(|(name, _)| name).join(", ");
    Err(format!(
        "unknown artifact kind {} (built-in kinds: {known}; or add templates/notebook-{kind}.md)",
        go_quote(kind)
    ))
}

fn write_artifact(
    root: &Path,
    db: &mut Sidecar,
    notebook: &symdesk_vault::Notebook,
    kind: &str,
    relative: &str,
    content: &str,
    sources: &[String],
) -> Result<(), String> {
    let absolute = secure_path(root, relative).map_err(|error| error.to_string())?;
    if let Some(parent) = absolute.parent() {
        create_dir_0750(parent)
            .map_err(|error| format!("create notebook artifact directory: {error}"))?;
    }
    let title = format!("{} — {}", notebook.title, kind_title(kind));
    let scalar = |value: &str| {
        render_go_yaml_string(value, 4, false)
            .map(|text| text.trim_end_matches('\n').to_owned())
            .map_err(|error| format!("encode artifact frontmatter: {error}"))
    };
    let created = time::OffsetDateTime::now_utc()
        .replace_nanosecond(0)
        .map_err(|error| error.to_string())?
        .format(&time::format_description::well_known::Rfc3339)
        .map_err(|error| error.to_string())?;
    let mut front = String::new();
    let _ = writeln!(front, "title: {}", scalar(&title)?);
    let _ = writeln!(front, "created: {}", scalar(&created)?);
    front.push_str("tags:\n    - notebook-artifact\n");
    let _ = writeln!(front, "    - {}", scalar(kind)?);
    let _ = writeln!(front, "notebook_id: {}", scalar(&notebook.id)?);
    let _ = writeln!(front, "artifact_kind: {}", scalar(kind)?);
    if sources.is_empty() {
        front.push_str("sources: []\n");
    } else {
        front.push_str("sources:\n");
        for source in sources {
            let _ = writeln!(front, "    - {}", scalar(source)?);
        }
    }
    let mut body = format!("# {title}\n\n{content}");
    if !content.ends_with('\n') {
        body.push('\n');
    }
    let full = format!("---\n{front}---\n\n{body}");
    crate::mutate_cli::snapshot(root, &absolute);
    write_private(&absolute, full.as_bytes())
        .map_err(|error| format!("write artifact: {error}"))?;
    crate::mutate_cli::parse(&absolute)
        .map_err(|error| format!("wrote artifact but failed to parse for indexing: {error}"))?;
    crate::mutate_cli::reindex(db, &absolute)
        .map_err(|error| format!("wrote artifact but failed to index: {error}"))?;
    Ok(())
}

/// Go `artifactKindTitle`.
fn kind_title(kind: &str) -> String {
    kind.split(['-', '_'])
        .filter(|word| !word.is_empty())
        .map(|word| {
            let mut characters = word.chars();
            characters.next().map_or_else(String::new, |first| {
                first.to_uppercase().collect::<String>() + characters.as_str()
            })
        })
        .collect::<Vec<_>>()
        .join(" ")
}

fn create_dir_0750(path: &Path) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        fs::DirBuilder::new()
            .recursive(true)
            .mode(0o750)
            .create(path)
    }
    #[cfg(not(unix))]
    {
        fs::create_dir_all(path)
    }
}

fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
    use std::io::Write as _;
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)?.write_all(data)
}

/// Go `filepath.Join` of slash segments (a lexical clean).
fn clean_slash(path: &str) -> String {
    let mut parts: Vec<&str> = Vec::new();
    for part in path.split('/') {
        match part {
            "" | "." => {}
            ".." => {
                if parts.last().is_some_and(|last| *last != "..") {
                    parts.pop();
                } else {
                    parts.push("..");
                }
            }
            value => parts.push(value),
        }
    }
    let joined = parts.join("/");
    if joined.is_empty() {
        ".".to_owned()
    } else {
        joined
    }
}
