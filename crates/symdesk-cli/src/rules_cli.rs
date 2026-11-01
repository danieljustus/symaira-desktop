//! `symdesk rules list|add|update|delete|test|dry-run` (Go
//! `cmd/symdesk/rules.go`, `internal/ingest/api.go`): classification rules in
//! the symingest document store.
use std::{path::Path, process::ExitCode};

use clap::{Arg, ArgMatches, Command};
use serde::Serialize;
use symaira_core_exit::ExitCode as CoreExitCode;
use symdesk_ingest::{ClassificationRule, validate_classification_rule};
use symdesk_vault::{go_lowercase, go_quote};

use crate::{
    emit_error,
    ingest_common::{SCHEMA_VERSION, configured_vault, go_base, go_ext, open_store, resolve},
    write_go_json, write_stderr, write_stdout,
};

pub fn cli() -> Command {
    let leaf = |name: &'static str| Command::new(name).arg(Arg::new("args").num_args(0..));
    Command::new("rules")
        .subcommand(leaf("list"))
        .subcommand(leaf("add"))
        .subcommand(leaf("update"))
        .subcommand(leaf("delete"))
        .subcommand(leaf("test"))
        .subcommand(leaf("dry-run"))
}

#[derive(Serialize)]
struct Rule<'a> {
    id: i64,
    pattern: &'a str,
    kind: &'a str,
    value: &'a str,
    created_at: &'a str,
}

impl<'a> From<&'a ClassificationRule> for Rule<'a> {
    fn from(rule: &'a ClassificationRule) -> Self {
        Self {
            id: rule.id,
            pattern: &rule.pattern,
            kind: &rule.kind,
            value: &rule.value,
            created_at: &rule.created_at,
        }
    }
}

#[derive(Serialize)]
struct RuleMatch<'a> {
    id: i64,
    pattern: &'a str,
    kind: &'a str,
    value: &'a str,
}

#[derive(Serialize)]
struct ProposedRule<'a> {
    pattern: &'a str,
    kind: &'a str,
    value: &'a str,
}

#[derive(Serialize)]
struct DryRunMatch {
    document_id: i64,
    note_path: String,
    title: String,
    matched_rule_ids: Vec<i64>,
}

#[derive(Serialize)]
struct DryRunSkipped {
    document_id: i64,
    note_path: String,
    reason: String,
}

fn parse_id(text: &str) -> Result<i64, String> {
    text.parse::<i64>().map_err(|error| {
        let reason = match error.kind() {
            std::num::IntErrorKind::PosOverflow | std::num::IntErrorKind::NegOverflow => {
                "value out of range"
            }
            _ => "invalid syntax",
        };
        format!(
            "invalid rule id {}: strconv.ParseInt: parsing {}: {reason}",
            go_quote(text),
            go_quote(text)
        )
    })
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let Some((name, sub)) = command.subcommand() else {
        return crate::process_exit(CoreExitCode::Ok);
    };
    let vault = match configured_vault(vault) {
        Ok(vault) => vault,
        Err(error) => return write_stderr(&format!("{error}\n"), CoreExitCode::Config),
    };
    let args: Vec<String> = sub
        .get_many::<String>("args")
        .into_iter()
        .flatten()
        .cloned()
        .collect();
    match execute(name, &args, &vault, json) {
        Ok(output) => output,
        Err(error) => emit_error(error, json),
    }
}

fn execute(name: &str, args: &[String], vault: &str, json: bool) -> Result<ExitCode, String> {
    let argument = |index: usize| args.get(index).map_or("", String::as_str);
    match name {
        "list" => {
            let resolved = resolve(vault)?;
            let store = open_store(&resolved)?;
            let rules = store
                .list_rules()
                .map_err(|error| format!("list rules: {error}"))?
                .unwrap_or_default();
            if json {
                return Ok(write_go_json(&serde_json::json!({
                    "schema_version": SCHEMA_VERSION,
                    "rules": rules.iter().map(Rule::from).collect::<Vec<_>>(),
                })));
            }
            let mut out = String::new();
            for rule in &rules {
                out.push_str(&format!(
                    "{}\t{}\t{}\t{}\n",
                    rule.id, rule.kind, rule.pattern, rule.value
                ));
            }
            Ok(write_stdout(out))
        }
        "add" => {
            let resolved = resolve(vault)?;
            let store = open_store(&resolved)?;
            let rule = store.add_rule(argument(0), argument(1), argument(2))?;
            Ok(rule_output("added", &rule, json))
        }
        "update" => {
            let id = parse_id(argument(0))?;
            let resolved = resolve(vault)?;
            let store = open_store(&resolved)?;
            let rule = store.update_rule(id, argument(1), argument(2), argument(3))?;
            Ok(rule_output("updated", &rule, json))
        }
        "delete" => {
            let id = parse_id(argument(0))?;
            let resolved = resolve(vault)?;
            let store = open_store(&resolved)?;
            store.delete_rule(id)?;
            if json {
                return Ok(write_go_json(&serde_json::json!({
                    "schema_version": SCHEMA_VERSION,
                    "id": id,
                    "deleted": true,
                })));
            }
            Ok(write_stdout(format!("deleted rule {id}\n")))
        }
        "test" => {
            let resolved = resolve(vault)?;
            let store = open_store(&resolved)?;
            let rules = store
                .list_rules()
                .map_err(|error| format!("list rules: {error}"))?
                .unwrap_or_default();
            let text = go_lowercase(argument(0));
            let matches: Vec<&ClassificationRule> = rules
                .iter()
                .filter(|rule| text.contains(&go_lowercase(&rule.pattern)))
                .collect();
            if json {
                let matches: Vec<RuleMatch<'_>> = matches
                    .iter()
                    .map(|rule| RuleMatch {
                        id: rule.id,
                        pattern: &rule.pattern,
                        kind: &rule.kind,
                        value: &rule.value,
                    })
                    .collect();
                return Ok(write_go_json(&serde_json::json!({
                    "schema_version": SCHEMA_VERSION,
                    "matches": matches,
                })));
            }
            let mut out = String::new();
            for rule in matches {
                out.push_str(&format!(
                    "{}\t{}\t{}\t{}\n",
                    rule.id, rule.kind, rule.pattern, rule.value
                ));
            }
            Ok(write_stdout(out))
        }
        _ => dry_run(argument(0), argument(1), argument(2), vault, json),
    }
}

fn rule_output(verb: &str, rule: &ClassificationRule, json: bool) -> ExitCode {
    if json {
        return write_go_json(&serde_json::json!({
            "schema_version": SCHEMA_VERSION,
            "rule": Rule::from(rule),
        }));
    }
    write_stdout(format!(
        "{verb} rule {}: {} {} -> {}\n",
        rule.id,
        rule.kind,
        go_quote(&rule.pattern),
        go_quote(&rule.value)
    ))
}

/// Go `ingest.noteBody`: the text after the YAML frontmatter the note writer emits.
fn note_body(content: &str) -> &str {
    const DELIM: &str = "---\n";
    let Some(rest) = content.strip_prefix(DELIM) else {
        return content;
    };
    match rest.find("\n---\n") {
        None => content,
        Some(index) => {
            let tail = &rest[index + "\n---\n".len()..];
            tail.strip_prefix('\n').unwrap_or(tail)
        }
    }
}

fn dry_run(
    pattern: &str,
    kind: &str,
    value: &str,
    vault: &str,
    json: bool,
) -> Result<ExitCode, String> {
    validate_classification_rule(pattern, kind, value)?;
    let resolved = resolve(vault)?;
    let store = open_store(&resolved)?;
    let documents = store
        .list_documents()
        .map_err(|error| format!("list documents: {error}"))?
        .unwrap_or_default();
    let pattern_lower = go_lowercase(pattern);
    let mut matches = Vec::new();
    let mut skipped = Vec::new();
    for document in &documents {
        let note_path = document.vault_path.clone().unwrap_or_default();
        if note_path.is_empty() {
            skipped.push(DryRunSkipped {
                document_id: document.id,
                note_path: String::new(),
                reason: "no note path recorded".to_owned(),
            });
            continue;
        }
        let data = match symdesk_vault::go_read(Path::new(&note_path)) {
            Ok(data) => data,
            Err((operation, error)) => {
                skipped.push(DryRunSkipped {
                    document_id: document.id,
                    reason: symdesk_vault::go_path_error(operation, Path::new(&note_path), &error),
                    note_path,
                });
                continue;
            }
        };
        let content = String::from_utf8_lossy(&data);
        if !go_lowercase(note_body(&content)).contains(&pattern_lower) {
            continue;
        }
        let base = go_base(&note_path);
        let ext = go_ext(&note_path);
        matches.push(DryRunMatch {
            document_id: document.id,
            title: base.strip_suffix(&ext).unwrap_or(&base).to_owned(),
            note_path,
            matched_rule_ids: vec![0],
        });
    }
    if json {
        return Ok(write_go_json(&serde_json::json!({
            "schema_version": SCHEMA_VERSION,
            "operation": "dry_run",
            "proposed_rule": ProposedRule { pattern, kind, value },
            "vault_path": resolved.vault,
            "total_documents": documents.len(),
            "matched_documents": matches.len(),
            "skipped_documents": skipped.len(),
            "matches": matches,
            "skipped": skipped,
        })));
    }
    let mut out = format!(
        "dry-run: {}/{} documents matched ({} skipped)\n",
        matches.len(),
        documents.len(),
        skipped.len()
    );
    for m in &matches {
        out.push_str(&format!(
            "  match: {} {} ({})\n",
            m.document_id, m.title, m.note_path
        ));
    }
    for s in &skipped {
        out.push_str(&format!(
            "  skip: {} {} ({})\n",
            s.document_id, s.note_path, s.reason
        ));
    }
    Ok(write_stdout(out))
}
