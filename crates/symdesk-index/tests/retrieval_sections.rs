use std::{fs, path::Path};

use serde::Deserialize;
use symdesk_index::{RetrievalSection, materialize_chunks, parse_markdown_retrieval_sections};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    source: String,
    markdown: String,
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
        .join("../../testdata/port/retrieval/retrieval-sections.json");
    serde_json::from_str(&fs::read_to_string(path).expect("read Go retrieval sections fixture"))
        .expect("decode Go retrieval sections fixture")
}

#[test]
fn parses_go_markdown_sections_and_materializes_their_chunks() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 5);
    for case in fixture.cases {
        let sections = parse_markdown_retrieval_sections(&case.source, case.markdown.as_bytes())
            .unwrap_or_else(|error| panic!("{}: parse sections: {error}", case.id));
        assert_eq!(sections, case.sections, "{} sections", case.id);

        let chunks = materialize_chunks(&case.source, &sections);
        assert_eq!(chunks.len(), case.chunks.len(), "{} chunk count", case.id);
        for (actual, expected) in chunks.iter().zip(case.chunks) {
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
