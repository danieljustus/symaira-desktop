//! Go: `cmd/symdesk/retention.go`.
//!
//! Retention evaluation, proposal review, and history commands backed by the
//! sidecar and vault state APIs.

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    time::{SystemTime, UNIX_EPOCH},
};

use clap::{Arg, ArgAction, Command};
use serde::Serialize;
use serde_json::{Value, json};
use symdesk_index::{DatasetPurgeService, IndexedDocument, Sidecar, open_for_vault};
use symdesk_vault::retention::{
    ACTION_FLAG_REVIEW, ACTION_TRASH, HistoryEntry, PROPOSAL_ITEM_STATUS_ACCEPTED,
    PROPOSAL_ITEM_STATUS_ACTION_COMPLETED, PROPOSAL_STATUS_ACCEPTED, PROPOSAL_STATUS_FAILED,
    PROPOSAL_STATUS_PARTIAL, PROPOSAL_STATUS_PENDING, Proposal, ProposalItem, append_history,
    evaluate, history_path, load_history, load_proposal, load_rules, proposal_dir,
    stable_action_id, write_proposal,
};
use symdesk_vault::retention_state::retention_state;
use symdesk_vault::{
    HistoryStore, activity_journal::append_activity, parse_bytes, secure_path, set_frontmatter_key,
};
use time::OffsetDateTime;

use crate::{emit_error, write_go_json, write_stdout};

/// Go: `newRetentionCmd`.
pub fn cli() -> Command {
    Command::new("retention")
        .about("Evaluate and apply document retention rules")
        .arg(
            Arg::new("verbose")
                .long("verbose")
                .action(ArgAction::SetTrue),
        )
        .subcommand(
            Command::new("eval")
                .about("Evaluate retention rules and stage a reviewable proposal")
                .arg(
                    Arg::new("rules")
                        .long("rules")
                        .num_args(1)
                        .help("Path to retention rules YAML file (default: <vault>/.symdesk/retention-rules.yaml)"),
                ),
        )
        .subcommand(Command::new("list").about("List documents due to expire"))
        .subcommand(
            Command::new("accept")
                .about("Accept a pending retention proposal")
                .arg(Arg::new("run-id").num_args(0..).action(ArgAction::Append)),
        )
        .subcommand(
            Command::new("reject")
                .about("Reject a pending retention proposal")
                .arg(Arg::new("run-id").num_args(0..).action(ArgAction::Append)),
        )
        .subcommand(
            Command::new("diff")
                .about("Show the proposed retention actions")
                .arg(Arg::new("run-id").num_args(0..).action(ArgAction::Append)),
        )
        .subcommand(
            Command::new("history")
                .about("Show the history of executed retention actions")
                .arg(Arg::new("extra").num_args(0..).action(ArgAction::Append)),
        )
}

/// Go: `newRetentionEvalCmd`'s `RunE`.
pub fn run_eval(
    vault: Option<&str>,
    rules_path: Option<&str>,
    output_json: bool,
) -> std::process::ExitCode {
    let vault_root = match crate::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, output_json),
    };
    let sidecar = match open_for_vault(&vault_root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), output_json),
    };
    let rules_file = rules_path
        .filter(|path| !path.is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| vault_root.join(".symdesk").join("retention-rules.yaml"));
    let rules = match load_rules(&rules_file) {
        Ok(rules) => rules,
        Err(error) => {
            return emit_error(
                format!("load rules from {}: {error}", rules_file.display()),
                output_json,
            );
        }
    };
    let docs = match sidecar.list_files(&vault_root, "") {
        Ok(docs) => docs,
        Err(error) => return emit_error(format!("list documents: {error}"), output_json),
    };

    let now = OffsetDateTime::now_utc();
    let mut all_items = Vec::new();
    let mut state_failures = Vec::new();
    for rule in &rules {
        for doc in &docs {
            let relative = match Path::new(&doc.path).strip_prefix(&vault_root) {
                Ok(path) => match path.to_str() {
                    Some(path) => path.replace('\\', "/"),
                    None => {
                        state_failures.push(format!("{}: non-UTF-8 vault path", doc.path));
                        continue;
                    }
                },
                Err(_) => {
                    state_failures.push(format!("{}: path is outside vault", doc.path));
                    continue;
                }
            };
            let state = match retention_state(&vault_root, &relative) {
                Ok(state) => state,
                Err(error) => {
                    state_failures.push(format!("{relative}: {error}"));
                    continue;
                }
            };
            if state.dataset && rule.name != state.rule_name {
                continue;
            }
            for mut item in evaluate(rule, &[state.meta], now) {
                item.rule_name.clone_from(&rule.name);
                item.fingerprint.clone_from(&state.fingerprint);
                all_items.push(item);
            }
        }
    }
    if !state_failures.is_empty() {
        return emit_error(
            format!(
                "retention evaluation failed closed: {}",
                state_failures.join("; ")
            ),
            output_json,
        );
    }

    let run_id = format!("ret-{}", now.unix_timestamp());
    let item_count = all_items.len();
    let proposal = Proposal {
        run_id: run_id.clone(),
        rule_name: "batch".to_owned(),
        created: now.to_offset(time::UtcOffset::UTC),
        items: (!all_items.is_empty()).then_some(all_items),
        status: PROPOSAL_STATUS_PENDING.to_owned(),
    };
    if let Err(error) = write_proposal(&vault_root, &proposal) {
        return emit_error(error.to_string(), output_json);
    }
    if output_json {
        #[derive(Serialize)]
        struct EvalOutput<'a> {
            item_count: usize,
            items: &'a Option<Vec<ProposalItem>>,
            run_id: &'a str,
            status: &'a str,
        }
        return write_go_json(&EvalOutput {
            item_count,
            items: &proposal.items,
            run_id: &run_id,
            status: PROPOSAL_STATUS_PENDING,
        });
    }
    write_stdout(format!(
        "map[item_count:{item_count} items:{} run_id:{run_id} status:pending]\n",
        render_items(proposal.items.as_deref().unwrap_or(&[])).trim_end()
    ))
}

/// Go: `newRetentionListCmd`'s `RunE`.
pub fn run_list(vault: Option<&str>, output_json: bool) -> std::process::ExitCode {
    let vault_root = match retention_vault(vault, output_json) {
        Ok(root) => root,
        Err(exit) => return exit,
    };

    let dir = proposal_dir(&vault_root);
    let mut entries = match fs::read_dir(&dir) {
        Ok(entries) => entries,
        Err(error) => {
            if error.kind() == std::io::ErrorKind::NotFound {
                // Go: `outputResult(map[string]interface{}{"proposals": []string{}, "message": "no pending proposals"})`
                return output(
                    json!({"proposals": Vec::<String>::new(), "message": "no pending proposals"}),
                    output_json,
                );
            }
            return emit_error(format!("open {}: {error}", dir.display()), output_json);
        }
    };

    // Go's `os.ReadDir` returns entries sorted by file name.
    let mut sorted: Vec<_> = entries.by_ref().flatten().collect();
    sorted.sort_by_key(|entry| entry.file_name());

    let mut proposals: Vec<Proposal> = Vec::new();
    for entry in sorted {
        let name = entry.file_name();
        let name = name.to_string_lossy().to_string();
        let path = entry.path();
        if path.is_dir() || path.extension().and_then(|ext| ext.to_str()) != Some("json") {
            continue;
        }
        let Some(run_id) = name.strip_suffix(".json") else {
            continue;
        };
        let proposal = match load_proposal(&vault_root, run_id) {
            Ok(proposal) => proposal,
            // Go skips an unreadable or undecodable proposal.
            Err(_) => continue,
        };
        if !matches!(
            proposal.status.as_str(),
            PROPOSAL_STATUS_PENDING | PROPOSAL_STATUS_FAILED | PROPOSAL_STATUS_PARTIAL
        ) {
            continue;
        }
        proposals.push(proposal);
    }

    if output_json {
        if proposals.is_empty() {
            return write_stdout("null\n".to_owned());
        }
        return write_go_json(&proposals);
    }

    if proposals.is_empty() {
        return write_stdout("no pending retention proposals\n".to_owned());
    }

    let mut rendered = String::new();
    for proposal in &proposals {
        rendered.push_str(&format!(
            "Proposal {} ({}): {} items pending review\n",
            proposal.run_id,
            go_local_time(proposal.created, false),
            proposal.items.as_ref().map_or(0, Vec::len)
        ));
        for item in proposal.items.as_deref().unwrap_or(&[]) {
            rendered.push_str(&format!(
                "  {} — expires {} → {}\n",
                item.path, item.expires_at, item.action
            ));
        }
    }
    write_stdout(rendered)
}

/// Go: `newRetentionAcceptCmd`'s `RunE`.
pub fn run_accept(
    vault: Option<&str>,
    run_ids: &[String],
    output_json: bool,
) -> std::process::ExitCode {
    let run_id = match exact_one(run_ids, output_json) {
        Ok(run_id) => run_id,
        Err(exit) => return exit,
    };
    let vault_root = match crate::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, output_json),
    };
    let mut sidecar = match open_for_vault(&vault_root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), output_json),
    };
    let mut proposal = match load_proposal(&vault_root, run_id) {
        Ok(proposal) => proposal,
        Err(error) => return emit_error(error.to_string(), output_json),
    };
    if !matches!(
        proposal.status.as_str(),
        PROPOSAL_STATUS_PENDING
            | PROPOSAL_STATUS_FAILED
            | PROPOSAL_STATUS_PARTIAL
            | PROPOSAL_STATUS_ACCEPTED
    ) {
        return emit_error(
            format!("proposal {run_id} is {}", proposal.status),
            output_json,
        );
    }

    let now = OffsetDateTime::now_utc();
    let history = HistoryStore::new(&vault_root);
    let mut acted = 0;
    let mut failures = Vec::new();
    let mut items = proposal.items.take();
    for index in 0..items.as_ref().map_or(0, Vec::len) {
        let item = items.as_ref().expect("present item vector")[index].clone();
        if item.status == PROPOSAL_ITEM_STATUS_ACCEPTED {
            continue;
        }
        if item.status == PROPOSAL_ITEM_STATUS_ACTION_COMPLETED {
            let entry = HistoryEntry {
                action_id: stable_action_id(&proposal.run_id, index),
                timestamp: now,
                rule_name: item.rule_name.clone(),
                action: item.action.clone(),
                path: item.path.clone(),
                title: item.title.clone(),
            };
            if let Err(error) = append_history(&vault_root, &entry) {
                let item = &mut items.as_mut().expect("present item vector")[index];
                item.failure = format!("history append failed after action: {error}");
                failures.push(format!("{}: {}", item.path, item.failure));
                continue;
            }
            let item = &mut items.as_mut().expect("present item vector")[index];
            item.status = PROPOSAL_ITEM_STATUS_ACCEPTED.to_owned();
            item.failure.clear();
            let item_path = item.path.clone();
            if let Err(error) = write_accept_progress(&vault_root, &proposal, &items) {
                return emit_error(
                    format!("save retention progress for {item_path}: {error}"),
                    output_json,
                );
            }
            continue;
        }

        let state = match retention_state(&vault_root, &item.path) {
            Ok(state) => state,
            Err(error) => {
                append_retention_failure(
                    &mut items.as_mut().expect("present item vector")[index],
                    format!("cannot re-read authoritative state: {error}"),
                    &mut failures,
                );
                continue;
            }
        };
        let dataset_slug = dataset_retention_slug(&item.path);
        if let Some(expected_slug) = dataset_slug.as_deref() {
            if !state.dataset
                || state.meta.path != item.path
                || state.meta.title.is_empty()
                || expected_slug.is_empty()
            {
                append_retention_failure(
                    &mut items.as_mut().expect("present item vector")[index],
                    "dataset handle is missing or changed".to_owned(),
                    &mut failures,
                );
                continue;
            }
            if item.rule_name.is_empty() || state.rule_name != item.rule_name {
                append_retention_failure(
                    &mut items.as_mut().expect("present item vector")[index],
                    format!(
                        "proposal is stale: dataset declares retention rule {:?}, proposal requires {:?}",
                        state.rule_name, item.rule_name
                    ),
                    &mut failures,
                );
                continue;
            }
        }
        if item.fingerprint.is_empty() {
            append_retention_failure(
                &mut items.as_mut().expect("present item vector")[index],
                "proposal has no fingerprint; re-run retention eval".to_owned(),
                &mut failures,
            );
            continue;
        }
        if state.fingerprint != item.fingerprint {
            append_retention_failure(
                &mut items.as_mut().expect("present item vector")[index],
                "proposal is stale: authoritative fingerprint changed".to_owned(),
                &mut failures,
            );
            continue;
        }

        if let Err(error) = apply_retention_action(
            &vault_root,
            &mut sidecar,
            &history,
            &item,
            dataset_slug.as_deref(),
        ) {
            append_retention_failure(
                &mut items.as_mut().expect("present item vector")[index],
                format!("action failed: {error}"),
                &mut failures,
            );
            continue;
        }

        {
            let item = &mut items.as_mut().expect("present item vector")[index];
            item.status = PROPOSAL_ITEM_STATUS_ACTION_COMPLETED.to_owned();
            item.failure.clear();
        }
        if let Err(error) = write_accept_progress(&vault_root, &proposal, &items) {
            return emit_error(
                format!("save action progress for {}: {error}", item.path),
                output_json,
            );
        }
        let entry = HistoryEntry {
            action_id: stable_action_id(&proposal.run_id, index),
            timestamp: now,
            rule_name: item.rule_name.clone(),
            action: item.action.clone(),
            path: item.path.clone(),
            title: item.title.clone(),
        };
        if let Err(error) = append_history(&vault_root, &entry) {
            let item = &mut items.as_mut().expect("present item vector")[index];
            item.failure = format!("history append failed after action: {error}");
            failures.push(format!("{}: {}", item.path, item.failure));
            continue;
        }
        {
            let item = &mut items.as_mut().expect("present item vector")[index];
            item.status = PROPOSAL_ITEM_STATUS_ACCEPTED.to_owned();
            item.failure.clear();
        }
        acted += 1;
        if let Err(error) = write_accept_progress(&vault_root, &proposal, &items) {
            return emit_error(
                format!("save retention progress for {}: {error}", item.path),
                output_json,
            );
        }
    }

    proposal.items = items;
    proposal.status = retention_proposal_status(proposal.items.as_deref().unwrap_or(&[]));
    if let Err(error) = write_proposal(&vault_root, &proposal) {
        return emit_error(format!("save retention proposal: {error}"), output_json);
    }
    let failure_text = failures.join("; ");
    let has_failures = !failures.is_empty();
    let final_status = proposal.status.clone();
    let output_exit = if output_json {
        #[derive(Serialize)]
        struct AcceptOutput<'a> {
            acted: usize,
            failures: Option<&'a [String]>,
            items: &'a Option<Vec<ProposalItem>>,
            run_id: &'a str,
            status: &'a str,
        }
        write_go_json(&AcceptOutput {
            acted,
            failures: (!failures.is_empty()).then_some(failures.as_slice()),
            items: &proposal.items,
            run_id,
            status: &proposal.status,
        })
    } else {
        let rendered_failures = if failures.is_empty() {
            "[]".to_owned()
        } else {
            format!("[{}]", failures.join(" "))
        };
        write_stdout(format!(
            "map[acted:{acted} failures:{rendered_failures} items:{} run_id:{run_id} status:{}]\n",
            render_items(proposal.items.as_deref().unwrap_or(&[])).trim_end(),
            proposal.status
        ))
    };
    if output_exit != std::process::ExitCode::SUCCESS {
        return output_exit;
    }
    if !has_failures {
        output_exit
    } else {
        emit_error(
            format!("retention acceptance {final_status}: {failure_text}"),
            output_json,
        )
    }
}

fn write_accept_progress(
    vault_root: &Path,
    proposal: &Proposal,
    items: &Option<Vec<ProposalItem>>,
) -> Result<(), String> {
    let mut progress = proposal.clone();
    progress.items = items.clone();
    progress.status = retention_proposal_status(progress.items.as_deref().unwrap_or(&[]));
    write_proposal(vault_root, &progress).map_err(|error| error.to_string())
}

fn apply_retention_action(
    vault_root: &Path,
    sidecar: &mut Sidecar,
    history: &HistoryStore,
    item: &ProposalItem,
    dataset_slug: Option<&str>,
) -> Result<(), String> {
    if let Some(slug) = dataset_slug {
        if item.action != ACTION_TRASH {
            return Err(format!(
                "dataset retention action must be trash, got {:?}",
                item.action
            ));
        }
        return DatasetPurgeService::new(vault_root, sidecar)
            .purge(slug, &item.rule_name, &item.fingerprint)
            .map_err(|error| error.to_string());
    }

    let relative = item.path.trim().replace('\\', "/");
    let absolute = secure_path(vault_root, &relative).map_err(|error| error.to_string())?;
    match item.action.as_str() {
        ACTION_TRASH => {
            let entry = history
                .trash(&relative)
                .map_err(|error| error.to_string())?;
            let key_path = vault_root.join(Path::new(&relative));
            let key = key_path
                .to_str()
                .ok_or_else(|| format!("non-UTF-8 vault path: {key_path:?}"))?;
            sidecar
                .delete_document(key)
                .map_err(|error| format!("moved to trash but failed to deindex: {error}"))?;
            let _ = append_activity(
                vault_root,
                "file_removed",
                &relative,
                &entry.name,
                "moved to trash",
            );
            Ok(())
        }
        ACTION_FLAG_REVIEW => {
            // Go treats a failed pre-mutation snapshot as a warning, not as a
            // reason to lose the requested status update.
            let _ = history.snapshot(&relative);
            set_frontmatter_key(&absolute, "status", "needs_review")
                .map_err(|error| error.to_string())?;
            let bytes = fs::read(&absolute).map_err(|error| format!("read file: {error}"))?;
            let key_path = vault_root.join(Path::new(&relative));
            let key = key_path
                .to_str()
                .ok_or_else(|| format!("non-UTF-8 vault path: {key_path:?}"))?;
            let document = parse_bytes(key, &bytes).map_err(|error| error.to_string())?;
            let metadata = fs::metadata(&absolute).map_err(|error| error.to_string())?;
            let mtime_ns =
                system_time_unix_nanos(metadata.modified().map_err(|error| error.to_string())?)?;
            let indexed = IndexedDocument::from_vault(&document, Some(mtime_ns))
                .map_err(|error| error.to_string())?;
            sidecar
                .index_document(&indexed)
                .map_err(|error| error.to_string())?;
            let _ = append_activity(
                vault_root,
                "status_changed",
                &relative,
                &document.title,
                "status set to needs_review",
            );
            Ok(())
        }
        action => Err(format!("unsupported retention action {:?}", action)),
    }
}

fn system_time_unix_nanos(value: SystemTime) -> Result<i64, String> {
    let nanos = match value.duration_since(UNIX_EPOCH) {
        Ok(duration) => i128::try_from(duration.as_nanos()),
        Err(error) => i128::try_from(error.duration().as_nanos()).map(|nanos| -nanos),
    }
    .map_err(|error| format!("file modification time is out of range: {error}"))?;
    i64::try_from(nanos).map_err(|error| format!("file modification time is out of range: {error}"))
}

fn append_retention_failure(item: &mut ProposalItem, message: String, failures: &mut Vec<String>) {
    item.status.clear();
    item.failure = message;
    failures.push(format!("{}: {}", item.path, item.failure));
}

fn retention_proposal_status(items: &[ProposalItem]) -> String {
    let mut accepted = 0;
    let mut failed = 0;
    let mut pending = 0;
    for item in items {
        match (
            item.status == PROPOSAL_ITEM_STATUS_ACCEPTED,
            !item.failure.is_empty(),
        ) {
            (true, _) => accepted += 1,
            (false, true) => failed += 1,
            (false, false) => pending += 1,
        }
    }
    if failed == 0 && pending == 0 {
        PROPOSAL_STATUS_ACCEPTED.to_owned()
    } else if failed == 0 {
        PROPOSAL_STATUS_PENDING.to_owned()
    } else if accepted == 0 {
        PROPOSAL_STATUS_FAILED.to_owned()
    } else {
        PROPOSAL_STATUS_PARTIAL.to_owned()
    }
}

fn dataset_retention_slug(path: &str) -> Option<String> {
    let path = path.trim().replace('\\', "/");
    let slug = path.strip_prefix("datasets/")?.strip_suffix(".md")?;
    if slug.is_empty() || slug.contains('/') {
        return None;
    }
    Some(slug.to_owned())
}

/// Go: `newRetentionRejectCmd`'s `RunE`.
pub fn run_reject(
    vault: Option<&str>,
    run_ids: &[String],
    output_json: bool,
) -> std::process::ExitCode {
    let run_id = match exact_one(run_ids, output_json) {
        Ok(run_id) => run_id,
        Err(exit) => return exit,
    };
    let vault_root = match retention_vault(vault, output_json) {
        Ok(root) => root,
        Err(exit) => return exit,
    };
    let mut proposal = match load_proposal_for_cli(&vault_root, run_id) {
        Ok(proposal) => proposal,
        Err(error) => return emit_error(error, output_json),
    };
    proposal.status = "rejected".to_owned();
    if let Err(error) = write_proposal(&vault_root, &proposal) {
        return emit_error(error.to_string(), output_json);
    }

    if output_json {
        let result = BTreeMap::from([("run_id", run_id), ("status", "rejected")]);
        write_go_json(&result)
    } else {
        write_stdout(format!("map[run_id:{run_id} status:rejected]\n"))
    }
}

/// Go: `newRetentionDiffCmd`'s `RunE`.
pub fn run_diff(
    vault: Option<&str>,
    run_ids: &[String],
    output_json: bool,
) -> std::process::ExitCode {
    let run_id = match exact_one(run_ids, output_json) {
        Ok(run_id) => run_id,
        Err(exit) => return exit,
    };
    let vault_root = match retention_vault(vault, output_json) {
        Ok(root) => root,
        Err(exit) => return exit,
    };
    let proposal = match load_proposal_for_cli(&vault_root, run_id) {
        Ok(proposal) => proposal,
        Err(error) => return emit_error(error, output_json),
    };

    if output_json {
        return write_go_json(&proposal.items);
    }
    write_stdout(render_items(proposal.items.as_deref().unwrap_or(&[])))
}

/// Go: `newRetentionHistoryCmd`'s `RunE`.
pub fn run_history(
    vault: Option<&str>,
    extra: &[String],
    output_json: bool,
) -> std::process::ExitCode {
    if let Some(extra) = extra.first() {
        return emit_error(
            format!("unknown command {extra:?} for \"symdesk retention history\""),
            output_json,
        );
    }
    let vault_root = match retention_vault(vault, output_json) {
        Ok(root) => root,
        Err(exit) => return exit,
    };
    let path = history_path(&vault_root);
    let missing =
        fs::metadata(&path).is_err_and(|error| error.kind() == std::io::ErrorKind::NotFound);
    let entries = match load_history(&vault_root) {
        Ok(entries) => entries,
        Err(error) => return emit_error(error.to_string(), output_json),
    };

    if output_json {
        if missing {
            return write_stdout("null\n".to_owned());
        }
        return write_go_json(&entries);
    }
    if entries.is_empty() {
        return write_stdout("no retention actions recorded\n".to_owned());
    }

    let mut rendered = String::new();
    for entry in &entries {
        rendered.push_str(&format!(
            "{}  {}  {} → {}\n",
            go_local_time(entry.timestamp, true),
            entry.rule_name,
            entry.path,
            entry.action
        ));
    }
    write_stdout(rendered)
}

fn retention_vault(
    vault: Option<&str>,
    output_json: bool,
) -> Result<PathBuf, std::process::ExitCode> {
    let vault_root = crate::resolve_vault(vault).map_err(|error| emit_error(error, output_json))?;
    // Go discards these `*sidecar.DB` handles without closing them for list,
    // reject, diff, and history. Keep the Rust connection alive until process
    // exit so metadata.json plus sidecar.db-wal/sidecar.db-shm match Go.
    let sidecar =
        open_for_vault(&vault_root).map_err(|error| emit_error(error.to_string(), output_json))?;
    std::mem::forget(sidecar);
    Ok(vault_root)
}

fn exact_one(values: &[String], output_json: bool) -> Result<&str, std::process::ExitCode> {
    if values.len() != 1 {
        return Err(emit_error(
            format!("accepts 1 arg(s), received {}", values.len()),
            output_json,
        ));
    }
    Ok(values[0].as_str())
}

fn load_proposal_for_cli(vault_root: &Path, run_id: &str) -> Result<Proposal, String> {
    load_proposal(vault_root, run_id).map_err(|error| error.to_string())
}

fn render_items(items: &[ProposalItem]) -> String {
    let rendered = items
        .iter()
        .map(|item| {
            format!(
                "{{Path:{} Title:{} ReferenceDate:{} ExpiresAt:{} Action:{} RuleName:{} Fingerprint:{} Status:{} Failure:{}}}",
                item.path,
                item.title,
                item.reference_date,
                item.expires_at,
                item.action,
                item.rule_name,
                item.fingerprint,
                item.status,
                item.failure
            )
        })
        .collect::<Vec<_>>()
        .join(" ");
    format!("[{rendered}]\n")
}

/// Go: `.Local().Format("2006-01-02 15:04[:05]")`; resolve the offset at the
/// event instant so historical daylight-saving transitions match.
fn go_local_time(value: time::OffsetDateTime, seconds: bool) -> String {
    let offset = time::UtcOffset::local_offset_at(value).unwrap_or(time::UtcOffset::UTC);
    let local = value.to_offset(offset);
    if seconds {
        format!(
            "{:04}-{:02}-{:02} {:02}:{:02}:{:02}",
            local.year(),
            u8::from(local.month()),
            local.day(),
            local.hour(),
            local.minute(),
            local.second()
        )
    } else {
        format!(
            "{:04}-{:02}-{:02} {:02}:{:02}",
            local.year(),
            u8::from(local.month()),
            local.day(),
            local.hour(),
            local.minute()
        )
    }
}

fn output(value: Value, output_json: bool) -> std::process::ExitCode {
    if output_json {
        write_go_json(&value)
    } else {
        // Go prints the map with `fmt.Printf("%+v\n", data)`.
        let rendered = match &value {
            Value::Object(map) => {
                let inner = map
                    .iter()
                    .map(|(key, value)| format!("{key}:{}", go_value(value)))
                    .collect::<Vec<_>>()
                    .join(" ");
                format!("map[{inner}]")
            }
            other => go_value(other),
        };
        write_stdout(format!("{rendered}\n"))
    }
}

fn go_value(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        Value::Array(items) if items.is_empty() => "[]".to_owned(),
        Value::Array(items) => format!(
            "[{}]",
            items.iter().map(go_value).collect::<Vec<_>>().join(" ")
        ),
        Value::Null => "<nil>".to_owned(),
        other => other.to_string(),
    }
}
