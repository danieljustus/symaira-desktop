#![cfg(unix)]
use std::{
    fs,
    os::unix::fs::symlink,
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

#[test]
fn inverse_sources_are_relative_for_both_vault_spellings() {
    let base = std::env::temp_dir().join(format!(
        "relations-symlink-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    let vault = base.join("vault");
    let alias = base.join("alias");
    let home = base.join("home");
    fs::create_dir_all(&vault).unwrap();
    fs::create_dir_all(&home).unwrap();
    fs::write(vault.join("Target.md"), "# Target\n").unwrap();
    fs::write(vault.join("task.md"), "# Task\n\n[[Target]]\n").unwrap();
    symlink(&vault, &alias).unwrap();
    let run = |root: &std::path::Path, args: &[&str]| {
        Command::new(env!("CARGO_BIN_EXE_symdesk"))
            .env("HOME", &home)
            .env("XDG_DATA_HOME", home.join("data"))
            .env("XDG_CONFIG_HOME", home.join("config"))
            .arg("--vault")
            .arg(root)
            .args(args)
            .output()
            .unwrap()
    };
    let output = run(&alias, &["index", "--json"]);
    assert!(
        output.status.success(),
        "{}",
        String::from_utf8_lossy(&output.stderr)
    );
    for root in [&alias, &vault] {
        let output = run(root, &["relations", "inverse", "Target.md", "--json"]);
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
        let json: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
        assert!(
            json.as_array()
                .unwrap()
                .iter()
                .any(|item| item["source"] == "task.md"),
            "{json}"
        );
        assert!(
            json.as_array()
                .unwrap()
                .iter()
                .all(|item| item["source"] == "task.md"),
            "{json}"
        );
    }
    fs::remove_dir_all(base).unwrap();
}
