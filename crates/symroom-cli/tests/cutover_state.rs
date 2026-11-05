//! RUST-018: the Rust `symroom` opens the room journal and identity store the
//! Go `symroom` created, unchanged.
//!
//! Replays the `symroom` queries of `testdata/port/cutover/state-v1-<os>.json`
//! (recorded from the real Go binary, see `cmd/symdesk/cutover_state_port_test.go`)
//! without Go: the frozen tree is unpacked at the fixed root, every read-only
//! query must print the recorded Go bytes and leave the tree byte-identical, and
//! a Rust-signed event must then extend the Go-signed chain so that
//! `symroom verify` still passes over both.

#![deny(unsafe_code)]

#[path = "../../test-support/cutover_state.rs"]
mod cutover_state;

use cutover_state::{diff, load, lock_root, normalize};

const BINARY: &str = env!("CARGO_BIN_EXE_symroom");
const EXPECTED_SYMROOM_QUERIES: usize = 6;
const EXPECTED_STATES: usize = 2;

#[test]
fn rust_reads_go_created_room_state_with_go_outputs_and_leaves_it_unchanged() {
    let manifest = load();
    assert_eq!(manifest.states.len(), EXPECTED_STATES);
    let root = lock_root(&manifest);
    let mut executed = 0;
    for state in &manifest.states {
        let queries: Vec<_> = state.queries.iter().filter(|q| q.tool == "symroom").collect();
        assert_eq!(queries.len(), EXPECTED_SYMROOM_QUERIES, "{}", state.name);
        root.unpack(state);
        for query in queries {
            let run = root.run(&manifest, BINARY, &query.cwd, &query.args, &[], &[]);
            executed += 1;
            assert_eq!(run.exit, query.exit_code, "{}/{}: {}", state.name, query.name, run.stderr);
            assert_eq!(normalize(&run.stdout), query.stdout, "{}/{}", state.name, query.name);
            assert_eq!(normalize(&run.stderr), query.stderr, "{}/{}", state.name, query.name);
        }
        // Room commands never touch symdesk's stores: the whole tree is exactly
        // the Go tree (the identity files and journals included).
        assert_eq!(diff(&state.files, &root.scan()), Vec::<String>::new(), "{}: tree after Rust reads", state.name);
    }
    assert_eq!(executed, EXPECTED_STATES * EXPECTED_SYMROOM_QUERIES);
}

#[test]
fn rust_signed_event_extends_the_go_signed_journal() {
    let manifest = load();
    let root = lock_root(&manifest);
    let mut steps = 0;
    for state in &manifest.states {
        root.unpack(state);
        for write in &state.writes {
            for step in write.steps.iter().filter(|step| step.tool == "symroom") {
                let run = root.run(&manifest, BINARY, &step.cwd, &step.args, &[], &[]);
                assert_eq!(run.exit, 0, "{:?}: {} {}", step.args, run.stdout, run.stderr);
                for expected in &step.expect_substr {
                    assert!(run.stdout.contains(expected.as_str()), "{:?}: {}", step.args, run.stdout);
                }
                steps += 1;
            }
        }
        // The rebuilt derived index and a second Rust read still open the room.
        let rebuild = root.run(&manifest, BINARY, "room", &["index".into(), "rebuild".into()], &[], &[]);
        assert_eq!(rebuild.exit, 0, "{}", rebuild.stderr);
        let verify = root.run(&manifest, BINARY, "room", &["verify".into()], &[], &[]);
        assert_eq!(verify.exit, 0, "{}", verify.stderr);
        assert!(verify.stdout.contains("PASSED"), "{}", verify.stdout);
    }
    assert_eq!(steps, EXPECTED_STATES * 3);
}

/// Restore exercise for the one state `symroom` owns outside a room: the
/// identity store. The first Rust run backs it up, a Rust write adds an
/// identity, the restore returns exactly the Go-created store, and the Go-signed
/// journal still verifies with the restored keys.
#[test]
fn first_rust_run_backup_and_restore_return_the_go_identity_store() {
    let manifest = load();
    let root = lock_root(&manifest);
    let enable_backup = ["SYMDESK_PRE_RUST_BACKUP"];
    let strings = |args: &[&str]| -> Vec<String> { args.iter().map(|a| (*a).to_owned()).collect() };
    let identity_files = |files: Vec<cutover_state::FileEntry>| -> Vec<cutover_state::FileEntry> {
        files.into_iter().filter(|f| f.path.starts_with("home/data/symroom/") && !f.path.starts_with("home/data/symroom/backups")).collect()
    };
    for state in &manifest.states {
        root.unpack(state);
        let first = root.run(&manifest, BINARY, "room", &strings(&["identity", "list"]), &[], &enable_backup);
        assert_eq!(first.exit, 0, "{}", first.stderr);
        assert_eq!(first.stdout, "bob\nowner\n");
        assert_eq!(first.stderr.matches("backed up once").count(), 1, "{}", first.stderr);
        let recorded: serde_json::Value = serde_json::from_slice(
            &std::fs::read(root.path.join("home/data/symroom/backups/pre-rust/manifest.json")).expect("manifest"),
        )
        .expect("manifest JSON");
        assert_eq!(recorded["tool"], "symroom");
        let sources: Vec<String> = recorded["files"].as_array().unwrap().iter().map(|f| f["source"].as_str().unwrap().to_owned()).collect();
        assert_eq!(sources.len(), 2, "owner and bob identity files: {sources:?}");
        let again = root.run(&manifest, BINARY, "room", &strings(&["identity", "list"]), &[], &enable_backup);
        assert!(again.stderr.is_empty(), "{}", again.stderr);

        let created = root.run(&manifest, BINARY, "room", &strings(&["identity", "create", "carol"]), &[], &enable_backup);
        assert_eq!(created.exit, 0, "{}", created.stderr);
        assert_ne!(diff(&identity_files(state.files.clone()), &identity_files(root.scan())), Vec::<String>::new());

        let restore = root.run(&manifest, BINARY, "room", &strings(&["backup", "restore-pre-rust"]), &[], &enable_backup);
        assert_eq!(restore.exit, 0, "{}", restore.stderr);
        assert_eq!(
            diff(&identity_files(state.files.clone()), &identity_files(root.scan())),
            Vec::<String>::new(),
            "{}: identity store must be byte-identical to the Go-created one",
            state.name
        );
        let list = root.run(&manifest, BINARY, "room", &strings(&["identity", "list"]), &[], &[]);
        assert_eq!(list.stdout, "bob\nowner\n");
        let verify = root.run(&manifest, BINARY, "room", &strings(&["verify"]), &[], &[]);
        assert_eq!(verify.exit, 0, "{}", verify.stderr);
        // The Go room journal itself was never part of the backup and is untouched.
        let journals: Vec<_> = state.files.iter().filter(|f| f.path.starts_with("room/")).cloned().collect();
        let current: Vec<_> = root.scan().into_iter().filter(|f| f.path.starts_with("room/")).collect();
        assert_eq!(diff(&journals, &current), Vec::<String>::new());
    }
}
