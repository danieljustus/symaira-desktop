#![deny(unsafe_code)]

// MCP-002: Go registers every legacy alias with the handler of its canonical
// tool (`internal/tools/aliases_test.go`). The native server must therefore
// answer an alias byte for byte like the canonical tool for the same inputs,
// leave the same resulting files behind, and advertise the same input schema
// and annotations. Each pair runs in two identical disposable roots.

#[path = "../../test-support/process_env.rs"]
mod process_env;

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    time::Duration,
};

use regex::Regex;
use serde_json::Value;

#[path = "support/isolated_root.rs"]
mod isolated_root;
#[path = "support/mcp_run.rs"]
mod mcp_run;

/// `(alias, canonical)` as registered by Go.
const ALIASES: [(&str, &str); 12] = [
    ("search_documents", "desk_search"),
    ("list_documents", "desk_docs"),
    ("ingest_file", "desk_ingest"),
    ("list_jobs", "desk_ingest_jobs"),
    ("retry_job", "desk_ingest_retry"),
    ("reocr", "desk_ingest_reocr"),
    ("list_rules", "desk_rules_list"),
    ("add_rule", "desk_rules_add"),
    ("delete_rule", "desk_rules_delete"),
    ("split_pdf", "desk_split_pdf"),
    ("merge_pdf", "desk_merge_pdf"),
    ("rotate_pdf", "desk_rotate_pdf"),
];

/// Inputs per alias: success and failure shapes, `$ROOT` is the disposable root.
fn inputs(alias: &str) -> &'static [&'static str] {
    match alias {
        "search_documents" => &[r#"{"query":"alpha"}"#, r#"{}"#, r#"{"query":5}"#],
        "list_documents" => &[r#"{}"#, r#"{"status":"open"}"#],
        "ingest_file" => &[
            r#"{"source_path":"$ROOT/cwd/note.txt"}"#,
            r#"{}"#,
            r#"{"source_path":"$ROOT/cwd/missing.txt"}"#,
        ],
        "list_jobs" => &[r#"{}"#],
        "retry_job" => &[r#"{"id":"1"}"#, r#"{"id":"x"}"#, r#"{}"#],
        "reocr" => &[
            r#"{}"#,
            r#"{"document_id":1}"#,
            r#"{"archive_path":"a","document_id":1}"#,
        ],
        "list_rules" => &[r#"{}"#],
        "add_rule" => &[
            r#"{"pattern":"invoice*","kind":"tag","value":"billing"}"#,
            r#"{"pattern":"x"}"#,
        ],
        "delete_rule" => &[r#"{"id":1}"#, r#"{}"#],
        "split_pdf" | "merge_pdf" | "rotate_pdf" => &[r#"{}"#, r#"{"input":"$ROOT/cwd/none.pdf"}"#],
        other => panic!("no inputs for {other}"),
    }
}

const NOTE: &str = "---\ntitle: Alpha\nstatus: open\n---\nalpha body text\n";

fn normalize(text: &str, root: &str) -> String {
    let timestamp =
        Regex::new(r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?")
            .unwrap();
    timestamp
        .replace_all(&text.replace(root, "$ROOT"), "<TIME>")
        .into_owned()
}

fn files(root: &Path, base: &Path, out: &mut BTreeMap<String, String>) {
    for entry in fs::read_dir(base).unwrap() {
        let path = entry.unwrap().path();
        if path.is_dir() {
            files(root, &path, out);
        } else if path.extension().is_some_and(|ext| ext != "db") {
            let relative = path
                .strip_prefix(root)
                .unwrap()
                .to_string_lossy()
                .into_owned();
            let body = String::from_utf8_lossy(&fs::read(&path).unwrap()).into_owned();
            out.insert(relative, normalize(&body, root.to_str().unwrap()));
        }
    }
}

struct Run {
    stdout: String,
    stderr: String,
    code: Option<i32>,
    files: BTreeMap<String, String>,
}

fn serve(requests: &[String], allow_write: bool) -> Run {
    let root: PathBuf = isolated_root::create("symdesk-mcp-alias");
    mcp_run::make_dirs(&root);
    let root_text = root.to_str().unwrap().to_owned();
    fs::write(root.join("vault/alpha.md"), NOTE).unwrap();
    fs::write(root.join("cwd/note.txt"), "plain text to ingest\n").unwrap();
    let mut args: Vec<std::ffi::OsString> = vec![
        "--vault".into(),
        root.join("vault").into_os_string(),
        "mcp".into(),
    ];
    if allow_write {
        args.push("--allow-write".into());
    }
    let input = requests
        .iter()
        .map(|request| format!("{}\n", request.replace("$ROOT", &root_text)))
        .collect::<String>();
    let outcome = mcp_run::run(
        &mcp_run::Spec {
            root: &root,
            args,
            input: input.into_bytes(),
            path_env: "".into(),
            extra_env: vec![(
                "SYMDESK_SIDECAR".into(),
                root.join("data/sidecar.db").into_os_string(),
            )],
            deadline: Duration::from_secs(90),
        },
        "alias",
    );
    let mut tree = BTreeMap::new();
    files(&root, &root.join("vault"), &mut tree);
    let run = Run {
        stdout: normalize(&String::from_utf8_lossy(&outcome.stdout), &root_text),
        stderr: normalize(&String::from_utf8_lossy(&outcome.stderr), &root_text),
        code: outcome.code,
        files: tree,
    };
    fs::remove_dir_all(&root).unwrap();
    run
}

fn call(name: &str, arguments: &str) -> String {
    format!(
        r#"{{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{{"name":"{name}","arguments":{arguments}}}}}"#
    )
}

#[test]
fn every_alias_answers_like_its_canonical_tool() {
    let mut compared = 0;
    let mut succeeded = 0;
    for (alias, canonical) in ALIASES {
        for arguments in inputs(alias) {
            let by_alias = serve(&[call(alias, arguments)], true);
            let by_canonical = serve(&[call(canonical, arguments)], true);
            assert_eq!(by_alias.code, by_canonical.code, "{alias}: exit code");
            assert_eq!(by_alias.stderr, by_canonical.stderr, "{alias}: stderr");
            assert_eq!(
                by_alias.stdout, by_canonical.stdout,
                "{alias} {arguments}: stdout differs from {canonical}"
            );
            assert_eq!(by_alias.files, by_canonical.files, "{alias}: vault state");
            assert!(
                !by_alias.stdout.contains("Unknown tool"),
                "{alias} must be registered: {}",
                by_alias.stdout
            );
            succeeded += usize::from(by_alias.stdout.contains(r#""isError":false"#));
            compared += 1;
        }
    }
    assert!(compared >= 25, "only {compared} alias calls compared");
    assert!(
        succeeded >= 6,
        "only {succeeded} successful calls: the comparison would be mostly error text"
    );
}

#[test]
fn read_only_aliases_stay_registered_without_allow_write() {
    for (alias, canonical) in [
        ("search_documents", "desk_search"),
        ("list_documents", "desk_docs"),
        ("list_jobs", "desk_ingest_jobs"),
        ("list_rules", "desk_rules_list"),
    ] {
        let arguments = inputs(alias)[0];
        let by_alias = serve(&[call(alias, arguments)], false);
        let by_canonical = serve(&[call(canonical, arguments)], false);
        assert_eq!(by_alias.stdout, by_canonical.stdout, "{alias}");
        assert!(by_alias.stdout.contains(r#""isError":false"#), "{alias}");
    }
    for alias in [
        "ingest_file",
        "retry_job",
        "reocr",
        "add_rule",
        "delete_rule",
        "split_pdf",
    ] {
        let refused = serve(&[call(alias, inputs(alias)[0])], false);
        assert!(
            refused.stdout.contains("Unknown tool"),
            "{alias} is a write tool"
        );
    }
}

#[test]
fn aliases_advertise_the_schema_and_annotations_of_their_canonical_tool() {
    let list = r#"{"jsonrpc":"2.0","id":"list","method":"tools/list"}"#.to_owned();
    let run = serve(&[list], true);
    let response: Value = serde_json::from_str(run.stdout.lines().next().unwrap()).unwrap();
    let tools = response["result"]["tools"].as_array().unwrap();
    let find = |name: &str| {
        tools
            .iter()
            .find(|tool| tool["name"] == name)
            .unwrap_or_else(|| panic!("{name} missing from tools/list"))
    };
    for (alias, canonical) in ALIASES {
        assert_eq!(
            find(alias)["inputSchema"],
            find(canonical)["inputSchema"],
            "{alias}: schema"
        );
        assert_eq!(
            find(alias)["annotations"],
            find(canonical)["annotations"],
            "{alias}: annotations"
        );
    }
}

// Negative control: the comparison must notice tools that are not aliases.
#[test]
fn comparison_rejects_a_different_tool() {
    let one = serve(&[call("list_rules", "{}")], true);
    let other = serve(&[call("list_jobs", "{}")], true);
    assert_ne!(one.stdout, other.stdout);
}
