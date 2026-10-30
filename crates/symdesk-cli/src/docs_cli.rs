//! Document listing and review CLI, backed by the shared sidecar.
use std::ffi::OsString;

/// Preserve pflag's sequential error handling and last-value-wins globals,
/// without changing the parser contract of unrelated command groups.
pub fn preprocess(args: &mut Vec<OsString>) -> Option<ExitCode> {
    let strings = args
        .iter()
        .map(|v| v.to_str())
        .collect::<Option<Vec<_>>>()?;
    let mut positionals = Vec::new();
    let mut i = 1;
    while i < strings.len() && positionals.len() < 2 {
        let token = strings[i];
        if token == "--" {
            return None;
        }
        if token == "--vault" || token == "--output" {
            i += 2;
            continue;
        }
        if !token.starts_with('-') {
            positionals.push(token);
        }
        i += 1;
    }
    if positionals.first() != Some(&"docs")
        || !positionals
            .get(1)
            .is_some_and(|v| matches!(*v, "list" | "review"))
    {
        return None;
    }
    let review = positionals[1] == "review";
    let mut json = false;
    let mut last = std::collections::BTreeMap::new();
    let mut remove = std::collections::BTreeSet::new();
    i = 1;
    while i < strings.len() {
        let token = strings[i];
        if token == "--" {
            break;
        }
        if !token.starts_with('-') {
            i += 1;
            continue;
        }
        let (flag, inline) = token
            .split_once('=')
            .map_or((token, None), |(a, b)| (a, Some(b)));
        if flag == "--json" && inline.is_none() {
            json = true;
            if let Some((old, _)) = last.insert(flag, (i, false)) {
                remove.insert(old);
            }
            i += 1;
            continue;
        }
        if flag == "--help" || flag == "-h" {
            return None;
        }
        let known = matches!(flag, "--vault" | "--output")
            || if review {
                flag == "--threshold"
            } else {
                matches!(
                    flag,
                    "--type"
                        | "--file-type"
                        | "--status"
                        | "--person"
                        | "--correspondent"
                        | "--year"
                        | "--due-before"
                        | "--min-confidence"
                        | "--max-confidence"
                        | "--asn"
                )
            };
        if !known {
            let error = if flag.starts_with("--") {
                format!("unknown flag: {flag}")
            } else {
                format!(
                    "unknown shorthand flag: '{}' in {token}",
                    token.chars().nth(1).unwrap_or('-')
                )
            };
            return Some(emit_error(error, json));
        }
        let value = if let Some(value) = inline {
            value
        } else if let Some(value) = strings.get(i + 1) {
            value
        } else {
            return Some(emit_error(format!("flag needs an argument: {flag}"), json));
        };
        if matches!(
            flag,
            "--threshold" | "--min-confidence" | "--max-confidence" | "--asn"
        ) && let Err(reason) = crate::duplicates_cli::parse_go_int(value)
        {
            return Some(emit_error(
                format!(
                    "invalid argument {} for {} flag: strconv.ParseInt: parsing {}: {reason}",
                    go_quote(value),
                    go_quote(flag),
                    go_quote(value)
                ),
                json,
            ));
        }
        if matches!(flag, "--vault" | "--output")
            && let Some((old, had_value)) = last.insert(flag, (i, inline.is_none()))
        {
            remove.insert(old);
            if had_value {
                remove.insert(old + 1);
            }
        }
        i += if inline.is_some() { 1 } else { 2 };
    }
    let mut index = 0;
    args.retain(|_| {
        let keep = !remove.contains(&index);
        index += 1;
        keep
    });
    None
}
use crate::{emit_error, resolve_vault, write_stdout};
use clap::{Arg, ArgMatches, Command};
use std::process::ExitCode;
use symdesk_index::{docs::DocsFilter, open_for_vault};
use symdesk_vault::go_quote;

pub fn cli() -> Command {
    let mut list = Command::new("list").arg(Arg::new("extra").num_args(0..));
    for flag in [
        "type",
        "file-type",
        "status",
        "person",
        "correspondent",
        "year",
        "due-before",
        "min-confidence",
        "max-confidence",
        "asn",
    ] {
        list = list.arg(
            Arg::new(flag)
                .long(flag)
                .num_args(1)
                .allow_hyphen_values(true)
                .overrides_with(flag),
        );
    }
    Command::new("docs").subcommand(list).subcommand(
        Command::new("review")
            .arg(Arg::new("extra").num_args(0..))
            .arg(
                Arg::new("threshold")
                    .long("threshold")
                    .num_args(1)
                    .allow_hyphen_values(true)
                    .overrides_with("threshold"),
            ),
    )
}

fn text(command: &ArgMatches, key: &str) -> String {
    command.get_one::<String>(key).cloned().unwrap_or_default()
}
fn number(command: &ArgMatches, key: &str) -> Result<i64, String> {
    let Some(raw) = command.get_one::<String>(key) else {
        return Ok(0);
    };
    crate::duplicates_cli::parse_go_int(raw).map_err(|reason| {
        format!(
            "invalid argument {} for \"--{key}\" flag: strconv.ParseInt: parsing {}: {reason}",
            go_quote(raw),
            go_quote(raw)
        )
    })
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    if let Some((name, sub)) = command.subcommand() {
        let keys: &[&str] = if name == "list" {
            &["min-confidence", "max-confidence", "asn"]
        } else {
            &["threshold"]
        };
        for key in keys {
            if let Err(error) = number(sub, key) {
                return crate::write_stderr(&format!("{error}\n"), crate::CoreExitCode::Generic);
            }
        }
    }
    let result = execute(command, vault, json);
    match result {
        Ok(out) => write_stdout(out),
        Err(error) => emit_error(error, json),
    }
}
fn execute(command: &ArgMatches, vault: Option<&str>, json: bool) -> Result<String, String> {
    let Some((name, sub)) = command.subcommand() else {
        return Ok(String::new());
    };
    // pflag validates integer syntax before the persistent pre-run opens config/vault.
    let keys: &[&str] = if name == "list" {
        &["min-confidence", "max-confidence", "asn"]
    } else {
        &["threshold"]
    };
    for key in keys {
        number(sub, key)?;
    }
    let root = resolve_vault(vault).map_err(|e| {
        if e.starts_with("vault path not configured") {
            "vault path not configured (use flag or SYMDESK_VAULT env)".into()
        } else {
            crate::index_cli::index_vault_error(vault, &e)
        }
    })?;
    let mut db = open_for_vault(&root).map_err(|e| e.to_string())?;
    let result = (|| {
        if name == "list" {
            let min = number(sub, "min-confidence")?;
            let max = number(sub, "max-confidence")?;
            let asn = sub
                .get_one::<String>("asn")
                .map(|_| number(sub, "asn"))
                .transpose()?;
            if asn.is_some_and(|v| v <= 0) {
                return Err("invalid --asn: must be a positive integer".into());
            }
            let filter = DocsFilter {
                document_type: text(sub, "type"),
                file_type: text(sub, "file-type"),
                status: text(sub, "status"),
                person: text(sub, "person"),
                correspondent: text(sub, "correspondent"),
                year: text(sub, "year"),
                due_before: text(sub, "due-before"),
                min_confidence: (min > 0).then_some(min),
                max_confidence: (max > 0).then_some(max),
                asn,
            };
            let mut rows = db.docs_list(&filter).map_err(|e| e.to_string())?;
            for row in &mut rows {
                row.path = crate::native_relative_path(&root, &row.path);
            }
            if json {
                return serde_json::to_string(&rows)
                    .map(|s| format!("{}\n", crate::go_escape_json(s)))
                    .map_err(|e| e.to_string());
            }
            let rows = rows.iter().map(|r| format!("{{Path:{} Title:{} Type:{} DocumentDate:{} Person:{} Status:{} DueDate:{} Confidence:{} Correspondent:{} DocumentType:{} ASN:{} Tags:[{}]}}",r.path,r.title,r.file_type,r.document_date,r.person,r.status,r.due_date,r.confidence,r.correspondent,r.document_type,r.asn,r.tags.join(" "))).collect::<Vec<_>>();
            Ok(format!("[{}]\n", rows.join(" ")))
        } else {
            let threshold = number(sub, "threshold")?;
            let threshold = if threshold > 0 {
                threshold
            } else {
                crate::config_paths::load_root_config()?.0.review_threshold
            };
            let rows = db.docs_review(threshold).map_err(|e| e.to_string())?;
            if json {
                if rows.is_empty() {
                    return Ok("null\n".into());
                }
                return serde_json::to_string(&rows)
                    .map(|s| format!("{}\n", crate::go_escape_json(s)))
                    .map_err(|e| e.to_string());
            }
            let rows = rows.iter().map(|r|format!("{{Path:{} Title:{} Status:{} DocumentType:{} DocumentDate:{} Confidence:{} Reasons:[{}]}}",r.path,r.title,r.status,r.document_type,r.document_date,r.confidence,r.reasons.join(" "))).collect::<Vec<_>>();
            Ok(format!("[{}]\n", rows.join(" ")))
        }
    })();
    if let Err(e) = db.close() {
        eprintln!("warning: failed to close sidecar database: {e}");
    }
    result
}
