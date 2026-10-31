//! Bounded child-process execution shared by the sibling-tool adapters.

use std::{
    io::Read,
    process::{Command, ExitStatus, Stdio},
    sync::mpsc,
    thread,
    time::{Duration, Instant},
};

/// Extra time to drain output after the child ended or was killed.
const PIPE_GRACE: Duration = Duration::from_secs(2);

/// A finished child. `timed_out` means the deadline killed it.
pub struct Finished {
    pub status: ExitStatus,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
    pub timed_out: bool,
    /// The caller's cancellation predicate killed it.
    pub cancelled: bool,
}

/// Run with stdin closed and stdout/stderr captured separately; kill the
/// child at the deadline. Spawn failures use Go's `fork/exec` wording.
///
/// # Errors
///
/// Returns the spawn failure text.
pub fn run_bounded(command: Command, timeout: Duration) -> Result<Finished, String> {
    run_cancellable(command, timeout, &|| false)
}

/// Like [`run_bounded`], and additionally kill the child as soon as `cancelled`
/// reports true (Go's `exec.CommandContext` on a cancelled context).
///
/// # Errors
///
/// Returns the spawn failure text.
pub fn run_cancellable(
    mut command: Command,
    timeout: Duration,
    cancelled: &dyn Fn() -> bool,
) -> Result<Finished, String> {
    command
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped());
    let program = command.get_program().to_string_lossy().into_owned();
    let mut child = command.spawn().map_err(|error| {
        format!(
            "fork/exec {program}: {}",
            crate::gofmt::os_error_text(&error)
        )
    })?;
    let drain = |mut pipe: Box<dyn Read + Send>| {
        let (sender, receiver) = mpsc::channel();
        thread::spawn(move || {
            let mut bytes = Vec::new();
            let _ = pipe.read_to_end(&mut bytes);
            let _ = sender.send(bytes);
        });
        receiver
    };
    let stdout = drain(Box::new(child.stdout.take().ok_or("stdout pipe")?));
    let stderr = drain(Box::new(child.stderr.take().ok_or("stderr pipe")?));
    let deadline = Instant::now() + timeout;
    let (status, timed_out, was_cancelled) = loop {
        match child.try_wait() {
            Ok(Some(status)) => break (status, false, false),
            Ok(None) if cancelled() => {
                let _ = child.kill();
                let status = child.wait().map_err(|error| error.to_string())?;
                break (status, false, true);
            }
            Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(5)),
            Ok(None) | Err(_) => {
                let _ = child.kill();
                let status = child.wait().map_err(|error| error.to_string())?;
                break (status, true, false);
            }
        }
    };
    // A descendant may keep the pipes open after the child is gone (Go would
    // block on Wait); bound the drain instead of hanging the caller.
    let grace = Instant::now() + PIPE_GRACE;
    let collect = |receiver: mpsc::Receiver<Vec<u8>>| {
        receiver
            .recv_timeout(grace.saturating_duration_since(Instant::now()))
            .unwrap_or_default()
    };
    let stdout = collect(stdout);
    let stderr = collect(stderr);
    Ok(Finished {
        status,
        stdout,
        stderr,
        timed_out,
        cancelled: was_cancelled,
    })
}
