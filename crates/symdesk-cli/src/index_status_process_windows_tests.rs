use super::super::{ProcessError, cleanup_failed_start, run_bounded};
use super::{CREATE_SUSPENDED_FLAG, WindowsJob, process_is_gone, terminate_test_process};
use std::os::windows::process::CommandExt;
use std::{
    fs,
    io::Write,
    path::{Path, PathBuf},
    process::{Child, Command},
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

fn fixture_command() -> Command {
    let (_, module) = module_path!().split_once("::").expect("test module path");
    let mut command = Command::new(std::env::current_exe().expect("current test executable"));
    command.args([
        "--exact",
        &format!("{module}::fixture_worker_process"),
        "--nocapture",
    ]);
    command
}

fn native_worker(root: &TestRoot, keep_leader: bool) -> Command {
    let mut command = fixture_command();
    command
        .env("SYMDESK_INDEX_STATUS_FIXTURE_ROLE", "leader")
        .env("PID_FILE", root.path().join("child.pid"))
        .env("READY_FILE", root.path().join("child.ready"))
        .env("CHILD_READY", root.path().join("descendant.ready"))
        .env("KEEP_LEADER", if keep_leader { "yes" } else { "no" });
    command
}

#[test]
fn fixture_worker_process() {
    let Ok(role) = std::env::var("SYMDESK_INDEX_STATUS_FIXTURE_ROLE") else {
        return;
    };
    let ready = PathBuf::from(std::env::var_os("CHILD_READY").expect("child ready path"));
    if role == "descendant" {
        let mut stdout = std::io::stdout();
        stdout.write_all(b"descendant-ready").expect("write marker");
        stdout.flush().expect("flush marker before readiness");
        fs::write(ready, "ready").expect("signal descendant readiness");
        thread::sleep(Duration::from_secs(300));
        return;
    }
    assert_eq!(role, "leader");
    let mut child = ChildGuard(
        fixture_command()
            .env("SYMDESK_INDEX_STATUS_FIXTURE_ROLE", "descendant")
            .spawn()
            .expect("spawn native descendant"),
    );
    fs::write(
        std::env::var_os("PID_FILE").expect("PID path"),
        child.0.id().to_string(),
    )
    .expect("write descendant PID");
    let deadline = Instant::now() + Duration::from_secs(10);
    while !ready.is_file() {
        assert!(child.0.try_wait().expect("probe descendant").is_none());
        assert!(Instant::now() < deadline, "descendant did not become ready");
        thread::sleep(Duration::from_millis(10));
    }
    assert!(
        child
            .0
            .try_wait()
            .expect("probe ready descendant")
            .is_none()
    );
    fs::write(
        std::env::var_os("READY_FILE").expect("leader ready path"),
        "ready",
    )
    .expect("signal leader readiness");
    if std::env::var("KEEP_LEADER").as_deref() == Ok("yes") {
        thread::sleep(Duration::from_secs(30));
    }
    // Exit this fixture process without running ChildGuard: only the owned
    // Job Object (or the negative-control test's fallback) may kill the child.
    std::process::exit(0);
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

    let output = run_bounded(native_worker(&root, false), Some(Duration::from_secs(10)))
        .expect("worker should complete normally");
    assert!(output.status.success());
    assert_descendant_exited(&root, &output.stdout);
}

#[test]
fn fixture_descendant_survives_without_job_cleanup() {
    // Prove the native fixture stays alive without Job cleanup; interpreter
    // startup and redirected-stdin behavior must not decide this test.
    let mut root = TestRoot::create("negative-control");
    let pid_path = root.path().join("child.pid");
    root.own_pid_file(pid_path.clone());
    let mut child = ChildGuard(native_worker(&root, false).spawn().expect("spawn fixture"));
    assert!(
        super::super::wait_for_exit(&mut child.0, Duration::from_secs(10))
            .expect("fixture leader exits")
            .success()
    );
    assert!(root.path().join("child.ready").is_file());
    let pid = fs::read_to_string(pid_path)
        .unwrap()
        .trim()
        .parse()
        .unwrap();
    assert!(!process_is_gone(pid, Duration::from_millis(100)).expect("live descendant"));
}

#[test]
fn timeout_terminates_job_and_kills_ready_descendant() {
    let mut root = TestRoot::create("timeout");
    let pid_path = root.path().join("child.pid");
    root.own_pid_file(pid_path);
    let started = Instant::now();

    let error = run_bounded(native_worker(&root, true), Some(Duration::from_secs(5)))
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
