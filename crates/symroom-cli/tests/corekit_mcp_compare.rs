#![deny(unsafe_code)]

// CK-MCP evidence: drive `symaira-core-mcp` (CoreKit's Rust port of the Go
// `mcpserver` package) with the raw stream bytes of the Go-recorded symroom
// scenarios and compare the produced frames with the recorded Go output.
// Tool handlers replay the recorded tool result text (or a stub), so only the
// transport, framing, validation and encoding layers are under comparison.
// The report is written to `$CK_MCP_REPORT` when set.

use std::{
    fs,
    io::{Cursor, Write},
    path::PathBuf,
    sync::{Arc, Mutex},
};

use serde_json::{Value, json};
use symaira_core_mcp::{Server, Tool, ToolError, ToolOutput};

/// Fixture cases whose frames, stderr or exit status differ between CoreKit's
/// `symaira-core-mcp` (rev 592bebb) and the Go-recorded fixture, in this
/// workspace's `serde_json` feature set (`preserve_order`, no
/// `arbitrary_precision`). The list is a tripwire: when CoreKit closes a gap
/// this test fails, and `docs/rust-port/corekit/mcp.md` must be re-evaluated.
const KNOWN_DIVERGENT: &[&str] = &[
    "mcp-mutations.json:mutation_json_layer_streams/checkpoint_invalid_utf8_question",
    "mcp-mutations.json:mutation_json_layer_streams/checkpoint_lone_surrogate_run_id",
    "mcp-mutations.json:mutation_json_layer_streams/note_invalid_utf8_text",
    "mcp-mutations.json:mutation_json_layer_streams/note_lone_surrogate_text",
    "mcp-mutations.json:mutation_json_layer_streams/note_with_nesting_200_in_unused_argument",
    "mcp-mutations.json:mutation_json_layer_streams/run_request_with_nesting_200_in_unused_argument",
    "mcp-parity.json:json_layer_streams/error_invalid_content_length_invalid_utf8_first_header",
    "mcp-parity.json:json_layer_streams/error_invalid_content_length_invalid_utf8_later_header",
    "mcp-parity.json:json_layer_streams/high_surrogate_then_high_surrogate",
    "mcp-parity.json:json_layer_streams/high_surrogate_then_non_low",
    "mcp-parity.json:json_layer_streams/invalid_utf8_id_string",
    "mcp-parity.json:json_layer_streams/invalid_utf8_method_framed",
    "mcp-parity.json:json_layer_streams/invalid_utf8_tool_argument_ignored",
    "mcp-parity.json:json_layer_streams/invalid_utf8_tool_name",
    "mcp-parity.json:json_layer_streams/invalid_utf8_truncated_sequence_method",
    "mcp-parity.json:json_layer_streams/limit_overflowing_float",
    "mcp-parity.json:json_layer_streams/lone_high_surrogate_id",
    "mcp-parity.json:json_layer_streams/lone_low_surrogate_id",
    "mcp-parity.json:json_layer_streams/lone_surrogate_method",
    "mcp-parity.json:json_layer_streams/lone_surrogate_tool_name",
    "mcp-parity.json:json_layer_streams/nesting_200_in_id",
    "mcp-parity.json:json_layer_streams/nesting_200_in_meta",
    "mcp-parity.json:json_layer_streams/nesting_200_in_tool_arguments",
    "mcp-parity.json:json_layer_streams/nesting_200_in_typed_argument",
    "mcp-parity.json:json_layer_streams/nesting_200_in_unused_member",
    "mcp-parity.json:json_layer_streams/nesting_exactly_at_limit_10000",
    "mcp-parity.json:json_layer_streams/nesting_over_limit_10001",
    "mcp-parity.json:json_layer_streams/nesting_over_limit_line_mode",
    "mcp-parity.json:json_layer_streams/timeout_seconds_overflowing_float",
    "mcp-parity.json:protocol_requests/call_meta_string",
    "mcp-parity.json:protocol_requests/call_name_null",
    "mcp-parity.json:protocol_requests/call_name_object",
    "mcp-parity.json:protocol_requests/call_name_upper_key",
    "mcp-parity.json:protocol_requests/mixed_case_keys",
    "mcp-parity.json:protocol_requests/uppercase_keys",
    "mcp-parity.json:protocol_streams/eof_lets_inflight_wait_finish",
    "mcp-parity.json:protocol_streams/error_empty_body_eof",
    "mcp-parity.json:protocol_streams/error_negative_content_length",
    "mcp-parity.json:protocol_streams/error_truncated_body",
    "mcp-parity.json:protocol_streams/error_truncated_body_after_valid_frame",
    "mcp-parity.json:protocol_streams/fatal_read_error_cancels_inflight_wait",
    "mcp-parity.json:protocol_streams/initialize_ping_list_framed",
    "mcp-parity.json:protocol_streams/invalid_request_waits_for_inflight_tool",
    "mcp-parity.json:protocol_streams/line_mode",
    "mcp-parity.json:protocol_streams/parse_error_waits_for_inflight_tool",
    "mcp-parity.json:protocol_streams/parse_errors_framed",
    "mcp-parity.json:protocol_streams/parse_errors_line_mode",
    "mcp-parity.json:protocol_streams/request_ids_preserved",
    "mcp-parity.json:protocol_streams/sync_response_not_blocked_by_inflight_tool",
];

#[derive(Clone, Default)]
struct Sink(Arc<Mutex<Vec<u8>>>);

impl Write for Sink {
    fn write(&mut self, buf: &[u8]) -> std::io::Result<usize> {
        self.0.lock().expect("sink").extend_from_slice(buf);
        Ok(buf.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

fn root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

/// First tool result `(text, is_error)` found in recorded Go output bytes.
fn canned(expected: &str) -> Option<(String, bool)> {
    let mut rest = expected;
    let mut bodies: Vec<&str> = Vec::new();
    loop {
        rest = rest.trim_start_matches(['\r', '\n']);
        if let Some(after) = rest.strip_prefix("Content-Length: ")
            && let Some((length, tail)) = after.split_once("\r\n\r\n")
            && let Ok(length) = length.parse::<usize>()
            && length <= tail.len()
            && tail.is_char_boundary(length)
        {
            bodies.push(&tail[..length]);
            rest = &tail[length..];
            continue;
        }
        if let Some((line, tail)) = rest.split_once('\n') {
            bodies.push(line);
            rest = tail;
            continue;
        }
        if !rest.is_empty() {
            bodies.push(rest);
        }
        break;
    }
    bodies.iter().find_map(|body| {
        let value: Value = serde_json::from_str(body).ok()?;
        let text = value["result"]["content"][0]["text"].as_str()?.to_owned();
        Some((text, value["result"]["isError"] == true))
    })
}

fn symroom_server(answer: Option<(String, bool)>) -> Server {
    let tools: Value = serde_json::from_slice(
        &fs::read(root().join("testdata/port/mcp/symroom-tools.json")).expect("tools fixture"),
    )
    .expect("tools json");
    let server = Server::new("symroom", "0.1.0");
    server.set_instructions(tools["instructions"].as_str().expect("instructions"));
    let answer = Arc::new(answer);
    for tool in tools["tools"].as_array().expect("tools") {
        let answer = Arc::clone(&answer);
        server
            .register_tool(
                Tool::new(
                    tool["name"].as_str().expect("name"),
                    tool["description"].as_str().expect("description"),
                )
                .schema(tool["input_schema"].clone())
                .handler(move |_, _| match answer.as_ref() {
                    Some((text, true)) => Err(Box::new(ToolError::new(text.clone()))),
                    Some((text, false)) => Ok(ToolOutput::from(text.clone())),
                    None => Ok(ToolOutput::from("stub")),
                }),
            )
            .expect("register");
    }
    server
}

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
                .map(|index| u8::from_str_radix(&after[index..index + 2], 16).expect("hex")),
        );
        rest = &after[end + 2..];
    }
    out.extend_from_slice(rest.as_bytes());
    out
}

fn clip(text: &str) -> String {
    if text.len() > 300 {
        let mut end = 300;
        while !text.is_char_boundary(end) {
            end -= 1;
        }
        format!("{}...({} bytes)", &text[..end], text.len())
    } else {
        text.to_owned()
    }
}

fn decode_frames(mut bytes: &[u8]) -> Vec<Value> {
    let mut values = Vec::new();
    while !bytes.is_empty() {
        let Some(end) = bytes.windows(4).position(|w| w == b"\r\n\r\n") else {
            break;
        };
        let header = std::str::from_utf8(&bytes[..end]).expect("header");
        let length: usize = header
            .strip_prefix("Content-Length: ")
            .expect("length header")
            .parse()
            .expect("length");
        let start = end + 4;
        values.push(serde_json::from_slice(&bytes[start..start + length]).expect("JSON"));
        bytes = &bytes[start + length..];
    }
    values
}

fn run_core(server: &Server, input: Vec<u8>) -> (String, String) {
    let sink = Sink::default();
    let result = server.serve_io(Cursor::new(input), sink.clone());
    let stdout = sink.0.lock().expect("sink").clone();
    (
        String::from_utf8_lossy(&stdout).into_owned(),
        result
            .err()
            .map(|error| error.to_string())
            .unwrap_or_default(),
    )
}

#[test]
fn core_mcp_symroom_scenarios_vs_go() {
    let mut diffs = Vec::new();
    let mut executed = 0usize;
    let mut skipped = 0usize;
    for name in ["mcp-parity.json", "mcp-mutations.json"] {
        let fixture: Value = serde_json::from_slice(
            &fs::read(root().join("testdata/port/room").join(name)).expect("fixture"),
        )
        .expect("json");
        for scenario in fixture["scenarios"].as_array().expect("scenarios") {
            for step in scenario["steps"].as_array().expect("steps") {
                let label = format!(
                    "{name}:{}/{}",
                    scenario["name"].as_str().unwrap_or("?"),
                    step["name"].as_str().unwrap_or("?")
                );
                // A product handler that panicked in Go (nil identity) is a
                // tool-side outcome, not a transport one: a stub cannot replay it.
                if step
                    .to_string()
                    .contains("Internal error: handler panicked")
                {
                    skipped += 1;
                    continue;
                }
                executed += 1;
                if let Some(input) = step["input"].as_str() {
                    let expected = step["output"].as_str().expect("output");
                    let answer = canned(expected);
                    let server = symroom_server(answer);
                    let (stdout, error) = run_core(&server, expand_hex(&expand_repeat(input)));
                    let expected_error = step["error"].as_str().unwrap_or("");
                    if stdout != expected || error != expected_error {
                        diffs.push(json!({
                            "case": label, "kind": "stream",
                            "go_stdout": clip(expected), "core_stdout": clip(&stdout),
                            "go_error": expected_error, "core_error": error,
                        }));
                    }
                } else {
                    let request = &step["request"];
                    let body = serde_json::to_vec(request).expect("request");
                    let mut input = format!("Content-Length: {}\r\n\r\n", body.len()).into_bytes();
                    input.extend_from_slice(&body);
                    let expected = step["response"].clone();
                    let answer = expected["result"]["content"][0]["text"]
                        .as_str()
                        .map(|text| (text.to_owned(), expected["result"]["isError"] == true));
                    let server = symroom_server(answer);
                    let (stdout, error) = run_core(&server, input);
                    let frames = decode_frames(stdout.as_bytes());
                    let actual = frames.first().cloned().unwrap_or(Value::Null);
                    // Tool inventory frames carry the oracle's own bytes; the
                    // semantic value is compared here, bytes in the process cases.
                    if actual != expected || !error.is_empty() {
                        diffs.push(json!({
                            "case": label, "kind": "request",
                            "go": clip(&expected.to_string()), "core": clip(&actual.to_string()),
                            "core_error": error,
                        }));
                    }
                }
            }
        }
    }
    let report = json!({"executed": executed, "skipped": skipped, "diff_count": diffs.len(), "diffs": diffs});
    if let Ok(path) = std::env::var("CK_MCP_REPORT") {
        fs::write(path, serde_json::to_vec_pretty(&report).expect("report")).expect("write");
    }
    eprintln!("symroom executed={executed} diffs={}", diffs.len());
    assert_eq!((executed, skipped), (341, 5), "recorded steps replayed");
    let mut divergent: Vec<String> = diffs
        .iter()
        .map(|diff| diff["case"].as_str().expect("case").to_owned())
        .collect();
    divergent.sort();
    assert_eq!(divergent, KNOWN_DIVERGENT, "divergence set changed");
}

/// Go starts one goroutine per `tools/call`, so N concurrent waits finish in
/// about one wait period (frozen: `six-hundred-concurrent-waits-answer-ping-first`
/// takes 2.9 to 5 s for 600 waits of 3 s). CoreKit's crate caps concurrent
/// handlers at 16 and blocks the reader while the cap is full.
#[test]
fn core_mcp_concurrency_cap_vs_go_unbounded_waits() {
    const CALLS: usize = 160;
    let wait = std::time::Duration::from_millis(300);
    let server = Server::new("symroom", "0.1.0");
    server
        .register_tool(
            Tool::new("room_run_wait", "wait")
                .schema(json!({"type": "object", "properties": {}}))
                .handler(move |_, _| {
                    std::thread::sleep(wait);
                    Ok(ToolOutput::from("done"))
                }),
        )
        .expect("register");
    let mut input = String::new();
    for id in 1..=CALLS {
        let body = json!({"jsonrpc":"2.0","id":id,"method":"tools/call",
            "params":{"name":"room_run_wait","arguments":{}}})
        .to_string();
        input.push_str(&format!("{body}\n"));
    }
    input.push_str(&format!(
        "{}\n",
        json!({"jsonrpc":"2.0","id":CALLS + 1,"method":"ping"})
    ));
    let started = std::time::Instant::now();
    let (stdout, error) = run_core(&server, input.into_bytes());
    let elapsed = started.elapsed();
    let ids: Vec<u64> = stdout
        .lines()
        .map(|line| {
            serde_json::from_str::<Value>(line).expect("line")["id"]
                .as_u64()
                .expect("id")
        })
        .collect();
    let ping_position = ids.iter().position(|id| *id == CALLS as u64 + 1);
    eprintln!(
        "concurrency calls={CALLS} wait_ms=300 elapsed_ms={} ping_position={ping_position:?} frames={} error={error:?}",
        elapsed.as_millis(),
        ids.len()
    );
    assert_eq!(ids.len(), CALLS + 1, "every request is answered");
    // Go answers the ping first and finishes all waits in about one period.
    // The capped CoreKit dispatcher needs at least CALLS / 16 periods and
    // answers the ping only after the reader got past the cap.
    assert!(elapsed >= wait * 8, "cap not observed: {elapsed:?}");
    assert!(ping_position.expect("ping answered") > CALLS / 2);
}
