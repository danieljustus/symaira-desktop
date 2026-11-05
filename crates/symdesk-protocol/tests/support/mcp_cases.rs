// Shared by the symdesk-protocol and symdesk-cli MCP-001 replays: decoding of
// `testdata/port/mcp/protocol-process.json` and regeneration of its large
// inputs from their (kind, n) description.
#![allow(dead_code)]

use serde::Deserialize;

#[derive(Deserialize)]
pub struct Fixture {
    pub schema_version: u32,
    pub cases: Vec<Case>,
}

#[derive(Deserialize)]
pub struct Generate {
    pub kind: String,
    pub n: usize,
}

#[derive(Deserialize)]
pub struct Case {
    pub id: String,
    #[serde(default)]
    pub allow_write: bool,
    pub input: String,
    #[serde(default)]
    pub input_hex: String,
    #[serde(default)]
    pub generate: Option<Generate>,
    pub stdout: String,
    pub stderr: String,
    pub exit_code: i32,
}

pub const PING: &str = r#"{"jsonrpc":"2.0","id":1,"method":"ping"}"#;

pub fn frame(body: &str) -> String {
    format!("Content-Length: {}\r\n\r\n{body}", body.len())
}

fn pad(total: usize) -> String {
    let prefix = r#"{"jsonrpc":"2.0","id":1,"method":"ping","params":{"pad":""#;
    let suffix = r#""}}"#;
    format!(
        "{prefix}{}{suffix}",
        "a".repeat(total - prefix.len() - suffix.len())
    )
}

fn generated(generate: &Generate) -> Vec<u8> {
    match generate.kind.as_str() {
        "pad-line" => format!("{}\n", pad(generate.n)).into_bytes(),
        "pad-frame" => frame(&pad(generate.n)).into_bytes(),
        "garbage-line" => format!("{}\n", "x".repeat(generate.n)).into_bytes(),
        "garbage-line-then-ping" => format!("{}\n{PING}\n", "x".repeat(generate.n)).into_bytes(),
        "oversized-frame-then-ping" => format!(
            "Content-Length: {}\r\n\r\n{}{}",
            generate.n,
            "x".repeat(generate.n),
            frame(PING)
        )
        .into_bytes(),
        "deep-line" => format!(
            "{}{}{}}}\n",
            r#"{"jsonrpc":"2.0","id":1,"method":"ping","params":"#,
            "[".repeat(generate.n),
            "]".repeat(generate.n)
        )
        .into_bytes(),
        other => panic!("unknown generator {other}"),
    }
}

fn decode_hex(text: &str) -> Vec<u8> {
    (0..text.len() / 2)
        .map(|i| u8::from_str_radix(&text[2 * i..2 * i + 2], 16).unwrap())
        .collect()
}

pub fn input(case: &Case) -> Vec<u8> {
    if let Some(generate) = &case.generate {
        generated(generate)
    } else if !case.input_hex.is_empty() {
        decode_hex(&case.input_hex)
    } else {
        case.input.clone().into_bytes()
    }
}

pub fn load_fixture() -> Fixture {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/mcp/protocol-process.json");
    serde_json::from_slice(&std::fs::read(path).expect("Go protocol transcript fixture"))
        .expect("decode Go protocol transcript")
}
