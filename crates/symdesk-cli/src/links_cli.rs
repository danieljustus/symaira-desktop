//! Indexed link inspection commands.
use crate::{emit_error, relative_path, resolve_vault, write_go_json, write_stdout};
use clap::{Arg, ArgMatches, Command};
use std::{fs, process::ExitCode};
use symdesk_index::open_for_vault;
use symdesk_vault::secure_path;

pub fn backlinks_cli() -> Command {
    Command::new("backlinks")
        .about("Get backlinks for a file or opaque contact reference target")
        .arg(Arg::new("file").num_args(0..))
}

pub fn run_backlinks(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
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
    let result = (|| -> Result<Vec<String>, String> {
        let root = resolve_vault(vault).map_err(|e| {
            if e.starts_with("vault path not configured") {
                "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
            } else {
                crate::index_cli::index_vault_error(vault, &e)
            }
        })?;
        let mut sidecar = open_for_vault(&root).map_err(|e| e.to_string())?;
        let contact = files[0].strip_prefix("contact:").is_some_and(|rest| {
            let parts: Vec<_> = rest.split(':').collect();
            parts.len() == 3 && parts.iter().all(|p| !p.is_empty())
        });
        let target = if contact {
            files[0].to_owned()
        } else {
            secure_path(&root, files[0])
                .map_err(|e| e.to_string())?
                .to_string_lossy()
                .into_owned()
        };
        let result = sidecar
            .backlinks(&target, contact)
            .map_err(|e| e.to_string())
            .map(|links| {
                links
                    .into_iter()
                    .map(|p| {
                        let canonical = fs::canonicalize(&p)
                            .map(|p| p.to_string_lossy().into_owned())
                            .unwrap_or(p);
                        relative_path(&root, &canonical)
                    })
                    .collect()
            });
        if let Err(e) = sidecar.close() {
            eprintln!("warning: failed to close sidecar database: {e}");
        }
        result
    })();
    match result {
        Ok(links) if output_json => write_go_json(&links),
        Ok(links) => write_stdout(format!("[{}]\n", links.join(" "))),
        Err(e) => emit_error(e, output_json),
    }
}
