#![deny(unsafe_code)]

use std::collections::HashSet;

use crate::Document;

const SEARCH_METADATA_START: &str = "__SYMDESK_SEARCH_METADATA_START__";
const SEARCH_METADATA_END: &str = "__SYMDESK_SEARCH_METADATA_END__";

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SearchMetadataField {
    pub name: String,
    pub value: String,
    pub weight: i32,
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct SearchMetadata {
    pub fields: Vec<SearchMetadataField>,
}

#[must_use]
pub fn search_metadata_from_document(document: &Document) -> SearchMetadata {
    let mut fields = Vec::new();
    let mut add = |name: &str, value: String, weight| {
        if !value.trim().is_empty() {
            fields.push(SearchMetadataField {
                name: name.to_owned(),
                value,
                weight,
            });
        }
    };
    add("title", document.title.clone(), 3);
    add("tags", document.tags.join(" "), 3);
    add("aliases", document.aliases.join(" "), 2);
    add("created", document.created.clone(), 1);
    add("document_date", document.document_date.clone(), 1);
    add("person", document.person.clone(), 1);
    add("status", document.status.clone(), 1);
    add("due_date", document.due_date.clone(), 1);
    add("ocr_json_path", document.ocr_json_path.clone(), 1);
    add("simhash", document.simhash.clone(), 1);
    add("type", document.document_type.clone(), 1);
    if let Some(asn) = document.asn {
        add("asn", asn.to_string(), 1);
    }
    for key in [
        "document_type",
        "correspondent",
        "source_path",
        "mime",
        "category",
        "ocr_engine",
        "archive_path",
        "imported_from",
        "import_run_id",
        "source_uri",
        "download_uri",
        "ingested_at",
        "sha256",
        "meeting_id",
        "notebook_id",
        "base_id",
    ] {
        if let Some(value) = document.frontmatter.get(key) {
            add(key, value.to_string(), 1);
        }
    }
    if let Some(value) = document.frontmatter.get("confidence") {
        add("confidence", value.to_string(), 1);
    }
    SearchMetadata { fields }
}

#[must_use]
pub fn format_search_metadata(metadata: &SearchMetadata) -> String {
    let mut fields = metadata.fields.clone();
    fields.sort_by(|left, right| {
        metadata_field_rank(&left.name)
            .cmp(&metadata_field_rank(&right.name))
            .then_with(|| left.name.cmp(&right.name))
            .then_with(|| left.value.cmp(&right.value))
    });
    let mut lines = String::new();
    for field in fields {
        let name = field.name.trim();
        let value = field.value.split_whitespace().collect::<Vec<_>>().join(" ");
        if name.is_empty() || value.is_empty() {
            continue;
        }
        let weight = field.weight.clamp(1, 4);
        for _ in 0..weight {
            lines.push_str(name);
            lines.push_str(": ");
            lines.push_str(&value);
            lines.push('\n');
        }
    }
    if lines.is_empty() {
        String::new()
    } else {
        format!("{SEARCH_METADATA_START}\n{lines}{SEARCH_METADATA_END}")
    }
}

#[must_use]
pub fn metadata_matches(query: &str, content: &str) -> Vec<String> {
    let Some(start) = content.find(SEARCH_METADATA_START) else {
        return Vec::new();
    };
    let metadata_start = start + SEARCH_METADATA_START.len();
    let Some(relative_end) = content[metadata_start..].find(SEARCH_METADATA_END) else {
        return Vec::new();
    };
    let metadata = &content[metadata_start..metadata_start + relative_end];
    let terms: Vec<String> = query
        .to_lowercase()
        .split_whitespace()
        .map(|term| term.trim_matches(['"', '\'']).to_owned())
        .filter(|term| !term.is_empty())
        .collect();
    if terms.is_empty() {
        return Vec::new();
    }
    let mut seen = HashSet::new();
    let mut matches = Vec::new();
    for line in metadata.lines() {
        let Some((name, value)) = line.split_once(':') else {
            continue;
        };
        let name = name.trim();
        let value = value.trim().to_lowercase();
        if name.is_empty() || value.is_empty() {
            continue;
        }
        if terms.iter().any(|term| value.contains(term)) && seen.insert(name.to_owned()) {
            matches.push(name.to_owned());
        }
    }
    matches
}

#[must_use]
pub fn strip_search_metadata(content: &str) -> String {
    let Some(start) = content.find(SEARCH_METADATA_START) else {
        return content.to_owned();
    };
    let after_start = start + SEARCH_METADATA_START.len();
    let Some(relative_end) = content[after_start..].find(SEARCH_METADATA_END) else {
        return content[..start].trim().to_owned();
    };
    let end = after_start + relative_end + SEARCH_METADATA_END.len();
    format!("{}\n{}", &content[..start], &content[end..])
        .trim()
        .to_owned()
}

fn metadata_field_rank(name: &str) -> u8 {
    match name {
        "title" => 0,
        "tags" => 1,
        "aliases" => 2,
        "created" => 3,
        "document_date" => 4,
        "due_date" => 5,
        "type" => 6,
        "status" => 7,
        "person" => 8,
        "correspondent" => 9,
        "asn" => 10,
        _ => 100,
    }
}
