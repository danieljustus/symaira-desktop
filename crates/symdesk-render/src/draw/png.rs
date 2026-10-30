//! Port of Go `internal/draw/emit/png.go`: the scene rasterizer built on
//! `golang.org/x/image/vector` and the deterministic PNG encoder (Go
//! `image/png` + `compress/flate`, see [`super::go_flate`]).
//!
//! Bit-exactness notes:
//!
//! * The recorded Go oracle ran on arm64, where the Go compiler fuses
//!   `a ± x*y` float32 expressions into FMA instructions unless the product
//!   is wrapped in an explicit `float32(...)` conversion (the `vector`
//!   package uses such conversions in its line rasterizers, but not in
//!   `lerp`/`devSquared`, and `png.go` has none). Those fused sites are
//!   reproduced with [`f32::mul_add`], which is a true IEEE fused
//!   multiply-add on every host, so the port is deterministic and equals the
//!   arm64 oracle. A Go build for amd64 (no fusion) can differ in rare
//!   anti-aliasing pixels.
//! * Float to integer conversions saturate (`as`), matching arm64.
//! * `image/draw`/`vector` pixel arithmetic is `uint32` and wraps.

#![allow(clippy::chunks_exact_to_as_chunks)]

use super::go_flate::{ZlibWriter, write_chunk};
use super::measure::FontWeight;
use super::scene::{
    PathSegment, Primitive, RectElement, Scene, TextElement, default_measurer, go_max,
};

/// Go `emit.RasterOptions`.
#[derive(Debug, Clone, Copy, Default)]
pub struct RasterOptions {
    pub scale: f64,
}

/// A rasterized `image.RGBA`.
pub struct Rgba {
    pub width: usize,
    pub height: usize,
    pub pix: Vec<u8>,
}

/// `float32(0.5522847498)`.
const KAPPA: f32 = f32::from_bits(0x3f0d_6289);
const ONE_MINUS_KAPPA: f32 = 1.0 - KAPPA;

// ---------------------------------------------------------------------------
// Go builtin min/max for float32 (NaN propagates, -0 < +0).
// ---------------------------------------------------------------------------
fn gmin32(a: f32, b: f32) -> f32 {
    if a.is_nan() || b.is_nan() {
        return f32::NAN;
    }
    if a == 0.0 && b == 0.0 {
        return if a.is_sign_negative() { a } else { b };
    }
    if a < b { a } else { b }
}

fn gmax32(a: f32, b: f32) -> f32 {
    if a.is_nan() || b.is_nan() {
        return f32::NAN;
    }
    if a == 0.0 && b == 0.0 {
        return if a.is_sign_negative() { b } else { a };
    }
    if a > b { a } else { b }
}

/// `a + x*y` as one FMA (arm64 `FMADDS`).
fn fmadd(a: f32, x: f32, y: f32) -> f32 {
    x.mul_add(y, a)
}

/// `a - x*y` as one FMA (arm64 `FMSUBS`).
fn fmsub(a: f32, x: f32, y: f32) -> f32 {
    (-x).mul_add(y, a)
}

// ---------------------------------------------------------------------------
// golang.org/x/image/vector
// ---------------------------------------------------------------------------
const FLOATING_POINT_MATH_THRESHOLD: usize = 512;
const PHI: u32 = 9;
const FX_ONE: i32 = 1 << PHI;
const FX_ONE_AND_A_HALF: i32 = (1 << PHI) + (1 << (PHI - 1));
const FX_ONE_MINUS_IOTA: i32 = (1 << PHI) - 1;
const ALMOST_65536: f32 = f32::from_bits(0x477f_ffff);

fn clamp(i: i32, width: i32) -> usize {
    if i < 0 {
        return 0;
    }
    if i < width {
        return i as usize;
    }
    width as usize
}

fn lerp(t: f32, px: f32, py: f32, qx: f32, qy: f32) -> (f32, f32) {
    (fmadd(px, t, qx - px), fmadd(py, t, qy - py))
}

fn dev_squared(ax: f32, ay: f32, bx: f32, by: f32, cx: f32, cy: f32) -> f32 {
    let devx = ax - 2.0 * bx + cx;
    let devy = ay - 2.0 * by + cy;
    fmadd(devx * devx, devy, devy)
}

struct Rasterizer {
    w: usize,
    h: usize,
    floating: bool,
    buf_f32: Vec<f32>,
    buf_u32: Vec<u32>,
    first_x: f32,
    first_y: f32,
    pen_x: f32,
    pen_y: f32,
    lo: usize,
    hi: usize, // exclusive; lo >= hi means untouched
}

impl Rasterizer {
    fn new(w: usize, h: usize) -> Rasterizer {
        let floating = w > FLOATING_POINT_MATH_THRESHOLD || h > FLOATING_POINT_MATH_THRESHOLD;
        let n = w * h;
        Rasterizer {
            w,
            h,
            floating,
            buf_f32: if floating { vec![0.0; n] } else { Vec::new() },
            buf_u32: if floating { Vec::new() } else { vec![0; n] },
            first_x: 0.0,
            first_y: 0.0,
            pen_x: 0.0,
            pen_y: 0.0,
            lo: usize::MAX,
            hi: 0,
        }
    }

    /// Go `NewRasterizer` for the next primitive: the buffers are already
    /// zero (cleared after each `draw`), only the pen state resets.
    fn begin(&mut self) {
        self.first_x = 0.0;
        self.first_y = 0.0;
        self.pen_x = 0.0;
        self.pen_y = 0.0;
    }

    fn touch(&mut self, idx: usize) {
        if idx < self.lo {
            self.lo = idx;
        }
        if idx + 1 > self.hi {
            self.hi = idx + 1;
        }
    }

    fn move_to(&mut self, ax: f32, ay: f32) {
        self.first_x = ax;
        self.first_y = ay;
        self.pen_x = ax;
        self.pen_y = ay;
    }

    fn close_path(&mut self) {
        self.line_to(self.first_x, self.first_y);
    }

    fn line_to(&mut self, bx: f32, by: f32) {
        if self.floating {
            self.floating_line_to(bx, by);
        } else {
            self.fixed_line_to(bx, by);
        }
    }

    fn quad_to(&mut self, bx: f32, by: f32, cx: f32, cy: f32) {
        let (ax, ay) = (self.pen_x, self.pen_y);
        let devsq = dev_squared(ax, ay, bx, by, cx, cy);
        if devsq >= 0.333 {
            let n = 1 + (3.0 * f64::from(devsq)).sqrt().sqrt() as i64;
            let mut t = 0.0f32;
            let n_inv = 1.0 / (n as f32);
            let mut i = 0;
            while i < n - 1 {
                t += n_inv;
                let (abx, aby) = lerp(t, ax, ay, bx, by);
                let (bcx, bcy) = lerp(t, bx, by, cx, cy);
                let (x, y) = lerp(t, abx, aby, bcx, bcy);
                self.line_to(x, y);
                i += 1;
            }
        }
        self.line_to(cx, cy);
    }

    fn cube_to(&mut self, bx: f32, by: f32, cx: f32, cy: f32, dx: f32, dy: f32) {
        let (ax, ay) = (self.pen_x, self.pen_y);
        let mut devsq = dev_squared(ax, ay, bx, by, dx, dy);
        let devsq_alt = dev_squared(ax, ay, cx, cy, dx, dy);
        if devsq < devsq_alt {
            devsq = devsq_alt;
        }
        if devsq >= 0.333 {
            let n = 1 + (3.0 * f64::from(devsq)).sqrt().sqrt() as i64;
            let mut t = 0.0f32;
            let n_inv = 1.0 / (n as f32);
            let mut i = 0;
            while i < n - 1 {
                t += n_inv;
                let (abx, aby) = lerp(t, ax, ay, bx, by);
                let (bcx, bcy) = lerp(t, bx, by, cx, cy);
                let (cdx, cdy) = lerp(t, cx, cy, dx, dy);
                let (abcx, abcy) = lerp(t, abx, aby, bcx, bcy);
                let (bcdx, bcdy) = lerp(t, bcx, bcy, cdx, cdy);
                let (x, y) = lerp(t, abcx, abcy, bcdx, bcdy);
                self.line_to(x, y);
                i += 1;
            }
        }
        self.line_to(dx, dy);
    }

    // -- fixed point -------------------------------------------------------
    fn add_u32(&mut self, base: usize, i: usize, v: u32) {
        let idx = base + i;
        if idx < self.buf_u32.len() {
            self.buf_u32[idx] = self.buf_u32[idx].wrapping_add(v);
            self.touch(idx);
        }
    }

    fn fixed_line_to(&mut self, bx: f32, by: f32) {
        let (mut ax, mut ay) = (self.pen_x, self.pen_y);
        let (mut bx, mut by) = (bx, by);
        self.pen_x = bx;
        self.pen_y = by;
        let mut dir: i32 = 1;
        if ay > by {
            dir = -1;
            std::mem::swap(&mut ax, &mut bx);
            std::mem::swap(&mut ay, &mut by);
        }
        if by - ay <= 0.000001 {
            return;
        }
        let dxdy = (bx - ax) / (by - ay);

        let ay_phi = (ay * FX_ONE as f32) as i32;
        let by_phi = (by * FX_ONE as f32) as i32;

        let mut x = (ax * FX_ONE as f32) as i32;
        let mut y = ay_phi >> PHI;
        let mut y_max = by_phi.wrapping_add(FX_ONE_MINUS_IOTA) >> PHI;
        if y_max > self.h as i32 {
            y_max = self.h as i32;
        }
        let width = self.w as i32;

        while y < y_max {
            let dy = (y.wrapping_add(1).wrapping_shl(PHI))
                .min(by_phi)
                .wrapping_sub(y.wrapping_shl(PHI).max(ay_phi));
            let x_next = x.wrapping_add((dy as f32 * dxdy) as i32);
            if y < 0 {
                x = x_next;
                y += 1;
                continue;
            }
            let base = (y.wrapping_mul(width)) as usize;
            let d = dy.wrapping_mul(dir);
            let (mut x0, mut x1) = (x, x_next);
            if x > x_next {
                std::mem::swap(&mut x0, &mut x1);
            }
            let x0i = x0 >> PHI;
            let x0_floor = x0i.wrapping_shl(PHI);
            let x1i = x1.wrapping_add(FX_ONE_MINUS_IOTA) >> PHI;
            let x1_ceil = x1i.wrapping_shl(PHI);

            if x1i <= x0i.wrapping_add(1) {
                let xmf = (x.wrapping_add(x_next) >> 1).wrapping_sub(x0_floor);
                self.add_u32(
                    base,
                    clamp(x0i, width),
                    d.wrapping_mul(FX_ONE.wrapping_sub(xmf)) as u32,
                );
                self.add_u32(
                    base,
                    clamp(x0i.wrapping_add(1), width),
                    d.wrapping_mul(xmf) as u32,
                );
            } else {
                let one_over_s = x1.wrapping_sub(x0);
                let two_over_s = one_over_s.wrapping_mul(2);
                let x0f = x0.wrapping_sub(x0_floor);
                let one_minus_x0f = FX_ONE.wrapping_sub(x0f);
                let one_minus_x0f_squared = one_minus_x0f.wrapping_mul(one_minus_x0f);
                let x1f = x1.wrapping_sub(x1_ceil).wrapping_add(FX_ONE);
                let x1f_squared = x1f.wrapping_mul(x1f);

                let dd = one_minus_x0f_squared
                    .wrapping_mul(d)
                    .wrapping_div(two_over_s);
                self.add_u32(base, clamp(x0i, width), dd as u32);

                if x1i == x0i.wrapping_add(2) {
                    let dd = two_over_s
                        .wrapping_shl(PHI)
                        .wrapping_sub(one_minus_x0f_squared)
                        .wrapping_sub(x1f_squared)
                        .wrapping_mul(d)
                        .wrapping_div(two_over_s);
                    self.add_u32(base, clamp(x0i.wrapping_add(1), width), dd as u32);
                } else {
                    let dd = FX_ONE_AND_A_HALF
                        .wrapping_sub(x0f)
                        .wrapping_shl(PHI + 1)
                        .wrapping_sub(one_minus_x0f_squared)
                        .wrapping_mul(d)
                        .wrapping_div(two_over_s);
                    self.add_u32(base, clamp(x0i.wrapping_add(1), width), dd as u32);
                    let d_times_s = (d.wrapping_shl(2 * PHI).wrapping_div(one_over_s)) as u32;
                    let mut xi = x0i.wrapping_add(2);
                    while xi < x1i.wrapping_sub(1) {
                        self.add_u32(base, clamp(xi, width), d_times_s);
                        xi = xi.wrapping_add(1);
                    }
                    const C: i32 = (1 << (PHI + 2)) - (FX_ONE_AND_A_HALF << 1);
                    let dd = x1f
                        .wrapping_shl(1)
                        .wrapping_add(C)
                        .wrapping_shl(PHI)
                        .wrapping_sub(x1f_squared)
                        .wrapping_mul(d)
                        .wrapping_div(two_over_s);
                    self.add_u32(base, clamp(x1i.wrapping_sub(1), width), dd as u32);
                }
                let dd = x1f_squared.wrapping_mul(d).wrapping_div(two_over_s);
                self.add_u32(base, clamp(x1i, width), dd as u32);
            }
            x = x_next;
            y += 1;
        }
    }

    // -- floating point ----------------------------------------------------
    fn add_f32(&mut self, base: usize, i: usize, v: f32) {
        let idx = base + i;
        if idx < self.buf_f32.len() {
            self.buf_f32[idx] += v;
            self.touch(idx);
        }
    }

    fn floating_line_to(&mut self, bx: f32, by: f32) {
        let (mut ax, mut ay) = (self.pen_x, self.pen_y);
        let (mut bx, mut by) = (bx, by);
        self.pen_x = bx;
        self.pen_y = by;
        let mut dir = 1.0f32;
        if ay > by {
            dir = -1.0;
            std::mem::swap(&mut ax, &mut bx);
            std::mem::swap(&mut ay, &mut by);
        }
        if by - ay <= 0.000001 {
            return;
        }
        let dxdy = (bx - ax) / (by - ay);

        let mut x = ax;
        let mut y = (f64::from(ay)).floor() as i32;
        let mut y_max = (f64::from(by)).ceil() as i32;
        if y_max > self.h as i32 {
            y_max = self.h as i32;
        }
        let width = self.w as i32;

        while y < y_max {
            let dy = gmin32((y.wrapping_add(1)) as f32, by) - gmax32(y as f32, ay);
            let x_next = x + dy * dxdy;
            if y < 0 {
                x = x_next;
                y += 1;
                continue;
            }
            let base = (y.wrapping_mul(width)) as usize;
            let d = dy * dir;
            let (mut x0, mut x1) = (x, x_next);
            if x > x_next {
                std::mem::swap(&mut x0, &mut x1);
            }
            let x0i = (f64::from(x0)).floor() as i32;
            let x0_floor = x0i as f32;
            let x1i = (f64::from(x1)).ceil() as i32;
            let x1_ceil = x1i as f32;

            if x1i <= x0i.wrapping_add(1) {
                let xmf = 0.5 * (x + x_next) - x0_floor;
                self.add_f32(base, clamp(x0i, width), d - d * xmf);
                self.add_f32(base, clamp(x0i.wrapping_add(1), width), d * xmf);
            } else {
                let s = 1.0 / (x1 - x0);
                let x0f = x0 - x0_floor;
                let one_minus_x0f = 1.0 - x0f;
                let a0 = 0.5 * s * one_minus_x0f * one_minus_x0f;
                let x1f = x1 - x1_ceil + 1.0;
                let am = 0.5 * s * x1f * x1f;

                self.add_f32(base, clamp(x0i, width), d * a0);

                if x1i == x0i.wrapping_add(2) {
                    self.add_f32(base, clamp(x0i.wrapping_add(1), width), d * (1.0 - a0 - am));
                } else {
                    let a1 = s * (1.5 - x0f);
                    self.add_f32(base, clamp(x0i.wrapping_add(1), width), d * (a1 - a0));
                    let d_times_s = d * s;
                    let mut xi = x0i.wrapping_add(2);
                    while xi < x1i.wrapping_sub(1) {
                        self.add_f32(base, clamp(xi, width), d_times_s);
                        xi = xi.wrapping_add(1);
                    }
                    let a2 = a1 + s * (x1i.wrapping_sub(x0i).wrapping_sub(3) as f32);
                    self.add_f32(base, clamp(x1i.wrapping_sub(1), width), d * (1.0 - a2 - am));
                }
                self.add_f32(base, clamp(x1i, width), d * am);
            }
            x = x_next;
            y += 1;
        }
    }

    // -- compositing -------------------------------------------------------
    /// `Draw(img, img.Bounds(), image.NewUniform(col), image.Point{})` with
    /// `DrawOp = draw.Over`: accumulate the coverage mask over the whole
    /// buffer (the running sum carries across rows) and blend. Pixels with a
    /// zero mask are left unchanged by `Over`, so only the touched span (and a
    /// non-zero residual tail) is visited. The buffer is cleared afterwards.
    fn draw(&mut self, img: &mut [u8], col: (u32, u32, u32, u32)) {
        let n = self.w * self.h;
        if self.lo >= self.hi {
            return;
        }
        let (lo, hi) = (self.lo, self.hi.min(n));
        let (sr, sg, sb, sa) = col;
        let mut blend = |i: usize, ma: u32| {
            if ma == 0 {
                return;
            }
            let a = 0xffff - (sa.wrapping_mul(ma) / 0xffff);
            let p = i * 4;
            let mix = |d: u8, s: u32| -> u8 {
                let sum = (u32::from(d) * 0x101)
                    .wrapping_mul(a)
                    .wrapping_add(s.wrapping_mul(ma));
                ((sum / 0xffff) >> 8) as u8
            };
            img[p] = mix(img[p], sr);
            img[p + 1] = mix(img[p + 1], sg);
            img[p + 2] = mix(img[p + 2], sb);
            img[p + 3] = mix(img[p + 3], sa);
        };
        if self.floating {
            let mut acc = 0.0f32;
            let mut ma = 0u32;
            for i in lo..hi {
                acc += self.buf_f32[i];
                let mut a = acc;
                if a < 0.0 {
                    a = -a;
                }
                if a > 1.0 {
                    a = 1.0;
                }
                ma = (ALMOST_65536 * a) as u32;
                blend(i, ma);
                self.buf_f32[i] = 0.0;
            }
            for i in hi..n {
                if ma == 0 {
                    break;
                }
                blend(i, ma);
            }
        } else {
            let mut acc = 0i32;
            let mut ma = 0u32;
            for i in lo..hi {
                acc = acc.wrapping_add(self.buf_u32[i] as i32);
                let mut a = if acc < 0 { acc.wrapping_neg() } else { acc };
                a >>= 2 * PHI - 16;
                if a > 0xffff {
                    a = 0xffff;
                }
                ma = a as u32;
                blend(i, ma);
                self.buf_u32[i] = 0;
            }
            for i in hi..n {
                if ma == 0 {
                    break;
                }
                blend(i, ma);
            }
        }
        self.lo = usize::MAX;
        self.hi = 0;
    }
}

// ---------------------------------------------------------------------------
// Colors
// ---------------------------------------------------------------------------

/// Premultiplied 16-bit `(r, g, b, a)` as `color.Color.RGBA()` reports it.
type Premul = (u32, u32, u32, u32);

fn nrgba_premul(r: u8, g: u8, b: u8, a: u8) -> Premul {
    let conv = |c: u8| -> u32 {
        let mut v = u32::from(c);
        v |= v << 8;
        v *= u32::from(a);
        v / 0xff
    };
    let mut aa = u32::from(a);
    aa |= aa << 8;
    (conv(r), conv(g), conv(b), aa)
}

fn hex_byte(a: u8, b: u8) -> u8 {
    // strconv.ParseUint(string([]byte{a, b}), 16, 8): 0 on a syntax error.
    let digit = |c: u8| -> Option<u8> { (c as char).to_digit(16).map(|d| d as u8) };
    match (digit(a), digit(b)) {
        (Some(h), Some(l)) => h << 4 | l,
        _ => 0,
    }
}

/// Go `parseColor`; `None` is its error return.
fn parse_color(c: &str, opacity: f64) -> Option<Premul> {
    let lowered = c.to_lowercase();
    let c = super::gotext::trim_space(&lowered);
    if c.is_empty() || c == "none" || c == "transparent" {
        return Some((0, 0, 0, 0));
    }
    let alpha_mul = if opacity > 0.0 && opacity <= 1.0 {
        opacity
    } else {
        1.0
    };
    let alpha = |a: u8| -> u8 { (f64::from(a) * alpha_mul) as u8 };
    if let Some(hex) = c.strip_prefix('#') {
        let h = hex.as_bytes();
        match h.len() {
            3 => {
                let (r, g, b) = (
                    hex_byte(h[0], h[0]),
                    hex_byte(h[1], h[1]),
                    hex_byte(h[2], h[2]),
                );
                return Some(nrgba_premul(r, g, b, (255.0 * alpha_mul) as u8));
            }
            4 => {
                let (r, g, b, a) = (
                    hex_byte(h[0], h[0]),
                    hex_byte(h[1], h[1]),
                    hex_byte(h[2], h[2]),
                    hex_byte(h[3], h[3]),
                );
                return Some(nrgba_premul(r, g, b, alpha(a)));
            }
            6 => {
                let (r, g, b) = (
                    hex_byte(h[0], h[1]),
                    hex_byte(h[2], h[3]),
                    hex_byte(h[4], h[5]),
                );
                return Some(nrgba_premul(r, g, b, (255.0 * alpha_mul) as u8));
            }
            8 => {
                let (r, g, b, a) = (
                    hex_byte(h[0], h[1]),
                    hex_byte(h[2], h[3]),
                    hex_byte(h[4], h[5]),
                    hex_byte(h[6], h[7]),
                );
                return Some(nrgba_premul(r, g, b, alpha(a)));
            }
            _ => {}
        }
    }
    match c {
        "white" => Some(nrgba_premul(255, 255, 255, (255.0 * alpha_mul) as u8)),
        "black" => Some(nrgba_premul(0, 0, 0, (255.0 * alpha_mul) as u8)),
        _ => None,
    }
}

// ---------------------------------------------------------------------------
// Scene rasterization
// ---------------------------------------------------------------------------
struct Xf {
    vb_x: f64,
    vb_y: f64,
    scale_x: f64,
    scale_y: f64,
}

impl Xf {
    fn tx(&self, x: f64) -> f32 {
        ((x - self.vb_x) * self.scale_x) as f32
    }
    fn ty(&self, y: f64) -> f32 {
        ((y - self.vb_y) * self.scale_y) as f32
    }
    fn ts(&self, size: f64) -> f32 {
        (size * self.scale_x) as f32
    }
}

fn is_paint(s: &str) -> bool {
    !s.is_empty() && s != "none"
}

fn add_round_rect_path(z: &mut Rasterizer, x: f32, y: f32, w: f32, h: f32, rx: f32, ry: f32) {
    if rx <= 0.0 || ry <= 0.0 {
        z.move_to(x, y);
        z.line_to(x + w, y);
        z.line_to(x + w, y + h);
        z.line_to(x, y + h);
        z.close_path();
        return;
    }
    // Each `a ± k` below is `a ± r*(1-kappa)` fused by the arm64 compiler.
    let xw_kx = fmsub(x + w, rx, ONE_MINUS_KAPPA); // x+w-kx
    let y_ky = fmadd(y, ry, ONE_MINUS_KAPPA); // y+ky
    let yh_ky = fmsub(y + h, ry, ONE_MINUS_KAPPA); // y+h-ky
    let x_kx = fmadd(x, rx, ONE_MINUS_KAPPA); // x+kx

    z.move_to(x + rx, y);
    z.line_to(x + w - rx, y);
    z.cube_to(xw_kx, y, x + w, y_ky, x + w, y + ry);
    z.line_to(x + w, y + h - ry);
    z.cube_to(x + w, yh_ky, xw_kx, y + h, x + w - rx, y + h);
    z.line_to(x + rx, y + h);
    z.cube_to(x_kx, y + h, x, yh_ky, x, y + h - ry);
    z.line_to(x, y + ry);
    z.cube_to(x, y_ky, x_kx, y, x + rx, y);
    z.close_path();
}

fn add_round_rect_path_ccw(z: &mut Rasterizer, x: f32, y: f32, w: f32, h: f32, rx: f64, ry: f64) {
    let rxf = rx as f32;
    let ryf = ry as f32;
    if rxf <= 0.0 || ryf <= 0.0 {
        z.move_to(x, y);
        z.line_to(x, y + h);
        z.line_to(x + w, y + h);
        z.line_to(x + w, y);
        z.close_path();
        return;
    }
    let x_kx = fmadd(x, rxf, ONE_MINUS_KAPPA);
    let y_ky = fmadd(y, ryf, ONE_MINUS_KAPPA);
    let yh_ky = fmsub(y + h, ryf, ONE_MINUS_KAPPA);
    let xw_kx = fmsub(x + w, rxf, ONE_MINUS_KAPPA);

    z.move_to(x + rxf, y);
    z.cube_to(x_kx, y, x, y_ky, x, y + ryf);
    z.line_to(x, y + h - ryf);
    z.cube_to(x, yh_ky, x_kx, y + h, x + rxf, y + h);
    z.line_to(x + w - rxf, y + h);
    z.cube_to(xw_kx, y + h, x + w, yh_ky, x + w, y + h - ryf);
    z.line_to(x + w, y + ryf);
    z.cube_to(x + w, y_ky, xw_kx, y, x + w - rxf, y);
    z.close_path();
}

fn add_ellipse_path(z: &mut Rasterizer, cx: f32, cy: f32, rx: f32, ry: f32) {
    let cy_oy_m = fmsub(cy, ry, KAPPA); // cy-oy
    let cx_ox_m = fmsub(cx, rx, KAPPA); // cx-ox
    let cx_ox_p = fmadd(cx, rx, KAPPA); // cx+ox
    let cy_oy_p = fmadd(cy, ry, KAPPA); // cy+oy
    z.move_to(cx - rx, cy);
    z.cube_to(cx - rx, cy_oy_m, cx_ox_m, cy - ry, cx, cy - ry);
    z.cube_to(cx_ox_p, cy - ry, cx + rx, cy_oy_m, cx + rx, cy);
    z.cube_to(cx + rx, cy_oy_p, cx_ox_p, cy + ry, cx, cy + ry);
    z.cube_to(cx_ox_m, cy + ry, cx - rx, cy_oy_p, cx - rx, cy);
    z.close_path();
}

fn add_ellipse_path_ccw(z: &mut Rasterizer, cx: f32, cy: f32, rx: f32, ry: f32) {
    let cy_oy_p = fmadd(cy, ry, KAPPA); // cy+oy
    let cx_ox_m = fmsub(cx, rx, KAPPA); // cx-ox
    let cx_ox_p = fmadd(cx, rx, KAPPA); // cx+ox
    let cy_oy_m = fmsub(cy, ry, KAPPA); // cy-oy
    z.move_to(cx - rx, cy);
    z.cube_to(cx - rx, cy_oy_p, cx_ox_m, cy + ry, cx, cy + ry);
    z.cube_to(cx_ox_p, cy + ry, cx + rx, cy_oy_p, cx + rx, cy);
    z.cube_to(cx + rx, cy_oy_m, cx_ox_p, cy - ry, cx, cy - ry);
    z.cube_to(cx_ox_m, cy - ry, cx - rx, cy_oy_m, cx - rx, cy);
    z.close_path();
}

struct Canvas {
    img: Vec<u8>,
    z: Rasterizer,
    xf: Xf,
}

impl Canvas {
    fn fill_with(&mut self, col: Premul, build: impl FnOnce(&mut Rasterizer, &Xf)) {
        self.z.begin();
        build(&mut self.z, &self.xf);
        self.z.draw(&mut self.img, col);
    }

    fn primitive(&mut self, p: &Primitive) {
        match p {
            Primitive::Rect(el) => self.rect(el),
            Primitive::Circle(el) => {
                let (cx, cy, r) = (self.xf.tx(el.cx), self.xf.ty(el.cy), self.xf.ts(el.r));
                if is_paint(&el.fill)
                    && let Some(c) = parse_color(&el.fill, el.opacity)
                {
                    self.fill_with(c, |z, _| add_ellipse_path(z, cx, cy, r, r));
                }
                if is_paint(&el.stroke)
                    && el.stroke_width > 0.0
                    && let Some(c) = parse_color(&el.stroke, el.opacity)
                {
                    let sw = self.xf.ts(el.stroke_width);
                    self.fill_with(c, |z, _| {
                        add_ellipse_path(z, cx, cy, r + sw / 2.0, r + sw / 2.0);
                        if r > sw / 2.0 {
                            add_ellipse_path_ccw(z, cx, cy, r - sw / 2.0, r - sw / 2.0);
                        }
                    });
                }
            }
            Primitive::Ellipse(el) => {
                let (cx, cy) = (self.xf.tx(el.cx), self.xf.ty(el.cy));
                let (rx, ry) = (self.xf.ts(el.rx), self.xf.ts(el.ry));
                if is_paint(&el.fill)
                    && let Some(c) = parse_color(&el.fill, el.opacity)
                {
                    self.fill_with(c, |z, _| add_ellipse_path(z, cx, cy, rx, ry));
                }
                if is_paint(&el.stroke)
                    && el.stroke_width > 0.0
                    && let Some(c) = parse_color(&el.stroke, el.opacity)
                {
                    let sw = self.xf.ts(el.stroke_width);
                    self.fill_with(c, |z, _| {
                        add_ellipse_path(z, cx, cy, rx + sw / 2.0, ry + sw / 2.0);
                        if rx > sw / 2.0 && ry > sw / 2.0 {
                            add_ellipse_path_ccw(z, cx, cy, rx - sw / 2.0, ry - sw / 2.0);
                        }
                    });
                }
            }
            Primitive::Line(el) => {
                self.line(
                    el.x1,
                    el.y1,
                    el.x2,
                    el.y2,
                    &el.stroke,
                    el.stroke_width,
                    el.opacity,
                );
            }
            Primitive::Polyline(el) => {
                let pts = el.points.as_deref().unwrap_or_default();
                if pts.len() < 2 {
                    return;
                }
                if is_paint(&el.stroke) && el.stroke_width > 0.0 {
                    for i in 0..pts.len() - 1 {
                        self.line(
                            pts[i].x,
                            pts[i].y,
                            pts[i + 1].x,
                            pts[i + 1].y,
                            &el.stroke,
                            el.stroke_width,
                            el.opacity,
                        );
                    }
                }
            }
            Primitive::Polygon(el) => {
                let pts = el.points.as_deref().unwrap_or_default();
                if pts.len() < 3 {
                    return;
                }
                if is_paint(&el.fill)
                    && let Some(c) = parse_color(&el.fill, el.opacity)
                {
                    self.fill_with(c, |z, xf| {
                        z.move_to(xf.tx(pts[0].x), xf.ty(pts[0].y));
                        for pt in &pts[1..] {
                            z.line_to(xf.tx(pt.x), xf.ty(pt.y));
                        }
                        z.close_path();
                    });
                }
                if is_paint(&el.stroke) && el.stroke_width > 0.0 {
                    for i in 0..pts.len() {
                        let next = (i + 1) % pts.len();
                        self.line(
                            pts[i].x,
                            pts[i].y,
                            pts[next].x,
                            pts[next].y,
                            &el.stroke,
                            el.stroke_width,
                            el.opacity,
                        );
                    }
                }
            }
            Primitive::Path(el) => {
                if el.segments.is_empty() {
                    return;
                }
                if is_paint(&el.fill)
                    && let Some(c) = parse_color(&el.fill, el.opacity)
                {
                    self.fill_with(c, |z, xf| trace_segments(z, xf, &el.segments));
                }
            }
            Primitive::Text(el) => self.text(el),
            Primitive::Group(el) => {
                for child in el.children.as_deref().unwrap_or_default() {
                    self.primitive(child);
                }
            }
        }
    }

    fn rect(&mut self, el: &RectElement) {
        let x = self.xf.tx(el.x);
        let y = self.xf.ty(el.y);
        let rw = self.xf.ts(el.width);
        let rh = self.xf.ts(el.height);
        let rx = self.xf.ts(el.rx);
        let ry = self.xf.ts(el.ry);
        if is_paint(&el.fill)
            && let Some(c) = parse_color(&el.fill, el.opacity)
        {
            self.fill_with(c, |z, _| add_round_rect_path(z, x, y, rw, rh, rx, ry));
        }
        if is_paint(&el.stroke)
            && el.stroke_width > 0.0
            && let Some(c) = parse_color(&el.stroke, el.opacity)
        {
            let sw = self.xf.ts(el.stroke_width);
            self.fill_with(c, |z, _| {
                add_round_rect_path(
                    z,
                    x - sw / 2.0,
                    y - sw / 2.0,
                    rw + sw,
                    rh + sw,
                    rx + sw / 2.0,
                    ry + sw / 2.0,
                );
                if rw > sw && rh > sw {
                    add_round_rect_path_ccw(
                        z,
                        x + sw / 2.0,
                        y + sw / 2.0,
                        rw - sw,
                        rh - sw,
                        go_max(0.0, f64::from(rx - sw / 2.0)),
                        go_max(0.0, f64::from(ry - sw / 2.0)),
                    );
                }
            });
        }
    }

    #[allow(clippy::too_many_arguments)]
    fn line(&mut self, ax: f64, ay: f64, bx: f64, by: f64, stroke: &str, width: f64, opacity: f64) {
        if !is_paint(stroke) || width <= 0.0 {
            return;
        }
        let Some(col) = parse_color(stroke, opacity) else {
            return;
        };
        let (x1, y1) = (self.xf.tx(ax), self.xf.ty(ay));
        let (x2, y2) = (self.xf.tx(bx), self.xf.ty(by));
        let sw = self.xf.ts(width);
        let dx = x2 - x1;
        let dy = y2 - y1;
        let len_sq = fmadd(dx * dx, dy, dy);
        if len_sq <= 0.0001 {
            return;
        }
        let length = f64::from(len_sq).sqrt() as f32;
        let na = -dy / length;
        let nb = sw / 2.0;
        let ma = dx / length;
        self.fill_with(col, |z, _| {
            z.move_to(fmadd(x1, na, nb), fmadd(y1, ma, nb));
            z.line_to(fmadd(x2, na, nb), fmadd(y2, ma, nb));
            z.line_to(fmsub(x2, na, nb), fmsub(y2, ma, nb));
            z.line_to(fmsub(x1, na, nb), fmsub(y1, ma, nb));
            z.close_path();
        });
    }

    fn text(&mut self, el: &TextElement) {
        let font_size = if el.font_size <= 0.0 {
            14.0
        } else {
            el.font_size
        };
        let fill = if el.fill.is_empty() {
            "#F5F4F0"
        } else {
            el.fill.as_str()
        };
        let Some(col) = parse_color(fill, el.opacity) else {
            return;
        };
        let m = default_measurer();
        let weight = FontWeight::from_go(&el.font_weight);
        let mut text_len = el.text_length;
        if text_len <= 0.0 {
            text_len = m.measure_text(&el.text, font_size, weight).0;
        }
        let start_x = match el.anchor.as_str() {
            "middle" => el.x - text_len / 2.0,
            "end" => el.x - text_len,
            _ => el.x,
        };
        let (line_height, descent, ascent) = match m.measure_text_detailed("Ag", font_size, weight)
        {
            Ok(t) => (t.line_height, t.descent, t.ascent),
            Err(_) => (0.0, 0.0, 0.0),
        };
        let start_y = match el.baseline.as_str() {
            "middle" | "central" => el.y + line_height / 2.0 - descent,
            "hanging" | "top" => el.y + ascent,
            _ => el.y,
        };
        let segs = m.outlines(&el.text, font_size, weight, start_x, start_y);
        if segs.is_empty() {
            return;
        }
        self.fill_with(col, |z, xf| trace_segments(z, xf, &segs));
    }
}

fn trace_segments(z: &mut Rasterizer, xf: &Xf, segments: &[PathSegment]) {
    for s in segments {
        let a = |i: usize| (xf.tx(s.args[i].x), xf.ty(s.args[i].y));
        match s.op.as_str() {
            "M" => {
                let (x, y) = a(0);
                z.move_to(x, y);
            }
            "L" => {
                let (x, y) = a(0);
                z.line_to(x, y);
            }
            "Q" => {
                let ((bx, by), (cx, cy)) = (a(0), a(1));
                z.quad_to(bx, by, cx, cy);
            }
            "C" => {
                let ((bx, by), (cx, cy), (dx, dy)) = (a(0), a(1), a(2));
                z.cube_to(bx, by, cx, cy, dx, dy);
            }
            "Z" => z.close_path(),
            _ => {}
        }
    }
}

/// The raster dimensions Go computes before allocating: `ceil(size*scale)`
/// with the 100-pixel fallback for non-positive values.
pub fn raster_dimensions(sc: &Scene, opts: &RasterOptions) -> (i64, i64) {
    let scale = if opts.scale <= 0.0 { 1.0 } else { opts.scale };
    let mut w = (sc.width * scale).ceil() as i64;
    let mut h = (sc.height * scale).ceil() as i64;
    if w <= 0 {
        w = 100;
    }
    if h <= 0 {
        h = 100;
    }
    (w, h)
}

/// Go `emit.Rasterize`.
pub fn rasterize(sc: Option<&Scene>, opts: &RasterOptions) -> Result<Rgba, String> {
    let sc = sc.ok_or_else(|| "scene is nil".to_owned())?;
    let (img_w, img_h) = raster_dimensions(sc, opts);
    let (w, h) = (img_w as usize, img_h as usize);
    let mut img = vec![0u8; w * h * 4];

    if !sc.background.is_empty()
        && sc.background != "none"
        && sc.background != "transparent"
        && let Some((sr, sg, sb, sa)) = parse_color(&sc.background, 1.0)
    {
        let px = [
            (sr >> 8) as u8,
            (sg >> 8) as u8,
            (sb >> 8) as u8,
            (sa >> 8) as u8,
        ];
        for chunk in img.chunks_exact_mut(4) {
            chunk.copy_from_slice(&px);
        }
    }

    let mut vb = sc.view_box;
    if vb.width <= 0.0 {
        vb.width = sc.width;
    }
    if vb.height <= 0.0 {
        vb.height = sc.height;
    }
    let xf = Xf {
        vb_x: vb.x,
        vb_y: vb.y,
        scale_x: img_w as f64 / vb.width,
        scale_y: img_h as f64 / vb.height,
    };
    let mut canvas = Canvas {
        img,
        z: Rasterizer::new(w, h),
        xf,
    };
    for p in &sc.primitives {
        canvas.primitive(p);
    }
    Ok(Rgba {
        width: w,
        height: h,
        pix: canvas.img,
    })
}

// ---------------------------------------------------------------------------
// image/png encoder
// ---------------------------------------------------------------------------
const FT_NONE: u8 = 0;
const FT_SUB: u8 = 1;
const FT_UP: u8 = 2;
const FT_AVERAGE: u8 = 3;
const FT_PAETH: u8 = 4;

fn abs8(d: u8) -> i64 {
    if d < 128 {
        i64::from(d)
    } else {
        256 - i64::from(d)
    }
}

fn paeth(a: u8, b: u8, c: u8) -> u8 {
    let pc = i64::from(c);
    let mut pa = i64::from(b) - pc;
    let mut pb = i64::from(a) - pc;
    let pcc = (pa + pb).abs();
    pa = pa.abs();
    pb = pb.abs();
    if pa <= pb && pa <= pcc {
        a
    } else if pb <= pcc {
        b
    } else {
        c
    }
}

/// Go `png.filter`: tries up, paeth, none, sub, average in that order and
/// returns the filter index whose row (in `cr[f]`) is to be written.
fn filter(cr: &mut [Vec<u8>; 5], pr: &[u8], bpp: usize) -> usize {
    let n = cr[0].len() - 1;
    let pdat = &pr[1..];
    let (c0, rest) = cr.split_at_mut(1);
    let cdat0 = &c0[0][1..];
    let (c1, rest) = rest.split_at_mut(1);
    let (c2, rest) = rest.split_at_mut(1);
    let (c3, c4) = rest.split_at_mut(1);
    let cdat1 = &mut c1[0][1..];
    let cdat2 = &mut c2[0][1..];
    let cdat3 = &mut c3[0][1..];
    let cdat4 = &mut c4[0][1..];

    // Up.
    let mut sum: i64 = 0;
    for i in 0..n {
        cdat2[i] = cdat0[i].wrapping_sub(pdat[i]);
        sum += abs8(cdat2[i]);
    }
    let mut best = sum;
    let mut filter = FT_UP;

    // Paeth.
    sum = 0;
    for i in 0..bpp {
        cdat4[i] = cdat0[i].wrapping_sub(pdat[i]);
        sum += abs8(cdat4[i]);
    }
    for i in bpp..n {
        cdat4[i] = cdat0[i].wrapping_sub(paeth(cdat0[i - bpp], pdat[i], pdat[i - bpp]));
        sum += abs8(cdat4[i]);
        if sum >= best {
            break;
        }
    }
    if sum < best {
        best = sum;
        filter = FT_PAETH;
    }

    // None.
    sum = 0;
    for &v in cdat0.iter().take(n) {
        sum += abs8(v);
        if sum >= best {
            break;
        }
    }
    if sum < best {
        best = sum;
        filter = FT_NONE;
    }

    // Sub.
    sum = 0;
    for i in 0..bpp {
        cdat1[i] = cdat0[i];
        sum += abs8(cdat1[i]);
    }
    for i in bpp..n {
        cdat1[i] = cdat0[i].wrapping_sub(cdat0[i - bpp]);
        sum += abs8(cdat1[i]);
        if sum >= best {
            break;
        }
    }
    if sum < best {
        best = sum;
        filter = FT_SUB;
    }

    // Average.
    sum = 0;
    for i in 0..bpp {
        cdat3[i] = cdat0[i].wrapping_sub(pdat[i] / 2);
        sum += abs8(cdat3[i]);
    }
    for i in bpp..n {
        cdat3[i] =
            cdat0[i].wrapping_sub(((u32::from(cdat0[i - bpp]) + u32::from(pdat[i])) / 2) as u8);
        sum += abs8(cdat3[i]);
        if sum >= best {
            break;
        }
    }
    if sum < best {
        filter = FT_AVERAGE;
    }
    usize::from(filter)
}

/// Go `png.Encoder{CompressionLevel: png.DefaultCompression}.Encode` for an
/// `*image.RGBA`.
pub fn encode_png(img: &Rgba) -> Result<Vec<u8>, String> {
    let (w, h) = (img.width, img.height);
    if w == 0 || h == 0 {
        return Err("encode png: invalid image size".to_owned());
    }
    let opaque = img.pix.chunks_exact(4).all(|p| p[3] == 0xff);
    let (color_type, bpp) = if opaque { (2u8, 3usize) } else { (6u8, 4usize) };

    let mut out: Vec<u8> = b"\x89PNG\r\n\x1a\n".to_vec();
    let mut ihdr = [0u8; 13];
    ihdr[0..4].copy_from_slice(&(w as u32).to_be_bytes());
    ihdr[4..8].copy_from_slice(&(h as u32).to_be_bytes());
    ihdr[8] = 8;
    ihdr[9] = color_type;
    write_chunk(&mut out, b"IHDR", &ihdr);

    let mut zw = ZlibWriter::new(out);
    let sz = 1 + bpp * w;
    let mut cr: [Vec<u8>; 5] = std::array::from_fn(|i| {
        let mut v = vec![0u8; sz];
        v[0] = i as u8;
        v
    });
    let mut pr = vec![0u8; sz];
    for y in 0..h {
        let row = &img.pix[y * w * 4..(y + 1) * w * 4];
        let dst = &mut cr[0][1..];
        if opaque {
            for (d, s) in dst.chunks_exact_mut(3).zip(row.chunks_exact(4)) {
                d.copy_from_slice(&s[..3]);
            }
        } else {
            for (d, s) in dst.chunks_exact_mut(4).zip(row.chunks_exact(4)) {
                if s[3] == 0x00 {
                    d.copy_from_slice(&[0, 0, 0, 0]);
                } else if s[3] == 0xff {
                    d.copy_from_slice(s);
                } else {
                    const M: u32 = 0x101 * 0xffff;
                    let a = u32::from(s[3]) * 0x101;
                    d[0] = ((u32::from(s[0]) * M / a) >> 8) as u8;
                    d[1] = ((u32::from(s[1]) * M / a) >> 8) as u8;
                    d[2] = ((u32::from(s[2]) * M / a) >> 8) as u8;
                    d[3] = s[3];
                }
            }
        }
        let f = filter(&mut cr, &pr, bpp);
        zw.write(&cr[f]);
        std::mem::swap(&mut pr, &mut cr[0]);
    }
    let mut out = zw.close();
    write_chunk(&mut out, b"IEND", &[]);
    Ok(out)
}

/// Go `emit.EmitPNG`: deterministic PNG bytes for the scene.
pub fn emit_png(sc: Option<&Scene>, opts: &RasterOptions) -> Result<Vec<u8>, String> {
    let img = rasterize(sc, opts)?;
    encode_png(&img)
}
