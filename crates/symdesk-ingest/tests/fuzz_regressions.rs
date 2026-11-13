//! Regressions for `cargo fuzz run mime_input` findings.
use symdesk_ingest::{gxml, structured};

/// Fuzz crash b6830f08: an xml declaration holding invalid UTF-8 made the
/// declaration parser slice a lossily converted string inside a multi-byte
/// character. Go slices raw bytes; the parser now does too.
const CRASH_XML_DECL: &[u8] = b"<?xml version=\xb0\x221\x0e\x00\x00\x00\x00\x00\x00*\x00\x080\x22 encod'n\x01\x00\x00\x03\xa3=\x22UTF-16\x22?><a/>\xff\x01";

#[test]
fn xml_declaration_with_invalid_utf8_does_not_panic() {
    let mut dec = gxml::Decoder::new(CRASH_XML_DECL);
    while dec.token().is_ok() {}
    let _ = structured::word_xml_to_text(CRASH_XML_DECL);
    let _ = structured::odf_xml_to_text(CRASH_XML_DECL);
    let _ = structured::pptx_xml_to_text(CRASH_XML_DECL);
    let _ = structured::sheet_xml_to_text(CRASH_XML_DECL, &[]);
}
