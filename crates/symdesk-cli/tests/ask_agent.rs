//! Replays `symdesk ask --agent` and `ask --notebook` against the process
//! fixture recorded from the real Go CLI (`make ask-agent-fixtures-generate`):
//! tool loop, externalizer, citation validation, notebook-scoped tools and the
//! event stream. Providers are loopback fakes serving the Go-scripted turns, so
//! the provider requests the CLI builds are compared too. Retrieval scores are
//! masked: hybrid ranking arithmetic differs per host and is pinned by the
//! retrieval differentials.
#![cfg(unix)]

use std::{
    collections::BTreeMap,
    fs,
    os::unix::fs::PermissionsExt,
    process::Command,
    sync::atomic::{AtomicUsize, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

#[path = "support/provider.rs"]
mod provider;
use provider::{FakeProvider, KEY, Turn};
#[path = "support/ask_replay.rs"]
mod ask_replay;
use ask_replay::{Request, compare_requests, normalize, parse_time, without_log_lines};

static ROOT_SEQUENCE: AtomicUsize = AtomicUsize::new(0);

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    hermes_script: String,
    files: BTreeMap<String, String>,
    file_time: String,
    cases: Vec<Case>,
}

#[derive(Deserialize, Clone)]
struct Case {
    name: String,
    args: Vec<String>,
    provider: String,
    #[serde(default)]
    mode: String,
    #[serde(default)]
    env: BTreeMap<String, String>,
    #[serde(default)]
    turns: Vec<TurnSpec>,
    exit_code: i32,
    stdout: String,
    stderr: String,
    #[serde(default)]
    requests: Vec<Request>,
    #[serde(default)]
    results: Vec<ResultFile>,
    #[serde(default)]
    log: String,
}

#[derive(Deserialize, Clone)]
struct TurnSpec {
    #[serde(default)]
    status: u16,
    body: String,
}

#[derive(Deserialize, Clone, PartialEq, Debug)]
struct ResultFile {
    digest_matches_vault: bool,
    name: String,
    size: usize,
    sha256: String,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/ai/ask-agent-process.json"
    ))
    .expect("decode Go-owned ask agent fixture")
}

fn run_replay(case: &Case, fixture: &Fixture) -> Result<(), String> {
    let root = fs::canonicalize(std::env::temp_dir())
        .unwrap()
        .join(format!(
            "symdesk-ask-agent-{}-{}-{}",
            case.name,
            std::process::id(),
            SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap()
                .as_nanos()
                + ROOT_SEQUENCE.fetch_add(1, Ordering::Relaxed) as u128
        ));
    let (home, bin, vault) = (root.join("home"), root.join("bin"), root.join("vault"));
    for dir in [&home, &bin, &vault] {
        fs::create_dir_all(dir).unwrap();
    }
    // Retrieval embeddings point at a closed port; keep the retry loop of the
    // failing embedder instant so setup does not depend on how long the host
    // takes to refuse a connection.
    let seek_config = home.join(".config/symseek");
    fs::create_dir_all(&seek_config).unwrap();
    fs::write(
        seek_config.join("config.toml"),
        "retry_count = 1\nretry_backoff_ms = 1\n",
    )
    .unwrap();
    let stamp = parse_time(&fixture.file_time);
    for (name, content) in &fixture.files {
        let dest = vault.join(name);
        fs::create_dir_all(dest.parent().unwrap()).unwrap();
        fs::write(&dest, content).unwrap();
        fs::File::options()
            .write(true)
            .open(&dest)
            .unwrap()
            .set_modified(stamp)
            .unwrap();
    }
    if case.provider == "hermes" {
        fs::write(bin.join("hermes"), &fixture.hermes_script).unwrap();
        fs::set_permissions(bin.join("hermes"), fs::Permissions::from_mode(0o700)).unwrap();
    }
    let log = root.join("hermes.log");
    let provider = matches!(case.provider.as_str(), "ollama" | "anthropic").then(|| {
        FakeProvider::start(
            case.turns
                .iter()
                .map(|turn| Turn {
                    status: turn.status,
                    body: turn.body.clone(),
                    stall: false,
                    delay_ms: 0,
                })
                .collect(),
        )
    });
    let command = |extra: &BTreeMap<String, String>| {
        let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        command
            .current_dir(&root)
            .env_clear()
            .env("PATH", format!("{}:/usr/bin:/bin", bin.display()))
            .env("HOME", &home)
            .env("TZ", "UTC")
            .env("XDG_CONFIG_HOME", home.join("config"))
            .env("XDG_CACHE_HOME", home.join("cache"))
            .env("XDG_DATA_HOME", home.join("data"))
            .env("FAKE_HERMES_LOG", &log)
            .env("FAKE_HERMES_MODE", &case.mode)
            .envs(extra);
        command
    };
    let closed = BTreeMap::from([(
        "SYMDESK_OLLAMA_URL".to_owned(),
        "http://127.0.0.1:9".to_owned(),
    )]);
    let setup = command(&closed)
        .arg("--vault")
        .arg(&vault)
        .arg("index")
        .output()
        .unwrap();
    assert!(
        setup.status.success(),
        "{}: index: {}",
        case.name,
        String::from_utf8_lossy(&setup.stderr)
    );
    let _ = fs::remove_file(&log);

    let mut env = BTreeMap::new();
    if case.provider != "none" {
        env.insert(
            "SYMDESK_OLLAMA_URL".to_owned(),
            "http://127.0.0.1:9".to_owned(),
        );
    }
    match (&case.provider[..], &provider) {
        ("ollama", Some(provider)) => {
            env.insert("SYMDESK_LLM_PROVIDER".to_owned(), "ollama".to_owned());
            env.insert("SYMDESK_OLLAMA_URL".to_owned(), provider.url.clone());
        }
        ("anthropic", Some(provider)) => {
            env.insert("SYMDESK_LLM_PROVIDER".to_owned(), "anthropic".to_owned());
            env.insert(
                "SYMDESK_ANTHROPIC_URL".to_owned(),
                format!("{}/v1/messages", provider.url),
            );
            env.insert("SYMDESK_LLM_API_KEY".to_owned(), KEY.to_owned());
        }
        ("hermes", _) => {
            env.insert("SYMDESK_LLM_PROVIDER".to_owned(), "hermes".to_owned());
        }
        _ => {}
    }
    env.extend(case.env.clone());
    let output = command(&env)
        .arg("--vault")
        .arg(&vault)
        .args(&case.args)
        .output()
        .unwrap();
    let root_text = root.to_string_lossy().into_owned();
    let url = provider.as_ref().map(|p| p.url.clone()).unwrap_or_default();
    let stdout = normalize(&String::from_utf8_lossy(&output.stdout), &root_text, &url);
    let stderr = normalize(
        &without_log_lines(&String::from_utf8_lossy(&output.stderr)),
        &root_text,
        &url,
    );
    let transcript = fs::read_to_string(&log)
        .unwrap_or_default()
        .replace(&root_text, "<ROOT>");
    let requests = provider
        .as_ref()
        .map(FakeProvider::requests)
        .unwrap_or_default();
    let results = observe_results(&home.join("data"), &vault);
    let _ = fs::remove_dir_all(&root);

    let label = &case.name;
    if output.status.code() != Some(case.exit_code) {
        return Err(format!(
            "{label}: exit {:?} != {}\nstdout: {stdout}\nstderr: {stderr}",
            output.status.code(),
            case.exit_code
        ));
    }
    if stdout != case.stdout {
        return Err(format!(
            "{label}: stdout\nrust: {stdout:?}\ngo:   {:?}",
            case.stdout
        ));
    }
    if stderr != case.stderr {
        return Err(format!("{label}: stderr {stderr:?} != {:?}", case.stderr));
    }
    if transcript != case.log {
        return Err(format!(
            "{label}: hermes transcript\nrust: {transcript:?}\ngo:   {:?}",
            case.log
        ));
    }
    compare_requests(label, &case.requests, &requests, &root_text, &url)?;
    if results != case.results {
        return Err(format!(
            "{label}: externalized results {results:?} != {:?}",
            case.results
        ));
    }
    Ok(())
}

fn observe_results(data: &std::path::Path, vault: &std::path::Path) -> Vec<ResultFile> {
    let canonical = fs::canonicalize(vault).unwrap();
    let want = &symdesk_vault::sha256_hex(canonical.to_string_lossy().as_bytes())[..16];
    let base = data.join("symdesk/agent-results");
    let mut out = Vec::new();
    for digest in fs::read_dir(&base).into_iter().flatten().flatten() {
        for task in fs::read_dir(digest.path()).into_iter().flatten().flatten() {
            for file in fs::read_dir(task.path()).into_iter().flatten().flatten() {
                let bytes = fs::read(file.path()).unwrap();
                out.push(ResultFile {
                    digest_matches_vault: digest.file_name().to_string_lossy() == want,
                    name: file.file_name().to_string_lossy().into_owned(),
                    size: bytes.len(),
                    sha256: symdesk_vault::sha256_hex(&bytes),
                });
            }
        }
    }
    out.sort_by(|a, b| a.name.cmp(&b.name));
    out
}

#[test]
fn ask_agent_matches_go_process_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.cases.len(),
        25,
        "every recorded Go case is replayed"
    );
    let failures: Vec<String> = fixture
        .cases
        .iter()
        .filter_map(|case| run_replay(case, &fixture).err())
        .collect();
    assert!(failures.is_empty(), "{}", failures.join("\n---\n"));
}

#[test]
fn replay_rejects_tampered_expectations() {
    let fixture = fixture();
    let case = |name: &str| {
        fixture
            .cases
            .iter()
            .find(|case| case.name == name)
            .cloned()
            .unwrap()
    };
    // A changed provider request must be noticed.
    let mut tampered = case("ollama_ls_json");
    tampered.requests[1].body = Some(serde_json::json!({"model": "tampered"}));
    assert!(
        run_replay(&tampered, &fixture)
            .unwrap_err()
            .contains("request 1 body")
    );
    // A changed externalized file must be noticed.
    let mut tampered = case("ollama_externalize");
    tampered.results[0].sha256 = "0".repeat(64);
    assert!(
        run_replay(&tampered, &fixture)
            .unwrap_err()
            .contains("externalized results")
    );
    // A changed event stream must be noticed.
    let mut tampered = case("anthropic_tool_use_json");
    tampered.stdout.push_str("tamper\n");
    assert!(
        run_replay(&tampered, &fixture)
            .unwrap_err()
            .contains("stdout")
    );
}
