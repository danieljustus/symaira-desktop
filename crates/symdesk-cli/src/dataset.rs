//! Go: `cmd/symdesk/dataset.go` dataset producer/import commands.

use std::{collections::BTreeMap, fs, path::PathBuf, process::ExitCode};

use clap::{Arg, Command};
use serde::Serialize;
use symdesk_index::{
    DatasetImportOptions, DatasetSyncOptions, DatasetSyncRow, DatasetSyncService, open_for_vault,
};
use symdesk_vault::{PropertyConfig, Provenance};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::{emit_error, write_go_json, write_stdout};

pub fn cli() -> Command {
    Command::new("dataset")
        .about("Manage bounded, Markdown-backed datasets")
        .subcommand(
            Command::new("sync")
                .about("Sync producer rows with explicit provenance")
                .arg(Arg::new("dataset").required(true))
                .arg(Arg::new("rows").long("rows").required(true).num_args(1))
                .arg(Arg::new("provenance").long("provenance").num_args(1))
                .arg(Arg::new("source-name").long("source-name").num_args(1))
                .arg(Arg::new("source-sha256").long("source-sha256").num_args(1))
                .arg(Arg::new("imported-at").long("imported-at").num_args(1))
                .arg(Arg::new("title").long("title").num_args(1))
                .arg(
                    Arg::new("identity-field")
                        .long("identity-field")
                        .num_args(1),
                )
                .arg(Arg::new("schema").long("schema").num_args(1))
                .arg(Arg::new("sensitivity").long("sensitivity").num_args(1))
                .arg(
                    Arg::new("retention-rule")
                        .long("retention-rule")
                        .num_args(1),
                ),
        )
        .subcommand(
            Command::new("import")
                .about("Import a CSV file as a Markdown-backed dataset")
                .arg(Arg::new("source").required(true))
                .arg(Arg::new("title").long("title").num_args(1))
                .arg(Arg::new("slug").long("slug").num_args(1))
                .arg(
                    Arg::new("identity-field")
                        .long("identity-field")
                        .num_args(1),
                )
                .arg(Arg::new("schema").long("schema").num_args(1))
                .arg(
                    Arg::new("refresh-command")
                        .long("refresh-command")
                        .num_args(1),
                )
                .arg(Arg::new("sensitivity").long("sensitivity").num_args(1))
                .arg(
                    Arg::new("retention-rule")
                        .long("retention-rule")
                        .num_args(1),
                )
                .arg(
                    Arg::new("imported-at")
                        .long("imported-at")
                        .num_args(1)
                        .help("RFC3339 timestamp (defaults to current UTC time)"),
                ),
        )
}

pub fn run(command: &clap::ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    match command.subcommand() {
        Some(("sync", args)) => run_sync(args, vault, json_output),
        Some(("import", args)) => run_import(args, vault, json_output),
        Some((other, _)) => emit_error(format!("unknown dataset subcommand: {other}"), json_output),
        None => emit_error("dataset subcommand is required".to_owned(), json_output),
    }
}

fn run_sync(args: &clap::ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    let rows_input = args
        .get_one::<String>("rows")
        .map(String::as_str)
        .unwrap_or("");
    let row_bytes = match read_json_input(rows_input) {
        Ok(bytes) => bytes,
        Err(error) => return emit_error(format!("read --rows: {error}"), json_output),
    };
    let rows: Vec<DatasetSyncRow> = match serde_json::from_slice(&row_bytes) {
        Ok(rows) => rows,
        Err(error) => return emit_error(format!("parse --rows: {error}"), json_output),
    };

    let mut provenance = Provenance::default();
    if let Some(input) = args.get_one::<String>("provenance") {
        let bytes = match read_json_input(input) {
            Ok(bytes) => bytes,
            Err(error) => return emit_error(format!("read --provenance: {error}"), json_output),
        };
        provenance = match serde_json::from_slice(&bytes) {
            Ok(provenance) => provenance,
            Err(error) => {
                return emit_error(format!("parse --provenance: {error}"), json_output);
            }
        };
    }
    set_if_present(&mut provenance.source_name, args, "source-name");
    set_if_present(&mut provenance.source_sha256, args, "source-sha256");
    set_if_present(&mut provenance.imported_at, args, "imported-at");

    let schema = match read_optional_schema(args) {
        Ok(schema) => schema,
        Err(error) => return emit_error(error, json_output),
    };
    let root = match crate::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, json_output),
    };
    let mut sidecar = match open_for_vault(&root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), json_output),
    };
    let options = DatasetSyncOptions {
        title: value_or_empty(args, "title"),
        slug: value_or_empty(args, "dataset"),
        identity_field: value_or_empty(args, "identity-field"),
        schema,
        provenance,
        sensitivity: value_or_empty(args, "sensitivity"),
        retention_rule: value_or_empty(args, "retention-rule"),
        rows,
    };
    match DatasetSyncService::new(&root, &mut sidecar).sync(options) {
        Ok(result) => output_sync_result(&result, json_output),
        Err(error) => emit_error(error.to_string(), json_output),
    }
}

fn run_import(args: &clap::ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    let schema = match read_optional_schema(args) {
        Ok(schema) => schema,
        Err(error) => return emit_error(error, json_output),
    };
    let now = match args.get_one::<String>("imported-at") {
        Some(value) => match OffsetDateTime::parse(value, &Rfc3339) {
            Ok(value) => Some(value),
            Err(error) => {
                return emit_error(format!("parse --imported-at: {error}"), json_output);
            }
        },
        None => None,
    };
    let root = match crate::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, json_output),
    };
    let mut sidecar = match open_for_vault(&root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), json_output),
    };
    let options = DatasetImportOptions {
        title: value_or_empty(args, "title"),
        slug: value_or_empty(args, "slug"),
        identity_field: value_or_empty(args, "identity-field"),
        schema,
        refresh_command: value_or_empty(args, "refresh-command"),
        sensitivity: value_or_empty(args, "sensitivity"),
        retention_rule: value_or_empty(args, "retention-rule"),
        now,
    };
    let source = args
        .get_one::<String>("source")
        .map(PathBuf::from)
        .unwrap_or_default();
    match DatasetSyncService::new(&root, &mut sidecar).import_csv(&source, options) {
        Ok(result) => {
            if json_output {
                write_go_json(&result)
            } else {
                let columns = result
                    .columns
                    .iter()
                    .map(|(name, property)| {
                        format!(
                            "{name}:{{Type:{} Label:{} Options:{} Description:{} Default:{}}}",
                            property.r#type,
                            property.label,
                            go_slice(&property.options),
                            property.description,
                            property.default
                        )
                    })
                    .collect::<Vec<_>>()
                    .join(" ");
                write_stdout(format!(
                    "&{{HandlePath:{} RawPath:{} Slug:{} Rows:{} Columns:map[{}] SourceSHA256:{} Sensitivity:{} RetentionRule:{}}}\n",
                    result.handle_path,
                    result.raw_path,
                    result.slug,
                    result.rows,
                    columns,
                    result.source_sha256,
                    result.sensitivity,
                    result.retention_rule
                ))
            }
        }
        Err(error) => emit_error(error.to_string(), json_output),
    }
}

fn read_optional_schema(
    args: &clap::ArgMatches,
) -> Result<BTreeMap<String, PropertyConfig>, String> {
    let Some(input) = args.get_one::<String>("schema") else {
        return Ok(BTreeMap::new());
    };
    let bytes = read_json_input(input).map_err(|error| format!("read --schema: {error}"))?;
    serde_json::from_slice(&bytes).map_err(|error| format!("parse --schema: {error}"))
}

fn read_json_input(input: &str) -> Result<Vec<u8>, std::io::Error> {
    let input = input.trim();
    if input.starts_with('[') || input.starts_with('{') {
        return Ok(input.as_bytes().to_vec());
    }
    if input.is_empty() {
        return Err(std::io::Error::new(
            std::io::ErrorKind::InvalidInput,
            "input is required",
        ));
    }
    fs::read(input)
}

fn set_if_present(target: &mut String, args: &clap::ArgMatches, name: &str) {
    if let Some(value) = args.get_one::<String>(name) {
        target.clone_from(value);
    }
}

fn value_or_empty(args: &clap::ArgMatches, name: &str) -> String {
    args.get_one::<String>(name).cloned().unwrap_or_default()
}

fn go_slice(values: &[String]) -> String {
    if values.is_empty() {
        "[]".to_owned()
    } else {
        format!("[{}]", values.join(" "))
    }
}

#[derive(Serialize)]
struct SyncJson<'a> {
    slug: &'a str,
    rows: usize,
    imported_rows: usize,
    raw_path: &'a str,
    handle_path: &'a str,
    idempotent: bool,
}

fn output_sync_result(result: &symdesk_index::DatasetSyncResult, json_output: bool) -> ExitCode {
    if json_output {
        return write_go_json(&SyncJson {
            slug: &result.slug,
            rows: result.rows,
            imported_rows: result.imported_rows,
            raw_path: &result.raw_path,
            handle_path: &result.handle_path,
            idempotent: result.idempotent,
        });
    }
    write_stdout(format!(
        "&{{Slug:{} Rows:{} ImportedRows:{} RawPath:{} HandlePath:{} Idempotent:{}}}\n",
        result.slug,
        result.rows,
        result.imported_rows,
        result.raw_path,
        result.handle_path,
        result.idempotent
    ))
}
