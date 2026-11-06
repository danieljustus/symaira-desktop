#!/usr/bin/env bash
# PG-001 replay, run inside a disposable PostgreSQL (see postgres-sidecar.sh;
# SYMDESK_PG_ADMIN_DSN names a superuser DSN of that server). Invoked by
# `make postgres-integration`.
#
# The Rust store replays testdata/port/storage/postgres.json, the scenarios the
# Go PostgresBackend recorded against a real server (last Go commit 075fdf94,
# tag v0.12.2). The Go recorder and the Go store's own integration tests left
# the tree in RUST-019, so this script only replays.
#
# Environment from the Makefile: CARGO.
set -euo pipefail

: "${SYMDESK_PG_ADMIN_DSN:?SYMDESK_PG_ADMIN_DSN is not set: run through make postgres-integration}"
cargo="${CARGO:-cargo}"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

"$cargo" test -p symdesk-storage --test postgres_contracts --locked -- --ignored --nocapture \
  > "$scratch/rust.log" 2>&1 || { cat "$scratch/rust.log" >&2; exit 1; }
grep -E '^replayed [0-9]+ scenarios' "$scratch/rust.log" \
  || { echo "postgres-integration: the Rust replay did not report its scenarios" >&2; cat "$scratch/rust.log" >&2; exit 1; }
grep -q 'test result: ok. 2 passed' "$scratch/rust.log" \
  || { echo "postgres-integration: the Rust replay did not pass" >&2; cat "$scratch/rust.log" >&2; exit 1; }
echo "PASS Rust store replays the Go-recorded PostgreSQL fixture"
