//! Resolve configured LLM keys without exposing secret references on failure.

use std::{
    io::Read,
    process::{Command, Stdio},
    sync::mpsc,
    thread,
    time::{Duration, Instant},
};

const PROCESS_TIMEOUT: Duration = Duration::from_secs(2);
const API_KEY_ENV: &str = "SYMDESK_LLM_API_KEY";

/// Resolves a raw API key, environment-backed key, Keychain key, or `op://` reference.
///
/// Missing tools, failed commands, and timed out commands return an empty string. In
/// particular, an unresolved `op://` reference is never returned as if it were a key.
pub fn resolve_key(reference: &str) -> String {
    resolve_key_with(reference, std::env::var(API_KEY_ENV).ok(), run_process)
}

fn resolve_key_with(
    reference: &str,
    environment_key: Option<String>,
    mut run: impl FnMut(&str, &[&str], Duration) -> Option<Vec<u8>>,
) -> String {
    let reference = if reference.is_empty() {
        environment_key.as_deref().unwrap_or_default()
    } else {
        reference
    };

    if reference.is_empty() {
        return run(
            "security",
            &[
                "find-generic-password",
                "-s",
                "symaira-desktop",
                "-a",
                "llm-api-key",
                "-w",
            ],
            PROCESS_TIMEOUT,
        )
        .map(|output| String::from_utf8_lossy(&output).trim().to_owned())
        .unwrap_or_default();
    }

    if reference.starts_with("op://") {
        return run("symvault", &["get", reference], PROCESS_TIMEOUT)
            .map(|output| String::from_utf8_lossy(&output).trim().to_owned())
            .unwrap_or_default();
    }

    reference.to_owned()
}

/// Runs a fixed executable with captured stdout and discarded stderr, enforcing a deadline.
///
/// Reading stdout on a helper thread allows the parent to keep checking the deadline even
/// when the child writes more than a pipe buffer. Stderr is sent to the null device so a
/// failing credential tool cannot disclose secret material through diagnostics.
fn run_process(program: &str, arguments: &[&str], timeout: Duration) -> Option<Vec<u8>> {
    let deadline = Instant::now() + timeout;
    let mut child = Command::new(program)
        .args(arguments)
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .ok()?;
    let mut stdout = child.stdout.take()?;
    let (sender, receiver) = mpsc::channel();
    let reader = thread::Builder::new()
        .name("symdesk-secret-output".to_owned())
        .spawn(move || {
            let mut output = Vec::new();
            let result = stdout.read_to_end(&mut output).ok().map(|_| output);
            let _ = sender.send(result);
        });
    let Ok(_reader) = reader else {
        let _ = child.kill();
        let _ = child.wait();
        return None;
    };

    let status = loop {
        match child.try_wait() {
            Ok(Some(status)) => break Some(status),
            Ok(None) if Instant::now() < deadline => {
                thread::sleep(Duration::from_millis(10));
            }
            Ok(None) | Err(_) => {
                let _ = child.kill();
                let _ = child.wait();
                break None;
            }
        }
    };
    if !status.is_some_and(|status| status.success()) {
        return None;
    }
    // A child can exit while a descendant still holds the inherited pipe open. Keep the
    // same deadline for output collection instead of joining the reader without a bound.
    let remaining = deadline.saturating_duration_since(Instant::now());
    receiver.recv_timeout(remaining).ok().flatten()
}

#[cfg(test)]
mod tests {
    #[cfg(unix)]
    use super::run_process;
    use super::{PROCESS_TIMEOUT, resolve_key_with};
    use std::time::Duration;

    #[cfg(unix)]
    use std::{
        fs,
        os::unix::fs::PermissionsExt,
        sync::atomic::{AtomicUsize, Ordering},
    };

    #[cfg(unix)]
    static NEXT_SCRIPT: AtomicUsize = AtomicUsize::new(0);

    #[cfg(unix)]
    fn fake_command(script: &str) -> std::path::PathBuf {
        let serial = NEXT_SCRIPT.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "symdesk-ai-secret-test-{}-{serial}",
            std::process::id()
        ));
        fs::write(&path, format!("#!/bin/sh\n{script}\n")).expect("write fake command");
        let mut permissions = fs::metadata(&path)
            .expect("stat fake command")
            .permissions();
        permissions.set_mode(0o700);
        fs::set_permissions(&path, permissions).expect("make fake command executable");
        path
    }

    #[test]
    fn raw_reference_is_returned_verbatim_without_running_a_process() {
        let key = resolve_key_with(" raw-key\n", None, |_, _, _| panic!("unexpected process"));
        assert_eq!(key, " raw-key\n");
    }

    #[test]
    fn empty_reference_uses_environment_before_keychain() {
        let key = resolve_key_with("", Some("env-key".to_owned()), |_, _, _| {
            panic!("environment key should avoid process lookup")
        });
        assert_eq!(key, "env-key");
    }

    #[test]
    fn empty_reference_uses_trimmed_keychain_output_with_bounded_timeout() {
        let key = resolve_key_with("", Some(String::new()), |program, args, timeout| {
            assert_eq!(program, "security");
            assert_eq!(
                args,
                [
                    "find-generic-password",
                    "-s",
                    "symaira-desktop",
                    "-a",
                    "llm-api-key",
                    "-w"
                ]
            );
            assert_eq!(timeout, PROCESS_TIMEOUT);
            Some(b" keychain-key \n".to_vec())
        });
        assert_eq!(key, "keychain-key");
    }

    #[test]
    fn keychain_timeout_returns_empty() {
        let key = resolve_key_with("", None, |_, _, timeout| {
            assert_eq!(timeout, Duration::from_secs(2));
            None
        });
        assert_eq!(key, "");
    }

    #[test]
    fn op_reference_uses_symvault_and_trims_resolved_value() {
        let key = resolve_key_with("op://vault/item/key", None, |program, args, timeout| {
            assert_eq!(program, "symvault");
            assert_eq!(args, ["get", "op://vault/item/key"]);
            assert_eq!(timeout, PROCESS_TIMEOUT);
            Some(b" resolved-key\n".to_vec())
        });
        assert_eq!(key, "resolved-key");
    }

    #[test]
    fn missing_failed_or_timed_out_symvault_never_returns_reference() {
        for result in [None, Some(Vec::new())] {
            let key = resolve_key_with("op://vault/item/key", None, |_, _, _| result.clone());
            assert_eq!(key, "");
        }
        let key = resolve_key_with("op://vault/item/key", None, |_, _, _| None);
        assert_eq!(key, "");
    }

    #[test]
    fn command_failure_stderr_is_not_used_as_a_key() {
        // The runner exposes stdout only; errors and stderr never enter the resolver result.
        let key = resolve_key_with("op://vault/item/key", None, |_, _, _| None);
        assert_eq!(key, "");
    }

    #[cfg(unix)]
    #[test]
    fn process_runner_discards_stderr() {
        let command = fake_command("printf 'stderr-secret' >&2\nprintf 'stdout-key\\n'");
        let output = run_process(
            command.to_str().expect("UTF-8 temp path"),
            &[],
            PROCESS_TIMEOUT,
        );
        fs::remove_file(command).expect("remove fake command");
        assert_eq!(output, Some(b"stdout-key\n".to_vec()));
    }

    #[cfg(unix)]
    #[test]
    fn process_runner_kills_a_command_at_its_deadline() {
        let command = fake_command("while :; do :; done");
        let started = std::time::Instant::now();
        let output = run_process(
            command.to_str().expect("UTF-8 temp path"),
            &[],
            Duration::from_millis(50),
        );
        fs::remove_file(command).expect("remove fake command");
        assert_eq!(output, None);
        assert!(started.elapsed() < Duration::from_secs(2));
    }

    #[cfg(unix)]
    #[test]
    fn process_runner_deadline_covers_descendants_holding_stdout_open() {
        // Waiting for the descendant would take at least 5s; returning near
        // the 50ms deadline proves it was not awaited. The 2s bound leaves
        // headroom for process spawn and reaping on loaded CI runners.
        let command = fake_command("(sleep 5) & exit 0");
        let started = std::time::Instant::now();
        let output = run_process(
            command.to_str().expect("UTF-8 temp path"),
            &[],
            Duration::from_millis(50),
        );
        fs::remove_file(command).expect("remove fake command");
        assert_eq!(output, None);
        assert!(started.elapsed() < Duration::from_secs(2));
    }
}
