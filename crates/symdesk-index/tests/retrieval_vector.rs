use std::{fs, path::Path};

use serde::Deserialize;
use serde_json::Value;
use symdesk_index::{
    RetrievalDb, RetrievalDocument, RetrievalVectorSearchResult, StoredRetrievalChunk,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    chunks: Vec<Chunk>,
    searches: Vec<SearchCase>,
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
}

#[derive(Deserialize)]
struct SearchCase {
    query: Vec<f32>,
    path_prefix: String,
    limit: i64,
    results: Vec<RetrievalVectorSearchResult>,
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/retrieval/retrieval-vector.json");
    serde_json::from_str(&fs::read_to_string(path).expect("read Go retrieval vector fixture"))
        .expect("decode Go retrieval vector fixture")
}

#[test]
fn replays_go_retrieval_vector_full_scan_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("system time after epoch")
        .as_nanos();
    let path = std::env::temp_dir().join(format!(
        "symdesk-retrieval-vector-{}-{nonce}.db",
        std::process::id()
    ));
    let database = RetrievalDb::open_at(&path).expect("open isolated retrieval database");
    let chunks: Vec<_> = fixture
        .chunks
        .into_iter()
        .map(|chunk| StoredRetrievalChunk {
            id: 0,
            uuid: chunk.uuid,
            document_path: chunk.document_path,
            chunk_index: chunk.chunk_index,
            content: chunk.content,
            norm: 0.0,
            dim: chunk.dim,
            model: chunk.model,
            embedding: chunk.embedding,
            hash: chunk.hash,
            char_start: None,
            char_end: None,
            anchor_kind: String::new(),
            anchor_value: String::new(),
            embedding_pending: false,
        })
        .collect();
    for path in chunks
        .iter()
        .map(|chunk| chunk.document_path.as_str())
        .collect::<std::collections::BTreeSet<_>>()
    {
        database
            .save_document(&RetrievalDocument {
                path: path.to_owned(),
                hash: format!("doc-{path}"),
                updated_at: "2026-01-02T03:04:05Z".to_owned(),
            })
            .expect("save retrieval document");
    }
    database
        .save_chunks(&chunks)
        .expect("save provider-free chunks");

    for case in fixture.searches {
        let actual = if case.path_prefix.is_empty() {
            database.search_vector(&case.query, case.limit)
        } else {
            database.search_vector_with_path(&case.query, &case.path_prefix, case.limit)
        }
        .expect("run full-scan vector search");
        assert_eq!(to_value(actual), to_value(case.results));
    }
    drop(database);
    let _ = fs::remove_file(&path);
    let _ = fs::remove_file(path.with_extension("db-wal"));
    let _ = fs::remove_file(path.with_extension("db-shm"));
}

fn to_value(value: impl serde::Serialize) -> Value {
    serde_json::to_value(value).expect("serialize retrieval vector result")
}
