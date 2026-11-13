//! CLI-008: replays the Go-recorded remote command allowlist fixture
//! (`testdata/port/cli/remote-command-process.json`) against the Rust
//! `symdesk serve` over loopback. Every accepted and rejected command vector,
//! the server-forced vault, prohibited local paths and the request envelope
//! must produce the same status, content type, body and vault files as the Go
//! server. Commands that the Rust CLI does not implement yet are compared on
//! the allow/deny decision only (`compare: "decision"` in the fixture).
#![cfg(unix)]

#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

#[path = "support/process_root.rs"]
mod process_root;

#[path = "support/cli_normalize.rs"]
mod cli_normalize;

#[path = "support/embedding_fake.rs"]
mod embedding_fake;

use std::{
    collections::BTreeMap,
    fs,
    io::{Read, Write},
    net::{TcpListener, TcpStream},
    path::{Path, PathBuf},
    process::{Child, Command, Stdio},
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

const ADMIN_TOKEN: &str = "remote-command-admin-token-0123456789abcdef";
const WORKER_TOKEN: &str = "remote-command-worker-token-fedcba9876543210";
const EXPECTED_CASES: usize = 5;
const EXPECTED_STEPS: usize = 184;
const EXPECTED_DECISION_STEPS: usize = 1;
const EXPECTED_FORBIDDEN: usize = 97;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    #[serde(default)]
    files: BTreeMap<String, String>,
    #[serde(default)]
    home_files: BTreeMap<String, String>,
    steps: Vec<Step>,
}

#[derive(Deserialize, Clone)]
struct Step {
    name: String,
    #[serde(default)]
    method: String,
    #[serde(default)]
    auth: String,
    #[serde(default)]
    args: Vec<String>,
    #[serde(default)]
    stdin: String,
    raw_body: Option<String>,
    #[serde(default)]
    compare: String,
    #[serde(default)]
    reason: String,
    status: u16,
    content_type: String,
    body: String,
    vault_files: Vec<String>,
    outside_files: Vec<String>,
}

fn fixture() -> Fixture {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/cli/remote-command-process.json"
    ))
    .expect("decode Go-owned remote command fixture");
    assert_eq!(fixture.schema_version, 1);
    fixture
}

struct Server {
    child: Child,
    address: String,
}

impl Drop for Server {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn start_server(binary: &str, root: &Path, vault: &Path, home: &Path, cwd: &Path) -> Server {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind loopback");
    let address = listener.local_addr().expect("local address").to_string();
    drop(listener);
    let _ = root;
    let child =
        Command::new(binary)
            .current_dir(cwd)
            .isolated_env()
            .envs(std::env::vars().filter(|(name, _)| {
                matches!(name.as_str(), "PATH" | "SYSTEMROOT" | "TEMP" | "TMP")
            }))
            .env("TZ", "UTC")
            .env("HOME", home)
            .env("USERPROFILE", home)
            .env("XDG_CONFIG_HOME", home.join("config"))
            .env("XDG_CACHE_HOME", home.join("cache"))
            .env("XDG_DATA_HOME", home.join("data"))
            .env("SYMDESK_SERVER_TOKEN", ADMIN_TOKEN)
            .env("SYMDESK_WORKER_TOKEN", WORKER_TOKEN)
            .arg("--vault")
            .arg(vault)
            .args(["serve", "--listen", &address])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .expect("spawn Rust symdesk serve");
    let mut server = Server { child, address };
    let deadline = Instant::now() + Duration::from_secs(30);
    while Instant::now() < deadline {
        if TcpStream::connect_timeout(
            &server.address.parse().expect("socket address"),
            Duration::from_millis(200),
        )
        .is_ok()
        {
            return server;
        }
        if let Ok(Some(status)) = server.child.try_wait() {
            panic!("Rust server exited early with {status}");
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    panic!("Rust server did not listen on {}", server.address);
}

struct Reply {
    status: u16,
    content_type: String,
    body: String,
}

fn decode_chunked(mut data: &[u8]) -> Vec<u8> {
    let mut out = Vec::new();
    loop {
        let Some(line_end) = data.windows(2).position(|window| window == b"\r\n") else {
            return out;
        };
        let size_text = std::str::from_utf8(&data[..line_end]).expect("chunk size");
        let size = usize::from_str_radix(size_text.split(';').next().unwrap_or("0").trim(), 16)
            .expect("chunk size hex");
        data = &data[line_end + 2..];
        if size == 0 {
            return out;
        }
        out.extend_from_slice(&data[..size]);
        data = &data[size + 2..];
    }
}

fn request(address: &str, step: &Step) -> Reply {
    let method = if step.method.is_empty() {
        "POST"
    } else {
        &step.method
    };
    let body = step.raw_body.clone().unwrap_or_else(|| {
        let mut payload = serde_json::json!({ "arguments": step.args });
        if !step.stdin.is_empty() {
            payload["stdin"] = serde_json::Value::String(step.stdin.clone());
        }
        payload.to_string()
    });
    let authorization = match step.auth.as_str() {
        "none" => String::new(),
        "worker" => format!("Authorization: Bearer {WORKER_TOKEN}\r\n"),
        _ => format!("Authorization: Bearer {ADMIN_TOKEN}\r\n"),
    };
    let head = format!(
        "{method} /api/v1/command HTTP/1.1\r\nHost: {address}\r\nContent-Type: application/json\r\n{authorization}Content-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    );
    let mut stream = TcpStream::connect(address).expect("connect to Rust server");
    stream
        .set_read_timeout(Some(Duration::from_secs(120)))
        .expect("read timeout");
    stream.write_all(head.as_bytes()).expect("write head");
    // A server may answer and close before an oversized body is consumed.
    let _ = stream.write_all(body.as_bytes());
    let mut raw = Vec::new();
    let _ = stream.read_to_end(&mut raw);
    let split = raw
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .unwrap_or_else(|| panic!("{}: no HTTP response head", step.name));
    let head = String::from_utf8_lossy(&raw[..split]).into_owned();
    let mut lines = head.lines();
    let status: u16 = lines
        .next()
        .and_then(|line| line.split_whitespace().nth(1))
        .and_then(|code| code.parse().ok())
        .expect("status line");
    let mut content_type = String::new();
    let mut chunked = false;
    for line in lines {
        let lower = line.to_ascii_lowercase();
        if let Some(value) = lower.strip_prefix("content-type:") {
            content_type = line[line.len() - value.len()..].trim().to_owned();
        }
        if lower.starts_with("transfer-encoding:") && lower.contains("chunked") {
            chunked = true;
        }
    }
    let payload = &raw[split + 4..];
    let body = if chunked {
        decode_chunked(payload)
    } else {
        payload.to_vec()
    };
    Reply {
        status,
        content_type,
        body: String::from_utf8_lossy(&body).into_owned(),
    }
}

fn list_files(directory: &Path, base: &Path, skip_hidden: bool, out: &mut Vec<String>) {
    let Ok(entries) = fs::read_dir(directory) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        let name = entry.file_name().to_string_lossy().into_owned();
        if path.is_dir() {
            if !(skip_hidden && name.starts_with('.')) {
                list_files(&path, base, skip_hidden, out);
            }
        } else {
            out.push(
                path.strip_prefix(base)
                    .expect("below base")
                    .to_string_lossy()
                    .replace('\\', "/"),
            );
        }
    }
}

fn vault_files(vault: &Path) -> Vec<String> {
    let mut files = Vec::new();
    list_files(vault, vault, true, &mut files);
    files.sort();
    files
}

fn outside_files(root: &Path) -> Vec<String> {
    let mut files = Vec::new();
    for entry in fs::read_dir(root).expect("case root").flatten() {
        let name = entry.file_name().to_string_lossy().into_owned();
        if matches!(name.as_str(), "vault" | "home" | "cwd") {
            continue;
        }
        let path = entry.path();
        if path.is_dir() {
            list_files(&path, root, false, &mut files);
        } else {
            files.push(name);
        }
    }
    files.sort();
    files
}

fn closed_port() -> String {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind loopback");
    let port = listener.local_addr().expect("local address").port();
    drop(listener);
    port.to_string()
}

fn write_files(vault: &Path, files: &BTreeMap<String, String>) {
    for (name, content) in files {
        let target = vault.join(name);
        fs::create_dir_all(target.parent().expect("file parent")).expect("make parent");
        fs::write(target, content).expect("write vault file");
    }
}

fn date_normalized(text: &str) -> String {
    cli_normalize::normalize(
        text,
        Path::new("/nonexistent-root"),
        Path::new("/nonexistent-root"),
        true,
    )
}

/// Whether `step` and `reply` agree under the step's comparison mode.
fn agrees(step: &Step, reply: &Reply, root: &Path, canonical: &Path) -> bool {
    if step.compare == "decision" {
        return (reply.status == 403) == (step.status == 403)
            && (step.status != 403 || reply.body == step.body);
    }
    let body = cli_normalize::normalize(&reply.body, root, canonical, true);
    reply.status == step.status && reply.content_type == step.content_type && body == step.body
}

#[test]
fn remote_command_allowlist_matches_go_server_over_loopback() {
    let fixture = fixture();
    assert_eq!(fixture.cases.len(), EXPECTED_CASES);
    let steps: Vec<&Step> = fixture.cases.iter().flat_map(|case| &case.steps).collect();
    assert_eq!(steps.len(), EXPECTED_STEPS);
    assert_eq!(
        steps
            .iter()
            .filter(|step| step.compare == "decision")
            .count(),
        EXPECTED_DECISION_STEPS,
        "the decision-only residual is explicit and bounded"
    );
    assert!(
        steps
            .iter()
            .filter(|step| step.compare == "decision")
            .all(|step| !step.reason.is_empty()),
        "every decision-only step names its reason"
    );
    assert_eq!(
        steps.iter().filter(|step| step.status == 403).count(),
        EXPECTED_FORBIDDEN,
        "the recorded suite keeps its rejections"
    );
    let binary = env!("CARGO_BIN_EXE_symdesk");
    let base: PathBuf = std::env::temp_dir().join(format!(
        "symdesk-remote-command-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock after epoch")
            .as_nanos()
    ));
    fs::create_dir(&base).expect("make isolated test directory");

    let (mut executed, mut rejected, mut exact, mut decision) = (0, 0, 0, 0);
    for case in &fixture.cases {
        let root = base.join(&case.name);
        fs::create_dir_all(&root).expect("make case root");
        let root = process_root::canonical_root(&root);
        let (vault, home, cwd) = (root.join("vault"), root.join("home"), root.join("cwd"));
        for directory in [&vault, &home, &cwd] {
            fs::create_dir_all(directory).expect("make case directory");
        }
        let canonical = fs::canonicalize(&root).expect("canonical case root");
        write_files(&vault, &case.files);
        let port = closed_port();
        let embeddings = embedding_fake::EmbeddingFake::start();
        write_files(
            &home,
            &case
                .home_files
                .iter()
                .map(|(name, content)| {
                    (
                        name.clone(),
                        content
                            .replace("$CLOSED_PORT", &port)
                            .replace("$EMBED_URL", &embeddings.url),
                    )
                })
                .collect(),
        );
        let server = start_server(binary, &root, &vault, &home, &cwd);
        let root_text = root.to_string_lossy().into_owned();
        for step in &case.steps {
            let mut materialized = step.clone();
            materialized.args = step
                .args
                .iter()
                .map(|argument| process_root::expand_argument(argument, &root_text))
                .collect();
            let reply = request(&server.address, &materialized);
            for token in [ADMIN_TOKEN, WORKER_TOKEN] {
                assert!(
                    !reply.body.contains(token),
                    "{} leaks a credential",
                    step.name
                );
            }
            assert!(
                agrees(step, &reply, &root, &canonical),
                "{}/{}: status {} (want {}), content-type {:?} (want {:?})\nbody {:?}\nwant {:?}",
                case.name,
                step.name,
                reply.status,
                step.status,
                reply.content_type,
                step.content_type,
                reply.body,
                step.body
            );
            if step.compare != "decision" {
                let mut files = vault_files(&vault);
                for file in &mut files {
                    *file = date_normalized(file);
                }
                assert_eq!(
                    files, step.vault_files,
                    "{}/{} vault files",
                    case.name, step.name
                );
                assert_eq!(
                    outside_files(&root),
                    step.outside_files,
                    "{}/{} files outside the vault",
                    case.name,
                    step.name
                );
                exact += 1;
            } else {
                decision += 1;
            }
            if step.status == 403 {
                rejected += 1;
            }
            executed += 1;
        }
    }
    assert_eq!(executed, EXPECTED_STEPS, "every fixture step ran");
    assert_eq!(decision, EXPECTED_DECISION_STEPS);
    assert_eq!(exact, EXPECTED_STEPS - EXPECTED_DECISION_STEPS);
    assert_eq!(rejected, EXPECTED_FORBIDDEN, "rejections were replayed");
    let _ = fs::remove_dir_all(base);
}

/// Negative control: the comparison must reject a changed status, content
/// type and body, and a flipped allow/deny decision.
#[test]
fn comparison_rejects_tampered_go_observations() {
    let fixture = fixture();
    let root = Path::new("/nonexistent-root");
    let step = fixture
        .cases
        .iter()
        .flat_map(|case| &case.steps)
        .find(|step| step.status == 200 && step.compare.is_empty())
        .expect("an exact accepted step")
        .clone();
    let reply = Reply {
        status: step.status,
        content_type: step.content_type.clone(),
        body: step.body.clone(),
    };
    assert!(agrees(&step, &reply, root, root));
    let mut changed = Reply {
        status: 422,
        content_type: reply.content_type.clone(),
        body: reply.body.clone(),
    };
    assert!(!agrees(&step, &changed, root, root), "changed status");
    changed = Reply {
        status: 200,
        content_type: "text/plain".to_owned(),
        body: reply.body.clone(),
    };
    assert!(!agrees(&step, &changed, root, root), "changed content type");
    changed = Reply {
        status: 200,
        content_type: reply.content_type.clone(),
        body: format!("{}x", reply.body),
    };
    assert!(!agrees(&step, &changed, root, root), "changed body");
    let denied = fixture
        .cases
        .iter()
        .flat_map(|case| &case.steps)
        .find(|step| step.status == 403)
        .expect("a rejected step")
        .clone();
    let mut decision = denied.clone();
    decision.compare = "decision".to_owned();
    let allowed = Reply {
        status: 200,
        content_type: String::new(),
        body: String::new(),
    };
    assert!(
        !agrees(&decision, &allowed, root, root),
        "an allowed command must not replay a 403"
    );
    let same_denial = Reply {
        status: 403,
        content_type: denied.content_type.clone(),
        body: denied.body.clone(),
    };
    assert!(agrees(&decision, &same_denial, root, root));
}

#[test]
fn recorded_responses_never_contain_server_credentials() {
    let fixture = fixture();
    for case in &fixture.cases {
        for step in &case.steps {
            assert!(
                !step.body.contains(ADMIN_TOKEN),
                "{}/{}",
                case.name,
                step.name
            );
            assert!(
                !step.body.contains(WORKER_TOKEN),
                "{}/{}",
                case.name,
                step.name
            );
        }
    }
}
