# Disposable SymRoom rollback check

Run on the host being checked:

```sh
python3 scripts/rust-port/room-rollback.py --go-ref v0.12.2 --rust-ref HEAD --report /tmp/room-rollback.json
```

The harness checks out both source revisions in temporary Git worktrees, builds
the Rust `symroom` CLI and the older Go CLI with temporary build outputs, then
creates a room under temporary `HOME` and XDG directories. Rust creates the
identity and room, writes a signed note, and the older Go binary verifies and
reads that journal. It also rejects a tampered signature before appending its
own signed note. Rust then verifies and reads the mixed-version journal. The
JSON report records both source commit IDs, executable SHA-256 values, command
results, and resulting journal hashes. The Go fallback is built locally from
the selected older source revision; it is not a public release artifact.

The builds use a cleared environment with temporary `HOME`, XDG directories,
and language caches, plus a restricted system `PATH`. They read the installed
Rust toolchain under `RUSTUP_HOME`; they do not change installed tools. The
harness does not run an installed SymRoom executable or open existing rooms,
vaults, identity stores, or user-managed data. Build dependencies may be fetched
into the temporary caches.
This is a local, single-host room-journal regression check; it does not cover
other stores, release packaging, installed consumers, or the all-platform
`DIST-006` gate, and it does not authorize release or cutover.
