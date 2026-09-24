use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, UNIX_EPOCH},
};

use serde_json::{Value, json};
use symdesk_index::{DatasetSyncOptions, DatasetSyncRow, DatasetSyncService, Sidecar};
use symdesk_vault::{PropertyConfig, Provenance, parse_dataset_handle};

static COUNTER: AtomicU64 = AtomicU64::new(0);
const FIXTURE: &str = include_str!("../../../testdata/port/dataset/service-sync.json");

struct Sandbox {
    root: PathBuf,
    sidecar: Sidecar,
}

impl Sandbox {
    fn new(name: &str) -> Self {
        let counter = COUNTER.fetch_add(1, Ordering::Relaxed);
        let root = std::env::temp_dir().join(format!(
            "symdesk-dataset-sync-rust-{name}-{}-{counter}",
            std::process::id()
        ));
        let _ = fs::remove_dir_all(&root);
        fs::create_dir_all(root.join("vault")).expect("create vault");
        fs::create_dir_all(root.join("state")).expect("create state");
        let sidecar = Sidecar::open(&root.join("state/sidecar.db")).expect("open sidecar");
        Self {
            root: root.join("vault"),
            sidecar,
        }
    }

    fn sync(&mut self, options: DatasetSyncOptions) -> Result<Value, String> {
        DatasetSyncService::new(&self.root, &mut self.sidecar)
            .sync(options)
            .map(|result| serde_json::to_value(result).expect("serialize result"))
            .map_err(|error| error.to_string())
    }
}

impl Drop for Sandbox {
    fn drop(&mut self) {
        let parent = self.root.parent().unwrap_or(&self.root);
        let _ = fs::remove_dir_all(parent);
    }
}

fn fixture() -> Value {
    serde_json::from_str(FIXTURE).expect("parse Go oracle fixture")
}

fn case<'a>(fixture: &'a Value, id: &str) -> &'a Value {
    fixture["cases"]
        .as_array()
        .expect("cases")
        .iter()
        .find(|case| case["id"] == id)
        .unwrap_or_else(|| panic!("missing fixture case {id}"))
}

fn row(identity: &str, values: Value) -> DatasetSyncRow {
    DatasetSyncRow {
        identity: identity.to_owned(),
        values: serde_json::from_value(values).expect("row values object"),
    }
}

fn property(kind: &str, label: &str) -> PropertyConfig {
    PropertyConfig {
        r#type: kind.to_owned(),
        label: label.to_owned(),
        ..PropertyConfig::default()
    }
}

fn base_options(
    slug: &str,
    title: &str,
    imported_at: &str,
    source_name: &str,
    source_sha256: &str,
) -> DatasetSyncOptions {
    DatasetSyncOptions {
        title: title.to_owned(),
        slug: slug.to_owned(),
        identity_field: "id".to_owned(),
        schema: BTreeMap::from([
            ("id".to_owned(), property("text", "")),
            ("value".to_owned(), property("text", "")),
        ]),
        provenance: Provenance {
            imported_at: imported_at.to_owned(),
            source_name: source_name.to_owned(),
            source_sha256: source_sha256.to_owned(),
        },
        sensitivity: String::new(),
        retention_rule: String::new(),
        rows: Vec::new(),
    }
}

fn expected_entry<'a>(state: &'a Value, path: &str) -> &'a Value {
    state["vault"]
        .as_array()
        .expect("vault entries")
        .iter()
        .find(|entry| entry["path"] == path)
        .unwrap_or_else(|| panic!("missing fixture vault entry {path}"))
}

fn assert_file_matches(state: &Value, root: &Path, relative: &str) {
    let expected = expected_entry(state, relative);
    let bytes = fs::read(root.join(relative)).expect("read persisted file");
    assert_eq!(
        bytes,
        expected["content"].as_str().expect("content").as_bytes()
    );
    assert_eq!(
        symdesk_vault::sha256_hex(&bytes),
        expected["sha256"].as_str().expect("sha256")
    );
    assert_eq!(bytes.len() as u64, expected["size"].as_u64().expect("size"));
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt as _;
        assert_eq!(
            fs::metadata(root.join(relative))
                .expect("metadata")
                .permissions()
                .mode()
                & 0o777,
            0o600
        );
    }
}

fn actual_rows(sidecar: &Sidecar, slug: &str) -> Value {
    Value::Array(
        sidecar
            .dataset_rows(slug)
            .expect("dataset rows")
            .into_iter()
            .map(sidecar_row_value)
            .collect(),
    )
}

fn sidecar_row_value(row: symdesk_index::DatasetRow) -> Value {
    json!({
        "dataset_slug": row.dataset_slug,
        "row_key": row.row_key,
        "identity": row.identity,
        "values_json": row.values_json,
        "source_path": row.source_path,
        "row_number": row.row_number,
    })
}

fn capture_state(sandbox: &Sandbox, label: &str, slug: &str, include_times: bool) -> Value {
    let mut state = serde_json::Map::new();
    state.insert("label".to_owned(), Value::String(label.to_owned()));
    state.insert(
        "vault".to_owned(),
        Value::Array(vault_manifest(&sandbox.root, include_times)),
    );
    match sandbox.sidecar.dataset_rows(slug) {
        Ok(rows) => {
            state.insert(
                "rows".to_owned(),
                Value::Array(rows.into_iter().map(sidecar_row_value).collect()),
            );
        }
        Err(error) => {
            state.insert("rows".to_owned(), Value::Array(Vec::new()));
            state.insert("rows_error".to_owned(), Value::String(error.to_string()));
        }
    }
    let handle_path = format!("datasets/{slug}.md");
    match fs::read(sandbox.root.join(&handle_path)) {
        Ok(bytes) => {
            let handle =
                parse_dataset_handle(&handle_path, &bytes).expect("parse persisted handle");
            state.insert(
                "handle".to_owned(),
                serde_json::to_value(handle).expect("serialize handle"),
            );
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            state.insert(
                "handle_error".to_owned(),
                Value::String(format!(
                    "open vault file {{{{VAULT}}}}/{handle_path}: openat {handle_path}: no such file or directory"
                )),
            );
        }
        Err(error) => panic!("read persisted handle: {error}"),
    }
    Value::Object(state)
}

fn vault_manifest(root: &Path, include_times: bool) -> Vec<Value> {
    fn visit(root: &Path, current: &Path, include_times: bool, entries: &mut Vec<Value>) {
        let mut children = fs::read_dir(current)
            .expect("read vault directory")
            .map(|entry| entry.expect("read vault entry"))
            .collect::<Vec<_>>();
        children.sort_by_key(fs::DirEntry::file_name);
        for entry in children {
            let path = entry.path();
            let relative = path
                .strip_prefix(root)
                .expect("relative vault path")
                .to_string_lossy()
                .replace('\\', "/");
            let metadata = entry.metadata().expect("vault metadata");
            let mut observed = serde_json::Map::new();
            observed.insert("path".to_owned(), Value::String(relative));
            observed.insert(
                "kind".to_owned(),
                Value::String(
                    if metadata.is_dir() {
                        "directory"
                    } else {
                        "file"
                    }
                    .to_owned(),
                ),
            );
            observed.insert("mode".to_owned(), Value::String(mode_string(&metadata)));
            observed.insert(
                "perm".to_owned(),
                Value::String(permission_string(&metadata)),
            );
            if metadata.is_file() {
                let bytes = fs::read(&path).expect("read vault file");
                observed.insert("size".to_owned(), Value::from(bytes.len() as u64));
                observed.insert(
                    "sha256".to_owned(),
                    Value::String(symdesk_vault::sha256_hex(&bytes)),
                );
                observed.insert(
                    "content".to_owned(),
                    Value::String(String::from_utf8(bytes).expect("UTF-8 fixture file")),
                );
                if include_times {
                    let modified = metadata.modified().expect("modified time");
                    let nanos = modified
                        .duration_since(UNIX_EPOCH)
                        .expect("post-epoch modified time")
                        .as_nanos();
                    let timestamp = time::OffsetDateTime::from_unix_timestamp_nanos(
                        i128::try_from(nanos).expect("mtime nanos"),
                    )
                    .expect("mtime range")
                    .format(&time::format_description::well_known::Rfc3339)
                    .expect("format mtime");
                    observed.insert("modified_at".to_owned(), Value::String(timestamp));
                }
            }
            entries.push(Value::Object(observed));
            if metadata.is_dir() {
                visit(root, &path, include_times, entries);
            }
        }
    }

    let mut entries = Vec::new();
    visit(root, root, include_times, &mut entries);
    entries.sort_by(|left, right| left["path"].as_str().cmp(&right["path"].as_str()));
    entries
}

#[cfg(unix)]
fn permission_bits(metadata: &fs::Metadata) -> u32 {
    use std::os::unix::fs::PermissionsExt as _;
    metadata.permissions().mode() & 0o777
}

#[cfg(not(unix))]
fn permission_bits(_metadata: &fs::Metadata) -> u32 {
    0
}

fn permission_string(metadata: &fs::Metadata) -> String {
    format!("0{:03o}", permission_bits(metadata))
}

fn mode_string(metadata: &fs::Metadata) -> String {
    #[cfg(unix)]
    {
        let bits = permission_bits(metadata);
        let mut output = String::with_capacity(10);
        output.push(if metadata.is_dir() { 'd' } else { '-' });
        for shift in [6_u32, 3, 0] {
            output.push(if bits & (0o4 << shift) != 0 { 'r' } else { '-' });
            output.push(if bits & (0o2 << shift) != 0 { 'w' } else { '-' });
            output.push(if bits & (0o1 << shift) != 0 { 'x' } else { '-' });
        }
        output
    }
    #[cfg(not(unix))]
    {
        let _ = metadata;
        "----------".to_owned()
    }
}

fn observed_call(label: &str, result: Result<Value, String>) -> Value {
    match result {
        Ok(result) => json!({"label": label, "result": result}),
        Err(error) => json!({"label": label, "error": error}),
    }
}

fn observed_case(id: &str, calls: Vec<Value>, states: Vec<Value>) -> Value {
    json!({"id": id, "calls": calls, "states": states})
}

fn expected_observations(case: &Value) -> Value {
    json!({"id": case["id"], "calls": case["calls"], "states": case["states"]})
}

#[test]
fn first_sync_typed_quoted_unicode_matches_go_oracle() {
    let fixture = fixture();
    let expected = case(&fixture, "first-sync-typed-quoted-unicode");
    let mut sandbox = Sandbox::new("typed-unicode");
    let options = DatasetSyncOptions {
        title: "Unicode Ledger".to_owned(),
        slug: "unicode-ledger".to_owned(),
        identity_field: "id".to_owned(),
        schema: BTreeMap::from([
            ("active".to_owned(), property("checkbox", "Active")),
            ("amount".to_owned(), property("number", "Amount")),
            ("id".to_owned(), property("text", "Identifier")),
            ("note".to_owned(), property("text", "Note")),
            ("payload".to_owned(), property("text", "Payload")),
            ("when".to_owned(), property("date", "When")),
        ]),
        provenance: Provenance {
            imported_at: "2026-03-01T10:11:12+02:00".to_owned(),
            source_name: "typed-feed.csv".to_owned(),
            source_sha256: "typed-sha-001".to_owned(),
        },
        sensitivity: " confidential ".to_owned(),
        retention_rule: " finance-7y ".to_owned(),
        rows: vec![
            row(
                "u-α",
                json!({"id":"ignored","active":true,"amount":12.5,"note":"Café, \"quoted\"\nline二","payload":["x","雪"],"when":"2026-02-28"}),
            ),
            row(
                "u-β",
                json!({"active":false,"amount":7,"note":"emoji 🚀","payload":{"k":"値"},"when":"2026-03-01"}),
            ),
        ],
    };

    let result = sandbox.sync(options).expect("first sync");
    assert_eq!(result, expected["calls"][0]["result"]);
    let state = &expected["states"][0];
    assert_file_matches(
        state,
        &sandbox.root,
        "datasets/unicode-ledger/2026-03-01.csv",
    );
    assert_file_matches(state, &sandbox.root, "datasets/unicode-ledger.md");
    assert_eq!(
        actual_rows(&sandbox.sidecar, "unicode-ledger"),
        state["rows"]
    );
    let handle_bytes =
        fs::read(sandbox.root.join("datasets/unicode-ledger.md")).expect("read handle");
    let handle =
        parse_dataset_handle("datasets/unicode-ledger.md", &handle_bytes).expect("parse handle");
    assert_eq!(
        serde_json::to_value(handle).expect("serialize handle"),
        state["handle"]
    );
}

#[test]
fn repeated_provenance_is_idempotent_and_does_not_rewrite() {
    let fixture = fixture();
    let expected = case(&fixture, "repeated-provenance-idempotent-no-rewrite");
    let mut sandbox = Sandbox::new("idempotent");
    let mut first_options = base_options(
        "idempotent",
        "Idempotent",
        "2026-03-02T01:02:03Z",
        "stable-feed",
        "stable-sha",
    );
    first_options.rows = vec![row(
        "original",
        json!({"id":"original","value":"authoritative"}),
    )];
    let first = sandbox.sync(first_options.clone()).expect("first sync");
    assert_eq!(first, expected["calls"][0]["result"]);

    let fixed = UNIX_EPOCH + Duration::from_secs(1_577_934_245);
    for relative in [
        "datasets/idempotent/2026-03-02.csv",
        "datasets/idempotent.md",
    ] {
        let file = fs::OpenOptions::new()
            .write(true)
            .open(sandbox.root.join(relative))
            .expect("open for timestamp");
        file.set_times(fs::FileTimes::new().set_accessed(fixed).set_modified(fixed))
            .expect("set fixed timestamps");
    }
    let raw_before =
        fs::read(sandbox.root.join("datasets/idempotent/2026-03-02.csv")).expect("read raw before");
    let handle_before =
        fs::read(sandbox.root.join("datasets/idempotent.md")).expect("read handle before");
    let raw_mtime = fs::metadata(sandbox.root.join("datasets/idempotent/2026-03-02.csv"))
        .expect("raw metadata")
        .modified()
        .expect("raw mtime");
    let handle_mtime = fs::metadata(sandbox.root.join("datasets/idempotent.md"))
        .expect("handle metadata")
        .modified()
        .expect("handle mtime");
    let rows_before = actual_rows(&sandbox.sidecar, "idempotent");

    let mut repeat_options = first_options;
    repeat_options.title = "Changed but ignored".to_owned();
    repeat_options.provenance.imported_at = "2026-03-09T09:09:09Z".to_owned();
    repeat_options.rows = vec![row(
        "replacement",
        json!({"id":"replacement","value":"must-not-write"}),
    )];
    let repeated = sandbox.sync(repeat_options).expect("idempotent repeat");
    assert_eq!(repeated, expected["calls"][1]["result"]);
    assert_eq!(
        fs::read(sandbox.root.join("datasets/idempotent/2026-03-02.csv")).expect("read raw after"),
        raw_before
    );
    assert_eq!(
        fs::read(sandbox.root.join("datasets/idempotent.md")).expect("read handle after"),
        handle_before
    );
    assert_eq!(
        fs::metadata(sandbox.root.join("datasets/idempotent/2026-03-02.csv"))
            .expect("raw metadata after")
            .modified()
            .expect("raw mtime after"),
        raw_mtime
    );
    assert_eq!(
        fs::metadata(sandbox.root.join("datasets/idempotent.md"))
            .expect("handle metadata after")
            .modified()
            .expect("handle mtime after"),
        handle_mtime
    );
    assert_eq!(actual_rows(&sandbox.sidecar, "idempotent"), rows_before);
    assert_eq!(rows_before, expected["states"][1]["rows"]);
    assert_file_matches(
        &expected["states"][1],
        &sandbox.root,
        "datasets/idempotent/2026-03-02.csv",
    );
    assert_file_matches(
        &expected["states"][1],
        &sandbox.root,
        "datasets/idempotent.md",
    );
    assert_eq!(
        capture_state(&sandbox, "after-repeat", "idempotent", true),
        expected["states"][1]
    );
}

#[test]
fn matching_handle_empty_sidecar_rebuild_matches_go_oracle() {
    let fixture = fixture();
    let expected = case(&fixture, "matching-handle-empty-sidecar-rebuild");
    let mut sandbox = Sandbox::new("rebuild");
    let mut options = base_options(
        "rebuild",
        "Rebuild",
        "2026-03-03T03:03:03Z",
        "rebuild-feed",
        "rebuild-sha",
    );
    options.rows = vec![
        row("one", json!({"id":"one","value":"raw-one"})),
        row("two", json!({"id":"two","value":"raw-two"})),
    ];
    let first = observed_call("first", sandbox.sync(options.clone()));
    sandbox
        .sidecar
        .delete_dataset("rebuild")
        .expect("delete derived rows");
    let empty = capture_state(&sandbox, "matching-handle-empty-sidecar", "rebuild", false);
    let mut repeat = options;
    repeat.rows = vec![row(
        "incoming-ignored",
        json!({"id":"incoming-ignored","value":"not-authoritative"}),
    )];
    let rebuilt = observed_call("matching-handle-rebuild", sandbox.sync(repeat));
    let after = capture_state(&sandbox, "after-rebuild", "rebuild", false);
    let actual = observed_case(
        "matching-handle-empty-sidecar-rebuild",
        vec![first, rebuilt],
        vec![empty, after],
    );
    assert_eq!(actual, expected_observations(expected));
}

#[test]
fn same_day_later_refresh_duplicate_ordering_matches_go_oracle() {
    let fixture = fixture();
    let expected = case(&fixture, "same-day-later-refresh-duplicate-ordering");
    let mut sandbox = Sandbox::new("refresh-ordering");
    let mut first_options = base_options(
        "refresh",
        "Refresh",
        "2026-04-01T08:00:00Z",
        "refresh-feed",
        "refresh-sha-1",
    );
    first_options.rows = vec![
        row("shared", json!({"id":"shared","value":"first"})),
        row("b", json!({"id":"b","value":"first-b"})),
    ];
    let first = observed_call("first", sandbox.sync(first_options.clone()));
    let first_state = capture_state(&sandbox, "after-first", "refresh", false);

    let mut same_day_options = first_options.clone();
    same_day_options.provenance.source_sha256 = "refresh-sha-2".to_owned();
    same_day_options.provenance.imported_at = "2026-04-01T17:00:00-04:00".to_owned();
    same_day_options.rows = vec![
        row("a", json!({"id":"a","value":"same-day-a"})),
        row("shared", json!({"id":"shared","value":"same-day-wins"})),
    ];
    let same_day = observed_call("same-day-refresh", sandbox.sync(same_day_options));
    let same_day_state = capture_state(&sandbox, "after-same-day", "refresh", false);

    let mut later_options = first_options;
    later_options.provenance.source_sha256 = "refresh-sha-3".to_owned();
    later_options.provenance.imported_at = "2026-04-02T00:00:01Z".to_owned();
    later_options.rows = vec![
        row("shared", json!({"id":"shared","value":"later-wins"})),
        row("c", json!({"id":"c","value":"later-c"})),
    ];
    let later = observed_call("later-date-refresh", sandbox.sync(later_options));
    let later_state = capture_state(&sandbox, "after-later-date", "refresh", false);
    let actual = observed_case(
        "same-day-later-refresh-duplicate-ordering",
        vec![first, same_day, later],
        vec![first_state, same_day_state, later_state],
    );
    assert_eq!(actual, expected_observations(expected));
}

#[test]
fn blank_title_preserves_existing_metadata_matches_go_oracle() {
    let fixture = fixture();
    let expected = case(&fixture, "blank-title-preserves-existing-metadata");
    let mut sandbox = Sandbox::new("preserve-metadata");
    let mut first_options = base_options(
        "metadata",
        "Original Title",
        "2026-05-01T00:00:00Z",
        "metadata-feed",
        "metadata-sha-1",
    );
    first_options.rows = vec![row("first", json!({"id":"first","value":"first"}))];
    let first = observed_call("first", sandbox.sync(first_options.clone()));

    let handle_path = sandbox.root.join("datasets/metadata.md");
    let handle_bytes = fs::read(&handle_path).expect("read handle for metadata seed");
    let mut handle = parse_dataset_handle("datasets/metadata.md", &handle_bytes)
        .expect("parse handle for metadata seed");
    handle.created = "2024-12-31T23:59:58Z".to_owned();
    handle.coverage = symdesk_vault::Coverage {
        from: "2024-01-01".to_owned(),
        to: "2024-12-31".to_owned(),
    };
    handle.refresh_command = "symdesk dataset refresh metadata".to_owned();
    fs::write(
        &handle_path,
        symdesk_vault::dataset::render_handle(&handle).expect("render seeded handle"),
    )
    .expect("write seeded handle");
    let seeded = capture_state(&sandbox, "seeded-existing-metadata", "metadata", false);

    let mut refresh_options = first_options;
    refresh_options.title.clear();
    refresh_options.provenance.source_sha256 = "metadata-sha-2".to_owned();
    refresh_options.provenance.imported_at = "2026-05-02T00:00:00Z".to_owned();
    refresh_options.rows = vec![row("second", json!({"id":"second","value":"second"}))];
    let refresh = observed_call("blank-title-refresh", sandbox.sync(refresh_options));
    let after = capture_state(&sandbox, "after-blank-title-refresh", "metadata", false);
    let actual = observed_case(
        "blank-title-preserves-existing-metadata",
        vec![first, refresh],
        vec![seeded, after],
    );
    assert_eq!(actual, expected_observations(expected));
}

#[test]
fn representative_validation_order_before_write_matches_go_oracle() {
    let fixture = fixture();
    let expected = case(&fixture, "representative-validation-order-before-write");
    let mut sandbox = Sandbox::new("validation-order");
    let mut base = base_options(
        "validation",
        "Validation",
        "2026-06-01T00:00:00Z",
        "validation-feed",
        "validation-sha",
    );
    base.rows = vec![row("one", json!({"id":"one","value":"valid"}))];

    let nil_result = DatasetSyncService::from_parts(None, None)
        .sync(base.clone())
        .map(|result| serde_json::to_value(result).expect("serialize result"))
        .map_err(|error| error.to_string());
    let mut invalid_policy = base.clone();
    invalid_policy.sensitivity = "secret".to_owned();
    invalid_policy.identity_field.clear();
    invalid_policy.provenance = Provenance::default();
    let mut missing_identity = base.clone();
    missing_identity.identity_field = " ".to_owned();
    missing_identity.provenance = Provenance::default();
    let mut missing_provenance = base.clone();
    missing_provenance.provenance.source_name.clear();
    missing_provenance.provenance.imported_at = "not-a-time".to_owned();
    missing_provenance.slug = "Bad Slug".to_owned();
    let mut invalid_timestamp = base.clone();
    invalid_timestamp.provenance.imported_at = "not-a-time".to_owned();
    invalid_timestamp.slug = "Bad Slug".to_owned();
    invalid_timestamp.rows.clear();
    let mut unsafe_slug = base.clone();
    unsafe_slug.slug = "Bad Slug".to_owned();
    unsafe_slug.rows.clear();
    let mut no_rows = base.clone();
    no_rows.rows.clear();
    let mut blank_before_duplicate = base.clone();
    blank_before_duplicate.rows = vec![
        row(" ", json!({})),
        row("dup", json!({})),
        row("dup", json!({})),
    ];
    let mut duplicate = base;
    duplicate.rows = vec![row("dup", json!({})), row("dup", json!({}))];

    let calls = vec![
        observed_call("nil-service-dependencies-first", nil_result),
        observed_call(
            "invalid-policy-before-identity-and-provenance",
            sandbox.sync(invalid_policy),
        ),
        observed_call("identity-before-provenance", sandbox.sync(missing_identity)),
        observed_call(
            "provenance-before-timestamp-and-slug",
            sandbox.sync(missing_provenance),
        ),
        observed_call(
            "timestamp-before-slug-and-rows",
            sandbox.sync(invalid_timestamp),
        ),
        observed_call("slug-before-rows", sandbox.sync(unsafe_slug)),
        observed_call("rows-before-row-identity", sandbox.sync(no_rows)),
        observed_call(
            "blank-identity-before-later-duplicate",
            sandbox.sync(blank_before_duplicate),
        ),
        observed_call(
            "duplicate-identity-after-valid-identities",
            sandbox.sync(duplicate),
        ),
    ];
    let state = capture_state(&sandbox, "after-invalid-calls", "validation", false);
    let actual = observed_case(
        "representative-validation-order-before-write",
        calls,
        vec![state],
    );
    assert_eq!(actual, expected_observations(expected));
}

#[test]
fn nonfinite_number_projection_partial_write_matches_go_oracle() {
    let fixture = fixture();
    let expected = case(&fixture, "nonfinite-number-projection-partial-write");
    let mut sandbox = Sandbox::new("nonfinite");
    let mut options = base_options(
        "nonfinite",
        "Nonfinite",
        "2026-07-01T00:00:00Z",
        "nonfinite-feed",
        "nonfinite-sha",
    );
    options.schema = BTreeMap::from([
        ("amount".to_owned(), property("number", "")),
        ("id".to_owned(), property("text", "")),
    ]);
    options.rows = vec![row("nan-row", json!({"id":"nan-row","amount":"NaN"}))];
    let call = observed_call("declared-number-string-nan", sandbox.sync(options));
    let state = capture_state(&sandbox, "after-projection-failure", "nonfinite", false);
    let actual = observed_case(
        "nonfinite-number-projection-partial-write",
        vec![call],
        vec![state],
    );
    assert_eq!(actual, expected_observations(expected));
}

#[test]
fn closed_sidecar_partial_write_matches_go_oracle() {
    let fixture = fixture();
    let expected = case(&fixture, "closed-sidecar-partial-write");
    let mut sandbox = Sandbox::new("closed-sidecar");
    sandbox.sidecar.close().expect("close sidecar");
    let mut options = base_options(
        "closed-sidecar",
        "Closed Sidecar",
        "2026-08-01T00:00:00Z",
        "closed-feed",
        "closed-sha",
    );
    options.rows = vec![row(
        "one",
        json!({"id":"one","value":"written-before-db-failure"}),
    )];
    let call = observed_call("closed-sidecar", sandbox.sync(options));
    let state = capture_state(
        &sandbox,
        "after-closed-sidecar-failure",
        "closed-sidecar",
        false,
    );
    let actual = observed_case("closed-sidecar-partial-write", vec![call], vec![state]);
    assert_eq!(actual, expected_observations(expected));
}

#[test]
fn json_unmarshal_large_integer_matches_go_float64_rounding() {
    let fixture = fixture();
    let expected = case(&fixture, "json-unmarshal-large-integer-float64-rounding");
    let mut sandbox = Sandbox::new("large-json-float64");
    let mut options = base_options(
        "large-json-float",
        "Large JSON Float",
        "2026-08-02T00:00:00Z",
        "large-json",
        "large-json-sha",
    );
    options.schema = BTreeMap::from([
        ("id".to_owned(), property("text", "")),
        ("nested".to_owned(), property("text", "")),
        ("value".to_owned(), property("text", "")),
    ]);
    options.rows = vec![row(
        "large",
        json!({"id":"large","value":9007199254740993_u64,"nested":{"value":9007199254740993_u64}}),
    )];
    let call = observed_call("json-unmarshal-float64", sandbox.sync(options));
    let state = capture_state(
        &sandbox,
        "after-json-number-sync",
        "large-json-float",
        false,
    );
    let raw = fs::read_to_string(
        sandbox
            .root
            .join("datasets/large-json-float/2026-08-02.csv"),
    )
    .expect("read large-number CSV");
    assert!(raw.contains("9007199254740992"), "raw CSV: {raw:?}");
    assert!(!raw.contains("9007199254740993"), "raw CSV: {raw:?}");
    assert!(
        raw.contains("\"\"value\"\":9007199254740992"),
        "raw CSV: {raw:?}"
    );
    assert_eq!(
        observed_case(
            "json-unmarshal-large-integer-float64-rounding",
            vec![call],
            vec![state]
        ),
        expected_observations(expected)
    );
}

#[test]
fn declared_and_executed_case_inventories_are_exact() {
    let fixture = fixture();
    let declared = fixture["cases"]
        .as_array()
        .expect("cases")
        .iter()
        .map(|case| case["id"].as_str().expect("case id"))
        .collect::<Vec<_>>();
    let executed = vec![
        "first-sync-typed-quoted-unicode",
        "repeated-provenance-idempotent-no-rewrite",
        "matching-handle-empty-sidecar-rebuild",
        "same-day-later-refresh-duplicate-ordering",
        "blank-title-preserves-existing-metadata",
        "representative-validation-order-before-write",
        "nonfinite-number-projection-partial-write",
        "closed-sidecar-partial-write",
        "json-unmarshal-large-integer-float64-rounding",
    ];
    assert_eq!(declared, executed);
    assert_eq!(
        fixture["cases"]
            .as_array()
            .expect("cases")
            .iter()
            .map(|case| case["calls"].as_array().expect("calls").len())
            .sum::<usize>(),
        22
    );
    assert_eq!(
        fixture["cases"]
            .as_array()
            .expect("cases")
            .iter()
            .map(|case| case["states"].as_array().expect("states").len())
            .sum::<usize>(),
        14
    );
}

#[test]
fn fixture_mutations_fail_actual_replay_without_rewriting_fixture() {
    let fixture = fixture();
    let expected = case(&fixture, "first-sync-typed-quoted-unicode");
    let mut sandbox = Sandbox::new("mutation-control");
    let options = DatasetSyncOptions {
        title: "Unicode Ledger".to_owned(),
        slug: "unicode-ledger".to_owned(),
        identity_field: "id".to_owned(),
        schema: BTreeMap::from([
            ("active".to_owned(), property("checkbox", "Active")),
            ("amount".to_owned(), property("number", "Amount")),
            ("id".to_owned(), property("text", "Identifier")),
            ("note".to_owned(), property("text", "Note")),
            ("payload".to_owned(), property("text", "Payload")),
            ("when".to_owned(), property("date", "When")),
        ]),
        provenance: Provenance {
            imported_at: "2026-03-01T10:11:12+02:00".to_owned(),
            source_name: "typed-feed.csv".to_owned(),
            source_sha256: "typed-sha-001".to_owned(),
        },
        sensitivity: " confidential ".to_owned(),
        retention_rule: " finance-7y ".to_owned(),
        rows: vec![
            row(
                "u-α",
                json!({"id":"ignored","active":true,"amount":12.5,"note":"Café, \"quoted\"\nline二","payload":["x","雪"],"when":"2026-02-28"}),
            ),
            row(
                "u-β",
                json!({"active":false,"amount":7,"note":"emoji 🚀","payload":{"k":"値"},"when":"2026-03-01"}),
            ),
        ],
    };
    let call = observed_call("first", sandbox.sync(options));
    let state = capture_state(&sandbox, "after-first", "unicode-ledger", false);
    let actual = observed_case("first-sync-typed-quoted-unicode", vec![call], vec![state]);
    assert_eq!(actual, expected_observations(expected));

    let mut changed_result = expected_observations(expected);
    changed_result["calls"][0]["result"]["rows"] = Value::from(3);
    assert_ne!(actual, changed_result);

    let mut changed_state = expected_observations(expected);
    changed_state["states"][0]["rows"][0]["row_number"] = Value::from(99);
    assert_ne!(actual, changed_state);

    assert_eq!(
        FIXTURE.as_bytes(),
        include_bytes!("../../../testdata/port/dataset/service-sync.json")
    );
}
