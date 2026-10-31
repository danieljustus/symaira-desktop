//! pdfcpu image import for the archive step (Go `api.ImportImagesFile` with the
//! default configuration, as `archive.wrapImageWithText` calls it).
//!
//! The decode side reproduces which Go `image` type each file decodes to, since
//! pdfcpu picks the PDF image encoding from that type: PNG (all colour types and
//! depths, tRNS, Adam7), JPEG (header only, passed through as DCT), BMP
//! (`golang.org/x/image/bmp`), TIFF (`golang.org/x/image/tiff`, multi page) and
//! WebP (`golang.org/x/image/webp`). The output side reproduces
//! `model.CreateImageResources` and `importImagePDFBytes` for `pos:full`: one
//! page per image, MediaBox = image size, one `/Im0` XObject, soft mask only
//! when an alpha value differs from 255, `/Interpolate true` unless both sides
//! are 1000 or more.
//!
//! Compared against Go through the canonical PDF description in
//! `scripts/rust-port/pdfops/pdfcanon.py` (see `tests/archive_pdf_contract.rs`).
//! Compressor output is not reproduced; decoded streams are.
//!
//! Declared limits: the lossy WebP YCbCr planes come from `image-webp`'s VP8
//! decoder (converted with Go's `color.YCbCrToRGB`). TIFF compressions are
//! uncompressed, PackBits, LZW, Deflate and CCITT Group 3/4 (each pinned by a
//! Go case). Group 4 goes through the `tiff` crate (its output polarity is
//! flipped to Go's); Group 3 has no crate decoder, so `g3_strip` ports the Go
//! reader's one-dimensional, EOL-strict flow on the `fax` code tables. A CMYK TIFF is rejected by Go itself
//! (`tiff-hand-cmyk`), and so here.

use std::io::Cursor;

use lopdf::{Dictionary, Document, Object, ObjectId, Stream, StringFormat, dictionary};

/// Pixel data in the representation of the Go `image` type it came from.
enum Pixels {
    Gray(Vec<u8>),
    Gray16(Vec<u16>),
    /// `image.RGBA`: alpha premultiplied.
    Rgba(Vec<[u8; 4]>),
    Rgba64(Vec<[u16; 4]>),
    Nrgba(Vec<[u8; 4]>),
    Nrgba64(Vec<[u16; 4]>),
    /// Indices plus the palette as straight (non-premultiplied) RGBA.
    Paletted(Vec<u8>, Vec<[u8; 4]>),
}

struct Decoded {
    width: usize,
    height: usize,
    pixels: Pixels,
}

/// One image prepared as PDF objects: the XObject dictionary and its data.
struct ImageResource {
    width: usize,
    height: usize,
    object: Stream,
    smask: Option<Stream>,
}

/// Import the file bytes as pdfcpu does and return the PDF (one page per image).
pub fn import_image_pdf(data: &[u8]) -> Result<Vec<u8>, String> {
    let resources = create_image_resources(data)?;
    build_pdf(resources)
}

fn create_image_resources(data: &[u8]) -> Result<Vec<ImageResource>, String> {
    let format = sniff(data)
        .ok_or_else(|| "decode image configuration: image: unknown format".to_owned())?;
    match format {
        Format::Jpeg => jpeg_resource(data).map(|r| vec![r]),
        Format::Png => decode_png(data)
            .and_then(|d| decoded_resource(d, true))
            .map(|r| vec![r]),
        Format::Bmp => decode_bmp(data)
            .and_then(|d| decoded_resource(d, false))
            .map(|r| vec![r]),
        Format::Webp => decode_webp(data)
            .and_then(|d| decoded_resource(d, false))
            .map(|r| vec![r]),
        Format::Tiff => decode_tiff(data)?
            .into_iter()
            .map(|d| decoded_resource(d, false))
            .collect(),
    }
}

#[derive(Clone, Copy, PartialEq)]
enum Format {
    Png,
    Jpeg,
    Bmp,
    Tiff,
    Webp,
}

/// Go `image.DecodeConfig` format sniffing (magic prefixes of the registered
/// decoders).
fn sniff(data: &[u8]) -> Option<Format> {
    if data.starts_with(b"\x89PNG\r\n\x1a\n") {
        Some(Format::Png)
    } else if data.starts_with(&[0xFF, 0xD8]) {
        Some(Format::Jpeg)
    } else if data.len() >= 6
        && data.starts_with(b"BM")
        && data[6..data.len().min(10)].iter().all(|b| *b == 0)
    {
        Some(Format::Bmp)
    } else if data.starts_with(b"II*\0") || data.starts_with(b"MM\0*") {
        Some(Format::Tiff)
    } else if data.len() >= 12 && &data[..4] == b"RIFF" && &data[8..12] == b"WEBP" {
        Some(Format::Webp)
    } else {
        None
    }
}

// ----------------------------------------------------------------------------
// PNG (Go image/png)

/// Go `image/png` rejections the `png` crate does not make: a tRNS chunk of the
/// wrong length for the colour type (or one on an alpha type), and surplus
/// decompressed pixel data ("too much pixel data").
fn png_go_checks(data: &[u8]) -> Result<(), String> {
    use std::io::Read;
    if data.len() < 33 || &data[..8] != b"\x89PNG\r\n\x1a\n" {
        return Ok(());
    }
    let (w, h) = (
        le32_be(&data[16..20]) as usize,
        le32_be(&data[20..24]) as usize,
    );
    let (depth, ctype, interlace) = (usize::from(data[24]), data[25], data[28]);
    let channels = match ctype {
        0 | 3 => 1,
        2 => 3,
        4 => 2,
        6 => 4,
        _ => return Ok(()),
    };
    let bpp = channels * depth;
    let mut at = 8;
    let mut idat = Vec::new();
    while at + 8 <= data.len() {
        let len = le32_be(&data[at..at + 4]) as usize;
        let kind = &data[at + 4..at + 8];
        let body = at + 8;
        let Some(end) = body.checked_add(len).filter(|e| *e <= data.len()) else {
            return Ok(());
        };
        match kind {
            b"tRNS" => {
                let ok = match ctype {
                    0 => len == 2,
                    2 => len == 6,
                    3 => len <= 256,
                    _ => return Err("png: tRNS, color type mismatch".to_owned()),
                };
                if !ok {
                    return Err("png: bad tRNS length".to_owned());
                }
            }
            b"IDAT" => idat.extend_from_slice(&data[body..end]),
            b"IEND" => break,
            _ => {}
        }
        at = end + 4;
    }
    let row = |pw: usize| 1 + (pw * bpp).div_ceil(8);
    let expected: usize = if interlace == 0 {
        h * row(w)
    } else {
        const PASSES: [(usize, usize, usize, usize); 7] = [
            (0, 0, 8, 8),
            (4, 0, 8, 8),
            (0, 4, 4, 8),
            (2, 0, 4, 4),
            (0, 2, 2, 4),
            (1, 0, 2, 2),
            (0, 1, 1, 2),
        ];
        PASSES
            .iter()
            .map(|&(xo, yo, xs, ys)| {
                let pw = if w > xo { (w - xo).div_ceil(xs) } else { 0 };
                let ph = if h > yo { (h - yo).div_ceil(ys) } else { 0 };
                if pw == 0 || ph == 0 { 0 } else { ph * row(pw) }
            })
            .sum()
    };
    let mut out = Vec::new();
    let read = flate2::read::ZlibDecoder::new(idat.as_slice())
        .take(expected as u64 + 1)
        .read_to_end(&mut out);
    if read.is_ok() && out.len() > expected {
        return Err("png: too much pixel data".to_owned());
    }
    Ok(())
}

fn le32_be(b: &[u8]) -> u32 {
    u32::from_be_bytes([b[0], b[1], b[2], b[3]])
}

fn decode_png(data: &[u8]) -> Result<Decoded, String> {
    png_go_checks(data)?;
    let mut decoder = png::Decoder::new(Cursor::new(data));
    decoder.set_transformations(png::Transformations::IDENTITY);
    let mut reader = decoder.read_info().map_err(|e| format!("png: {e}"))?;
    let (color, depth) = reader.output_color_type();
    let (width, height) = {
        let info = reader.info();
        (info.width as usize, info.height as usize)
    };
    let palette: Option<Vec<u8>> = reader.info().palette.as_ref().map(|p| p.to_vec());
    let trns: Option<Vec<u8>> = reader.info().trns.as_ref().map(|p| p.to_vec());
    let mut buf = vec![0u8; reader.output_buffer_size().ok_or("png: image too large")?];
    let frame = reader
        .next_frame(&mut buf)
        .map_err(|e| format!("png: {e}"))?;
    // Go reads to the end of the IDAT stream and rejects surplus pixel data.
    reader.finish().map_err(|e| format!("png: {e}"))?;
    let line = frame.line_size;
    let d = depth as u8 as usize;
    let sample = |row: &[u8], x: usize| -> u16 {
        match d {
            16 => u16::from(row[2 * x]) << 8 | u16::from(row[2 * x + 1]),
            8 => u16::from(row[x]),
            _ => {
                let per = 8 / d;
                let byte = row[x / per];
                let shift = 8 - d * (x % per + 1);
                u16::from((byte >> shift) & ((1u8 << d) - 1))
            }
        }
    };
    let tr16 = |i: usize| -> u16 {
        trns.as_ref()
            .and_then(|t| t.get(2 * i..2 * i + 2))
            .map(|b| u16::from(b[0]) << 8 | u16::from(b[1]))
            .unwrap_or(0)
    };
    // The png crate keeps one byte per sample of tRNS for depths up to 8.
    let tr_lo = |i: usize| -> u8 { trns.as_ref().and_then(|t| t.get(i)).copied().unwrap_or(0) };
    let mut rows = (0..height).map(|y| &buf[y * line..(y + 1) * line]);
    let pixels = match color {
        png::ColorType::Grayscale => {
            let use_trns = trns.is_some();
            if d == 16 {
                if use_trns {
                    let ty = tr16(0);
                    let mut px = Vec::with_capacity(width * height);
                    for row in &mut rows {
                        for x in 0..width {
                            let v = sample(row, x);
                            px.push([v, v, v, if v == ty { 0 } else { 0xFFFF }]);
                        }
                    }
                    Pixels::Nrgba64(px)
                } else {
                    let mut px = Vec::with_capacity(width * height);
                    for row in &mut rows {
                        for x in 0..width {
                            px.push(sample(row, x));
                        }
                    }
                    Pixels::Gray16(px)
                }
            } else {
                // Go scales sub-byte samples to 8 bits: v*255/max.
                let scale = match d {
                    1 => 0xFF,
                    2 => 0x55,
                    4 => 0x11,
                    _ => 1,
                };
                let ty = tr_lo(0).wrapping_mul(scale);
                let mut gray = Vec::with_capacity(width * height);
                let mut nrgba = Vec::with_capacity(width * height);
                for row in &mut rows {
                    for x in 0..width {
                        let v = (sample(row, x) as u8).wrapping_mul(scale);
                        if use_trns {
                            nrgba.push([v, v, v, if v == ty { 0 } else { 0xFF }]);
                        } else {
                            gray.push(v);
                        }
                    }
                }
                if use_trns {
                    Pixels::Nrgba(nrgba)
                } else {
                    Pixels::Gray(gray)
                }
            }
        }
        png::ColorType::GrayscaleAlpha => {
            if d == 16 {
                let mut px = Vec::with_capacity(width * height);
                for row in &mut rows {
                    for x in 0..width {
                        let y = u16::from(row[4 * x]) << 8 | u16::from(row[4 * x + 1]);
                        let a = u16::from(row[4 * x + 2]) << 8 | u16::from(row[4 * x + 3]);
                        px.push([y, y, y, a]);
                    }
                }
                Pixels::Nrgba64(px)
            } else {
                let mut px = Vec::with_capacity(width * height);
                for row in &mut rows {
                    for x in 0..width {
                        let y = row[2 * x];
                        px.push([y, y, y, row[2 * x + 1]]);
                    }
                }
                Pixels::Nrgba(px)
            }
        }
        png::ColorType::Rgb => {
            let use_trns = trns.is_some();
            if d == 16 {
                let key = [tr16(0), tr16(1), tr16(2)];
                let mut px = Vec::with_capacity(width * height);
                for row in &mut rows {
                    for x in 0..width {
                        let p = [
                            sample(row, 3 * x),
                            sample(row, 3 * x + 1),
                            sample(row, 3 * x + 2),
                        ];
                        let a = if use_trns && p == key { 0 } else { 0xFFFF };
                        px.push([p[0], p[1], p[2], a]);
                    }
                }
                if use_trns {
                    Pixels::Nrgba64(px)
                } else {
                    Pixels::Rgba64(px)
                }
            } else {
                let key = [tr_lo(0), tr_lo(1), tr_lo(2)];
                let mut px = Vec::with_capacity(width * height);
                for row in &mut rows {
                    for x in 0..width {
                        let p = [row[3 * x], row[3 * x + 1], row[3 * x + 2]];
                        let a = if use_trns && p == key { 0 } else { 0xFF };
                        px.push([p[0], p[1], p[2], a]);
                    }
                }
                if use_trns {
                    Pixels::Nrgba(px)
                } else {
                    Pixels::Rgba(px)
                }
            }
        }
        png::ColorType::Rgba => {
            if d == 16 {
                let mut px = Vec::with_capacity(width * height);
                for row in &mut rows {
                    for x in 0..width {
                        px.push([
                            sample(row, 4 * x),
                            sample(row, 4 * x + 1),
                            sample(row, 4 * x + 2),
                            sample(row, 4 * x + 3),
                        ]);
                    }
                }
                Pixels::Nrgba64(px)
            } else {
                let mut px = Vec::with_capacity(width * height);
                for row in &mut rows {
                    for x in 0..width {
                        px.push([row[4 * x], row[4 * x + 1], row[4 * x + 2], row[4 * x + 3]]);
                    }
                }
                Pixels::Nrgba(px)
            }
        }
        png::ColorType::Indexed => {
            let plte = palette.ok_or("png: missing PLTE chunk")?;
            let entries = plte.len() / 3;
            if plte.len() % 3 != 0 || entries == 0 || entries > (1usize << d) {
                return Err("png: invalid format: bad palette length".to_owned());
            }
            let mut pal: Vec<[u8; 4]> = (0..entries)
                .map(|i| [plte[3 * i], plte[3 * i + 1], plte[3 * i + 2], 0xFF])
                .collect();
            if let Some(t) = &trns {
                if t.len() > 256 || t.len() > (1usize << d) {
                    return Err("png: invalid format: bad tRNS length".to_owned());
                }
                // Go extends the palette with opaque black up to the tRNS length.
                while pal.len() < t.len() {
                    pal.push([0, 0, 0, 0xFF]);
                }
                for (i, a) in t.iter().enumerate() {
                    pal[i][3] = *a;
                }
            }
            let mut pix = Vec::with_capacity(width * height);
            for row in &mut rows {
                for x in 0..width {
                    let idx = sample(row, x) as usize;
                    if idx >= pal.len() {
                        // Go grows the palette to the largest index used, opaque black.
                        while pal.len() <= idx {
                            pal.push([0, 0, 0, 0xFF]);
                        }
                    }
                    pix.push(idx as u8);
                }
            }
            Pixels::Paletted(pix, pal)
        }
    };
    Ok(Decoded {
        width,
        height,
        pixels,
    })
}

// ----------------------------------------------------------------------------
// BMP (golang.org/x/image/bmp)

fn le16(b: &[u8]) -> u32 {
    u32::from(b[0]) | u32::from(b[1]) << 8
}
fn le32(b: &[u8]) -> u32 {
    u32::from(b[0]) | u32::from(b[1]) << 8 | u32::from(b[2]) << 16 | u32::from(b[3]) << 24
}

fn decode_bmp(data: &[u8]) -> Result<Decoded, String> {
    const UNSUPPORTED: &str = "bmp: unsupported BMP image";
    let need = |n: usize| -> Result<(), String> {
        if data.len() < n {
            Err("unexpected EOF".to_owned())
        } else {
            Ok(())
        }
    };
    need(18)?;
    if &data[..2] != b"BM" {
        return Err("bmp: invalid format".to_owned());
    }
    let offset = le32(&data[10..14]);
    let info_len = le32(&data[14..18]);
    if info_len != 40 && info_len != 108 && info_len != 124 {
        return Err(UNSUPPORTED.to_owned());
    }
    need(14 + info_len as usize)?;
    let width = le32(&data[18..22]) as i32 as i64;
    let mut height = le32(&data[22..26]) as i32 as i64;
    let mut top_down = false;
    if height < 0 {
        height = -height;
        top_down = true;
    }
    if width < 0 || height < 0 || (width == 0) != (height == 0) {
        return Err(UNSUPPORTED.to_owned());
    }
    if width
        .checked_mul(height)
        .and_then(|v| v.checked_mul(4))
        .is_none_or(|v| v > i64::from(i32::MAX))
    {
        return Err(UNSUPPORTED.to_owned());
    }
    let planes = le16(&data[26..28]);
    let bpp = le16(&data[28..30]);
    let mut compression = le32(&data[30..34]);
    if compression == 3
        && info_len > 40
        && data.len() >= 14 + 70
        && le32(&data[54..58]) == 0xff0000
        && le32(&data[58..62]) == 0xff00
        && le32(&data[62..66]) == 0xff
        && le32(&data[66..70]) == 0xff000000
    {
        compression = 0;
    }
    if planes != 1 || compression != 0 {
        return Err(UNSUPPORTED.to_owned());
    }
    let (w, h) = (width as usize, height as usize);
    let mut pos = 14 + info_len as usize;
    let rows: Vec<usize> = if top_down {
        (0..h).collect()
    } else {
        (0..h).rev().collect()
    };
    match bpp {
        1 | 2 | 4 | 8 => {
            let mut used = le32(&data[46..50]);
            if used == 0 {
                used = 1 << bpp;
            } else if used > (1 << bpp) {
                return Err(UNSUPPORTED.to_owned());
            }
            if offset != 14 + info_len + used * 4 {
                return Err(UNSUPPORTED.to_owned());
            }
            need(pos + used as usize * 4)?;
            let palette: Vec<[u8; 4]> = (0..used as usize)
                .map(|i| {
                    let e = &data[pos + 4 * i..];
                    [e[2], e[1], e[0], 0xFF]
                })
                .collect();
            pos += used as usize * 4;
            let mut pix = vec![0u8; w * h];
            if w > 0 && h > 0 {
                let bpp = bpp as usize;
                let per_byte = 8 / bpp;
                let row_bytes = (w.div_ceil(per_byte) + 3) & !3;
                for y in rows {
                    need(pos + row_bytes)?;
                    let b = &data[pos..pos + row_bytes];
                    pos += row_bytes;
                    let mask = ((1u16 << bpp) - 1) as u8;
                    let (mut byte_index, mut bit_index) = (0usize, 8usize);
                    for x in 0..w {
                        bit_index -= bpp;
                        let idx = (b[byte_index] >> bit_index) & mask;
                        if idx as usize >= palette.len() {
                            return Err("bmp: invalid palette index".to_owned());
                        }
                        pix[y * w + x] = idx;
                        if bit_index == 0 {
                            byte_index += 1;
                            bit_index = 8;
                        }
                    }
                }
            }
            Ok(Decoded {
                width: w,
                height: h,
                pixels: Pixels::Paletted(pix, palette),
            })
        }
        24 => {
            if offset != 14 + info_len {
                return Err(UNSUPPORTED.to_owned());
            }
            let mut px = vec![[0u8; 4]; w * h];
            if w > 0 && h > 0 {
                let row_bytes = (3 * w + 3) & !3;
                for y in rows {
                    need(pos + row_bytes)?;
                    let b = &data[pos..pos + row_bytes];
                    pos += row_bytes;
                    for x in 0..w {
                        px[y * w + x] = [b[3 * x + 2], b[3 * x + 1], b[3 * x], 0xFF];
                    }
                }
            }
            Ok(Decoded {
                width: w,
                height: h,
                pixels: Pixels::Rgba(px),
            })
        }
        32 => {
            if offset != 14 + info_len {
                return Err(UNSUPPORTED.to_owned());
            }
            let allow_alpha = info_len > 40;
            let mut px = vec![[0u8; 4]; w * h];
            if w > 0 && h > 0 {
                for y in rows {
                    need(pos + 4 * w)?;
                    let b = &data[pos..pos + 4 * w];
                    pos += 4 * w;
                    for x in 0..w {
                        let a = if allow_alpha { b[4 * x + 3] } else { 0xFF };
                        px[y * w + x] = [b[4 * x + 2], b[4 * x + 1], b[4 * x], a];
                    }
                }
            }
            Ok(Decoded {
                width: w,
                height: h,
                pixels: Pixels::Nrgba(px),
            })
        }
        _ => Err(UNSUPPORTED.to_owned()),
    }
}

// ----------------------------------------------------------------------------
// WebP (golang.org/x/image/webp)

fn decode_webp(data: &[u8]) -> Result<Decoded, String> {
    let mut decoder =
        image_webp::WebPDecoder::new(Cursor::new(data)).map_err(|e| format!("webp: {e}"))?;
    let (w, h) = decoder.dimensions();
    let has_alpha = decoder.has_alpha();
    let size = decoder
        .output_buffer_size()
        .ok_or_else(|| "webp: image too large".to_owned())?;
    let mut buf = vec![0u8; size];
    decoder
        .read_image(&mut buf)
        .map_err(|e| format!("webp: {e}"))?;
    let (width, height) = (w as usize, h as usize);
    let mut px = Vec::with_capacity(width * height);
    if has_alpha {
        for c in buf.as_chunks::<4>().0 {
            px.push([c[0], c[1], c[2], c[3]]);
        }
    } else {
        for c in buf.as_chunks::<3>().0 {
            px.push([c[0], c[1], c[2], 0xFF]);
        }
    }
    // Lossy files: Go keeps the decoder's YCbCr planes (`image.YCbCr`, or
    // `image.NYCbCrA` with an ALPH chunk) and pdfcpu converts them with
    // `color.YCbCrToRGB`; take the planes from the VP8 decoder and do the same,
    // keeping only the alpha plane of the generic reader. Lossless files are
    // NRGBA already.
    if let Some(vp8) = riff_chunk(data, b"VP8 ") {
        let frame = image_webp::vp8::Vp8Decoder::decode_frame(Cursor::new(vp8))
            .map_err(|e| format!("webp: {e}"))?;
        let ystride = usize::from(frame.width).div_ceil(16) * 16;
        let cstride = ystride / 2;
        if usize::from(frame.width) != width || usize::from(frame.height) != height {
            return Err("webp: inconsistent image sizes".to_owned());
        }
        for y in 0..height {
            for x in 0..width {
                let yy = frame.ybuf[y * ystride + x];
                let cb = frame.ubuf[(y / 2) * cstride + x / 2];
                let cr = frame.vbuf[(y / 2) * cstride + x / 2];
                let (r, g, b) = ycbcr_to_rgb(yy, cb, cr);
                let a = px[y * width + x][3];
                px[y * width + x] = [r, g, b, a];
            }
        }
    }
    Ok(Decoded {
        width,
        height,
        pixels: Pixels::Nrgba(px),
    })
}

/// Payload of the first RIFF chunk with the given FourCC (simple and extended WebP).
fn riff_chunk<'a>(data: &'a [u8], fourcc: &[u8; 4]) -> Option<&'a [u8]> {
    if data.len() < 12 || &data[..4] != b"RIFF" || &data[8..12] != b"WEBP" {
        return None;
    }
    let mut at = 12;
    while at + 8 <= data.len() {
        let size = le32(&data[at + 4..]) as usize;
        let body = at + 8;
        let end = body.checked_add(size)?.min(data.len());
        if &data[at..at + 4] == fourcc {
            return Some(&data[body..end]);
        }
        at = body.checked_add(size)?.checked_add(size & 1)?;
    }
    None
}

/// Go `color.YCbCrToRGB` (fixed point, clamped).
fn ycbcr_to_rgb(y: u8, cb: u8, cr: u8) -> (u8, u8, u8) {
    let yy1 = i32::from(y) * 0x10101;
    let cb1 = i32::from(cb) - 128;
    let cr1 = i32::from(cr) - 128;
    let clamp = |v: i32| -> u8 {
        if (v as u32) & 0xff00_0000 == 0 {
            (v >> 16) as u8
        } else {
            (!(v >> 31)) as u8
        }
    };
    (
        clamp(yy1 + 91881 * cr1),
        clamp(yy1 - 22554 * cb1 - 46802 * cr1),
        clamp(yy1 + 116130 * cb1),
    )
}

// ----------------------------------------------------------------------------
// TIFF (golang.org/x/image/tiff), every IFD of the chain

/// One IFD of the chain, as far as the palette workaround needs it.
struct IfdInfo {
    /// Byte position of the inline PhotometricInterpretation value.
    photometric_at: Option<usize>,
    photometric: u16,
    compression: u16,
    compression_at: Option<usize>,
    /// Group 3 (T.4) strip layout, which the `tiff` crate cannot decode.
    strips: G3Layout,
    colormap: Option<Vec<u16>>,
}

/// Walk the IFD chain (Go: `createImageResourcesForTIFF`) and collect what the
/// `tiff` crate cannot give: palette pages, which it refuses outright.
fn scan_ifds(data: &[u8]) -> Result<Vec<IfdInfo>, String> {
    let little = &data[..2] == b"II";
    let u16_at = |o: usize| -> Result<u16, String> {
        let b = data
            .get(o..o + 2)
            .ok_or("invalid TIFF file: unexpected EOF")?;
        Ok(if little {
            u16::from_le_bytes([b[0], b[1]])
        } else {
            u16::from_be_bytes([b[0], b[1]])
        })
    };
    let u32_at = |o: usize| -> Result<u32, String> {
        let b = data
            .get(o..o + 4)
            .ok_or("invalid TIFF file: unexpected EOF")?;
        Ok(if little {
            u32::from_le_bytes([b[0], b[1], b[2], b[3]])
        } else {
            u32::from_be_bytes([b[0], b[1], b[2], b[3]])
        })
    };
    let mut off = u32_at(4)? as usize;
    if off < 8 || off >= data.len() {
        return Err("invalid TIFF file: no valid IFD".to_owned());
    }
    let mut seen = std::collections::HashSet::new();
    let mut out = Vec::new();
    while off != 0 {
        if off >= data.len() {
            return Err(format!("invalid TIFF IFD offset {off}"));
        }
        if !seen.insert(off) {
            return Err(format!("cyclic TIFF IFD offset {off}"));
        }
        if out.len() >= 10_000 {
            return Err("TIFF page count exceeds limit 10000".to_owned());
        }
        let n = usize::from(u16_at(off)?);
        let mut info = IfdInfo {
            photometric_at: None,
            photometric: 0,
            compression: 1,
            compression_at: None,
            strips: G3Layout::default(),
            colormap: None,
        };
        for i in 0..n {
            let e = off + 2 + 12 * i;
            let tag = u16_at(e)?;
            if tag == 259 {
                info.compression = u16_at(e + 8)?;
                info.compression_at = Some(e + 8);
            } else if matches!(
                tag,
                256 | 257 | 258 | 266 | 273 | 277 | 278 | 279 | 322 | 323
            ) {
                let kind = u16_at(e + 2)?;
                let count = u32_at(e + 4)? as usize;
                let width = match kind {
                    3 => 2usize,
                    4 => 4,
                    _ => 0,
                };
                let mut values = Vec::new();
                if width != 0 && count <= 1 << 20 {
                    let total = count.saturating_mul(width);
                    let base = if total <= 4 {
                        e + 8
                    } else {
                        u32_at(e + 8)? as usize
                    };
                    for k in 0..count {
                        let at = base + k * width;
                        values.push(if width == 2 {
                            u32::from(u16_at(at)?)
                        } else {
                            u32_at(at)?
                        });
                    }
                }
                let first = values.first().copied();
                match tag {
                    256 => info.strips.width = first,
                    257 => info.strips.height = first,
                    258 => info.strips.bits = first,
                    266 => info.strips.fill_order = first,
                    273 => info.strips.offsets = values,
                    277 => info.strips.samples = first,
                    278 => info.strips.rows_per_strip = first,
                    279 => info.strips.counts = values,
                    _ => info.strips.tiled = true,
                }
            } else if tag == 262 {
                info.photometric_at = Some(e + 8);
                info.photometric = u16_at(e + 8)?;
            } else if tag == 320 {
                let count = u32_at(e + 4)? as usize;
                let at = u32_at(e + 8)? as usize;
                if !count.is_multiple_of(3) || count == 0 || count > 768 {
                    return Err("tiff: bad ColorMap length".to_owned());
                }
                let mut map = Vec::with_capacity(count);
                for k in 0..count {
                    map.push(u16_at(at + 2 * k)?);
                }
                info.colormap = Some(map);
            }
        }
        out.push(info);
        off = u32_at(off + 2 + 12 * n)? as usize;
    }
    Ok(out)
}

/// The tags a Group 3 strip decode needs.
#[derive(Default)]
struct G3Layout {
    width: Option<u32>,
    height: Option<u32>,
    bits: Option<u32>,
    samples: Option<u32>,
    fill_order: Option<u32>,
    rows_per_strip: Option<u32>,
    offsets: Vec<u32>,
    counts: Vec<u32>,
    tiled: bool,
}

/// MSB-first bit source over one strip, shaped for the `fax` code tables:
/// `peek` zero-pads at the end of the data so a short final code still
/// resolves, `consume` fails past the end (Go: `ccitt: incomplete code`).
struct StripBits<'a> {
    data: &'a [u8],
    lsb_first: bool,
    pos: usize,
}

impl StripBits<'_> {
    fn remaining(&self) -> usize {
        self.data.len() * 8 - self.pos
    }
    fn bit(&self, at: usize) -> u16 {
        let byte = self.data[at / 8];
        let shift = if self.lsb_first { at % 8 } else { 7 - at % 8 };
        u16::from((byte >> shift) & 1)
    }
}

impl fax::BitReader for StripBits<'_> {
    type Error = ();
    fn peek(&self, bits: u8) -> Option<u16> {
        if bits > 16 || self.remaining() == 0 {
            return None;
        }
        let mut v = 0u16;
        for k in 0..usize::from(bits) {
            v <<= 1;
            if self.pos + k < self.data.len() * 8 {
                v |= self.bit(self.pos + k);
            }
        }
        Some(v)
    }
    fn consume(&mut self, bits: u8) -> Result<(), ()> {
        if usize::from(bits) > self.remaining() {
            return Err(());
        }
        self.pos += usize::from(bits);
        Ok(())
    }
    fn bits_to_byte_boundary(&self) -> u8 {
        (self.pos % 8) as u8
    }
}

/// `ccitt.decodeEOL`: eleven zero bits then a one, nothing in between.
fn g3_eol(bits: &mut StripBits<'_>) -> bool {
    if bits.remaining() < 12 {
        return false;
    }
    for k in 0..12 {
        if bits.bit(bits.pos + k) != u16::from(k == 11) {
            return false;
        }
    }
    bits.pos += 12;
    true
}

/// One strip of `golang.org/x/image/ccitt` Group 3 (one-dimensional, EOL before
/// every row, optional EOL after the last row), as 1 byte per pixel, 0xFF
/// white. Errors mirror the Go reader's failures, not its wording.
fn g3_strip(data: &[u8], lsb_first: bool, width: usize, rows: usize) -> Result<Vec<u8>, String> {
    use fax::maps::{black, white};
    let mut bits = StripBits {
        data,
        lsb_first,
        pos: 0,
    };
    if !g3_eol(&mut bits) {
        return Err("ccitt: missing End-of-Line".to_owned());
    }
    let mut out = Vec::with_capacity(width * rows);
    let mut truncated = false;
    for row in 0..rows {
        let mut x = 0usize;
        let mut white_run = true;
        while x < width {
            let mut total = 0usize;
            loop {
                let code = if white_run {
                    white::decode(&mut bits)
                } else {
                    black::decode(&mut bits)
                };
                let Some(n) = code else {
                    return Err("ccitt: invalid code".to_owned());
                };
                total += usize::from(n);
                if total > 1 << 20 {
                    return Err("ccitt: run length too long".to_owned());
                }
                if n <= 0x3F {
                    break;
                }
            }
            if total > width - x {
                return Err("ccitt: run length overflows width".to_owned());
            }
            out.resize(out.len() + total, if white_run { 0xFF } else { 0x00 });
            x += total;
            white_run = !white_run;
        }
        if !g3_eol(&mut bits) {
            if row + 1 == rows {
                truncated = true;
            } else {
                return Err("ccitt: missing End-of-Line".to_owned());
            }
        }
    }
    if !truncated {
        // Return To Control: the five EOLs that follow the last row's EOL.
        for _ in 0..5 {
            if !g3_eol(&mut bits) {
                return Err("ccitt: missing End-of-Line".to_owned());
            }
        }
    }
    Ok(out)
}

/// A Group 3 page: the `tiff` crate has no Fax3 decoder, so the strips go
/// through [`g3_strip`] (bilevel, one sample, strips only).
fn decode_g3_page(data: &[u8], ifd: &IfdInfo) -> Result<Pixels, String> {
    let l = &ifd.strips;
    let (Some(w), Some(h)) = (l.width, l.height) else {
        return Err("tiff: missing image dimensions".to_owned());
    };
    if l.bits.unwrap_or(1) != 1 || l.samples.unwrap_or(1) != 1 || l.tiled || ifd.photometric > 1 {
        return Err("tiff: unsupported Group 3 layout".to_owned());
    }
    let (width, height) = (w as usize, h as usize);
    let rps = l.rows_per_strip.unwrap_or(u32::MAX).max(1) as usize;
    let strips = height.div_ceil(rps);
    if l.offsets.len() < strips || l.counts.len() < strips {
        return Err("tiff: inconsistent strip layout".to_owned());
    }
    let lsb_first = l.fill_order == Some(2);
    let mut pixels = Vec::with_capacity(width * height);
    for k in 0..strips {
        let (off, len) = (l.offsets[k] as usize, l.counts[k] as usize);
        let chunk = data
            .get(off..off.saturating_add(len))
            .ok_or("tiff: strip outside the file")?;
        let rows = rps.min(height - k * rps);
        pixels.extend(g3_strip(chunk, lsb_first, width, rows)?);
    }
    Ok(Pixels::Gray(pixels))
}

fn decode_tiff(data: &[u8]) -> Result<Vec<Decoded>, String> {
    use tiff::ColorType;
    use tiff::decoder::{Decoder, DecodingResult};
    use tiff::tags::Tag;

    if data.len() < 8 {
        return Err("unexpected EOF".to_owned());
    }
    if &data[..2] != b"II" && &data[..2] != b"MM" {
        return Err("invalid TIFF byte order".to_owned());
    }
    let ifds = scan_ifds(data)?;
    // Palette pages decode as gray indices; the crate refuses RGBPalette itself.
    let mut patched = data.to_vec();
    let little = &data[..2] == b"II";
    for ifd in &ifds {
        if let (Some(at), 3) = (ifd.compression_at, ifd.compression) {
            // Placeholder so the crate parses the IFD; the strips never reach it.
            patched[at..at + 2].copy_from_slice(&if little {
                1u16.to_le_bytes()
            } else {
                1u16.to_be_bytes()
            });
        }
        if let (Some(at), 3) = (ifd.photometric_at, ifd.photometric) {
            let one: [u8; 2] = if little {
                1u16.to_le_bytes()
            } else {
                1u16.to_be_bytes()
            };
            patched[at..at + 2].copy_from_slice(&one);
        }
    }
    let mut decoder = Decoder::new(Cursor::new(&patched[..])).map_err(|e| format!("tiff: {e}"))?;
    let mut out = Vec::new();
    for (index, ifd) in ifds.iter().enumerate() {
        if index > 0 {
            decoder.next_image().map_err(|e| format!("tiff: {e}"))?;
        }
        let (w, h) = decoder.dimensions().map_err(|e| format!("tiff: {e}"))?;
        let (width, height) = (w as usize, h as usize);
        let color = decoder.colortype().map_err(|e| format!("tiff: {e}"))?;
        let photometric = if ifd.photometric == 3 {
            3
        } else {
            ifd.photometric
        };
        let extra = decoder
            .find_tag_unsigned::<u16>(Tag::ExtraSamples)
            .map_err(|e| format!("tiff: {e}"))?;
        if ifd.compression == 3 {
            out.push(Decoded {
                width,
                height,
                pixels: decode_g3_page(data, ifd)?,
            });
            continue;
        }
        let decoded = decoder.read_image().map_err(|e| format!("tiff: {e}"))?;
        let wide = |v: &DecodingResult| -> Option<Vec<u16>> {
            match v {
                DecodingResult::U16(v) => Some(v.clone()),
                DecodingResult::U8(v) => Some(v.iter().map(|b| u16::from(*b)).collect()),
                _ => None,
            }
        };
        let pixels = match (color, &decoded) {
            (ColorType::Gray(8), DecodingResult::U8(v)) if photometric == 3 => {
                let map = ifd.colormap.as_ref().ok_or("tiff: missing colormap")?;
                let n = map.len() / 3;
                let pal: Vec<[u8; 4]> = (0..n)
                    .map(|i| {
                        [
                            (map[i] >> 8) as u8,
                            (map[n + i] >> 8) as u8,
                            (map[2 * n + i] >> 8) as u8,
                            0xFF,
                        ]
                    })
                    .collect();
                if v.iter().any(|i| usize::from(*i) >= pal.len()) {
                    return Err("tiff: invalid color index".to_owned());
                }
                Pixels::Paletted(v.clone(), pal)
            }
            (ColorType::Gray(1), DecodingResult::U8(v)) => {
                let fax = ifd.compression == 4;
                let row = width.div_ceil(8);
                let mut g = Vec::with_capacity(width * height);
                for y in 0..height {
                    for x in 0..width {
                        let bit = (v[y * row + x / 8] >> (7 - x % 8)) & 1;
                        let value = if (photometric == 0) != fax {
                            1 - bit
                        } else {
                            bit
                        };
                        g.push(value * 0xFF);
                    }
                }
                Pixels::Gray(g)
            }
            (ColorType::Gray(8), DecodingResult::U8(v)) => Pixels::Gray(if photometric == 0 {
                v.iter().map(|b| 255 - b).collect()
            } else {
                v.clone()
            }),
            (ColorType::Gray(16), _) => {
                let v = wide(&decoded).ok_or("tiff: unsupported sample type")?;
                Pixels::Gray16(if photometric == 0 {
                    v.iter().map(|b| 65535 - b).collect()
                } else {
                    v
                })
            }
            (ColorType::RGB(8), DecodingResult::U8(v)) => Pixels::Rgba(
                v.as_chunks::<3>()
                    .0
                    .iter()
                    .map(|c| [c[0], c[1], c[2], 0xFF])
                    .collect(),
            ),
            (ColorType::RGB(16), _) => {
                let v = wide(&decoded).ok_or("tiff: unsupported sample type")?;
                Pixels::Rgba64(
                    v.as_chunks::<3>()
                        .0
                        .iter()
                        .map(|c| [c[0], c[1], c[2], 0xFFFF])
                        .collect(),
                )
            }
            (ColorType::RGBA(8), DecodingResult::U8(v)) => {
                let px: Vec<[u8; 4]> = v
                    .as_chunks::<4>()
                    .0
                    .iter()
                    .map(|c| [c[0], c[1], c[2], c[3]])
                    .collect();
                match extra {
                    Some(1) => Pixels::Rgba(px),
                    Some(2) => Pixels::Nrgba(px),
                    _ => return Err("tiff: wrong number of samples for RGB".to_owned()),
                }
            }
            (ColorType::RGBA(16), _) => {
                let v = wide(&decoded).ok_or("tiff: unsupported sample type")?;
                let px: Vec<[u16; 4]> = v
                    .as_chunks::<4>()
                    .0
                    .iter()
                    .map(|c| [c[0], c[1], c[2], c[3]])
                    .collect();
                match extra {
                    Some(1) => Pixels::Rgba64(px),
                    Some(2) => Pixels::Nrgba64(px),
                    _ => return Err("tiff: wrong number of samples for RGB".to_owned()),
                }
            }
            _ => return Err(format!("tiff: unsupported color model {color:?}")),
        };
        out.push(Decoded {
            width,
            height,
            pixels,
        });
    }
    Ok(out)
}

// ----------------------------------------------------------------------------
// JPEG: pdfcpu does not decode, it passes the file through as /DCTDecode

fn jpeg_resource(data: &[u8]) -> Result<ImageResource, String> {
    let bad = |m: &str| Err(format!("decode image configuration: {m}"));
    if data.len() < 2 || data[0] != 0xFF || data[1] != 0xD8 {
        return bad("invalid JPEG format: missing SOI marker");
    }
    let mut pos = 2usize;
    let mut jfif = false;
    let mut adobe: Option<u8> = None;
    let mut sof: Option<(usize, usize, usize, [u8; 4])> = None; // width, height, ncomp, ids
    let byte = |p: usize| data.get(p).copied();
    loop {
        // Go: readFull two bytes, then skip to the next 0xFF.
        let (Some(mut a), Some(mut b)) = (byte(pos), byte(pos + 1)) else {
            return bad("unexpected EOF");
        };
        pos += 2;
        while a != 0xFF {
            a = b;
            let Some(n) = byte(pos) else {
                return bad("unexpected EOF");
            };
            b = n;
            pos += 1;
        }
        let mut marker = b;
        if marker == 0 {
            continue;
        }
        while marker == 0xFF {
            let Some(n) = byte(pos) else {
                return bad("unexpected EOF");
            };
            marker = n;
            pos += 1;
        }
        if marker == 0xD9 {
            break;
        }
        if (0xD0..=0xD7).contains(&marker) {
            continue;
        }
        let Some(len) = data.get(pos..pos + 2) else {
            return bad("unexpected EOF");
        };
        pos += 2;
        let n = (usize::from(len[0]) << 8 | usize::from(len[1])) as i64 - 2;
        if n < 0 {
            return bad("invalid JPEG format: short segment length");
        }
        let n = n as usize;
        let segment = data.get(pos..).unwrap_or(&[]);
        let ensure = |count: usize| -> Result<(), String> {
            if segment.len() < count {
                Err("decode image configuration: unexpected EOF".to_owned())
            } else {
                Ok(())
            }
        };
        match marker {
            0xC0..=0xC2 => {
                if sof.is_some() {
                    return bad("invalid JPEG format: multiple SOF markers");
                }
                let ncomp = match n {
                    9 => 1,
                    15 => 3,
                    18 => 4,
                    _ => return bad("unsupported JPEG feature: number of components"),
                };
                ensure(n)?;
                if segment[0] != 8 {
                    return bad("unsupported JPEG feature: precision");
                }
                let height = usize::from(segment[1]) << 8 | usize::from(segment[2]);
                let width = usize::from(segment[3]) << 8 | usize::from(segment[4]);
                if usize::from(segment[5]) != ncomp {
                    return bad("invalid JPEG format: SOF has wrong length");
                }
                let mut ids = [0u8; 4];
                let mut hv_all = [0u8; 4];
                for i in 0..ncomp {
                    ids[i] = segment[6 + 3 * i];
                    if ids[..i].contains(&ids[i]) {
                        return bad("invalid JPEG format: repeated component identifier");
                    }
                    if segment[8 + 3 * i] > 3 {
                        return bad("invalid JPEG format: bad Tq value");
                    }
                    let hv = segment[7 + 3 * i];
                    let (h, v) = (usize::from(hv >> 4), usize::from(hv & 0x0F));
                    if !(1..=4).contains(&h) || !(1..=4).contains(&v) {
                        return bad("invalid JPEG format: luma/chroma subsampling ratio");
                    }
                    if h == 3 || v == 3 {
                        return bad("unsupported JPEG feature: luma/chroma subsampling ratio");
                    }
                    hv_all[i] = hv;
                    let (h, v) = if ncomp == 1 { (1, 1) } else { (h, v) };
                    let unsupported = match ncomp {
                        3 => match i {
                            0 => v == 4,
                            1 => {
                                let (h0, v0) =
                                    (usize::from(hv_all[0] >> 4), usize::from(hv_all[0] & 15));
                                h0 % h != 0 || v0 % v != 0
                            }
                            _ => hv_all[1] != hv_all[2],
                        },
                        4 => match i {
                            0 => hv != 0x11 && hv != 0x22,
                            1 | 2 => hv != 0x11,
                            _ => hv_all[0] != hv,
                        },
                        _ => false,
                    };
                    if unsupported {
                        return bad("unsupported JPEG feature: luma/chroma subsampling ratio");
                    }
                }
                sof = Some((width, height, ncomp, ids));
                pos += n;
                if jfif {
                    break;
                }
            }
            0xDA => break,
            0xE0 => {
                if n >= 5 {
                    ensure(5)?;
                    jfif = &segment[..5] == b"JFIF\0";
                }
                ensure(n)?;
                pos += n;
            }
            0xEE => {
                if n >= 12 {
                    ensure(12)?;
                    if &segment[..5] == b"Adobe" {
                        adobe = Some(segment[11]);
                    }
                }
                ensure(n)?;
                pos += n;
            }
            0xC4 | 0xDB | 0xDD | 0xE1..=0xEF | 0xFE => {
                ensure(n)?;
                pos += n;
            }
            m if m < 0xC0 => return bad("invalid JPEG format: unknown marker"),
            _ => return bad("unsupported JPEG feature: unknown marker"),
        }
    }
    let Some((width, height, ncomp, ids)) = sof else {
        return bad("invalid JPEG format: missing SOF marker");
    };
    let is_rgb = !jfif && (adobe == Some(0) || ids[..3] == *b"RGB");
    let colorspace = match ncomp {
        1 => "DeviceGray",
        3 if !is_rgb => "DeviceRGB",
        4 => "DeviceCMYK",
        _ => return Err("create JPEG image resources: unexpected color model for JPEG".to_owned()),
    };
    let mut dict = dictionary! {
        "Type" => "XObject",
        "Subtype" => "Image",
        "Width" => width as i64,
        "Height" => height as i64,
        "BitsPerComponent" => 8,
        "ColorSpace" => colorspace,
        "Filter" => "DCTDecode",
    };
    if colorspace == "DeviceCMYK" {
        dict.set(
            "Decode",
            vec![
                1.into(),
                0.into(),
                1.into(),
                0.into(),
                1.into(),
                0.into(),
                1.into(),
                0.into(),
            ],
        );
    }
    if width < 1000 || height < 1000 {
        dict.set("Interpolate", true);
    }
    Ok(ImageResource {
        width,
        height,
        object: Stream::new(dict, data.to_vec()),
        smask: None,
    })
}

// ----------------------------------------------------------------------------
// pdfcpu's pixel buffers and stream dictionaries

/// Go `color.NRGBAModel.Convert` of a premultiplied 8-bit colour.
fn rgba_to_nrgba(c: [u8; 4]) -> [u8; 4] {
    let (r, g, b, a) = (
        u32::from(c[0]) * 0x101,
        u32::from(c[1]) * 0x101,
        u32::from(c[2]) * 0x101,
        u32::from(c[3]) * 0x101,
    );
    if a == 0xFFFF {
        return [(r >> 8) as u8, (g >> 8) as u8, (b >> 8) as u8, 0xFF];
    }
    if a == 0 {
        return [0, 0, 0, 0];
    }
    let f = |v: u32| (((v * 0xFFFF) / a) >> 8) as u8;
    [f(r), f(g), f(b), (a >> 8) as u8]
}

/// Go `color.NRGBA64Model.Convert` of a premultiplied 16-bit colour.
fn rgba64_to_nrgba64(c: [u16; 4]) -> [u16; 4] {
    let (r, g, b, a) = (
        u32::from(c[0]),
        u32::from(c[1]),
        u32::from(c[2]),
        u32::from(c[3]),
    );
    if a == 0xFFFF {
        return c;
    }
    if a == 0 {
        return [0, 0, 0, 0];
    }
    let f = |v: u32| ((v * 0xFFFF) / a) as u16;
    [f(r), f(g), f(b), c[3]]
}

fn flate_image(
    width: usize,
    height: usize,
    bpc: i64,
    colorspace: Object,
    buf: Vec<u8>,
    smask: Option<Vec<u8>>,
) -> ImageResource {
    let smask_stream = smask.map(|sm| {
        Stream::new(
            dictionary! {
                "Type" => "XObject", "Subtype" => "Image", "BitsPerComponent" => bpc,
                "ColorSpace" => "DeviceGray", "Width" => width as i64, "Height" => height as i64,
            },
            sm,
        )
    });
    let mut dict = dictionary! {
        "Type" => "XObject", "Subtype" => "Image", "Width" => width as i64, "Height" => height as i64,
        "BitsPerComponent" => bpc, "ColorSpace" => colorspace,
    };
    if width < 1000 || height < 1000 {
        dict.set("Interpolate", true);
    }
    ImageResource {
        width,
        height,
        object: Stream::new(dict, buf),
        smask: smask_stream,
    }
}

/// The soft mask pdfcpu keeps only when some alpha differs from opaque.
fn soft_mask8(alpha: impl Iterator<Item = u8> + Clone) -> Option<Vec<u8>> {
    alpha.clone().any(|a| a != 0xFF).then(|| alpha.collect())
}

fn decoded_resource(d: Decoded, png: bool) -> Result<ImageResource, String> {
    let (w, h) = (d.width, d.height);
    Ok(match d.pixels {
        Pixels::Gray(v) => flate_image(w, h, 8, "DeviceGray".into(), v, None),
        Pixels::Gray16(v) => flate_image(
            w,
            h,
            16,
            "DeviceGray".into(),
            v.iter().flat_map(|x| x.to_be_bytes()).collect(),
            None,
        ),
        Pixels::Rgba(v) => {
            let n: Vec<[u8; 4]> = v.iter().map(|c| rgba_to_nrgba(*c)).collect();
            let sm = soft_mask8(n.iter().map(|c| c[3]));
            flate_image(
                w,
                h,
                8,
                "DeviceRGB".into(),
                n.iter().flat_map(|c| [c[0], c[1], c[2]]).collect(),
                sm,
            )
        }
        Pixels::Nrgba(v) => {
            let sm = soft_mask8(v.iter().map(|c| c[3]));
            flate_image(
                w,
                h,
                8,
                "DeviceRGB".into(),
                v.iter().flat_map(|c| [c[0], c[1], c[2]]).collect(),
                sm,
            )
        }
        Pixels::Rgba64(v) => {
            let n: Vec<[u16; 4]> = v.iter().map(|c| rgba64_to_nrgba64(*c)).collect();
            rgb16(w, h, &n)
        }
        Pixels::Nrgba64(v) => rgb16(w, h, &v),
        Pixels::Paletted(pix, palette) => {
            if palette.is_empty() {
                return Err(
                    "create decoded image resources: paletted image: missing palette".to_owned(),
                );
            }
            if png {
                let lookup: Vec<u8> = palette.iter().flat_map(|c| [c[0], c[1], c[2]]).collect();
                let cs = Object::Array(vec![
                    Object::Name(b"Indexed".to_vec()),
                    Object::Name(b"DeviceRGB".to_vec()),
                    Object::Integer(palette.len() as i64 - 1),
                    Object::String(lookup, StringFormat::Hexadecimal),
                ]);
                let sm = soft_mask8(pix.iter().map(|i| palette[*i as usize][3]));
                let sm = sm.map(|_| pix.iter().map(|i| palette[*i as usize][3]).collect());
                flate_image(w, h, 8, cs, pix, sm)
            } else {
                // Other formats go through convertToRGBA: opaque straight RGB.
                let n: Vec<[u8; 4]> = pix
                    .iter()
                    .map(|i| rgba_to_nrgba(palette[*i as usize]))
                    .collect();
                let sm = soft_mask8(n.iter().map(|c| c[3]));
                flate_image(
                    w,
                    h,
                    8,
                    "DeviceRGB".into(),
                    n.iter().flat_map(|c| [c[0], c[1], c[2]]).collect(),
                    sm,
                )
            }
        }
    })
}

fn rgb16(w: usize, h: usize, v: &[[u16; 4]]) -> ImageResource {
    let buf: Vec<u8> = v
        .iter()
        .flat_map(|c| [c[0], c[1], c[2]])
        .flat_map(u16::to_be_bytes)
        .collect();
    let sm = v
        .iter()
        .any(|c| c[3] != 0xFFFF)
        .then(|| v.iter().flat_map(|c| c[3].to_be_bytes()).collect());
    flate_image(w, h, 16, "DeviceRGB".into(), buf, sm)
}

// ----------------------------------------------------------------------------
// Document assembly (pdfcpu `Pos == Full`: page = image)

fn go_f(v: f64) -> String {
    format!("{v:.6}")
}

fn build_pdf(resources: Vec<ImageResource>) -> Result<Vec<u8>, String> {
    let mut doc = Document::with_version("1.7");
    let pages_id: ObjectId = doc.new_object_id();
    let mut kids = Vec::new();
    for res in resources {
        let smask_id = res.smask.map(|s| doc.add_object(s));
        let mut object = res.object;
        if let Some(id) = smask_id {
            object.dict.set("SMask", Object::Reference(id));
        }
        let is_dct = object.dict.get(b"Filter").is_ok();
        if !is_dct {
            object.dict.set("Filter", "FlateDecode");
            object = compress(object)?;
        }
        let image_id = doc.add_object(object);
        let resources_id = doc.add_object(dictionary! {
            "ProcSet" => vec![Object::Name(b"PDF".to_vec()), Object::Name(b"Text".to_vec()), Object::Name(b"ImageB".to_vec()), Object::Name(b"ImageC".to_vec()), Object::Name(b"ImageI".to_vec())],
            "XObject" => dictionary! { "Im0" => Object::Reference(image_id) },
        });
        let content = format!(
            "q {} 0 0 {} 0 0 cm /Im0 Do Q",
            go_f(res.width as f64),
            go_f(res.height as f64)
        );
        let content_id = doc.add_object(compress(Stream::new(
            Dictionary::new(),
            content.into_bytes(),
        ))?);
        let page_id = doc.add_object(dictionary! {
            "Type" => "Page",
            "Parent" => Object::Reference(pages_id),
            "MediaBox" => vec![0.into(), 0.into(), Object::Real(res.width as f32), Object::Real(res.height as f32)],
            "Resources" => Object::Reference(resources_id),
            "Contents" => Object::Reference(content_id),
        });
        kids.push(Object::Reference(page_id));
    }
    let count = kids.len() as i64;
    doc.objects.insert(
        pages_id,
        Object::Dictionary(dictionary! {
            "Type" => "Pages",
            "Count" => count,
            "Kids" => kids,
            "MediaBox" => vec![0.into(), 0.into(), Object::Real(595.0), Object::Real(842.0)],
        }),
    );
    let catalog =
        doc.add_object(dictionary! { "Type" => "Catalog", "Pages" => Object::Reference(pages_id) });
    doc.trailer.set("Root", Object::Reference(catalog));
    let mut out = Vec::new();
    doc.save_to(&mut out)
        .map_err(|e| format!("write PDF: {e}"))?;
    Ok(out)
}

/// Flate-compress a stream's content in place (`/Filter` must be set by caller
/// for images; content streams get it here).
pub(crate) fn compress(mut stream: Stream) -> Result<Stream, String> {
    use std::io::Write;
    let mut encoder = flate2::write::ZlibEncoder::new(Vec::new(), flate2::Compression::default());
    encoder
        .write_all(&stream.content)
        .map_err(|e| e.to_string())?;
    stream.content = encoder.finish().map_err(|e| e.to_string())?;
    stream.dict.set("Filter", "FlateDecode");
    stream.dict.set("Length", stream.content.len() as i64);
    stream.allows_compression = false;
    Ok(stream)
}
