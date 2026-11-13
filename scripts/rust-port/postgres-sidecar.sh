#!/usr/bin/env bash
# Disposable PostgreSQL for the PG-001 differential (docs/rust-port/rust-015-pg-progress.md).
#
#   postgres-sidecar.sh run -- <command ...>      start a container, run the command with
#                                                  SYMDESK_PG_ADMIN_DSN set, always stop it
#   postgres-sidecar.sh up   <name> [network]     start (prints only the admin DSN on stdout)
#   postgres-sidecar.sh down <name> [network]     stop the container (and remove the network
#                                                  when one is given)
#
# Without a network the server is published on a random 127.0.0.1 port (host runs). With a
# network it is reachable only by container name on that network (Linux gate container).
# The superuser password is generated per run and never written to disk or printed except as
# part of the DSN handed to the caller. If SYMDESK_PG_ADMIN_DSN is already set, `run` uses that
# server and starts nothing.
set -euo pipefail

# Same image the existing CI sidecars run (postgres:17); pinned by digest, which is the
# multi-arch index so arm64 and amd64 hosts resolve their own variant.
IMAGE="${SYMDESK_PG_IMAGE:-postgres:17@sha256:0af65001d05296a2ead57ac4a6412433d8913d1bb5d0c88435a7d1e1ee5cb04b}"

up() { # name [network]
  local name="$1" network="${2:-}" password args=()
  password="$(openssl rand -hex 16)"
  args=(run -d --rm --name "$name" -e "POSTGRES_PASSWORD=$password" -e POSTGRES_DB=postgres
        --label symdesk.owner=r015-pg-sidecar --shm-size=128m
        --tmpfs /var/lib/postgresql/data:rw,size=1g)
  if [ -n "$network" ]; then
    docker network inspect "$network" >/dev/null 2>&1 || docker network create "$network" >/dev/null
    args+=(--network "$network")
  else
    args+=(-p 127.0.0.1::5432)
  fi
  docker "${args[@]}" "$IMAGE" -c fsync=off -c max_connections=200 >/dev/null
  # The image restarts the server once after initdb; two consecutive successes over TCP
  # (not the unix socket the init phase listens on) mean the real server is up.
  local ok=0 i
  for i in $(seq 1 120); do
    if docker exec "$name" pg_isready -q -h 127.0.0.1 -U postgres >/dev/null 2>&1; then
      ok=$((ok + 1)); [ "$ok" -ge 2 ] && break
    else
      ok=0
    fi
    sleep 0.5
  done
  if [ "$ok" -lt 2 ]; then
    docker logs "$name" >&2 || true
    down "$name" "$network"
    echo "postgres-sidecar: server did not become ready" >&2
    return 1
  fi
  if [ -n "$network" ]; then
    printf 'postgres://postgres:%s@%s:5432/postgres?sslmode=disable\n' "$password" "$name"
  else
    local port
    port="$(docker port "$name" 5432/tcp | head -n1 | sed 's/.*://')"
    printf 'postgres://postgres:%s@127.0.0.1:%s/postgres?sslmode=disable\n' "$password" "$port"
  fi
}

down() { # name [network]
  docker stop -t 5 "$1" >/dev/null 2>&1 || true
  docker rm -f "$1" >/dev/null 2>&1 || true
  if [ -n "${2:-}" ]; then docker network rm "$2" >/dev/null 2>&1 || true; fi
}

cmd="${1:-}"; shift || true
case "$cmd" in
  up) up "$@" ;;
  down) down "$@" ;;
  run)
    [ "${1:-}" = "--" ] && shift
    [ "$#" -gt 0 ] || { echo "usage: $0 run -- command ..." >&2; exit 2; }
    if [ -n "${SYMDESK_PG_ADMIN_DSN:-}" ]; then exec "$@"; fi
    command -v docker >/dev/null 2>&1 || {
      echo "postgres-sidecar: SYMDESK_PG_ADMIN_DSN is unset and docker is unavailable;" \
           "run through scripts/rust-port/local-gate.sh (starts a sidecar) or point the" \
           "variable at a disposable PostgreSQL server" >&2
      exit 2
    }
    name="symdesk-pg-$(printf '%s' "${LOCAL_GATE_SLUG:-main}" | tr -c 'a-zA-Z0-9_.-' '-')-$$"
    trap 'down "$name"' EXIT INT TERM
    SYMDESK_PG_ADMIN_DSN="$(up "$name")"
    export SYMDESK_PG_ADMIN_DSN
    "$@"
    ;;
  *) echo "usage: $0 run -- cmd... | up name [network] | down name [network]" >&2; exit 2 ;;
esac
