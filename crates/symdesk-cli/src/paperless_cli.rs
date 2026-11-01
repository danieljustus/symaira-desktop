//! `symdesk paperless import|migrate` (Go `cmd/symdesk/paperless.go`,
//! `paperless_migrate.go`).
//!
//! `import` replays an export directory through the filesystem importer and
//! indexes the written notes. `migrate` runs the API importer in-process with
//! the symingest configuration (vault, store, archive, OCR engine) exactly
//! like `ingest.PaperlessMigrate`.
//!
//! Declared differences from Go: cancellation by signal is not wired (the run
//! always completes); `time.Parse` wording for `--since` is reproduced for
//! the RFC 3339 layout only.

use std::{
    path::{Path, PathBuf},
    process::ExitCode,
    sync::{Arc, Mutex},
};

use clap::{Arg, ArgAction, ArgMatches, Command};
use serde::Serialize;
use symdesk_ingest::{
    Store,
    paperless_export::{ImportOptions, import},
    paperless_run::{Options, run as run_import},
    pipeline::{Engine, Pipeline},
    writer::NoteWriter,
};
use symdesk_vault::{go_path_error, go_quote};

use crate::{
    config_paths::resolve_ingest, emit_error, mutate_cli::reindex, service_cli::with_deps,
    write_go_json, write_stdout,
};

pub fn cli() -> Command {
    Command::new("paperless")
        .subcommand(
            Command::new("import")
                .arg(Arg::new("args").num_args(0..))
                .arg(
                    Arg::new("dry-run")
                        .long("dry-run")
                        .action(ArgAction::SetTrue),
                ),
        )
        .subcommand(
            Command::new("migrate")
                // Go's `migrate` has no argument validator: extra words are ignored.
                .arg(Arg::new("extra").num_args(0..).hide(true))
                .arg(Arg::new("base-url").long("base-url").num_args(1))
                .arg(Arg::new("token").long("token").num_args(1))
                .arg(Arg::new("since").long("since").num_args(1))
                .arg(
                    Arg::new("dry-run")
                        .long("dry-run")
                        .action(ArgAction::SetTrue),
                )
                .arg(Arg::new("plan").long("plan").action(ArgAction::SetTrue))
                .arg(Arg::new("resume").long("resume").action(ArgAction::SetTrue))
                .arg(
                    Arg::new("deep-verify")
                        .long("deep-verify")
                        .action(ArgAction::SetTrue),
                )
                .arg(
                    Arg::new("retry-failed")
                        .long("retry-failed")
                        .action(ArgAction::SetTrue),
                )
                .arg(Arg::new("concurrency").long("concurrency").num_args(1))
                .arg(Arg::new("limit").long("limit").num_args(1)),
        )
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    match command.subcommand() {
        Some(("import", args)) => run_import_command(args, vault, json),
        Some(("migrate", args)) => run_migrate(args, json),
        _ => crate::process_exit(crate::CoreExitCode::Ok),
    }
}

fn format_dry_run(dry_run: bool) -> &'static str {
    if dry_run { "(dry-run)" } else { "" }
}

fn run_import_command(args: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let export = args
        .get_many::<String>("args")
        .and_then(|mut values| values.next().cloned())
        .unwrap_or_default();
    let export_dir = PathBuf::from(&export);
    match std::fs::metadata(&export_dir) {
        Err(error) => {
            return emit_error(
                format!(
                    "export directory not found: {}",
                    go_path_error("stat", &export_dir, &error)
                ),
                json,
            );
        }
        Ok(metadata) if !metadata.is_dir() => {
            return emit_error(format!("export path is not a directory: {export}"), json);
        }
        Ok(_) => {}
    }
    let dry_run = args.get_flag("dry-run");
    with_deps(vault, json, |root, sidecar| {
        let options = ImportOptions {
            vault_root: root.to_path_buf(),
            export_dir,
            dry_run,
        };
        let summary = import(&options).map_err(|error| error.to_string())?;
        if !dry_run {
            // Go indexes each written note through the open sidecar.
            for result in summary.results.iter().flatten() {
                if matches!(result.action.as_str(), "created" | "updated") {
                    let note = Path::new(&result.note_path);
                    let absolute = if note.is_absolute() {
                        note.to_path_buf()
                    } else {
                        root.join(note)
                    };
                    let _ = reindex(sidecar, &absolute);
                }
            }
        }
        if json {
            return Ok(write_go_json(&summary));
        }
        let mut out = format!(
            "Paperless import {}\n  Total:   {}\n  Created: {}\n  Updated: {}\n",
            format_dry_run(dry_run),
            summary.total,
            summary.created,
            summary.updated
        );
        if summary.skipped > 0 {
            out.push_str(&format!("  Skipped: {}\n", summary.skipped));
        }
        if summary.errors > 0 {
            out.push_str(&format!("  Errors:  {}\n", summary.errors));
        }
        let mut errors = String::new();
        for result in summary.results.iter().flatten() {
            let asn = if result.asn > 0 {
                format!(" (ASN {})", result.asn)
            } else {
                String::new()
            };
            match result.action.as_str() {
                "error" => errors.push_str(&format!(
                    "  ✗ #{} {}: {}\n",
                    result.paperless_id,
                    go_quote(&result.title),
                    result.error
                )),
                "created" => out.push_str(&format!(
                    "  + #{} {} → {}{asn}\n",
                    result.paperless_id,
                    go_quote(&result.title),
                    result.note_path
                )),
                "updated" => out.push_str(&format!(
                    "  ↻ #{} {} → {}{asn}\n",
                    result.paperless_id,
                    go_quote(&result.title),
                    result.note_path
                )),
                "skipped_idempotent" => out.push_str(&format!(
                    "  = #{} {} (unchanged)\n",
                    result.paperless_id,
                    go_quote(&result.title)
                )),
                _ => {}
            }
        }
        if !errors.is_empty() {
            eprint!("{errors}");
        }
        Ok(write_stdout(out))
    })
}

#[derive(Serialize)]
struct MigrateStats {
    total: usize,
    imported: usize,
    skipped: usize,
    failed: usize,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    warnings: Vec<String>,
}

/// Go `time.Parse(time.RFC3339, text)`: the date (`2006-01-02` of the text,
/// as Go formats it in the parsed zone) or Go's parse error.
fn parse_rfc3339_since(text: &str) -> Result<Option<String>, String> {
    let layout = "2006-01-02T15:04:05Z07:00";
    let fail = |rest: &str, element: &str| {
        format!(
            "parsing time {} as {}: cannot parse {} as {}",
            go_quote(text),
            go_quote(layout),
            go_quote(rest),
            go_quote(element)
        )
    };
    let range = |what: &str| format!("parsing time {}: {what} out of range", go_quote(text));
    let bytes = text.as_bytes();
    let mut at = 0;
    let number = |at: &mut usize, width: usize| -> Option<i64> {
        let slice = bytes.get(*at..*at + width)?;
        if !slice.iter().all(u8::is_ascii_digit) {
            return None;
        }
        *at += width;
        std::str::from_utf8(slice).ok()?.parse().ok()
    };
    let literal = |at: &mut usize, expected: u8| -> bool {
        if bytes.get(*at) == Some(&expected) {
            *at += 1;
            true
        } else {
            false
        }
    };
    let year = number(&mut at, 4).ok_or_else(|| fail(&text[at..], "2006"))?;
    if !literal(&mut at, b'-') {
        return Err(fail(&text[at..], "-"));
    }
    let month = number(&mut at, 2).ok_or_else(|| fail(&text[at..], "01"))?;
    if !(1..=12).contains(&month) {
        return Err(range("month"));
    }
    if !literal(&mut at, b'-') {
        return Err(fail(&text[at..], "-"));
    }
    let day = number(&mut at, 2).ok_or_else(|| fail(&text[at..], "02"))?;
    if !(1..=31).contains(&day) {
        return Err(range("day"));
    }
    if !literal(&mut at, b'T') {
        return Err(fail(&text[at..], "T"));
    }
    let hour = number(&mut at, 2).ok_or_else(|| fail(&text[at..], "15"))?;
    if hour > 23 {
        return Err(range("hour"));
    }
    if !literal(&mut at, b':') {
        return Err(fail(&text[at..], ":"));
    }
    let minute = number(&mut at, 2).ok_or_else(|| fail(&text[at..], "04"))?;
    if minute > 59 {
        return Err(range("minute"));
    }
    if !literal(&mut at, b':') {
        return Err(fail(&text[at..], ":"));
    }
    let second = number(&mut at, 2).ok_or_else(|| fail(&text[at..], "05"))?;
    if second > 59 {
        return Err(range("second"));
    }
    if matches!(bytes.get(at), Some(b'.' | b',')) {
        let mut end = at + 1;
        while bytes.get(end).is_some_and(u8::is_ascii_digit) {
            end += 1;
        }
        if end > at + 1 {
            at = end;
        }
    }
    let utc_zero;
    if literal(&mut at, b'Z') {
        utc_zero = true;
    } else if matches!(bytes.get(at), Some(b'+' | b'-')) {
        let sign = bytes[at];
        at += 1;
        let hours = number(&mut at, 2).ok_or_else(|| fail(&text[at - 1..], "Z07:00"))?;
        if !literal(&mut at, b':') {
            return Err(fail(&text[at - 3..], "Z07:00"));
        }
        let minutes = number(&mut at, 2).ok_or_else(|| fail(&text[at - 3..], "Z07:00"))?;
        if hours > 24 {
            return Err(format!(
                "parsing time {}: time zone offset hour out of range",
                go_quote(text)
            ));
        }
        if minutes > 60 {
            return Err(format!(
                "parsing time {}: time zone offset minute out of range",
                go_quote(text)
            ));
        }
        utc_zero = hours == 0 && minutes == 0 && sign == b'+';
    } else {
        return Err(fail(&text[at..], "Z07:00"));
    }
    if at < text.len() {
        return Err(format!(
            "parsing time {}: extra text: {}",
            go_quote(text),
            go_quote(&text[at..])
        ));
    }
    let days_in_month = match month {
        4 | 6 | 9 | 11 => 30,
        2 if (year % 4 == 0 && year % 100 != 0) || year % 400 == 0 => 29,
        2 => 28,
        _ => 31,
    };
    if day > days_in_month {
        return Err(range("day"));
    }
    // Go's zero time (`IsZero`) means "no lower bound".
    if utc_zero && year == 1 && month == 1 && day == 1 && hour == 0 && minute == 0 && second == 0 {
        return Ok(None);
    }
    Ok(Some(text[..10].to_owned()))
}

/// Go `slog` text handler line for a `log.Printf` message (the CLI installs
/// the text handler as the default logger): `time=... level=INFO msg=...`.
fn slog_info(message: &str) {
    let now = time::OffsetDateTime::now_utc();
    let now = now.to_offset(crate::local_offset_at(now));
    let format = time::format_description::parse_borrowed::<2>(
        "[year]-[month]-[day]T[hour]:[minute]:[second].[subsecond digits:3]",
    )
    .expect("static slog timestamp");
    let stamp = now.format(&format).unwrap_or_default();
    let zone = if now.offset() == time::UtcOffset::UTC {
        "Z".to_owned()
    } else {
        now.format(
            &time::format_description::parse_borrowed::<2>(
                "[offset_hour sign:mandatory]:[offset_minute]",
            )
            .expect("static offset format"),
        )
        .unwrap_or_default()
    };
    let needs_quoting = message.is_empty()
        || message
            .chars()
            .any(|c| c == ' ' || c == '=' || c == '"' || c.is_control() || c.is_whitespace());
    let rendered = if needs_quoting {
        go_quote(message)
    } else {
        message.to_owned()
    };
    eprintln!("time={stamp}{zone} level=INFO msg={rendered}");
}

fn int_flag(args: &ArgMatches, name: &str) -> i64 {
    args.get_one::<String>(name)
        .and_then(|raw| crate::duplicates_cli::parse_go_int(raw).ok())
        .unwrap_or(0)
}

fn run_migrate(args: &ArgMatches, json: bool) -> ExitCode {
    let flag = |name: &str| args.get_one::<String>(name).cloned().unwrap_or_default();
    let base_url = flag("base-url");
    let token = flag("token");
    if base_url.is_empty() {
        return emit_error("--base-url is required".to_owned(), json);
    }
    if token.is_empty() {
        return emit_error("--token is required".to_owned(), json);
    }
    let since_text = flag("since");
    let mut since = String::new();
    if !since_text.is_empty() {
        match parse_rfc3339_since(&since_text) {
            Ok(date) => since = date.unwrap_or_default(),
            Err(error) => {
                return emit_error(format!("invalid --since (want RFC3339): {error}"), json);
            }
        }
    }
    let dry_run = args.get_flag("dry-run");
    let concurrency = usize::try_from(int_flag(args, "concurrency").max(0)).unwrap_or(0);
    let limit = usize::try_from(int_flag(args, "limit").max(0)).unwrap_or(0);
    let migrate = || -> Result<MigrateStats, String> {
        // ingest.PaperlessMigrate
        let resolved = resolve_ingest()?;
        if resolved.vault.is_empty() {
            return Err("no vault configured".to_owned());
        }
        let store = Store::open(Path::new(&resolved.db_path))
            .map_err(|error| format!("open document store: {error}"))?;
        let store = Arc::new(Mutex::new(store));
        let mut pipeline = Pipeline::new(Arc::clone(&store), NoteWriter::new(&resolved.vault));
        let engine: Arc<dyn Engine> = if resolved.ollama_model.is_empty() {
            Arc::new(symdesk_ingest::ocr::Runner::default_runner(
                &resolved.ocr_lang,
            ))
        } else {
            Arc::new(symdesk_ingest::vlm::VlmRunner::new(
                &resolved.ollama_base_url,
                &resolved.ollama_model,
                &resolved.ocr_lang,
                None,
            ))
        };
        pipeline.engine = Some(engine);
        pipeline.archive_dir.clone_from(&resolved.archive);
        pipeline.vault_root.clone_from(&resolved.vault);
        pipeline.log = Box::new(slog_info);
        let options = Options {
            base_url,
            token,
            since,
            dry_run,
            plan: args.get_flag("plan"),
            resume: args.get_flag("resume"),
            deep_verify: args.get_flag("deep-verify"),
            retry_failed: args.get_flag("retry-failed"),
            concurrency,
            limit,
            target_vault: resolved.vault.clone(),
            target_archive: resolved.archive.clone(),
            log: Arc::new(slog_info),
            ..Options::default()
        };
        let outcome = run_import(&options, &pipeline);
        if let Some(error) = outcome.error {
            return Err(error);
        }
        let stats = outcome.stats.unwrap_or_default();
        Ok(MigrateStats {
            total: stats.total,
            imported: stats.imported,
            skipped: stats.skipped,
            failed: stats.failed,
            warnings: stats.warnings,
        })
    };
    let stats = match migrate() {
        Ok(stats) => stats,
        Err(error) => return emit_error(format!("paperless migration failed: {error}"), json),
    };
    if json {
        return write_go_json(&stats);
    }
    let mut out = format!(
        "Paperless migration {}\n  Total:    {}\n  Imported: {}\n  Skipped:  {}\n  Failed:   {}\n",
        format_dry_run(dry_run),
        stats.total,
        stats.imported,
        stats.skipped,
        stats.failed
    );
    for warning in &stats.warnings {
        out.push_str(&format!("  warning:  {warning}\n"));
    }
    write_stdout(out)
}
