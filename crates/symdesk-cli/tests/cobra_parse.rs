//! Cobra/pflag front half of the CLI (CLI-001..CLI-004), replayed against real
//! Go processes: command resolution, flag grammar at every argv position,
//! argument counts, required/exclusive flags, `--help`/`--version`, generated
//! `help`/`completion`/`__complete` and every diagnostic.
#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

use serde::Deserialize;
use std::{
    collections::BTreeMap,
    fs,
    path::Path,
    process::Command,
    sync::{
        Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    time::{SystemTime, UNIX_EPOCH},
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    binary_version: String,
    root_registration_order: Vec<String>,
    command_paths: usize,
    family_counts: BTreeMap<String, usize>,
    cases: Vec<Case>,
}

#[derive(Deserialize, Clone)]
struct Case {
    family: String,
    args: Vec<String>,
    #[serde(default)]
    env: BTreeMap<String, String>,
    #[serde(default)]
    files: BTreeMap<String, String>,
    exit_code: i32,
    stdout: String,
    stderr: String,
}

#[derive(Deserialize)]
struct Documents {
    help: BTreeMap<String, String>,
    completion: BTreeMap<String, String>,
    root_usage: String,
}

fn resolve(documents: &Documents, text: &str) -> String {
    if let Some(key) = text.strip_prefix("@help:") {
        return documents.help[key].clone();
    }
    if let Some(key) = text.strip_prefix("@completion:") {
        return documents.completion[key].clone();
    }
    if let Some(prefix) = text.strip_suffix("@root_usage") {
        return format!("{prefix}{}", documents.root_usage);
    }
    text.to_owned()
}

struct Observed {
    code: Option<i32>,
    stdout: String,
    stderr: String,
}

fn run(case: &Case, root: &Path) -> Observed {
    let home = root.join("home");
    fs::create_dir_all(&home).unwrap();
    for (relative, content) in &case.files {
        let path = home.join(relative);
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(path, content).unwrap();
    }
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    command
        .current_dir(root)
        .isolated_env()
        .env("HOME", &home)
        .env("USERPROFILE", &home)
        .env("XDG_CONFIG_HOME", home.join("config"))
        .env("XDG_DATA_HOME", home.join("data"))
        .env("XDG_CACHE_HOME", home.join("cache"))
        .env("TZ", "UTC")
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("PATH", root)
        .args(&case.args);
    for (key, value) in &case.env {
        command.env(key, value);
    }
    let output = command.output().unwrap();
    let normalize = |bytes: &[u8]| {
        String::from_utf8_lossy(bytes)
            .replace(home.to_str().unwrap(), "$HOME")
            .replace(root.to_str().unwrap(), "$ROOT")
    };
    Observed {
        code: output.status.code(),
        stdout: normalize(&output.stdout),
        stderr: normalize(&output.stderr),
    }
}

/// Compare one observation with the Go-recorded case.
fn mismatch(documents: &Documents, case: &Case, observed: &Observed) -> Option<String> {
    let stdout = resolve(documents, &case.stdout);
    let stderr = resolve(documents, &case.stderr);
    if observed.code != Some(case.exit_code) {
        return Some(format!("exit {:?} != {}", observed.code, case.exit_code));
    }
    if observed.stdout != stdout {
        return Some(format!("stdout {:?} != {:?}", observed.stdout, stdout));
    }
    if observed.stderr != stderr {
        return Some(format!("stderr {:?} != {:?}", observed.stderr, stderr));
    }
    None
}

fn load() -> (Fixture, Documents) {
    let fixture: Fixture = serde_json::from_slice(include_bytes!(
        "../../../testdata/port/cli/cobra-parse-process.json"
    ))
    .expect("Go process fixture");
    let documents: Documents = serde_json::from_slice(include_bytes!(
        "../../../testdata/port/cli/help-process.json"
    ))
    .expect("Go help documents");
    (fixture, documents)
}

fn scratch(name: &str) -> std::path::PathBuf {
    let root = std::env::temp_dir().join(format!(
        "symdesk-{name}-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    fs::create_dir_all(&root).unwrap();
    root
}

#[test]
fn every_cobra_parse_case_matches_the_go_process() {
    let (fixture, documents) = load();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.binary_version, "devel");
    assert_eq!(fixture.command_paths, 207);
    assert_eq!(fixture.root_registration_order.len(), 48);
    assert_eq!(
        format!("{}\n", fixture.root_registration_order.join("\n")),
        include_str!("../../../testdata/port/cli/cobra-root-order.txt"),
        "root registration order drift"
    );
    let total: usize = fixture.family_counts.values().sum();
    assert_eq!(total, fixture.cases.len());
    assert!(total > 5000, "fixture shrank to {total} cases");
    for (family, count) in &fixture.family_counts {
        let actual = fixture.cases.iter().filter(|c| &c.family == family).count();
        assert_eq!(*count, actual, "{family}");
        assert!(actual > 0);
    }
    let base = scratch("cobra-parse");
    let next = AtomicUsize::new(0);
    let executed = AtomicUsize::new(0);
    let failures: Mutex<Vec<String>> = Mutex::new(Vec::new());
    std::thread::scope(|scope| {
        for _ in 0..8 {
            scope.spawn(|| {
                loop {
                    let index = next.fetch_add(1, Ordering::SeqCst);
                    let Some(case) = fixture.cases.get(index) else {
                        return;
                    };
                    let root = base.join(format!("c{index}"));
                    fs::create_dir_all(&root).unwrap();
                    let observed = run(case, &root);
                    fs::remove_dir_all(&root).unwrap();
                    executed.fetch_add(1, Ordering::SeqCst);
                    if let Some(reason) = mismatch(&documents, case, &observed) {
                        failures
                            .lock()
                            .unwrap()
                            .push(format!("{:?} [{}]: {reason}", case.args, case.family));
                    }
                }
            });
        }
    });
    fs::remove_dir_all(&base).unwrap();
    assert_eq!(executed.load(Ordering::SeqCst), fixture.cases.len());
    let mut failures = failures.into_inner().unwrap();
    failures.sort();
    assert!(
        failures.is_empty(),
        "{} of {} cases differ from Go; first: {}",
        failures.len(),
        fixture.cases.len(),
        failures.first().map_or("", String::as_str)
    );
}

/// Negative control: a tampered Go observation must be rejected, so the
/// replay cannot pass vacuously.
#[test]
fn tampered_go_observations_are_rejected() {
    let (fixture, documents) = load();
    let base = scratch("cobra-parse-negative");
    let mut probed = 0;
    for family in ["version", "tree", "flags", "help", "complete"] {
        let case = fixture
            .cases
            .iter()
            .find(|case| case.family == family && case.exit_code != 0 && !case.stderr.is_empty())
            .or_else(|| fixture.cases.iter().find(|case| case.family == family))
            .expect("case per family");
        let root = base.join(format!("n{probed}"));
        fs::create_dir_all(&root).unwrap();
        let observed = run(case, &root);
        assert!(
            mismatch(&documents, case, &observed).is_none(),
            "{:?} baseline",
            case.args
        );
        for tampered in [
            Case {
                exit_code: case.exit_code + 1,
                ..case.clone()
            },
            Case {
                stdout: format!("{}x", resolve(&documents, &case.stdout)),
                ..case.clone()
            },
            Case {
                stderr: format!("x{}", resolve(&documents, &case.stderr)),
                ..case.clone()
            },
        ] {
            assert!(
                mismatch(&documents, &tampered, &observed).is_some(),
                "{:?} tamper accepted",
                case.args
            );
        }
        probed += 1;
    }
    assert_eq!(probed, 5);
    fs::remove_dir_all(&base).unwrap();
}
