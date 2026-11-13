//! Replays the Go `internal/compose` symmemory adapters against a loopback
//! shell double (`make compose-memory-fixtures-generate`).
#![cfg(unix)]

use std::{
    fs,
    os::unix::fs::PermissionsExt,
    path::Path,
    sync::atomic::{AtomicU32, Ordering},
    time::Instant,
};

use serde::Deserialize;
use serde_json::Value;
use symdesk_service::{gofmt::escape_json, memory};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    symmemory_script: String,
    calls: Vec<Call>,
}

#[derive(Deserialize, Clone)]
struct Call {
    #[serde(rename = "fn")]
    function: String,
    #[serde(default)]
    args: Vec<String>,
    mode: String,
    #[serde(default)]
    pre_entities: Vec<String>,
    #[serde(default)]
    missing: bool,
    #[serde(default)]
    value: String,
    #[serde(default)]
    error: String,
    #[serde(default)]
    log: String,
    #[serde(default)]
    state: Vec<String>,
}

static COUNTER: AtomicU32 = AtomicU32::new(0);
/// Serializes the write-then-exec of the fake tool. Two test threads doing it
/// at once race on Linux: a fork in one thread briefly inherits the other
/// thread's still-open script descriptor and the exec fails with ETXTBSY.
static REPLAY: std::sync::Mutex<()> = std::sync::Mutex::new(());

fn fixture() -> Fixture {
    serde_json::from_str(include_str!(
        "../../../testdata/port/ai/compose-memory.json"
    ))
    .expect("decode Go-owned compose memory fixture")
}

fn json<T: serde::Serialize>(value: &T) -> String {
    escape_json(&serde_json::to_string(value).unwrap())
}

struct Observed {
    value: String,
    error: String,
    log: String,
    state: Vec<String>,
}

/// Go's `encoding/json` syntax diagnostics are a declared difference: for
/// decode failures only the Go-owned prefix is compared.
fn comparable(error: &str) -> String {
    match error.find("failed to unmarshal ") {
        Some(start) => {
            let tail = &error[start..];
            let end = tail.find(": ").unwrap_or(tail.len());
            error[..start + end].to_owned()
        }
        None => error.to_owned(),
    }
}

fn replay(call: &Call, script: &str) -> Observed {
    let _serial = REPLAY
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner);
    let root = std::env::temp_dir().join(format!(
        "symdesk-compose-memory-{}-{}",
        std::process::id(),
        COUNTER.fetch_add(1, Ordering::SeqCst)
    ));
    let state = root.join("state");
    fs::create_dir_all(&state).unwrap();
    let write_exec = |name: &str, content: &str| {
        let path = root.join(name);
        fs::write(&path, content).unwrap();
        fs::set_permissions(&path, fs::Permissions::from_mode(0o700)).unwrap();
        path
    };
    write_exec("symmemory-double", script);
    let log_path = root.join("symmemory.log");
    // The double reads its parameters from the environment; a wrapper exports
    // them so this test never mutates its own process environment.
    let wrapper = write_exec(
        "symmemory",
        &format!(
            "#!/bin/sh\nexport FAKE_SYMMEMORY_STATE='{}'\nexport FAKE_SYMMEMORY_LOG='{}'\nexport FAKE_SYMMEMORY_MODE='{}'\nexec '{}' \"$@\"\n",
            state.display(),
            log_path.display(),
            call.mode,
            root.join("symmemory-double").display()
        ),
    );
    for name in &call.pre_entities {
        fs::write(state.join(name), "").unwrap();
    }
    let wrapper_path = wrapper.display().to_string();
    let missing = call.missing;
    let resolve = move |name: &str| -> Result<String, String> {
        if missing {
            Err(format!("synthetic: {name} not installed"))
        } else {
            Ok(wrapper_path.clone())
        }
    };
    let arg = |index: usize| call.args[index].as_str();
    let outcome: Result<String, String> = match call.function.as_str() {
        "ListEntities" => memory::list_entities(&resolve).map(|v| json(&v)),
        "GetNeighbors" => memory::get_neighbors(&resolve, arg(0)).map(|v| json(&v)),
        "ResolveCandidates" => memory::resolve_candidates(&resolve, arg(0)).map(|v| json(&v)),
        "ShowEntity" => memory::show_entity(&resolve, arg(0)).map(|v| json(&v)),
        "EnsureEntity" => memory::ensure_entity(&resolve, arg(0), arg(1)).map(|v| json(&v)),
        "RelateEntities" => {
            memory::relate_entities(&resolve, arg(0), arg(1), arg(2)).map(|()| json(&Value::Null))
        }
        "SetMemory" => {
            memory::set_memory(&resolve, arg(0), arg(1), &call.args[2..]).map(|v| json(&v))
        }
        other => panic!("unknown function {other}"),
    };
    let (value, error) = match outcome {
        Ok(value) => (value, String::new()),
        Err(error) => (String::new(), error),
    };
    let log = fs::read_to_string(&log_path).unwrap_or_default();
    let mut state_names: Vec<String> = fs::read_dir(&state)
        .unwrap()
        .map(|entry| entry.unwrap().file_name().to_string_lossy().into_owned())
        .collect();
    state_names.sort();
    remove(&root);
    Observed {
        value,
        error,
        log,
        state: state_names,
    }
}

fn remove(root: &Path) {
    fs::remove_dir_all(root).unwrap();
}

#[test]
fn symmemory_adapters_match_go_oracle() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.calls.len(), 30);
    let started = Instant::now();
    for (index, call) in fixture.calls.iter().enumerate() {
        let label = format!("#{index} {} {:?} ({})", call.function, call.args, call.mode);
        let observed = replay(call, &fixture.symmemory_script);
        assert_eq!(observed.value, call.value, "{label}: value");
        assert_eq!(
            comparable(&observed.error),
            comparable(&call.error),
            "{label}: error"
        );
        assert_eq!(observed.log, call.log, "{label}: argv transcript");
        assert_eq!(observed.state, call.state, "{label}: tool state");
    }
    // The only slow case is Go's 5s ListEntities deadline.
    assert!(
        started.elapsed().as_secs() < 60,
        "deadline must kill the hung tool"
    );
}

#[test]
fn replay_detects_a_tampered_oracle_expectation() {
    let fixture = fixture();
    let call = fixture
        .calls
        .iter()
        .find(|call| call.function == "SetMemory" && call.mode == "ok")
        .cloned()
        .unwrap();
    let observed = replay(&call, &fixture.symmemory_script);
    assert_eq!(observed.value, call.value, "error: {}", observed.error);
    assert_ne!(observed.value, call.value.replace("m1", "m2"));
}
