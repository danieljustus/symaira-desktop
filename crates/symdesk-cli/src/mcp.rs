#![allow(clippy::module_name_repetitions)]

use std::{
    io::{self, BufRead, Write},
    path::PathBuf,
    sync::{Arc, Mutex},
    thread,
};

use serde::Serialize;
use serde_json::{Value, json};
use symdesk_index::{ListedDocument, SearchHit, Sidecar, path_for_vault};

const PROTOCOL_VERSION: &str = "2024-11-05";
const MAX_MESSAGE_BYTES: usize = 1 << 20;
const PARSE_ERROR: i64 = -32700;
const METHOD_NOT_FOUND: i64 = -32601;
const INVALID_PARAMS: i64 = -32602;
const INTERNAL_ERROR: i64 = -32603;

#[derive(Clone)]
struct ServerConfig {
    version: String,
    vault: Option<String>,
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
struct McpSearchEntry {
    path: String,
    title: String,
    snippet: String,
    score: i64,
}

#[derive(Serialize)]
struct McpSearchResponse {
    results: Vec<McpSearchEntry>,
}

#[derive(Debug)]
enum ReadFailure {
    Parse { mode: ResponseMode, message: String },
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
                    calls.push(thread::spawn(move || {
                        dispatch(&request, mode, &config, &output)
                    }));
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
    calls: &mut Vec<thread::JoinHandle<io::Result<()>>>,
    output: &Arc<Mutex<W>>,
    config: &Arc<ServerConfig>,
) where
    W: Write + Send + 'static,
{
    for call in calls.drain(..) {
        match call.join() {
            Ok(Ok(())) => {}
            Ok(Err(error)) => {
                // The first writer error is surfaced by a best-effort internal
                // response only when possible; the transport caller still gets
                // a clean shutdown after all handlers have been reaped.
                let _ = send_error(
                    output,
                    ResponseMode::Line,
                    Value::Null,
                    INTERNAL_ERROR,
                    error.to_string(),
                );
            }
            Err(_) => {
                let _ = send_error(
                    output,
                    ResponseMode::Line,
                    Value::Null,
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
    if !matches!(name, "desk_status" | "desk_ls" | "desk_search") {
        return send_error(
            output,
            mode,
            request.id.clone(),
            METHOD_NOT_FOUND,
            format!("Unknown tool: {name}"),
        );
    }

    match call_tool(name, arguments, config) {
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

fn call_tool(name: &str, arguments: Value, config: &ServerConfig) -> Result<Value, String> {
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
            let mut files = sidecar.list_files(dir).map_err(|error| error.to_string())?;
            if files.is_empty() {
                sidecar
                    .refresh_index(&vault)
                    .map_err(|error| error.to_string())?;
                files = sidecar.list_files(dir).map_err(|error| error.to_string())?;
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
            let args = object_arguments(arguments)?;
            let query = args
                .get("query")
                .and_then(Value::as_str)
                .unwrap_or_default();
            if query.is_empty() {
                return Err("query is required".to_owned());
            }
            let (vault, sidecar) = open_sidecar(config)?;
            let hits = sidecar.search(query).map_err(|error| error.to_string())?;
            Ok(Value::String(
                serde_json::to_string(&McpSearchResponse {
                    results: hits.iter().map(|hit| search_entry(&vault, hit)).collect(),
                })
                .map_err(|error| error.to_string())?,
            ))
        }
        _ => Err(format!("Unknown tool: {name}")),
    }
}

fn object_arguments(arguments: Value) -> Result<serde_json::Map<String, Value>, String> {
    arguments
        .as_object()
        .cloned()
        .ok_or_else(|| "invalid arguments: expected an object".to_owned())
}

fn open_sidecar(config: &ServerConfig) -> Result<(PathBuf, Sidecar), String> {
    let vault = super::resolve_vault(config.vault.as_deref())?;
    let sidecar_path = path_for_vault(&vault).map_err(|error| error.to_string())?;
    let sidecar = Sidecar::open(&sidecar_path).map_err(|error| error.to_string())?;
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

fn search_entry(root: &std::path::Path, hit: &SearchHit) -> McpSearchEntry {
    McpSearchEntry {
        path: super::relative_path(root, &hit.path),
        title: hit.title.clone(),
        snippet: hit.snippet.clone(),
        score: 0,
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
    let Some(object) = value.as_object() else {
        return Err(ReadFailure::Parse {
            mode,
            message: "json: cannot unmarshal array into Go value of type mcpserver.requestAlias"
                .to_owned(),
        });
    };
    let method = match object.get("method") {
        None => String::new(),
        Some(Value::String(method)) => method.clone(),
        Some(_) => {
            return Err(ReadFailure::Parse {
                mode,
                message: "json: cannot unmarshal non-string into Go struct field requestAlias.method of type string".to_owned(),
            });
        }
    };
    let id = object.get("id").cloned().unwrap_or(Value::Null);
    Ok((
        Request {
            id,
            has_id: object.contains_key("id"),
            method,
            params: object.get("params").cloned().unwrap_or(Value::Null),
        },
        mode,
    ))
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
        assert_eq!(tools.len(), 3);
        assert_eq!(
            tools
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
        let (_output, result) = serve_io(Cursor::new(oversized), Vec::new(), None::<String>);
        assert!(result.is_err());

        let (_output, result) = serve_io(
            Cursor::new(b"Content-Length: 10\r\n\r\n{}".to_vec()),
            Vec::new(),
            None::<String>,
        );
        assert!(result.is_err());
    }
}
