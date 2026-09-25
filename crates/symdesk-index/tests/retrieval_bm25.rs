use std::{fs, path::Path};

use serde::Deserialize;
use serde_json::Value;
use symdesk_index::{RetrievalDb, RetrievalDocument, RetrievalSearchResult, StoredRetrievalChunk};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    migrations: Vec<String>,
    documents: Vec<RetrievalDocument>,
    chunks: Vec<StoredRetrievalChunk>,
    by_document: Vec<DocumentRows>,
    searches: Vec<SearchCase>,
}

#[derive(Deserialize)]
struct DocumentRows {
    path: String,
    chunks: Vec<StoredRetrievalChunk>,
}

#[derive(Deserialize)]
struct SearchCase {
    query: String,
    path: String,
    limit: i64,
    results: Vec<RetrievalSearchResult>,
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/retrieval/retrieval-bm25.json");
    serde_json::from_str(&fs::read_to_string(path).expect("read Go retrieval fixture"))
        .expect("decode Go retrieval fixture")
}

#[test]
fn replays_go_retrieval_storage_and_bm25_contract() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.migrations.len(), 13);
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("system time after epoch")
        .as_nanos();
    let path = std::env::temp_dir().join(format!(
        "symdesk-retrieval-bm25-{}-{nonce}.db",
        std::process::id()
    ));
    let database = RetrievalDb::open_at(&path).expect("open isolated retrieval database");
    for document in &fixture.documents {
        database
            .save_document(document)
            .expect("save retrieval document");
    }
    database
        .save_chunks(&fixture.chunks)
        .expect("save provider-free chunks");

    let migrations: Vec<String> = {
        let connection = rusqlite::Connection::open(&path).expect("open migration view");
        let mut statement = connection
            .prepare("SELECT version FROM schema_migrations ORDER BY version")
            .expect("query applied migrations");
        statement
            .query_map([], |row| row.get(0))
            .expect("read applied migrations")
            .collect::<Result<_, _>>()
            .expect("collect applied migrations")
    };
    assert_eq!(migrations, fixture.migrations);

    for expected in &fixture.by_document {
        let actual = database
            .get_chunks_for_document(&expected.path)
            .expect("read document chunks");
        assert_eq!(
            to_value(actual),
            to_value(&expected.chunks),
            "{}",
            expected.path
        );
    }
    for case in &fixture.searches {
        let actual = if case.path.is_empty() {
            database.search_bm25(&case.query, case.limit)
        } else {
            database.search_bm25_with_path(&case.query, &case.path, case.limit)
        }
        .expect("run BM25 search");
        assert_eq!(
            to_value(actual),
            to_value(&case.results),
            "{} {:?}",
            case.query,
            case.path
        );
    }
    drop(database);
    let _ = fs::remove_file(&path);
    let _ = fs::remove_file(path.with_extension("db-wal"));
    let _ = fs::remove_file(path.with_extension("db-shm"));
}

fn to_value(value: impl serde::Serialize) -> Value {
    serde_json::to_value(value).expect("serialize retrieval result")
}
