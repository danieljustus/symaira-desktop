#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    io::Write,
    path::PathBuf,
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

use serde::Deserialize;

#[path = "support/isolated_root.rs"]
mod isolated_root;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    #[serde(default)]
    allow_write: bool,
    #[serde(default)]
    framed: bool,
    request: String,
    stdout: String,
    stderr: String,
    exit_code: i32,
    #[serde(default)]
    files: BTreeMap<String, String>,
    #[serde(default)]
    indexed: bool,
    #[serde(default)]
    external_link: bool,
    #[serde(default)]
    no_vault: bool,
    after_files: BTreeMap<String, String>,
    #[serde(default)]
    file_mtime: u64,
    #[serde(default)]
    results: BTreeMap<String, String>,
    #[serde(default)]
    after_results: BTreeMap<String, String>,
    sidecar_exists: bool,
    after_index: BTreeMap<String, BTreeMap<String, String>>,
}

#[test]
fn read_tools_match_go_stdio_calls_and_authoritative_files() {
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/mcp/read-process.json"),
        )
        .expect("Go process transcript fixture"),
    )
    .expect("decode Go process transcript");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 157);
    for case in fixture.cases {
        replay(&case);
    }
}

fn replay(case: &Case) {
    let root = isolated_root::create("symdesk-mcp-read");
    for dir in ["home", "config", "cache", "data", "tmp", "vault", "cwd"] {
        fs::create_dir_all(root.join(dir)).unwrap();
    }
    let root = root.canonicalize().unwrap();
    for (path, body) in &case.files {
        let path = root.join("vault").join(path);
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(&path, body).unwrap();
        if case.file_mtime > 0 {
            let stamp = std::time::UNIX_EPOCH + Duration::from_secs(case.file_mtime);
            fs::File::options()
                .write(true)
                .open(path)
                .unwrap()
                .set_times(fs::FileTimes::new().set_accessed(stamp).set_modified(stamp))
                .unwrap();
        }
    }
    let hash = symdesk_vault::sha256_hex(root.join("vault").to_string_lossy().as_bytes());
    let results_root = root.join("data/symdesk/agent-results").join(&hash[..16]);
    for (path, body) in &case.results {
        let path = results_root.join(path);
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(path, body).unwrap();
    }
    if case.indexed {
        let mut sidecar = symdesk_index::Sidecar::open(&root.join("data/sidecar.db")).unwrap();
        for (path, body) in &case.files {
            let path = root.join("vault").join(path);
            let document =
                symdesk_vault::parse_bytes(&path.to_string_lossy(), body.as_bytes()).unwrap();
            let indexed = symdesk_index::IndexedDocument::from_vault(&document, None).unwrap();
            sidecar.index_document(&indexed).unwrap();
        }
        if case.external_link {
            let path = root.join("external/link.md");
            fs::create_dir_all(path.parent().unwrap()).unwrap();
            fs::write(&path, "[[alpha]]\n").unwrap();
            let document =
                symdesk_vault::parse_bytes(&path.to_string_lossy(), b"[[alpha]]\n").unwrap();
            sidecar
                .index_document(
                    &symdesk_index::IndexedDocument::from_vault(&document, None).unwrap(),
                )
                .unwrap();
        }
    }
    let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
    if case.no_vault {
        command.arg("mcp");
    } else {
        command.args(["--vault", root.join("vault").to_str().unwrap(), "mcp"]);
    }
    if case.allow_write {
        command.arg("--allow-write");
    }
    command.env_clear();
    command.env("SYMDESK_SIDECAR", root.join("data/sidecar.db"));
    for (key, dir) in [
        ("HOME", "home"),
        ("USERPROFILE", "home"),
        ("XDG_CONFIG_HOME", "config"),
        ("XDG_CACHE_HOME", "cache"),
        ("XDG_DATA_HOME", "data"),
        ("TMPDIR", "tmp"),
        ("TMP", "tmp"),
        ("TEMP", "tmp"),
    ] {
        command.env(key, root.join(dir));
    }
    if let Some(value) = std::env::var_os("SYSTEMROOT") {
        command.env("SYSTEMROOT", value);
    }
    command
        .env("PATH", "")
        .env("LANG", "C")
        .env("LC_ALL", "C")
        .env("TZ", "UTC");
    let mut child = command
        .current_dir(root.join("cwd"))
        .stdin(Stdio::piped())
        .stdout(fs::File::create(root.join("stdout")).unwrap())
        .stderr(fs::File::create(root.join("stderr")).unwrap())
        .spawn()
        .expect("start native MCP without Go fallback");
    let input = if case.framed {
        format!(
            "Content-Length: {}\r\n\r\n{}",
            case.request.len(),
            case.request
        )
    } else {
        format!("{}\n", case.request)
    };
    child
        .stdin
        .take()
        .unwrap()
        .write_all(input.as_bytes())
        .unwrap();
    let deadline = Instant::now() + Duration::from_secs(30);
    let status = loop {
        if let Some(status) = child.try_wait().unwrap() {
            break status;
        }
        if Instant::now() >= deadline {
            child.kill().unwrap();
            child.wait().unwrap();
            panic!("MCP catalog deadline: {}", case.id);
        }
        thread::sleep(Duration::from_millis(5));
    };
    let stdout = fs::read(root.join("stdout")).unwrap();
    let stderr = fs::read(root.join("stderr")).unwrap();
    assert_eq!(
        root.join("data/sidecar.db").exists(),
        case.sidecar_exists,
        "{} sidecar creation",
        case.id
    );
    let mut after_index = BTreeMap::new();
    if case.sidecar_exists {
        let sidecar = symdesk_index::Sidecar::open(&root.join("data/sidecar.db")).unwrap();
        for file in sidecar.list_files(&root.join("vault"), "").unwrap() {
            let path = PathBuf::from(&file.path);
            let relative = path
                .strip_prefix(root.join("vault"))
                .map_or_else(
                    |_| format!("../{}", path.strip_prefix(&root).unwrap().to_string_lossy()),
                    |relative| relative.to_string_lossy().into_owned(),
                )
                .replace(std::path::MAIN_SEPARATOR, "/");
            after_index.insert(relative, sidecar.get_properties(&file.path).unwrap());
        }
    }
    assert_eq!(
        after_index, case.after_index,
        "{} derived index state",
        case.id
    );
    if case.external_link {
        assert_eq!(
            fs::read_to_string(root.join("external/link.md")).unwrap(),
            "[[alpha]]\n"
        );
    }
    let mut after = BTreeMap::new();
    collect_files(&root.join("vault"), &root.join("vault"), &mut after);
    let stdout = String::from_utf8(stdout)
        .unwrap()
        .replace(root.to_str().unwrap(), "$ROOT");
    let stderr = String::from_utf8(stderr)
        .unwrap()
        .replace(root.to_str().unwrap(), "$ROOT");
    if !case.results.is_empty() {
        let mut results = BTreeMap::new();
        collect_files(&results_root, &results_root, &mut results);
        assert_eq!(
            results, case.after_results,
            "{} externalized files",
            case.id
        );
    }
    fs::remove_dir_all(root).unwrap();
    assert_eq!(after, case.after_files, "{} authoritative files", case.id);
    assert_eq!(status.code(), Some(case.exit_code), "{}", case.id);
    assert_eq!(stderr, case.stderr, "{} stderr", case.id);
    assert_eq!(stdout, case.stdout, "{} raw protocol bytes", case.id);
}

fn collect_files(
    root: &std::path::Path,
    directory: &std::path::Path,
    out: &mut BTreeMap<String, String>,
) {
    for entry in fs::read_dir(directory).unwrap() {
        let entry = entry.unwrap();
        let path = entry.path();
        if entry.file_type().unwrap().is_dir() {
            collect_files(root, &path, out);
        } else {
            out.insert(
                path.strip_prefix(root)
                    .unwrap()
                    .to_string_lossy()
                    .replace(std::path::MAIN_SEPARATOR, "/"),
                fs::read_to_string(path).unwrap(),
            );
        }
    }
}
