#![deny(unsafe_code)]

//! Regression for #964: Go's `history.Store` caches an `os.Root` for the
//! process lifetime, which keeps the vault directory locked on Windows. The
//! Rust store owns its `Dir` and must release it on drop, so the vault can be
//! removed right after a history operation on every OS.

use std::fs;

use symdesk_vault::history::HistoryStore;

#[test]
fn dropping_history_store_releases_vault_directory() {
    let vault = std::env::temp_dir().join(format!(
        "symdesk-history-lifetime-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .expect("clock after epoch")
            .as_nanos()
    ));
    fs::create_dir_all(&vault).expect("create vault");
    fs::write(vault.join("note.md"), "# Note\n").expect("write note");

    let store = HistoryStore::new(&vault);
    let entry = store.snapshot("note.md").expect("snapshot note");
    assert!(entry.is_some(), "snapshot should record the note");
    drop(store);

    fs::remove_dir_all(&vault).expect("vault directory is released after drop");
    assert!(!vault.exists());
}
