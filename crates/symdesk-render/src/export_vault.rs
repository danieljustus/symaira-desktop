//! Vault-backed export (Go `export.Note`, `export.View` and
//! `resolveTransclusions`): a note is resolved inside the vault, its `![[..]]`
//! embeds are expanded, and the result is shaped as Markdown, HTML or the
//! PDF source (frontmatter block + body).
//!
//! Error texts are the Go ones, including the `*embed ..*` and
//! `*Error loading ..*` lines that are rendered into the document instead of
//! being returned. Reads are confined like `vault.Root`: the path is resolved
//! through [`secure_path`] first (lexical traversal and symlink escapes are
//! rejected), a path that is not a regular file or exceeds the 64 MiB read
//! limit is refused, and only then is it read.
//!
//! Deliberate divergence: Go's `resolveTransclusions` never terminates on an
//! embed cycle (a note that embeds itself). The port stops with an error after
//! [`MAX_EMBED_EXPANSIONS`] expansions while an embed still remains, or when
//! the body passes [`MAX_EXPANDED_BYTES`]. No Go-recorded case can exist for
//! that input. A document that needs exactly [`MAX_EMBED_EXPANSIONS`]
//! expansions and then has no embed left still succeeds; the cap is a
//! declared limit (RENDER-002), not a Go behavior.
//!
//! Second declared divergence: a note body that is not valid UTF-8 is decoded
//! lossily by `symdesk_vault::parse_bytes` (invalid bytes become U+FFFD) while
//! Go keeps the raw bytes. The Go oracle fixture is JSON and cannot carry
//! such bytes, so no recorded case exists; the behavior is pinned by a Rust
//! unit test instead.

use std::{
    collections::BTreeMap,
    fs,
    io::Read as _,
    path::{Component, MAIN_SEPARATOR_STR, Path, PathBuf},
};

use symdesk_vault::{Document, go_path_error, parse_bytes, secure_path};

use crate::{
    export_html::{render_note_html, trim_go_space},
    export_markdown::markdown_table,
    export_pdf::pdf_frontmatter,
};

/// `vault.maxRootReadBytes`.
pub const MAX_ROOT_READ_BYTES: u64 = 64 << 20;
/// Bound on `![[..]]` expansions per document (Go has none, see module docs).
/// Exactly this many expansions succeed when no embed remains afterwards.
pub const MAX_EMBED_EXPANSIONS: usize = 10_000;
/// Bound on the expanded body size (Go has none, see module docs).
pub const MAX_EXPANDED_BYTES: usize = 64 << 20;

/// The parts of `dbviews.View` that `export.View` reads.
#[derive(Clone, Debug, Default)]
pub struct ViewSpec {
    pub name: String,
    pub columns: Vec<String>,
}

/// Go `export.Note`. `format` is `"pdf"`, `"html"` or anything else for
/// Markdown, exactly as `Options.Format` is.
///
/// # Errors
///
/// Returns the Go error text when the path escapes the vault or the note
/// cannot be read or parsed.
pub fn export_note(root: &Path, rel_path: &str, format: &str) -> Result<Vec<u8>, String> {
    let (doc, body) = note_markdown(root, rel_path)?;
    if format == "pdf" {
        // The PDF templates typeset the title from frontmatter; a body
        // heading would print it twice.
        let front = pdf_frontmatter(&doc.frontmatter, &doc.title)?;
        return Ok(format!("{front}{body}").into_bytes());
    }
    let mut out = String::new();
    if !doc.title.is_empty() {
        out.push_str("# ");
        out.push_str(&doc.title);
        out.push_str("\n\n");
    }
    out.push_str(&body);
    if format == "html" {
        return Ok(render_note_html(&out).into_bytes());
    }
    Ok(out.into_bytes())
}

/// Go `export.View`: the table of `rows` followed by every row's note.
///
/// # Errors
///
/// Only the PDF frontmatter encoding can fail; unreadable notes are rendered
/// as `*Error loading ..*` lines, as in Go.
pub fn export_view(
    root: &Path,
    view: &ViewSpec,
    rows: &[BTreeMap<String, String>],
    format: &str,
) -> Result<Vec<u8>, String> {
    let is_pdf = format == "pdf";
    // A PDF carries one frontmatter block at the top; embedded notes
    // contribute body only.
    let note_format = if is_pdf { "" } else { format };

    let mut out = String::new();
    if !view.name.is_empty() && !is_pdf {
        out.push_str(&format!("# {}\n\n", view.name));
    }
    if rows.is_empty() {
        out.push_str("No matching notes.\n");
    } else {
        out.push_str(&markdown_table(&view.columns, rows));
        out.push('\n');
        for row in rows {
            let path = row.get("path").map_or("", String::as_str);
            if path.is_empty() {
                continue;
            }
            let rel_path = if Path::new(path).is_absolute() {
                go_rel(root, Path::new(path)).unwrap_or_default()
            } else {
                path.to_owned()
            };
            match export_note(root, &rel_path, note_format) {
                Err(error) => out.push_str(&format!("\n*Error loading {rel_path}: {error}*\n")),
                Ok(markdown) => {
                    // A rule between notes; in a PDF it must not be the first
                    // thing in the document or it would open a frontmatter.
                    out.push_str("---\n\n");
                    out.push_str(&String::from_utf8_lossy(&markdown));
                    out.push('\n');
                }
            }
        }
    }

    if is_pdf {
        let front = pdf_frontmatter(&BTreeMap::new(), &view.name)?;
        return Ok(format!("{front}{out}").into_bytes());
    }
    if format == "html" {
        return Ok(render_note_html(&out).into_bytes());
    }
    Ok(out.into_bytes())
}

fn note_markdown(root: &Path, rel_path: &str) -> Result<(Document, String), String> {
    let abs = secure_path(root, rel_path).map_err(|error| error.to_string())?;
    let doc = parse_file_in_root(root, &abs)?;
    let body = resolve_transclusions(root, &doc.body)?;
    Ok((doc, body))
}

/// Go `resolveTransclusions`: replace the leftmost `![[ref]]` until none is
/// left; replacements are scanned again, so nested embeds expand.
///
/// # Errors
///
/// Only the cycle bounds of this port (see module docs) can fail.
pub fn resolve_transclusions(root: &Path, body: &str) -> Result<String, String> {
    resolve_transclusions_bounded(root, body, MAX_EMBED_EXPANSIONS, MAX_EXPANDED_BYTES)
}

/// [`resolve_transclusions`] with explicit cycle bounds, so a test can reach
/// them without expanding tens of megabytes.
///
/// # Errors
///
/// Fails when an embed is still left after `max_expansions` replacements or
/// the body grew past `max_bytes`.
pub fn resolve_transclusions_bounded(
    root: &Path,
    body: &str,
    max_expansions: usize,
    max_bytes: usize,
) -> Result<String, String> {
    let mut body = body.to_owned();
    for _ in 0..max_expansions {
        // Go rescans the whole body every iteration: a replacement may start
        // with (or be empty before) `]` and complete a `![[x]` that began
        // arbitrarily far earlier, so the scan restarts at 0.
        let Some((start, inner_start, inner_end)) = find_embed(&body, 0) else {
            return Ok(body);
        };
        let inner = &body[inner_start..inner_end];
        // Strip a heading or block fragment.
        let reference = inner.split_once('#').map_or(inner, |(head, _)| head);
        let reference = trim_go_space(reference);
        let reference = if reference.ends_with(".md") {
            reference.to_owned()
        } else {
            format!("{reference}.md")
        };
        let replacement = embed_replacement(root, &reference);
        // The match ends two bytes after the capture ("]]").
        body.replace_range(start..inner_end + 2, &replacement);
        if body.len() > max_bytes {
            return Err(format!(
                "embed expansion exceeds {max_bytes} bytes (embed cycle?)"
            ));
        }
    }
    // The cap is spent: only an embed that is still there is an error.
    if find_embed(&body, 0).is_none() {
        return Ok(body);
    }
    Err(format!(
        "embed expansion exceeds {max_expansions} embeds (embed cycle?)"
    ))
}

fn embed_replacement(root: &Path, reference: &str) -> String {
    match secure_path(root, reference) {
        Err(error) => format!("*embed error: {error}*"),
        Ok(candidate) => {
            if !reachable(&candidate) {
                return format!("*embed not found: {reference}*");
            }
            match parse_file_in_root(root, &candidate) {
                Err(error) => format!("*embed error: {error}*"),
                Ok(doc) => doc.body,
            }
        }
    }
}

/// Leftmost match of `!\[\[([^\]]+)\]\]` at or after `from`: returns the byte
/// offsets of the match start, the capture start and the capture end.
fn find_embed(body: &str, from: usize) -> Option<(usize, usize, usize)> {
    let mut at = from;
    while let Some(offset) = body[at..].find("![[") {
        let start = at + offset;
        let inner = start + 3;
        // No ']' anywhere after: no later candidate can close either.
        let length = body[inner..].find(']')?;
        let close = inner + length;
        if length > 0 && body[close..].starts_with("]]") {
            return Some((start, inner, close));
        }
        // Every candidate whose run reaches this ']' fails the same way.
        at = if length == 0 { start + 1 } else { close };
    }
    None
}

/// Go `vault.StatInRoot` on a path already resolved by `secure_path`: it only
/// has to tell reachable from not reachable. A dangling symlink is not.
fn reachable(path: &Path) -> bool {
    let Ok(link) = fs::symlink_metadata(path) else {
        return false;
    };
    !link.file_type().is_symlink() || fs::metadata(path).is_ok()
}

/// Go `vault.ParseFileInRoot` for a canonical path below `root`.
fn parse_file_in_root(root: &Path, path: &Path) -> Result<Document, String> {
    let data = read_in_root(root, path)?;
    parse_bytes(&path.to_string_lossy(), &data).map_err(|error| error.to_string())
}

fn read_in_root(root: &Path, path: &Path) -> Result<Vec<u8>, String> {
    let shown = path.display();
    let canonical_root = fs::canonicalize(root).map_err(|error| error.to_string())?;
    let relative = path
        .strip_prefix(&canonical_root)
        .or_else(|_| path.strip_prefix(root))
        .map_err(|_| format!("vault path escapes root: {shown}"))?;
    let relative = if relative.as_os_str().is_empty() {
        PathBuf::from(".")
    } else {
        relative.to_path_buf()
    };
    let open_error = |error: &std::io::Error| {
        format!(
            "open vault file {shown}: {}",
            go_path_error("openat", &relative, error)
        )
    };
    // Open without blocking, then judge the type on the opened handle: a FIFO
    // swapped in after `secure_path` must neither block the open nor be read.
    let file = open_nonblocking(path).map_err(|error| open_error(&error))?;
    let metadata = file.metadata().map_err(|error| open_error(&error))?;
    if !metadata.is_file() {
        return Err(format!("vault path is not a regular file: {shown}"));
    }
    if metadata.len() > MAX_ROOT_READ_BYTES {
        return Err(too_large(path));
    }
    let mut data = Vec::new();
    file.take(MAX_ROOT_READ_BYTES + 1)
        .read_to_end(&mut data)
        .map_err(|error| {
            format!(
                "read vault file {shown}: {}",
                go_path_error("read", &relative, &error)
                    .rsplit_once(": ")
                    .map_or_else(String::new, |(_, text)| text.to_owned())
            )
        })?;
    if data.len() as u64 > MAX_ROOT_READ_BYTES {
        return Err(too_large(path));
    }
    Ok(data)
}

fn open_nonblocking(path: &Path) -> std::io::Result<fs::File> {
    let mut options = fs::OpenOptions::new();
    options.read(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt as _;
        // Opening a FIFO for reading would otherwise wait for a writer.
        options.custom_flags(libc::O_NONBLOCK);
    }
    options.open(path)
}

fn too_large(path: &Path) -> String {
    format!(
        "vault file exceeds {MAX_ROOT_READ_BYTES} byte read limit: {}",
        path.display()
    )
}

/// Go `filepath.Rel(base, target)` for the lexical case. Go returns an error
/// (and `export.View` then uses the empty path) when one side is absolute and
/// the other is not, or when `base` still needs `..` to be reached.
fn go_rel(base: &Path, target: &Path) -> Option<String> {
    if base.is_absolute() != target.is_absolute() {
        return None;
    }
    let base = clean_components(base);
    let target = clean_components(target);
    let common = base
        .iter()
        .zip(&target)
        .take_while(|(left, right)| left == right)
        .count();
    if base[common..].iter().any(|part| part == "..") {
        return None;
    }
    let mut parts: Vec<&str> = base[common..].iter().map(|_| "..").collect();
    parts.extend(target[common..].iter().map(String::as_str));
    if parts.is_empty() {
        return Some(".".to_owned());
    }
    Some(parts.join(MAIN_SEPARATOR_STR))
}

/// Lexically clean path components, `filepath.Clean` style: `.` dropped,
/// `..` removes the previous component (and is dropped at the root).
fn clean_components(path: &Path) -> Vec<String> {
    let mut parts: Vec<String> = Vec::new();
    let absolute = path.is_absolute();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                if parts.last().is_some_and(|last| last != "..") {
                    parts.pop();
                } else if !absolute {
                    parts.push("..".to_owned());
                }
            }
            Component::Normal(name) => parts.push(name.to_string_lossy().into_owned()),
            Component::RootDir | Component::Prefix(_) => {
                parts.push(component.as_os_str().to_string_lossy().into_owned());
            }
        }
    }
    parts
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn find_embed_matches_the_go_regexp_shapes() {
        fn found(s: &str) -> Option<(&str, &str)> {
            find_embed(s, 0).map(|(a, b, c)| (&s[a..c + 2], &s[b..c]))
        }
        assert_eq!(found("x ![[a]] y"), Some(("![[a]]", "a")));
        assert_eq!(found("![[]] ![[b]]"), Some(("![[b]]", "b")));
        assert_eq!(found("![[a]b]] ![[c]]"), Some(("![[c]]", "c")));
        assert_eq!(found("![[[c]]]"), Some(("![[[c]]", "[c")));
        assert_eq!(found("![[a\nb]]"), Some(("![[a\nb]]", "a\nb")));
        assert_eq!(found("![[a"), None);
        assert_eq!(found("[[a]]"), None);
        assert_eq!(found("![[a]"), None);
    }

    #[test]
    fn go_rel_follows_filepath_rel() {
        let rel = |a: &str, b: &str| go_rel(Path::new(a), Path::new(b));
        if cfg!(unix) {
            assert_eq!(rel("/v", "/v/a.md").as_deref(), Some("a.md"));
            assert_eq!(rel("/v", "/v").as_deref(), Some("."));
            assert_eq!(rel("/v/x", "/v/a.md").as_deref(), Some("../a.md"));
            assert_eq!(rel("/v", "/o/s.md").as_deref(), Some("../o/s.md"));
            assert_eq!(rel("/v", "/v/sub/../b.md").as_deref(), Some("b.md"));
        }
        assert_eq!(rel("v", "/v/a.md"), None);
    }
}
