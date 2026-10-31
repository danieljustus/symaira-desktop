//! Replays the Go-generated IVF/Hamming/heap vector fixture
//! (`internal/retrieval/internal/db/retrieval_ivf_fixture_test.go`): ranked
//! results, the generation counter and the persisted IVF snapshot after every
//! step of three scenarios.

use std::{fs, path::Path};

use rusqlite::{Connection, OptionalExtension};
use serde::Deserialize;
use serde_json::Value;
use sha2::{Digest, Sha256};
use symdesk_index::{
    RetrievalDb, RetrievalDocument, RetrievalVectorSearchResult, StoredRetrievalChunk,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    scenarios: Vec<Scenario>,
}

#[derive(Deserialize)]
struct Scenario {
    name: String,
    steps: Vec<Step>,
}

#[derive(Deserialize)]
struct Step {
    op: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    chunks: Vec<Chunk>,
    #[serde(default)]
    query: Vec<f32>,
    #[serde(default)]
    path_prefix: String,
    #[serde(default)]
    limit: i64,
    #[serde(default)]
    results: Vec<RetrievalVectorSearchResult>,
    state: State,
}

#[derive(Deserialize, Debug, PartialEq)]
struct State {
    generation: i64,
    chunks: i64,
    ivf_sha256: Option<String>,
    ivf_bytes: Option<i64>,
}

#[derive(Deserialize, Clone)]
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

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/retrieval/retrieval-ivf.json");
    serde_json::from_str(&fs::read_to_string(path).expect("read Go retrieval IVF fixture"))
        .expect("decode Go retrieval IVF fixture")
}

fn stored(chunk: &Chunk) -> StoredRetrievalChunk {
    StoredRetrievalChunk {
        id: 0,
        uuid: chunk.uuid.clone(),
        document_path: chunk.document_path.clone(),
        chunk_index: chunk.chunk_index,
        content: chunk.content.clone(),
        norm: 0.0,
        dim: chunk.dim,
        model: chunk.model.clone(),
        embedding: chunk.embedding.clone(),
        hash: chunk.hash.clone(),
        char_start: None,
        char_end: None,
        anchor_kind: String::new(),
        anchor_value: String::new(),
        embedding_pending: false,
    }
}

fn document(path: &str, prefix: &str) -> RetrievalDocument {
    RetrievalDocument {
        path: path.to_owned(),
        hash: format!("{prefix}{path}"),
        updated_at: "2026-01-02T03:04:05Z".to_owned(),
    }
}

fn state(path: &Path) -> State {
    let connection = Connection::open(path).expect("open state connection");
    let generation = connection
        .query_row(
            "SELECT value FROM index_meta WHERE key = 'generation'",
            [],
            |row| row.get(0),
        )
        .expect("read generation");
    let chunks = connection
        .query_row("SELECT COUNT(*) FROM chunks", [], |row| row.get(0))
        .expect("count chunks");
    let snapshot: Option<Vec<u8>> = connection
        .query_row(
            "SELECT data FROM index_storage WHERE key = 'ivf'",
            [],
            |row| row.get(0),
        )
        .optional()
        .expect("read IVF snapshot");
    State {
        generation,
        chunks,
        ivf_sha256: snapshot
            .as_ref()
            .map(|data| format!("{:x}", Sha256::digest(data))),
        ivf_bytes: snapshot.map(|data| i64::try_from(data.len()).expect("snapshot size")),
    }
}

fn to_value(value: impl serde::Serialize) -> Value {
    serde_json::to_value(value).expect("serialize vector result")
}

/// Exact top-k by cosine, ignoring the shortlist and the IVF prefilter.
fn exact_ids(chunks: &[Chunk], query: &[f32], limit: usize) -> Vec<String> {
    let norm = |values: &[f32]| values.iter().map(|v| f64::from(v * v)).sum::<f64>().sqrt();
    let mut scored = chunks
        .iter()
        .map(|chunk| {
            let dot: f64 = query
                .iter()
                .zip(&chunk.embedding)
                .map(|(a, b)| f64::from(a * b))
                .sum();
            let denominator = norm(query) * norm(&chunk.embedding);
            let score = if query.len() == chunk.embedding.len() && denominator > 0.0 {
                dot / denominator
            } else {
                0.0
            };
            (chunk.uuid.clone(), score)
        })
        .collect::<Vec<_>>();
    scored.sort_by(|a, b| b.1.partial_cmp(&a.1).expect("finite scores"));
    scored
        .into_iter()
        .take(limit)
        .map(|(uuid, _)| uuid)
        .collect()
}

#[test]
fn replays_go_ivf_hamming_and_heap_vector_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.scenarios.len(), 3);
    let mut approximate_searches = 0;
    let mut ivf_snapshots = 0;
    let mut searches = 0;
    for scenario in fixture.scenarios {
        let directory = std::env::temp_dir().join(format!(
            "symdesk-retrieval-ivf-{}-{}-{}",
            scenario.name,
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .expect("system time after epoch")
                .as_nanos()
        ));
        fs::create_dir_all(&directory).expect("create scenario directory");
        let path = directory.join("retrieval.db");
        let mut database: Option<RetrievalDb> = None;
        let mut live: Vec<Chunk> = Vec::new();
        for (number, step) in scenario.steps.iter().enumerate() {
            let label = format!("{} step {number} ({})", scenario.name, step.op);
            match step.op.as_str() {
                "open" => database = Some(RetrievalDb::open_at(&path).expect("open database")),
                "close" => drop(database.take()),
                "save" => {
                    let db = database.as_ref().expect("open database");
                    db.save_document(&document(&step.path, "doc-"))
                        .expect("save document");
                    let rows = step.chunks.iter().map(stored).collect::<Vec<_>>();
                    db.save_chunks(&rows).expect("save chunks");
                    live.extend(step.chunks.iter().cloned());
                }
                "delete" => {
                    database
                        .as_ref()
                        .expect("open database")
                        .delete_document(&step.path)
                        .expect("delete document");
                    live.retain(|chunk| chunk.document_path != step.path);
                }
                "replace" => {
                    let rows = step.chunks.iter().map(stored).collect::<Vec<_>>();
                    database
                        .as_ref()
                        .expect("open database")
                        .replace_document_chunks(&document(&step.path, "doc2-"), &rows)
                        .expect("replace document");
                    live.retain(|chunk| chunk.document_path != step.path);
                    live.extend(step.chunks.iter().cloned());
                }
                "search" => {
                    let db = database.as_ref().expect("open database");
                    let actual = if step.path_prefix.is_empty() {
                        db.search_vector(&step.query, step.limit)
                    } else {
                        db.search_vector_with_path(&step.query, &step.path_prefix, step.limit)
                    }
                    .expect("vector search");
                    searches += 1;
                    if step.path_prefix.is_empty() && !step.query.iter().all(|v| *v == 0.0) {
                        let ids = actual
                            .iter()
                            .map(|hit| hit.chunk.uuid.clone())
                            .collect::<Vec<_>>();
                        if ids != exact_ids(&live, &step.query, step.limit as usize) {
                            approximate_searches += 1;
                        }
                    }
                    assert_eq!(to_value(actual), to_value(&step.results), "{label}");
                }
                other => panic!("unknown fixture operation {other}"),
            }
            let actual_state = state(&path);
            assert_eq!(actual_state, step.state, "{label}: generation/IVF state");
            ivf_snapshots += usize::from(actual_state.ivf_sha256.is_some());
        }
        drop(database);
        let _ = fs::remove_dir_all(&directory);
    }
    // The fixture must exercise the approximate layer, not only exact scans:
    // some IVF/shortlist result differs from an exact ranking, and snapshots
    // were persisted along the way.
    assert!(searches >= 30, "fixture searches replayed: {searches}");
    assert!(
        approximate_searches > 0,
        "no search differed from the exact ranking; the IVF is not exercised"
    );
    assert!(ivf_snapshots > 10, "IVF snapshots seen: {ivf_snapshots}");
}
