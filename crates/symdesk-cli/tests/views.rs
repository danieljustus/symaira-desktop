//! Replays the Go-owned saved-view read process fixture
//! (`testdata/port/cli/views-process.json`) against the Rust binary.

#[path = "support/views_state.rs"]
mod views_state;

#[path = "support/process_root.rs"]
mod process_root;

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

const EXPECTED_CASES: usize = 5;
const EXPECTED_STEPS: usize = 84;

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
    sparse_files: BTreeMap<String, u64>,
    #[serde(default)]
    skip_index: bool,
    #[serde(default)]
    post_index_files: BTreeMap<String, String>,
    #[serde(default)]
    no_vault: bool,
    #[serde(default)]
    unix_only: bool,
    /// `insensitive`/`sensitive`: the case only applies to a file system of
    /// that case-folding class (each recorded natively by Go).
    #[serde(default)]
    fs_case: String,
    steps: Vec<Step>,
    states: Vec<serde_json::Value>,
}

#[derive(Deserialize)]
struct Step {
    args: Vec<String>,
    exit_code: i32,
    stdout: String,
    stderr: String,
}

fn fixture() -> Fixture {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/cli/views-process.json"
    ))
    .expect("decode Go-owned duplicates fixture");
    assert_eq!(fixture.schema_version, 1);
    fixture
}

fn write_files(vault: &Path, files: &BTreeMap<String, String>) {
    for (name, content) in files {
        let target = vault.join(name);
        fs::create_dir_all(target.parent().expect("file parent")).expect("make parent");
        fs::write(target, content).expect("write vault file");
    }
}

struct Run {
    exit: i32,
    stdout: String,
    stderr: String,
}

fn run(binary: &str, home: &Path, cwd: &Path, args: &[String]) -> Run {
    let output =
        Command::new(binary)
            .current_dir(cwd)
            .env_clear()
            .envs(std::env::vars().filter(|(name, _)| {
                matches!(name.as_str(), "PATH" | "SYSTEMROOT" | "TEMP" | "TMP")
            }))
            .env("TZ", "UTC")
            .env("HOME", home)
            .env("USERPROFILE", home)
            .env("XDG_CONFIG_HOME", home.join("config"))
            .env("XDG_CACHE_HOME", home.join("cache"))
            .env("XDG_DATA_HOME", home.join("data"))
            .args(args)
            .output()
            .expect("run Rust symdesk process");
    Run {
        exit: output.status.code().unwrap_or(-1),
        stdout: String::from_utf8(output.stdout).expect("UTF-8 stdout"),
        stderr: String::from_utf8(output.stderr).expect("UTF-8 stderr"),
    }
}

/// Replaces the temporary root with `$ROOT` like the Go generator.
fn normalize(text: &str, raw_root: &Path, canonical_root: &Path) -> String {
    let mut text = text.to_owned();
    for root in [canonical_root, raw_root] {
        let root = root.to_string_lossy().into_owned();
        text = text.replace(&root, "$ROOT");
        text = text.replace(&root.replace('\\', "\\\\"), "$ROOT");
    }
    normalize_pointers(&slash_root_paths(&text))
}

#[cfg(not(windows))]
fn slash_root_paths(text: &str) -> String {
    text.to_owned()
}

/// Rewrites the separators of every `$ROOT` path token to `/`; a doubled
/// backslash inside a token is one separator (Go `slashRootPaths`).
#[cfg(windows)]
fn slash_root_paths(text: &str) -> String {
    let mut out = String::new();
    let mut rest = text;
    while let Some(index) = rest.find("$ROOT") {
        out.push_str(&rest[..index + "$ROOT".len()]);
        rest = &rest[index + "$ROOT".len()..];
        let bytes = rest.as_bytes();
        let mut consumed = 0;
        while consumed < bytes.len() {
            let c = bytes[consumed];
            if c == b'\\' || c == b'/' {
                out.push('/');
                consumed += 1;
                if consumed < bytes.len() && bytes[consumed] == b'\\' {
                    consumed += 1;
                }
            } else if c == b'.' || c == b'_' || c == b'-' || c.is_ascii_alphanumeric() {
                out.push(char::from(c));
                consumed += 1;
            } else {
                break;
            }
        }
        rest = &rest[consumed..];
    }
    out.push_str(rest);
    out
}

#[test]
fn views_match_go_process_contract() {
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
    let binary = env!("CARGO_BIN_EXE_symdesk");
    let base: PathBuf = std::env::temp_dir().join(format!(
        "symdesk-views-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock after epoch")
            .as_nanos()
    ));
    fs::create_dir(&base).expect("make isolated test directory");
    let fs_class = fs_case_class(&base);
    assert!(
        fixture.cases.iter().any(|case| case.fs_case == fs_class),
        "no Go recording for a case-{fs_class} file system"
    );

    let mut executed_cases = 0;
    let mut executed_steps = 0;
    for case in &fixture.cases {
        if skipped(case, fs_class) {
            continue;
        }
        executed_cases += 1;
        let root = base.join(&case.name);
        fs::create_dir_all(&root).expect("make case root");
        let root = process_root::canonical_root(&root);
        let vault = root.join("vault");
        let home = root.join("home");
        let cwd = root.join("cwd");
        for directory in [&vault, &home, &cwd] {
            fs::create_dir_all(directory).expect("make case directory");
        }
        let canonical_root = fs::canonicalize(&root).expect("canonical case root");
        write_files(&vault, &case.files);
        if !case.skip_index && !case.no_vault {
            let setup = run(
                binary,
                &home,
                &cwd,
                &[
                    "--vault".to_owned(),
                    vault.to_string_lossy().into_owned(),
                    "index".to_owned(),
                ],
            );
            assert_eq!(setup.exit, 0, "{} setup index: {}", case.name, setup.stderr);
        }
        write_files(&vault, &case.post_index_files);
        for (name, size) in &case.sparse_files {
            let file = fs::File::create(vault.join(name)).expect("create sparse file");
            file.set_len(*size).expect("size sparse file");
        }
        assert_eq!(case.steps.len(), case.states.len());
        for (step, expected_state) in case.steps.iter().zip(&case.states) {
            let mut args = Vec::new();
            if !case.no_vault {
                args.push("--vault".to_owned());
                args.push(vault.to_string_lossy().into_owned());
            }
            let root_text = root.to_string_lossy().into_owned();
            args.extend(step.args.iter().map(|arg| arg.replace("$ROOT", &root_text)));
            let observed = run(binary, &home, &cwd, &args);
            let label = format!("{} {:?}", case.name, step.args);
            assert_eq!(
                normalize(&observed.stdout, &root, &canonical_root),
                step.stdout,
                "{label} stdout"
            );
            assert_eq!(
                normalize(&observed.stderr, &root, &canonical_root),
                step.stderr,
                "{label} stderr"
            );
            assert_eq!(observed.exit, step.exit_code, "{label} exit");
            assert_eq!(
                &views_state::snapshot(&root),
                expected_state,
                "{label} state"
            );
            executed_steps += 1;
        }
    }
    let expected_steps: usize = fixture
        .cases
        .iter()
        .filter(|case| !skipped(case, fs_class))
        .map(|case| case.steps.len())
        .sum();
    assert_eq!(executed_steps, expected_steps, "every fixture step ran");
    let expected_cases = fixture
        .cases
        .iter()
        .filter(|case| !skipped(case, fs_class))
        .count();
    assert_eq!(executed_cases, expected_cases, "every fixture case ran");
    fs::remove_dir_all(base).expect("remove only this test's temporary directory");
}

fn normalize_pointers(text: &str) -> String {
    let mut output = String::new();
    let mut rest = text;
    while let Some(index) = rest.find("0x") {
        output.push_str(&rest[..index]);
        rest = &rest[index + 2..];
        let count = rest.bytes().take_while(u8::is_ascii_hexdigit).count();
        if count == 0 {
            output.push_str("0x");
        } else {
            output.push_str("$PTR");
            rest = &rest[count..];
        }
    }
    output.push_str(rest);
    output
}

fn skipped(case: &Case, fs_class: &str) -> bool {
    (cfg!(windows) && case.unix_only) || (!case.fs_case.is_empty() && case.fs_case != fs_class)
}

/// Mirrors Go's `viewsFSCaseClass`: does the test file system fold case?
fn fs_case_class(dir: &Path) -> &'static str {
    let probe = dir.join("CaseProbe");
    fs::write(&probe, b"").expect("write case probe");
    let folded = dir.join("caseprobe").exists();
    fs::remove_file(probe).expect("remove case probe");
    if folded { "insensitive" } else { "sensitive" }
}
