//! Go: `cmd/symdesk/recipes.go` over `internal/recipes`.
//! `validate` parses and checks a recipe without invoking a runner; `run`
//! asks the configured runner for a reviewable proposal; `diff`, `accept` and
//! `reject` review a recorded run. The behavior lives in `symdesk-service`.

use std::process::ExitCode;

use clap::{Arg, ArgAction, Command};
use serde_json::{Value, json};
use symdesk_core::compose::Resolver;
use symdesk_service::recipes::{self, Change, Recipe};

use crate::{emit_error, resolve_vault_root, write_go_json, write_stdout};

pub fn cli() -> Command {
    let positional = || Arg::new("args").num_args(0..).action(ArgAction::Append);
    Command::new("recipe")
        .about("Validate, run, and review vault automation recipes")
        .subcommand(
            Command::new("validate")
                .about("Validate a declarative recipe without running it")
                .arg(positional()),
        )
        .subcommand(
            Command::new("run")
                .about("Ask the configured runner for a reviewable change proposal")
                .arg(positional())
                .arg(
                    Arg::new("trigger")
                        .long("trigger")
                        .default_value("manual")
                        .help("event trigger for this run"),
                ),
        )
        .subcommand(
            Command::new("diff")
                .about("Review or diff a pending recipe run")
                .arg(positional()),
        )
        .subcommand(
            Command::new("accept")
                .about("Review or accept a pending recipe run")
                .arg(positional()),
        )
        .subcommand(
            Command::new("reject")
                .about("Review or reject a pending recipe run")
                .arg(positional()),
        )
}

/// Cobra `ExactArgs(1)`.
fn exactly_one(args: &clap::ArgMatches) -> Result<String, String> {
    let values = args
        .get_many::<String>("args")
        .map(|values| values.cloned().collect::<Vec<_>>())
        .unwrap_or_default();
    match values.as_slice() {
        [one] => Ok(one.clone()),
        other => Err(format!("accepts 1 arg(s), received {}", other.len())),
    }
}

pub fn run(command: &clap::ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    let Some((name, args)) = command.subcommand() else {
        return emit_error("recipe subcommand is required".to_owned(), json_output);
    };
    let argument = match exactly_one(args) {
        Ok(argument) => argument,
        Err(error) => return emit_error(error, json_output),
    };
    let result = match name {
        "validate" => return validate(&argument, json_output),
        "run" => start(&argument, args, vault, json_output),
        _ => review(name, &argument, vault, json_output),
    };
    match result {
        Ok(code) => code,
        Err(error) => emit_error(error, json_output),
    }
}

fn validate(path: &str, json_output: bool) -> ExitCode {
    let recipe = match recipes::load(path) {
        Ok(recipe) => recipe,
        Err(error) => return emit_error(error, json_output),
    };
    if json_output {
        return write_go_json(&json!({"recipe": recipe, "status": "valid"}));
    }
    write_stdout(format!(
        "map[recipe:{{Version:{} Name:{} Triggers:[{}] Tools:[{}] WriteCap:{}}} status:valid]\n",
        recipe.version,
        recipe.name,
        joined(recipe.triggers.as_deref()),
        joined(recipe.tools.as_deref()),
        recipe.write_cap
    ))
}

fn joined(values: Option<&[String]>) -> String {
    values.unwrap_or_default().join(" ")
}

/// Go `vault.ResolveVaultRoot("", cfg)`: its not-configured wording differs
/// from the service-dependency one the other commands print.
fn vault_root(vault: Option<&str>) -> Result<String, String> {
    resolve_vault_root(vault)
        .map(|path| path.to_string_lossy().into_owned())
        .map_err(|error| {
            if error.starts_with("vault path not configured") {
                "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
            } else {
                error
            }
        })
}

fn start(
    path: &str,
    args: &clap::ArgMatches,
    vault: Option<&str>,
    json_output: bool,
) -> Result<ExitCode, String> {
    let trigger = args
        .get_one::<String>("trigger")
        .map_or("manual", String::as_str);
    let root = vault_root(vault)?;
    let recipe: Recipe = recipes::load(path)?;
    let config = crate::ai_cli::load_config()?;
    let resolver = Resolver::from_environment().map_err(|error| error.to_string())?;
    let manifest = recipes::start(&resolver, &root, &recipe, trigger, &config.recipe_runner)?;
    let trace = recipes::run_dir(&root, &manifest.request.run_id).join("trace.md");
    let trace = trace.to_string_lossy();
    if json_output {
        return Ok(write_go_json(&json!({
            "changes": changes_value(manifest.response.changes.as_deref()),
            "run_id": manifest.request.run_id,
            "status": manifest.status,
            "trace": trace,
        })));
    }
    Ok(write_stdout(format!(
        "map[changes:{} run_id:{} status:{} trace:{trace}]\n",
        changes_text(manifest.response.changes.as_deref()),
        manifest.request.run_id,
        manifest.status
    )))
}

fn review(
    action: &str,
    run_id: &str,
    vault: Option<&str>,
    json_output: bool,
) -> Result<ExitCode, String> {
    let root = vault_root(vault)?;
    let status = match action {
        "diff" => {
            let changes = recipes::pending_diff(&root, run_id)?;
            if json_output {
                return Ok(write_go_json(&changes_value(changes.as_deref())));
            }
            return Ok(write_stdout(format!(
                "{}\n",
                changes_text(changes.as_deref())
            )));
        }
        "accept" => {
            recipes::accept(&root, run_id)?;
            "accepted"
        }
        _ => {
            recipes::reject(&root, run_id)?;
            "rejected"
        }
    };
    if json_output {
        return Ok(write_go_json(&json!({"run_id": run_id, "status": status})));
    }
    Ok(write_stdout(format!(
        "map[run_id:{run_id} status:{status}]\n"
    )))
}

/// A nil Go slice marshals as `null`, an empty one as `[]`.
fn changes_value(changes: Option<&[Change]>) -> Value {
    changes.map_or(Value::Null, |changes| json!(changes))
}

/// Go `%+v` of `[]recipes.Change`; nil and empty both print `[]`.
fn changes_text(changes: Option<&[Change]>) -> String {
    let items = changes
        .unwrap_or_default()
        .iter()
        .map(|change| format!("{{Path:{} Content:{}}}", change.path, change.content))
        .collect::<Vec<_>>();
    format!("[{}]", items.join(" "))
}
