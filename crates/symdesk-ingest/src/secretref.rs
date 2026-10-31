//! Port of `symaira-corekit/secretref.Resolve`: the shared secret-reference
//! format (`symvault://path`, `keychain://service/account`, `env://NAME`, bare
//! `NAME` treated as an environment variable). The resolved value is never
//! part of an error.

use std::io::Read;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

/// Bound for every symvault/keychain subprocess call (`DefaultTimeout`).
pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(5);

const NO_REFERENCE: &str = "no credential reference or default provided";
const SYMVAULT_NOT_FOUND: &str = "symvault binary not found on PATH";
const TIMEOUT: &str = "secretref: subprocess timed out";

fn label(reference: &str) -> &str {
    if reference.is_empty() {
        "<default>"
    } else {
        reference
    }
}

/// `secretref.Resolve(ctx, reference, envDefault)`.
pub fn resolve(reference: &str, env_default: &str) -> Result<String, String> {
    let target = if reference.is_empty() {
        env_default
    } else {
        reference
    };
    if target.is_empty() {
        return Err(NO_REFERENCE.to_owned());
    }
    if let Some(path) = target.strip_prefix("symvault://") {
        return resolve_symvault(path).map_err(|e| format!("resolve {}: {e}", label(reference)));
    }
    if let Some(rest) = target.strip_prefix("keychain://") {
        let (service, account) = match rest.split_once('/') {
            Some((s, a)) if !s.is_empty() && !a.is_empty() => (s, a),
            _ => {
                return Err(format!(
                    "resolve {}: invalid keychain reference, expected keychain://service/account",
                    label(reference)
                ));
            }
        };
        return resolve_keychain(service, account)
            .map_err(|e| format!("resolve {}: {e}", label(reference)));
    }
    let name = target.strip_prefix("env://").unwrap_or(target);
    match std::env::var_os(name) {
        Some(v) if !v.is_empty() => Ok(v.to_string_lossy().into_owned()),
        _ => Err(format!(
            "environment variable {name} is not set (reference {})",
            label(reference)
        )),
    }
}

fn validate_vault_path(path: &str) -> Result<(), String> {
    if path.is_empty() {
        return Err("invalid symvault credential path: empty".into());
    }
    if path.starts_with('-') {
        return Err("invalid symvault credential path: must not start with '-'".into());
    }
    if path.contains('\0') {
        return Err("invalid symvault credential path: contains a null byte".into());
    }
    // unicode.IsControl: the Cc category (C0, DEL and C1).
    if path
        .chars()
        .any(|c| c.is_control())
    {
        return Err("invalid symvault credential path: contains control characters".into());
    }
    Ok(())
}

/// `exec.LookPath`: the first executable regular file on `PATH`.
fn look_path(name: &str) -> bool {
    use std::os::unix::fs::PermissionsExt;
    let Some(paths) = std::env::var_os("PATH") else {
        return false;
    };
    std::env::split_paths(&paths).any(|dir| {
        let dir = if dir.as_os_str().is_empty() {
            std::path::PathBuf::from(".")
        } else {
            dir
        };
        std::fs::metadata(dir.join(name))
            .map(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
            .unwrap_or(false)
    })
}

fn run_capture(program: &str, args: &[&str], what: &str) -> Result<String, String> {
    let mut child = Command::new(program)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| e.to_string())?;
    let mut stdout = child.stdout.take();
    let mut stderr = child.stderr.take();
    let out_thread = std::thread::spawn(move || {
        let mut buf = Vec::new();
        if let Some(s) = stdout.as_mut() {
            let _ = s.read_to_end(&mut buf);
        }
        buf
    });
    let err_thread = std::thread::spawn(move || {
        let mut buf = Vec::new();
        if let Some(s) = stderr.as_mut() {
            let _ = s.read_to_end(&mut buf);
        }
        buf
    });
    let deadline = Instant::now() + DEFAULT_TIMEOUT;
    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) => {
                if Instant::now() >= deadline {
                    let _ = child.kill();
                    let _ = child.wait();
                    return Err(format!(
                        "{TIMEOUT}: {what} timed out after {}s",
                        DEFAULT_TIMEOUT.as_secs()
                    ));
                }
                std::thread::sleep(Duration::from_millis(5));
            }
            Err(e) => return Err(e.to_string()),
        }
    };
    let out = out_thread.join().unwrap_or_default();
    let err = err_thread.join().unwrap_or_default();
    if !status.success() {
        let base = exit_status_text(&status);
        let detail = String::from_utf8_lossy(&err).trim().to_owned();
        return Err(if detail.is_empty() {
            base
        } else {
            format!("{base}: {detail}")
        });
    }
    Ok(String::from_utf8_lossy(&out).trim().to_owned())
}

/// `exec.ExitError.Error()`.
fn exit_status_text(status: &std::process::ExitStatus) -> String {
    use std::os::unix::process::ExitStatusExt;
    match (status.code(), status.signal()) {
        (Some(code), _) => format!("exit status {code}"),
        (None, Some(sig)) => format!("signal: {}", signal_name(sig)),
        _ => "exit status 1".into(),
    }
}

fn signal_name(sig: i32) -> String {
    match sig {
        1 => "hangup",
        2 => "interrupt",
        3 => "quit",
        6 => "aborted",
        9 => "killed",
        11 => "segmentation fault",
        13 => "broken pipe",
        14 => "alarm clock",
        15 => "terminated",
        _ => return format!("signal {sig}"),
    }
    .to_owned()
}

fn resolve_symvault(path: &str) -> Result<String, String> {
    validate_vault_path(path)?;
    if !look_path("symvault") {
        return Err(SYMVAULT_NOT_FOUND.into());
    }
    run_capture("symvault", &["get", "--", path, "--print"], "symvault get")
}

#[cfg(target_os = "macos")]
fn resolve_keychain(service: &str, account: &str) -> Result<String, String> {
    run_capture(
        "security",
        &["find-generic-password", "-w", "-s", service, "-a", account],
        "keychain lookup",
    )
}

#[cfg(not(target_os = "macos"))]
fn resolve_keychain(_service: &str, _account: &str) -> Result<String, String> {
    Err("keychain:// references are only resolvable on macOS".into())
}
