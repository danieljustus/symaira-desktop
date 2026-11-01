#![deny(unsafe_code)]

// MCP-001: replay of the Go-recorded raw initialize/ping/notification/framing
// transcripts (`testdata/port/mcp/protocol-process.json`).

#[path = "../../test-support/process_env.rs"]
mod process_env;

use std::{fs, path::PathBuf, time::Duration};

#[path = "support/isolated_root.rs"]
mod isolated_root;
#[path = "../../symdesk-protocol/tests/support/mcp_cases.rs"]
mod mcp_cases;
#[path = "support/mcp_run.rs"]
mod mcp_run;

use mcp_cases::{Fixture, input};

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

// The Go and Rust test binaries carry different build versions: replace the
// version and recompute the Content-Length of every leading frame.
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

#[test]
fn raw_protocol_transcripts_match_go() {
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/mcp/protocol-process.json"),
        )
        .expect("Go protocol transcript fixture"),
    )
    .expect("decode Go protocol transcript");
    assert_eq!(fixture.schema_version, 1);
    assert!(
        fixture.cases.len() >= 300,
        "fixture shrank: {}",
        fixture.cases.len()
    );
    let mut executed = 0;
    let mut failures = Vec::new();
    for case in &fixture.cases {
        let root = isolated_root::create("symdesk-mcp-protocol");
        mcp_run::make_dirs(&root);
        let mut args = vec![
            "--vault".into(),
            root.join("vault").into_os_string(),
            "mcp".into(),
        ];
        if case.allow_write {
            args.push("--allow-write".into());
        }
        let outcome = mcp_run::run(
            &mcp_run::Spec {
                root: &root,
                args,
                input: input(case),
                path_env: "".into(),
                extra_env: Vec::new(),
                deadline: Duration::from_secs(60),
            },
            &case.id,
        );
        let root_text = root.to_str().unwrap().to_owned();
        fs::remove_dir_all(&root).unwrap();
        executed += 1;
        let stdout = normalize_stdout(&String::from_utf8(outcome.stdout).expect("UTF-8 stdout"));
        let stderr = String::from_utf8(outcome.stderr)
            .expect("UTF-8 stderr")
            .replace(&root_text, "$ROOT");
        if outcome.code != Some(case.exit_code) {
            failures.push(format!(
                "{}: exit {:?}, want {}",
                case.id, outcome.code, case.exit_code
            ));
        } else if stderr != case.stderr {
            failures.push(format!(
                "{}: stderr {:?}, want {:?}",
                case.id, stderr, case.stderr
            ));
        } else if stdout != case.stdout {
            failures.push(format!(
                "{}: stdout {:?}, want {:?}",
                case.id,
                &stdout[..stdout.len().min(300)],
                &case.stdout[..case.stdout.len().min(300)]
            ));
        }
    }
    assert_eq!(executed, fixture.cases.len());
    assert!(
        failures.is_empty(),
        "{} of {} cases differ:\n{}",
        failures.len(),
        executed,
        failures.join("\n")
    );
}
