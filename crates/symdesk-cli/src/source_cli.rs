use std::{path::Path, process::ExitCode};

use clap::{Arg, ArgAction, ArgMatches, Command};
use serde_json::json;
use symdesk_index::{SearchSource, SourceRegistry};

use crate::external_index;

pub fn cli() -> Command {
    let argument = |name: &'static str| Arg::new(name).num_args(0..).action(ArgAction::Append);
    Command::new("sources")
        .visible_alias("source")
        .subcommand_required(true)
        .subcommand(Command::new("add").arg(argument("path")))
        .subcommand(Command::new("list").arg(argument("extra")))
        .subcommand(Command::new("remove").arg(argument("id-or-path")))
        .subcommand(Command::new("watch").arg(argument("id-or-path")))
}

/// Go's `cobra.ExactArgs(1)`.
fn single_argument<'a>(args: &'a ArgMatches, name: &str) -> Result<&'a str, String> {
    let values = args
        .get_many::<String>(name)
        .map(|values| values.map(String::as_str).collect::<Vec<_>>())
        .unwrap_or_default();
    match values.as_slice() {
        [value] => Ok(value),
        other => Err(format!("accepts 1 arg(s), received {}", other.len())),
    }
}

/// Go's `%+v` of a `retrieval.Source`.
fn go_source(source: &SearchSource) -> String {
    format!("{{ID:{} Path:{}}}", source.id, source.path)
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    let (name, args) = match command.subcommand() {
        Some(subcommand) => subcommand,
        None => return super::emit_error("unknown sources subcommand".to_owned(), json_output),
    };
    let argument = match name {
        "add" => Some(single_argument(args, "path")),
        "remove" | "watch" => Some(single_argument(args, "id-or-path")),
        _ => None,
    };
    let argument = match argument.transpose() {
        Ok(argument) => argument,
        Err(error) => return super::emit_error(error, json_output),
    };
    let vault = match super::resolve_vault(vault) {
        Ok(path) => path,
        Err(error) => return super::emit_error(error, json_output),
    };
    let registry = match SourceRegistry::open(&vault) {
        Ok(registry) => registry,
        Err(error) => {
            return super::emit_error(format!("validate vault root: {error}"), json_output);
        }
    };
    match (name, argument) {
        ("add", Some(path)) => {
            let source = match registry.add(Path::new(path)) {
                Ok(source) => source,
                Err(error) => return super::emit_error(error.to_string(), json_output),
            };
            if let Err(error) = external_index::index_directory(Path::new(&source.path)) {
                return super::emit_error(
                    format!("index external source {}: {error}", source.path),
                    json_output,
                );
            }
            emit(
                &sorted_object([("status", json!("indexed")), ("source", json!(source))]),
                format!("map[source:{} status:indexed]\n", go_source(&source)),
                json_output,
            )
        }
        ("list", _) => match registry.list() {
            Ok(sources) => {
                if json_output {
                    if sources.is_empty() {
                        super::write_stdout("null\n".to_owned())
                    } else {
                        super::write_go_json(&sources)
                    }
                } else {
                    super::write_stdout(format!(
                        "[{}]\n",
                        sources.iter().map(go_source).collect::<Vec<_>>().join(" ")
                    ))
                }
            }
            Err(error) => super::emit_error(error.to_string(), json_output),
        },
        ("remove", Some(id_or_path)) => {
            let source = match find_source(&registry, id_or_path) {
                Ok(source) => source,
                Err(error) => return super::emit_error(error, json_output),
            };
            let removed = match external_index::remove_directory(Path::new(&source.path)) {
                Ok(removed) => removed,
                Err(error) => {
                    return super::emit_error(
                        format!("remove indexed source {}: {error}", source.path),
                        json_output,
                    );
                }
            };
            if let Err(error) = registry.remove(&source.id) {
                return super::emit_error(error.to_string(), json_output);
            }
            emit(
                &sorted_object([
                    ("status", json!("removed")),
                    ("source", json!(source)),
                    ("removed", json!(removed)),
                ]),
                format!(
                    "map[removed:{removed} source:{} status:removed]\n",
                    go_source(&source)
                ),
                json_output,
            )
        }
        ("watch", Some(id_or_path)) => {
            let source = match find_source(&registry, id_or_path) {
                Ok(source) => source,
                Err(error) => return super::emit_error(error, json_output),
            };
            match external_index::watch_directory(Path::new(&source.path)) {
                Ok(()) => ExitCode::SUCCESS,
                Err(error) => super::emit_error(error, json_output),
            }
        }
        _ => super::emit_error("unknown sources subcommand".to_owned(), json_output),
    }
}

fn find_source(registry: &SourceRegistry, id_or_path: &str) -> Result<SearchSource, String> {
    registry
        .list()
        .map_err(|error| error.to_string())?
        .into_iter()
        .find(|source| source.id == id_or_path || source.path == id_or_path)
        .ok_or_else(|| format!("external source {id_or_path:?} not registered"))
}

/// Go marshals `map[string]interface{}` with sorted keys; the JSON value type
/// used here keeps insertion order, so build the object from a sorted map.
fn sorted_object<const N: usize>(entries: [(&str, serde_json::Value); N]) -> serde_json::Value {
    let sorted = entries
        .into_iter()
        .map(|(key, value)| (key.to_owned(), value))
        .collect::<std::collections::BTreeMap<_, _>>();
    serde_json::to_value(sorted).unwrap_or(serde_json::Value::Null)
}

fn emit(value: &serde_json::Value, text: String, json_output: bool) -> ExitCode {
    if json_output {
        super::write_go_json(value)
    } else {
        super::write_stdout(text)
    }
}
