#![deny(unsafe_code)]

/// Render already-resolved Markdown using the pure helper called by Go export.
///
/// This intentionally implements only the shipped `noteToHTML` line rules; it
/// is not a general Markdown parser and does not resolve transclusions/files.
/// The demonstrated parity domain is valid UTF-8 Markdown. Go byte strings
/// containing invalid UTF-8 are outside this `&str` API's contract.
pub fn render_note_html(markdown: &str) -> String {
    let mut html = String::new();
    html.push_str("<!DOCTYPE html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n");
    html.push_str("<title>Exported Note</title>\n</head>\n<body>\n");

    let mut in_list = false;
    for line in markdown.split('\n') {
        let trimmed = trim_go_space(line);
        if let Some(heading) = trimmed.strip_prefix("# ") {
            html.push_str("<h1>");
            html.push_str(&escape_go_html(heading));
            html.push_str("</h1>\n");
        } else if let Some(heading) = trimmed.strip_prefix("## ") {
            html.push_str("<h2>");
            html.push_str(&escape_go_html(heading));
            html.push_str("</h2>\n");
        } else if let Some(heading) = trimmed.strip_prefix("### ") {
            html.push_str("<h3>");
            html.push_str(&escape_go_html(heading));
            html.push_str("</h3>\n");
        } else if trimmed.starts_with("- ") || trimmed.starts_with("* ") {
            if !in_list {
                html.push_str("<ul>\n");
                in_list = true;
            }
            // Keep Go's nested TrimPrefix order: remove "- " first, then "* ".
            let item = trimmed.strip_prefix("- ").unwrap_or(trimmed);
            let item = item.strip_prefix("* ").unwrap_or(item);
            html.push_str("<li>");
            html.push_str(&escape_go_html(item));
            html.push_str("</li>\n");
        } else if trimmed.is_empty() {
            if in_list {
                html.push_str("</ul>\n");
                in_list = false;
            }
            html.push('\n');
        } else {
            if in_list {
                html.push_str("</ul>\n");
                in_list = false;
            }
            html.push_str("<p>");
            html.push_str(&escape_go_html(trimmed));
            html.push_str("</p>\n");
        }
    }
    if in_list {
        html.push_str("</ul>\n");
    }
    html.push_str("</body>\n</html>");
    html
}

fn trim_go_space(value: &str) -> &str {
    value.trim_matches(is_go_space)
}

// Go 1.26.6 strings.TrimSpace and Rust 1.98 str::trim share these 25 scalars.
// Keep the explicit Go set frozen across future Unicode-table changes.
fn is_go_space(character: char) -> bool {
    matches!(
        character,
        '\u{0009}'..='\u{000D}'
            | ' '
            | '\u{0085}'
            | '\u{00A0}'
            | '\u{1680}'
            | '\u{2000}'..='\u{200A}'
            | '\u{2028}'
            | '\u{2029}'
            | '\u{202F}'
            | '\u{205F}'
            | '\u{3000}'
    )
}

fn escape_go_html(value: &str) -> String {
    let mut escaped = String::with_capacity(value.len());
    for character in value.chars() {
        match character {
            '&' => escaped.push_str("&amp;"),
            '<' => escaped.push_str("&lt;"),
            '>' => escaped.push_str("&gt;"),
            '"' => escaped.push_str("&#34;"),
            '\'' => escaped.push_str("&#39;"),
            other => escaped.push(other),
        }
    }
    escaped
}

#[cfg(test)]
mod tests {
    use super::render_note_html;
    use serde::Deserialize;

    const GO_ORACLE_COMMIT: &str = "305ba383f3b5143f7fa5698c8684554fc94b74c6";
    const GO_ORACLE_SOURCE_SHA256: &str =
        "fb244cc8ce36b8e4e8199b1e9183cf009f107b3a3d4ec14bf7a5afe10ee60861";
    const CASE_IDS: [&str; 9] = [
        "empty",
        "trailing-newline",
        "all-five-html-entities",
        "go-unicode-trim",
        "crlf",
        "headings-lists-transitions",
        "nested-list-prefix-trim",
        "plain-wikilinks",
        "multiple-blank-lines",
    ];

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
        source: String,
        source_sha256: String,
    }

    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Case {
        id: String,
        markdown: String,
        markdown_sha256: String,
        html: String,
        html_sha256: String,
    }

    #[test]
    fn go_note_html_fixture_matches_exact_bytes() {
        let fixture: Fixture =
            serde_json::from_str(include_str!("../../../testdata/port/render/html-note.json"))
                .expect("decode Go-generated note HTML fixture");

        assert_eq!(fixture.schema_version, 1);
        assert_eq!(fixture.oracle.commit, GO_ORACLE_COMMIT);
        assert_eq!(fixture.oracle.go_version, "go1.26.6");
        assert_eq!(fixture.oracle.source, "internal/export/export.go");
        assert_eq!(fixture.oracle.source_sha256, GO_ORACLE_SOURCE_SHA256);
        assert_eq!(fixture.cases.len(), CASE_IDS.len());

        let actual_ids: Vec<&str> = fixture.cases.iter().map(|case| case.id.as_str()).collect();
        assert_eq!(actual_ids, CASE_IDS);
        for case in fixture.cases {
            // These assertions validate digest syntax, not digest values.
            // `make render-html-differential` requires the production Go
            // fixture-integrity check before this byte-parity comparator.
            assert_eq!(case.markdown_sha256.len(), 64, "{}", case.id);
            assert!(
                case.markdown_sha256
                    .bytes()
                    .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase()),
                "{}",
                case.id
            );
            assert_eq!(case.html_sha256.len(), 64, "{}", case.id);
            assert!(
                case.html_sha256
                    .bytes()
                    .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase()),
                "{}",
                case.id
            );

            let actual = render_note_html(&case.markdown);
            assert_eq!(actual.as_bytes(), case.html.as_bytes(), "case {}", case.id);
        }
    }
}
