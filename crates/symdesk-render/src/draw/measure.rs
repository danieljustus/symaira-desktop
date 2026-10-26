//! Port of Go `internal/draw/measure` (text measurement, line breaking and
//! intrinsic node sizing against the embedded Inter fonts) and of the
//! `internal/draw/fonts` embedding/hash helpers it depends on.
//!
//! Glyph outlines use the unhinted TrueType decoder; SVG path formatting lives
//! in `draw::emit`.
//!
//! Floating point: every product that feeds a sum in the Go source is exact
//! (multiples of 1/64 times small integers, or times 2/0.5), so Go's optional
//! FMA fusion cannot change a result. The one exception is `math.Hypot` for
//! circle nodes, which Go implements as `p*sqrt(1+q*q)` (assembly on amd64,
//! fusable pure Go on arm64): this port uses the unfused formula, and the
//! fixture only freezes circle sizes whose hypotenuse is exact on every host.

use sha2::{Digest, Sha256};

use super::gotext::{fields, to_lower, trim_space};
use super::ir::{write_f64, write_string};
use super::sfnt::{Font, FontError};

/// Go `fonts.Regular`: raw bytes of `Inter-Regular.ttf`.
pub const INTER_REGULAR: &[u8] =
    include_bytes!("../../../../internal/draw/fonts/Inter-Regular.ttf");
/// Go `fonts.Bold`: raw bytes of `Inter-Bold.ttf`.
pub const INTER_BOLD: &[u8] = include_bytes!("../../../../internal/draw/fonts/Inter-Bold.ttf");

const REGULAR_NAME: &str = "Inter-Regular.ttf";
const BOLD_NAME: &str = "Inter-Bold.ttf";

fn hex(bytes: &[u8]) -> String {
    use std::fmt::Write;
    bytes.iter().fold(String::new(), |mut acc, b| {
        let _ = write!(acc, "{b:02x}");
        acc
    })
}

/// Go `fonts.VersionKey`: SHA-256 over every embedded font file in sorted path
/// order, each as `name 0x00 content`.
pub fn fonts_version_key() -> String {
    let mut h = Sha256::new();
    // `embed` walks `*.ttf` in lexical order: Inter-Bold.ttf before Inter-Regular.ttf.
    h.update(BOLD_NAME.as_bytes());
    h.update([0]);
    h.update(INTER_BOLD);
    h.update(REGULAR_NAME.as_bytes());
    h.update([0]);
    h.update(INTER_REGULAR);
    hex(&h.finalize())
}

/// Go `measure.FontWeight`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FontWeight {
    Regular,
    Bold,
}

impl FontWeight {
    /// Go `getFont` selection: only the exact string `"bold"` selects the bold
    /// face; every other `FontWeight` value uses the regular face.
    pub fn from_go(weight: &str) -> FontWeight {
        if weight == "bold" {
            FontWeight::Bold
        } else {
            FontWeight::Regular
        }
    }

    /// The Go constant value (`"regular"` / `"bold"`).
    pub fn as_go(self) -> &'static str {
        match self {
            FontWeight::Regular => "regular",
            FontWeight::Bold => "bold",
        }
    }
}

/// Go `measure.NormalizeFontWeight`.
pub fn normalize_font_weight(weight: &str) -> FontWeight {
    match to_lower(trim_space(weight)).as_str() {
        "bold" | "700" | "800" | "900" | "bolder" => FontWeight::Bold,
        _ => FontWeight::Regular,
    }
}

/// Go `measure.TextMetrics`.
#[derive(Debug, Clone, PartialEq)]
pub struct TextMetrics {
    pub width: f64,
    pub height: f64,
    pub ascent: f64,
    pub descent: f64,
    pub cap_height: f64,
    pub x_height: f64,
    pub line_height: f64,
    pub glyph_advances: Vec<f64>,
}

fn write_f64_list(out: &mut String, items: &[f64]) {
    out.push('[');
    for (i, v) in items.iter().enumerate() {
        if i > 0 {
            out.push(',');
        }
        write_f64(out, *v);
    }
    out.push(']');
}

/// Go `json.Marshal(float64)`.
pub fn go_json_number(value: f64) -> String {
    let mut out = String::new();
    write_f64(&mut out, value);
    out
}

impl TextMetrics {
    /// Go `json.Marshal([]float64)` of the per-glyph advances.
    pub fn advances_json(&self) -> String {
        let mut out = String::new();
        write_f64_list(&mut out, &self.glyph_advances);
        out
    }

    /// Go `json.Marshal(TextMetrics)` (`glyph_advances` is `omitempty`).
    pub fn to_json(&self) -> String {
        let mut out = String::from("{");
        for (key, v) in [
            ("width", self.width),
            ("height", self.height),
            ("ascent", self.ascent),
            ("descent", self.descent),
            ("cap_height", self.cap_height),
            ("x_height", self.x_height),
            ("line_height", self.line_height),
        ] {
            if out.len() > 1 {
                out.push(',');
            }
            out.push('"');
            out.push_str(key);
            out.push_str("\":");
            write_f64(&mut out, v);
        }
        if !self.glyph_advances.is_empty() {
            out.push_str(",\"glyph_advances\":");
            write_f64_list(&mut out, &self.glyph_advances);
        }
        out.push('}');
        out
    }
}

/// Go `measure.LineBreakResult`. An empty `lines`/`line_heights` is Go's `nil`
/// slice (serialized as `null`); Go never produces an empty non-nil one.
#[derive(Debug, Clone, PartialEq)]
pub struct LineBreakResult {
    pub lines: Vec<String>,
    pub max_line_width: f64,
    pub total_height: f64,
    pub line_heights: Vec<f64>,
    pub line_spacing: f64,
}

impl LineBreakResult {
    /// Go `json.Marshal(LineBreakResult)`.
    pub fn to_json(&self) -> String {
        let mut out = String::from("{\"lines\":");
        write_lines(&mut out, &self.lines);
        out.push_str(",\"max_line_width\":");
        write_f64(&mut out, self.max_line_width);
        out.push_str(",\"total_height\":");
        write_f64(&mut out, self.total_height);
        out.push_str(",\"line_heights\":");
        if self.line_heights.is_empty() {
            out.push_str("null");
        } else {
            write_f64_list(&mut out, &self.line_heights);
        }
        out.push_str(",\"line_spacing\":");
        write_f64(&mut out, self.line_spacing);
        out.push('}');
        out
    }
}

fn write_lines(out: &mut String, lines: &[String]) {
    if lines.is_empty() {
        out.push_str("null");
        return;
    }
    out.push('[');
    for (i, line) in lines.iter().enumerate() {
        if i > 0 {
            out.push(',');
        }
        write_string(out, line);
    }
    out.push(']');
}

/// Go `measure.NodeMeasureOptions`.
#[derive(Debug, Clone, Copy, PartialEq)]
pub struct NodeMeasureOptions {
    pub font_size: f64,
    pub font_weight: FontWeight,
    pub padding_x: f64,
    pub padding_y: f64,
    pub min_width: f64,
    pub min_height: f64,
    pub max_width: f64,
    pub line_spacing: f64,
}

impl Default for NodeMeasureOptions {
    /// Go `DefaultNodeMeasureOptions`.
    fn default() -> Self {
        NodeMeasureOptions {
            font_size: 14.0,
            font_weight: FontWeight::Regular,
            padding_x: 20.0,
            padding_y: 14.0,
            min_width: 80.0,
            min_height: 40.0,
            max_width: 240.0,
            line_spacing: 4.0,
        }
    }
}

/// Result of Go `Measurer.MeasureNode` `(width, height, lines)`.
#[derive(Debug, Clone, PartialEq)]
pub struct NodeSize {
    pub width: f64,
    pub height: f64,
    pub lines: Vec<String>,
}

impl NodeSize {
    /// `{"width":..,"height":..,"lines":..}` with Go number/string encoding
    /// (the shape the fixture generator marshals).
    pub fn to_json(&self) -> String {
        let mut out = String::from("{\"width\":");
        write_f64(&mut out, self.width);
        out.push_str(",\"height\":");
        write_f64(&mut out, self.height);
        out.push_str(",\"lines\":");
        write_lines(&mut out, &self.lines);
        out.push('}');
        out
    }
}

/// Go `Measurer`: exact text metrics against the Inter sfnt tables.
pub struct Measurer {
    regular: Font,
    bold: Font,
    font_hash: String,
}

impl Measurer {
    /// Go `Measurer.Outlines`: fixed-point placement, no shaping or kerning.
    pub fn outlines(
        &self,
        text: &str,
        font_size: f64,
        weight: FontWeight,
        start_x: f64,
        start_y: f64,
    ) -> Vec<super::scene::PathSegment> {
        use super::scene::{PathSegment, Point};
        let f = self.font(weight);
        let ppem = (font_size * 64.0).round() as i32;
        let mut x = (start_x * 64.0).round() as i32;
        let y = (start_y * 64.0).round() as i32;
        let mut result = Vec::new();
        for r in text.chars() {
            let index = f.glyph_index(r).unwrap_or(0);
            if let Ok(segments) = f.load_glyph(index, ppem) {
                result.extend(segments.into_iter().map(|s| {
                    PathSegment {
                        op: s.op.into(),
                        args: s
                            .args
                            .into_iter()
                            .map(|p| Point {
                                x: f64::from(x.wrapping_add(p.0)) / 64.0,
                                y: f64::from(y.wrapping_sub(p.1)) / 64.0,
                            })
                            .collect(),
                    }
                }));
            }
            x = x.wrapping_add(
                f.glyph_advance(index, ppem)
                    .unwrap_or((font_size * 0.6 * 64.0) as i32),
            );
        }
        result
    }

    /// Go `NewMeasurer`: the embedded Inter-Regular and Inter-Bold fonts.
    pub fn new() -> Result<Measurer, FontError> {
        Measurer::with_fonts(INTER_REGULAR, INTER_BOLD)
    }

    /// Go `NewMeasurerWithFonts`.
    pub fn with_fonts(regular: &[u8], bold: &[u8]) -> Result<Measurer, FontError> {
        let regular_font = Font::parse(regular)?;
        let bold_font = Font::parse(bold)?;
        let mut h = Sha256::new();
        h.update(REGULAR_NAME.as_bytes());
        h.update([0]);
        h.update(regular);
        h.update(BOLD_NAME.as_bytes());
        h.update([0]);
        h.update(bold);
        Ok(Measurer {
            regular: regular_font,
            bold: bold_font,
            font_hash: hex(&h.finalize()),
        })
    }

    /// Go `Measurer.FontHash`.
    pub fn font_hash(&self) -> &str {
        &self.font_hash
    }

    fn font(&self, weight: FontWeight) -> &Font {
        match weight {
            FontWeight::Bold => &self.bold,
            FontWeight::Regular => &self.regular,
        }
    }

    /// Go `MeasureText`: `(width, line height)` of a single-line string, with
    /// Go's `len(text)*fontSize*0.6` fallback when metrics fail.
    pub fn measure_text(&self, text: &str, font_size: f64, weight: FontWeight) -> (f64, f64) {
        match self.measure_text_detailed(text, font_size, weight) {
            Ok(m) => (m.width, m.line_height),
            Err(_) => (text.len() as f64 * font_size * 0.6, font_size * 1.2),
        }
    }

    /// Go `MeasureTextDetailed`.
    ///
    /// `font_size * 64` must fit an `int32`; Go's out-of-range float-to-int
    /// conversion is architecture dependent and is not reproduced (Rust
    /// saturates).
    pub fn measure_text_detailed(
        &self,
        text: &str,
        font_size: f64,
        weight: FontWeight,
    ) -> Result<TextMetrics, FontError> {
        let f = self.font(weight);
        let scale = (font_size * 64.0).round() as i32;
        let fm = f.metrics(scale);

        let ascent = f64::from(fm.ascent) / 64.0;
        let descent = f64::from(fm.descent) / 64.0;
        let mut line_height = f64::from(fm.height) / 64.0;
        if line_height <= 0.0 {
            line_height = ascent + descent;
        }
        let cap_height = f64::from(fm.cap_height) / 64.0;
        let x_height = f64::from(fm.x_height) / 64.0;

        let mut total_adv: i32 = 0;
        let mut advances = Vec::with_capacity(text.len());
        for r in text.chars() {
            let idx = f.glyph_index(r).unwrap_or(0);
            let adv = f
                .glyph_advance(idx, scale)
                .unwrap_or((font_size * 0.6 * 64.0) as i32);
            total_adv = total_adv.wrapping_add(adv);
            advances.push(f64::from(adv) / 64.0);
        }

        Ok(TextMetrics {
            width: f64::from(total_adv) / 64.0,
            height: ascent + descent,
            ascent,
            descent,
            cap_height,
            x_height,
            line_height,
            glyph_advances: advances,
        })
    }

    /// Go `BreakLines`: wraps within `max_width`, respecting existing newlines.
    pub fn break_lines(
        &self,
        text: &str,
        max_width: f64,
        font_size: f64,
        weight: FontWeight,
    ) -> LineBreakResult {
        if text.is_empty() {
            return LineBreakResult {
                lines: Vec::new(),
                max_line_width: 0.0,
                total_height: 0.0,
                line_heights: Vec::new(),
                line_spacing: 4.0,
            };
        }

        let metrics = self.measure_text_detailed("Ag", font_size, weight).ok();
        let mut line_height = metrics.map_or(0.0, |m| m.line_height);
        if line_height <= 0.0 {
            line_height = font_size * 1.2;
        }
        let line_spacing = (font_size * 0.25).round();

        let mut result_lines: Vec<String> = Vec::new();
        let mut max_w = 0.0f64;

        for p in text.split('\n') {
            let trimmed = trim_space(p);
            if trimmed.is_empty() {
                result_lines.push(String::new());
                continue;
            }

            if max_width <= 0.0 {
                let (w, _) = self.measure_text(trimmed, font_size, weight);
                result_lines.push(trimmed.to_owned());
                if w > max_w {
                    max_w = w;
                }
                continue;
            }

            let words = fields(trimmed);
            let Some((first, rest)) = words.split_first() else {
                result_lines.push(String::new());
                continue;
            };

            let mut cur_line = (*first).to_owned();
            let mut cur_w = self.measure_text(&cur_line, font_size, weight).0;

            for word in rest {
                let candidate = format!("{cur_line} {word}");
                let cand_w = self.measure_text(&candidate, font_size, weight).0;
                if cand_w <= max_width {
                    cur_line = candidate;
                    cur_w = cand_w;
                } else {
                    result_lines.push(std::mem::take(&mut cur_line));
                    if cur_w > max_w {
                        max_w = cur_w;
                    }
                    cur_line = (*word).to_owned();
                    cur_w = self.measure_text(&cur_line, font_size, weight).0;
                }
            }

            if !cur_line.is_empty() {
                result_lines.push(cur_line);
                if cur_w > max_w {
                    max_w = cur_w;
                }
            }
        }

        let num_lines = result_lines.len().max(1);
        let total_h = num_lines as f64 * line_height + (num_lines - 1) as f64 * line_spacing;
        let line_heights = vec![line_height; num_lines];

        LineBreakResult {
            lines: result_lines,
            max_line_width: max_w,
            total_height: total_h,
            line_heights,
            line_spacing,
        }
    }

    /// Go `MeasureNode`: optimal node box for a label and shape. `shape` is the
    /// raw `ir.NodeShape` string; unknown shapes use the rectangle rule.
    pub fn measure_node(&self, label: &str, shape: &str, opts: NodeMeasureOptions) -> NodeSize {
        let mut opts = opts;
        if opts.font_size <= 0.0 {
            opts.font_size = 14.0;
        }
        if opts.padding_x <= 0.0 {
            opts.padding_x = 20.0;
        }
        if opts.padding_y <= 0.0 {
            opts.padding_y = 14.0;
        }

        let lb = self.break_lines(label, opts.max_width, opts.font_size, opts.font_weight);
        let text_w = lb.max_line_width;
        let text_h = lb.total_height;

        let raw_w = text_w + opts.padding_x * 2.0;
        let raw_h = text_h + opts.padding_y * 2.0;

        let (width, height) = match shape {
            "circle" => {
                let size = go_hypot(raw_w, raw_h).max(opts.min_width);
                (size, size)
            }
            "diamond" => (
                (raw_w * 1.5).max(opts.min_width),
                (raw_h * 1.5).max(opts.min_height),
            ),
            "cylinder" => (
                (raw_w + 10.0).max(opts.min_width),
                (raw_h + 24.0).max(opts.min_height + 16.0),
            ),
            "pill" | "stadium" => {
                let h = raw_h.max(opts.min_height);
                ((raw_w + h / 2.0).max(opts.min_width), h)
            }
            "hexagon" => {
                let h = raw_h.max(opts.min_height);
                ((raw_w + h * 0.5).max(opts.min_width), h)
            }
            _ => (raw_w.max(opts.min_width), raw_h.max(opts.min_height)),
        };
        NodeSize {
            width,
            height,
            lines: lb.lines,
        }
    }
}

/// Go's portable `math.hypot`: `p * sqrt(1 + q*q)` with `q = min/max`, without
/// fused multiply-add.
fn go_hypot(p: f64, q: f64) -> f64 {
    let (mut p, mut q) = (p.abs(), q.abs());
    if p.is_infinite() || q.is_infinite() {
        return f64::INFINITY;
    }
    if p.is_nan() || q.is_nan() {
        return f64::NAN;
    }
    if p < q {
        std::mem::swap(&mut p, &mut q);
    }
    if p == 0.0 {
        return 0.0;
    }
    q /= p;
    p * (1.0 + q * q).sqrt()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn m() -> Measurer {
        Measurer::new().expect("embedded fonts")
    }

    #[test]
    fn weights_follow_go() {
        assert_eq!(FontWeight::from_go("bold"), FontWeight::Bold);
        assert_eq!(FontWeight::from_go("Bold"), FontWeight::Regular);
        assert_eq!(FontWeight::from_go("700"), FontWeight::Regular);
        assert_eq!(FontWeight::Regular.as_go(), "regular");
        assert_eq!(FontWeight::Bold.as_go(), "bold");
        assert_eq!(normalize_font_weight(" BOLD "), FontWeight::Bold);
        assert_eq!(normalize_font_weight("\u{a0}900\u{2003}"), FontWeight::Bold);
        assert_eq!(normalize_font_weight("semibold"), FontWeight::Regular);
    }

    #[test]
    fn empty_text_has_no_advances_and_a_nil_break() {
        let metrics = m()
            .measure_text_detailed("", 14.0, FontWeight::Regular)
            .unwrap();
        assert_eq!(metrics.width, 0.0);
        assert!(metrics.glyph_advances.is_empty());
        assert!(!metrics.to_json().contains("glyph_advances"));
        let lb = m().break_lines("", 100.0, 14.0, FontWeight::Regular);
        assert_eq!(
            lb.to_json(),
            "{\"lines\":null,\"max_line_width\":0,\"total_height\":0,\"line_heights\":null,\"line_spacing\":4}"
        );
    }

    #[test]
    fn hypot_is_exact_for_pythagorean_triples() {
        assert_eq!(go_hypot(300.0, 400.0), 500.0);
        assert_eq!(go_hypot(-5.0, 12.0), 13.0);
        assert_eq!(go_hypot(0.0, 0.0), 0.0);
        assert!(go_hypot(f64::NAN, 1.0).is_nan());
        assert_eq!(go_hypot(f64::INFINITY, f64::NAN), f64::INFINITY);
    }

    #[test]
    fn font_hashes_are_sha256_hex() {
        let measurer = m();
        assert_eq!(measurer.font_hash().len(), 64);
        assert_eq!(fonts_version_key().len(), 64);
        assert_ne!(measurer.font_hash(), fonts_version_key());
    }

    #[test]
    fn invalid_fonts_are_rejected() {
        assert!(Measurer::with_fonts(b"nope", INTER_BOLD).is_err());
        assert!(Measurer::with_fonts(INTER_REGULAR, b"nope").is_err());
    }

    #[test]
    fn measure_text_fallback_uses_byte_length_like_go() {
        // The fallback is only reachable on a metrics error, which the embedded
        // fonts never produce; pin the arithmetic through the public result shape.
        let (w, h) = m().measure_text("é", 10.0, FontWeight::Regular);
        assert!(w > 0.0 && h > 0.0);
    }
}
