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
