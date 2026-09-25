//! Go-compatible materialization of parser sections into retrieval chunks.

const CHUNK_SIZE: usize = 1000;
const CHUNK_OVERLAP: usize = 200;
const CHUNK_NAMESPACE: [u8; 16] = [
    0x23, 0x40, 0xd1, 0x2a, 0x65, 0x6a, 0x5d, 0x01, 0x97, 0x1a, 0x40, 0x58, 0x6e, 0xe6, 0x13, 0xa6,
];

#[derive(Clone, Debug, Eq, PartialEq, serde::Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct RetrievalAnchor {
    pub kind: String,
    pub value: String,
}

#[derive(Clone, Debug, Eq, PartialEq, serde::Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct RetrievalSection {
    pub text: String,
    pub start: usize,
    pub anchor: RetrievalAnchor,
    #[serde(default)]
    pub synthetic: bool,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct RetrievalChunk {
    pub uuid: String,
    pub chunk_index: usize,
    pub content: String,
    pub hash: String,
    pub char_start: Option<usize>,
    pub char_end: Option<usize>,
    pub anchor_kind: String,
    pub anchor_value: String,
}

/// Splits sections like Go's `buildChunksFromSections`, retaining source byte spans.
pub fn materialize_chunks(source: &str, sections: &[RetrievalSection]) -> Vec<RetrievalChunk> {
    let spans = sections
        .iter()
        .flat_map(|section| {
            split_spans(section.text.as_bytes(), 0, 0)
                .into_iter()
                .map(|mut span| {
                    if !section.synthetic {
                        span.start += section.start;
                        span.end += section.start;
                    }
                    let mut anchor = section.anchor.clone();
                    if anchor.kind == "text" && !section.synthetic {
                        anchor.value = format!("offset:{}", span.start);
                    }
                    (span, anchor, section.synthetic)
                })
        })
        .collect::<Vec<_>>();

    spans
        .into_iter()
        .enumerate()
        .map(|(chunk_index, (span, anchor, synthetic))| {
            let hash = symdesk_vault::sha256_hex(&span.text);
            let start = span.start;
            let mut name = Vec::with_capacity(source.len() + hash.len() + 24);
            name.extend_from_slice(source.as_bytes());
            name.push(0);
            name.extend_from_slice(hash.as_bytes());
            name.push(0);
            name.extend_from_slice(start.to_string().as_bytes());
            RetrievalChunk {
                uuid: uuid_v5(CHUNK_NAMESPACE, &name),
                chunk_index,
                content: String::from_utf8_lossy(&span.text).into_owned(),
                hash,
                char_start: (!synthetic).then_some(span.start),
                char_end: (!synthetic).then_some(span.end),
                anchor_kind: anchor.kind,
                anchor_value: anchor.value,
            }
        })
        .collect()
}

struct Span {
    text: Vec<u8>,
    start: usize,
    end: usize,
}

fn split_spans(text: &[u8], base: usize, first_separator: usize) -> Vec<Span> {
    if text.len() <= CHUNK_SIZE {
        return vec![Span {
            text: text.to_vec(),
            start: base,
            end: base + text.len(),
        }];
    }
    let separators: [&[u8]; 4] = [b"\n\n", b"\n", b" ", b""];
    let Some((separator_index, separator)) = separators
        .get(first_separator..)
        .unwrap_or_default()
        .iter()
        .enumerate()
        .find(|(_, separator)| separator.is_empty() || find_bytes(text, separator).is_some())
        .map(|(offset, separator)| (first_separator + offset, *separator))
    else {
        let mut spans = Vec::new();
        let step = CHUNK_SIZE - CHUNK_OVERLAP;
        let mut start = 0;
        while start < text.len() {
            let end = (start + CHUNK_SIZE).min(text.len());
            spans.push(Span {
                text: text[start..end].to_vec(),
                start: base + start,
                end: base + end,
            });
            if end == text.len() {
                break;
            }
            start += step;
        }
        return spans;
    };

    let parts = split_parts(text, separator);
    let mut final_spans = Vec::new();
    let mut current = Vec::new();
    let mut chunk_start = 0;
    for (i, (part_start, part_end)) in parts.iter().copied().enumerate() {
        let part = &text[part_start..part_end];
        if part.len() > CHUNK_SIZE {
            if !current.is_empty() {
                final_spans.push(Span {
                    end: base + chunk_start + current.len(),
                    text: std::mem::take(&mut current),
                    start: base + chunk_start,
                });
            }
            final_spans.extend(split_spans(part, base + part_start, separator_index + 1));
            continue;
        }
        if !current.is_empty() {
            if current.len() + separator.len() + part.len() <= CHUNK_SIZE {
                current.extend_from_slice(separator);
                current.extend_from_slice(part);
            } else {
                let overlap_start = current.len().saturating_sub(CHUNK_OVERLAP);
                let tail = current[overlap_start..].to_vec();
                let new_start = if tail.is_empty() {
                    part_start
                } else {
                    chunk_start + overlap_start
                };
                let chunk_end = chunk_start + current.len();
                final_spans.push(Span {
                    text: std::mem::take(&mut current),
                    start: base + chunk_start,
                    end: base + chunk_end,
                });
                current.extend_from_slice(tail);
                if !current.is_empty() && !tail.ends_with(separator) {
                    current.extend_from_slice(separator);
                }
                current.extend_from_slice(part);
                chunk_start = new_start;
            }
        } else {
            current.extend_from_slice(part);
            chunk_start = part_start;
        }
    }
    if !current.is_empty() {
        final_spans.push(Span {
            end: base + chunk_start + current.len(),
            text: current,
            start: base + chunk_start,
        });
    }
    final_spans
}

fn split_parts(text: &[u8], separator: &[u8]) -> Vec<(usize, usize)> {
    if separator.is_empty() {
        let mut parts = std::str::from_utf8(text)
            .expect("source sections are valid UTF-8")
            .char_indices()
            .map(|(start, _)| start)
            .collect::<Vec<_>>();
        parts.push(text.len());
        return parts.windows(2).map(|pair| (pair[0], pair[1])).collect();
    }
    let mut parts = Vec::new();
    let mut start = 0;
    while let Some(offset) = find_bytes(&text[start..], separator) {
        let end = start + offset;
        parts.push((start, end));
        start = end + separator.len();
    }
    parts.push((start, text.len()));
    parts
}

fn find_bytes(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}

fn uuid_v5(namespace: [u8; 16], name: &[u8]) -> String {
    let mut input = Vec::with_capacity(namespace.len() + name.len());
    input.extend_from_slice(&namespace);
    input.extend_from_slice(name);
    let mut bytes = sha1(&input);
    bytes[6] = (bytes[6] & 0x0f) | 0x50;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    format!(
        "{:02x}{:02x}{:02x}{:02x}-{:02x}{:02x}-{:02x}{:02x}-{:02x}{:02x}-{:02x}{:02x}{:02x}{:02x}{:02x}{:02x}",
        bytes[0],
        bytes[1],
        bytes[2],
        bytes[3],
        bytes[4],
        bytes[5],
        bytes[6],
        bytes[7],
        bytes[8],
        bytes[9],
        bytes[10],
        bytes[11],
        bytes[12],
        bytes[13],
        bytes[14],
        bytes[15]
    )
}

fn sha1(input: &[u8]) -> [u8; 20] {
    let bit_len = (input.len() as u64).wrapping_mul(8);
    let mut message = input.to_vec();
    message.push(0x80);
    while message.len() % 64 != 56 {
        message.push(0);
    }
    message.extend_from_slice(&bit_len.to_be_bytes());
    let (mut h0, mut h1, mut h2, mut h3, mut h4) = (
        0x67452301u32,
        0xefcdab89u32,
        0x98badcfeu32,
        0x10325476u32,
        0xc3d2e1f0u32,
    );
    for block in message.chunks_exact(64) {
        let mut words = [0u32; 80];
        for (i, bytes) in block.chunks_exact(4).enumerate() {
            words[i] = u32::from_be_bytes(bytes.try_into().expect("four-byte word"));
        }
        for i in 16..80 {
            words[i] = (words[i - 3] ^ words[i - 8] ^ words[i - 14] ^ words[i - 16]).rotate_left(1);
        }
        let (mut a, mut b, mut c, mut d, mut e) = (h0, h1, h2, h3, h4);
        for (i, word) in words.iter().enumerate() {
            let (f, k) = match i {
                0..=19 => ((b & c) | ((!b) & d), 0x5a827999),
                20..=39 => (b ^ c ^ d, 0x6ed9eba1),
                40..=59 => ((b & c) | (b & d) | (c & d), 0x8f1bbcdc),
                _ => (b ^ c ^ d, 0xca62c1d6),
            };
            let next = a
                .rotate_left(5)
                .wrapping_add(f)
                .wrapping_add(e)
                .wrapping_add(k)
                .wrapping_add(*word);
            (a, b, c, d, e) = (next, a, b.rotate_left(30), c, d);
        }
        h0 = h0.wrapping_add(a);
        h1 = h1.wrapping_add(b);
        h2 = h2.wrapping_add(c);
        h3 = h3.wrapping_add(d);
        h4 = h4.wrapping_add(e);
    }
    let mut output = [0; 20];
    for (chunk, word) in output.chunks_exact_mut(4).zip([h0, h1, h2, h3, h4]) {
        chunk.copy_from_slice(&word.to_be_bytes());
    }
    output
}
