// Copyright 2017 The Go Authors. All rights reserved.
// Adapted from golang.org/x/image/font/sfnt v0.47.0 truetype.go.
// BSD-3-Clause terms are reproduced in ../../LICENSE-x-image.
//! TrueType outline decoding following x/image/font/sfnt's contour traversal.
use super::{Font, GlyphError, scale, u16_at, u32_at};
type P = (i32, i32);
/// An unhinted glyph segment in 26.6 pixel coordinates (Y increases down).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct GlyphSegment {
    pub op: &'static str,
    pub args: Vec<(i32, i32)>,
}
fn mid(a: P, b: P) -> P {
    ((a.0 + b.0) / 2, (a.1 + b.1) / 2)
}
fn word(data: &[u8], i: &mut usize) -> Result<u16, GlyphError> {
    let v = u16_at(data, *i).ok_or(GlyphError)?;
    *i += 2;
    Ok(v)
}
fn byte(data: &[u8], i: &mut usize) -> Result<u8, GlyphError> {
    let v = *data.get(*i).ok_or(GlyphError)?;
    *i += 1;
    Ok(v)
}
fn push(out: &mut Vec<GlyphSegment>, op: &'static str, args: &[P]) {
    out.push(GlyphSegment {
        op,
        args: args.to_vec(),
    });
}
impl Font {
    fn table_bytes(&self, tag: &[u8; 4]) -> Result<&[u8], GlyphError> {
        let n = u16_at(&self.data, 4).ok_or(GlyphError)? as usize;
        for i in 0..n {
            let o = 12 + i * 16;
            if self.data.get(o..o + 4) == Some(tag.as_slice()) {
                let a = u32_at(&self.data, o + 8).ok_or(GlyphError)? as usize;
                let n = u32_at(&self.data, o + 12).ok_or(GlyphError)? as usize;
                return self.data.get(a..a + n).ok_or(GlyphError);
            }
        }
        Err(GlyphError)
    }
    /// Go `LoadGlyph` for TrueType outlines, without hinting.
    pub fn load_glyph(&self, index: u16, ppem: i32) -> Result<Vec<GlyphSegment>, GlyphError> {
        let mut segments = self.raw_glyph(index, 0)?;
        for seg in &mut segments {
            for p in &mut seg.args {
                p.0 = scale(p.0.wrapping_mul(ppem), self.units_per_em);
                p.1 = scale(p.1.wrapping_mul(ppem), self.units_per_em).wrapping_neg();
            }
        }
        Ok(segments)
    }
    fn raw_glyph(&self, index: u16, depth: usize) -> Result<Vec<GlyphSegment>, GlyphError> {
        if i32::from(index) >= self.num_glyphs || depth >= 8 {
            return Err(GlyphError);
        }
        let loca = self.table_bytes(b"loca")?;
        let head = self.table_bytes(b"head")?;
        let long = u16_at(head, 50).ok_or(GlyphError)? != 0;
        let location = |i: usize| -> Result<usize, GlyphError> {
            if long {
                Ok(u32_at(loca, i * 4).ok_or(GlyphError)? as usize)
            } else {
                Ok(u16_at(loca, i * 2).ok_or(GlyphError)? as usize * 2)
            }
        };
        let data = self
            .table_bytes(b"glyf")?
            .get(location(index as usize)?..location(index as usize + 1)?)
            .ok_or(GlyphError)?;
        let mut out = vec![];
        if data.is_empty() {
            return Ok(out);
        }
        let contours = u16_at(data, 0).ok_or(GlyphError)? as i16;
        let mut at = 10;
        if contours == -1 {
            loop {
                let flags = word(data, &mut at)?;
                let glyph = word(data, &mut at)?;
                let (dx, dy) = if flags & 1 != 0 {
                    (
                        word(data, &mut at)? as i16 as i32,
                        word(data, &mut at)? as i16 as i32,
                    )
                } else {
                    (
                        byte(data, &mut at)? as i8 as i32,
                        byte(data, &mut at)? as i8 as i32,
                    )
                };
                if flags & 2 == 0 {
                    return Err(GlyphError);
                }
                let transform = if flags & 8 != 0 {
                    let a = word(data, &mut at)? as i16 as i64;
                    Some((a, 0, 0, a))
                } else if flags & 64 != 0 {
                    Some((
                        word(data, &mut at)? as i16 as i64,
                        0,
                        0,
                        word(data, &mut at)? as i16 as i64,
                    ))
                } else if flags & 128 != 0 {
                    Some((
                        word(data, &mut at)? as i16 as i64,
                        word(data, &mut at)? as i16 as i64,
                        word(data, &mut at)? as i16 as i64,
                        word(data, &mut at)? as i16 as i64,
                    ))
                } else {
                    None
                };
                let mut component = self.raw_glyph(glyph, depth + 1)?;
                for s in &mut component {
                    for p in &mut s.args {
                        let (x, y) = *p;
                        *p = if let Some((xx, xy, yx, yy)) = transform {
                            (
                                dx + (((i64::from(x) * xx + 8192) >> 14) as i32)
                                    + (((i64::from(y) * yx + 8192) >> 14) as i32),
                                dy + (((i64::from(x) * xy + 8192) >> 14) as i32)
                                    + (((i64::from(y) * yy + 8192) >> 14) as i32),
                            )
                        } else {
                            (x + dx, y + dy)
                        };
                    }
                }
                out.extend(component);
                if flags & 32 == 0 {
                    break;
                }
            }
            return Ok(out);
        }
        if contours < 0 {
            return Err(GlyphError);
        }
        if contours == 0 {
            return Ok(out);
        }
        let mut ends = vec![];
        for _ in 0..contours {
            ends.push(word(data, &mut at)? as usize);
        }
        let count = ends.last().copied().ok_or(GlyphError)? + 1;
        let hints = word(data, &mut at)? as usize;
        at += hints;
        let mut flags = vec![];
        while flags.len() < count {
            let f = byte(data, &mut at)?;
            let n = if f & 8 != 0 {
                usize::from(byte(data, &mut at)?) + 1
            } else {
                1
            };
            if flags.len() + n > count {
                return Err(GlyphError);
            }
            flags.extend(std::iter::repeat_n(f, n));
        }
        let mut coords = vec![(0, 0); count];
        for axis in 0..2 {
            let mut value = 0_i16;
            for (p, f) in coords.iter_mut().zip(&flags) {
                let short = if axis == 0 { 2 } else { 4 };
                let same = if axis == 0 { 16 } else { 32 };
                let delta = if f & short != 0 {
                    let v = i16::from(byte(data, &mut at)?);
                    if f & same != 0 { v } else { -v }
                } else if f & same == 0 {
                    word(data, &mut at)? as i16
                } else {
                    0
                };
                value = value.wrapping_add(delta);
                if axis == 0 {
                    p.0 = i32::from(value)
                } else {
                    p.1 = i32::from(value)
                }
            }
        }
        let mut begin = 0;
        for end in ends {
            if end < begin || end >= count {
                return Err(GlyphError);
            }
            let mut first_on = None;
            let mut first_off = None;
            let mut last_off = None;
            for i in begin..=end {
                let p = coords[i];
                let on = flags[i] & 1 != 0;
                if first_on.is_none() {
                    if on {
                        first_on = Some(p);
                        push(&mut out, "M", &[p]);
                    } else if let Some(a) = first_off {
                        let m = mid(a, p);
                        first_on = Some(m);
                        last_off = Some(p);
                        push(&mut out, "M", &[m]);
                    } else {
                        first_off = Some(p);
                    }
                } else if let Some(last) = last_off {
                    if on {
                        push(&mut out, "Q", &[last, p]);
                        last_off = None;
                    } else {
                        push(&mut out, "Q", &[last, mid(last, p)]);
                        last_off = Some(p);
                    }
                } else if on {
                    push(&mut out, "L", &[p]);
                } else {
                    last_off = Some(p);
                }
            }
            let first = first_on.unwrap_or_default();
            match (first_off, last_off) {
                (None, None) => push(&mut out, "L", &[first]),
                (None, Some(a)) | (Some(a), None) => push(&mut out, "Q", &[a, first]),
                (Some(a), Some(b)) => {
                    push(&mut out, "Q", &[b, mid(b, a)]);
                    push(&mut out, "Q", &[a, first]);
                }
            }
            begin = end + 1;
        }
        Ok(out)
    }
}
