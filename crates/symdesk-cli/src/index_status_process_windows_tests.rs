use super::super::{ProcessError, cleanup_failed_start, run_bounded};
use super::{CREATE_SUSPENDED_FLAG, WindowsJob, process_is_gone, terminate_test_process};
use std::os::windows::process::CommandExt;
use std::{
    fs,
    path::{Path, PathBuf},
    process::{Child, Command},
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

    fn path(&self) -> &Path {
        &self.path
    }

    fn own_pid_file(&mut self, path: PathBuf) {
        self.pid_files.push(path);
    }
}

impl Drop for TestRoot {
    fn drop(&mut self) {
        // Production cleanup should already have closed the job. Retain a
        // bounded fixture-only fallback so assertion failures cannot leak the
        // known descendant process.
        for path in &self.pid_files {
            if let Ok(pid) = fs::read_to_string(path)
                && let Ok(pid) = pid.trim().parse()
            {
                let _ = terminate_test_process(pid);
            }
        }
        let _ = fs::remove_dir_all(&self.path);
    }
}

struct ChildGuard(Child);

impl Drop for ChildGuard {
    fn drop(&mut self) {
        let _ = self.0.kill();
        let _ = super::super::wait_for_exit(&mut self.0, Duration::from_secs(1));
    }
}

fn powershell_worker(root: &TestRoot, keep_leader: bool) -> Command {
    let powershell = std::env::var_os("SystemRoot")
        .map(PathBuf::from)
        .map(|root| root.join("System32/WindowsPowerShell/v1.0/powershell.exe"))
        .unwrap_or_else(|| PathBuf::from("powershell.exe"));
    let mut command = Command::new(powershell);
    command
        .args([
            "-NoLogo",
            "-NoProfile",
            "-NonInteractive",
            "-Command",
            r#"$child = Start-Process -FilePath $env:ComSpec -ArgumentList '/c','echo descendant-ready & echo ready>"%CHILD_READY%" & timeout /t 300 /nobreak' -NoNewWindow -PassThru; [IO.File]::WriteAllText($env:PID_FILE,[string]$child.Id); $deadline = [DateTime]::UtcNow.AddSeconds(10); while (-not (Test-Path $env:CHILD_READY) -and [DateTime]::UtcNow -lt $deadline) { Start-Sleep -Milliseconds 10 }; if (-not (Test-Path $env:CHILD_READY)) { exit 42 }; [IO.File]::WriteAllText($env:READY_FILE,'ready'); if ($env:KEEP_LEADER -eq 'yes') { Start-Sleep -Seconds 30 }"#,
        ])
        .env("PID_FILE", root.path().join("child.pid"))
        .env("READY_FILE", root.path().join("child.ready"))
        .env("CHILD_READY", root.path().join("descendant.ready"))
        .env("KEEP_LEADER", if keep_leader { "yes" } else { "no" });
    command
}

fn assert_descendant_exited(root: &TestRoot, stdout: &[u8]) {
    let ready = root.path().join("child.ready");
    assert!(ready.exists(), "child readiness handshake was not observed");
    assert!(
        String::from_utf8_lossy(stdout).contains("descendant-ready"),
        "descendant's inherited stdout marker was not captured: {stdout:?}"
    );
    let pid_path = root.path().join("child.pid");
    let pid: u32 = fs::read_to_string(&pid_path)
        .expect("descendant PID file")
        .trim()
        .parse()
        .expect("valid descendant PID");
    assert!(
        process_is_gone(pid, Duration::from_secs(2)).expect("probe descendant process"),
        "descendant process {pid} remains live after worker cleanup"
    );
}

#[test]
fn normal_leader_exit_closes_job_and_kills_ready_descendant() {
    let mut root = TestRoot::create("normal-exit");
    let pid_path = root.path().join("child.pid");
    root.own_pid_file(pid_path);

    let output = run_bounded(
        powershell_worker(&root, false),
        Some(Duration::from_secs(10)),
    )
    .expect("worker should complete normally");
    assert!(output.status.success());
    assert_descendant_exited(&root, &output.stdout);
}

#[test]
fn timeout_terminates_job_and_kills_ready_descendant() {
    let mut root = TestRoot::create("timeout");
    let pid_path = root.path().join("child.pid");
    root.own_pid_file(pid_path);
    let started = Instant::now();

    let error = run_bounded(powershell_worker(&root, true), Some(Duration::from_secs(5)))
        .expect_err("leader must exceed the deadline");
    assert!(started.elapsed() < Duration::from_secs(8));
    let output = match error {
        ProcessError::TimedOut(output) => {
            assert!(
                output.status.code().is_some(),
                "timed-out leader was not reaped"
            );
            output
        }
        other => panic!("expected timeout, got {other:?}"),
    };
    assert_descendant_exited(&root, &output.stdout);
}

#[test]
fn spawn_failure_has_no_child_to_leave_behind() {
    let root = TestRoot::create("spawn-failure");
    let error = run_bounded(
        Command::new(root.path().join("missing-worker.exe")),
        Some(Duration::from_secs(1)),
    )
    .expect_err("missing executable must fail to spawn");
    assert!(matches!(error, ProcessError::Spawn(_)), "got {error:?}");
}

#[test]
fn assignment_failure_terminates_and_reaps_the_suspended_child() {
    let job = WindowsJob::create().expect("create Job Object");
    let mut command = Command::new("cmd.exe");
    command
        .args(["/c", "exit 0"])
        .creation_flags(CREATE_SUSPENDED_FLAG);
    let mut child = ChildGuard(command.spawn().expect("spawn suspended child"));
    let assignment_error = job
        .reject_test_assignment(&child.0)
        .expect_err("invalid Job Object handle must reject assignment");
    let assignment_error_kind = assignment_error.kind();

    let error = cleanup_failed_start(&job, &mut child.0, assignment_error);
    assert_eq!(error.kind(), assignment_error_kind, "{error}");
    assert!(
        child.0.try_wait().expect("query reaped child").is_some(),
        "assignment-failed child was not reaped"
    );
}

#[test]
fn completed_child_without_descendants_is_cleaned_idempotently() {
    let mut command = Command::new("cmd.exe");
    command.args(["/c", "exit 0"]);
    let output = run_bounded(command, Some(Duration::from_secs(5)))
        .expect("completed child should clean up normally");
    assert!(output.status.success());
}
