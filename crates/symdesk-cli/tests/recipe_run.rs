//! Replays `symdesk recipe run|diff|accept|reject` against the process fixture
//! recorded from the real Go CLI (`make recipe-run-fixtures-generate`).
//! The fixture's own shell runner executes unchanged under the Rust binary.
#![cfg(unix)]

use std::{
    collections::BTreeMap,
    fs,
    os::unix::fs::{MetadataExt, PermissionsExt},
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

/// Two replays in one test binary can share a clock tick; the counter keeps
/// their scratch roots (and Hermes transcripts) distinct.
static ROOT_SEQUENCE: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    runner_script: String,
    cases: Vec<Case>,
}

#[derive(Deserialize, Clone)]
struct Case {
    name: String,
    #[serde(default)]
    files: BTreeMap<String, String>,
    #[serde(default)]
    symlinks: BTreeMap<String, String>,
    steps: Vec<Step>,
}

#[derive(Deserialize, Clone)]
struct Step {
    args: Vec<String>,
    #[serde(default)]
    env: BTreeMap<String, String>,
    #[serde(default)]
    write: BTreeMap<String, String>,
    #[serde(default)]
    no_vault: bool,
    #[serde(default)]
    inexact_prefix: String,
    exit_code: i32,
    stdout: String,
    stderr: String,
    vault: BTreeMap<String, String>,
    #[serde(default)]
    runner: String,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/ai/recipe-run-process.json"
    ))
    .expect("decode Go-owned recipe run fixture")
}

fn scratch(label: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "symdesk-recipe-run-{label}-{}-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos(),
        ROOT_SEQUENCE.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ));
    fs::create_dir_all(&dir).unwrap();
    fs::canonicalize(dir).unwrap()
}

/// Go's harness writes fixture inputs with mode 0600.
fn write_private(path: impl AsRef<Path>, content: &str) {
    use std::{io::Write, os::unix::fs::OpenOptionsExt};
    let mut file = fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)
        .unwrap();
    file.write_all(content.as_bytes()).unwrap();
}

fn is_run_id(name: &str) -> bool {
    let digits = name.bytes().take_while(u8::is_ascii_digit).count();
    (16..=19).contains(&digits)
        && name.as_bytes().get(digits) == Some(&b'-')
        && name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')
}

struct Normalizer {
    root: String,
    ids: Vec<String>,
}

impl Normalizer {
    fn apply(&self, text: &str) -> String {
        let mut text = text.to_owned();
        for (index, id) in self.ids.iter().enumerate() {
            text = text.replace(id, &format!("<RUN{}>", index + 1));
        }
        text = text.replace(&self.root, "<ROOT>");
        text = replace_between(&text, "\"created\": \"", '"', "<TIME>");
        replace_digits_after(&text, ".symdesk-proposal-", "<N>")
    }
}

fn replace_between(text: &str, start: &str, end: char, replacement: &str) -> String {
    let mut out = String::new();
    let mut rest = text;
    while let Some(index) = rest.find(start) {
        out.push_str(&rest[..index + start.len()]);
        let tail = &rest[index + start.len()..];
        let stop = tail.find(end).unwrap_or(tail.len());
        out.push_str(replacement);
        rest = &tail[stop..];
    }
    out.push_str(rest);
    out
}

fn replace_digits_after(text: &str, prefix: &str, replacement: &str) -> String {
    let mut out = String::new();
    let mut rest = text;
    while let Some(index) = rest.find(prefix) {
        out.push_str(&rest[..index + prefix.len()]);
        let tail = &rest[index + prefix.len()..];
        let digits = tail.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 {
            rest = tail;
            continue;
        }
        out.push_str(replacement);
        rest = &tail[digits..];
    }
    out.push_str(rest);
    out
}

fn perm_string(mode: u32) -> String {
    let mut out = String::from("-");
    for shift in [6, 3, 0] {
        let bits = (mode >> shift) & 7;
        out.push(if bits & 4 != 0 { 'r' } else { '-' });
        out.push(if bits & 2 != 0 { 'w' } else { '-' });
        out.push(if bits & 1 != 0 { 'x' } else { '-' });
    }
    out
}

fn snapshot(vault: &Path, normalizer: &Normalizer) -> BTreeMap<String, String> {
    fn walk(base: &Path, dir: &Path, normalizer: &Normalizer, out: &mut BTreeMap<String, String>) {
        let mut entries: Vec<_> = fs::read_dir(dir).unwrap().map(|e| e.unwrap()).collect();
        entries.sort_by_key(fs::DirEntry::file_name);
        for entry in entries {
            let path = entry.path();
            let relative = path
                .strip_prefix(base)
                .unwrap()
                .to_string_lossy()
                .into_owned();
            let key = normalizer.apply(&relative);
            let kind = entry.file_type().unwrap();
            if kind.is_symlink() {
                let target = fs::read_link(&path).unwrap().to_string_lossy().into_owned();
                out.insert(key, format!("symlink:{}", normalizer.apply(&target)));
            } else if kind.is_dir() {
                out.insert(format!("{key}/"), String::new());
                walk(base, &path, normalizer, out);
            } else {
                let body = String::from_utf8_lossy(&fs::read(&path).unwrap()).into_owned();
                let mode = fs::metadata(&path).unwrap().mode() & 0o777;
                out.insert(
                    key,
                    format!("mode:{}\n{}", perm_string(mode), normalizer.apply(&body)),
                );
            }
        }
    }
    let mut out = BTreeMap::new();
    walk(vault, vault, normalizer, &mut out);
    out
}

fn discover(vault: &Path, ids: &mut Vec<String>) {
    let Ok(entries) = fs::read_dir(vault.join(".symdesk/runs")) else {
        return;
    };
    let mut names: Vec<String> = entries
        .filter_map(Result::ok)
        .filter(|e| e.file_type().is_ok_and(|t| t.is_dir()))
        .map(|e| e.file_name().to_string_lossy().into_owned())
        .filter(|name| is_run_id(name))
        .collect();
    names.sort();
    for name in names {
        if !ids.contains(&name) {
            ids.push(name);
        }
    }
}

fn stdout_run_id(stdout: &str) -> Option<String> {
    let start = stdout.find("run_id")? + "run_id".len();
    let tail = stdout[start..].trim_start_matches(['"', ':']);
    let id: String = tail
        .chars()
        .take_while(|c| c.is_ascii_alphanumeric() || *c == '-')
        .collect();
    is_run_id(&id).then_some(id)
}

/// Replays one case; returns the number of executed steps or the first mismatch.
fn replay(case: &Case, script: &str) -> Result<usize, String> {
    let root = scratch(&case.name);
    let home = root.join("home");
    let vault = root.join("vault");
    let log_dir = root.join("runner-log");
    for dir in [
        &home,
        &vault,
        &log_dir,
        &root.join("bin"),
        &root.join("outside"),
    ] {
        fs::create_dir_all(dir).unwrap();
    }
    let runner = root.join("bin/fake-runner");
    fs::write(&runner, script).unwrap();
    fs::set_permissions(&runner, fs::Permissions::from_mode(0o700)).unwrap();
    for (name, content) in &case.files {
        let dest = root.join(name);
        fs::create_dir_all(dest.parent().unwrap()).unwrap();
        write_private(dest, content);
    }
    for (link, target) in &case.symlinks {
        std::os::unix::fs::symlink(root.join(target), vault.join(link)).unwrap();
    }
    let root_text = root.to_string_lossy().into_owned();
    let mut normalizer = Normalizer {
        root: root_text.clone(),
        ids: Vec::new(),
    };
    let mut run_id = String::new();
    let mut executed = 0;
    for (index, step) in case.steps.iter().enumerate() {
        let expand = |text: &str| text.replace("$ROOT", &root_text).replace("$RUN", &run_id);
        for (name, content) in &step.write {
            let dest = vault.join(expand(name));
            fs::create_dir_all(dest.parent().unwrap()).unwrap();
            write_private(dest, &expand(content));
        }
        let mut args: Vec<String> = Vec::new();
        if !step.no_vault {
            args.extend(["--vault".to_owned(), vault.to_string_lossy().into_owned()]);
        }
        args.extend(step.args.iter().map(|a| expand(a)));
        let path = "/usr/bin:/bin".to_owned();
        let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        command
            .current_dir(&root)
            .env_clear()
            .env("PATH", &path)
            .env("HOME", &home)
            .env("TZ", "UTC")
            .env("XDG_CONFIG_HOME", home.join("config"))
            .env("XDG_CACHE_HOME", home.join("cache"))
            .env("XDG_DATA_HOME", home.join("data"))
            .env("SYMDESK_RECIPE_RUNNER", &runner)
            .env("FAKE_RUNNER_LOG", log_dir.join("runner.log"))
            .args(&args);
        for (key, value) in &step.env {
            command.env(key, expand(value).replace("$PATH", &path));
        }
        let output = command.output().expect("run Rust symdesk process");
        discover(&vault, &mut normalizer.ids);
        let stdout_raw = String::from_utf8_lossy(&output.stdout).into_owned();
        if let Some(id) = stdout_run_id(&stdout_raw) {
            run_id = id;
        }
        let stdout = normalizer.apply(&stdout_raw);
        let stderr = normalizer.apply(&String::from_utf8_lossy(&output.stderr));
        let runner_log = fs::read_to_string(log_dir.join("runner.log"))
            .map(|log| normalizer.apply(&log))
            .unwrap_or_default();
        let _ = fs::remove_file(log_dir.join("runner.log"));
        let label = format!("{} step {index} {:?}", case.name, step.args);
        if output.status.code() != Some(step.exit_code) {
            return Err(format!(
                "{label}: exit {:?} != {}\nstdout: {stdout}\nstderr: {stderr}",
                output.status.code(),
                step.exit_code
            ));
        }
        if step.inexact_prefix.is_empty() {
            if stdout != step.stdout {
                return Err(format!(
                    "{label}: stdout\nrust: {stdout:?}\ngo:   {:?}",
                    step.stdout
                ));
            }
            if stderr != step.stderr {
                return Err(format!(
                    "{label}: stderr\nrust: {stderr:?}\ngo:   {:?}",
                    step.stderr
                ));
            }
        } else if !stdout.starts_with(&step.inexact_prefix)
            && !stderr.starts_with(&step.inexact_prefix)
        {
            return Err(format!(
                "{label}: inexact prefix {:?} missing\nstdout: {stdout:?}\nstderr: {stderr:?}",
                step.inexact_prefix
            ));
        }
        let observed = snapshot(&vault, &normalizer);
        if observed != step.vault {
            let missing: Vec<_> = step
                .vault
                .keys()
                .filter(|k| !observed.contains_key(*k))
                .collect();
            let extra: Vec<_> = observed
                .keys()
                .filter(|k| !step.vault.contains_key(*k))
                .collect();
            let differing: Vec<_> = observed
                .iter()
                .filter(|(k, v)| step.vault.get(*k).is_some_and(|w| w != *v))
                .map(|(k, v)| format!("{k}: rust {v:?} go {:?}", step.vault[k]))
                .collect();
            return Err(format!(
                "{label}: vault tree\nmissing {missing:?}\nextra {extra:?}\ndiffering {differing:?}"
            ));
        }
        if runner_log != step.runner {
            return Err(format!(
                "{label}: runner transcript\nrust: {runner_log:?}\ngo:   {:?}",
                step.runner
            ));
        }
        executed += 1;
    }
    fs::remove_dir_all(root).expect("remove only this replay's directory");
    Ok(executed)
}

#[test]
fn recipe_run_matches_go_process_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 10);
    let mut total = 0;
    for case in &fixture.cases {
        total += replay(case, &fixture.runner_script).unwrap_or_else(|error| panic!("{error}"));
    }
    assert_eq!(total, 75, "every recorded Go step must be replayed");
}

#[test]
fn replay_rejects_a_tampered_expectation() {
    let fixture = fixture();
    let mut case = fixture
        .cases
        .iter()
        .find(|case| case.name == "empty_and_null_changes")
        .cloned()
        .expect("recorded case");
    case.steps[1].stdout = "[{\"path\":\"x\"}]\n".to_owned();
    let error = replay(&case, &fixture.runner_script).unwrap_err();
    assert!(error.contains("stdout"), "{error}");
}

#[test]
fn replay_rejects_a_changed_exit_status_and_tree() {
    let fixture = fixture();
    let mut case = fixture
        .cases
        .iter()
        .find(|case| case.name == "ok_json_flow")
        .cloned()
        .unwrap();
    case.steps[3].vault.remove("notes/out.md");
    let error = replay(&case, &fixture.runner_script).unwrap_err();
    assert!(error.contains("vault tree"), "{error}");
    let mut case = fixture
        .cases
        .iter()
        .find(|case| case.name == "ok_json_flow")
        .cloned()
        .unwrap();
    case.steps[4].exit_code = 0;
    assert!(
        replay(&case, &fixture.runner_script)
            .unwrap_err()
            .contains("exit")
    );
}
