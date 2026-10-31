//! CLI-005 / CLI-006: replays the Go-owned output-mode and error-taxonomy
//! process fixture (`testdata/port/cli/cli-errors-process.json`) against the
//! Rust binary: every step runs in a disposable HOME/XDG/cwd with a disposable
//! vault and compares exit code, stdout and stderr byte for byte (after the
//! documented normalization of times, vault ids and the temporary root).
#![cfg(unix)]

#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

#[path = "support/process_root.rs"]
mod process_root;

#[path = "support/cli_normalize.rs"]
mod cli_normalize;

use std::{
    collections::BTreeMap,
    fs,
    net::TcpListener,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

const EXPECTED_CASES: usize = 18;
const EXPECTED_STEPS: usize = 1231;
/// Steps whose Go process exited non-zero: the negative half of the suite.
const EXPECTED_FAILING_STEPS: usize = 668;
const EXPECTED_CANARY_CASES: usize = 4;

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
    post_index_files: BTreeMap<String, String>,
    #[serde(default)]
    skip_index: bool,
    #[serde(default)]
    no_vault: bool,
    #[serde(default)]
    unix_only: bool,
    #[serde(default)]
    env: BTreeMap<String, String>,
    #[serde(default)]
    canaries: Vec<String>,
    steps: Vec<Step>,
}

#[derive(Deserialize, Clone)]
struct Step {
    args: Vec<String>,
    #[serde(default)]
    stdin: String,
    exit_code: i32,
    stdout: String,
    stderr: String,
}

fn fixture() -> Fixture {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/cli/cli-errors-process.json"
    ))
    .expect("decode Go-owned CLI errors fixture");
    assert_eq!(fixture.schema_version, 1);
    fixture
}

struct Run {
    exit: i32,
    stdout: String,
    stderr: String,
}

fn base_command(binary: &str, home: &Path, cwd: &Path) -> Command {
    let mut command = Command::new(binary);
    command
        .current_dir(cwd)
        .isolated_env()
        .envs(
            std::env::vars().filter(|(name, _)| {
                matches!(name.as_str(), "PATH" | "SYSTEMROOT" | "TEMP" | "TMP")
            }),
        )
        .env("TZ", "UTC")
        .env("HOME", home)
        .env("USERPROFILE", home)
        .env("XDG_CONFIG_HOME", home.join("config"))
        .env("XDG_CACHE_HOME", home.join("cache"))
        .env("XDG_DATA_HOME", home.join("data"));
    command
}

fn run(
    binary: &str,
    home: &Path,
    cwd: &Path,
    env: &[(String, String)],
    args: &[String],
    stdin: &str,
) -> Run {
    use std::io::Write;
    let mut command = base_command(binary, home, cwd);
    command
        .envs(env.iter().map(|(name, value)| (name, value)))
        .args(args)
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::piped())
        .stderr(std::process::Stdio::piped());
    let mut child = command.spawn().expect("spawn Rust symdesk process");
    child
        .stdin
        .take()
        .expect("stdin pipe")
        .write_all(stdin.as_bytes())
        .expect("write stdin");
    let output = child.wait_with_output().expect("wait for Rust symdesk");
    Run {
        exit: output.status.code().unwrap_or(-1),
        stdout: String::from_utf8(output.stdout).expect("UTF-8 stdout"),
        stderr: String::from_utf8(output.stderr).expect("UTF-8 stderr"),
    }
}

fn write_files(vault: &Path, files: &BTreeMap<String, String>) {
    for (name, content) in files {
        let target = vault.join(name);
        fs::create_dir_all(target.parent().expect("file parent")).expect("make parent");
        fs::write(target, content).expect("write vault file");
    }
}

fn files_under(directory: &Path, out: &mut Vec<PathBuf>) {
    let Ok(entries) = fs::read_dir(directory) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            files_under(&path, out);
        } else {
            out.push(path);
        }
    }
}

/// Occurrences of `canary` in the case's streams and every file below `root`.
fn canary_hits(canary: &str, observed: &Run, root: &Path) -> usize {
    let count = |bytes: &[u8]| String::from_utf8_lossy(bytes).matches(canary).count();
    let mut files = Vec::new();
    files_under(root, &mut files);
    count(observed.stdout.as_bytes())
        + count(observed.stderr.as_bytes())
        + files
            .iter()
            .map(|path| count(&fs::read(path).unwrap_or_default()))
            .sum::<usize>()
}

fn closed_port() -> String {
    let listener = TcpListener::bind("127.0.0.1:0").expect("bind loopback");
    let port = listener.local_addr().expect("local address").port();
    drop(listener);
    port.to_string()
}

fn matches_go(step: &Step, observed: &Run, root: &Path, canonical: &Path) -> bool {
    observed.exit == step.exit_code
        && cli_normalize::normalize(&observed.stdout, root, canonical, false) == step.stdout
        && cli_normalize::normalize(&observed.stderr, root, canonical, false) == step.stderr
}

#[test]
fn cli_output_modes_and_errors_match_go_process_contract() {
    let fixture = fixture();
    assert_eq!(fixture.cases.len(), EXPECTED_CASES);
    assert_eq!(
        fixture
            .cases
            .iter()
            .map(|case| case.steps.len())
            .sum::<usize>(),
        EXPECTED_STEPS
    );
    assert_eq!(
        fixture
            .cases
            .iter()
            .flat_map(|case| &case.steps)
            .filter(|step| step.exit_code != 0)
            .count(),
        EXPECTED_FAILING_STEPS,
        "the recorded suite keeps its negative half"
    );
    assert_eq!(
        fixture
            .cases
            .iter()
            .filter(|case| !case.canaries.is_empty())
            .count(),
        EXPECTED_CANARY_CASES
    );
    let binary = env!("CARGO_BIN_EXE_symdesk");
    let base: PathBuf = std::env::temp_dir().join(format!(
        "symdesk-cli-errors-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock after epoch")
            .as_nanos()
    ));
    fs::create_dir(&base).expect("make isolated test directory");

    let (mut executed_cases, mut executed_steps, mut canary_checks) = (0, 0, 0);
    for case in &fixture.cases {
        assert!(!case.unix_only || cfg!(unix));
        executed_cases += 1;
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
        let root_text = root.to_string_lossy().into_owned();
        let materialize = |argument: &str| {
            let argument = argument.replace("$CLOSED_PORT", &port);
            process_root::expand_argument(&argument, &root_text)
        };
        let env: Vec<(String, String)> = case
            .env
            .iter()
            .map(|(name, value)| (name.clone(), materialize(value)))
            .collect();
        if !case.skip_index {
            let setup = run(
                binary,
                &home,
                &cwd,
                &[],
                &[
                    "--vault".to_owned(),
                    vault.to_string_lossy().into_owned(),
                    "index".to_owned(),
                ],
                "",
            );
            assert_eq!(setup.exit, 0, "{} setup index: {}", case.name, setup.stderr);
        }
        write_files(&vault, &case.post_index_files);
        for step in &case.steps {
            let mut args = Vec::new();
            if !case.no_vault {
                args.push("--vault".to_owned());
                args.push(vault.to_string_lossy().into_owned());
            }
            args.extend(step.args.iter().map(|argument| materialize(argument)));
            let observed = run(binary, &home, &cwd, &env, &args, &step.stdin);
            let label = format!("{} {:?}", case.name, step.args);
            assert_eq!(
                cli_normalize::normalize(&observed.stdout, &root, &canonical, false),
                step.stdout,
                "{label} stdout"
            );
            assert_eq!(
                cli_normalize::normalize(&observed.stderr, &root, &canonical, false),
                step.stderr,
                "{label} stderr"
            );
            assert_eq!(observed.exit, step.exit_code, "{label} exit");
            for canary in &case.canaries {
                assert_eq!(
                    canary_hits(canary, &observed, &root),
                    0,
                    "{label} leaks a canary"
                );
                canary_checks += 1;
            }
            executed_steps += 1;
        }
    }
    assert_eq!(executed_steps, EXPECTED_STEPS, "every fixture step ran");
    assert_eq!(executed_cases, EXPECTED_CASES, "every fixture case ran");
    assert!(canary_checks > 0, "canary scans ran");
    let _ = fs::remove_dir_all(base);
}

/// Negative control: the comparison used above must reject a changed byte,
/// a changed exit code and a swapped stream.
#[test]
fn comparison_rejects_tampered_go_observations() {
    let fixture = fixture();
    let step = fixture
        .cases
        .iter()
        .flat_map(|case| &case.steps)
        .find(|step| step.exit_code == 1 && !step.stderr.is_empty())
        .expect("a failing step with stderr");
    let root = Path::new("/nonexistent-root");
    let observed = Run {
        exit: step.exit_code,
        stdout: step.stdout.clone(),
        stderr: step.stderr.clone(),
    };
    assert!(matches_go(step, &observed, root, root));
    let mut changed = step.clone();
    changed.stderr.push('x');
    assert!(!matches_go(&changed, &observed, root, root), "changed byte");
    let mut changed = step.clone();
    changed.exit_code = 0;
    assert!(
        !matches_go(&changed, &observed, root, root),
        "changed exit code"
    );
    let swapped = Run {
        exit: observed.exit,
        stdout: observed.stderr.clone(),
        stderr: observed.stdout.clone(),
    };
    assert!(!matches_go(step, &swapped, root, root), "swapped streams");
}

#[test]
fn recorded_canaries_are_never_part_of_recorded_output() {
    let fixture = fixture();
    let mut checked = 0;
    for case in &fixture.cases {
        for canary in &case.canaries {
            for step in &case.steps {
                assert!(!step.stdout.contains(canary), "{} stdout", case.name);
                assert!(!step.stderr.contains(canary), "{} stderr", case.name);
                checked += 1;
            }
        }
    }
    assert!(checked > 0);
}
