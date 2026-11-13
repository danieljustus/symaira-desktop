//! Byte-exact port of `emit.EmitGIF` (`internal/draw/emit/gif.go`): every
//! scene is rasterized with [`rasterize`], dithered onto the Plan 9 palette
//! with `image/draw`'s Floyd-Steinberg drawer and the frames are written by a
//! literal transliteration of `image/gif`'s `EncodeAll` and `compress/lzw`'s
//! LSB writer.
//!
//! `emit.EmitGIF` leaves `gif.GIF.Config` zero, so the encoder takes the
//! logical screen size from the first frame, writes no global colour table
//! and gives every frame a local 256-entry colour table.
//!
//! Derived from the Go standard library (BSD-3-Clause, see
//! `LICENSE-x-image`).

use super::png::{RasterOptions, Rgba, rasterize};
use super::scene::Scene;

/// `palette.Plan9` as R, G, B (every entry is opaque).
#[rustfmt::skip]
const PLAN9: [[u8; 3]; 256] = [
    [0x00, 0x00, 0x00], [0x00, 0x00, 0x44], [0x00, 0x00, 0x88], [0x00, 0x00, 0xcc], [0x00, 0x44, 0x00], [0x00, 0x44, 0x44],
    [0x00, 0x44, 0x88], [0x00, 0x44, 0xcc], [0x00, 0x88, 0x00], [0x00, 0x88, 0x44], [0x00, 0x88, 0x88], [0x00, 0x88, 0xcc],
    [0x00, 0xcc, 0x00], [0x00, 0xcc, 0x44], [0x00, 0xcc, 0x88], [0x00, 0xcc, 0xcc], [0x00, 0xdd, 0xdd], [0x11, 0x11, 0x11],
    [0x00, 0x00, 0x55], [0x00, 0x00, 0x99], [0x00, 0x00, 0xdd], [0x00, 0x55, 0x00], [0x00, 0x55, 0x55], [0x00, 0x4c, 0x99],
    [0x00, 0x49, 0xdd], [0x00, 0x99, 0x00], [0x00, 0x99, 0x4c], [0x00, 0x99, 0x99], [0x00, 0x93, 0xdd], [0x00, 0xdd, 0x00],
    [0x00, 0xdd, 0x49], [0x00, 0xdd, 0x93], [0x00, 0xee, 0x9e], [0x00, 0xee, 0xee], [0x22, 0x22, 0x22], [0x00, 0x00, 0x66],
    [0x00, 0x00, 0xaa], [0x00, 0x00, 0xee], [0x00, 0x66, 0x00], [0x00, 0x66, 0x66], [0x00, 0x55, 0xaa], [0x00, 0x4f, 0xee],
    [0x00, 0xaa, 0x00], [0x00, 0xaa, 0x55], [0x00, 0xaa, 0xaa], [0x00, 0x9e, 0xee], [0x00, 0xee, 0x00], [0x00, 0xee, 0x4f],
    [0x00, 0xff, 0x55], [0x00, 0xff, 0xaa], [0x00, 0xff, 0xff], [0x33, 0x33, 0x33], [0x00, 0x00, 0x77], [0x00, 0x00, 0xbb],
    [0x00, 0x00, 0xff], [0x00, 0x77, 0x00], [0x00, 0x77, 0x77], [0x00, 0x5d, 0xbb], [0x00, 0x55, 0xff], [0x00, 0xbb, 0x00],
    [0x00, 0xbb, 0x5d], [0x00, 0xbb, 0xbb], [0x00, 0xaa, 0xff], [0x00, 0xff, 0x00], [0x44, 0x00, 0x44], [0x44, 0x00, 0x88],
    [0x44, 0x00, 0xcc], [0x44, 0x44, 0x00], [0x44, 0x44, 0x44], [0x44, 0x44, 0x88], [0x44, 0x44, 0xcc], [0x44, 0x88, 0x00],
    [0x44, 0x88, 0x44], [0x44, 0x88, 0x88], [0x44, 0x88, 0xcc], [0x44, 0xcc, 0x00], [0x44, 0xcc, 0x44], [0x44, 0xcc, 0x88],
    [0x44, 0xcc, 0xcc], [0x44, 0x00, 0x00], [0x55, 0x00, 0x00], [0x55, 0x00, 0x55], [0x4c, 0x00, 0x99], [0x49, 0x00, 0xdd],
    [0x55, 0x55, 0x00], [0x55, 0x55, 0x55], [0x4c, 0x4c, 0x99], [0x49, 0x49, 0xdd], [0x4c, 0x99, 0x00], [0x4c, 0x99, 0x4c],
    [0x4c, 0x99, 0x99], [0x49, 0x93, 0xdd], [0x49, 0xdd, 0x00], [0x49, 0xdd, 0x49], [0x49, 0xdd, 0x93], [0x49, 0xdd, 0xdd],
    [0x4f, 0xee, 0xee], [0x66, 0x00, 0x00], [0x66, 0x00, 0x66], [0x55, 0x00, 0xaa], [0x4f, 0x00, 0xee], [0x66, 0x66, 0x00],
    [0x66, 0x66, 0x66], [0x55, 0x55, 0xaa], [0x4f, 0x4f, 0xee], [0x55, 0xaa, 0x00], [0x55, 0xaa, 0x55], [0x55, 0xaa, 0xaa],
    [0x4f, 0x9e, 0xee], [0x4f, 0xee, 0x00], [0x4f, 0xee, 0x4f], [0x4f, 0xee, 0x9e], [0x55, 0xff, 0xaa], [0x55, 0xff, 0xff],
    [0x77, 0x00, 0x00], [0x77, 0x00, 0x77], [0x5d, 0x00, 0xbb], [0x55, 0x00, 0xff], [0x77, 0x77, 0x00], [0x77, 0x77, 0x77],
    [0x5d, 0x5d, 0xbb], [0x55, 0x55, 0xff], [0x5d, 0xbb, 0x00], [0x5d, 0xbb, 0x5d], [0x5d, 0xbb, 0xbb], [0x55, 0xaa, 0xff],
    [0x55, 0xff, 0x00], [0x55, 0xff, 0x55], [0x88, 0x00, 0x88], [0x88, 0x00, 0xcc], [0x88, 0x44, 0x00], [0x88, 0x44, 0x44],
    [0x88, 0x44, 0x88], [0x88, 0x44, 0xcc], [0x88, 0x88, 0x00], [0x88, 0x88, 0x44], [0x88, 0x88, 0x88], [0x88, 0x88, 0xcc],
    [0x88, 0xcc, 0x00], [0x88, 0xcc, 0x44], [0x88, 0xcc, 0x88], [0x88, 0xcc, 0xcc], [0x88, 0x00, 0x00], [0x88, 0x00, 0x44],
    [0x99, 0x00, 0x4c], [0x99, 0x00, 0x99], [0x93, 0x00, 0xdd], [0x99, 0x4c, 0x00], [0x99, 0x4c, 0x4c], [0x99, 0x4c, 0x99],
    [0x93, 0x49, 0xdd], [0x99, 0x99, 0x00], [0x99, 0x99, 0x4c], [0x99, 0x99, 0x99], [0x93, 0x93, 0xdd], [0x93, 0xdd, 0x00],
    [0x93, 0xdd, 0x49], [0x93, 0xdd, 0x93], [0x93, 0xdd, 0xdd], [0x99, 0x00, 0x00], [0xaa, 0x00, 0x00], [0xaa, 0x00, 0x55],
    [0xaa, 0x00, 0xaa], [0x9e, 0x00, 0xee], [0xaa, 0x55, 0x00], [0xaa, 0x55, 0x55], [0xaa, 0x55, 0xaa], [0x9e, 0x4f, 0xee],
    [0xaa, 0xaa, 0x00], [0xaa, 0xaa, 0x55], [0xaa, 0xaa, 0xaa], [0x9e, 0x9e, 0xee], [0x9e, 0xee, 0x00], [0x9e, 0xee, 0x4f],
    [0x9e, 0xee, 0x9e], [0x9e, 0xee, 0xee], [0xaa, 0xff, 0xff], [0xbb, 0x00, 0x00], [0xbb, 0x00, 0x5d], [0xbb, 0x00, 0xbb],
    [0xaa, 0x00, 0xff], [0xbb, 0x5d, 0x00], [0xbb, 0x5d, 0x5d], [0xbb, 0x5d, 0xbb], [0xaa, 0x55, 0xff], [0xbb, 0xbb, 0x00],
    [0xbb, 0xbb, 0x5d], [0xbb, 0xbb, 0xbb], [0xaa, 0xaa, 0xff], [0xaa, 0xff, 0x00], [0xaa, 0xff, 0x55], [0xaa, 0xff, 0xaa],
    [0xcc, 0x00, 0xcc], [0xcc, 0x44, 0x00], [0xcc, 0x44, 0x44], [0xcc, 0x44, 0x88], [0xcc, 0x44, 0xcc], [0xcc, 0x88, 0x00],
    [0xcc, 0x88, 0x44], [0xcc, 0x88, 0x88], [0xcc, 0x88, 0xcc], [0xcc, 0xcc, 0x00], [0xcc, 0xcc, 0x44], [0xcc, 0xcc, 0x88],
    [0xcc, 0xcc, 0xcc], [0xcc, 0x00, 0x00], [0xcc, 0x00, 0x44], [0xcc, 0x00, 0x88], [0xdd, 0x00, 0x93], [0xdd, 0x00, 0xdd],
    [0xdd, 0x49, 0x00], [0xdd, 0x49, 0x49], [0xdd, 0x49, 0x93], [0xdd, 0x49, 0xdd], [0xdd, 0x93, 0x00], [0xdd, 0x93, 0x49],
    [0xdd, 0x93, 0x93], [0xdd, 0x93, 0xdd], [0xdd, 0xdd, 0x00], [0xdd, 0xdd, 0x49], [0xdd, 0xdd, 0x93], [0xdd, 0xdd, 0xdd],
    [0xdd, 0x00, 0x00], [0xdd, 0x00, 0x49], [0xee, 0x00, 0x4f], [0xee, 0x00, 0x9e], [0xee, 0x00, 0xee], [0xee, 0x4f, 0x00],
    [0xee, 0x4f, 0x4f], [0xee, 0x4f, 0x9e], [0xee, 0x4f, 0xee], [0xee, 0x9e, 0x00], [0xee, 0x9e, 0x4f], [0xee, 0x9e, 0x9e],
    [0xee, 0x9e, 0xee], [0xee, 0xee, 0x00], [0xee, 0xee, 0x4f], [0xee, 0xee, 0x9e], [0xee, 0xee, 0xee], [0xee, 0x00, 0x00],
    [0xff, 0x00, 0x00], [0xff, 0x00, 0x55], [0xff, 0x00, 0xaa], [0xff, 0x00, 0xff], [0xff, 0x55, 0x00], [0xff, 0x55, 0x55],
    [0xff, 0x55, 0xaa], [0xff, 0x55, 0xff], [0xff, 0xaa, 0x00], [0xff, 0xaa, 0x55], [0xff, 0xaa, 0xaa], [0xff, 0xaa, 0xff],
    [0xff, 0xff, 0x00], [0xff, 0xff, 0x55], [0xff, 0xff, 0xaa], [0xff, 0xff, 0xff],
];

/// `draw.sqDiff`: the squared difference shifted right by two.
fn sq_diff(x: u32, y: u32) -> u32 {
    let d = x.wrapping_sub(y);
    d.wrapping_mul(d) >> 2
}

fn clamp(i: i32) -> i32 {
    i.clamp(0, 0xffff)
}

/// `draw.FloydSteinberg.Draw(paletted, rgba.Bounds(), rgba, image.Point{})`
/// onto an `image.NewPaletted(bounds, palette.Plan9)`: one palette index per
/// pixel in row-major order.
pub fn floyd_steinberg_plan9(img: &Rgba) -> Vec<u8> {
    let (w, h) = (img.width, img.height);
    let palette: Vec<[i32; 4]> = PLAN9
        .iter()
        .map(|c| {
            [
                i32::from(c[0]) * 0x101,
                i32::from(c[1]) * 0x101,
                i32::from(c[2]) * 0x101,
                0xffff,
            ]
        })
        .collect();
    let mut pix = vec![0u8; w * h];
    let mut curr = vec![[0i32; 4]; w + 2];
    let mut next = vec![[0i32; 4]; w + 2];
    for y in 0..h {
        for x in 0..w {
            // color.RGBA.RGBA: the 8-bit channels widened to 16 bits.
            let o = (y * w + x) * 4;
            let widen = |v: u8| i32::from(v) * 0x101;
            let mut e = [
                widen(img.pix[o]),
                widen(img.pix[o + 1]),
                widen(img.pix[o + 2]),
                widen(img.pix[o + 3]),
            ];
            for k in 0..4 {
                e[k] = clamp(e[k] + curr[x + 1][k] / 16);
            }
            let mut best_index = 0usize;
            let mut best_sum = u32::MAX;
            for (index, p) in palette.iter().enumerate() {
                let sum = sq_diff(e[0] as u32, p[0] as u32)
                    .wrapping_add(sq_diff(e[1] as u32, p[1] as u32))
                    .wrapping_add(sq_diff(e[2] as u32, p[2] as u32))
                    .wrapping_add(sq_diff(e[3] as u32, p[3] as u32));
                if sum < best_sum {
                    best_index = index;
                    best_sum = sum;
                    if sum == 0 {
                        break;
                    }
                }
            }
            pix[y * w + x] = best_index as u8;
            for k in 0..4 {
                e[k] -= palette[best_index][k];
            }
            for k in 0..4 {
                next[x][k] += e[k] * 3;
                next[x + 1][k] += e[k] * 5;
                next[x + 2][k] += e[k];
                curr[x + 2][k] += e[k] * 7;
            }
        }
        std::mem::swap(&mut curr, &mut next);
        next.fill([0; 4]);
    }
    pix
}

// ---------------------------------------------------------------------------
// compress/lzw writer (LSB order)
// ---------------------------------------------------------------------------

const MAX_CODE: u32 = (1 << 12) - 1;
const INVALID_CODE: u32 = u32::MAX;
const TABLE_SIZE: usize = 4 * (1 << 12);
const TABLE_MASK: u32 = TABLE_SIZE as u32 - 1;

/// Go `lzw.Writer` with `lzw.LSB`, emitting through a GIF sub-block writer.
struct LzwWriter<'a> {
    out: &'a mut BlockWriter,
    lit_width: u32,
    n_bits: u32,
    width: u32,
    bits: u32,
    hi: u32,
    overflow: u32,
    saved_code: u32,
    table: Vec<u32>,
}

impl<'a> LzwWriter<'a> {
    fn new(out: &'a mut BlockWriter, lit_width: u32) -> LzwWriter<'a> {
        LzwWriter {
            out,
            lit_width,
            n_bits: 0,
            width: 1 + lit_width,
            bits: 0,
            hi: (1 << lit_width) + 1,
            overflow: 1 << (lit_width + 1),
            saved_code: INVALID_CODE,
            table: vec![0; TABLE_SIZE],
        }
    }

    fn write_lsb(&mut self, c: u32) {
        self.bits |= c << self.n_bits;
        self.n_bits += self.width;
        while self.n_bits >= 8 {
            self.out.write_byte(self.bits as u8);
            self.bits >>= 8;
            self.n_bits -= 8;
        }
    }

    /// Returns true when the writer ran out of codes and reset itself.
    fn inc_hi(&mut self) -> bool {
        self.hi += 1;
        if self.hi == self.overflow {
            self.width += 1;
            self.overflow <<= 1;
        }
        if self.hi == MAX_CODE {
            let clear = 1u32 << self.lit_width;
            self.write_lsb(clear);
            self.width = self.lit_width + 1;
            self.hi = clear + 1;
            self.overflow = clear << 1;
            self.table.fill(0);
            return true;
        }
        false
    }

    fn write(&mut self, p: &[u8]) {
        if p.is_empty() {
            return;
        }
        let mut p = p;
        let mut code = self.saved_code;
        if code == INVALID_CODE {
            let clear = 1u32 << self.lit_width;
            self.write_lsb(clear);
            code = u32::from(p[0]);
            p = &p[1..];
        }
        'outer: for &x in p {
            let literal = u32::from(x);
            let key = code << 8 | literal;
            let mut hash = (key >> 12 ^ key) & TABLE_MASK;
            let mut h = hash;
            let mut t = self.table[h as usize];
            while t != 0 {
                if key == t >> 12 {
                    code = t & MAX_CODE;
                    continue 'outer;
                }
                h = (h + 1) & TABLE_MASK;
                t = self.table[h as usize];
            }
            self.write_lsb(code);
            code = literal;
            if self.inc_hi() {
                continue;
            }
            loop {
                if self.table[hash as usize] == 0 {
                    self.table[hash as usize] = (key << 12) | self.hi;
                    break;
                }
                hash = (hash + 1) & TABLE_MASK;
            }
        }
        self.saved_code = code;
    }

    fn close(mut self) {
        if self.saved_code != INVALID_CODE {
            self.write_lsb(self.saved_code);
            self.inc_hi();
        } else {
            let clear = 1u32 << self.lit_width;
            self.write_lsb(clear);
        }
        let eof = (1u32 << self.lit_width) + 1;
        self.write_lsb(eof);
        if self.n_bits > 0 {
            self.out.write_byte(self.bits as u8);
        }
    }
}

/// gif `blockWriter`: `(n, n bytes)` sub-blocks with `1 <= n <= 255`.
struct BlockWriter {
    out: Vec<u8>,
    block: Vec<u8>,
}

impl BlockWriter {
    fn write_byte(&mut self, c: u8) {
        self.block.push(c);
        if self.block.len() == 255 {
            self.out.push(255);
            self.out.append(&mut self.block);
        }
    }

    /// `blockWriter.close`: pending sub-block (if any) plus the terminator.
    fn close(&mut self) {
        if !self.block.is_empty() {
            self.out.push(self.block.len() as u8);
            self.out.append(&mut self.block);
        }
        self.out.push(0);
    }
}

/// An `image.Paletted` over `palette.Plan9` whose bounds start at (0, 0).
pub struct PalettedFrame {
    pub width: usize,
    pub height: usize,
    pub pix: Vec<u8>,
}

/// `image.NewPaletted` + `draw.FloydSteinberg.Draw` for a rasterized frame.
pub fn dither_frame(img: &Rgba) -> PalettedFrame {
    PalettedFrame {
        width: img.width,
        height: img.height,
        pix: floyd_steinberg_plan9(img),
    }
}

/// `gif.EncodeAll` for a GIF with zero `Config`, no disposal and the same
/// delay for every frame, where every frame carries the Plan 9 palette.
pub fn encode_all(frames: &[PalettedFrame], delay: i64) -> Result<Vec<u8>, String> {
    if frames.is_empty() {
        return Err("gif: must provide at least one image".to_owned());
    }
    let (cfg_w, cfg_h) = (frames[0].width, frames[0].height);
    let mut out = Vec::new();
    out.extend_from_slice(b"GIF89a");
    out.extend_from_slice(&(cfg_w as u16).to_le_bytes());
    out.extend_from_slice(&(cfg_h as u16).to_le_bytes());
    // No global colour table: flags, background index, pixel aspect ratio.
    out.extend_from_slice(&[0, 0, 0]);
    if frames.len() > 1 {
        // NETSCAPE2.0 loop extension with LoopCount 0 (infinite).
        out.extend_from_slice(&[0x21, 0xff, 0x0b]);
        out.extend_from_slice(b"NETSCAPE2.0");
        out.extend_from_slice(&[0x03, 0x01, 0x00, 0x00, 0x00]);
    }
    for pm in frames {
        if pm.width >= 1 << 16 || pm.height >= 1 << 16 {
            return Err("gif: image block is too large to encode".to_owned());
        }
        if pm.width > cfg_w || pm.height > cfg_h {
            return Err("gif: image block is out of bounds".to_owned());
        }
        if delay > 0 {
            // Graphic control extension (no transparency, no disposal).
            out.extend_from_slice(&[0x21, 0xf9, 0x04, 0x00]);
            out.extend_from_slice(&(delay as u16).to_le_bytes());
            out.extend_from_slice(&[0x00, 0x00]);
        }
        out.push(0x2c);
        out.extend_from_slice(&[0, 0, 0, 0]);
        out.extend_from_slice(&(pm.width as u16).to_le_bytes());
        out.extend_from_slice(&(pm.height as u16).to_le_bytes());
        // Local colour table: 256 entries, padded size 7, flag 0x80.
        out.push(0x80 | 7);
        for c in &PLAN9 {
            out.extend_from_slice(c);
        }
        out.push(8); // LZW minimum code size
        let mut bw = BlockWriter {
            out: std::mem::take(&mut out),
            block: Vec::new(),
        };
        let mut lzw = LzwWriter::new(&mut bw, 8);
        lzw.write(&pm.pix);
        lzw.close();
        bw.close();
        out = bw.out;
    }
    out.push(0x3b);
    Ok(out)
}

/// Go `emit.EmitGIF`. A `None` scene is a nil `*scene.Scene`. `delay_cs <= 0`
/// selects the 50 cs default.
pub fn emit_gif(
    scenes: &[Option<&Scene>],
    delay_cs: i64,
    opts: &RasterOptions,
) -> Result<Vec<u8>, String> {
    if scenes.is_empty() {
        return Err("no scenes provided for animated gif".to_owned());
    }
    let delay_cs = if delay_cs <= 0 { 50 } else { delay_cs };
    let mut frames = Vec::with_capacity(scenes.len());
    for (i, sc) in scenes.iter().enumerate() {
        let rgba = rasterize(*sc, opts).map_err(|e| format!("rasterize frame {i}: {e}"))?;
        frames.push(dither_frame(&rgba));
    }
    encode_all(&frames, delay_cs).map_err(|e| format!("encode animated gif: {e}"))
}

/// [`emit_gif`] over owned scenes (the shape `AnimateFrames` returns).
pub fn emit_gif_scenes(
    scenes: &[Scene],
    delay_cs: i64,
    opts: &RasterOptions,
) -> Result<Vec<u8>, String> {
    let refs: Vec<Option<&Scene>> = scenes.iter().map(Some).collect();
    emit_gif(&refs, delay_cs, opts)
}
