//! Source-contract tests, not captured Go oracle responses. The companion
//! tests/mcp_pdf_differential.py compares actual native CLI responses and files.
use std::{fs, io::Cursor};

use super::*;

#[path = "../../tests/support/isolated_root.rs"]
mod isolated_root;

const NAMES: [&str; 6] = [
    "desk_split_pdf", "desk_merge_pdf", "desk_rotate_pdf",
    "split_pdf", "merge_pdf", "rotate_pdf",
];

fn run(input: &str, allow_write: bool) -> Vec<u8> {
    let (output, result) = serve_io(Cursor::new(input.as_bytes().to_vec()), Vec::new(), None, allow_write);
    result.expect("MCP transport");
    output
}

fn call(name: &str, arguments: Option<&str>, allow_write: bool) -> Value {
    let args = arguments.map(|raw| format!(",\"arguments\":{raw}")).unwrap_or_default();
    let frame = format!("{{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{{\"name\":\"{name}\"{args}}}}}\n");
    serde_json::from_slice(&run(&frame, allow_write)).expect("one response")
}

#[test]
fn pdf_write_opt_in_flag_defaults_and_explicit_boolean_values() {
    for (extra, expected) in [
        (vec![], false),
        (vec!["--allow-write"], true),
        (vec!["--allow-write=true"], true),
        (vec!["--allow-write=false"], false),
        (vec!["--allow-write=1"], true),
        (vec!["--allow-write=F"], false),
        (vec!["--allow-write", "--allow-write=false"], false),
    ] {
        let mut args = vec!["symdesk", "mcp"];
        args.extend(extra);
        let parsed = crate::cli().try_get_matches_from(args).expect("valid boolean flag");
        assert_eq!(parsed.subcommand_matches("mcp").unwrap().get_flag("allow-write"), expected);
    }
    for value in ["yes", "no", "on", "", "maybe"] {
        assert!(crate::cli().try_get_matches_from([
            "symdesk", "mcp", &format!("--allow-write={value}"),
        ]).is_err());
    }
}

#[test]
fn pdf_catalog_and_status_respect_write_permission() {
    for allowed in [false, true] {
        let response: Value = serde_json::from_slice(&run(
            "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}\n", allowed,
        )).unwrap();
        let tools = response["result"]["tools"].as_array().unwrap();
        assert_eq!(tools.len(), if allowed { 10 } else { 4 });
        let pdfs: Vec<_> = tools.iter().filter(|tool| {
            NAMES.contains(&tool["name"].as_str().unwrap())
        }).collect();
        assert_eq!(pdfs.len(), if allowed { 6 } else { 0 });
        if allowed {
            assert_eq!(pdfs.iter().map(|tool| tool["name"].as_str().unwrap()).collect::<Vec<_>>(), NAMES);
            for (canonical, alias) in pdfs[..3].iter().zip(&pdfs[3..]) {
                assert_eq!(canonical["inputSchema"], alias["inputSchema"]);
                assert_eq!(canonical["annotations"], json!({}));
                assert_eq!(alias["annotations"], json!({}));
            }
        }
        let status = call("desk_status", Some("{}"), allowed);
        let body: Value = serde_json::from_str(status["result"]["content"][0]["text"].as_str().unwrap()).unwrap();
        assert_eq!(body["capabilities"], if allowed { "read_write" } else { "read_only" });
    }
}

#[test]
fn pdf_disabled_calls_and_aliases_leave_files_unchanged() {
    let root = isolated_root::create("symdesk-pdf-disabled");
    let input = root.join("input.pdf");
    let output = root.join("output.pdf");
    fs::write(&input, b"sentinel input, not a real PDF").unwrap();
    fs::write(&output, b"sentinel output").unwrap();
    let args = json!({
        "input": input, "inputs": [input, input], "at": "1", "output_dir": root,
        "output": output, "degrees": 90,
    }).to_string();
    for name in NAMES {
        for raw in [Some(args.as_str()), Some("{\"input\":false}"), None] {
            let response = call(name, raw, false);
            assert_eq!(response, json!({
                "jsonrpc":"2.0", "id":1,
                "error":{"code":METHOD_NOT_FOUND,"message":format!("Unknown tool: {name}")},
            }));
        }
    }
    assert_eq!(fs::read(&input).unwrap(), b"sentinel input, not a real PDF");
    assert_eq!(fs::read(&output).unwrap(), b"sentinel output");
    assert_eq!(fs::read_dir(&root).unwrap().count(), 2);
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn pdf_enabled_call_validation_and_aliases_use_tool_error_envelopes() {
    let cases = [
        ("desk_split_pdf", "{}", "input, at and output_dir are required"),
        ("desk_split_pdf", "null", "input, at and output_dir are required"),
        ("desk_split_pdf", r#"{"input":false}"#, "json: cannot unmarshal bool into Go struct field .input of type string"),
        ("desk_merge_pdf", "{}", "at least two input PDFs are required"),
        ("desk_merge_pdf", r#"{"inputs":["a","b"]}"#, "output is required"),
        ("desk_merge_pdf", r#"{"output":false,"inputs":[]}"#, "json: cannot unmarshal bool into Go struct field .output of type string"),
        ("desk_merge_pdf", r#"{"inputs":"a"}"#, "json: cannot unmarshal string into Go struct field .inputs of type []string"),
        ("desk_merge_pdf", r#"{"inputs":[1]}"#, "json: cannot unmarshal number into Go struct field .inputs of type string"),
        ("desk_rotate_pdf", "{}", "input and output are required"),
        ("desk_rotate_pdf", r#"{"degrees":90.0}"#, "json: cannot unmarshal number 90.0 into Go struct field .degrees of type int"),
        ("desk_rotate_pdf", r#"{"degrees":9e1}"#, "json: cannot unmarshal number 9e1 into Go struct field .degrees of type int"),
        ("desk_rotate_pdf", r#"{"degrees":1e999}"#, "json: cannot unmarshal number 1e999 into Go struct field .degrees of type int"),
        ("desk_rotate_pdf", r#"{"degrees":9223372036854775808}"#, "json: cannot unmarshal number 9223372036854775808 into Go struct field .degrees of type int"),
        ("desk_rotate_pdf", r#"{"pages":false,"degrees":90.0}"#, "json: cannot unmarshal bool into Go struct field .pages of type string"),
    ];
    for (name, arguments, expected) in cases {
        let canonical = call(name, Some(arguments), true);
        assert_eq!(canonical, call(name.strip_prefix("desk_").unwrap(), Some(arguments), true));
        assert_eq!(canonical, json!({"jsonrpc":"2.0","id":1,"result":{
            "content":[{"type":"text","text":expected}],"isError":true,
        }}));
    }
    for name in NAMES {
        let missing = call(name, None, true);
        assert_eq!(missing["result"]["content"][0]["text"], "unexpected end of JSON input");
        assert_eq!(missing["result"]["isError"], true);
    }
}

#[test]
fn pdf_facade_checks_input_before_rotation_and_preserves_it() {
    let root = isolated_root::create("symdesk-pdf-order");
    let input = root.join("input.pdf");
    let output = root.join("absent-dir/output.pdf");
    fs::write(&input, b"sentinel input, not a real PDF").unwrap();
    let args = json!({"input":input,"output":output,"degrees":0}).to_string();
    let response = call("desk_rotate_pdf", Some(&args), true);
    assert_eq!(response["result"]["content"][0]["text"],
        "rotation must be one of -270, -180, -90, 90, 180, 270");
    assert_eq!(fs::read(&input).unwrap(), b"sentinel input, not a real PDF");
    assert!(!output.parent().unwrap().exists());
    let missing = json!({"input":root.join("missing.pdf"),"output":output,"degrees":0}).to_string();
    let response = call("rotate_pdf", Some(&missing), true);
    assert!(response["result"]["content"][0]["text"].as_str().unwrap().starts_with("rotate input: stat "));
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn pdf_malformed_params_reject_and_notification_errors_are_silent() {
    for allowed in [false, true] {
        for (params, expected) in [
            (r#"{"name":true}"#, "json: cannot unmarshal bool into Go struct field .name of type string"),
            (r#"{"name":"desk_rotate_pdf","_meta":false}"#, "json: cannot unmarshal bool into Go struct field ._meta of type map[string]interface {}"),
        ] {
            let request = format!("{{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{params}}}\n");
            let response: Value = serde_json::from_slice(&run(&request, allowed)).unwrap();
            assert_eq!(response["error"], json!({"code":INVALID_PARAMS,"message":format!("Invalid params: {expected}")}));
        }
        let notification = "{\"jsonrpc\":\"2.0\",\"method\":\"tools/call\",\"params\":{\"name\":\"desk_split_pdf\",\"arguments\":{}}}\n";
        assert!(run(notification, allowed).is_empty());
        let invalid = "{\"jsonrpc\":\"2.0\",\"id\":{},\"method\":\"tools/call\",\"params\":{\"name\":\"desk_split_pdf\"}}\n";
        let response: Value = serde_json::from_slice(&run(invalid, allowed)).unwrap();
        assert_eq!(response["error"]["code"], INVALID_REQUEST);
    }
    let folded = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"NAME\":\"desk_rotate_pdf\",\"name\":null,\"ARGUMENTS\":{}}}\n";
    let response: Value = serde_json::from_slice(&run(folded, true)).unwrap();
    assert_eq!(response, call("desk_rotate_pdf", Some("{}"), true));
}

#[test]
fn pdf_response_escaping_and_framing_preserve_go_wire_shape() {
    for mode in [ResponseMode::Line, ResponseMode::Framed] {
        let output = Arc::new(Mutex::new(Vec::new()));
        send_tool_result(&output, mode, json!("<&>"), json!({"output":"<&>\u{2028}\u{2029}"}), false).unwrap();
        let bytes = output.lock().unwrap().clone();
        let text = String::from_utf8(bytes).unwrap();
        let body = if mode == ResponseMode::Framed {
            let (header, body) = text.split_once("\r\n\r\n").unwrap();
            assert_eq!(header, format!("Content-Length: {}", body.len()));
            body
        } else {
            assert!(text.ends_with('\n'));
            text.trim_end_matches('\n')
        };
        assert!(!body.contains(['<', '>', '&', '\u{2028}', '\u{2029}']));
        assert!(body.contains(r#""id":"\u003c\u0026\u003e""#));
        let parsed: Value = serde_json::from_str(body).unwrap();
        assert_eq!(parsed["result"]["content"][0]["text"],
            r#"{"output":"\u003c\u0026\u003e\u2028\u2029"}"#);
    }
}

#[test]
fn pdf_shared_typed_arguments_do_not_collapse_decode_failure_to_null() {
    let deep = format!("{}0{}", "[".repeat(200), "]".repeat(200));
    for (name, field) in [("desk_search", "query"), ("desk_ask", "query"), ("desk_ls", "dir")] {
        for unknown in ["1e999", deep.as_str()] {
            for raw in [
                format!("{{\"{field}\":false,\"unknown\":{unknown}}}"),
                format!("{{\"unknown\":{unknown},\"{field}\":false}}"),
            ] {
                let response = call(name, Some(&raw), false);
                assert_eq!(response["result"]["isError"], true);
                assert_eq!(response["result"]["content"][0]["text"], go_string_field_type_error("bool", field));
            }
        }
        assert_eq!(call(name, None, false)["result"]["content"][0]["text"], "unexpected end of JSON input");
    }
    for notebook in [false, true] {
        assert_eq!(go_string_arguments(Some("null"), notebook).unwrap(), (String::new(), String::new()));
        assert_eq!(go_string_arguments(Some(r#"{"query":"kept","query":null,"ignored":1e999}"#), notebook).unwrap().0, "kept");
    }
    assert_eq!(go_string_fields(Some("null"), &["dir"], "unused").unwrap(), [""]);
}

#[test]
fn pdf_go_json_surrogates_metadata_overflow_and_first_error_order() {
    for (raw, expected) in [
        (r#""\ud800""#, "\u{fffd}"),
        (r#""\udc00""#, "\u{fffd}"),
        (r#""\ud800x\ud800\udc00""#, "\u{fffd}x\u{10000}"),
        (r#""\ud83d\ude00""#, "😀"),
        (r#""\\ud800""#, r"\ud800"),
    ] {
        let normalized = go_json_text(raw.as_bytes()).unwrap();
        assert_eq!(serde_json::from_slice::<String>(&normalized).unwrap(), expected);
        let args = format!("{{\"input\":{raw}}}");
        assert_eq!(call("desk_split_pdf", Some(&args), true)["result"]["content"][0]["text"],
            "input, at and output_dir are required");
        let params = format!("{{\"name\":{raw},\"arguments\":{{}}}}");
        let frame = format!("{{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{params}}}\n");
        let response: Value = serde_json::from_slice(&run(&frame, true)).unwrap();
        assert_eq!(response["error"]["message"], format!("Unknown tool: {expected}"));
    }
    let overflow = "json: cannot unmarshal number 1e999 into Go struct field ._meta of type float64";
    let wrong_name = "json: cannot unmarshal bool into Go struct field .name of type string";
    for allowed in [false, true] {
        for (params, expected) in [
            (r#"{"name":"desk_rotate_pdf","_meta":{"a":[{"n":1e999}]},"arguments":{}}"#, overflow),
            (r#"{"name":false,"_meta":{"n":1e999}}"#, wrong_name),
            (r#"{"_meta":{"n":1e999},"name":false}"#, overflow),
            (r#"{"_meta":{"n":1e999},"_meta":null,"name":"desk_rotate_pdf"}"#, overflow),
        ] {
            let frame = format!("{{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{params}}}\n");
            let response: Value = serde_json::from_slice(&run(&frame, allowed)).unwrap();
            assert_eq!(response["error"], json!({"code":INVALID_PARAMS,"message":format!("Invalid params: {expected}")}));
        }
    }
    let frame = r#"{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"desk_rotate_pdf","_meta":{"n":1e-999,"text":"1e999\ud800","\udc00":true},"arguments":{"degrees":0}}}
"#;
    let response: Value = serde_json::from_slice(&run(frame, true)).unwrap();
    assert_eq!(response["result"]["content"][0]["text"], "input and output are required");
    assert!(go_json_text(format!("{}0{}", "[".repeat(10_000), "]".repeat(10_000)).as_bytes()).is_ok());
    assert!(go_json_text(format!("{}0{}", "[".repeat(10_001), "]".repeat(10_001)).as_bytes()).is_err());
}

#[test]
fn pdf_writer_failure_wakes_server_with_nonclosable_input() {
    use std::{io::Read, time::Duration};

    struct ChannelReader(mpsc::Receiver<Vec<u8>>, Cursor<Vec<u8>>);
    impl Read for ChannelReader {
        fn read(&mut self, buf: &mut [u8]) -> io::Result<usize> {
            if buf.is_empty() { return Ok(0); }
            loop {
                let count = self.1.read(buf)?;
                if count != 0 { return Ok(count); }
                match self.0.recv() {
                    Ok(bytes) => self.1 = Cursor::new(bytes),
                    Err(_) => return Ok(0),
                }
            }
        }
    }
    struct BrokenWriter;
    impl Write for BrokenWriter {
        fn write(&mut self, _: &[u8]) -> io::Result<usize> {
            Err(io::Error::new(io::ErrorKind::BrokenPipe, "controlled writer failure"))
        }
        fn flush(&mut self) -> io::Result<()> { Ok(()) }
    }
    // Exercise both the synchronous response and asynchronous tools/call path.
    for method in ["ping", "tools/call"] {
        let (send, read) = mpsc::channel();
        let (done, result) = mpsc::channel();
        let server = thread::spawn(move || {
            let (_, outcome) = serve_io(io::BufReader::new(ChannelReader(read, Cursor::new(Vec::new()))),
                BrokenWriter, None, true);
            done.send(outcome).unwrap();
        });
        send.send(format!("{{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"{method}\",\"params\":{{\"name\":\"desk_rotate_pdf\",\"arguments\":{{}}}}}}\n").into_bytes()).unwrap();
        // Keep the input alive until the result arrives. EOF must not be what
        // releases the failed transport. Drop it before asserting to reap it
        // even if this negative control times out on a regressed server.
        let outcome = result.recv_timeout(Duration::from_secs(3));
        drop(send);
        server.join().unwrap();
        assert_eq!(outcome.unwrap().unwrap_err().kind(), io::ErrorKind::BrokenPipe);
    }
}
