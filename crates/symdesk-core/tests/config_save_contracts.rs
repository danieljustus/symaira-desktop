#![deny(unsafe_code)]

//! Replays the Go-owned CFG-004 fixture for `config.Save`
//! (`internal/config/port_config_save_contract_test.go`, fixture
//! `testdata/port/config/config-save.json`).
//!
//! Every case runs the real [`symdesk_core::config::save`] against its own
//! sandbox and compares the resulting file content, permission bits, directory
//! modes, file set and failure stage with what the Go implementation produced.
//! The sandbox root itself is harness scaffolding and is never compared.

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};

use serde::Deserialize;
use symdesk_core::config::{self, Config};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    #[serde(rename = "cases")]
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    platform: String,
    setup: Setup,
    path: String,
    expected: Expected,
}

#[derive(Deserialize)]
struct Setup {
    dirs: Vec<DirSetup>,
    file: Option<FileSetup>,
}

#[derive(Deserialize)]
struct DirSetup {
    path: String,
    mode: u32,
}

#[derive(Deserialize)]
struct FileSetup {
    path: String,
    content: String,
    mode: u32,
}

#[derive(Deserialize)]
struct Expected {
    outcome: String,
    #[serde(default)]
    error_stage: Option<String>,
    #[serde(default)]
    error_prefix: Option<String>,
    #[serde(default)]
    error_message_go: Option<String>,
    file_content: String,
    file_mode: u32,
    dir_modes: BTreeMap<String, u32>,
    file_set: Vec<String>,
}

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/config/config-save.json"
    ))
    .expect("decode config save fixture")
}

/// The same configuration value the Go oracle writes in every case, field for
/// field. The empty API key matches Go's explicit empty `LLMAPIKey`.
fn canonical_config() -> Config {
    let mut config = Config::default();
    config.vault = "/vault".to_owned();
    config.inbox = "inbox".to_owned();
    config.review_threshold = 85;
    config.llm_provider = "ollama".to_owned();
    config.llm_model = "claude-sonnet-5".to_owned();
    config.ollama_url = "http://127.0.0.1:11434".to_owned();
    config.recipe_runner = String::new();
    config.hermes_session = String::new();
    config.language = "de".to_owned();
    config.max_tokens = 8192;
    config.agent_max_iterations = 5;
    config.history_max_per_file = 20;
    config.history_max_age_days = 90;
    config.history_checkpoint_max_age_days = 30;
    config.trash_retention_days = 30;
    config.results_max_age_days = 30;
    config.results_max_per_task = 20;
    config.dataset_export_max_sensitivity = "internal".to_owned();
    config.storage_path_template = "{{year}}/{{title}}".to_owned();
    config
}

fn unique_temp_dir(label: &str) -> PathBuf {
    use std::time::{SystemTime, UNIX_EPOCH};
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("system clock after the epoch")
        .as_nanos();
    let dir = std::env::temp_dir().join(format!(
        "symdesk-core-config-save-{label}-{}-{nanos}",
        std::process::id()
    ));
    fs::create_dir_all(&dir).expect("create sandbox");
    dir
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(mode)).expect("chmod");
}

#[cfg(not(unix))]
fn set_mode(_path: &Path, _mode: u32) {}

#[cfg(unix)]
fn mode_of(path: &Path) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    fs::metadata(path).expect("stat").permissions().mode() & 0o777
}

#[test]
fn config_save_filesystem_contract_matches_go() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);

    for case in &fixture.cases {
        if case.platform == "unix" && cfg!(not(unix)) {
            continue;
        }
        run_case(case);
    }
}

fn run_case(case: &Case) {
    let root = unique_temp_dir(&case.id);

    for dir in &case.setup.dirs {
        let full = root.join(&dir.path);
        fs::create_dir_all(&full).expect("setup dir");
        set_mode(&full, dir.mode);
    }
    if let Some(file) = &case.setup.file {
        let full = root.join(&file.path);
        fs::create_dir_all(full.parent().expect("file parent")).expect("setup file parent");
        fs::write(&full, file.content.as_bytes()).expect("setup file");
        set_mode(&full, file.mode);
    }

    let target = root.join(&case.path);
    let outcome = config::save(target.to_str().expect("utf-8 path"), &canonical_config());

    match (&case.expected.outcome[..], outcome) {
        ("ok", Ok(())) => {
            let observed = fs::read(&target).expect("read saved file");
            assert_eq!(
                String::from_utf8(observed).expect("utf-8 content"),
                case.expected.file_content,
                "case {}: file content differs",
                case.id
            );
            assert_eq!(
                collect_files(&root),
                sorted(case.expected.file_set.clone()),
                "case {}: file set differs",
                case.id
            );
            #[cfg(unix)]
            {
                assert_eq!(
                    mode_of(&target),
                    case.expected.file_mode,
                    "case {}: file mode differs",
                    case.id
                );
                assert_eq!(
                    collect_dir_modes(&root),
                    case.expected.dir_modes,
                    "case {}: directory modes differ",
                    case.id
                );
            }
        }
        ("error", Err(error)) => {
            let prefix = case
                .expected
                .error_prefix
                .as_deref()
                .expect("fixture error prefix");
            assert!(
                error.starts_with(prefix),
                "case {}: error {error:?} does not start with {prefix:?}",
                case.id
            );
            assert!(
                case.expected.error_stage.is_some(),
                "case {}: fixture must name the failure stage",
                case.id
            );
            assert_eq!(
                collect_files(&root),
                sorted(case.expected.file_set.clone()),
                "case {}: file set differs after the failure",
                case.id
            );
            #[cfg(unix)]
            assert_eq!(
                collect_dir_modes(&root),
                case.expected.dir_modes,
                "case {}: directory modes differ after the failure",
                case.id
            );
            assert!(
                case.expected.error_message_go.is_some(),
                "case {}: the Go error text is recorded for comparison",
                case.id
            );
        }
        (expected, Err(error)) => {
            panic!("case {}: expected {expected}, got error {error:?}", case.id)
        }
        (expected, Ok(())) => panic!("case {}: expected {expected}, save succeeded", case.id),
    }

    let _ = fs::remove_dir_all(&root);
}

fn sorted(mut values: Vec<String>) -> Vec<String> {
    values.sort();
    values
}

/// Every file below the sandbox, relative and sorted, excluding the sandbox
/// root. Unreadable directories are skipped rather than failing the case that
/// created them.
fn collect_files(root: &Path) -> Vec<String> {
    let mut files = Vec::new();
    walk(root, root, &mut |relative, is_dir| {
        if !is_dir {
            files.push(relative);
        }
    });
    files.sort();
    files
}

#[cfg(unix)]
fn collect_dir_modes(root: &Path) -> BTreeMap<String, u32> {
    let mut modes = BTreeMap::new();
    walk(root, root, &mut |relative, is_dir| {
        if is_dir && !relative.is_empty() {
            let mode = mode_of(&root.join(&relative));
            modes.insert(relative, mode);
        }
    });
    modes
}

fn walk(root: &Path, dir: &Path, visit: &mut impl FnMut(String, bool)) {
    let entries = match fs::read_dir(dir) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::PermissionDenied => return,
        Err(error) => panic!("read_dir {}: {error}", dir.display()),
    };
    for entry in entries {
        let entry = entry.expect("directory entry");
        let path = entry.path();
        let file_type = entry.file_type().expect("file type");
        let relative = path
            .strip_prefix(root)
            .expect("path below root")
            .to_string_lossy()
            .replace('\\', "/");
        if file_type.is_dir() {
            visit(relative.clone(), true);
            walk(root, &path, visit);
        } else {
            visit(relative, false);
        }
    }
}
