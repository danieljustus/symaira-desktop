#!/usr/bin/env bash
# PG-001 differential, run inside a disposable PostgreSQL (see postgres-sidecar.sh;
# SYMDESK_PG_ADMIN_DSN names a superuser DSN of that server). Invoked by
# `make postgres-integration`; `make postgres-fixtures-generate` passes `generate`.
#
#   check     1. pggen --check: re-run every scenario through the Go store against this
#                server and require the committed fixture byte for byte
#             2. the Go store's own postgres-tagged integration tests (they must RUN, not skip)
#             3. the Rust store replays the Go-recorded fixture against this server
#   generate  rewrite testdata/port/storage/postgres.json from the Go store (reviewed commit)
#
# Environment from the Makefile: CARGO, PORTGEN_CHECK_ENV / PORTGEN_GENERATE_GO_ENV (env
# prefixes that strip or set the generator controls).
set -euo pipefail

: "${SYMDESK_PG_ADMIN_DSN:?SYMDESK_PG_ADMIN_DSN is not set: run through make postgres-integration}"
mode="${1:-check}"
cargo="${CARGO:-cargo}"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT

case "$mode" in
  generate)
    # shellcheck disable=SC2086 # the env prefix is a word list
    ${PORTGEN_GENERATE_GO_ENV:-env} GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/pggen
    ;;
  check)
    # shellcheck disable=SC2086
    ${PORTGEN_CHECK_ENV:-env} GOTOOLCHAIN=go1.26.10 go run ./scripts/rust-port/cmd/pggen --check

    GOTOOLCHAIN=go1.26.10 go test -tags postgres -count=1 -v ./internal/storage -run '^TestPostgres' \
      > "$scratch/go-store.log" 2>&1 || { cat "$scratch/go-store.log" >&2; exit 1; }
    for test in TestPostgresGetSetRemoveQuery TestPostgresValueIsolation TestPostgresInvalidKey; do
      grep -q -- "--- PASS: $test " "$scratch/go-store.log" \
        || { echo "postgres-integration: Go $test did not run and pass" >&2; cat "$scratch/go-store.log" >&2; exit 1; }
    done
    echo "PASS Go store postgres-tagged integration tests"

    "$cargo" test -p symdesk-storage --test postgres_contracts --locked -- --ignored --nocapture \
      > "$scratch/rust.log" 2>&1 || { cat "$scratch/rust.log" >&2; exit 1; }
    grep -E '^replayed [0-9]+ scenarios' "$scratch/rust.log" \
      || { echo "postgres-integration: the Rust replay did not report its scenarios" >&2; cat "$scratch/rust.log" >&2; exit 1; }
    grep -q 'test result: ok. 2 passed' "$scratch/rust.log" \
      || { echo "postgres-integration: the Rust replay did not pass" >&2; cat "$scratch/rust.log" >&2; exit 1; }
    echo "PASS Rust store replays the Go-recorded PostgreSQL fixture"
    ;;
  *)
    echo "usage: $0 check|generate" >&2
    exit 2
    ;;
esac
