//! Go-compatible materialization of parser sections into retrieval chunks.

use std::{fs::OpenOptions, path::Path};

use rusqlite::{Connection, params};

use crate::SidecarError;

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

#[derive(Clone, Debug, Eq, PartialEq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub struct RetrievalDocument {
    pub path: String,
    pub hash: String,
    pub updated_at: String,
}

#[derive(Clone, Debug, PartialEq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub struct StoredRetrievalChunk {
    pub id: i64,
    pub uuid: String,
    pub document_path: String,
    pub chunk_index: i64,
    pub content: String,
    pub embedding: Vec<f32>,
    pub hash: String,
    pub norm: f32,
    pub dim: i64,
    #[serde(rename = "embedding_model")]
    pub model: String,
    pub char_start: Option<i64>,
    pub char_end: Option<i64>,
    pub anchor_kind: String,
    pub anchor_value: String,
    pub embedding_pending: bool,
}

#[derive(Clone, Debug, PartialEq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub struct RetrievalSearchResult {
    pub chunk: RetrievalSearchChunk,
    pub bm25_rank: usize,
}

#[derive(Clone, Debug, PartialEq, serde::Deserialize, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub struct RetrievalSearchChunk {
    pub id: i64,
    pub uuid: String,
    pub document_path: String,
    pub chunk_index: i64,
    pub content: String,
    pub embedding: Vec<f32>,
    pub hash: String,
}

/// Isolated provider-free retrieval storage using the Go database's schema.
pub struct RetrievalDb {
    connection: Connection,
}

const RETRIEVAL_MIGRATIONS: &[(&str, &str)] = &[
    (
        "0001_baseline",
        include_str!("../migrations/retrieval/0001_baseline.sql"),
    ),
    (
        "0002_meta",
        include_str!("../migrations/retrieval/0002_meta.sql"),
    ),
    (
        "0003_index_storage",
        include_str!("../migrations/retrieval/0003_index_storage.sql"),
    ),
    (
        "0004_binary_signature",
        include_str!("../migrations/retrieval/0004_binary_signature.sql"),
    ),
    (
        "0005_quantized_sidecar",
        include_str!("../migrations/retrieval/0005_quantized_sidecar.sql"),
    ),
    (
        "0006_folder_contexts",
        include_str!("../migrations/retrieval/0006_folder_contexts.sql"),
    ),
    (
        "0007_backfill_embedding_dim",
        include_str!("../migrations/retrieval/0007_backfill_embedding_dim.sql"),
    ),
    (
        "0008_chunk_spans",
        include_str!("../migrations/retrieval/0008_chunk_spans.sql"),
    ),
    (
        "0009_extractions",
        include_str!("../migrations/retrieval/0009_extractions.sql"),
    ),
    (
        "0010_embedding_pending",
        include_str!("../migrations/retrieval/0010_embedding_pending.sql"),
    ),
    (
        "0011_location_anchors",
        include_str!("../migrations/retrieval/0011_location_anchors.sql"),
    ),
    (
        "0012_german_norm",
        include_str!("../migrations/retrieval/0012_german_norm.sql"),
    ),
    (
        "0013_german_trigram",
        include_str!("../migrations/retrieval/0013_german_trigram.sql"),
    ),
];

impl RetrievalDb {
    /// Opens or creates a standalone retrieval database and applies Go-compatible migrations.
    pub fn open_at(path: impl AsRef<Path>) -> Result<Self, SidecarError> {
        let path = path.as_ref();
        if path.to_string_lossy().trim().is_empty() {
            return Err(SidecarError::Contract(
                "database path is required".to_owned(),
            ));
        }
        let path = if path.is_absolute() {
            path.to_path_buf()
        } else {
            std::env::current_dir()?.join(path)
        };
        if let Some(parent) = path.parent() {
            super::create_parent_dir(parent)?;
        }
        create_database_file(&path)?;
        let mut connection = Connection::open(path)?;
        connection.execute_batch(
            "PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON; PRAGMA journal_mode=WAL;",
        )?;
        migrate_retrieval(&mut connection)?;
        backfill_content_norm(&connection)?;
        Ok(Self { connection })
    }

    pub fn save_document(&self, document: &RetrievalDocument) -> Result<(), SidecarError> {
        self.connection.execute(
            "INSERT INTO documents (path, hash, updated_at) VALUES (?1, ?2, ?3)
             ON CONFLICT(path) DO UPDATE SET hash=excluded.hash, updated_at=excluded.updated_at",
            params![document.path, document.hash, document.updated_at],
        )?;
        Ok(())
    }

    pub fn save_chunks(&self, chunks: &[StoredRetrievalChunk]) -> Result<(), SidecarError> {
        let transaction = self.connection.unchecked_transaction()?;
        {
            let mut statement = transaction.prepare(
                "INSERT INTO chunks (uuid, document_path, chunk_index, content, embedding, hash, norm,
                 binary_signature, embedding_dim, embedding_model, char_start, char_end,
                 embedding_pending, anchor_kind, anchor_value, content_norm)
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, NULL, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15)",
            )?;
            for chunk in chunks {
                let embedding = encode_embedding(&chunk.embedding);
                statement.execute(params![
                    chunk.uuid,
                    chunk.document_path,
                    chunk.chunk_index,
                    chunk.content,
                    embedding,
                    chunk.hash,
                    embedding_norm(&chunk.embedding),
                    chunk.dim,
                    chunk.model,
                    chunk.char_start,
                    chunk.char_end,
                    i64::from(chunk.embedding_pending),
                    chunk.anchor_kind,
                    chunk.anchor_value,
                    symdesk_core::german::normalized_text(&chunk.content),
                ])?;
            }
        }
        transaction.commit()?;
        let _ = self.connection.execute(
            "UPDATE index_meta SET value = value + 1 WHERE key = 'generation'",
            [],
        );
        Ok(())
    }

    pub fn get_chunks_for_document(
        &self,
        document_path: &str,
    ) -> Result<Vec<StoredRetrievalChunk>, SidecarError> {
        let mut statement = self.connection.prepare(
            "SELECT id, uuid, document_path, chunk_index, content, embedding, hash, norm,
             embedding_dim, embedding_model, char_start, char_end, embedding_pending,
             anchor_kind, anchor_value FROM chunks WHERE document_path = ?1 ORDER BY chunk_index ASC",
        )?;
        let rows = statement.query_map([document_path], read_stored_chunk)?;
        rows.collect::<Result<Vec<_>, _>>().map_err(Into::into)
    }

    pub fn search_bm25(
        &self,
        query: &str,
        limit: i64,
    ) -> Result<Vec<RetrievalSearchResult>, SidecarError> {
        self.search_bm25_with_path(query, "", limit)
    }

    pub fn search_bm25_with_path(
        &self,
        query: &str,
        path_prefix: &str,
        limit: i64,
    ) -> Result<Vec<RetrievalSearchResult>, SidecarError> {
        let fts_query = symdesk_core::german::fts_query(query);
        if fts_query.is_empty() {
            return Ok(Vec::new());
        }
        let trigram_query = symdesk_core::german::trigram_query(query);
        let mut sql = String::from(
            "SELECT c.id, c.uuid, c.document_path, c.chunk_index, c.content, c.embedding, c.hash
             FROM chunks c JOIN (
                 SELECT rowid, MAX(bm) AS bm FROM (
                     SELECT rowid, bm25(chunks_fts) AS bm FROM chunks_fts WHERE chunks_fts MATCH ?1
                     UNION ALL SELECT rowid, NULL FROM chunks_norm WHERE chunks_norm MATCH ?2
                     UNION ALL SELECT rowid, NULL FROM chunks_tri WHERE chunks_tri MATCH ?3
                 ) GROUP BY rowid
             ) sm ON sm.rowid = c.id",
        );
        if !path_prefix.is_empty() {
            sql.push_str(
                " WHERE c.document_path LIKE ?4 || '%' ORDER BY sm.bm IS NULL, sm.bm ASC LIMIT ?5",
            );
        } else {
            sql.push_str(" ORDER BY sm.bm IS NULL, sm.bm ASC LIMIT ?4");
        }
        let mut statement = self.connection.prepare(&sql)?;
        let mut rank = 1;
        let mut results = Vec::new();
        let mut rows = if path_prefix.is_empty() {
            statement.query(params![fts_query, fts_query, trigram_query, limit])?
        } else {
            statement.query(params![
                fts_query,
                fts_query,
                trigram_query,
                path_prefix,
                limit
            ])?
        };
        while let Some(row) = rows.next()? {
            results.push(RetrievalSearchResult {
                chunk: RetrievalSearchChunk {
                    id: row.get(0)?,
                    uuid: row.get(1)?,
                    document_path: row.get(2)?,
                    chunk_index: row.get(3)?,
                    content: row.get(4)?,
                    embedding: decode_embedding(&row.get::<_, Vec<u8>>(5)?),
                    hash: row.get(6)?,
                },
                bm25_rank: rank,
            });
            rank += 1;
        }
        Ok(results)
    }
}

fn create_database_file(path: &Path) -> Result<(), SidecarError> {
    let mut options = OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    match options.open(path) {
        Ok(file) => drop(file),
        Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {}
        Err(error) => return Err(error.into()),
    }
    Ok(())
}

fn migrate_retrieval(connection: &mut Connection) -> Result<(), SidecarError> {
    connection.execute_batch(
        "CREATE TABLE IF NOT EXISTS schema_migrations (
             version TEXT PRIMARY KEY,
             applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
         )",
    )?;
    for (version, sql) in RETRIEVAL_MIGRATIONS {
        let applied: bool = connection.query_row(
            "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = ?1)",
            [version],
            |row| row.get(0),
        )?;
        if applied {
            continue;
        }
        let transaction = connection.transaction()?;
        transaction.execute_batch(sql)?;
        transaction.execute(
            "INSERT INTO schema_migrations(version) VALUES (?1)",
            [version],
        )?;
        transaction.commit()?;
    }
    Ok(())
}

fn backfill_content_norm(connection: &Connection) -> Result<(), SidecarError> {
    let mut statement =
        connection.prepare("SELECT id, content FROM chunks WHERE content_norm IS NULL")?;
    let rows = statement.query_map([], |row| {
        Ok((row.get::<_, i64>(0)?, row.get::<_, String>(1)?))
    })?;
    let missing = rows.collect::<Result<Vec<_>, _>>()?;
    drop(statement);
    if missing.is_empty() {
        return Ok(());
    }
    let transaction = connection.unchecked_transaction()?;
    for (id, content) in missing {
        transaction.execute(
            "UPDATE chunks SET content_norm = ?1 WHERE id = ?2",
            params![symdesk_core::german::normalized_text(&content), id],
        )?;
    }
    transaction.commit()?;
    Ok(())
}

fn read_stored_chunk(row: &rusqlite::Row<'_>) -> rusqlite::Result<StoredRetrievalChunk> {
    let bytes: Vec<u8> = row.get(5)?;
    Ok(StoredRetrievalChunk {
        id: row.get(0)?,
        uuid: row.get(1)?,
        document_path: row.get(2)?,
        chunk_index: row.get(3)?,
        content: row.get(4)?,
        embedding: decode_embedding(&bytes),
        hash: row.get(6)?,
        norm: row.get::<_, Option<f32>>(7)?.unwrap_or_default(),
        dim: row.get::<_, Option<i64>>(8)?.unwrap_or_default(),
        model: row.get::<_, Option<String>>(9)?.unwrap_or_default(),
        char_start: row.get(10)?,
        char_end: row.get(11)?,
        embedding_pending: row.get::<_, Option<i64>>(12)?.unwrap_or_default() != 0,
        anchor_kind: row.get::<_, Option<String>>(13)?.unwrap_or_default(),
        anchor_value: row.get::<_, Option<String>>(14)?.unwrap_or_default(),
    })
}

fn encode_embedding(values: &[f32]) -> Vec<u8> {
    values
        .iter()
        .flat_map(|value| value.to_le_bytes())
        .collect()
}

fn decode_embedding(bytes: &[u8]) -> Vec<f32> {
    if bytes.len() % 4 != 0 {
        return Vec::new();
    }
    bytes
        .chunks_exact(4)
        .map(|value| f32::from_le_bytes(value.try_into().expect("four-byte float")))
        .collect()
}

fn embedding_norm(values: &[f32]) -> f32 {
    values
        .iter()
        .map(|value| f64::from(*value * *value))
        .sum::<f64>()
        .sqrt() as f32
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
    for (part_start, part_end) in parts.iter().copied() {
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
                current.extend_from_slice(&tail);
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
