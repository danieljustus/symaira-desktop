#![deny(unsafe_code)]

//! Go `os/exec.LookPath` (go1.26 `lp_unix.go` / `lp_windows.go`) for the
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
    if matches!(file, "" | "." | "..") {
        return None;
    }
    if cfg!(windows) {
        let pathext = env::var_os("PATHEXT");
        let no_default_cwd = env::var_os("NoDefaultCurrentDirectoryInExePath").is_some();
        look_path_windows(
            Path::new("."),
            file,
            &env::var_os("PATH").unwrap_or_default(),
            pathext.as_deref(),
            no_default_cwd,
        )
    } else {
        look_path_unix(
            Path::new("."),
            file,
            &env::var_os("PATH").unwrap_or_default(),
        )
    }
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
#[cfg_attr(windows, allow(dead_code))]
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

#[cfg_attr(windows, allow(dead_code))]
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

/// Go `lp_windows.go` `lookPathExts` for a name without a separator, including
/// the implicit current-directory lookup (skipped when
/// `NoDefaultCurrentDirectoryInExePath` is set) and PATHEXT.
#[cfg_attr(not(windows), allow(dead_code))]
fn look_path_windows(
    cwd: &Path,
    file: &str,
    path: &OsStr,
    pathext: Option<&OsStr>,
    no_default_cwd: bool,
) -> Option<PathBuf> {
    let exts = path_ext(pathext);
    // The first relative hit; once set, Go ends in `ErrDot` unless an absolute
    // PATH entry resolves to the very same file.
    let mut dot_hit: Option<PathBuf> = None;
    if !no_default_cwd {
        dot_hit = windows_find_executable(cwd, Path::new(file), &exts);
    }
    for dir in split_list(path) {
        if dir.as_os_str().is_empty() {
            // Windows skips empty entries, like PowerShell.
            continue;
        }
        let Some(found) = windows_find_executable(cwd, &dir.join(file), &exts) else {
            continue;
        };
        if let Some(dot) = &dot_hit
            && !same_file(&at(cwd, dot), &at(cwd, &found))
        {
            return None;
        }
        if !found.is_absolute() {
            if dot_hit.is_none() {
                dot_hit = Some(found);
            }
            continue;
        }
        return Some(found);
    }
    None
}

#[cfg_attr(not(windows), allow(dead_code))]
fn same_file(left: &Path, right: &Path) -> bool {
    match (fs::canonicalize(left), fs::canonicalize(right)) {
        (Ok(left), Ok(right)) => left == right,
        _ => false,
    }
}

/// Go `pathExt`: lower-cased PATHEXT entries with a leading dot, or the default
/// list.
#[cfg_attr(not(windows), allow(dead_code))]
fn path_ext(pathext: Option<&OsStr>) -> Vec<String> {
    match pathext.map(|value| value.to_string_lossy().to_lowercase()) {
        Some(value) if !value.is_empty() => value
            .split(';')
            .filter(|entry| !entry.is_empty())
            .map(|entry| {
                if entry.starts_with('.') {
                    entry.to_owned()
                } else {
                    format!(".{entry}")
                }
            })
            .collect(),
        _ => [".com", ".exe", ".bat", ".cmd"]
            .into_iter()
            .map(str::to_owned)
            .collect(),
    }
}

/// Go `lp_windows.go` `findExecutable`.
#[cfg_attr(not(windows), allow(dead_code))]
fn windows_find_executable(cwd: &Path, file: &Path, exts: &[String]) -> Option<PathBuf> {
    if exts.is_empty() {
        return windows_stat_ok(&at(cwd, file)).then(|| file.to_path_buf());
    }
    let text = file.to_string_lossy();
    if has_ext(&text) && windows_stat_ok(&at(cwd, file)) {
        return Some(file.to_path_buf());
    }
    // Keep checking extensions so names like "foo.bat.exe" still resolve.
    exts.iter().find_map(|ext| {
        let candidate = PathBuf::from(format!("{text}{ext}"));
        windows_stat_ok(&at(cwd, &candidate)).then_some(candidate)
    })
}

#[cfg_attr(not(windows), allow(dead_code))]
fn windows_stat_ok(path: &Path) -> bool {
    fs::metadata(path).is_ok_and(|metadata| !metadata.is_dir())
}

/// Go `hasExt`: a dot after the last separator.
#[cfg_attr(not(windows), allow(dead_code))]
fn has_ext(file: &str) -> bool {
    let Some(dot) = file.rfind('.') else {
        return false;
    };
    file.rfind([':', '\\', '/'])
        .is_none_or(|separator| separator < dot)
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

    #[test]
    fn windows_default_extensions_and_pathext_normalisation() {
        let defaults = [".com", ".exe", ".bat", ".cmd"];
        assert_eq!(path_ext(None), defaults);
        assert_eq!(path_ext(Some(OsStr::new(""))), defaults);
        assert_eq!(path_ext(Some(OsStr::new(".EXE;;CMD"))), [".exe", ".cmd"]);
        assert!(has_ext("dir.d\\prog.exe"));
        assert!(!has_ext("dir.d\\prog"));
        assert!(!has_ext("prog"));
    }

    #[test]
    fn windows_absolute_path_entry_resolves_with_an_extension() {
        let scratch = Scratch::new();
        let exe = scratch.file("tools/symvault.exe", 0o755);
        let path = join(&[&scratch.abs("tools")]);
        assert_eq!(
            look_path_windows(&scratch.cwd, "symvault", &path, None, true),
            Some(exe)
        );
        scratch.file("other/symvault.txt", 0o755);
        let path = join(&[&scratch.abs("other")]);
        assert_eq!(
            look_path_windows(&scratch.cwd, "symvault", &path, None, true),
            None
        );
    }

    #[test]
    fn windows_rejects_relative_hits_and_the_implicit_working_directory() {
        let scratch = Scratch::new();
        scratch.file("cwd/bin/symvault.exe", 0o755);
        let abs = scratch.file("abs/symvault.exe", 0o755);
        // A relative PATH entry is a dot hit: rejected, and so is a later
        // absolute entry that is not the very same file.
        let relative = join(&[Path::new("bin")]);
        assert_eq!(
            look_path_windows(&scratch.cwd, "symvault", &relative, None, true),
            None
        );
        let shadowed = join(&[Path::new("bin"), &scratch.abs("abs")]);
        assert_eq!(
            look_path_windows(&scratch.cwd, "symvault", &shadowed, None, true),
            None
        );
        // The working directory is searched implicitly unless opted out.
        scratch.file("cwd/symvault.exe", 0o755);
        let only_abs = join(&[&scratch.abs("abs")]);
        assert_eq!(
            look_path_windows(&scratch.cwd, "symvault", &only_abs, None, false),
            None
        );
        assert_eq!(
            look_path_windows(&scratch.cwd, "symvault", &only_abs, None, true),
            Some(abs)
        );
    }
}
