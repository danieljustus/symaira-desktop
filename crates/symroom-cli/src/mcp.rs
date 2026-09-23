#![deny(unsafe_code)]

use std::{
    ffi::OsString,
    fs,
    io::{self, BufRead, Write},
    path::{Path, PathBuf},
    process::ExitCode,
    sync::atomic::{AtomicU64, Ordering},
    time::Duration,
};

use serde::Serialize;
use serde_json::{Value, json};
use sha2::{Digest, Sha256};

const INSTRUCTIONS: &str = "Use room_* tools to inspect and record the signed room work record. There is no approval-granting tool in this server.";
const MCP_USAGE: &str = "Usage of mcp:\n  -artifact-root string\n    \tArtifact root directory\n  -identity string\n    \tSigning identity name\n  -room string\n    \tRoom directory (default \".\")\n";

pub fn run_cli(args: &[OsString]) -> ExitCode {
    let mut room = PathBuf::from(".");
    let mut artifact_root = PathBuf::new();
    let mut identity_name = None;
    let mut i = 0;
    while i < args.len() {
        let raw = args[i].to_string_lossy();
        if raw == "-h" || raw == "--help" {
            print!("{MCP_USAGE}");
            return ExitCode::SUCCESS;
        }
        if !raw.starts_with('-') {
            break;
        }
        let flag = raw.trim_start_matches('-');
        let (name, inline_value) = flag
            .split_once('=')
            .map_or((flag, None), |(name, value)| (name, Some(value.to_owned())));
        i += 1;
        let value = if let Some(value) = inline_value {
            OsString::from(value)
        } else if let Some(value) = args.get(i) {
            i += 1;
            value.clone()
        } else {
            eprintln!("flag needs an argument: -{name}");
            eprint!("{MCP_USAGE}");
            return ExitCode::from(2);
        };
        match name {
            "room" => room = PathBuf::from(value),
            "artifact-root" => artifact_root = PathBuf::from(value),
            "identity" => identity_name = Some(value.to_string_lossy().into_owned()),
            _ => {
                eprintln!("flag provided but not defined: -{name}");
                eprint!("{MCP_USAGE}");
                return ExitCode::from(2);
            }
        }
    }
    if artifact_root.as_os_str().is_empty() {
        artifact_root.clone_from(&room);
    }
    let stdin = io::stdin();
    let stdout = io::stdout();
    let identity = match identity_name {
        Some(name) => match symroom_core::identity::load(&name) {
            Ok(identity) => Some(identity),
            Err(error) => {
                eprintln!("{error}");
                return ExitCode::FAILURE;
            }
        },
        None => None,
    };
    match serve_io_with_identity(
        stdin.lock(),
        stdout.lock(),
        &room,
        &artifact_root,
        identity.as_ref(),
    ) {
        Ok(()) => ExitCode::SUCCESS,
        Err(error) => {
            eprintln!("{error}");
            ExitCode::FAILURE
        }
    }
}

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

pub fn serve_io_with_artifact_root<R: BufRead, W: Write>(
    input: R,
    output: W,
    room_dir: &Path,
    artifact_root: &Path,
) -> io::Result<()> {
    serve_io_with_identity(input, output, room_dir, artifact_root, None)
}

pub fn serve_io_with_identity<R: BufRead, W: Write>(
    mut input: R,
    mut output: W,
    room_dir: &Path,
    artifact_root: &Path,
    identity: Option<&symroom_core::identity::Identity>,
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
            "tools/call" => call(&request, id, room_dir, artifact_root, identity),
            method => {
                json!({"jsonrpc":"2.0","id":id,"error":{"code":-32601,"message":format!("Method not found: {method}")}})
            }
        };
        write_response(&mut output, response)?;
    }
}

fn call(
    request: &Value,
    id: Value,
    room_dir: &Path,
    artifact_root: &Path,
    identity: Option<&symroom_core::identity::Identity>,
) -> Value {
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
    if name == "room_status" {
        return match room_status(room_dir) {
            Ok(value) => tool_result(id, value),
            Err(error) => tool_error(id, error),
        };
    }
    if name == "room_artifact_list" {
        return match artifact_list(room_dir, artifact_root) {
            Ok(value) => tool_result(id, value),
            Err(error) => tool_error(id, error),
        };
    }
    if name == "room_run_wait" {
        let args = params.get("arguments").unwrap_or(&Value::Null);
        let Some(run_id) = args
            .get("run_id")
            .and_then(Value::as_str)
            .filter(|id| !id.is_empty())
        else {
            return tool_error(id, "run_id is required".to_owned());
        };
        let timeout = args
            .get("timeout_seconds")
            .and_then(Value::as_f64)
            .filter(|seconds| *seconds > 0.0 && seconds.is_finite())
            .and_then(|seconds| Duration::try_from_secs_f64(seconds).ok())
            .unwrap_or(Duration::from_secs(30));
        return match symroom_core::runs::wait(room_dir, run_id, timeout) {
            Ok(run) => tool_result(id, serde_json::to_string(&run).expect("run serializes")),
            Err(symroom_core::runs::RunWaitError::Timeout) => {
                tool_error(id, "wait timed out before approval decision".to_owned())
            }
            Err(symroom_core::runs::RunWaitError::Cancelled) => {
                tool_error(id, "run was denied: run cancelled".to_owned())
            }
            Err(symroom_core::runs::RunWaitError::Denied) => {
                let error = symroom_core::runs::get(room_dir, run_id)
                    .map(|run| run.error.unwrap_or_default())
                    .unwrap_or_default();
                tool_error(id, format!("run was denied: {error}"))
            }
        };
    }
    if matches!(
        name,
        "room_note_post" | "room_artifact_link" | "room_run_request" | "room_checkpoint_request"
    ) {
        let args = params.get("arguments").unwrap_or(&Value::Null);
        let Some(identity) = identity else {
            return tool_error(id, "identity not found".to_owned());
        };
        let mutation = match name {
            "room_note_post" => mutate_note(room_dir, args, identity),
            "room_artifact_link" => mutate_artifact(room_dir, artifact_root, args, identity),
            "room_run_request" => mutate_run(room_dir, args, identity),
            "room_checkpoint_request" => mutate_checkpoint(room_dir, args, identity),
            _ => unreachable!(),
        };
        return match mutation {
            Ok(event) => tool_result(id, serde_json::to_string(&event).expect("event serializes")),
            Err(error) => tool_error(id, error),
        };
    }
    tool_error(
        id,
        format!("{name} is not implemented in the current SymRoom MCP slice"),
    )
}

fn mutate_note(
    room_dir: &Path,
    args: &Value,
    identity: &symroom_core::identity::Identity,
) -> Result<symroom_core::event::Event, String> {
    let text = args
        .get("text")
        .and_then(Value::as_str)
        .filter(|text| !text.is_empty())
        .ok_or("text is required")?;
    let room = room_id(room_dir)?;
    let stats = symroom_core::journal::read_journal_stats(room_dir)
        .map_err(|error| format!("read journal stats: {error}"))?;
    if stats
        .member_state
        .members
        .get(&identity.member_id)
        .is_some_and(|member| member.role == "observer")
    {
        return Err("observer role has read-only access".to_owned());
    }
    let author = symroom_core::journal::author_stats(room_dir, &identity.member_id)
        .map_err(|error| error.to_string())?;
    let body = serde_json::value::RawValue::from_string(
        serde_json::to_string(&json!({"text":text})).map_err(|error| error.to_string())?,
    )
    .map_err(|error| error.to_string())?;
    append_signed(
        room_dir,
        identity,
        room,
        unique_event_id(),
        "note.posted",
        body,
        author.seq + 1,
        author.prev,
        stats.max_lamport + 1,
    )
}

fn mutate_artifact(
    room_dir: &Path,
    artifact_root: &Path,
    args: &Value,
    identity: &symroom_core::identity::Identity,
) -> Result<symroom_core::event::Event, String> {
    let path = args
        .get("path")
        .and_then(Value::as_str)
        .filter(|path| !path.is_empty())
        .ok_or("path is required")?;
    let file = PathBuf::from(path);
    let file = if file.is_absolute() {
        file
    } else {
        std::env::current_dir()
            .map_err(|error| error.to_string())?
            .join(file)
    };
    let root = if artifact_root.is_absolute() {
        artifact_root.to_path_buf()
    } else {
        std::env::current_dir()
            .map_err(|error| error.to_string())?
            .join(artifact_root)
    };
    let root = lexical_normalize(&root);
    let file = lexical_normalize(&file);
    let rel = file
        .strip_prefix(&root)
        .map_err(|_| "path is outside artifact root")?;
    let rel = rel.to_string_lossy().into_owned();
    let contents = fs::read(&file).map_err(|error| format!("compute sha256: {error}"))?;
    let hash = hex::encode(Sha256::digest(contents));
    let digest = hex::encode(Sha256::digest(format!("{rel}{hash}").as_bytes()));
    let artifact_id = format!("art_{}", &digest[..16]);
    let title = args
        .get("title")
        .and_then(Value::as_str)
        .filter(|title| !title.is_empty())
        .map(str::to_owned)
        .unwrap_or_else(|| {
            file.file_name()
                .unwrap_or_default()
                .to_string_lossy()
                .into_owned()
        });
    let body = serde_json::value::RawValue::from_string(serde_json::to_string(&json!({"artifact_id":artifact_id,"path":rel,"sha256":hash,"title":title,"symdesk_id":""})).map_err(|error| error.to_string())?).map_err(|error| error.to_string())?;
    append_signed(
        room_dir,
        identity,
        "rm_test".to_owned(),
        format!("ev_{}", &artifact_id[4..]),
        "artifact.linked",
        body,
        0,
        String::new(),
        0,
    )
}

fn mutate_run(
    room_dir: &Path,
    args: &Value,
    identity: &symroom_core::identity::Identity,
) -> Result<symroom_core::event::Event, String> {
    let title = args
        .get("title")
        .and_then(Value::as_str)
        .filter(|title| !title.is_empty())
        .ok_or("title is required")?;
    symroom_core::runs::request(
        room_dir,
        title,
        args.get("plan_file")
            .and_then(Value::as_str)
            .unwrap_or_default(),
        args.get("adapter")
            .and_then(Value::as_str)
            .unwrap_or_default(),
        identity,
    )
    .map_err(|error| error.to_string())
}

fn mutate_checkpoint(
    room_dir: &Path,
    args: &Value,
    identity: &symroom_core::identity::Identity,
) -> Result<symroom_core::event::Event, String> {
    let run_id = args
        .get("run_id")
        .and_then(Value::as_str)
        .filter(|id| !id.is_empty())
        .ok_or("run_id and question are required")?;
    let question = args
        .get("question")
        .and_then(Value::as_str)
        .filter(|question| !question.is_empty())
        .ok_or("run_id and question are required")?;
    let checkpoint_id = format!("chk_{}", &unique_event_id()[3..]);
    let body = serde_json::value::RawValue::from_string(
        serde_json::to_string(
            &json!({"checkpoint_id":checkpoint_id,"run_id":run_id,"question":question}),
        )
        .map_err(|error| error.to_string())?,
    )
    .map_err(|error| error.to_string())?;
    append_signed(
        room_dir,
        identity,
        "rm_test".to_owned(),
        format!("ev_{}", &checkpoint_id[4..]),
        "checkpoint.requested",
        body,
        0,
        String::new(),
        0,
    )
}

fn append_signed(
    room_dir: &Path,
    identity: &symroom_core::identity::Identity,
    room: String,
    id: String,
    kind: &str,
    body: Box<serde_json::value::RawValue>,
    seq: u64,
    prev: String,
    lamport: u64,
) -> Result<symroom_core::event::Event, String> {
    let mut event = symroom_core::event::Event {
        v: symroom_core::event::CURRENT_VERSION,
        id,
        room,
        author: identity.member_id.clone(),
        seq,
        prev,
        lamport,
        ts: symroom_core::event::format_timestamp(time::OffsetDateTime::now_utc()),
        kind: kind.to_owned(),
        body,
        sig: None,
    };
    if seq == 0 {
        let author = symroom_core::journal::author_stats(room_dir, &identity.member_id)
            .map_err(|error| error.to_string())?;
        event.seq = author.seq + 1;
        event.prev = author.prev;
        let stats = symroom_core::journal::read_journal_stats(room_dir)
            .map_err(|error| error.to_string())?;
        event.lamport = stats.max_lamport + 1;
    }
    event.sign(identity).map_err(|error| error.to_string())?;
    symroom_core::journal::append_event(room_dir, &event).map_err(|error| error.to_string())?;
    Ok(event)
}

fn room_id(room_dir: &Path) -> Result<String, String> {
    let content = fs::read_to_string(room_dir.join("room.toml"))
        .map_err(|error| format!("read room.toml: {error}"))?;
    content
        .lines()
        .find_map(|line| {
            line.split_once('=')
                .filter(|(key, _)| key.trim() == "id")
                .map(|(_, value)| value.trim().trim_matches('"').to_owned())
        })
        .ok_or_else(|| "room id is missing".to_owned())
}

fn lexical_normalize(path: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for component in path.components() {
        match component {
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                out.pop();
            }
            other => out.push(other.as_os_str()),
        }
    }
    out
}

fn unique_event_id() -> String {
    static NEXT: AtomicU64 = AtomicU64::new(0);
    let nonce = NEXT.fetch_add(1, Ordering::Relaxed);
    let stamp = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap_or_default()
        .as_nanos();
    let digest = Sha256::digest(format!("{stamp}:{}:{nonce}", std::process::id()).as_bytes());
    format!("ev_{}", &hex::encode(digest)[..20])
}

fn tool_result(id: Value, text: String) -> Value {
    json!({"jsonrpc":"2.0","id":id,"result":{"content":[{"type":"text","text":text}],"isError":false}})
}

#[derive(Serialize)]
struct RoomConfig {
    schema_version: i32,
    id: String,
    created: String,
    root_pubkey: String,
    root_event: String,
}

#[derive(Serialize)]
struct RoomStatus {
    max_lamport: u64,
    room: RoomConfig,
}

fn room_status(room_dir: &Path) -> Result<String, String> {
    let content = fs::read_to_string(room_dir.join("room.toml"))
        .map_err(|error| format!("read room.toml: {error}"))?;
    let mut room = RoomConfig {
        schema_version: 0,
        id: String::new(),
        created: String::new(),
        root_pubkey: String::new(),
        root_event: String::new(),
    };
    for line in content.lines() {
        let Some((key, value)) = line.split_once('=') else {
            continue;
        };
        let value = value.trim().trim_matches('"').to_owned();
        match key.trim() {
            "id" => room.id = value,
            "created" => room.created = value,
            "root_pubkey" => room.root_pubkey = value,
            "root_event" => room.root_event = value,
            _ => {}
        }
    }
    let stats = symroom_core::journal::read_journal_stats(room_dir)
        .map_err(|error| format!("read journal stats: {error}"))?;
    serde_json::to_string(&RoomStatus {
        max_lamport: stats.max_lamport,
        room,
    })
    .map_err(|error| error.to_string())
}

#[derive(Serialize)]
struct ArtifactRef {
    id: String,
    path: String,
    sha256: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    symdesk_id: String,
    status: String,
}

fn artifact_list(room_dir: &Path, artifact_root: &Path) -> Result<String, String> {
    let events = symroom_core::journal::merge_all(room_dir).map_err(|error| error.to_string())?;
    let mut active = std::collections::BTreeMap::<String, (String, String, String, String)>::new();
    for event in events {
        let body: Value = serde_json::from_str(event.body.get()).unwrap_or(Value::Null);
        match event.kind.as_str() {
            "artifact.linked" => {
                let Some(id) = body.get("artifact_id").and_then(Value::as_str) else {
                    continue;
                };
                let Some(path) = body.get("path").and_then(Value::as_str) else {
                    continue;
                };
                let Some(hash) = body.get("sha256").and_then(Value::as_str) else {
                    continue;
                };
                active.insert(
                    id.to_owned(),
                    (
                        path.to_owned(),
                        hash.to_owned(),
                        body.get("title")
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_owned(),
                        body.get("symdesk_id")
                            .and_then(Value::as_str)
                            .unwrap_or_default()
                            .to_owned(),
                    ),
                );
            }
            "artifact.unlinked" => {
                if let Some(id) = body.get("artifact_id").and_then(Value::as_str) {
                    active.remove(id);
                }
            }
            _ => {}
        }
    }
    if active.is_empty() {
        return Ok("null".to_owned());
    }
    let refs = active
        .into_iter()
        .map(|(id, (path, expected_hash, title, symdesk_id))| {
            let file_path = artifact_root.join(&path);
            let status = match fs::read(&file_path) {
                Ok(bytes) => {
                    if format!("{:x}", Sha256::digest(&bytes)) == expected_hash {
                        "ok"
                    } else {
                        "modified"
                    }
                }
                Err(error) if error.kind() == io::ErrorKind::NotFound => "missing",
                Err(_) => "error",
            };
            ArtifactRef {
                id,
                path,
                sha256: expected_hash,
                title,
                symdesk_id,
                status: status.to_owned(),
            }
        })
        .collect::<Vec<_>>();
    serde_json::to_string(&refs).map_err(|error| error.to_string())
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
    if first.trim_start().starts_with('{') || !first.contains(':') {
        return Ok(Some(first.into_bytes()));
    }
    let mut content_length = first
        .trim()
        .strip_prefix("Content-Length:")
        .map(str::trim)
        .map(str::parse::<usize>)
        .transpose()
        .map_err(invalid_data)?;
    if first.trim().starts_with("Content-Length:") && content_length.is_none() {
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
        if let Some(length) = header.trim().strip_prefix("Content-Length:") {
            content_length = Some(length.trim().parse::<usize>().map_err(invalid_data)?);
        }
    }
    let length = content_length.ok_or_else(|| {
        io::Error::new(io::ErrorKind::InvalidData, "missing Content-Length header")
    })?;
    if length == 0 || length > 1 << 20 {
        return Err(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("invalid Content-Length: {length}"),
        ));
    }
    let mut body = vec![0; length];
    input.read_exact(&mut body)?;
    Ok(Some(body))
}

fn invalid_data(error: std::num::ParseIntError) -> io::Error {
    io::Error::new(io::ErrorKind::InvalidData, error)
}

fn write_response<W: Write>(output: &mut W, value: Value) -> io::Result<()> {
    let body = serde_json::to_vec(&value).map_err(io::Error::other)?;
    write!(output, "Content-Length: {}\r\n\r\n", body.len())?;
    output.write_all(&body)
}
