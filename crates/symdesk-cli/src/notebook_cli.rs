//! Notebook CLI reads use the same vault reader as MCP.
use std::process::ExitCode;

use clap::{Arg, ArgMatches, Command};
use symdesk_vault::{list_notebooks, load_notebook, resolve_notebook_sources};

#[derive(serde::Serialize)]
struct NotebookShow<'a> {
    created: &'a str,
    description: &'a str,
    id: &'a str,
    path: &'a str,
    sources: &'a [symdesk_vault::NotebookSource],
    title: &'a str,
}

pub fn cli() -> Command {
    Command::new("notebook")
        .subcommand(Command::new("list").arg(Arg::new("args").num_args(0..)))
        .subcommand(Command::new("show").arg(Arg::new("args").num_args(0..)))
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let Some((name, matches)) = command.subcommand() else {
        return crate::process_exit(crate::CoreExitCode::Ok);
    };
    let args = matches
        .get_many::<String>("args")
        .map(|v| v.cloned().collect::<Vec<_>>())
        .unwrap_or_default();
    if name == "show" && args.len() != 1 {
        return crate::emit_error(format!("accepts 1 arg(s), received {}", args.len()), json);
    }
    let root = match crate::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => {
            return crate::emit_error(
                if error.starts_with("vault path not configured") {
                    "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
                } else {
                    crate::index_cli::index_vault_error(vault, &error)
                },
                json,
            );
        }
    };
    let mut db = match symdesk_index::open_for_vault(&root) {
        Ok(db) => db,
        Err(error) => return crate::emit_error(error.to_string(), json),
    };
    let result = (|| -> Result<ExitCode, String> {
        if name == "list" {
            let notebooks = list_notebooks(&root)?;
            return Ok(if json {
                crate::write_go_json(&notebooks)
            } else {
                crate::write_stdout(format!(
                    "[{}]\n",
                    notebooks
                        .iter()
                        .map(|n| format!("{n:p}"))
                        .collect::<Vec<_>>()
                        .join(" ")
                ))
            });
        }
        let notebook = load_notebook(&root, &args[0])?;
        let sources = resolve_notebook_sources(&root, &notebook);
        Ok(if json {
            crate::write_go_json(&NotebookShow {
                created: &notebook.created,
                description: &notebook.description,
                id: &notebook.id,
                path: &notebook.path,
                sources: &sources,
                title: &notebook.title,
            })
        } else {
            crate::write_stdout(format!(
                "map[created:{} description:{} id:{} path:{} sources:[{}] title:{}]\n",
                notebook.created,
                notebook.description,
                notebook.id,
                notebook.path,
                sources
                    .iter()
                    .map(|s| format!(
                        "{{Path:{} Title:{} Missing:{}}}",
                        s.path, s.title, s.missing
                    ))
                    .collect::<Vec<_>>()
                    .join(" "),
                notebook.title
            ))
        })
    })();
    if let Err(error) = db.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
    match result {
        Ok(exit) => exit,
        Err(error) => crate::emit_error(error, json),
    }
}
