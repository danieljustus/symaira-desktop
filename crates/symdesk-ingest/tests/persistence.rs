use std::{
    path::PathBuf,
    sync::{Arc, Barrier},
};
use symdesk_ingest::{MIGRATIONS, Store};

struct Temp(PathBuf);
impl Drop for Temp {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

#[test]
fn independent_connections_claim_exactly_once() {
    let temp = Temp(std::env::temp_dir().join(format!("ingest-claim-{}", std::process::id())));
    std::fs::create_dir(&temp.0).unwrap();
    let path = temp.0.join("queue.db");
    let store = Store::open(&path).unwrap();
    let (document, _) = store
        .create_or_get("input.pdf", "hash", "application/pdf", "vault")
        .unwrap();
    let enqueued = store.enqueue_job(document.id, "ocr").unwrap();
    let barrier = Arc::new(Barrier::new(5));
    let threads = (0..4)
        .map(|_| {
            let store = Store::open(&path).unwrap();
            let barrier = barrier.clone();
            std::thread::spawn(move || {
                barrier.wait();
                store.claim_job_by_id(enqueued.id).unwrap()
            })
        })
        .collect::<Vec<_>>();
    barrier.wait();
    let claimed = threads
        .into_iter()
        .filter_map(|t| t.join().unwrap())
        .collect::<Vec<_>>();
    assert_eq!(claimed.len(), 1);
    assert_eq!(claimed[0].attempts, 1);
    assert_eq!(
        store
            .connection()
            .query_row("SELECT attempts FROM jobs WHERE id=?", [enqueued.id], |r| r
                .get::<_, i64>(0))
            .unwrap(),
        1
    );
    drop(store);
}

#[test]
fn upgrade_legacy_rows_preserves_jobs_and_extractions() {
    let mut c = rusqlite::Connection::open_in_memory().unwrap();
    c.execute_batch("PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(version TEXT PRIMARY KEY,applied_at DATETIME DEFAULT CURRENT_TIMESTAMP);").unwrap();
    for (version, sql) in &MIGRATIONS[..13] {
        let tx = c.transaction().unwrap();
        tx.execute_batch(sql).unwrap();
        tx.execute(
            "INSERT INTO schema_migrations(version) VALUES (?)",
            [version],
        )
        .unwrap();
        tx.commit().unwrap();
    }
    c.execute_batch("INSERT INTO documents(source_path,sha256,mime) VALUES ('old.pdf','legacy','application/pdf'); INSERT INTO jobs(document_id,kind) VALUES (1,'ocr'); INSERT INTO document_extractions(document_id,profile,field_type,value) VALUES (1,'invoice','amount','42');").unwrap();
    let mut store = Store::from_connection(c).unwrap();
    let (doc, created) = store
        .create_or_get("new.pdf", "legacy", "application/pdf", "")
        .unwrap();
    assert!(!created);
    assert_eq!(doc.source_path, "old.pdf");
    let job = store.claim_job().unwrap().unwrap();
    assert_eq!(job.source_path, "old.pdf");
    store.complete_job(job.id).unwrap();
    assert_eq!(store.by_id(doc.id).unwrap().status, "done");
    assert_eq!(
        store
            .connection()
            .query_row("SELECT value FROM document_extractions", [], |r| r
                .get::<_, String>(0))
            .unwrap(),
        "42"
    );
    assert_eq!(
        store
            .connection()
            .query_row("SELECT count(*) FROM pragma_foreign_key_check", [], |r| r
                .get::<_, i64>(
                0
            ))
            .unwrap(),
        0
    );
}
