#![deny(unsafe_code)]

//! Go `os/exec.LookPath` (go1.26 `lp_unix.go`) for the
//! provider executables `identity::load` shells out to.
//!
//! `std::process::Command::new("symvault")` resolves the name with the child's
//! `PATH` as is, so a relative or empty `PATH` entry would run `./symvault`
//! from the working directory. Go refuses: a hit found through a relative path
//! is reported as `exec.ErrDot` ("cannot run executable found relative to
//! current directory") and `identity.Load` treats any `LookPath` or
//! `Cmd.Output` error as "provider unavailable" without printing it, so the
//! chain silently falls through. [`look_path`] mirrors that: it returns a path
//! only where Go returns one *without* an error, and the caller spawns that
//! absolute path.
//! `GODEBUG=execerrdot=0` is deliberately not honored: relative execution is
//! never enabled by an environment override.

use std::{
    env,
    ffi::OsStr,
    fs,
    path::{Path, PathBuf},
};

/// Resolve `file` (a bare command name) like Go's `exec.LookPath`, returning
/// `None` for both "not found" and the `ErrDot` rejection, which `identity.Load`
/// handles identically.
pub fn look_path(file: &str) -> Option<PathBuf> {
    look_path_in(file, &env::var_os("PATH").unwrap_or_default())
}

/// Resolve against an explicitly supplied PATH.
pub fn look_path_in(file: &str, path: &OsStr) -> Option<PathBuf> {
    if matches!(file, "" | "." | "..") {
        return None;
    }
    look_path_unix(Path::new("."), file, path)
}

/// A candidate as the filesystem sees it: relative paths resolve against `cwd`
/// (the process working directory in production, a scratch directory in tests).
fn at(cwd: &Path, candidate: &Path) -> PathBuf {
    if candidate.is_absolute() {
        candidate.to_path_buf()
    } else {
        cwd.join(candidate)
    }
}

/// Go `filepath.SplitList`: an empty list is no entries, not one empty entry.
fn split_list(path: &OsStr) -> Vec<PathBuf> {
    if path.is_empty() {
        return Vec::new();
    }
    env::split_paths(path).collect()
}

/// Go `lp_unix.go` `lookPath` for a name without a separator.
///
/// The first PATH entry that holds an executable decides; a relative hit
/// (including the empty entry, which means ".") is rejected rather than
/// skipped, so it shadows a later absolute entry exactly like Go.
///
/// Go checks `X_OK` with `faccessat`; without `unsafe` the executable bit of
/// the resolved file stands in for it, which only differs for a file that is
/// executable for another user than the current one.
fn look_path_unix(cwd: &Path, file: &str, path: &OsStr) -> Option<PathBuf> {
    if file.contains('/') {
        return None;
    }
    for dir in split_list(path) {
        let dir = if dir.as_os_str().is_empty() {
            PathBuf::from(".")
        } else {
            dir
        };
        let candidate = dir.join(file);
        if is_unix_executable(&at(cwd, &candidate)) {
            return candidate.is_absolute().then_some(candidate);
        }
    }
    None
}

fn is_unix_executable(path: &Path) -> bool {
    let Ok(metadata) = fs::metadata(path) else {
        return false;
    };
    if metadata.is_dir() {
        return false;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        metadata.permissions().mode() & 0o111 != 0
    }
    #[cfg(not(unix))]
    {
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::ffi::OsString;
    use std::sync::atomic::{AtomicU64, Ordering};

    static COUNTER: AtomicU64 = AtomicU64::new(0);

    /// A scratch tree; its `cwd` directory plays the process working directory.
    struct Scratch {
        root: PathBuf,
        cwd: PathBuf,
    }

    impl Scratch {
        fn new() -> Self {
            let path = env::temp_dir().join(format!(
                "symroom-lookpath-{}-{}",
                std::process::id(),
                COUNTER.fetch_add(1, Ordering::Relaxed)
            ));
            fs::create_dir_all(path.join("cwd")).expect("scratch dir");
            // Canonical, so absolute PATH entries stay absolute on every host.
            let root = fs::canonicalize(&path).expect("canonical scratch dir");
            Self {
                cwd: root.join("cwd"),
                root,
            }
        }

        fn file(&self, relative: &str, mode: u32) -> PathBuf {
            let path = self.root.join(relative);
            fs::create_dir_all(path.parent().expect("parent")).expect("parent dir");
            fs::write(&path, b"#!/bin/sh\n").expect("write file");
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                fs::set_permissions(&path, fs::Permissions::from_mode(mode)).expect("mode");
            }
            let _ = mode;
            path
        }

        fn abs(&self, relative: &str) -> PathBuf {
            self.root.join(relative)
        }
    }

    impl Drop for Scratch {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.root);
        }
    }

    fn join(entries: &[&Path]) -> OsString {
        env::join_paths(entries).expect("join PATH")
    }

    #[cfg(unix)]
    #[test]
    fn unix_returns_the_first_absolute_executable() {
        let scratch = Scratch::new();
        let first = scratch.file("a/symvault", 0o755);
        scratch.file("b/symvault", 0o755);
        let path = join(&[&scratch.abs("a"), &scratch.abs("b")]);
        assert_eq!(look_path_unix(&scratch.cwd, "symvault", &path), Some(first));
    }

    #[cfg(unix)]
    #[test]
    fn unix_rejects_a_relative_hit_instead_of_skipping_it() {
        // Go: ErrDot is returned at the first executable, even though a later
        // absolute entry holds a perfectly good one.
        let scratch = Scratch::new();
        scratch.file("cwd/symvault", 0o755);
        scratch.file("cwd/bin/symvault", 0o755);
        scratch.file("abs/symvault", 0o755);
        for relative in [".", "bin"] {
            let path = join(&[Path::new(relative), &scratch.abs("abs")]);
            assert_eq!(
                look_path_unix(&scratch.cwd, "symvault", &path),
                None,
                "relative entry {relative:?}"
            );
        }
        // The empty entry means "." — leading or trailing.
        for text in [
            format!(":{}", scratch.abs("abs").display()),
            format!("{}:", scratch.abs("abs").display()),
        ] {
            let outcome = look_path_unix(&scratch.cwd, "symvault", OsStr::new(&text));
            if text.starts_with(':') {
                assert_eq!(outcome, None, "leading empty entry");
            } else {
                // The absolute entry comes first and wins.
                assert_eq!(outcome, Some(scratch.abs("abs").join("symvault")));
            }
        }
        let trailing = format!("{}:", scratch.abs("none").display());
        assert_eq!(
            look_path_unix(&scratch.cwd, "symvault", OsStr::new(&trailing)),
            None,
            "trailing empty entry reaches the planted ./symvault"
        );
    }

    #[cfg(unix)]
    #[test]
    fn unix_skips_non_executable_and_directories_then_continues() {
        let scratch = Scratch::new();
        fs::create_dir_all(scratch.cwd.join("symvault")).expect("a directory named symvault");
        scratch.file("plain/symvault", 0o644);
        let good = scratch.file("good/symvault", 0o755);
        let path = join(&[Path::new("."), &scratch.abs("plain"), &scratch.abs("good")]);
        assert_eq!(look_path_unix(&scratch.cwd, "symvault", &path), Some(good));
    }

    #[cfg(unix)]
    #[test]
    fn unix_empty_path_is_not_the_working_directory() {
        let scratch = Scratch::new();
        scratch.file("cwd/symvault", 0o755);
        assert_eq!(
            look_path_unix(&scratch.cwd, "symvault", OsStr::new("")),
            None
        );
    }

    #[test]
    fn reserved_names_never_resolve() {
        for name in ["", ".", ".."] {
            assert_eq!(look_path(name), None);
        }
    }
}
