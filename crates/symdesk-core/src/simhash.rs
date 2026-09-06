#![deny(unsafe_code)]

//! Byte-compatible 64-bit SimHash contracts from the Go oracle.

pub const MINIMUM_BODY_LENGTH: usize = 64;
pub const SHORT_BODY_SIMILARITY_CAP: u32 = 50;

#[must_use]
pub fn content_length(text: &str) -> usize {
    text.trim().chars().count()
}

#[must_use]
pub fn compute(text: &str) -> u64 {
    let mut vector = [0_i32; 64];
    for token in text.to_lowercase().split_whitespace() {
        let hash = fnv1a64(token.as_bytes());
        for (bit, value) in vector.iter_mut().enumerate() {
            if hash & (1_u64 << bit) == 0 {
                *value -= 1;
            } else {
                *value += 1;
            }
        }
    }
    vector
        .iter()
        .enumerate()
        .fold(0_u64, |mut output, (bit, value)| {
            if *value > 0 {
                output |= 1_u64 << bit;
            }
            output
        })
}

#[must_use]
pub fn compute_hex(text: &str) -> String {
    format!("{:016x}", compute(text))
}

#[must_use]
pub const fn hamming_distance(left: u64, right: u64) -> u32 {
    (left ^ right).count_ones()
}

#[must_use]
pub const fn similarity(left: u64, right: u64) -> u32 {
    (64 - hamming_distance(left, right)) * 100 / 64
}

#[must_use]
pub fn similarity_for_content(left: u64, right: u64, left_body: &str, right_body: &str) -> u32 {
    let score = similarity(left, right);
    if (content_length(left_body) < MINIMUM_BODY_LENGTH
        || content_length(right_body) < MINIMUM_BODY_LENGTH)
        && score > SHORT_BODY_SIMILARITY_CAP
    {
        SHORT_BODY_SIMILARITY_CAP
    } else {
        score
    }
}

/// Parses the exact 16-character hexadecimal representation.
///
/// # Errors
///
/// Returns the Go-compatible public error text for invalid length or digits.
pub fn parse_hex(value: &str) -> Result<u64, String> {
    if value.len() != 16 {
        return Err(format!(
            "simhash hex must be 16 characters, got {}",
            value.len()
        ));
    }
    for byte in value.bytes() {
        if !byte.is_ascii_hexdigit() {
            let display = if byte.is_ascii_graphic() {
                format!("U+{byte:04X} '{}'", char::from(byte))
            } else {
                format!("U+{byte:04X}")
            };
            return Err(format!(
                "invalid simhash hex: encoding/hex: invalid byte: {display}"
            ));
        }
    }
    u64::from_str_radix(value, 16).map_err(|error| format!("invalid simhash hex: {error}"))
}

const fn fnv1a64(value: &[u8]) -> u64 {
    let mut hash = 14_695_981_039_346_656_037_u64;
    let mut index = 0;
    while index < value.len() {
        hash ^= value[index] as u64;
        hash = hash.wrapping_mul(1_099_511_628_211);
        index += 1;
    }
    hash
}
