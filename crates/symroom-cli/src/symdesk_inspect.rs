#![deny(unsafe_code)]
// This module is shared between the `symroom` binary and the MCP integration
// test (which includes `mcp.rs` and this file by path), so it stays free of
// dependencies on the other CLI modules.

use std::{
    ffi::OsStr,
    io::Read,
    path::{Path, PathBuf},
    process::{Command, Stdio},
    sync::mpsc,
    thread,
    time::{Duration, Instant},
};

use serde::{
    Deserialize,
    de::{Deserializer, MapAccess, Visitor},
};
use serde_json::Value;

/// Go `exec.LookPath` over the process `PATH`.
pub(crate) fn look_path(name: &str) -> Option<PathBuf> {
    look_path_in(name, None)
}

/// Go `exec.LookPath` over `search_path`, or the process `PATH` when `None`.
pub(crate) fn look_path_in(name: &str, search_path: Option<&OsStr>) -> Option<PathBuf> {
    let search_path = search_path
        .map(OsStr::to_os_string)
        .or_else(|| std::env::var_os("PATH"))?;
    for directory in std::env::split_paths(&search_path) {
        #[cfg(windows)]
        let names = windows_tool_names(name);
        #[cfg(not(windows))]
        let names = vec![name.to_owned()];
        for candidate in names {
            let path = directory.join(candidate);
            if path.is_file() && is_executable(&path) {
                return Some(path);
            }
        }
    }
    None
}

#[cfg(windows)]
fn windows_tool_names(name: &str) -> Vec<String> {
    if Path::new(name).extension().is_some() {
        return vec![name.to_owned()];
    }
    let extensions = std::env::var_os("PATHEXT")
        .map(|value| {
            value
                .to_string_lossy()
                .split(';')
                .map(|extension| extension.to_ascii_lowercase())
                .filter(|extension| !extension.is_empty())
                .collect::<Vec<_>>()
        })
        .filter(|extensions| !extensions.is_empty())
        .unwrap_or_else(|| vec![".com".to_owned(), ".exe".to_owned()]);
    extensions
        .into_iter()
        .map(|extension| format!("{name}{extension}"))
        .collect()
}

#[cfg(unix)]
fn is_executable(path: &Path) -> bool {
    use std::os::unix::fs::PermissionsExt;
    std::fs::metadata(path).is_ok_and(|metadata| metadata.permissions().mode() & 0o111 != 0)
}

#[cfg(not(unix))]
fn is_executable(path: &Path) -> bool {
    path.is_file()
}

pub(crate) fn symdesk_command(path: &Path) -> Command {
    let mut command = Command::new(path);
    if let Some(path) = std::env::var_os("PATH") {
        command.env("PATH", path);
    }
    command
}

/// Ordered object members (duplicates kept) so Go's last-match-wins struct
/// decoding can be replayed.
struct Pairs(Vec<(String, Value)>);

impl<'de> Deserialize<'de> for Pairs {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct PairsVisitor;
        impl<'de> Visitor<'de> for PairsVisitor {
            type Value = Pairs;
            fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                formatter.write_str("a JSON object")
            }
            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Pairs, A::Error> {
                let mut pairs = Vec::new();
                while let Some(entry) = map.next_entry::<String, Value>()? {
                    pairs.push(entry);
                }
                Ok(Pairs(pairs))
            }
        }
        deserializer.deserialize_map(PairsVisitor)
    }
}

/// Go `json.Unmarshal(output, &desk.InspectResult{...})` followed by the
/// caller's `err == nil` check: any decode error (syntax, non-object, or a
/// field of the wrong type) discards the whole result.
fn document_id_from_output(output: &[u8]) -> String {
    match serde_json::from_slice::<Value>(output) {
        Ok(Value::Null) => return String::new(),
        Ok(Value::Object(_)) => {}
        _ => return String::new(),
    }
    let Ok(Pairs(pairs)) = serde_json::from_slice::<Pairs>(output) else {
        return String::new();
    };
    let mut document_id = String::new();
    for (key, value) in pairs {
        let field = key.to_ascii_lowercase();
        match (field.as_str(), value) {
            ("document_id", Value::String(text)) => document_id = text,
            ("vault_name", Value::String(_)) | ("valid", Value::Bool(_)) => {}
            ("document_id" | "vault_name" | "valid", Value::Null) => {}
            ("document_id" | "vault_name" | "valid", _) => return String::new(),
            _ => {}
        }
    }
    document_id
}

/// Go `desk.InspectPath`: `symdesk inspect <path> --json` under a one second
/// deadline; every failure yields an empty document id.
pub(crate) fn inspect_symdesk(path: &Path) -> String {
    inspect_symdesk_in(path, None)
}

pub(crate) fn inspect_symdesk_in(path: &Path, search_path: Option<&OsStr>) -> String {
    let Some(symdesk_path) = look_path_in("symdesk", search_path) else {
        return String::new();
    };
    let mut child = match symdesk_command(&symdesk_path)
        .arg("inspect")
        .arg(path)
        .arg("--json")
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
    {
        Ok(child) => child,
        Err(_) => return String::new(),
    };
    let Some(mut stdout) = child.stdout.take() else {
        let _ = child.kill();
        let _ = child.wait();
        return String::new();
    };
    let (send, receive) = mpsc::channel();
    thread::spawn(move || {
        let mut output = Vec::new();
        let _ = stdout.read_to_end(&mut output);
        let _ = send.send(output);
    });
    let deadline = Instant::now() + Duration::from_secs(1);
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                if !status.success() {
                    return String::new();
                }
                let Ok(output) = receive.recv_timeout(Duration::from_millis(100)) else {
                    return String::new();
                };
                return document_id_from_output(&output);
            }
            Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(10)),
            Ok(None) | Err(_) => {
                let _ = child.kill();
                let _ = child.wait();
                let _ = receive.recv_timeout(Duration::from_millis(100));
                return String::new();
            }
        }
    }
}
