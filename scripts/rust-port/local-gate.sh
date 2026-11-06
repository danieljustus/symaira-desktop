#!/usr/bin/env bash
# Local replacement for per-PR CI during the local completion mode
# (docs/rust-port/local-first.md). Runs make targets natively on macOS arm64
# and in a native Linux arm64 container, and appends one receipt per run.
#
#   scripts/rust-port/local-gate.sh macos|linux|all [make-target ...]
#
# Default targets: rust-check rust-lint rust-test port-contract (Go-free:
# provenance checksums + the frozen replay of every Go-recorded fixture).
# Env: LOCAL_GATE_LOG_DIR / LOCAL_GATE_RECEIPTS, LOCAL_GATE_SLUG (separates
#      the container cache volumes of parallel worktrees), ALLOW_DIRTY=1
#      (receipt is marked dirty and is not evidence), MAKE_PREFIX (e.g. the
#      dev-external build launcher for the macOS run).
#
# postgres-integration needs a PostgreSQL server. The macOS run starts its own
# container through `make` (scripts/rust-port/postgres-sidecar.sh, random
# 127.0.0.1 port). The Linux gate container has no docker, so this script starts
# a sidecar for it on a private docker network (symdesk-gate-pg-$slug on
# symdesk-gate-net-$slug, per-run password, no published port), hands its
# superuser DSN to the container as SYMDESK_PG_ADMIN_DSN and always removes both
# afterwards.
set -euo pipefail

mode="${1:-all}"
shift || true
targets="${*:-rust-check rust-lint rust-test port-contract}"
root="$(git rev-parse --show-toplevel)"
cd "$root"
sha="$(git rev-parse HEAD)"
dirty=false
if [ -n "$(git status --porcelain)" ]; then
  [ "${ALLOW_DIRTY:-0}" = 1 ] || { echo "local-gate: working tree is dirty; commit first or set ALLOW_DIRTY=1" >&2; exit 2; }
  dirty=true
fi
log_dir="${LOCAL_GATE_LOG_DIR:-$root/target/local-gate}"
receipts="${LOCAL_GATE_RECEIPTS:-$log_dir/receipts.jsonl}"
mkdir -p "$log_dir" "$(dirname "$receipts")"
image=symdesk-local-gate:1
slug="${LOCAL_GATE_SLUG:-main}"

receipt() { # host exit started log
  printf '{"sha":"%s","dirty":%s,"host":"%s","targets":"%s","exit":%s,"started":"%s","finished":"%s","log":"%s"}\n' \
    "$sha" "$dirty" "$1" "$targets" "$2" "$3" "$(date -u +%FT%TZ)" "$4" >> "$receipts"
}

# shellcheck disable=SC2329 # invoked as "run_$host"
run_macos() {
  [ "$(uname -s)/$(uname -m)" = Darwin/arm64 ] || { echo "local-gate: macos needs native Darwin/arm64" >&2; return 2; }
  # shellcheck disable=SC2086 # word lists
  ${MAKE_PREFIX:-} make $targets
}

# shellcheck disable=SC2329 # invoked as "run_$host"
run_linux() {
  docker build -q -t "$image" scripts/rust-port/local-gate >/dev/null
  common="$(cd "$(git rev-parse --git-common-dir)" && pwd)"
  # Same absolute paths as the host so a worktree's .git file still resolves.
  pg_args=()
  pg_name="symdesk-gate-pg-$slug"
  pg_net="symdesk-gate-net-$slug"
  case " $targets " in
    *" postgres-integration "*)
      scripts/rust-port/postgres-sidecar.sh down "$pg_name" "$pg_net" || true
      pg_dsn="$(scripts/rust-port/postgres-sidecar.sh up "$pg_name" "$pg_net")" \
        || { scripts/rust-port/postgres-sidecar.sh down "$pg_name" "$pg_net" || true; return 1; }
      pg_args=(--network "$pg_net" -e "SYMDESK_PG_ADMIN_DSN=$pg_dsn")
      ;;
  esac
  # shellcheck disable=SC2086 # $targets is a word list
  docker run --rm ${pg_args[@]+"${pg_args[@]}"} -v "$root:$root" -v "$common:$common" -w "$root" \
    -v "symdesk-gate-registry:/home/dev/.cargo/registry" \
    -v "symdesk-gate-target-$slug:/home/dev/target" \
    -e CARGO_TARGET_DIR=/home/dev/target -e MIRI_TARGET_DIR=/home/dev/target/miri \
    -e CARGO_INCREMENTAL=0 \
    -e RENDER_EVIDENCE_ROOT="${RENDER_EVIDENCE_ROOT:+${RENDER_EVIDENCE_ROOT}-linux}" \
    -e RENDER_RUNTIME_ROOT="${RENDER_RUNTIME_ROOT:+${RENDER_RUNTIME_ROOT}-linux}" \
    "$image" make $targets
  ec=$?
  if [ ${#pg_args[@]} -gt 0 ]; then
    scripts/rust-port/postgres-sidecar.sh down "$pg_name" "$pg_net" || true
  fi
  return "$ec"
}

status=0
for host in $( [ "$mode" = all ] && echo "macos linux" || echo "$mode" ); do
  case "$host" in macos|linux) ;; *) echo "usage: $0 macos|linux|all [make-target ...]" >&2; exit 2 ;; esac
  log="$log_dir/${host}-${sha:0:8}-$(date -u +%H%M%S).log"
  started="$(date -u +%FT%TZ)"
  echo "local-gate: $host @ ${sha:0:8} [$targets] -> $log"
  set +e
  "run_$host" > "$log" 2>&1
  ec=$?
  set -e
  receipt "$host" "$ec" "$started" "$log"
  echo "local-gate: $host exit=$ec"
  [ "$ec" = 0 ] || { status=1; tail -n 40 "$log"; }
done
exit "$status"
