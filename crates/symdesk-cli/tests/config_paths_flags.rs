//! Regression for config CRUD interception shadowing persistent paths flags.
use std::{fs, process::Command};

#[test]
fn persistent_flags_between_config_and_paths_reach_the_paths_command() {
    struct TempRoot(std::path::PathBuf);
    impl Drop for TempRoot {
        fn drop(&mut self) {
            fs::remove_dir_all(&self.0).unwrap();
        }
    }
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let root = TempRoot(std::env::temp_dir().join(format!(
        "symdesk-config-paths-flags-{}-{nonce}",
        std::process::id()
    )));
    fs::create_dir(&root.0).unwrap();
    let home = root.0.join("home");
    fs::create_dir(&home).unwrap();
    let run = |args: &[&str]| {
        let output = Command::new(env!("CARGO_BIN_EXE_symdesk"))
            .args(args)
            .current_dir(&root.0)
            .env_clear()
            .env("HOME", &home)
            .env("USERPROFILE", &home)
            .env("XDG_CONFIG_HOME", home.join("config"))
            .env("XDG_DATA_HOME", home.join("data"))
            .env("XDG_CACHE_HOME", home.join("cache"))
            .env("PATH", &root.0)
            .output()
            .unwrap();
        assert!(output.status.success(), "{args:?}: {output:?}");
        assert!(output.stderr.is_empty(), "{args:?}: {output:?}");
        let _: serde_json::Value = serde_json::from_slice(&output.stdout).unwrap();
        output.stdout
    };
    let expected = run(&["config", "paths", "--output=json"]);
    for args in [
        vec!["config", "--json", "paths"],
        vec!["config", "--output", "json", "paths"],
        vec!["config", "--output=json", "paths"],
        vec!["--json", "config", "paths"],
        vec!["config", "paths", "--json"],
    ] {
        assert_eq!(run(&args), expected, "{args:?}");
    }
}
