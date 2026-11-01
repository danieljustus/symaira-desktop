#![deny(unsafe_code)]

//! Replays the Go-recorded PostgreSQL store fixture
//! `testdata/port/storage/postgres.json` (`scripts/rust-port/cmd/pggen`)
//! against a real, disposable PostgreSQL server.
//!
//! The live replay needs `SYMDESK_PG_ADMIN_DSN` (a superuser DSN) and is
//! `#[ignore]`d so `cargo nextest run --workspace` stays server-less; it runs
//! through `make postgres-integration`, which provides the server on both gate
//! hosts. An absent variable fails the ignored test instead of skipping it, so
//! a zero-case run can never pass. The offline tests below pin the fixture's
//! shape and the SQLite-parity facts it records.

use std::collections::{BTreeSet, HashMap};
use std::time::Duration;

use base64::Engine as _;
use base64::engine::general_purpose::STANDARD as B64;
use serde_json::{Map, Value, json};
use sha2::{Digest, Sha256};
use symdesk_storage::{PostgresBackend, StorageError};
use tokio::task::JoinHandle;
use tokio_postgres::{Client, NoTls, SimpleQueryMessage};

const FIXTURE: &str = include_str!("../../../testdata/port/storage/postgres.json");
const ADMIN_DSN_ENV: &str = "SYMDESK_PG_ADMIN_DSN";
const DB_PREFIX: &str = "symdesk_pg_rs_";
const INLINE_VALUE_MAX: usize = 4096;

fn fixture() -> Value {
    serde_json::from_str(FIXTURE).expect("fixture is valid JSON")
}

fn str_of<'a>(value: &'a Value, key: &str) -> Option<&'a str> {
    value.get(key).and_then(Value::as_str)
}

// ---------------------------------------------------------------------------
// Offline checks

#[test]
fn fixture_is_well_formed_and_covers_every_error_class() {
    let doc = fixture();
    assert_eq!(doc["schema_version"], 1);
    assert_eq!(doc["server_major"], 17);
    let scenarios = doc["scenarios"].as_array().expect("scenarios");
    assert!(scenarios.len() >= 20, "{} scenarios", scenarios.len());
    let mut ids = BTreeSet::new();
    let mut classes = BTreeSet::new();
    let mut ops = BTreeSet::new();
    let mut sqlstates = BTreeSet::new();
    for scenario in scenarios {
        let id = str_of(scenario, "id").expect("id");
        assert!(ids.insert(id.to_string()), "duplicate scenario {id}");
        for step in scenario["steps"].as_array().expect("steps") {
            ops.insert(str_of(step, "op").expect("op").to_string());
            let result = step.get("result").expect("every step records a result");
            if let Some(err) = result.get("err") {
                classes.insert(str_of(err, "class").expect("class").to_string());
                if let Some(state) = str_of(err, "sqlstate") {
                    sqlstates.insert(state.to_string());
                }
            }
        }
    }
    for class in [
        "not_found",
        "invalid_key",
        "empty_dsn",
        "dsn",
        "connect",
        "server",
        "timeout",
        "closed",
    ] {
        assert!(classes.contains(class), "no recorded {class} error");
    }
    for state in [
        "28P01", "3D000", "42501", "42703", "42P10", "22021", "23502", "54000",
    ] {
        assert!(sqlstates.contains(state), "no recorded SQLSTATE {state}");
    }
    for op in [
        "open",
        "open_many",
        "close",
        "get",
        "set",
        "remove",
        "query",
        "query_digest",
        "bulk_set",
        "get_one_of",
        "sql",
        "spawn",
        "join",
        "expect_pending",
        "sleep",
    ] {
        assert!(ops.contains(op), "no recorded {op} step");
    }
}

/// PostgreSQL and SQLite store the same data the same way; the only recorded
/// difference is that PostgreSQL rejects NUL characters in keys and prefixes.
#[test]
fn recorded_sqlite_parity_differs_only_on_nul_keys() {
    let doc = fixture();
    let mut parity_scenarios = 0;
    for scenario in doc["scenarios"].as_array().expect("scenarios") {
        let Some(diffs) = scenario.get("sqlite_diffs") else {
            continue;
        };
        parity_scenarios += 1;
        let diffs: Vec<usize> = diffs
            .as_array()
            .expect("diff list")
            .iter()
            .map(|v| v.as_u64().expect("index") as usize)
            .collect();
        let id = str_of(scenario, "id").unwrap();
        if id == "invalid_keys" {
            assert_eq!(diffs, vec![5, 6, 7, 8], "NUL-key steps of {id}");
            let steps = scenario["steps"].as_array().unwrap();
            for index in &diffs {
                let step = &steps[*index];
                let text = format!(
                    "{}{}",
                    str_of(step, "key").unwrap_or(""),
                    str_of(step, "prefix").unwrap_or("")
                );
                assert!(text.contains('\0'), "step {index} has no NUL");
                assert_eq!(str_of(&step["result"]["err"], "sqlstate"), Some("22021"));
            }
        } else {
            assert!(diffs.is_empty(), "{id} differs from SQLite at {diffs:?}");
        }
    }
    assert_eq!(parity_scenarios, 6);
}

// ---------------------------------------------------------------------------
// Live replay

struct Admin {
    user: String,
    password: String,
    host: String,
    port: String,
}

impl Admin {
    fn parse(dsn: &str) -> Self {
        let rest = dsn
            .strip_prefix("postgres://")
            .expect("postgres:// admin DSN");
        let (userinfo, tail) = rest.split_once('@').expect("userinfo");
        let (user, password) = userinfo.split_once(':').expect("password");
        let authority = tail.split(['/', '?']).next().expect("authority");
        let (host, port) = authority.rsplit_once(':').expect("port");
        Self {
            user: user.to_string(),
            password: password.to_string(),
            host: host.to_string(),
            port: port.to_string(),
        }
    }

    fn role_password(&self, role: &str) -> String {
        let digest = Sha256::digest(format!("{}\0{role}", self.password));
        format!("pw-{}", hex::encode(&digest[..8]))
    }

    async fn connect(&self, database: &str) -> Client {
        let config = format!(
            "host={} port={} user={} password={} dbname={database} sslmode=disable",
            self.host, self.port, self.user, self.password
        );
        let (client, connection) = tokio_postgres::connect(&config, NoTls)
            .await
            .expect("admin connection");
        tokio::spawn(async move {
            let _ = connection.await;
        });
        client
    }
}

fn query_escape(text: &str) -> String {
    let mut out = String::new();
    for byte in text.bytes() {
        match byte {
            b'a'..=b'z' | b'A'..=b'Z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(byte as char)
            }
            b' ' => out.push('+'),
            other => out.push_str(&format!("%{other:02X}")),
        }
    }
    out
}

struct Scenario {
    admin: std::sync::Arc<Admin>,
    db: String,
    backends: HashMap<String, PostgresBackend>,
    sessions: HashMap<String, Client>,
    tasks: HashMap<String, JoinHandle<Value>>,
}

impl Scenario {
    fn subst(&self, text: &str) -> String {
        let mut out = text.replace("{db}", &self.db);
        while let Some(start) = out.find("{pw:") {
            let end = start + out[start..].find('}').expect("closing brace");
            let role = out[start + 4..end].to_string();
            out.replace_range(start..=end, &self.admin.role_password(&role));
        }
        out
    }

    fn normalize(&self, text: &str) -> String {
        text.replace(&self.db, "{db}")
    }

    fn build_dsn(&self, spec: Option<&Value>) -> String {
        let empty = Value::Null;
        let spec = spec.unwrap_or(&empty);
        if let Some(raw) = str_of(spec, "raw") {
            return raw.to_string();
        }
        let user = str_of(spec, "user").unwrap_or("postgres");
        let password = match str_of(spec, "password") {
            Some("role") => self.admin.role_password(user),
            Some("wrong") => format!("wrong-{}", self.admin.password),
            _ => self.admin.password.clone(),
        };
        let port = if str_of(spec, "host") == Some("closed") {
            "1"
        } else {
            self.admin.port.as_str()
        };
        let database = match str_of(spec, "database") {
            None | Some("scenario") => self.db.clone(),
            Some("missing") => "symdesk_pg_missing".to_string(),
            Some(other) => other.to_string(),
        };
        let mut params: std::collections::BTreeMap<String, String> =
            [("sslmode".to_string(), "disable".to_string())].into();
        if let Some(extra) = spec.get("params").and_then(Value::as_object) {
            for (key, value) in extra {
                params.insert(key.clone(), value.as_str().unwrap().to_string());
            }
        }
        if str_of(spec, "form") == Some("keyword") {
            let mut parts = vec![
                format!("host={}", self.admin.host),
                format!("port={port}"),
                format!("user={user}"),
                format!("password={password}"),
                format!("dbname={database}"),
            ];
            parts.extend(params.iter().map(|(k, v)| format!("{k}={v}")));
            return parts.join(" ");
        }
        let query: Vec<String> = params
            .iter()
            .map(|(k, v)| format!("{k}={}", query_escape(v).replace('+', "%20")))
            .collect();
        format!(
            "postgres://{}:{}@{}:{port}/{database}?{}",
            query_escape(user),
            query_escape(&password),
            self.admin.host,
            query.join("&")
        )
    }

    fn err_value(&self, error: &StorageError) -> Value {
        err_value(&self.db, error)
    }
}

fn err_value(db: &str, error: &StorageError) -> Value {
    let normalize = |text: &str| text.replace(db, "{db}");
    match error {
        StorageError::NotFound(_) => {
            json!({"class": "not_found", "message": normalize(&error.to_string())})
        }
        StorageError::InvalidKey => json!({"class": "invalid_key", "message": error.to_string()}),
        StorageError::EmptyDsn => json!({"class": "empty_dsn", "message": error.to_string()}),
        StorageError::Timeout => json!({"class": "timeout"}),
        StorageError::Closed => json!({"class": "closed"}),
        StorageError::Dsn(_) => json!({"class": "dsn", "prefix": "parse PostgreSQL DSN: "}),
        StorageError::Connect {
            sqlstate,
            server_message,
            ..
        } => {
            let mut map = Map::new();
            map.insert("class".into(), "connect".into());
            if let Some(state) = sqlstate {
                map.insert("sqlstate".into(), state.clone().into());
            }
            map.insert("prefix".into(), "connect to PostgreSQL: ".into());
            if let Some(message) = server_message {
                map.insert("message".into(), normalize(message).into());
            }
            Value::Object(map)
        }
        StorageError::Server { sqlstate, .. } => json!({
            "class": "server",
            "sqlstate": sqlstate,
            "message": normalize(&error.to_string()),
        }),
        StorageError::Client { .. } => json!({"class": "client", "message": error.to_string()}),
    }
}

fn value_info(bytes: &[u8]) -> Value {
    if bytes.len() <= INLINE_VALUE_MAX {
        let mut map = Map::new();
        map.insert("len".into(), bytes.len().into());
        if !bytes.is_empty() {
            map.insert("b64".into(), B64.encode(bytes).into());
        }
        Value::Object(map)
    } else {
        json!({"len": bytes.len(), "sha256": hex::encode(Sha256::digest(bytes))})
    }
}

fn value_bytes(spec: Option<&Value>) -> Vec<u8> {
    let Some(spec) = spec else {
        return Vec::new();
    };
    if let Some(fill) = spec.get("fill") {
        let byte = fill.as_u64().unwrap() as u8;
        let len = spec["len"].as_u64().unwrap() as usize;
        return vec![byte; len];
    }
    B64.decode(str_of(spec, "b64").unwrap_or("")).unwrap()
}

/// One storage operation; shared by direct steps and spawned tasks.
async fn store_op(
    backend: PostgresBackend,
    step: Value,
    classify: impl Fn(&StorageError) -> Value,
) -> Value {
    let key = str_of(&step, "key").unwrap_or("").to_string();
    let prefix = str_of(&step, "prefix").unwrap_or("").to_string();
    let deadline = step
        .get("timeout_ms")
        .and_then(Value::as_u64)
        .map(Duration::from_millis);
    let op = str_of(&step, "op").unwrap().to_string();
    macro_rules! call {
        ($method:ident $(, $arg:expr)*) => {
            match deadline {
                Some(limit) => backend.bounded(limit).$method($($arg),*).await,
                None => backend.$method($($arg),*).await,
            }
        };
    }
    match op.as_str() {
        "get" => match call!(get, &key) {
            Ok(value) => json!({"value": value_info(&value)}),
            Err(error) => json!({"err": classify(&error)}),
        },
        "set" => match call!(set, &key, &value_bytes(step.get("value"))) {
            Ok(()) => json!({}),
            Err(error) => json!({"err": classify(&error)}),
        },
        "remove" => match call!(remove, &key) {
            Ok(()) => json!({}),
            Err(error) => json!({"err": classify(&error)}),
        },
        "query" => match call!(query, &prefix) {
            Ok(entries) => {
                let list: Vec<Value> = entries
                    .iter()
                    .map(|e| json!({"key": e.key, "value": value_info(&e.value)}))
                    .collect();
                json!({"entries": list})
            }
            Err(error) => json!({"err": classify(&error)}),
        },
        "query_digest" => match call!(query, &prefix) {
            Ok(entries) => {
                let mut hasher = Sha256::new();
                for entry in &entries {
                    hasher.update(format!("{}\0{}\n", entry.key, hex::encode(&entry.value)));
                }
                json!({"summary": {"count": entries.len(), "sha256": hex::encode(hasher.finalize())}})
            }
            Err(error) => json!({"err": classify(&error)}),
        },
        "bulk_set" => {
            let count = step["count"].as_u64().unwrap();
            for i in 0..count {
                let key = format!("{prefix}{i:05}");
                let digest = Sha256::digest(key.as_bytes());
                if let Err(error) = backend.set(&key, &digest[..8]).await {
                    return json!({"err": classify(&error)});
                }
            }
            json!({})
        }
        "get_one_of" => match call!(get, &key) {
            Ok(value) => {
                let encoded = B64.encode(&value);
                let matched = step["values_b64"]
                    .as_array()
                    .unwrap()
                    .iter()
                    .any(|candidate| candidate.as_str() == Some(encoded.as_str()));
                json!({"matched": matched})
            }
            Err(error) => json!({"err": classify(&error)}),
        },
        other => panic!("unknown store op {other}"),
    }
}

impl Scenario {
    async fn exec(&mut self, step: &Value) -> Value {
        let op = str_of(step, "op").unwrap();
        let name = |field: &str| {
            str_of(step, field)
                .filter(|s| !s.is_empty())
                .unwrap_or("b")
                .to_string()
        };
        match op {
            "open" => {
                let dsn = self.build_dsn(step.get("dsn"));
                let opened =
                    tokio::time::timeout(Duration::from_secs(20), PostgresBackend::open(&dsn))
                        .await
                        .expect("open finishes");
                match opened {
                    Ok(backend) => {
                        self.backends.insert(name("as"), backend);
                        json!({})
                    }
                    Err(error) => json!({"err": self.err_value(&error)}),
                }
            }
            "open_many" => self.open_many(step).await,
            "close" => {
                if let Some(backend) = self.backends.get(&name("on")) {
                    backend.close();
                }
                json!({})
            }
            "get" | "set" | "remove" | "query" | "get_one_of" | "query_digest" | "bulk_set" => {
                let backend = self
                    .backends
                    .get(&name("on"))
                    .unwrap_or_else(|| panic!("backend not open for {op}"))
                    .clone();
                let classifier = classifier(self.db.clone());
                store_op(backend, step.clone(), classifier).await
            }
            "sql" => self.sql(step).await,
            "spawn" => {
                let id = str_of(step, "id").unwrap().to_string();
                let task = step["task"].clone();
                let backend = self
                    .backends
                    .get(&name_of(&task))
                    .expect("backend for spawned task")
                    .clone();
                let classifier = classifier(self.db.clone());
                self.tasks
                    .insert(id, tokio::spawn(store_op(backend, task, classifier)));
                json!({})
            }
            "join" => {
                let id = str_of(step, "id").unwrap();
                self.tasks
                    .remove(id)
                    .expect("task")
                    .await
                    .expect("task completes")
            }
            "expect_pending" => {
                let id = str_of(step, "id").unwrap();
                json!({"pending": !self.tasks[id].is_finished()})
            }
            "sleep" => {
                tokio::time::sleep(Duration::from_millis(step["ms"].as_u64().unwrap())).await;
                json!({})
            }
            other => panic!("unknown op {other}"),
        }
    }

    async fn open_many(&mut self, step: &Value) -> Value {
        let dsn = self.build_dsn(step.get("dsn"));
        let count = step["count"].as_u64().unwrap();
        let mut handles = Vec::new();
        for _ in 0..count {
            let dsn = dsn.clone();
            handles.push(tokio::spawn(async move {
                tokio::time::timeout(Duration::from_secs(30), PostgresBackend::open(&dsn))
                    .await
                    .expect("open finishes")
            }));
        }
        let (mut opened, mut unexpected) = (0, 0);
        for handle in handles {
            match handle.await.unwrap() {
                Ok(backend) => {
                    opened += 1;
                    backend.close();
                }
                Err(StorageError::Server { sqlstate, .. })
                    if ["23505", "42P07", "42710"].contains(&sqlstate.as_str()) => {}
                Err(_) => unexpected += 1,
            }
        }
        json!({"summary": {"at_least_one_opened": opened >= 1, "unexpected_failures": unexpected}})
    }

    async fn sql(&mut self, step: &Value) -> Value {
        let session = str_of(step, "session")
            .filter(|s| !s.is_empty())
            .unwrap_or("s")
            .to_string();
        if !self.sessions.contains_key(&session) {
            let database = if str_of(step, "db") == Some("admin") {
                "postgres".to_string()
            } else {
                self.db.clone()
            };
            let client = self.admin.connect(&database).await;
            self.sessions.insert(session.clone(), client);
        }
        let text = self.subst(str_of(step, "sql").unwrap());
        match self.sessions[&session].simple_query(&text).await {
            Ok(messages) => {
                let rows: Vec<Value> = messages
                    .iter()
                    .filter_map(|message| match message {
                        SimpleQueryMessage::Row(row) => Some(Value::Array(
                            (0..row.len())
                                .map(|i| {
                                    row.get(i).map_or(Value::Null, |cell| {
                                        Value::String(self.normalize(cell))
                                    })
                                })
                                .collect(),
                        )),
                        _ => None,
                    })
                    .collect();
                json!({"rows": rows})
            }
            Err(error) => match error.as_db_error() {
                Some(db) => json!({"err": {
                    "class": "server",
                    "sqlstate": db.code().code(),
                    "message": self.normalize(&format!("{}: {} (SQLSTATE {})", db.severity(), db.message(), db.code().code())),
                }}),
                None => panic!("session error without SQLSTATE: {error}"),
            },
        }
    }

    async fn final_state(&self) -> Value {
        let client = self.admin.connect(&self.db).await;
        let exists = client
            .simple_query("SELECT to_regclass('public.storage_entries') IS NOT NULL")
            .await
            .unwrap();
        let present = exists
            .iter()
            .any(|m| matches!(m, SimpleQueryMessage::Row(r) if r.get(0) == Some("t")));
        if !present {
            return Value::Null;
        }
        match client
            .simple_query("SELECT storage_key, encode(value::bytea, 'hex') FROM storage_entries ORDER BY storage_key COLLATE \"C\"")
            .await
        {
            Ok(messages) => Value::Array(
                messages
                    .iter()
                    .filter_map(|m| match m {
                        SimpleQueryMessage::Row(r) => {
                            Some(Value::String(format!("{}={}", r.get(0).unwrap(), r.get(1).unwrap())))
                        }
                        _ => None,
                    })
                    .collect(),
            ),
            Err(error) => match error.as_db_error() {
                Some(db) => json!([format!("unreadable:{}", db.code().code())]),
                None => panic!("final state: {error}"),
            },
        }
    }
}

fn name_of(task: &Value) -> String {
    str_of(task, "on")
        .filter(|s| !s.is_empty())
        .unwrap_or("b")
        .to_string()
}

fn classifier(db: String) -> impl Fn(&StorageError) -> Value + Send + 'static {
    move |error| err_value(&db, error)
}

async fn run_scenario(admin: &std::sync::Arc<Admin>, spec: &Value) -> Result<(), String> {
    let id = str_of(spec, "id").unwrap();
    let db = format!("{DB_PREFIX}{id}");
    let roles: Vec<String> = spec
        .get("roles")
        .and_then(Value::as_array)
        .map(|list| {
            list.iter()
                .map(|r| r.as_str().unwrap().to_string())
                .collect()
        })
        .unwrap_or_default();

    let control = admin.connect("postgres").await;
    for role in &roles {
        control
            .batch_execute(&format!(
                "DO $$BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = '{role}') THEN CREATE ROLE {role} LOGIN; END IF; END$$"
            ))
            .await
            .map_err(|e| e.to_string())?;
        control
            .batch_execute(&format!(
                "ALTER ROLE {role} LOGIN PASSWORD '{}'",
                admin.role_password(role)
            ))
            .await
            .map_err(|e| e.to_string())?;
    }
    control
        .batch_execute(&format!("DROP DATABASE IF EXISTS \"{db}\" WITH (FORCE)"))
        .await
        .map_err(|e| e.to_string())?;
    control
        .batch_execute(&format!("CREATE DATABASE \"{db}\""))
        .await
        .map_err(|e| e.to_string())?;

    let mut scenario = Scenario {
        admin: admin.clone(),
        db: db.clone(),
        backends: HashMap::new(),
        sessions: HashMap::new(),
        tasks: HashMap::new(),
    };
    let mut outcome = Ok(());
    for (index, step) in spec["steps"].as_array().unwrap().iter().enumerate() {
        let actual = scenario.exec(step).await;
        if actual != step["result"] {
            outcome = Err(format!(
                "scenario {id} step {index} ({}):\n  go:   {}\n  rust: {actual}",
                str_of(step, "op").unwrap(),
                step["result"]
            ));
            break;
        }
    }
    for (_, task) in scenario.tasks.drain() {
        let _ = task.await;
    }
    for backend in scenario.backends.values() {
        backend.close();
    }
    if outcome.is_ok() {
        let final_state = scenario.final_state().await;
        if final_state != spec["final"] {
            outcome = Err(format!(
                "scenario {id} final table state:\n  go:   {}\n  rust: {final_state}",
                spec["final"]
            ));
        }
    }
    scenario.sessions.clear();
    drop(scenario);
    let _ = control
        .batch_execute(&format!("DROP DATABASE IF EXISTS \"{db}\" WITH (FORCE)"))
        .await;
    for role in &roles {
        let _ = control
            .batch_execute(&format!("DROP ROLE IF EXISTS {role}"))
            .await;
    }
    outcome
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
#[ignore = "needs SYMDESK_PG_ADMIN_DSN; run through make postgres-integration"]
async fn replays_go_recorded_postgres_fixture() {
    let dsn = std::env::var(ADMIN_DSN_ENV)
        .unwrap_or_else(|_| panic!("{ADMIN_DSN_ENV} is not set: use make postgres-integration"));
    let admin = std::sync::Arc::new(Admin::parse(&dsn));
    let doc = fixture();

    let control = admin.connect("postgres").await;
    let rows = control
        .simple_query("SELECT current_setting('server_version_num')::int / 10000")
        .await
        .unwrap();
    let major = rows
        .iter()
        .find_map(|m| match m {
            SimpleQueryMessage::Row(r) => r.get(0).map(str::to_string),
            _ => None,
        })
        .unwrap();
    assert_eq!(
        major,
        doc["server_major"].to_string(),
        "server major version"
    );

    let scenarios = doc["scenarios"].as_array().unwrap();
    let mut failures = Vec::new();
    let mut steps = 0;
    for spec in scenarios {
        steps += spec["steps"].as_array().unwrap().len();
        if let Err(failure) = run_scenario(&admin, spec).await {
            failures.push(failure);
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} scenarios differ:\n{}",
        failures.len(),
        scenarios.len(),
        failures.join("\n")
    );
    println!(
        "replayed {} scenarios / {steps} steps against PostgreSQL {major}",
        scenarios.len()
    );
}
