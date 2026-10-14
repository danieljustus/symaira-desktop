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
    #[serde(default)]
    instructions_absent: bool,
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
                if case.instructions_absent {
                    assert!(
                        response["result"].get("instructions").is_none(),
                        "{}",
                        case.id
                    );
                }
            }
            "mcp001-ping-string-id" => {
                let response: Value = serde_json::from_slice(&output.stdout).expect("response");
                assert_eq!(response["id"], "ping");
                assert_eq!(response["result"], json_object());
            }
            "mcp001-initialize-notification" => assert!(output.stdout.is_empty()),
            // CoreKit v0.18 mcpserver: valid JSON that is not a JSON-RPC 2.0
            // request object is an Invalid Request with a null id.
            "mcp001-null-method" | "mcp001-invalid-array" | "mcp001-invalid-method-type" => {
                let response: Value = serde_json::from_slice(&output.stdout).expect("error frame");
                assert_eq!(response["id"], Value::Null, "{}", case.id);
                assert_eq!(response["error"]["code"], -32600, "{}", case.id);
                assert_eq!(
                    response["error"]["message"], "Invalid Request",
                    "{}",
                    case.id
                );
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

#[cfg(unix)]
#[test]
fn stdout_write_failure_exits_while_stdin_stays_open() {
    use std::{
        io::Write,
        process::Stdio,
        thread,
        time::{Duration, Instant},
    };

    let root = std::env::temp_dir().join(format!(
        "symdesk-mcp-write-failure-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    fs::create_dir(&root).unwrap();
    let mut checked = 0;
    for framed in [false, true] {
        for method in ["ping", "tools/call"] {
            let stdout = root.join(format!("{framed}-{checked}.stdout"));
            let stderr = root.join(format!("{framed}-{checked}.stderr"));
            fs::write(&stdout, []).unwrap();
            let mut child = Command::new(env!("CARGO_BIN_EXE_symdesk"))
                .arg("mcp")
                .env_clear()
                .env("HOME", &root)
                .env("XDG_CONFIG_HOME", &root)
                .env("XDG_DATA_HOME", &root)
                .env("PATH", &root)
                .stdin(Stdio::piped())
                .stdout(fs::File::open(&stdout).unwrap())
                .stderr(fs::File::create(&stderr).unwrap())
                .spawn()
                .unwrap();
            let body = serde_json::to_vec(&serde_json::json!({
                "jsonrpc":"2.0", "id":1, "method":method,
                "params":{"name":"desk_status", "arguments":{}}
            }))
            .unwrap();
            let mut stdin = child.stdin.take().unwrap();
            if framed {
                write!(stdin, "Content-Length: {}\r\n\r\n", body.len()).unwrap();
            }
            stdin.write_all(&body).unwrap();
            if !framed {
                stdin.write_all(b"\n").unwrap();
            }
            stdin.flush().unwrap();
            let deadline = Instant::now() + Duration::from_secs(5);
            let status = loop {
                if let Some(status) = child.try_wait().unwrap() {
                    break Some(status);
                }
                if Instant::now() >= deadline {
                    child.kill().unwrap();
                    child.wait().unwrap();
                    break None;
                }
                thread::sleep(Duration::from_millis(5));
            };
            // Keep this handle alive until the terminal status is observed.
            drop(stdin);
            let diagnostics = fs::read_to_string(&stderr).unwrap();
            assert!(
                status.is_some(),
                "{method}/{framed}: MCP requires EOF after failed stdout write"
            );
            assert!(
                status.unwrap().code().is_some_and(|code| code > 0),
                "{diagnostics}"
            );
            assert!(!diagnostics.is_empty(), "write error must be diagnosed");
            assert!(fs::read(stdout).unwrap().is_empty());
            checked += 1;
        }
    }
    assert_eq!(checked, 4);
    fs::remove_dir_all(root).unwrap();
}
