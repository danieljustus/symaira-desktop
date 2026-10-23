//! Minimal TrueType (`sfnt`) reader: the subset of Go `golang.org/x/image/font/sfnt`
//! (v0.47.0) that `internal/draw/measure` calls for text measurement.
//!
//! Covered: the table directory checks, `head`, `maxp`, `hhea`, `hmtx` and
//! `OS/2` (version >= 2) validation, `cmap` format 4/6/12 glyph lookup with the
//! "widest platform encoding wins" selection, advance widths and the font
//! metrics (`Ascent`, `Descent`, `Height`, `XHeight`, `CapHeight`) at
//! `font.HintingNone`, including `fixed.Int26_6` (int32) arithmetic.
//!
//! Not covered (the embedded Inter fonts never need them): glyph outlines
//! (`glyf`/`loca` parsing, `LoadGlyph`), kerning, `post`, CFF/OpenType-PostScript
//! fonts, `cmap` format 0 and `OS/2` versions below 2. Fonts needing those are
//! rejected with [`FontError::Unsupported`] instead of being measured wrongly.
//! Go additionally validates the `glyf`/`loca`/`post`/`kern`/`GPOS` tables at
//! parse time; this reader does not, so accept/reject parity for corrupted font
//! files is not frozen (only the embedded fonts are).

use std::fmt;

const MAX_NUM_TABLES: usize = 256;
const MAX_TABLE_OFFSET: u32 = 1 << 29;
const MAX_TABLE_LENGTH: u32 = 1 << 29;
const MAX_CMAP_SEGMENTS: u32 = 20_000;

/// Why a font could not be loaded.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum FontError {
    /// Structurally invalid for the subset this reader needs (message mirrors the
    /// Go error it corresponds to).
    Invalid(&'static str),
    /// Valid sfnt features this reader deliberately does not implement.
    Unsupported(&'static str),
}

impl fmt::Display for FontError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            FontError::Invalid(m) => write!(f, "sfnt: invalid {m}"),
            FontError::Unsupported(m) => write!(f, "sfnt: unsupported {m}"),
        }
    }
}

impl std::error::Error for FontError {}

#[derive(Clone, Copy, Default)]
struct Table {
    offset: u32,
    length: u32,
}

#[derive(Clone)]
struct Cmap4Entry {
    end: u16,
    start: u16,
    delta: u16,
    offset: u16,
}

#[derive(Clone)]
enum Cmap {
    /// Format 4 segments plus the byte range (absolute offset, length) of the
    /// glyph-id array that follows them.
    Fmt4 {
        entries: Vec<Cmap4Entry>,
        indexes_base: usize,
        indexes_length: u32,
    },
    Fmt6 {
        first_code: u16,
        entries: Vec<u16>,
    },
    Fmt12 {
        /// `(start, end, delta)`.
        entries: Vec<(u32, u32, u32)>,
    },
}

/// A parsed font (owns its bytes).
#[derive(Clone)]
pub struct Font {
    data: Vec<u8>,
    units_per_em: i32,
    ascent: i32,
    descent: i32,
    line_gap: i32,
    x_height: i32,
    cap_height: i32,
    num_h_metrics: i32,
    num_glyphs: i32,
    hmtx: Table,
    cmap: Cmap,
}

/// A glyph lookup that Go reports as an `error` (out-of-range index, table read
/// outside the font); callers fall back exactly as the Go code does.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct GlyphError;

/// `font.Metrics` at 26.6 fixed point, as produced by `Font.Metrics`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct FixedMetrics {
    pub height: i32,
    pub ascent: i32,
    pub descent: i32,
    pub x_height: i32,
    pub cap_height: i32,
}

fn u16_at(b: &[u8], at: usize) -> Option<u16> {
    let s = b.get(at..at.checked_add(2)?)?;
    Some(u16::from_be_bytes([s[0], s[1]]))
}

fn u32_at(b: &[u8], at: usize) -> Option<u32> {
    let s = b.get(at..at.checked_add(4)?)?;
    Some(u32::from_be_bytes([s[0], s[1], s[2], s[3]]))
}

/// Go `scale`: `x / unitsPerEm` rounded to nearest, int32 wrapping arithmetic.
fn scale(x: i32, units_per_em: i32) -> i32 {
    let half = units_per_em / 2;
    let x = if x >= 0 {
        x.wrapping_add(half)
    } else {
        x.wrapping_sub(half)
    };
    x.wrapping_div(units_per_em)
}

/// Go `platformEncodingWidth`.
fn platform_encoding_width(pid: u16, psid: u16) -> u32 {
    match (pid, psid) {
        (0, 3) => 2,
        (0, 4) => 4,
        (1, 0) => 1,
        (3, 0) | (3, 1) => 2,
        (3, 10) => 4,
        _ => 0,
    }
}

/// Go `supportedCmapFormat`.
fn supported_cmap_format(format: u16, pid: u16, psid: u16) -> bool {
    match format {
        0 => pid == 1 && psid == 0,
        4 | 6 | 12 => true,
        _ => false,
    }
}

impl Font {
    /// Go `sfnt.Parse` for the supported subset.
    pub fn parse(src: &[u8]) -> Result<Font, FontError> {
        let head_dir = src.get(..12).ok_or(FontError::Invalid("bounds"))?;
        match u32::from_be_bytes([head_dir[0], head_dir[1], head_dir[2], head_dir[3]]) {
            0x0001_0000 | 0x7472_7565 => {}
            0x4f54_544f => {
                return Err(FontError::Unsupported("PostScript outlines"));
            }
            0x7474_6366 => return Err(FontError::Invalid("single font")),
            _ => return Err(FontError::Invalid("font")),
        }
        let num_tables = usize::from(u16::from_be_bytes([head_dir[4], head_dir[5]]));
        if num_tables > MAX_NUM_TABLES {
            return Err(FontError::Unsupported("number of tables"));
        }
        let dir = src
            .get(12..12 + 16 * num_tables)
            .ok_or(FontError::Invalid("bounds"))?;

        let mut tables: [Table; 6] = [Table::default(); 6];
        const HEAD: usize = 0;
        const HHEA: usize = 1;
        const HMTX: usize = 2;
        const MAXP: usize = 3;
        const CMAP: usize = 4;
        const OS2: usize = 5;
        let mut final_table_offset: i32 = 0;
        let mut prev_tag = 0u32;
        for (i, rec) in (0..num_tables).map(|i| (i, &dir[16 * i..16 * i + 16])) {
            let tag = u32::from_be_bytes([rec[0], rec[1], rec[2], rec[3]]);
            if i > 0 && tag <= prev_tag {
                return Err(FontError::Invalid("table tag order"));
            }
            prev_tag = tag;
            let o = u32::from_be_bytes([rec[8], rec[9], rec[10], rec[11]]);
            let n = u32::from_be_bytes([rec[12], rec[13], rec[14], rec[15]]);
            if o > MAX_TABLE_OFFSET || n > MAX_TABLE_LENGTH {
                return Err(FontError::Unsupported("table offset/length"));
            }
            if o & 3 != 0 {
                return Err(FontError::Invalid("table offset"));
            }
            let end = (o + n) as i32;
            if final_table_offset < end {
                final_table_offset = end;
            }
            let slot = match tag {
                0x6865_6164 => Some(HEAD),
                0x6868_6561 => Some(HHEA),
                0x686d_7478 => Some(HMTX),
                0x6d61_7870 => Some(MAXP),
                0x636d_6170 => Some(CMAP),
                0x4f53_2f32 => Some(OS2),
                _ => None,
            };
            if let Some(slot) = slot {
                tables[slot] = Table {
                    offset: o,
                    length: n,
                };
            }
        }
        if usize::try_from(final_table_offset).map_or(true, |end| end > src.len()) {
            return Err(FontError::Invalid("source data"));
        }

        // head
        let head = tables[HEAD];
        if head.length != 54 {
            return Err(FontError::Invalid("head table"));
        }
        let units_per_em =
            i32::from(u16_at(src, head.offset as usize + 18).ok_or(FontError::Invalid("bounds"))?);
        if units_per_em == 0 {
            return Err(FontError::Invalid("head table"));
        }

        // maxp (TrueType outlines: 32 bytes)
        let maxp = tables[MAXP];
        if maxp.length != 32 {
            return Err(FontError::Invalid("maxp table"));
        }
        let num_glyphs =
            i32::from(u16_at(src, maxp.offset as usize + 4).ok_or(FontError::Invalid("bounds"))?);

        // cmap
        let cmap = parse_cmap(src, tables[CMAP])?;

        // hhea
        let hhea = tables[HHEA];
        if hhea.length != 36 {
            return Err(FontError::Invalid("hhea table"));
        }
        let hh =
            |at: usize| u16_at(src, hhea.offset as usize + at).ok_or(FontError::Invalid("bounds"));
        let num_h_metrics = i32::from(hh(34)?);
        if num_h_metrics > num_glyphs || num_h_metrics == 0 {
            return Err(FontError::Invalid("hhea table"));
        }
        let ascent = i32::from(hh(4)? as i16);
        let descent = i32::from(hh(6)? as i16);
        let line_gap = i32::from(hh(8)? as i16);

        // hmtx
        let hmtx = tables[HMTX];
        let want_short = 4 * num_h_metrics;
        let want_full = want_short + 2 * (num_glyphs - num_h_metrics);
        if i64::from(hmtx.length) != i64::from(want_short)
            && i64::from(hmtx.length) != i64::from(want_full)
        {
            return Err(FontError::Invalid("hmtx table"));
        }

        // OS/2
        let os2 = tables[OS2];
        if os2.length == 0 {
            return Err(FontError::Unsupported("missing OS/2 table"));
        }
        if os2.length < 2 {
            return Err(FontError::Invalid("OS/2 table"));
        }
        let version = u16_at(src, os2.offset as usize).ok_or(FontError::Invalid("bounds"))?;
        if version < 2 {
            if os2.length < 68 {
                return Err(FontError::Invalid("OS/2 table"));
            }
            return Err(FontError::Unsupported("OS/2 version below 2"));
        }
        if os2.length < 96 {
            return Err(FontError::Invalid("OS/2 table"));
        }
        let x_height = i32::from(
            u16_at(src, os2.offset as usize + 86).ok_or(FontError::Invalid("bounds"))? as i16,
        );
        let cap_height = i32::from(
            u16_at(src, os2.offset as usize + 88).ok_or(FontError::Invalid("bounds"))? as i16,
        );

        Ok(Font {
            data: src.to_vec(),
            units_per_em,
            ascent,
            descent,
            line_gap,
            x_height,
            cap_height,
            num_h_metrics,
            num_glyphs,
            hmtx,
            cmap,
        })
    }

    /// Go `Font.NumGlyphs`.
    pub fn num_glyphs(&self) -> i32 {
        self.num_glyphs
    }

    /// Go `Font.UnitsPerEm`.
    pub fn units_per_em(&self) -> i32 {
        self.units_per_em
    }

    /// Go `Font.Metrics` with `font.HintingNone`; `ppem` is 26.6 fixed point.
    pub fn metrics(&self, ppem: i32) -> FixedMetrics {
        let upem = self.units_per_em;
        let m = |v: i32| scale(v.wrapping_mul(ppem), upem);
        FixedMetrics {
            height: m(self
                .ascent
                .wrapping_sub(self.descent)
                .wrapping_add(self.line_gap)),
            ascent: m(self.ascent),
            descent: m(self.descent).wrapping_neg(),
            x_height: m(self.x_height),
            cap_height: m(self.cap_height),
        }
    }

    /// Go `Font.GlyphIndex`; an error is reported as `Err(GlyphError)` (Go callers map it to
    /// glyph 0).
    pub fn glyph_index(&self, r: char) -> Result<u16, GlyphError> {
        let r = r as u32;
        match &self.cmap {
            Cmap::Fmt4 {
                entries,
                indexes_base,
                indexes_length,
            } => {
                if r > 0xffff {
                    return Ok(0);
                }
                let c = r as u16;
                let (mut i, mut j) = (0usize, entries.len());
                while i < j {
                    let h = i + (j - i) / 2;
                    let e = &entries[h];
                    if c < e.start {
                        j = h;
                    } else if e.end < c {
                        i = h + 1;
                    } else if e.offset == 0 {
                        return Ok(c.wrapping_add(e.delta));
                    } else {
                        // Go: uint32(entry.offset) + 2*uint32(h-len(entries)+int(c-entry.start))
                        let rel = h as i64 - entries.len() as i64 + i64::from(c - e.start);
                        let offset = (u32::from(e.offset) as i64 + 2 * rel) as u32;
                        if offset > *indexes_length || offset.wrapping_add(2) > *indexes_length {
                            return Err(GlyphError);
                        }
                        return u16_at(&self.data, indexes_base + offset as usize)
                            .ok_or(GlyphError);
                    }
                }
                Ok(0)
            }
            Cmap::Fmt6 {
                first_code,
                entries,
            } => {
                let c16 = r as u16;
                if c16 < *first_code {
                    return Ok(0);
                }
                let idx = usize::from(c16 - first_code);
                Ok(entries.get(idx).copied().unwrap_or(0))
            }
            Cmap::Fmt12 { entries } => {
                let (mut i, mut j) = (0usize, entries.len());
                while i < j {
                    let h = i + (j - i) / 2;
                    let (start, end, delta) = entries[h];
                    if r < start {
                        j = h;
                    } else if end < r {
                        i = h + 1;
                    } else {
                        return Ok(r.wrapping_sub(start).wrapping_add(delta) as u16);
                    }
                }
                Ok(0)
            }
        }
    }

    /// Go `Font.GlyphAdvance` with `font.HintingNone`; `ppem` is 26.6 fixed point.
    pub fn glyph_advance(&self, glyph: u16, ppem: i32) -> Result<i32, GlyphError> {
        if i32::from(glyph) >= self.num_glyphs {
            return Err(GlyphError);
        }
        let mut x = i32::from(glyph);
        let last = self.num_h_metrics - 1;
        if x > last {
            x = last;
        }
        let at = self.hmtx.offset as usize + 4 * x as usize;
        let adv = i32::from(u16_at(&self.data, at).ok_or(GlyphError)?);
        Ok(scale(adv.wrapping_mul(ppem), self.units_per_em))
    }
}

fn parse_cmap(src: &[u8], cmap: Table) -> Result<Cmap, FontError> {
    const HEADER: u32 = 4;
    const ENTRY: u32 = 8;
    let bounds = FontError::Invalid("bounds");
    if cmap.length < HEADER {
        return Err(FontError::Invalid("cmap table"));
    }
    let base = cmap.offset as usize;
    let num_subtables = u32::from(u16_at(src, base + 2).ok_or(bounds.clone())?);
    if cmap.length < HEADER + ENTRY * num_subtables {
        return Err(FontError::Invalid("cmap table"));
    }
    let (mut best_width, mut best_offset, mut best_format) = (0u32, 0u32, 0u16);
    for i in 0..num_subtables as usize {
        let rec = base + 4 + 8 * i;
        let pid = u16_at(src, rec).ok_or(bounds.clone())?;
        let psid = u16_at(src, rec + 2).ok_or(bounds.clone())?;
        let width = platform_encoding_width(pid, psid);
        if width <= best_width {
            continue;
        }
        let offset = u32_at(src, rec + 4).ok_or(bounds.clone())?;
        if offset > cmap.length - 4 {
            return Err(FontError::Invalid("cmap table"));
        }
        let format = u16_at(src, base + offset as usize).ok_or(bounds.clone())?;
        if !supported_cmap_format(format, pid, psid) {
            continue;
        }
        u16_at(src, base + offset as usize + 2).ok_or(bounds.clone())?; // length: bounds check only
        best_width = width;
        best_offset = offset;
        best_format = format;
    }
    if best_width == 0 {
        return Err(FontError::Unsupported("cmap encodings"));
    }
    let mut offset = best_offset;
    match best_format {
        4 => {
            const SUB_HEADER: u32 = 14;
            if offset + SUB_HEADER > cmap.length {
                return Err(FontError::Invalid("cmap table"));
            }
            let seg_count_x2 = u16_at(src, base + offset as usize + 6).ok_or(bounds.clone())?;
            if seg_count_x2 & 1 != 0 {
                return Err(FontError::Invalid("cmap table"));
            }
            let seg_count = u32::from(seg_count_x2 / 2);
            if seg_count > MAX_CMAP_SEGMENTS {
                return Err(FontError::Unsupported("number of cmap segments"));
            }
            offset += SUB_HEADER;
            let e_length = 8 * seg_count + 2;
            if offset + e_length > cmap.length {
                return Err(FontError::Invalid("cmap table"));
            }
            let b = src
                .get(base + offset as usize..base + (offset + e_length) as usize)
                .ok_or(bounds.clone())?;
            let n = seg_count as usize;
            let entries = (0..n)
                .map(|i| Cmap4Entry {
                    end: u16_at(b, 2 * i).unwrap_or(0),
                    start: u16_at(b, 2 * n + 2 + 2 * i).unwrap_or(0),
                    delta: u16_at(b, 4 * n + 2 + 2 * i).unwrap_or(0),
                    offset: u16_at(b, 6 * n + 2 + 2 * i).unwrap_or(0),
                })
                .collect();
            offset += e_length;
            Ok(Cmap::Fmt4 {
                entries,
                indexes_base: base + offset as usize,
                indexes_length: cmap.length - offset,
            })
        }
        6 => {
            const SUB_HEADER: u32 = 10;
            if offset + SUB_HEADER > cmap.length {
                return Err(FontError::Invalid("cmap table"));
            }
            let first_code = u16_at(src, base + offset as usize + 6).ok_or(bounds.clone())?;
            let entry_count =
                u32::from(u16_at(src, base + offset as usize + 8).ok_or(bounds.clone())?);
            offset += SUB_HEADER;
            let e_length = 2 * entry_count;
            if offset + e_length > cmap.length {
                return Err(FontError::Invalid("cmap table"));
            }
            let b = src
                .get(base + offset as usize..base + (offset + e_length) as usize)
                .ok_or(bounds.clone())?;
            let entries = (0..entry_count as usize)
                .map(|i| u16_at(b, 2 * i).unwrap_or(0))
                .collect();
            Ok(Cmap::Fmt6 {
                first_code,
                entries,
            })
        }
        12 => {
            const SUB_HEADER: u32 = 16;
            if offset + SUB_HEADER > cmap.length {
                return Err(FontError::Invalid("cmap table"));
            }
            let length = u32_at(src, base + offset as usize + 4).ok_or(bounds.clone())?;
            if cmap.length < offset || length > cmap.length - offset {
                return Err(FontError::Invalid("cmap table"));
            }
            let num_groups = u32_at(src, base + offset as usize + 12).ok_or(bounds.clone())?;
            if num_groups > MAX_CMAP_SEGMENTS {
                return Err(FontError::Unsupported("number of cmap segments"));
            }
            let e_length = 12 * num_groups;
            if SUB_HEADER + e_length != length {
                return Err(FontError::Invalid("cmap table"));
            }
            offset += SUB_HEADER;
            let b = src
                .get(base + offset as usize..base + (offset + e_length) as usize)
                .ok_or(bounds.clone())?;
            let entries = (0..num_groups as usize)
                .map(|i| {
                    (
                        u32_at(b, 12 * i).unwrap_or(0),
                        u32_at(b, 12 * i + 4).unwrap_or(0),
                        u32_at(b, 12 * i + 8).unwrap_or(0),
                    )
                })
                .collect();
            Ok(Cmap::Fmt12 { entries })
        }
        _ => Err(FontError::Unsupported("cmap format")),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const REGULAR: &[u8] = include_bytes!("../../../../internal/draw/fonts/Inter-Regular.ttf");

    #[test]
    fn scale_rounds_half_away_from_zero_like_go() {
        assert_eq!(scale(1024, 2048), 1);
        assert_eq!(scale(1023, 2048), 0);
        assert_eq!(scale(-1024, 2048), -1);
        assert_eq!(scale(-1023, 2048), 0);
        assert_eq!(scale(i32::MAX, 2048), (i32::MAX.wrapping_add(1024)) / 2048);
    }

    #[test]
    fn embedded_font_loads() {
        let font = Font::parse(REGULAR).expect("embedded Inter parses");
        assert_eq!(font.units_per_em(), 2048);
        assert_eq!(font.num_glyphs(), 2937);
        // 'A' is mapped; the last code point is not (maps to .notdef).
        assert_ne!(font.glyph_index('A'), Ok(0));
        assert_eq!(font.glyph_index('\u{10FFFF}'), Ok(0));
        assert!(font.glyph_advance(u16::MAX, 64).is_err());
    }

    #[test]
    fn every_truncation_of_the_header_is_rejected_without_panicking() {
        for len in (0..400).chain([REGULAR.len() / 2, REGULAR.len() * 9 / 10]) {
            assert!(Font::parse(&REGULAR[..len]).is_err(), "len {len}");
        }
    }

    #[test]
    fn corrupted_structure_is_rejected() {
        let mut bad_magic = REGULAR.to_vec();
        bad_magic[0] = 0xff;
        assert_eq!(
            Font::parse(&bad_magic).err(),
            Some(FontError::Invalid("font"))
        );

        let mut ttc = REGULAR.to_vec();
        ttc[..4].copy_from_slice(b"ttcf");
        assert_eq!(
            Font::parse(&ttc).err(),
            Some(FontError::Invalid("single font"))
        );

        let mut otto = REGULAR.to_vec();
        otto[..4].copy_from_slice(b"OTTO");
        assert!(matches!(Font::parse(&otto), Err(FontError::Unsupported(_))));

        let mut many = REGULAR.to_vec();
        many[4] = 0x02; // 512 tables
        assert!(matches!(Font::parse(&many), Err(FontError::Unsupported(_))));

        // Table records start at byte 12; swap the first two tags to break ordering.
        let mut order = REGULAR.to_vec();
        let (a, b) = (12, 28);
        for i in 0..4 {
            order.swap(a + i, b + i);
        }
        assert_eq!(
            Font::parse(&order).err(),
            Some(FontError::Invalid("table tag order"))
        );

        let mut misaligned = REGULAR.to_vec();
        misaligned[12 + 8 + 3] |= 1; // first table offset not 4-byte aligned
        assert_eq!(
            Font::parse(&misaligned).err(),
            Some(FontError::Invalid("table offset"))
        );
    }

    #[test]
    fn missing_or_zeroed_core_tables_are_rejected() {
        // Zero the head table length (offset 12 + 16*idx + 12) of every record in turn;
        // each of head/hhea/hmtx/maxp/cmap/OS/2 must make the parse fail.
        let num = usize::from(u16::from_be_bytes([REGULAR[4], REGULAR[5]]));
        let mut rejected = 0;
        for i in 0..num {
            let mut f = REGULAR.to_vec();
            let at = 12 + 16 * i + 12;
            f[at..at + 4].copy_from_slice(&0u32.to_be_bytes());
            let tag = &REGULAR[12 + 16 * i..12 + 16 * i + 4];
            let required = [&b"head"[..], b"hhea", b"hmtx", b"maxp", b"cmap", b"OS/2"];
            if required.contains(&tag) {
                assert!(Font::parse(&f).is_err(), "tag {:?}", tag);
                rejected += 1;
            }
        }
        assert_eq!(rejected, 6);
    }

    #[test]
    fn display_names_the_failure() {
        assert_eq!(
            FontError::Invalid("head table").to_string(),
            "sfnt: invalid head table"
        );
        assert_eq!(
            FontError::Unsupported("cmap format").to_string(),
            "sfnt: unsupported cmap format"
        );
    }
}
