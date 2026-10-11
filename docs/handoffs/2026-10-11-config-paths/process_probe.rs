use std::{
    io::Read,
    process::{Command, ExitStatus, Stdio},
    sync::mpsc::{self, RecvTimeoutError},
    thread,
    time::{Duration, Instant},
};

fn run_process(program: &str, arguments: &[&str], timeout: Duration) -> Option<Vec<u8>> {
    let deadline = Instant::now() + timeout;
    let mut child = match Command::new(program)
        .args(arguments)
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
    {
        Ok(child) => child,
        Err(error) => {
            eprintln!("stage=spawn kind={:?} errno={:?} error={error}", error.kind(), error.raw_os_error());
            return None;
        }
    };
    let Some(mut stdout) = child.stdout.take() else {
        eprintln!("stage=stdout-take missing-pipe");
        return None;
    };
    let (sender, receiver) = mpsc::channel();
    let reader = thread::Builder::new()
        .name("symdesk-secret-output".to_owned())
        .spawn(move || {
            let mut output = Vec::new();
            let result = match stdout.read_to_end(&mut output) {
                Ok(_) => Some(output),
                Err(error) => {
                    eprintln!("stage=read kind={:?} errno={:?} error={error}", error.kind(), error.raw_os_error());
                    None
                }
            };
            let _ = sender.send(result);
        });
    let Ok(_reader) = reader else {
        eprintln!("stage=reader-thread-spawn failure");
        let _ = child.kill();
        let _ = child.wait();
        return None;
    };

    let status: Option<ExitStatus> = loop {
        match child.try_wait() {
            Ok(Some(status)) => break Some(status),
            Ok(None) if Instant::now() < deadline => thread::sleep(Duration::from_millis(10)),
            Ok(None) => {
                eprintln!("stage=child-deadline");
                let _ = child.kill();
                let _ = child.wait();
                break None;
            }
            Err(error) => {
                eprintln!("stage=try-wait kind={:?} errno={:?} error={error}", error.kind(), error.raw_os_error());
                let _ = child.kill();
                let _ = child.wait();
                break None;
            }
        }
    };
    if !status.is_some_and(|status| status.success()) {
        if let Some(status) = status {
            eprintln!("stage=child-status success=false code={:?}", status.code());
        }
        return None;
    }
    let remaining = deadline.saturating_duration_since(Instant::now());
    match receiver.recv_timeout(remaining) {
        Ok(Some(output)) => Some(output),
        Ok(None) => {
            eprintln!("stage=reader-result none");
            None
        }
        Err(RecvTimeoutError::Timeout) => {
            eprintln!("stage=reader-deadline");
            None
        }
        Err(RecvTimeoutError::Disconnected) => {
            eprintln!("stage=reader-channel-disconnected");
            None
        }
    }
}

fn main() {
    let timeout = Duration::from_secs(2);
    let output = run_process(
        "/bin/sh",
        &["-c", "printf 'stderr-secret' >&2; printf 'stdout-key\\n'"],
        timeout,
    );
    assert_eq!(output, Some(b"stdout-key\n".to_vec()));
    println!("case=stdout-plus-discarded-stderr result=Some(stdout-key\\n)");

    let output = run_process("/bin/sh", &["-c", "printf 'ignored\\n'; exit 7"], timeout);
    assert_eq!(output, None);
    println!("case=nonzero-shell-status result=None");

    let output = run_process(concat!("/symdesk", "-nonexistent-probe-command"), &[], timeout);
    assert_eq!(output, None);
    println!("case=spawn-ENOENT result=None");

    let output = run_process(
        "/bin/sh",
        &["-c", "(sleep 1) & exit 0"],
        Duration::from_millis(50),
    );
    assert_eq!(output, None);
    println!("case=descendant-holds-stdout-pipe result=None");
}
