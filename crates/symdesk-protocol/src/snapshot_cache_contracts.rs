use super::*;
use std::{thread, time::Instant};

struct TempRoot(PathBuf);
impl Drop for TempRoot {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn snapshot(state: &AppState) -> Arc<SnapshotPayload> {
    state
        .snapshot_cache
        .get_or_build(|| current_root_identity(state), || snapshot_payload(state))
        .expect("snapshot")
}

fn await_notes(state: &AppState, expected: &[(&str, &str)]) -> Arc<SnapshotPayload> {
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        let payload = snapshot(state);
        let value: serde_json::Value = serde_json::from_slice(&payload.plain).unwrap();
        let notes = value["notes"].as_array().unwrap();
        if notes.len() == expected.len()
            && expected.iter().all(|(path, content)| {
                notes
                    .iter()
                    .any(|n| n["path"] == *path && n["content"] == *content)
            })
        {
            return payload;
        }
        assert!(
            Instant::now() < deadline,
            "snapshot never refreshed: {value}"
        );
        thread::sleep(Duration::from_millis(10));
    }
}

#[test]
fn native_snapshot_cache_reflects_external_vault_lifecycle() {
    let root = TempRoot(std::env::temp_dir().join(format!(
        "symdesk-snapshot-contract-{}-{}",
        std::process::id(),
        unix_nanos(SystemTime::now())
    )));
    fs::create_dir(&root.0).unwrap();
    let root_path = fs::canonicalize(&root.0).unwrap();
    fs::write(root_path.join("note.md"), "first").unwrap();
    let state = AppState {
        vault_root: root_path.clone(),
        token: Arc::from(Vec::<u8>::new()),
        version: "test".to_owned(),
        auth_failures: Mutex::new(AuthThrottle::default()),
        snapshot_cache: SnapshotCache::new(&root_path),
    };
    let first = await_notes(&state, &[("note.md", "first")]);
    let repeat = snapshot(&state);
    assert!(
        Arc::ptr_eq(&first, &repeat),
        "warm snapshot must reuse the payload"
    );
    let mut decompressed = Vec::new();
    flate2::read::GzDecoder::new(&repeat.compressed[..])
        .read_to_end(&mut decompressed)
        .unwrap();
    assert_eq!(decompressed, repeat.plain);

    fs::write(root_path.join("note.md"), "second changed content").unwrap();
    let changed = await_notes(&state, &[("note.md", "second changed content")]);
    assert_ne!(first.etag, changed.etag);
    fs::create_dir(root_path.join("nested")).unwrap();
    fs::write(root_path.join("nested/new.md"), "new").unwrap();
    await_notes(
        &state,
        &[
            ("note.md", "second changed content"),
            ("nested/new.md", "new"),
        ],
    );
    fs::rename(root_path.join("note.md"), root_path.join("moved.md")).unwrap();
    await_notes(
        &state,
        &[
            ("moved.md", "second changed content"),
            ("nested/new.md", "new"),
        ],
    );
    fs::remove_file(root_path.join("moved.md")).unwrap();
    await_notes(&state, &[("nested/new.md", "new")]);
    fs::remove_dir_all(root_path.join("nested")).unwrap();
    await_notes(&state, &[]);
}

#[test]
fn warm_cache_reopens_replaced_root_and_file_reads_use_new_root() {
    let old_root = TempRoot(std::env::temp_dir().join(format!(
        "symdesk-snapshot-replacement-{}-{}",
        std::process::id(),
        unix_nanos(SystemTime::now())
    )));
    fs::create_dir(&old_root.0).unwrap();
    let root_path = fs::canonicalize(&old_root.0).unwrap();
    fs::write(root_path.join("note.md"), "old").unwrap();
    let state = AppState {
        vault_root: root_path.clone(),
        token: Arc::from(Vec::<u8>::new()),
        version: "test".to_owned(),
        auth_failures: Mutex::new(AuthThrottle::default()),
        snapshot_cache: SnapshotCache::new(&root_path),
    };
    let first = await_notes(&state, &[("note.md", "old")]);

    let displaced = root_path.with_extension("displaced");
    fs::rename(&root_path, &displaced).unwrap();
    fs::create_dir(&root_path).unwrap();
    fs::write(root_path.join("note.md"), "new").unwrap();

    let replacement = await_notes(&state, &[("note.md", "new")]);
    assert_ne!(first.etag, replacement.etag);
    let current_root = open_current_root(&state).unwrap();
    let mut file = current_root.open("note.md").unwrap();
    let mut body = String::new();
    file.read_to_string(&mut body).unwrap();
    assert_eq!(body, "new");
    fs::remove_dir_all(displaced).unwrap();
}

#[cfg(unix)]
#[test]
fn snapshot_preserves_legal_unix_backslashes_in_path_and_etag_material() {
    let root = TempRoot(std::env::temp_dir().join(format!(
        "symdesk-snapshot-backslash-{}-{}",
        std::process::id(),
        unix_nanos(SystemTime::now())
    )));
    fs::create_dir(&root.0).unwrap();
    let root_path = fs::canonicalize(&root.0).unwrap();
    let name = "literal\\name.md";
    fs::write(root_path.join(name), "content").unwrap();
    let state = AppState {
        vault_root: root_path.clone(),
        token: Arc::from(Vec::<u8>::new()),
        version: "test".to_owned(),
        auth_failures: Mutex::new(AuthThrottle::default()),
        snapshot_cache: SnapshotCache::new(&root_path),
    };
    let payload = await_notes(&state, &[(name, "content")]);
    assert!(payload.etag.len() == 64);
    assert_eq!(normalize_snapshot_path(Path::new(name)), name);
}

#[tokio::test]
async fn snapshot_read_failure_returns_http_500_retains_dirty_cache_and_retries() {
    let root = TempRoot(std::env::temp_dir().join(format!(
        "symdesk-snapshot-read-failure-{}-{}",
        std::process::id(),
        unix_nanos(SystemTime::now())
    )));
    fs::create_dir(&root.0).unwrap();
    let root_path = fs::canonicalize(&root.0).unwrap();
    fs::write(root_path.join("note.md"), "complete").unwrap();
    let state = Arc::new(AppState {
        vault_root: root_path,
        token: Arc::from(Vec::<u8>::new()),
        version: "test".to_owned(),
        auth_failures: Mutex::new(AuthThrottle::default()),
        snapshot_cache: SnapshotCache::uncached(),
    });
    state.snapshot_cache.set_healthy(true);
    let old = snapshot(&state);
    fs::write(state.vault_root.join("note.md"), "updated complete").unwrap();
    state.snapshot_cache.set_dirty(true);
    state.snapshot_cache.inject_read_failure();
    let response = handle_snapshot(State(Arc::clone(&state)), HeaderMap::new(), Method::GET).await;
    assert_eq!(response.status(), StatusCode::INTERNAL_SERVER_ERROR);
    assert!(state.snapshot_cache.is_dirty());
    let retained = state.snapshot_cache.payload().unwrap();
    assert!(Arc::ptr_eq(&old, &retained));

    let retried = snapshot(&state);
    let value: serde_json::Value = serde_json::from_slice(&retried.plain).unwrap();
    assert_eq!(value["notes"].as_array().unwrap().len(), 1);
    assert_eq!(value["notes"][0]["content"], "updated complete");
    assert_ne!(old.etag, retried.etag);
    assert!(!state.snapshot_cache.is_dirty());
    assert!(Arc::ptr_eq(&retried, &snapshot(&state)));
}

#[test]
fn production_snapshot_reader_preserves_partial_bytes_on_error() {
    let mut bytes = Vec::new();
    let error = read_snapshot_bytes(InjectedReadFailure::default(), &mut bytes).unwrap_err();
    assert_eq!(bytes, b"par");
    assert_eq!(error.to_string(), "injected snapshot read failure");
}
