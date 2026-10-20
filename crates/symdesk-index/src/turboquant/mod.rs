//! Local port of the pinned CoreKit `vectorkit/turboquant` codec.
//!
//! The rotation (Go `math/rand` sign flips plus a normalized Walsh-Hadamard
//! transform), uniform min/max scalar quantization, LSB-first bit packing,
//! 32-byte codec metadata and the sidecar blob/JSON layout follow the Go
//! package operation for operation so persisted bytes stay interchangeable.
//!
//! Floating-point note: Go may fuse `a*b+c` into FMA on arm64 in decode and
//! scoring. Rust never fuses, which matches Go on amd64. Encode has no fusable
//! expression, so packed codes are identical on every architecture.
//!
//! Intentional differences: Go `Score`/`ScoreCosine` panic on a packed code
//! shorter than the codec needs; Rust returns `0.0` instead. Go nil metadata
//! pointers have no Rust equivalent.

mod go_rand_cooked;
pub mod sidecar_corpus;

use serde::Deserialize;
use thiserror::Error;

use crate::retrieval_quant_sidecar::RetrievalQuantSidecar;

/// Current sidecar codec wire version (Go `CodecVersion`).
pub const CODEC_VERSION: i64 = 1;
const METADATA_VERSION: i64 = 1;

/// Error with the exact Go error string.
#[derive(Clone, Debug, Eq, Error, PartialEq)]
#[error("{0}")]
pub struct TurboQuantError(String);

fn err(message: String) -> TurboQuantError {
    TurboQuantError(message)
}

/// Go `math/rand` source (`rngSource`), reproduced for `Intn(2)` sign draws.
struct GoRand {
    vec: [i64; 607],
    tap: usize,
    feed: usize,
}

impl GoRand {
    fn new(seed: i64) -> Self {
        const INT32_MAX: i64 = (1 << 31) - 1;
        let mut seed = seed % INT32_MAX;
        if seed < 0 {
            seed += INT32_MAX;
        }
        if seed == 0 {
            seed = 89_482_311;
        }
        let mut x = seed as i32;
        let mut vec = [0_i64; 607];
        for i in -20_i32..607 {
            x = seedrand(x);
            if i >= 0 {
                let mut u = i64::from(x) << 40;
                x = seedrand(x);
                u ^= i64::from(x) << 20;
                x = seedrand(x);
                u ^= i64::from(x);
                u ^= go_rand_cooked::RNG_COOKED[i as usize];
                vec[i as usize] = u;
            }
        }
        Self {
            vec,
            tap: 0,
            feed: 607 - 273,
        }
    }

    fn int63(&mut self) -> i64 {
        self.tap = self.tap.checked_sub(1).unwrap_or(606);
        self.feed = self.feed.checked_sub(1).unwrap_or(606);
        let x = self.vec[self.feed].wrapping_add(self.vec[self.tap]);
        self.vec[self.feed] = x;
        x & i64::MAX
    }

    /// Go `Intn(2)`: power-of-two path of `Int31n`, i.e. `Int31() & 1`.
    fn intn2(&mut self) -> i64 {
        (self.int63() >> 32) & 1
    }
}

fn seedrand(x: i32) -> i32 {
    const A: i32 = 48_271;
    const Q: i32 = 44_488;
    const R: i32 = 3_399;
    let mut x = A * (x % Q) - R * (x / Q);
    if x < 0 {
        x += i32::MAX;
    }
    x
}

/// In-place Walsh-Hadamard transform; non-power-of-two lengths are a no-op.
pub fn hadamard_transform(vec: &mut [f64]) {
    let n = vec.len();
    if n == 0 || n & (n - 1) != 0 {
        return;
    }
    let mut step = 1;
    while step < n {
        for i in (0..n).step_by(step << 1) {
            for j in i..i + step {
                let (u, v) = (vec[j], vec[j + step]);
                vec[j] = u + v;
                vec[j + step] = u - v;
            }
        }
        step <<= 1;
    }
}

fn next_power_of_2(n: usize) -> usize {
    n.max(1).next_power_of_two()
}

/// Deterministic random rotation `H * D / sqrt(n)` (Go `RandomRotation`).
#[derive(Clone, Debug)]
pub struct RandomRotation {
    signs: Vec<f64>,
}

impl RandomRotation {
    /// Go `NewRandomRotation(dim, seed)`.
    #[must_use]
    pub fn new(dim: usize, seed: i64) -> Self {
        let mut rng = GoRand::new(seed);
        let signs = (0..next_power_of_2(dim))
            .map(|_| if rng.intn2() == 0 { 1.0 } else { -1.0 })
            .collect();
        Self { signs }
    }

    /// Padded (power-of-two) output dimension.
    #[must_use]
    pub fn padded(&self) -> usize {
        self.signs.len()
    }

    /// Go `ApplyRotation`; `output` must hold at least `padded()` values.
    pub fn apply(&self, input: &[f64], output: &mut [f64]) {
        let n = self.padded();
        for i in 0..n {
            output[i] = if i < input.len() {
                input[i] * self.signs[i]
            } else {
                0.0
            };
        }
        self.finish(&mut output[..n], false);
    }

    /// Go `InverseApplyRotation`; `input` and `output` hold `padded()` values.
    pub fn inverse(&self, input: &[f64], output: &mut [f64]) {
        let n = self.padded();
        output[..n].copy_from_slice(&input[..n]);
        self.finish(&mut output[..n], true);
    }

    fn finish(&self, out: &mut [f64], signs_after: bool) {
        hadamard_transform(out);
        if signs_after {
            for (v, s) in out.iter_mut().zip(&self.signs) {
                *v *= s;
            }
        }
        let factor = 1.0 / (out.len() as f64).sqrt();
        for v in out {
            *v *= factor;
        }
    }
}

/// Packed code for one vector (Go `PackedCode`).
#[derive(Clone, Debug, PartialEq)]
pub struct PackedCode {
    pub bytes: Vec<u8>,
    pub min: f64,
    pub max: f64,
}

/// Deserialized 32-byte codec header (Go `Metadata`).
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct Metadata {
    pub version: i64,
    pub dim: i64,
    pub bit_width: i64,
    pub seed: i64,
    pub block_size: i64,
    pub rot_dim: i64,
}

/// TurboQuant codec (Go `Codec`).
#[derive(Clone, Debug)]
pub struct Codec {
    dim: usize,
    bit_width: i64,
    seed: i64,
    block_size: i64,
    rot_dim: usize,
    rotation: RandomRotation,
    /// Bits for channels `< rot_dim / 2` and for the rest; equal unless split.
    low_bits: usize,
    high_bits: usize,
}

impl Codec {
    /// Go `NewCodec(dim, BitWidth(bw), seed, blockSize)`.
    pub fn new(
        dim: i64,
        bit_width: i64,
        seed: i64,
        block_size: i64,
    ) -> Result<Self, TurboQuantError> {
        if dim <= 0 {
            return Err(err(format!("turboquant: dimension mismatch: dim={dim}")));
        }
        let (low_bits, high_bits) = match bit_width {
            2 => (2, 2),
            3 => (3, 3),
            4 => (4, 4),
            25 => (2, 3),
            35 => (3, 4),
            _ => {
                return Err(err(format!(
                    "turboquant: unsupported bit width: {bit_width}"
                )));
            }
        };
        let dim = dim as usize;
        let rotation = RandomRotation::new(dim, seed);
        Ok(Self {
            dim,
            bit_width,
            seed,
            block_size,
            rot_dim: rotation.padded(),
            rotation,
            low_bits,
            high_bits,
        })
    }

    #[must_use]
    pub fn dim(&self) -> usize {
        self.dim
    }

    #[must_use]
    pub fn rot_dim(&self) -> usize {
        self.rot_dim
    }

    fn bits(&self, channel: usize) -> usize {
        if channel < self.rot_dim / 2 {
            self.low_bits
        } else {
            self.high_bits
        }
    }

    /// Go `packedBytesLen`.
    #[must_use]
    pub fn packed_len(&self) -> usize {
        let half = self.rot_dim / 2;
        (half * self.low_bits + (self.rot_dim - half) * self.high_bits).div_ceil(8)
    }

    fn rotate(&self, vec: &[f32]) -> Vec<f64> {
        let floats: Vec<f64> = vec.iter().map(|&v| f64::from(v)).collect();
        let mut rotated = vec![0.0; self.rot_dim];
        self.rotation.apply(&floats, &mut rotated);
        rotated
    }

    /// Bin midpoints in `[0, 1]` for every channel of `code`.
    fn midpoints<'a>(&'a self, code: &'a [u8]) -> impl Iterator<Item = f64> + 'a {
        let mut offset = 0;
        (0..self.rot_dim).map(move |i| {
            let bits = self.bits(i);
            let idx = unpack_bits(code, offset, bits);
            offset += bits;
            (idx as f64 + 0.5) / (1_u32 << bits) as f64
        })
    }

    /// Go `Encode`.
    pub fn encode(&self, vec: &[f32]) -> Result<PackedCode, TurboQuantError> {
        if vec.len() != self.dim {
            return Err(err(format!(
                "turboquant: dimension mismatch: got {}, expected {}",
                vec.len(),
                self.dim
            )));
        }
        let rotated = self.rotate(vec);
        let (mut min, mut max) = (f64::MAX, -f64::MAX);
        for &v in &rotated {
            if v < min {
                min = v;
            }
            if v > max {
                max = v;
            }
        }
        let mut range = max - min;
        if range < 1e-10 {
            range = 1e-10;
        }
        let mut bytes = vec![0_u8; self.packed_len()];
        let mut offset = 0;
        for (i, &v) in rotated.iter().enumerate() {
            // NaN passes through like Go's two comparisons.
            let normalized = ((v - min) / range).clamp(0.0, 1.0);
            let bits = self.bits(i);
            let levels = 1_i64 << bits;
            // Go int(NaN) is 0 on arm64 and MinInt64 (clamped to 0) on amd64;
            // Rust `as` yields 0 too.
            let idx = ((normalized * levels as f64) as i64).clamp(0, levels - 1);
            pack_bits(&mut bytes, offset, idx as u32, bits);
            offset += bits;
        }
        Ok(PackedCode { bytes, min, max })
    }

    /// Go `Decode`.
    pub fn decode(&self, code: &PackedCode) -> Result<Vec<f32>, TurboQuantError> {
        let need = self.packed_len();
        if code.bytes.len() < need {
            return Err(err(format!(
                "turboquant: code bytes too short: got {}, need {need}",
                code.bytes.len()
            )));
        }
        let span = code.max - code.min;
        let rotated: Vec<f64> = self
            .midpoints(&code.bytes)
            .map(|m| code.min + span * m)
            .collect();
        let mut result = vec![0.0; self.rot_dim];
        self.rotation.inverse(&rotated, &mut result);
        Ok(result[..self.dim].iter().map(|&v| v as f32).collect())
    }

    /// Go `Score`: estimated inner product. Returns 0 on a dimension mismatch
    /// (as Go) or a short code (Go panics).
    #[must_use]
    pub fn score(&self, query: &[f32], code: &PackedCode) -> f32 {
        if query.len() != self.dim || code.bytes.len() < self.packed_len() {
            return 0.0;
        }
        let rotated = self.rotate(query);
        let (mut dot, mut sum_q) = (0.0, 0.0);
        for (r, m) in rotated.iter().zip(self.midpoints(&code.bytes)) {
            dot += r * m;
            sum_q += r;
        }
        (sum_q * code.min + (code.max - code.min) * dot) as f32
    }

    /// Go `ScoreCosine`.
    #[must_use]
    pub fn score_cosine(&self, query: &[f32], code: &PackedCode) -> f32 {
        if code.bytes.len() < self.packed_len() {
            return 0.0;
        }
        let ip = self.score(query, code);
        let q_norm = query
            .iter()
            .map(|&v| f64::from(v) * f64::from(v))
            .sum::<f64>()
            .sqrt();
        if q_norm < 1e-10 {
            return 0.0;
        }
        let span = code.max - code.min;
        let code_norm = self
            .midpoints(&code.bytes)
            .map(|m| {
                let v = code.min + span * m;
                v * v
            })
            .sum::<f64>()
            .sqrt();
        if code_norm < 1e-10 {
            return 0.0;
        }
        (f64::from(ip) / (q_norm * code_norm)) as f32
    }

    /// Go `MarshalMetadata`: six little-endian `u32` fields padded to 32 bytes.
    #[must_use]
    pub fn marshal_metadata(&self) -> [u8; 32] {
        let mut buf = [0_u8; 32];
        let fields = [
            METADATA_VERSION,
            self.dim as i64,
            self.bit_width,
            self.seed,
            self.block_size,
            self.rot_dim as i64,
        ];
        for (chunk, field) in buf.as_chunks_mut::<4>().0.iter_mut().zip(fields) {
            chunk.copy_from_slice(&(field as u32).to_le_bytes());
        }
        buf
    }

    /// Go `CodecFromMetadata`.
    pub fn from_metadata(meta: &Metadata) -> Result<Self, TurboQuantError> {
        Self::new(meta.dim, meta.bit_width, meta.seed, meta.block_size)
    }

    /// Go `EncodeSidecar`: `f32` min/max header plus packed code, and metadata.
    pub fn encode_sidecar(
        &self,
        vec: &[f32],
        norm: f32,
    ) -> Result<(Vec<u8>, SidecarMeta), TurboQuantError> {
        let code = self.encode(vec)?;
        let blob = RetrievalQuantSidecar {
            min: code.min as f32,
            max: code.max as f32,
            packed: code.bytes,
        }
        .to_blob();
        let meta = SidecarMeta {
            codec_version: CODEC_VERSION,
            dimension: self.dim as i64,
            bit_width: self.bit_width,
            quantizer_mode: "scalar".to_owned(),
            projection_seed: self.seed,
            norm,
        };
        Ok((blob, meta))
    }

    /// Go `CodecFromSidecarMeta`.
    pub fn from_sidecar_meta(meta: &SidecarMeta) -> Result<Self, TurboQuantError> {
        if meta.codec_version != CODEC_VERSION {
            return Err(err(format!(
                "turboquant: sidecar codec version mismatch: codec_version={}, want {CODEC_VERSION}",
                meta.codec_version
            )));
        }
        if meta.dimension <= 0 {
            return Err(err(format!(
                "turboquant: dimension mismatch: dimension={}",
                meta.dimension
            )));
        }
        if meta.bit_width <= 0 {
            return Err(err(format!(
                "turboquant: unsupported bit width: bit_width={}",
                meta.bit_width
            )));
        }
        Self::new(meta.dimension, meta.bit_width, meta.projection_seed, 0)
    }

    /// Go `ValidateSidecarMeta`.
    pub fn validate_sidecar_meta(&self, meta: &SidecarMeta) -> Result<(), TurboQuantError> {
        if meta.codec_version != CODEC_VERSION {
            return Err(err(format!(
                "turboquant: sidecar codec version mismatch: codec_version={}, want {CODEC_VERSION}",
                meta.codec_version
            )));
        }
        if meta.dimension != self.dim as i64 {
            return Err(err(format!(
                "turboquant: dimension mismatch: meta dimension={}, codec dimension={}",
                meta.dimension, self.dim
            )));
        }
        if meta.bit_width != self.bit_width {
            return Err(err(format!(
                "turboquant: unsupported bit width: meta bit_width={}, codec bit_width={}",
                meta.bit_width, self.bit_width
            )));
        }
        if meta.projection_seed != self.seed {
            return Err(err(format!(
                "turboquant: sidecar seed mismatch: meta projection_seed={}, codec seed={}",
                meta.projection_seed, self.seed
            )));
        }
        Ok(())
    }
}

/// Go `UnmarshalMetadata`.
pub fn unmarshal_metadata(data: &[u8]) -> Result<Metadata, TurboQuantError> {
    if data.len() < 24 {
        return Err(err(format!(
            "turboquant: metadata bytes too short: need 24, got {}",
            data.len()
        )));
    }
    let field = |i: usize| {
        i64::from(u32::from_le_bytes([
            data[i],
            data[i + 1],
            data[i + 2],
            data[i + 3],
        ]))
    };
    let meta = Metadata {
        version: field(0),
        dim: field(4),
        bit_width: field(8),
        seed: field(12),
        block_size: field(16),
        rot_dim: field(20),
    };
    if meta.version != METADATA_VERSION {
        return Err(err(format!(
            "turboquant: unsupported metadata version: got version {}, expected {METADATA_VERSION}",
            meta.version
        )));
    }
    Ok(meta)
}

/// Go `UnpackSidecarBlob`, widening the `f32` header to `f64` like Go.
pub fn unpack_sidecar_blob(blob: &[u8]) -> Result<PackedCode, TurboQuantError> {
    let sidecar = RetrievalQuantSidecar::read_blob(blob).map_err(|e| err(e.to_string()))?;
    Ok(PackedCode {
        bytes: sidecar.packed,
        min: f64::from(sidecar.min),
        max: f64::from(sidecar.max),
    })
}

/// Go `DecodeSidecar`.
pub fn decode_sidecar(blob: &[u8], meta: &SidecarMeta) -> Result<Vec<f32>, TurboQuantError> {
    let codec = Codec::from_sidecar_meta(meta)?;
    codec.decode(&unpack_sidecar_blob(blob)?)
}

/// JSON sidecar metadata (Go `SidecarMeta`, same as Desktop `QuantSidecarMeta`).
#[derive(Clone, Debug, Default, Deserialize, PartialEq)]
#[serde(default)]
pub struct SidecarMeta {
    pub codec_version: i64,
    pub dimension: i64,
    pub bit_width: i64,
    pub quantizer_mode: String,
    pub projection_seed: i64,
    pub norm: f32,
}

impl SidecarMeta {
    /// Go `json.Marshal` bytes for this struct, including `omitempty` and
    /// Go's `float32` number formatting. Non-finite norms fail like Go.
    pub fn to_json(&self) -> Result<String, TurboQuantError> {
        let mode = serde_json::to_string(&self.quantizer_mode)
            .map_err(|e| err(e.to_string()))?
            .replace('<', "\\u003c")
            .replace('>', "\\u003e")
            .replace('&', "\\u0026")
            .replace('\u{2028}', "\\u2028")
            .replace('\u{2029}', "\\u2029");
        let mut out = format!(
            "{{\"codec_version\":{},\"dimension\":{},\"bit_width\":{},\"quantizer_mode\":{mode}",
            self.codec_version, self.dimension, self.bit_width
        );
        if self.projection_seed != 0 {
            out += &format!(",\"projection_seed\":{}", self.projection_seed);
        }
        if self.norm != 0.0 {
            out += &format!(",\"norm\":{}", go_json_f32(self.norm)?);
        }
        out.push('}');
        Ok(out)
    }
}

/// Go `encoding/json` float32 formatting: shortest round-trip digits, `%e`
/// style below 1e-6 or from 1e21 with a signed, at-least-one-digit exponent.
fn go_json_f32(v: f32) -> Result<String, TurboQuantError> {
    if !v.is_finite() {
        return Err(err(format!("json: unsupported value: {v}")));
    }
    let abs = v.abs();
    if abs != 0.0 && (abs < 1e-6 || abs >= 1e21) {
        let s = format!("{v:e}");
        Ok(match s.split_once('e') {
            Some((mantissa, exp)) if !exp.starts_with('-') => format!("{mantissa}e+{exp}"),
            _ => s,
        })
    } else {
        Ok(format!("{v}"))
    }
}

fn pack_bits(buf: &mut [u8], offset: usize, val: u32, bits: usize) {
    for b in 0..bits {
        if (val >> b) & 1 == 1 {
            let at = offset + b;
            buf[at / 8] |= 1 << (at % 8);
        }
    }
}

fn unpack_bits(buf: &[u8], offset: usize, bits: usize) -> u32 {
    let mut val = 0;
    for b in 0..bits {
        let at = offset + b;
        if buf[at / 8] & (1 << (at % 8)) != 0 {
            val |= 1 << b;
        }
    }
    val
}
