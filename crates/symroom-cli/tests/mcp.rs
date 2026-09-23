#[path = "../src/mcp.rs"]
mod mcp;

use std::{
    io::{BufReader, Cursor, Write},
    path::PathBuf,
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
    mcp::serve_io_with_artifact_root(
        BufReader::new(Cursor::new(input)),
        &mut output,
        &room,
        &room,
    )
    .expect("MCP serve");
    let actual = decode_frames(&output);
    let expected = cases
        .iter()
        .map(|case| case["response"].clone())
        .collect::<Vec<_>>();
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
    let mut input = Vec::new();
    for case in cases {
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
    }
    let mut output = Vec::new();
    mcp::serve_io_with_identity(
        BufReader::new(Cursor::new(input)),
        &mut output,
        &room,
        &artifact_root,
        Some(&identity),
    )
    .expect("MCP serve");
    let actual = decode_frames(&output);
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
        event.as_object_mut().unwrap().remove("ts");
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
    mcp::serve_io_with_artifact_root(
        BufReader::new(Cursor::new(input)),
        &mut output,
        &room,
        &room,
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
    let mut child = Command::new(env!("CARGO_BIN_EXE_symroom"))
        .arg("mcp")
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
    assert!(output.status.success(), "stderr: {:?}", output.stderr);
    assert_eq!(
        decode_frames(&output.stdout),
        vec![fixture["cases"][0]["response"].clone()]
    );
}

#[test]
fn mcp_cli_help_exits_successfully() {
    let args = [std::ffi::OsString::from("--help")];
    let _ = mcp::run_cli(&args);
}
