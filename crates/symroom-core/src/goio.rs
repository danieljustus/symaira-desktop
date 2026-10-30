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
    go_clean(&os_text(path.as_os_str()), cfg!(windows))
}

/// First code point of the block that carries bytes which are not UTF-8 (see
/// [`os_text`]): `ESCAPE_BASE + byte` stands for one raw byte.
const ESCAPE_BASE: u32 = 0x0010_F800;

fn escaped(ch: char) -> bool {
    (ESCAPE_BASE + 0x80..=ESCAPE_BASE + 0xFF).contains(&u32::from(ch))
}

/// The byte an escape character stands for.
#[must_use]
pub fn escaped_byte(ch: char) -> Option<u8> {
    escaped(ch).then(|| (u32::from(ch) - ESCAPE_BASE) as u8)
}

fn push_escaped(out: &mut String, byte: u8) {
    out.push(char::from_u32(ESCAPE_BASE + u32::from(byte)).expect("escape block is valid"));
}

/// Go strings are byte strings, Rust's are UTF-8: an argument, path or file
/// name with bytes that are not UTF-8 would be rewritten by `to_string_lossy`.
/// `os_text` keeps it lossless: every byte that is not part of valid UTF-8 (and
/// every byte of the rare character that lives in the escape block itself)
/// becomes one private-use character `U+10F880..=U+10F8FF`, so the mapping is
/// a bijection on byte strings. [`text_os`] and [`out_bytes`] undo it at the
/// operating-system boundary; [`json_text`] gives the Go JSON view (one
/// `U+FFFD` per escaped byte).
#[must_use]
pub fn os_text(text: &std::ffi::OsStr) -> String {
    #[cfg(unix)]
    {
        use std::os::unix::ffi::OsStrExt;
        bytes_text(text.as_bytes())
    }
    #[cfg(not(unix))]
    {
        text.to_string_lossy().into_owned()
    }
}

/// [`os_text`] for a byte string.
#[must_use]
pub fn bytes_text(mut bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len());
    loop {
        match std::str::from_utf8(bytes) {
            Ok(valid) => {
                push_valid(&mut out, valid);
                return out;
            }
            Err(error) => {
                let (valid, rest) = bytes.split_at(error.valid_up_to());
                push_valid(
                    &mut out,
                    std::str::from_utf8(valid).expect("validated prefix"),
                );
                let bad = error.error_len().unwrap_or(rest.len());
                for byte in &rest[..bad] {
                    push_escaped(&mut out, *byte);
                }
                bytes = &rest[bad..];
            }
        }
    }
}

fn push_valid(out: &mut String, valid: &str) {
    for ch in valid.chars() {
        if escaped(ch) {
            let mut buffer = [0_u8; 4];
            for byte in ch.encode_utf8(&mut buffer).bytes() {
                push_escaped(out, byte);
            }
        } else {
            out.push(ch);
        }
    }
}

/// The raw bytes behind a string made by [`os_text`].
#[must_use]
pub fn out_bytes(text: &str) -> std::borrow::Cow<'_, [u8]> {
    if !text.chars().any(escaped) {
        return std::borrow::Cow::Borrowed(text.as_bytes());
    }
    let mut bytes = Vec::with_capacity(text.len());
    for ch in text.chars() {
        if escaped(ch) {
            bytes.push((u32::from(ch) - ESCAPE_BASE) as u8);
        } else {
            let mut buffer = [0_u8; 4];
            bytes.extend_from_slice(ch.encode_utf8(&mut buffer).as_bytes());
        }
    }
    std::borrow::Cow::Owned(bytes)
}

/// The operating-system string behind a string made by [`os_text`].
#[must_use]
pub fn text_os(text: &str) -> std::ffi::OsString {
    #[cfg(unix)]
    {
        use std::os::unix::ffi::OsStringExt;
        std::ffi::OsString::from_vec(out_bytes(text).into_owned())
    }
    #[cfg(not(unix))]
    {
        std::ffi::OsString::from(text)
    }
}

/// What Go's `encoding/json` makes of the bytes once a string has been
/// serialized: each byte that is not UTF-8 is written as the escape `\ufffd`
/// (a genuine `U+FFFD` stays a literal character).
#[must_use]
pub fn json_finish(serialized: &str) -> std::borrow::Cow<'_, str> {
    if !serialized.chars().any(escaped) {
        return std::borrow::Cow::Borrowed(serialized);
    }
    let mut out = String::with_capacity(serialized.len());
    for ch in serialized.chars() {
        if escaped(ch) {
            out.push_str("\\ufffd");
        } else {
            out.push(ch);
        }
    }
    std::borrow::Cow::Owned(out)
}

/// A path held as [`os_text`] text, as an operating-system path.
#[must_use]
pub fn text_path(text: &str) -> PathBuf {
    PathBuf::from(text_os(text))
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

/// [`path_error`] for a path held as [`os_text`] text.
#[must_use]
pub fn path_error_str(operation: &str, path: &str, error: &io::Error) -> io::Error {
    io::Error::new(
        error.kind(),
        format!(
            "{operation} {}: {}",
            go_clean(path, cfg!(windows)),
            os_message(error, cfg!(windows))
        ),
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

/// Go `os.MkdirAll(path, perm)`: creates every missing ancestor with `mode`
/// (the umask still applies), never touches the mode of a directory that
/// exists, and fails with `mkdir <path>: ...` naming the component that could
/// not be created (a non-directory in the way is `not a directory`).
///
/// # Errors
/// Returns the Go-formatted `mkdir` error.
pub fn mkdir_all(path: &str, mode: u32) -> io::Result<()> {
    if let Ok(meta) = fs::metadata(text_path(path)) {
        if meta.is_dir() {
            return Ok(());
        }
        #[cfg(unix)]
        let not_a_directory = io::Error::from_raw_os_error(20);
        #[cfg(not(unix))]
        let not_a_directory = io::Error::from(io::ErrorKind::NotADirectory);
        return Err(path_error_str("mkdir", path, &not_a_directory));
    }
    let bytes = path.as_bytes();
    let mut end = bytes.len();
    while end > 0 && is_path_separator(bytes[end - 1], cfg!(windows)) {
        end -= 1;
    }
    let mut parent = end;
    while parent > 0 && !is_path_separator(bytes[parent - 1], cfg!(windows)) {
        parent -= 1;
    }
    if parent > 1 {
        mkdir_all(&path[..parent - 1], mode)?;
    }
    match mkdir(&text_path(path), mode) {
        Ok(()) => Ok(()),
        Err(error) => {
            if fs::symlink_metadata(text_path(path)).is_ok_and(|meta| meta.is_dir()) {
                return Ok(());
            }
            Err(path_error_str("mkdir", path, &error))
        }
    }
}

#[cfg(unix)]
fn mkdir(path: &Path, mode: u32) -> io::Result<()> {
    use std::os::unix::fs::DirBuilderExt;
    fs::DirBuilder::new().mode(mode).create(path)
}

#[cfg(not(unix))]
fn mkdir(path: &Path, _mode: u32) -> io::Result<()> {
    fs::create_dir(path)
}

/// Go `filepath.Join` of two path strings: concatenated with a separator and
/// lexically cleaned, so an absolute second element does not replace the first
/// and `..` never reaches the file system.
#[must_use]
pub fn join(base: &str, element: &str) -> String {
    go_clean(&format!("{base}/{element}"), cfg!(windows))
}

/// Go `os.OpenFile(path, O_WRONLY|O_CREATE|O_TRUNC, mode)` followed by a
/// write: the mode applies (minus the umask) only when the file is created, and
/// failures read `open <path>: ...` or `write <path>: ...`.
///
/// # Errors
/// Returns the Go-formatted open or write error.
pub fn write_file(path: &str, data: &[u8], mode: u32) -> io::Result<()> {
    use io::Write;
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(mode);
    }
    #[cfg(not(unix))]
    let _ = mode;
    let mut file = options
        .open(text_path(path))
        .map_err(|error| path_error_str("open", path, &error))?;
    file.write_all(data)
        .map_err(|error| path_error_str("write", path, &error))
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
        return text_path(&clean(path));
    }
    match getwd() {
        Ok(cwd) => text_path(&clean(&cwd.join(path))),
        Err(_) => path.to_path_buf(),
    }
}

#[cfg(test)]
mod tests {

    #[test]
    #[cfg(unix)]
    fn byte_escape_round_trips_every_byte_string_and_marks_only_invalid_bytes() {
        use super::{escaped_byte, json_finish, os_text, out_bytes, text_os};
        use std::os::unix::ffi::{OsStrExt, OsStringExt};
        let samples: [&[u8]; 8] = [
            b"plain",
            b"\xff\xfe",
            b"a\xe2\x82b",
            "é界😀".as_bytes(),
            // The reserved block itself is escaped byte for byte.
            "\u{10f880}\u{10f8ff}".as_bytes(),
            b"\xf4\x8f\xa2\x80",
            b"",
            b"\xc0\x80x\xed\xa0\x80",
        ];
        for sample in samples {
            let text = os_text(std::ffi::OsStr::from_bytes(sample));
            assert_eq!(out_bytes(&text).as_ref(), sample, "{sample:?}");
            assert_eq!(text_os(&text).into_vec(), sample);
        }
        assert_eq!(os_text(std::ffi::OsStr::new("é界😀")), "é界😀");
        assert_eq!(
            escaped_byte(
                os_text(std::ffi::OsStr::from_bytes(b"\xff"))
                    .chars()
                    .next()
                    .unwrap()
            ),
            Some(0xff)
        );
        assert_eq!(
            json_finish(&os_text(std::ffi::OsStr::from_bytes(b"a\xffb\xef\xbf\xbd"))),
            "a\\ufffdb\u{fffd}"
        );
    }
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
