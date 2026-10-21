//! Text stamp on an archival PDF (Go `addTextLayerToPDF`): each page with OCR
//! text gets one Helvetica line appended as its own content stream. The
//! contract is semantic. Byte layout and pdfcpu's diagonal Form XObject
//! placement are not reproduced, and stamp geometry is a later slice.

use lopdf::{Dictionary, Document, Object, ObjectId, Stream, dictionary};

use crate::archive::split_pages;

/// Resource name of the stamp font. It is distinct from any name the page
/// already uses.
const STAMP_FONT: &str = "ArchiveStampFont";

/// Stamp one page per OCR page with non-blank text. The returned bytes are a
/// full rewrite through lopdf. Blank text returns the input unchanged, as Go
/// copies the source.
///
/// Errors use the Go kind prefix: `add watermark to page N` for a failure on
/// page N, including unreadable input.
pub fn stamp_pdf(input: &[u8], ocr_text: &str) -> Result<Vec<u8>, String> {
    let texts = split_pages(ocr_text);
    if texts.iter().all(|text| text.trim().is_empty()) {
        return Ok(input.to_vec());
    }
    let mut doc =
        Document::load_mem(input).map_err(|error| format!("add watermark to page 1: {error}"))?;
    let pages = doc.get_pages();
    for (index, text) in texts.iter().enumerate() {
        let text = text.trim();
        if text.is_empty() {
            continue;
        }
        let page_number = u32::try_from(index + 1).unwrap_or(u32::MAX);
        let page_id = *pages
            .get(&page_number)
            .ok_or_else(|| format!("add watermark to page {page_number}: page out of range"))?;
        stamp_page(&mut doc, page_id, text)
            .map_err(|error| format!("add watermark to page {page_number}: {error}"))?;
    }
    let mut out = Vec::new();
    doc.save_to(&mut out)
        .map_err(|error| format!("save stamped PDF: {error}"))?;
    Ok(out)
}

fn stamp_page(doc: &mut Document, page_id: ObjectId, text: &str) -> Result<(), String> {
    let lines = stamp_lines(text);
    let mut content = String::new();
    for (index, line) in lines.iter().enumerate() {
        let y = 72 + 30 * (lines.len() - 1 - index);
        content.push_str(&format!(
            "q BT /{STAMP_FONT} 24 Tf 0 g 72 {y} Td ({}) Tj ET Q\n",
            pdf_literal(line)?
        ));
    }
    let font_id = doc.add_object(dictionary! {
        "Type" => "Font",
        "Subtype" => "Type1",
        "BaseFont" => "Helvetica",
        "Encoding" => "WinAnsiEncoding",
    });
    let mut resources = doc
        .get_page_resources(page_id)
        .map_err(|error| error.to_string())?
        .0
        .cloned()
        .unwrap_or_default();
    let mut fonts = resources
        .get(b"Font")
        .ok()
        .and_then(|font| doc.dereference(font).ok())
        .and_then(|(_, font)| font.as_dict().ok().cloned())
        .unwrap_or_default();
    fonts.set(STAMP_FONT, Object::Reference(font_id));
    resources.set("Font", Object::Dictionary(fonts));
    let existing = doc.get_page_contents(page_id);
    if existing.is_empty()
        && doc
            .get_dictionary(page_id)
            .map_err(|error| error.to_string())?
            .has(b"Contents")
    {
        return Err("direct page content streams are not supported".to_owned());
    }
    let stream_id = doc.add_object(Stream::new(Dictionary::new(), content.into_bytes()));
    let mut contents: Vec<Object> = existing.into_iter().map(Object::Reference).collect();
    contents.push(Object::Reference(stream_id));
    let page = doc
        .get_dictionary_mut(page_id)
        .map_err(|error| error.to_string())?;
    page.set("Resources", Object::Dictionary(resources));
    page.set("Contents", Object::Array(contents));
    Ok(())
}

/// Non-blank stamp lines in order. Each becomes its own text block, as the Go
/// stamp wraps multi-line OCR text across lines.
fn stamp_lines(text: &str) -> Vec<&str> {
    text.lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .collect()
}

/// Escape text as a PDF literal string body. Only printable ASCII is stamped,
/// because the Helvetica encoding for other characters is not part of this
/// slice.
fn pdf_literal(text: &str) -> Result<String, String> {
    let mut out = String::with_capacity(text.len());
    for character in text.chars() {
        match character {
            '(' | ')' | '\\' => {
                out.push('\\');
                out.push(character);
            }
            ' '..='~' => out.push(character),
            _ => return Err("only printable ASCII stamp text is supported".to_owned()),
        }
    }
    Ok(out)
}

/// What the Go oracle records for a stamped output: page count, validity,
/// MediaBox size and whether the page carries its stamp text.
#[derive(Debug, Clone, PartialEq)]
pub struct StampedPage {
    pub width: f64,
    pub height: f64,
    pub stamped: bool,
}

#[derive(Debug, Clone, PartialEq)]
pub struct StampObservation {
    pub page_count: usize,
    pub valid: bool,
    pub pages: Vec<StampedPage>,
}

/// Observe a stamped output the way the fixture records it. A stamp counts as
/// present when the page content holds the literal this module writes for the
/// page's text.
pub fn observe_stamped(output: &[u8], ocr_text: &str) -> Result<StampObservation, String> {
    let doc = Document::load_mem(output).map_err(|error| error.to_string())?;
    let texts = split_pages(ocr_text);
    let mut pages = Vec::new();
    for (index, (_, page_id)) in doc.get_pages().into_iter().enumerate() {
        let [x0, y0, x1, y1] = media_box(&doc, page_id).ok_or("page has no MediaBox")?;
        let text = texts.get(index).map_or("", |text| text.trim());
        let content = doc.get_page_content(page_id);
        let lines = stamp_lines(text);
        let stamped = !lines.is_empty()
            && lines.iter().all(|line| {
                let literal = format!("({}) Tj", pdf_literal(line).unwrap_or_default());
                content
                    .windows(literal.len())
                    .any(|window| window == literal.as_bytes())
            });
        pages.push(StampedPage {
            width: x1 - x0,
            height: y1 - y0,
            stamped,
        });
    }
    Ok(StampObservation {
        page_count: pages.len(),
        valid: true,
        pages,
    })
}

fn media_box(doc: &Document, page_id: ObjectId) -> Option<[f64; 4]> {
    let mut node = page_id;
    loop {
        let dictionary = doc.get_dictionary(node).ok()?;
        if let Ok(object) = dictionary.get(b"MediaBox") {
            let (_, object) = doc.dereference(object).ok()?;
            let values = object.as_array().ok()?;
            if values.len() != 4 {
                return None;
            }
            let mut out = [0.0; 4];
            for (slot, value) in out.iter_mut().zip(values) {
                let (_, value) = doc.dereference(value).ok()?;
                *slot = value
                    .as_float()
                    .map(f64::from)
                    .or_else(|_| value.as_i64().map(|v| v as f64))
                    .ok()?;
            }
            return Some(out);
        }
        node = dictionary.get(b"Parent").ok()?.as_reference().ok()?;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::Engine as _;

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
        input_pdf: String,
        ocr_text: String,
        want: Want,
    }

    #[derive(serde::Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Want {
        ok: bool,
        error_kind: String,
        page_count: usize,
        valid: bool,
        pages: Option<Vec<WantPage>>,
    }

    #[derive(serde::Deserialize)]
    #[serde(deny_unknown_fields)]
    struct WantPage {
        width: f64,
        height: f64,
        stamped: bool,
    }

    #[test]
    fn go_archive_stamp_fixture_matches_semantics() {
        let fixture: Fixture = serde_json::from_str(include_str!(
            "../../../testdata/port/render/archive-stamp.json"
        ))
        .expect("decode Go-generated archive stamp fixture");
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
            let input = base64::engine::general_purpose::STANDARD
                .decode(&case.input_pdf)
                .expect("decode fixture input");
            match stamp_pdf(&input, &case.ocr_text) {
                Err(error) => {
                    assert!(!case.want.ok, "case {} failed: {error}", case.id);
                    let kind = error.split(':').next().unwrap_or_default();
                    assert_eq!(kind, case.want.error_kind, "case {}", case.id);
                }
                Ok(output) => {
                    assert!(case.want.ok, "case {} unexpectedly stamped", case.id);
                    let observed = observe_stamped(&output, &case.ocr_text)
                        .unwrap_or_else(|error| panic!("case {} observe: {error}", case.id));
                    assert_eq!(
                        observed.page_count, case.want.page_count,
                        "case {}",
                        case.id
                    );
                    assert_eq!(observed.valid, case.want.valid, "case {}", case.id);
                    let want_pages = case.want.pages.as_deref().unwrap_or_default();
                    assert_eq!(observed.pages.len(), want_pages.len(), "case {}", case.id);
                    for (index, (got, want)) in observed.pages.iter().zip(want_pages).enumerate() {
                        assert_eq!(
                            (got.width, got.height, got.stamped),
                            (want.width, want.height, want.stamped),
                            "case {} page {}",
                            case.id,
                            index + 1
                        );
                    }
                }
            }
        }
    }

    #[test]
    fn blank_text_copies_the_input_without_loading_it() {
        assert_eq!(stamp_pdf(b"not a pdf", " \n\t ").unwrap(), b"not a pdf");
    }

    #[test]
    fn literal_escapes_delimiters_and_rejects_non_ascii() {
        assert_eq!(pdf_literal("a(b)c\\d").unwrap(), "a\\(b\\)c\\\\d");
        assert!(pdf_literal("Grüße").is_err());
    }
}
