use std::{collections::BTreeMap, fs, path::Path};

use serde::Deserialize;
use serde_json::{Value, json};
use symdesk_index::{
    RetrievalDb, RetrievalDocument, RetrievalHybridSearchResult, StoredRetrievalChunk,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle_commit: String,
    source_hashes: BTreeMap<String, String>,
    normalizations: Vec<String>,
    cases: Vec<SearchCase>,
}

#[derive(Deserialize)]
struct SearchCase {
    id: String,
    query: String,
    embedding: Vec<f32>,
    query_model: String,
    path_prefix: String,
    limit: i64,
    corpus: String,
    failure: Option<String>,
    chunks: Vec<Chunk>,
    results: Vec<Value>,
    warnings: Vec<String>,
    error_class: Option<String>,
    error: Option<String>,
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
    embedding_model: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq)]
struct ResultRow {
    id: i64,
    uuid: String,
    document_path: String,
    chunk_index: i64,
    content: String,
    hash: String,
    bm25_rank: usize,
    vector_rank: usize,
    rrf_score: f32,
    cosine_score: f32,
    metadata_matches: Option<Vec<String>>,
    vector_mode: Option<String>,
}

fn fixture() -> Fixture {
    let path =
        Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/retrieval/hybrid.json");
    serde_json::from_str(&fs::read_to_string(path).expect("read Go retrieval-hybrid fixture"))
        .expect("decode Go retrieval-hybrid fixture")
}

#[test]
fn replays_go_hybrid_retrieval_contracts() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.oracle_commit,
        "3c1ef32f7de92420a972d34f6067a9b5f2de63c9"
    );
    assert!(!fixture.normalizations.is_empty());
    assert!(
        fixture
            .normalizations
            .iter()
            .any(|rule| rule.contains("BM25 failure warning"))
    );
    assert!(
        fixture
            .normalizations
            .iter()
            .any(|rule| { rule.contains("mixed embedding error example pairs") })
    );
    assert!(
        fixture
            .source_hashes
            .values()
            .all(|hash| hash.len() == 64 && hash.bytes().all(|byte| byte.is_ascii_hexdigit()))
    );
    assert_eq!(
        fixture.cases.len(),
        10,
        "all named hybrid shapes must execute"
    );

    for case in fixture.cases {
        assert!(
            !case.corpus.is_empty(),
            "{} must name its seed corpus",
            case.id
        );
        let nonce = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("system time after epoch")
            .as_nanos();
        let path = std::env::temp_dir().join(format!(
            "symdesk-retrieval-hybrid-{}-{nonce}.db",
            std::process::id()
        ));
        let database = RetrievalDb::open_at(&path).expect("open isolated retrieval database");
        let chunks: Vec<_> = case
            .chunks
            .iter()
            .map(|chunk| StoredRetrievalChunk {
                id: 0,
                uuid: chunk.uuid.clone(),
                document_path: chunk.document_path.clone(),
                chunk_index: chunk.chunk_index,
                content: chunk.content.clone(),
                embedding: chunk.embedding.clone(),
                hash: chunk.hash.clone(),
                norm: 0.0,
                dim: chunk.dim,
                model: chunk.embedding_model.clone(),
                char_start: None,
                char_end: None,
                anchor_kind: String::new(),
                anchor_value: String::new(),
                embedding_pending: false,
            })
            .collect();
        for document_path in chunks
            .iter()
            .map(|chunk| chunk.document_path.as_str())
            .collect::<std::collections::BTreeSet<_>>()
        {
            database
                .save_document(&RetrievalDocument {
                    path: document_path.to_owned(),
                    hash: format!("doc-{document_path}"),
                    updated_at: "2026-01-02T03:04:05Z".to_owned(),
                })
                .expect("save retrieval document");
        }
        database
            .save_chunks(&chunks)
            .expect("save Go-seeded chunks");

        if case.failure.as_deref() == Some("bm25") {
            let connection = rusqlite::Connection::open(&path).expect("open fault-injection view");
            connection
                .execute_batch("DROP TABLE chunks_fts")
                .expect("make only the BM25 index unavailable");
        } else if case.failure.as_deref() == Some("vector") {
            let connection = rusqlite::Connection::open(&path).expect("open fault-injection view");
            connection
                .execute_batch(
                    "ALTER TABLE chunks RENAME COLUMN embedding TO unavailable_embedding",
                )
                .expect("make vector scan fail after mixed-space detection");
        }

        let actual = database.search_hybrid_with_path(
            &case.query,
            &case.embedding,
            &case.query_model,
            &case.path_prefix,
            case.limit,
        );
        match (case.error_class.as_deref(), actual) {
            (Some("mixed_embedding_spaces"), Err(error)) => {
                assert_eq!(
                    error.to_string(),
                    case.error.as_deref().unwrap(),
                    "{}",
                    case.id
                );
            }
            (Some("vector_failure"), Err(error)) => {
                assert!(
                    error.to_string().contains("no such column"),
                    "{}: {error}",
                    case.id
                );
            }
            (Some(class), other) => panic!("{} expected {class}, got {other:?}", case.id),
            (None, Ok(response)) => {
                let actual_value = to_value(
                    response
                        .results
                        .iter()
                        .map(project_result)
                        .collect::<Vec<_>>(),
                );
                let expected_value = to_value(&case.results);
                let mut actual_rows: Vec<ResultRow> =
                    serde_json::from_value(actual_value).expect("decode actual hybrid rows");
                let mut expected_rows: Vec<ResultRow> =
                    serde_json::from_value(expected_value).expect("decode Go fixture hybrid rows");
                assert_descending_scores(&actual_rows, &case.id, "Rust");
                assert_descending_scores(&expected_rows, &case.id, "Go fixture");
                assert_eq!(
                    canonicalize_ties(&mut actual_rows),
                    canonicalize_ties(&mut expected_rows),
                    "{} ({})",
                    case.id,
                    case.corpus
                );
                assert_eq!(
                    canonical_warnings(&response.warnings),
                    canonical_warnings(&case.warnings),
                    "{} warning behavior",
                    case.id
                );
                match case.id.as_str() {
                    "keyword-leg" => assert!(case.results.iter().any(|row| row["bm25_rank"] != 0)),
                    "semantic-leg" => assert!(case.results.iter().all(|row| row["bm25_rank"] == 0)),
                    "overlap-and-metadata" => assert!(
                        case.results
                            .iter()
                            .any(|row| { row["bm25_rank"] != 0 && row["vector_rank"] != 0 })
                    ),
                    "equal-rrf-tie" => {
                        assert_eq!(case.results[0]["rrf_score"], case.results[1]["rrf_score"])
                    }
                    "limit-and-path" => {
                        assert_eq!(case.results.len(), 1);
                        assert!(
                            case.results[0]["document_path"]
                                .as_str()
                                .unwrap()
                                .starts_with(&case.path_prefix)
                        );
                    }
                    _ => {}
                }
            }
            (None, Err(error)) => panic!("{} unexpectedly failed: {error}", case.id),
        }

        drop(database);
        let _ = fs::remove_file(&path);
        let _ = fs::remove_file(path.with_extension("db-wal"));
        let _ = fs::remove_file(path.with_extension("db-shm"));
    }
}

fn canonical_warnings(warnings: &[String]) -> Vec<String> {
    warnings
        .iter()
        .map(|warning| {
            if warning.starts_with("warning: BM25 search failed, falling back to vector-only: ") {
                "bm25_fallback".to_owned()
            } else {
                warning.clone()
            }
        })
        .collect()
}

fn project_result(result: &RetrievalHybridSearchResult) -> Value {
    let mut value = json!({
        "id": result.chunk.id,
        "uuid": result.chunk.uuid,
        "document_path": result.chunk.document_path,
        "chunk_index": result.chunk.chunk_index,
        "content": result.chunk.content,
        "hash": result.chunk.hash,
        "bm25_rank": result.bm25_rank,
        "vector_rank": result.vector_rank,
        "rrf_score": result.rrf_score,
        "cosine_score": result.cosine_score,
        "metadata_matches": result.metadata_matches,
        "vector_mode": result.vector_mode,
    });
    let object = value.as_object_mut().expect("object result");
    if result.metadata_matches.is_empty() {
        object.remove("metadata_matches");
    }
    if result.vector_mode.is_empty() {
        object.remove("vector_mode");
    }
    value
}

fn assert_descending_scores(results: &[ResultRow], case_id: &str, source: &str) {
    for pair in results.windows(2) {
        assert!(
            pair[0].rrf_score >= pair[1].rrf_score,
            "{case_id} {source} results not sorted by descending score: {} then {}",
            pair[0].rrf_score,
            pair[1].rrf_score
        );
    }
}

fn canonicalize_ties(results: &mut [ResultRow]) -> Vec<ResultRow> {
    let mut start = 0;
    while start < results.len() {
        let score = results[start].rrf_score;
        let mut end = start + 1;
        while end < results.len() && results[end].rrf_score == score {
            end += 1;
        }
        results[start..end].sort_by(|left, right| left.uuid.cmp(&right.uuid));
        start = end;
    }
    results.to_vec()
}

fn to_value(value: impl serde::Serialize) -> Value {
    serde_json::to_value(value).expect("serialize hybrid result")
}
