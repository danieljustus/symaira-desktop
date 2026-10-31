//! Process lifecycle shared by the long-running commands (`events`, `worker`,
//! `mcp`): Go's `signal.Notify(SIGINT, SIGTERM)` and the stdin-EOF watcher of
//! `symdesk events`, plus Go's SIGPIPE rule.
//!
//! Go ends the process with `SIGPIPE` only when a write to file descriptor 1
//! or 2 fails with `EPIPE`; an externally delivered `SIGPIPE` is ignored. Rust
//! ignores the signal at startup, so [`die_by_sigpipe`] restores the default
//! action and raises the signal on exactly that write failure.
//!
//! The handler is a plain `extern "C"` function that only stores an atomic and
//! writes one byte to a self-pipe (both async-signal-safe); waiters poll the
//! pipe. Handlers are reset to the default by `exec`, so spawned children do
//! not inherit them.
#![allow(unsafe_code)]

#[cfg(unix)]
use std::sync::atomic::{AtomicBool, AtomicI32, Ordering};
use std::{
    io::{self, Read},
    time::Duration,
};

#[cfg(unix)]
static SHUTDOWN: AtomicBool = AtomicBool::new(false);
#[cfg(unix)]
static WRITE_FD: AtomicI32 = AtomicI32::new(-1);
#[cfg(unix)]
static READ_FD: AtomicI32 = AtomicI32::new(-1);

#[cfg(unix)]
extern "C" fn on_signal(_signal: libc::c_int) {
    SHUTDOWN.store(true, Ordering::SeqCst);
    let fd = WRITE_FD.load(Ordering::SeqCst);
    if fd >= 0 {
        let byte = 1_u8;
        // SAFETY: `write` is async-signal-safe and `fd` is the write end of
        // the self-pipe created in `install_shutdown_handlers`, which is never
        // closed. A full pipe (EAGAIN) is fine: the flag is already set.
        unsafe {
            libc::write(fd, (&raw const byte).cast(), 1);
        }
    }
}

/// Installs Go's `signal.Notify(SIGINT, SIGTERM)` equivalent. Idempotent.
#[cfg(unix)]
pub fn install_shutdown_handlers() -> io::Result<()> {
    if READ_FD.load(Ordering::SeqCst) >= 0 {
        return Ok(());
    }
    let mut fds = [0 as libc::c_int; 2];
    // SAFETY: `fds` is a valid two-element array for `pipe` to fill.
    if unsafe { libc::pipe(fds.as_mut_ptr()) } != 0 {
        return Err(io::Error::last_os_error());
    }
    for fd in fds {
        // SAFETY: `fd` is a descriptor just returned by `pipe`.
        unsafe {
            let flags = libc::fcntl(fd, libc::F_GETFL);
            libc::fcntl(fd, libc::F_SETFL, flags | libc::O_NONBLOCK);
            libc::fcntl(fd, libc::F_SETFD, libc::FD_CLOEXEC);
        }
    }
    READ_FD.store(fds[0], Ordering::SeqCst);
    WRITE_FD.store(fds[1], Ordering::SeqCst);
    for signal in [libc::SIGINT, libc::SIGTERM] {
        // SAFETY: `on_signal` is an `extern "C"` function that only performs
        // async-signal-safe operations.
        let previous =
            unsafe { libc::signal(signal, on_signal as *const () as libc::sighandler_t) };
        if previous == libc::SIG_ERR {
            return Err(io::Error::last_os_error());
        }
    }
    Ok(())
}

/// Whether SIGINT or SIGTERM was delivered since [`install_shutdown_handlers`].
#[cfg(unix)]
pub fn shutdown_requested() -> bool {
    SHUTDOWN.load(Ordering::SeqCst)
}

/// Blocks up to `timeout` for a shutdown signal; returns whether one arrived.
#[cfg(unix)]
pub fn wait_for_shutdown(timeout: Duration) -> bool {
    if shutdown_requested() {
        return true;
    }
    let fd = READ_FD.load(Ordering::SeqCst);
    if fd < 0 {
        std::thread::sleep(timeout);
        return shutdown_requested();
    }
    let mut poll = libc::pollfd {
        fd,
        events: libc::POLLIN,
        revents: 0,
    };
    let millis = libc::c_int::try_from(timeout.as_millis()).unwrap_or(libc::c_int::MAX);
    // SAFETY: `poll` points at one valid `pollfd`; EINTR (a delivered signal)
    // simply ends the wait early and the flag is re-read below.
    unsafe {
        libc::poll(&raw mut poll, 1, millis);
    }
    shutdown_requested()
}

/// Terminates the process the way a failed write to descriptor 1 or 2 does in
/// Go: default `SIGPIPE` action, no output, signal exit status.
#[cfg(unix)]
pub fn die_by_sigpipe() -> ! {
    // SAFETY: resets the SIGPIPE disposition to the default and raises it; no
    // handler code or shared memory is involved.
    unsafe {
        libc::signal(libc::SIGPIPE, libc::SIG_DFL);
        libc::raise(libc::SIGPIPE);
    }
    // `raise` of a default-terminate signal does not return.
    std::process::abort()
}

/// A writer that ends the process by `SIGPIPE` when the reader is gone, like
/// Go's `os.Stdout`/`os.Stderr`; other errors pass through unchanged.
pub struct SigpipeWriter<W>(pub W);

impl<W: io::Write> io::Write for SigpipeWriter<W> {
    fn write(&mut self, buf: &[u8]) -> io::Result<usize> {
        self.0.write(buf).inspect_err(|error| {
            if error.kind() == io::ErrorKind::BrokenPipe {
                die_by_sigpipe();
            }
        })
    }

    fn flush(&mut self) -> io::Result<()> {
        self.0.flush().inspect_err(|error| {
            if error.kind() == io::ErrorKind::BrokenPipe {
                die_by_sigpipe();
            }
        })
    }
}

/// Go `os.Hostname()` (empty when unavailable, like Go's ignored error).
#[cfg(unix)]
pub fn hostname() -> String {
    let mut buffer = [0_u8; 256];
    // SAFETY: `buffer` is valid for `buffer.len()` bytes; the result is
    // NUL-terminated on success (the last byte is forced to NUL).
    let status = unsafe { libc::gethostname(buffer.as_mut_ptr().cast(), buffer.len() - 1) };
    if status != 0 {
        return String::new();
    }
    let end = buffer
        .iter()
        .position(|byte| *byte == 0)
        .unwrap_or(buffer.len());
    String::from_utf8_lossy(&buffer[..end]).into_owned()
}

/// Go's `bufio.Scanner` over stdin discarding every line: the returned flag
/// is set once stdin reaches EOF, fails, or a single line exceeds the
/// scanner's 64 KiB token limit.
pub fn watch_stdin_closed() -> std::sync::mpsc::Receiver<()> {
    const MAX_TOKEN: usize = 64 * 1024;
    let (sender, receiver) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let mut stdin = io::stdin().lock();
        let mut buffer = [0_u8; 8192];
        let mut line = 0_usize;
        loop {
            match stdin.read(&mut buffer) {
                Ok(0) | Err(_) => break,
                Ok(n) => {
                    for byte in &buffer[..n] {
                        if *byte == b'\n' {
                            line = 0;
                        } else {
                            line += 1;
                        }
                    }
                    if line >= MAX_TOKEN {
                        break;
                    }
                }
            }
        }
        let _ = sender.send(());
    });
    receiver
}

// Windows keeps the pre-existing behaviour (no signal handling, Rust's own
// broken-pipe errors); signal semantics are POSIX-only (CLI-007).
#[cfg(not(unix))]
pub fn install_shutdown_handlers() -> io::Result<()> {
    Ok(())
}

#[cfg(not(unix))]
pub fn shutdown_requested() -> bool {
    false
}

#[cfg(not(unix))]
pub fn wait_for_shutdown(timeout: Duration) -> bool {
    std::thread::sleep(timeout);
    false
}

#[cfg(not(unix))]
pub fn die_by_sigpipe() -> ! {
    std::process::exit(1)
}

#[cfg(not(unix))]
pub fn hostname() -> String {
    std::env::var("COMPUTERNAME").unwrap_or_default()
}
