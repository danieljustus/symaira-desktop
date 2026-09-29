use std::{fs, path::Path};

use serde::Deserialize;
use symdesk_index::{RetrievalDb, RetrievalDocument, StoredRetrievalChunk};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    document_hash: String,
    pending_chunks: Vec<Chunk>,
    resolved_chunks: Vec<Chunk>,
    reembedded_documents: usize,
    generation: i64,
    remaining_extractions: i64,
    old_text_fts_hits: i64,
    new_text_fts_hits: i64,
}

#[derive(Clone, Deserialize)]
struct Chunk {
    uuid: String,
    document_path: String,
    chunk_index: i64,
    content: String,
    embedding: Vec<f32>,
    hash: String,
    dim: i64,
    #[serde(rename = "embedding_model")]
    model: String,
    char_start: Option<i64>,
    char_end: Option<i64>,
    anchor_kind: String,
    anchor_value: String,
    embedding_pending: bool,
}

fn stored(chunk: &Chunk) -> StoredRetrievalChunk {
    StoredRetrievalChunk {
        id: 0,
        uuid: chunk.uuid.clone(),
        document_path: chunk.document_path.clone(),
        chunk_index: chunk.chunk_index,
        content: chunk.content.clone(),
        embedding: chunk.embedding.clone(),
        hash: chunk.hash.clone(),
        norm: 0.0,
        dim: chunk.dim,
        model: chunk.model.clone(),
        char_start: chunk.char_start,
        char_end: chunk.char_end,
        anchor_kind: chunk.anchor_kind.clone(),
        anchor_value: chunk.anchor_value.clone(),
        embedding_pending: chunk.embedding_pending,
    }
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/retrieval/pending-rebuild.json");
    serde_json::from_str(&fs::read_to_string(path).expect("read Go pending rebuild fixture"))
        .expect("decode Go pending rebuild fixture")
}

#[test]
fn replaces_pending_document_with_go_fake_embedder_output() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.reembedded_documents, 1);
    assert!(!fixture.pending_chunks.is_empty());
    assert!(!fixture.resolved_chunks.is_empty());
    assert!(fixture.pending_chunks.iter().all(|chunk| {
        chunk.embedding_pending && chunk.embedding.is_empty() && chunk.model == "local-hash"
    }));
    assert!(fixture.resolved_chunks.iter().all(|chunk| {
        !chunk.embedding_pending && !chunk.embedding.is_empty() && chunk.model == "fake-model"
    }));

    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("system time after epoch")
        .as_nanos();
    let path = std::env::temp_dir().join(format!(
        "symdesk-pending-rebuild-{}-{nonce}.db",
        std::process::id()
    ));
    let database = RetrievalDb::open_at(&path).expect("open isolated retrieval database");
    let pending: Vec<_> = fixture.pending_chunks.iter().map(stored).collect();
    database
        .save_document(&RetrievalDocument {
            path: "$DOC".to_owned(),
            hash: "old-pending-hash".to_owned(),
            updated_at: "2026-09-01T00:00:00Z".to_owned(),
        })
        .expect("save pending document");
    database
        .save_chunks(&pending)
        .expect("save Go pending chunks");
    let raw = rusqlite::Connection::open(&path).expect("open Go extraction state");
    raw.execute(
        "INSERT INTO extractions (document_path, class, value, evidence_text, created_at)
         VALUES ('$DOC', 'fact', 'old value', 'old evidence', '2026-09-01T00:00:00Z')",
        [],
    )
    .expect("save source document extraction");
    drop(raw);
    let resolved: Vec<_> = fixture.resolved_chunks.iter().map(stored).collect();
    database
        .replace_document_chunks(
            &RetrievalDocument {
                path: "$DOC".to_owned(),
                hash: fixture.document_hash,
                updated_at: "2026-09-02T00:00:00Z".to_owned(),
            },
            &resolved,
        )
        .expect("replace pending chunks with Go oracle output");
    let actual = database
        .get_chunks_for_document("$DOC")
        .expect("read replacement");
    assert_eq!(actual.len(), fixture.resolved_chunks.len());
    for (actual, expected) in actual.iter().zip(&fixture.resolved_chunks) {
        assert_eq!(actual.uuid, expected.uuid);
        assert_eq!(actual.chunk_index, expected.chunk_index);
        assert_eq!(actual.content, expected.content);
        assert_eq!(actual.embedding, expected.embedding);
        assert_eq!(actual.hash, expected.hash);
        assert_eq!(actual.dim, expected.dim);
        assert_eq!(actual.model, expected.model);
        assert_eq!(actual.char_start, expected.char_start);
        assert_eq!(actual.char_end, expected.char_end);
        assert_eq!(actual.anchor_kind, expected.anchor_kind);
        assert_eq!(actual.anchor_value, expected.anchor_value);
        assert_eq!(actual.embedding_pending, expected.embedding_pending);
    }
    assert_eq!(database.count_pending_chunks().expect("pending count"), 0);
    let raw = rusqlite::Connection::open(&path).expect("inspect replacement side effects");
    let generation: i64 = raw
        .query_row(
            "SELECT value FROM index_meta WHERE key = 'generation'",
            [],
            |row| row.get(0),
        )
        .expect("read generation");
    assert_eq!(generation, fixture.generation);
    let extractions: i64 = raw
        .query_row(
            "SELECT COUNT(*) FROM extractions WHERE document_path = '$DOC'",
            [],
            |row| row.get(0),
        )
        .expect("count deleted extractions");
    assert_eq!(extractions, fixture.remaining_extractions);
    let old_fts_hits: i64 = raw
        .query_row(
            "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'staleonlymarker'",
            [],
            |row| row.get(0),
        )
        .expect("search stale content");
    assert_eq!(old_fts_hits, fixture.old_text_fts_hits);
    let new_fts_hits: i64 = raw
        .query_row(
            "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'freshonlymarker'",
            [],
            |row| row.get(0),
        )
        .expect("search fresh content");
    assert_eq!(new_fts_hits, fixture.new_text_fts_hits);
    drop(raw);
    drop(database);
    let _ = fs::remove_file(path);
}
