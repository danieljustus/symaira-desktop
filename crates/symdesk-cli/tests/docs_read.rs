//! Replays every case captured by the real Go CLI oracle.
#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

#[path = "support/process_root.rs"]
mod process_root;
#[path = "support/root_paths.rs"]
mod root_paths;

use serde::Deserialize;
use std::{
    collections::BTreeMap,
    fs,
    path::Path,
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};
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
    skip_index: bool,
    #[serde(default)]
    no_vault: bool,
    #[serde(default)]
    unix_only: bool,
    steps: Vec<Step>,
}
#[derive(Deserialize)]
struct Step {
    args: Vec<String>,
    exit_code: i32,
    stdout: String,
    stderr: String,
}
fn run(home: &Path, cwd: &Path, args: &[String]) -> std::process::Output {
    Command::new(env!("CARGO_BIN_EXE_symdesk"))
        .current_dir(cwd)
        .isolated_env()
        .envs(
            std::env::vars()
                .filter(|(n, _)| matches!(n.as_str(), "PATH" | "SYSTEMROOT" | "TEMP" | "TMP")),
        )
        .env("TZ", "UTC")
        .env("HOME", home)
        .env("USERPROFILE", home)
        .env("XDG_CONFIG_HOME", home.join("config"))
        .env("XDG_CACHE_HOME", home.join("cache"))
        .env("XDG_DATA_HOME", home.join("data"))
        .args(args)
        .output()
        .expect("run native Rust CLI")
}
fn snapshot(root: &Path) -> BTreeMap<String, Vec<u8>> {
    fn walk(root: &Path, dir: &Path, out: &mut BTreeMap<String, Vec<u8>>) {
        for entry in fs::read_dir(dir).unwrap() {
            let p = entry.unwrap().path();
            if p.is_dir() {
                walk(root, &p, out)
            } else {
                out.insert(
                    p.strip_prefix(root).unwrap().to_string_lossy().into_owned(),
                    fs::read(p).unwrap(),
                );
            }
        }
    }
    let mut out = BTreeMap::new();
    walk(root, root, &mut out);
    out
}
#[test]
fn docs_read_matches_go_process_contract() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/cli/docs-read-process.json"
    ))
    .unwrap();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 4);
    assert_eq!(
        fixture.cases.iter().map(|c| c.steps.len()).sum::<usize>(),
        90
    );
    let base = std::env::temp_dir().join(format!(
        "docs-read-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    fs::create_dir_all(&base).unwrap();
    let mut executed = 0;
    for case in &fixture.cases {
        if cfg!(windows) && case.unix_only {
            continue;
        }
        let root = base.join(&case.name);
        fs::create_dir_all(&root).unwrap();
        let root = process_root::canonical_root(&root);
        let home = root.join("home");
        let vault = root.join("vault");
        let cwd = root.join("cwd");
        for d in [&home, &vault, &cwd] {
            fs::create_dir_all(d).unwrap();
        }
        for (name, content) in &case.files {
            let p = vault.join(name);
            fs::create_dir_all(p.parent().unwrap()).unwrap();
            fs::write(p, content).unwrap();
        }
        if !case.skip_index && !case.no_vault {
            let setup = run(
                &home,
                &cwd,
                &[
                    "--vault".into(),
                    vault.to_string_lossy().into_owned(),
                    "index".into(),
                ],
            );
            assert!(
                setup.status.success(),
                "setup: {}",
                String::from_utf8_lossy(&setup.stderr)
            );
        }
        let before = snapshot(&vault);
        for step in &case.steps {
            let mut args = Vec::new();
            if !case.no_vault {
                args.extend(["--vault".to_owned(), vault.to_string_lossy().into_owned()]);
            }
            args.extend(
                step.args
                    .iter()
                    .map(|a| a.replace("$ROOT", &root.to_string_lossy())),
            );
            let output = run(&home, &cwd, &args);
            let normal = |bytes: Vec<u8>| {
                let text = String::from_utf8(bytes)
                    .unwrap()
                    .replace(&root.to_string_lossy().replace('\\', "\\\\"), "$ROOT")
                    .replace(root.to_str().unwrap(), "$ROOT");
                root_paths::slash_root_paths(&text)
            };
            assert_eq!(
                normal(output.stdout),
                step.stdout,
                "{} {:?} stdout",
                case.name,
                step.args
            );
            assert_eq!(
                normal(output.stderr),
                step.stderr,
                "{} {:?} stderr",
                case.name,
                step.args
            );
            assert_eq!(
                output.status.code(),
                Some(step.exit_code),
                "{} {:?} exit",
                case.name,
                step.args
            );
            assert_eq!(
                snapshot(&vault),
                before,
                "read-only commands preserve all Markdown bytes"
            );
            executed += 1;
        }
    }
    let expected = fixture
        .cases
        .iter()
        .filter(|c| !(cfg!(windows) && c.unix_only))
        .map(|c| c.steps.len())
        .sum::<usize>();
    assert_eq!(executed, expected);
    assert!(executed > 0);
    fs::remove_dir_all(base).unwrap();
}
