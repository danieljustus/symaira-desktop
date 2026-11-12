#![deny(unsafe_code)]

// MCP-003: replay of `testdata/port/mcp/calls-process.json`, recorded from the
// real Go `symdesk mcp` process. Every case runs the native server once per
// step inside one disposable root and compares the raw stdout/stderr/exit code
// of every step plus the complete resulting state (files and SQLite rows).

#[path = "../../test-support/process_env.rs"]
mod process_env;

use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    time::Duration,
};

use regex::Regex;
use serde::Deserialize;
use serde_json::{Value, json};

#[path = "support/isolated_root.rs"]
mod isolated_root;
#[path = "support/mcp_run.rs"]
mod mcp_run;

type Rows = Vec<BTreeMap<String, Value>>;
type Dump = BTreeMap<String, Rows>;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Step {
    request: String,
    #[serde(default)]
    framed: bool,
    stdout: String,
    stderr: String,
    exit_code: i32,
}

#[derive(Deserialize)]
struct SeedSql {
    db: String,
    sql: Vec<String>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    #[serde(default)]
    allow_write: bool,
    #[serde(default)]
    no_vault: bool,
    #[serde(default)]
    indexed: bool,
    #[serde(default)]
    files: BTreeMap<String, String>,
    #[serde(default)]
    root_files: BTreeMap<String, String>,
    #[serde(default)]
    env: BTreeMap<String, String>,
    #[serde(default)]
    prepare: Vec<Vec<String>>,
    #[serde(default)]
    seed_sql: Vec<SeedSql>,
    steps: Vec<Step>,
    after_files: BTreeMap<String, String>,
    after_db: BTreeMap<String, Dump>,
}

fn normalize(text: &str, root: &str) -> String {
    let timestamp =
        Regex::new(r"\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?")
            .unwrap();
    let journal = Regex::new(r"journal/\d{4}-\d{2}-\d{2}\.ndjson").unwrap();
    let asset = Regex::new(r"pasted-\d{4}-\d{2}-\d{2}-\d{6}").unwrap();
    let text = text.replace(root, "$ROOT");
    let text = timestamp.replace_all(&text, "<TIME>");
    // Only the journal of the run's own (UTC) day is clock-dependent: seeded
    // journals of other days keep their names (mirrors `mcpCallsNormalize`).
    let today = format!("journal/{}.ndjson", time::OffsetDateTime::now_utc().date());
    let text = journal.replace_all(&text, |found: &regex::Captures<'_>| {
        if &found[0] == today.as_str() {
            "journal/<DATE>.ndjson".to_owned()
        } else {
            found[0].to_owned()
        }
    });
    asset.replace_all(&text, "pasted-<CLOCK>").into_owned()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn dump_db(path: &Path, root: &str) -> Dump {
    use rusqlite::types::ValueRef;
    let db = rusqlite::Connection::open(path).unwrap();
    let mut stmt = db
        .prepare(
            "SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
        )
        .unwrap();
    let tables: Vec<(String, String)> = stmt
        .query_map([], |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, Option<String>>(1)?.unwrap_or_default(),
            ))
        })
        .unwrap()
        .map(Result::unwrap)
        .collect();
    let virtuals: Vec<String> = tables
        .iter()
        .filter(|(_, sql)| sql.to_uppercase().starts_with("CREATE VIRTUAL TABLE"))
        .map(|(name, _)| format!("{name}_"))
        .collect();
    let mut out = Dump::new();
    for (name, sql) in &tables {
        if sql.to_uppercase().starts_with("CREATE VIRTUAL TABLE")
            || virtuals.iter().any(|prefix| name.starts_with(prefix))
        {
            continue;
        }
        let mut stmt = db
            .prepare(&format!("SELECT * FROM \"{name}\" ORDER BY 1,2"))
            .unwrap();
        let columns: Vec<String> = stmt.column_names().map(str::to_owned).collect();
        let mut rows = stmt.query([]).unwrap();
        let mut entries = Rows::new();
        while let Some(row) = rows.next().unwrap() {
            let mut entry = BTreeMap::new();
            let mut raw_path = String::new();
            for (index, column) in columns.iter().enumerate() {
                if column == "path"
                    && let ValueRef::Text(bytes) = row.get_ref(index).unwrap()
                {
                    raw_path = String::from_utf8_lossy(bytes).into_owned();
                }
                let mut value = match row.get_ref(index).unwrap() {
                    ValueRef::Null => Value::Null,
                    ValueRef::Integer(n) => json!(n),
                    ValueRef::Real(n) => {
                        if n.fract() == 0.0 && n.abs() < 1e15 {
                            json!(n as i64)
                        } else {
                            json!(n)
                        }
                    }
                    ValueRef::Text(bytes) | ValueRef::Blob(bytes) => {
                        match std::str::from_utf8(bytes) {
                            Ok(text) => json!(normalize(text, root)),
                            Err(_) => json!(format!("<BLOB>{}", hex(bytes))),
                        }
                    }
                };
                if column.contains("mtime") && !value.is_null() {
                    value = json!("<MTIME>");
                }
                entry.insert(column.clone(), value);
            }
            if name == "files" {
                normalize_indexed_file(&mut entry, &raw_path, root);
            }
            entries.push(entry);
        }
        out.insert(name.clone(), entries);
    }
    out
}

// Verifies a sidecar `files` row against the file it indexes, then replaces
// the columns derived from the raw bytes (hash, size, simhash) by values of the
// normalized content: generated notes embed clock readings and the disposable
// root path, which differ between runs and hosts. A row whose hash no longer
// matches the file is recorded as "<STALE>".
fn normalize_indexed_file(entry: &mut BTreeMap<String, Value>, raw_path: &str, root: &str) {
    let Some(raw_sha) = entry
        .get("sha256")
        .and_then(Value::as_str)
        .map(str::to_owned)
    else {
        return;
    };
    if raw_path.is_empty() {
        return;
    }
    let body = fs::read(raw_path).unwrap_or_else(|error| panic!("{raw_path}: {error}"));
    if symdesk_vault::sha256_hex(&body) != raw_sha {
        for column in ["sha256", "size", "simhash"] {
            entry.insert(column.into(), json!("<STALE>"));
        }
        return;
    }
    let text = String::from_utf8_lossy(&body).into_owned();
    let normalized = normalize(&text, root);
    entry.insert(
        "sha256".into(),
        json!(symdesk_vault::sha256_hex(normalized.as_bytes())),
    );
    entry.insert("size".into(), json!(normalized.len()));
    if normalized != text {
        entry.insert("simhash".into(), json!("<SIMHASH>"));
    }
}

fn snapshot(root: &Path) -> (BTreeMap<String, String>, BTreeMap<String, Dump>) {
    let root_text = root.to_str().unwrap().to_owned();
    let mut files = BTreeMap::new();
    let mut dbs = BTreeMap::new();
    let mut stack = vec![root.to_path_buf()];
    while let Some(directory) = stack.pop() {
        for entry in fs::read_dir(&directory).unwrap() {
            let entry = entry.unwrap();
            let path = entry.path();
            let relative = path
                .strip_prefix(root)
                .unwrap()
                .to_string_lossy()
                .replace(std::path::MAIN_SEPARATOR, "/");
            if entry.file_type().unwrap().is_dir() {
                if matches!(relative.as_str(), "cwd" | "tmp" | "cache") {
                    continue;
                }
                stack.push(path);
                continue;
            }
            if relative.ends_with("-wal")
                || relative.ends_with("-shm")
                || relative.ends_with("-journal")
                || matches!(relative.as_str(), "stdout" | "stderr")
            {
                continue;
            }
            if relative.ends_with(".db") {
                dbs.insert(relative, dump_db(&path, &root_text));
                continue;
            }
            let data = fs::read(&path).unwrap();
            let value = match String::from_utf8(data) {
                Ok(text) => normalize(&text, &root_text),
                Err(error) => format!("<BINARY_HEX>{}", hex(error.as_bytes())),
            };
            files.insert(normalize(&relative, &root_text), value);
        }
    }
    (files, dbs)
}

fn index_vault(root: &Path, files: &BTreeMap<String, String>) {
    let mut sidecar = symdesk_index::Sidecar::open(&root.join("data/sidecar.db")).unwrap();
    for (path, body) in files {
        let path = root.join("vault").join(path);
        let document =
            symdesk_vault::parse_bytes(&path.to_string_lossy(), body.as_bytes()).unwrap();
        let indexed = symdesk_index::IndexedDocument::from_vault(&document, None).unwrap();
        sidecar.index_document(&indexed).unwrap();
    }
}

fn seed_files(base: &Path, files: &BTreeMap<String, String>) {
    for (path, body) in files {
        let path = base.join(path);
        fs::create_dir_all(path.parent().unwrap()).unwrap();
        fs::write(path, body).unwrap();
    }
}

fn spec<'a>(
    root: &'a Path,
    args: Vec<std::ffi::OsString>,
    input: Vec<u8>,
    case: &Case,
) -> mcp_run::Spec<'a> {
    let root_text = root.to_str().unwrap();
    let mut extra_env: Vec<(std::ffi::OsString, std::ffi::OsString)> = vec![(
        "SYMDESK_SIDECAR".into(),
        root.join("data/sidecar.db").into_os_string(),
    )];
    for (key, value) in &case.env {
        extra_env.push((key.into(), value.replace("$ROOT", root_text).into()));
    }
    mcp_run::Spec {
        root,
        args,
        input,
        path_env: "".into(),
        extra_env,
        deadline: Duration::from_secs(90),
    }
}

fn replay(case: &Case, failures: &mut Vec<String>) {
    let root = isolated_root::create("symdesk-mcp-calls");
    mcp_run::make_dirs(&root);
    let root = root.canonicalize().unwrap();
    let root_text = root.to_str().unwrap().to_owned();
    let vault = root.join("vault");
    seed_files(&vault, &case.files);
    seed_files(&root, &case.root_files);
    if case.indexed {
        index_vault(&root, &case.files);
    }
    for prepare in &case.prepare {
        let mut args: Vec<std::ffi::OsString> =
            vec!["--vault".into(), vault.clone().into_os_string()];
        args.extend(
            prepare
                .iter()
                .map(|arg| arg.replace("$ROOT", &root_text).into()),
        );
        mcp_run::run(&spec(&root, args, Vec::new(), case), &case.id);
    }
    for seed in &case.seed_sql {
        let db = rusqlite::Connection::open(root.join(&seed.db)).unwrap();
        for statement in &seed.sql {
            db.execute_batch(statement)
                .unwrap_or_else(|error| panic!("{}: seed {statement}: {error}", case.id));
        }
    }
    for (index, step) in case.steps.iter().enumerate() {
        let mut args: Vec<std::ffi::OsString> = if case.no_vault {
            vec!["mcp".into()]
        } else {
            vec![
                "--vault".into(),
                vault.clone().into_os_string(),
                "mcp".into(),
            ]
        };
        if case.allow_write {
            args.push("--allow-write".into());
        }
        let request = step.request.replace("$ROOT", &root_text);
        let input = if step.framed {
            format!("Content-Length: {}\r\n\r\n{request}", request.len())
        } else {
            format!("{request}\n")
        };
        let outcome = mcp_run::run(
            &spec(&root, args, input.into_bytes(), case),
            &format!("{}#{index}", case.id),
        );
        let stdout = normalize(&String::from_utf8_lossy(&outcome.stdout), &root_text);
        let stderr = normalize(&String::from_utf8_lossy(&outcome.stderr), &root_text);
        if outcome.code != Some(step.exit_code) {
            failures.push(format!(
                "{}#{index}: exit {:?}, want {}",
                case.id, outcome.code, step.exit_code
            ));
        }
        if stderr != step.stderr {
            failures.push(format!(
                "{}#{index}: stderr {stderr:?}, want {:?}",
                case.id, step.stderr
            ));
        }
        if stdout != step.stdout {
            failures.push(format!(
                "{}#{index}: stdout {:?}\n    want {:?}",
                case.id,
                &stdout[..stdout.len().min(600)],
                &step.stdout[..step.stdout.len().min(600)]
            ));
        }
    }
    let (files, dbs) = snapshot(&root);
    if files != case.after_files {
        failures.push(format!(
            "{}: files {:?}, want {:?}",
            case.id,
            files.keys().collect::<Vec<_>>(),
            case.after_files.keys().collect::<Vec<_>>()
        ));
        for (key, value) in &files {
            if case.after_files.get(key) != Some(value) {
                failures.push(format!(
                    "{}: file {key}: {value:?} vs {:?}",
                    case.id,
                    case.after_files.get(key)
                ));
            }
        }
    }
    if dbs != case.after_db {
        let detail = dbs
            .iter()
            .filter(|(key, dump)| case.after_db.get(*key) != Some(dump))
            .map(|(key, dump)| {
                let want = case.after_db.get(key);
                let tables: Vec<String> = dump
                    .iter()
                    .filter(|(table, rows)| want.and_then(|w| w.get(*table)) != Some(*rows))
                    .map(|(table, rows)| {
                        format!("{table}: {rows:?} vs {:?}", want.and_then(|w| w.get(table)))
                    })
                    .collect();
                format!("{key}: {}", tables.join("; "))
            })
            .collect::<Vec<_>>();
        failures.push(format!(
            "{}: databases differ (have {:?}, want {:?}): {}",
            case.id,
            dbs.keys().collect::<Vec<_>>(),
            case.after_db.keys().collect::<Vec<_>>(),
            detail.join(" | ")
        ));
    }
    fs::remove_dir_all(&root).unwrap();
}

#[test]
fn recorded_tool_calls_match_go_stdio_and_resulting_state() {
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/mcp/calls-process.json"),
        )
        .expect("Go process transcript fixture"),
    )
    .expect("decode Go process transcript");
    assert_eq!(fixture.schema_version, 1);
    let filter = std::env::var("MCP_CALLS_FILTER").unwrap_or_default();
    let mut failures = Vec::new();
    let mut executed = 0;
    let mut steps = 0;
    for case in fixture
        .cases
        .iter()
        .filter(|case| case.id.contains(&filter))
    {
        replay(case, &mut failures);
        executed += 1;
        steps += case.steps.len();
    }
    assert!(executed > 0, "no case matches the filter");
    if filter.is_empty() {
        assert!(executed >= 640, "fixture shrank to {executed} cases");
    }
    println!("mcp_calls: executed {executed} cases / {steps} steps (filter {filter:?})");
    assert!(
        failures.is_empty(),
        "{} problem(s) over {executed} cases / {steps} steps:\n{}",
        failures.len(),
        failures.join("\n")
    );
}

// Negative controls: the replay must reject a wrong stdout, a wrong exit code,
// a missing database row and an extra file.
#[test]
fn mutated_expectations_are_detected() {
    let fixture: Fixture = serde_json::from_slice(
        &fs::read(
            PathBuf::from(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/mcp/calls-process.json"),
        )
        .unwrap(),
    )
    .unwrap();
    let mut case = fixture
        .cases
        .into_iter()
        .find(|case| case.id == "rules-add-list")
        .expect("rules-add-list case");
    let mut failures = Vec::new();
    replay(&case, &mut failures);
    assert!(failures.is_empty(), "{failures:?}");

    case.steps[1].stdout = case.steps[1].stdout.replace("invoice", "invoicf");
    case.steps[2].exit_code = 3;
    case.after_files
        .insert("extra.md".to_owned(), "x".to_owned());
    case.after_db
        .get_mut("data/symdesk/symingest.db")
        .unwrap()
        .get_mut("classification_rules")
        .unwrap()
        .pop();
    let mut failures = Vec::new();
    replay(&case, &mut failures);
    let text = failures.join("\n");
    assert!(text.contains("rules-add-list#1: stdout"), "{text}");
    assert!(text.contains("rules-add-list#2: exit"), "{text}");
    assert!(text.contains("files"), "{text}");
    assert!(text.contains("databases differ"), "{text}");
}
