use rusqlite::Connection;
use symdesk_index::{
    RetrievalAnchor, RetrievalByteSection, RetrievalDb, RetrievalDocument, StoredRetrievalChunk,
    materialize_byte_chunks,
};

fn section(text: &[u8]) -> RetrievalByteSection {
    RetrievalByteSection {
        text: text.to_vec(),
        start: 0,
        anchor: RetrievalAnchor {
            kind: "text".to_owned(),
            value: "offset:0".to_owned(),
        },
        synthetic: false,
    }
}

#[test]
fn byte_sections_keep_invalid_utf8_as_raw_text_storage() {
    let chunks = materialize_byte_chunks("doc.csv", &[section(b"a\tcaf\xe9 \xff")]);
    assert_eq!(chunks.len(), 1);
    let chunk = &chunks[0];
    assert_eq!(chunk.content, "a\tcaf\u{fffd} \u{fffd}");
    assert_eq!(chunk.raw_content.as_deref(), Some(&b"a\tcaf\xe9 \xff"[..]));
    // The hash covers the exact bytes, not the replacement text.
    assert_eq!(chunk.hash, symdesk_vault::sha256_hex(b"a\tcaf\xe9 \xff"));
    let valid = materialize_byte_chunks("doc.csv", &[section("a\tcafé".as_bytes())]);
    assert_eq!(valid[0].raw_content, None);

    let directory =
        std::env::temp_dir().join(format!("symdesk-raw-content-{}", std::process::id()));
    std::fs::create_dir_all(&directory).expect("create scratch directory");
    let path = directory.join("retrieval.db");
    let database = RetrievalDb::open_at(&path).expect("open retrieval DB");
    let document = RetrievalDocument {
        path: "doc.csv".to_owned(),
        hash: "h".to_owned(),
        updated_at: "2026-09-01T00:00:00Z".to_owned(),
    };
    let stored = StoredRetrievalChunk {
        id: 0,
        uuid: chunk.uuid.clone(),
        document_path: "doc.csv".to_owned(),
        chunk_index: 0,
        content: chunk.content.clone(),
        embedding: vec![0.25, -0.5, 1.0],
        hash: chunk.hash.clone(),
        norm: 0.0,
        dim: 3,
        model: "m".to_owned(),
        char_start: Some(0),
        char_end: Some(9),
        anchor_kind: "text".to_owned(),
        anchor_value: "offset:0".to_owned(),
        embedding_pending: false,
    };
    database
        .replace_document_chunks_raw(
            &document,
            &[stored],
            std::slice::from_ref(&chunk.raw_content),
        )
        .expect("store raw chunk");

    let connection = Connection::open(&path).expect("open raw connection");
    let (class, bytes): (String, Vec<u8>) = connection
        .query_row(
            "SELECT typeof(content), CAST(content AS BLOB) FROM chunks",
            [],
            |row| Ok((row.get(0)?, row.get(1)?)),
        )
        .expect("read stored bytes");
    assert_eq!(class, "text");
    assert_eq!(bytes, b"a\tcaf\xe9 \xff");

    // Reading a Go-written byte string must not fail; callers see U+FFFD.
    let read = database
        .get_chunks_for_document("doc.csv")
        .expect("read chunk holding invalid UTF-8");
    assert_eq!(read[0].content, "a\tcaf\u{fffd} \u{fffd}");
    drop(database);
    let _ = std::fs::remove_dir_all(directory);
}
