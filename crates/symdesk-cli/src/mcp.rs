#![allow(clippy::module_name_repetitions)]

use std::{
    collections::BTreeMap,
    io::{self, BufRead, Write},
    path::PathBuf,
    sync::{Arc, Mutex},
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
    params: Value,
    raw_arguments: Option<String>,
}

#[derive(Default)]
struct OrderedFields(Vec<(String, Value)>);

impl<'de> Deserialize<'de> for OrderedFields {
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: serde::Deserializer<'de>,
    {
        struct OrderedFieldsVisitor;

        impl<'de> Visitor<'de> for OrderedFieldsVisitor {
            type Value = OrderedFields;

            fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                formatter.write_str("an object of ordered string arguments")
            }

            fn visit_map<M>(self, mut map: M) -> Result<Self::Value, M::Error>
            where
                M: MapAccess<'de>,
            {
                let mut fields = Vec::new();
                while let Some(key) = map.next_key::<String>()? {
                    fields.push((key, map.next_value::<Value>()?));
                }
                Ok(OrderedFields(fields))
            }
        }

        deserializer.deserialize_map(OrderedFieldsVisitor)
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

/// Runs the representative read-only MCP server on stdin/stdout.
///
/// Only protocol frames are written to stdout. Diagnostics and transport
/// failures are returned to the caller so the CLI can write them to stderr.
pub fn serve(vault: Option<String>) -> io::Result<()> {
    let vault = vault.or_else(|| std::env::var("SYMDESK_VAULT").ok());
    let reader = io::BufReader::new(io::stdin());
    let writer = io::stdout();
    let (_writer, result) = serve_io(reader, writer, vault);
    result
}

fn serve_io<R, W>(reader: R, writer: W, vault: Option<String>) -> (W, io::Result<()>)
where
    R: BufRead,
    W: Write + Send + 'static,
{
    let output = Arc::new(Mutex::new(writer));
    let config = Arc::new(ServerConfig {
        version: super::VERSION.to_owned(),
        vault,
        sidecar_anchor: Mutex::new(None),
    });
    let mut calls = Vec::new();
    let mut reader = reader;
    let mut terminal_error = None;

    loop {
        match read_request(&mut reader) {
            Ok(None) => break,
            Ok(Some((request, mode))) => {
                if request.method == "tools/call" {
                    let output = Arc::clone(&output);
                    let config = Arc::clone(&config);
                    calls.push((
                        mode,
                        request.id.clone(),
                        thread::spawn(move || dispatch(&request, mode, &config, &output)),
                    ));
                } else if let Err(error) = dispatch(&request, mode, &config, &output) {
                    terminal_error = Some(error);
                    break;
                }
            }
            Err(ReadFailure::Parse { mode, message }) => {
                join_calls(&mut calls, &output, &config);
                if let Err(error) = send_error(
                    &output,
                    mode,
                    Value::Null,
                    PARSE_ERROR,
                    format!("Parse error: {message}"),
                ) {
                    terminal_error = Some(error);
                    break;
                }
            }
            Err(ReadFailure::InvalidRequest { mode }) => {
                join_calls(&mut calls, &output, &config);
                if let Err(error) = send_error(
                    &output,
                    mode,
                    Value::Null,
                    INVALID_REQUEST,
                    "Invalid Request".to_owned(),
                ) {
                    terminal_error = Some(error);
                    break;
                }
            }
            Err(ReadFailure::Fatal(error)) => {
                terminal_error = Some(error);
                break;
            }
        }
    }

    join_calls(&mut calls, &output, &config);
    let result = terminal_error.map_or(Ok(()), Err);
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

fn join_calls<W>(
    calls: &mut Vec<(ResponseMode, Value, thread::JoinHandle<io::Result<()>>)>,
    output: &Arc<Mutex<W>>,
    config: &Arc<ServerConfig>,
) where
    W: Write + Send + 'static,
{
    for (mode, id, call) in calls.drain(..) {
        match call.join() {
            Ok(Ok(())) => {}
            Ok(Err(error)) => {
                // The first writer error is surfaced by a best-effort internal
                // response only when possible; the transport caller still gets
                // a clean shutdown after all handlers have been reaped.
                let _ = send_error(output, mode, id, INTERNAL_ERROR, error.to_string());
            }
            Err(_) => {
                let _ = send_error(
                    output,
                    mode,
                    id,
                    INTERNAL_ERROR,
                    format!("Internal error: handler panicked ({})", config.version),
                );
            }
        }
    }
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
    // A notification has no response, including an unknown method.
    if !request.has_id {
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
        "tools/list" => send_result(
            output,
            mode,
            request.id.clone(),
            json!({"tools": tool_definitions()}),
        ),
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
    let Some(params) = request.params.as_object() else {
        if request.params.is_null() {
            return send_error(
                output,
                mode,
                request.id.clone(),
                METHOD_NOT_FOUND,
                "Unknown tool: ".to_owned(),
            );
        }
        return send_error(
            output,
            mode,
            request.id.clone(),
            INVALID_PARAMS,
            "Invalid params: expected an object".to_owned(),
        );
    };
    let name = params
        .get("name")
        .and_then(Value::as_str)
        .unwrap_or_default();
    let arguments = params.get("arguments").cloned().unwrap_or(Value::Null);
    if !matches!(name, "desk_status" | "desk_ls" | "desk_search" | "desk_ask") {
        return send_error(
            output,
            mode,
            request.id.clone(),
            METHOD_NOT_FOUND,
            format!("Unknown tool: {name}"),
        );
    }

    match call_tool(name, arguments, request.raw_arguments.as_deref(), config) {
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
    arguments: Value,
    raw_arguments: Option<&str>,
    config: &ServerConfig,
) -> Result<Value, String> {
    match name {
        "desk_status" => Ok(json!({
            "version": config.version,
            "vault": config.vault.clone().unwrap_or_default(),
            "capabilities": "read_only",
        })),
        "desk_ls" => {
            let args = object_arguments(arguments)?;
            let dir = args.get("dir").and_then(Value::as_str).unwrap_or_default();
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
        "desk_search" => {
            let (query, _) = go_string_arguments(raw_arguments, &arguments, false)?;
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
            let (query, notebook) = go_string_arguments(raw_arguments, &arguments, true)?;
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
        _ => Err(format!("Unknown tool: {name}")),
    }
}

fn go_string_arguments(
    raw_arguments: Option<&str>,
    parsed_arguments: &Value,
    include_notebook: bool,
) -> Result<(String, String), String> {
    let raw_arguments = raw_arguments.ok_or_else(|| "unexpected end of JSON input".to_owned())?;
    if parsed_arguments.is_null() {
        return Ok((String::new(), String::new()));
    }
    if !parsed_arguments.is_object() {
        let go_type = match parsed_arguments {
            Value::Bool(_) => "bool",
            Value::Number(_) => "number",
            Value::String(_) => "string",
            Value::Array(_) => "array",
            Value::Object(_) | Value::Null => unreachable!("handled above"),
        };
        let target = if include_notebook {
            r#"struct { Query string "json:\"query\""; Notebook string "json:\"notebook\"" }"#
        } else {
            r#"struct { Query string "json:\"query\"" }"#
        };
        return Err(format!(
            "json: cannot unmarshal {go_type} into Go value of type {target}"
        ));
    }
    let fields: OrderedFields = serde_json::from_str(raw_arguments).map_err(|error| {
        if error.is_eof() {
            "unexpected end of JSON input".to_owned()
        } else {
            error.to_string()
        }
    })?;
    let mut query = String::new();
    let mut notebook = String::new();
    for (key, value) in fields.0 {
        let field = if symdesk_vault::dataset::go_equal_fold(&key, "query") {
            Some((&mut query, "query"))
        } else if include_notebook && symdesk_vault::dataset::go_equal_fold(&key, "notebook") {
            Some((&mut notebook, "notebook"))
        } else {
            None
        };
        let Some((target, field_name)) = field else {
            continue;
        };
        match value {
            Value::Null => {}
            Value::String(value) => *target = value,
            Value::Bool(_) => return Err(go_string_field_type_error("bool", field_name)),
            Value::Number(_) => return Err(go_string_field_type_error("number", field_name)),
            Value::Array(_) => return Err(go_string_field_type_error("array", field_name)),
            Value::Object(_) => return Err(go_string_field_type_error("object", field_name)),
        }
    }
    Ok((query, notebook))
}

fn go_string_field_type_error(value_type: &str, field: &str) -> String {
    format!("json: cannot unmarshal {value_type} into Go struct field .{field} of type string")
}

fn object_arguments(arguments: Value) -> Result<serde_json::Map<String, Value>, String> {
    arguments
        .as_object()
        .cloned()
        .ok_or_else(|| "invalid arguments: expected an object".to_owned())
}

fn open_sidecar(config: &ServerConfig) -> Result<(PathBuf, Sidecar), String> {
    let vault = super::resolve_vault(config.vault.as_deref())?;
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
        path: super::relative_path(root, &file.path),
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

fn tool_definitions() -> Vec<Value> {
    vec![
        json!({
            "name": "desk_status",
            "description": "Returns the current version and vault path configuration for symdesk.",
            "inputSchema": {"type": "object", "properties": {}},
            "annotations": {"readOnlyHint": true},
        }),
        json!({
            "name": "desk_ls",
            "description": "Lists files in the vault.",
            "inputSchema": {"type": "object", "properties": {"dir": {"type": "string"}}},
            "annotations": {"readOnlyHint": true},
        }),
        json!({
            "name": "desk_search",
            "description": "Searches notes with full-text terms plus path:, tag:, type:, status:, filename:, filetype:, created:, modified:, quoted phrases, -negation and /regex/. Filetype accepts comma-separated extensions (for example pdf,epub); dates accept YYYY-MM-DD, YYYY-MM-DD..YYYY-MM-DD and last day/week/month/year. Invalid syntax falls back to plain full-text and returns a hint.",
            "inputSchema": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]},
            "annotations": {"readOnlyHint": true},
        }),
        json!({
            "name": "desk_ask",
            "description": "Asks the AI a question about the vault. Uses a local Ollama instance when configured; otherwise returns the top search results with a note that AI is not configured. The answer is returned as one aggregated text (no streaming). Pass notebook to restrict retrieval and citations to that notebook's sources instead of the whole vault.",
            "inputSchema": {"type": "object", "properties": {"query": {"type": "string"}, "notebook": {"type": "string", "description": "optional: notebook id or path to restrict retrieval and citations to"}}, "required": ["query"]},
            "annotations": {"readOnlyHint": true},
        }),
    ]
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
        || serde_json::to_string(&value).unwrap_or_default(),
        str::to_owned,
    );
    send_result(
        output,
        mode,
        id,
        json!({
            "content": [{"type": "text", "text": text}],
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
    let data = serde_json::to_vec(&response).map_err(io::Error::other)?;
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
            writer.write_all(&data)?;
            writer.write_all(b"\n")?;
        }
        ResponseMode::Framed => {
            write!(writer, "Content-Length: {}\r\n\r\n", data.len())?;
            writer.write_all(&data)?;
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
    let value: Value = serde_json::from_slice(data).map_err(|error| ReadFailure::Parse {
        mode,
        message: if error.is_eof() {
            "unexpected end of JSON input".to_owned()
        } else {
            error.to_string()
        },
    })?;
    // CoreKit v0.18 mcpserver: well-formed JSON that is not a JSON-RPC 2.0
    // request object is an Invalid Request answered with a null id.
    let invalid = || ReadFailure::InvalidRequest { mode };
    let object = value.as_object().ok_or_else(invalid)?;
    if object.get("jsonrpc").and_then(Value::as_str) != Some("2.0") {
        return Err(invalid());
    }
    let method = match object.get("method") {
        Some(Value::String(method)) if !method.is_empty() => method.clone(),
        _ => return Err(invalid()),
    };
    if object
        .get("id")
        .is_some_and(|id| !matches!(id, Value::Null | Value::String(_) | Value::Number(_)))
        || object
            .get("params")
            .is_some_and(|params| !matches!(params, Value::Object(_) | Value::Array(_)))
    {
        return Err(invalid());
    }
    let id = object.get("id").cloned().unwrap_or(Value::Null);
    let raw_arguments = raw_arguments_from_frame(data);
    Ok((
        Request {
            id,
            has_id: object.contains_key("id"),
            method,
            params: object.get("params").cloned().unwrap_or(Value::Null),
            raw_arguments,
        },
        mode,
    ))
}

fn raw_arguments_from_frame(data: &[u8]) -> Option<String> {
    let frame: BTreeMap<String, Box<RawValue>> = serde_json::from_slice(data).ok()?;
    let params = frame.get("params")?;
    let params: BTreeMap<String, Box<RawValue>> = serde_json::from_str(params.get()).ok()?;
    params.get("arguments").map(|raw| raw.get().to_owned())
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
        assert_eq!(tools.len(), 4);
        assert_eq!(
            tools
                .iter()
                .map(|tool| tool["name"].as_str().unwrap())
                .collect::<Vec<_>>(),
            ["desk_status", "desk_ls", "desk_search", "desk_ask"]
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
        let (_output, result) = serve_io(Cursor::new(oversized), Vec::new(), None::<String>);
        assert!(result.is_err());

        let (_output, result) = serve_io(
            Cursor::new(b"Content-Length: 10\r\n\r\n{}".to_vec()),
            Vec::new(),
            None::<String>,
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
    fn framed_handler_error_preserves_framing_and_request_id() {
        let output = Arc::new(Mutex::new(Vec::new()));
        let config = Arc::new(ServerConfig {
            version: "test".to_owned(),
            vault: None,
            sidecar_anchor: Mutex::new(None),
        });
        let mut calls = vec![(
            ResponseMode::Framed,
            Value::from(7),
            thread::spawn(|| -> io::Result<()> { Err(io::Error::other("response exceeds limit")) }),
        )];

        join_calls(&mut calls, &output, &config);

        let response = String::from_utf8(output.lock().unwrap().clone()).unwrap();
        assert!(response.starts_with("Content-Length: "));
        let payload = response.split_once("\r\n\r\n").unwrap().1;
        let payload: Value = serde_json::from_str(payload).unwrap();
        assert_eq!(payload["id"], 7);
        assert_eq!(payload["error"]["code"], INTERNAL_ERROR);
    }
}
