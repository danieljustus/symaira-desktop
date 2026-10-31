//! Replays Go `secrets.ResolveKey`/`Source` resolution order against loopback
//! `security` and `symvault` doubles (`make secrets-resolve-fixtures-generate`),
//! and scans every observable surface for the synthetic canary (SEC-002).
#![cfg(unix)]

use std::{
    fs,
    os::unix::fs::PermissionsExt,
    sync::atomic::{AtomicU32, Ordering},
};

use serde::Deserialize;
use symdesk_core::{compose::Resolver, compose_probe::ToolCache};
use symdesk_service::secrets::{Environment, resolve_key_in, source_in};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    canary: String,
    double_script: String,
    cases: Vec<Case>,
}

#[derive(Deserialize, Clone)]
struct Case {
    id: String,
    #[serde(rename = "ref")]
    reference: String,
    #[serde(default)]
    env_key: String,
    #[serde(default)]
    security: String,
    #[serde(default)]
    symvault: String,
    key: String,
    source: String,
    #[serde(default)]
    log: String,
    canary_leaks: usize,
}

static COUNTER: AtomicU32 = AtomicU32::new(0);

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/ai/secrets-resolve.json"
    ))
    .expect("decode Go-owned secrets fixture")
}

struct Observed {
    key: String,
    source: String,
    log: String,
}

fn replay(case: &Case, script: &str) -> Observed {
    let root = fs::canonicalize(std::env::temp_dir())
        .unwrap()
        .join(format!(
            "symdesk-secrets-{}-{}",
            std::process::id(),
            COUNTER.fetch_add(1, Ordering::SeqCst)
        ));
    let bin = root.join("bin");
    fs::create_dir_all(&bin).unwrap();
    let log_path = root.join("tools.log");
    // Each double exports its own parameters, so this test never mutates the
    // process environment (the parameters are not secrets).
    let install = |name: &str, mode: &str| {
        let body = script.replacen(
            "\n",
            &format!(
                "\nexport FAKE_SECRETS_LOG='{}' FAKE_SECURITY_MODE='{}' FAKE_SYMVAULT_MODE='{}'\n",
                log_path.display(),
                if name == "security" { mode } else { "" },
                if name == "symvault" { mode } else { "" },
            ),
            1,
        );
        let path = bin.join(name);
        fs::write(&path, body).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
    };
    if !case.security.is_empty() {
        install("security", &case.security);
    }
    if !case.symvault.is_empty() {
        install("symvault", &case.symvault);
    }
    let path = format!("{}:/usr/bin:/bin", bin.display());
    let resolver = Resolver {
        home: root.join("home").display().to_string(),
        symaira_bin: String::new(),
        path: path.clone(),
        cwd: root.clone(),
        allow_relative_path: false,
    };
    let tools = ToolCache::default();
    let environment = Environment {
        api_key: (!case.env_key.is_empty()).then(|| case.env_key.clone()),
        resolver: &resolver,
        tools: &tools,
        path: &path,
    };
    let key = resolve_key_in(&case.reference, &environment);
    let source = source_in(&case.reference, &environment);
    let log = fs::read_to_string(&log_path)
        .unwrap_or_default()
        .replace(root.to_str().unwrap(), "<ROOT>");
    fs::remove_dir_all(&root).unwrap();
    Observed { key, source, log }
}

fn leaks(canary: &str, observed: &Observed) -> usize {
    [&observed.source, &observed.log]
        .iter()
        .map(|surface| surface.matches(canary).count())
        .sum()
}

#[test]
fn secret_resolution_matches_go_oracle_and_never_leaks_the_canary() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 14);
    for case in &fixture.cases {
        let observed = replay(case, &fixture.double_script);
        assert_eq!(observed.key, case.key, "{}: resolved key", case.id);
        assert_eq!(observed.source, case.source, "{}: source label", case.id);
        assert_eq!(observed.log, case.log, "{}: tool transcript", case.id);
        let found = leaks(&fixture.canary, &observed);
        assert_eq!(found, case.canary_leaks, "{}: canary leak count", case.id);
        assert_eq!(
            found, 0,
            "{}: the resolved secret reached a diagnostic surface",
            case.id
        );
    }
}

#[test]
fn the_canary_scanner_detects_a_planted_leak() {
    let fixture = fixture();
    let planted = Observed {
        key: String::new(),
        source: format!("symvault {}", fixture.canary),
        log: String::new(),
    };
    assert_eq!(leaks(&fixture.canary, &planted), 1);
    let case = fixture
        .cases
        .iter()
        .find(|case| case.id == "symvault-ok")
        .unwrap();
    let observed = replay(case, &fixture.double_script);
    assert_eq!(
        observed.key, fixture.canary,
        "the secret itself is only the return value"
    );
}
