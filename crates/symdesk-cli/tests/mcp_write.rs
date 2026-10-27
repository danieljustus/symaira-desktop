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
    #[serde(default)]
    stdout_by_goos: BTreeMap<String, String>,
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
    #[serde(default)]
    hash_inputs: BTreeMap<String, String>,
    after_files: BTreeMap<String, String>,
    after_db: BTreeMap<String, Vec<BTreeMap<String, serde_json::Value>>>,
    sidecar_exists: bool,
    after_index: BTreeMap<String, BTreeMap<String, String>>,
}

#[test]
fn frozen_write_transcript_checksum_is_unchanged() {
    let bytes = fs::read(
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../testdata/port/mcp/write-process.json"),
    )
    .expect("frozen Unix MCP write transcript");
    assert_eq!(
        symdesk_vault::sha256_hex(&bytes),
        "6d0a8572b1082e6410260be3d7fb37c5fe9001597c55fe103c32714e62453294"
    );
}

#[test]
#[cfg_attr(
    windows,
    ignore = "Unix MCP write transcript has no native Windows syscall/path observations; frozen checksum remains checked"
)]
fn write_tools_match_go_stdio_calls_and_authoritative_files() {
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/mcp/write-process.json"),
        )
        .expect("Go process transcript fixture"),
    )
    .expect("decode Go process transcript");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 128);
    for case in fixture.cases {
        replay(&case);
    }
}

fn replay(case: &Case) {
    let root = isolated_root::create("symdesk-mcp-write");
    for dir in ["home", "config", "cache", "data", "tmp", "vault", "cwd"] {
        fs::create_dir_all(root.join(dir)).unwrap();
    }
    let root = root.canonicalize().unwrap();
    for (path, body) in &case.files {
        let path = root.join("vault").join(path);
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
            after_index.insert(
                relative,
                sidecar
                    .get_properties(&file.path)
                    .unwrap()
                    .into_iter()
                    .map(|(k, v)| (k, normalize(&v)))
                    .collect::<BTreeMap<_, _>>(),
            );
        }
    }
    assert_eq!(
        after_index, case.after_index,
        "{} derived index state",
        case.id
    );
    let mut after_db = BTreeMap::new();
    if case.sidecar_exists {
        let db = rusqlite::Connection::open(root.join("data/sidecar.db")).unwrap();
        for table in ["files", "file_properties", "links", "index_lifecycle"] {
            let mut stmt = db
                .prepare(&format!("SELECT * FROM {table} ORDER BY 1,2"))
                .unwrap();
            let columns = stmt
                .column_names()
                .into_iter()
                .map(str::to_owned)
                .collect::<Vec<_>>();
            let mut rows = stmt.query([]).unwrap();
            let mut entries = Vec::new();
            while let Some(row) = rows.next().unwrap() {
                let mut entry = BTreeMap::new();
                for (i, col) in columns.iter().enumerate() {
                    use rusqlite::types::ValueRef;
                    let value = match row.get_ref(i).unwrap() {
                        ValueRef::Null => serde_json::Value::Null,
                        ValueRef::Integer(n) => serde_json::json!(n),
                        ValueRef::Real(n) => serde_json::json!(n),
                        ValueRef::Text(bytes) | ValueRef::Blob(bytes) => {
                            serde_json::json!(normalize(
                                &String::from_utf8_lossy(bytes)
                                    .replace(root.to_str().unwrap(), "$ROOT")
                            ))
                        }
                    };
                    entry.insert(col.clone(), value);
                }
                if table == "files" {
                    let path = entry["path"]
                        .as_str()
                        .unwrap()
                        .replace("$ROOT", root.to_str().unwrap());
                    let relative = PathBuf::from(&path)
                        .strip_prefix(root.join("vault"))
                        .unwrap()
                        .to_string_lossy()
                        .replace(std::path::MAIN_SEPARATOR, "/");
                    let bytes = case
                        .hash_inputs
                        .get(&relative)
                        .map_or_else(|| fs::read(path).unwrap(), |text| text.as_bytes().to_vec());
                    assert_eq!(
                        entry["sha256"],
                        symdesk_vault::sha256_hex(&bytes),
                        "{} index SHA",
                        case.id
                    );
                    entry.insert(
                        "sha256".into(),
                        serde_json::json!(symdesk_vault::sha256_hex(
                            normalize(&String::from_utf8(bytes).unwrap()).as_bytes()
                        )),
                    );
                }
                if entry.get("mtime_ns").is_some_and(|v| !v.is_null()) {
                    entry.insert("mtime_ns".into(), serde_json::json!("<MTIME>"));
                }
                entries.push(entry);
            }
            after_db.insert(table.to_owned(), entries);
        }
    }
    assert_eq!(
        after_db, case.after_db,
        "{} complete mutation tables",
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
    fs::remove_dir_all(root).unwrap();
    assert_eq!(after, case.after_files, "{} authoritative files", case.id);
    assert_eq!(status.code(), Some(case.exit_code), "{}", case.id);
    assert_eq!(stderr, case.stderr, "{} stderr", case.id);
    let goos = if cfg!(target_os = "macos") {
        "darwin"
    } else {
        std::env::consts::OS
    };
    let expected = if case.stdout_by_goos.is_empty() {
        &case.stdout
    } else {
        case.stdout_by_goos.get(goos).expect(
            "native Go syscall transcript missing; run mcp-write-fixtures-generate on this OS",
        )
    };
    assert_eq!(
        &normalize(&stdout),
        expected,
        "{} raw protocol bytes",
        case.id
    );
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
                normalize(
                    &path
                        .strip_prefix(root)
                        .unwrap()
                        .to_string_lossy()
                        .replace(std::path::MAIN_SEPARATOR, "/"),
                ),
                match String::from_utf8(fs::read(path).unwrap()) {
                    Ok(text) => normalize(&text),
                    Err(error) => format!(
                        "<BINARY_HEX>{}",
                        error
                            .as_bytes()
                            .iter()
                            .map(|byte| format!("{byte:02x}"))
                            .collect::<String>()
                    ),
                },
            );
        }
    }
}

// Only generated UTC clock fields and the daily journal filename vary.
fn normalize(input: &str) -> String {
    let mut out = String::new();
    let mut rest = input;
    while !rest.is_empty() {
        let b = rest.as_bytes();
        if b.len() >= 20
            && b[..4].iter().all(u8::is_ascii_digit)
            && b[4] == b'-'
            && b[7] == b'-'
            && b[10] == b'T'
            && b[13] == b':'
            && b[16] == b':'
        {
            let mut n = 19;
            if b.get(n) == Some(&b'.') {
                n += 1;
                while b.get(n).is_some_and(u8::is_ascii_digit) {
                    n += 1;
                }
            }
            if b.get(n) == Some(&b'Z') {
                out.push_str("<TIME>");
                rest = &rest[n + 1..];
                continue;
            }
        }
        if rest.starts_with("pasted-")
            && b.len() >= 24
            && b[7..11].iter().all(u8::is_ascii_digit)
            && b[11] == b'-'
            && b[14] == b'-'
            && b[17] == b'-'
            && b[18..24].iter().all(u8::is_ascii_digit)
        {
            out.push_str("pasted-<CLOCK>");
            rest = &rest[24..];
            continue;
        }
        if rest.starts_with("journal/")
            && b.len() >= 25
            && b[8..12].iter().all(u8::is_ascii_digit)
            && &b[18..25] == b".ndjson"
        {
            out.push_str("journal/<DATE>.ndjson");
            rest = &rest[25..];
            continue;
        }
        let ch = rest.chars().next().unwrap();
        out.push(ch);
        rest = &rest[ch.len_utf8()..];
    }
    out
}
