use std::{
    fs,
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

#[test]
fn index_only_selected_file_by_relative_or_absolute_path() {
    let base = std::env::temp_dir().join(format!(
        "index-single-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    let vault = base.join("vault");
    let home = base.join("home");
    fs::create_dir_all(&vault).unwrap();
    fs::create_dir_all(&home).unwrap();
    for name in ["a.md", "b.md"] {
        fs::write(vault.join(name), "# Original\n").unwrap();
    }
    let run = |arg: Option<&std::path::Path>| {
        let mut cmd = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        cmd.env("HOME", &home)
            .env("XDG_DATA_HOME", home.join("data"))
            .env("XDG_CONFIG_HOME", home.join("config"))
            .arg("--vault")
            .arg(&vault)
            .arg("index");
        if let Some(arg) = arg {
            cmd.arg(arg);
        }
        let output = cmd.arg("--json").output().unwrap();
        assert!(
            output.status.success(),
            "{} {}",
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
        serde_json::from_slice::<serde_json::Value>(&output.stdout).unwrap()
    };
    assert_eq!(run(None)["indexed"], 2);
    fs::write(vault.join("a.md"), "# Changed A\n").unwrap();
    fs::write(vault.join("b.md"), "# Changed B\n").unwrap();
    assert_eq!(run(Some(std::path::Path::new("a.md")))["indexed"], 1);
    assert_eq!(run(Some(&vault.join("b.md")))["indexed"], 1);
    assert_eq!(run(None)["skipped"], 2);
    fs::write(vault.join("a.md"), "# Again A\n").unwrap();
    fs::write(vault.join("b.md"), "# Again B\n").unwrap();
    assert_eq!(run(Some(&vault.join("a.md")))["indexed"], 1);
    let result = run(None);
    assert_eq!(result["indexed"], 1);
    assert_eq!(result["skipped"], 1);
    fs::remove_dir_all(base).unwrap();
}
