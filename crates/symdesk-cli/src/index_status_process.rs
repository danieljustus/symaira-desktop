use std::{
    fs::{self, File, OpenOptions},
    io::{self, Read, Seek},
    path::PathBuf,
    process::{Child, Command, ExitStatus, Stdio},
    sync::atomic::{AtomicU64, Ordering},
    thread,
    time::{Duration, Instant, SystemTime, UNIX_EPOCH},
};

#[cfg(windows)]
#[allow(unsafe_code)] // Narrow Win32 Job Object and thread-handle boundary.
#[path = "index_status_process_windows.rs"]
mod windows_tree;

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
    #[cfg(windows)]
    let job = windows_tree::WindowsJob::create().map_err(ProcessError::Spawn)?;
    command
        .stdin(Stdio::null())
        .stdout(Stdio::from(stdout_file))
        .stderr(Stdio::from(stderr_file));
    prepare_process_group(&mut command);

    let deadline = timeout.and_then(|duration| Instant::now().checked_add(duration));
    let mut child = command.spawn().map_err(ProcessError::Spawn)?;
    #[cfg(windows)]
    if let Err(error) = job.assign_and_resume(&child) {
        return Err(ProcessError::Spawn(cleanup_failed_start(
            &job, &mut child, error,
        )));
    }
    loop {
        match child.try_wait() {
            Ok(Some(status)) => {
                // The owned process tree still outlives the leader if any child
                // inherited stdio. Keep the platform ownership handle until
                // cleanup confirms that all descendants are gone.
                #[cfg(unix)]
                terminate_process_tree(child.id()).map_err(ProcessError::Wait)?;
                #[cfg(windows)]
                job.terminate_and_wait(Duration::from_secs(1))
                    .map_err(ProcessError::Wait)?;
                let output = read_output(status, &stdout_capture, &stderr_capture)?;
                return Ok(output);
            }
            Ok(None) => {}
            Err(error) => {
                #[cfg(unix)]
                let _ = terminate_process_tree(child.id());
                #[cfg(windows)]
                let _ = job.terminate_and_wait(Duration::from_secs(1));
                let _ = child.kill();
                let _ = wait_for_exit(&mut child, Duration::from_secs(1));
                return Err(ProcessError::Wait(error));
            }
        }

        if deadline.is_some_and(|at| Instant::now() >= at) {
            #[cfg(unix)]
            let cleanup = terminate_process_tree(child.id());
            #[cfg(windows)]
            let cleanup = job.terminate_and_wait(Duration::from_secs(1));
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
    // Keep the primary thread suspended until WindowsJob owns the process.
    command.creation_flags(CREATE_NEW_PROCESS_GROUP | windows_tree::CREATE_SUSPENDED_FLAG);
}

#[cfg(windows)]
fn cleanup_failed_start(
    job: &windows_tree::WindowsJob,
    child: &mut Child,
    cause: io::Error,
) -> io::Error {
    let job_cleanup = job.terminate_and_wait(Duration::from_secs(1));
    // Assignment may have failed, so also terminate the direct process handle.
    // It is still suspended and cannot have started descendants.
    let child_reap = terminate_child_and_reap(child, Duration::from_secs(1));
    match (job_cleanup, child_reap) {
        (Ok(()), Ok(_)) => cause,
        (job_result, child_result) => io::Error::other(format!(
            "worker start/assignment failed ({cause}); job cleanup: {}; child reap: {}",
            job_result
                .err()
                .map_or_else(|| "ok".to_owned(), |error| error.to_string()),
            child_result
                .err()
                .map_or_else(|| "ok".to_owned(), |error| error.to_string()),
        )),
    }
}

#[cfg(windows)]
fn terminate_child_and_reap(child: &mut Child, timeout: Duration) -> io::Result<ExitStatus> {
    let deadline = Instant::now() + timeout;
    let mut last_error = None;
    loop {
        match child.try_wait() {
            Ok(Some(status)) => return Ok(status),
            Ok(None) => {}
            Err(error) => last_error = Some(error),
        }
        if let Err(error) = child.kill() {
            last_error = Some(error);
        }
        let now = Instant::now();
        if now >= deadline {
            return Err(last_error.unwrap_or_else(|| {
                io::Error::new(
                    io::ErrorKind::TimedOut,
                    "could not terminate and reap suspended index-status worker",
                )
            }));
        }
        thread::sleep(Duration::from_millis(5).min(deadline.saturating_duration_since(now)));
    }
}

#[cfg(unix)]
fn terminate_process_tree(pid: u32) -> io::Result<()> {
    let target = format!("-{pid}");
    let mut kill = Command::new("/bin/kill");
    // procps kill can report success without signaling a negative PGID unless
    // options are explicitly terminated. The existence probe needs this too.
    kill.args(["-KILL", "--", &target]);
    if bounded_cleanup(&mut kill)?.success() {
        return Ok(());
    }
    // A normal worker exit commonly leaves no group. Distinguish that from
    // an unsuccessful cleanup while the owned group is still present.
    let mut probe = Command::new("/bin/kill");
    probe.args(["-0", "--", &target]);
    if bounded_cleanup(&mut probe)?.success() {
        return Err(io::Error::other(
            "index status process group survived cleanup",
        ));
    }
    Ok(())
}

#[cfg(unix)]
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
        path::PathBuf,
        process::{Command, Stdio},
        thread,
        time::{Duration, Instant, SystemTime, UNIX_EPOCH},
    };

    struct TestRoot {
        path: PathBuf,
        pid_files: Vec<PathBuf>,
    }

    impl TestRoot {
        fn create(label: &str) -> Self {
            let nonce = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .expect("clock")
                .as_nanos();
            let path = std::env::temp_dir().join(format!(
                "symdesk-index-status-{label}-{}-{nonce}",
                std::process::id()
            ));
            fs::create_dir(&path).expect("create test root");
            Self {
                path,
                pid_files: Vec::new(),
            }
        }

        fn path(&self) -> &std::path::Path {
            &self.path
        }

        fn own_pid_file(&mut self, path: PathBuf) {
            self.pid_files.push(path);
        }
    }

    impl Drop for TestRoot {
        fn drop(&mut self) {
            // Test cleanup is independent of the assertions, so a failed
            // descendant-death check cannot strand the fixture process.
            for path in &self.pid_files {
                if let Ok(pid) = fs::read_to_string(path) {
                    let _ = Command::new("/bin/kill")
                        .args(["-KILL", pid.trim()])
                        .stdout(Stdio::null())
                        .stderr(Stdio::null())
                        .status();
                }
            }
            let _ = fs::remove_dir_all(&self.path);
        }
    }

    fn process_is_live(pid: &str) -> bool {
        if !Command::new("/bin/kill")
            .args(["-0", pid])
            .stderr(Stdio::null())
            .status()
            .expect("probe descendant PID")
            .success()
        {
            return false;
        }

        #[cfg(target_os = "linux")]
        {
            let stat = fs::read_to_string(format!("/proc/{pid}/stat"));
            if let Ok(stat) = stat
                && let Some((_, fields)) = stat.rsplit_once(')')
                && fields.split_whitespace().next() == Some("Z")
            {
                return false;
            }
        }
        #[cfg(target_os = "macos")]
        {
            let output = Command::new("/bin/ps")
                .args(["-o", "stat=", "-p", pid])
                .output()
                .expect("read descendant process state");
            let state = String::from_utf8_lossy(&output.stdout);
            if output.status.success()
                && (state.trim().is_empty() || state.trim_start().starts_with('Z'))
            {
                return false;
            }
        }
        true
    }

    fn wait_until_process_gone(pid: &str, timeout: Duration) -> bool {
        let deadline = Instant::now() + timeout;
        loop {
            if !process_is_live(pid) {
                return true;
            }
            if Instant::now() >= deadline {
                return false;
            }
            thread::sleep(Duration::from_millis(10));
        }
    }

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
    fn already_gone_leader_is_a_successful_cleanup_case() {
        let output = run_bounded(Command::new("/usr/bin/true"), Some(Duration::from_secs(2)))
            .expect("leader exits before cleanup");
        assert!(output.status.success());
        assert!(output.stdout.is_empty());
        assert!(output.stderr.is_empty());
    }

    #[test]
    fn launch_failure_is_reported_without_a_child_to_reap() {
        let root = TestRoot::create("spawn-failure");
        let command = Command::new(root.path().join("missing-worker"));
        let error = run_bounded(command, Some(Duration::from_secs(1)))
            .expect_err("missing executable must fail to spawn");
        assert!(matches!(error, ProcessError::Spawn(_)), "got {error:?}");
    }

    #[test]
    fn successful_worker_preserves_stdout_and_stderr_bytes() {
        let mut command = Command::new("/bin/sh");
        command
            .arg("-c")
            .arg(r"printf '\377\000'; printf '\376\n' >&2");
        let output =
            run_bounded(command, Some(Duration::from_secs(2))).expect("worker exits successfully");
        assert!(output.status.success());
        assert_eq!(output.stdout, [0xff, 0x00]);
        assert_eq!(output.stderr, [0xfe, b'\n']);
    }

    #[test]
    fn exited_leader_does_not_leave_live_descendants() {
        let mut root = TestRoot::create("exited-leader");
        let pid_path = root.path().join("pid");
        let ready = root.path().join("ready");
        root.own_pid_file(pid_path.clone());
        let mut command = Command::new("/bin/sh");
        command.args(["-c", "/bin/sh -c 'printf ready > \"$READY\"; exec /bin/sleep 60' & printf '%s' \"$!\" > \"$PIDFILE\"; while [ ! -f \"$READY\" ]; do /bin/sleep 0.01; done; exit 0"])
            .env("READY", &ready).env("PIDFILE", &pid_path);
        let output =
            run_bounded(command, Some(Duration::from_secs(2))).expect("worker exits successfully");
        assert!(output.status.success());
        let pid = fs::read_to_string(&pid_path).expect("descendant readiness handshake");
        let survived = !wait_until_process_gone(&pid, Duration::from_secs(1));
        assert!(!survived, "descendant {pid} survived its leader");
    }

    #[test]
    fn deadline_kills_blocking_child_tree_without_waiting_on_inherited_stdio() {
        let mut root = TestRoot::create("timeout");
        let ready_path = root.path().join("descendant.ready");
        let pid_path = root.path().join("descendant.pid");
        root.own_pid_file(pid_path.clone());
        let script = "/bin/sh -c 'trap \"\" TERM; printf ready > \"$READY_PATH\"; printf descendant-ready; exec /bin/sleep 60' & child=$!; printf '%s' \"$child\" > \"$PID_PATH\"; while [ ! -s \"$READY_PATH\" ]; do /bin/sleep 0.01; done; printf parent-ready; wait \"$child\"";
        let mut command = Command::new("/bin/sh");
        command
            .arg("-c")
            .arg(script)
            .env("READY_PATH", &ready_path)
            .env("PID_PATH", &pid_path);

        let started = Instant::now();
        let error = run_bounded(command, Some(Duration::from_millis(1_000)))
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
        let gone = wait_until_process_gone(&pid.to_string(), Duration::from_secs(1));
        assert!(
            gone,
            "descendant process {pid} survived process-group cleanup"
        );
    }
}
