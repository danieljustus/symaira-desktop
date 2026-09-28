use std::{fs, path::Path};

use serde::Deserialize;
use symdesk_index::{RetrievalSection, materialize_chunks};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    source: String,
    sections: Vec<RetrievalSection>,
    chunks: Vec<Chunk>,
}

#[derive(Deserialize)]
struct Chunk {
    uuid: String,
    chunk_index: usize,
    content: String,
    hash: String,
    char_start: Option<usize>,
    char_end: Option<usize>,
    anchor_kind: String,
    anchor_value: String,
}

fn fixture() -> Fixture {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/retrieval/retrieval-chunks.json");
    serde_json::from_str(&fs::read_to_string(path).expect("read retrieval chunk fixture"))
        .expect("decode retrieval chunk fixture")
}

#[test]
fn materializes_go_retrieval_chunks() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 3);
    for case in fixture.cases {
        let actual = materialize_chunks(&case.source, &case.sections);
        assert_eq!(actual.len(), case.chunks.len(), "case {}", case.id);
        for (actual, expected) in actual.iter().zip(case.chunks) {
            assert_eq!(actual.uuid, expected.uuid, "{} UUID", case.id);
            assert_eq!(
                actual.chunk_index, expected.chunk_index,
                "{} index",
                case.id
            );
            assert_eq!(actual.content, expected.content, "{} content", case.id);
            assert_eq!(actual.hash, expected.hash, "{} hash", case.id);
            assert_eq!(actual.char_start, expected.char_start, "{} start", case.id);
            assert_eq!(actual.char_end, expected.char_end, "{} end", case.id);
            assert_eq!(
                actual.anchor_kind, expected.anchor_kind,
                "{} anchor kind",
                case.id
            );
            assert_eq!(
                actual.anchor_value, expected.anchor_value,
                "{} anchor",
                case.id
            );
        }
    }
}
