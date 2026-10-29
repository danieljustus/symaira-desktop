//! Port of ledongthuc/pdf Page.GetPlainText operator policy (not its layout
//! extractor). In particular BT inserts a newline, TJ ignores positioning,
//! apostrophe adds no newline, and the Go double-quote arity bug drops a page.
//! Font decoding uses lopdf; unusual encodings/malformed PDFs remain a parity
//! risk beyond the deterministic oracle corpus.
use super::retrieval_extract::contract;
use crate::{RetrievalAnchor, RetrievalByteSection, SidecarError};
use lopdf::{Document, Object, content::Content};
const NO_TEXT: &str = "PDF contains no extractable text (may be image-only)";
const DECOMPRESSED_LIMIT: usize = 100 << 20;

fn pages(bytes: &[u8]) -> Result<Vec<(u32, String)>, SidecarError> {
    pages_with_limit(bytes, DECOMPRESSED_LIMIT)
}

fn pages_with_limit(bytes: &[u8], limit: usize) -> Result<Vec<(u32, String)>, SidecarError> {
    if !bytes.starts_with(b"%PDF-") {
        return Err(contract(
            "failed to open PDF: not a PDF file: invalid header",
        ));
    }
    let doc =
        Document::load_mem(bytes).map_err(|e| contract(format!("failed to open PDF: {e}")))?;
    let mut result = Vec::new();
    for (number, id) in doc.get_pages() {
        let Ok(data) = doc.get_page_content_with_limit(id, limit) else {
            continue;
        };
        let Ok(content) = Content::decode(&data) else {
            continue;
        };
        let fonts = doc.get_page_fonts(id).unwrap_or_default();
        let mut encoding = None;
        let mut text = String::new();
        let mut failed = false;
        for op in content.operations {
            match op.operator.as_str() {
                "BT" => text.push('\n'),
                "Tf" => {
                    if op.operands.len() != 2 {
                        failed = true;
                        break;
                    }
                    encoding = op.operands[0]
                        .as_name()
                        .ok()
                        .and_then(|name| fonts.get(name))
                        .and_then(|font| font.get_font_encoding_with_limit(&doc, limit).ok());
                }
                "T*" => text.push('\n'),
                "\"" => {
                    failed = true;
                    break;
                }
                "Tj" | "'" => {
                    if op.operands.len() != 1 {
                        failed = true;
                        break;
                    }
                    if let Ok(s) = op.operands[0].as_str() {
                        text.push_str(&decode(s, encoding.as_ref()));
                    }
                }
                "TJ" => {
                    if let Some(Object::Array(array)) = op.operands.first() {
                        for value in array {
                            if let Ok(s) = value.as_str() {
                                text.push_str(&decode(s, encoding.as_ref()));
                            }
                        }
                    }
                }
                _ => {}
            }
        }
        if !failed {
            result.push((number, text));
        }
    }
    Ok(result)
}
fn decode(bytes: &[u8], encoding: Option<&lopdf::Encoding<'_>>) -> String {
    encoding
        .and_then(|e| Document::decode_text(e, bytes).ok())
        .unwrap_or_else(|| String::from_utf8_lossy(bytes).into_owned())
}
pub(super) fn text(bytes: &[u8]) -> Result<Vec<u8>, SidecarError> {
    let mut text = String::new();
    for (_, page) in pages(bytes)? {
        text.push_str(&page);
        text.push('\n');
    }
    let text = text.trim();
    if text.is_empty() {
        return Err(contract(NO_TEXT));
    }
    Ok(symdesk_core::textnorm::dehyphenate(text).into_bytes())
}
pub(super) fn sections(bytes: &[u8]) -> Result<Vec<RetrievalByteSection>, SidecarError> {
    let mut sections = Vec::new();
    let mut start = 0;
    for (page, text) in pages(bytes)? {
        let text = text.trim();
        if text.is_empty() {
            continue;
        }
        sections.push(RetrievalByteSection {
            text: text.as_bytes().to_vec(),
            start,
            anchor: RetrievalAnchor {
                kind: "page".into(),
                value: page.to_string(),
            },
            synthetic: false,
        });
        start += text.len() + 2;
    }
    if sections.is_empty() {
        return Err(contract(NO_TEXT));
    }
    Ok(sections)
}

#[cfg(test)]
mod tests {
    use base64::{Engine, engine::general_purpose::STANDARD};
    #[test]
    fn retrieval_extract_pdf_decompression_safety_divergence() {
        let fixture: serde_json::Value = serde_json::from_slice(
            &std::fs::read(
                std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
                    .join("../../testdata/port/retrieval/document-extraction.json"),
            )
            .unwrap(),
        )
        .unwrap();
        let case = fixture["cases"]
            .as_array()
            .unwrap()
            .iter()
            .find(|c| c["id"] == "simple.pdf")
            .unwrap();
        let bytes = STANDARD
            .decode(case["input_base64"].as_str().unwrap())
            .unwrap();
        let mut doc = lopdf::Document::load_mem(&bytes).unwrap();
        for object in doc.objects.values_mut() {
            if let lopdf::Object::Stream(stream) = object {
                stream.set_plain_content("BT /F1 12 Tf (x) Tj ET\n".repeat(100).into_bytes());
                stream.compress().unwrap();
            }
        }
        let mut compressed = Vec::new();
        doc.save_to(&mut compressed).unwrap();
        assert_eq!(super::DECOMPRESSED_LIMIT, 100 << 20);
        assert!(
            !super::pages_with_limit(&compressed, super::DECOMPRESSED_LIMIT)
                .unwrap()
                .is_empty()
        );
        // Go has no equivalent cap. Exercise the exact production limit path
        // at a small bound rather than allocating a 100-MiB test stream.
        assert!(super::pages_with_limit(&compressed, 32).unwrap().is_empty());
    }
}
