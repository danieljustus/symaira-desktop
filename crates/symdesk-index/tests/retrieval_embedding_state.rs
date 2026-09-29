use std::{fs, path::Path};

use serde::Deserialize;
use symdesk_index::{
    RetrievalDb, RetrievalDocument, RetrievalEmbeddingSpaceCount, StoredRetrievalChunk,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    documents: Vec<Document>,
    chunks: Vec<Chunk>,
    legacy_null_uuids: Vec<String>,
    pending_total: i64,
    pending_by_document: Vec<PendingCount>,
    embedding_spaces: Vec<RetrievalEmbeddingSpaceCount>,
}

#[derive(Deserialize)]
struct Document {
    path: String,
    hash: String,
}

#[derive(Deserialize)]
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
    embedding_pending: bool,
}

#[derive(Deserialize)]
struct PendingCount {
    path: String,
    count: i64,
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/retrieval/embedding-state.json");
    serde_json::from_str(&fs::read_to_string(path).expect("read Go embedding-state fixture"))
        .expect("decode Go embedding-state fixture")
}

#[test]
fn matches_go_pending_counts_and_embedding_spaces() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("system time after epoch")
        .as_nanos();
    let path = std::env::temp_dir().join(format!(
        "symdesk-embedding-state-{}-{nonce}.db",
        std::process::id()
    ));
    let database = RetrievalDb::open_at(&path).expect("open isolated retrieval database");
    for document in &fixture.documents {
        database
            .save_document(&RetrievalDocument {
                path: document.path.clone(),
                hash: document.hash.clone(),
                updated_at: "1970-01-01T00:00:00Z".to_owned(),
            })
            .expect("save retrieval document");
    }
    let chunks: Vec<StoredRetrievalChunk> = fixture
        .chunks
        .into_iter()
        .map(|chunk| StoredRetrievalChunk {
            id: 0,
            uuid: chunk.uuid,
            document_path: chunk.document_path,
            chunk_index: chunk.chunk_index,
            content: chunk.content,
            embedding: chunk.embedding,
            hash: chunk.hash,
            norm: 0.0,
            dim: chunk.dim,
            model: chunk.model,
            char_start: None,
            char_end: None,
            anchor_kind: String::new(),
            anchor_value: String::new(),
            embedding_pending: chunk.embedding_pending,
        })
        .collect();
    database
        .save_chunks(&chunks)
        .expect("save retrieval chunks");
    if !fixture.legacy_null_uuids.is_empty() {
        let raw = rusqlite::Connection::open(&path).expect("open legacy-row fixture view");
        for uuid in &fixture.legacy_null_uuids {
            raw.execute(
                "UPDATE chunks SET embedding_dim = NULL, embedding_model = NULL WHERE uuid = ?1",
                [uuid],
            )
            .expect("restore legacy NULL embedding metadata");
        }
    }

    assert_eq!(
        database
            .count_pending_chunks()
            .expect("count pending chunks"),
        fixture.pending_total
    );
    for expected in &fixture.pending_by_document {
        assert_eq!(
            database
                .count_pending_chunks_for_document(&expected.path)
                .expect("count document pending chunks"),
            expected.count,
            "{}",
            expected.path
        );
    }
    assert_eq!(
        database
            .detect_mixed_embedding_spaces()
            .expect("detect embedding spaces"),
        fixture.embedding_spaces
    );
    drop(database);
    let _ = fs::remove_file(path);
}

#[test]
fn replaces_one_document_and_rolls_back_failed_rebuild() {
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("system time after epoch")
        .as_nanos();
    let path = std::env::temp_dir().join(format!(
        "symdesk-pending-replacement-{}-{nonce}.db",
        std::process::id()
    ));
    let database = RetrievalDb::open_at(&path).expect("open isolated retrieval database");
    let old = StoredRetrievalChunk {
        id: 0,
        uuid: "old-chunk".to_owned(),
        document_path: "/vault/a.md".to_owned(),
        chunk_index: 0,
        content: "old pending content".to_owned(),
        embedding: vec![],
        hash: "old-hash".to_owned(),
        norm: 0.0,
        dim: 0,
        model: "local-hash".to_owned(),
        char_start: Some(0),
        char_end: Some(19),
        anchor_kind: "text".to_owned(),
        anchor_value: "offset:0".to_owned(),
        embedding_pending: true,
    };
    let unrelated = StoredRetrievalChunk {
        uuid: "other-chunk".to_owned(),
        document_path: "/vault/b.md".to_owned(),
        content: "unrelated document marker".to_owned(),
        ..old.clone()
    };
    database
        .save_document(&RetrievalDocument {
            path: "/vault/a.md".to_owned(),
            hash: "old-doc-hash".to_owned(),
            updated_at: "2026-09-01T00:00:00Z".to_owned(),
        })
        .expect("save original document");
    database
        .save_document(&RetrievalDocument {
            path: "/vault/b.md".to_owned(),
            hash: "other-doc-hash".to_owned(),
            updated_at: "2026-09-01T00:00:00Z".to_owned(),
        })
        .expect("save unrelated document");
    database
        .save_chunks(&[old.clone(), unrelated.clone()])
        .expect("save original chunks");
    let raw = rusqlite::Connection::open(&path).expect("open extraction fixture view");
    raw.execute(
        "INSERT INTO extractions (document_path, class, value, evidence_text, created_at)
         VALUES ('/vault/a.md', 'fact', 'old fact', 'old evidence', '2026-09-01T00:00:00Z')",
        [],
    )
    .expect("insert document extraction");
    drop(raw);

    let replacement = StoredRetrievalChunk {
        uuid: "resolved-chunk".to_owned(),
        content: "rebuilt semantic content".to_owned(),
        embedding: vec![0.25, 0.75],
        hash: "new-hash".to_owned(),
        norm: 0.0,
        dim: 2,
        model: "fixture-model".to_owned(),
        char_end: Some(25),
        embedding_pending: false,
        ..old.clone()
    };
    let document = RetrievalDocument {
        path: "/vault/a.md".to_owned(),
        hash: "new-doc-hash".to_owned(),
        updated_at: "2026-09-02T00:00:00Z".to_owned(),
    };
    database
        .replace_document_chunks(&document, &[replacement.clone()])
        .expect("replace pending document chunks");
    assert_eq!(
        database
            .get_chunks_for_document("/vault/a.md")
            .expect("read replaced chunks"),
        vec![StoredRetrievalChunk {
            id: 3,
            norm: 0.7905694,
            ..replacement.clone()
        }]
    );
    assert_eq!(
        database
            .get_chunks_for_document("/vault/b.md")
            .expect("read unrelated chunks")[0]
            .uuid,
        unrelated.uuid
    );
    assert_eq!(database.count_pending_chunks().expect("pending count"), 1);
    let raw = rusqlite::Connection::open(&path).expect("check Go-compatible rebuild effects");
    let generation: i64 = raw
        .query_row(
            "SELECT value FROM index_meta WHERE key = 'generation'",
            [],
            |row| row.get(0),
        )
        .expect("read generation");
    assert_eq!(generation, 3, "save plus Go-equivalent delete/save bumps");
    let extractions: i64 = raw
        .query_row(
            "SELECT COUNT(*) FROM extractions WHERE document_path = '/vault/a.md'",
            [],
            |row| row.get(0),
        )
        .expect("count removed extractions");
    assert_eq!(extractions, 0, "Go rebuild deletes document extractions");
    let old_fts_hits: i64 = raw
        .query_row(
            "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'pending'",
            [],
            |row| row.get(0),
        )
        .expect("search deleted chunk content");
    let new_fts_hits: i64 = raw
        .query_row(
            "SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH 'semantic'",
            [],
            |row| row.get(0),
        )
        .expect("search replacement chunk content");
    assert_eq!(old_fts_hits, 0, "deleted chunk text leaves FTS");
    assert_eq!(new_fts_hits, 1, "replacement chunk text enters FTS");
    raw.execute(
        "INSERT INTO extractions (document_path, class, value, evidence_text, created_at)
         VALUES ('/vault/a.md', 'fact', 'rollback fact', 'rollback evidence', '2026-09-02T00:00:00Z')",
        [],
    )
    .expect("insert extraction for rollback check");
    drop(raw);

    let raw = rusqlite::Connection::open(&path).expect("open replacement failure trigger");
    raw.execute_batch(
        "CREATE TRIGGER reject_replacement BEFORE INSERT ON chunks
         WHEN NEW.uuid = 'reject-chunk' BEGIN SELECT RAISE(ABORT, 'fixture failure'); END;",
    )
    .expect("install deterministic insert failure");
    drop(raw);
    let failed = StoredRetrievalChunk {
        uuid: "reject-chunk".to_owned(),
        ..replacement
    };
    assert!(
        database
            .replace_document_chunks(
                &RetrievalDocument {
                    hash: "should-rollback".to_owned(),
                    ..document.clone()
                },
                &[failed]
            )
            .is_err()
    );
    let raw = rusqlite::Connection::open(&path).expect("verify extraction rollback");
    let extraction_count: i64 = raw
        .query_row(
            "SELECT COUNT(*) FROM extractions WHERE document_path = '/vault/a.md'",
            [],
            |row| row.get(0),
        )
        .expect("count rolled-back extraction");
    assert_eq!(extraction_count, 1);
    drop(raw);
    assert_eq!(
        database
            .get_chunks_for_document("/vault/a.md")
            .expect("read rolled-back chunks")[0]
            .uuid,
        "resolved-chunk"
    );
    let raw = rusqlite::Connection::open(&path).expect("check rolled-back metadata");
    let hash: String = raw
        .query_row(
            "SELECT hash FROM documents WHERE path = '/vault/a.md'",
            [],
            |row| row.get(0),
        )
        .expect("read document hash");
    assert_eq!(hash, "new-doc-hash");
    drop(raw);
    drop(database);
    let _ = fs::remove_file(path);
}
