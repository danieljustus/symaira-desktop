//! Go's `retrieval.IndexDirectory`, `RemoveDirectory` and `WatchDirectory`:
//! external folders are indexed in place into the shared hybrid retrieval
//! database (never the vault sidecar), keyed by the file paths found on disk.
//!
//! Declared differences from the Go engine:
//! - Go prepares the files of one directory in parallel and prints their
//!   `Indexed:` lines in completion order; Rust processes them sequentially in
//!   walk order, which is one valid serialization of the Go behavior.
//! - Go shares one embedder (learned dimension and per-text cache) across the
//!   directory pass. The learned dimension is shared here too; the per-text
//!   cache is per file.
//! - Files whose bytes are not UTF-8 are rejected by the plain-text and
//!   Markdown section parsers instead of being stored lossily.

use std::{
    collections::{BTreeSet, HashSet},
    fs, io,
    path::{Path, PathBuf},
    sync::mpsc,
    time::{Duration, Instant},
};

use notify::{EventKind, RecursiveMode, Watcher, event::ModifyKind};
use symdesk_index::{
    MAX_RETRIEVAL_SOURCE_BYTES, RetrievalDocument, RetrievalSection, detect_sidecar_path,
    import_extraction_sidecar, materialize_byte_chunks, materialize_chunks,
    parse_csv_retrieval_sections, parse_document_retrieval_sections,
    parse_markdown_retrieval_sections, parse_text_retrieval_sections,
};
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::index_cli::{
    RetrievalContext, embed_chunks, go_path_extension, open_retrieval_context, stored_chunks,
};

const PLAIN_TEXT_EXTENSIONS: &[&str] = &[
    ".go", ".py", ".js", ".ts", ".json", ".yaml", ".yml", ".sh", ".css",
];
const DEBOUNCE: Duration = Duration::from_millis(500);
const STATUS_INTERVAL: Duration = Duration::from_secs(30);

/// Go's `os` error text without the Rust ` (os error N)` suffix.
fn os_error_text(error: &io::Error) -> String {
    match error.kind() {
        io::ErrorKind::NotFound => "no such file or directory".to_owned(),
        io::ErrorKind::PermissionDenied => "permission denied".to_owned(),
        io::ErrorKind::NotADirectory => "not a directory".to_owned(),
        _ => {
            let text = error.to_string();
            text.find(" (os error ")
                .map_or(text.clone(), |index| text[..index].to_owned())
        }
    }
}

fn user_friendly(context: &str, error: &str, suggestion: &str) -> String {
    format!("{context}: {error}\nHint: {suggestion}")
}

/// Go's `shouldSkipDir`.
fn should_skip_dir(name: &str) -> bool {
    (name.starts_with('.') && name != "." && name != "..")
        || matches!(
            name,
            "node_modules" | "dist" | "vendor" | "build" | "target"
        )
}

/// Go's `isWithinDir`: lexical, with a trailing separator guard.
fn is_within_dir(path: &str, dir: &str) -> bool {
    path == dir
        || path
            .strip_prefix(dir)
            .is_some_and(|rest| rest.starts_with(std::path::MAIN_SEPARATOR))
}

fn lower_extension(path: &str) -> String {
    go_path_extension(path).to_lowercase()
}

fn is_supported_extension(extension: &str) -> bool {
    PLAIN_TEXT_EXTENSIONS.contains(&extension)
        || symdesk_core::document_format::is_supported(extension)
}

/// Go's `UnsupportedDocumentSkipMessage`, or `None` when the extension is not
/// a recognized-but-unindexed document format.
fn unsupported_skip_message(path: &str, extension: &str) -> Option<String> {
    let format = symdesk_core::document_format::unsupported(extension)?;
    let normalized = symdesk_core::document_format::normalize_extension(extension);
    Some(format!("Skipping {path}: {normalized} ({})", format.reason))
}

/// Go's `canonicalIndexDirectory`.
fn canonical_index_directory(dir: &Path) -> Result<String, String> {
    let mut absolute = if dir.is_absolute() {
        dir.to_path_buf()
    } else {
        std::env::current_dir()
            .map_err(|error| {
                user_friendly(
                    "failed to get absolute path",
                    &os_error_text(&error),
                    "Check that the path is valid and try again",
                )
            })?
            .join(dir)
    };
    absolute = clean(&absolute);
    // Preserve lexical absolute paths unless the directory itself is a link.
    if fs::symlink_metadata(&absolute).is_ok_and(|metadata| metadata.file_type().is_symlink()) {
        absolute = fs::canonicalize(&absolute).map_err(|error| {
            user_friendly(
                "cannot access directory",
                &format!("lstat {}: {}", absolute.display(), os_error_text(&error)),
                "Check that the path is valid and try again",
            )
        })?;
    }
    let metadata = fs::metadata(&absolute).map_err(|error| {
        user_friendly(
            "cannot access directory",
            &format!("stat {}: {}", absolute.display(), os_error_text(&error)),
            "Check file permissions and ensure the directory exists",
        )
    })?;
    if !metadata.is_dir() {
        return Err(format!(
            "target path is not a directory: {}",
            absolute.display()
        ));
    }
    fs::read_dir(&absolute)
        .and_then(|mut entries| entries.next().transpose())
        .map_err(|error| {
            format!(
                "source directory is not readable: open {}: {}",
                absolute.display(),
                os_error_text(&error)
            )
        })?;
    absolute
        .to_str()
        .map(str::to_owned)
        .ok_or_else(|| format!("non-UTF-8 directory path: {absolute:?}"))
}

/// Lexical `filepath.Clean` for absolute paths.
fn clean(path: &Path) -> PathBuf {
    let mut output = PathBuf::new();
    for component in path.components() {
        match component {
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                output.pop();
            }
            other => output.push(other.as_os_str()),
        }
    }
    output
}

struct Walk {
    found: BTreeSet<String>,
    issues: Vec<String>,
}

fn walk_directory(root: &str) -> Result<Walk, String> {
    let mut walk = Walk {
        found: BTreeSet::new(),
        issues: Vec::new(),
    };
    let name = Path::new(root).file_name().map_or_else(
        || root.to_owned(),
        |name| name.to_string_lossy().into_owned(),
    );
    if should_skip_dir(&name) {
        return Ok(walk);
    }
    walk_entries(Path::new(root), &mut walk)
        .map_err(|error| format!("failed walking directory: {}", walk_error_text(&error)))?;
    Ok(walk)
}

fn walk_error_text(error: &io::Error) -> String {
    format!("open: {}", os_error_text(error))
}

fn walk_entries(directory: &Path, walk: &mut Walk) -> io::Result<()> {
    let mut entries = fs::read_dir(directory)?.collect::<io::Result<Vec<_>>>()?;
    entries.sort_by_key(fs::DirEntry::file_name);
    for entry in entries {
        let file_type = entry.file_type()?;
        let name = entry.file_name().to_string_lossy().into_owned();
        let path = entry.path();
        if file_type.is_dir() {
            if should_skip_dir(&name) {
                continue;
            }
            walk_entries(&path, walk)?;
            continue;
        }
        // File symlinks are never followed: a source must not index content
        // outside its registered directory.
        if file_type.is_symlink() {
            continue;
        }
        let shown = path.to_string_lossy().into_owned();
        let extension = lower_extension(&shown);
        if is_supported_extension(&extension) {
            if let Ok(metadata) = entry.metadata()
                && metadata.len() > MAX_RETRIEVAL_SOURCE_BYTES as u64
            {
                eprintln!(
                    "Skipping {shown}: file size {} exceeds {MAX_RETRIEVAL_SOURCE_BYTES} byte limit",
                    metadata.len()
                );
                continue;
            }
            walk.found.insert(shown);
        } else if let Some(message) = unsupported_skip_message(&shown, &extension) {
            eprintln!("{message}");
            walk.issues.push(message);
        }
    }
    Ok(())
}

/// Outcome of preparing one file: the failure text printed as `Warning: ...`
/// plus whether it was a DRM-protected document.
struct IndexFailure {
    message: String,
    drm: bool,
}

impl IndexFailure {
    fn plain(message: String) -> Self {
        Self {
            message,
            drm: false,
        }
    }
}

enum Sections {
    Text(Vec<RetrievalSection>),
    Bytes(Vec<symdesk_index::RetrievalByteSection>),
}

/// Go's `ParseFileSections` for a path already read into `bytes`.
fn file_sections(path: &str, extension: &str, bytes: &[u8]) -> Result<Sections, String> {
    let limited = &bytes[..bytes.len().min(MAX_RETRIEVAL_SOURCE_BYTES + 1)];
    match extension {
        ".pdf" => parse_document_retrieval_sections(path, extension, bytes)
            .map(Sections::Bytes)
            .map_err(|error| error.to_string()),
        ".md" | ".markdown" => {
            if bytes.len() > MAX_RETRIEVAL_SOURCE_BYTES {
                return Err(format!(
                    "file {path} exceeds {MAX_RETRIEVAL_SOURCE_BYTES} byte limit ({} bytes)",
                    bytes.len()
                ));
            }
            parse_markdown_retrieval_sections(path, bytes)
                .map(Sections::Text)
                .map_err(|error| error.to_string())
        }
        ".csv" => parse_csv_retrieval_sections(path, limited)
            .map(Sections::Bytes)
            .map_err(|error| error.to_string()),
        ".docx" | ".xlsx" | ".pptx" | ".odt" | ".ods" | ".odp" | ".epub" => {
            parse_document_retrieval_sections(path, extension, bytes)
                .map(Sections::Bytes)
                .map_err(|error| error.to_string())
        }
        ".html" | ".htm" | ".rtf" => parse_document_retrieval_sections(path, extension, limited)
            .map(Sections::Bytes)
            .map_err(|error| error.to_string()),
        _ => parse_text_retrieval_sections(path, limited)
            .map(Sections::Text)
            .map_err(|error| error.to_string()),
    }
}

/// Go's `IndexFile` (`prepareIndex` + `commitIndex`) for one external file.
fn index_file(
    context: &RetrievalContext,
    path: &str,
    expected_dimension: &mut Option<usize>,
) -> Result<(), IndexFailure> {
    let (config, database, runtime) = context;
    let bytes = fs::read(path).map_err(|error| {
        IndexFailure::plain(format!(
            "failed to compute hash for {path}: {}",
            os_error_text(&error)
        ))
    })?;
    let hash = symdesk_vault::sha256_hex(&bytes);
    let existing = database.get_document(path).map_err(|error| {
        IndexFailure::plain(format!("failed to query document from DB: {error}"))
    })?;
    if existing.as_ref().is_some_and(|stored| stored.hash == hash)
        && !database
            .count_pending_chunks_for_document(path)
            .is_ok_and(|pending| pending > 0)
    {
        return Ok(());
    }

    let extension = lower_extension(path);
    let sections = file_sections(path, &extension, &bytes).map_err(|error| IndexFailure {
        drm: error.contains("DRM-protected"),
        message: format!("failed to parse {path}: {error}"),
    })?;
    let chunks = match &sections {
        Sections::Text(sections) => materialize_chunks(path, sections),
        Sections::Bytes(sections) => materialize_byte_chunks(path, sections),
    };
    let texts = chunks
        .iter()
        .map(|chunk| chunk.content.clone())
        .collect::<Vec<_>>();
    let raw_contents = chunks
        .iter()
        .map(|chunk| chunk.raw_content.clone())
        .collect::<Vec<_>>();
    let embeddings = runtime.block_on(embed_chunks(
        config,
        &texts,
        &raw_contents,
        expected_dimension,
    ));
    let stored = stored_chunks(path, chunks, embeddings, &config.model);
    let record = RetrievalDocument {
        path: path.to_owned(),
        hash,
        updated_at: OffsetDateTime::now_utc()
            .format(&Rfc3339)
            .unwrap_or_default(),
    };
    if existing.is_some() {
        database
            .replace_document_chunks_raw(&record, &stored, &raw_contents)
            .map_err(|error| {
                IndexFailure::plain(format!("failed to delete old document version: {error}"))
            })?;
    } else {
        database.save_document(&record).map_err(|error| {
            IndexFailure::plain(format!("failed to save document metadata: {error}"))
        })?;
        database
            .save_chunks_raw(&stored, &raw_contents)
            .map_err(|error| IndexFailure::plain(format!("failed to save chunks: {error}")))?;
    }

    // Go detects a sidecar only for `.md` paths, from the whole file text.
    if let Some(sidecar) = detect_sidecar_path(path, &String::from_utf8_lossy(&bytes)) {
        let shown = sidecar.to_string_lossy();
        match import_extraction_sidecar(database, path, &sidecar) {
            Ok(()) => eprintln!("Imported extraction sidecar: {shown}"),
            Err(error) => eprintln!(
                "Warning: failed to import extraction sidecar {shown} for {path}: {error}"
            ),
        }
    }
    let pending = stored
        .iter()
        .filter(|chunk| chunk.embedding_pending)
        .count();
    if pending > 0 {
        eprintln!(
            "Indexed: {path} ({} chunks, {pending} pending — embedding backend unavailable, will re-embed when available)",
            stored.len()
        );
    } else {
        eprintln!("Indexed: {path} ({} chunks)", stored.len());
    }
    Ok(())
}

fn index_directory_in(context: &RetrievalContext, directory: &Path) -> Result<(), String> {
    let absolute = canonical_index_directory(directory)?;
    let mut walk = walk_directory(&absolute)?;
    let database = &context.1;
    let existing = database
        .list_documents()
        .map_err(|error| format!("failed listing existing documents: {error}"))?;

    let mut expected_dimension = context.0.embedding_dim;
    for path in &walk.found {
        if let Err(failure) = index_file(context, path, &mut expected_dimension) {
            eprintln!("Warning: {}", failure.message);
            if failure.drm {
                walk.issues.push(format!(
                    "DRM-protected document {path}: {}",
                    failure.message
                ));
            }
        }
    }

    // Orphan detection: drop documents whose file no longer exists on disk.
    for document in existing {
        if is_within_dir(&document.path, &absolute) && !walk.found.contains(&document.path) {
            database.delete_document(&document.path).map_err(|error| {
                format!(
                    "failed to delete orphaned document {}: {error}",
                    document.path
                )
            })?;
            eprintln!("Removed deleted file from index: {}", document.path);
        }
    }

    if walk.issues.is_empty() {
        Ok(())
    } else {
        Err(format!(
            "document format issues:\n{}",
            walk.issues.join("\n")
        ))
    }
}

/// Go's `retrieval.IndexDirectory`.
pub(crate) fn index_directory(directory: &Path) -> Result<(), String> {
    let context = open_retrieval_context()?;
    index_directory_in(&context, directory)
}

/// Go's `removalIdentity`: resolve existing ancestors even when the path is
/// gone; only non-existence allows a lexical suffix.
fn removal_identity(path: &Path) -> io::Result<PathBuf> {
    let mut candidate = path.to_path_buf();
    let mut suffix = Vec::new();
    loop {
        match fs::canonicalize(&candidate) {
            Ok(mut resolved) => {
                for part in suffix.iter().rev() {
                    resolved.push(part);
                }
                return Ok(clean(&resolved));
            }
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                let Some(parent) = candidate.parent().map(Path::to_path_buf) else {
                    return Err(error);
                };
                let Some(name) = candidate.file_name().map(std::ffi::OsStr::to_os_string) else {
                    return Err(error);
                };
                suffix.push(name);
                candidate = parent;
            }
            Err(error) => return Err(error),
        }
    }
}

/// Go's `retrieval.RemoveDirectory`: documents whose canonical identity is
/// within `directory` are dropped; the folder on disk is never touched.
pub(crate) fn remove_directory(directory: &Path) -> Result<usize, String> {
    let (_, database, _) = open_retrieval_context()?;
    let absolute = if directory.is_absolute() {
        directory.to_path_buf()
    } else {
        std::env::current_dir()
            .map_err(|error| format!("resolve source directory: {}", os_error_text(&error)))?
            .join(directory)
    };
    let absolute = clean(&absolute);
    let root = removal_identity(&absolute)
        .map_err(|error| format!("resolve source directory: {}", os_error_text(&error)))?;
    let (absolute, root) = (
        absolute.to_string_lossy().into_owned(),
        root.to_string_lossy().into_owned(),
    );
    let documents = database
        .list_documents()
        .map_err(|error| format!("failed listing existing documents: {error}"))?;
    let mut removed = HashSet::new();
    for document in documents {
        let path = Path::new(&document.path);
        // Relative labels and URLs are not local source identities.
        if !path.is_absolute() {
            continue;
        }
        let parent = path.parent().unwrap_or(path);
        let parent = match removal_identity(parent) {
            Ok(parent) => parent,
            Err(error) => {
                if !is_within_dir(&document.path, &absolute)
                    && !is_within_dir(&document.path, &root)
                {
                    continue;
                }
                return Err(format!(
                    "resolve indexed document directory: {}",
                    os_error_text(&error)
                ));
            }
        };
        let identity = parent
            .join(path.file_name().unwrap_or_default())
            .to_string_lossy()
            .into_owned();
        if !is_within_dir(&identity, &root) {
            continue;
        }
        database.delete_document(&document.path).map_err(|error| {
            format!(
                "failed to remove indexed document {}: {error}",
                document.path
            )
        })?;
        removed.insert(identity);
    }
    Ok(removed.len())
}

/// Go's `applyIncrementalChanges`.
fn apply_incremental_changes(
    context: &RetrievalContext,
    absolute: &str,
    changed: &BTreeSet<String>,
    expected_dimension: &mut Option<usize>,
) -> Result<(), String> {
    let database = &context.1;
    let (mut indexed, mut removed) = (0usize, 0usize);
    for path in changed {
        if !is_within_dir(path, absolute) {
            continue;
        }
        let metadata = match fs::metadata(path) {
            Ok(metadata) => metadata,
            Err(error) if error.kind() == io::ErrorKind::NotFound => {
                match database.delete_document(path) {
                    Ok(()) => removed += 1,
                    Err(error) => eprintln!("Warning: failed to delete {path}: {error}"),
                }
                continue;
            }
            Err(error) => {
                eprintln!("Warning: stat {path}: {}", os_error_text(&error));
                continue;
            }
        };
        if metadata.is_dir() {
            continue;
        }
        let extension = lower_extension(path);
        if !is_supported_extension(&extension) {
            if let Some(message) = unsupported_skip_message(path, &extension) {
                eprintln!("{message}");
            }
            continue;
        }
        if let Err(failure) = index_file(context, path, expected_dimension) {
            eprintln!("Warning: {}", failure.message);
            continue;
        }
        indexed += 1;
    }

    // A rename out of the watched tree produces no second event for the
    // original path, so sweep documents whose backing file has disappeared.
    let documents = database
        .list_documents()
        .map_err(|error| format!("failed listing existing documents: {error}"))?;
    for document in documents {
        if !is_within_dir(&document.path, absolute) || fs::metadata(&document.path).is_ok() {
            continue;
        }
        match database.delete_document(&document.path) {
            Ok(()) => removed += 1,
            Err(error) => eprintln!("Warning: failed to drop orphan {}: {error}", document.path),
        }
    }
    if indexed > 0 || removed > 0 {
        eprintln!("Watch: indexed={indexed} removed={removed}");
    }
    Ok(())
}

fn watch_directories(
    watcher: &mut impl Watcher,
    directory: &Path,
    is_root: bool,
) -> Result<(), String> {
    let name = directory
        .file_name()
        .map_or_else(String::new, |name| name.to_string_lossy().into_owned());
    if should_skip_dir(&name) {
        return Ok(());
    }
    let _ = is_root;
    watcher
        .watch(directory, RecursiveMode::NonRecursive)
        .map_err(|error| error.to_string())?;
    let mut entries = fs::read_dir(directory)
        .map_err(|error| os_error_text(&error))?
        .collect::<io::Result<Vec<_>>>()
        .map_err(|error| os_error_text(&error))?;
    entries.sort_by_key(fs::DirEntry::file_name);
    for entry in entries {
        if entry
            .file_type()
            .map_err(|error| os_error_text(&error))?
            .is_dir()
        {
            watch_directories(watcher, &entry.path(), false)?;
        }
    }
    Ok(())
}

/// True when an event path lies in a directory Go never watches.
fn in_skipped_directory(path: &Path, absolute: &Path) -> bool {
    let Ok(relative) = path.strip_prefix(absolute) else {
        return false;
    };
    let mut components = relative.components().peekable();
    while let Some(component) = components.next() {
        if components.peek().is_some() && should_skip_dir(&component.as_os_str().to_string_lossy())
        {
            return true;
        }
    }
    false
}

fn status_time() -> String {
    let now = OffsetDateTime::now_local().unwrap_or_else(|_| OffsetDateTime::now_utc());
    format!("{:02}:{:02}:{:02}", now.hour(), now.minute(), now.second())
}

/// Go's `retrieval.WatchDirectory`. Like Go's CLI it runs until the process is
/// terminated (the Go root command carries no signal-aware context).
pub(crate) fn watch_directory(directory: &Path) -> Result<(), String> {
    let context = open_retrieval_context()?;
    let absolute = canonical_index_directory(directory)?;
    let (sender, receiver) = mpsc::channel();
    let mut watcher = notify::recommended_watcher(move |event| {
        let _ = sender.send(event);
    })
    .map_err(|error| {
        user_friendly(
            "failed to create file watcher",
            &error.to_string(),
            "Ensure you have the necessary permissions to watch the directory",
        )
    })?;
    watch_directories(&mut watcher, Path::new(&absolute), true)
        .map_err(|error| format!("failed to setup watchers: {error}"))?;

    eprintln!("Performing initial sync for: {absolute}");
    index_directory_in(&context, Path::new(&absolute))
        .map_err(|error| format!("initial sync failed: {error}"))?;

    let documents = context
        .1
        .list_documents()
        .map_err(|error| format!("failed to list documents: {error}"))?;
    let file_count = documents
        .iter()
        .filter(|document| is_within_dir(&document.path, &absolute))
        .count();
    eprintln!("Watching {file_count} files in {absolute}");

    let mut expected_dimension = context.0.embedding_dim;
    let mut pending: BTreeSet<String> = BTreeSet::new();
    let mut deadline: Option<Instant> = None;
    let mut next_status = Instant::now() + STATUS_INTERVAL;
    let mut last_change = status_time();
    loop {
        let now = Instant::now();
        let mut wake = next_status;
        if let Some(at) = deadline {
            wake = wake.min(at);
        }
        match receiver.recv_timeout(wake.saturating_duration_since(now)) {
            Ok(Ok(event)) => {
                let relevant = match event.kind {
                    EventKind::Create(_) | EventKind::Remove(_) => true,
                    EventKind::Modify(kind) => !matches!(kind, ModifyKind::Metadata(_)),
                    _ => false,
                };
                if !relevant {
                    continue;
                }
                let is_create = matches!(event.kind, EventKind::Create(_));
                let mut touched = false;
                for path in event.paths {
                    if in_skipped_directory(&path, Path::new(&absolute)) {
                        continue;
                    }
                    if is_create
                        && let Ok(metadata) = fs::metadata(&path)
                        && metadata.is_dir()
                    {
                        let name = path
                            .file_name()
                            .map_or_else(String::new, |name| name.to_string_lossy().into_owned());
                        if !should_skip_dir(&name)
                            && let Err(error) = watch_directories(&mut watcher, &path, false)
                        {
                            eprintln!(
                                "Warning: failed to watch directory {}: {error}",
                                path.display()
                            );
                        }
                    }
                    pending.insert(path.to_string_lossy().into_owned());
                    touched = true;
                }
                if touched {
                    last_change = status_time();
                    deadline = Some(Instant::now() + DEBOUNCE);
                }
            }
            Ok(Err(error)) => eprintln!("Watcher error: {error}"),
            Err(mpsc::RecvTimeoutError::Timeout) => {
                let now = Instant::now();
                if deadline.is_some_and(|at| now >= at) {
                    deadline = None;
                    let changed = std::mem::take(&mut pending);
                    if let Err(error) = apply_incremental_changes(
                        &context,
                        &absolute,
                        &changed,
                        &mut expected_dimension,
                    ) {
                        eprintln!("Incremental re-index error: {error}");
                    }
                }
                if now >= next_status {
                    eprintln!("Watching {file_count} files, last change at {last_change}");
                    next_status = now + STATUS_INTERVAL;
                }
            }
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                return Err("watcher event channel closed".to_owned());
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn skip_directories_follow_go() {
        for skipped in [
            ".git",
            ".hidden",
            "node_modules",
            "dist",
            "vendor",
            "build",
            "target",
        ] {
            assert!(should_skip_dir(skipped), "{skipped}");
        }
        for walked in ["src", "docs", ".", "..", "builds"] {
            assert!(!should_skip_dir(walked), "{walked}");
        }
    }

    #[test]
    fn within_dir_uses_a_separator_guard() {
        assert!(is_within_dir("/docs", "/docs"));
        assert!(is_within_dir("/docs/a.md", "/docs"));
        assert!(!is_within_dir("/docs2/a.md", "/docs"));
    }

    #[test]
    fn extensions_route_like_go() {
        assert_eq!(lower_extension("/x/UPPER.TXT"), ".txt");
        assert_eq!(lower_extension("/x/.csv"), ".csv");
        assert_eq!(lower_extension("/x/noext"), "");
        assert!(is_supported_extension(".go"));
        assert!(is_supported_extension(".text"));
        assert!(!is_supported_extension(".doc"));
        assert_eq!(
            unsupported_skip_message("/x/a.doc", ".doc").as_deref(),
            Some("Skipping /x/a.doc: .doc (legacy binary Office parser is not available)")
        );
        assert_eq!(unsupported_skip_message("/x/a.bin", ".bin"), None);
    }

    #[test]
    fn removal_identity_keeps_a_missing_suffix() {
        let root = std::env::temp_dir();
        let root = fs::canonicalize(root).expect("canonical temp dir");
        let missing = root.join("symdesk-removal-identity-missing/a/b");
        assert_eq!(removal_identity(&missing).expect("identity"), missing);
    }
}
