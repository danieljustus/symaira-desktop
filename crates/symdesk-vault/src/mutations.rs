#![deny(unsafe_code)]

use std::{
    fs::{self, File, OpenOptions},
    io::{self, Write},
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};

use noyalib::Value;
use thiserror::Error;

mod go_yaml;

/// Errors returned by frontmatter mutations.
#[derive(Debug, Error)]
pub enum MutationError {
    #[error("read file: {source}")]
    Read {
        #[source]
        source: io::Error,
    },
    #[error("write file: {source}")]
    Write {
        #[source]
        source: io::Error,
    },
    #[error("create temp file: {source}")]
    TempCreate {
        #[source]
        source: io::Error,
    },
    #[error("write temp file: {source}")]
    TempWrite {
        #[source]
        source: io::Error,
    },
    #[error("sync temp file: {source}")]
    TempSync {
        #[source]
        source: io::Error,
    },
    #[error("rename temp file: {source}")]
    TempRename {
        #[source]
        source: io::Error,
    },
    #[error("marshal value: {detail}")]
    Marshal { detail: String },
}

/// Writes a scalar frontmatter key using the legacy Go writer's non-atomic I/O
/// and quoting rules. The selected path is intentionally not confined here.
///
/// # Errors
/// Returns [`MutationError`] when the selected file cannot be read or written.
pub fn set_frontmatter_key(
    path: impl AsRef<Path>,
    key: &str,
    value: &str,
) -> Result<(), MutationError> {
    let path = path.as_ref();
    let data = fs::read(path).map_err(|source| MutationError::Read { source })?;
    let mut lines = split_on_lf(&data);
    let rendered = format!("{key}: {}", quote_yaml(value)).into_bytes();

    let Some((start, end)) = frontmatter_bounds(&lines) else {
        let mut new_lines = Vec::with_capacity(lines.len() + 3);
        new_lines.push(b"---".to_vec());
        new_lines.push(rendered);
        new_lines.push(b"---".to_vec());
        new_lines.append(&mut lines);
        return write_scalar(path, &join_with(&new_lines, b"\n"));
    };

    let mut replaced = false;
    for line in &mut lines[(start + 1)..end] {
        if matches_key(line, key) {
            *line = rendered.clone();
            replaced = true;
            break;
        }
    }
    if !replaced {
        lines.insert(end, rendered);
    }
    write_scalar(path, &join_with(&lines, b"\n"))
}

/// Writes a typed YAML frontmatter value atomically while preserving all bytes
/// outside the changed line. Collections use the same inline/block split as the
/// Go implementation: sequences are inline and mappings use the YAML encoder.
///
/// # Errors
/// Returns [`MutationError`] when value rendering or the atomic write fails.
pub fn set_frontmatter_value(
    path: impl AsRef<Path>,
    key: &str,
    value: &Value,
) -> Result<(), MutationError> {
    let path = path.as_ref();
    let data = fs::read(path).map_err(|source| MutationError::Read { source })?;
    let separator = if data.windows(2).any(|window| window == b"\r\n") {
        b"\r\n".as_slice()
    } else {
        b"\n".as_slice()
    };
    let mut lines = split_on(&data, separator);
    let rendered = format!("{key}: {}", yaml_value_string(value)?).into_bytes();
    let Some((start, end)) = frontmatter_bounds(&lines) else {
        let mut new_lines = Vec::with_capacity(lines.len() + 3);
        new_lines.push(b"---".to_vec());
        new_lines.push(rendered);
        new_lines.push(b"---".to_vec());
        new_lines.append(&mut lines);
        return write_atomic(path, &join_with(&new_lines, separator));
    };

    let mut replaced = false;
    for line in &mut lines[(start + 1)..end] {
        if matches_key(line, key) {
            *line = rendered.clone();
            replaced = true;
            break;
        }
    }
    if !replaced {
        lines.insert(end, rendered);
    }
    write_atomic(path, &join_with(&lines, separator))
}

/// Deletes the first matching frontmatter key atomically. A missing frontmatter
/// block is a no-op; an absent key inside an existing block still performs the
/// Go-compatible atomic rewrite.
///
/// # Errors
/// Returns [`MutationError`] when the selected file cannot be read or rewritten.
pub fn delete_frontmatter_value(path: impl AsRef<Path>, key: &str) -> Result<(), MutationError> {
    let path = path.as_ref();
    let data = fs::read(path).map_err(|source| MutationError::Read { source })?;
    let separator = if data.windows(2).any(|window| window == b"\r\n") {
        b"\r\n".as_slice()
    } else {
        b"\n".as_slice()
    };
    let mut lines = split_on(&data, separator);
    let Some((start, end)) = frontmatter_bounds(&lines) else {
        return Ok(());
    };
    if let Some(index) = (start + 1..end).find(|index| matches_key(&lines[*index], key)) {
        lines.remove(index);
    }
    write_atomic(path, &join_with(&lines, separator))
}

fn quote_yaml(value: &str) -> String {
    if value.is_empty() {
        return "\"\"".to_owned();
    }
    let mut is_number = true;
    for (index, character) in value.chars().enumerate() {
        if character.is_ascii_digit() || character == '.' {
            continue;
        }
        if character == '-' && index == 0 {
            continue;
        }
        is_number = false;
        break;
    }
    if is_number {
        return value.to_owned();
    }
    format!("\"{}\"", value.replace('"', "\\\""))
}

fn yaml_value_string(value: &Value) -> Result<String, MutationError> {
    match value {
        Value::String(value) => Ok(quote_yaml(value)),
        Value::Number(value) => go_yaml::format_scalar_number(value, false)
            .map_err(|detail| MutationError::Marshal { detail }),
        Value::Bool(value) => Ok(value.to_string()),
        Value::Null => Ok("null".to_owned()),
        Value::Sequence(values) => values
            .iter()
            .map(yaml_value_string)
            .collect::<Result<Vec<_>, _>>()
            .map(|parts| format!("[{}]", parts.join(", "))),
        Value::Mapping(value) => {
            go_yaml::render_mapping(value).map_err(|detail| MutationError::Marshal { detail })
        }
        Value::Tagged(_) => noyalib::to_string(value)
            .map(|serialized| serialized.trim().to_owned())
            .map_err(|error| MutationError::Marshal {
                detail: error.to_string(),
            }),
    }
}

fn split_on_lf(data: &[u8]) -> Vec<Vec<u8>> {
    split_on(data, b"\n")
}

fn split_on(data: &[u8], separator: &[u8]) -> Vec<Vec<u8>> {
    let mut result = Vec::new();
    let mut start = 0;
    while let Some(relative) = data[start..]
        .windows(separator.len())
        .position(|window| window == separator)
    {
        let end = start + relative;
        result.push(data[start..end].to_vec());
        start = end + separator.len();
    }
    result.push(data[start..].to_vec());
    result
}

fn join_with(lines: &[Vec<u8>], separator: &[u8]) -> Vec<u8> {
    let capacity = lines.iter().map(Vec::len).sum::<usize>()
        + separator
            .len()
            .saturating_mul(lines.len().saturating_sub(1));
    let mut result = Vec::with_capacity(capacity);
    for (index, line) in lines.iter().enumerate() {
        if index != 0 {
            result.extend_from_slice(separator);
        }
        result.extend_from_slice(line);
    }
    result
}

fn frontmatter_bounds(lines: &[Vec<u8>]) -> Option<(usize, usize)> {
    let mut start = None;
    for (index, line) in lines.iter().enumerate() {
        let trimmed = trim_trailing_cr(line);
        if trimmed == b"---" {
            if let Some(start) = start {
                return Some((start, index));
            }
            start = Some(index);
        }
    }
    None
}

fn matches_key(line: &[u8], key: &str) -> bool {
    let trimmed = trim_trailing_cr(line);
    trimmed == key.as_bytes() || trimmed.starts_with(format!("{key}: ").as_bytes())
}

fn trim_trailing_cr(line: &[u8]) -> &[u8] {
    let end = line
        .iter()
        .rposition(|&byte| byte != b'\r')
        .map_or(0, |index| index + 1);
    &line[..end]
}

fn write_scalar(path: &Path, data: &[u8]) -> Result<(), MutationError> {
    let mut options = OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o644);
    }
    let mut file = options
        .open(path)
        .map_err(|source| MutationError::Write { source })?;
    file.write_all(data)
        .map_err(|source| MutationError::Write { source })
}

static TEMP_COUNTER: AtomicU64 = AtomicU64::new(0);

fn write_atomic(path: &Path, data: &[u8]) -> Result<(), MutationError> {
    let directory = atomic_directory(path);
    let mut file_and_path: Option<(File, PathBuf)> = None;
    for _ in 0..100 {
        let counter = TEMP_COUNTER.fetch_add(1, Ordering::Relaxed);
        let candidate = directory.join(format!(
            ".symdesk-frontmatter-{}-{counter}.tmp",
            std::process::id()
        ));
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        match options.open(&candidate) {
            Ok(file) => {
                file_and_path = Some((file, candidate));
                break;
            }
            Err(error) if error.kind() == io::ErrorKind::AlreadyExists => continue,
            Err(source) => return Err(MutationError::TempCreate { source }),
        }
    }
    let Some((mut file, temporary)) = file_and_path else {
        return Err(MutationError::TempCreate {
            source: io::Error::new(
                io::ErrorKind::AlreadyExists,
                "temporary name space exhausted",
            ),
        });
    };

    let result = (|| {
        file.write_all(data)
            .map_err(|source| MutationError::TempWrite { source })?;
        file.sync_all()
            .map_err(|source| MutationError::TempSync { source })?;
        drop(file);
        fs::rename(&temporary, path).map_err(|source| MutationError::TempRename { source })
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

fn atomic_directory(path: &Path) -> &Path {
    path.parent()
        .filter(|parent| !parent.as_os_str().is_empty())
        .unwrap_or_else(|| Path::new("."))
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{
        fs,
        time::{SystemTime, UNIX_EPOCH},
    };

    struct OwnedTempDir {
        path: PathBuf,
    }

    impl OwnedTempDir {
        fn new(name: &str) -> Self {
            let stamp = SystemTime::now()
                .duration_since(UNIX_EPOCH)
                .expect("clock")
                .as_nanos();
            let counter = TEMP_COUNTER.fetch_add(1, Ordering::Relaxed);
            let path = std::env::temp_dir().join(format!(
                "symdesk-mutation-{name}-{}-{stamp}-{counter}",
                std::process::id()
            ));
            fs::create_dir(&path).expect("create private test directory");
            Self { path }
        }
    }

    impl Drop for OwnedTempDir {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.path);
        }
    }

    fn temp_path(dir: &OwnedTempDir) -> PathBuf {
        dir.path.join("note.md")
    }

    #[test]
    fn scalar_preserves_opaque_body_bytes_and_go_quotes() {
        let dir = OwnedTempDir::new("scalar");
        let path = temp_path(&dir);
        let input = b"---\r\ntitle: old\r\n# comment\r\n---\r\nbody\xff\n";
        fs::write(&path, input).expect("write input");
        set_frontmatter_key(&path, "title", "a\\b\"c").expect("set key");
        let output = fs::read(&path).expect("read output");
        assert!(output.ends_with(b"body\xff\n"));
        assert!(
            output
                .windows(b"title: \"a\\b\\\"c\"".len())
                .any(|window| { window == b"title: \"a\\b\\\"c\"" })
        );
    }

    #[test]
    fn typed_values_are_inline_and_atomic_write_changes_mode_on_unix() {
        let dir = OwnedTempDir::new("typed");
        let path = temp_path(&dir);
        fs::write(&path, b"---\nold: value\n---\nbody\n").expect("write input");
        set_frontmatter_value(
            &path,
            "items",
            &Value::Sequence(vec![Value::from("one"), Value::from(2_i64), Value::Null]),
        )
        .expect("set value");
        let output = fs::read(&path).expect("read output");
        assert!(
            output
                .windows(b"items: [\"one\", 2, null]".len())
                .any(|window| { window == b"items: [\"one\", 2, null]" })
        );
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(
                fs::metadata(&path).expect("metadata").permissions().mode() & 0o777,
                0o600
            );
        }
    }

    #[test]
    fn failed_atomic_rename_cleans_temporary_file() {
        let dir = OwnedTempDir::new("failure");
        let target = dir.path.join("target");
        fs::create_dir_all(&target).expect("create target directory");
        let temporary_files = || {
            fs::read_dir(&dir.path)
                .expect("read temporary parent")
                .filter_map(Result::ok)
                .filter(|entry| {
                    entry
                        .file_name()
                        .to_string_lossy()
                        .starts_with(".symdesk-frontmatter-")
                })
                .count()
        };
        let before = temporary_files();
        let result = write_atomic(&target, b"cannot replace directory");
        assert!(matches!(result, Err(MutationError::TempRename { .. })));
        assert_eq!(temporary_files(), before);
    }

    #[test]
    fn bare_relative_paths_use_the_current_directory_without_changing_it() {
        assert_eq!(atomic_directory(Path::new("note.md")), Path::new("."));
        assert_eq!(
            atomic_directory(Path::new("notes/note.md")),
            Path::new("notes")
        );
    }
}
