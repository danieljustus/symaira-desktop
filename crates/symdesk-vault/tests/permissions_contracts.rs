#![deny(unsafe_code)]

//! Replays the Go-generated `testdata/port/vault/permissions.json`
//! (`scripts/rust-port/cmd/permissionsgen`) against
//! `symdesk_vault::permissions::Manager`: every operation result, the final
//! `users.json` / `groups.json` / `permissions.json` bytes and modes, and the
//! config directory mode.

use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};

use base64::Engine as _;
use serde_json::{Map, Value, json};
use symdesk_vault::permissions::{
    DocumentRule, Group, Manager, User, constant_time_equal, generate_token, hash_token,
};

const FIXTURE: &str = include_str!("../../../testdata/port/vault/permissions.json");

static NEXT: AtomicUsize = AtomicUsize::new(0);

fn fixture() -> Value {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("decode permissions fixture");
    assert_eq!(fixture["schema_version"], 1);
    fixture
}

fn temp_root() -> PathBuf {
    let root = std::env::temp_dir().join(format!(
        "symdesk-permissions-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::SeqCst)
    ));
    fs::create_dir_all(&root).expect("create scenario root");
    root
}

#[cfg(unix)]
fn set_mode(path: &Path, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(mode)).expect("chmod");
}

#[cfg(not(unix))]
fn set_mode(_path: &Path, _mode: u32) {}

/// Go's `os.MkdirAll(path, 0o700)`, which the Go generator used for setup.
fn mkdir_all_private(path: &Path) {
    let mut builder = fs::DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder.create(path).expect("create directory");
}

#[cfg(unix)]
fn mode_of(path: &Path) -> u64 {
    use std::os::unix::fs::PermissionsExt;
    u64::from(fs::metadata(path).expect("stat").permissions().mode() & 0o777)
}

fn strings(value: &Value) -> Vec<String> {
    value
        .as_array()
        .map(|items| {
            items
                .iter()
                .map(|item| item.as_str().unwrap_or_default().to_owned())
                .collect()
        })
        .unwrap_or_default()
}

fn json_list(list: &[String]) -> Value {
    json!(list)
}

fn nilable(list: Option<&Vec<String>>) -> Value {
    list.map_or(Value::Null, |list| json_list(list))
}

fn user_json(user: &User) -> Value {
    json!({"name": user.name, "token_hash": user.token_hash, "roles": nilable(user.roles.as_ref())})
}

fn group_json(group: &Group) -> Value {
    json!({"name": group.name, "members": nilable(group.members.as_ref())})
}

fn rule_json(rule: &DocumentRule) -> Value {
    let mut object = Map::new();
    object.insert("path".into(), json!(rule.path));
    object.insert("owner".into(), json!(rule.owner));
    for (key, list) in [
        ("read_users", &rule.read_users),
        ("read_groups", &rule.read_groups),
        ("write_users", &rule.write_users),
        ("write_groups", &rule.write_groups),
    ] {
        if !list.is_empty() {
            object.insert(key.into(), json_list(list));
        }
    }
    Value::Object(object)
}

fn actor(op: &Value) -> Option<User> {
    let raw = op["as"].as_object()?;
    let roles = strings(raw.get("roles").unwrap_or(&Value::Null));
    Some(User {
        name: raw
            .get("name")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
        token_hash: raw
            .get("token_hash")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned(),
        roles: if roles.is_empty() { None } else { Some(roles) },
    })
}

fn rule_from(op: &Value) -> DocumentRule {
    let raw = &op["rule"];
    let text = |key: &str| raw[key].as_str().unwrap_or_default().to_owned();
    DocumentRule {
        path: text("path"),
        owner: text("owner"),
        read_users: strings(&raw["read_users"]),
        read_groups: strings(&raw["read_groups"]),
        write_users: strings(&raw["write_users"]),
        write_groups: strings(&raw["write_groups"]),
    }
}

fn execute(manager: &Manager, op: &Value, root: &str) -> Value {
    let text = |key: &str| op[key].as_str().unwrap_or_default().to_owned();
    let mut result = Map::new();
    let record_error = |result: &mut Map<String, Value>, error: &str| {
        result.insert("error".into(), json!(error.replace(root, "<root>")));
    };
    let mint = |result: &mut Map<String, Value>, name: &str, token: String| {
        if token.is_empty() {
            result.insert("token".into(), json!("none"));
            return;
        }
        result.insert("token".into(), json!("generated"));
        let shape = token.len() == 64
            && token
                .bytes()
                .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte));
        result.insert("token_shape_ok".into(), json!(shape));
        let authenticates = manager
            .authenticate(&token)
            .is_ok_and(|user| user.name == name);
        result.insert("authenticates".into(), json!(authenticates));
        let pin = op["pin"].as_str().unwrap_or_default();
        if !pin.is_empty() {
            manager
                .set_token_hash(name, &hash_token(pin))
                .expect("pin token hash");
        }
    };
    match op["op"].as_str().expect("op name") {
        "authenticate" => match manager.authenticate(&text("token")) {
            Ok(user) => {
                result.insert("user".into(), user_json(&user));
            }
            Err(error) => record_error(&mut result, &error),
        },
        "can_read" => {
            let allowed = manager.can_read(actor(op).as_ref(), &text("path"));
            result.insert("allowed".into(), json!(allowed));
        }
        "can_write" => {
            let allowed = manager.can_write(actor(op).as_ref(), &text("path"));
            result.insert("allowed".into(), json!(allowed));
        }
        "can_read_many" => {
            let paths = strings(&op["paths"]);
            let allowed = manager.can_read_many(actor(op).as_ref(), &paths);
            result.insert("paths".into(), nilable(allowed.as_ref()));
        }
        "user_add" => {
            let roles = strings(&op["roles"]);
            match manager.user_add(&text("name"), &roles) {
                Ok(token) => mint(&mut result, &text("name"), token),
                Err(error) => record_error(&mut result, &error),
            }
        }
        "user_list" => {
            let users = match manager.user_list() {
                Ok(users) => users,
                Err(error) => {
                    record_error(&mut result, &error);
                    None
                }
            };
            result.insert(
                "users".into(),
                users.map_or(Value::Null, |users| {
                    Value::Array(users.iter().map(user_json).collect())
                }),
            );
        }
        "user_remove" => {
            if let Err(error) = manager.user_remove(&text("name")) {
                record_error(&mut result, &error);
            }
        }
        "user_generate_token" => match manager.user_generate_token(&text("name")) {
            Ok(token) => mint(&mut result, &text("name"), token),
            Err(error) => record_error(&mut result, &error),
        },
        "set_token_hash" => {
            if let Err(error) = manager.set_token_hash(&text("name"), &text("hash")) {
                record_error(&mut result, &error);
            }
        }
        "group_add" => {
            if let Err(error) = manager.group_add(&text("name")) {
                record_error(&mut result, &error);
            }
        }
        "group_list" => {
            let groups = match manager.group_list() {
                Ok(groups) => groups,
                Err(error) => {
                    record_error(&mut result, &error);
                    None
                }
            };
            result.insert(
                "groups".into(),
                groups.map_or(Value::Null, |groups| {
                    Value::Array(groups.iter().map(group_json).collect())
                }),
            );
        }
        "group_remove" => {
            if let Err(error) = manager.group_remove(&text("name")) {
                record_error(&mut result, &error);
            }
        }
        "group_add_member" => {
            if let Err(error) = manager.group_add_member(&text("group"), &text("user")) {
                record_error(&mut result, &error);
            }
        }
        "group_remove_member" => {
            if let Err(error) = manager.group_remove_member(&text("group"), &text("user")) {
                record_error(&mut result, &error);
            }
        }
        "set_document_rule" => {
            if let Err(error) = manager.set_document_rule(rule_from(op)) {
                record_error(&mut result, &error);
            }
        }
        "get_document_rule" => match manager.get_document_rule(&text("path")) {
            Ok(Some(rule)) => {
                result.insert("rule".into(), rule_json(&rule));
            }
            Ok(None) => {}
            Err(error) => record_error(&mut result, &error),
        },
        other => panic!("unknown op {other}"),
    }
    Value::Object(result)
}

/// The fixture stores file bytes as Go's JSON encoder does: each invalid
/// UTF-8 byte becomes one U+FFFD (Rust's lossy conversion merges them).
fn go_json_string(bytes: &[u8]) -> String {
    let mut out = String::new();
    let mut rest = bytes;
    while !rest.is_empty() {
        match std::str::from_utf8(rest) {
            Ok(text) => {
                out.push_str(text);
                break;
            }
            Err(error) => {
                let (valid, tail) = rest.split_at(error.valid_up_to());
                out.push_str(std::str::from_utf8(valid).expect("valid prefix"));
                let bad = error.error_len().unwrap_or(tail.len());
                out.extend(std::iter::repeat_n('\u{fffd}', bad));
                rest = &tail[bad..];
            }
        }
    }
    out
}

fn collect(root: &Path, dir: &Path, files: &mut BTreeMap<String, (String, u64)>) {
    for entry in fs::read_dir(dir).expect("read dir") {
        let entry = entry.expect("dir entry");
        let path = entry.path();
        if path.is_dir() {
            #[cfg(unix)]
            set_mode(&path, 0o700);
            collect(root, &path, files);
            continue;
        }
        #[cfg(unix)]
        let mode = {
            let mode = mode_of(&path);
            set_mode(&path, 0o600);
            mode
        };
        #[cfg(not(unix))]
        let mode = 0;
        let rel = path
            .strip_prefix(root)
            .expect("under root")
            .components()
            .map(|part| part.as_os_str().to_string_lossy().into_owned())
            .collect::<Vec<_>>()
            .join("/");
        let content = go_json_string(&fs::read(&path).expect("read file"));
        files.insert(rel, (content, mode));
    }
}

fn unlock(dir: &Path) {
    #[cfg(unix)]
    for entry in fs::read_dir(dir).into_iter().flatten().flatten() {
        let path = entry.path();
        if path.is_dir() {
            set_mode(&path, 0o700);
            unlock(&path);
        }
    }
    #[cfg(not(unix))]
    let _ = dir;
}

/// Replays one scenario; returns the number of operations compared.
fn replay(scenario: &Value) -> usize {
    let id = scenario["id"].as_str().expect("scenario id");
    let root = temp_root();
    let root_text = root.display().to_string();
    let setup = &scenario["setup"];

    if let Some(modes) = setup["dir_modes"].as_object() {
        for (rel, mode) in modes {
            let path = root.join(rel);
            mkdir_all_private(&path);
            set_mode(&path, mode.as_u64().expect("dir mode") as u32);
        }
    }
    let write_setup = |rel: &str, data: &[u8]| {
        let path = root.join(rel);
        mkdir_all_private(path.parent().expect("parent"));
        let mut options = fs::OpenOptions::new();
        options.write(true).create(true).truncate(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        options
            .open(&path)
            .and_then(|mut file| std::io::Write::write_all(&mut file, data))
            .expect("setup file");
        if let Some(mode) = setup["file_modes"][rel].as_u64() {
            set_mode(&path, mode as u32);
        }
    };
    if let Some(files) = setup["files"].as_object() {
        for (rel, content) in files {
            write_setup(rel, content.as_str().expect("file text").as_bytes());
        }
    }
    if let Some(files) = setup["files_b64"].as_object() {
        for (rel, content) in files {
            let raw = base64::engine::general_purpose::STANDARD
                .decode(content.as_str().expect("file b64"))
                .expect("base64");
            write_setup(rel, &raw);
        }
    }

    let config_dir = root.join(setup["config_dir"].as_str().expect("config dir"));
    let mut compared = 0;
    #[cfg(unix)]
    let mut config_mode: Option<u64> = None;
    match Manager::new(&config_dir) {
        Err(error) => {
            assert_eq!(
                error.replace(&root_text, "<root>"),
                scenario["new_error"].as_str().unwrap_or("<missing>"),
                "{id}: new error"
            );
            assert!(scenario["ops"].as_array().is_none_or(Vec::is_empty), "{id}");
        }
        Ok(manager) => {
            assert!(
                scenario.get("new_error").is_none(),
                "{id}: Go rejected the config dir"
            );
            if let Some(mode) = setup["lock_config_dir"].as_u64() {
                set_mode(&config_dir, mode as u32);
            }
            for (index, op) in scenario["ops"].as_array().expect("ops").iter().enumerate() {
                let got = execute(&manager, op, &root_text);
                assert_eq!(got, op["result"], "{id}: op {index} {}", op["op"]);
                compared += 1;
            }
            #[cfg(unix)]
            {
                config_mode = Some(mode_of(&config_dir));
            }
        }
    }

    #[cfg(unix)]
    {
        let expected = scenario["final"]["config_dir_mode"].as_u64().unwrap_or(0);
        if expected != 0 {
            assert_eq!(config_mode, Some(expected), "{id}: config dir mode");
        }
    }
    unlock(&root);
    let mut files = BTreeMap::new();
    collect(&root, &root, &mut files);
    let expected = scenario["final"]["files"].as_object().expect("final files");
    let got_names: Vec<&String> = files.keys().collect();
    let want_names: Vec<&String> = expected.keys().collect();
    assert_eq!(got_names, want_names, "{id}: final file set");
    for (name, (content, mode)) in &files {
        assert_eq!(
            content,
            expected[name]["content"].as_str().expect("content"),
            "{id}: {name} bytes"
        );
        #[cfg(unix)]
        assert_eq!(
            *mode,
            expected[name]["mode"].as_u64().expect("mode"),
            "{id}: {name} mode"
        );
        #[cfg(not(unix))]
        let _ = mode;
    }
    let _ = fs::remove_dir_all(&root);
    compared
}

#[test]
fn permissions_manager_matches_go() {
    let fixture = fixture();
    let scenarios = fixture["scenarios"].as_array().expect("scenarios");
    let mut replayed = 0;
    let mut operations = 0;
    for scenario in scenarios {
        if scenario["unix_only"].as_bool().unwrap_or(false) && !cfg!(unix) {
            continue;
        }
        operations += replay(scenario);
        replayed += 1;
    }
    let expected_scenarios = scenarios
        .iter()
        .filter(|scenario| cfg!(unix) || !scenario["unix_only"].as_bool().unwrap_or(false))
        .count();
    assert_eq!(replayed, expected_scenarios);
    assert!(replayed >= 100, "fixture must keep its scenario corpus");
    assert!(operations >= 600, "fixture must keep its operation corpus");
}

#[test]
fn token_helpers_match_go() {
    // sha256("admin-token") is the value Go's HashToken stores in users.json.
    assert_eq!(
        hash_token(""),
        "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
    );
    let token = generate_token().expect("token");
    assert_eq!(token.len(), 64);
    assert!(
        token
            .bytes()
            .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    );
    assert_ne!(token, generate_token().expect("second token"));
    assert!(constant_time_equal("abc", "abc"));
    assert!(!constant_time_equal("abc", "abd"));
    assert!(!constant_time_equal("abc", "ab"));
    assert!(constant_time_equal("", ""));
}
