//! Go: `cmd/symdesk/dataset.go` dataset producer/import commands.

use std::{collections::BTreeMap, fs, process::ExitCode};

use clap::{Arg, Command};
use serde::Serialize;
use symdesk_index::{DatasetSyncOptions, DatasetSyncRow, DatasetSyncService, open_for_vault};
use symdesk_vault::{PropertyConfig, Provenance};

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
}

pub fn run(command: &clap::ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    match command.subcommand() {
        Some(("sync", args)) => run_sync(args, vault, json_output),
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
