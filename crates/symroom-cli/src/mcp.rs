#![deny(unsafe_code)]

use std::{
    io::{self, BufRead, Write},
    path::Path,
};

use serde_json::{Value, json};

const INSTRUCTIONS: &str = "Use room_* tools to inspect and record the signed room work record. There is no approval-granting tool in this server.";

fn tools() -> Value {
    json!([
        {"name":"room_status","description":"Return room metadata and journal statistics","inputSchema":{"type":"object","properties":{}}},
        {"name":"room_journal_tail","description":"Return the merged signed journal tail","inputSchema":{"type":"object","properties":{"limit":{"type":"integer"}}}},
        {"name":"room_note_post","description":"Post a signed note to the room journal","inputSchema":{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}},
        {"name":"room_artifact_list","description":"List linked room artifacts","inputSchema":{"type":"object","properties":{}}},
        {"name":"room_artifact_link","description":"Link and hash an artifact, recording a signed event","inputSchema":{"type":"object","required":["path"],"properties":{"path":{"type":"string"},"title":{"type":"string"}}}},
        {"name":"room_run_request","description":"Request a run with a signed journal event","inputSchema":{"type":"object","required":["title"],"properties":{"title":{"type":"string"},"plan_file":{"type":"string"},"adapter":{"type":"string"}}}},
        {"name":"room_run_wait","description":"Wait for a run approval decision","inputSchema":{"type":"object","required":["run_id"],"properties":{"run_id":{"type":"string"},"timeout_seconds":{"type":"number"}}}},
        {"name":"room_checkpoint_request","description":"Request a signed human checkpoint","inputSchema":{"type":"object","required":["run_id","question"],"properties":{"run_id":{"type":"string"},"question":{"type":"string"}}}}
    ])
}

pub fn serve_io<R: BufRead, W: Write>(
    mut input: R,
    mut output: W,
    room_dir: &Path,
) -> io::Result<()> {
    loop {
        let Some(body) = read_frame(&mut input)? else {
            return Ok(());
        };
        let request: Value = match serde_json::from_slice(&body) {
            Ok(value) => value,
            Err(error) => {
                write_response(
                    &mut output,
                    json!({"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":format!("Parse error: {error}")}}),
                )?;
                continue;
            }
        };
        if request.get("id").is_none() {
            continue;
        }
        let id = request["id"].clone();
        let response = match request
            .get("method")
            .and_then(Value::as_str)
            .unwrap_or_default()
        {
            "initialize" => {
                json!({"jsonrpc":"2.0","id":id,"result":{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"symroom","version":"0.1.0"},"instructions":INSTRUCTIONS}})
            }
            "ping" => json!({"jsonrpc":"2.0","id":id,"result":{}}),
            "tools/list" => json!({"jsonrpc":"2.0","id":id,"result":{"tools":tools()}}),
            "tools/call" => call(&request, id, room_dir),
            method => {
                json!({"jsonrpc":"2.0","id":id,"error":{"code":-32601,"message":format!("Method not found: {method}")}})
            }
        };
        write_response(&mut output, response)?;
    }
}

fn call(request: &Value, id: Value, room_dir: &Path) -> Value {
    let params = &request["params"];
    let Some(params) = params.as_object() else {
        return json!({"jsonrpc":"2.0","id":id,"error":{"code":-32602,"message":"Invalid params: expected an object"}});
    };
    let name = params
        .get("name")
        .and_then(Value::as_str)
        .unwrap_or_default();
    if !tools()
        .as_array()
        .is_some_and(|items| items.iter().any(|item| item["name"] == name))
    {
        return json!({"jsonrpc":"2.0","id":id,"error":{"code":-32601,"message":format!("Unknown tool: {name}")}});
    }
    if name == "room_journal_tail" {
        let args = params.get("arguments").unwrap_or(&Value::Null);
        let limit = args
            .get("limit")
            .and_then(Value::as_i64)
            .filter(|n| *n > 0)
            .unwrap_or(20) as usize;
        return match symroom_core::journal::merge_all(room_dir) {
            Ok(mut events) => {
                if events.len() > limit {
                    events.drain(..events.len() - limit);
                }
                let text = if events.is_empty() {
                    "null".to_owned()
                } else {
                    serde_json::to_string(&events).expect("journal events serialize")
                };
                json!({"jsonrpc":"2.0","id":id,"result":{"content":[{"type":"text","text":text}],"isError":false}})
            }
            Err(error) => tool_error(id, error.to_string()),
        };
    }
    tool_error(
        id,
        format!("{name} is not implemented in the current SymRoom MCP slice"),
    )
}

fn tool_error(id: Value, message: String) -> Value {
    json!({"jsonrpc":"2.0","id":id,"result":{"content":[{"type":"text","text":message}],"isError":true}})
}

fn read_frame<R: BufRead>(input: &mut R) -> io::Result<Option<Vec<u8>>> {
    let mut first = String::new();
    loop {
        first.clear();
        if input.read_line(&mut first)? == 0 {
            return Ok(None);
        }
        if !first.trim().is_empty() {
            break;
        }
    }
    if let Some(length) = first.trim().strip_prefix("Content-Length:") {
        let length = length.trim().parse::<usize>().map_err(invalid_data)?;
        if length == 0 || length > 1 << 20 {
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                "invalid Content-Length",
            ));
        }
        loop {
            let mut header = String::new();
            input.read_line(&mut header)?;
            if header == "\r\n" || header == "\n" {
                break;
            }
            if header.is_empty() {
                return Err(io::Error::new(
                    io::ErrorKind::UnexpectedEof,
                    "incomplete headers",
                ));
            }
        }
        let mut body = vec![0; length];
        input.read_exact(&mut body)?;
        return Ok(Some(body));
    }
    Ok(Some(first.into_bytes()))
}

fn invalid_data(error: std::num::ParseIntError) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, error)
}

fn write_response<W: Write>(output: &mut W, value: Value) -> io::Result<()> {
    let body = serde_json::to_vec(&value).map_err(io::Error::other)?;
    write!(output, "Content-Length: {}\r\n\r\n", body.len())?;
    output.write_all(&body)
}
