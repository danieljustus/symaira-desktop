//! The OCR text layer on an archival PDF (Go `addTextLayerToPDF`, issue #1142),
//! a port of the pdfcpu v0.16.2 text watermark with `OnTop = false`.
//!
//! Go stamps page by page: every OCR page with non-blank text becomes one
//! `api.AddWatermarksFile` call that selects PDF page N (a selection naming no
//! existing page selects **all** pages). A call reproduced here, in pdfcpu's
//! order of effects:
//!
//! 1. the first optional content group (`/OCProperties`, created once as
//!    "Background"), a new `ExtGState` (CA/ca 1), a new Helvetica font object;
//! 2. per selected page: the text is laid out like `model.WriteColumn` (centered
//!    on the viewport, font size scaled so the widest line spans half the
//!    viewport width, Helvetica metrics from the 14 core fonts) into a Form
//!    XObject (`/OC`, `/Matrix`, own `/Resources`), referenced from the page
//!    resources as `GS<n>`/`Fm<n>` and drawn by a `/Artifact` marked section
//!    rotated along the viewport diagonal, which is **prepended to the first
//!    content stream**;
//! 3. `/Rotate` is normalised into the boxes, `%p` `%P` `%t` `%v` `%%` in the
//!    OCR text are expanded like `format.Text`.
//!
//! Compared against Go by `tests/archive_pdf_contract.rs` through a canonical
//! description of the written PDF (page tree, boxes, resources, form and
//! content streams, optional content) and by `tests/archive_stamp_contract.rs`
//! through Poppler text recovery. Not reproduced (declared): pdfcpu's
//! read-time optimisation of foreign PDFs (duplicate font/image merging), the
//! aliasing of an inherited `/Resources` dictionary between pages, mutation of
//! an indirect `ExtGState`/`XObject` sub-dictionary in place (the sub-dictionary
//! is written back inline), and PDF 2.0 core font metrics.

use std::collections::HashSet;

use lopdf::{Dictionary, Document, Object, ObjectId, Stream, StringFormat, dictionary};

use crate::archive::split_pages;
use crate::import::compress;

/// Longest `/Parent` chain followed for inherited page attributes.
const MAX_PAGE_TREE_DEPTH: usize = 64;

/// MediaBox used when a page has none, the Go default page (A4 portrait).
const DEFAULT_MEDIA_BOX: [f64; 4] = [0.0, 0.0, 595.0, 842.0];

/// pdfcpu `DefaultWatermarkConfig`: font size, relative scale.
const FONT_SIZE: f64 = 24.0;
const SCALE: f64 = 0.5;

/// Helvetica `FontBBox` (lower left, upper right) in glyph space.
const FBOX: [f64; 4] = [-166.0, -225.0, 1000.0, 931.0];

/// Helvetica glyph widths for bytes 0..=255 through pdfcpu's WinAnsi glyph map;
/// a glyph without metrics counts 1000.
const HELVETICA_WIDTHS: [i64; 256] = [
    1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000,
    1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000, 1000,
    278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, 556, 556, 556,
    556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, 1015, 667, 667, 722, 722, 667,
    611, 778, 722, 278, 500, 667, 556, 833, 722, 778, 667, 778, 722, 667, 611, 722, 667, 944, 667,
    667, 611, 278, 278, 278, 469, 556, 333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500,
    222, 833, 556, 556, 556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
    1000, 556, 1000, 222, 556, 333, 1000, 556, 556, 333, 1000, 667, 333, 1000, 1000, 611, 1000,
    1000, 222, 222, 333, 333, 350, 556, 1000, 333, 1000, 500, 333, 944, 1000, 500, 667, 1000, 333,
    556, 556, 556, 556, 260, 556, 333, 737, 370, 556, 584, 1000, 737, 333, 400, 584, 333, 333, 333,
    556, 537, 278, 333, 333, 365, 556, 834, 834, 834, 611, 667, 667, 667, 667, 667, 667, 1000, 722,
    667, 667, 667, 667, 278, 278, 278, 278, 722, 722, 778, 778, 778, 778, 778, 584, 778, 722, 722,
    722, 722, 667, 667, 611, 556, 556, 556, 556, 556, 556, 889, 500, 556, 556, 556, 556, 278, 278,
    278, 278, 556, 556, 556, 556, 556, 556, 556, 584, 611, 556, 556, 556, 556, 500, 556, 500,
];

/// The metric tables, exposed so the contract test can compare them with the
/// ones Go reports through its public font API.
pub fn helvetica_metrics() -> ([i64; 256], [f64; 4]) {
    (HELVETICA_WIDTHS, FBOX)
}

// ---- geometry ---------------------------------------------------------------

#[derive(Clone, Copy, Debug)]
struct Rect {
    llx: f64,
    lly: f64,
    urx: f64,
    ury: f64,
}

impl Rect {
    fn width(&self) -> f64 {
        self.urx - self.llx
    }
    fn height(&self) -> f64 {
        self.ury - self.lly
    }
    fn translate(&mut self, dx: f64, dy: f64) {
        self.llx += dx;
        self.urx += dx;
        self.lly += dy;
        self.ury += dy;
    }
    fn bits(&self) -> [u64; 4] {
        [
            self.llx.to_bits(),
            self.lly.to_bits(),
            self.urx.to_bits(),
            self.ury.to_bits(),
        ]
    }
}

fn user_units(glyph_units: f64, size: f64) -> f64 {
    glyph_units / 1000.0 * size
}

fn text_width_units(bytes: &[u8]) -> i64 {
    bytes
        .iter()
        .map(|b| HELVETICA_WIDTHS[usize::from(*b)])
        .sum()
}

/// `Repository.TextBoundingBox`: the box of `bytes` at the origin.
fn text_bbox(bytes: &[u8], size: f64) -> Rect {
    let width = user_units(text_width_units(bytes) as f64, size);
    let height = user_units(FBOX[3] - FBOX[1], size);
    let y = -user_units(-FBOX[1], size).ceil();
    Rect {
        llx: 0.0,
        lly: y,
        urx: width,
        ury: y + height,
    }
}

fn union_point(r: Rect, x: f64, y: f64) -> Rect {
    let (mut llx, mut lly, mut urx, mut ury) = (r.llx, r.lly, r.urx, r.ury);
    if x < r.llx {
        llx = x;
    } else if x > r.urx {
        urx = x;
    }
    if y < r.lly {
        lly = y;
    } else if y > r.ury {
        ury = y;
    }
    Rect { llx, lly, urx, ury }
}

/// `calcBoundingBoxForLines`: union of the line boxes stacked downwards from (x, y).
fn lines_bbox(lines: &[Vec<u8>], x: f64, mut y: f64, size: f64) -> Rect {
    let mut boxed: Option<Rect> = None;
    for line in lines {
        let mut b = text_bbox(line, size);
        b.translate(x, y);
        boxed = Some(match boxed {
            None => b,
            Some(r) => {
                let r = union_point(r, b.llx, b.lly);
                union_point(r, b.urx, b.ury)
            }
        });
        y -= b.height();
    }
    boxed.expect("at least one line")
}

type Matrix = [[f64; 3]; 3];

fn mat_mul(a: &Matrix, b: &Matrix) -> Matrix {
    let mut p = [[0.0; 3]; 3];
    for i in 0..3 {
        for j in 0..3 {
            for k in 0..3 {
                p[i][j] += a[i][k] * b[k][j];
            }
        }
    }
    p
}

/// `matrix.CalcTransformMatrix(1, 1, sin, cos, dx, dy)`.
fn transform_matrix(sin: f64, cos: f64, dx: f64, dy: f64) -> Matrix {
    let ident: Matrix = [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [0.0, 0.0, 1.0]];
    let m2: Matrix = [[cos, sin, 0.0], [-sin, cos, 0.0], [0.0, 0.0, 1.0]];
    let m3: Matrix = [[1.0, 0.0, 0.0], [0.0, 1.0, 0.0], [dx, dy, 1.0]];
    mat_mul(&mat_mul(&ident, &m2), &m3)
}

fn matrix_cm(m: &Matrix) -> String {
    format!(
        "{:.5} {:.5} {:.5} {:.5} {:.5} {:.5}",
        m[0][0], m[0][1], m[1][0], m[1][1], m[2][0], m[2][1]
    )
}

const DEG_TO_RAD: f64 = std::f64::consts::PI / 180.0;
const RAD_TO_DEG: f64 = 180.0 / std::f64::consts::PI;

// ---- text -------------------------------------------------------------------

/// pdfcpu `format.Text`: `%p[n]` page number (+n), `%P` page count, `%t` the
/// timestamp, `%v` the pdfcpu version, `%%` a percent sign; a lone `%` is
/// dropped together with nothing else. Returns whether the text is page
/// dependent ("unique"), which defeats form caching.
fn format_text(text: &str, page_nr: usize, page_count: usize) -> (String, bool) {
    let bytes = text.as_bytes();
    let (mut out, mut digits): (Vec<u8>, Vec<u8>) = (Vec::new(), Vec::new());
    let (mut has_percent, mut unique, mut page_tag) = (false, false, false);
    let offset_of = |digits: &[u8]| -> usize {
        std::str::from_utf8(digits)
            .ok()
            .and_then(|s| s.parse().ok())
            .unwrap_or(0)
    };
    for &c in bytes {
        if page_tag {
            if c.is_ascii_digit() {
                digits.push(c);
                continue;
            }
            out.extend((offset_of(&digits) + page_nr).to_string().bytes());
            page_tag = false;
            digits.clear();
        }
        if c == b'%' {
            if has_percent {
                out.push(b'%');
            }
            has_percent = true;
            continue;
        }
        if has_percent {
            has_percent = false;
            match c {
                b'p' => {
                    page_tag = true;
                    unique = true;
                    continue;
                }
                b'P' => {
                    out.extend(page_count.to_string().bytes());
                    unique = true;
                    continue;
                }
                b't' => {
                    out.extend(timestamp().bytes());
                    unique = true;
                    continue;
                }
                b'v' => {
                    out.extend(b"v0.16.2");
                    unique = true;
                    continue;
                }
                _ => {}
            }
        }
        out.push(c);
    }
    if page_tag {
        out.extend((offset_of(&digits) + page_nr).to_string().bytes());
    }
    (String::from_utf8_lossy(&out).into_owned(), unique)
}

/// Go `time.Now().Format("2006-01-02 15:04")` (pdfcpu's default timestamp
/// format), in local time.
fn timestamp() -> String {
    let now = time::OffsetDateTime::now_local().unwrap_or_else(|_| time::OffsetDateTime::now_utc());
    let format = time::macros::format_description!("[year]-[month]-[day] [hour]:[minute]");
    now.format(&format).unwrap_or_default()
}

/// pdfcpu `SplitMultilineStr`: a literal backslash-n counts as a line break and
/// consecutive breaks keep their empty lines.
fn split_multiline(text: &str) -> Vec<String> {
    let text = text.replace("\\n", "\n");
    let (mut fields, mut was_field, mut from) = (Vec::new(), false, 0usize);
    for (i, c) in text.char_indices() {
        if c == '\n' {
            if was_field {
                fields.push(text[from..i].to_owned());
                was_field = false;
            } else {
                fields.push(String::new());
            }
        } else if !was_field {
            from = i;
            was_field = true;
        }
    }
    if was_field {
        fields.push(text[from..].to_owned());
    }
    fields
}

/// `DecodeUTF8ToByte`: code points up to U+00FF are bytes, the 27 Windows-1252
/// extras map to their code page byte, everything else becomes a space.
fn encode_win_ansi(text: &str) -> Vec<u8> {
    const SPECIALS: [(u32, u8); 27] = [
        (0x20AC, 128),
        (0x201A, 130),
        (0x0192, 131),
        (0x201E, 132),
        (0x2026, 133),
        (0x2020, 134),
        (0x2021, 135),
        (0x02C6, 136),
        (0x2030, 137),
        (0x0160, 138),
        (0x2039, 139),
        (0x0152, 140),
        (0x017D, 142),
        (0x2018, 145),
        (0x2019, 146),
        (0x201C, 147),
        (0x201D, 148),
        (0x2022, 149),
        (0x2013, 150),
        (0x2014, 151),
        (0x02DC, 152),
        (0x2122, 153),
        (0x0161, 154),
        (0x203A, 155),
        (0x0153, 156),
        (0x017E, 158),
        (0x0178, 159),
    ];
    text.chars()
        .map(|c| {
            let code = u32::from(c);
            if code <= 0xFF {
                code as u8
            } else {
                SPECIALS
                    .iter()
                    .find(|(u, _)| *u == code)
                    .map_or(b' ', |(_, b)| *b)
            }
        })
        .collect()
}

/// `types.Escape`: backslash before `\`, `(` and `)`; `\n \r \t \b \f` as
/// letters. Bytes above 0x7F stay raw, as in pdfcpu.
fn pdf_escape(bytes: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(bytes.len());
    for &b in bytes {
        let c = match b {
            0x0A => b'n',
            0x0D => b'r',
            0x09 => b't',
            0x08 => b'b',
            0x0C => b'f',
            b'\\' | b'(' | b')' => b,
            _ => {
                out.push(b);
                continue;
            }
        };
        out.push(b'\\');
        out.push(c);
    }
    out
}

fn type_name(o: &Object) -> &'static str {
    o.enum_variant()
}

struct Column {
    /// Form content stream bytes.
    content: Vec<u8>,
    /// Text box translated to the origin (pdfcpu `wm.Bb`).
    bb: Rect,
}

/// `model.WriteColumn` for the stamp's fixed parameters: centered anchor,
/// middle vertical alignment, relative scale, no margins, no wrapping.
fn write_column(text: &str, vp_w: f64, vp_h: f64) -> Column {
    let r = Rect {
        llx: 0.0,
        lly: 0.0,
        urx: vp_w,
        ury: vp_h,
    };
    let lines: Vec<Vec<u8>> = split_multiline(text)
        .iter()
        .map(|l| encode_win_ansi(l))
        .collect();
    let (mut x, mut y) = (r.llx + 0.0 + 0.0, r.lly + 0.0 + 0.0);
    // scaleFontSize (relative): width of the unscaled block.
    let unscaled = lines_bbox(&lines, x, y, FONT_SIZE);
    let www = unscaled.width() + 0.0 + 0.0 + 2.0 * 0.0;
    let size = r.width() * SCALE * FONT_SIZE / www;
    // deltaAlignMiddle
    let ascent = user_units((FBOX[3] - FBOX[1]) + FBOX[1], size);
    let line_height = user_units(FBOX[3] - FBOX[1], size);
    let dy1 = -ascent + (lines.len() as f64 * line_height + 0.0 + 0.0) / 2.0 - 0.0;
    y += dy1.ceil();
    let mut b = lines_bbox(&lines, x, y, size);
    // horizontalWrapUp, centered, no width
    let half = b.width() / 2.0;
    b.translate(-half, 0.0);
    b.llx -= 0.0 + 0.0;
    b.urx += 0.0 + 0.0;
    b.lly -= 0.0 + 0.0;
    b.ury += 0.0 + 0.0;
    // horAdjustBoundingBoxForLines with dx = dy = 0
    let mut dx = 0.0;
    if r.urx - b.llx < b.width() {
        dx -= b.width() - (r.urx - b.llx);
        x += dx;
        b.translate(dx, 0.0);
    } else if b.llx < r.llx {
        dx += r.llx - b.llx;
        x += dx;
        b.translate(dx, 0.0);
    }
    let mut dy = 0.0;
    if r.ury - b.lly < b.height() {
        dy -= b.height() - (r.ury - b.lly);
        y += dy;
        b.translate(0.0, dy);
    } else if b.lly < r.lly {
        dy += r.lly - b.lly;
        y += dy;
        b.translate(0.0, dy);
    }
    // CalcRotateTransformMatrix(0, colBB)
    let (sin, cos) = (0.0f64, 1.0f64);
    let mdx = b.llx + b.width() / 2.0 + sin * (b.height() / 2.0) - cos * b.width() / 2.0;
    let mdy = b.lly + b.height() / 2.0 - cos * (b.height() / 2.0) - sin * b.width() / 2.0;
    let m = transform_matrix(sin, cos, mdx, mdy);
    let mut out: Vec<u8> = Vec::new();
    out.extend_from_slice(b"q ");
    out.extend_from_slice(format!("BT /F1 {size} Tf ET ").as_bytes());
    out.extend_from_slice(format!("{} cm ", matrix_cm(&m)).as_bytes());
    x -= b.llx;
    y -= b.lly;
    let (shift_x, shift_y) = (-b.llx, -b.lly);
    b.translate(shift_x, shift_y);
    // renderText
    for line in &lines {
        let mut lb = text_bbox(line, size);
        lb.translate(x, y);
        let dx = lb.width() / 2.0;
        out.extend_from_slice(
            format!(
                "BT 0 Tw 0.50 0.50 0.50 RG 0.50 0.50 0.50 rg {:.2} {:.2} Td 0 Tr (",
                x - dx,
                y
            )
            .as_bytes(),
        );
        out.extend(pdf_escape(line));
        out.extend_from_slice(b") Tj ET ");
        y -= line_height;
    }
    out.extend_from_slice(b"Q ");
    Column {
        content: out,
        bb: b,
    }
}

// ---- document access ----------------------------------------------------------

/// The value of an inheritable page attribute: the page's own, else the nearest
/// ancestor's, indirect references resolved. The `/Parent` chain is followed
/// through a visited set and a depth cap. `Ok(None)` means no node has the key.
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

fn number(doc: &Document, object: &Object) -> Option<f64> {
    let (_, value) = doc.dereference(object).ok()?;
    value
        .as_float()
        .map(f64::from)
        .or_else(|_| value.as_i64().map(|v| v as f64))
        .ok()
}

fn rect_attr(doc: &Document, page_id: ObjectId, key: &[u8]) -> Result<Option<Rect>, String> {
    let Some(object) = inherited(doc, page_id, key)? else {
        return Ok(None);
    };
    let Ok(values) = object.as_array() else {
        return Ok(None);
    };
    if values.len() != 4 {
        return Ok(None);
    }
    let mut v = [0.0; 4];
    for (slot, value) in v.iter_mut().zip(values) {
        match number(doc, value) {
            Some(n) => *slot = n,
            None => return Ok(None),
        }
    }
    Ok(Some(Rect {
        llx: v[0].min(v[2]),
        lly: v[1].min(v[3]),
        urx: v[0].max(v[2]),
        ury: v[1].max(v[3]),
    }))
}

/// The value as an exact PDF real: lopdf stores reals as `f32`, which is too
/// coarse for pdfcpu's 12-digit boxes, so the digits travel as a name and
/// [`fix_reals`] writes them as numbers.
fn real(value: f64) -> Object {
    Object::Name(format!("SymdeskReal={value}").into_bytes())
}

fn fix_reals(bytes: Vec<u8>) -> Vec<u8> {
    const MARK: &[u8] = b"/SymdeskReal=";
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i..].starts_with(MARK) {
            out.push(b' ');
            i += MARK.len();
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    out
}

fn rect_array(r: &Rect) -> Object {
    Object::Array(vec![real(r.llx), real(r.lly), real(r.urx), real(r.ury)])
}

fn name_array(names: &[&str]) -> Object {
    Object::Array(
        names
            .iter()
            .map(|n| Object::Name(n.as_bytes().to_vec()))
            .collect(),
    )
}

fn proc_set() -> Object {
    name_array(&["PDF", "Text", "ImageB", "ImageC", "ImageI"])
}

// ---- the watermark call -------------------------------------------------------

/// `prepareOCPropertiesInRoot`: the first existing OCG, else a new
/// "Background" group registered in the catalog.
fn prepare_oc_properties(doc: &mut Document) -> Result<ObjectId, String> {
    let root = doc
        .trailer
        .get(b"Root")
        .and_then(Object::as_reference)
        .map_err(|_| "catalog: missing Root".to_owned())?;
    let existing = {
        let catalog = doc
            .get_dictionary(root)
            .map_err(|e| format!("catalog: {e}"))?;
        match catalog.get(b"OCProperties") {
            Ok(object) => {
                let (_, props) = doc.dereference(object).map_err(|e| {
                    format!("optional content properties: dereference dictionary: {e}")
                })?;
                let props = props
                    .as_dict()
                    .map_err(|_| "optional content properties: missing dictionary".to_owned())?;
                match props.get(b"OCGs") {
                    Ok(ocgs) => {
                        let (_, ocgs) = doc.dereference(ocgs).map_err(|e| {
                            format!("corrupt optional content groups: dereference OCGs: {e}")
                        })?;
                        let ocgs = ocgs.as_array().map_err(|_| {
                            "corrupt optional content groups: dereference OCGs: not an array"
                                .to_owned()
                        })?;
                        match ocgs.first() {
                            Some(Object::Reference(id)) => Some(*id),
                            Some(other) => {
                                return Err(format!(
                                    "corrupt optional content groups: first OCG has type {}",
                                    type_name(other)
                                ));
                            }
                            None => None,
                        }
                    }
                    Err(_) => None,
                }
            }
            Err(_) => None,
        }
    };
    if let Some(id) = existing {
        return Ok(id);
    }
    let ocg = doc.add_object(dictionary! {
        "Name" => Object::String(b"Background".to_vec(), StringFormat::Literal),
        "Type" => "OCG",
        "Usage" => dictionary! {
            "PageElement" => dictionary! { "Subtype" => "BG" },
            "View" => dictionary! { "ViewState" => "ON" },
            "Print" => dictionary! { "PrintState" => "ON" },
            "Export" => dictionary! { "ExportState" => "ON" },
        },
    });
    let group = |category: &str| {
        Object::Dictionary(dictionary! {
            "Category" => vec![Object::Name(category.as_bytes().to_vec())],
            "Event" => category,
            "OCGs" => vec![Object::Reference(ocg)],
        })
    };
    let properties = dictionary! {
        "OCGs" => vec![Object::Reference(ocg)],
        "D" => dictionary! {
            "AS" => vec![group("View"), group("Print"), group("Export")],
            "ON" => vec![Object::Reference(ocg)],
            "Order" => Vec::<Object>::new(),
            "RBGroups" => Vec::<Object>::new(),
        },
    };
    doc.get_dictionary_mut(root)
        .map_err(|e| format!("catalog: {e}"))?
        .set("OCProperties", Object::Dictionary(properties));
    Ok(ocg)
}

struct Call<'a> {
    ocg: ObjectId,
    gstate: ObjectId,
    font: ObjectId,
    text: &'a str,
    page_count: usize,
    forms: Vec<([u64; 4], ObjectId)>,
    patched: HashSet<ObjectId>,
}

/// `AddWatermarks` for one OCR page's text on the selected pages.
fn add_watermarks(
    doc: &mut Document,
    pages: &[ObjectId],
    selected: &[usize],
    text: &str,
) -> Result<(), String> {
    let ocg = prepare_oc_properties(doc).map_err(|e| format!("prepare optional content: {e}"))?;
    let gstate = doc.add_object(
        dictionary! { "Type" => "ExtGState", "CA" => Object::Real(1.0), "ca" => Object::Real(1.0) },
    );
    let font = doc.add_object(dictionary! {
        "Type" => "Font", "Subtype" => "Type1", "BaseFont" => "Helvetica", "Encoding" => "WinAnsiEncoding",
    });
    let mut call = Call {
        ocg,
        gstate,
        font,
        text,
        page_count: pages.len(),
        forms: Vec::new(),
        patched: HashSet::new(),
    };
    for &page_nr in selected {
        add_page_watermark(doc, &mut call, pages[page_nr - 1], page_nr)
            .map_err(|e| format!("page {page_nr}: {e}"))?;
    }
    Ok(())
}

fn add_page_watermark(
    doc: &mut Document,
    call: &mut Call,
    page_id: ObjectId,
    page_nr: usize,
) -> Result<(), String> {
    // Inherited attributes.
    let media = rect_attr(doc, page_id, b"MediaBox")?.unwrap_or(Rect {
        llx: DEFAULT_MEDIA_BOX[0],
        lly: DEFAULT_MEDIA_BOX[1],
        urx: DEFAULT_MEDIA_BOX[2],
        ury: DEFAULT_MEDIA_BOX[3],
    });
    let crop = rect_attr(doc, page_id, b"CropBox")?;
    let rotate = match inherited(doc, page_id, b"Rotate")? {
        Some(object) => number(doc, object).map_or(0, |v| v as i64),
        None => 0,
    };
    let mut resources = match inherited(doc, page_id, b"Resources")? {
        None | Some(Object::Null) => None,
        Some(Object::Dictionary(d)) => Some(d.clone()),
        Some(_) => return Err("update resources: page /Resources is not a dictionary".to_owned()),
    };
    // normalizePageWatermark
    let mut vp = crop.unwrap_or(media);
    let mut new_boxes: Option<Rect> = None;
    if rotate != 0 {
        if matches!(rotate, 90 | -90 | 270 | -270) {
            let w = vp.width();
            vp.urx = vp.llx + vp.height();
            vp.ury = vp.lly + w;
        }
        new_boxes = Some(vp);
    }
    // createForm
    let (text, unique) = format_text(call.text, page_nr, call.page_count);
    let column = write_column(&text, vp.width(), vp.height());
    let bb = column.bb;
    let cached = (!unique)
        .then(|| {
            call.forms
                .iter()
                .find(|(k, _)| *k == bb.bits())
                .map(|(_, id)| *id)
        })
        .flatten();
    let form_id = match cached {
        Some(id) => id,
        None => {
            let res = doc.add_object(dictionary! {
                "Font" => dictionary! { "F1" => Object::Reference(call.font) },
                "ProcSet" => proc_set(),
            });
            let mut bbox = bb;
            bbox.translate(-bb.llx, -bb.lly);
            let stream = Stream::new(
                dictionary! {
                    "Type" => "XObject",
                    "Subtype" => "Form",
                    "BBox" => rect_array(&bbox),
                    "Matrix" => vec![1.into(), 0.into(), 0.into(), 1.into(), 0.into(), 0.into()],
                    "OC" => Object::Reference(call.ocg),
                    "Resources" => Object::Reference(res),
                },
                column.content,
            );
            let id = doc.add_object(compress(stream)?);
            call.forms.push((bb.bits(), id));
            id
        }
    };
    // addPageWatermarkResources
    let (gs_id, xo_id) = {
        let fresh = |doc: &Document,
                     dict: &mut Dictionary,
                     category: &str,
                     prefix: &str,
                     target: ObjectId|
         -> Result<String, String> {
            let (mut sub, id) = match dict.get(category.as_bytes()) {
                Ok(object) => {
                    let (_, value) = doc
                        .dereference(object)
                        .map_err(|e| format!("{category}: dereference dictionary: {e}"))?;
                    let sub = value
                        .as_dict()
                        .map_err(|_| format!("{category}: missing dictionary"))?
                        .clone();
                    let mut n = 0;
                    while sub.has(format!("{prefix}{n}").as_bytes()) {
                        n += 1;
                    }
                    (sub, format!("{prefix}{n}"))
                }
                Err(_) => (Dictionary::new(), format!("{prefix}0")),
            };
            sub.set(id.as_str(), Object::Reference(target));
            dict.set(category, Object::Dictionary(sub));
            Ok(id)
        };
        let mut dict = resources.take().unwrap_or_default();
        let gs = fresh(doc, &mut dict, "ExtGState", "GS", call.gstate)?;
        let xo = fresh(doc, &mut dict, "XObject", "Fm", form_id)?;
        resources = Some(dict);
        (gs, xo)
    };
    // Matrix of the drawn stamp (wmContent).
    let r = (vp.height() / vp.width()).atan() * RAD_TO_DEG;
    let r = if bb.width() / bb.height() < 1.0 {
        r - 90.0
    } else {
        r
    };
    let (sin, cos) = ((r * DEG_TO_RAD).sin(), (r * DEG_TO_RAD).cos());
    let dy0 = bb.lly;
    let (llx, lly) = (
        vp.llx + (vp.width() / 2.0 - bb.width() / 2.0),
        vp.lly + (vp.height() / 2.0 - bb.height() / 2.0),
    );
    let dx =
        llx + bb.width() / 2.0 + 0.0 + sin * (bb.height() / 2.0 + dy0) - cos * bb.width() / 2.0;
    let dy =
        lly + bb.height() / 2.0 + 0.0 - cos * (bb.height() / 2.0 + dy0) - sin * bb.width() / 2.0;
    let m = transform_matrix(sin, cos, dx, dy);
    let wm = format!(
        " /Artifact <</Subtype /Watermark /Type /Pagination >>BDC q {} cm /{gs_id} gs /{xo_id} Do Q EMC ",
        matrix_cm(&m)
    );
    // Page dictionary updates.
    {
        let page = doc.get_dictionary_mut(page_id).map_err(|e| e.to_string())?;
        page.set(
            "Resources",
            Object::Dictionary(resources.unwrap_or_default()),
        );
        if let Some(b) = new_boxes {
            page.set("MediaBox", rect_array(&b));
            page.set("CropBox", rect_array(&b));
            page.remove(b"Rotate");
        }
    }
    patch_contents(doc, call, page_id, &wm, rotate, vp).map_err(|e| format!("update contents: {e}"))
}

/// `ContentBytesForPageRotation`.
fn rotation_bytes(rot: i64, w: f64, h: f64) -> String {
    let (dx, dy) = match rot {
        90 | -270 => (0.0, h),
        -90 | 270 => (w, 0.0),
        180 | -180 => (w, h),
        _ => (0.0, 0.0),
    };
    let r = -(rot as f64);
    let m = transform_matrix((r * DEG_TO_RAD).sin(), (r * DEG_TO_RAD).cos(), dx, dy);
    format!("{} cm ", matrix_cm(&m))
}

fn stream_text(stream: &Stream) -> Result<Vec<u8>, String> {
    if stream.dict.get(b"Filter").is_ok() {
        stream
            .decompressed_content()
            .map_err(|e| format!("decode stream: {e}"))
    } else {
        Ok(stream.content.clone())
    }
}

fn patched_stream(
    stream: &Stream,
    wm: &str,
    rotate: i64,
    vp: Rect,
    is_last: bool,
) -> Result<Stream, String> {
    let old = stream_text(stream)?;
    let content: Vec<u8> = if rotate == 0 {
        let mut c = wm.as_bytes().to_vec();
        c.extend(old);
        c
    } else {
        let mut c = format!(" q {}", rotation_bytes(rotate, vp.width(), vp.height())).into_bytes();
        c.extend(old);
        if is_last {
            c.extend_from_slice(b" Q");
        }
        c
    };
    let mut dict = stream.dict.clone();
    dict.remove(b"Filter");
    dict.remove(b"DecodeParms");
    dict.remove(b"Length");
    compress(Stream::new(dict, content))
}

fn patch_contents(
    doc: &mut Document,
    call: &mut Call,
    page_id: ObjectId,
    wm: &str,
    rotate: i64,
    vp: Rect,
) -> Result<(), String> {
    let contents = doc
        .get_dictionary(page_id)
        .map_err(|e| e.to_string())?
        .get(b"Contents")
        .ok()
        .cloned();
    let Some(contents) = contents else {
        // insertPageContentsForWM
        let stream = compress(Stream::new(Dictionary::new(), wm.as_bytes().to_vec()))?;
        let id = doc.add_object(stream);
        doc.get_dictionary_mut(page_id)
            .map_err(|e| e.to_string())?
            .set("Contents", Object::Reference(id));
        return Ok(());
    };
    let patch_one =
        |doc: &mut Document, call: &mut Call, id: ObjectId, is_last: bool| -> Result<(), String> {
            if call.patched.contains(&id) {
                return Ok(());
            }
            let stream = doc
                .get_object(id)
                .map_err(|e| format!("content stream obj#{}: {e}", id.0))?
                .as_stream()
                .map_err(|_| format!("content stream obj#{}: expected stream dictionary", id.0))?;
            let patched = patched_stream(stream, wm, rotate, vp, is_last)?;
            doc.objects.insert(id, Object::Stream(patched));
            call.patched.insert(id);
            Ok(())
        };
    match contents {
        Object::Reference(id) => {
            let target = doc
                .get_object(id)
                .map_err(|e| format!("Contents: {e}"))?
                .clone();
            match target {
                Object::Stream(_) => patch_one(doc, call, id, true),
                Object::Array(items) => {
                    patch_array(doc, call, page_id, items, wm, rotate, patch_one)
                }
                other => Err(format!(
                    "Contents: expected stream dictionary or array, got {}",
                    type_name(&other)
                )),
            }
        }
        Object::Array(items) => patch_array(doc, call, page_id, items, wm, rotate, patch_one),
        Object::Stream(stream) => {
            // A direct stream cannot occur in a valid PDF; patch it in the page.
            let patched = patched_stream(&stream, wm, rotate, vp, true)?;
            doc.get_dictionary_mut(page_id)
                .map_err(|e| e.to_string())?
                .set("Contents", Object::Stream(patched));
            Ok(())
        }
        other => Err(format!(
            "Contents: expected stream dictionary or array, got {}",
            type_name(&other)
        )),
    }
}

fn patch_array(
    doc: &mut Document,
    call: &mut Call,
    page_id: ObjectId,
    items: Vec<Object>,
    wm: &str,
    rotate: i64,
    patch_one: impl Fn(&mut Document, &mut Call, ObjectId, bool) -> Result<(), String>,
) -> Result<(), String> {
    let Some(first) = items.first() else {
        return Ok(());
    };
    let Object::Reference(first_id) = first else {
        return Err(format!(
            "content array entry 1: expected indirect reference, got {}",
            type_name(first)
        ));
    };
    if call.patched.contains(first_id) {
        return Ok(());
    }
    patch_one(doc, call, *first_id, items.len() == 1).map_err(|e| {
        format!(
            "content array entry 1 obj#{}: patch stream: {e}",
            first_id.0
        )
    })?;
    if items.len() == 1 {
        return Ok(());
    }
    // newContentStreamForWatermark: " Q " closes the rotation wrapper, else empty.
    let tail: &[u8] = if rotate != 0 { b" Q " } else { b"" };
    let _ = wm;
    let id = doc.add_object(compress(Stream::new(Dictionary::new(), tail.to_vec()))?);
    let mut all = items;
    all.push(Object::Reference(id));
    doc.get_dictionary_mut(page_id)
        .map_err(|e| e.to_string())?
        .set("Contents", Object::Array(all));
    call.patched.insert(id);
    Ok(())
}

/// Stamp one page per OCR page with non-blank text, as Go's loop of
/// `AddWatermarksFile` calls does. Blank text returns the input unchanged.
///
/// Errors use the Go kind prefix: `add watermark to page N` for a failure on
/// OCR page N, including unreadable input.
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
    for (page_number, text) in stamps {
        let selected: Vec<usize> = if page_number <= pages.len() {
            vec![page_number]
        } else {
            (1..=pages.len()).collect()
        };
        add_watermarks(&mut doc, &pages, &selected, text)
            .map_err(|error| format!("add watermark to page {page_number}: {error}"))?;
    }
    let mut out = Vec::new();
    doc.save_to(&mut out)
        .map_err(|error| format!("save stamped PDF: {error}"))?;
    Ok(fix_reals(out))
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
        let error = stamp_pdf(b"not a pdf", " \u{c}hello").unwrap_err();
        assert!(
            error.starts_with("add watermark to page 1:"),
            "unexpected error: {error}"
        );
    }

    #[test]
    fn escape_matches_types_escape() {
        assert_eq!(pdf_escape(b"a(b)c\\d"), b"a\\(b\\)c\\\\d");
        assert_eq!(pdf_escape(b"a\tb\xfc\r"), b"a\\tb\xfc\\r");
    }

    #[test]
    fn win_ansi_maps_like_the_go_encoder() {
        assert_eq!(encode_win_ansi("Gr\u{fc}\u{df}e"), b"Gr\xfc\xdfe");
        assert_eq!(
            encode_win_ansi("5\u{20ac} \u{201c}q\u{201d}"),
            b"5\x80 \x93q\x94"
        );
        assert_eq!(encode_win_ansi("\u{81}\u{a0}\u{ad}"), b"\x81\xa0\xad");
        assert_eq!(encode_win_ansi("\u{65e5}\u{1F600}a\u{301}"), b"  a ");
    }

    #[test]
    fn format_text_expands_placeholders_like_pdfcpu() {
        assert_eq!(format_text("p%p of %P", 3, 7), ("p3 of 7".to_owned(), true));
        assert_eq!(format_text("%p10x", 3, 7).0, "13x");
        assert_eq!(
            format_text("100%% sure%", 1, 1),
            ("100% sure".to_owned(), false)
        );
        assert_eq!(format_text("50%x", 1, 1).0, "50x");
        assert_eq!(format_text("%v", 1, 1).0, "v0.16.2");
    }

    #[test]
    fn multiline_split_keeps_empty_lines() {
        assert_eq!(split_multiline("a\n\nb"), ["a", "", "b"]);
        assert_eq!(split_multiline("a\\nb"), ["a", "b"]);
        assert_eq!(split_multiline("a\n"), ["a"]);
        assert_eq!(split_multiline("\na"), ["", "a"]);
    }

    #[test]
    fn real_travels_with_full_precision() {
        let doc_bytes = fix_reals(b"[/SymdeskReal=0.774143603638475 /SymdeskReal=-1.5]".to_vec());
        assert_eq!(doc_bytes, b"[ 0.774143603638475  -1.5]");
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
        let error = inherited(&doc, ids[0], b"MediaBox").unwrap_err();
        assert!(error.contains("too deep"), "unexpected error: {error}");
    }
}
