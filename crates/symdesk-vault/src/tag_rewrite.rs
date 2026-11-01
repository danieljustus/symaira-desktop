#![deny(unsafe_code)]

//! Vault-wide tag rewriting: the file half of Go's `Service.TagsRename`,
//! `TagsMerge` and `TagsDelete` (`internal/vault/tags.go`:
//! `RewriteInlineTags`, `RewriteDocumentTagsAndBodyInRoot`).

use std::path::Path;

use noyalib::Value;

use crate::{
    Document,
    mutations::{frontmatter_bounds_str, quote_yaml, write_atomic},
    tags::find_inline_tag_spans,
};

/// Callback result of an inline-tag rewrite: the replacement tag and whether
/// the tag is kept (`false` removes it, together with one adjacent space).
pub type InlineTagRewrite<'a> = dyn FnMut(&str) -> (String, bool) + 'a;

/// Go `RewriteInlineTags`: replaces or removes inline tags in `body`.
/// Returns the rewritten body and whether anything changed.
pub fn rewrite_inline_tags(body: &str, mutate: &mut InlineTagRewrite<'_>) -> (String, bool) {
    let spans = find_inline_tag_spans(body);
    if spans.is_empty() {
        return (body.to_owned(), false);
    }
    let bytes = body.as_bytes();
    let mut changed = false;
    let mut out: Vec<u8> = Vec::with_capacity(bytes.len());
    let mut last_end = 0;
    for span in &spans {
        let (new_tag, keep) = mutate(&span.tag);
        if keep && new_tag == span.tag {
            continue;
        }
        changed = true;
        if keep {
            out.extend_from_slice(&bytes[last_end..span.start]);
            out.push(b'#');
            out.extend_from_slice(new_tag.as_bytes());
            last_end = span.end;
        } else {
            let mut cut_start = span.start;
            let mut cut_end = span.end;
            if cut_start > 0
                && bytes[cut_start - 1] == b' '
                && (cut_end == bytes.len() || matches!(bytes[cut_end], b' ' | b'\n' | b'\r'))
            {
                cut_start -= 1;
            } else if (cut_start == 0 || bytes[cut_start - 1] == b'\n')
                && cut_end < bytes.len()
                && bytes[cut_end] == b' '
            {
                cut_end += 1;
            }
            out.extend_from_slice(&bytes[last_end..cut_start]);
            last_end = cut_end;
        }
    }
    if !changed {
        return (body.to_owned(), false);
    }
    out.extend_from_slice(&bytes[last_end..]);
    (String::from_utf8_lossy(&out).into_owned(), true)
}

/// Frontmatter tag rewrite callback: `None` leaves the tags alone.
pub type FrontmatterTagRewrite<'a> = dyn FnMut(&[String]) -> Option<Vec<String>> + 'a;
/// Body rewrite callback: `None` leaves the body alone.
pub type BodyRewrite<'a> = dyn FnMut(&str) -> Option<String> + 'a;

/// Go `RewriteDocumentTagsAndBodyInRoot`: rewrites the `tags:` frontmatter
/// line and the inline tags of `path` (already parsed as `doc`) and writes the
/// file atomically when anything changed.
///
/// # Errors
/// Returns Go's wording (`read file: ...`, `marshal tags: ...`, temp-file
/// errors) when the file cannot be read or written.
pub fn rewrite_document_tags_and_body(
    path: &Path,
    doc: &Document,
    mutate_frontmatter: &mut FrontmatterTagRewrite<'_>,
    mutate_body: &mut BodyRewrite<'_>,
) -> Result<bool, String> {
    let data = crate::go_read(path).map_err(|(operation, error)| {
        format!(
            "read file: {}",
            crate::go_path_error(operation, path, &error)
        )
    })?;
    let content = String::from_utf8_lossy(&data).into_owned();
    let line_ending = if content.contains("\r\n") {
        "\r\n"
    } else {
        "\n"
    };
    let mut lines: Vec<String> = content.split(line_ending).map(str::to_owned).collect();
    let (fm_start, fm_end) = frontmatter_bounds_str(&lines);

    let mut new_tags: Vec<String> = Vec::new();
    let mut frontmatter_changed = false;
    if let (Some(_), Some(_)) = (fm_start, fm_end)
        && let Some(raw) = doc.frontmatter.get("tags")
    {
        let current: Vec<String> = match raw {
            Value::Sequence(items) => items
                .iter()
                .filter_map(|item| match item {
                    Value::String(text) => Some(text.clone()),
                    _ => None,
                })
                .collect(),
            Value::String(text) => vec![text.clone()],
            _ => Vec::new(),
        };
        if let Some(updated) = mutate_frontmatter(&current) {
            new_tags = updated;
            frontmatter_changed = true;
        }
    }

    let new_body = mutate_body(&doc.body);
    let body_changed = new_body.is_some();
    if !frontmatter_changed && !body_changed {
        return Ok(false);
    }

    let mut fm_end_index = fm_end;
    if frontmatter_changed {
        let (Some(start), Some(end)) = (fm_start, fm_end) else {
            unreachable!("frontmatter bounds checked above")
        };
        let rendered = format!(
            "[{}]",
            new_tags
                .iter()
                .map(|tag| quote_yaml(tag))
                .collect::<Vec<_>>()
                .join(", ")
        );
        let prefix = "tags: ";
        let mut replaced = false;
        for line in &mut lines[start + 1..end] {
            let trimmed = line.trim_end_matches('\r');
            if trimmed.starts_with(prefix) || trimmed == "tags" {
                *line = format!("{prefix}{rendered}");
                replaced = true;
                break;
            }
        }
        if !replaced {
            lines.insert(end, format!("{prefix}{rendered}"));
            fm_end_index = Some(end + 1);
        }
    }

    let output = if let (Some(_), Some(end)) = (fm_start, fm_end_index) {
        let front = lines[..=end].join(line_ending);
        match new_body {
            Some(body) => format!("{front}{line_ending}{body}"),
            None => format!("{front}{line_ending}{}", lines[end + 1..].join(line_ending)),
        }
    } else {
        new_body.unwrap_or(content)
    };
    write_atomic(path, output.as_bytes()).map_err(|error| error.to_string())?;
    Ok(true)
}
