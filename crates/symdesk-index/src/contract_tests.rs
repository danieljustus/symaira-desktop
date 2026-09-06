use std::collections::HashMap;

use rusqlite::{Connection, types::ValueRef};
use serde::Deserialize;
use serde_json::{Value, json};

use super::{IndexedDocument, MIGRATIONS, SearchHit, Sidecar};

const GO_MIGRATIONS: &[(&str, &str)] = &[
    (
        "001_init",
        include_str!("../../../internal/sidecar/migrations/001_init.sql"),
    ),
    (
        "002_doc_metadata",
        include_str!("../../../internal/sidecar/migrations/002_doc_metadata.sql"),
    ),
    (
        "003_asn",
        include_str!("../../../internal/sidecar/migrations/003_asn.sql"),
    ),
    (
        "004_file_stat_cache",
        include_str!("../../../internal/sidecar/migrations/004_file_stat_cache.sql"),
    ),
    (
        "005_type",
        include_str!("../../../internal/sidecar/migrations/005_type.sql"),
    ),
    (
        "006_links_to_path_index",
        include_str!("../../../internal/sidecar/migrations/006_links_to_path_index.sql"),
    ),
    (
        "007_created_at",
        include_str!("../../../internal/sidecar/migrations/007_created_at.sql"),
    ),
    (
        "008_index_lifecycle",
        include_str!("../../../internal/sidecar/migrations/008_index_lifecycle.sql"),
    ),
    (
        "009_german_norm",
        include_str!("../../../internal/sidecar/migrations/009_german_norm.sql"),
    ),
    (
        "010_german_trigram",
        include_str!("../../../internal/sidecar/migrations/010_german_trigram.sql"),
    ),
    (
        "011_dataset_rows",
        include_str!("../../../internal/sidecar/migrations/011_dataset_rows.sql"),
    ),
];

#[test]
fn rust_migration_bytes_match_go_oracle() {
    assert_eq!(MIGRATIONS, GO_MIGRATIONS);
}

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    documents: Vec<DocumentInput>,
    updated_input: DocumentInput,
    batch_inputs: Vec<DocumentInput>,
    pragmas: HashMap<String, String>,
    migrations: Vec<String>,
    schema: Vec<SchemaObject>,
    initial: Value,
    searches: Vec<SearchCase>,
    updated: Value,
    deleted: Value,
    batch_error: String,
    after_batch_error: Value,
}

#[derive(Clone, Deserialize)]
struct DocumentInput {
    path: String,
    markdown: String,
    mtime_ns: i64,
}

#[derive(Deserialize)]
struct SchemaObject {
    #[serde(rename = "type")]
    object_type: String,
    name: String,
    sql: String,
}

#[derive(Deserialize)]
struct SearchCase {
    query: String,
    scoped: bool,
    #[serde(default)]
    allowed: Vec<String>,
    hits: Vec<SearchHitFixture>,
}

#[derive(Deserialize)]
struct SearchHitFixture {
    path: String,
    title: String,
    snippet: String,
}

#[test]
fn go_sidecar_contract_matches_rust() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/sidecar/contracts.json"
    ))
    .expect("decode sidecar fixture");
    assert_eq!(fixture.schema_version, 1);
    let directory = std::env::temp_dir().join(format!("symdesk-index-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&directory);
    let path = directory.join("sidecar.db");
    let mut sidecar = Sidecar::open(&path).expect("open sidecar");
    assert_eq!(pragmas(&sidecar.connection), fixture.pragmas);
    assert_eq!(migrations(&sidecar.connection), fixture.migrations);

    let actual_schema = schema(&sidecar.connection);
    assert_eq!(
        actual_schema
            .iter()
            .map(|entry| (&entry.object_type, &entry.name))
            .collect::<Vec<_>>(),
        fixture
            .schema
            .iter()
            .map(|entry| (&entry.object_type, &entry.name))
            .collect::<Vec<_>>()
    );
    for expected in fixture.schema.iter().filter(|entry| {
        !entry.name.starts_with("fts_search_")
            && !entry.name.starts_with("fts_norm_")
            && !entry.name.starts_with("fts_tri_")
    }) {
        let actual = actual_schema
            .iter()
            .find(|entry| entry.name == expected.name)
            .expect("schema object exists");
        assert_eq!(actual.sql, expected.sql, "schema SQL for {}", expected.name);
    }

    let initial = fixture.documents.iter().map(indexed).collect::<Vec<_>>();
    sidecar.index_documents(&initial).expect("initial index");
    assert_eq!(snapshot(&sidecar.connection), fixture.initial);

    for search in fixture.searches {
        let hits = if search.scoped {
            sidecar
                .search_scoped(&search.query, &search.allowed)
                .expect("scoped search")
        } else {
            sidecar.search(&search.query).expect("search")
        };
        assert_eq!(
            hits,
            search
                .hits
                .into_iter()
                .map(|hit| SearchHit {
                    path: hit.path,
                    title: hit.title,
                    snippet: hit.snippet,
                })
                .collect::<Vec<_>>(),
            "search {:?}",
            search.query
        );
    }

    sidecar
        .index_document(&indexed(&fixture.updated_input))
        .expect("update document");
    assert_eq!(snapshot(&sidecar.connection), fixture.updated);
    sidecar
        .delete_document("vault/link.md")
        .expect("delete document");
    assert_eq!(snapshot(&sidecar.connection), fixture.deleted);

    let mut batch = fixture.batch_inputs.iter().map(indexed).collect::<Vec<_>>();
    batch[1].asn = Some(0);
    let error = sidecar
        .index_documents(&batch)
        .expect_err("invalid ASN must fail");
    assert_eq!(error.to_string(), fixture.batch_error);
    assert_eq!(snapshot(&sidecar.connection), fixture.after_batch_error);
    sidecar.check_integrity().expect("integrity check");
    drop(sidecar);
    let _ = std::fs::remove_dir_all(directory);
}

fn indexed(input: &DocumentInput) -> IndexedDocument {
    let document = symdesk_vault::parse_bytes(&input.path, input.markdown.as_bytes())
        .expect("parse fixture document");
    IndexedDocument::from_vault(&document, Some(input.mtime_ns)).expect("convert document")
}

fn pragmas(connection: &Connection) -> HashMap<String, String> {
    [
        "journal_mode",
        "foreign_keys",
        "busy_timeout",
        "integrity_check",
    ]
    .into_iter()
    .map(|name| {
        let value = connection
            .query_row(&format!("PRAGMA {name}"), [], |row| {
                Ok(match row.get_ref(0)? {
                    ValueRef::Integer(value) => value.to_string(),
                    ValueRef::Text(value) => String::from_utf8_lossy(value).into_owned(),
                    value => format!("{value:?}"),
                })
            })
            .expect("pragma");
        (name.to_owned(), value)
    })
    .collect()
}

fn migrations(connection: &Connection) -> Vec<String> {
    let mut statement = connection
        .prepare("SELECT version FROM schema_migrations ORDER BY version")
        .expect("prepare migrations");
    statement
        .query_map([], |row| row.get(0))
        .expect("query migrations")
        .collect::<Result<_, _>>()
        .expect("collect migrations")
}

fn schema(connection: &Connection) -> Vec<SchemaObject> {
    let mut statement = connection
        .prepare("SELECT type,name,COALESCE(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name")
        .expect("prepare schema");
    statement
        .query_map([], |row| {
            Ok(SchemaObject {
                object_type: row.get(0)?,
                name: row.get(1)?,
                sql: row.get(2)?,
            })
        })
        .expect("query schema")
        .collect::<Result<_, _>>()
        .expect("collect schema")
}

fn snapshot(connection: &Connection) -> Value {
    let files = query_values(
        connection,
        r#"SELECT id,path,sha256,title,created_at,modified_at,"type",document_date,person,status,due_date,confidence,ocr_json_path,simhash,asn,size,mtime_ns FROM files ORDER BY id"#,
        |row| {
            Ok(json!({
                "id": row.get::<_, i64>(0)?, "path": row.get::<_, String>(1)?,
                "sha256": row.get::<_, String>(2)?, "title": row.get::<_, String>(3)?,
                "created_at": row.get::<_, String>(4)?, "modified_at": row.get::<_, String>(5)?,
                "type": row.get::<_, String>(6)?, "document_date": row.get::<_, Option<String>>(7)?,
                "person": row.get::<_, Option<String>>(8)?, "status": row.get::<_, Option<String>>(9)?,
                "due_date": row.get::<_, Option<String>>(10)?, "confidence": row.get::<_, Option<i64>>(11)?,
                "ocr_json_path": row.get::<_, Option<String>>(12)?, "simhash": row.get::<_, Option<String>>(13)?,
                "asn": row.get::<_, Option<i64>>(14)?, "size": row.get::<_, Option<i64>>(15)?,
                "mtime_ns": row.get::<_, Option<i64>>(16)?
            }))
        },
    );
    let properties = query_values(
        connection,
        "SELECT file_id,key,value,value_type FROM file_properties ORDER BY file_id,key,value",
        |row| {
            Ok(
                json!({"file_id":row.get::<_,i64>(0)?,"key":row.get::<_,String>(1)?,"value":row.get::<_,Option<String>>(2)?,"value_type":row.get::<_,String>(3)?}),
            )
        },
    );
    let links = query_values(
        connection,
        "SELECT from_path,to_path,kind FROM links ORDER BY from_path,to_path,kind",
        |row| {
            Ok(
                json!({"from":row.get::<_,String>(0)?,"to":row.get::<_,String>(1)?,"kind":row.get::<_,String>(2)?}),
            )
        },
    );
    let fts_search = query_values(
        connection,
        "SELECT rowid,title,body FROM fts_search ORDER BY rowid",
        |row| {
            Ok(
                json!({"rowid":row.get::<_,i64>(0)?,"title":row.get::<_,String>(1)?,"body":row.get::<_,String>(2)?}),
            )
        },
    );
    let fts_norm = query_values(
        connection,
        "SELECT rowid,norm FROM fts_norm ORDER BY rowid",
        |row| Ok(json!({"rowid":row.get::<_,i64>(0)?,"norm":row.get::<_,String>(1)?})),
    );
    let fts_tri = query_values(
        connection,
        "SELECT rowid,body FROM fts_tri ORDER BY rowid",
        |row| Ok(json!({"rowid":row.get::<_,i64>(0)?,"body":row.get::<_,String>(1)?})),
    );
    json!({"files":files,"properties":properties,"links":links,"fts_search":fts_search,"fts_norm":fts_norm,"fts_tri":fts_tri})
}

fn query_values<F>(connection: &Connection, sql: &str, mut mapper: F) -> Vec<Value>
where
    F: FnMut(&rusqlite::Row<'_>) -> rusqlite::Result<Value>,
{
    let mut statement = connection.prepare(sql).expect("prepare snapshot");
    statement
        .query_map([], |row| mapper(row))
        .expect("query snapshot")
        .collect::<Result<_, _>>()
        .expect("collect snapshot")
}
