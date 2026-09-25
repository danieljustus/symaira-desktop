use std::{
    collections::BTreeMap,
    fs,
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    json: bool,
    manifests: BTreeMap<String, String>,
    exit_code: i32,
    stdout: String,
    stderr: String,
}

#[test]
fn history_tasks_matches_go_process_contract() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/cli/history-tasks.json"
    ))
    .expect("decode Go-owned history tasks fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 6);
    let root = std::env::temp_dir().join(format!(
        "symdesk-history-tasks-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock after epoch")
            .as_nanos()
    ));
    fs::create_dir(&root).expect("make isolated test directory");
    for case in fixture.cases {
        let case_root = root.join(&case.name);
        let vault = case_root.join("vault");
        let home = case_root.join("home");
        let checkpoints = vault.join(".symdesk/history/checkpoints");
        fs::create_dir_all(&checkpoints).expect("make checkpoint directory");
        fs::create_dir_all(&home).expect("make isolated home directory");
        for (name, manifest) in &case.manifests {
            fs::write(checkpoints.join(name), manifest).expect("write checkpoint manifest");
        }
        let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        if case.json {
            command.arg("--json");
        }
        let output = command
            .env("TZ", "UTC")
            .env("HOME", &home)
            .env("USERPROFILE", &home)
            .env("XDG_CONFIG_HOME", home.join("config"))
            .env("XDG_CACHE_HOME", home.join("cache"))
            .env("XDG_DATA_HOME", home.join("data"))
            .args(["--vault"])
            .arg(&vault)
            .args(["history", "tasks"])
            .output()
            .expect("run Rust symdesk process");
        assert_eq!(
            output.status.code().unwrap_or(-1),
            case.exit_code,
            "{} exit",
            case.name
        );
        #[cfg(windows)]
        let mut stdout = String::from_utf8(output.stdout).expect("UTF-8 stdout");
        #[cfg(not(windows))]
        let stdout = String::from_utf8(output.stdout).expect("UTF-8 stdout");
        #[cfg(windows)]
        if !case.json {
            let format = time::format_description::parse_borrowed::<2>(
                "[year]-[month]-[day] [hour]:[minute]:[second]",
            )
            .expect("timestamp format");
            for manifest in case.manifests.values() {
                let value: serde_json::Value = serde_json::from_str(manifest).expect("checkpoint");
                let utc = time::OffsetDateTime::parse(
                    value["timestamp"].as_str().expect("timestamp"),
                    &time::format_description::well_known::Rfc3339,
                )
                .expect("UTC timestamp");
                let local = utc.to_offset(
                    time::UtcOffset::local_offset_at(utc).unwrap_or(time::UtcOffset::UTC),
                );
                let local_text = local.format(&format).expect("local timestamp");
                assert!(
                    stdout.contains(&local_text),
                    "{} local timestamp",
                    case.name
                );
                stdout = stdout.replace(&local_text, &utc.format(&format).expect("UTC timestamp"));
            }
        }
        assert_eq!(stdout, case.stdout, "{} stdout", case.name);
        assert_eq!(
            String::from_utf8_lossy(&output.stderr),
            case.stderr,
            "{} stderr",
            case.name
        );
    }
    fs::remove_dir_all(root).expect("remove only this test's temporary directory");
}
