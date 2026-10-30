//! Byte-exact port of the Go `compress/flate` compressor subset that
//! `image/png` reaches with `png.DefaultCompression`: `zlib.DefaultCompression`
//! maps to flate level 6 (hash chains, lazy matching, dynamic/fixed/stored
//! block selection), plus the `compress/zlib` framing, the `hash/adler32`
//! checksum, the `bufio.Writer(1<<15)` that sits between zlib and the PNG
//! `IDAT` chunk writer, and PNG chunk framing with `hash/crc32`.
//!
//! Go's output depends on the exact sequence of `Write` calls (the window
//! shifts and block boundaries follow the chunking), so the compressor is a
//! literal transliteration of `deflate.go` / `huffman_bit_writer.go` /
//! `huffman_code.go` / `token.go`, driven row by row by the caller.
//!
//! Derived from the Go standard library (BSD-3-Clause, see
//! `LICENSE-x-image`).
// A literal transliteration of the Go sources: index loops are kept as written.
#![allow(clippy::needless_range_loop)]

const WINDOW_SIZE: usize = 1 << 15;
const WINDOW_MASK: usize = WINDOW_SIZE - 1;
const BASE_MATCH_LENGTH: u32 = 3;
const MIN_MATCH_LENGTH: usize = 4;
const MAX_MATCH_LENGTH: usize = 258;
const BASE_MATCH_OFFSET: u32 = 1;
const MAX_FLATE_BLOCK_TOKENS: usize = 1 << 14;
const MAX_STORE_BLOCK_SIZE: usize = 65535;
const HASH_BITS: u32 = 17;
const HASH_SIZE: usize = 1 << HASH_BITS;
const HASH_MASK: usize = HASH_SIZE - 1;
const MAX_HASH_OFFSET: isize = 1 << 24;
const SKIP_NEVER: usize = i32::MAX as usize;
const HASH_MUL: u32 = 0x1e35_a7bd;

// Level 6 (zlib default): good, lazy, nice, chain, fastSkipHashing.
const L6_GOOD: usize = 8;
const L6_LAZY: usize = 16;
const L6_NICE: usize = 128;
const L6_CHAIN: usize = 128;

const OFFSET_CODE_COUNT: usize = 30;
const END_BLOCK_MARKER: usize = 256;
const LENGTH_CODES_START: usize = 257;
const CODEGEN_CODE_COUNT: usize = 19;
const BAD_CODE: u8 = 255;
const BUFFER_FLUSH_SIZE: usize = 240;
const BUFFER_SIZE: usize = BUFFER_FLUSH_SIZE + 8;
const MAX_NUM_LIT: usize = 286;
const MAX_BITS_LIMIT: usize = 16;

const LENGTH_SHIFT: u32 = 22;
const OFFSET_MASK: u32 = (1 << LENGTH_SHIFT) - 1;
const MATCH_TYPE: u32 = 1 << 30;

const LENGTH_EXTRA_BITS: [i8; 29] = [
    0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2, 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0,
];
const LENGTH_BASE: [u32; 29] = [
    0, 1, 2, 3, 4, 5, 6, 7, 8, 10, 12, 14, 16, 20, 24, 28, 32, 40, 48, 56, 64, 80, 96, 112, 128,
    160, 192, 224, 255,
];
const OFFSET_EXTRA_BITS: [i8; 30] = [
    0, 0, 0, 0, 1, 1, 2, 2, 3, 3, 4, 4, 5, 5, 6, 6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13,
    13,
];
const OFFSET_BASE: [u32; 30] = [
    0x000000, 0x000001, 0x000002, 0x000003, 0x000004, 0x000006, 0x000008, 0x00000c, 0x000010,
    0x000018, 0x000020, 0x000030, 0x000040, 0x000060, 0x000080, 0x0000c0, 0x000100, 0x000180,
    0x000200, 0x000300, 0x000400, 0x000600, 0x000800, 0x000c00, 0x001000, 0x001800, 0x002000,
    0x003000, 0x004000, 0x006000,
];
const CODEGEN_ORDER: [usize; 19] = [
    16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15,
];

fn length_codes() -> &'static [u32; 256] {
    use std::sync::OnceLock;
    static T: OnceLock<[u32; 256]> = OnceLock::new();
    T.get_or_init(|| {
        // lengthCodes[length-3]: runs of equal codes, as listed in token.go.
        let runs: [(u32, usize); 29] = [
            (0, 1),
            (1, 1),
            (2, 1),
            (3, 1),
            (4, 1),
            (5, 1),
            (6, 1),
            (7, 1),
            (8, 2),
            (9, 2),
            (10, 2),
            (11, 2),
            (12, 4),
            (13, 4),
            (14, 4),
            (15, 4),
            (16, 8),
            (17, 8),
            (18, 8),
            (19, 8),
            (20, 16),
            (21, 16),
            (22, 16),
            (23, 16),
            (24, 32),
            (25, 32),
            (26, 32),
            (27, 31),
            (28, 1),
        ];
        let mut out = [0u32; 256];
        let mut i = 0;
        for (code, n) in runs {
            for _ in 0..n {
                out[i] = code;
                i += 1;
            }
        }
        debug_assert_eq!(i, 256);
        out
    })
}

fn offset_codes() -> &'static [u32; 256] {
    use std::sync::OnceLock;
    static T: OnceLock<[u32; 256]> = OnceLock::new();
    T.get_or_init(|| {
        // The table in token.go: 0,1,2,3,4,4,5,5,6x4,7x4, then 8 and 9 (8
        // entries each), then 10 and 11 (16 each), 12 and 13 (32 each) and
        // 14 and 15 (64 each).
        let mut out = [0u32; 256];
        let head: [u32; 16] = [0, 1, 2, 3, 4, 4, 5, 5, 6, 6, 6, 6, 7, 7, 7, 7];
        out[..16].copy_from_slice(&head);
        let mut i = 16;
        for (code, n) in [
            (8u32, 8usize),
            (9, 8),
            (10, 16),
            (11, 16),
            (12, 32),
            (13, 32),
            (14, 64),
            (15, 64),
        ] {
            for _ in 0..n {
                out[i] = code;
                i += 1;
            }
        }
        debug_assert_eq!(i, 256);
        out
    })
}

fn length_code(len: u32) -> u32 {
    length_codes()[len as usize]
}

fn offset_code(off: u32) -> u32 {
    let t = offset_codes();
    if (off as usize) < t.len() {
        return t[off as usize];
    }
    if ((off >> 7) as usize) < t.len() {
        return t[(off >> 7) as usize] + 14;
    }
    t[(off >> 14) as usize] + 28
}

fn literal_token(l: u32) -> u32 {
    l
}

fn match_token(xlength: u32, xoffset: u32) -> u32 {
    MATCH_TYPE + (xlength << LENGTH_SHIFT) + xoffset
}

#[derive(Clone, Copy, Default)]
struct HCode {
    code: u16,
    len: u16,
}

#[derive(Clone, Copy)]
struct LiteralNode {
    literal: u16,
    freq: i32,
}

#[derive(Clone, Copy, Default)]
struct LevelInfo {
    level: i32,
    last_freq: i32,
    next_char_freq: i32,
    next_pair_freq: i32,
    needed: i32,
}

struct HuffmanEncoder {
    codes: Vec<HCode>,
}

fn reverse_bits(number: u16, bit_length: u8) -> u16 {
    (number << (16 - u32::from(bit_length))).reverse_bits()
}

impl HuffmanEncoder {
    fn new(size: usize) -> HuffmanEncoder {
        HuffmanEncoder {
            codes: vec![HCode::default(); size],
        }
    }

    fn fixed_literal() -> HuffmanEncoder {
        let mut h = HuffmanEncoder::new(MAX_NUM_LIT);
        for ch in 0..MAX_NUM_LIT as u16 {
            let (bits, size): (u16, u16) = if ch < 144 {
                (ch + 48, 8)
            } else if ch < 256 {
                (ch + 400 - 144, 9)
            } else if ch < 280 {
                (ch - 256, 7)
            } else {
                (ch + 192 - 280, 8)
            };
            h.codes[usize::from(ch)] = HCode {
                code: reverse_bits(bits, size as u8),
                len: size,
            };
        }
        h
    }

    fn fixed_offset() -> HuffmanEncoder {
        let mut h = HuffmanEncoder::new(30);
        for ch in 0..30u16 {
            h.codes[usize::from(ch)] = HCode {
                code: reverse_bits(ch, 5),
                len: 5,
            };
        }
        h
    }

    fn bit_length(&self, freq: &[i32]) -> i32 {
        let mut total: i32 = 0;
        for (i, &f) in freq.iter().enumerate() {
            if f != 0 {
                total += f * i32::from(self.codes[i].len);
            }
        }
        total
    }

    fn bit_counts(list: &mut [LiteralNode], max_bits: i32) -> [i32; 17] {
        assert!(
            (max_bits as usize) < MAX_BITS_LIMIT,
            "flate: maxBits too large"
        );
        let n = (list.len() - 1) as i32; // list carries the sentinel slot
        list[n as usize] = LiteralNode {
            literal: u16::MAX,
            freq: i32::MAX,
        };
        let mut max_bits = max_bits;
        if max_bits > n - 1 {
            max_bits = n - 1;
        }
        let mut levels = [LevelInfo::default(); MAX_BITS_LIMIT + 1];
        let mut leaf_counts = [[0i32; MAX_BITS_LIMIT]; MAX_BITS_LIMIT];
        for level in 1..=max_bits {
            levels[level as usize] = LevelInfo {
                level,
                last_freq: list[1].freq,
                next_char_freq: list[2].freq,
                next_pair_freq: list[0].freq + list[1].freq,
                needed: 0,
            };
            leaf_counts[level as usize][level as usize] = 2;
            if level == 1 {
                levels[level as usize].next_pair_freq = i32::MAX;
            }
        }
        levels[max_bits as usize].needed = 2 * n - 4;
        let mut level = max_bits as usize;
        loop {
            let l = levels[level];
            if l.next_pair_freq == i32::MAX && l.next_char_freq == i32::MAX {
                levels[level].needed = 0;
                levels[level + 1].next_pair_freq = i32::MAX;
                level += 1;
                continue;
            }
            let prev_freq = l.last_freq;
            if l.next_char_freq < l.next_pair_freq {
                let n2 = leaf_counts[level][level] + 1;
                levels[level].last_freq = l.next_char_freq;
                leaf_counts[level][level] = n2;
                levels[level].next_char_freq = list[n2 as usize].freq;
            } else {
                levels[level].last_freq = l.next_pair_freq;
                let (lo, hi) = leaf_counts.split_at_mut(level);
                hi[0][..level].copy_from_slice(&lo[level - 1][..level]);
                levels[level - 1].needed = 2;
            }
            levels[level].needed -= 1;
            if levels[level].needed == 0 {
                if levels[level].level == max_bits {
                    break;
                }
                levels[level + 1].next_pair_freq = prev_freq + levels[level].last_freq;
                level += 1;
            } else {
                while levels[level - 1].needed > 0 {
                    level -= 1;
                }
            }
        }
        assert_eq!(
            leaf_counts[max_bits as usize][max_bits as usize], n,
            "leafCounts[maxBits][maxBits] != n"
        );
        let mut bit_count = [0i32; 17];
        let mut bits = 1usize;
        let counts = leaf_counts[max_bits as usize];
        let mut lv = max_bits as usize;
        while lv > 0 {
            bit_count[bits] = counts[lv] - counts[lv - 1];
            bits += 1;
            lv -= 1;
        }
        bit_count
    }

    fn assign_encoding_and_size(
        &mut self,
        bit_count: &[i32],
        list: &mut [LiteralNode],
        max_bits: usize,
    ) {
        let mut code: u16 = 0;
        let mut end = list.len();
        for (n, &bits) in bit_count.iter().enumerate().take(max_bits + 1) {
            code <<= 1;
            if n == 0 || bits == 0 {
                continue;
            }
            let chunk = &mut list[end - bits as usize..end];
            chunk.sort_by_key(|n| n.literal);
            for node in chunk.iter() {
                self.codes[usize::from(node.literal)] = HCode {
                    code: reverse_bits(code, n as u8),
                    len: n as u16,
                };
                code = code.wrapping_add(1);
            }
            end -= bits as usize;
        }
    }

    fn generate(&mut self, freq: &[i32], max_bits: i32) {
        let mut list: Vec<LiteralNode> = Vec::with_capacity(freq.len() + 1);
        for (i, &f) in freq.iter().enumerate() {
            if f != 0 {
                list.push(LiteralNode {
                    literal: i as u16,
                    freq: f,
                });
            } else {
                self.codes[i].len = 0;
            }
        }
        let count = list.len();
        if count <= 2 {
            for (i, node) in list.iter().enumerate() {
                self.codes[usize::from(node.literal)] = HCode {
                    code: i as u16,
                    len: 1,
                };
            }
            return;
        }
        list.sort_by(|a, b| {
            if a.freq == b.freq {
                a.literal.cmp(&b.literal)
            } else {
                a.freq.cmp(&b.freq)
            }
        });
        list.push(LiteralNode {
            literal: 0,
            freq: 0,
        });
        let bit_count = HuffmanEncoder::bit_counts(&mut list, max_bits);
        // Go's bitCount slice spans the (possibly reduced) maxBits + 1 entries.
        let mb = max_bits.min(count as i32 - 1) as usize;
        list.truncate(count);
        self.assign_encoding_and_size(&bit_count, &mut list, mb);
    }
}

/// Go `bufio.Writer` with a 32 KiB buffer whose `Flush` emits one PNG `IDAT`
/// chunk, appended to `out`.
pub struct IdatWriter {
    buf: Vec<u8>,
    n: usize,
    pub out: Vec<u8>,
}

const BUFIO_SIZE: usize = 1 << 15;

impl IdatWriter {
    pub fn new(out: Vec<u8>) -> IdatWriter {
        IdatWriter {
            buf: vec![0; BUFIO_SIZE],
            n: 0,
            out,
        }
    }

    fn emit_chunk(&mut self, data_start: Option<&[u8]>) {
        // `data_start` is Some for a direct (unbuffered) write, otherwise the
        // buffered bytes are emitted.
        let tmp;
        let data: &[u8] = match data_start {
            Some(d) => d,
            None => {
                tmp = self.buf[..self.n].to_vec();
                &tmp
            }
        };
        write_chunk(&mut self.out, b"IDAT", data);
    }

    fn flush(&mut self) {
        if self.n == 0 {
            return;
        }
        self.emit_chunk(None);
        self.n = 0;
    }

    /// Go `bufio.Writer.Write`.
    pub fn write(&mut self, mut p: &[u8]) {
        while p.len() > BUFIO_SIZE - self.n {
            let n;
            if self.n == 0 {
                // Large write, empty buffer: write directly (one IDAT chunk).
                self.emit_chunk(Some(p));
                n = p.len();
            } else {
                n = BUFIO_SIZE - self.n;
                self.buf[self.n..self.n + n].copy_from_slice(&p[..n]);
                self.n += n;
                self.flush();
            }
            p = &p[n..];
        }
        self.buf[self.n..self.n + p.len()].copy_from_slice(p);
        self.n += p.len();
    }

    pub fn finish(mut self) -> Vec<u8> {
        self.flush();
        self.out
    }
}

fn crc32_table() -> &'static [u32; 256] {
    use std::sync::OnceLock;
    static T: OnceLock<[u32; 256]> = OnceLock::new();
    T.get_or_init(|| {
        let mut t = [0u32; 256];
        for (i, e) in t.iter_mut().enumerate() {
            let mut c = i as u32;
            for _ in 0..8 {
                c = if c & 1 == 1 {
                    0xedb8_8320 ^ (c >> 1)
                } else {
                    c >> 1
                };
            }
            *e = c;
        }
        t
    })
}

fn crc32_ieee(parts: &[&[u8]]) -> u32 {
    let t = crc32_table();
    let mut crc = !0u32;
    for part in parts {
        for &b in *part {
            crc = t[usize::from((crc as u8) ^ b)] ^ (crc >> 8);
        }
    }
    !crc
}

/// Go `png.encoder.writeChunk`.
pub fn write_chunk(out: &mut Vec<u8>, name: &[u8; 4], data: &[u8]) {
    out.extend_from_slice(&(data.len() as u32).to_be_bytes());
    out.extend_from_slice(name);
    out.extend_from_slice(data);
    out.extend_from_slice(&crc32_ieee(&[name, data]).to_be_bytes());
}

struct HuffmanBitWriter {
    w: IdatWriter,
    bits: u64,
    nbits: u32,
    bytes: [u8; BUFFER_SIZE],
    nbytes: usize,
    codegen_freq: [i32; CODEGEN_CODE_COUNT],
    literal_freq: Vec<i32>,
    offset_freq: Vec<i32>,
    codegen: Vec<u8>,
    literal_encoding: HuffmanEncoder,
    offset_encoding: HuffmanEncoder,
    codegen_encoding: HuffmanEncoder,
    fixed_literal: HuffmanEncoder,
    fixed_offset: HuffmanEncoder,
}

impl HuffmanBitWriter {
    fn new(w: IdatWriter) -> HuffmanBitWriter {
        HuffmanBitWriter {
            w,
            bits: 0,
            nbits: 0,
            bytes: [0; BUFFER_SIZE],
            nbytes: 0,
            codegen_freq: [0; CODEGEN_CODE_COUNT],
            literal_freq: vec![0; MAX_NUM_LIT],
            offset_freq: vec![0; OFFSET_CODE_COUNT],
            codegen: vec![0; MAX_NUM_LIT + OFFSET_CODE_COUNT + 1],
            literal_encoding: HuffmanEncoder::new(MAX_NUM_LIT),
            offset_encoding: HuffmanEncoder::new(OFFSET_CODE_COUNT),
            codegen_encoding: HuffmanEncoder::new(CODEGEN_CODE_COUNT),
            fixed_literal: HuffmanEncoder::fixed_literal(),
            fixed_offset: HuffmanEncoder::fixed_offset(),
        }
    }

    fn flush(&mut self) {
        let mut n = self.nbytes;
        while self.nbits != 0 {
            self.bytes[n] = self.bits as u8;
            self.bits >>= 8;
            if self.nbits > 8 {
                self.nbits -= 8;
            } else {
                self.nbits = 0;
            }
            n += 1;
        }
        self.bits = 0;
        let chunk = self.bytes[..n].to_vec();
        self.w.write(&chunk);
        self.nbytes = 0;
    }

    fn write_bits(&mut self, b: i32, nb: u32) {
        self.bits |= (b as i64 as u64) << self.nbits;
        self.nbits += nb;
        if self.nbits >= 48 {
            self.spill();
        }
    }

    fn write_code(&mut self, c: HCode) {
        self.bits |= u64::from(c.code) << self.nbits;
        self.nbits += u32::from(c.len);
        if self.nbits >= 48 {
            self.spill();
        }
    }

    fn spill(&mut self) {
        let bits = self.bits;
        self.bits >>= 48;
        self.nbits -= 48;
        let mut n = self.nbytes;
        for k in 0..6 {
            self.bytes[n + k] = (bits >> (8 * k)) as u8;
        }
        n += 6;
        if n >= BUFFER_FLUSH_SIZE {
            let chunk = self.bytes[..n].to_vec();
            self.w.write(&chunk);
            n = 0;
        }
        self.nbytes = n;
    }

    fn write_bytes(&mut self, bytes: &[u8]) {
        let mut n = self.nbytes;
        assert!(self.nbits & 7 == 0, "writeBytes with unfinished bits");
        while self.nbits != 0 {
            self.bytes[n] = self.bits as u8;
            self.bits >>= 8;
            self.nbits -= 8;
            n += 1;
        }
        if n != 0 {
            let chunk = self.bytes[..n].to_vec();
            self.w.write(&chunk);
        }
        self.nbytes = 0;
        self.w.write(bytes);
    }

    fn generate_codegen(&mut self, num_literals: usize, num_offsets: usize) {
        self.codegen_freq = [0; CODEGEN_CODE_COUNT];
        let codegen = &mut self.codegen;
        for i in 0..num_literals {
            codegen[i] = self.literal_encoding.codes[i].len as u8;
        }
        for i in 0..num_offsets {
            codegen[num_literals + i] = self.offset_encoding.codes[i].len as u8;
        }
        codegen[num_literals + num_offsets] = BAD_CODE;

        let mut size = codegen[0];
        let mut count: i32 = 1;
        let mut out_index = 0usize;
        let mut in_index = 1usize;
        while size != BAD_CODE {
            let next_size = codegen[in_index];
            if next_size == size {
                count += 1;
                in_index += 1;
                continue;
            }
            if size != 0 {
                codegen[out_index] = size;
                out_index += 1;
                self.codegen_freq[usize::from(size)] += 1;
                count -= 1;
                while count >= 3 {
                    let n = 6.min(count);
                    codegen[out_index] = 16;
                    out_index += 1;
                    codegen[out_index] = (n - 3) as u8;
                    out_index += 1;
                    self.codegen_freq[16] += 1;
                    count -= n;
                }
            } else {
                while count >= 11 {
                    let n = 138.min(count);
                    codegen[out_index] = 18;
                    out_index += 1;
                    codegen[out_index] = (n - 11) as u8;
                    out_index += 1;
                    self.codegen_freq[18] += 1;
                    count -= n;
                }
                if count >= 3 {
                    codegen[out_index] = 17;
                    out_index += 1;
                    codegen[out_index] = (count - 3) as u8;
                    out_index += 1;
                    self.codegen_freq[17] += 1;
                    count = 0;
                }
            }
            count -= 1;
            while count >= 0 {
                codegen[out_index] = size;
                out_index += 1;
                self.codegen_freq[usize::from(size)] += 1;
                count -= 1;
            }
            size = next_size;
            count = 1;
            in_index += 1;
        }
        codegen[out_index] = BAD_CODE;
    }

    fn dynamic_size(&self, extra_bits: i32) -> (i32, usize) {
        let mut num_codegens = self.codegen_freq.len();
        while num_codegens > 4 && self.codegen_freq[CODEGEN_ORDER[num_codegens - 1]] == 0 {
            num_codegens -= 1;
        }
        let header = 3
            + 5
            + 5
            + 4
            + (3 * num_codegens as i32)
            + self.codegen_encoding.bit_length(&self.codegen_freq)
            + self.codegen_freq[16] * 2
            + self.codegen_freq[17] * 3
            + self.codegen_freq[18] * 7;
        let size = header
            + self.literal_encoding.bit_length(&self.literal_freq)
            + self.offset_encoding.bit_length(&self.offset_freq)
            + extra_bits;
        (size, num_codegens)
    }

    fn fixed_size(&self, extra_bits: i32) -> i32 {
        3 + self.fixed_literal.bit_length(&self.literal_freq)
            + self.fixed_offset.bit_length(&self.offset_freq)
            + extra_bits
    }

    fn stored_size(input: Option<&[u8]>) -> (i32, bool) {
        match input {
            None => (0, false),
            Some(i) if i.len() <= MAX_STORE_BLOCK_SIZE => (((i.len() + 5) * 8) as i32, true),
            Some(_) => (0, false),
        }
    }

    fn write_stored_header(&mut self, length: usize, eof: bool) {
        self.write_bits(i32::from(eof), 3);
        self.flush();
        self.write_bits(length as i32, 16);
        self.write_bits(i32::from(!(length as u16)), 16);
    }

    fn write_fixed_header(&mut self, eof: bool) {
        self.write_bits(if eof { 3 } else { 2 }, 3);
    }

    fn write_dynamic_header(
        &mut self,
        num_literals: usize,
        num_offsets: usize,
        num_codegens: usize,
        eof: bool,
    ) {
        self.write_bits(if eof { 5 } else { 4 }, 3);
        self.write_bits(num_literals as i32 - 257, 5);
        self.write_bits(num_offsets as i32 - 1, 5);
        self.write_bits(num_codegens as i32 - 4, 4);
        for i in 0..num_codegens {
            let value = u32::from(self.codegen_encoding.codes[CODEGEN_ORDER[i]].len);
            self.write_bits(value as i32, 3);
        }
        let mut i = 0usize;
        loop {
            let code_word = self.codegen[i];
            i += 1;
            if code_word == BAD_CODE {
                break;
            }
            let c = self.codegen_encoding.codes[usize::from(code_word)];
            self.write_code(c);
            match code_word {
                16 => {
                    let v = i32::from(self.codegen[i]);
                    self.write_bits(v, 2);
                    i += 1;
                }
                17 => {
                    let v = i32::from(self.codegen[i]);
                    self.write_bits(v, 3);
                    i += 1;
                }
                18 => {
                    let v = i32::from(self.codegen[i]);
                    self.write_bits(v, 7);
                    i += 1;
                }
                _ => {}
            }
        }
    }

    fn index_tokens(&mut self, tokens: &[u32]) -> (usize, usize) {
        self.literal_freq.iter_mut().for_each(|f| *f = 0);
        self.offset_freq.iter_mut().for_each(|f| *f = 0);
        for &t in tokens {
            if t < MATCH_TYPE {
                self.literal_freq[t as usize] += 1;
                continue;
            }
            let length = (t - MATCH_TYPE) >> LENGTH_SHIFT;
            let offset = t & OFFSET_MASK;
            self.literal_freq[LENGTH_CODES_START + length_code(length) as usize] += 1;
            self.offset_freq[offset_code(offset) as usize] += 1;
        }
        let mut num_literals = self.literal_freq.len();
        while self.literal_freq[num_literals - 1] == 0 {
            num_literals -= 1;
        }
        let mut num_offsets = self.offset_freq.len();
        while num_offsets > 0 && self.offset_freq[num_offsets - 1] == 0 {
            num_offsets -= 1;
        }
        if num_offsets == 0 {
            self.offset_freq[0] = 1;
            num_offsets = 1;
        }
        let lit_freq = self.literal_freq.clone();
        self.literal_encoding.generate(&lit_freq, 15);
        let off_freq = self.offset_freq.clone();
        self.offset_encoding.generate(&off_freq, 15);
        (num_literals, num_offsets)
    }

    fn write_tokens(&mut self, tokens: &[u32], fixed: bool) {
        for &t in tokens {
            if t < MATCH_TYPE {
                let c = if fixed {
                    self.fixed_literal.codes[t as usize]
                } else {
                    self.literal_encoding.codes[t as usize]
                };
                self.write_code(c);
                continue;
            }
            let length = (t - MATCH_TYPE) >> LENGTH_SHIFT;
            let lc = length_code(length) as usize;
            let c = if fixed {
                self.fixed_literal.codes[lc + LENGTH_CODES_START]
            } else {
                self.literal_encoding.codes[lc + LENGTH_CODES_START]
            };
            self.write_code(c);
            let extra_length_bits = LENGTH_EXTRA_BITS[lc] as u32;
            if extra_length_bits > 0 {
                let extra = (length - LENGTH_BASE[lc]) as i32;
                self.write_bits(extra, extra_length_bits);
            }
            let offset = t & OFFSET_MASK;
            let oc = offset_code(offset) as usize;
            let c = if fixed {
                self.fixed_offset.codes[oc]
            } else {
                self.offset_encoding.codes[oc]
            };
            self.write_code(c);
            let extra_offset_bits = OFFSET_EXTRA_BITS[oc] as u32;
            if extra_offset_bits > 0 {
                let extra = (offset - OFFSET_BASE[oc]) as i32;
                self.write_bits(extra, extra_offset_bits);
            }
        }
    }

    /// Go `huffmanBitWriter.writeBlock`. `tokens` must not already carry the
    /// end-of-block marker; it is appended here.
    fn write_block(&mut self, tokens: &mut Vec<u32>, eof: bool, input: Option<&[u8]>) {
        tokens.push(END_BLOCK_MARKER as u32);
        let (num_literals, num_offsets) = self.index_tokens(tokens);

        let mut extra_bits: i32 = 0;
        let (stored_size, storable) = HuffmanBitWriter::stored_size(input);
        if storable {
            for length_code in LENGTH_CODES_START + 8..num_literals {
                extra_bits += self.literal_freq[length_code]
                    * i32::from(LENGTH_EXTRA_BITS[length_code - LENGTH_CODES_START]);
            }
            for offset_code in 4..num_offsets {
                extra_bits +=
                    self.offset_freq[offset_code] * i32::from(OFFSET_EXTRA_BITS[offset_code]);
            }
        }

        let mut use_fixed = true;
        let mut size = self.fixed_size(extra_bits);
        self.generate_codegen(num_literals, num_offsets);
        let cf = self.codegen_freq;
        self.codegen_encoding.generate(&cf, 7);
        let (dynamic_size, num_codegens) = self.dynamic_size(extra_bits);
        if dynamic_size < size {
            size = dynamic_size;
            use_fixed = false;
        }
        if storable && stored_size < size {
            let input = input.unwrap_or_default();
            self.write_stored_header(input.len(), eof);
            self.write_bytes(input);
            tokens.pop();
            return;
        }
        if use_fixed {
            self.write_fixed_header(eof);
        } else {
            self.write_dynamic_header(num_literals, num_offsets, num_codegens, eof);
        }
        self.write_tokens(tokens, use_fixed);
        tokens.pop();
    }
}

/// Go `compress/flate.compressor` at level 6 behind a `compress/zlib.Writer`.
pub struct ZlibWriter {
    w: HuffmanBitWriter,
    window: Vec<u8>,
    index: usize,
    window_end: usize,
    block_start: usize,
    byte_available: bool,
    sync: bool,
    tokens: Vec<u32>,
    length: usize,
    offset: usize,
    max_insert_index: isize,
    chain_head: isize,
    hash_head: Vec<u32>,
    hash_prev: Vec<u32>,
    hash_offset: isize,
    wrote_header: bool,
    adler_a: u32,
    adler_b: u32,
}

fn hash4(b: &[u8]) -> u32 {
    ((u32::from(b[3]) | u32::from(b[2]) << 8 | u32::from(b[1]) << 16 | u32::from(b[0]) << 24)
        .wrapping_mul(HASH_MUL))
        >> (32 - HASH_BITS)
}

impl ZlibWriter {
    /// A zlib stream (default level) whose bytes go through the PNG `IDAT`
    /// `bufio` writer; `out` already holds the PNG signature and `IHDR`.
    pub fn new(out: Vec<u8>) -> ZlibWriter {
        ZlibWriter {
            w: HuffmanBitWriter::new(IdatWriter::new(out)),
            window: vec![0; 2 * WINDOW_SIZE],
            index: 0,
            window_end: 0,
            block_start: 0,
            byte_available: false,
            sync: false,
            tokens: Vec::with_capacity(MAX_FLATE_BLOCK_TOKENS + 1),
            length: MIN_MATCH_LENGTH - 1,
            offset: 0,
            max_insert_index: 0,
            chain_head: -1,
            hash_head: vec![0; HASH_SIZE],
            hash_prev: vec![0; WINDOW_SIZE],
            hash_offset: 1,
            wrote_header: false,
            adler_a: 1,
            adler_b: 0,
        }
    }

    fn fill_deflate(&mut self, b: &[u8]) -> usize {
        if self.index >= 2 * WINDOW_SIZE - (MIN_MATCH_LENGTH + MAX_MATCH_LENGTH) {
            self.window.copy_within(WINDOW_SIZE..2 * WINDOW_SIZE, 0);
            self.index -= WINDOW_SIZE;
            self.window_end -= WINDOW_SIZE;
            if self.block_start >= WINDOW_SIZE {
                self.block_start -= WINDOW_SIZE;
            } else {
                self.block_start = i32::MAX as usize;
            }
            self.hash_offset += WINDOW_SIZE as isize;
            if self.hash_offset > MAX_HASH_OFFSET {
                let delta = self.hash_offset - 1;
                self.hash_offset -= delta;
                self.chain_head -= delta;
                for v in self.hash_prev.iter_mut() {
                    *v = if *v as isize > delta {
                        (*v as isize - delta) as u32
                    } else {
                        0
                    };
                }
                for v in self.hash_head.iter_mut() {
                    *v = if *v as isize > delta {
                        (*v as isize - delta) as u32
                    } else {
                        0
                    };
                }
            }
        }
        let n = b.len().min(self.window.len() - self.window_end);
        self.window[self.window_end..self.window_end + n].copy_from_slice(&b[..n]);
        self.window_end += n;
        n
    }

    fn write_block(&mut self, index: usize) {
        if index > 0 {
            // Move the tokens out so the bit writer can borrow `self.window`.
            let mut tokens = std::mem::take(&mut self.tokens);
            let window = if self.block_start <= index {
                Some(self.window[self.block_start..index].to_vec())
            } else {
                None
            };
            self.block_start = index;
            self.w.write_block(&mut tokens, false, window.as_deref());
            self.tokens = tokens;
        }
    }

    fn find_match(
        &self,
        pos: usize,
        prev_head: isize,
        prev_length: usize,
        lookahead: usize,
    ) -> Option<(usize, usize)> {
        let min_match_look = MAX_MATCH_LENGTH.min(lookahead);
        let win = &self.window[0..pos + min_match_look];
        let nice = (win.len() - pos).min(L6_NICE);
        let mut tries = L6_CHAIN;
        let mut length = prev_length;
        if length >= L6_GOOD {
            tries >>= 2;
        }
        let mut w_end = win[pos + length];
        let w_pos = &win[pos..];
        let min_index = pos as isize - WINDOW_SIZE as isize;
        let mut found: Option<(usize, usize)> = None;
        let mut i = prev_head;
        while tries > 0 {
            if w_end == win[i as usize + length] {
                let n = match_len(&win[i as usize..], w_pos, min_match_look);
                if n > length && (n > MIN_MATCH_LENGTH || pos - i as usize <= 4096) {
                    length = n;
                    found = Some((n, pos - i as usize));
                    if n >= nice {
                        break;
                    }
                    w_end = win[pos + n];
                }
            }
            if i == min_index {
                break;
            }
            i = self.hash_prev[i as usize & WINDOW_MASK] as isize - self.hash_offset;
            if i < min_index || i < 0 {
                break;
            }
            tries -= 1;
        }
        found
    }

    fn deflate(&mut self) {
        if self.window_end - self.index < MIN_MATCH_LENGTH + MAX_MATCH_LENGTH && !self.sync {
            return;
        }
        self.max_insert_index = self.window_end as isize - (MIN_MATCH_LENGTH as isize - 1);
        loop {
            assert!(self.index <= self.window_end, "index > windowEnd");
            let lookahead = self.window_end - self.index;
            if lookahead < MIN_MATCH_LENGTH + MAX_MATCH_LENGTH {
                if !self.sync {
                    break;
                }
                if lookahead == 0 {
                    if self.byte_available {
                        self.tokens
                            .push(literal_token(u32::from(self.window[self.index - 1])));
                        self.byte_available = false;
                    }
                    if !self.tokens.is_empty() {
                        self.write_block(self.index);
                        self.tokens.clear();
                    }
                    break;
                }
            }
            if (self.index as isize) < self.max_insert_index {
                let hash = hash4(&self.window[self.index..self.index + MIN_MATCH_LENGTH]);
                let slot = hash as usize & HASH_MASK;
                self.chain_head = self.hash_head[slot] as isize;
                self.hash_prev[self.index & WINDOW_MASK] = self.chain_head as u32;
                self.hash_head[slot] = (self.index as isize + self.hash_offset) as u32;
            }
            let prev_length = self.length;
            let prev_offset = self.offset;
            self.length = MIN_MATCH_LENGTH - 1;
            self.offset = 0;
            let min_index = self.index.saturating_sub(WINDOW_SIZE);

            // fastSkipHashing == skipNever for level 6 (lazy matching).
            if self.chain_head - self.hash_offset >= min_index as isize
                && lookahead > prev_length
                && prev_length < L6_LAZY
                && let Some((new_length, new_offset)) = self.find_match(
                    self.index,
                    self.chain_head - self.hash_offset,
                    MIN_MATCH_LENGTH - 1,
                    lookahead,
                )
            {
                self.length = new_length;
                self.offset = new_offset;
            }
            if prev_length >= MIN_MATCH_LENGTH && self.length <= prev_length {
                self.tokens.push(match_token(
                    prev_length as u32 - BASE_MATCH_LENGTH,
                    prev_offset as u32 - BASE_MATCH_OFFSET,
                ));
                // Level 6: d.length <= fastSkipHashing(skipNever) is always true.
                let _ = SKIP_NEVER;
                let new_index = self.index + prev_length - 1;
                let mut index = self.index;
                index += 1;
                while index < new_index {
                    if (index as isize) < self.max_insert_index {
                        let hash = hash4(&self.window[index..index + MIN_MATCH_LENGTH]);
                        let slot = hash as usize & HASH_MASK;
                        self.hash_prev[index & WINDOW_MASK] = self.hash_head[slot];
                        self.hash_head[slot] = (index as isize + self.hash_offset) as u32;
                    }
                    index += 1;
                }
                self.index = index;
                self.byte_available = false;
                self.length = MIN_MATCH_LENGTH - 1;
                if self.tokens.len() == MAX_FLATE_BLOCK_TOKENS {
                    self.write_block(self.index);
                    self.tokens.clear();
                }
            } else {
                if self.byte_available {
                    let i = self.index - 1;
                    self.tokens.push(literal_token(u32::from(self.window[i])));
                    if self.tokens.len() == MAX_FLATE_BLOCK_TOKENS {
                        self.write_block(i + 1);
                        self.tokens.clear();
                    }
                }
                self.index += 1;
                self.byte_available = true;
            }
        }
    }

    fn write_header(&mut self) {
        self.wrote_header = true;
        let mut s = [0x78u8, 2 << 6];
        let v = u16::from_be_bytes(s) % 31;
        s[1] += (31 - v) as u8;
        self.w.w.write(&s);
    }

    /// `zlib.Writer.Write`.
    pub fn write(&mut self, mut b: &[u8]) {
        if !self.wrote_header {
            self.write_header();
        }
        if b.is_empty() {
            return;
        }
        let whole = b;
        while !b.is_empty() {
            self.deflate();
            let n = self.fill_deflate(b);
            b = &b[n..];
        }
        for &v in whole {
            self.adler_a = (self.adler_a + u32::from(v)) % 65521;
            self.adler_b = (self.adler_b + self.adler_a) % 65521;
        }
    }

    /// `zlib.Writer.Close` followed by the `bufio` flush; returns the PNG
    /// bytes accumulated so far.
    pub fn close(mut self) -> Vec<u8> {
        if !self.wrote_header {
            self.write_header();
        }
        self.sync = true;
        self.deflate();
        self.w.write_stored_header(0, true);
        self.w.flush();
        let checksum = (self.adler_b << 16) | self.adler_a;
        self.w.w.write(&checksum.to_be_bytes());
        self.w.w.finish()
    }
}

fn match_len(a: &[u8], b: &[u8], max: usize) -> usize {
    let a = &a[..max];
    let b = &b[..max];
    for i in 0..max {
        if a[i] != b[i] {
            return i;
        }
    }
    max
}
