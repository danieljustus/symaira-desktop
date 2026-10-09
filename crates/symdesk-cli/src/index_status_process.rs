use std::{
    fs::{self, File, OpenOptions},
    io::{self, Read, Seek},
    path::PathBuf,
    process::{Child, Command, ExitStatus, Stdio},
    sync::atomic::{AtomicU64, Ordering},
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

static CAPTURE_NONCE: AtomicU64 = AtomicU64::new(0);

#[derive(Debug)]
pub struct ProcessOutput {
    pub status: ExitStatus,
    pub stdout: Vec<u8>,
    pub stderr: Vec<u8>,
}

#[derive(Debug)]
pub enum ProcessError {
    Spawn(io::Error),
    Wait(io::Error),
    Read(io::Error),
    TimedOut(ProcessOutput),
}

struct CaptureFile {
    path: PathBuf,
    file: File,
}

impl CaptureFile {
    fn create(label: &str) -> io::Result<(Self, File)> {
        let root = std::env::temp_dir();
        for _ in 0..32 {
            let clock = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .unwrap_or_default()
                .as_nanos();
            let nonce = CAPTURE_NONCE.fetch_add(1, Ordering::Relaxed);
            let path = root.join(format!(
                "symdesk-index-status-{}-{clock}-{nonce}-{label}.tmp",
                std::process::id()
            ));
            let mut options = OpenOptions::new();
            options.read(true).write(true).create_new(true);
            #[cfg(unix)]
            {
                use std::os::unix::fs::OpenOptionsExt;
                options.mode(0o600);
            }
            match options.open(&path) {
                Ok(file) => {
                    let capture = Self { path, file };
                    let writer = capture.file.try_clone()?;
                    return Ok((capture, writer));
                }
                Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
                Err(error) => return Err(error),
            }
        }
        Err(io::Error::new(
            io::ErrorKind::AlreadyExists,
            "could not allocate unique index-status capture files",
        ))
    }

    fn read(&self) -> io::Result<Vec<u8>> {
        // Read the owned file handle, not a pathname that can be replaced.
        let mut reader = &self.file;
        reader.rewind()?;
        let mut bytes = Vec::new();
        reader.read_to_end(&mut bytes)?;
        Ok(bytes)
    }
}

impl Drop for CaptureFile {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.path);
    }
}

/// Runs a child with file-backed output and an optional wall-clock deadline.
/// File-backed output prevents an inherited pipe descriptor from blocking the
/// parent after the direct child exits or is terminated.
pub fn run_bounded(
    mut command: Command,
    timeout: Option<Duration>,
) -> Result<ProcessOutput, ProcessError> {
    let (stdout_capture, stdout_file) =
        CaptureFile::create("stdout").map_err(ProcessError::Spawn)?;
    let (stderr_capture, stderr_file) =
        CaptureFile::create("stderr").map_err(ProcessError::Spawn)?;
    command
        .stdin(Stdio::null())
        .stdout(Stdio::from(stdout_file))
        .stderr(Stdio::from(stderr_file));
    prepare_process_group(&mut command);

    let deadline = timeout.and_then(|duration| Instant::now().checked_add(duration));
    let mut child = command.spawn().map_err(ProcessError::Spawn)?;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                // The process group still belongs to this invocation even if
                // its leader has exited. Inherited output is not reaping proof.
                #[cfg(unix)]
                terminate_process_tree(child.id()).map_err(ProcessError::Wait)?;
                let output = read_output(status, &stdout_capture, &stderr_capture)?;
                return Ok(output);
            }
            Ok(None) => {}
            Err(error) => {
                let _ = terminate_process_tree(child.id());
                let _ = child.kill();
                let _ = wait_for_exit(&mut child, Duration::from_secs(1));
                return Err(ProcessError::Wait(error));
            }
        }

        if deadline.is_some_and(|at| Instant::now() >= at) {
            let cleanup = terminate_process_tree(child.id());
            let _ = child.kill();
            let status =
                wait_for_exit(&mut child, Duration::from_secs(1)).map_err(ProcessError::Wait)?;
            cleanup.map_err(ProcessError::Wait)?;
            let output = read_output(status, &stdout_capture, &stderr_capture)?;
            return Err(ProcessError::TimedOut(output));
        }
        let delay = deadline.map_or(Duration::from_millis(10), |at| {
            at.saturating_duration_since(Instant::now())
                .min(Duration::from_millis(10))
        });
        if !delay.is_zero() {
            thread::sleep(delay);
        }
    }
}

fn read_output(
    status: ExitStatus,
    stdout_capture: &CaptureFile,
    stderr_capture: &CaptureFile,
) -> Result<ProcessOutput, ProcessError> {
    Ok(ProcessOutput {
        status,
        stdout: stdout_capture.read().map_err(ProcessError::Read)?,
        stderr: stderr_capture.read().map_err(ProcessError::Read)?,
    })
}

#[cfg(unix)]
fn prepare_process_group(command: &mut Command) {
    use std::os::unix::process::CommandExt;
    command.process_group(0);
}

#[cfg(windows)]
fn prepare_process_group(command: &mut Command) {
    use std::os::windows::process::CommandExt;
    const CREATE_NEW_PROCESS_GROUP: u32 = 0x0000_0200;
    command.creation_flags(CREATE_NEW_PROCESS_GROUP);
}

#[cfg(unix)]
fn terminate_process_tree(pid: u32) -> io::Result<()> {
    let target = format!("-{pid}");
    let mut kill = Command::new("/bin/kill");
    kill.args(["-KILL", &target]);
    if bounded_cleanup(&mut kill)?.success() {
        return Ok(());
    }
    // A normal worker exit commonly leaves no group. Distinguish that from
    // an unsuccessful cleanup while the owned group is still present.
    let mut probe = Command::new("/bin/kill");
    probe.args(["-0", &target]);
    if bounded_cleanup(&mut probe)?.success() {
        return Err(io::Error::other(
            "index status process group survived cleanup",
        ));
    }
    Ok(())
}

#[cfg(windows)]
fn terminate_process_tree(pid: u32) -> io::Result<()> {
    let pid = pid.to_string();
    let mut command = Command::new("taskkill");
    command.arg("/PID").arg(pid).arg("/T").arg("/F");
    if bounded_cleanup(&mut command)?.success() {
        Ok(())
    } else {
        Err(io::Error::other("index status process tree cleanup failed"))
    }
}

fn bounded_cleanup(command: &mut Command) -> io::Result<ExitStatus> {
    let mut helper = command
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()?;
    let result = wait_for_exit(&mut helper, Duration::from_millis(250));
    if result.is_err() {
        let _ = helper.kill();
        let _ = wait_for_exit(&mut helper, Duration::from_millis(250));
    }
    result
}

fn wait_for_exit(child: &mut Child, timeout: Duration) -> io::Result<ExitStatus> {
    let deadline = Instant::now() + timeout;
    loop {
        if let Some(status) = child.try_wait()? {
            return Ok(status);
        }
        if Instant::now() >= deadline {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "process cleanup exceeded deadline",
            ));
        }
        thread::sleep(Duration::from_millis(5));
    }
}

#[cfg(not(any(unix, windows)))]
fn prepare_process_group(_command: &mut Command) {}

#[cfg(not(any(unix, windows)))]
fn terminate_process_tree(_pid: u32) -> io::Result<()> {
    Err(io::Error::new(
        io::ErrorKind::Unsupported,
        "process tree cleanup is unsupported",
    ))
}

#[cfg(all(test, unix))]
mod tests {
    use super::{ProcessError, bounded_cleanup, run_bounded};
    use std::{
        fs,
        process::{Command, Stdio},
        thread,
        time::{Duration, Instant, SystemTime, UNIX_EPOCH},
    };

    #[test]
    fn cleanup_helper_cannot_extend_deadline_indefinitely() {
        let mut command = Command::new("/bin/sleep");
        command.arg("60");
        let started = Instant::now();
        let error = bounded_cleanup(&mut command).expect_err("cleanup helper must be bounded");
        assert_eq!(error.kind(), std::io::ErrorKind::TimedOut);
        assert!(started.elapsed() < Duration::from_secs(2));
    }

    #[test]
    fn exited_leader_does_not_leave_live_descendants() {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "status-exited-leader-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&root).unwrap();
        let pid_path = root.join("pid");
        let ready = root.join("ready");
        let mut command = Command::new("/bin/sh");
        command.args(["-c", "/bin/sh -c 'printf ready > \"$READY\"; exec /bin/sleep 60' & printf '%s' \"$!\" > \"$PIDFILE\"; while [ ! -f \"$READY\" ]; do /bin/sleep 0.01; done; exit 0"])
            .env("READY", &ready).env("PIDFILE", &pid_path);
        let result = run_bounded(command, Some(Duration::from_secs(2)));
        let pid = fs::read_to_string(&pid_path).expect("descendant handshake");
        let deadline = Instant::now() + Duration::from_secs(1);
        let survived = loop {
            let alive = Command::new("/bin/kill")
                .args(["-0", &pid])
                .stderr(Stdio::null())
                .status()
                .unwrap()
                .success();
            if !alive || Instant::now() >= deadline {
                break alive;
            }
            thread::sleep(Duration::from_millis(10));
        };
        // Release the fixture process even when this regression fails.
        if survived {
            let _ = Command::new("/bin/kill").args(["-KILL", &pid]).status();
        }
        fs::remove_dir_all(root).unwrap();
        assert!(result.expect("worker result").status.success());
        assert!(!survived, "descendant {pid} survived its leader");
    }

    #[test]
    fn deadline_kills_blocking_child_tree_without_waiting_on_inherited_stdio() {
        let nonce = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let root = std::env::temp_dir().join(format!(
            "symdesk-index-status-process-test-{}-{nonce}",
            std::process::id()
        ));
        fs::create_dir(&root).expect("create test root");
        let ready_path = root.join("descendant.ready");
        let pid_path = root.join("descendant.pid");
        let script = "/bin/sh -c 'trap \"\" TERM; printf ready > \"$READY_PATH\"; printf descendant-ready; exec /bin/sleep 60' & child=$!; printf '%s' \"$child\" > \"$PID_PATH\"; while [ ! -s \"$READY_PATH\" ]; do /bin/sleep 0.01; done; printf parent-ready; wait \"$child\"";
        let mut command = Command::new("/bin/sh");
        command
            .arg("-c")
            .arg(script)
            .env("READY_PATH", &ready_path)
            .env("PID_PATH", &pid_path);

        let started = Instant::now();
        let error = run_bounded(command, Some(Duration::from_millis(150)))
            .expect_err("blocking process tree must be bounded");
        let elapsed = started.elapsed();
        assert!(elapsed < Duration::from_secs(2), "elapsed {elapsed:?}");
        let output = match error {
            ProcessError::TimedOut(output) => output,
            other => panic!("expected timeout, got {other:?}"),
        };
        assert!(
            ready_path.exists(),
            "descendant readiness handshake completed"
        );
        let stdout = String::from_utf8_lossy(&output.stdout);
        assert!(stdout.contains("descendant-ready"), "stdout: {stdout:?}");
        assert!(stdout.contains("parent-ready"), "stdout: {stdout:?}");
        let pid = fs::read_to_string(&pid_path)
            .expect("read descendant PID")
            .parse::<u32>()
            .expect("valid descendant PID");
        let mut gone = false;
        for _ in 0..40 {
            let status = Command::new("/bin/kill")
                .arg("-0")
                .arg(pid.to_string())
                .status()
                .expect("probe descendant PID");
            if !status.success() {
                gone = true;
                break;
            }
            thread::sleep(Duration::from_millis(25));
        }
        assert!(
            gone,
            "descendant process {pid} survived process-group cleanup"
        );
        fs::remove_dir_all(root).expect("remove test root");
    }
}
