#![deny(unsafe_code)]

use std::{
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_commit: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    #[serde(default)]
    flag: Option<String>,
    #[serde(default)]
    empty_flag: bool,
    #[serde(default)]
    env: Option<String>,
    selected: String,
}

struct TempRoot(PathBuf);

impl TempRoot {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-config-vault-selection-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir_all(root.join("home")).expect("create home");
        fs::create_dir_all(root.join("config/symdesk")).expect("create config directory");
        fs::create_dir_all(root.join("data")).expect("create data directory");
        Self(root)
    }

    fn path(&self, name: &str) -> PathBuf {
        self.0.join(name)
    }
}

impl Drop for TempRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/cli/config-vault-selection.json");
    serde_json::from_slice(&fs::read(path).expect("Go-generated vault selection fixture"))
        .expect("valid fixture")
}

fn vault(root: &TempRoot, alias: &str) -> PathBuf {
    let path = root.path(alias);
    fs::create_dir_all(&path).expect("create vault");
    fs::write(path.join(format!("{alias}.md")), format!("# {alias}\n")).expect("write marker");
    path
}

#[test]
fn cli_vault_precedence_matches_go_process_fixture() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle_commit,
        "e023816a9db2b3d71514049195886fe1b9766a5a"
    );
    assert_eq!(fixture.cases.len(), 4);

    for case in fixture.cases {
        let root = TempRoot::new();
        let toml_vault = vault(&root, "toml");
        let env_vault = vault(&root, "env");
        let flag_vault = vault(&root, "flag");
        let config = format!("vault = {:?}\n", toml_vault.to_string_lossy());
        fs::write(root.path("config/symdesk/config.toml"), config).expect("write config");

        let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        command
            .env_clear()
            .env("HOME", root.path("home"))
            .env("USERPROFILE", root.path("home"))
            .env("XDG_CONFIG_HOME", root.path("config"))
            .env("XDG_DATA_HOME", root.path("data"))
            .env("TMPDIR", &root.0)
            .env("TEMP", &root.0)
            .env("TMP", &root.0)
            .env("LANG", "C")
            .env("LC_ALL", "C")
            .env("TZ", "UTC")
            .env("TERM", "dumb")
            .env("NO_COLOR", "1");
        if let Some(alias) = case.env.as_deref() {
            let path = match alias {
                "env" => env_vault.as_path(),
                other => panic!("unknown fixture env vault {other}"),
            };
            command.env("SYMDESK_VAULT", path);
        }
        if let Some(alias) = case.flag.as_deref() {
            let path = match alias {
                "flag" => flag_vault.as_path(),
                other => panic!("unknown fixture flag vault {other}"),
            };
            command.arg(format!("--vault={}", path.display()));
        } else if case.empty_flag {
            command.arg("--vault=");
        }
        let output = command
            .args(["ls", "--json"])
            .output()
            .expect("run Rust CLI");
        assert_eq!(
            output.status.code(),
            Some(0),
            "case {}: stdout={:?}, stderr={:?}",
            case.id,
            output.stdout,
            output.stderr
        );
        let listed: Vec<serde_json::Value> = serde_json::from_slice(&output.stdout)
            .unwrap_or_else(|error| panic!("case {}: invalid listing JSON: {error}", case.id));
        let expected_marker = format!("{}.md", case.selected);
        assert!(
            listed.iter().any(|entry| entry["path"] == expected_marker),
            "case {} selected {:?}, output {:?}",
            case.id,
            case.selected,
            output.stdout
        );
    }
}
