//! Bounded PDF-tool capture. Unix owns a process group; escaped groups are not
//! a sandbox guarantee. Native non-Unix tree ownership is still unaccepted.
use crate::pdfops::Context;
use std::path::Path;

#[cfg(unix)]
pub(crate) fn run(ctx: &Context, path: &Path, args: &[String]) -> Result<String, String> {
    use std::{
        io::{self, Read},
        net::Shutdown,
        os::{
            fd::OwnedFd,
            unix::{net::UnixStream, process::CommandExt},
        },
        process::{Command, Stdio},
        sync::mpsc,
        thread,
        time::{Duration, Instant},
    };
    const LIMIT: usize = 16 * 1024 * 1024;
    fn capture(mut reader: UnixStream) -> Result<Vec<u8>, String> {
        let mut bytes = Vec::new();
        let mut chunk = [0u8; 8192];
        loop {
            match reader.read(&mut chunk) {
                Ok(0) => return Ok(bytes),
                Ok(n) => {
                    if bytes.len() + n > LIMIT {
                        return Err("PDF tool output exceeds 16 MiB".into());
                    }
                    bytes.extend_from_slice(&chunk[..n]);
                }
                Err(e) if e.kind() == io::ErrorKind::Interrupted => continue,
                Err(e) => return Err(e.to_string()),
            }
        }
    }
    let label = path.file_name().unwrap_or_default().to_string_lossy();
    if let Some(error) = ctx.error() {
        return Err(format!("{label}: {error}"));
    }
    let (out, out_writer) = UnixStream::pair().map_err(|e| e.to_string())?;
    let (err, err_writer) = UnixStream::pair().map_err(|e| e.to_string())?;
    let stop_out = out.try_clone().map_err(|e| e.to_string())?;
    let stop_err = err.try_clone().map_err(|e| e.to_string())?;
    let mut child = Command::new(path)
        .args(args)
        .process_group(0)
        .stdin(Stdio::null())
        .stdout(Stdio::from(OwnedFd::from(out_writer)))
        .stderr(Stdio::from(OwnedFd::from(err_writer)))
        .spawn()
        .map_err(|e| format!("{label}: fork/exec {}: {e}", path.display()))?;
    // Independent blocking readers drain at producer speed without sleeping
    // between socket chunks. The owner can shut them down before joining,
    // even if an escaped descendant keeps an inherited handle open.
    let (send, receive) = mpsc::channel();
    let readers: Vec<_> = [out, err]
        .into_iter()
        .enumerate()
        .map(|(index, reader)| {
            let send = send.clone();
            thread::spawn(move || {
                let _ = send.send((index, capture(reader)));
            })
        })
        .collect();
    drop(send);
    let mut captures = [None, None];
    let deadline = Instant::now() + Duration::from_secs(60);
    let mut capture_error = None;
    let status = loop {
        while let Ok((index, result)) = receive.try_recv() {
            captures[index] = Some(result);
        }
        if captures.iter().flatten().any(Result::is_err) {
            break None;
        }
        match child.try_wait() {
            Ok(Some(status)) => break Some(status),
            Ok(None) => {}
            Err(error) => {
                capture_error = Some(error.to_string());
                break None;
            }
        }
        if ctx.error().is_some() || Instant::now() >= deadline {
            break None;
        }
        thread::sleep(Duration::from_millis(5));
    };
    // Reuse the repository's fixed /bin/kill process-group pattern. Cleanup is
    // performed once even after a normal leader exit, before dropping capture.
    let cleanup = terminate_group(child.id());
    let status = match status {
        Some(status) => Ok(status),
        None => {
            let _ = child.kill();
            wait(&mut child)
        }
    };
    while captures.iter().any(Option::is_none) {
        match receive.recv_timeout(Duration::from_secs(1)) {
            Ok((index, result)) => captures[index] = Some(result),
            Err(_) => {
                capture_error = Some("PDF process group capture did not close".into());
                break;
            }
        }
    }
    let _ = stop_out.shutdown(Shutdown::Read);
    let _ = stop_err.shutdown(Shutdown::Read);
    for reader in readers {
        if reader.join().is_err() {
            capture_error = Some("PDF capture reader failed".into());
        }
    }
    cleanup?;
    let status = status?;
    if let Some(error) = capture_error {
        return Err(error);
    }
    let [stdout, stderr] = captures;
    let stdout = stdout.ok_or("PDF stdout capture missing")??;
    let stderr = stderr.ok_or("PDF stderr capture missing")??;
    if !status.success() {
        let message = String::from_utf8_lossy(&stderr);
        let fallback = status.code().map_or_else(
            || "signal: killed".into(),
            |code| format!("exit status {code}"),
        );
        let message = if message.trim().is_empty() {
            &fallback
        } else {
            message.trim()
        };
        return Err(format!("{label}: {message}"));
    }
    Ok(String::from_utf8_lossy(&stdout).into_owned())
}

#[cfg(unix)]
fn wait(child: &mut std::process::Child) -> Result<std::process::ExitStatus, String> {
    use std::{
        thread,
        time::{Duration, Instant},
    };
    let deadline = Instant::now() + Duration::from_secs(1);
    loop {
        if let Some(status) = child.try_wait().map_err(|e| e.to_string())? {
            return Ok(status);
        }
        if Instant::now() >= deadline {
            return Err("PDF process cleanup exceeded deadline".into());
        }
        thread::sleep(Duration::from_millis(5));
    }
}

#[cfg(unix)]
fn terminate_group(pid: u32) -> Result<(), String> {
    use std::process::{Command, Stdio};
    let signal = |argument: &str| -> Result<bool, String> {
        let mut child = Command::new("/bin/kill")
            .args([argument, "--", &format!("-{pid}")])
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .spawn()
            .map_err(|e| e.to_string())?;
        match wait(&mut child) {
            Ok(status) => Ok(status.success()),
            Err(error) => {
                let _ = child.kill();
                let _ = wait(&mut child);
                Err(error)
            }
        }
    };
    if signal("-KILL")? || !signal("-0")? {
        Ok(())
    } else {
        Err("PDF process group survived cleanup".into())
    }
}

#[cfg(not(unix))]
pub(crate) fn run(_ctx: &Context, _path: &Path, _args: &[String]) -> Result<String, String> {
    Err("PDF subprocess isolation is unsupported on this platform".into())
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::time::{Duration, Instant};

    #[test]
    fn output_limit_kills_active_writer() {
        let mut ctx = Context::default();
        ctx.deadline = Some(Instant::now() + Duration::from_secs(5));
        let error = run(&ctx, Path::new("/usr/bin/yes"), &[]).unwrap_err();
        assert_eq!(error, "PDF tool output exceeds 16 MiB");
    }
}
