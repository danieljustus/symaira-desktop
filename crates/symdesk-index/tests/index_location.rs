use std::{
    collections::BTreeMap,
    fs,
    io::Read,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use rusqlite::{Connection, params};
use serde::Deserialize;
use serde_json::{Value, json};
use symdesk_index::{index_location_for_vault, relocate_index_for_vault, symseek_config_path};

#[derive(Debug, Deserialize)]
struct LocationCase {
    id: String,
    environment: BTreeMap<String, String>,
    vault_root: String,
    config_toml: Option<String>,
    legacy_json: Option<String>,
    seed_files: Option<BTreeMap<String, String>>,
    expected_path: Option<String>,
    error_prefix: Option<String>,
    config_after: Option<Value>,
    migrated_json_config: Option<bool>,
}

#[derive(Debug, Deserialize)]
struct Relocation {
    input_rows: Vec<Row>,
    source_rows_after: Vec<Row>,
    relocated_rows: Vec<Row>,
    relocated_path: String,
    config_after: Value,
    source_preserved: bool,
    destination_replaced: bool,
    vault_relocation_error: String,
    config_unchanged_after_reject: bool,
    rejected_destination_absent: bool,
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
struct Row {
    id: i64,
    body: String,
}

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<LocationCase>,
    relocation: Relocation,
}

struct TestDir(PathBuf);
static NEXT_TEST_DIR: AtomicU64 = AtomicU64::new(0);

impl TestDir {
    fn new() -> Self {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock before Unix epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symdesk-index-location-{}-{nonce}-{}",
            std::process::id(),
            NEXT_TEST_DIR.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).expect("create isolated fixture directory");
        Self(path)
    }
}

impl Drop for TestDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join("testdata/port/retrieval/index-location.json");
    let bytes = fs::read(&path).expect("Go-generated retrieval location fixture");
    serde_json::from_slice(&bytes).expect("valid retrieval location fixture")
}

#[test]
fn location_and_config_paths_replay_go_fixture() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 11);
    for case in &fixture.cases {
        let root = TestDir::new();
        let (cwd, _home, temp_root, environment) = prepare_case(&root.0, case);
        let vault_root = expand_string(&case.vault_root, &root.0, &temp_root);
        if !vault_root.trim().is_empty() {
            fs::create_dir_all(&vault_root).expect("create vault root");
        }
        let result = index_location_for_vault(&vault_root, &environment, &cwd, &temp_root);
        if let Some(prefix) = &case.error_prefix {
            let error = result.expect_err("Go fixture expects a path error");
            assert!(
                error.to_string().starts_with(prefix),
                "case {} error {:?} did not start with {:?}",
                case.id,
                error.to_string(),
                prefix
            );
            continue;
        }
        let actual = result.unwrap_or_else(|error| panic!("case {}: {error}", case.id));
        let expected = resolve_expected_path(
            case.expected_path.as_deref().expect("expected path"),
            &root.0,
            &temp_root,
            &vault_root,
        );
        assert_eq!(actual, expected, "case {}", case.id);

        let config_path = symseek_config_path(&environment, &cwd);
        if let Some(expected_config) = &case.config_after
            && config_path.exists()
        {
            let contents = fs::read_to_string(&config_path).expect("read replayed TOML config");
            let mut actual_config = effective_config(&contents);
            normalize_config_path(&mut actual_config, &root.0);
            assert_eq!(actual_config, *expected_config, "config case {}", case.id);
        }
        if let Some(expected_migrated) = case.migrated_json_config {
            assert_eq!(
                config_path.exists(),
                expected_migrated,
                "migration case {}",
                case.id
            );
        }
    }
}

#[test]
fn relocation_persists_path_and_preserves_symseek_config_fields() {
    let fixture = fixture();
    let expected = &fixture.relocation;
    assert!(expected.source_preserved);
    assert!(expected.destination_replaced);
    assert!(expected.config_unchanged_after_reject);
    assert!(expected.rejected_destination_absent);

    let root = TestDir::new();
    let cwd = root.0.join("cwd");
    let home = root.0.join("home");
    let data_home = root.0.join("data");
    let temp_root = root.0.join("tmp");
    for directory in [&cwd, &home, &data_home, &temp_root] {
        fs::create_dir_all(directory).expect("create isolated environment directory");
    }
    let environment = BTreeMap::from([
        ("HOME".to_owned(), home.to_string_lossy().into_owned()),
        (
            "USERPROFILE".to_owned(),
            home.to_string_lossy().into_owned(),
        ),
        (
            "XDG_DATA_HOME".to_owned(),
            data_home.to_string_lossy().into_owned(),
        ),
        (
            "TMPDIR".to_owned(),
            temp_root.to_string_lossy().into_owned(),
        ),
    ]);
    let config_path = symseek_config_path(&environment, &cwd);
    fs::create_dir_all(config_path.parent().expect("config parent"))
        .expect("create config directory");
    fs::write(
        &config_path,
        "model = \"preserve-model\"\nembedding_dim = 512\nretry_count = 4\nvector_quantization = \"turbo-prod\"\nvector_quant_bits = 3\nvector_exact_rerank = false\nrerank_query = true\nrerank_model = \"rerank-model\"\nexpand_query = true\nexpand_model = \"expand-model\"\n",
    )
    .expect("seed symseek config");

    let source = index_location_for_vault("", &environment, &cwd, &temp_root)
        .expect("resolve standalone index");
    fs::create_dir_all(source.parent().expect("source parent")).expect("create source directory");
    let connection = Connection::open(&source).expect("create standalone index");
    connection
        .execute_batch("CREATE TABLE relocation_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL)")
        .expect("create test rows table");
    for row in &expected.input_rows {
        connection
            .execute(
                "INSERT INTO relocation_rows (id, body) VALUES (?1, ?2)",
                params![row.id, row.body],
            )
            .expect("insert source row");
    }
    drop(connection);

    let destination = cwd.join("relocated/retrieval.db");
    fs::create_dir_all(destination.parent().expect("destination parent"))
        .expect("create destination directory");
    fs::write(&destination, b"old destination").expect("seed destination conflict");
    let relocated = relocate_index_for_vault("", &destination, &environment, &cwd, &temp_root)
        .expect("relocate standalone index");
    assert_eq!(relocated, destination);
    assert_eq!(
        expected_path(&expected.relocated_path, &root.0, &temp_root),
        relocated
    );
    assert_eq!(read_rows(&source), expected.source_rows_after);
    assert_eq!(read_rows(&destination), expected.relocated_rows);

    let mut header = [0; 16];
    fs::File::open(&destination)
        .expect("open relocated database")
        .read_exact(&mut header)
        .expect("read sqlite header");
    assert_eq!(&header, b"SQLite format 3\0");

    let mut config_after =
        effective_config(&fs::read_to_string(&config_path).expect("read saved config"));
    normalize_config_path(&mut config_after, &root.0);
    assert_eq!(config_after, expected.config_after);
    let config_before_reject = config_after.clone();

    let forbidden = cwd.join("must-not-exist.db");
    let error = relocate_index_for_vault(" ", &forbidden, &environment, &cwd, &temp_root)
        .expect_err("vault-scoped relocation must be rejected");
    assert_eq!(error.to_string(), expected.vault_relocation_error);
    let mut config_after_reject = effective_config(
        &fs::read_to_string(&config_path).expect("read config after rejected relocation"),
    );
    normalize_config_path(&mut config_after_reject, &root.0);
    assert_eq!(config_after_reject, config_before_reject);
    assert!(!forbidden.exists());
}

fn prepare_case(
    root: &Path,
    case: &LocationCase,
) -> (PathBuf, PathBuf, PathBuf, BTreeMap<String, String>) {
    let cwd = root.join("cwd");
    let home = root.join("home");
    let temp_root = root.join("tmp");
    for directory in [&cwd, &home, &temp_root] {
        fs::create_dir_all(directory).expect("create fixture directory");
    }
    let environment = case
        .environment
        .iter()
        .map(|(key, value)| (key.clone(), expand_string(value, root, &temp_root)))
        .collect::<BTreeMap<_, _>>();
    let config_path = symseek_config_path(&environment, &cwd);
    if let Some(config) = &case.config_toml {
        fs::create_dir_all(config_path.parent().expect("config parent"))
            .expect("create config parent");
        fs::write(&config_path, config).expect("write config TOML");
    }
    if let Some(legacy) = &case.legacy_json {
        let legacy_path = config_path.with_file_name("config.json");
        fs::create_dir_all(legacy_path.parent().expect("legacy config parent"))
            .expect("create legacy config parent");
        fs::write(legacy_path, legacy).expect("write legacy JSON config");
    }
    if let Some(files) = &case.seed_files {
        for (path, contents) in files {
            let path = expand(path, root, &temp_root);
            fs::create_dir_all(path.parent().expect("seed parent")).expect("create seed parent");
            fs::write(path, contents).expect("write seeded file");
        }
    }
    (cwd, home, temp_root, environment)
}

fn read_rows(path: &Path) -> Vec<Row> {
    let connection = Connection::open(path).expect("open SQLite database");
    let mut statement = connection
        .prepare("SELECT id, body FROM relocation_rows ORDER BY id")
        .expect("prepare row query");
    statement
        .query_map([], |row| {
            Ok(Row {
                id: row.get(0)?,
                body: row.get(1)?,
            })
        })
        .expect("query rows")
        .collect::<Result<Vec<_>, _>>()
        .expect("read rows")
}

fn expand(value: &str, root: &Path, temp_root: &Path) -> PathBuf {
    PathBuf::from(expand_string(value, root, temp_root))
}

fn expand_string(value: &str, root: &Path, temp_root: &Path) -> String {
    value
        .replace("$ROOT", &root.to_string_lossy())
        .replace("$TMPDIR", &temp_root.to_string_lossy())
}

fn resolve_expected_path(value: &str, root: &Path, temp_root: &Path, vault_root: &str) -> PathBuf {
    let mut value = expand_string(value, root, temp_root);
    if value.contains("$VAULT_HASH") {
        let canonical = fs::canonicalize(vault_root).expect("canonical fixture vault");
        let digest = symdesk_vault::sha256_hex(canonical.to_string_lossy().as_bytes());
        value = value.replace("$VAULT_HASH", &digest[..16]);
    }
    PathBuf::from(value)
}

fn expected_path(value: &str, root: &Path, temp_root: &Path) -> PathBuf {
    PathBuf::from(expand_string(value, root, temp_root))
}

fn normalize_config_path(config: &mut Value, root: &Path) {
    if let Some(path) = config.get("index_path").and_then(Value::as_str) {
        let normalized = lexical_clean(Path::new(path))
            .to_string_lossy()
            .replace(&root.to_string_lossy().to_string(), "$ROOT");
        config["index_path"] = json!(normalized.replace('\\', "/"));
    }
}

fn effective_config(contents: &str) -> Value {
    let mut config = json!({
        "ollama_url": "http://localhost:11434/api/embeddings",
        "model": "qwen3-embedding:0.6b",
        "embedding_dim": 768,
        "timeout_seconds": 120,
        "retry_count": 2,
        "retry_backoff_ms": 500,
        "index_cooldown_seconds": 5,
        "vector_backend": "sqlite",
        "index_path": "",
        "vector_quantization": "off",
        "vector_quant_bits": 4,
        "vector_quantized_shortlist": 200,
        "vector_exact_rerank": true,
        "rerank_query": false,
        "rerank_model": "",
        "rerank_timeout_seconds": 120,
        "expand_query": false,
        "expand_model": "",
        "expand_timeout_seconds": 120
    });
    let parsed: Value =
        serde_json::to_value(toml::from_str::<toml::Value>(contents).expect("parse TOML config"))
            .expect("convert TOML config to JSON");
    config
        .as_object_mut()
        .expect("defaults are object")
        .extend(parsed.as_object().expect("TOML table is object").clone());
    config
}

fn lexical_clean(path: &Path) -> PathBuf {
    let mut cleaned = PathBuf::new();
    for component in path.components() {
        match component {
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                if !cleaned.pop() {
                    cleaned.push(component.as_os_str());
                }
            }
            other => cleaned.push(other.as_os_str()),
        }
    }
    cleaned
}
