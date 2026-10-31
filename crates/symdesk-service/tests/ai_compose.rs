//! Replays the Go `ai.Ask`/`Transform`/`PromptOne` composition surface,
//! recorded with a loopback `hermes` shell double (`make ai-compose-fixtures-generate`).
#![cfg(unix)]

use std::{
    collections::BTreeMap,
    fs,
    os::unix::fs::PermissionsExt,
    path::PathBuf,
    sync::atomic::{AtomicU32, Ordering},
};

use serde::Deserialize;
use symdesk_core::config::Config;
use symdesk_service::ai::{self, Context, Doc};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    hermes_script: String,
    cases: Vec<Case>,
}

#[derive(Deserialize, Clone)]
struct Case {
    id: String,
    op: String,
    provider: String,
    #[serde(default)]
    session: String,
    #[serde(default)]
    language: String,
    #[serde(default)]
    api_key: String,
    #[serde(default)]
    mode: String,
    #[serde(default)]
    no_binary: bool,
    #[serde(default)]
    not_executable: bool,
    #[serde(default)]
    query: String,
    #[serde(default)]
    text: String,
    #[serde(default)]
    intent: String,
    #[serde(default)]
    docs: Vec<DocCase>,
    chunks: Vec<String>,
    #[serde(default)]
    error: String,
    #[serde(default)]
    result: String,
    #[serde(default)]
    log: String,
}

#[derive(Deserialize, Clone)]
struct DocCase {
    path: String,
    title: String,
    snippet: String,
}

static COUNTER: AtomicU32 = AtomicU32::new(0);

fn fixture() -> Fixture {
    serde_json::from_str(include_str!("../../../testdata/port/ai/ai-compose.json"))
        .expect("decode Go-owned AI composition fixture")
}

struct Observed {
    chunks: Vec<String>,
    error: String,
    result: String,
    log: String,
}

fn replay(case: &Case, script: &str) -> Observed {
    let root = std::env::temp_dir().join(format!(
        "symdesk-ai-compose-{}-{}",
        std::process::id(),
        COUNTER.fetch_add(1, Ordering::SeqCst)
    ));
    let bin = root.join("bin");
    fs::create_dir_all(&bin).unwrap();
    if !case.no_binary {
        fs::write(bin.join("hermes"), script).unwrap();
        let mode = if case.not_executable { 0o600 } else { 0o700 };
        fs::set_permissions(bin.join("hermes"), fs::Permissions::from_mode(mode)).unwrap();
    }
    let log_path: PathBuf = root.join("hermes.log");
    let path = format!("{}:/usr/bin:/bin", bin.display());
    let environment = BTreeMap::from([
        ("PATH".to_owned(), path.clone()),
        ("HOME".to_owned(), root.join("home").display().to_string()),
    ]);
    let child_env = BTreeMap::from([
        ("PATH".to_owned(), path),
        ("HOME".to_owned(), root.join("home").display().to_string()),
        ("FAKE_HERMES_LOG".to_owned(), log_path.display().to_string()),
        ("FAKE_HERMES_MODE".to_owned(), case.mode.clone()),
    ]);
    let mut config = Config::default();
    config.llm_provider.clone_from(&case.provider);
    config.hermes_session.clone_from(&case.session);
    config.language.clone_from(&case.language);
    config.apply_environment(&BTreeMap::from([(
        "SYMDESK_LLM_API_KEY".to_owned(),
        case.api_key.clone(),
    )]));
    // Mirrors the double of Go's secrets.ResolveKey without symvault or keychain.
    let mut resolve_key = |reference: &str| {
        if reference.starts_with("op://") {
            String::new()
        } else {
            reference.to_owned()
        }
    };
    let mut context = Context::new(&config, &environment, &mut resolve_key);
    context.child_env = Some(&child_env);
    let mut chunks = Vec::new();
    let mut error = String::new();
    let mut result = String::new();
    match case.op.as_str() {
        "ask" => {
            let docs: Vec<Doc> = case
                .docs
                .iter()
                .map(|doc| Doc {
                    path: doc.path.clone(),
                    title: doc.title.clone(),
                    snippet: doc.snippet.clone(),
                })
                .collect();
            ai::ask(&mut context, &case.query, &docs, &mut |chunk| {
                chunks.push(chunk.to_owned());
            });
        }
        "transform" => ai::transform(&mut context, &case.text, &case.intent, &mut |chunk| {
            chunks.push(chunk.to_owned());
        }),
        "prompt_one" => match ai::prompt_one(&mut context, &case.text) {
            Ok(value) => result = value,
            Err(failure) => error = failure,
        },
        other => panic!("unknown op {other}"),
    }
    let log = fs::read_to_string(&log_path).unwrap_or_default();
    fs::remove_dir_all(&root).unwrap();
    Observed {
        chunks,
        error,
        result,
        log,
    }
}

#[test]
fn ai_composition_matches_go_oracle() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 40);
    let mut hermes_runs = 0;
    for case in &fixture.cases {
        let observed = replay(case, &fixture.hermes_script);
        assert_eq!(observed.chunks, case.chunks, "{}: chunks", case.id);
        assert_eq!(observed.error, case.error, "{}: error", case.id);
        assert_eq!(observed.result, case.result, "{}: result", case.id);
        assert_eq!(
            observed.log, case.log,
            "{}: hermes argv/stdin transcript",
            case.id
        );
        if !case.log.is_empty() {
            hermes_runs += 1;
        }
    }
    assert!(
        hermes_runs >= 25,
        "the Hermes child was exercised {hermes_runs} times"
    );
}

#[test]
fn replay_detects_a_tampered_oracle_expectation() {
    let fixture = fixture();
    let mut case = fixture
        .cases
        .iter()
        .find(|case| case.id == "ask-hermes-ok")
        .cloned()
        .unwrap();
    case.chunks = vec!["a different answer".to_owned()];
    let observed = replay(&case, &fixture.hermes_script);
    assert_ne!(observed.chunks, case.chunks);
}

#[test]
fn hermes_deadline_kills_the_child_and_names_the_go_bound() {
    let fixture = fixture();
    let case = Case {
        mode: "sleep".to_owned(),
        ..fixture
            .cases
            .iter()
            .find(|case| case.id == "prompt-one-hermes-ok")
            .cloned()
            .unwrap()
    };
    let script = format!(
        "{}\n",
        fixture.hermes_script.replace(
            "  ok) printf 'the answer' ;;",
            "  sleep) sleep 30 ;;\n  ok) printf 'the answer' ;;"
        )
    );
    let root = std::env::temp_dir().join(format!("symdesk-ai-timeout-{}", std::process::id()));
    let bin = root.join("bin");
    fs::create_dir_all(&bin).unwrap();
    fs::write(bin.join("hermes"), script).unwrap();
    fs::set_permissions(bin.join("hermes"), fs::Permissions::from_mode(0o700)).unwrap();
    let path = format!("{}:/usr/bin:/bin", bin.display());
    let environment = BTreeMap::from([("PATH".to_owned(), path.clone())]);
    let child_env = BTreeMap::from([
        ("PATH".to_owned(), path),
        (
            "FAKE_HERMES_LOG".to_owned(),
            root.join("log").display().to_string(),
        ),
        ("FAKE_HERMES_MODE".to_owned(), case.mode.clone()),
    ]);
    let mut config = Config::default();
    "hermes".clone_into(&mut config.llm_provider);
    let mut resolve_key = |_: &str| String::new();
    let mut context = Context::new(&config, &environment, &mut resolve_key);
    context.child_env = Some(&child_env);
    context.hermes_timeout = std::time::Duration::from_millis(300);
    let started = std::time::Instant::now();
    let error = ai::prompt_one(&mut context, "p").unwrap_err();
    assert_eq!(error, "hermes backend timed out after 5m0s");
    assert!(
        started.elapsed() < std::time::Duration::from_secs(10),
        "child must be killed"
    );
    fs::remove_dir_all(root).unwrap();
}
