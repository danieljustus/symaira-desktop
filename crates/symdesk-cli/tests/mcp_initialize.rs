#![deny(unsafe_code)]

use std::{
    fs,
    path::Path,
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use serde_json::Value;

#[derive(Deserialize)]
struct Fixture {
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    request: String,
}

#[test]
fn mcp001_initialize_replays_generated_cases() {
    let fixture_path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/mcp/representative.json");
    let fixture: Fixture = serde_json::from_slice(&fs::read(fixture_path).expect("Go fixture"))
        .expect("valid Go fixture");
    let root = std::env::temp_dir().join(format!(
        "symdesk-mcp001-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system time")
            .as_nanos()
    ));
    for path in ["home", "vault", "tmp"] {
        fs::create_dir_all(root.join(path)).expect("create isolated test directory");
    }

    let mut seen = 0;
    for case in fixture
        .cases
        .iter()
        .filter(|case| case.id.starts_with("mcp001-"))
    {
        seen += 1;
        let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        command.args(["mcp"]).env_clear();
        for (name, value) in [
            ("HOME", root.join("home")),
            ("USERPROFILE", root.join("home")),
            ("TMPDIR", root.join("tmp")),
            ("TMP", root.join("tmp")),
            ("TEMP", root.join("tmp")),
            ("SYMDESK_VAULT", root.join("vault")),
        ] {
            command.env(name, value);
        }
        for name in ["PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"] {
            if let Some(value) = std::env::var_os(name) {
                command.env(name, value);
            }
        }
        let mut child = command
            .stdin(std::process::Stdio::piped())
            .stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped())
            .spawn()
            .expect("launch symdesk MCP");
        use std::io::Write;
        child
            .stdin
            .take()
            .expect("stdin pipe")
            .write_all(format!("{}\n", case.request).as_bytes())
            .expect("write MCP request");
        let output = child.wait_with_output().expect("wait for symdesk");
        assert!(output.status.success(), "{}: {:?}", case.id, output.stderr);
        match case.id.as_str() {
            "mcp001-initialize-string-id" | "mcp001-initialize-null-id" => {
                let response: Value = serde_json::from_slice(&output.stdout).expect("response");
                let expected_id = if case.id.ends_with("null-id") {
                    Value::Null
                } else {
                    Value::String("init".to_owned())
                };
                assert_eq!(response["id"], expected_id, "{}", case.id);
                assert_eq!(response["result"]["protocolVersion"], "2024-11-05");
                assert_eq!(response["result"]["capabilities"]["tools"], json_object());
                assert_eq!(response["result"]["serverInfo"]["name"], "symdesk");
            }
            "mcp001-ping-string-id" => {
                let response: Value = serde_json::from_slice(&output.stdout).expect("response");
                assert_eq!(response["id"], "ping");
                assert_eq!(response["result"], json_object());
            }
            "mcp001-initialize-notification" => assert!(output.stdout.is_empty()),
            "mcp001-null-method" => {
                let response: Value = serde_json::from_slice(&output.stdout).expect("response");
                assert_eq!(response["id"], "null-method");
                assert_eq!(response["error"]["code"], -32601);
                assert_eq!(response["error"]["message"], "Method not found: ");
            }
            "mcp001-invalid-array" | "mcp001-invalid-method-type" => {
                let response: Value = serde_json::from_slice(&output.stdout).expect("error frame");
                assert_eq!(response["error"]["code"], -32700, "{}", case.id);
            }
            _ => unreachable!("unexpected MCP-001 fixture case {}", case.id),
        }
    }
    assert_eq!(seen, 7, "Go fixture must retain all MCP-001 cases");
    fs::remove_dir_all(root).expect("remove isolated test directory");
}

fn json_object() -> Value {
    Value::Object(serde_json::Map::new())
}
