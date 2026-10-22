//! Go-shaped filesystem errors.
//!
//! Go reports an OS failure as `<op> <path>: <strerror>` (`*fs.PathError`),
//! with the path exactly as the caller built it through `filepath.Join`
//! (lexically cleaned, so `./journal` is printed `journal`). The `symroom`
//! command line prints these strings verbatim, so the Rust port builds the same
//! text. The wording is the host's: lower-case libc text on Unix, the Windows
//! system message (capitalised, trailing period) on Windows.
//!
//! The helpers return [`io::Error`] values whose [`io::ErrorKind`] is the
//! original kind, so callers that branch on `NotFound` keep working; only the
//! `Display` text changes. Check `raw_os_error` before wrapping.

use std::{
    fs,
    io::{self, Read},
    path::{Path, PathBuf},
};

/// Go `filepath.Clean` for the host: no `.` elements, no repeated separators,
/// `..` resolved lexically, and `.` for an empty result. A line-for-line port
/// of Go's `internal/filepathlite.Clean`, so a leading `..` is kept and the
/// Windows volume rules apply on Windows.
#[must_use]
pub fn clean(path: &Path) -> String {
    go_clean(&path.to_string_lossy(), cfg!(windows))
}

/// Go's `lazybuf`: the output is written lazily, and `diverged` records
/// whether it ever stopped being a prefix of the input (Go's `buf != nil`),
/// which `postClean` depends on.
struct LazyBuf<'a> {
    path: &'a [u8],
    buf: Option<Vec<u8>>,
    w: usize,
}

impl LazyBuf<'_> {
    fn index(&self, i: usize) -> u8 {
        self.buf.as_ref().map_or(self.path[i], |buf| buf[i])
    }

    fn append(&mut self, c: u8) {
        match &mut self.buf {
            None => {
                if self.w < self.path.len() && self.path[self.w] == c {
                    self.w += 1;
                    return;
                }
                let mut buf = vec![0_u8; self.path.len()];
                buf[..self.w].copy_from_slice(&self.path[..self.w]);
                buf[self.w] = c;
                self.buf = Some(buf);
            }
            Some(buf) => {
                if self.w == buf.len() {
                    buf.push(c);
                } else {
                    buf[self.w] = c;
                }
            }
        }
        self.w += 1;
    }

    fn prepend(&mut self, prefix: &[u8]) {
        if let Some(buf) = &mut self.buf {
            buf.splice(0..0, prefix.iter().copied());
        }
        self.w += prefix.len();
    }
}

const fn is_path_separator(c: u8, windows: bool) -> bool {
    c == b'/' || (windows && c == b'\\')
}

/// Go `filepath.Clean` for either host family, testable on any OS.
///
/// `windows` selects Go's Windows rules (`\\` separator, volume names, `postClean`).
#[must_use]
pub fn go_clean(path: &str, windows: bool) -> String {
    let original = path.as_bytes();
    let separator = if windows { b'\\' } else { b'/' };
    let vol_len = if windows {
        volume_name_len(original)
    } else {
        0
    };
    let path = &original[vol_len..];
    if path.is_empty() {
        if vol_len > 1
            && is_path_separator(original[0], windows)
            && is_path_separator(original[1], windows)
        {
            // Should be UNC.
            return from_slash(original, windows);
        }
        let mut text = path_text(original);
        text.push('.');
        return text;
    }
    let rooted = is_path_separator(path[0], windows);
    let n = path.len();
    let mut out = LazyBuf {
        path,
        buf: None,
        w: 0,
    };
    let (mut r, mut dotdot) = (0_usize, 0_usize);
    if rooted {
        out.append(separator);
        r = 1;
        dotdot = 1;
    }
    while r < n {
        if is_path_separator(path[r], windows) {
            // Empty path element.
            r += 1;
        } else if path[r] == b'.' && (r + 1 == n || is_path_separator(path[r + 1], windows)) {
            // `.` element.
            r += 1;
        } else if path[r] == b'.'
            && path[r + 1] == b'.'
            && (r + 2 == n || is_path_separator(path[r + 2], windows))
        {
            // `..` element: remove to the last separator.
            r += 2;
            if out.w > dotdot {
                out.w -= 1;
                while out.w > dotdot && !is_path_separator(out.index(out.w), windows) {
                    out.w -= 1;
                }
            } else if !rooted {
                // Cannot backtrack, but not rooted, so keep the `..` element.
                if out.w > 0 {
                    out.append(separator);
                }
                out.append(b'.');
                out.append(b'.');
                dotdot = out.w;
            }
        } else {
            // Real path element: add a separator if needed, then copy it.
            if (rooted && out.w != 1) || (!rooted && out.w != 0) {
                out.append(separator);
            }
            while r < n && !is_path_separator(path[r], windows) {
                out.append(path[r]);
                r += 1;
            }
        }
    }
    // Turn an empty result into ".".
    if out.w == 0 {
        out.append(b'.');
    }
    if windows {
        post_clean(&mut out, vol_len);
    }
    let body = out
        .buf
        .as_ref()
        .map_or_else(|| path[..out.w].to_vec(), |buf| buf[..out.w].to_vec());
    let mut joined = original[..vol_len].to_vec();
    joined.extend_from_slice(&body);
    from_slash(&joined, windows)
}

fn path_text(bytes: &[u8]) -> String {
    String::from_utf8_lossy(bytes).into_owned()
}

fn from_slash(bytes: &[u8], windows: bool) -> String {
    let bytes: Vec<u8> = bytes
        .iter()
        .map(|&c| if windows && c == b'/' { b'\\' } else { c })
        .collect();
    path_text(&bytes)
}

/// Go `postClean`: keep Clean from turning a relative path into a rooted or
/// drive-relative one on Windows.
fn post_clean(out: &mut LazyBuf<'_>, vol_len: usize) {
    if vol_len != 0 || out.buf.is_none() {
        return;
    }
    // A ':' in the first element (`a/../c:`) would become a drive: insert `.\`.
    let first_colon = out
        .buf
        .as_ref()
        .into_iter()
        .flatten()
        .copied()
        .take_while(|&c| !is_path_separator(c, true))
        .any(|c| c == b':');
    if first_colon {
        out.prepend(b".\\");
        return;
    }
    // A path beginning `\??\` would become a Root Local Device path.
    let starts_device = out.buf.as_ref().is_some_and(|buf| {
        buf.len() >= 3 && is_path_separator(buf[0], true) && buf[1] == b'?' && buf[2] == b'?'
    });
    if starts_device {
        out.prepend(b"\\.");
    }
}

/// Length of the Windows volume name at the start of `path` (Go
/// `volumeNameLen`).
fn volume_name_len(path: &[u8]) -> usize {
    let sep = |c: u8| is_path_separator(c, true);
    if path.len() >= 2 && path[1] == b':' {
        // Drive letter.
        return 2;
    }
    if path.is_empty() || !sep(path[0]) {
        return 0;
    }
    if path_has_prefix_fold(path, b"\\\\.\\UNC") {
        return unc_len(path, b"\\\\.\\UNC\\".len());
    }
    if path_has_prefix_fold(path, b"\\\\.")
        || path_has_prefix_fold(path, b"\\\\?")
        || path_has_prefix_fold(path, b"\\??")
    {
        // Local Device or Root Local Device path: the next element is part of
        // the volume name.
        if path.len() == 3 {
            return 3;
        }
        let tail = path.get(4..).unwrap_or_default();
        return match tail.iter().position(|&c| sep(c)) {
            None => path.len(),
            Some(index) => path.len() - (tail.len() - index - 1) - 1,
        };
    }
    if path.len() >= 2 && sep(path[1]) {
        return unc_len(path, 2);
    }
    0
}

/// Go `pathHasPrefixFold`: case-insensitive prefix test treating both
/// separators as equal; the byte after the prefix must be a separator.
fn path_has_prefix_fold(s: &[u8], prefix: &[u8]) -> bool {
    if s.len() < prefix.len() {
        return false;
    }
    for (index, &expected) in prefix.iter().enumerate() {
        if is_path_separator(expected, true) {
            if !is_path_separator(s[index], true) {
                return false;
            }
        } else if !expected.eq_ignore_ascii_case(&s[index]) {
            return false;
        }
    }
    s.len() <= prefix.len() || is_path_separator(s[prefix.len()], true)
}

/// Go `uncLen`: the host and share are part of the volume name.
fn unc_len(path: &[u8], prefix_len: usize) -> usize {
    let mut count = 0;
    for (index, &c) in path.iter().enumerate().skip(prefix_len) {
        if is_path_separator(c, true) {
            count += 1;
            if count == 2 {
                return index;
            }
        }
    }
    path.len()
}

/// Go's `syscall.Errno.Error()` text for an OS error. `windows` selects the host
/// family so both wordings are unit-testable on any OS: Windows keeps the
/// `FormatMessage` text, everything else lower-cases libc's.
#[must_use]
pub fn os_message(error: &io::Error, windows: bool) -> String {
    if windows {
        match error.raw_os_error() {
            Some(1) => return "Incorrect function.".to_owned(),
            Some(2) => return "The system cannot find the file specified.".to_owned(),
            Some(3) => return "The system cannot find the path specified.".to_owned(),
            Some(5) => return "Access is denied.".to_owned(),
            _ => {}
        }
    }
    let mut text = error.to_string();
    if let Some(index) = text.rfind(" (os error ") {
        text.truncate(index);
    }
    if windows {
        return text;
    }
    let mut chars = text.chars();
    chars
        .next()
        .map(|first| first.to_lowercase().collect::<String>() + chars.as_str())
        .unwrap_or_default()
}

/// Go `*fs.PathError` text: `<op> <path>: <strerror>`.
#[must_use]
pub fn path_error_text(operation: &str, path: &Path, error: &io::Error) -> String {
    format!(
        "{operation} {}: {}",
        clean(path),
        os_message(error, cfg!(windows))
    )
}

/// Wraps `error` so its `Display` is the Go path error, keeping the kind.
#[must_use]
pub fn path_error(operation: &str, path: &Path, error: &io::Error) -> io::Error {
    io::Error::new(error.kind(), path_error_text(operation, path, error))
}

fn open_options() -> fs::OpenOptions {
    let mut options = fs::OpenOptions::new();
    options.read(true);
    #[cfg(windows)]
    {
        use std::os::windows::fs::OpenOptionsExt;
        // Go opens a directory with backup semantics, so reading it fails in
        // `read` rather than `open`.
        const FILE_FLAG_BACKUP_SEMANTICS: u32 = 0x0200_0000;
        options.custom_flags(FILE_FLAG_BACKUP_SEMANTICS);
    }
    options
}

/// Opens like Go's `os.Open` but returns the raw OS error, for callers that
/// format the failure themselves.
///
/// # Errors
/// Returns the unmodified OS error.
pub fn open_raw(path: &Path) -> io::Result<fs::File> {
    open_options().open(path)
}

/// Go `os.Open`: failures are `open <path>: ...`.
///
/// # Errors
/// Returns the Go-formatted open error.
pub fn open(path: &Path) -> io::Result<fs::File> {
    open_raw(path).map_err(|error| path_error("open", path, &error))
}

/// Go `os.ReadFile`: `open` failures and, for a directory, `read` failures.
/// The returned error is already Go-formatted.
///
/// # Errors
/// Returns the Go-formatted open or read error.
pub fn read_file(path: &Path) -> io::Result<Vec<u8>> {
    let mut file = open(path)?;
    let mut contents = Vec::new();
    file.read_to_end(&mut contents)
        .map_err(|error| path_error("read", path, &error))?;
    Ok(contents)
}

/// Go `os.ReadDir`: failures are `open <path>: ...`.
///
/// # Errors
/// Returns the Go-formatted open error.
pub fn read_dir(path: &Path) -> io::Result<fs::ReadDir> {
    fs::read_dir(path).map_err(|error| path_error("open", path, &error))
}

/// Go `os.Getwd` on Unix: `$PWD` wins when it is absolute and names the same
/// directory as `.` (same device and inode), so a symlinked working directory
/// keeps its symlink name. Otherwise the kernel's working directory is used.
/// Windows never consults `$PWD`.
///
/// # Errors
/// Returns the OS error when the working directory cannot be determined.
pub fn getwd() -> io::Result<PathBuf> {
    getwd_with(std::env::var_os("PWD").as_deref(), Path::new("."))
}

/// `dot` is the path Go stats as "."; it is a parameter so the `$PWD` rule is
/// testable without changing the process working directory.
fn getwd_with(pwd: Option<&std::ffi::OsStr>, dot: &Path) -> io::Result<PathBuf> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::MetadataExt;
        if let Some(pwd) = pwd.map(PathBuf::from).filter(|pwd| pwd.is_absolute()) {
            let dot = fs::metadata(dot)?;
            if let Ok(named) = fs::metadata(&pwd)
                && named.dev() == dot.dev()
                && named.ino() == dot.ino()
            {
                return Ok(pwd);
            }
        }
    }
    #[cfg(not(unix))]
    let _ = (pwd, dot);
    std::env::current_dir()
}

/// Go `filepath.Abs` on Unix: the cleaned `Getwd` joined with a relative path.
/// An unreadable working directory leaves the path unchanged.
#[must_use]
pub fn abs(path: &Path) -> PathBuf {
    if path.is_absolute() {
        return PathBuf::from(clean(path));
    }
    match getwd() {
        Ok(cwd) => PathBuf::from(clean(&cwd.join(path))),
        Err(_) => path.to_path_buf(),
    }
}

#[cfg(test)]
mod tests {
    #[cfg(unix)]
    use super::getwd_with;
    use super::{clean, go_clean, os_message, path_error_text, read_dir, read_file};
    use std::{io, path::Path};

    /// `cleantests`, `nonwincleantests` and `wincleantests` from Go's
    /// `path/filepath/path_test.go` (go1.26.10), generated from that file.
    const CLEAN_TESTS: &[(&str, &str)] = &[
        (r"abc", r"abc"),
        (r"abc/def", r"abc/def"),
        (r"a/b/c", r"a/b/c"),
        (r".", r"."),
        (r"..", r".."),
        (r"../..", r"../.."),
        (r"../../abc", r"../../abc"),
        (r"/abc", r"/abc"),
        (r"/", r"/"),
        ("", r"."),
        (r"abc/", r"abc"),
        (r"abc/def/", r"abc/def"),
        (r"a/b/c/", r"a/b/c"),
        (r"./", r"."),
        (r"../", r".."),
        (r"../../", r"../.."),
        (r"/abc/", r"/abc"),
        (r"abc//def//ghi", r"abc/def/ghi"),
        (r"abc//", r"abc"),
        (r"abc/./def", r"abc/def"),
        (r"/./abc/def", r"/abc/def"),
        (r"abc/.", r"abc"),
        (r"abc/def/ghi/../jkl", r"abc/def/jkl"),
        (r"abc/def/../ghi/../jkl", r"abc/jkl"),
        (r"abc/def/..", r"abc"),
        (r"abc/def/../..", r"."),
        (r"/abc/def/../..", r"/"),
        (r"abc/def/../../..", r".."),
        (r"/abc/def/../../..", r"/"),
        (r"abc/def/../../../ghi/jkl/../../../mno", r"../../mno"),
        (r"/../abc", r"/abc"),
        (r"a/../b:/../../c", r"../c"),
        (r"abc/./../def", r"def"),
        (r"abc//./../def", r"def"),
        (r"abc/../../././../def", r"../../def"),
    ];

    const NON_WINDOWS_CLEAN_TESTS: &[(&str, &str)] = &[
        (r"//abc", r"/abc"),
        (r"///abc", r"/abc"),
        (r"//abc//", r"/abc"),
    ];

    const WINDOWS_CLEAN_TESTS: &[(&str, &str)] = &[
        (r"c:", r"c:."),
        (r"c:\", r"c:\"),
        (r"c:\abc", r"c:\abc"),
        (r"c:abc\..\..\.\.\..\def", r"c:..\..\def"),
        (r"c:\abc\def\..\..", r"c:\"),
        (r"c:\..\abc", r"c:\abc"),
        (r"c:..\abc", r"c:..\abc"),
        (r"c:\b:\..\..\..\d", r"c:\d"),
        (r"\", r"\"),
        (r"/", r"\"),
        (r"\\i\..\c$", r"\\i\..\c$"),
        (r"\\i\..\i\c$", r"\\i\..\i\c$"),
        (r"\\i\..\I\c$", r"\\i\..\I\c$"),
        (r"\\host\share\foo\..\bar", r"\\host\share\bar"),
        (r"//host/share/foo/../baz", r"\\host\share\baz"),
        (r"\\host\share\foo\..\..\..\..\bar", r"\\host\share\bar"),
        (r"\\.\C:\a\..\..\..\..\bar", r"\\.\C:\bar"),
        (r"\\.\C:\\\\a", r"\\.\C:\a"),
        (r"\\a\b\..\c", r"\\a\b\c"),
        (r"\\a\b", r"\\a\b"),
        (r".\c:", r".\c:"),
        (r".\c:\foo", r".\c:\foo"),
        (r".\c:foo", r".\c:foo"),
        (r"//abc", r"\\abc"),
        (r"///abc", r"\\\abc"),
        (r"//abc//", r"\\abc\\"),
        (r"\\?\C:\", r"\\?\C:\"),
        (r"\\?\C:\a", r"\\?\C:\a"),
        (r"a/../c:", r".\c:"),
        (r"a\..\c:", r".\c:"),
        (r"a/../c:/a", r".\c:\a"),
        (r"a/../../c:", r"..\c:"),
        (r"foo:bar", r"foo:bar"),
        (r"/a/../??/a", r"\.\??\a"),
    ];

    #[test]
    fn go_clean_matches_go_unix_table() {
        for (input, want) in CLEAN_TESTS.iter().chain(NON_WINDOWS_CLEAN_TESTS) {
            assert_eq!(go_clean(input, false), *want, "Clean({input:?})");
            assert_eq!(
                go_clean(want, false),
                *want,
                "Clean({want:?}) is idempotent"
            );
        }
    }

    /// The Windows rules run on every host because `go_clean` is a string port.
    /// This covers the `c:` -> `c:.` volume edge case, UNC and device paths.
    #[test]
    fn go_clean_matches_go_windows_table() {
        for (input, want) in CLEAN_TESTS {
            let want = want.replace('/', "\\");
            assert_eq!(go_clean(input, true), want, "Clean({input:?})");
        }
        for (input, want) in WINDOWS_CLEAN_TESTS {
            assert_eq!(go_clean(input, true), *want, "Clean({input:?})");
            assert_eq!(go_clean(want, true), *want, "Clean({want:?}) is idempotent");
        }
    }

    #[test]
    fn clean_matches_go_filepath_clean_on_the_host() {
        assert_eq!(clean(Path::new("./journal")), "journal");
        assert_eq!(clean(Path::new("")), ".");
        assert_eq!(
            clean(Path::new("../x")),
            "..".to_owned() + std::path::MAIN_SEPARATOR_STR + "x"
        );
        #[cfg(windows)]
        assert_eq!(clean(Path::new("c:")), "c:.");
    }

    #[cfg(unix)]
    #[test]
    fn getwd_honours_pwd_for_a_symlinked_working_directory() {
        use std::os::unix::fs::symlink;
        let root = std::env::temp_dir().join(format!("symroom-getwd-{}", std::process::id()));
        let real = root.join("real");
        let link = root.join("link");
        let other = root.join("other");
        std::fs::create_dir_all(&real).unwrap();
        std::fs::create_dir_all(&other).unwrap();
        symlink(&real, &link).unwrap();
        let kernel = std::env::current_dir().unwrap();

        // `dot` stands for the working directory, reached through the symlink.
        // PWD names the same directory: Go keeps the symlink name.
        assert_eq!(getwd_with(Some(link.as_os_str()), &link).unwrap(), link);
        assert_eq!(getwd_with(Some(real.as_os_str()), &link).unwrap(), real);
        // PWD names another directory, is relative, empty or unset: kernel cwd.
        for pwd in [
            Some(other.as_os_str()),
            Some("link".as_ref()),
            Some("".as_ref()),
            None,
        ] {
            assert_eq!(getwd_with(pwd, &link).unwrap(), kernel);
        }
        std::fs::remove_dir_all(&root).unwrap();
    }

    #[test]
    fn windows_and_unix_wording() {
        let not_found = io::Error::from_raw_os_error(2);
        assert_eq!(
            os_message(&not_found, true),
            "The system cannot find the file specified."
        );
        assert_eq!(
            os_message(&io::Error::from_raw_os_error(3), true),
            "The system cannot find the path specified."
        );
        #[cfg(unix)]
        {
            assert_eq!(os_message(&not_found, false), "no such file or directory");
            assert_eq!(
                os_message(&io::Error::from_raw_os_error(20), false),
                "not a directory"
            );
        }
    }

    #[test]
    fn read_failures_name_the_go_operation_and_keep_the_kind() {
        let root = std::env::temp_dir().join(format!("symroom-goio-{}", std::process::id()));
        std::fs::create_dir_all(&root).unwrap();
        let missing = root.join("missing.txt");
        let error = read_file(&missing).unwrap_err();
        assert_eq!(error.kind(), io::ErrorKind::NotFound);
        assert_eq!(
            error.to_string(),
            path_error_text("open", &missing, &io::Error::from_raw_os_error(2))
        );
        let error = read_dir(&missing).err().unwrap();
        assert!(error.to_string().starts_with("open "));
        std::fs::remove_dir_all(&root).unwrap();
    }
}
