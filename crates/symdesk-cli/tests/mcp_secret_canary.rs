#![deny(unsafe_code)]
#![cfg(unix)]

// SEC-002 (MCP part): planted credentials must never reach MCP responses,
// stderr or any file under the disposable HOME/vault. Three layers:
//   1. the corpus recorded from the Go server (`calls-process.json`): every
//      case that plants a secret in its environment is scanned (responses,
//      stderr, resulting files) and replayed natively with the same scan;
//   2. live native runs against a loopback provider that echoes the credential
//      back (transform, ask, autofill), with extra server/admin/worker tokens
//      planted in the environment;
//   3. planted-leak negative controls: a sibling tool that prints the secret
//      must be caught by the very same scan, end to end through the binary.

#[path = "../../test-support/process_env.rs"]
mod process_env;

use std::{
    collections::BTreeMap,
    ffi::OsString,
    fs,
    io::{Read, Write},
    net::{SocketAddr, TcpListener, TcpStream},
    os::unix::fs::PermissionsExt,
    path::{Path, PathBuf},
    sync::{
        Arc,
        atomic::{AtomicBool, AtomicUsize, Ordering},
    },
    thread,
    time::Duration,
};

use serde::Deserialize;

#[path = "support/isolated_root.rs"]
mod isolated_root;
#[path = "support/mcp_run.rs"]
mod mcp_run;

const CANARY: &str = "port-only-synthetic-mcp-canary-3b9e";
const EXTRA_CANARIES: [(&str, &str); 3] = [
    (
        "SYMDESK_SERVER_TOKEN",
        "port-only-synthetic-server-token-51c2",
    ),
    (
        "SYMDESK_ADMIN_TOKEN",
        "port-only-synthetic-admin-token-8f07",
    ),
    (
        "SYMDESK_WORKER_TOKEN",
        "port-only-synthetic-worker-token-a6d4",
    ),
];

#[derive(Deserialize)]
struct Fixture {
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Step {
    request: String,
    stdout: String,
    stderr: String,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    #[serde(default)]
    allow_write: bool,
    #[serde(default)]
    no_vault: bool,
    #[serde(default)]
    files: BTreeMap<String, String>,
    #[serde(default)]
    env: BTreeMap<String, String>,
    steps: Vec<Step>,
    after_files: BTreeMap<String, String>,
}

fn is_secret_name(name: &str) -> bool {
    let name = name.to_ascii_uppercase();
    ["KEY", "TOKEN", "SECRET", "PASSWORD", "CREDENTIAL"]
        .iter()
        .any(|word| name.contains(word))
}

fn fixture() -> Fixture {
    serde_json::from_slice(
        &fs::read(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/mcp/calls-process.json"),
        )
        .expect("Go process transcript fixture"),
    )
    .expect("decode Go process transcript")
}

/// The planted values of a corpus case (non-empty secret-named environment values).
fn planted(case: &Case) -> Vec<String> {
    case.env
        .iter()
        .filter(|(name, value)| is_secret_name(name) && !value.is_empty())
        .map(|(_, value)| value.clone())
        .collect()
}

/// Where each planted value appears: the raw text and its JSON-escaped form.
fn hits(planted: &[String], observed: &[(String, String)]) -> Vec<String> {
    let mut found = Vec::new();
    for (place, text) in observed {
        for value in planted {
            let escaped = serde_json::to_string(value).unwrap();
            let escaped = escaped.trim_matches('"');
            if text.contains(value.as_str()) || text.contains(escaped) {
                found.push(format!("{place} contains a planted secret"));
            }
        }
    }
    found
}

fn files_under(dir: &Path, out: &mut Vec<PathBuf>) {
    for entry in fs::read_dir(dir).unwrap() {
        let path = entry.unwrap().path();
        if path.is_dir() {
            files_under(&path, out);
        } else {
            out.push(path);
        }
    }
}

/// Every file below `root` (sidecar databases and all) as lossy text.
fn tree(root: &Path) -> Vec<(String, String)> {
    let mut paths = Vec::new();
    files_under(root, &mut paths);
    paths
        .into_iter()
        .map(|path| {
            let place = format!("file {}", path.strip_prefix(root).unwrap().display());
            let body = String::from_utf8_lossy(&fs::read(&path).unwrap()).into_owned();
            (place, body)
        })
        .collect()
}

fn serve(
    root: &Path,
    requests: &[String],
    allow_write: bool,
    no_vault: bool,
    path_env: &str,
    env: &[(String, String)],
    label: &str,
) -> Vec<(String, String)> {
    let root_text = root.to_str().unwrap();
    let mut args: Vec<OsString> = if no_vault {
        vec!["mcp".into()]
    } else {
        vec![
            "--vault".into(),
            root.join("vault").into_os_string(),
            "mcp".into(),
        ]
    };
    if allow_write {
        args.push("--allow-write".into());
    }
    let input: String = requests
        .iter()
        .map(|request| format!("{}\n", request.replace("$ROOT", root_text)))
        .collect();
    let mut extra_env: Vec<(OsString, OsString)> = vec![(
        "SYMDESK_SIDECAR".into(),
        root.join("data/sidecar.db").into_os_string(),
    )];
    for (key, value) in env {
        extra_env.push((key.into(), value.replace("$ROOT", root_text).into()));
    }
    let outcome = mcp_run::run(
        &mcp_run::Spec {
            root,
            args,
            input: input.into_bytes(),
            path_env: path_env.into(),
            extra_env,
            deadline: Duration::from_secs(90),
        },
        label,
    );
    assert_eq!(
        outcome.code,
        Some(0),
        "{label}: the server exits cleanly at end of input"
    );
    vec![
        (
            format!("{label} stdout"),
            String::from_utf8_lossy(&outcome.stdout).into_owned(),
        ),
        (
            format!("{label} stderr"),
            String::from_utf8_lossy(&outcome.stderr).into_owned(),
        ),
    ]
}

fn fresh_root() -> PathBuf {
    let root = isolated_root::create("symdesk-mcp-canary");
    mcp_run::make_dirs(&root);
    root
}

fn call(name: &str, arguments: &str) -> String {
    format!(
        r#"{{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{{"name":"{name}","arguments":{arguments}}}}}"#
    )
}

/// Loopback provider answering every request with 401 and a body that echoes
/// the credential, so a client that prints provider errors verbatim leaks it.
struct Echo {
    url: String,
    address: SocketAddr,
    requests: Arc<AtomicUsize>,
    stop: Arc<AtomicBool>,
    handle: Option<thread::JoinHandle<()>>,
}

impl Echo {
    fn start() -> Self {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let address = listener.local_addr().unwrap();
        let requests = Arc::new(AtomicUsize::new(0));
        let stop = Arc::new(AtomicBool::new(false));
        let (seen, halt) = (requests.clone(), stop.clone());
        let handle = thread::spawn(move || {
            while let Ok((mut stream, _)) = listener.accept() {
                if halt.load(Ordering::SeqCst) {
                    break;
                }
                let mut request = Vec::new();
                let mut chunk = [0_u8; 4096];
                while let Ok(n) = stream.read(&mut chunk) {
                    request.extend_from_slice(&chunk[..n]);
                    if n == 0 || request.windows(4).any(|window| window == b"\r\n\r\n") {
                        break;
                    }
                }
                seen.fetch_add(1, Ordering::SeqCst);
                let body = format!("{{\"error\":\"invalid credential {CANARY}\"}}");
                let _ = write!(
                    stream,
                    "HTTP/1.1 401 Test\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                    body.len()
                );
            }
        });
        Self {
            url: format!("http://{address}"),
            address,
            requests,
            stop,
            handle: Some(handle),
        }
    }
}

impl Drop for Echo {
    fn drop(&mut self) {
        self.stop.store(true, Ordering::SeqCst);
        let _ = TcpStream::connect(self.address);
        if let Some(handle) = self.handle.take() {
            let _ = handle.join();
        }
    }
}

#[test]
fn recorded_go_responses_carry_no_planted_secret() {
    let fixture = fixture();
    let mut planting_cases = 0;
    for case in &fixture.cases {
        let secrets = planted(case);
        if secrets.is_empty() {
            continue;
        }
        planting_cases += 1;
        let mut observed: Vec<(String, String)> = Vec::new();
        for (index, step) in case.steps.iter().enumerate() {
            observed.push((format!("{}#{index} stdout", case.id), step.stdout.clone()));
            observed.push((format!("{}#{index} stderr", case.id), step.stderr.clone()));
        }
        for (path, body) in &case.after_files {
            observed.push((format!("{} file {path}", case.id), body.clone()));
        }
        assert_eq!(
            hits(&secrets, &observed),
            Vec::<String>::new(),
            "{}",
            case.id
        );
    }
    assert!(
        planting_cases >= 1,
        "the corpus must plant at least one secret (transform-anthropic-secret-canary)"
    );
}

#[test]
fn native_replay_of_planting_cases_leaks_nothing() {
    let fixture = fixture();
    let mut replayed = 0;
    for case in &fixture.cases {
        let secrets = planted(case);
        if secrets.is_empty() {
            continue;
        }
        let root = fresh_root();
        for (path, body) in &case.files {
            let target = root.join("vault").join(path);
            fs::create_dir_all(target.parent().unwrap()).unwrap();
            fs::write(target, body).unwrap();
        }
        let env: Vec<(String, String)> = case
            .env
            .iter()
            .map(|(key, value)| (key.clone(), value.clone()))
            .collect();
        let requests: Vec<String> = case.steps.iter().map(|step| step.request.clone()).collect();
        let mut observed = serve(
            &root,
            &requests,
            case.allow_write,
            case.no_vault,
            "",
            &env,
            &case.id,
        );
        observed.extend(tree(&root));
        assert_eq!(
            hits(&secrets, &observed),
            Vec::<String>::new(),
            "{}",
            case.id
        );
        fs::remove_dir_all(&root).unwrap();
        replayed += 1;
    }
    assert!(replayed >= 1, "no planting case replayed");
}

fn planted_environment(echo: &Echo) -> Vec<(String, String)> {
    let mut env = vec![
        ("SYMDESK_LLM_PROVIDER".to_owned(), "anthropic".to_owned()),
        ("SYMDESK_LLM_API_KEY".to_owned(), CANARY.to_owned()),
        ("SYMDESK_ANTHROPIC_URL".to_owned(), echo.url.clone()),
        ("SYMDESK_OLLAMA_URL".to_owned(), echo.url.clone()),
    ];
    env.extend(
        EXTRA_CANARIES
            .iter()
            .map(|(key, value)| ((*key).to_owned(), (*value).to_owned())),
    );
    env
}

fn all_secrets() -> Vec<String> {
    let mut secrets = vec![CANARY.to_owned()];
    secrets.extend(EXTRA_CANARIES.iter().map(|(_, value)| (*value).to_owned()));
    secrets
}

#[test]
fn echoing_provider_cannot_leak_the_key_through_any_tool() {
    let echo = Echo::start();
    let root = fresh_root();
    let vault = root.join("vault");
    fs::write(
        vault.join("alpha.md"),
        "---\ntitle: Alpha\ntype: letter\n---\nalpha body\n",
    )
    .unwrap();
    fs::create_dir_all(vault.join("bases")).unwrap();
    fs::write(
        vault.join("bases/letters.md"),
        "---\ntype: base\ntitle: Letters\nviews:\n  - id: letters\n    name: Letters\n    filters:\n      - key: type\n        value: letter\n---\n",
    )
    .unwrap();
    let requests = vec![
        r#"{"jsonrpc":"2.0","id":"init","method":"initialize","params":{}}"#.to_owned(),
        r#"{"jsonrpc":"2.0","id":"list","method":"tools/list"}"#.to_owned(),
        call("desk_status", "{}"),
        call("desk_transform", r#"{"text":"hello","intent":"summarize"}"#),
        call("desk_ask", r#"{"query":"alpha"}"#),
        call(
            "desk_autofill",
            r#"{"view":"letters","property":"summary","dry_run":true}"#,
        ),
        call(
            "desk_autofill",
            r#"{"view":"letters","property":"summary"}"#,
        ),
    ];
    let mut observed = serve(
        &root,
        &requests,
        true,
        false,
        "",
        &planted_environment(&echo),
        "echo",
    );
    observed.extend(tree(&root));
    let stdout = &observed[0].1;
    assert!(
        echo.requests.load(Ordering::SeqCst) >= 1,
        "the provider was never reached, the scan would prove nothing: {stdout}"
    );
    assert!(
        stdout.contains("Request failed") || stdout.contains("failed"),
        "transform must surface the provider failure: {stdout}"
    );
    assert_eq!(hits(&all_secrets(), &observed), Vec::<String>::new());
    fs::remove_dir_all(&root).unwrap();
}

/// A sibling `symbrowse` that prints the planted key: `desk_clip` saves its
/// output into a note, so the leak reaches a file below the vault.
fn leaking_clipper(root: &Path) -> PathBuf {
    let bin = root.join("bin");
    fs::create_dir_all(&bin).unwrap();
    let script = bin.join("symbrowse");
    fs::write(
        &script,
        "#!/bin/sh\nprintf '# Leaky page\\n\\nkey=%s\\n' \"$SYMDESK_LLM_API_KEY\"\n",
    )
    .unwrap();
    fs::set_permissions(&script, fs::Permissions::from_mode(0o755)).unwrap();
    bin
}

#[test]
fn planted_leak_is_detected_end_to_end() {
    let root = fresh_root();
    let bin = leaking_clipper(&root);
    let env = vec![("SYMDESK_LLM_API_KEY".to_owned(), CANARY.to_owned())];
    let mut observed = serve(
        &root,
        &[call(
            "desk_clip",
            r#"{"url":"https://example.invalid/page"}"#,
        )],
        true,
        false,
        bin.to_str().unwrap(),
        &env,
        "leak",
    );
    observed.extend(
        tree(&root.join("vault"))
            .into_iter()
            .map(|(place, body)| (format!("vault {place}"), body)),
    );
    let found = hits(&[CANARY.to_owned()], &observed);
    assert!(
        found
            .iter()
            .any(|place| place.starts_with("vault file Clipped")),
        "the scan must find the key the clipper planted in the note: {found:?}"
    );
    fs::remove_dir_all(&root).unwrap();
}

#[test]
fn scan_flags_leaks_in_every_stream_and_in_json_escaped_form() {
    let secrets = vec!["sk-test\"quoted".to_owned()];
    let clean = vec![("stdout".to_owned(), "nothing here".to_owned())];
    assert!(hits(&secrets, &clean).is_empty());
    for (place, text) in [
        ("stdout", "x sk-test\"quoted y"),
        ("stderr", "x sk-test\\\"quoted y"),
        ("file a.md", "key: sk-test\"quoted"),
    ] {
        let found = hits(&secrets, &[(place.to_owned(), text.to_owned())]);
        assert_eq!(found, vec![format!("{place} contains a planted secret")]);
    }
}
