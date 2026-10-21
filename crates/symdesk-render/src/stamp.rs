//! Text stamp on an archival PDF (Go `addTextLayerToPDF`, issue #1142).
//!
//! Each page with OCR text gets one gray Helvetica block drawn **under** the
//! page content, as Go's `OnTop = false` does. The block is scaled so its
//! widest line spans half the MediaBox width, centered and rotated 45
//! degrees, which is where pdfcpu places the text on the fixture pages and
//! why Poppler's `pdftotext` recovers it from an 8 by 8 point page. The page
//! keeps every original `/Contents` stream and every original resource: the
//! effective `/Resources` (own, or inherited through the `/Parent` chain,
//! direct or indirect) is cloned, the stamp font is merged into its `/Font`
//! dictionary and the result is written back on the page.
//!
//! Compared against Go by the `archive_stamp_contract` integration test:
//! page count, validity, MediaBox size, the letters and digits `pdftotext`
//! recovers from each page, and the error kind. Structure is asserted on top
//! of that (stamp font, preserved contents and resources, untouched pages).
//!
//! Go behaviors reproduced on purpose:
//! - Text for an OCR page the PDF does not have is stamped on **every** page,
//!   because pdfcpu treats a page selection that names no existing page as
//!   "all pages".
//! - Text is encoded as Windows-1252 (WinAnsi, Helvetica). A rune that
//!   encoding cannot represent becomes a space.
//!
//! Open, not reproduced: pdfcpu's Form XObject, optional-content layer,
//! `ExtGState` and `Artifact` marking (the layout is semantic, not byte
//! identical); `/Rotate`, `/CropBox` and `/UserUnit` handling; exact
//! Helvetica widths above U+007E (an average width only affects the scale);
//! a stamp is visible gray text, as in Go, although the Go comment calls it
//! invisible.

use std::collections::HashSet;

use lopdf::{Dictionary, Document, Object, ObjectId, Stream, dictionary};

use crate::archive::split_pages;

/// Resource name of the stamp font. A page that already maps this name to
/// another object gets a numbered name instead.
const STAMP_FONT: &str = "ArchiveStampFont";

/// Longest `/Parent` chain followed for inherited page attributes.
const MAX_PAGE_TREE_DEPTH: usize = 64;

/// MediaBox used when a page has none, the Go default page (A4 portrait).
const DEFAULT_MEDIA_BOX: [f64; 4] = [0.0, 0.0, 595.0, 842.0];

/// Share of the page width the widest stamp line spans (pdfcpu scale 0.5).
const STAMP_WIDTH_SHARE: f64 = 0.5;

/// Line height as a multiple of the font size, measured from pdfcpu's form.
const LINE_HEIGHT: f64 = 1.156;

/// Font size used when no line has a measurable width.
const FALLBACK_FONT_SIZE: f64 = 12.0;

/// Stamp one page per OCR page with non-blank text. The returned bytes are a
/// full rewrite through lopdf. Blank text returns the input unchanged, as Go
/// copies the source.
///
/// Errors use the Go kind prefix: `add watermark to page N` for a failure on
/// page N, including unreadable input.
pub fn stamp_pdf(input: &[u8], ocr_text: &str) -> Result<Vec<u8>, String> {
    let texts = split_pages(ocr_text);
    let stamps: Vec<(usize, &str)> = texts
        .iter()
        .enumerate()
        .filter_map(|(index, text)| {
            let text = text.trim();
            (!text.is_empty()).then_some((index + 1, text))
        })
        .collect();
    let Some(&(first_page, _)) = stamps.first() else {
        return Ok(input.to_vec());
    };
    let mut doc = Document::load_mem(input)
        .map_err(|error| format!("add watermark to page {first_page}: {error}"))?;
    let pages: Vec<ObjectId> = doc.get_pages().into_values().collect();
    let mut font_id = None;
    for (page_number, text) in stamps {
        let targets = match pages.get(page_number - 1) {
            Some(&page_id) => vec![page_id],
            None => pages.clone(),
        };
        for page_id in targets {
            stamp_page(&mut doc, &mut font_id, page_id, text)
                .map_err(|error| format!("add watermark to page {page_number}: {error}"))?;
        }
    }
    let mut out = Vec::new();
    doc.save_to(&mut out)
        .map_err(|error| format!("save stamped PDF: {error}"))?;
    Ok(out)
}

fn stamp_page(
    doc: &mut Document,
    font_slot: &mut Option<ObjectId>,
    page_id: ObjectId,
    text: &str,
) -> Result<(), String> {
    let media_box = media_box(doc, page_id).unwrap_or(DEFAULT_MEDIA_BOX);
    let mut resources = effective_resources(doc, page_id)?;
    let font_id = *font_slot.get_or_insert_with(|| {
        doc.add_object(dictionary! {
            "Type" => "Font",
            "Subtype" => "Type1",
            "BaseFont" => "Helvetica",
            "Encoding" => "WinAnsiEncoding",
        })
    });
    let mut fonts = match resources.get(b"Font") {
        Ok(object) => doc
            .dereference(object)
            .map_err(|error| format!("page /Font resource: {error}"))?
            .1
            .as_dict()
            .map_err(|_| "page /Font resource is not a dictionary".to_owned())?
            .clone(),
        Err(_) => Dictionary::new(),
    };
    let font_name = free_font_name(&fonts, font_id);
    fonts.set(font_name.as_str(), Object::Reference(font_id));
    resources.set("Font", Object::Dictionary(fonts));

    let existing = doc.get_page_contents(page_id);
    if existing.is_empty() {
        let page = doc.get_dictionary(page_id).map_err(|e| e.to_string())?;
        if let Ok(contents) = page.get(b"Contents") {
            let empty_array = matches!(
                doc.dereference(contents),
                Ok((_, Object::Array(items))) if items.is_empty()
            );
            if !empty_array {
                return Err("direct page content streams are not supported".to_owned());
            }
        }
    }
    let stamp = Stream::new(
        Dictionary::new(),
        stamp_content(&font_name, text, media_box),
    );
    let stream_id = doc.add_object(stamp);
    // The stamp goes first so the page content paints over it (Go OnTop=false).
    let mut contents = vec![Object::Reference(stream_id)];
    contents.extend(existing.into_iter().map(Object::Reference));
    let page = doc
        .get_dictionary_mut(page_id)
        .map_err(|error| error.to_string())?;
    page.set("Resources", Object::Dictionary(resources));
    page.set("Contents", Object::Array(contents));
    Ok(())
}

/// A resource name for the stamp font: the base name when it is free or
/// already maps to the stamp font, a numbered one when another font owns it.
fn free_font_name(fonts: &Dictionary, font_id: ObjectId) -> String {
    let mut name = STAMP_FONT.to_owned();
    let mut suffix = 0u32;
    loop {
        match fonts.get(name.as_bytes()) {
            Err(_) => return name,
            Ok(Object::Reference(id)) if *id == font_id => return name,
            Ok(_) => {
                suffix += 1;
                name = format!("{STAMP_FONT}{suffix}");
            }
        }
    }
}

/// The value of an inheritable page attribute: the page's own, else the
/// nearest ancestor's, with indirect references resolved. The `/Parent`
/// chain is followed through a visited set and a depth cap, so a looped or
/// absurdly deep page tree ends the walk instead of hanging it. `Ok(None)`
/// means no node on the chain has the key.
fn inherited<'a>(
    doc: &'a Document,
    page_id: ObjectId,
    key: &[u8],
) -> Result<Option<&'a Object>, String> {
    let mut seen = HashSet::new();
    let mut node = page_id;
    for _ in 0..MAX_PAGE_TREE_DEPTH {
        if !seen.insert(node) {
            return Err("page tree /Parent chain loops".to_owned());
        }
        let dictionary = doc.get_dictionary(node).map_err(|e| e.to_string())?;
        if let Ok(value) = dictionary.get(key) {
            let (_, value) = doc
                .dereference(value)
                .map_err(|error| format!("{}: {error}", String::from_utf8_lossy(key)))?;
            return Ok(Some(value));
        }
        match dictionary.get(b"Parent").and_then(Object::as_reference) {
            Ok(parent) => node = parent,
            Err(_) => return Ok(None),
        }
    }
    Err("page tree /Parent chain is too deep".to_owned())
}

/// The page's effective `/Resources` as an owned dictionary: its own or an
/// inherited one, direct or indirect. A page without any gets an empty
/// dictionary; a malformed or unresolvable one is an error, never silently
/// dropped.
fn effective_resources(doc: &Document, page_id: ObjectId) -> Result<Dictionary, String> {
    match inherited(doc, page_id, b"Resources")? {
        None | Some(Object::Null) => Ok(Dictionary::new()),
        Some(Object::Dictionary(resources)) => Ok(resources.clone()),
        Some(_) => Err("page /Resources is not a dictionary".to_owned()),
    }
}

/// The stamp content stream: one `q ... Q` block per line, rotated 45
/// degrees about the MediaBox center and sized so the widest line spans
/// [`STAMP_WIDTH_SHARE`] of the box width.
fn stamp_content(font_name: &str, text: &str, media_box: [f64; 4]) -> Vec<u8> {
    let lines: Vec<Vec<u8>> = stamp_lines(text).into_iter().map(encode_win_ansi).collect();
    let widths: Vec<f64> = lines.iter().map(|line| text_width_em(line)).collect();
    let widest = widths.iter().copied().fold(0.0, f64::max);
    let page_width = (media_box[2] - media_box[0]).abs();
    let mut size = STAMP_WIDTH_SHARE * page_width / widest;
    if !size.is_finite() || size <= 0.0 {
        size = FALLBACK_FONT_SIZE;
    }
    let center_x = (media_box[0] + media_box[2]) / 2.0;
    let center_y = (media_box[1] + media_box[3]) / 2.0;
    let line_height = LINE_HEIGHT * size;
    let block_height = line_height * lines.len() as f64;
    let mut out = Vec::new();
    for (index, (line, width)) in lines.iter().zip(&widths).enumerate() {
        let x = -width * size / 2.0;
        let y = block_height / 2.0 - line_height * (index as f64 + 0.8);
        out.extend_from_slice(
            format!(
                "q 0.70711 0.70711 -0.70711 0.70711 {center_x:.4} {center_y:.4} cm \
                 BT /{font_name} {size:.4} Tf 0.5 g 0 Tr {x:.4} {y:.4} Td ("
            )
            .as_bytes(),
        );
        out.extend(pdf_literal(line));
        out.extend_from_slice(b") Tj ET Q\n");
    }
    out
}

/// Non-blank stamp lines in order. Each becomes its own text block, as the Go
/// stamp wraps multi-line OCR text across lines.
fn stamp_lines(text: &str) -> Vec<&str> {
    text.lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .collect()
}

/// Windows-1252 bytes of the text. A rune the code page cannot represent
/// becomes a space, as in the Go output.
fn encode_win_ansi(text: &str) -> Vec<u8> {
    const SPECIALS: [(u32, u8); 27] = [
        (0x20AC, 0x80),
        (0x201A, 0x82),
        (0x0192, 0x83),
        (0x201E, 0x84),
        (0x2026, 0x85),
        (0x2020, 0x86),
        (0x2021, 0x87),
        (0x02C6, 0x88),
        (0x2030, 0x89),
        (0x0160, 0x8A),
        (0x2039, 0x8B),
        (0x0152, 0x8C),
        (0x017D, 0x8E),
        (0x2018, 0x91),
        (0x2019, 0x92),
        (0x201C, 0x93),
        (0x201D, 0x94),
        (0x2022, 0x95),
        (0x2013, 0x96),
        (0x2014, 0x97),
        (0x02DC, 0x98),
        (0x2122, 0x99),
        (0x0161, 0x9A),
        (0x203A, 0x9B),
        (0x0153, 0x9C),
        (0x017E, 0x9E),
        (0x0178, 0x9F),
    ];
    text.chars()
        .map(|character| {
            let code = u32::from(character);
            match code {
                0x00..=0x7F | 0xA0..=0xFF | 0x81 | 0x8D | 0x8F | 0x90 | 0x9D => code as u8,
                _ => SPECIALS
                    .iter()
                    .find(|(unicode, _)| *unicode == code)
                    .map_or(b' ', |(_, byte)| *byte),
            }
        })
        .collect()
}

/// Helvetica advance widths for 0x20..=0x7E in 1/1000 em.
const HELVETICA_WIDTHS: [u16; 95] = [
    278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, // 20-2F
    556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, // 30-3F
    1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, // 40-4F
    667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556, // 50-5F
    333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, // 60-6F
    556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, // 70-7E
];

/// Width of encoded text in em. Bytes above 0x7E use an average width and
/// control bytes none; the width only sets the stamp scale.
fn text_width_em(bytes: &[u8]) -> f64 {
    let total: u32 = bytes
        .iter()
        .map(|&byte| match byte {
            0x20..=0x7E => u32::from(HELVETICA_WIDTHS[usize::from(byte - 0x20)]),
            0x00..=0x1F | 0x7F => 0,
            _ => 556,
        })
        .sum();
    f64::from(total) / 1000.0
}

/// Escape bytes as a PDF literal string body. Delimiters get a backslash;
/// control and non-ASCII bytes become octal escapes, so the body stays ASCII.
fn pdf_literal(bytes: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(bytes.len());
    for &byte in bytes {
        match byte {
            b'(' | b')' | b'\\' => {
                out.push(b'\\');
                out.push(byte);
            }
            0x20..=0x7E => out.push(byte),
            _ => out.extend_from_slice(format!("\\{byte:03o}").as_bytes()),
        }
    }
    out
}

/// The page's effective MediaBox as `[x0, y0, x1, y1]`, own or inherited.
/// `None` when it is absent, malformed or the parent chain loops.
fn media_box(doc: &Document, page_id: ObjectId) -> Option<[f64; 4]> {
    let Ok(Some(object)) = inherited(doc, page_id, b"MediaBox") else {
        return None;
    };
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
    Some([
        out[0].min(out[2]),
        out[1].min(out[3]),
        out[0].max(out[2]),
        out[1].max(out[3]),
    ])
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn blank_text_copies_the_input_without_loading_it() {
        assert_eq!(stamp_pdf(b"not a pdf", " \n\t ").unwrap(), b"not a pdf");
    }

    #[test]
    fn unreadable_input_names_the_first_stamped_page() {
        // Leading whitespace is trimmed before the split, so page 1 is first.
        let error = stamp_pdf(b"not a pdf", " \u{c}hello").unwrap_err();
        assert!(
            error.starts_with("add watermark to page 1:"),
            "unexpected error: {error}"
        );
    }

    #[test]
    fn literal_escapes_delimiters_and_non_ascii_bytes() {
        assert_eq!(pdf_literal(b"a(b)c\\d"), b"a\\(b\\)c\\\\d");
        assert_eq!(pdf_literal(b"a\tb\xfc"), b"a\\011b\\374");
    }

    #[test]
    fn win_ansi_maps_like_the_go_encoder() {
        assert_eq!(encode_win_ansi("Gr\u{fc}\u{df}e"), b"Gr\xfc\xdfe");
        assert_eq!(
            encode_win_ansi("5\u{20ac} \u{201c}q\u{201d}"),
            b"5\x80 \x93q\x94"
        );
        // Control passthrough and unmappable runes, as measured on Go.
        assert_eq!(encode_win_ansi("\u{81}\u{a0}\u{ad}"), b"\x81\xa0\xad");
        assert_eq!(encode_win_ansi("\u{65e5}\u{1F600}a\u{301}"), b"  a ");
        assert_eq!(encode_win_ansi("a\tb\rc"), b"a\tb\rc");
    }

    #[test]
    fn helvetica_width_matches_the_measured_pdfcpu_scale() {
        // pdfcpu scaled "only" to 2.181025 pt on an 8 pt wide page.
        let size = STAMP_WIDTH_SHARE * 8.0 / text_width_em(b"only");
        assert!((size - 2.181_025).abs() < 1e-5, "size {size}");
    }

    #[test]
    fn inherited_lookup_stops_on_a_parent_loop() {
        let mut doc = Document::with_version("1.5");
        let a = doc.new_object_id();
        let b = doc.new_object_id();
        doc.objects.insert(
            a,
            Object::Dictionary(dictionary! {"Parent" => Object::Reference(b)}),
        );
        doc.objects.insert(
            b,
            Object::Dictionary(dictionary! {"Parent" => Object::Reference(a)}),
        );
        let error = inherited(&doc, a, b"MediaBox").unwrap_err();
        assert!(error.contains("loops"), "unexpected error: {error}");
        assert_eq!(media_box(&doc, a), None);
        assert!(effective_resources(&doc, a).is_err());
    }

    #[test]
    fn inherited_lookup_caps_the_chain_depth() {
        let mut doc = Document::with_version("1.5");
        let ids: Vec<ObjectId> = (0..MAX_PAGE_TREE_DEPTH + 8)
            .map(|_| doc.new_object_id())
            .collect();
        for window in ids.windows(2) {
            doc.objects.insert(
                window[0],
                Object::Dictionary(dictionary! {"Parent" => Object::Reference(window[1])}),
            );
        }
        let last = *ids.last().unwrap();
        doc.objects.insert(
            last,
            Object::Dictionary(dictionary! {
                "MediaBox" => Object::Array(vec![
                    Object::Integer(0),
                    Object::Integer(0),
                    Object::Integer(1),
                    Object::Integer(1),
                ])
            }),
        );
        let error = inherited(&doc, ids[0], b"MediaBox").unwrap_err();
        assert!(error.contains("too deep"), "unexpected error: {error}");
    }

    #[test]
    fn a_dangling_resources_reference_is_an_error_not_an_empty_dictionary() {
        let mut doc = Document::with_version("1.5");
        let page = doc.add_object(dictionary! {"Resources" => Object::Reference((99, 0))});
        assert!(effective_resources(&doc, page).is_err());
    }

    #[test]
    fn font_name_avoids_a_foreign_font_with_the_stamp_name() {
        let ours = (7, 0);
        let mut fonts = Dictionary::new();
        assert_eq!(free_font_name(&fonts, ours), STAMP_FONT);
        fonts.set(STAMP_FONT, Object::Reference(ours));
        assert_eq!(free_font_name(&fonts, ours), STAMP_FONT);
        fonts.set(STAMP_FONT, Object::Reference((8, 0)));
        assert_eq!(free_font_name(&fonts, ours), "ArchiveStampFont1");
    }
}
