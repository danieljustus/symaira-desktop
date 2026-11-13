//! Hermetic process environments still need Windows' operating-system root.
//! Go os/exec adds SYSTEMROOT when absent; Rust std::process does not. Without
//! it Winsock can fail to load its provider (WSAEPROVIDERFAILEDINIT / 10106).
use std::process::Command;

pub trait IsolatedEnvironment {
    fn isolated_env(&mut self) -> &mut Self;
}

impl IsolatedEnvironment for Command {
    fn isolated_env(&mut self) -> &mut Self {
        self.env_clear();
        // RUST-018: the automatic pre-Rust state backup is covered by its own
        // tests; every other hermetic process opts out so the Go-recorded
        // side-effect fixtures (exact file trees) stay unchanged.
        self.env("SYMDESK_PRE_RUST_BACKUP", "off");
        // A coverage run (`cargo llvm-cov`) starts instrumented binaries that
        // write a profile on exit. Without the variable it lands as
        // `default_*.profraw` in the working directory the test observes.
        // The child profile is discarded: forwarding the parent's pattern
        // keeps one file of several megabytes per child process (more than
        // 150 GB in one workspace run).
        if std::env::var_os("LLVM_PROFILE_FILE").is_some() {
            self.env("LLVM_PROFILE_FILE", "/dev/null");
        }
        #[cfg(windows)]
        self.env(
            "SYSTEMROOT",
            std::env::var_os("SYSTEMROOT").expect("Windows process requires SYSTEMROOT"),
        );
        self
    }
}
