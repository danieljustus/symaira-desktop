#![deny(unsafe_code)]

// CK-MCP evidence: drive `symaira-core-mcp` (CoreKit's Rust port of the Go
// `mcpserver` package) with the raw bytes of the Go-recorded symdesk fixtures
// and compare its frames, stderr and exit status with the recorded Go output.
// Tool handlers are stubs: only the transport, framing and dispatch layers are
// under comparison. The report is written to `$CK_MCP_REPORT` when set.

#[path = "../../symdesk-protocol/tests/support/mcp_cases.rs"]
mod mcp_cases;

use std::{
    fs,
    io::{Cursor, Write},
    path::PathBuf,
    sync::{Arc, Mutex},
};

use serde_json::{Value, json};
use symaira_core_mcp::{Server, Tool, ToolAnnotations, ToolOutput};

/// Fixture cases whose frames, stderr or exit status differ between CoreKit's
/// `symaira-core-mcp` (rev 592bebb) and the Go-recorded fixture, in this
/// workspace's `serde_json` feature set (`preserve_order`, no
/// `arbitrary_precision`). The list is a tripwire: when CoreKit closes a gap
/// this test fails, and `docs/rust-port/corekit/mcp.md` must be re-evaluated.
const KNOWN_DIVERGENT: &[&str] = &[
    "deep-nesting-10000",
    "deep-nesting-20000",
    "deep-nesting-5000",
    "deep-nesting-9998",
    "deep-nesting-9999",
    "frame-body-length-chars-not-bytes",
    "frame-body-short",
    "frame-claim-limit-short-body",
    "frame-header-then-eof-after-blank",
    "frame-initialize",
    "frame-invalid-utf8-body",
    "frame-invalid-utf8-header",
    "frame-length-01",
    "frame-length-13",
    "frame-length-14",
    "frame-three-notifications-then-init",
    "frame-two",
    "init-id-00",
    "init-id-01",
    "init-id-02",
    "init-id-03",
    "init-id-04",
    "init-id-05",
    "init-id-06",
    "init-id-07",
    "init-id-08",
    "init-id-09",
    "init-id-16",
    "init-id-17",
    "init-id-18",
    "init-id-19",
    "init-id-20",
    "init-jsonrpc-05",
    "init-params-00",
    "init-params-02",
    "init-params-03",
    "init-params-07",
    "init-params-08",
    "init-params-09",
    "init-params-10",
    "init-params-11",
    "init-params-12",
    "init-params-13",
    "init-read-write",
    "init-read-write-framed",
    "invalid-20",
    "invalid-22",
    "invalid-26",
    "invalid-28",
    "invalid-31",
    "invalid-32",
    "invalid-33",
    "line-five-mixed",
    "line-nul",
    "line-two-requests",
    "method-id-00",
    "method-id-17",
    "method-null-id-00",
    "method-null-id-17",
    "ping-id-07",
    "ping-id-18",
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

/// The symdesk tool inventory of the Go oracle. Read-only mode exposes exactly
/// the tools flagged `read_only`; `only` pins an explicit recorded list.
fn symdesk_server(version: &str, only: Option<&[String]>, allow_write: bool) -> Server {
    let tools: Value = serde_json::from_slice(
        &fs::read(root().join("testdata/port/mcp/symdesk-tools.json")).expect("tools fixture"),
    )
    .expect("tools json");
    let server = Server::new("symdesk", version);
    let mut listed: Vec<&Value> = tools["tools"].as_array().expect("tools").iter().collect();
    if let Some(only) = only {
        listed = only
            .iter()
            .map(|name| {
                listed
                    .iter()
                    .copied()
                    .find(|tool| tool["name"] == name.as_str())
                    .expect("listed tool is in the tool fixture")
            })
            .collect();
    }
    if only.is_none() && !allow_write {
        listed.retain(|tool| tool["read_only"].as_bool().unwrap_or(false));
    }
    for tool in listed {
        let annotations = ToolAnnotations {
            read_only_hint: tool["read_only"].as_bool().unwrap_or(false),
            destructive_hint: tool["destructive"].as_bool().unwrap_or(false),
            ..ToolAnnotations::default()
        };
        server
            .register_tool(
                Tool::new(
                    tool["name"].as_str().expect("name"),
                    tool["description"].as_str().expect("description"),
                )
                .schema(tool["input_schema"].clone())
                .annotations(annotations)
                .handler(|_, _| Ok(ToolOutput::from("stub"))),
            )
            .expect("register");
    }
    server
}

const SERVER_INFO: &str = r#""serverInfo":{"name":"symdesk","version":""#;

fn normalize_version(text: &str) -> String {
    let mut out = String::new();
    let mut rest = text;
    while let Some(at) = rest.find(SERVER_INFO) {
        let start = at + SERVER_INFO.len();
        out.push_str(&rest[..start]);
        let end = rest[start..].find('"').expect("closing quote") + start;
        out.push_str("<VERSION>");
        rest = &rest[end..];
    }
    out.push_str(rest);
    out
}

fn normalize_stdout(text: &str) -> String {
    let mut out = String::new();
    let mut rest = text;
    while let Some(after) = rest.strip_prefix("Content-Length: ") {
        let Some((length, body_and_rest)) = after.split_once("\r\n\r\n") else {
            break;
        };
        let Ok(length) = length.parse::<usize>() else {
            break;
        };
        if length > body_and_rest.len() || !body_and_rest.is_char_boundary(length) {
            break;
        }
        let body = normalize_version(&body_and_rest[..length]);
        out.push_str(&format!("Content-Length: {}\r\n\r\n{body}", body.len()));
        rest = &body_and_rest[length..];
    }
    out.push_str(&normalize_version(rest));
    out
}

fn run_core(server: &Server, input: Vec<u8>) -> (Vec<u8>, String, i32) {
    let sink = Sink::default();
    let result = server.serve_io(Cursor::new(input), sink.clone());
    let stdout = sink.0.lock().expect("sink").clone();
    match result {
        Ok(()) => (stdout, String::new(), 0),
        Err(error) => (stdout, format!("{error}\n"), 1),
    }
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

#[test]
fn core_mcp_protocol_transcripts_vs_go() {
    let fixture = mcp_cases::load_fixture();
    let mut diffs = Vec::new();
    let mut executed = 0;
    for case in &fixture.cases {
        let server = symdesk_server("1.2.3", None, case.allow_write);
        let (stdout, stderr, code) = run_core(&server, mcp_cases::input(case));
        executed += 1;
        let actual = normalize_stdout(&String::from_utf8_lossy(&stdout));
        let expected = normalize_stdout(&case.stdout);
        if actual != expected || stderr != case.stderr || code != case.exit_code {
            diffs.push(json!({
                "case": case.id,
                "go_stdout": clip(&expected),
                "core_stdout": clip(&actual),
                "go_stderr": case.stderr,
                "core_stderr": stderr,
                "go_exit": case.exit_code,
                "core_exit": code,
            }));
        }
    }
    let report = json!({"executed": executed, "diff_count": diffs.len(), "diffs": diffs});
    if let Ok(path) = std::env::var("CK_MCP_REPORT") {
        fs::write(path, serde_json::to_vec_pretty(&report).expect("report")).expect("write");
    }
    eprintln!("protocol executed={executed} diffs={}", diffs.len());
    assert_eq!(executed, 334, "the whole Go transcript fixture is replayed");
    let mut divergent: Vec<String> = diffs
        .iter()
        .map(|diff| diff["case"].as_str().expect("case").to_owned())
        .collect();
    divergent.sort();
    assert_eq!(divergent, KNOWN_DIVERGENT, "divergence set changed");
}

#[test]
fn core_mcp_tools_list_vs_go() {
    let fixture: Value = serde_json::from_slice(
        &fs::read(root().join("testdata/port/mcp/tools-process.json")).expect("tools-process"),
    )
    .expect("json");
    let mut diffs = Vec::new();
    let mut executed = 0;
    for case in fixture["cases"].as_array().expect("cases") {
        let expected = case["stdout"].as_str().expect("stdout");
        let body = expected
            .split_once("\r\n\r\n")
            .map_or(expected, |(_, body)| body);
        let listing: Value = serde_json::from_str(body).expect("recorded tools/list frame");
        let names: Vec<String> = listing["result"]["tools"]
            .as_array()
            .expect("recorded tools")
            .iter()
            .map(|tool| tool["name"].as_str().expect("name").to_owned())
            .collect();
        let server = symdesk_server("1.2.3", Some(&names), true);
        let request = case["request"].as_str().expect("request");
        let input = if case["framed"] == true {
            mcp_cases::frame(request).into_bytes()
        } else {
            format!("{request}\n").into_bytes()
        };
        let (stdout, stderr, _code) = run_core(&server, input);
        executed += 1;
        let actual = String::from_utf8_lossy(&stdout).into_owned();
        if actual != expected || stderr != case["stderr"].as_str().unwrap_or("") {
            let at = actual
                .bytes()
                .zip(expected.bytes())
                .position(|(a, b)| a != b)
                .unwrap_or(0);
            let from = at.saturating_sub(60);
            diffs.push(json!({
                "case": case["id"], "listed_tools": names.len(), "first_diff_byte": at,
                "go": expected.get(from..(at + 80).min(expected.len())),
                "core": actual.get(from..(at + 80).min(actual.len())),
            }));
        }
    }
    eprintln!("tools executed={executed} diffs={}", diffs.len());
    assert_eq!(executed, 4);
    // Object keys come out in insertion order under `preserve_order`, Go sorts
    // them: every recorded tools/list differs at the first nested schema.
    assert_eq!(diffs.len(), 4);
    if let Ok(path) = std::env::var("CK_MCP_REPORT_TOOLS") {
        fs::write(
            path,
            serde_json::to_vec_pretty(&json!({"executed": executed, "diffs": diffs})).expect("r"),
        )
        .expect("write");
    }
}
