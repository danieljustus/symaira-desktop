use crate::{RetrievalAnchor, RetrievalSection, SidecarError};

const MAX_MARKDOWN_BYTES: usize = 10 << 20;

/// Parses Markdown into the same durable sections used by Go retrieval indexing,
/// then prepends the vault metadata section when the existing vault parser can
/// read the document metadata.
///
/// Input must be valid UTF-8 because retrieval section offsets and contents are
/// represented as Rust strings. This covers Markdown only. PDF and other format
/// extraction remain separate.
pub fn parse_markdown_retrieval_sections(
    source_path: &str,
    markdown: &[u8],
) -> Result<Vec<RetrievalSection>, SidecarError> {
    if markdown.len() > MAX_MARKDOWN_BYTES {
        return Err(SidecarError::Contract(format!(
            "markdown content exceeds {MAX_MARKDOWN_BYTES} byte limit ({} bytes)",
            markdown.len()
        )));
    }

    let text = std::str::from_utf8(markdown).map_err(|error| {
        SidecarError::Contract(format!(
            "markdown content must be valid UTF-8 (invalid byte at {})",
            error.valid_up_to()
        ))
    })?;
    let (body, frontmatter_bytes) = strip_frontmatter(&text);
    let mut sections = parse_sections(body, frontmatter_bytes);

    if let Ok(document) = symdesk_vault::parse_bytes(source_path, markdown) {
        let metadata = symdesk_vault::search_metadata_from_document(&document);
        let formatted = symdesk_vault::format_search_metadata(&metadata);
        if !formatted.is_empty() {
            sections.insert(
                0,
                RetrievalSection {
                    text: formatted,
                    start: 0,
                    anchor: RetrievalAnchor {
                        kind: "section".to_owned(),
                        value: "metadata".to_owned(),
                    },
                    synthetic: true,
                },
            );
        }
    }
    Ok(sections)
}

fn strip_frontmatter(text: &str) -> (&str, usize) {
    if !text.starts_with("---") {
        return (text, 0);
    }
    let mut lines = text.split_inclusive('\n');
    let Some(first) = lines.next() else {
        return (text, 0);
    };
    if first.trim() != "---" {
        return (text, 0);
    }
    let mut offset = first.len();
    for line in lines {
        offset += line.len();
        if line.trim() == "---" {
            return (&text[offset..], offset);
        }
    }
    (text, 0)
}

fn parse_sections(text: &str, base_offset: usize) -> Vec<RetrievalSection> {
    let mut headings = Vec::new();
    let mut offset = 0;
    for line in text.split_inclusive('\n') {
        let trimmed = line.trim_end_matches('\n').trim();
        if let Some((level, name)) = markdown_heading(trimmed) {
            headings.push((offset, level, name.to_owned()));
        }
        offset += line.len();
    }

    if headings.is_empty() {
        if text.trim().is_empty() {
            return Vec::new();
        }
        return vec![RetrievalSection {
            text: text.to_owned(),
            start: base_offset,
            anchor: RetrievalAnchor {
                kind: "text".to_owned(),
                value: format!("offset:{base_offset}"),
            },
            synthetic: false,
        }];
    }

    let mut sections = Vec::with_capacity(headings.len() + 1);
    let (first_start, _, _) = &headings[0];
    let prefix = &text[..*first_start];
    if !prefix.trim().is_empty() {
        sections.push(RetrievalSection {
            text: prefix.trim().to_owned(),
            start: base_offset,
            anchor: RetrievalAnchor {
                kind: "text".to_owned(),
                value: format!("offset:{base_offset}"),
            },
            synthetic: false,
        });
    }

    let mut path_parts: Vec<String> = Vec::new();
    for index in 0..headings.len() {
        let (start, level, name) = &headings[index];
        path_parts.truncate(level.saturating_sub(1));
        path_parts.push(name.clone());
        let end = headings
            .get(index + 1)
            .map_or(text.len(), |heading| heading.0);
        let heading_body = text[*start..end].trim();
        if heading_body.is_empty() {
            continue;
        }
        sections.push(RetrievalSection {
            text: heading_body.to_owned(),
            start: base_offset + start,
            anchor: RetrievalAnchor {
                kind: "heading".to_owned(),
                value: path_parts.join(" > "),
            },
            synthetic: false,
        });
    }
    sections
}

fn markdown_heading(line: &str) -> Option<(usize, &str)> {
    let level = line.bytes().take_while(|byte| *byte == b'#').count();
    if level == 0 || level > 6 || line.as_bytes().get(level) != Some(&b' ') {
        return None;
    }
    let name = line[level..].trim();
    (!name.is_empty()).then_some((level, name))
}
