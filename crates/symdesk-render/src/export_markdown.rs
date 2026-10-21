//! Pure Markdown export helpers from Go `internal/export`: search result sets,
//! saved-view tables and plain wikilink targets. Vault file resolution
//! (notes, views, transclusions) is not part of this module.

use std::collections::{BTreeMap, BTreeSet};
use std::fmt::Write as _;

use serde::Deserialize;
use symdesk_vault::{render_go_yaml_double_quoted, render_go_yaml_string};

use crate::export_html::trim_go_space;

#[derive(Debug, Clone, PartialEq, Deserialize)]
pub struct LocationAnchor {
    pub kind: String,
    pub value: String,
}

/// Export-facing subset of a search result (Go `export.SearchHit`).
#[derive(Debug, Clone, PartialEq, Deserialize)]
pub struct SearchHit {
    pub path: String,
    pub title: String,
    pub snippet: String,
    pub score: f64,
    pub anchor: Option<LocationAnchor>,
    pub metadata_matches: Option<Vec<String>>,
}

/// Render a search result set as Markdown (Go `export.SearchResults`). The
/// PDF form omits the body heading because the templates typeset the title.
/// An empty `created` is stamped with the current UTC time, as in Go.
pub fn search_results(
    query: &str,
    title: &str,
    created: &str,
    hits: &[SearchHit],
    format: &str,
) -> Result<String, String> {
    let mut format = go_to_lower(trim_go_space(format));
    if format == "md" {
        format = "markdown".to_owned();
    }
    if format != "markdown" && format != "pdf" {
        return Err(format!(
            "unsupported search export format {}: use markdown or pdf",
            crate::pdfops::quote(&format)
        ));
    }
    let title = if trim_go_space(title).is_empty() {
        "Search results"
    } else {
        title
    };
    let now;
    let created = if trim_go_space(created).is_empty() {
        now = now_rfc3339();
        now.as_str()
    } else {
        created
    };

    // yaml.v3 sorts the map keys; Go forces the query to double-quoted style.
    let yaml = |value: &str| render_go_yaml_string(value, 4, false).map_err(|e| e.to_string());
    let mut b = String::from("---\n");
    let _ = writeln!(b, "created: {}", yaml(created)?);
    let _ = writeln!(b, "query: {}", render_go_yaml_double_quoted(query));
    let _ = writeln!(b, "result_count: {}", hits.len());
    let _ = writeln!(b, "title: {}", yaml(title)?);
    b.push_str("type: search_result_set\n---\n\n");
    if format != "pdf" {
        let _ = write!(b, "# {}\n\n", heading_text(title));
    }
    let _ = write!(b, "Query: `{}`\n\n", code_text(query));
    let plural = if hits.len() == 1 { "" } else { "s" };
    let _ = write!(b, "**{} result{plural}**\n\n", hits.len());

    if hits.is_empty() {
        b.push_str("_No matching passages._\n");
        return Ok(b);
    }
    for (i, hit) in hits.iter().enumerate() {
        let _ = write!(b, "## {}. {}\n\n", i + 1, heading_text(&hit.title));
        let _ = writeln!(
            b,
            "**Source:** [[{}]] (`{}`)",
            wiki_target(hit),
            code_text(&hit.path)
        );
        match usable_anchor(hit) {
            Some(anchor) => {
                let _ = writeln!(
                    b,
                    "**Location:** {}: {}",
                    inline_text(&anchor.kind),
                    inline_text(&anchor.value)
                );
            }
            None => b.push_str("**Location:** Not available\n"),
        }
        let _ = writeln!(b, "**Score:** {:.6}", hit.score);
        if let Some(fields) = hit.metadata_matches.as_deref().filter(|f| !f.is_empty()) {
            let fields: Vec<String> = fields.iter().map(|f| inline_text(f)).collect();
            let _ = writeln!(b, "**Metadata matches:** {}", fields.join(", "));
        }
        b.push('\n');
        b.push_str(&blockquote(&hit.snippet));
        b.push_str("\n\n");
    }
    Ok(b)
}

/// Markdown table for a saved view (Go `writeMarkdownTable`): declared and
/// row columns, deduplicated and byte-sorted; pipes in cells are escaped.
pub fn markdown_table(columns: &[String], rows: &[BTreeMap<String, String>]) -> String {
    let ordered: BTreeSet<&str> = columns
        .iter()
        .map(String::as_str)
        .chain(rows.iter().flat_map(|row| row.keys().map(String::as_str)))
        .collect();
    let ordered: Vec<&str> = ordered.into_iter().collect();
    let mut b = format!("| {} |\n|", ordered.join(" | "));
    b.push_str(&" --- |".repeat(ordered.len()));
    b.push('\n');
    for row in rows {
        let cells: Vec<String> = ordered
            .iter()
            .map(|col| {
                row.get(*col)
                    .map_or(String::new(), |v| v.replace('|', "\\|"))
            })
            .collect();
        let _ = writeln!(b, "| {} |", cells.join(" | "));
    }
    b
}

/// First-seen unique wikilink targets with alias and fragment removed (Go
/// `export.PlainLinks`). Embeds match too, as the Go regexp does.
pub fn plain_links(body: &str) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    let mut rest = body;
    // Equivalent to the leftmost-first `\[\[([^\]]+)\]\]` scan.
    while let Some(start) = rest.find("[[") {
        let inner = &rest[start + 2..];
        let len = inner.find(']').unwrap_or(inner.len());
        if len == 0 || !inner[len..].starts_with("]]") {
            rest = &rest[start + 1..];
            continue;
        }
        let mut target = trim_go_space(&inner[..len]);
        if let Some(i) = target.find('|') {
            target = &target[..i];
        }
        if let Some(i) = target.find('#') {
            target = &target[..i];
        }
        if !target.is_empty() && !out.iter().any(|t| t == target) {
            out.push(target.to_owned());
        }
        rest = &inner[len + 2..];
    }
    out
}

fn usable_anchor(hit: &SearchHit) -> Option<&LocationAnchor> {
    hit.anchor
        .as_ref()
        .filter(|a| !a.kind.is_empty() && !a.value.is_empty())
}

fn wiki_target(hit: &SearchHit) -> String {
    let parts: Vec<String> = hit.path.split('/').map(escape_wiki_part).collect();
    let mut target = parts.join("/");
    if let Some(anchor) = usable_anchor(hit) {
        let _ = write!(
            target,
            "#{}:{}",
            escape_wiki_part(&anchor.kind),
            escape_wiki_part(&anchor.value)
        );
    }
    target
}

// Go url.PathEscape, then "&" -> "%26".
fn escape_wiki_part(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for byte in value.bytes() {
        if byte.is_ascii_alphanumeric() || b"-_.~$+:=@".contains(&byte) {
            out.push(byte as char);
        } else {
            let _ = write!(out, "%{byte:02X}");
        }
    }
    out
}

fn single_line(value: &str) -> String {
    value.replace(['\r', '\n'], " ")
}

fn heading_text(value: &str) -> String {
    let value = single_line(trim_go_space(value));
    let value = value.trim_start_matches('#');
    if value.is_empty() {
        "Untitled result".to_owned()
    } else {
        value.to_owned()
    }
}

fn inline_text(value: &str) -> String {
    single_line(trim_go_space(value)).replace('`', "'")
}

fn code_text(value: &str) -> String {
    single_line(value).replace('`', "'")
}

fn blockquote(value: &str) -> String {
    value
        .replace("\r\n", "\n")
        .split('\n')
        .map(|line| format!("> {line}"))
        .collect::<Vec<_>>()
        .join("\n")
}

// Go strings.ToLower maps rune by rune; U+0130 is the only scalar whose
// Unicode lowercase is not a single scalar, and Go maps it to 'i'.
pub(crate) fn go_to_lower(value: &str) -> String {
    value
        .chars()
        .map(|c| match c {
            '\u{130}' => 'i',
            c => c.to_lowercase().next().unwrap_or(c),
        })
        .collect()
}

fn now_rfc3339() -> String {
    let format =
        time::macros::format_description!("[year]-[month]-[day]T[hour]:[minute]:[second]Z");
    time::OffsetDateTime::now_utc()
        .format(&format)
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Fixture {
        schema_version: u32,
        oracle: Oracle,
        cases: Vec<Case>,
    }

    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Oracle {
        commit: String,
        go_version: String,
        sources: BTreeMap<String, String>,
    }

    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Case {
        id: String,
        search: Option<SearchInput>,
        table: Option<TableInput>,
        links: Option<String>,
        want: Outcome,
    }

    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct SearchInput {
        query: String,
        title: String,
        created: String,
        format: String,
        hits: Option<Vec<SearchHit>>,
    }

    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct TableInput {
        columns: Option<Vec<String>>,
        rows: Option<Vec<BTreeMap<String, String>>>,
    }

    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Outcome {
        output: String,
        error: String,
        links: Vec<String>,
    }

    #[test]
    fn go_export_markdown_fixture_matches_exact_bytes() {
        let fixture: Fixture = serde_json::from_str(include_str!(
            "../../../testdata/port/render/export-markdown.json"
        ))
        .expect("decode Go-generated export Markdown fixture");
        let provenance: serde_json::Value =
            serde_json::from_str(include_str!("../../../testdata/port/provenance.json"))
                .expect("decode central port provenance");
        assert_eq!(fixture.schema_version, 1);
        assert_eq!(
            Some(fixture.oracle.commit.as_str()),
            provenance["oracle"]["commit"].as_str()
        );
        assert_eq!(fixture.oracle.go_version, "go1.26.9");
        assert_eq!(
            fixture.oracle.sources.keys().collect::<Vec<_>>(),
            [
                "internal/export/export.go",
                "internal/export/search_results.go"
            ]
        );
        let mut kinds = [0; 3];
        for case in &fixture.cases {
            let (output, error, links) = match (&case.search, &case.table, &case.links) {
                (Some(s), None, None) => {
                    kinds[0] += 1;
                    let hits = s.hits.clone().unwrap_or_default();
                    match search_results(&s.query, &s.title, &s.created, &hits, &s.format) {
                        Ok(out) => (out, String::new(), Vec::new()),
                        Err(err) => (String::new(), err, Vec::new()),
                    }
                }
                (None, Some(t), None) => {
                    kinds[1] += 1;
                    let columns = t.columns.clone().unwrap_or_default();
                    let rows = t.rows.clone().unwrap_or_default();
                    (markdown_table(&columns, &rows), String::new(), Vec::new())
                }
                (None, None, Some(body)) => {
                    kinds[2] += 1;
                    (String::new(), String::new(), plain_links(body))
                }
                _ => panic!("case {} must set exactly one input", case.id),
            };
            assert_eq!(output, case.want.output, "case {}", case.id);
            assert_eq!(error, case.want.error, "case {}", case.id);
            assert_eq!(links, case.want.links, "case {}", case.id);
        }
        assert!(kinds.iter().all(|n| *n > 0), "{kinds:?}");
    }

    #[test]
    fn blank_created_is_stamped_with_go_rfc3339_utc() {
        let out = search_results("q", "", " ", &[], "md").unwrap();
        let line = out.lines().nth(1).unwrap();
        // created: "YYYY-MM-DDTHH:MM:SSZ"
        assert_eq!(
            line.len(),
            "created: \"2026-10-19T08:00:00Z\"".len(),
            "{line}"
        );
        assert!(
            line.starts_with("created: \"") && line.ends_with("Z\""),
            "{line}"
        );
    }
}
