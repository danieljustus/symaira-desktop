#![deny(unsafe_code)]

use std::{
    fs,
    path::{Path, PathBuf},
    process::{Command, Output},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;

const ORACLE_REVISION: &str = "07eb8d9fefcc9e150302558ea0613d49e6dfc201";

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_revision: String,
    source_hashes: std::collections::BTreeMap<String, String>,
    member_id: String,
    room_toml: String,
    journal_files: Vec<FixtureFile>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct FixtureFile {
    path: String,
    content: String,
}

#[derive(Deserialize)]
struct Case {
    name: String,
    args: Vec<String>,
    #[serde(default)]
    path_mode: String,
    exit_code: i32,
    stdout: String,
    stderr: String,
    final_files: Vec<ExpectedFile>,
    #[cfg_attr(not(unix), allow(dead_code))]
    #[serde(default)]
    dir_mode: u32,
}

#[derive(Deserialize)]
struct ExpectedFile {
    path: String,
    content: String,
    #[cfg_attr(not(unix), allow(dead_code))]
    mode: u32,
}

#[test]
fn brain_profile_cli_matches_go_process_output_and_install_files() {
    let fixture_path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/room/brain-profile-cli.json");
    let data = fs::read(fixture_path).expect("read Go-generated brain-profile fixture");
    let fixture: Fixture = serde_json::from_slice(&data).expect("parse brain-profile fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle_revision, ORACLE_REVISION);
    assert_eq!(fixture.source_hashes.len(), 3);
    assert!(fixture.source_hashes.values().all(|hash| hash.len() == 64));
    assert_eq!(fixture.journal_files.len(), 1);
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| case.name == "install-no-symbrain")
    );
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| case.name == "install-symbrain")
    );
    assert!(
        fixture
            .cases
            .iter()
            .any(|case| case.name == "install-fallback")
    );
    assert!(
        fixture
            .cases
            .iter()
            .find(|case| case.name == "render")
            .is_some_and(|case| case.stdout.contains(&fixture.member_id))
    );

    let temp = TempDir::new();
    for case in &fixture.cases {
        if cfg!(windows) && matches!(case.path_mode.as_str(), "helper" | "helper-fails") {
            continue;
        }
        let isolated = temp.path.join(&case.name);
        let home = isolated.join("home");
        let room = isolated.join("room");
        let journal = room.join("journal");
        let tmp = isolated.join("tmp");
        let path_dir = isolated.join("path");
        fs::create_dir_all(&home).expect("create isolated HOME");
        fs::create_dir_all(&journal).expect("create fixture journal");
        fs::create_dir_all(&tmp).expect("create isolated TMPDIR");
        fs::create_dir_all(&path_dir).expect("create isolated PATH");
        fs::write(room.join("room.toml"), &fixture.room_toml).expect("write fixture room config");
        for file in &fixture.journal_files {
            fs::write(journal.join(&file.path), file.content.as_bytes())
                .expect("write Go-generated journal event");
        }
        #[cfg(unix)]
        if case.path_mode == "helper" || case.path_mode == "helper-fails" {
            write_fake_symbrain(&path_dir, case.path_mode == "helper-fails");
        }

        let output = run_symroom(&case.args, &home, &room, &path_dir, &tmp);
        assert_eq!(
            output.status.code(),
            Some(case.exit_code),
            "exit code case {}",
            case.name
        );
        assert_eq!(
            normalize_home(&output.stdout, &home),
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
        let profiles = home.join(".config/symbrain/profiles");
        let actual = read_profile_files(&profiles);
        let expected: Vec<_> = case
            .final_files
            .iter()
            .map(|file| (file.path.clone(), file.content.clone()))
            .collect();
        assert_eq!(actual, expected, "profile files case {}", case.name);
        #[cfg(unix)]
        if !expected.is_empty() {
            assert_eq!(
                mode(&profiles),
                case.dir_mode,
                "profile directory mode case {}",
                case.name
            );
            assert_eq!(
                mode(&profiles.join(&case.final_files[0].path)),
                case.final_files[0].mode,
                "profile file mode case {}",
                case.name
            );
        }
    }
}

fn run_symroom(args: &[String], home: &Path, room: &Path, path: &Path, temp: &Path) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_symroom"));
    command
        .args(args)
        .env_clear()
        .env("HOME", home)
        .env("USERPROFILE", home)
        .env("PATH", path)
        .env("TMPDIR", temp)
        .env("TZ", "UTC")
        .env("LC_ALL", "C")
        .env("LANG", "C")
        .env("SYMROOM_ROOM_DIR", room);
    command
        .output()
        .expect("run Rust symroom brain-profile CLI")
}

#[cfg(unix)]
fn write_fake_symbrain(path: &Path, fail: bool) {
    use std::os::unix::fs::PermissionsExt;
    let script = if fail {
        "#!/bin/sh\n/bin/cat >/dev/null\nexit 1\n"
    } else {
        "#!/bin/sh\nprintf 'called %s %s %s\\n' \"$1\" \"$2\" \"$3\"\n/bin/cat\n"
    };
    let executable = path.join("symbrain");
    fs::write(&executable, script).expect("write fake symbrain");
    fs::set_permissions(executable, fs::Permissions::from_mode(0o755))
        .expect("make fake symbrain executable");
}

fn normalize_home(output: &[u8], home: &Path) -> Vec<u8> {
    String::from_utf8(output.to_vec())
        .expect("symroom output is UTF-8")
        .replace(&home.to_string_lossy().to_string(), "<HOME>")
        .into_bytes()
}

fn read_profile_files(dir: &Path) -> Vec<(String, String)> {
    let Ok(entries) = fs::read_dir(dir) else {
        return Vec::new();
    };
    let mut files = entries
        .map(|entry| {
            let path = entry.expect("read profile entry").path();
            let name = path
                .file_name()
                .expect("profile filename")
                .to_string_lossy()
                .into_owned();
            let content = fs::read_to_string(path).expect("read installed profile");
            (name, content)
        })
        .collect::<Vec<_>>();
    files.sort_by(|left, right| left.0.cmp(&right.0));
    files
}

#[cfg(unix)]
fn mode(path: &Path) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    fs::metadata(path)
        .expect("read profile permissions")
        .permissions()
        .mode()
        & 0o7777
}

struct TempDir {
    path: PathBuf,
}

impl TempDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock after Unix epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symroom-brain-profile-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create isolated test directory");
        Self { path }
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.path).expect("remove isolated test directory");
    }
}
