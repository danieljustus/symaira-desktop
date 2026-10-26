//! Bounded retrieval document extraction, matching the Go parser's text rules.
use crate::retrieval_markdown::MAX_RETRIEVAL_SOURCE_BYTES;
use crate::{RetrievalAnchor, RetrievalByteSection, SidecarError};

pub(super) fn contract(message: impl Into<String>) -> SidecarError {
    SidecarError::Contract(message.into())
}
pub(super) fn normalize(text: &str) -> String {
    let mut result = String::new();
    let mut newlines = 0;
    for ch in text.chars() {
        if ch == '\n' {
            newlines += 1;
        } else {
            newlines = 0;
        }
        if newlines <= 2 {
            result.push(ch);
        }
    }
    result.trim().to_owned()
}
pub(super) fn html_text(bytes: &[u8]) -> String {
    use scraper::node::Node;
    let html = scraper::Html::parse_document(&String::from_utf8_lossy(bytes));
    fn walk(node: scraper::ElementRef<'_>, out: &mut String) {
        let name = node.value().name();
        if matches!(name, "script" | "style" | "head") {
            return;
        }
        for child in node.children() {
            match child.value() {
                Node::Text(t) => {
                    if !(t.trim().is_empty() && t.contains('\n')) {
                        out.push_str(t);
                    }
                }
                Node::Element(_) => {
                    if let Some(element) = scraper::ElementRef::wrap(child) {
                        walk(element, out);
                    }
                }
                _ => {}
            }
        }
        if matches!(
            name,
            "p" | "div" | "li" | "tr" | "h1" | "h2" | "h3" | "h4" | "h5" | "h6"
        ) {
            out.push_str("\n\n");
        }
    }
    let mut out = String::new();
    walk(html.root_element(), &mut out);
    normalize(&out)
}
fn rtf_text(data: &[u8]) -> Vec<u8> {
    let (mut ignored, mut depth, mut i) = (0, 0, 0);
    let mut out = Vec::new();
    while i < data.len() {
        match data[i] {
            b'{' => {
                depth += 1;
                if [
                    b"\\fonttbl".as_slice(),
                    b"\\colortbl",
                    b"\\stylesheet",
                    b"\\*",
                ]
                .iter()
                .any(|prefix| data[i + 1..].starts_with(prefix))
                {
                    ignored = depth;
                }
            }
            b'}' => {
                if ignored == depth {
                    ignored = 0;
                }
                if depth > 0 {
                    depth -= 1;
                }
            }
            b'\\' => {
                let start = i + 1;
                if start == data.len() {
                    i += 1;
                    continue;
                }
                if matches!(data[start], b'\\' | b'{' | b'}') {
                    if ignored == 0 {
                        out.push(data[start]);
                    }
                    i += 2;
                    continue;
                }
                let mut j = start;
                while j < data.len() && data[j].is_ascii_alphabetic() {
                    j += 1;
                }
                if ignored == 0 {
                    match &data[start..j] {
                        b"par" | b"line" | b"page" => out.push(b'\n'),
                        b"tab" => out.push(b' '),
                        _ => {}
                    }
                }
                while j < data.len() && (data[j] == b'-' || data[j].is_ascii_digit()) {
                    j += 1;
                }
                if data.get(j) == Some(&b' ') {
                    j += 1;
                }
                i = j;
                continue;
            }
            ch => {
                if ignored == 0 && !matches!(ch, b'\r' | b'\n') {
                    out.push(ch);
                }
            }
        }
        i += 1;
    }
    let mut folded = Vec::new();
    let mut newlines = 0;
    for ch in out {
        if ch == b'\n' {
            newlines += 1;
        } else {
            newlines = 0;
        }
        if newlines <= 2 {
            folded.push(ch);
        }
    }
    crate::retrieval_csv::trim_space(&folded)
}

/// Extract format-specific text from bytes already read from a confined source.
pub fn extract_retrieval_text(
    source_path: &str,
    extension: &str,
    bytes: &[u8],
) -> Result<Vec<u8>, SidecarError> {
    if extension.eq_ignore_ascii_case(".pdf") {
        return crate::retrieval_pdf::text(bytes);
    }
    if matches!(
        extension.to_ascii_lowercase().as_str(),
        ".docx" | ".xlsx" | ".pptx" | ".odt" | ".ods" | ".odp" | ".epub"
    ) {
        return crate::retrieval_archive::archive_text(
            source_path,
            &extension.to_ascii_lowercase(),
            bytes,
        );
    }
    if bytes.len() > MAX_RETRIEVAL_SOURCE_BYTES {
        return Err(contract(format!(
            "file {source_path} exceeds {MAX_RETRIEVAL_SOURCE_BYTES} byte limit ({} bytes)",
            MAX_RETRIEVAL_SOURCE_BYTES + 1
        )));
    }
    match extension.to_ascii_lowercase().as_str() {
        ".html" | ".htm" => Ok(html_text(bytes).into_bytes()),
        ".rtf" => Ok(rtf_text(bytes)),
        _ => Err(contract(format!(
            "unsupported extraction extension {extension}"
        ))),
    }
}
/// Anchor-aware route used by retrieval re-embedding.
pub fn parse_document_retrieval_sections(
    source_path: &str,
    extension: &str,
    bytes: &[u8],
) -> Result<Vec<RetrievalByteSection>, SidecarError> {
    if extension.eq_ignore_ascii_case(".pdf") {
        return crate::retrieval_pdf::sections(bytes);
    }
    let text = extract_retrieval_text(source_path, extension, bytes)?;
    if crate::retrieval_csv::trim_space(&text).is_empty() {
        return Ok(Vec::new());
    }
    Ok(vec![RetrievalByteSection {
        text,
        start: 0,
        anchor: RetrievalAnchor {
            kind: "text".into(),
            value: "offset:0".into(),
        },
        synthetic: false,
    }])
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::{Engine, engine::general_purpose::STANDARD};
    use serde::Deserialize;
    #[derive(Clone, Deserialize)]
    struct Case {
        id: String,
        extension: String,
        input_base64: String,
        #[serde(default)]
        oversized: bool,
        text_base64: String,
        #[serde(default)]
        error: String,
        sections: Vec<Expected>,
        #[serde(default)]
        sections_error: String,
    }
    #[derive(Clone, Deserialize)]
    struct Expected {
        text_base64: String,
        start: usize,
        anchor_kind: String,
        anchor_value: String,
        synthetic: bool,
    }
    #[derive(Deserialize)]
    struct Fixture {
        schema_version: u32,
        cases: Vec<Case>,
    }
    fn fixture() -> Fixture {
        serde_json::from_slice(
            &std::fs::read(
                std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
                    .join("../../testdata/port/retrieval/document-extraction.json"),
            )
            .unwrap(),
        )
        .unwrap()
    }
    fn message(error: SidecarError) -> String {
        match error {
            SidecarError::Contract(m) => m,
            other => other.to_string(),
        }
    }
    fn compare(case: &Case) -> bool {
        let input = if case.oversized {
            vec![b'a'; MAX_RETRIEVAL_SOURCE_BYTES + 1]
        } else {
            STANDARD.decode(&case.input_base64).unwrap()
        };
        let expected = if case.error.is_empty() {
            Ok(STANDARD.decode(&case.text_base64).unwrap())
        } else {
            Err(case.error.clone())
        };
        let actual = extract_retrieval_text("$PATH", &case.extension, &input).map_err(message);
        assert_eq!(actual, expected, "{} flattened text", case.id);
        let expected = if case.sections_error.is_empty() {
            Ok(case
                .sections
                .iter()
                .map(|s| RetrievalByteSection {
                    text: STANDARD.decode(&s.text_base64).unwrap(),
                    start: s.start,
                    anchor: RetrievalAnchor {
                        kind: s.anchor_kind.clone(),
                        value: s.anchor_value.clone(),
                    },
                    synthetic: s.synthetic,
                })
                .collect::<Vec<_>>())
        } else {
            Err(case.sections_error.clone())
        };
        parse_document_retrieval_sections("$PATH", &case.extension, &input).map_err(message)
            == expected
    }
    #[test]
    fn retrieval_extract_go_fixture() {
        let f = fixture();
        assert_eq!(f.schema_version, 1);
        assert_eq!(f.cases.len(), 60, "all Go extraction cases must execute");
        for c in &f.cases {
            assert!(compare(c), "{} sections drift", c.id);
        }
    }
    #[test]
    fn retrieval_extract_negative_control() {
        let mut f = fixture();
        let c = f.cases.iter_mut().find(|c| !c.sections.is_empty()).unwrap();
        assert!(compare(c));
        c.sections[0].text_base64 = STANDARD.encode("tampered expectation");
        assert!(!compare(c));
    }
}
