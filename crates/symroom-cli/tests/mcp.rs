#[path = "../src/mcp.rs"]
mod mcp;

use std::{
    io::{BufReader, Cursor},
    path::PathBuf,
};

use serde_json::{Value, json};

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
    let room = temporary_room("oracle");
    let mut output = Vec::new();
    mcp::serve_io(BufReader::new(Cursor::new(input)), &mut output, &room).expect("MCP serve");
    let actual = decode_frames(&output);
    let expected = cases
        .iter()
        .map(|case| case["response"].clone())
        .collect::<Vec<_>>();
    assert_eq!(actual, expected);
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
    mcp::serve_io(BufReader::new(Cursor::new(input)), &mut output, &room).expect("MCP serve");
    let response = decode_frames(&output).remove(0);
    assert_eq!(response["error"]["code"], -32601);
    assert_eq!(response["error"]["message"], "Unknown tool: room_approve");
    let _ = std::fs::remove_dir_all(room);
}
