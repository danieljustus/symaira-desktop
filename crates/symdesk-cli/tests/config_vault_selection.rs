#![deny(unsafe_code)]

#[path = "../../../scripts/rust-port/rust/oracle_identity.rs"]
mod oracle_identity;

use std::sync::atomic::{AtomicU64, Ordering};
static TEMP_DIR_COUNTER: AtomicU64 = AtomicU64::new(0);

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
        // Parallel tests can read the same clock value (coarse on macOS).
        let nonce = format!(
            "{nonce}-{}",
            TEMP_DIR_COUNTER.fetch_add(1, Ordering::Relaxed)
        );
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
    assert_eq!(fixture.oracle_commit, oracle_identity::commit());
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

#[cfg(unix)]
#[test]
fn consumed_raw_environment_is_rejected_without_defaulting_or_overriding_flags() {
    use std::{ffi::OsString, os::unix::ffi::OsStringExt};

    let root = TempRoot::new();
    let isolated = || {
        let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        command
            .env_clear()
            .current_dir(&root.0)
            .env("HOME", root.path("home"))
            .env("USERPROFILE", root.path("home"))
            .env("XDG_CONFIG_HOME", root.path("config"))
            .env("XDG_DATA_HOME", root.path("data"))
            .env("TMPDIR", &root.0)
            .env("TEMP", &root.0)
            .env("TMP", &root.0)
            .env("PATH", root.path("no-credential-tools"))
            // A missing fixture helper yields no key, even if URL validation regresses.
            .env("SYMDESK_LLM_API_KEY", "op://missing-fixture-key")
            .env(
                "SYMDESK_SERVER_TOKEN",
                "fixture-admin-token-no-real-authority-12345678",
            );
        command
    };
    for name in [
        "SYMDESK_SERVER_TOKEN",
        "SYMDESK_WORKER_TOKEN",
        "SYMDESK_SERVER_LISTEN",
        "SYMDESK_ANTHROPIC_URL",
        "SYMDESK_OLLAMA_MODEL",
    ] {
        let mut command = isolated();
        command.env(name, OsString::from_vec(vec![0xff]));
        let expected_exit = if name == "SYMDESK_ANTHROPIC_URL" || name == "SYMDESK_OLLAMA_MODEL" {
            command.args(["transform", "summarize", "--text=fixture"]);
            if name == "SYMDESK_ANTHROPIC_URL" {
                command.env("SYMDESK_LLM_PROVIDER", "anthropic");
            } else {
                command.env("SYMDESK_OLLAMA_URL", "http://127.0.0.1:0");
            }
            0
        } else {
            command
                .arg(format!("--vault={}", root.0.display()))
                .arg("serve");
            if name == "SYMDESK_SERVER_LISTEN" {
                // The former fallback must fail token validation before it could bind.
                command.env("SYMDESK_SERVER_TOKEN", "");
            } else {
                command.arg("--listen=invalid-fixture-address");
            }
            1
        };
        let output = command.output().expect("run consumed environment control");
        assert_eq!(output.status.code(), Some(expected_exit), "{name}");
        let message = format!(
            "{}{}",
            String::from_utf8_lossy(&output.stdout),
            String::from_utf8_lossy(&output.stderr)
        );
        assert!(
            message.contains(&format!("environment variable {name} is not valid UTF-8")),
            "{name}: {message}"
        );
    }
    for (name, flag) in [
        (
            "SYMDESK_SERVER_TOKEN",
            "--token=fixture-admin-token-no-real-authority-12345678",
        ),
        (
            "SYMDESK_WORKER_TOKEN",
            "--worker-token=fixture-worker-token-no-real-authority-12345678",
        ),
        ("SYMDESK_SERVER_LISTEN", "--listen=invalid-fixture-address"),
    ] {
        let output = isolated()
            .env(name, OsString::from_vec(vec![0xff]))
            .arg(format!("--vault={}", root.0.display()))
            .args(["serve", flag])
            .args((name != "SYMDESK_SERVER_LISTEN").then_some("--listen=invalid-fixture-address"))
            .output()
            .expect("run flag override control");
        assert_eq!(output.status.code(), Some(1), "{name}");
        let message = String::from_utf8_lossy(&output.stderr);
        assert!(
            message.contains("invalid listen address"),
            "{name}: {message}"
        );
        assert!(
            !message.contains("not valid UTF-8"),
            "a winning flag must not consume {name}"
        );
    }
}
