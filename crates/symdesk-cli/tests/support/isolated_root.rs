use std::{
    fs,
    io::ErrorKind,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

static NEXT_ROOT: AtomicU64 = AtomicU64::new(0);

pub fn create(prefix: &str) -> PathBuf {
    let nonce = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .expect("system clock")
        .as_nanos();
    create_with_nonce(&std::env::temp_dir(), prefix, nonce)
}

fn create_with_nonce(parent: &Path, prefix: &str, nonce: u128) -> PathBuf {
    loop {
        let sequence = NEXT_ROOT.fetch_add(1, Ordering::Relaxed);
        let root = parent.join(format!(
            "{prefix}-{}-{nonce}-{sequence}",
            std::process::id()
        ));
        // Claim the root before creating children. A clock tick is not a
        // uniqueness guarantee for parallel replays, and stale roots must
        // never be reused or removed by another test's cleanup.
        match fs::create_dir(&root) {
            Ok(()) => return root,
            Err(error) if error.kind() == ErrorKind::AlreadyExists => continue,
            Err(error) => panic!("create isolated test root: {error}"),
        }
    }
}

#[test]
fn identical_clock_ticks_keep_replay_state_and_cleanup_separate() {
    let parent = std::env::temp_dir();
    let first = create_with_nonce(&parent, "symdesk-mcp-clock-control", 0);
    let second = create_with_nonce(&parent, "symdesk-mcp-clock-control", 0);
    assert_ne!(first, second);
    fs::write(first.join("sidecar.db"), b"first replay").expect("first state");
    fs::write(second.join("sidecar.db"), b"second replay").expect("second state");
    fs::remove_dir_all(first).expect("cleanup first replay");
    assert_eq!(
        fs::read(second.join("sidecar.db")).expect("second state survives"),
        b"second replay"
    );
    fs::remove_dir_all(second).expect("cleanup second replay");
}
