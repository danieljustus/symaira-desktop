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
        #[cfg(windows)]
        self.env(
            "SYSTEMROOT",
            std::env::var_os("SYSTEMROOT").expect("Windows process requires SYSTEMROOT"),
        );
        self
    }
}
