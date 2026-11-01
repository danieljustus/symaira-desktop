//! `symdesk views exec|new-entry|export-csv|import-csv|embed` (Go
//! `cmd/symdesk/views.go`, `internal/service/views.go`, `views_csv.go`,
//! `views_embed.go`): view execution over the indexed properties and the
//! commands built on it. Storage and parsing stay in symdesk-vault; the
//! sidecar is re-indexed after writes like Go's service.
//!
//! Declared differences from Go are listed in `views_query.rs` plus:
//! * the wording of YAML syntax and type errors of an embed specification
//!   (`yaml.v3` text has no equivalent in the Rust YAML reader);
//! * `export-csv` of a `dataset:` view needs the export-sensitivity policy,
//!   which is not ported: it fails with an explicit message.

use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    io::Write,
    path::Path,
    process::ExitCode,
};

use clap::{Arg, ArgAction, ArgMatches, Command};
use serde::Serialize;
use serde_json::Value;
use symdesk_index::{DatasetViewSpec, Sidecar, docs::DocsFilter};
use symdesk_vault::{
    BASES_DIR, Base, PropertyConfig, View, get_base, go_path_error, list_bases,
    migrate_legacy_views, secure_path, slugify,
};

use crate::{
    mutate_cli::{new_note, parse, reindex, snapshot},
    props_tags_cli::apply_props_edit,
    service_cli::{go_string_map, with_deps},
    views_query::{
        Row, evaluate_formula, evaluate_rollup, matches_filter, matches_group, parse_date_flexible,
        parse_number_flexible, percent_v,
    },
    write_go_json, write_stdout,
};

/// Clap definitions of the five commands (appended to `views`).
pub fn commands() -> Vec<Command> {
    vec![
        Command::new("exec").arg(Arg::new("args").num_args(0..)),
        Command::new("new-entry").arg(Arg::new("args").num_args(0..)),
        Command::new("export-csv")
            .arg(Arg::new("args").num_args(0..))
            // Cobra's local `--output` shadows the persistent switch; the
            // argv rewrite renames it for Clap.
            .arg(Arg::new("local-output").long("local-output").num_args(1)),
        Command::new("import-csv")
            .arg(Arg::new("args").num_args(0..))
            .arg(Arg::new("apply").long("apply").action(ArgAction::SetTrue))
            .arg(Arg::new("folder").long("folder").num_args(1))
            .arg(Arg::new("map").long("map").num_args(1))
            .arg(Arg::new("title-col").long("title-col").num_args(1))
            .arg(Arg::new("base").long("base").num_args(1))
            .arg(Arg::new("on-collision").long("on-collision").num_args(1)),
        Command::new("embed")
            .arg(Arg::new("args").num_args(0..))
            .arg(Arg::new("spec").long("spec").num_args(1))
            .arg(Arg::new("base").long("base").num_args(1))
            .arg(Arg::new("view").long("view").num_args(1))
            .arg(Arg::new("limit").long("limit").num_args(1)),
    ]
}

/// Renames the command-local `--output` of `views export-csv`.
pub fn rewrite_output_flag(args: &mut [std::ffi::OsString]) {
    let Some(at) = args
        .windows(2)
        .position(|pair| pair[0] == "views" && pair[1] == "export-csv")
    else {
        return;
    };
    for argument in args.iter_mut().skip(at + 2) {
        if argument == "--output" {
            *argument = std::ffi::OsString::from("--local-output");
        } else if let Some(value) = argument.to_str().and_then(|v| v.strip_prefix("--output=")) {
            *argument = std::ffi::OsString::from(format!("--local-output={value}"));
        }
    }
}

pub fn is_exec_command(name: &str) -> bool {
    matches!(
        name,
        "exec" | "new-entry" | "export-csv" | "import-csv" | "embed"
    )
}

pub fn run(name: &str, command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let args: Vec<String> = command
        .get_many::<String>("args")
        .map(|values| values.cloned().collect())
        .unwrap_or_default();
    with_deps(vault, json, |root, db| {
        migrate_legacy_views(root);
        match name {
            "exec" => {
                let rows = exec_by_id(root, db, &args[0])?;
                Ok(render_rows(rows.as_deref(), json))
            }
            "new-entry" => new_entry(root, db, &args[0], &args[1], json),
            "export-csv" => export_csv(root, db, &args[0], command, json),
            "import-csv" => import_csv(root, db, &args[0], command, json),
            _ => embed(root, db, &args, command, json),
        }
    })
}

// ---- view lookup and execution ------------------------------------------

fn find_view(root: &Path, id: &str) -> Result<View, String> {
    list_bases(root)
        .map_err(|error| error.to_string())?
        .into_iter()
        .flat_map(|base| base.views)
        .find(|view| view.id == id)
        .ok_or_else(|| "view not found".to_owned())
}

/// A document selected by a view source: stored path and title.
struct SourceDocument {
    path: String,
    title: String,
}

fn contains_tag(tags: &[String], tag: &str) -> bool {
    tags.iter()
        .any(|candidate| symdesk_vault::dataset::go_equal_fold(candidate.trim(), tag))
}

fn go_quote(text: &str) -> String {
    symdesk_vault::go_quote(text)
}

/// Go `resolveViewDocuments`.
fn resolve_documents(
    root: &Path,
    db: &Sidecar,
    view: &View,
) -> Result<Vec<SourceDocument>, String> {
    let source = view.source.trim();
    let listed = |documents: Vec<symdesk_index::ListedDocument>| {
        documents
            .into_iter()
            .map(|document| SourceDocument {
                path: document.path,
                title: document.title,
            })
            .collect::<Vec<_>>()
    };
    if source.is_empty() {
        return db
            .list_files(root, "")
            .map(listed)
            .map_err(|error| error.to_string());
    }
    if let Some(tag) = source.strip_prefix("tag:") {
        let tag = tag.trim();
        let tag = tag.strip_prefix('#').unwrap_or(tag);
        if tag.is_empty() {
            return Err("view source tag cannot be empty".to_owned());
        }
        let docs = db
            .docs_list(&DocsFilter::default())
            .map_err(|error| error.to_string())?;
        return Ok(docs
            .into_iter()
            .filter(|doc| contains_tag(&doc.tags, tag))
            .map(|doc| SourceDocument {
                path: doc.path,
                title: doc.title,
            })
            .collect());
    }
    if let Some(reference) = source.strip_prefix("notebook:") {
        let reference = reference.trim();
        if reference.is_empty() {
            return Err("view source notebook reference cannot be empty".to_owned());
        }
        let notebook = symdesk_vault::load_notebook(root, reference).map_err(|error| {
            format!(
                "view source notebook {} not found: {error}",
                go_quote(reference)
            )
        })?;
        let mut documents = Vec::new();
        for source_path in &notebook.sources {
            let Ok(absolute) = secure_path(root, source_path) else {
                continue;
            };
            let Ok(metadata) = fs::metadata(&absolute) else {
                continue;
            };
            if metadata.is_dir() {
                continue;
            }
            let key = absolute.to_string_lossy().into_owned();
            let title = match db.get_title(&key) {
                Ok(title) if !title.is_empty() => title,
                _ => match parse(&absolute) {
                    Ok(parsed) if !parsed.title.is_empty() => parsed.title,
                    _ => {
                        let base = Path::new(source_path)
                            .file_name()
                            .map(|name| name.to_string_lossy().into_owned())
                            .unwrap_or_default();
                        match base.rfind('.') {
                            Some(dot) => base[..dot].to_owned(),
                            None => base,
                        }
                    }
                },
            };
            documents.push(SourceDocument { path: key, title });
        }
        return Ok(documents);
    }
    let folder = source.strip_prefix('/').unwrap_or(source);
    let absolute = secure_path(root, folder).map_err(|error| {
        format!(
            "view source folder {} is invalid: {error}",
            go_quote(&view.source)
        )
    })?;
    match fs::metadata(&absolute) {
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            return Err(format!(
                "view source folder {} does not exist",
                go_quote(&view.source)
            ));
        }
        Err(error) => {
            return Err(format!(
                "view source folder {}: {}",
                go_quote(&view.source),
                go_path_error("stat", &absolute, &error)
            ));
        }
        Ok(metadata) if !metadata.is_dir() => {
            return Err(format!(
                "view source folder {} is not a directory",
                go_quote(&view.source)
            ));
        }
        Ok(_) => {}
    }
    let mut prefix = symdesk_ingest::ocr::go_clean(&absolute.to_string_lossy());
    prefix.push(std::path::MAIN_SEPARATOR);
    db.list_files(root, &prefix)
        .map(listed)
        .map_err(|error| error.to_string())
}

/// Go `executeDatasetViewWithTotal`.
fn dataset_rows(
    root: &Path,
    db: &Sidecar,
    view: &View,
    limit: i64,
) -> Result<(Vec<Row>, i64), String> {
    let slug = view
        .source
        .trim()
        .strip_prefix("dataset:")
        .unwrap_or("")
        .trim()
        .to_owned();
    if slug.is_empty() {
        return Err("view source dataset slug cannot be empty".to_owned());
    }
    let handle = read_dataset_handle_text(root, &slug)
        .map_err(|inner| format!("dataset {} not found: {inner}", go_quote(&slug)))?;
    let schema: BTreeMap<String, String> = handle
        .schema
        .iter()
        .map(|(key, property)| (key.clone(), property.r#type.clone()))
        .collect();
    let spec = DatasetViewSpec {
        source: view.source.clone(),
        filters: view
            .filters
            .iter()
            .map(|filter| symdesk_index::DatasetQueryFilter {
                key: filter.key.clone(),
                operator: filter.operator.clone(),
                value: filter.value.clone(),
            })
            .collect(),
        filter_group: view.filter_group.clone(),
        sorts: view
            .sorts
            .iter()
            .map(|sort| symdesk_index::DatasetSort {
                key: sort.key.clone(),
                ascending: sort.ascending,
            })
            .collect(),
        group_by: view.group_by.clone(),
        computed: view
            .computed
            .iter()
            .map(|(name, column)| (name.clone(), column.formula.clone(), column.rollup.clone()))
            .collect(),
    };
    db.dataset_view_rows(&schema, &spec, limit)
        .map_err(|error| error.to_string())
}

/// Go `ViewsExec` for a resolved view; `None` stands for Go's nil slice.
fn exec_view(root: &Path, db: &Sidecar, view: &View) -> Result<Option<Vec<Row>>, String> {
    if view.source.trim().starts_with("dataset:") {
        return dataset_rows(root, db, view, 10_000).map(|(rows, _)| Some(rows));
    }
    let documents = resolve_documents(root, db, view)?;
    let mut rows: Vec<Row> = Vec::with_capacity(documents.len());
    let mut index_of: BTreeMap<String, usize> = BTreeMap::new();
    for document in &documents {
        let mut props: Row = db
            .get_properties(&document.path)
            .unwrap_or_default()
            .into_iter()
            .map(|(key, value)| (key, Value::String(value)))
            .collect();
        props.insert("_path".to_owned(), Value::String(document.path.clone()));
        props.insert("_title".to_owned(), Value::String(document.title.clone()));
        index_of.insert(document.path.clone(), rows.len());
        rows.push(props);
    }
    let mut links: BTreeMap<String, Vec<String>> = BTreeMap::new();
    if !view.computed.is_empty()
        && let Ok(edges) = db.all_links()
    {
        for (source, target) in edges {
            links.entry(source).or_default().push(target);
        }
    }
    let mut matched: Vec<usize> = Vec::new();
    for document in &documents {
        let index = index_of[&document.path];
        for (column, computed) in &view.computed {
            let value = if computed.formula.is_empty() {
                if computed.rollup.is_empty() {
                    continue;
                }
                let lookup = |target: &str, property: &str| -> Option<String> {
                    let at = index_of.get(target)?;
                    rows[*at].get(property).map(percent_v)
                };
                evaluate_rollup(&computed.rollup, &document.path, &links, &lookup)
            } else {
                evaluate_formula(&computed.formula, &rows[index])
            };
            rows[index].insert(column.clone(), Value::String(value));
        }
        let props = &rows[index];
        let mut keep = view
            .filters
            .iter()
            .all(|filter| matches_filter(props, filter));
        if keep && let Some(group) = &view.filter_group {
            keep = matches_group(props, group);
        }
        if keep {
            matched.push(index);
        }
    }
    if matched.is_empty() {
        return Ok(None);
    }
    Ok(Some(
        matched.into_iter().map(|at| rows[at].clone()).collect(),
    ))
}

/// Go `readDatasetHandle(root, "datasets/<slug>.md")`; the error is the bare
/// inner text so each caller adds its own wording.
fn read_dataset_handle_text(
    root: &Path,
    slug: &str,
) -> Result<symdesk_vault::DatasetHandle, String> {
    let relative = format!("datasets/{slug}.md");
    let dir = cap_std::fs::Dir::open_ambient_dir(root, cap_std::ambient_authority())
        .map_err(|error| error.to_string())?;
    let bytes = match crate::dataset::read_dataset_handle(&dir, Path::new(&relative)) {
        Ok(Some(bytes)) => bytes,
        Ok(None) => {
            return Err(format!(
                "open vault file {}: openat {relative}: no such file or directory",
                root.join(&relative).display()
            ));
        }
        Err(error) => return Err(error),
    };
    symdesk_vault::parse_dataset_handle(&relative, &bytes).map_err(|error| error.to_string())
}

fn dataset_sensitivity_rank(value: &str) -> i32 {
    match value {
        "public" => 0,
        "internal" => 1,
        "confidential" => 2,
        "restricted" => 3,
        _ => -1,
    }
}

/// Go `checkDatasetExport`: a dataset-sourced view is exported only up to the
/// configured maximum sensitivity of its authoritative handle.
fn check_dataset_export(root: &Path, view: &View) -> Result<(), String> {
    let source = view.source.trim();
    let Some(slug) = source.strip_prefix("dataset:") else {
        return Ok(());
    };
    let slug = slug.trim();
    if slug.is_empty() {
        return Err("dataset export requires a dataset slug".to_owned());
    }
    if slug != symdesk_vault::slugify(slug) {
        return Err(format!(
            "dataset export policy: dataset slug {} is not filesystem-safe",
            go_quote(slug)
        ));
    }
    let handle = read_dataset_handle_text(root, slug)
        .map_err(|error| format!("dataset export policy: {error}"))?;
    if handle.slug != slug {
        return Err(format!(
            "dataset export policy: dataset handle slug {} does not match path slug {}",
            go_quote(&handle.slug),
            go_quote(slug)
        ));
    }
    let configured = crate::ai_cli::load_config()
        .map(|config| config.dataset_export_max_sensitivity)
        .unwrap_or_default();
    let configured = configured.trim().to_lowercase();
    let max = if configured.is_empty() {
        "internal".to_owned()
    } else {
        configured
    };
    if dataset_sensitivity_rank(&max) < 0 {
        return Err(format!(
            "dataset export policy: invalid dataset sensitivity {} (valid: public, internal, confidential, restricted)",
            go_quote(&max)
        ));
    }
    if dataset_sensitivity_rank(&handle.sensitivity) > dataset_sensitivity_rank(&max) {
        return Err(format!(
            "dataset export denied: dataset {} is {}; maximum configured export sensitivity is {max} (set dataset_export_max_sensitivity explicitly to opt in)",
            go_quote(slug),
            handle.sensitivity
        ));
    }
    Ok(())
}

fn exec_by_id(root: &Path, db: &Sidecar, id: &str) -> Result<Option<Vec<Row>>, String> {
    let view = find_view(root, id)?;
    exec_view(root, db, &view)
}

// ---- output --------------------------------------------------------------

fn go_row_text(row: &Row) -> String {
    let entries: Vec<String> = row
        .iter()
        .map(|(key, value)| format!("{key}:{}", crate::dataset::go_debug_value(value)))
        .collect();
    format!("map[{}]", entries.join(" "))
}

fn render_rows(rows: Option<&[Row]>, json: bool) -> ExitCode {
    if json {
        return write_go_json(&rows);
    }
    let rendered: Vec<String> = rows.unwrap_or_default().iter().map(go_row_text).collect();
    write_stdout(format!("[{}]\n", rendered.join(" ")))
}

fn status_map(entries: &[(&str, &str)], json: bool) -> ExitCode {
    let map: BTreeMap<String, String> = entries
        .iter()
        .map(|(key, value)| ((*key).to_owned(), (*value).to_owned()))
        .collect();
    if json {
        write_go_json(&map)
    } else {
        write_stdout(format!("{}\n", go_string_map(map.iter())))
    }
}

// ---- new-entry -------------------------------------------------------------

fn new_entry(
    root: &Path,
    db: &mut Sidecar,
    id: &str,
    title: &str,
    json: bool,
) -> Result<ExitCode, String> {
    let view = find_view(root, id)?;
    let mut template_ref = String::new();
    let mut defaults: BTreeMap<String, String> = BTreeMap::new();
    if let Some(template) = &view.template {
        template_ref.clone_from(&template.r#ref);
        defaults.extend(template.defaults.clone());
    }
    for filter in &view.filters {
        if filter.operator.is_empty()
            || symdesk_vault::dataset::go_equal_fold(&filter.operator, "equals")
        {
            defaults.insert(filter.key.clone(), filter.value.clone());
        }
    }
    let path = new_note(root, db, title, "", &template_ref)?;
    for (key, value) in &defaults {
        apply_props_edit(root, db, &path, key, value)?;
    }
    Ok(status_map(&[("path", &path)], json))
}

// ---- CSV helpers -------------------------------------------------------------

/// Columns shown for rows: the view's, else the sorted union of row keys
/// with the title first (Go `ViewsExportCSV` / `ExecuteBaseEmbed`).
fn visible_columns(preferred: &[String], rows: &[Row]) -> Vec<String> {
    let mut columns: Vec<String> = preferred.to_vec();
    if columns.is_empty() {
        let keys: BTreeSet<&String> = rows
            .iter()
            .flat_map(|row| row.keys())
            .filter(|key| key.as_str() != "_path")
            .collect();
        let sorted: Vec<String> = keys.iter().map(|key| (*key).clone()).collect();
        let without = |target: &str| -> Vec<String> {
            sorted
                .iter()
                .filter(|key| key.as_str() != target)
                .cloned()
                .collect()
        };
        columns = if keys.contains(&"_title".to_owned()) {
            std::iter::once("_title".to_owned())
                .chain(without("_title"))
                .collect()
        } else if keys.contains(&"title".to_owned()) {
            std::iter::once("title".to_owned())
                .chain(without("title"))
                .collect()
        } else {
            sorted
        };
    }
    let mut clean: Vec<String> = columns
        .iter()
        .map(|column| column.trim().to_owned())
        .filter(|column| !column.is_empty())
        .collect();
    if clean.is_empty() {
        clean = vec!["title".to_owned()];
    }
    clean
}

/// A row cell as Go prints it: arrays joined with ", ", nil as empty.
fn cell_text(row: &Row, column: &str) -> String {
    let value = match row.get(column) {
        Some(value) => Some(value),
        None if column == "title" => row.get("_title"),
        None => None,
    };
    match value {
        None | Some(Value::Null) => String::new(),
        Some(Value::Array(items)) => items.iter().map(percent_v).collect::<Vec<_>>().join(", "),
        Some(other) => percent_v(other),
    }
}

/// Go `encoding/csv` `fieldNeedsQuotes` for a comma-separated writer.
fn csv_field_needs_quotes(field: &str) -> bool {
    if field.is_empty() {
        return false;
    }
    if field == "\\." {
        return true;
    }
    if field.contains([',', '"', '\r', '\n']) {
        return true;
    }
    field.chars().next().is_some_and(char::is_whitespace)
}

fn csv_record(fields: &[String]) -> String {
    let mut line = String::new();
    for (index, field) in fields.iter().enumerate() {
        if index > 0 {
            line.push(',');
        }
        if csv_field_needs_quotes(field) {
            line.push('"');
            line.push_str(&field.replace('"', "\"\""));
            line.push('"');
        } else {
            line.push_str(field);
        }
    }
    line.push('\n');
    line
}

fn export_csv(
    root: &Path,
    db: &Sidecar,
    id: &str,
    command: &ArgMatches,
    json: bool,
) -> Result<ExitCode, String> {
    let view = find_view(root, id).map_err(|error| format!("views export csv: {error}"))?;
    check_dataset_export(root, &view)?;
    let rows = exec_view(root, db, &view)
        .map_err(|error| format!("views exec {}: {error}", go_quote(id)))?
        .unwrap_or_default();
    let columns = visible_columns(&view.columns, &rows);
    let mut data = String::new();
    let headers: Vec<String> = columns
        .iter()
        .map(|column| match column.as_str() {
            "_title" => "title".to_owned(),
            "_path" => "path".to_owned(),
            other => other.to_owned(),
        })
        .collect();
    data.push_str(&csv_record(&headers));
    for row in &rows {
        let record: Vec<String> = columns
            .iter()
            .map(|column| cell_text(row, column))
            .collect();
        data.push_str(&csv_record(&record));
    }
    let output = command
        .get_one::<String>("local-output")
        .cloned()
        .unwrap_or_default();
    if output.is_empty() {
        return Ok(write_stdout(data));
    }
    write_private_file(Path::new(&output), data.as_bytes()).map_err(|error| {
        format!(
            "write csv output: {}",
            go_path_error("open", Path::new(&output), &error)
        )
    })?;
    Ok(status_map(
        &[("output", &output), ("status", "exported")],
        json,
    ))
}

/// Go `os.WriteFile(path, data, 0o600)`.
fn write_private_file(path: &Path, data: &[u8]) -> std::io::Result<()> {
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)?.write_all(data)
}

// ---- CSV import -------------------------------------------------------------

#[derive(Serialize)]
struct CsvRowResult {
    line_number: usize,
    title: String,
    path: String,
    properties: BTreeMap<String, Value>,
    status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    error: String,
}

#[derive(Serialize)]
struct MalformedRow {
    line_number: usize,
    raw: String,
    reason: String,
}

#[derive(Serialize)]
struct CsvReport {
    dry_run: bool,
    total_rows: usize,
    imported_count: usize,
    skipped_count: usize,
    collision_count: usize,
    error_count: usize,
    rows: Vec<CsvRowResult>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    malformed_rows: Vec<MalformedRow>,
    #[serde(skip_serializing_if = "String::is_empty")]
    created_base: String,
}

impl CsvReport {
    /// Go `%+v` of the `*CSVImportReport`.
    fn go_text(&self) -> String {
        let rows: Vec<String> = self
            .rows
            .iter()
            .map(|row| {
                let properties: Vec<String> = row
                    .properties
                    .iter()
                    .map(|(key, value)| format!("{key}:{}", percent_v(value)))
                    .collect();
                format!(
                    "{{LineNumber:{} Title:{} Path:{} Properties:map[{}] Status:{} Error:{}}}",
                    row.line_number,
                    row.title,
                    row.path,
                    properties.join(" "),
                    row.status,
                    row.error
                )
            })
            .collect();
        let malformed: Vec<String> = self
            .malformed_rows
            .iter()
            .map(|row| {
                format!(
                    "{{LineNumber:{} Raw:{} Reason:{}}}",
                    row.line_number, row.raw, row.reason
                )
            })
            .collect();
        format!(
            "&{{DryRun:{} TotalRows:{} ImportedCount:{} SkippedCount:{} CollisionCount:{} ErrorCount:{} Rows:[{}] MalformedRows:[{}] CreatedBase:{}}}\n",
            self.dry_run,
            self.total_rows,
            self.imported_count,
            self.skipped_count,
            self.collision_count,
            self.error_count,
            rows.join(" "),
            malformed.join(" "),
            self.created_base
        )
    }
}

/// Go `csv.Reader{LazyQuotes: true, FieldsPerRecord: -1}.ReadAll`.
fn read_lazy_csv(input: &[u8]) -> Vec<Vec<String>> {
    let mut lines: Vec<Vec<u8>> = Vec::new();
    let mut offset = 0;
    while offset < input.len() {
        let rest = &input[offset..];
        let consumed = rest
            .iter()
            .position(|byte| *byte == b'\n')
            .map_or(rest.len(), |index| index + 1);
        let mut line = rest[..consumed].to_vec();
        offset += consumed;
        let has_newline = line.last() == Some(&b'\n');
        if !has_newline && line.last() == Some(&b'\r') {
            line.pop();
        } else if line.ends_with(b"\r\n") {
            let at = line.len() - 2;
            line[at] = b'\n';
            line.pop();
        }
        lines.push(line);
    }
    let newline_len = |line: &[u8]| usize::from(line.last() == Some(&b'\n'));
    let mut records: Vec<Vec<String>> = Vec::new();
    let mut cursor = 0;
    while cursor < lines.len() {
        let mut line = lines[cursor].clone();
        cursor += 1;
        if line.len() == newline_len(&line) {
            continue;
        }
        let mut fields: Vec<Vec<u8>> = Vec::new();
        let mut at = 0;
        'record: loop {
            let remaining = &line[at..];
            if remaining.first() != Some(&b'"') {
                let comma = remaining.iter().position(|byte| *byte == b',');
                let end = comma.unwrap_or_else(|| remaining.len() - newline_len(remaining));
                fields.push(remaining[..end].to_vec());
                if let Some(comma) = comma {
                    at += comma + 1;
                    continue;
                }
                break;
            }
            let mut field = Vec::new();
            at += 1;
            loop {
                let remaining = &line[at..];
                if let Some(quote) = remaining.iter().position(|byte| *byte == b'"') {
                    field.extend_from_slice(&remaining[..quote]);
                    at += quote + 1;
                    let after = &line[at..];
                    match after.first() {
                        Some(b'"') => {
                            field.push(b'"');
                            at += 1;
                        }
                        Some(b',') => {
                            at += 1;
                            fields.push(field);
                            continue 'record;
                        }
                        _ if after.len() == newline_len(after) => {
                            fields.push(field);
                            break 'record;
                        }
                        _ => field.push(b'"'),
                    }
                } else if !remaining.is_empty() {
                    field.extend_from_slice(remaining);
                    if cursor >= lines.len() {
                        fields.push(field);
                        break 'record;
                    }
                    line = lines[cursor].clone();
                    cursor += 1;
                    at = 0;
                } else {
                    // Abrupt end of input inside a quoted field.
                    fields.push(field);
                    break 'record;
                }
            }
        }
        records.push(
            fields
                .into_iter()
                .map(|field| String::from_utf8_lossy(&field).into_owned())
                .collect(),
        );
    }
    records
}

fn sanitize_prop_key(header: &str) -> String {
    let lowered = header.trim().to_lowercase().replace([' ', '-'], "_");
    let kept: String = lowered
        .bytes()
        .filter(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || *byte == b'_')
        .map(char::from)
        .collect();
    let trimmed = kept.trim_matches('_');
    if trimmed.is_empty() {
        "prop".to_owned()
    } else {
        trimmed.to_owned()
    }
}

fn collect_distinct_options(values: &[String]) -> Vec<String> {
    let mut seen = BTreeSet::new();
    let mut options = Vec::new();
    for value in values {
        let value = value.trim();
        if !value.is_empty() && seen.insert(symdesk_vault::go_lowercase(value)) {
            options.push(value.to_owned());
        }
    }
    options.sort();
    options
}

fn infer_property_type(values: &[String]) -> &'static str {
    if values.is_empty() {
        return "text";
    }
    let mut all_numeric = true;
    let mut all_dates = true;
    for value in values {
        let value = value.trim();
        if value.is_empty() {
            continue;
        }
        if parse_number_flexible(value).is_none() {
            all_numeric = false;
        }
        if parse_date_flexible(value).is_none() {
            all_dates = false;
        }
    }
    if all_numeric {
        return "number";
    }
    if all_dates {
        return "date";
    }
    let distinct = collect_distinct_options(values);
    if distinct.len() <= 10 && values.len() >= 3 && distinct.len() < values.len() {
        return "select";
    }
    "text"
}

fn join_folder(folder: &str, file: &str) -> String {
    if folder.is_empty() {
        file.to_owned()
    } else {
        symdesk_ingest::ocr::go_clean(&format!("{folder}/{file}"))
    }
}

fn write_atomic_private(path: &Path, data: &[u8]) -> Result<(), String> {
    let directory = path.parent().unwrap_or_else(|| Path::new("."));
    let mut counter = 0u64;
    let (temp, mut file) = loop {
        let candidate = directory.join(format!(".symdesk-csv-{}{counter}.tmp", std::process::id()));
        let mut options = fs::OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        match options.open(&candidate) {
            Ok(file) => break (candidate, file),
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => counter += 1,
            Err(error) => return Err(format!("create temp file: {error}")),
        }
    };
    let result = file
        .write_all(data)
        .map_err(|error| format!("write temp file: {error}"))
        .and_then(|()| {
            file.sync_all()
                .map_err(|error| format!("sync temp file: {error}"))
        })
        .and_then(|()| {
            drop(file);
            fs::rename(&temp, path).map_err(|error| format!("rename temp file: {error}"))
        });
    if result.is_err() {
        let _ = fs::remove_file(&temp);
    }
    result
}

fn make_dirs_0750(path: &Path) -> std::io::Result<()> {
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o750);
    }
    builder.create(path)
}

fn rfc3339_now() -> String {
    time::OffsetDateTime::now_utc()
        .replace_nanosecond(0)
        .expect("zero nanoseconds")
        .format(&time::format_description::well_known::Rfc3339)
        .unwrap_or_default()
}

#[allow(clippy::too_many_lines)]
fn import_csv(
    root: &Path,
    db: &mut Sidecar,
    file: &str,
    command: &ArgMatches,
    json: bool,
) -> Result<ExitCode, String> {
    if Path::new(file).is_dir() {
        // Go opens the directory, then fails on the first read.
        return Err(format!("parse csv: read {file}: is a directory"));
    }
    let data = fs::read(file).map_err(|error| {
        format!(
            "open csv file: {}",
            go_path_error("open", Path::new(file), &error)
        )
    })?;
    let flag = |name: &str| command.get_one::<String>(name).cloned().unwrap_or_default();
    let apply = command.get_flag("apply");
    let folder = flag("folder");
    let title_column = flag("title-col");
    let base_name = flag("base");
    let mut collision_mode = flag("on-collision").trim().to_lowercase();
    if collision_mode.is_empty() {
        collision_mode = "suffix".to_owned();
    }
    let mut mapping: BTreeMap<String, String> = BTreeMap::new();
    let map_flag = flag("map");
    if !map_flag.is_empty() {
        for pair in map_flag.split(',') {
            if let Some((key, value)) = pair.split_once('=') {
                mapping.insert(key.trim().to_owned(), value.trim().to_owned());
            }
        }
    }
    let records = read_lazy_csv(&data);
    if records.is_empty() {
        return Err("csv is empty".to_owned());
    }
    let headers: Vec<String> = records[0].iter().map(|h| h.trim().to_owned()).collect();
    let mut report = CsvReport {
        dry_run: !apply,
        total_rows: records.len() - 1,
        imported_count: 0,
        skipped_count: 0,
        collision_count: 0,
        error_count: 0,
        rows: Vec::new(),
        malformed_rows: Vec::new(),
        created_base: String::new(),
    };
    let mut property_values: BTreeMap<String, Vec<String>> = BTreeMap::new();
    let folder_rel = folder.trim_matches(['/', '\\']).to_owned();
    let mapped_key = |column: &str| -> String {
        let key = sanitize_prop_key(column);
        if let Some(mapped) = mapping.get(column).filter(|mapped| !mapped.is_empty()) {
            return mapped.clone();
        }
        if let Some(mapped) = mapping.get(&key).filter(|mapped| !mapped.is_empty()) {
            return mapped.clone();
        }
        key
    };
    for (index, row) in records.iter().enumerate().skip(1) {
        let line_number = index + 1;
        if row.is_empty() || (row.len() == 1 && row[0].trim().is_empty()) {
            continue;
        }
        if row.len() != headers.len() {
            report.malformed_rows.push(MalformedRow {
                line_number,
                raw: row.join(","),
                reason: format!(
                    "column count mismatch: expected {}, got {}",
                    headers.len(),
                    row.len()
                ),
            });
            report.error_count += 1;
            continue;
        }
        let mut props: BTreeMap<String, Value> = BTreeMap::new();
        let mut title = String::new();
        for (column_index, column) in headers.iter().enumerate() {
            let value = row[column_index].trim().to_owned();
            let key = mapped_key(column);
            if !title_column.is_empty()
                && (symdesk_vault::dataset::go_equal_fold(column, &title_column)
                    || symdesk_vault::dataset::go_equal_fold(&key, &title_column))
            {
                title.clone_from(&value);
            } else if title.is_empty()
                && (key == "title"
                    || ["title", "name", "subject"]
                        .iter()
                        .any(|name| symdesk_vault::dataset::go_equal_fold(column, name)))
            {
                title.clone_from(&value);
            }
            if !value.is_empty() {
                props.insert(key.clone(), Value::String(value.clone()));
                property_values.entry(key).or_default().push(value);
            }
        }
        if title.is_empty() {
            let first = props
                .get(&sanitize_prop_key(&headers[0]))
                .and_then(Value::as_str)
                .filter(|value| !value.is_empty());
            title = first.map_or_else(|| format!("Row {index}"), str::to_owned);
        }
        let mut slug = slugify(&title);
        if slug.is_empty() {
            slug = format!("row-{index}");
        }
        let mut target = join_folder(&folder_rel, &format!("{slug}.md"));
        let result = |status: &str, error: &str, path: &str| CsvRowResult {
            line_number,
            title: title.clone(),
            path: path.to_owned(),
            properties: props.clone(),
            status: status.to_owned(),
            error: error.to_owned(),
        };
        let mut absolute = match secure_path(root, &target) {
            Ok(path) => path,
            Err(error) => {
                report
                    .rows
                    .push(result("error", &format!("invalid path: {error}"), &target));
                report.error_count += 1;
                continue;
            }
        };
        let mut collided = false;
        if fs::metadata(&absolute).is_ok() {
            collided = true;
            report.collision_count += 1;
            if collision_mode == "skip" {
                report.rows.push(result(
                    "skipped",
                    "file already exists (collision skipped)",
                    &target,
                ));
                report.skipped_count += 1;
                continue;
            }
            if collision_mode == "error" {
                report.rows.push(result(
                    "error",
                    "file already exists (collision error)",
                    &target,
                ));
                report.error_count += 1;
                continue;
            }
            let mut suffix = 2;
            loop {
                target = join_folder(&folder_rel, &format!("{slug}-{suffix}.md"));
                let Ok(next) = secure_path(root, &target) else {
                    break;
                };
                if matches!(fs::metadata(&next), Err(error) if error.kind() == std::io::ErrorKind::NotFound)
                {
                    absolute = next;
                    break;
                }
                suffix += 1;
            }
        }
        let mut frontmatter: BTreeMap<String, noyalib::Value> = BTreeMap::new();
        frontmatter.insert("title".to_owned(), noyalib::Value::from(title.clone()));
        frontmatter.insert("created".to_owned(), noyalib::Value::from(rfc3339_now()));
        let mut is_document = false;
        for (key, value) in &props {
            if key == "title" {
                continue;
            }
            let text = value.as_str().unwrap_or_default();
            if key == "tags" {
                let tags: Vec<noyalib::Value> = text
                    .split(',')
                    .map(str::trim)
                    .filter(|tag| !tag.is_empty())
                    .map(noyalib::Value::from)
                    .collect();
                frontmatter.insert("tags".to_owned(), noyalib::Value::Sequence(tags));
                continue;
            }
            if matches!(
                key.as_str(),
                "document_date" | "correspondent" | "status" | "amount" | "due_date"
            ) {
                is_document = true;
            }
            frontmatter.insert(key.clone(), noyalib::Value::from(text.to_owned()));
        }
        frontmatter.insert(
            "type".to_owned(),
            noyalib::Value::from(if is_document { "document" } else { "note" }),
        );
        let yaml = match symdesk_vault::render_go_yaml_map(&frontmatter) {
            Ok(yaml) => yaml,
            Err(error) => {
                report.rows.push(result(
                    "error",
                    &format!("marshal frontmatter: {error}"),
                    &target,
                ));
                report.error_count += 1;
                continue;
            }
        };
        let content = format!("---\n{yaml}---\n\n# {title}\n");
        if apply {
            if let Err(error) = make_dirs_0750(absolute.parent().unwrap_or(root)) {
                report.rows.push(result(
                    "error",
                    &format!(
                        "create parent directory: {}",
                        go_path_error("mkdir", absolute.parent().unwrap_or(root), &error)
                    ),
                    &target,
                ));
                report.error_count += 1;
                continue;
            }
            if let Err(error) = write_atomic_private(&absolute, content.as_bytes()) {
                report
                    .rows
                    .push(result("error", &format!("write file: {error}"), &target));
                report.error_count += 1;
                continue;
            }
            let _ = reindex(db, &absolute);
            let status = if collided {
                "created (collision suffixed)"
            } else {
                "created"
            };
            report.rows.push(result(status, "", &target));
        } else {
            let status = if collided {
                "preview (collision suffix needed)"
            } else {
                "preview"
            };
            report.rows.push(result(status, "", &target));
        }
        report.imported_count += 1;
    }
    if !base_name.is_empty() {
        let base_title = base_name.trim().to_owned();
        let base_slug = slugify(&base_title);
        let base_path = format!("{BASES_DIR}{}{base_slug}.md", std::path::MAIN_SEPARATOR);
        let mut properties: BTreeMap<String, PropertyConfig> = BTreeMap::new();
        let mut column_order: Vec<String> = Vec::new();
        for header in &headers {
            let key = mapped_key(header);
            if key == "title" {
                continue;
            }
            column_order.push(key.clone());
            let values = property_values.get(&key).cloned().unwrap_or_default();
            let property_type = infer_property_type(&values);
            let options = if property_type == "select" {
                collect_distinct_options(&values)
            } else {
                Vec::new()
            };
            properties.insert(
                key,
                PropertyConfig {
                    r#type: property_type.to_owned(),
                    label: header.clone(),
                    options,
                    ..PropertyConfig::default()
                },
            );
        }
        let source = if folder_rel.is_empty() {
            String::new()
        } else {
            format!("{folder_rel}/")
        };
        let mut columns = vec!["title".to_owned()];
        columns.extend(column_order);
        let mut base = Base {
            id: base_slug.clone(),
            path: base_path.clone(),
            title: base_title.clone(),
            description: format!(
                "Imported from CSV on {}",
                time::OffsetDateTime::now_utc().date()
            ),
            created: rfc3339_now(),
            tags: vec!["base".to_owned()],
            properties,
            views: vec![View {
                id: format!("all_{base_slug}"),
                name: format!("All {base_title}"),
                r#type: "table".to_owned(),
                source,
                columns,
                ..View::default()
            }],
            extras: BTreeMap::new(),
        };
        if apply {
            let take = |path: &Path| snapshot(root, path);
            symdesk_vault::save_base_with_snapshot(root, &mut base, take)
                .map_err(|error| error.to_string())
                .and_then(|()| {
                    let absolute = secure_path(root, &base.path).map_err(|e| e.to_string())?;
                    parse(&absolute)
                        .map_err(|e| format!("wrote base but failed to parse for indexing: {e}"))?;
                    reindex(db, &absolute)
                        .map(|_| ())
                        .map_err(|e| format!("wrote base but failed to index: {e}"))
                })
                .map_err(|error| format!("create base {}: {error}", go_quote(&base_title)))?;
        }
        report.created_base = base_path;
    }
    Ok(if json {
        write_go_json(&report)
    } else {
        write_stdout(report.go_text())
    })
}

// ---- embed ------------------------------------------------------------------

#[derive(Default)]
struct EmbedSpec {
    base: String,
    view: String,
    limit: i64,
    cap: i64,
    columns: Vec<String>,
}

fn yaml_scalar_text(value: &noyalib::Value) -> Option<String> {
    match value {
        noyalib::Value::String(text) => Some(text.clone()),
        noyalib::Value::Bool(flag) => Some(flag.to_string()),
        noyalib::Value::Number(_) => value
            .as_i64()
            .map(|number| number.to_string())
            .or_else(|| value.as_f64().map(|number| number.to_string())),
        noyalib::Value::Null => Some(String::new()),
        _ => None,
    }
}

/// Go `yaml.Unmarshal` into `BaseEmbedSpec` (type-error wording is a
/// declared difference except for the top-level shapes below).
fn parse_embed_spec(text: &str) -> Result<EmbedSpec, String> {
    let invalid = |detail: String| format!("invalid symdesk-base specification: {detail}");
    let value: noyalib::Value =
        noyalib::from_str(text).map_err(|error| invalid(format!("yaml: {error}")))?;
    let mismatch_at = |line: usize, tag: &str, into: &str| {
        invalid(format!(
            "yaml: unmarshal errors:\n  line {line}: cannot unmarshal {tag} into {into}"
        ))
    };
    let mismatch = |tag: &str, into: &str| mismatch_at(1, tag, into);
    // yaml.v3 shows a long scalar as its first seven bytes and an ellipsis.
    let short = |shown: &str| {
        if shown.len() > 10 {
            let mut end = 7;
            while !shown.is_char_boundary(end) {
                end -= 1;
            }
            format!("{}...", &shown[..end])
        } else {
            shown.to_owned()
        }
    };
    // 1-based line of a top-level key, and the first top-level duplicate.
    let key_line = |name: &str| {
        text.lines()
            .position(|line| {
                line.strip_prefix(name)
                    .is_some_and(|rest| rest.trim_start().starts_with(':'))
            })
            .map_or(1, |index| index + 1)
    };
    let mapping = match &value {
        noyalib::Value::Null => return Ok(EmbedSpec::default()),
        noyalib::Value::Mapping(mapping) => mapping,
        noyalib::Value::Sequence(_) => {
            return Err(mismatch("!!seq", "service.BaseEmbedSpec"));
        }
        other => {
            let (tag, shown) = match other {
                noyalib::Value::String(text) => ("!!str", text.clone()),
                noyalib::Value::Bool(flag) => ("!!bool", flag.to_string()),
                noyalib::Value::Number(_) if other.as_i64().is_some() => (
                    "!!int",
                    other.as_i64().map(|n| n.to_string()).unwrap_or_default(),
                ),
                _ => (
                    "!!float",
                    other.as_f64().map(|n| n.to_string()).unwrap_or_default(),
                ),
            };
            return Err(mismatch(
                &format!("{tag} `{}`", short(&shown)),
                "service.BaseEmbedSpec",
            ));
        }
    };
    let mut first_seen: BTreeMap<String, usize> = BTreeMap::new();
    let mut duplicates = Vec::new();
    for (index, line) in text.lines().enumerate() {
        let starts_key = line
            .chars()
            .next()
            .is_some_and(|c| !c.is_whitespace() && !matches!(c, '#' | '-' | '[' | '{'));
        if !starts_key {
            continue;
        }
        let Some((key, _)) = line.split_once(':') else {
            continue;
        };
        let key = key
            .trim()
            .trim_matches(|c| c == '"' || c == '\'')
            .to_owned();
        match first_seen.get(&key) {
            Some(first) => duplicates.push(format!(
                "  line {}: mapping key {} already defined at line {first}",
                index + 1,
                go_quote(&key)
            )),
            None => {
                first_seen.insert(key, index + 1);
            }
        }
    }
    if !duplicates.is_empty() {
        return Err(invalid(format!(
            "yaml: unmarshal errors:\n{}",
            duplicates.join("\n")
        )));
    }
    let mut spec = EmbedSpec::default();
    let string_field = |name: &str| -> Result<String, String> {
        match mapping.get(name) {
            None => Ok(String::new()),
            Some(field) => yaml_scalar_text(field).ok_or_else(|| mismatch("!!seq", "string")),
        }
    };
    spec.base = string_field("base")?;
    spec.view = string_field("view")?;
    for (name, target) in [("limit", &mut spec.limit), ("cap", &mut spec.cap)] {
        match mapping.get(name) {
            None | Some(noyalib::Value::Null) => {}
            Some(field) => {
                *target = field.as_i64().ok_or_else(|| {
                    mismatch_at(
                        key_line(name),
                        &format!(
                            "!!str `{}`",
                            short(&yaml_scalar_text(field).unwrap_or_default())
                        ),
                        "int",
                    )
                })?;
            }
        }
    }
    match mapping.get("columns") {
        None | Some(noyalib::Value::Null) => {}
        Some(noyalib::Value::Sequence(items)) => {
            for item in items {
                spec.columns
                    .push(yaml_scalar_text(item).ok_or_else(|| mismatch("!!seq", "string"))?);
            }
        }
        Some(_) => return Err(mismatch("!!str", "[]string")),
    }
    Ok(spec)
}

#[derive(Serialize)]
struct EmbedResult {
    base_id: String,
    base_path: String,
    base_title: String,
    view_id: String,
    view_name: String,
    columns: Vec<String>,
    rows: Option<Vec<Row>>,
    total_rows: i64,
    capped: bool,
    row_cap: i64,
    markdown: String,
}

fn sanitize_markdown_cell(text: &str) -> String {
    text.replace("\r\n", " ")
        .replace('\n', " ")
        .replace('|', "\\|")
        .trim()
        .to_owned()
}

fn render_markdown_table(
    columns: &[String],
    rows: &[Row],
    base: &Base,
    total_rows: i64,
    capped: bool,
) -> String {
    let mut out = String::from("|");
    for column in columns {
        let shown = match column.as_str() {
            "_title" => "Title".to_owned(),
            "_path" => "Path".to_owned(),
            other => base
                .properties
                .get(other)
                .filter(|property| !property.label.is_empty())
                .map_or_else(|| other.to_owned(), |property| property.label.clone()),
        };
        out.push_str(&format!(" {} |", sanitize_markdown_cell(&shown)));
    }
    out.push_str("\n|");
    for _ in columns {
        out.push_str(" --- |");
    }
    out.push('\n');
    for row in rows {
        out.push('|');
        for column in columns {
            out.push_str(&format!(
                " {} |",
                sanitize_markdown_cell(&cell_text(row, column))
            ));
        }
        out.push('\n');
    }
    if capped {
        out.push_str(&format!(
            "\n*Showing {} of {total_rows} rows. [[{}|Open {}]]*\n",
            rows.len(),
            base.path,
            base.title
        ));
    }
    out
}

fn embed(
    root: &Path,
    db: &Sidecar,
    args: &[String],
    command: &ArgMatches,
    json: bool,
) -> Result<ExitCode, String> {
    let spec_yaml = if let Some(argument) = args.first() {
        match fs::read(argument) {
            Ok(data) => String::from_utf8_lossy(&data).into_owned(),
            Err(_) => argument.clone(),
        }
    } else {
        let flag = |name: &str| command.get_one::<String>(name).cloned().unwrap_or_default();
        let limit = match command.get_one::<String>("limit") {
            Some(raw) => crate::duplicates_cli::parse_go_int(raw).unwrap_or(0),
            None => 0,
        };
        if !flag("spec").is_empty() {
            flag("spec")
        } else if !flag("base").is_empty() {
            let mut yaml = format!("base: {}\n", flag("base"));
            if !flag("view").is_empty() {
                yaml.push_str(&format!("view: {}\n", flag("view")));
            }
            if limit > 0 {
                yaml.push_str(&format!("limit: {limit}\n"));
            }
            yaml
        } else {
            return Err("provide spec YAML as argument or via --spec/--base flags".to_owned());
        }
    };
    let spec = parse_embed_spec(&spec_yaml)?;
    let base_ref = spec.base.trim().to_owned();
    if base_ref.is_empty() {
        return Err("symdesk-base specification requires a non-empty 'base' field".to_owned());
    }
    let base =
        get_base(root, &base_ref).map_err(|_| format!("base {} not found", go_quote(&base_ref)))?;
    if base.views.is_empty() {
        return Err(format!(
            "base {} has no views defined",
            go_quote(&base.title)
        ));
    }
    let spec_view = spec.view.trim();
    let selected = if spec_view.is_empty() {
        &base.views[0]
    } else {
        base.views
            .iter()
            .find(|view| {
                view.id == spec_view || symdesk_vault::dataset::go_equal_fold(&view.name, spec_view)
            })
            .ok_or_else(|| {
                format!(
                    "view {} not found in base {}",
                    go_quote(&spec.view),
                    go_quote(&base.title)
                )
            })?
    };
    let mut row_cap = spec.limit;
    if row_cap <= 0 {
        row_cap = spec.cap;
    }
    if row_cap <= 0 {
        row_cap = 10;
    }
    let wrap = |error: String| format!("execute view {}: {error}", go_quote(&selected.id));
    let (rows, total_rows): (Option<Vec<Row>>, i64) =
        if selected.source.trim().starts_with("dataset:") {
            let (rows, total) = dataset_rows(root, db, selected, row_cap).map_err(wrap)?;
            (Some(rows), total)
        } else {
            let rows = exec_by_id(root, db, &selected.id).map_err(wrap)?;
            let total = i64::try_from(rows.as_ref().map_or(0, Vec::len)).unwrap_or(i64::MAX);
            (rows, total)
        };
    let preferred = if spec.columns.is_empty() {
        selected.columns.clone()
    } else {
        spec.columns.clone()
    };
    let all_rows = rows.clone().unwrap_or_default();
    let columns = visible_columns(&preferred, &all_rows);
    let shown = i64::try_from(all_rows.len()).unwrap_or(i64::MAX);
    let mut capped = shown < total_rows;
    let mut display = rows;
    if shown > row_cap {
        let keep = usize::try_from(row_cap).unwrap_or(usize::MAX);
        if let Some(list) = display.as_mut() {
            list.truncate(keep);
        }
        capped = true;
    }
    let markdown = render_markdown_table(
        &columns,
        display.as_deref().unwrap_or_default(),
        &base,
        total_rows,
        capped,
    );
    let result = EmbedResult {
        base_id: base.id.clone(),
        base_path: base.path.clone(),
        base_title: base.title.clone(),
        view_id: selected.id.clone(),
        view_name: selected.name.clone(),
        columns,
        rows: display,
        total_rows,
        capped,
        row_cap,
        markdown,
    };
    Ok(if json {
        write_go_json(&result)
    } else {
        write_stdout(result.markdown)
    })
}
