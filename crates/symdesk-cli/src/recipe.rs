//! Go: `cmd/symdesk/recipes.go` and `internal/recipes.Validate`.
//! The validate subcommand parses and checks a recipe without invoking a runner.

use std::{fs, process::ExitCode};

use clap::{Arg, Command};
use serde::Serialize;

use crate::{emit_error, write_go_json, write_stdout};

#[derive(Serialize, serde::Deserialize)]
#[serde(default)]
struct Recipe {
    version: i64,
    name: String,
    triggers: Vec<String>,
    tools: Vec<String>,
    write_cap: i64,
}

impl Default for Recipe {
    fn default() -> Self {
        Self {
            version: 0,
            name: String::new(),
            triggers: Vec::new(),
            tools: Vec::new(),
            write_cap: 0,
        }
    }
}

pub fn cli() -> Command {
    Command::new("recipe")
        .about("Validate declarative automation recipes")
        .subcommand(
            Command::new("validate")
                .about("Validate a declarative recipe without running it")
                .arg(Arg::new("recipe").required(true)),
        )
}

pub fn run(command: &clap::ArgMatches, json_output: bool) -> ExitCode {
    let Some(("validate", args)) = command.subcommand() else {
        return emit_error("recipe subcommand is required".to_owned(), json_output);
    };
    let Some(path) = args.get_one::<String>("recipe") else {
        return emit_error("recipe path is required".to_owned(), json_output);
    };
    let input = match fs::read_to_string(path) {
        Ok(input) => input,
        Err(error) => return emit_error(format!("{error}"), json_output),
    };
    let recipe: Recipe = match noyalib::from_str(&input) {
        Ok(recipe) => recipe,
        Err(error) => return emit_error(format!("parse recipe: {error}"), json_output),
    };
    if let Err(error) = validate(&recipe) {
        return emit_error(error, json_output);
    }
    if json_output {
        return write_go_json(&ValidatedRecipe {
            recipe: &recipe,
            status: "valid",
        });
    }
    let triggers = recipe.triggers.join(" ");
    let tools = recipe.tools.join(" ");
    write_stdout(format!(
        "map[recipe:{{Version:{} Name:{} Triggers:[{}] Tools:[{}] WriteCap:{}}} status:valid]\n",
        recipe.version, recipe.name, triggers, tools, recipe.write_cap
    ))
}

#[derive(Serialize)]
struct ValidatedRecipe<'a> {
    recipe: &'a Recipe,
    status: &'static str,
}

fn validate(recipe: &Recipe) -> Result<(), String> {
    if recipe.version != 1 {
        return Err(format!("unsupported recipe version {}", recipe.version));
    }
    if recipe.name.trim().is_empty() {
        return Err("recipe name is required".to_owned());
    }
    if recipe.triggers.is_empty() {
        return Err("at least one trigger is required".to_owned());
    }
    for trigger in &recipe.triggers {
        if !matches!(trigger.as_str(), "manual" | "save" | "commit" | "schedule") {
            return Err(format!("unsupported trigger {trigger:?}"));
        }
    }
    if recipe.write_cap < 0 {
        return Err("write_cap cannot be negative".to_owned());
    }
    if recipe.tools.is_empty() {
        return Err("at least one allowed tool is required".to_owned());
    }
    let mut seen = std::collections::BTreeSet::new();
    for tool in &recipe.tools {
        if tool.trim().is_empty() {
            return Err("tool allow-list cannot include an empty name".to_owned());
        }
        if !seen.insert(tool) {
            return Err(format!("tool {tool:?} is listed more than once"));
        }
    }
    Ok(())
}
