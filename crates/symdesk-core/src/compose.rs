//! Runtime discovery of separately released siblings, never absorbed modules.
//!
//! Unix Go `internal/compose/resolve.go` contract. Environment and cwd are
//! captured explicitly, so callers/tests need not mutate process-global state.
//! Windows PATHEXT/volume semantics are deliberately not exposed as Unix parity.
use crate::config::quote_artifact;
use rustix::fs::{Access, AtFlags, CWD, accessat};
use std::{
    fs, io,
    os::unix::fs::PermissionsExt,
    path::{Component, Path, PathBuf},
};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum ResolveOrigin {
    SymairaBinEnv,
    ManagedRuntime,
    Path,
}
impl ResolveOrigin {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::SymairaBinEnv => "symaira_bin_env",
            Self::ManagedRuntime => "managed_runtime",
            Self::Path => "path",
        }
    }
}

/// Inputs to a single discovery. Empty PATH is distinct from a PATH containing
/// an empty component; only the latter implies the current directory.
#[derive(Clone, Debug)]
pub struct Resolver {
    pub home: String,
    pub symaira_bin: String,
    pub path: String,
    pub cwd: PathBuf,
    /// Go's explicit GODEBUG=execerrdot=0 compatibility escape hatch.
    pub allow_relative_path: bool,
}
impl Resolver {
    pub fn from_environment() -> io::Result<Self> {
        Ok(Self {
            home: std::env::var("HOME").unwrap_or_default(),
            symaira_bin: std::env::var("SYMAIRA_BIN").unwrap_or_default(),
            path: std::env::var("PATH").unwrap_or_default(),
            cwd: std::env::current_dir()?,
            allow_relative_path: std::env::var("GODEBUG")
                .unwrap_or_default()
                .split(',')
                .filter_map(|v| v.strip_prefix("execerrdot="))
                .next_back()
                == Some("0"),
        })
    }
    pub fn managed_runtime_dir(&self) -> String {
        if self.home.is_empty() {
            String::new()
        } else {
            clean_join(&self.home, ".symaira/bin")
        }
    }
    pub fn managed_runtime_dir_exists(&self) -> bool {
        let dir = self.managed_runtime_dir();
        !dir.is_empty() && self.absolute(&dir).is_dir()
    }
    /// Resolve without executing the discovered file. Managed tiers check mode
    /// bits (as Go does), PATH uses effective-access checks and rejects ErrDot.
    pub fn resolve_with_origin(&self, name: &str) -> Result<(String, ResolveOrigin), String> {
        if name.contains('/') {
            return self.look_path(name).map(|path| (path, ResolveOrigin::Path));
        }
        for (dir, origin) in [
            (&self.symaira_bin, ResolveOrigin::SymairaBinEnv),
            (&self.managed_runtime_dir(), ResolveOrigin::ManagedRuntime),
        ] {
            if !dir.is_empty() {
                let candidate = clean_join(dir, name);
                if fs::metadata(self.absolute(&candidate))
                    .is_ok_and(|m| !m.is_dir() && m.permissions().mode() & 0o111 != 0)
                {
                    return Ok((candidate, origin));
                }
            }
        }
        self.look_path(name)
            .map(|path| (path, ResolveOrigin::Path))
            .map_err(|error| {
                let dir = self.managed_runtime_dir();
                let dir = if dir.is_empty() {
                    "the managed runtime directory"
                } else {
                    &dir
                };
                format!("{name} not found via $SYMAIRA_BIN, {dir}, or PATH: {error}")
            })
    }
    pub fn resolve(&self, name: &str) -> Result<String, String> {
        self.resolve_with_origin(name).map(|(path, _)| path)
    }
    fn absolute(&self, path: &str) -> PathBuf {
        self.cwd.join(path)
    }
    fn find_executable(&self, path: &str) -> Result<(), String> {
        let absolute = self.absolute(path);
        let info = fs::metadata(&absolute)
            .map_err(|error| format!("stat {path}: {}", syscall_text(&error)))?;
        if info.is_dir() {
            return Err("is a directory".to_owned());
        }
        match accessat(CWD, &absolute, Access::EXEC_OK, AtFlags::EACCESS) {
            Ok(()) => Ok(()),
            Err(rustix::io::Errno::NOSYS | rustix::io::Errno::PERM) => {
                if info.permissions().mode() & 0o111 != 0 {
                    Ok(())
                } else {
                    Err("permission denied".to_owned())
                }
            }
            Err(error) => Err(syscall_text(&io::Error::from(error))),
        }
    }
    fn look_path(&self, name: &str) -> Result<String, String> {
        let failure = |message: &str| format!("exec: {}: {message}", quote_artifact(name));
        if matches!(name, "" | "." | "..") {
            return Err(failure("executable file not found in $PATH"));
        }
        if name.contains('/') {
            self.find_executable(name)
                .map_err(|error| failure(&error))?;
            return Ok(name.to_owned());
        }
        if !self.path.is_empty() {
            for dir in self.path.split(':') {
                let candidate = clean_join(if dir.is_empty() { "." } else { dir }, name);
                if self.find_executable(&candidate).is_ok() {
                    if !Path::new(&candidate).is_absolute() && !self.allow_relative_path {
                        return Err(failure(
                            "cannot run executable found relative to current directory",
                        ));
                    }
                    return Ok(candidate);
                }
            }
        }
        Err(failure("executable file not found in $PATH"))
    }
}

fn syscall_text(error: &io::Error) -> String {
    // Go lowercases syscall diagnostics and does not append Rust's errno suffix.
    let message = error.to_string();
    message
        .split(" (os error ")
        .next()
        .unwrap_or(&message)
        .to_lowercase()
}
fn clean_join(dir: &str, name: &str) -> String {
    let joined = format!("{dir}/{name}");
    let mut clean = PathBuf::new();
    for part in Path::new(&joined).components() {
        match part {
            Component::CurDir => (),
            Component::ParentDir => {
                if clean.file_name().is_some_and(|v| v != "..") {
                    clean.pop();
                } else if !clean.has_root() {
                    clean.push("..");
                }
            }
            _ => clean.push(part.as_os_str()),
        }
    }
    if clean.as_os_str().is_empty() {
        ".".to_owned()
    } else {
        clean.to_string_lossy().into_owned()
    }
}
