//! Note and document mutations; Markdown first, derived sidecar second.
#[path = "mutate_date.rs"]
mod date;
use clap::{Arg, ArgAction, ArgMatches, Command};
use date::parse_daily_date;
#[path = "mutate_asn_lock.rs"]
mod asn_lock;
use asn_lock::AsnLock;
use noyalib::Value;
use serde_json::json;
use std::{
    fs,
    io::{self, Read},
    path::Path,
    process::ExitCode,
};
use symdesk_index::{IndexedDocument, Sidecar, open_for_vault};
use symdesk_vault::{
    HistoryStore, activity_journal::append_activity, go_quote, parse_bytes, secure_path,
    set_frontmatter_key, set_frontmatter_value,
};

fn args(c: &ArgMatches) -> Vec<String> {
    c.get_many::<String>("args")
        .into_iter()
        .flatten()
        .cloned()
        .collect()
}
fn leaf(name: &'static str) -> Command {
    Command::new(name).arg(Arg::new("args").num_args(0..))
}
pub fn doc_commands() -> Vec<Command> {
    let batch = |name| leaf(name).arg(Arg::new("stdin").long("stdin").action(ArgAction::SetTrue));
    vec![
        batch("status"),
        batch("due"),
        batch("type"),
        batch("correspondent"),
        Command::new("tag")
            .subcommand(batch("add"))
            .subcommand(batch("remove")),
        leaf("asn"),
    ]
}
pub fn doc_cli() -> Command {
    Command::new("doc").hide(true).subcommands(doc_commands())
}
pub fn note_cli() -> Command {
    Command::new("note")
        .subcommand(
            leaf("new")
                .arg(Arg::new("title").long("title").overrides_with("title"))
                .arg(
                    Arg::new("template")
                        .long("template")
                        .overrides_with("template"),
                ),
        )
        .subcommand(leaf("daily").arg(Arg::new("date").long("date").overrides_with("date")))
        .subcommand(leaf("move"))
        .subcommand(leaf("delete"))
}
pub fn run(command: &ArgMatches, vault: Option<&str>, json_output: bool, note: bool) -> ExitCode {
    let result = execute(command, vault, note);
    match result {
        Ok(value) if json_output => crate::write_go_json(&value),
        Ok(value) => crate::write_stdout(format!("{}\n", go_value(&value))),
        Err(error) => crate::emit_error(error, json_output),
    }
}
fn go_value(v: &serde_json::Value) -> String {
    match v {
        serde_json::Value::Object(m)
            if m.contains_key("file")
                && m.contains_key("status")
                && !m.contains_key("asn")
                && !m.contains_key("trash_name") =>
        {
            format!(
                "{{File:{} Status:{} Error:{}}}",
                m["file"].as_str().unwrap_or(""),
                m["status"].as_str().unwrap_or(""),
                m.get("error")
                    .and_then(serde_json::Value::as_str)
                    .unwrap_or("")
            )
        }
        serde_json::Value::Object(m) => format!(
            "map[{}]",
            m.iter()
                .map(|(k, v)| format!("{k}:{}", go_value(v)))
                .collect::<Vec<_>>()
                .join(" ")
        ),
        serde_json::Value::Array(a) => {
            format!("[{}]", a.iter().map(go_value).collect::<Vec<_>>().join(" "))
        }
        serde_json::Value::String(s) => s.clone(),
        _ => v.to_string(),
    }
}
fn execute(
    command: &ArgMatches,
    vault: Option<&str>,
    note: bool,
) -> Result<serde_json::Value, String> {
    let Some((name, mut sub)) = command.subcommand() else {
        return Ok(json!(null));
    };
    let mut op = name;
    if name == "tag" {
        let Some((action, nested)) = sub.subcommand() else {
            return Ok(json!(null));
        };
        op = action;
        sub = nested;
    }
    let a = args(sub);
    let expected = if note {
        match name {
            "move" => Some(2),
            "delete" => Some(1),
            _ => None,
        }
    } else if name == "asn" {
        Some(2)
    } else {
        None
    };
    if let Some(n) = expected
        && a.len() != n
    {
        return Err(format!("accepts {n} arg(s), received {}", a.len()));
    }
    if note && name == "new" && !sub.contains_id("title") {
        return Err("required flag(s) \"title\" not set".into());
    }
    if !note && name != "asn" && a.is_empty() {
        return Err("requires at least 1 arg(s), only received 0".into());
    }
    // Batch argument validation precedes opening the vault in the Go CLI.
    let mut batch = None;
    if !note && name != "asn" {
        let tag = name == "tag";
        let stdin = sub.get_flag("stdin");
        if stdin && a.len() != 1 {
            return Err(format!(
                "with --stdin, pass exactly one {} argument",
                if tag { "tag" } else { "value" }
            ));
        }
        if !stdin && a.len() < 2 {
            return Err(if tag {
                "need a tag and at least one file (or use --stdin)"
            } else {
                "need at least one file and a value (or use --stdin)"
            }
            .into());
        }
        let value = if tag {
            a[0].clone()
        } else {
            a.last().unwrap().clone()
        };
        let files = if stdin {
            let mut input = String::new();
            io::stdin()
                .read_to_string(&mut input)
                .map_err(|e| format!("read stdin: {e}"))?;
            if input.lines().any(|l| l.len() >= 1024 * 1024) {
                return Err("read stdin: bufio.Scanner: token too long".into());
            }
            let files = input
                .lines()
                .map(str::trim)
                .filter(|l| !l.is_empty())
                .map(str::to_owned)
                .collect::<Vec<_>>();
            if files.is_empty() {
                return Err("no file paths on stdin".into());
            }
            files
        } else if tag {
            a[1..].to_vec()
        } else {
            a[..a.len() - 1].to_vec()
        };
        batch = Some((files, value));
    }
    let root = crate::resolve_vault(vault).map_err(|e| {
        if e.starts_with("vault path not configured") {
            "vault path not configured (use flag or SYMDESK_VAULT env)".into()
        } else {
            crate::index_cli::index_vault_error(vault, &e)
        }
    })?;
    let mut db = open_for_vault(&root).map_err(|e| e.to_string())?;
    let result = (|| {
        if note {
            return note_execute(&root, &mut db, name, sub, &a);
        }
        if name == "asn" {
            let n = assign_asn(&root, &mut db, &a[0], &a[1])?;
            return Ok(json!({"asn":n,"file":a[0],"status":"updated"}));
        }
        let (files, value) = batch.unwrap();
        let mut results = Vec::new();
        let mut updated = 0;
        let mut failed = 0;
        for file in &files {
            match mutate(&root, &mut db, name, op, file, &value) {
                Ok(()) => {
                    updated += 1;
                    results.push(json!({"file":file,"status":"updated"}));
                }
                Err(e) => {
                    failed += 1;
                    results.push(json!({"error":e,"file":file,"status":"error"}));
                }
            }
        }
        if files.len() == 1 && failed == 1 {
            return Err(format!(
                "{}: {}",
                files[0],
                results[0]["error"].as_str().unwrap()
            ));
        }
        Ok(
            json!({"failed":failed,"results":results,"status":if failed==0 {"updated"} else if updated==0 {"failed"} else {"partial"},"updated":updated}),
        )
    })();
    if let Err(e) = db.close() {
        eprintln!("warning: failed to close sidecar database: {e}");
    }
    result
}
fn parse(path: &Path) -> Result<symdesk_vault::Document, String> {
    let bytes = symdesk_vault::go_read(path)
        .map_err(|(op, e)| symdesk_vault::go_path_error(op, path, &e))?;
    parse_bytes(&path.to_string_lossy(), &bytes).map_err(|e| e.to_string())
}
fn reindex(db: &mut Sidecar, path: &Path) -> Result<symdesk_vault::Document, String> {
    let doc = parse(path)?;
    let mtime = fs::metadata(path)
        .and_then(|m| m.modified())
        .map_err(|e| e.to_string())?;
    let nanos = mtime
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_nanos() as i64)
        .unwrap_or(0);
    let indexed = IndexedDocument::from_vault(&doc, Some(nanos)).map_err(|e| e.to_string())?;
    db.index_document(&indexed).map_err(|e| e.to_string())?;
    Ok(doc)
}
fn snapshot(root: &Path, path: &Path) {
    if let Ok(rel) = path.strip_prefix(root) {
        let _ = HistoryStore::new(root).snapshot(&rel.to_string_lossy());
    }
}
fn mutate(
    root: &Path,
    db: &mut Sidecar,
    name: &str,
    op: &str,
    file: &str,
    value: &str,
) -> Result<(), String> {
    if name == "status"
        && !matches!(
            value,
            "open" | "paid" | "submitted" | "done" | "needs_review" | "waiting_for_reply"
        )
    {
        return Err(format!(
            "invalid status {} (valid: open, paid, submitted, done, needs_review, waiting_for_reply)",
            go_quote(value)
        ));
    }
    if value.trim().is_empty() && matches!(name, "type" | "correspondent" | "tag") {
        return Err(format!(
            "{} must not be empty",
            match name {
                "type" => "document type",
                "tag" => "tag",
                _ => "correspondent",
            }
        ));
    }
    let path = secure_path(root, file).map_err(|e| e.to_string())?;
    if name == "tag" {
        let mut tags = parse(&path)?.tags;
        let value = value.trim();
        if op == "add" {
            if !tags.iter().any(|v| v == value) {
                tags.push(value.into());
            }
        } else {
            tags.retain(|v| v != value);
        }
        set_frontmatter_value(
            &path,
            "tags",
            &Value::Sequence(tags.into_iter().map(Value::from).collect()),
        )
        .map_err(|e| e.to_string())?;
    } else {
        if matches!(name, "status" | "due") {
            snapshot(root, &path);
        }
        let key = match name {
            "type" => "document_type",
            "due" => "due_date",
            other => other,
        };
        set_frontmatter_key(&path, key, value).map_err(|e| e.to_string())?;
    }
    let doc = reindex(db, &path)?;
    if matches!(name, "status" | "due") {
        let (event, details) = if name == "status" {
            ("status_changed", format!("status set to {value}"))
        } else {
            ("file_changed", format!("due date set to {value}"))
        };
        let _ = append_activity(root, event, file, &doc.title, &details);
    }
    Ok(())
}
fn note_execute(
    root: &Path,
    db: &mut Sidecar,
    name: &str,
    sub: &ArgMatches,
    a: &[String],
) -> Result<serde_json::Value, String> {
    match name {
        "move" => {
            let old = secure_path(root, &a[0]).map_err(|e| e.to_string())?;
            let new = symdesk_vault::move_note(root, &a[0], &a[1]).map_err(|e| e.to_string())?;
            db.delete_document(&old.to_string_lossy())
                .map_err(|e| e.to_string())?;
            reindex(db, &new)?;
            Ok(json!({"from":a[0],"status":"moved","to":a[1]}))
        }
        "delete" => {
            let path = secure_path(root, &a[0]).map_err(|e| e.to_string())?;
            let rel = path
                .strip_prefix(root)
                .map_err(|e| e.to_string())?
                .to_string_lossy();
            let entry = HistoryStore::new(root)
                .trash(&rel)
                .map_err(|e| e.to_string())?;
            db.delete_document(&path.to_string_lossy())
                .map_err(|e| format!("moved to trash but failed to deindex: {e}"))?;
            let _ = append_activity(root, "file_removed", &rel, &entry.name, "moved to trash");
            Ok(json!({"file":entry.original_path,"status":"trashed","trash_name":entry.name}))
        }
        "daily" => {
            let now = time::OffsetDateTime::now_utc();
            let raw = sub
                .get_one::<String>("date")
                .map(String::as_str)
                .unwrap_or("");
            let date = if raw.is_empty() {
                now.date().to_string()
            } else {
                parse_daily_date(raw)?
            };
            let file = format!("{date}.md");
            let path = secure_path(root, &file).map_err(|e| e.to_string())?;
            if !path.exists() {
                new_note(root, db, &date, "", "daily")?;
            }
            Ok(json!({"path":file,"status":"ok"}))
        }
        "new" => {
            let title = sub.get_one::<String>("title").unwrap();
            let template = sub
                .get_one::<String>("template")
                .map(String::as_str)
                .unwrap_or("");
            let file = new_note(
                root,
                db,
                title,
                a.first().map(String::as_str).unwrap_or(""),
                template,
            )?;
            Ok(json!({"path":file,"status":"created"}))
        }
        _ => Ok(json!(null)),
    }
}
fn new_note(
    root: &Path,
    db: &mut Sidecar,
    title: &str,
    content: &str,
    template: &str,
) -> Result<String, String> {
    if title.is_empty() {
        return Err("title is required".into());
    }
    let file = symdesk_vault::note_file_name(title);
    let path = secure_path(root, &file).map_err(|e| e.to_string())?;
    let now = time::OffsetDateTime::now_utc();
    let created = symdesk_vault::notes::format_rfc3339(now);
    let tpl = if template.is_empty() {
        String::new()
    } else {
        secure_path(root, &format!("templates/{template}.md"))
            .ok()
            .and_then(|p| fs::read_to_string(p).ok())
            .unwrap_or_default()
    };
    let tpl = tpl
        .replace("{{title}}", title)
        .replace("{{date}}", &now.date().to_string())
        .replace(
            "{{time}}",
            &format!("{:02}:{:02}", now.hour(), now.minute()),
        );
    let front = tpl.starts_with("---\n");
    let mut body = if tpl.is_empty() {
        symdesk_vault::note_document(title, &created, content)
    } else if front {
        tpl.clone()
    } else {
        symdesk_vault::note_document(title, &created, &tpl)
    };
    if !tpl.is_empty() && !content.is_empty() {
        body.push('\n');
        body.push_str(content);
    }
    snapshot(root, &path);
    fs::write(&path, &body).map_err(|e| format!("failed to write file: {e}"))?;
    if front {
        let _ = set_frontmatter_key(&path, "title", title);
        let _ = set_frontmatter_key(&path, "created", &created);
    }
    let mut doc = parse(&path)?;
    doc.sha256 = symdesk_vault::sha256_hex(body.as_bytes());
    let indexed = IndexedDocument::from_vault(&doc, None).map_err(|e| e.to_string())?;
    db.index_document(&indexed)
        .map_err(|e| format!("failed to index new file: {e}"))?;
    let _ = append_activity(root, "file_added", &file, title, "created note");
    Ok(file)
}
fn assign_asn(root: &Path, db: &mut Sidecar, file: &str, value: &str) -> Result<i64, String> {
    let path = secure_path(root, file).map_err(|e| e.to_string())?;
    let target = path
        .strip_prefix(root)
        .map_err(|e| e.to_string())?
        .to_string_lossy()
        .into_owned();
    let _lock = AsnLock::acquire(root)?;
    let mut assigned = std::collections::BTreeMap::<i64, Vec<String>>::new();
    let mut malformed = Vec::new();
    let mut errors = Vec::new();
    for rel in symdesk_vault::walk_markdown(root)
        .map_err(|e| format!("scan vault ASN assignments: {e}"))?
    {
        match parse(&root.join(&rel)) {
            Ok(doc) => {
                if let Some(n) = doc.asn {
                    assigned
                        .entry(n)
                        .or_default()
                        .push(rel.to_string_lossy().into_owned());
                }
            }
            Err(e) => {
                if e.starts_with("invalid asn in ") {
                    if rel.to_string_lossy() != target {
                        malformed.push((rel, e));
                    }
                } else {
                    errors.push((rel, e));
                }
            }
        }
    }
    if let Some((rel, e)) = malformed.first() {
        return Err(format!(
            "cannot assign ASN while {} has malformed ASN: {}",
            rel.display(),
            e.rsplit(": ").next().unwrap_or(e)
        ));
    }
    if let Some((rel, e)) = errors.first() {
        return Err(format!(
            "cannot assign ASN while {} cannot be parsed: {e}",
            rel.display()
        ));
    }
    let n = if value.eq_ignore_ascii_case("next") {
        (1..).find(|n| !assigned.contains_key(n)).unwrap()
    } else {
        value
            .parse::<i64>()
            .map_err(|_| "ASN must be a positive integer or \"next\"".to_owned())?
    };
    if n < 1 {
        return Err("invalid ASN: must be a positive integer".into());
    }
    if let Some(other) = assigned
        .get(&n)
        .and_then(|paths| paths.iter().find(|p| **p != target))
    {
        return Err(format!("ASN {n} is already assigned to {other}"));
    }
    snapshot(root, &path);
    set_frontmatter_value(&path, "asn", &Value::from(n)).map_err(|e| format!("set ASN: {e}"))?;
    reindex(db, &path).map_err(|e| format!("index ASN-updated document: {e}"))?;
    Ok(n)
}

pub fn prepare_args(args: &mut Vec<std::ffi::OsString>) -> Option<ExitCode> {
    let mut commands = Vec::new();
    let mut at = 1;
    while at < args.len() && commands.len() < 2 {
        let token = args[at].to_str()?;
        if token == "--" {
            return None;
        }
        if matches!(token, "--vault" | "--output") {
            at += 2;
            continue;
        }
        if !token.starts_with('-') {
            commands.push(token);
        }
        at += 1;
    }
    let family = *commands.first()?;
    let verb = *commands.get(1)?;
    if !matches!(family, "note" | "doc" | "docs")
        || (family == "docs" && matches!(verb, "list" | "review"))
    {
        return None;
    }
    let mut index;
    let mut normalized = vec![args[0].clone()];
    let mut output = None;
    let mut vault = None;
    let mut json = false;
    index = 1;
    while index < args.len() {
        let token = args[index].to_str()?;
        if token == "--" {
            normalized.extend_from_slice(&args[index..]);
            break;
        }
        if token == "--json" {
            json = true;
            index += 1;
            continue;
        }
        if let Some(raw) = token.strip_prefix("--json=") {
            json = match raw {
                "1" | "t" | "T" | "true" | "TRUE" | "True" => true,
                "0" | "f" | "F" | "false" | "FALSE" | "False" => false,
                _ => {
                    return Some(crate::emit_error(
                        format!(
                            "invalid argument {} for \"--json\" flag: strconv.ParseBool: parsing {}: invalid syntax",
                            go_quote(raw),
                            go_quote(raw)
                        ),
                        json,
                    ));
                }
            };
            index += 1;
            continue;
        }
        let flag = token.split_once('=').map_or(token, |(flag, _)| flag);
        if flag == "--vault" || flag == "--output" {
            let value = if let Some((_, value)) = token.split_once('=') {
                value.to_owned()
            } else {
                index += 1;
                let Some(value) = args.get(index) else {
                    return Some(crate::emit_error(
                        format!("flag needs an argument: {flag}"),
                        json,
                    ));
                };
                value.to_str()?.to_owned()
            };
            if flag == "--vault" {
                vault = Some(value);
            } else {
                output = Some(value);
            }
        } else if matches!(flag, "--title" | "--template" | "--date") {
            normalized.push(args[index].clone());
            if !token.contains('=') {
                index += 1;
                let Some(value) = args.get(index) else {
                    return Some(crate::emit_error(
                        format!("flag needs an argument: {flag}"),
                        json,
                    ));
                };
                normalized.push(value.clone());
            }
        } else if token == "--stdin" {
            normalized.push(args[index].clone());
        } else if token.starts_with("--") && token != "--help" {
            return Some(crate::emit_error(format!("unknown flag: {flag}"), json));
        } else {
            normalized.push(args[index].clone());
        }
        index += 1;
    }
    // Insert inherited flags before `--`, never behind the positional sentinel.
    let at = normalized
        .iter()
        .position(|v| v == "--")
        .unwrap_or(normalized.len());
    let mut globals = Vec::new();
    if json {
        globals.push(std::ffi::OsString::from("--json"));
    }
    for (name, value) in [("--output", output), ("--vault", vault)] {
        if let Some(value) = value {
            globals.extend([name.into(), value.into()]);
        }
    }
    normalized.splice(at..at, globals);
    *args = normalized;
    None
}
