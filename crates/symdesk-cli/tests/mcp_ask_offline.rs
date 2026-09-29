#![deny(unsafe_code)]

use std::{
    fs,
    io::Write,
    path::PathBuf,
    process::{Command, Stdio},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use serde_json::Value;
use symdesk_index::Sidecar;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<FixtureCase>,
}

#[derive(Deserialize)]
struct FixtureCase {
    id: String,
    documents: Vec<FixtureDocument>,
    expected_tool: Value,
    calls: Vec<FixtureCall>,
    provider_requests: Vec<Value>,
}

#[derive(Deserialize)]
struct FixtureCall {
    id: String,
    arguments_json: String,
    expected: Value,
}

#[derive(Deserialize)]
struct FixtureDocument {
    path: String,
    body: String,
}

struct TestRoot(PathBuf);

impl TestRoot {
    fn new(id: &str) -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-mcp-ask-{id}-{}-{nonce}",
            std::process::id()
        ));
        for child in ["home/config", "home/cache", "data", "tmp", "cwd", "vault"] {
            fs::create_dir_all(root.join(child)).expect("create isolated test directory");
        }
        Self(root)
    }

    fn path(&self, child: &str) -> PathBuf {
        self.0.join(child)
    }
}

impl Drop for TestRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

#[test]
fn real_mcp_ask_replays_go_handler_envelope() {
    let fixture_path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/mcp/ask-offline.json");
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(fixture_path).expect("Go-generated offline Ask MCP fixture"),
    )
    .expect("decode offline Ask MCP fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 1);
    for case in &fixture.cases {
        replay_case(case);
    }
}

fn replay_case(case: &FixtureCase) {
    let root = TestRoot::new(&case.id);
    let vault = root.path("vault").canonicalize().expect("canonical vault");
    for document in &case.documents {
        let path = vault.join(&document.path);
        fs::create_dir_all(path.parent().expect("document parent"))
            .expect("create document parent");
        fs::write(path, &document.body).expect("write source-controlled test document");
    }

    let sidecar_path = root.path("data/sidecar.db");
    let mut sidecar = Sidecar::open(&sidecar_path).expect("open isolated sidecar");
    sidecar
        .refresh_index_for_cli(&vault)
        .expect("index test vault for scoped FTS");
    drop(sidecar);

    let mut requests = vec![r#"{"jsonrpc":"2.0","id":1,"method":"tools/list"}"#.to_owned()];
    for (index, call) in case.calls.iter().enumerate() {
        requests.push(call_request(index + 2, &call.arguments_json));
    }
    let frames = run_mcp(&root, &vault, &sidecar_path, &requests, None);
    assert_eq!(
        frames.len(),
        case.calls.len() + 1,
        "{} returned unexpected MCP frames",
        case.id
    );
    let by_id = frames
        .iter()
        .filter_map(|frame| frame["id"].as_u64().map(|id| (id, frame)))
        .collect::<std::collections::BTreeMap<_, _>>();
    let list_response = by_id.get(&1).expect("tools/list response id");
    let tools = list_response["result"]["tools"]
        .as_array()
        .expect("tools/list array");
    let ask_tool = tools
        .iter()
        .find(|tool| tool["name"] == "desk_ask")
        .expect("desk_ask catalog entry");
    assert_eq!(
        normalize_value(ask_tool.clone(), &vault),
        case.expected_tool
    );
    for (index, call) in case.calls.iter().enumerate() {
        let response = normalize_value(
            (*by_id
                .get(&((index + 2) as u64))
                .expect("tools/call response id"))
            .clone(),
            &vault,
        );
        match call.id.as_str() {
            "duplicate-folded-query-last-valid" | "duplicate-folded-query-last-empty" => {
                assert!(
                    response["result"]["isError"] == true,
                    "{}: {response}",
                    call.id
                );
                assert_eq!(
                    response["result"]["content"][0]["text"],
                    "ambiguous query arguments"
                );
            }
            "notebook-kelvin-case" => {
                assert!(
                    response["result"]["isError"] == true,
                    "{}: {response}",
                    call.id
                );
                assert_eq!(
                    response["result"]["content"][0]["text"],
                    "notebook-scoped desk_ask is not implemented by the Rust MCP port"
                );
            }
            _ => assert_eq!(response, call.expected, "{}", call.id),
        }
    }
    assert!(
        case.provider_requests.is_empty(),
        "offline Ask fixture must not make provider calls"
    );

    let notebook_frames = run_mcp(
        &root,
        &vault,
        &sidecar_path,
        &[call_request(
            100,
            r#"{"query":"tag:askscope","notebook":"notebook-fixture"}"#,
        )],
        None,
    );
    assert!(notebook_frames[0]["result"]["isError"] == true);
    assert_eq!(
        notebook_frames[0]["result"]["content"][0]["text"],
        "notebook-scoped desk_ask is not implemented by the Rust MCP port"
    );

    let configured_provider_frames = run_mcp(
        &root,
        &vault,
        &sidecar_path,
        &[call_request(101, r#"{"query":"tag:askscope"}"#)],
        Some("llm_provider = \"openai\"\n"),
    );
    assert!(configured_provider_frames[0]["result"]["isError"] == true);
    assert_eq!(
        configured_provider_frames[0]["result"]["content"][0]["text"],
        "Rust ask provider \"openai\" is not implemented"
    );
}

fn run_mcp(
    root: &TestRoot,
    vault: &std::path::Path,
    sidecar: &std::path::Path,
    requests: &[String],
    config: Option<&str>,
) -> Vec<Value> {
    let config_dir = root.path("home/config/symdesk");
    fs::create_dir_all(&config_dir).expect("create private config directory");
    if let Some(config) = config {
        fs::write(config_dir.join("config.toml"), config).expect("write controlled AI config");
    }
    let mut child = Command::new(env!("CARGO_BIN_EXE_symdesk"))
        .arg("mcp")
        .env_clear()
        .env("PATH", std::env::var("PATH").unwrap_or_default())
        .env("HOME", root.path("home"))
        .env("USERPROFILE", root.path("home"))
        .env("XDG_CONFIG_HOME", root.path("home/config"))
        .env("XDG_CACHE_HOME", root.path("home/cache"))
        .env("XDG_DATA_HOME", root.path("data"))
        .env("TMPDIR", root.path("tmp"))
        .env("TMP", root.path("tmp"))
        .env("TEMP", root.path("tmp"))
        .env("SYMDESK_VAULT", vault)
        .env("SYMDESK_SIDECAR", sidecar)
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC")
        .current_dir(root.path("cwd"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .expect("launch symdesk MCP process");
    let input = requests.join("\n") + "\n";
    child
        .stdin
        .take()
        .expect("MCP stdin")
        .write_all(input.as_bytes())
        .expect("send MCP requests");
    let output = child.wait_with_output().expect("wait for MCP process");
    assert!(
        output.status.success(),
        "symdesk MCP failed: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    serde_json::Deserializer::from_slice(&output.stdout)
        .into_iter::<Value>()
        .map(|frame| frame.expect("decode MCP response frame"))
        .collect()
}

fn call_request(id: usize, arguments_json: &str) -> String {
    format!(
        r#"{{"jsonrpc":"2.0","id":{id},"method":"tools/call","params":{{"name":"desk_ask","arguments":{arguments_json}}}}}"#
    )
}

fn normalize_value(mut value: Value, vault: &std::path::Path) -> Value {
    if let Some(text) = value
        .pointer("/result/content/0/text")
        .and_then(Value::as_str)
        .map(str::to_owned)
    {
        *value
            .pointer_mut("/result/content/0/text")
            .expect("text field") =
            Value::String(text.replace(&vault.to_string_lossy().to_string(), "$VAULT"));
    }
    value
}
