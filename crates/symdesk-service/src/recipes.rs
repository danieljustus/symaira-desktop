//! Go `internal/recipes`: the vault-side contract for reviewed automation.
//! Runners only propose changes; SymDesk validates, records and applies them.
//!
//! Every file name, mode, message and ordering below mirrors the Go package;
//! `testdata/port/ai/recipe-run-process.json` is recorded from the real Go CLI.

use std::{
    collections::BTreeSet,
    fs,
    io::{self, Read, Write},
    os::unix::{
        fs::{DirBuilderExt, OpenOptionsExt, PermissionsExt},
        process::ExitStatusExt,
    },
    path::{Path, PathBuf},
    process::{Command, ExitStatus, Stdio},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::{Deserialize, Serialize};
use serde_json::Value;
use symdesk_core::{compose::Resolver, config::go_quote};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::gofmt::{escape_json, join, os_error_text, path_error};

pub const CONTRACT_VERSION: i64 = 1;
const VALID_TRIGGERS: [&str; 4] = ["manual", "save", "commit", "schedule"];
const ZERO_TIME: &str = "0001-01-01T00:00:00Z";

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Recipe {
    pub version: i64,
    pub name: String,
    pub triggers: Option<Vec<String>>,
    pub tools: Option<Vec<String>>,
    pub write_cap: i64,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct Change {
    pub path: String,
    pub content: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct Request {
    pub contract_version: i64,
    pub run_id: String,
    pub vault: String,
    pub recipe: Recipe,
    pub trigger: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct Response {
    pub contract_version: i64,
    pub trace: Option<Vec<String>>,
    pub changes: Option<Vec<Change>>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize)]
pub struct Manifest {
    pub request: Request,
    pub response: Response,
    pub status: String,
    /// RFC 3339 with up to nine fractional digits, as Go's `time.Time` marshals.
    pub created: String,
}

/// Go `Load`: read, parse and validate a recipe file.
///
/// # Errors
///
/// Returns Go's wording for read, parse and validation failures.
pub fn load(path: &str) -> Result<Recipe, String> {
    let bytes = fs::read(path).map_err(|error| path_error("open", Path::new(path), &error))?;
    let input = String::from_utf8(bytes)
        .map_err(|_| "parse recipe: yaml: invalid leading UTF-8 octet".to_owned())?;
    let recipe: Recipe =
        noyalib::from_str(&input).map_err(|error| format!("parse recipe: {error}"))?;
    validate(&recipe)?;
    Ok(recipe)
}

/// Go `Validate`.
///
/// # Errors
///
/// Returns the first violated rule with Go's wording.
pub fn validate(recipe: &Recipe) -> Result<(), String> {
    if recipe.version != CONTRACT_VERSION {
        return Err(format!("unsupported recipe version {}", recipe.version));
    }
    if recipe.name.trim().is_empty() {
        return Err("recipe name is required".to_owned());
    }
    let triggers = recipe.triggers.as_deref().unwrap_or_default();
    if triggers.is_empty() {
        return Err("at least one trigger is required".to_owned());
    }
    for trigger in triggers {
        if !VALID_TRIGGERS.contains(&trigger.as_str()) {
            return Err(format!("unsupported trigger {}", go_quote(trigger)));
        }
    }
    if recipe.write_cap < 0 {
        return Err("write_cap cannot be negative".to_owned());
    }
    let tools = recipe.tools.as_deref().unwrap_or_default();
    if tools.is_empty() {
        return Err("at least one allowed tool is required".to_owned());
    }
    let mut seen = BTreeSet::new();
    for tool in tools {
        if tool.trim().is_empty() {
            return Err("tool allow-list cannot include an empty name".to_owned());
        }
        if !seen.insert(tool.as_str()) {
            return Err(format!("tool {} is listed more than once", go_quote(tool)));
        }
    }
    Ok(())
}

/// Go `Start`: delegate to the configured runner executable. The runner gets a
/// versioned JSON request and can only return proposed file changes. An empty
/// `runner_name` disables execution.
///
/// # Errors
///
/// Returns Go's wording for every validation, spawn, response and persistence
/// failure.
pub fn start(
    resolver: &Resolver,
    vault_root: &str,
    recipe: &Recipe,
    trigger: &str,
    runner_name: &str,
) -> Result<Manifest, String> {
    validate(recipe)?;
    if !VALID_TRIGGERS.contains(&trigger) {
        return Err(format!("unsupported trigger {}", go_quote(trigger)));
    }
    if !recipe
        .triggers
        .as_deref()
        .unwrap_or_default()
        .iter()
        .any(|allowed| allowed == trigger)
    {
        return Err(format!(
            "recipe {} does not allow {trigger} runs",
            go_quote(&recipe.name)
        ));
    }
    if runner_name.is_empty() {
        return Err("no recipe runner configured; set recipe_runner (or SYMDESK_RECIPE_RUNNER) to an executable that speaks the recipe contract".to_owned());
    }
    let runner = resolver.resolve(runner_name).map_err(|_| {
        format!(
            "configured recipe runner {} not found on PATH, in $SYMAIRA_BIN or the managed runtime",
            go_quote(runner_name)
        )
    })?;
    let run_id = format!("{}-{}", unix_nanos(), safe_name(&recipe.name));
    let request = Request {
        contract_version: CONTRACT_VERSION,
        run_id: run_id.clone(),
        vault: vault_root.to_owned(),
        recipe: recipe.clone(),
        trigger: trigger.to_owned(),
    };
    let dir = run_dir(vault_root, &run_id);
    make_dirs(&dir).map_err(|error| error.to_string_for(&dir))?;
    let request_path = dir.join("request.json");
    let response_path = dir.join("response.json");
    write_json(&request_path, &request)?;

    let output = run_combined(
        &runner,
        &[
            "recipe",
            "run",
            "--request",
            &request_path.to_string_lossy(),
            "--response",
            &response_path.to_string_lossy(),
        ],
    );
    match output {
        Err(message) => return Err(format!("recipe runner failed: {message}")),
        Ok((status, combined)) if !status.success() => {
            return Err(format!(
                "recipe runner failed: {}: {}",
                status_text(status),
                combined.trim()
            ));
        }
        Ok(_) => {}
    }
    let bytes = fs::read(&response_path).map_err(|error| {
        format!(
            "recipe runner did not produce a response: {}",
            path_error("open", &response_path, &error)
        )
    })?;
    let response =
        decode_response(&bytes).map_err(|error| format!("parse runner response: {error}"))?;
    if response.contract_version != CONTRACT_VERSION {
        return Err(format!(
            "unsupported runner contract version {}",
            response.contract_version
        ));
    }
    validate_changes(
        vault_root,
        recipe.write_cap,
        response.changes.as_deref().unwrap_or_default(),
    )?;
    let manifest = Manifest {
        request,
        response,
        status: "pending".to_owned(),
        created: format_time(&OffsetDateTime::now_utc()),
    };
    write_json(&dir.join("manifest.json"), &manifest)?;
    write_trace(&dir.join("trace.md"), &manifest)?;
    Ok(manifest)
}

/// Go `Accept`: install every proposed file or none.
///
/// # Errors
///
/// Returns Go's wording for every failure; on a failed install the files that
/// were already replaced are restored.
pub fn accept(vault_root: &str, run_id: &str) -> Result<(), String> {
    accept_with(vault_root, run_id, &mut |from, to| fs::rename(from, to))
}

/// [`accept`] with an injectable rename, so the rollback path is testable.
///
/// # Errors
///
/// See [`accept`].
pub fn accept_with(
    vault_root: &str,
    run_id: &str,
    rename: &mut dyn FnMut(&Path, &Path) -> io::Result<()>,
) -> Result<(), String> {
    let (mut manifest, path) = load_manifest(vault_root, run_id)?;
    if manifest.status != "pending" {
        return Err(format!("run {run_id} is {}", manifest.status));
    }
    let changes = manifest.response.changes.clone().unwrap_or_default();
    validate_changes(vault_root, manifest.request.recipe.write_cap, &changes)?;
    // Validate every destination before changing anything, then prepare each
    // replacement.
    let mut paths = Vec::with_capacity(changes.len());
    for change in &changes {
        let secured = symdesk_vault::secure_path(Path::new(vault_root), &change.path)
            .map_err(|error| error.to_string())?;
        paths.push(secured);
    }
    let mut temps: Vec<Option<PathBuf>> = Vec::with_capacity(changes.len());
    let cleanup = |temps: &[Option<PathBuf>]| {
        for temp in temps.iter().flatten() {
            let _ = fs::remove_file(temp);
        }
    };
    for (index, change) in changes.iter().enumerate() {
        let parent = paths[index].parent().unwrap_or(Path::new("."));
        let prepared = make_dirs(parent)
            .map_err(|error| error.to_string_for(parent))
            .and_then(|()| create_temp(parent, &change.content));
        match prepared {
            Ok(temp) => temps.push(Some(temp)),
            Err(error) => {
                cleanup(&temps);
                return Err(error);
            }
        }
    }
    let result = install(&paths, &mut temps, rename);
    cleanup(&temps);
    result?;
    "accepted".clone_into(&mut manifest.status);
    write_json(&path, &manifest)
}

fn install(
    paths: &[PathBuf],
    temps: &mut [Option<PathBuf>],
    rename: &mut dyn FnMut(&Path, &Path) -> io::Result<()>,
) -> Result<(), String> {
    let mut backups: Vec<Option<Vec<u8>>> = Vec::with_capacity(paths.len());
    for target in paths {
        match read_following_symlinks(target) {
            Ok(bytes) => backups.push(Some(bytes)),
            Err((_, error)) if error.kind() == io::ErrorKind::NotFound => backups.push(None),
            Err((op, error)) => return Err(path_error(op, target, &error)),
        }
    }
    for (index, target) in paths.iter().enumerate() {
        let temp = temps[index].clone().unwrap_or_default();
        if let Err(error) = rename(&temp, target) {
            for (restore, backup) in paths.iter().zip(&backups).take(index) {
                match backup {
                    Some(bytes) => {
                        let _ = fs::write(restore, bytes);
                    }
                    None => {
                        let _ = fs::remove_file(restore);
                    }
                }
            }
            return Err(format!(
                "apply proposal atomically: rename {} {}: {}",
                temp.display(),
                target.display(),
                os_error_text(&error)
            ));
        }
        temps[index] = None;
    }
    Ok(())
}

/// Go `Reject`.
///
/// # Errors
///
/// Returns Go's wording for load, status and write failures.
pub fn reject(vault_root: &str, run_id: &str) -> Result<(), String> {
    let (mut manifest, path) = load_manifest(vault_root, run_id)?;
    if manifest.status != "pending" {
        return Err(format!("run {run_id} is {}", manifest.status));
    }
    "rejected".clone_into(&mut manifest.status);
    write_json(&path, &manifest)
}

/// Go `PendingDiff`: the proposed changes of any recorded run.
///
/// # Errors
///
/// Returns Go's wording when the manifest cannot be loaded.
pub fn pending_diff(vault_root: &str, run_id: &str) -> Result<Option<Vec<Change>>, String> {
    load_manifest(vault_root, run_id).map(|(manifest, _)| manifest.response.changes)
}

/// Directory holding `request.json`, `response.json`, `manifest.json`,
/// `trace.md` of one run.
#[must_use]
pub fn run_dir(vault_root: &str, run_id: &str) -> PathBuf {
    join(&[vault_root, ".symdesk", "runs", run_id])
}

fn validate_changes(root: &str, cap: i64, changes: &[Change]) -> Result<(), String> {
    if i64::try_from(changes.len()).unwrap_or(i64::MAX) > cap {
        return Err(format!(
            "runner proposed {} writes, exceeding write_cap {cap}",
            changes.len()
        ));
    }
    let mut seen = BTreeSet::new();
    for change in changes {
        if change.path.trim().is_empty() {
            return Err("proposed change path is required".to_owned());
        }
        if let Err(error) = symdesk_vault::secure_path(Path::new(root), &change.path) {
            return Err(format!(
                "invalid proposed path {}: {error}",
                go_quote(&change.path)
            ));
        }
        if !seen.insert(change.path.as_str()) {
            return Err(format!(
                "runner proposed duplicate change for {}",
                go_quote(&change.path)
            ));
        }
    }
    Ok(())
}

fn safe_name(value: &str) -> String {
    value
        .chars()
        .map(|c| if c.is_ascii_alphanumeric() { c } else { '-' })
        .collect()
}

fn unix_nanos() -> u128 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |elapsed| elapsed.as_nanos())
}

/// A directory-creation failure carries Go's `MkdirAll` wording.
struct MkdirError(io::Error);
impl MkdirError {
    fn to_string_for(&self, path: &Path) -> String {
        path_error("mkdir", path, &self.0)
    }
}

fn make_dirs(path: &Path) -> Result<(), MkdirError> {
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o755)
        .create(path)
        .map_err(MkdirError)
}

fn create_temp(dir: &Path, content: &str) -> Result<PathBuf, String> {
    for attempt in 0..10_000_u128 {
        let name = format!(
            ".symdesk-proposal-{}",
            (unix_nanos().wrapping_add(attempt) % 4_294_967_295) + 1
        );
        let path = dir.join(name);
        let file = fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&path);
        let mut file = match file {
            Ok(file) => file,
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(error) => return Err(path_error("open", &path, &error)),
        };
        let written = file
            .write_all(content.as_bytes())
            .and_then(|()| file.set_permissions(fs::Permissions::from_mode(0o644)));
        if let Err(error) = written {
            let _ = fs::remove_file(&path);
            return Err(path_error("write", &path, &error));
        }
        return Ok(path);
    }
    Err("createTemp: too many attempts".to_owned())
}

fn read_following_symlinks(path: &Path) -> Result<Vec<u8>, (&'static str, io::Error)> {
    let mut file = fs::File::open(path).map_err(|error| ("open", error))?;
    let mut bytes = Vec::new();
    file.read_to_end(&mut bytes)
        .map_err(|error| ("read", error))?;
    Ok(bytes)
}

fn write_json<T: Serialize>(path: &Path, value: &T) -> Result<(), String> {
    let rendered = serde_json::to_string_pretty(value).map_err(|error| error.to_string())?;
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o644)
        .open(path)
        .map_err(|error| path_error("open", path, &error))?;
    file.write_all(escape_json(&rendered).as_bytes())
        .map_err(|error| path_error("write", path, &error))
}

fn write_trace(path: &Path, manifest: &Manifest) -> Result<(), String> {
    let mut lines: Vec<String> = vec![
        "# Recipe run".to_owned(),
        String::new(),
        format!("Status: {}", manifest.status),
        String::new(),
        "## Trace".to_owned(),
        String::new(),
    ];
    lines.extend(manifest.response.trace.clone().unwrap_or_default());
    lines.extend([
        String::new(),
        "## Proposed changes".to_owned(),
        String::new(),
    ]);
    let mut paths: Vec<&str> = manifest
        .response
        .changes
        .as_deref()
        .unwrap_or_default()
        .iter()
        .map(|change| change.path.as_str())
        .collect();
    paths.sort_unstable();
    lines.extend(paths.into_iter().map(|path| format!("- `{path}`")));
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o644)
        .open(path)
        .map_err(|error| path_error("open", path, &error))?;
    file.write_all(format!("{}\n", lines.join("\n")).as_bytes())
        .map_err(|error| path_error("write", path, &error))
}

fn load_manifest(root: &str, run_id: &str) -> Result<(Manifest, PathBuf), String> {
    let path = run_dir(root, run_id).join("manifest.json");
    let bytes = fs::read(&path).map_err(|error| path_error("open", &path, &error))?;
    let manifest = decode_manifest(&bytes)?;
    Ok((manifest, path))
}

/// Run a program with stdout and stderr on one pipe (Go `CombinedOutput`).
fn run_combined(program: &str, args: &[&str]) -> Result<(ExitStatus, String), String> {
    let (mut reader, writer) = io::pipe().map_err(|error| os_error_text(&error))?;
    let writer_err = writer.try_clone().map_err(|error| os_error_text(&error))?;
    let mut command = Command::new(program);
    command
        .args(args)
        .stdin(Stdio::null())
        .stdout(writer)
        .stderr(writer_err);
    let mut child = command
        .spawn()
        .map_err(|error| format!("fork/exec {program}: {}", os_error_text(&error)))?;
    // The Command still owns the pipe's write ends; release them so reading
    // ends when the runner (and any inherited descendants) close theirs.
    drop(command);
    let mut output = Vec::new();
    let read = reader.read_to_end(&mut output);
    let status = child.wait().map_err(|error| os_error_text(&error))?;
    read.map_err(|error| os_error_text(&error))?;
    Ok((status, String::from_utf8_lossy(&output).into_owned()))
}

fn status_text(status: ExitStatus) -> String {
    if let Some(code) = status.code() {
        return format!("exit status {code}");
    }
    let signal = status.signal().unwrap_or_default();
    let name = match signal {
        1 => "hangup",
        2 => "interrupt",
        3 => "quit",
        6 => "aborted",
        9 => "killed",
        11 => "segmentation fault",
        13 => "broken pipe",
        15 => "terminated",
        _ => return format!("signal: {signal}"),
    };
    format!("signal: {name}")
}

// ---------------------------------------------------------------------------
// encoding/json-compatible decoding (case-insensitive keys, null is a no-op,
// unknown keys ignored, Go's type-error wording).
// ---------------------------------------------------------------------------

fn kind(value: &Value) -> String {
    match value {
        Value::Null => "null".to_owned(),
        Value::Bool(_) => "bool".to_owned(),
        Value::Number(number) => format!("number {number}"),
        Value::String(_) => "string".to_owned(),
        Value::Array(_) => "array".to_owned(),
        Value::Object(_) => "object".to_owned(),
    }
}

fn syntax_error(error: &serde_json::Error) -> String {
    if error.is_eof() {
        "unexpected end of JSON input".to_owned()
    } else {
        // Go prints `invalid character 'x' ...`; the exact byte-level
        // diagnostics of encoding/json are a declared difference.
        error.to_string()
    }
}

struct Fields<'a> {
    values: Vec<Option<&'a Value>>,
}

fn fields<'a>(value: &'a Value, names: &[&str], ty: &str) -> Result<Fields<'a>, String> {
    let mut values = vec![None; names.len()];
    match value {
        Value::Null => {}
        Value::Object(map) => {
            for (key, item) in map {
                let index = names
                    .iter()
                    .position(|name| *name == key)
                    .or_else(|| names.iter().position(|name| name.eq_ignore_ascii_case(key)));
                if let Some(index) = index {
                    values[index] = Some(item);
                }
            }
        }
        other => {
            return Err(format!(
                "json: cannot unmarshal {} into Go value of type {ty}",
                kind(other).split(' ').next().unwrap_or("value")
            ));
        }
    }
    Ok(Fields { values })
}

fn type_error(value: &Value, path: &str, go_type: &str) -> String {
    format!(
        "json: cannot unmarshal {} into Go struct field {path} of type {go_type}",
        kind(value)
    )
}

fn as_int(value: Option<&Value>, path: &str) -> Result<i64, String> {
    match value {
        None | Some(Value::Null) => Ok(0),
        Some(Value::Number(number)) => number
            .as_i64()
            .ok_or_else(|| type_error(&Value::Number(number.clone()), path, "int")),
        Some(other) => Err(type_error(other, path, "int")),
    }
}

fn as_string(value: Option<&Value>, path: &str) -> Result<String, String> {
    match value {
        None | Some(Value::Null) => Ok(String::new()),
        Some(Value::String(text)) => Ok(text.clone()),
        Some(other) => Err(type_error(other, path, "string")),
    }
}

fn as_strings(value: Option<&Value>, path: &str) -> Result<Option<Vec<String>>, String> {
    match value {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Array(items)) => items
            .iter()
            .map(|item| as_string(Some(item), path))
            .collect::<Result<Vec<_>, _>>()
            .map(Some),
        Some(other) => Err(type_error(other, path, "[]string")),
    }
}

fn decode_changes(value: Option<&Value>, path: &str) -> Result<Option<Vec<Change>>, String> {
    match value {
        None | Some(Value::Null) => Ok(None),
        Some(Value::Array(items)) => items
            .iter()
            .map(|item| {
                let f = fields(item, &["path", "content"], "recipes.Change")?;
                Ok(Change {
                    path: as_string(f.values[0], &format!("{path}.path"))?,
                    content: as_string(f.values[1], &format!("{path}.content"))?,
                })
            })
            .collect::<Result<Vec<_>, String>>()
            .map(Some),
        Some(other) => Err(type_error(other, path, "[]recipes.Change")),
    }
}

fn decode_response_value(value: &Value, prefix: &str) -> Result<Response, String> {
    let f = fields(
        value,
        &["contract_version", "trace", "changes"],
        "recipes.Response",
    )?;
    Ok(Response {
        contract_version: as_int(f.values[0], &format!("{prefix}.contract_version"))?,
        trace: as_strings(f.values[1], &format!("{prefix}.trace"))?,
        changes: decode_changes(f.values[2], &format!("{prefix}.changes"))?,
    })
}

/// Decode a runner response like Go's `json.Unmarshal` into `Response`.
fn decode_response(bytes: &[u8]) -> Result<Response, String> {
    let value: Value = serde_json::from_slice(bytes).map_err(|error| syntax_error(&error))?;
    decode_response_value(&value, "Response")
}

fn decode_recipe(value: Option<&Value>, prefix: &str) -> Result<Recipe, String> {
    let Some(value) = value else {
        return Ok(Recipe::default());
    };
    let f = fields(
        value,
        &["version", "name", "triggers", "tools", "write_cap"],
        "recipes.Recipe",
    )?;
    Ok(Recipe {
        version: as_int(f.values[0], &format!("{prefix}.version"))?,
        name: as_string(f.values[1], &format!("{prefix}.name"))?,
        triggers: as_strings(f.values[2], &format!("{prefix}.triggers"))?,
        tools: as_strings(f.values[3], &format!("{prefix}.tools"))?,
        write_cap: as_int(f.values[4], &format!("{prefix}.write_cap"))?,
    })
}

fn decode_manifest(bytes: &[u8]) -> Result<Manifest, String> {
    let value: Value = serde_json::from_slice(bytes).map_err(|error| syntax_error(&error))?;
    let f = fields(
        &value,
        &["request", "response", "status", "created"],
        "recipes.Manifest",
    )?;
    let request = match f.values[0] {
        None => Request::default(),
        Some(item) => {
            let r = fields(
                item,
                &["contract_version", "run_id", "vault", "recipe", "trigger"],
                "recipes.Request",
            )?;
            Request {
                contract_version: as_int(r.values[0], "Manifest.request.contract_version")?,
                run_id: as_string(r.values[1], "Manifest.request.run_id")?,
                vault: as_string(r.values[2], "Manifest.request.vault")?,
                recipe: decode_recipe(r.values[3], "Manifest.request.recipe")?,
                trigger: as_string(r.values[4], "Manifest.request.trigger")?,
            }
        }
    };
    let response = match f.values[1] {
        None => Response::default(),
        Some(item) => decode_response_value(item, "Manifest.response")?,
    };
    let created = match f.values[3] {
        None | Some(Value::Null) => ZERO_TIME.to_owned(),
        Some(Value::String(text)) => {
            let parsed = OffsetDateTime::parse(text, &Rfc3339).map_err(|_| {
                format!(
                    "parsing time {} as \"2006-01-02T15:04:05Z07:00\": cannot parse input",
                    go_quote(text)
                )
            })?;
            format_time(&parsed)
        }
        Some(other) => return Err(type_error(other, "Manifest.created", "time.Time")),
    };
    Ok(Manifest {
        request,
        response,
        status: as_string(f.values[2], "Manifest.status")?,
        created,
    })
}

/// Go `time.Time` JSON: RFC 3339 with the shortest fractional second.
#[must_use]
pub fn format_time(time: &OffsetDateTime) -> String {
    let nanos = time.nanosecond();
    let fraction = if nanos == 0 {
        String::new()
    } else {
        format!(".{nanos:09}").trim_end_matches('0').to_owned()
    };
    let offset = time.offset();
    let zone = if offset.is_utc() {
        "Z".to_owned()
    } else {
        let (hours, minutes, _) = offset.as_hms();
        format!(
            "{}{:02}:{:02}",
            if offset.whole_seconds() < 0 { '-' } else { '+' },
            hours.abs(),
            minutes.abs()
        )
    };
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}{fraction}{zone}",
        time.year(),
        u8::from(time.month()),
        time.day(),
        time.hour(),
        time.minute(),
        time.second()
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicU32, Ordering};

    static COUNTER: AtomicU32 = AtomicU32::new(0);

    fn scratch() -> PathBuf {
        let dir = std::env::temp_dir().join(format!(
            "symdesk-service-recipes-{}-{}",
            std::process::id(),
            COUNTER.fetch_add(1, Ordering::SeqCst)
        ));
        fs::create_dir_all(&dir).unwrap();
        fs::canonicalize(dir).unwrap()
    }

    fn recipe() -> Recipe {
        Recipe {
            version: 1,
            name: "r".to_owned(),
            triggers: Some(vec!["manual".to_owned()]),
            tools: Some(vec!["t".to_owned()]),
            write_cap: 5,
        }
    }

    fn pending_manifest(root: &Path, changes: &[(&str, &str)]) -> String {
        let run_id = "1-r".to_owned();
        let dir = run_dir(&root.to_string_lossy(), &run_id);
        fs::create_dir_all(&dir).unwrap();
        let manifest = Manifest {
            request: Request {
                contract_version: 1,
                run_id: run_id.clone(),
                vault: root.to_string_lossy().into_owned(),
                recipe: recipe(),
                trigger: "manual".to_owned(),
            },
            response: Response {
                contract_version: 1,
                trace: None,
                changes: Some(
                    changes
                        .iter()
                        .map(|(path, content)| Change {
                            path: (*path).to_owned(),
                            content: (*content).to_owned(),
                        })
                        .collect(),
                ),
            },
            status: "pending".to_owned(),
            created: format_time(&OffsetDateTime::now_utc()),
        };
        write_json(&dir.join("manifest.json"), &manifest).unwrap();
        run_id
    }

    #[test]
    fn failed_rename_restores_replaced_files_and_removes_new_ones() {
        let root = scratch();
        fs::write(root.join("old.md"), "original").unwrap();
        let run_id = pending_manifest(
            &root,
            &[
                ("old.md", "changed"),
                ("new.md", "fresh"),
                ("third.md", "x"),
            ],
        );
        let mut calls = 0;
        let error = accept_with(&root.to_string_lossy(), &run_id, &mut |from, to| {
            calls += 1;
            if calls == 3 {
                return Err(io::Error::from_raw_os_error(13));
            }
            fs::rename(from, to)
        })
        .unwrap_err();
        assert!(
            error.starts_with("apply proposal atomically: rename "),
            "{error}"
        );
        assert!(error.ends_with(": permission denied"), "{error}");
        assert_eq!(fs::read_to_string(root.join("old.md")).unwrap(), "original");
        assert!(!root.join("new.md").exists());
        assert!(!root.join("third.md").exists());
        let leftovers: Vec<_> = fs::read_dir(&root)
            .unwrap()
            .map(|entry| entry.unwrap().file_name().to_string_lossy().into_owned())
            .filter(|name| name.starts_with(".symdesk-proposal-"))
            .collect();
        assert!(leftovers.is_empty(), "temp files left: {leftovers:?}");
        let status = load_manifest(&root.to_string_lossy(), &run_id)
            .unwrap()
            .0
            .status;
        assert_eq!(
            status, "pending",
            "a failed install must not change the status"
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn accept_installs_all_files_with_mode_0644() {
        let root = scratch();
        let run_id = pending_manifest(&root, &[("a/b.md", "one"), ("c.md", "two")]);
        accept(&root.to_string_lossy(), &run_id).unwrap();
        assert_eq!(fs::read_to_string(root.join("a/b.md")).unwrap(), "one");
        assert_eq!(
            fs::metadata(root.join("c.md"))
                .unwrap()
                .permissions()
                .mode()
                & 0o777,
            0o644
        );
        assert_eq!(
            accept(&root.to_string_lossy(), &run_id).unwrap_err(),
            format!("run {run_id} is accepted")
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn validation_mirrors_go_wording() {
        let mut r = recipe();
        r.write_cap = -1;
        assert_eq!(validate(&r).unwrap_err(), "write_cap cannot be negative");
        let mut r = recipe();
        r.tools = Some(vec!["a".into(), "a".into()]);
        assert_eq!(
            validate(&r).unwrap_err(),
            "tool \"a\" is listed more than once"
        );
    }

    #[test]
    fn response_decoding_is_case_insensitive_and_type_checked() {
        let response = decode_response(
            br#"{"CONTRACT_VERSION":1,"Trace":["t"],"changes":[{"PATH":"a","extra":1}],"x":null}"#,
        )
        .unwrap();
        assert_eq!(response.contract_version, 1);
        assert_eq!(response.changes.unwrap()[0].path, "a");
        assert_eq!(
            decode_response(br#"{"contract_version":"1"}"#).unwrap_err(),
            "json: cannot unmarshal string into Go struct field Response.contract_version of type int"
        );
        assert_eq!(
            decode_response(b"{\"contract_version\":").unwrap_err(),
            "unexpected end of JSON input"
        );
    }

    #[test]
    fn go_time_format_trims_fraction_and_keeps_offsets() {
        let parsed = OffsetDateTime::parse("2026-01-02T03:04:05.100+02:00", &Rfc3339).unwrap();
        assert_eq!(format_time(&parsed), "2026-01-02T03:04:05.1+02:00");
        let whole = OffsetDateTime::parse("2026-01-02T03:04:05Z", &Rfc3339).unwrap();
        assert_eq!(format_time(&whole), "2026-01-02T03:04:05Z");
    }
}
