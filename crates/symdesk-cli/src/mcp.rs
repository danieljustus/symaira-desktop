#![allow(clippy::module_name_repetitions)]

mod pdf;
#[cfg(test)]
mod pdf_tests;
mod read;

use std::{
    collections::BTreeMap,
    io::{self, BufRead, Write},
    path::PathBuf,
    sync::{Arc, Mutex, mpsc},
    thread,
};

use crate::search_cli::{self, CliSearchHit};
use serde::{
    Deserialize, Serialize,
    de::{MapAccess, Visitor},
};
use serde_json::{Value, json, value::RawValue};
use symdesk_index::{
    ListedDocument, SearchHit, SearchSource, Sidecar, SourceRegistry, open_for_vault,
};

const PROTOCOL_VERSION: &str = "2024-11-05";
const MAX_MESSAGE_BYTES: usize = 1 << 20;
const PARSE_ERROR: i64 = -32700;
const INVALID_REQUEST: i64 = -32600;
const METHOD_NOT_FOUND: i64 = -32601;
const INVALID_PARAMS: i64 = -32602;
const INTERNAL_ERROR: i64 = -32603;

struct ServerConfig {
    version: String,
    vault: Option<String>,
    allow_write: bool,
    pdf_context: symdesk_render::pdfops::Context,
    write_error: Mutex<Option<io::Error>>,
    /// One sidecar connection kept open for the whole session, like Go's
    /// pooled handle. Without a live connection, concurrently dispatched calls
    /// each re-ran WAL setup, migrations and backfill on a database nobody held
    /// open, which on Windows raced into SQLITE_PROTOCOL (#1190).
    sidecar_anchor: Mutex<Option<Sidecar>>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum ResponseMode {
    Line,
    Framed,
}

#[derive(Clone)]
struct Request {
    id: Value,
    has_id: bool,
    method: String,
    raw_arguments: Option<String>,
    raw_params: Option<String>,
}

#[derive(Default)]
struct OrderedFields<T = Value>(Vec<(String, T)>);

impl<'de, T: Deserialize<'de>> Deserialize<'de> for OrderedFields<T> {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        struct OrderedFieldsVisitor<T>(std::marker::PhantomData<T>);

        impl<'de, T: Deserialize<'de>> Visitor<'de> for OrderedFieldsVisitor<T> {
            type Value = OrderedFields<T>;

            fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                formatter.write_str("an object of ordered string arguments")
            }

            fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
            where
                M: MapAccess<'de>,
            {
                let mut fields = Vec::new();
                while let Some(key) = map.next_key::<String>()? {
                    fields.push((key, map.next_value::<T>()?));
                }
                Ok(OrderedFields(fields))
            }
        }

        deserializer.deserialize_map(OrderedFieldsVisitor(std::marker::PhantomData))
    }
}

#[derive(Serialize)]
struct RpcResponse {
    jsonrpc: &'static str,
    id: Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    result: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<RpcError>,
}

#[derive(Serialize)]
struct RpcError {
    code: i64,
    message: String,
}

#[derive(Serialize)]
struct McpLsEntry {
    path: String,
    title: String,
    #[serde(rename = "type", skip_serializing_if = "String::is_empty")]
    document_type: String,
    modified: String,
}

#[derive(Serialize)]
struct McpSearchResponse {
    results: Vec<CliSearchHit>,
    #[serde(skip_serializing_if = "Option::is_none")]
    hint: Option<&'static str>,
}

#[derive(Debug)]
enum ReadFailure {
    Parse { mode: ResponseMode, message: String },
    InvalidRequest { mode: ResponseMode },
    Fatal(io::Error),
}

enum TransportEvent {
    Read(Result<Option<(Request, ResponseMode)>, ReadFailure>),
    WriteFailed,
}

impl ServerConfig {
    fn record_write_error(&self, error: &io::Error, events: &mpsc::SyncSender<TransportEvent>) {
        self.pdf_context.cancel();
        let mut first = self.write_error.lock().unwrap_or_else(|e| e.into_inner());
        if first.is_none() {
            *first = Some(io::Error::new(error.kind(), error.to_string()));
        }
        // Do not block a handler behind the single buffered read. Either event
        // wakes the server, which checks write_error before another dispatch.
        let _ = events.try_send(TransportEvent::WriteFailed);
    }
}

/// Runs the MCP server on stdin/stdout, read-only unless explicitly opted in.
///
/// Only protocol frames are written to stdout. Diagnostics and transport
/// failures are returned to the caller so the CLI can write them to stderr.
pub fn serve(vault: Option<String>, allow_write: bool) -> io::Result<()> {
    let vault = vault.or_else(|| std::env::var("SYMDESK_VAULT").ok());
    let reader = io::BufReader::new(io::stdin());
    #[cfg(unix)]
    let writer = {
        use std::os::fd::AsFd;
        // Stdout deliberately suppresses EBADF. Protocol transport must retain
        // that error so it can cancel in-flight tools, not report silent success.
        std::fs::File::from(io::stdout().as_fd().try_clone_to_owned()?)
    };
    #[cfg(not(unix))]
    let writer = io::stdout();
    let (_writer, result) = serve_io(reader, writer, vault, allow_write);
    result
}

fn serve_io<R, W>(
    reader: R,
    writer: W,
    vault: Option<String>,
    allow_write: bool,
) -> (W, io::Result<()>)
where
    R: BufRead + Send + 'static,
    W: Write + Send + 'static,
{
    let output = Arc::new(Mutex::new(writer));
    let config = Arc::new(ServerConfig {
        version: super::VERSION.to_owned(),
        vault,
        allow_write,
        pdf_context: symdesk_render::pdfops::Context::default(),
        write_error: Mutex::new(None),
        sidecar_anchor: Mutex::new(None),
    });
    let mut calls = Vec::new();
    let mut terminal_error = None;

    // Like Go ServeIO, a failed writer must wake the server even when the
    // input blocks. Bound read-ahead to one message. A generic non-closable
    // reader may remain blocked after abort, but owns no writer or PDF context.
    let (events, incoming) = mpsc::sync_channel(1);
    let reads = events.clone();
    let input = thread::spawn(move || {
        let mut reader = reader;
        loop {
            let next = read_request(&mut reader);
            let done = matches!(&next, Ok(None) | Err(ReadFailure::Fatal(_)));
            if reads.send(TransportEvent::Read(next)).is_err() || done {
                break;
            }
        }
    });
    let mut input_finished = false;

    while let Ok(event) = incoming.recv() {
        if config
            .write_error
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .is_some()
        {
            break;
        }
        let TransportEvent::Read(next) = event else {
            break;
        };
        match next {
            Ok(None) => {
                input_finished = true;
                break;
            }
            Ok(Some((request, mode))) => {
                if request.method == "tools/call" {
                    let output = Arc::clone(&output);
                    let config = Arc::clone(&config);
                    let events = events.clone();
                    calls.push((
                        mode,
                        request.has_id.then(|| request.id.clone()),
                        thread::spawn(move || {
                            let result = dispatch(&request, mode, &config, &output);
                            if let Err(error) = &result {
                                config.record_write_error(error, &events);
                            }
                            result
                        }),
                    ));
                } else if let Err(error) = dispatch(&request, mode, &config, &output) {
                    config.record_write_error(&error, &events);
                    terminal_error = Some(error);
                    break;
                }
            }
            Err(ReadFailure::Parse { mode, message }) => {
                if let Err(error) = join_calls(&mut calls, &output, &config) {
                    terminal_error = Some(error);
                    break;
                }
                if let Err(error) = send_error(
                    &output,
                    mode,
                    Value::Null,
                    PARSE_ERROR,
                    format!("Parse error: {message}"),
                ) {
                    config.record_write_error(&error, &events);
                    terminal_error = Some(error);
                    break;
                }
            }
            Err(ReadFailure::InvalidRequest { mode }) => {
                if let Err(error) = join_calls(&mut calls, &output, &config) {
                    terminal_error = Some(error);
                    break;
                }
                if let Err(error) = send_error(
                    &output,
                    mode,
                    Value::Null,
                    INVALID_REQUEST,
                    "Invalid Request".to_owned(),
                ) {
                    config.record_write_error(&error, &events);
                    terminal_error = Some(error);
                    break;
                }
            }
            Err(ReadFailure::Fatal(error)) => {
                config.pdf_context.cancel();
                input_finished = true;
                terminal_error = Some(error);
                break;
            }
        }
    }

    // Clean EOF drains without cancellation. Fatal reads cancel before this
    // join; worker write failures cancel immediately, including during drain.
    let drain = join_calls(&mut calls, &output, &config);
    config.pdf_context.cancel();
    drop(incoming);
    if input_finished {
        let _ = input.join();
    }
    let write_error = config
        .write_error
        .lock()
        .unwrap_or_else(|e| e.into_inner())
        .take();
    let result = write_error.or(terminal_error).map_or(drain, Err);
    match Arc::try_unwrap(output) {
        Ok(mutex) => (
            mutex
                .into_inner()
                .unwrap_or_else(|poisoned| poisoned.into_inner()),
            result,
        ),
        Err(_) => panic!("MCP output still has active references after joining handlers"),
    }
}

type PendingCall = (
    ResponseMode,
    Option<Value>,
    thread::JoinHandle<io::Result<()>>,
);

fn join_calls<W>(
    calls: &mut Vec<PendingCall>,
    output: &Arc<Mutex<W>>,
    config: &Arc<ServerConfig>,
) -> io::Result<()>
where
    W: Write + Send + 'static,
{
    let mut first_error = None;
    for (mode, id, call) in calls.drain(..) {
        let result = match call.join() {
            Ok(result) => result,
            Err(_) => match id {
                Some(id) => send_error(
                    output,
                    mode,
                    id,
                    INTERNAL_ERROR,
                    "Internal error: handler panicked".to_owned(),
                ),
                None => Ok(()),
            },
        };
        if let Err(error) = result {
            config.pdf_context.cancel();
            first_error.get_or_insert(error);
        }
    }
    first_error.map_or(Ok(()), Err)
}

fn dispatch<W>(
    request: &Request,
    mode: ResponseMode,
    config: &ServerConfig,
    output: &Arc<Mutex<W>>,
) -> io::Result<()>
where
    W: Write + Send + 'static,
{
    // Only tools/call has side effects. Dispatch it even without an id;
    // dispatch_tool_call suppresses its success and error responses.
    if !request.has_id && request.method != "tools/call" {
        return Ok(());
    }

    match request.method.as_str() {
        "initialize" => send_result(
            output,
            mode,
            request.id.clone(),
            json!({
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {"tools": {}},
                "serverInfo": {"name": "symdesk", "version": config.version},
            }),
        ),
        "ping" => send_result(output, mode, request.id.clone(), json!({})),
        "tools/list" => send_tools_list(output, mode, request.id.clone(), config.allow_write),
        "tools/call" => dispatch_tool_call(request, mode, config, output),
        method => send_error(
            output,
            mode,
            request.id.clone(),
            METHOD_NOT_FOUND,
            format!("Method not found: {method}"),
        ),
    }
}

fn dispatch_tool_call<W>(
    request: &Request,
    mode: ResponseMode,
    config: &ServerConfig,
    output: &Arc<Mutex<W>>,
) -> io::Result<()>
where
    W: Write + Send + 'static,
{
    let name = match tool_name(request) {
        Ok(name) => name,
        Err(error) => {
            if !request.has_id {
                return Ok(());
            }
            return send_error(
                output,
                mode,
                request.id.clone(),
                INVALID_PARAMS,
                format!("Invalid params: {error}"),
            );
        }
    };
    let name = name.as_str();
    if !matches!(
        name,
        "desk_status" | "desk_ls" | "desk_search" | "desk_ask" | "search_documents"
    ) && !read::supports(name)
        && !(config.allow_write && pdf::canonical_name(name).is_some())
    {
        if !request.has_id {
            return Ok(());
        }
        return send_error(
            output,
            mode,
            request.id.clone(),
            METHOD_NOT_FOUND,
            format!("Unknown tool: {name}"),
        );
    }

    let result = call_tool(name, request.raw_arguments.as_deref(), config);
    if !request.has_id {
        return Ok(());
    }
    match result {
        Ok(value) => send_tool_result(output, mode, request.id.clone(), value, false),
        Err(message) => send_tool_result(
            output,
            mode,
            request.id.clone(),
            Value::String(message),
            true,
        ),
    }
}

fn call_tool(
    name: &str,
    raw_arguments: Option<&str>,
    config: &ServerConfig,
) -> Result<Value, String> {
    match name {
        "desk_status" => Ok(json!({
            "version": config.version,
            "vault": config.vault.clone().unwrap_or_default(),
            "capabilities": if config.allow_write { "read_write" } else { "read_only" },
        })),
        "desk_ls" => {
            let args = go_string_fields(
                raw_arguments,
                &["dir"],
                r#"struct { Dir string "json:\"dir\"" }"#,
            )?;
            let dir = &args[0];
            let (vault, mut sidecar) = open_sidecar(config)?;
            let mut files = sidecar
                .list_files(&vault, dir)
                .map_err(|error| error.to_string())?;
            if files.is_empty() {
                sidecar
                    .refresh_index(&vault)
                    .map_err(|error| error.to_string())?;
                files = sidecar
                    .list_files(&vault, dir)
                    .map_err(|error| error.to_string())?;
            }
            if files.is_empty() {
                return Ok(Value::String("null".to_owned()));
            }
            Ok(Value::String(
                serde_json::to_string(
                    &files
                        .iter()
                        .map(|file| list_entry(&vault, file))
                        .collect::<Vec<_>>(),
                )
                .map_err(|error| error.to_string())?,
            ))
        }
        "desk_search" | "search_documents" => {
            let (query, _) = go_string_arguments(raw_arguments, false)?;
            if query.is_empty() {
                return Err("query is required".to_owned());
            }
            let (vault, sidecar) = open_sidecar(config)?;
            let sources = SourceRegistry::open(&vault)
                .and_then(|registry| registry.list())
                .map_err(|error| error.to_string())?;
            let response = if query.trim().is_empty() {
                McpSearchResponse {
                    results: Vec::new(),
                    hint: None,
                }
            } else {
                match search_cli::hybrid_search(&vault, &query, &sources, &sidecar)? {
                    Some(results) => McpSearchResponse {
                        results,
                        hint: None,
                    },
                    None => {
                        let response = sidecar
                            .search_plan(&vault, &query)
                            .map_err(|error| error.to_string())?;
                        McpSearchResponse {
                            results: response
                                .results
                                .iter()
                                .map(|hit| search_entry(&vault, hit, &sources))
                                .collect(),
                            hint: response.hint,
                        }
                    }
                }
            };
            Ok(Value::String(
                serde_json::to_string(&response).map_err(|error| error.to_string())?,
            ))
        }
        "desk_ask" => {
            let (query, notebook) = go_string_arguments(raw_arguments, true)?;
            if query.is_empty() {
                return Err("query is required".to_owned());
            }
            crate::ai_cli::ensure_offline_ask_provider()?;
            let (vault, sidecar) = open_sidecar(config)?;
            let sources = if notebook.is_empty() {
                SourceRegistry::open(&vault)
                    .and_then(|registry| registry.list())
                    .map_err(|error| error.to_string())?
            } else {
                Vec::new()
            };
            let hits = crate::ai_cli::search_for_ask(
                &vault,
                &query,
                (!notebook.is_empty()).then_some(notebook.as_str()),
                &sources,
                &sidecar,
            )?;
            let paths = hits
                .iter()
                .map(|hit| crate::ai_cli::ask_display_path(&vault, &hit.path, &sources))
                .collect::<Vec<_>>();
            let answer = crate::ai_cli::offline_ask_chunks(&paths).concat();
            Ok(json!({"answer": answer}))
        }
        _ if read::supports(name) => read::call(name, raw_arguments, config),
        _ if config.allow_write && pdf::canonical_name(name).is_some() => {
            pdf::call(name, raw_arguments, &config.pdf_context)
        }
        _ => Err(format!("Unknown tool: {name}")),
    }
}

// Match the shared Go tools/call parameter decoder before tool admission.
// Duplicate/null names and malformed _meta must not bypass that boundary.
fn tool_name(request: &Request) -> Result<String, String> {
    let raw = request
        .raw_params
        .as_deref()
        .ok_or_else(|| "unexpected end of JSON input".to_owned())?;
    if !raw.starts_with('{') {
        return Err(r#"json: cannot unmarshal array into Go value of type struct { Name string "json:\"name\""; Arguments json.RawMessage "json:\"arguments\""; Meta map[string]interface {} "json:\"_meta,omitempty\"" }"#.into());
    }
    let fields = go_raw_fields(raw)?;
    let mut name = String::new();
    for (key, value) in fields.0 {
        let is_name = symdesk_vault::dataset::go_equal_fold(&key, "name");
        let is_meta = symdesk_vault::dataset::go_equal_fold(&key, "_meta");
        let raw = value.get();
        if (!is_name && !is_meta) || raw == "null" {
            continue;
        }
        if is_name && raw.starts_with('"') {
            name = serde_json::from_str(raw).map_err(|error| error.to_string())?;
        } else if is_meta && raw.starts_with('{') {
            validate_meta_numbers(raw)?;
        } else {
            let value_type = go_json_type(raw);
            let (field, target) = if is_name {
                ("name", "string")
            } else {
                ("_meta", "map[string]interface {}")
            };
            return Err(format!(
                "json: cannot unmarshal {value_type} into Go struct field .{field} of type {target}"
            ));
        }
    }
    Ok(name)
}

fn go_json_type(raw: &str) -> &str {
    match raw.as_bytes().first() {
        Some(b'"') => "string",
        Some(b'[') => "array",
        Some(b'{') => "object",
        Some(b't' | b'f') => "bool",
        Some(b'n') => "null",
        _ => "number",
    }
}

// Go encoding/json accepts lone UTF-16 surrogates as U+FFFD, including keys.
// Normalize only those escapes, before serde's typed string decoding. Keep
// numbers raw, preserve escaped backslashes/pairs, and bound nesting without
// recursive Value construction (Go's scanner limit is 10,000 containers).
fn go_json_text(data: &[u8]) -> Result<Vec<u8>, String> {
    let mut bytes = data.to_vec();
    let mut quoted = false;
    let mut depth = 0_usize;
    let mut index = 0;
    while index < bytes.len() {
        match bytes[index] {
            b'\\' if quoted => {
                let hex = |at: usize| -> Option<u16> {
                    if bytes.get(at..at + 2)? != b"\\u" {
                        return None;
                    }
                    u16::from_str_radix(std::str::from_utf8(bytes.get(at + 2..at + 6)?).ok()?, 16)
                        .ok()
                };
                if let Some(first) = hex(index) {
                    if (0xd800..=0xdbff).contains(&first)
                        && hex(index + 6).is_some_and(|next| (0xdc00..=0xdfff).contains(&next))
                    {
                        index += 12;
                        continue;
                    }
                    if (0xd800..=0xdfff).contains(&first) {
                        bytes[index..index + 6].copy_from_slice(b"\\ufffd");
                    }
                    index += 6;
                } else {
                    index += 2;
                }
                continue;
            }
            b'"' => quoted = !quoted,
            b'[' | b'{' if !quoted => {
                depth += 1;
                if depth > 10_000 {
                    return Err(format!(
                        "invalid character '{}' exceeded max depth",
                        char::from(bytes[index])
                    ));
                }
            }
            b']' | b'}' if !quoted => depth = depth.saturating_sub(1),
            _ => {}
        }
        index += 1;
    }
    Ok(bytes)
}

fn go_raw_fields(raw: &str) -> Result<OrderedFields<Box<RawValue>>, String> {
    let bytes = go_json_text(raw.as_bytes())?;
    serde_json::from_slice(&bytes).map_err(|error| error.to_string())
}

// _meta is map[string]any in Go, unlike arguments (RawMessage). Its numeric
// leaves must fit float64, even in unknown nested fields or duplicate keys.
// The JSON grammar has already been checked; scan leaves in source order with
// constant stack space, never converting ignored tool arguments to Value.
fn validate_meta_numbers(raw: &str) -> Result<(), String> {
    let bytes = raw.as_bytes();
    let mut quoted = false;
    let mut index = 0;
    while index < bytes.len() {
        match bytes[index] {
            b'\\' if quoted => {
                index += 2;
                continue;
            }
            b'"' => quoted = !quoted,
            b'-' | b'0'..=b'9' if !quoted => {
                let start = index;
                while index < bytes.len()
                    && matches!(bytes[index], b'0'..=b'9' | b'-' | b'+' | b'.' | b'e' | b'E')
                {
                    index += 1;
                }
                let token = &raw[start..index];
                if !token.parse::<f64>().is_ok_and(f64::is_finite) {
                    return Err(format!(
                        "json: cannot unmarshal number {token} into Go struct field ._meta of type float64"
                    ));
                }
                continue;
            }
            _ => {}
        }
        index += 1;
    }
    Ok(())
}

fn go_string_arguments(
    raw_arguments: Option<&str>,
    include_notebook: bool,
) -> Result<(String, String), String> {
    let (names, target): (&[&str], &str) = if include_notebook {
        (
            &["query", "notebook"],
            r#"struct { Query string "json:\"query\""; Notebook string "json:\"notebook\"" }"#,
        )
    } else {
        (&["query"], r#"struct { Query string "json:\"query\"" }"#)
    };
    let mut values = go_string_fields(raw_arguments, names, target)?.into_iter();
    Ok((
        values.next().unwrap_or_default(),
        values.next().unwrap_or_default(),
    ))
}

fn go_string_fields(
    raw: Option<&str>,
    names: &[&str],
    target: &str,
) -> Result<Vec<String>, String> {
    let raw = raw.ok_or_else(|| "unexpected end of JSON input".to_owned())?;
    let mut values = vec![String::new(); names.len()];
    if raw == "null" {
        return Ok(values);
    }
    if !raw.starts_with('{') {
        return Err(format!(
            "json: cannot unmarshal {} into Go value of type {target}",
            go_json_type(raw)
        ));
    }
    // Unknown values remain RawMessage: their depth/numeric representation
    // must not overwrite an earlier typed-field error with missing arguments.
    for (key, value) in go_raw_fields(raw)?.0 {
        let Some(index) = names
            .iter()
            .position(|name| symdesk_vault::dataset::go_equal_fold(&key, name))
        else {
            continue;
        };
        let raw = value.get();
        if raw == "null" {
            continue;
        }
        if !raw.starts_with('"') {
            return Err(go_string_field_type_error(go_json_type(raw), names[index]));
        }
        values[index] = serde_json::from_str(raw).map_err(|error| error.to_string())?;
    }
    Ok(values)
}

fn go_string_field_type_error(value_type: &str, field: &str) -> String {
    format!("json: cannot unmarshal {value_type} into Go struct field .{field} of type string")
}

fn open_sidecar(config: &ServerConfig) -> Result<(PathBuf, Sidecar), String> {
    let vault = super::resolve_vault(config.vault.as_deref()).map_err(|error| {
        if error == "vault path not configured (use --vault, SYMDESK_VAULT env, or config file)" {
            "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
        } else {
            error
        }
    })?;
    {
        // The first call sets the database up once and keeps that connection;
        // later calls block here only until it exists. Calls still run
        // concurrently with their own connections afterwards.
        let mut anchor = config
            .sidecar_anchor
            .lock()
            .map_err(|_| "sidecar anchor lock poisoned".to_owned())?;
        if anchor.is_none() {
            *anchor = Some(open_for_vault(&vault).map_err(|error| error.to_string())?);
        }
    }
    let sidecar = open_for_vault(&vault).map_err(|error| error.to_string())?;
    Ok((vault, sidecar))
}

fn list_entry(root: &std::path::Path, file: &ListedDocument) -> McpLsEntry {
    McpLsEntry {
        // Go's MCP Ls returns filepath.Rel, i.e. native separators.
        path: super::native_relative_path(root, &file.path),
        title: file.title.clone(),
        document_type: file.document_type.clone(),
        modified: file.modified_at.clone(),
    }
}

fn search_entry(root: &std::path::Path, hit: &SearchHit, sources: &[SearchSource]) -> CliSearchHit {
    let external = sources
        .iter()
        .any(|source| std::path::Path::new(&hit.path).starts_with(&source.path));
    CliSearchHit {
        path: if external {
            hit.path.clone()
        } else {
            super::relative_path(root, &hit.path)
        },
        title: hit.title.clone(),
        snippet: hit.snippet.clone(),
        score: 0.0,
        anchor: None,
        metadata_matches: Vec::new(),
        source_type: external.then_some("external"),
        read_only: external,
    }
}

// These crate-local assets are recorded from the real Go stdio process by
// TestMCPSurfaceProcessPortFixture. Keep RawMessage schema key order intact;
// serializing through Value would change the observable tools/list bytes.
fn send_tools_list<W: Write + Send + 'static>(
    output: &Arc<Mutex<W>>,
    mode: ResponseMode,
    id: Value,
    allow_write: bool,
) -> io::Result<()> {
    #[derive(Serialize)]
    struct Response {
        jsonrpc: &'static str,
        id: Value,
        result: Box<RawValue>,
    }
    let catalog = if allow_write {
        include_str!("mcp/assets/tools-read-write.json")
    } else {
        include_str!("mcp/assets/tools-read-only.json")
    };
    let response = Response {
        jsonrpc: "2.0",
        id,
        result: RawValue::from_string(catalog.trim_end().to_owned()).map_err(io::Error::other)?,
    };
    let data = super::go_escape_json(serde_json::to_string(&response).map_err(io::Error::other)?);
    write_data(output, mode, data.as_bytes())
}

fn send_result<W>(
    output: &Arc<Mutex<W>>,
    mode: ResponseMode,
    id: Value,
    result: Value,
) -> io::Result<()>
where
    W: Write + Send + 'static,
{
    write_response(
        output,
        mode,
        RpcResponse {
            jsonrpc: "2.0",
            id,
            result: Some(result),
            error: None,
        },
    )
}

fn send_tool_result<W>(
    output: &Arc<Mutex<W>>,
    mode: ResponseMode,
    id: Value,
    value: Value,
    is_error: bool,
) -> io::Result<()>
where
    W: Write + Send + 'static,
{
    let text = value.as_str().map_or_else(
        || super::go_escape_json(serde_json::to_string(&value).unwrap_or_default()),
        str::to_owned,
    );
    send_result(
        output,
        mode,
        id,
        // Key order is written in Go's sorted map order so the bytes do not
        // depend on whether serde_json's `preserve_order` is unified on.
        json!({
            "content": [{"text": text, "type": "text"}],
            "isError": is_error,
        }),
    )
}

fn send_error<W>(
    output: &Arc<Mutex<W>>,
    mode: ResponseMode,
    id: Value,
    code: i64,
    message: String,
) -> io::Result<()>
where
    W: Write + Send + 'static,
{
    write_response(
        output,
        mode,
        RpcResponse {
            jsonrpc: "2.0",
            id,
            result: None,
            error: Some(RpcError { code, message }),
        },
    )
}

fn write_response<W>(
    output: &Arc<Mutex<W>>,
    mode: ResponseMode,
    response: RpcResponse,
) -> io::Result<()>
where
    W: Write + Send + 'static,
{
    let data = super::go_escape_json(serde_json::to_string(&response).map_err(io::Error::other)?)
        .into_bytes();
    write_data(output, mode, &data)
}

fn write_data<W: Write + Send + 'static>(
    output: &Arc<Mutex<W>>,
    mode: ResponseMode,
    data: &[u8],
) -> io::Result<()> {
    if data.len() > MAX_MESSAGE_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("MCP response exceeds {MAX_MESSAGE_BYTES} bytes"),
        ));
    }
    let mut writer = output
        .lock()
        .map_err(|_| io::Error::other("MCP output lock poisoned"))?;
    match mode {
        ResponseMode::Line => {
            writer.write_all(data)?;
            writer.write_all(b"\n")?;
        }
        ResponseMode::Framed => {
            write!(writer, "Content-Length: {}\r\n\r\n", data.len())?;
            writer.write_all(data)?;
        }
    }
    writer.flush()
}

fn read_request<R: BufRead>(
    reader: &mut R,
) -> Result<Option<(Request, ResponseMode)>, ReadFailure> {
    let Some((first, _terminated)) = read_non_empty_line(reader).map_err(ReadFailure::Fatal)?
    else {
        return Ok(None);
    };
    let first = String::from_utf8_lossy(&first).trim().to_owned();
    if first.starts_with('{') || !first.contains(':') {
        return parse_request(first.as_bytes(), ResponseMode::Line).map(Some);
    }

    let mut content_length = None;
    parse_header(&first, &mut content_length)?;
    loop {
        let (line, terminated) = read_line_limited(reader).map_err(ReadFailure::Fatal)?;
        if !terminated {
            return Err(ReadFailure::Fatal(io::Error::new(
                io::ErrorKind::UnexpectedEof,
                "truncated MCP headers",
            )));
        }
        let line = String::from_utf8_lossy(&line)
            .trim_end_matches(['\r', '\n'])
            .to_owned();
        if line.is_empty() {
            break;
        }
        parse_header(&line, &mut content_length)?;
    }
    let length = content_length.ok_or_else(|| {
        ReadFailure::Fatal(io::Error::new(
            io::ErrorKind::InvalidData,
            "missing Content-Length header",
        ))
    })?;
    if length == 0 || length > MAX_MESSAGE_BYTES {
        return Err(ReadFailure::Fatal(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("invalid Content-Length: {length}"),
        )));
    }
    let mut body = vec![0_u8; length];
    reader.read_exact(&mut body).map_err(|error| {
        ReadFailure::Fatal(io::Error::new(
            io::ErrorKind::UnexpectedEof,
            format!("read body: {error}"),
        ))
    })?;
    parse_request(&body, ResponseMode::Framed).map(Some)
}

fn parse_header(line: &str, content_length: &mut Option<usize>) -> Result<(), ReadFailure> {
    if let Some(value) = line.strip_prefix("Content-Length:") {
        let parsed = value.trim().parse::<usize>().map_err(|_| {
            ReadFailure::Fatal(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("invalid Content-Length: {:?}", value.trim()),
            ))
        })?;
        *content_length = Some(parsed);
    }
    Ok(())
}

fn parse_request(data: &[u8], mode: ResponseMode) -> Result<(Request, ResponseMode), ReadFailure> {
    // Go keeps params/arguments as RawMessage. Do not coerce an argument's
    // numeric token through f64 before its handler can validate a Go int.
    let parse_error = |error: serde_json::Error| ReadFailure::Parse {
        mode,
        message: if error.is_eof() {
            "unexpected end of JSON input".to_owned()
        } else {
            error.to_string()
        },
    };
    let compatible = go_json_text(data).map_err(|message| ReadFailure::Parse { mode, message })?;
    let data = compatible.as_slice();
    let raw: Box<RawValue> = serde_json::from_slice(data).map_err(parse_error)?;
    // CoreKit v0.18 mcpserver: well-formed JSON that is not a JSON-RPC 2.0
    // request object is an Invalid Request answered with a null id.
    let invalid = || ReadFailure::InvalidRequest { mode };
    if !raw.get().starts_with('{') {
        return Err(invalid());
    }
    let object: BTreeMap<String, Box<RawValue>> =
        serde_json::from_str(raw.get()).map_err(parse_error)?;
    let string_field = |key: &str| {
        object
            .get(key)
            .and_then(|value| serde_json::from_str::<String>(value.get()).ok())
    };
    if string_field("jsonrpc").as_deref() != Some("2.0") {
        return Err(invalid());
    }
    let method = match string_field("method") {
        Some(method) if !method.is_empty() => method,
        _ => return Err(invalid()),
    };
    let id: Value = object
        .get("id")
        .map(|raw| serde_json::from_str(raw.get()))
        .transpose()
        .map_err(parse_error)?
        .unwrap_or(Value::Null);
    if !matches!(id, Value::Null | Value::String(_) | Value::Number(_))
        || object
            .get("params")
            .is_some_and(|params| !matches!(params.get().as_bytes().first(), Some(b'{' | b'[')))
    {
        return Err(invalid());
    }
    let raw_arguments = raw_arguments_from_frame(data);
    let raw_params = object.get("params").map(|raw| raw.get().to_owned());
    Ok((
        Request {
            id,
            has_id: object.contains_key("id"),
            method,
            raw_arguments,
            raw_params,
        },
        mode,
    ))
}

fn raw_arguments_from_frame(data: &[u8]) -> Option<String> {
    let frame: BTreeMap<String, Box<RawValue>> = serde_json::from_slice(data).ok()?;
    let params = frame.get("params")?;
    let params: OrderedFields<Box<RawValue>> = serde_json::from_str(params.get()).ok()?;
    params
        .0
        .into_iter()
        .rev()
        .find(|(key, _)| symdesk_vault::dataset::go_equal_fold(key, "arguments"))
        .map(|(_, raw)| raw.get().to_owned())
}

fn read_non_empty_line<R: BufRead>(reader: &mut R) -> io::Result<Option<(Vec<u8>, bool)>> {
    loop {
        let (line, terminated) = read_line_limited(reader)?;
        if line.iter().any(|byte| !byte.is_ascii_whitespace()) {
            return Ok(Some((line, terminated)));
        }
        if !terminated {
            return Ok(None);
        }
    }
}

fn read_line_limited<R: BufRead>(reader: &mut R) -> io::Result<(Vec<u8>, bool)> {
    let mut line = Vec::new();
    loop {
        let chunk = reader.fill_buf()?;
        if chunk.is_empty() {
            return Ok((line, false));
        }
        let newline = chunk.iter().position(|byte| *byte == b'\n');
        let take = newline.map_or(chunk.len(), |position| position + 1);
        if line.len() + take > MAX_MESSAGE_BYTES {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("line exceeds {MAX_MESSAGE_BYTES} bytes"),
            ));
        }
        line.extend_from_slice(&chunk[..take]);
        reader.consume(take);
        if newline.is_some() {
            return Ok((line, true));
        }
    }
}

#[cfg(test)]
mod tests {
    use std::io::Cursor;

    use super::*;

    fn run(input: &[u8]) -> String {
        let (output, result) = serve_io(
            Cursor::new(input.to_vec()),
            Vec::new(),
            Some("/tmp/rust006-vault".to_owned()),
            false,
        );
        result.expect("server should finish cleanly");
        String::from_utf8(output).expect("responses are UTF-8")
    }

    #[test]
    fn line_initialize_and_tools_list_match_representative_contract() {
        let output = run(br#"{"jsonrpc":"2.0","id":1,"method":"initialize"}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
"#);
        let responses: Vec<Value> = output
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect();
        assert_eq!(responses.len(), 2);
        assert_eq!(responses[0]["result"]["protocolVersion"], PROTOCOL_VERSION);
        assert_eq!(
            responses[0]["result"]["serverInfo"],
            json!({"name":"symdesk","version":super::super::VERSION})
        );
        let tools = responses[1]["result"]["tools"].as_array().unwrap();
        assert_eq!(tools.len(), 29);
        assert_eq!(
            tools[..3]
                .iter()
                .map(|tool| tool["name"].as_str().unwrap())
                .collect::<Vec<_>>(),
            ["desk_status", "desk_ls", "desk_search"]
        );
        assert!(
            tools
                .iter()
                .all(|tool| tool["annotations"]["readOnlyHint"] == true)
        );
    }

    #[test]
    fn tool_errors_use_mcp_content_shape_and_unknown_codes() {
        let output = run(
            br#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"desk_search","arguments":{}}}
{"jsonrpc":"2.0","id":2,"method":"nope"}
{"jsonrpc":"2.0","method":"nope"}
"#,
        );
        let responses: Vec<Value> = output
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect();
        assert_eq!(responses.len(), 2);
        let tool_error = responses
            .iter()
            .find(|response| response["id"] == 1)
            .unwrap();
        let method_error = responses
            .iter()
            .find(|response| response["id"] == 2)
            .unwrap();
        assert_eq!(tool_error["result"]["isError"], true);
        assert_eq!(
            tool_error["result"]["content"][0]["text"],
            "query is required"
        );
        assert_eq!(method_error["error"]["code"], METHOD_NOT_FOUND);
    }

    #[test]
    fn framed_requests_return_framed_responses() {
        let body = br#"{"jsonrpc":"2.0","id":7,"method":"initialize"}"#;
        let input = format!(
            "Content-Length: {}\r\n\r\n{}",
            body.len(),
            String::from_utf8_lossy(body)
        );
        let output = run(input.as_bytes());
        assert!(output.starts_with("Content-Length: "));
        let payload = output.split_once("\r\n\r\n").unwrap().1;
        let response: Value = serde_json::from_str(payload).unwrap();
        assert_eq!(response["id"], 7);
    }

    #[test]
    fn malformed_line_is_parse_error_and_notification_is_silent() {
        let output = run(b"not-json\n{\"jsonrpc\":\"2.0\",\"method\":\"nope\"}\n");
        let responses: Vec<Value> = output
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect();
        assert_eq!(responses.len(), 1);
        assert_eq!(responses[0]["error"]["code"], PARSE_ERROR);
    }

    #[test]
    fn oversized_line_and_truncated_frame_are_rejected_without_output() {
        let oversized = vec![b'x'; MAX_MESSAGE_BYTES + 1];
        let (_output, result) = serve_io(Cursor::new(oversized), Vec::new(), None::<String>, false);
        assert!(result.is_err());

        let (_output, result) = serve_io(
            Cursor::new(b"Content-Length: 10\r\n\r\n{}".to_vec()),
            Vec::new(),
            None::<String>,
            false,
        );
        assert!(result.is_err());
    }

    #[test]
    fn oversized_response_is_rejected_before_writing() {
        let response = RpcResponse {
            jsonrpc: "2.0",
            id: Value::from(1),
            result: Some(Value::String("x".repeat(MAX_MESSAGE_BYTES))),
            error: None,
        };
        let output = Arc::new(Mutex::new(Vec::new()));
        let result = write_response(&output, ResponseMode::Line, response);
        assert!(result.is_err());
        assert!(output.lock().unwrap().is_empty());
    }

    #[test]
    fn handler_write_error_is_returned_without_a_second_response() {
        let output = Arc::new(Mutex::new(Vec::new()));
        let config = Arc::new(ServerConfig {
            version: "test".to_owned(),
            vault: None,
            allow_write: false,
            pdf_context: symdesk_render::pdfops::Context::default(),
            write_error: Mutex::new(None),
            sidecar_anchor: Mutex::new(None),
        });
        let mut calls = vec![(
            ResponseMode::Framed,
            Some(Value::from(7)),
            thread::spawn(|| -> io::Result<()> { Err(io::Error::other("response exceeds limit")) }),
        )];

        let error = join_calls(&mut calls, &output, &config).unwrap_err();
        assert_eq!(error.to_string(), "response exceeds limit");
        assert!(output.lock().unwrap().is_empty());
    }
}
