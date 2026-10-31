//! CONTACT-001: Redact masks, export bytes and the encrypted backup format,
//! replayed against fixtures recorded from the real Go services. The Go-written
//! backup blob must restore here, every damaged variant must fail with Go's
//! exact error and leave the target untouched, and a backup written here is
//! handed to the Go oracle by `make contacts-security-differential`.
use base64::Engine as _;
use serde_json::{Value, json};
use std::path::PathBuf;
use symdesk_contacts::backup::{HEADER_SIZE, MAGIC, restore};
use symdesk_contacts::export::{ExportPerson, encode_csv, encode_json};
use symdesk_contacts::redact::redact;
use symdesk_contacts::security::Service;

fn fixture(name: &str) -> Value {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/contacts")
        .join(name);
    serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap()
}

fn snapshot(db: &rusqlite::Connection, query: &str) -> Value {
    let mut stmt = db.prepare(query).unwrap();
    let width = stmt.column_count();
    let rows: Vec<Vec<Value>> = stmt
        .query_map([], |r| {
            (0..width)
                .map(|c| {
                    Ok(match r.get_ref(c)? {
                        rusqlite::types::ValueRef::Null => Value::Null,
                        rusqlite::types::ValueRef::Text(s) => {
                            json!(std::str::from_utf8(s).unwrap())
                        }
                        rusqlite::types::ValueRef::Integer(n) => json!(n),
                        other => panic!("unsupported cell {other:?}"),
                    })
                })
                .collect()
        })
        .unwrap()
        .collect::<rusqlite::Result<_>>()
        .unwrap();
    json!(rows)
}

fn scratch(label: &str) -> PathBuf {
    let nanos = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let dir = std::env::temp_dir().join(format!(
        "symdesk-contacts-{label}-{}-{nanos}",
        std::process::id()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

#[test]
fn go_contacts_redact_contract() {
    let fixture = fixture("redact.json");
    let cases = fixture["cases"].as_array().unwrap();
    assert_eq!(cases.len(), 35);
    let mut changed = 0;
    for case in cases {
        let input = case["input"].as_str().unwrap();
        let got = redact(input);
        assert_eq!(got, case["output"].as_str().unwrap(), "redact({input:?})");
        if got != input {
            changed += 1;
        }
    }
    assert!(changed >= 15, "only {changed} redactions exercised");
    // Negative control: an implementation using Unicode \d/\s would also mask
    // these, so a regression to Unicode classes fails the fixture above.
    assert_eq!(
        redact("nbsp 555\u{a0}123\u{a0}4567 phone"),
        "nbsp 555\u{a0}123\u{a0}4567 phone"
    );
    assert_eq!(redact("٠١٢٣٤٥٦٧٨٩"), "٠١٢٣٤٥٦٧٨٩");
}

fn exporter<'a>(
    db: &'a rusqlite::Connection,
    ids: &'a [Value],
    stamp: &'a str,
    next: &'a std::cell::Cell<usize>,
) -> Service<
    'a,
    impl FnMut() -> String + 'a,
    impl FnMut() -> String + 'a,
    impl FnMut() -> String + 'a,
> {
    Service::new(
        db,
        move || {
            let i = next.get();
            next.set(i + 1);
            ids[i].as_str().unwrap().to_owned()
        },
        move || stamp.to_owned(),
        || "actor".to_owned(),
    )
}

#[test]
fn go_contacts_export_contract() {
    let fixture = fixture("export-backup.json");
    let export = &fixture["export"];
    let stamp = fixture["timestamp"].as_str().unwrap();
    let ids = fixture["ids"].as_array().unwrap();
    let next = std::cell::Cell::new(0);
    let db = symdesk_contacts::open_memory().unwrap();
    let mut svc = exporter(&db, ids, stamp, &next);

    let (mut json_out, mut csv_out) = (Vec::new(), Vec::new());
    svc.export_json(&mut json_out).unwrap();
    svc.export_csv(&mut csv_out).unwrap();
    assert_eq!(
        String::from_utf8(json_out).unwrap(),
        export["empty_json"].as_str().unwrap()
    );
    assert_eq!(
        String::from_utf8(csv_out).unwrap(),
        export["empty_csv"].as_str().unwrap()
    );

    db.execute_batch(fixture["seed"].as_str().unwrap()).unwrap();
    let (mut json_out, mut csv_out) = (Vec::new(), Vec::new());
    svc.export_json(&mut json_out).unwrap();
    svc.export_csv(&mut csv_out).unwrap();
    let json_text = String::from_utf8(json_out).unwrap();
    let csv_text = String::from_utf8(csv_out).unwrap();
    assert_eq!(json_text, export["json"].as_str().unwrap());
    assert_eq!(csv_text, export["csv"].as_str().unwrap());
    for private in ["private note", "Not Exported"] {
        assert!(
            !json_text.contains(private) && !csv_text.contains(private),
            "export leaked {private}"
        );
    }
    assert_eq!(
        snapshot(
            &db,
            "SELECT id, occurred_at, operation, actor, entity_type, entity_id, detail FROM audit_events ORDER BY id"
        ),
        export["audit_after_exports"]
    );

    // A NULL name column fails closed with Go's scan error and records nothing.
    let null_db = symdesk_contacts::open_memory().unwrap();
    null_db
        .execute(
            "INSERT INTO persons(id,display_name,given_name,created_at,updated_at) VALUES ('n1','Null Given',NULL,?1,?1)",
            [stamp],
        )
        .unwrap();
    let null_next = std::cell::Cell::new(0);
    let mut null_svc = exporter(&null_db, ids, stamp, &null_next);
    let mut sink = Vec::new();
    let json_err = null_svc.export_json(&mut sink).unwrap_err();
    let csv_err = null_svc.export_csv(&mut sink).unwrap_err();
    assert_eq!(
        json_err.message,
        export["null_name_json_error"].as_str().unwrap()
    );
    assert_eq!(
        csv_err.message,
        export["null_name_csv_error"].as_str().unwrap()
    );
    assert_eq!(json_err.kind, export["null_name_kind"].as_str().unwrap());
    assert!(sink.is_empty());
    assert_eq!(snapshot(&null_db, "SELECT id FROM audit_events"), json!([]));
}

#[test]
fn export_encoders_match_go_quoting() {
    let person = |name: &str| ExportPerson {
        id: "x".into(),
        display_name: name.into(),
        ..ExportPerson::default()
    };
    // Negative controls for each quoting trigger: plain text stays unquoted.
    assert_eq!(
        encode_csv(&[person("plain")]).lines().nth(1).unwrap(),
        "x,plain,,,,,"
    );
    assert_eq!(
        encode_csv(&[person("\\.")]).lines().nth(1).unwrap(),
        "x,\"\\.\",,,,,"
    );
    assert_eq!(
        encode_csv(&[person("a\\.b")]).lines().nth(1).unwrap(),
        "x,a\\.b,,,,,"
    );
    assert!(encode_json(&[person("a<b>&\u{2029}")]).contains("a\\u003cb\\u003e\\u0026\\u2029"));
    assert_eq!(encode_json(&[]), "null\n");
}

fn open_go_backup(fixture: &Value) -> Vec<u8> {
    base64::engine::general_purpose::STANDARD
        .decode(fixture["backup"]["blob_b64"].as_str().unwrap())
        .unwrap()
}

#[test]
fn go_contacts_backup_restore_contract() {
    let fixture = fixture("export-backup.json");
    let backup = &fixture["backup"];
    let passphrase = backup["passphrase"].as_str().unwrap().as_bytes();
    let blob = open_go_backup(&fixture);
    assert_eq!(blob.len() as u64, backup["blob_len"].as_u64().unwrap());
    assert_eq!(&blob[..MAGIC.len()], &MAGIC);
    let dir = scratch("restore");
    let stamp = fixture["timestamp"].as_str().unwrap().to_owned();
    let audit_id = fixture["ids"][5].as_str().unwrap().to_owned();

    let target = dir.join("restored.db");
    restore(
        passphrase,
        &blob,
        &target,
        || audit_id.clone(),
        || stamp.clone(),
        || "actor".to_owned(),
    )
    .unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let mode = std::fs::metadata(&target).unwrap().permissions().mode() & 0o777;
        assert_eq!(
            format!("{mode:o}"),
            backup["restored_mode"].as_str().unwrap()
        );
    }
    assert!(!dir.join("restored.db.restoring").exists());
    let db = rusqlite::Connection::open(&target).unwrap();
    let queries = backup["restore_queries"].as_array().unwrap();
    let snapshots = backup["restore_snapshots"].as_array().unwrap();
    assert_eq!((queries.len(), snapshots.len()), (6, 6));
    for (query, want) in queries.iter().zip(snapshots) {
        assert_eq!(&snapshot(&db, query.as_str().unwrap()), want, "{query}");
    }

    let negatives = backup["negatives"].as_array().unwrap();
    assert_eq!(negatives.len(), 12);
    for (i, case) in negatives.iter().enumerate() {
        let data = base64::engine::general_purpose::STANDARD
            .decode(case["blob_b64"].as_str().unwrap())
            .unwrap();
        let neg_target = dir.join(format!("neg-{i}.db"));
        let err = restore(
            case["passphrase"].as_str().unwrap().as_bytes(),
            &data,
            &neg_target,
            || audit_id.clone(),
            || stamp.clone(),
            || "actor".to_owned(),
        )
        .expect_err(case["name"].as_str().unwrap());
        assert_eq!(
            err.message,
            case["error"].as_str().unwrap(),
            "{}",
            case["name"]
        );
        assert_eq!(err.kind, case["kind"].as_str().unwrap());
        assert!(
            !neg_target.exists() && !dir.join(format!("neg-{i}.db.restoring")).exists(),
            "{} touched the target",
            case["name"]
        );
    }
    // The damaged variants are genuinely damaged: the pristine blob still restores.
    assert!(blob.len() > HEADER_SIZE);

    // Empty passphrase backup is rejected before anything is written.
    let live = symdesk_contacts::open_memory().unwrap();
    let next = std::cell::Cell::new(0);
    let ids = fixture["ids"].as_array().unwrap();
    let mut svc = exporter(&live, ids, &stamp, &next);
    let mut out = Vec::new();
    let err = svc.backup(b"", &mut out).unwrap_err();
    assert_eq!(err.message, backup["backup_error"].as_str().unwrap());
    assert_eq!(err.kind, backup["backup_error_kind"].as_str().unwrap());
    assert!(out.is_empty());
    std::fs::remove_dir_all(&dir).unwrap();
}

#[test]
fn rust_backup_round_trips_and_is_handed_to_go() {
    let fixture = fixture("export-backup.json");
    let passphrase = fixture["backup"]["passphrase"].as_str().unwrap().as_bytes();
    let stamp = fixture["timestamp"].as_str().unwrap().to_owned();
    let ids = fixture["ids"].as_array().unwrap();
    let live = symdesk_contacts::open_memory().unwrap();
    live.execute_batch(
        "INSERT INTO persons(id,display_name,given_name,family_name,created_at,updated_at) VALUES
         ('p1','Ada Lovelace','','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z'),
         ('p2','Bob, \"The\" Builder','','','2026-01-02T03:04:05Z','2026-01-02T03:04:05Z');",
    )
    .unwrap();
    let next = std::cell::Cell::new(0);
    let mut svc = exporter(&live, ids, &stamp, &next);
    let mut sink = Vec::new();
    svc.export_json(&mut sink).unwrap();
    let mut blob = Vec::new();
    svc.backup(passphrase, &mut blob).unwrap();
    assert_eq!(&blob[..MAGIC.len()], &MAGIC);
    assert!(blob.len() > HEADER_SIZE + 16);
    let audit = snapshot(
        &live,
        "SELECT operation, detail FROM audit_events ORDER BY id",
    );
    assert_eq!(
        audit[1],
        json!([
            "backup_created",
            format!("bytes={}", blob.len() - HEADER_SIZE)
        ])
    );

    let dir = scratch("rust-backup");
    let restored = dir.join("restored.db");
    restore(
        passphrase,
        &blob,
        &restored,
        || ids[5].as_str().unwrap().to_owned(),
        || stamp.clone(),
        || "actor".to_owned(),
    )
    .unwrap();
    let db = rusqlite::Connection::open(&restored).unwrap();
    assert_eq!(
        snapshot(&db, "SELECT id, display_name FROM persons ORDER BY id"),
        json!([["p1", "Ada Lovelace"], ["p2", "Bob, \"The\" Builder"]])
    );
    // Salt and nonce are fresh per backup, and a flipped tag byte is rejected.
    let mut again = Vec::new();
    svc.backup(passphrase, &mut again).unwrap();
    assert_ne!(
        blob[MAGIC.len()..HEADER_SIZE],
        again[MAGIC.len()..HEADER_SIZE]
    );
    let mut damaged = blob.clone();
    *damaged.last_mut().unwrap() ^= 1;
    assert!(
        restore(
            passphrase,
            &damaged,
            &dir.join("bad.db"),
            || "x".into(),
            || "t".into(),
            || "a".into()
        )
        .is_err()
    );
    assert!(!dir.join("bad.db").exists());

    if let Ok(exchange) = std::env::var("PORT_RUST_BACKUP_DIR") {
        std::fs::write(PathBuf::from(exchange).join("rust.bak"), &blob).unwrap();
    }
    std::fs::remove_dir_all(&dir).unwrap();
}
