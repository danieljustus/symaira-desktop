//! RUST-018: the Rust `symdesk` opens state the Go binaries created, unchanged.
//!
//! Replays `testdata/port/cutover/state-v1-<os>.json` (recorded by
//! `cmd/symdesk/cutover_state_port_test.go` from the real Go `symdesk`,
//! `symroom` and the Go contacts store) without Go: the frozen tree is unpacked
//! at the fixed root, every recorded read-only query is run with the Rust binary
//! and compared with the recorded Go output, and the tree must be byte-identical
//! afterwards. Rust writes (note + index refresh, saved view, ingest) must then
//! succeed and leave the stores readable by a second Rust process.

#![deny(unsafe_code)]

#[path = "../../test-support/cutover_state.rs"]
mod cutover_state;

use cutover_state::{State, Step, diff, load, lock_root, normalize};

const BINARY: &str = env!("CARGO_BIN_EXE_symdesk");
/// Recorded `symdesk` queries per state (frozen-contract case counts).
const EXPECTED_SYMDESK_QUERIES: usize = 28;
const EXPECTED_STATES: usize = 2;

/// The documents and chunks of a retrieval index as comparable text.
fn retrieval_rows(path: &std::path::Path) -> Vec<String> {
    use rusqlite::{Connection, OpenFlags, types::ValueRef};
    let connection = Connection::open_with_flags(path, OpenFlags::SQLITE_OPEN_READ_ONLY)
        .unwrap_or_else(|error| panic!("open {}: {error}", path.display()));
    let mut rows = Vec::new();
    for (label, sql, columns) in [
        ("document", "SELECT path, hash FROM documents ORDER BY path", 2),
        (
            "chunk",
            "SELECT document_path, chunk_index, content, hash, embedding, norm FROM chunks ORDER BY document_path, chunk_index",
            6,
        ),
    ] {
        let mut statement = connection.prepare(sql).expect("prepare");
        let mut cursor = statement.query([]).expect("query");
        while let Some(row) = cursor.next().expect("row") {
            let mut parts = Vec::new();
            for index in 0..columns {
                parts.push(match row.get_ref(index).expect("column") {
                    ValueRef::Null => "NULL".to_owned(),
                    ValueRef::Integer(value) => value.to_string(),
                    ValueRef::Real(value) => format!("{value:?}"),
                    ValueRef::Text(value) => String::from_utf8_lossy(value).into_owned(),
                    ValueRef::Blob(value) => format!("blob:{}", value.len()),
                });
            }
            rows.push(format!("{label}|{}", parts.join("|")));
        }
    }
    rows
}

/// The per-vault index Go and Rust both seed on the first read must hold the
/// same documents and chunks as the standalone store it was seeded from, which
/// itself stays byte-identical (checked by the tree comparison).
fn assert_seeded_from_standalone(root: &cutover_state::Root, state: &State) {
    for product in state
        .after_reads
        .iter()
        .filter(|f| f.volatile == "migration-product" && f.path.ends_with("/retrieval.db"))
    {
        let seeded = retrieval_rows(&root.path.join(&product.path));
        let standalone = retrieval_rows(&root.path.join("home/data/symdesk/retrieval.db"));
        assert!(seeded.iter().filter(|row| row.starts_with("chunk|")).count() >= 4, "{seeded:?}");
        assert_eq!(seeded, standalone, "{}: seeded index differs from the standalone store", state.name);
    }
}

fn symdesk_queries(state: &State) -> Vec<&cutover_state::Query> {
    state.queries.iter().filter(|q| q.tool == "symdesk").collect()
}

#[test]
fn rust_reads_go_created_state_with_go_outputs_and_leaves_it_unchanged() {
    let manifest = load();
    assert_eq!(manifest.states.len(), EXPECTED_STATES);
    let root = lock_root(&manifest);
    let mut executed = 0;
    for state in &manifest.states {
        let queries = symdesk_queries(state);
        assert_eq!(queries.len(), EXPECTED_SYMDESK_QUERIES, "{}", state.name);
        root.unpack(state);
        assert_eq!(diff(&state.files, &root.scan()), Vec::<String>::new(), "{}: unpacked tree", state.name);
        let mut mismatches = Vec::new();
        for query in queries {
            let run = root.run(&manifest, BINARY, &query.cwd, &query.args, &[], &[]);
            executed += 1;
            let (stdout, stderr) = (normalize(&run.stdout), normalize(&run.stderr));
            if run.exit != query.exit_code || stdout != query.stdout || stderr != query.stderr {
                mismatches.push(format!(
                    "{}/{}: exit {} (want {})\n  stdout: {:?}\n  want:   {:?}\n  stderr: {:?}\n  want:   {:?}",
                    state.name, query.name, run.exit, query.exit_code, stdout, query.stdout, stderr, query.stderr
                ));
            }
        }
        assert!(mismatches.is_empty(), "Rust output differs from the recorded Go output:\n{}", mismatches.join("\n"));
        // Rust must not migrate or rewrite Go state on read: the tree equals what
        // Go itself left after the same reads (unchanged for the settled state).
        assert_eq!(diff(&state.after_reads, &root.scan()), Vec::<String>::new(), "{}: tree after Rust reads", state.name);
        assert_seeded_from_standalone(&root, state);
        assert_eq!(
            state.after_reads.iter().any(|f| f.volatile == "migration-product"),
            state.name == "unmigrated",
            "only the unmigrated state may migrate on read"
        );
        if state.name == "settled" {
            assert_eq!(diff(&state.files, &root.scan()), Vec::<String>::new(), "settled tree must be byte-identical");
        }
    }
    assert_eq!(executed, EXPECTED_STATES * EXPECTED_SYMDESK_QUERIES);
}

fn run_step(root: &cutover_state::Root, manifest: &cutover_state::Manifest, step: &Step) -> String {
    let run = root.run(manifest, BINARY, &step.cwd, &step.args, &[], &[]);
    assert_eq!(run.exit, 0, "{:?}: stdout {} stderr {}", step.args, run.stdout, run.stderr);
    for expected in &step.expect_substr {
        assert!(
            run.stdout.contains(expected.as_str()),
            "{:?}: stdout lacks {expected:?}: {}",
            step.args,
            run.stdout
        );
    }
    run.stdout
}

#[test]
fn rust_writes_keep_go_created_stores_openable() {
    let manifest = load();
    assert_eq!(manifest.states.len(), EXPECTED_STATES);
    let root = lock_root(&manifest);
    let mut steps_run = 0;
    for state in &manifest.states {
        root.unpack(state);
        // Reads first, as a user would, then every declared write scenario.
        for query in symdesk_queries(state) {
            root.run(&manifest, BINARY, &query.cwd, &query.args, &[], &[]);
        }
        for write in &state.writes {
            for step in write.steps.iter().filter(|step| step.tool == "symdesk") {
                run_step(&root, &manifest, step);
                steps_run += 1;
            }
        }
        // A second Rust process must still open every Go-created store and
        // answer the recorded read queries with the recorded exit codes.
        for query in symdesk_queries(state) {
            let run = root.run(&manifest, BINARY, &query.cwd, &query.args, &[], &[]);
            assert_eq!(run.exit, query.exit_code, "{}/{} after Rust writes: {}", state.name, query.name, run.stderr);
        }
        let status = root.run(&manifest, BINARY, "cwd", &["--vault".into(), "$ROOT/vault".into(), "--json".into(), "index".into(), "status".into()], &[], &[]);
        assert_eq!(status.exit, 0, "{}", status.stderr);
        let status: serde_json::Value = serde_json::from_str(&status.stdout).expect("index status JSON");
        assert!(status["document_count"].as_u64().unwrap_or(0) >= 3, "{status}");
        // The contacts store written by Go is still the same store afterwards.
        let contact = root.run(&manifest, BINARY, "cwd", &["--vault".into(), "$ROOT/vault".into(), "--json".into(), "relations".into(), "contact".into(), "Ada Lovelace".into()], &[], &[]);
        assert_eq!(contact.exit, 0, "{}", contact.stderr);
        assert!(contact.stdout.contains(&manifest.vars["ADA"]), "{}", contact.stdout);
    }
    assert!(steps_run >= 2 * 10, "write scenarios executed: {steps_run}");
}

fn strings(args: &[&str]) -> Vec<String> {
    args.iter().map(|arg| (*arg).to_owned()).collect()
}

fn without_backups(files: Vec<cutover_state::FileEntry>) -> Vec<cutover_state::FileEntry> {
    files
        .into_iter()
        .filter(|file| {
            file.path != "home/data/symdesk/backups"
                && !file.path.starts_with("home/data/symdesk/backups/")
        })
        .collect()
}

/// The RUST-018 restore exercise on Go-created state: the first Rust run backs
/// the Go state up, Rust writes change it, `backup restore-pre-rust` gives the
/// original Go bytes back (the whole frozen tree, not a sample) and the Rust
/// reads match the recorded Go outputs again.
#[test]
fn first_rust_run_backup_and_restore_return_go_created_state_byte_identically() {
    let manifest = load();
    let root = lock_root(&manifest);
    let enable_backup = ["SYMDESK_PRE_RUST_BACKUP"];
    for state in &manifest.states {
        root.unpack(state);
        let first = root.run(
            &manifest,
            BINARY,
            "cwd",
            &strings(&["--vault", "$ROOT/vault", "--json", "config", "paths"]),
            &[],
            &enable_backup,
        );
        assert_eq!(first.exit, 0, "{}: {}", state.name, first.stderr);
        assert_eq!(first.stderr.matches("backed up once").count(), 1, "{}", first.stderr);
        let backup_dir = root.path.join("home/data/symdesk/backups/pre-rust");
        let recorded: serde_json::Value =
            serde_json::from_slice(&std::fs::read(backup_dir.join("manifest.json")).expect("manifest"))
                .expect("backup manifest JSON");
        assert_eq!(recorded["schema_version"], 1);
        // Every Go-recorded state file is in the backup with the Go digest: the
        // backup holds the Go bytes, not a re-encoding.
        let backed: std::collections::BTreeMap<String, (String, String)> = recorded["files"]
            .as_array()
            .expect("files")
            .iter()
            .map(|file| {
                (
                    file["source"].as_str().expect("source").to_owned(),
                    (
                        file["sha256"].as_str().expect("sha").to_owned(),
                        file["method"].as_str().expect("method").to_owned(),
                    ),
                )
            })
            .collect();
        let mut compared = 0;
        for file in state.files.iter().filter(|f| {
            f.kind == "f"
                && f.volatile.is_empty()
                && (f.path.starts_with("home/data/symdesk/") || f.path.starts_with("home/config/"))
        }) {
            let source = root.path.join(&file.path).to_string_lossy().into_owned();
            let (digest, method) = backed.get(&source).unwrap_or_else(|| panic!("{}: {source} not backed up", state.name));
            assert_eq!(digest, &file.sha256, "{}: {}", state.name, file.path);
            assert_eq!(method, "copy", "{}: {}", state.name, file.path);
            compared += 1;
        }
        assert!(compared >= 4, "{}: databases and JSON compared: {compared}", state.name);
        for database in ["symingest.db", "symrelate.db"] {
            assert!(
                backed.keys().any(|source| source.ends_with(&format!("data/symdesk/{database}"))),
                "{}: {database} must be in the backup",
                state.name
            );
        }
        // The Go fixture has no live WAL, so nothing needed the SQLite snapshot path.
        // A second run is silent and idempotent.
        let again = root.run(&manifest, BINARY, "cwd", &strings(&["--vault", "$ROOT/vault", "--json", "config", "paths"]), &[], &enable_backup);
        assert!(again.stderr.is_empty(), "{}", again.stderr);

        // Rust writes: stores (sidecar, retrieval, ingest) and the vault entries
        // those commands own (the source registry, the ingested note).
        for args in [
            &["--vault", "$ROOT/vault", "sources", "add", "$ROOT/inbox"][..],
            &["--vault", "$ROOT/vault", "ingest", "$ROOT/inbox/second.md"][..],
            &["--vault", "$ROOT/vault", "index"][..],
        ] {
            let run = root.run(&manifest, BINARY, "cwd", &strings(args), &[], &enable_backup);
            assert_eq!(run.exit, 0, "{}: {:?}: {} {}", state.name, args, run.stdout, run.stderr);
        }
        let changed = root.scan();
        assert_ne!(diff(&state.files, &without_backups(changed.clone())), Vec::<String>::new(), "{}: Rust writes must have changed the state", state.name);

        let dry = root.run(&manifest, BINARY, "cwd", &strings(&["backup", "restore-pre-rust", "--dry-run"]), &[], &enable_backup);
        assert_eq!(dry.exit, 0, "{}", dry.stderr);
        assert!(dry.stdout.contains("Dry run"), "{}", dry.stdout);
        assert_eq!(diff(&changed, &root.scan()), Vec::<String>::new(), "dry run must not change anything");

        let restore = root.run(&manifest, BINARY, "cwd", &strings(&["backup", "restore-pre-rust"]), &[], &enable_backup);
        assert_eq!(restore.exit, 0, "{}: {}", state.name, restore.stderr);
        assert!(restore.stdout.contains("all digests verified"), "{}", restore.stdout);
        // The restore covers the state locations (data and config directories):
        // those are byte-identical to the Go-created ones.
        let in_state_locations = |files: Vec<cutover_state::FileEntry>| -> Vec<cutover_state::FileEntry> {
            files.into_iter().filter(|f| f.path == "home" || f.path.starts_with("home/")).collect()
        };
        assert_eq!(
            diff(&in_state_locations(state.files.clone()), &in_state_locations(without_backups(root.scan()))),
            Vec::<String>::new(),
            "{}: restored state locations must be byte-identical to the Go-created ones",
            state.name
        );
        // The Markdown vault is user data and is deliberately not restored: what
        // Rust added there (source registry, ingested note, journal lines) is
        // still present. Put the original vault back to compare the whole tree
        // and replay the reads against the Go-era vault.
        let vault_before = root.scan();
        assert!(
            diff(&state.files, &without_backups(vault_before)).iter().any(|p| p.contains("vault/")),
            "{}: the Rust writes changed the vault, which the restore does not touch",
            state.name
        );
        root.unpack_prefix(state, "vault");
        assert_eq!(
            diff(&state.files, &without_backups(root.scan())),
            Vec::<String>::new(),
            "{}: restored tree must be byte-identical to the Go-created tree",
            state.name
        );
        // The Rust-era state was moved aside, never deleted.
        let aside_exists = std::fs::read_dir(root.path.join("home/data/symdesk/backups"))
            .expect("backups dir")
            .flatten()
            .any(|entry| entry.file_name().to_string_lossy().starts_with("pre-restore-"));
        assert!(aside_exists, "{}", state.name);

        // Reads work on the restored state and match the recorded Go outputs.
        for query in symdesk_queries(state) {
            let run = root.run(&manifest, BINARY, &query.cwd, &query.args, &[], &[]);
            assert_eq!(run.exit, query.exit_code, "{}/{} after restore", state.name, query.name);
            assert_eq!(normalize(&run.stdout), query.stdout, "{}/{} after restore", state.name, query.name);
            assert_eq!(normalize(&run.stderr), query.stderr, "{}/{} after restore", state.name, query.name);
        }
    }
}
