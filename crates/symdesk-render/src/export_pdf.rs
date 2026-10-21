//! The frontmatter block the PDF renderer reads (Go `export.pdfFrontmatter`).
//! The profiles take their named fields from frontmatter, so a note's block is
//! carried through with the resolved title filled in when it sets none.

use std::collections::BTreeMap;

use noyalib::Value;

/// Encode `front` plus a derived title as the PDF document's frontmatter.
/// Returns an empty string when there is nothing to write, as Go does.
pub fn pdf_frontmatter(front: &BTreeMap<String, Value>, title: &str) -> Result<String, String> {
    let mut meta = front.clone();
    if !meta.contains_key("title") && !title.is_empty() {
        meta.insert("title".to_owned(), Value::String(title.to_owned()));
    }
    if meta.is_empty() {
        return Ok(String::new());
    }
    let encoded = symdesk_vault::render_go_yaml_map(&meta).map_err(|error| error.to_string())?;
    Ok(format!("---\n{encoded}---\n\n"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(serde::Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Fixture {
        schema_version: u32,
        oracle: Oracle,
        cases: Vec<Case>,
    }

    #[derive(serde::Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Oracle {
        commit: String,
        go_version: String,
        source: String,
        source_sha256: String,
    }

    #[derive(serde::Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Case {
        id: String,
        frontmatter: String,
        title: String,
        output: String,
        error: String,
    }

    #[test]
    fn go_pdf_frontmatter_fixture_matches_exact_bytes() {
        let fixture: Fixture = serde_json::from_str(include_str!(
            "../../../testdata/port/render/pdf-frontmatter.json"
        ))
        .expect("decode Go-generated PDF frontmatter fixture");
        let provenance: serde_json::Value =
            serde_json::from_str(include_str!("../../../testdata/port/provenance.json"))
                .expect("decode central port provenance");
        assert_eq!(fixture.schema_version, 1);
        assert_eq!(
            Some(fixture.oracle.commit.as_str()),
            provenance["oracle"]["commit"].as_str()
        );
        assert_eq!(fixture.oracle.go_version, "go1.26.10");
        assert_eq!(fixture.oracle.source, "internal/export/export.go");
        assert_eq!(fixture.oracle.source_sha256.len(), 64);
        assert!(!fixture.cases.is_empty());
        for case in &fixture.cases {
            // Decode like the vault does. Go's error text is out of scope here:
            // the only Go error path is a marshal failure, which the fixture
            // corpus does not exercise, so any error is a mismatch.
            let front: BTreeMap<String, Value> = if case.frontmatter.trim().is_empty() {
                BTreeMap::new()
            } else {
                noyalib::from_str(&case.frontmatter)
                    .unwrap_or_else(|error| panic!("case {} decode: {error}", case.id))
            };
            let actual = pdf_frontmatter(&front, &case.title)
                .unwrap_or_else(|error| panic!("case {}: {error}", case.id));
            assert_eq!(actual, case.output, "case {}", case.id);
            assert_eq!(case.error, "", "case {}", case.id);
        }
    }
}
