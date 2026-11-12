#![deny(unsafe_code)]
// Miri cannot execute the native Go process; native Cargo/CI must run this gate.
#![cfg(not(miri))]

use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    path::Path,
    time::{SystemTime, UNIX_EPOCH},
};

use regex as _;
use serde::Deserialize;
use symdesk_core::config;
use time as _;
use toml as _;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Fixture {
    schema_version: u8,
    complete: bool,
    oracle: serde_json::Value,
    goos: String,
    goarch: String,
    go_version: String,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Case {
    id: String,
    root: String,
    environment: BTreeMap<String, String>,
    contacts: [String; 4],
    ingest: Vec<Ingest>,
    before: BTreeMap<String, String>,
    after: BTreeMap<String, String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Ingest {
    name: String,
    value: String,
    error: String,
}

struct RestoreWorkingDirectory(std::path::PathBuf);

impl Drop for RestoreWorkingDirectory {
    fn drop(&mut self) {
        std::env::set_current_dir(&self.0).expect("restore native replay working directory");
    }
}

fn private_case_root(capture: &Path, id: &str, path: &Path) -> bool {
    path.parent() == Some(capture)
        && path == capture.join(id)
        && fs::symlink_metadata(path)
            .is_ok_and(|metadata| metadata.is_dir() && !metadata.file_type().is_symlink())
}

fn snapshot(root: &Path) -> BTreeMap<String, String> {
    fn visit(root: &Path, path: &Path, result: &mut BTreeMap<String, String>) {
        let relative = path.strip_prefix(root).expect("contained capture entry");
        let key = if relative.as_os_str().is_empty() {
            ".".to_owned()
        } else {
            relative
                .to_str()
                .expect("UTF-8 synthetic path")
                .replace(std::path::MAIN_SEPARATOR, "/")
        };
        let metadata =
            fs::symlink_metadata(path).expect("inspect capture entry without following links");
        if metadata.is_dir() && !metadata.file_type().is_symlink() {
            result.insert(key, "directory".to_owned());
            for entry in fs::read_dir(path).expect("read private capture directory") {
                visit(root, &entry.expect("capture entry").path(), result);
            }
        } else {
            use std::fmt::Write as _;
            let (prefix, bytes) = if metadata.file_type().is_symlink() {
                let target = fs::read_link(path).expect("retain native link target");
                (
                    "symlink:",
                    target
                        .to_str()
                        .expect("UTF-8 synthetic link")
                        .as_bytes()
                        .to_vec(),
                )
            } else {
                (
                    "file:",
                    fs::read(path).expect("read seeded synthetic store"),
                )
            };
            let mut value = String::from(prefix);
            for byte in bytes {
                write!(value, "{byte:02x}").expect("write String");
            }
            result.insert(key, value);
        }
    }
    let mut result = BTreeMap::new();
    visit(root, root, &mut result);
    result
}

/// Native Go captures of `configgen --store-paths-root`, one per supported OS,
/// recorded by `scripts/rust-port/store_paths_record.py` (macOS arm64 directly,
/// Linux arm64 inside the `symdesk-local-gate` container). They replace the
/// former live `go run` so the test needs no Go once Go is deleted.
#[cfg(target_os = "macos")]
const RECORDED_CAPTURE: &str =
    include_str!("../../../testdata/port/storage/store-paths-darwin.json");
#[cfg(target_os = "linux")]
const RECORDED_CAPTURE: &str =
    include_str!("../../../testdata/port/storage/store-paths-linux.json");
#[cfg(not(any(target_os = "macos", target_os = "linux")))]
const RECORDED_CAPTURE: &str = "";

fn hex_decode(value: &str) -> Vec<u8> {
    assert_eq!(value.len() % 2, 0, "even hex length");
    (0..value.len())
        .step_by(2)
        .map(|index| u8::from_str_radix(&value[index..index + 2], 16).expect("hex byte"))
        .collect()
}

fn hex_encode(bytes: &[u8]) -> String {
    use std::fmt::Write as _;
    let mut value = String::new();
    for byte in bytes {
        write!(value, "{byte:02x}").expect("write String");
    }
    value
}

/// Rewrites the capture root recorded by Go to this run's private root. Symlink
/// snapshots carry their targets hex-encoded, so those are rewritten too.
fn retarget(snapshot: &mut BTreeMap<String, String>, recorded: &str, capture: &str) {
    for value in snapshot.values_mut() {
        if let Some(hex) = value.strip_prefix("symlink:") {
            let target = String::from_utf8(hex_decode(hex)).expect("UTF-8 synthetic link");
            *value = format!(
                "symlink:{}",
                hex_encode(target.replace(recorded, capture).as_bytes())
            );
        }
    }
}

/// Recreates a case's seeded state from its recorded `before` snapshot, the
/// state the Go producer wrote before it resolved any path.
#[cfg(unix)]
fn materialize(case_root: &Path, before: &BTreeMap<String, String>) {
    fs::create_dir_all(case_root).expect("case root");
    for (key, value) in before {
        if key == "." {
            continue;
        }
        let path = case_root.join(key);
        if value == "directory" {
            fs::create_dir_all(&path).expect("seed directory");
        } else if let Some(hex) = value.strip_prefix("file:") {
            fs::create_dir_all(path.parent().expect("seed parent")).expect("seed parent");
            fs::write(&path, hex_decode(hex)).expect("seed file");
        } else if let Some(hex) = value.strip_prefix("symlink:") {
            fs::create_dir_all(path.parent().expect("seed parent")).expect("seed parent");
            let target = String::from_utf8(hex_decode(hex)).expect("UTF-8 synthetic link");
            std::os::unix::fs::symlink(target, &path).expect("seed symlink");
        } else {
            panic!("unknown recorded snapshot entry {key:?}");
        }
    }
}

#[cfg(not(unix))]
fn materialize(_case_root: &Path, _before: &BTreeMap<String, String>) {
    panic!("Go store-path captures are recorded for macOS and Linux only");
}

#[test]
fn contacts_and_ingest_paths_match_native_go() {
    // Native Go capture per OS (committed, recorded by Go), replayed into a
    // fresh private root. No inherited environment reaches Rust.
    assert!(!RECORDED_CAPTURE.is_empty(), "no Go capture for this OS");
    let repository = Path::new(env!("CARGO_MANIFEST_DIR"))
        .parent()
        .and_then(Path::parent)
        .expect("workspace root");
    let nonce = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock after epoch")
        .as_nanos();
    let parent = std::env::var_os("SYMDESK_CFG_CAPTURE_DIR")
        .map_or_else(std::env::temp_dir, std::path::PathBuf::from);
    fs::create_dir_all(&parent).expect("capture output directory");
    let root = parent.join(format!("symdesk-cfg-paths-{}-{nonce}", std::process::id()));
    let maker = fs::DirBuilder::new();
    #[cfg(unix)]
    let maker = {
        use std::os::unix::fs::DirBuilderExt;
        let mut maker = maker;
        maker.mode(0o700);
        maker
    };
    maker.create(&root).expect("fresh private capture parent");
    // Go resolved its root before capturing; resolve ours the same way so a
    // cwd-relative XDG value (macOS /var -> /private/var) names the same path.
    let root = fs::canonicalize(root).expect("canonical native root");
    let capture = root.join("capture");
    fs::create_dir(&capture).expect("private capture directory");
    let capture_text = capture.to_str().expect("UTF-8 capture root").to_owned();
    assert!(
        !capture_text.contains(['"', '\\']),
        "capture root must not need JSON escaping"
    );

    let probe: serde_json::Value =
        serde_json::from_str(RECORDED_CAPTURE).expect("recorded capture");
    let recorded_root = Path::new(
        probe["cases"][0]["root"]
            .as_str()
            .expect("recorded case root"),
    )
    .parent()
    .and_then(Path::to_str)
    .expect("recorded capture root")
    .to_owned();
    let raw = RECORDED_CAPTURE.replace(&recorded_root, &capture_text);
    let mut fixture: Fixture = serde_json::from_str(&raw).expect("strict native capture schema");
    for case in &mut fixture.cases {
        retarget(&mut case.before, &recorded_root, &capture_text);
        retarget(&mut case.after, &recorded_root, &capture_text);
        materialize(Path::new(&case.root), &case.before);
    }
    let mut unexpected: serde_json::Value = serde_json::from_str(&raw).expect("capture control");
    unexpected
        .as_object_mut()
        .expect("capture object")
        .insert("unexpected_top_level".to_owned(), true.into());
    assert!(
        serde_json::from_slice::<Fixture>(
            &serde_json::to_vec(&unexpected).expect("serialize unknown-field control")
        )
        .is_err(),
        "unknown top-level fields must fail through the actual capture decoder"
    );
    assert_eq!(fixture.schema_version, 1);
    assert!(
        fixture.complete,
        "partial Go observations never certify parity"
    );
    assert_eq!(fixture.go_version, "go1.26.10");
    assert_eq!(
        fixture.goos,
        if cfg!(target_os = "macos") {
            "darwin"
        } else {
            std::env::consts::OS
        }
    );
    // Both captures were recorded on arm64 (macOS directly, Linux in the gate
    // container). The store paths do not depend on the architecture, so an
    // amd64 host replays the arm64 recording of its OS.
    assert_eq!(fixture.goarch, "arm64");
    // ConfigDir and Go CoreKit's DefaultPath share the native-home/absolute-XDG
    // base rules for these captured cases. Derive the filename from the live Go
    // output instead of adding another hand-authored path fixture.
    let go_config_path = |case: &Case| {
        Path::new(&case.contacts[0])
            .join("config.toml")
            .to_string_lossy()
            .into_owned()
    };
    let home_defaults = fixture
        .cases
        .iter()
        .find(|case| case.id == "home-defaults")
        .expect("Go capture has HOME defaults");
    let no_home = fixture
        .cases
        .iter()
        .find(|case| case.id == "no-home")
        .expect("Go capture has an empty-home case");
    let different_home_profile = fixture
        .cases
        .iter()
        .find(|case| case.id == "different-home-profile")
        .expect("Go capture separates HOME and USERPROFILE");
    let mut home_only = home_defaults.environment.clone();
    home_only.remove("USERPROFILE");
    assert_eq!(
        config::global_path(&home_only),
        go_config_path(if cfg!(windows) {
            no_home
        } else {
            home_defaults
        }),
        "HOME-only selection matches the native Go observation"
    );
    let mut userprofile_only = different_home_profile.environment.clone();
    userprofile_only.remove("HOME");
    assert_eq!(
        config::global_path(&userprofile_only),
        go_config_path(if cfg!(windows) {
            different_home_profile
        } else {
            no_home
        }),
        "USERPROFILE-only selection matches the native Go observation"
    );
    assert_eq!(
        config::global_path(&BTreeMap::new()),
        go_config_path(no_home),
        "empty environment matches the native Go fallback"
    );
    let absolute_xdg = fixture
        .cases
        .iter()
        .find(|case| case.id == "xdg-without-home")
        .expect("Go capture has absolute XDG without a home");
    assert_eq!(
        config::global_path(&absolute_xdg.environment),
        go_config_path(absolute_xdg),
        "absolute XDG remains authoritative without either home variable"
    );
    let provenance: serde_json::Value = serde_json::from_slice(
        &fs::read(repository.join("testdata/port/provenance.json"))
            .expect("canonical oracle provenance"),
    )
    .expect("parse oracle provenance");
    assert_eq!(fixture.oracle, provenance["oracle"]);
    let expected_ids = [
        "fresh",
        "legacy-files",
        "both-files",
        "primary-directory",
        "legacy-directory",
        "mixed-archive",
        "contacts-overrides",
        "padded-overrides",
        "blank-overrides",
        "home-defaults",
        "no-home",
        "xdg-without-home",
        "relative-xdg",
        "lexical-xdg",
        "different-home-profile",
        "legacy-symlink-files",
        "legacy-symlink-directories",
        "legacy-dangling-symlinks",
        "primary-symlinks",
        "primary-dangling-symlinks",
    ];
    let expected_names = [
        "symingest.db",
        "archive",
        " archive ",
        "./archive",
        "nested/../archive",
        r"nested\file",
        "..",
        " . ",
        "nested/file",
        "nested/../..",
        "../escape",
        "/absolute",
        r"C:relative",
        r"C:\absolute",
        r"\absolute",
        "nested/\"\n",
        "nested/\x00\x7f\u{85}\u{a0}\u{200b}\u{2028}",
        "nested/😀",
        "",
    ];
    assert_eq!(fixture.cases.len(), expected_ids.len());
    assert!(private_case_root(&capture, "fresh", &capture.join("fresh")));
    for invalid in ["../outside", "fresh/../outside", "fresh/home"] {
        assert!(!private_case_root(
            &capture,
            "fresh",
            &capture.join(invalid)
        ));
    }
    // Exercise link rejection through the same guard, without changing Go seeds.
    let control = root.join("case-root-control");
    fs::create_dir(&control).expect("private root-guard control");
    let link = control.join("fresh");
    #[cfg(unix)]
    std::os::unix::fs::symlink(capture.join("fresh"), &link).expect("root-link control");
    #[cfg(windows)]
    std::os::windows::fs::symlink_dir(capture.join("fresh"), &link).expect("root-link control");
    assert!(!private_case_root(&control, "fresh", &link));
    let mut seen = BTreeSet::new();
    // ponytail: this integration binary has one test; use a child process
    // before adding parallel tests that also depend on the working directory.
    let _restore_directory = RestoreWorkingDirectory(
        std::env::current_dir().expect("original replay working directory"),
    );
    for case in &fixture.cases {
        assert!(expected_ids.contains(&case.id.as_str()), "unknown case ID");
        assert!(seen.insert(case.id.as_str()), "duplicate case {}", case.id);
        assert_eq!(
            case.before, case.after,
            "Go resolver wrote state: {}",
            case.id
        );
        let case_root = Path::new(&case.root);
        assert!(
            private_case_root(&capture, &case.id, case_root),
            "capture path containment"
        );
        std::env::set_current_dir(case_root).expect("private case working directory");
        assert_eq!(
            snapshot(case_root),
            case.before,
            "seeded Go state: {}",
            case.id
        );
        let paths = config::contacts_paths(&case.environment);
        assert_eq!(
            [
                paths.config_dir,
                paths.data_dir,
                paths.cache_dir,
                paths.db_path
            ],
            case.contacts,
            "contacts {}",
            case.id
        );
        assert_eq!(
            case.ingest
                .iter()
                .map(|item| item.name.as_str())
                .collect::<Vec<_>>(),
            expected_names,
            "complete input inventory {}",
            case.id
        );
        for item in &case.ingest {
            let actual = config::ingest_data_path(&case.environment, &item.name);
            let expected = if item.error.is_empty() {
                Ok(item.value.clone())
            } else {
                assert!(
                    item.value.is_empty(),
                    "error is distinct from a successful empty path"
                );
                Err(item.error.clone())
            };
            assert_eq!(actual, expected, "ingest {} name {:?}", case.id, item.name);
        }
        assert_eq!(
            snapshot(case_root),
            case.before,
            "Rust resolver wrote state: {}",
            case.id
        );
    }
    assert_eq!(seen, BTreeSet::from(expected_ids));
    println!(
        "PASS recorded native contacts/ingest: {} layouts, {} ingest observations, unchanged seeded state",
        fixture.cases.len(),
        fixture
            .cases
            .iter()
            .map(|case| case.ingest.len())
            .sum::<usize>()
    );
}
