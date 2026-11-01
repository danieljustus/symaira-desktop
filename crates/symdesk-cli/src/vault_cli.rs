//! `symdesk vault adopt|health` (Go `cmd/symdesk/vault_adopt.go`,
//! `vault_health.go`, `internal/health`).
use std::{
    collections::BTreeMap,
    fs,
    path::Path,
    process::ExitCode,
    time::{Instant, SystemTime, UNIX_EPOCH},
};

use clap::{Arg, ArgAction, ArgMatches, Command};
use noyalib::Value;
use serde::Serialize;
use symdesk_vault::{HistoryStore, backfill_frontmatter, go_quote, parse_bytes, walk_markdown};
use time::OffsetDateTime;

use crate::{emit_error, resolve_vault_root, write_go_json, write_stdout};

pub fn cli() -> Command {
    Command::new("vault")
        .subcommand(crate::vault_health_cli::command())
        .subcommand(
            Command::new("adopt")
                .arg(Arg::new("dir").num_args(0..).hide(true))
                .arg(
                    Arg::new("dry-run")
                        .long("dry-run")
                        .action(ArgAction::SetTrue),
                ),
        )
}

pub fn run(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    match command.subcommand() {
        Some(("adopt", adopt)) => run_adopt(adopt, vault, output_json),
        Some(("health", health)) => crate::vault_health_cli::run(health, vault, output_json),
        _ => crate::process_exit(crate::CoreExitCode::Ok),
    }
}

#[derive(Serialize, Default)]
struct AdoptFileResult {
    path: String,
    status: &'static str,
    #[serde(skip_serializing_if = "String::is_empty")]
    title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    created: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    tags: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    added_fields: Vec<&'static str>,
    #[serde(skip_serializing_if = "String::is_empty")]
    error: String,
}

#[derive(Serialize)]
struct AdoptReport {
    schema_version: u32,
    vault: String,
    started_at: String,
    finished_at: String,
    duration_seconds: f64,
    dry_run: bool,
    total: usize,
    adopted: usize,
    skipped: usize,
    failed: usize,
    documents: Vec<AdoptFileResult>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    warnings: Vec<String>,
}

fn run_adopt(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let dry_run = command.get_flag("dry-run");
    let directory = command
        .get_many::<String>("dir")
        .and_then(|mut values| values.next())
        .map(String::as_str);
    let root = match resolve_vault_root(directory.or(vault)) {
        Ok(root) => root,
        Err(error) => return emit_error(error, output_json),
    };
    let mut sidecar = if dry_run {
        None
    } else {
        symdesk_index::open_for_vault(&root).ok()
    };
    let report = match adopt(&root, dry_run, sidecar.as_mut()) {
        Ok(report) => report,
        Err(error) => return emit_error(error, output_json),
    };
    if let Some(mut sidecar) = sidecar
        && let Err(error) = sidecar.close()
    {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
    if output_json {
        return write_go_json(&report);
    }
    write_stdout(adopt_text(&report))
}

/// Go `health.Adopt`.
fn adopt(
    root: &Path,
    dry_run: bool,
    mut sidecar: Option<&mut symdesk_index::Sidecar>,
) -> Result<AdoptReport, String> {
    let started = Instant::now();
    let started_at = OffsetDateTime::now_utc();
    let mut report = AdoptReport {
        schema_version: 1,
        vault: root.to_string_lossy().into_owned(),
        started_at: symdesk_vault::history::format_rfc3339_nano_utc(started_at),
        finished_at: String::new(),
        duration_seconds: 0.0,
        dry_run,
        total: 0,
        adopted: 0,
        skipped: 0,
        failed: 0,
        documents: Vec::new(),
        warnings: Vec::new(),
    };
    let history = (!dry_run).then(|| HistoryStore::new(root));

    let canonical = fs::canonicalize(root).map_err(|error| format!("walk vault: {error}"))?;
    let mut relative_paths = walk_markdown(root).map_err(|error| format!("walk vault: {error}"))?;
    relative_paths.sort_by(|left, right| left.as_os_str().cmp(right.as_os_str()));
    report.total = relative_paths.len();

    for relative in relative_paths {
        let path = canonical.join(&relative);
        let rel = relative.to_string_lossy().replace('\\', "/");
        let bytes = match fs::read(&path) {
            Ok(bytes) => bytes,
            Err(error) => {
                report.failed += 1;
                report.documents.push(failed(rel, error.to_string()));
                continue;
            }
        };
        let document = match parse_bytes(&path.to_string_lossy(), &bytes) {
            Ok(document) => document,
            Err(error) => {
                report.failed += 1;
                report.documents.push(failed(rel, error.to_string()));
                continue;
            }
        };

        let has_title = matches!(
            document.frontmatter.get("title"),
            Some(Value::String(title)) if !title.trim().is_empty()
        ) && !document.yaml_timestamps.contains_key("title");
        // Go reads `created` with a `.(string)` assertion: an unquoted YAML
        // timestamp decodes to `time.Time`, counts as missing and is
        // backfilled a second time (the oracle's behavior, kept as is).
        let has_created = matches!(
            document.frontmatter.get("created"),
            Some(Value::String(created)) if !created.trim().is_empty()
        ) && !document.yaml_timestamps.contains_key("created");
        let has_tags = document.frontmatter.contains_key("tags");
        if has_title && has_created && has_tags {
            report.skipped += 1;
            report.documents.push(AdoptFileResult {
                path: rel,
                status: "skipped",
                ..AdoptFileResult::default()
            });
            continue;
        }

        let mut missing: BTreeMap<String, Value> = BTreeMap::new();
        let mut added: Vec<&'static str> = Vec::new();
        let title = if has_title {
            document.title.clone()
        } else {
            let derived = extract_h1_title(&document.body).unwrap_or_else(|| {
                let base = path
                    .file_name()
                    .map(|name| name.to_string_lossy().into_owned())
                    .unwrap_or_default();
                base.strip_suffix(".md")
                    .map_or_else(|| base.clone(), str::to_owned)
            });
            missing.insert("title".to_owned(), Value::String(derived.clone()));
            added.push("title");
            derived
        };
        let created = if has_created {
            document.created.clone()
        } else {
            let derived = derive_created_time(&path);
            missing.insert("created".to_owned(), Value::String(derived.clone()));
            added.push("created");
            derived
        };
        let tags = if has_tags {
            document.tags.clone()
        } else {
            missing.insert("tags".to_owned(), Value::Sequence(Vec::new()));
            added.push("tags");
            Vec::new()
        };
        let result = |status| AdoptFileResult {
            path: rel.clone(),
            status,
            title: title.clone(),
            created: created.clone(),
            tags: tags.clone(),
            added_fields: added.clone(),
            error: String::new(),
        };

        if dry_run {
            report.adopted += 1;
            report.documents.push(result("adopted"));
            continue;
        }
        if let Some(history) = &history
            && let Err(error) = history.snapshot(&rel)
        {
            report
                .warnings
                .push(format!("history snapshot failed for {rel}: {error}"));
        }
        if let Err(error) = backfill_frontmatter(&path, &missing) {
            report.failed += 1;
            report.documents.push(failed(rel, error));
            continue;
        }
        if let Some(sidecar) = sidecar.as_deref_mut() {
            let _ = crate::mutate_cli::reindex(sidecar, &path);
        }
        report.adopted += 1;
        report.documents.push(result("adopted"));
    }

    let finished_at = OffsetDateTime::now_utc();
    report.finished_at = symdesk_vault::history::format_rfc3339_nano_utc(finished_at);
    report.duration_seconds = started.elapsed().as_secs_f64();
    Ok(report)
}

fn failed(path: String, error: String) -> AdoptFileResult {
    AdoptFileResult {
        path,
        status: "failed",
        error,
        ..AdoptFileResult::default()
    }
}

/// Go `extractH1Title`: the first non-blank `# ` heading of the body.
fn extract_h1_title(body: &str) -> Option<String> {
    for line in body.split('\n') {
        let line = line.strip_suffix('\r').unwrap_or(line).trim();
        if let Some(title) = line.strip_prefix("# ") {
            let title = title.trim();
            if !title.is_empty() {
                return Some(title.to_owned());
            }
        }
    }
    None
}

/// Go `deriveCreatedTime`: a `YYYY-MM-DD` file name, else the birth time on
/// macOS and Windows, else the modification time (UTC, whole seconds).
fn derive_created_time(path: &Path) -> String {
    let stem = path
        .file_name()
        .map(|name| name.to_string_lossy().into_owned())
        .unwrap_or_default();
    let stem = stem.strip_suffix(".md").unwrap_or(&stem);
    if let Some(date) = parse_daily_date(stem) {
        return format!("{date}T00:00:00Z");
    }
    let metadata = fs::metadata(path).ok();
    let moment = metadata.as_ref().and_then(|metadata| {
        #[cfg(any(target_os = "macos", windows))]
        if let Ok(created) = metadata.created()
            && created > UNIX_EPOCH
        {
            return Some(created);
        }
        metadata.modified().ok()
    });
    let seconds = moment
        .unwrap_or_else(SystemTime::now)
        .duration_since(UNIX_EPOCH)
        .map_or(0, |elapsed| elapsed.as_secs());
    let when = OffsetDateTime::from_unix_timestamp(i64::try_from(seconds).unwrap_or(0))
        .unwrap_or(OffsetDateTime::UNIX_EPOCH);
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}Z",
        when.year(),
        u8::from(when.month()),
        when.day(),
        when.hour(),
        when.minute(),
        when.second()
    )
}

/// Go `time.Parse("2006-01-02", ...)`: a valid calendar date.
fn parse_daily_date(text: &str) -> Option<String> {
    let bytes = text.as_bytes();
    if bytes.len() != 10 || bytes[4] != b'-' || bytes[7] != b'-' {
        return None;
    }
    let number = |range: std::ops::Range<usize>| -> Option<u32> {
        let part = &text[range];
        part.bytes()
            .all(|byte| byte.is_ascii_digit())
            .then(|| part.parse().ok())
            .flatten()
    };
    let (year, month, day) = (number(0..4)?, number(5..7)?, number(8..10)?);
    let month_enum = time::Month::try_from(u8::try_from(month).ok()?).ok()?;
    time::Date::from_calendar_date(
        i32::try_from(year).ok()?,
        month_enum,
        u8::try_from(day).ok()?,
    )
    .ok()?;
    Some(text.to_owned())
}

fn adopt_text(report: &AdoptReport) -> String {
    let mut out = String::new();
    let mode = if report.dry_run { " (dry-run)" } else { "" };
    out.push_str(&format!("Vault adoption{mode}\n"));
    out.push_str(&format!("  Vault:    {}\n", report.vault));
    out.push_str(&format!("  Total:    {}\n", report.total));
    out.push_str(&format!("  Adopted:  {}\n", report.adopted));
    out.push_str(&format!("  Skipped:  {}\n", report.skipped));
    out.push_str(&format!("  Failed:   {}\n", report.failed));
    let mut has_changes = false;
    for document in &report.documents {
        match document.status {
            "adopted" => {
                if !has_changes {
                    out.push_str("\nChanges:\n");
                    has_changes = true;
                }
                out.push_str(&format!("  {}:\n", document.path));
                if !document.title.is_empty() {
                    out.push_str(&format!("    + title: {}\n", go_quote(&document.title)));
                }
                if !document.created.is_empty() {
                    out.push_str(&format!("    + created: {}\n", go_quote(&document.created)));
                }
                if document.tags.is_empty() {
                    out.push_str("    + tags: []\n");
                } else {
                    out.push_str(&format!("    + tags: [{}]\n", document.tags.join(" ")));
                }
            }
            "failed" => out.push_str(&format!("  {}: error: {}\n", document.path, document.error)),
            _ => {}
        }
    }
    for warning in &report.warnings {
        out.push_str(&format!("  warning:  {warning}\n"));
    }
    out
}
