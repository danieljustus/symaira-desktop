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
    match search_path {
        Some(path) => symroom_core::lookpath::look_path_in(name, path),
        None => symroom_core::lookpath::look_path(name),
    }
}

pub(crate) fn symdesk_command(path: &Path) -> Command {
    let mut command = Command::new(path);
    // Go's `exec.Cmd` with a nil Stdin reads from the null device, so the child
    // must never inherit the caller's stdin (the MCP JSON-RPC client stream).
    command.stdin(Stdio::null());
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
        .stdin(Stdio::null())
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

#[cfg(all(test, unix))]
mod tests {
    use std::{ffi::OsString, os::unix::fs::PermissionsExt};

    use super::*;

    fn write_executable(path: &Path) {
        std::fs::write(path, "#!/bin/sh\nexit 0\n").expect("write fake tool");
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o755))
            .expect("make fake tool executable");
    }

    /// Removes the probe file from the working directory even on a panic.
    struct RemoveOnDrop(PathBuf);

    impl Drop for RemoveOnDrop {
        fn drop(&mut self) {
            let _ = std::fs::remove_file(&self.0);
        }
    }

    #[test]
    fn absolute_path_hit_is_found() {
        let directory =
            std::env::temp_dir().join(format!("symdesk-lookpath-abs-{}", std::process::id()));
        std::fs::create_dir_all(&directory).expect("tool directory");
        write_executable(&directory.join("symdesk"));
        let found = look_path_in("symdesk", Some(directory.as_os_str()));
        let _ = std::fs::remove_dir_all(&directory);
        assert_eq!(found, Some(directory.join("symdesk")));
    }

    /// Go `exec.LookPath` returns `ErrDot` for a relative hit and does not keep
    /// searching, which `desk.InspectPath` reports as "symdesk not found".
    #[test]
    fn dot_and_empty_path_elements_are_rejected_like_go_errdot() {
        let name = format!("symdesk-relative-probe-{}", std::process::id());
        let probe = std::env::current_dir().expect("cwd").join(&name);
        let _guard = RemoveOnDrop(probe.clone());
        write_executable(&probe);
        for element in [".", ""] {
            assert_eq!(
                look_path_in(&name, Some(OsStr::new(element))),
                None,
                "PATH element {element:?} must not resolve to the working directory"
            );
        }
        // The first hit decides: a later absolute directory is not consulted.
        let absolute =
            std::env::temp_dir().join(format!("symdesk-lookpath-later-{}", std::process::id()));
        std::fs::create_dir_all(&absolute).expect("tool directory");
        write_executable(&absolute.join(&name));
        let mut joined = OsString::from(".");
        joined.push(":");
        joined.push(absolute.as_os_str());
        let found = look_path_in(&name, Some(joined.as_os_str()));
        let _ = std::fs::remove_dir_all(&absolute);
        assert_eq!(found, None, "relative hit first: Go stops with ErrDot");
    }

    #[test]
    fn other_relative_directories_are_rejected_too() {
        let cwd = std::env::current_dir().expect("cwd");
        let directory =
            std::env::temp_dir().join(format!("symdesk-lookpath-rel-{}", std::process::id()));
        std::fs::create_dir_all(&directory).expect("tool directory");
        write_executable(&directory.join("symdesk"));
        let mut relative = PathBuf::new();
        for _ in cwd.components().skip(1) {
            relative.push("..");
        }
        relative.push(directory.strip_prefix("/").expect("absolute temp dir"));
        let found = look_path_in("symdesk", Some(relative.as_os_str()));
        let _ = std::fs::remove_dir_all(&directory);
        assert_eq!(found, None);
    }
}
