//! Go: `cmd/symdesk/dataset.go` dataset producer/import/query commands.

use std::{collections::BTreeMap, fs, io::Read, path::Path, process::ExitCode};

use cap_std::{ambient_authority, fs::Dir};
use clap::{Arg, Command};
use serde::Serialize;
use serde_json::Value;
use symdesk_index::{
    DatasetQueryFilter, DatasetSyncOptions, DatasetSyncRow, DatasetSyncService, open_for_vault,
};
use symdesk_vault::{PropertyConfig, Provenance, parse_dataset_handle};

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
            Command::new("query")
                .about("Query a dataset with bounded structured selection")
                .arg(Arg::new("dataset").required(true))
                .arg(Arg::new("columns").long("columns").num_args(1))
                .arg(Arg::new("filters").long("filters").num_args(1))
                .arg(
                    Arg::new("limit")
                        .long("limit")
                        .num_args(1)
                        .value_parser(clap::value_parser!(i64)),
                ),
        )
}

pub fn run(command: &clap::ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    match command.subcommand() {
        Some(("sync", args)) => run_sync(args, vault, json_output),
        Some(("query", args)) => run_query(args, vault, json_output),
        Some((other, _)) => emit_error(format!("unknown dataset subcommand: {other}"), json_output),
        None => emit_error("dataset subcommand is required".to_owned(), json_output),
    }
}

fn run_query(args: &clap::ArgMatches, vault: Option<&str>, json_output: bool) -> ExitCode {
    let slug = value_or_empty(args, "dataset");
    let root = match crate::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => return emit_error(error, json_output),
    };
    let rel = format!("datasets/{slug}.md");
    let safe_rel = Path::new(&rel);
    if safe_rel
        .components()
        .any(|component| !matches!(component, std::path::Component::Normal(_)))
    {
        return emit_error(format!("dataset {slug:?} not found"), json_output);
    }
    let vault_dir = match Dir::open_ambient_dir(&root, ambient_authority()) {
        Ok(dir) => dir,
        Err(_) => return emit_error(format!("dataset {slug:?} not found"), json_output),
    };
    let bytes = match read_dataset_handle(&vault_dir, safe_rel) {
        Ok(Some(bytes)) => bytes,
        Ok(None) => return emit_error(format!("dataset {slug:?} not found"), json_output),
        Err(error) => return emit_error(error, json_output),
    };
    let handle = match parse_dataset_handle(&rel, &bytes) {
        Ok(handle) => handle,
        Err(error) => return emit_error(error.to_string(), json_output),
    };
    if handle.schema.is_empty() {
        return emit_error("dataset schema is required".to_owned(), json_output);
    }
    let mut columns: Vec<String> = args
        .get_one::<String>("columns")
        .map(|value| {
            value
                .split(',')
                .map(str::trim)
                .filter(|part| !part.is_empty())
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default();
    let mut seen = std::collections::BTreeSet::new();
    columns.retain(|column| seen.insert(column.clone()));
    if columns.is_empty() {
        columns.extend(handle.schema.keys().cloned());
        columns.sort();
    }
    for column in &columns {
        if !matches!(column.as_str(), "identity" | "_identity" | "_key")
            && !handle.schema.contains_key(column)
        {
            return emit_error(format!("dataset column {column:?} not found"), json_output);
        }
    }
    let requested = args.get_one::<i64>("limit").copied().unwrap_or(0);
    let limit = if requested <= 0 {
        10
    } else {
        requested.min(1000) as usize
    };
    let sidecar = match open_for_vault(&root) {
        Ok(sidecar) => sidecar,
        Err(error) => return emit_error(error.to_string(), json_output),
    };
    let filters = match args.get_one::<String>("filters") {
        Some(input) => match serde_json::from_str::<Vec<DatasetQueryFilter>>(input) {
            Ok(filters) => filters,
            Err(error) => {
                return emit_error(format!("parse --filters: {error}"), json_output);
            }
        },
        None => Vec::new(),
    };
    let schema = handle
        .schema
        .iter()
        .map(|(key, property)| {
            (
                key.clone(),
                if property.r#type.is_empty() {
                    "text".to_owned()
                } else {
                    property.r#type.clone()
                },
            )
        })
        .collect();
    let (total_rows, source_rows) =
        match sidecar.dataset_query_page_filtered(&handle.slug, &schema, &filters, limit) {
            Ok(rows) => rows,
            Err(error) => return emit_error(error.to_string(), json_output),
        };
    let mut rows = Vec::with_capacity(source_rows.len());
    for source in source_rows {
        let values: BTreeMap<String, Value> = match serde_json::from_str(&source.values_json) {
            Ok(values) => values,
            Err(error) => {
                return emit_error(
                    format!("decode dataset row {:?}: {error}", source.row_key),
                    json_output,
                );
            }
        };
        let projected = columns
            .iter()
            .map(|column| {
                let value = match column.as_str() {
                    "identity" | "_identity" => Value::String(source.identity.clone()),
                    "_key" => Value::String(source.row_key.clone()),
                    _ => values.get(column).cloned().unwrap_or(Value::Null),
                };
                (column.clone(), value)
            })
            .collect();
        rows.push(projected);
    }
    output_query_result(
        &QueryJson {
            dataset: &handle.slug,
            columns: &columns,
            total_rows,
            limit,
            returned_rows: rows.len(),
            capped: rows.len() < total_rows,
            rows,
        },
        json_output,
    )
}

fn read_dataset_handle(root: &Dir, rel: &Path) -> Result<Option<Vec<u8>>, String> {
    const MAX_HANDLE_BYTES: u64 = 64 << 20;
    let mut options = cap_std::fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt;
        options.custom_flags(libc::O_NONBLOCK);
    }
    let file = match root.open_with(rel, &options) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(format!("read dataset handle: {error}")),
    };
    let metadata = file
        .metadata()
        .map_err(|error| format!("read dataset handle metadata: {error}"))?;
    if !metadata.is_file() {
        return Err(format!(
            "dataset handle {} is not a regular file",
            rel.display()
        ));
    }
    if metadata.len() > MAX_HANDLE_BYTES {
        return Err(format!(
            "dataset handle {} exceeds the read limit",
            rel.display()
        ));
    }
    let mut bytes = Vec::with_capacity(usize::try_from(metadata.len()).unwrap_or(0));
    file.take(MAX_HANDLE_BYTES + 1)
        .read_to_end(&mut bytes)
        .map_err(|error| format!("read dataset handle: {error}"))?;
    if u64::try_from(bytes.len()).unwrap_or(u64::MAX) > MAX_HANDLE_BYTES {
        return Err(format!(
            "dataset handle {} exceeds the read limit",
            rel.display()
        ));
    }
    Ok(Some(bytes))
}

#[derive(Serialize)]
struct QueryJson<'a> {
    dataset: &'a str,
    columns: &'a [String],
    rows: Vec<BTreeMap<String, Value>>,
    total_rows: usize,
    returned_rows: usize,
    limit: usize,
    capped: bool,
}

fn output_query_result(result: &QueryJson<'_>, json_output: bool) -> ExitCode {
    if json_output {
        return write_go_json(result);
    }
    let columns = result.columns.join(" ");
    let rows = result
        .rows
        .iter()
        .map(|row| {
            let values = row
                .iter()
                .map(|(key, value)| format!("{key}:{}", go_debug_value(value)))
                .collect::<Vec<_>>()
                .join(" ");
            format!("map[{values}]")
        })
        .collect::<Vec<_>>()
        .join(" ");
    write_stdout(format!(
        "&{{Dataset:{} Columns:[{}] Rows:[{}] TotalRows:{} ReturnedRows:{} Limit:{} Capped:{}}}\n",
        result.dataset,
        columns,
        rows,
        result.total_rows,
        result.returned_rows,
        result.limit,
        result.capped
    ))
}

fn go_debug_value(value: &Value) -> String {
    match value {
        Value::Null => "<nil>".to_owned(),
        Value::String(value) => value.clone(),
        Value::Array(values) => format!(
            "[{}]",
            values
                .iter()
                .map(go_debug_value)
                .collect::<Vec<_>>()
                .join(" ")
        ),
        Value::Object(values) => format!(
            "map[{}]",
            values
                .iter()
                .map(|(key, value)| format!("{key}:{}", go_debug_value(value)))
                .collect::<Vec<_>>()
                .join(" ")
        ),
        _ => value.to_string(),
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
