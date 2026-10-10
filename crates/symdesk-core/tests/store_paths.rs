#![deny(unsafe_code)]
// Miri cannot execute the native Go process; native Cargo/CI must run this gate.
#![cfg(not(miri))]

use std::{
    collections::{BTreeMap, BTreeSet},
    fs,
    path::Path,
    process::{Command, Stdio},
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

#[test]
fn contacts_and_ingest_paths_match_native_go() {
    // Fresh native Go capture on every OS, not a hand-authored expectation or
    // a platform-normalized Darwin file. No inherited environment reaches Rust.
    let crate_root = Path::new(env!("CARGO_MANIFEST_DIR"));
    let repository = crate_root
        .parent()
        .and_then(Path::parent)
        .expect("workspace root");
    let nonce = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("clock after epoch")
        .as_nanos();
    let parent = std::env::var_os("SYMDESK_CFG_CAPTURE_DIR")
        .map_or_else(std::env::temp_dir, std::path::PathBuf::from);
    fs::create_dir_all(&parent).expect("native capture output directory");
    let root = parent.join(format!("symdesk-cfg-paths-{}-{nonce}", std::process::id()));
    let builder = fs::DirBuilder::new();
    #[cfg(unix)]
    let builder = {
        use std::os::unix::fs::DirBuilderExt;
        let mut builder = builder;
        builder.mode(0o700);
        builder
    };
    builder.create(&root).expect("fresh private capture parent");
    // Keep the native drive volume: Windows canonicalize introduces \\?\,
    // which Go filepath.Rel cannot compare to its ordinary-drive working directory.
    let root = std::path::absolute(root).expect("absolute native root");
    let capture = root.join("capture");
    let output = capture.join("native-go.json");
    let stdout = fs::File::create(root.join("go.stdout.log")).expect("retain producer stdout");
    let stderr = fs::File::create(root.join("go.stderr.log")).expect("retain producer stderr");
    let status = Command::new("go")
        .current_dir(crate_root)
        .env("GOTOOLCHAIN", "go1.26.9")
        .env("CGO_ENABLED", "0")
        .arg("run")
        .arg(repository.join("scripts/rust-port/cmd/configgen"))
        .arg("--store-paths-root")
        .arg(&capture)
        .arg("--output")
        .arg(&output)
        .stdin(Stdio::null())
        .stdout(stdout)
        .stderr(stderr)
        .status()
        .expect("execute genuine Go producer; Go is required, never skipped");
    assert!(
        status.success(),
        "native Go capture failed: {status}; original logs retained at {}",
        root.display()
    );
    let raw = fs::read(&output).expect("retain/read complete original Go capture");
    let fixture: Fixture = serde_json::from_slice(&raw).expect("strict native capture schema");
    let mut unexpected: serde_json::Value = serde_json::from_slice(&raw).expect("capture control");
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
    assert_eq!(fixture.go_version, "go1.26.9");
    assert_eq!(
        fixture.goos,
        if cfg!(target_os = "macos") {
            "darwin"
        } else {
            std::env::consts::OS
        }
    );
    assert_eq!(
        fixture.goarch,
        match std::env::consts::ARCH {
            "aarch64" => "arm64",
            "x86_64" => "amd64",
            arch => arch,
        }
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
    let mut seen = BTreeSet::new();
    for case in &fixture.cases {
        assert!(seen.insert(case.id.as_str()), "duplicate case {}", case.id);
        assert_eq!(
            case.before, case.after,
            "Go resolver wrote state: {}",
            case.id
        );
        let case_root = Path::new(&case.root);
        assert!(
            case_root.starts_with(&capture) && case_root != capture,
            "capture path containment"
        );
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
    assert_eq!(fs::read(&output).expect("original capture unchanged"), raw);
    println!(
        "PASS native contacts/ingest: {} layouts, {} ingest observations, unchanged seeded state; retained {}",
        fixture.cases.len(),
        fixture
            .cases
            .iter()
            .map(|case| case.ingest.len())
            .sum::<usize>(),
        output.display()
    );
}
