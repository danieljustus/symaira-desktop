//! Typed inverse note relations from indexed frontmatter and wikilinks.
use std::process::ExitCode;

use clap::{Arg, ArgMatches, Command};
use symdesk_index::open_for_vault;
use symdesk_vault::secure_path;

use crate::{emit_error, resolve_vault, write_go_json, write_stdout};

pub fn cli() -> Command {
    Command::new("relations")
        .about("Inspect typed relations between notes")
        .subcommand(Command::new("inverse").arg(Arg::new("file").num_args(0..)))
        .subcommand(related_command())
        .subcommand(crate::contact_cli::command())
}

/// Shared by `relations related` and the hidden top-level `related`.
pub fn related_command() -> Command {
    Command::new("related").arg(Arg::new("file").num_args(0..))
}

pub fn run_related(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let files: Vec<_> = command
        .get_many::<String>("file")
        .into_iter()
        .flatten()
        .collect();
    if files.len() != 1 {
        return emit_error(
            format!("accepts 1 arg(s), received {}", files.len()),
            output_json,
        );
    }
    crate::related_cli::run(files[0], vault, output_json)
}

pub fn run(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    if let Some(("related", related)) = command.subcommand() {
        return run_related(related, vault, output_json);
    }
    if let Some(("contact", contact)) = command.subcommand() {
        return crate::contact_cli::run(contact, vault, output_json);
    }
    let Some(("inverse", command)) = command.subcommand() else {
        return crate::process_exit(crate::CoreExitCode::Ok);
    };
    let files: Vec<_> = command
        .get_many::<String>("file")
        .into_iter()
        .flatten()
        .collect();
    if files.len() != 1 {
        return emit_error(
            format!("accepts 1 arg(s), received {}", files.len()),
            output_json,
        );
    }
    let root = match resolve_vault(vault).map_err(|e| {
        if e.starts_with("vault path not configured") {
            "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
        } else {
            crate::index_cli::index_vault_error(vault, &e)
        }
    }) {
        Ok(root) => root,
        Err(e) => return emit_error(e, output_json),
    };
    let mut sidecar = match open_for_vault(&root) {
        Ok(sidecar) => sidecar,
        Err(e) => return emit_error(e.to_string(), output_json),
    };
    let result = secure_path(&root, files[0])
        .map_err(|e| e.to_string())
        .and_then(|target| {
            sidecar
                .inverse_relations(&root, &target.to_string_lossy())
                .map_err(|e| e.to_string())
        });
    if let Err(e) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {e}");
    }
    match result {
        Ok(relations) if output_json => write_go_json(&relations),
        Ok(relations) => write_stdout(format!(
            "[{}]\n",
            relations
                .iter()
                .map(|r| format!(
                    "{{Source:{} Title:{} Property:{}}}",
                    r.source, r.title, r.property
                ))
                .collect::<Vec<_>>()
                .join(" ")
        )),
        Err(e) => emit_error(e, output_json),
    }
}
