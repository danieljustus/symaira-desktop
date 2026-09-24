#![deny(unsafe_code)]

use std::{
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

const ORACLE_REVISION: &str = "439d04347bb2881495bff3acc52a43dc7bff6d39";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: std::collections::BTreeMap<String, String>,
    seed_files: Vec<IdentityFile>,
    cases: Vec<Case>,
}

#[derive(Clone, Deserialize)]
struct IdentityFile {
    name: String,
    content: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    args: Vec<String>,
    #[serde(default)]
    empty_store: bool,
    #[serde(default)]
    list_extras: bool,
    #[serde(default)]
    dynamic_keys: bool,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_files: Vec<IdentityFile>,
}

#[test]
fn identity_cli_matches_go_process_and_key_file_contract() {
    let fixture_path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/room/identity-cli.json");
    let data = fs::read(fixture_path).expect("read Go-generated identity fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse identity fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 3);
    assert!(fixture.source_hashes.values().all(|hash| hash.len() == 64));
    assert!(fixture.cases.iter().any(|case| case.dynamic_keys));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 0));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 2));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 5));
    assert!(fixture.cases.iter().any(|case| case.exit_code == 4));

    let temp = TempDir::new();
    for case in &fixture.cases {
        let isolated = temp.path.join(&case.name);
        let home = isolated.join("home");
        let data_home = isolated.join("data");
        let tmp = isolated.join("tmp");
        let identities = data_home.join("symroom/identities");
        fs::create_dir_all(&home).expect("create isolated HOME");
        fs::create_dir_all(&tmp).expect("create isolated TMPDIR");
        fs::create_dir_all(&identities).expect("create identities directory");
        if !case.empty_store {
            for file in &fixture.seed_files {
                fs::write(identities.join(&file.name), file.content.as_bytes())
                    .expect("write fixture identity");
            }
        }
        if case.list_extras {
            fs::create_dir(identities.join("nested.json")).expect("create ignored JSON directory");
            fs::write(identities.join("ignored.json.bak"), b"ignored")
                .expect("create ignored backup file");
        }

        let output = run_symroom(&case.args, &home, &data_home, &tmp);
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "exit code case {}",
            case.name
        );
        assert_eq!(
            normalize_stdout(&output.stdout, case.dynamic_keys),
            case.stdout.as_bytes(),
            "stdout case {}",
            case.name
        );
        assert_eq!(
            output.stderr,
            case.stderr.as_bytes(),
            "stderr case {}",
            case.name
        );
        let actual = read_identity_files(&identities, case.dynamic_keys);
        let expected: Vec<_> = case
            .final_files
            .iter()
            .map(|file| (file.name.clone(), file.content.clone()))
            .collect();
        assert_eq!(actual, expected, "identity files case {}", case.name);
        if case.dynamic_keys {
            verify_generated_key(&identities.join("fresh.json"));
        }
    }
}

fn run_symroom(args: &[String], home: &Path, data_home: &Path, tmp: &Path) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(args)
        .env_clear()
        .env("HOME", home)
        .env("XDG_DATA_HOME", data_home)
        .env("TMPDIR", tmp)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C");
    command.output().expect("run Rust symroom identity CLI")
}

fn read_identity_files(dir: &Path, normalize_keys: bool) -> Vec<(String, String)> {
    let mut paths = fs::read_dir(dir)
        .expect("read identity directory")
        .map(|entry| entry.expect("read identity entry").path())
        .filter(|path| path.is_file() && path.extension().is_some_and(|ext| ext == "json"))
        .collect::<Vec<_>>();
    paths.sort();
    paths
        .into_iter()
        .map(|path| {
            let name = path
                .file_name()
                .expect("identity filename")
                .to_string_lossy()
                .into_owned();
            let content = fs::read_to_string(&path).expect("read identity JSON");
            let content = if normalize_keys && name == "fresh.json" {
                let mut stored: symroom_core::identity::StoredIdentity =
                    serde_json::from_str(&content).expect("parse generated identity");
                stored.member_id = "<member_id>".to_owned();
                stored.public_key = "<public_key>".to_owned();
                stored.private_key = "<private_key>".to_owned();
                go_json(&stored)
            } else {
                content
            };
            (name, content)
        })
        .collect()
}

fn go_json(value: &impl serde::Serialize) -> String {
    serde_json::to_string_pretty(value)
        .expect("serialize identity")
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

fn normalize_stdout(output: &[u8], dynamic_keys: bool) -> Vec<u8> {
    if !dynamic_keys {
        return output.to_vec();
    }
    let rendered = String::from_utf8(output.to_vec()).expect("create output is UTF-8");
    let member_id = rendered
        .strip_prefix("Created identity fresh (")
        .and_then(|value| value.strip_suffix(")\n"))
        .expect("create output shape");
    assert_eq!(member_id.len(), 20, "generated member ID length");
    assert!(member_id.starts_with("mem_"), "generated member ID prefix");
    assert!(member_id[4..].bytes().all(|byte| byte.is_ascii_hexdigit()));
    b"Created identity fresh (<member_id>)\n".to_vec()
}

fn verify_generated_key(path: &Path) {
    let content = fs::read_to_string(path).expect("read generated identity");
    let stored: symroom_core::identity::StoredIdentity =
        serde_json::from_str(&content).expect("parse generated key file");
    let public = hex::decode(stored.public_key).expect("decode public key");
    let private = hex::decode(stored.private_key).expect("decode private key");
    assert_eq!(public.len(), 32);
    assert_eq!(private.len(), 64);
    assert_eq!(&private[32..], public);
    let generated = symroom_core::identity::identity_from_private_key("fresh", &private)
        .expect("private key is internally consistent");
    assert_eq!(generated.member_id, stored.member_id);
    assert_eq!(generated.public_key, public);
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        assert_eq!(
            fs::metadata(path)
                .expect("stat generated file")
                .permissions()
                .mode()
                & 0o777,
            0o600
        );
        let directory = path.parent().expect("identity directory");
        assert_eq!(
            fs::metadata(directory)
                .expect("stat identity directory")
                .permissions()
                .mode()
                & 0o777,
            0o700
        );
    }
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("system clock after epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symroom-identity-cli-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir_all(&path).expect("create temporary test directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}
