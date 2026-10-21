#[path = "../src/mcp.rs"]
mod mcp;
#[path = "../src/symdesk_inspect.rs"]
#[allow(
    dead_code,
    reason = "shared with the binary; the test only uses the search-path variants"
)]
mod symdesk_inspect;

use std::{
    io::{BufReader, Cursor, Write},
    path::{Path, PathBuf},
    process::{Command, Stdio},
};

use serde_json::{Value, json};
use sha2::Digest;

fn temporary_room(suffix: &str) -> PathBuf {
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("clock after epoch")
        .as_nanos();
    let room = std::env::temp_dir().join(format!(
        "symroom-mcp-{suffix}-{}-{nonce}",
        std::process::id()
    ));
    std::fs::create_dir_all(&room).expect("temporary room directory");
    room
}

fn fixture_room(suffix: &str, journal_lines: &[String]) -> PathBuf {
    let room = temporary_room(suffix);
    std::fs::write(
        room.join("room.toml"),
        "schema_version = 1\nid = \"rm_fixture\"\ncreated = \"2026-09-01T00:00:00Z\"\nroot_pubkey = \"ed25519:fixture\"\nroot_event = \"ev_fixture\"\n",
    )
    .expect("room config");
    std::fs::write(room.join("known.txt"), b"artifact-content").expect("artifact file");
    let journal = room.join("journal");
    std::fs::create_dir_all(&journal).expect("journal directory");
    std::fs::write(
        journal.join("member_fixture.jsonl"),
        format!("{}\n", journal_lines.join("\n")),
    )
    .expect("journal fixture");
    room
}

#[test]
fn go_mcp_inventory_call_and_error_frames_match() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Value = serde_json::from_slice(
        &std::fs::read(root.join("testdata/port/room/mcp-parity.json")).expect("Go oracle fixture"),
    )
    .expect("valid Go oracle fixture");
    let cases = fixture["cases"].as_array().expect("cases");
    let names = cases[0]["response"]["result"]["tools"]
        .as_array()
        .expect("tool inventory")
        .iter()
        .map(|tool| tool["name"].as_str().expect("tool name"))
        .collect::<Vec<_>>();
    assert_eq!(names.len(), 8);
    assert!(!names.iter().any(|name| name.contains("approve")));

    let mut input = Vec::new();
    for case in cases {
        let body = serde_json::to_vec(&case["request"]).expect("request JSON");
        input.extend_from_slice(format!("Content-Length: {}\r\n\r\n", body.len()).as_bytes());
        input.extend_from_slice(&body);
    }
    let journal_lines = fixture["journal_lines"]
        .as_array()
        .expect("signed Go journal lines")
        .iter()
        .map(|line| line.as_str().expect("journal line").to_owned())
        .collect::<Vec<_>>();
    assert!(
        journal_lines
            .iter()
            .all(|line| line.contains("\"sig\":\"ed25519:"))
    );
    let room = fixture_room("oracle", &journal_lines);
    let mut output = Vec::new();
    mcp::serve_io_with_identity(
        BufReader::new(Cursor::new(input)),
        &mut output,
        &room,
        &room,
        None,
    )
    .expect("MCP serve");
    let mut actual = decode_frames(&output);
    let mut expected = cases
        .iter()
        .map(|case| case["response"].clone())
        .collect::<Vec<_>>();
    actual.sort_by_key(|frame| frame["id"].as_u64().expect("numeric request ID"));
    expected.sort_by_key(|frame| frame["id"].as_u64().expect("numeric oracle ID"));
    assert_eq!(actual, expected);
    let _ = std::fs::remove_dir_all(room);
}

#[test]
fn go_mcp_mutation_events_match_and_are_signed() {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..");
    let fixture: Value = serde_json::from_slice(
        &std::fs::read(root.join("testdata/port/room/mcp-mutations.json"))
            .expect("Go mutation oracle fixture"),
    )
    .expect("valid mutation fixture");
    let seed = sha2::Sha256::digest(b"symroom-mcp-mutation-seed");
    let identity = symroom_core::identity::identity_from_private_key("fixture", seed.as_slice())
        .expect("fixed fixture identity");
    let artifact_root = root.join("testdata/port/room");
    let room = fixture_room("mutations", &[]);
    let cases = fixture["cases"].as_array().expect("mutation cases");
    let mut actual = Vec::new();
    for case in cases {
        let mut input = Vec::new();
        let mut request = case["request"].clone();
        let args = request["params"]["arguments"]
            .as_object_mut()
            .expect("tool args");
        if args.get("path").and_then(Value::as_str) == Some("${ARTIFACT}") {
            args.insert(
                "path".to_owned(),
                json!(artifact_root.join("mcp-artifact.txt").to_string_lossy()),
            );
        }
        let body = serde_json::to_vec(&request).expect("request JSON");
        input.extend_from_slice(format!("Content-Length: {}\r\n\r\n", body.len()).as_bytes());
        input.extend_from_slice(&body);
        let mut output = Vec::new();
        // No `symdesk` on PATH, so the recorded `symdesk_id` is deterministic.
        let path_dir = temporary_room("no-symdesk-path");
        mcp::serve_io_with_search_path(
            BufReader::new(Cursor::new(input)),
            &mut output,
            &room,
            &artifact_root,
            Some(&identity),
            Some(path_dir.as_os_str()),
        )
        .expect("MCP serve");
        let _ = std::fs::remove_dir_all(path_dir);
        actual.extend(decode_frames(&output));
    }
    assert_eq!(actual.len(), cases.len());
    for (response, case) in actual.iter().zip(cases) {
        assert_eq!(response["result"]["isError"], false);
        let text = response["result"]["content"][0]["text"]
            .as_str()
            .expect("event text");
        let mut event: Value = serde_json::from_str(text).expect("event JSON");
        let raw: symroom_core::event::Event = serde_json::from_str(text).expect("typed event");
        raw.verify_signature(&identity.public_key)
            .expect("signature verifies");
        // Go stamps a wall-clock `ts` on note.posted only; the other three kinds
        // keep Go's literal empty string, which stays in the comparison.
        if event["kind"] == "note.posted" {
            event.as_object_mut().unwrap().remove("ts");
        } else {
            assert_eq!(event["ts"], "", "{} must leave ts empty", event["kind"]);
        }
        event.as_object_mut().unwrap().remove("sig");
        event.as_object_mut().unwrap().remove("prev");
        if event["kind"] == "note.posted" {
            event["id"] = json!("ev_<generated>");
        }
        if event["kind"] == "checkpoint.requested" {
            event["id"] = json!("ev_<generated>");
            event["body"]["checkpoint_id"] = json!("chk_<generated>");
        }
        let expected = &case["response"];
        assert_eq!(response["id"], expected["id"]);
        assert_eq!(response["result"]["isError"], expected["result"]["isError"]);
        let expected_event: Value = serde_json::from_str(
            expected["result"]["content"][0]["text"]
                .as_str()
                .expect("expected event text"),
        )
        .expect("normalized expected event");
        assert_eq!(event, expected_event);
    }
    let journal = std::fs::read_to_string(
        room.join("journal")
            .join(format!("{}.jsonl", identity.member_id)),
    )
    .expect("mutations persisted");
    let lines = journal.lines().collect::<Vec<_>>();
    assert_eq!(lines.len(), 4);
    for line in lines {
        let event = symroom_core::event::Event::unmarshal_json_line(line.as_bytes())
            .expect("journal event parses");
        event
            .verify_signature(&identity.public_key)
            .expect("persisted event signature verifies");
    }
    let _ = std::fs::remove_dir_all(room);
}

fn decode_frames(mut bytes: &[u8]) -> Vec<Value> {
    let mut values = Vec::new();
    while !bytes.is_empty() {
        let end = bytes
            .windows(4)
            .position(|window| window == b"\r\n\r\n")
            .expect("frame header");
        let header = std::str::from_utf8(&bytes[..end]).expect("ASCII header");
        let length = header
            .strip_prefix("Content-Length: ")
            .expect("length header")
            .parse::<usize>()
            .expect("length");
        let start = end + 4;
        values.push(serde_json::from_slice(&bytes[start..start + length]).expect("response JSON"));
        bytes = &bytes[start + length..];
    }
    values
}

#[test]
fn no_approval_granting_call_is_exposed() {
    let request = json!({"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"room_approve","arguments":{}}});
    let body = serde_json::to_vec(&request).expect("request");
    let input = format!(
        "Content-Length: {}\r\n\r\n{}",
        body.len(),
        String::from_utf8(body).unwrap()
    );
    let room = temporary_room("deny");
    let mut output = Vec::new();
    mcp::serve_io_with_identity(
        BufReader::new(Cursor::new(input)),
        &mut output,
        &room,
        &room,
        None,
    )
    .expect("MCP serve");
    let response = decode_frames(&output).remove(0);
    assert_eq!(response["error"]["code"], -32601);
    assert_eq!(response["error"]["message"], "Unknown tool: room_approve");
    let _ = std::fs::remove_dir_all(room);
}

#[test]
fn mcp_subcommand_serves_framed_tool_inventory() {
    let fixture: Value = serde_json::from_slice(
        &std::fs::read(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/room/mcp-parity.json"),
        )
        .unwrap(),
    )
    .unwrap();
    let request = serde_json::to_vec(&fixture["cases"][0]["request"]).unwrap();
    // Go `mcp` always resolves a signing identity before serving, so the
    // subcommand needs one in a disposable data home.
    let home = std::env::temp_dir().join(format!("symroom-mcp-serve-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&home);
    std::fs::create_dir_all(&home).unwrap();
    let created = Command::new(env!("CARGO_BIN_EXE_symroom"))
        .args(["identity", "create", "owner"])
        .env("HOME", &home)
        .env("XDG_DATA_HOME", &home)
        .env("XDG_CONFIG_HOME", &home)
        .output()
        .unwrap();
    assert!(created.status.success(), "identity create: {created:?}");
    let mut child = Command::new(env!("CARGO_BIN_EXE_symroom"))
        .args(["mcp", "--identity", "owner"])
        .env("HOME", &home)
        .env("XDG_DATA_HOME", &home)
        .env("XDG_CONFIG_HOME", &home)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    {
        let mut stdin = child.stdin.take().unwrap();
        write!(stdin, "Content-Length: {}\r\n\r\n", request.len()).unwrap();
        stdin.write_all(&request).unwrap();
    }
    let output = child.wait_with_output().unwrap();
    let _ = std::fs::remove_dir_all(&home);
    assert!(output.status.success(), "stderr: {:?}", output.stderr);
    assert_eq!(
        decode_frames(&output.stdout),
        vec![fixture["cases"][0]["response"].clone()]
    );
}

#[test]
fn stdio_malformed_oversized_interleaved_and_eof_frames_are_clean() {
    let room = temporary_room("stdio-hygiene");
    let wait = json!({
        "jsonrpc":"2.0",
        "id":2,
        "method":"tools/call",
        "params":{"name":"room_run_wait","arguments":{"run_id":"run_missing","timeout_seconds":1}}
    });
    let ping = json!({"jsonrpc":"2.0","id":3,"method":"ping"});
    let list = json!({"jsonrpc":"2.0","id":4,"method":"tools/list"});
    let mut input = b"Content-Length: 5\r\n\r\n{bad}".to_vec();
    for request in [wait, ping, list] {
        let body = serde_json::to_vec(&request).expect("request JSON");
        input.extend_from_slice(format!("Content-Length: {}\r\n\r\n", body.len()).as_bytes());
        input.extend_from_slice(&body);
    }

    let mut output = Vec::new();
    mcp::serve_io_with_identity(
        BufReader::new(Cursor::new(input)),
        &mut output,
        &room,
        &room,
        None,
    )
    .expect("malformed request is reported and stream continues through EOF");
    let frames = decode_frames(&output);
    assert_eq!(frames.len(), 4);
    assert_eq!(frames[0]["id"], Value::Null);
    assert_eq!(frames[0]["error"]["code"], -32700);
    assert_eq!(
        frames[1..]
            .iter()
            .map(|response| response["id"].as_i64().unwrap())
            .collect::<Vec<_>>(),
        [3, 4, 2]
    );
    assert!(
        String::from_utf8(output)
            .unwrap()
            .starts_with("Content-Length: ")
    );

    let mut output = Vec::new();
    let error = mcp::serve_io_with_identity(
        BufReader::new(Cursor::new(b"Content-Length: 1048577\r\n\r\n")),
        &mut output,
        &room,
        &room,
        None,
    )
    .expect_err("oversized frame rejected");
    assert_eq!(error.kind(), std::io::ErrorKind::InvalidData);
    assert!(output.is_empty(), "oversized input must not leak to stdout");

    let mut output = Vec::new();
    mcp::serve_io_with_identity(
        BufReader::new(Cursor::new(Vec::<u8>::new())),
        &mut output,
        &room,
        &room,
        None,
    )
    .expect("empty EOF shuts down cleanly");
    assert!(output.is_empty(), "EOF must not write to stdout");
    let _ = std::fs::remove_dir_all(room);
}

// Seeded raw-frame property check (MCP-005): random mixes of valid frames,
// malformed headers, truncated bodies, oversized claims and junk must never
// leak junk to stdout, never emit a non-frame byte, and always end cleanly.
#[test]
fn raw_frame_property_random_streams_keep_stdout_frames_only() {
    const SENTINEL: &[u8] = b"SYMROOM-LEAK-SENTINEL";
    let room = temporary_room("raw-frame-property");
    let mut state = 0x9e37_79b9_7f4a_7c15_u64;
    let mut next = move || {
        state ^= state << 13;
        state ^= state >> 7;
        state ^= state << 17;
        state
    };
    let mut responses = 0_usize;
    for case in 0..200_u32 {
        let mut input = Vec::new();
        let pieces = 1 + next() % 6;
        for _ in 0..pieces {
            match next() % 5 {
                0 => {
                    let request = json!({"jsonrpc":"2.0","id":case,"method":"ping"});
                    let body = serde_json::to_vec(&request).expect("request JSON");
                    input.extend_from_slice(
                        format!("Content-Length: {}\r\n\r\n", body.len()).as_bytes(),
                    );
                    input.extend_from_slice(&body);
                }
                1 => input.extend_from_slice(b"Content-Length: 4\r\n\r\n{[]}"),
                2 => {
                    // Header claims more bytes than follow: truncated body at EOF.
                    input.extend_from_slice(b"Content-Length: 64\r\n\r\n");
                    input.extend_from_slice(SENTINEL);
                }
                3 => input.extend_from_slice(b"Content-Length: 1048577\r\n\r\n"),
                _ => {
                    for _ in 0..(next() % 48) {
                        input.push((next() % 256) as u8);
                    }
                }
            }
        }

        let mut output = Vec::new();
        let result = mcp::serve_io_with_identity(
            BufReader::new(Cursor::new(input)),
            &mut output,
            &room,
            &room,
            None,
        );
        assert!(
            !output
                .windows(SENTINEL.len())
                .any(|window| window == SENTINEL),
            "case {case}: junk leaked to stdout"
        );
        // Every byte written to stdout must belong to a complete, parseable
        // response: a Content-Length frame, or (as in Go, which answers a
        // JSON line in kind) one newline-terminated JSON-RPC line.
        let mut rest = output.as_slice();
        while !rest.is_empty() {
            if rest.starts_with(b"{") {
                let end = rest
                    .iter()
                    .position(|byte| *byte == b'\n')
                    .unwrap_or_else(|| panic!("case {case}: unterminated response line"));
                let value: Value = serde_json::from_slice(&rest[..end])
                    .unwrap_or_else(|_| panic!("case {case}: response line is not JSON"));
                assert_eq!(value["jsonrpc"], "2.0", "case {case}");
                responses += 1;
                rest = &rest[end + 1..];
                continue;
            }
            let end = rest
                .windows(4)
                .position(|window| window == b"\r\n\r\n")
                .unwrap_or_else(|| panic!("case {case}: stdout is not framed"));
            let header = std::str::from_utf8(&rest[..end]).expect("ASCII header");
            let length = header
                .strip_prefix("Content-Length: ")
                .and_then(|value| value.parse::<usize>().ok())
                .unwrap_or_else(|| panic!("case {case}: bad response header {header:?}"));
            let start = end + 4;
            let body = rest
                .get(start..start + length)
                .unwrap_or_else(|| panic!("case {case}: truncated response frame"));
            let value: Value = serde_json::from_slice(body)
                .unwrap_or_else(|_| panic!("case {case}: response body is not JSON"));
            assert_eq!(value["jsonrpc"], "2.0", "case {case}");
            responses += 1;
            rest = &rest[start + length..];
        }
        if let Err(error) = result {
            assert!(
                matches!(
                    error.kind(),
                    std::io::ErrorKind::InvalidData | std::io::ErrorKind::UnexpectedEof
                ),
                "case {case}: unexpected error kind {:?}",
                error.kind()
            );
        }
    }
    // Guard against a vacuous pass: the valid-frame path must actually be exercised.
    assert!(responses > 0, "no response frames were produced");
    let _ = std::fs::remove_dir_all(room);
}

#[test]
fn room_status_is_not_queued_behind_four_run_waits() {
    let room = fixture_room("parallel-status", &[]);
    let mut input = Vec::new();
    for id in 1..=4 {
        let request = json!({
            "jsonrpc":"2.0",
            "id":id,
            "method":"tools/call",
            "params":{"name":"room_run_wait","arguments":{"run_id":format!("run_missing_{id}"),"timeout_seconds":1}}
        });
        let body = serde_json::to_vec(&request).expect("wait request JSON");
        input.extend_from_slice(format!("Content-Length: {}\r\n\r\n", body.len()).as_bytes());
        input.extend_from_slice(&body);
    }
    let status = json!({
        "jsonrpc":"2.0",
        "id":5,
        "method":"tools/call",
        "params":{"name":"room_status","arguments":{}}
    });
    let body = serde_json::to_vec(&status).expect("status request JSON");
    input.extend_from_slice(format!("Content-Length: {}\r\n\r\n", body.len()).as_bytes());
    input.extend_from_slice(&body);

    let mut output = Vec::new();
    mcp::serve_io_with_identity(
        BufReader::new(Cursor::new(input)),
        &mut output,
        &room,
        &room,
        None,
    )
    .expect("MCP serve");
    let responses = decode_frames(&output);
    assert_eq!(responses.len(), 5);
    assert_eq!(responses[0]["id"], 5, "status should finish before waits");
    assert_eq!(responses[0]["result"]["isError"], false);
    assert!(
        responses[1..]
            .iter()
            .all(|response| response["result"]["isError"] == true)
    );
    let waiter_ids = responses[1..]
        .iter()
        .map(|response| response["id"].as_i64().expect("response id"))
        .collect::<std::collections::BTreeSet<_>>();
    assert_eq!(
        waiter_ids,
        [1, 2, 3, 4]
            .into_iter()
            .collect::<std::collections::BTreeSet<_>>()
    );
    let _ = std::fs::remove_dir_all(room);
}

#[test]
fn mcp_cli_help_exits_successfully() {
    let args = [std::ffi::OsString::from("--help")];
    let _ = mcp::run_cli(&args, |_| {
        unreachable!("help exits before identity resolution")
    });
}

// ---------------------------------------------------------------------------
// MCP-004 scenario replay: every step below is real Go output recorded by the
// room-mcp-fixtures-generate target (internal/room/mcp/port_scenarios_test.go).
// ---------------------------------------------------------------------------

const PARITY_STEPS: usize = 208;
const MUTATION_STEPS: usize = 135;

fn repo_root() -> PathBuf {
    let mut out = PathBuf::new();
    for component in PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .components()
    {
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

fn materialize(setup: &Value) -> PathBuf {
    let room = temporary_room("scenario");
    if let Some(toml) = setup["room_toml"].as_str() {
        std::fs::write(room.join("room.toml"), toml).expect("room.toml");
    }
    if let Some(files) = setup["files"].as_object() {
        for (name, content) in files {
            std::fs::write(room.join(name), content.as_str().expect("file text")).expect("file");
        }
    }
    if let Some(lines) = setup["journal_lines"].as_array() {
        let journal = room.join("journal");
        std::fs::create_dir_all(&journal).expect("journal dir");
        if !lines.is_empty() {
            let joined = lines
                .iter()
                .map(|line| line.as_str().expect("journal line"))
                .collect::<Vec<_>>()
                .join("\n");
            std::fs::write(
                journal.join(setup["journal_file"].as_str().expect("journal file")),
                format!("{joined}\n"),
            )
            .expect("journal file");
        }
    }
    room
}

fn substitute_value(value: &Value, replacements: &[(&str, String)]) -> Value {
    match value {
        Value::String(text) => Value::String(substitute_text(text, replacements)),
        Value::Array(items) => Value::Array(
            items
                .iter()
                .map(|item| substitute_value(item, replacements))
                .collect(),
        ),
        Value::Object(map) => Value::Object(
            map.iter()
                .map(|(key, item)| (key.clone(), substitute_value(item, replacements)))
                .collect(),
        ),
        other => other.clone(),
    }
}

fn substitute_text(text: &str, replacements: &[(&str, String)]) -> String {
    let mut out = text.to_owned();
    for (from, to) in replacements {
        out = out.replace(from, to);
    }
    expand_repeat(&out)
}

// `{{repeat:C:N}}` expands to the single character C repeated N times.
fn expand_repeat(text: &str) -> String {
    let mut out = text.to_owned();
    while let Some(start) = out.find("{{repeat:") {
        let rest = &out[start + "{{repeat:".len()..];
        let end = rest.find("}}").expect("repeat terminator");
        let (character, count) = rest[..end].split_once(':').expect("repeat spec");
        let expanded = character.repeat(count.parse().expect("repeat count"));
        out = format!("{}{expanded}{}", &out[..start], &rest[end + 2..]);
    }
    out
}

// `{{hex:AABB}}` expands to raw bytes (invalid UTF-8 cannot live in a JSON
// fixture string); everything else is copied byte for byte.
fn expand_hex(text: &str) -> Vec<u8> {
    let mut out = Vec::new();
    let mut rest = text;
    while let Some(start) = rest.find("{{hex:") {
        out.extend_from_slice(&rest.as_bytes()[..start]);
        let after = &rest[start + "{{hex:".len()..];
        let end = after.find("}}").expect("hex terminator");
        out.extend(
            (0..end)
                .step_by(2)
                .map(|index| u8::from_str_radix(&after[index..index + 2], 16).expect("hex byte")),
        );
        rest = &after[end + 2..];
    }
    out.extend_from_slice(rest.as_bytes());
    out
}

fn mask_json_string_value(text: &str, key: &str, replacement: &str) -> String {
    let needle = format!("\"{key}\":\"");
    let mut out = String::new();
    let mut rest = text;
    loop {
        let Some(index) = rest.find(&needle) else {
            out.push_str(rest);
            return out;
        };
        let start = index + needle.len();
        let Some(end) = rest[start..].find('"') else {
            out.push_str(rest);
            return out;
        };
        out.push_str(&rest[..start]);
        out.push_str(replacement);
        rest = &rest[start + end..];
    }
}

// Only note.posted carries a generated wall-clock `ts` in Go; run.requested,
// checkpoint.requested and artifact.linked keep Go's literal `"ts":""`, which
// therefore stays unmasked and is compared byte for byte.
fn mask_generated_timestamps(text: &str) -> String {
    let trimmed = text.trim();
    if trimmed.starts_with('[')
        && let Ok(items) = serde_json::from_str::<Vec<Box<serde_json::value::RawValue>>>(trimmed)
    {
        let masked = items
            .iter()
            .map(|item| mask_event_timestamp(item.get()))
            .collect::<Vec<_>>();
        return format!("[{}]", masked.join(","));
    }
    mask_event_timestamp(text)
}

fn mask_event_timestamp(text: &str) -> String {
    let is_note =
        serde_json::from_str::<Value>(text).is_ok_and(|event| event["kind"] == "note.posted");
    if is_note {
        mask_json_string_value(text, "ts", "<ts>")
    } else {
        text.to_owned()
    }
}

fn normalize_event_text(text: &str) -> String {
    if !text.contains("\"kind\":\"") {
        return text.to_owned();
    }
    let mut text = mask_generated_timestamps(text);
    text = mask_json_string_value(&text, "sig", "<sig>");
    text = mask_json_string_value(&text, "prev", "<prev>");
    if text.contains("\"kind\":\"note.posted\"")
        || text.contains("\"kind\":\"checkpoint.requested\"")
    {
        text = mask_json_string_value(&text, "id", "ev_<generated>");
        text = mask_json_string_value(&text, "checkpoint_id", "chk_<generated>");
    }
    text
}

// Go builds `room_artifact_list` from a map, so the oracle sorts it by id.
fn sort_artifact_refs(text: &str) -> String {
    let Ok(items) = serde_json::from_str::<Vec<Box<serde_json::value::RawValue>>>(text) else {
        return text.to_owned();
    };
    if items.is_empty() {
        return text.to_owned();
    }
    let mut keyed = Vec::new();
    for item in &items {
        let probe: Value = serde_json::from_str(item.get()).expect("artifact ref");
        let (Some(id), Some(_)) = (probe["id"].as_str(), probe["status"].as_str()) else {
            return text.to_owned();
        };
        keyed.push((id.to_owned(), item.get().to_owned()));
    }
    keyed.sort_by(|left, right| left.0.cmp(&right.0));
    format!(
        "[{}]",
        keyed
            .into_iter()
            .map(|(_, raw)| raw)
            .collect::<Vec<_>>()
            .join(",")
    )
}

struct ScenarioContext<'a> {
    scenario: &'a Value,
    room: PathBuf,
    artifact_root: PathBuf,
}

impl ScenarioContext<'_> {
    fn normalize_output(&self, text: &str) -> String {
        let mut out = text.to_owned();
        if self.scenario["sort_artifact_lists"] == true {
            out = sort_artifact_refs(&out);
        }
        if self.scenario["normalize_events"] == true {
            out = normalize_event_text(&out);
        }
        let room = self.room.to_string_lossy().into_owned();
        let root = self.artifact_root.to_string_lossy().into_owned();
        if root != room {
            out = out.replace(&root, "${ARTIFACT_ROOT}");
        }
        out.replace(&room, "${ROOM}")
    }

    fn normalize_tree(&self, value: &Value) -> Value {
        match value {
            Value::String(text) => Value::String(self.normalize_output(text)),
            Value::Array(items) => {
                Value::Array(items.iter().map(|item| self.normalize_tree(item)).collect())
            }
            Value::Object(map) => Value::Object(
                map.iter()
                    .map(|(key, item)| (key.clone(), self.normalize_tree(item)))
                    .collect(),
            ),
            other => other.clone(),
        }
    }
}

/// Installs the `symdesk` script the Go oracle ran as an executable in `dir`.
fn install_fake_symdesk(dir: &Path, script: &str) {
    let path = dir.join("symdesk");
    std::fs::write(&path, format!("#!/bin/sh\n{script}\n")).expect("fake symdesk");
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o700))
            .expect("fake symdesk mode");
    }
}

fn identity_from_seed(seed: &[u8]) -> symroom_core::identity::Identity {
    let seed = sha2::Sha256::digest(seed);
    symroom_core::identity::identity_from_private_key("fixture", seed.as_slice())
        .expect("fixed fixture identity")
}

/// A scenario that installs a fake `symdesk` is recorded from a `#!/bin/sh`
/// script that neither Go's `exec.LookPath` nor Rust can run on Windows (a
/// PATHEXT extension is required and there is no shell script loader), so it
/// is replayed on Unix only. The Go oracle skips exactly the same scenarios
/// (`skipFakeSymdesk` in internal/room/mcp/port_scenarios_test.go).
fn needs_fake_symdesk(scenario: &Value) -> bool {
    scenario["symdesk_script"]
        .as_str()
        .is_some_and(|script| !script.is_empty())
}

/// Whether this platform skips the fake-symdesk scenarios.
fn skips_fake_symdesk() -> bool {
    cfg!(windows)
}

/// Replays every scenario of a fixture file and returns the number of executed
/// steps so a vacuous pass is rejected. `fixture_steps` is the step count of
/// the whole fixture on every platform; the count executed here is that total
/// minus the fake-symdesk steps on Windows, derived from the fixture itself.
fn replay_scenarios(fixture_name: &str, fixture_steps: usize) -> usize {
    let root = repo_root();
    let fixture: Value = serde_json::from_slice(
        &std::fs::read(root.join("testdata/port/room").join(fixture_name)).expect("fixture"),
    )
    .expect("valid fixture");
    let scenarios = fixture["scenarios"].as_array().expect("scenarios");
    let setups = fixture["setups"].as_object().expect("setups");
    let fixture_identity = identity_from_seed(b"symroom-mcp-fixture-signing-seed");
    let mutation_identity = identity_from_seed(b"symroom-mcp-mutation-seed");
    let step_count = |scenario: &Value| scenario["steps"].as_array().expect("steps").len();
    let total_steps: usize = scenarios.iter().map(step_count).sum();
    assert_eq!(
        total_steps, fixture_steps,
        "{fixture_name}: fixture step count"
    );
    let fake_symdesk_steps: usize = scenarios
        .iter()
        .filter(|scenario| needs_fake_symdesk(scenario))
        .map(step_count)
        .sum();
    // Independent of the skip decision below, so a skip that is wrongly
    // active (or missing) on a platform changes `steps_run` away from this.
    let expected_steps = if cfg!(windows) {
        total_steps - fake_symdesk_steps
    } else {
        total_steps
    };
    let mut steps_run = 0;
    for scenario in scenarios {
        let name = scenario["name"].as_str().expect("scenario name");
        if skips_fake_symdesk() && needs_fake_symdesk(scenario) {
            continue;
        }
        let setup = &setups[scenario["setup"].as_str().expect("setup name")];
        let room = materialize(setup);
        // Every Go-signed setup line must verify under the Rust canonical form.
        for line in setup["journal_lines"].as_array().into_iter().flatten() {
            let event = symroom_core::event::Event::unmarshal_json_line(
                line.as_str().expect("line").as_bytes(),
            )
            .expect("setup line parses");
            event
                .verify_signature(&fixture_identity.public_key)
                .unwrap_or_else(|error| panic!("{name}: Go-signed setup line rejected: {error}"));
        }
        let artifact_root = if scenario["artifact_root"] == "testdata" {
            root.join("testdata/port/room")
        } else {
            room.clone()
        };
        let context = ScenarioContext {
            scenario,
            room: room.clone(),
            artifact_root: artifact_root.clone(),
        };
        let identity = (scenario["identity"] == true).then_some(&mutation_identity);
        // The scenario's own executable search path: empty, or holding exactly
        // the fake `symdesk` script the Go oracle ran against.
        let path_dir = temporary_room("scenario-path");
        if let Some(script) = scenario["symdesk_script"].as_str() {
            install_fake_symdesk(&path_dir, script);
        }
        let replacements = [
            ("${ROOM}", room.to_string_lossy().into_owned()),
            (
                "${ARTIFACT_ROOT}",
                artifact_root.to_string_lossy().into_owned(),
            ),
            (
                "${ARTIFACT}",
                root.join("testdata/port/room/mcp-artifact.txt")
                    .to_string_lossy()
                    .into_owned(),
            ),
        ];
        for step in scenario["steps"].as_array().expect("steps") {
            let label = format!("{name}/{}", step["name"].as_str().unwrap_or("?"));
            steps_run += 1;
            if let Some(input) = step["input"].as_str() {
                let input = substitute_text(input, &replacements);
                let mut output = Vec::new();
                let result = mcp::serve_io_with_search_path(
                    BufReader::new(Cursor::new(expand_hex(&input))),
                    &mut output,
                    &room,
                    &artifact_root,
                    identity,
                    Some(path_dir.as_os_str()),
                );
                let error = result.err().map(|error| error.to_string());
                assert_eq!(
                    context.normalize_output(&String::from_utf8(output).expect("UTF-8 output")),
                    step["output"].as_str().expect("expected output"),
                    "{label}: stream bytes"
                );
                assert_eq!(
                    error
                        .map(|text| context.normalize_output(&text))
                        .unwrap_or_default(),
                    step["error"].as_str().expect("expected error"),
                    "{label}: stream error"
                );
            } else {
                let request = substitute_value(&step["request"], &replacements);
                let body = serde_json::to_vec(&request).expect("request JSON");
                let mut input = format!("Content-Length: {}\r\n\r\n", body.len()).into_bytes();
                input.extend_from_slice(&body);
                let mut output = Vec::new();
                mcp::serve_io_with_search_path(
                    BufReader::new(Cursor::new(input)),
                    &mut output,
                    &room,
                    &artifact_root,
                    identity,
                    Some(path_dir.as_os_str()),
                )
                .unwrap_or_else(|error| panic!("{label}: serve failed: {error}"));
                let frames = decode_frames(&output);
                assert!(frames.len() <= 1, "{label}: {} frames", frames.len());
                let actual = frames
                    .first()
                    .map_or(Value::Null, |frame| context.normalize_tree(frame));
                assert_eq!(actual, step["response"], "{label}: response");
            }
        }
        verify_scenario_journal(
            name,
            scenario,
            &context,
            &room,
            &fixture_identity,
            &mutation_identity,
        );
        let _ = std::fs::remove_dir_all(room);
        let _ = std::fs::remove_dir_all(path_dir);
    }
    assert_eq!(steps_run, expected_steps, "{fixture_name}: executed steps");
    assert!(
        steps_run > 0,
        "{fixture_name}: scenario replay must not be vacuous"
    );
    steps_run
}

fn verify_scenario_journal(
    name: &str,
    scenario: &Value,
    context: &ScenarioContext<'_>,
    room: &Path,
    fixture_identity: &symroom_core::identity::Identity,
    mutation_identity: &symroom_core::identity::Identity,
) {
    let mut actual = serde_json::Map::new();
    let mut entries = std::fs::read_dir(room.join("journal"))
        .map(|entries| entries.map(|entry| entry.expect("entry").path()).collect())
        .unwrap_or_else(|_| Vec::<PathBuf>::new());
    entries.sort();
    for path in entries {
        let content = std::fs::read_to_string(&path).expect("journal file");
        let lines = content
            .trim_end_matches('\n')
            .split('\n')
            .collect::<Vec<_>>();
        for (index, line) in lines.iter().enumerate() {
            let event = symroom_core::event::Event::unmarshal_json_line(line.as_bytes())
                .expect("journal event parses");
            let key = if event.author == mutation_identity.member_id {
                &mutation_identity.public_key
            } else {
                &fixture_identity.public_key
            };
            event
                .verify_signature(key)
                .unwrap_or_else(|error| panic!("{name}: journal signature rejected: {error}"));
            let expected_prev = if index == 0 {
                format!("sha256:{}", "0".repeat(64))
            } else {
                format!(
                    "sha256:{}",
                    hex::encode(sha2::Sha256::digest(lines[index - 1].as_bytes()))
                )
            };
            assert_eq!(event.prev, expected_prev, "{name}: hash chain");
            assert_eq!(event.seq, index as u64 + 1, "{name}: author sequence");
        }
        if scenario["normalize_events"] == true {
            actual.insert(
                path.file_name()
                    .expect("name")
                    .to_string_lossy()
                    .into_owned(),
                Value::Array(
                    lines
                        .iter()
                        .map(|line| Value::String(context.normalize_output(line)))
                        .collect(),
                ),
            );
        }
    }
    if scenario["normalize_events"] == true {
        let expected = scenario
            .get("journal")
            .cloned()
            .unwrap_or_else(|| json!({}));
        assert_eq!(
            Value::Object(actual),
            expected,
            "{name}: journal side effects"
        );
    }
}

#[test]
fn go_mcp_read_and_protocol_scenarios_match() {
    let steps = replay_scenarios("mcp-parity.json", PARITY_STEPS);
    assert!(steps > 100, "scenario replay must not be vacuous");
}

#[test]
fn go_mcp_mutation_scenarios_match_journal_side_effects() {
    let steps = replay_scenarios("mcp-mutations.json", MUTATION_STEPS);
    assert!(steps > 60, "scenario replay must not be vacuous");
}

#[test]
fn go_mcp_catalog_has_the_eight_room_tools_in_order_and_no_approval_tool() {
    let fixture: Value = serde_json::from_slice(
        &std::fs::read(repo_root().join("testdata/port/room/mcp-parity.json")).expect("fixture"),
    )
    .expect("valid fixture");
    let tools = fixture["cases"][0]["response"]["result"]["tools"]
        .as_array()
        .expect("tools");
    let names = tools
        .iter()
        .map(|tool| tool["name"].as_str().expect("name"))
        .collect::<Vec<_>>();
    assert_eq!(
        names,
        [
            "room_status",
            "room_journal_tail",
            "room_note_post",
            "room_artifact_list",
            "room_artifact_link",
            "room_run_request",
            "room_run_wait",
            "room_checkpoint_request",
        ]
    );
    assert!(!names.iter().any(|name| {
        ["approve", "grant", "deny", "resolve", "start"]
            .iter()
            .any(|word| name.contains(word))
    }));
}

// Go starts a goroutine per `tools/call`; the Rust server bounds the number of
// calls executing at once (`MAX_IN_FLIGHT_CALLS`, small in the test build) and
// stops reading while it is full, so a flood of long-running waits can never
// exhaust the OS thread limit (a panic-abort in release builds). Every call is
// still answered, in batches, and a later cheap request is not lost.
#[test]
fn tool_calls_in_flight_are_bounded_and_every_call_is_answered() {
    let room = fixture_room("in-flight-cap", &[]);
    let wait_seconds = 0.25;
    let calls = mcp::MAX_IN_FLIGHT_CALLS * 4;
    let mut input = Vec::new();
    for id in 1..=calls {
        let request = json!({
            "jsonrpc":"2.0",
            "id":id,
            "method":"tools/call",
            "params":{"name":"room_run_wait","arguments":{"run_id":"run_missing","timeout_seconds":wait_seconds}}
        });
        let body = serde_json::to_vec(&request).expect("wait request JSON");
        input.extend_from_slice(format!("Content-Length: {}\r\n\r\n", body.len()).as_bytes());
        input.extend_from_slice(&body);
    }
    let ping = serde_json::to_vec(&json!({"jsonrpc":"2.0","id":calls + 1,"method":"ping"}))
        .expect("ping JSON");
    input.extend_from_slice(format!("Content-Length: {}\r\n\r\n", ping.len()).as_bytes());
    input.extend_from_slice(&ping);

    let started = std::time::Instant::now();
    let mut output = Vec::new();
    mcp::serve_io_with_identity(
        BufReader::new(Cursor::new(input)),
        &mut output,
        &room,
        &room,
        None,
    )
    .expect("MCP serve");
    let elapsed = started.elapsed();

    let frames = decode_frames(&output);
    assert_eq!(frames.len(), calls + 1, "every request is answered");
    let ids = frames
        .iter()
        .map(|frame| frame["id"].as_u64().expect("numeric id"))
        .collect::<std::collections::BTreeSet<_>>();
    assert_eq!(ids, (1..=calls as u64 + 1).collect());
    for frame in &frames {
        if frame["id"].as_u64() == Some(calls as u64 + 1) {
            assert_eq!(frame["result"], json!({}));
        } else {
            assert_eq!(frame["result"]["isError"], true);
            assert_eq!(
                frame["result"]["content"][0]["text"],
                "wait timed out before approval decision"
            );
        }
    }
    // With the cap each full batch takes a whole wait: four batches of eight.
    // Unbounded, all calls would overlap and finish in about one wait.
    let minimum = std::time::Duration::from_secs_f64(wait_seconds * 3.0);
    assert!(
        elapsed >= minimum,
        "in-flight calls were not bounded: {calls} waits of {wait_seconds}s finished in {elapsed:?}"
    );
    let _ = std::fs::remove_dir_all(room);
}
