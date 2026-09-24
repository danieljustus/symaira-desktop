#![deny(unsafe_code)]

//! Authoritative retention state used by retention evaluation and acceptance.

use std::{
    io::{self, Read},
    path::{Component, Path, PathBuf},
};

use cap_std::fs::Dir;
use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::{
    parse_bytes, parse_dataset_handle,
    retention::{self, DocMeta, RawSource},
    secure_path,
};

const DATASET_TYPE: &str = "dataset";
const DATASET_RAW_DIR: &str = "datasets";
const MAX_ROOT_READ_BYTES: u64 = 64 << 20;

/// The current authoritative bytes and metadata bound to a staged retention item.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct RetentionState {
    pub meta: DocMeta,
    pub rule_name: String,
    pub fingerprint: String,
    pub dataset: bool,
}

/// Fail-closed errors returned while resolving authoritative retention state.
#[derive(Debug, Error)]
pub enum RetentionStateError {
    #[error("retention state requires a vault")]
    VaultRequired,
    #[error("invalid retention path {0:?}")]
    InvalidPath(String),
    #[error(transparent)]
    UnsafePath(#[from] crate::SecurePathError),
    #[error("{0}")]
    Parse(String),
    #[error("{0}")]
    DatasetContract(String),
    #[error("dataset raw source {slug}/{name} is a symlink")]
    RawSourceSymlink { slug: String, name: String },
    #[error("vault path is not a regular file: {0}")]
    NotRegular(String),
    #[error("vault file exceeds {MAX_ROOT_READ_BYTES} byte read limit: {0}")]
    ReadLimit(String),
    #[error(transparent)]
    Io(#[from] io::Error),
}

impl RetentionStateError {
    /// Stable error class used by the cross-language contract fixture.
    #[must_use]
    pub fn class(&self) -> &'static str {
        match self {
            Self::VaultRequired | Self::InvalidPath(_) => "invalid_path",
            Self::UnsafePath(_) => "unsafe_path",
            Self::Parse(_) => "parse",
            Self::DatasetContract(_) => "dataset_contract",
            Self::RawSourceSymlink { .. } => "symlink",
            Self::Io(error) if error.kind() == io::ErrorKind::NotFound => "not_found",
            Self::NotRegular(_) | Self::ReadLimit(_) | Self::Io(_) => "filesystem",
        }
    }
}

/// Reads current authoritative vault state for a vault-relative path.
///
/// Every filesystem read is performed through a `cap_std::fs::Dir` rooted at
/// `vault_root`. The path is also passed through the existing canonical
/// confinement check so lexical traversal and symlink escapes match the Go
/// oracle's fail-closed behavior.
///
/// # Errors
///
/// Returns an error for invalid or escaping paths, malformed documents,
/// mismatched dataset handles, raw-source symlinks, and filesystem failures.
pub fn retention_state(
    vault_root: &Path,
    rel_path: &str,
) -> Result<RetentionState, RetentionStateError> {
    if vault_root.as_os_str().is_empty() {
        return Err(RetentionStateError::VaultRequired);
    }
    let rel_path = normalize_rel_path(rel_path);
    if rel_path.is_empty() || Path::new(&rel_path).is_absolute() {
        return Err(RetentionStateError::InvalidPath(rel_path));
    }

    let _ = secure_path(vault_root, &rel_path)?;
    let root = Dir::open_ambient_dir(vault_root, cap_std::ambient_authority())?;
    let data = read_regular(&root, &rel_path)?;
    let document = parse_bytes(&rel_path, &data)
        .map_err(|error| RetentionStateError::Parse(error.to_string()))?;

    if document.document_type == DATASET_TYPE {
        return dataset_retention_state(vault_root, &root, &rel_path, &data);
    }

    Ok(RetentionState {
        meta: retention::doc_meta_from_document(&document),
        rule_name: String::new(),
        fingerprint: retention::fingerprint(
            None,
            &[RawSource {
                path: rel_path,
                data,
            }],
        ),
        dataset: false,
    })
}

fn dataset_retention_state(
    vault_root: &Path,
    root: &Dir,
    rel_path: &str,
    handle_data: &[u8],
) -> Result<RetentionState, RetentionStateError> {
    let handle = parse_dataset_handle(rel_path, handle_data)
        .map_err(|error| RetentionStateError::DatasetContract(error.to_string()))?;
    let canonical_path = path_to_slash(&clean_relative(
        &Path::new(DATASET_RAW_DIR).join(format!("{}.md", handle.slug)),
    ));
    if rel_path != canonical_path {
        return Err(RetentionStateError::DatasetContract(format!(
            "dataset handle path {rel_path:?} does not match dataset {:?}",
            handle.slug
        )));
    }

    let raw_dir = clean_relative(&Path::new(DATASET_RAW_DIR).join(&handle.slug));
    let raw_dir_slash = path_to_slash(&raw_dir);
    let _ = secure_path(vault_root, &raw_dir_slash)?;
    let mut sources = Vec::new();
    match root.read_dir(&raw_dir) {
        Ok(entries) => {
            for entry in entries {
                let entry = entry?;
                let name = entry.file_name().to_string_lossy().into_owned();
                let file_type = entry.file_type()?;
                if file_type.is_dir() || !has_csv_extension(&name) {
                    continue;
                }
                if file_type.is_symlink() {
                    return Err(RetentionStateError::RawSourceSymlink {
                        slug: handle.slug,
                        name,
                    });
                }
                let source_rel = path_to_slash(&raw_dir.join(&name));
                let data = read_regular(root, &source_rel)?;
                sources.push(RawSource {
                    path: source_rel,
                    data,
                });
            }
        }
        Err(error) if error.kind() == io::ErrorKind::NotFound => {}
        Err(error) => return Err(RetentionStateError::Io(error)),
    }
    sources.sort_by(|left, right| left.path.cmp(&right.path));

    Ok(RetentionState {
        meta: DocMeta {
            path: rel_path.to_owned(),
            title: handle.title,
            document_date: handle.coverage.to,
            created: handle.created,
            due_date: String::new(),
            status: String::new(),
            correspondent: String::new(),
            document_type: DATASET_TYPE.to_owned(),
            person: String::new(),
            tags: Vec::new(),
        },
        rule_name: handle.retention_rule,
        fingerprint: retention::fingerprint(Some(handle_data), &sources),
        dataset: true,
    })
}

fn read_regular(root: &Dir, rel_path: &str) -> Result<Vec<u8>, RetentionStateError> {
    let mut options = cap_std::fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt;
        // Match Go's rooted reader: opening a FIFO must not block before fstat.
        options.custom_flags(libc::O_NONBLOCK);
    }
    let mut file = root.open_with(Path::new(rel_path), &options)?;
    let metadata = file.metadata()?;
    if !metadata.is_file() {
        return Err(RetentionStateError::NotRegular(rel_path.to_owned()));
    }
    if metadata.len() > MAX_ROOT_READ_BYTES {
        return Err(RetentionStateError::ReadLimit(rel_path.to_owned()));
    }
    let mut data = Vec::with_capacity(usize::try_from(metadata.len()).unwrap_or(0));
    file.by_ref()
        .take(MAX_ROOT_READ_BYTES + 1)
        .read_to_end(&mut data)?;
    if u64::try_from(data.len()).unwrap_or(u64::MAX) > MAX_ROOT_READ_BYTES {
        return Err(RetentionStateError::ReadLimit(rel_path.to_owned()));
    }
    Ok(data)
}

fn normalize_rel_path(path: &str) -> String {
    let trimmed = path.trim();
    #[cfg(windows)]
    {
        trimmed.replace('\\', "/")
    }
    #[cfg(not(windows))]
    {
        trimmed.to_owned()
    }
}

fn clean_relative(path: &Path) -> PathBuf {
    let mut output = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                if matches!(output.components().next_back(), Some(Component::Normal(_))) {
                    let _ = output.pop();
                } else {
                    output.push(Component::ParentDir.as_os_str());
                }
            }
            Component::Normal(value) => output.push(value),
            Component::RootDir | Component::Prefix(_) => {}
        }
    }
    output
}

fn path_to_slash(path: &Path) -> String {
    path.to_string_lossy().replace('\\', "/")
}

fn has_csv_extension(name: &str) -> bool {
    PathBuf::from(name)
        .extension()
        .and_then(|extension| extension.to_str())
        .is_some_and(|extension| extension.eq_ignore_ascii_case("csv"))
}
