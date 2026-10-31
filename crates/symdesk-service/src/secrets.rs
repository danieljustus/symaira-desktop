//! Go `internal/secrets`: resolve the configured LLM key without exposing
//! secret references on failure.
//!
//! Resolution order (Go `ResolveKey`): an empty reference falls back to
//! `SYMDESK_LLM_API_KEY`, then the macOS Keychain entry; an `op://` reference
//! needs `symvault` (discovered through the sibling-tool probe, executed by
//! name on `PATH`); anything else is the raw key, verbatim. Every failure
//! returns the empty string: neither a reference nor a diagnostic escapes.

use std::{
    io::Read,
    process::{Command, Stdio},
    sync::mpsc,
    thread,
    time::{Duration, Instant},
};

use symdesk_core::{compose::Resolver, compose_probe::ToolCache};

const PROCESS_TIMEOUT: Duration = Duration::from_secs(2);
const API_KEY_ENV: &str = "SYMDESK_LLM_API_KEY";
const KEYCHAIN_ARGS: [&str; 6] = [
    "find-generic-password",
    "-s",
    "symaira-desktop",
    "-a",
    "llm-api-key",
    "-w",
];

/// Everything resolution reads from its surroundings, made explicit so
/// callers and tests never depend on ambient process state.
pub struct Environment<'a> {
    /// `SYMDESK_LLM_API_KEY`, when set (empty is treated as unset by Go).
    pub api_key: Option<String>,
    /// Sibling-tool discovery inputs for `symvault`.
    pub resolver: &'a Resolver,
    /// `HasTool` memoization, shared for the process lifetime.
    pub tools: &'a ToolCache,
    /// `PATH` used to find `security` and `symvault` for execution.
    pub path: &'a str,
}

/// Resolves a raw API key, environment-backed key, Keychain key, or `op://` reference.
///
/// Missing tools, failed commands, and timed out commands return an empty string. In
/// particular, an unresolved `op://` reference is never returned as if it were a key.
#[must_use]
pub fn resolve_key_in(reference: &str, environment: &Environment<'_>) -> String {
    resolve_key_with(
        reference,
        environment.api_key.clone(),
        || has_symvault(environment),
        |program, arguments, timeout| run_process(program, arguments, timeout, environment.path),
    )
}

fn has_symvault(environment: &Environment<'_>) -> bool {
    environment
        .tools
        .has_tool(environment.resolver, "symvault")
        .available
}

/// Go `Source`: a human-readable origin of the key, never the key itself.
#[must_use]
pub fn source_in(reference: &str, environment: &Environment<'_>) -> String {
    let reference = match reference {
        "" => environment.api_key.clone().unwrap_or_default(),
        other => other.to_owned(),
    };
    if reference.is_empty() {
        let found = run_process(
            "security",
            &KEYCHAIN_ARGS,
            PROCESS_TIMEOUT,
            environment.path,
        )
        .is_some();
        return if found { "keychain" } else { "none" }.to_owned();
    }
    if reference.starts_with("op://") {
        return if has_symvault(environment) {
            "symvault"
        } else {
            "symvault (missing)"
        }
        .to_owned();
    }
    "config/env".to_owned()
}

/// Process-environment convenience over [`resolve_key_in`].
#[must_use]
pub fn resolve_key(reference: &str) -> String {
    use std::sync::OnceLock;
    static TOOLS: OnceLock<ToolCache> = OnceLock::new();
    let Ok(resolver) = Resolver::from_environment() else {
        return String::new();
    };
    let path = std::env::var("PATH").unwrap_or_default();
    resolve_key_in(
        reference,
        &Environment {
            api_key: std::env::var(API_KEY_ENV).ok(),
            resolver: &resolver,
            tools: TOOLS.get_or_init(ToolCache::default),
            path: &path,
        },
    )
}

pub fn resolve_key_with(
    reference: &str,
    environment_key: Option<String>,
    mut has_symvault: impl FnMut() -> bool,
    mut run: impl FnMut(&str, &[&str], Duration) -> Option<Vec<u8>>,
) -> String {
    let reference = if reference.is_empty() {
        environment_key.as_deref().unwrap_or_default()
    } else {
        reference
    };

    if reference.is_empty() {
        return run("security", &KEYCHAIN_ARGS, PROCESS_TIMEOUT)
            .map(|output| String::from_utf8_lossy(&output).trim().to_owned())
            .unwrap_or_default();
    }

    if reference.starts_with("op://") {
        if !has_symvault() {
            return String::new();
        }
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
fn run_process(
    program: &str,
    arguments: &[&str],
    timeout: Duration,
    path: &str,
) -> Option<Vec<u8>> {
    let deadline = Instant::now() + timeout;
    let mut child = Command::new(program)
        .args(arguments)
        .env("PATH", path)
        .stdin(Stdio::null())
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
    use super::{PROCESS_TIMEOUT, resolve_key_with, run_process};
    use std::time::Duration;

    const PATH: &str = "/usr/bin:/bin";
    const NEVER: fn() -> bool = || panic!("unexpected symvault probe");

    #[test]
    fn raw_reference_is_returned_verbatim_without_running_a_process() {
        let key = resolve_key_with(" raw-key\n", None, NEVER, |_, _, _| {
            panic!("unexpected process")
        });
        assert_eq!(key, " raw-key\n");
    }

    #[test]
    fn empty_reference_uses_environment_before_keychain() {
        let key = resolve_key_with("", Some("env-key".to_owned()), NEVER, |_, _, _| {
            panic!("environment key should avoid process lookup")
        });
        assert_eq!(key, "env-key");
    }

    #[test]
    fn empty_reference_uses_trimmed_keychain_output_with_bounded_timeout() {
        let key = resolve_key_with("", Some(String::new()), NEVER, |program, args, timeout| {
            assert_eq!(program, "security");
            assert_eq!(args[0], "find-generic-password");
            assert_eq!(args[5], "-w");
            assert_eq!(timeout, PROCESS_TIMEOUT);
            Some(b" keychain-key \n".to_vec())
        });
        assert_eq!(key, "keychain-key");
    }

    #[test]
    fn op_reference_needs_symvault_and_trims_resolved_value() {
        let key = resolve_key_with(
            "op://vault/item/key",
            None,
            || true,
            |program, args, timeout| {
                assert_eq!(program, "symvault");
                assert_eq!(args, ["get", "op://vault/item/key"]);
                assert_eq!(timeout, PROCESS_TIMEOUT);
                Some(b" resolved-key\n".to_vec())
            },
        );
        assert_eq!(key, "resolved-key");
        let missing = resolve_key_with(
            "op://vault/item/key",
            None,
            || false,
            |_, _, _| panic!("a missing symvault must not be executed"),
        );
        assert_eq!(missing, "");
    }

    #[test]
    fn failed_or_timed_out_tools_never_return_the_reference() {
        for result in [None, Some(Vec::new())] {
            let key = resolve_key_with(
                "op://vault/item/key",
                None,
                || true,
                |_, _, _| result.clone(),
            );
            assert_eq!(key, "");
        }
    }

    #[test]
    fn process_runner_discards_stderr() {
        let output = run_process(
            "/bin/sh",
            &["-c", "printf 'stderr-secret' >&2\nprintf 'stdout-key\\n'"],
            PROCESS_TIMEOUT,
            PATH,
        );
        assert_eq!(output, Some(b"stdout-key\n".to_vec()));
    }

    #[test]
    fn process_runner_rejects_nonzero_status() {
        let output = run_process(
            "/bin/sh",
            &["-c", "printf 'stdout-key\\n'; exit 7"],
            PROCESS_TIMEOUT,
            PATH,
        );
        assert_eq!(output, None);
    }

    #[test]
    fn process_runner_kills_a_command_at_its_deadline() {
        let started = std::time::Instant::now();
        let output = run_process(
            "/bin/sh",
            &["-c", "while :; do :; done"],
            Duration::from_millis(50),
            PATH,
        );
        assert_eq!(output, None);
        assert!(started.elapsed() < Duration::from_secs(2));
    }

    #[test]
    fn process_runner_deadline_covers_descendants_holding_stdout_open() {
        // Waiting for the descendant would take at least 5s; returning near
        // the 50ms deadline proves it was not awaited.
        let started = std::time::Instant::now();
        let output = run_process(
            "/bin/sh",
            &["-c", "(sleep 5) & exit 0"],
            Duration::from_millis(50),
            PATH,
        );
        assert_eq!(output, None);
        assert!(started.elapsed() < Duration::from_secs(2));
    }
}
