//! Port of Go's `html.UnescapeString` (html/escape.go, attribute = false).
#[rustfmt::skip]
mod table {
    include!("html_entities.rs");
}
use table::{ENTITY, ENTITY2};

const LONGEST_WITHOUT_SEMICOLON: usize = 6;

const REPLACEMENT: [u32; 32] = [
    0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039,
    0x0152, 0x008D, 0x017D, 0x008F, 0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
    0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
];

fn lookup(name: &[u8]) -> Option<u32> {
    let name = std::str::from_utf8(name).ok()?;
    ENTITY
        .binary_search_by(|(n, _)| (*n).cmp(name))
        .ok()
        .map(|i| ENTITY[i].1)
}

fn lookup2(name: &[u8]) -> Option<(u32, u32)> {
    let name = std::str::from_utf8(name).ok()?;
    ENTITY2
        .binary_search_by(|(n, _, _)| (*n).cmp(name))
        .ok()
        .map(|i| (ENTITY2[i].1, ENTITY2[i].2))
}

/// Go `utf8.EncodeRune` (invalid values encode U+FFFD).
fn push_rune(out: &mut Vec<u8>, r: u32) {
    let c = char::from_u32(r).unwrap_or('\u{FFFD}');
    let mut b = [0; 4];
    out.extend_from_slice(c.encode_utf8(&mut b).as_bytes());
}

/// Returns bytes consumed from `s` (which starts at an `&`).
fn unescape_entity(out: &mut Vec<u8>, s: &[u8]) -> usize {
    if s.len() <= 1 {
        out.push(s[0]);
        return 1;
    }
    let mut i = 1;
    if s[i] == b'#' {
        if s.len() <= 3 {
            out.push(s[0]);
            return 1;
        }
        i += 1;
        let c = s[i];
        let mut hex = false;
        if c == b'x' || c == b'X' {
            hex = true;
            i += 1;
        }
        let mut x: i32 = 0;
        while i < s.len() {
            let c = s[i];
            i += 1;
            if hex {
                if c.is_ascii_digit() {
                    x = x.wrapping_mul(16).wrapping_add(i32::from(c - b'0'));
                    continue;
                } else if (b'a'..=b'f').contains(&c) {
                    x = x.wrapping_mul(16).wrapping_add(i32::from(c - b'a') + 10);
                    continue;
                } else if (b'A'..=b'F').contains(&c) {
                    x = x.wrapping_mul(16).wrapping_add(i32::from(c - b'A') + 10);
                    continue;
                }
            } else if c.is_ascii_digit() {
                x = x.wrapping_mul(10).wrapping_add(i32::from(c - b'0'));
                continue;
            }
            if c != b';' {
                i -= 1;
            }
            break;
        }
        if i <= 3 {
            out.push(s[0]);
            return 1;
        }
        let r = if (0x80..=0x9F).contains(&x) {
            REPLACEMENT[(x - 0x80) as usize]
        } else if x == 0 || (0xD800..=0xDFFF).contains(&x) || !(0..=0x10FFFF).contains(&x) {
            0xFFFD
        } else {
            x as u32
        };
        push_rune(out, r);
        return i;
    }
    while i < s.len() {
        let c = s[i];
        i += 1;
        if c.is_ascii_alphanumeric() {
            continue;
        }
        if c != b';' {
            i -= 1;
        }
        break;
    }
    let name = &s[1..i];
    if !name.is_empty() {
        if let Some(x) = lookup(name).filter(|x| *x != 0) {
            push_rune(out, x);
            return i;
        } else if let Some((a, b)) = lookup2(name) {
            push_rune(out, a);
            push_rune(out, b);
            return i;
        } else {
            let max_len = (name.len() - 1).min(LONGEST_WITHOUT_SEMICOLON);
            for j in (2..=max_len).rev() {
                if let Some(x) = lookup(&name[..j]).filter(|x| *x != 0) {
                    push_rune(out, x);
                    return j + 1;
                }
            }
        }
    }
    out.extend_from_slice(&s[..i]);
    i
}

/// `html.UnescapeString` over the bytes of `s`.
pub fn unescape(s: &str) -> String {
    let b = s.as_bytes();
    let Some(first) = b.iter().position(|c| *c == b'&') else {
        return s.to_string();
    };
    let mut out = Vec::with_capacity(b.len());
    out.extend_from_slice(&b[..first]);
    let mut src = first;
    while src < b.len() {
        if b[src] == b'&' {
            src += unescape_entity(&mut out, &b[src..]);
        } else {
            let next = b[src..]
                .iter()
                .position(|c| *c == b'&')
                .map_or(b.len(), |p| src + p);
            out.extend_from_slice(&b[src..next]);
            src = next;
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}
