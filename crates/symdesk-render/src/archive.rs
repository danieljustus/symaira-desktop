//! Pure contract of the archival PDF step (Go `internal/archive`): OCR page
//! splitting, archive naming, the text-layer probe, sidecar trimming and the
//! format dispatch. The pdfcpu watermark and image import stay out of scope.

use crate::export_html::trim_go_space;
use crate::export_markdown::go_to_lower;

/// Which stamping path `Generate` takes for a supported input.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ArchiveFormat {
    Pdf,
    Image,
}

/// Split OCR text into pages (Go `splitPages`). Blank text yields no pages.
/// The page-marker form wins over form feeds, as in Go.
pub fn split_pages(text: &str) -> Vec<String> {
    let text = trim_go_space(text);
    if text.is_empty() {
        return Vec::new();
    }
    if text.contains("--- Page ---") {
        return text
            .split("\n\n--- Page ---\n\n")
            .map(str::to_owned)
            .collect();
    }
    if text.contains('\u{c}') {
        return text.split('\u{c}').map(str::to_owned).collect();
    }
    vec![text.to_owned()]
}

/// Vault-relative archive path (Go `ArchivePath`): `_archive.pdf` replaces the
/// extension.
pub fn archive_path(doc_rel_path: &str) -> String {
    let extension = path_extension(doc_rel_path);
    let base = doc_rel_path.strip_suffix(extension).unwrap_or(doc_rel_path);
    format!("{base}_archive.pdf")
}

/// True when the content has both text operators, as the Go probe checks
/// (`BT` and `ET` anywhere in the bytes).
pub fn has_text_layer(content: &str) -> bool {
    content.contains("BT") && content.contains("ET")
}

/// OCR sidecar text with surrounding Go whitespace removed (Go `OCRTextFromFile`).
pub fn ocr_text(content: &str) -> String {
    trim_go_space(content).to_owned()
}

/// Stamping path for an input file, or the Go error text for an unsupported
/// extension. The extension is lowercased before matching, as in Go.
pub fn archive_format(input_path: &str) -> Result<ArchiveFormat, String> {
    let extension = go_to_lower(path_extension(input_path));
    match extension.as_str() {
        ".pdf" => Ok(ArchiveFormat::Pdf),
        ".png" | ".jpg" | ".jpeg" | ".tif" | ".tiff" | ".webp" | ".bmp" => Ok(ArchiveFormat::Image),
        _ => Err(format!(
            "unsupported format for archive generation: {extension}"
        )),
    }
}

// Go filepath.Ext on Unix: the suffix from the last dot in the final path
// element, including a leading dot, or empty when there is none.
fn path_extension(path: &str) -> &str {
    let element_start = path.rfind('/').map_or(0, |index| index + 1);
    match path[element_start..].rfind('.') {
        Some(index) => &path[element_start + index..],
        None => "",
    }
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
        op: String,
        input: String,
        want: Want,
    }

    #[derive(serde::Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Want {
        strings: Vec<String>,
        bool: bool,
        error: String,
    }

    #[test]
    fn go_archive_contract_fixture_matches() {
        let fixture: Fixture = serde_json::from_str(include_str!(
            "../../../testdata/port/render/archive-contract.json"
        ))
        .expect("decode Go-generated archive contract fixture");
        let provenance: serde_json::Value =
            serde_json::from_str(include_str!("../../../testdata/port/provenance.json"))
                .expect("decode central port provenance");
        assert_eq!(fixture.schema_version, 1);
        assert_eq!(
            Some(fixture.oracle.commit.as_str()),
            provenance["oracle"]["commit"].as_str()
        );
        assert_eq!(fixture.oracle.go_version, "go1.26.10");
        assert_eq!(fixture.oracle.source, "internal/archive/archive.go");
        assert_eq!(fixture.oracle.source_sha256.len(), 64);
        for case in &fixture.cases {
            let (strings, flag, error) = match case.op.as_str() {
                "split_pages" => (split_pages(&case.input), false, String::new()),
                "archive_path" => (vec![archive_path(&case.input)], false, String::new()),
                "has_text_layer" => (Vec::new(), has_text_layer(&case.input), String::new()),
                "ocr_text" => (vec![ocr_text(&case.input)], false, String::new()),
                "generate_unsupported" => match archive_format(&case.input) {
                    Err(error) => (Vec::new(), false, error),
                    Ok(format) => panic!("case {} unexpectedly supported: {format:?}", case.id),
                },
                other => panic!("case {} has unknown op {other}", case.id),
            };
            assert_eq!(strings, case.want.strings, "case {}", case.id);
            assert_eq!(flag, case.want.bool, "case {}", case.id);
            assert_eq!(error, case.want.error, "case {}", case.id);
        }
    }

    #[test]
    fn supported_formats_are_dispatched_without_stamping() {
        assert_eq!(archive_format("scan.PDF"), Ok(ArchiveFormat::Pdf));
        assert_eq!(archive_format("a/b.JPEG"), Ok(ArchiveFormat::Image));
        assert_eq!(archive_format("a/b.webp"), Ok(ArchiveFormat::Image));
    }
}
