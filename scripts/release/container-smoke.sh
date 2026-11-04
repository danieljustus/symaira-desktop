#!/usr/bin/env bash
# Ecosystem smoke for the server image (DIST-004): version, health, auth,
# persisted vault data across a container restart, and the Home Assistant
# add-on options file (/data/options.json) as the only token source.
#
#   scripts/release/container-smoke.sh <image> <expected-version>
set -euo pipefail

image="$1"
version="$2"
data=$(mktemp -d "${TMPDIR:-/tmp}/symdesk-smoke.XXXXXX")
name="symdesk-smoke-$$"
token="smoke-token-0123456789abcdef0123456789"
ha_token="ha-token-abcdefghijklmnopqrstuvwxyz012345"
port="${SMOKE_PORT:-18787}"
base="http://127.0.0.1:$port"
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  # Files below data/ belong to uid 10001 inside the container.
  docker run --rm -v "$data:/data" --entrypoint rm "$image" -rf /data/vault /data/state /data/options.json >/dev/null 2>&1 || true
  rm -rf "$data" "$data.read"
}
trap cleanup EXIT

fail() { echo "FAIL $*" >&2; docker logs "$name" >&2 2>/dev/null || true; exit 1; }

got=$(docker run --rm "$image" version)
[ "$got" = "symdesk $version" ] || fail "version: got '$got', want 'symdesk $version'"

start() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" -p "127.0.0.1:$port:8787" -v "$data:/data" "$@" "$image" >/dev/null
  for _ in $(seq 1 60); do
    if curl -fsS "$base/healthz" >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  fail "server did not become healthy"
}
status() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

start -e SYMDESK_SERVER_TOKEN="$token"
[ "$(status "$base/api/v1/status")" = 401 ] || fail "unauthenticated status is not 401"
[ "$(status -H "Authorization: Bearer wrong-token-0123456789abcdefghijkl" "$base/api/v1/status")" = 401 ] || fail "wrong token is not 401"
[ "$(status -H "Authorization: Bearer $token" "$base/api/v1/status")" = 200 ] || fail "authenticated status is not 200"
body=$'---\ntitle: Smoke\n---\nPersisted across restarts.\n'
code=$(status -X PUT -H "Authorization: Bearer $token" --data-binary "$body" "$base/api/v1/files?path=smoke.md")
case "$code" in 200|201|204) ;; *) fail "PUT /api/v1/files returned $code" ;; esac

start -e SYMDESK_SERVER_TOKEN="$token"
curl -fsS -o "$data.read" -H "Authorization: Bearer $token" "$base/api/v1/files?path=smoke.md"
printf '%s' "$body" | cmp -s - "$data.read" || fail "file did not survive the restart: $(cat "$data.read")"
docker exec "$name" test -f /data/vault/smoke.md || fail "file is not below the /data/vault volume"

# Home Assistant: the token comes only from the add-on options file.
docker run --rm -v "$data:/data" --entrypoint sh "$image" -c \
  "printf '%s' '{\"server_token\":\"$ha_token\",\"listen\":\"0.0.0.0:8787\",\"vault\":\"/data/vault\"}' > /data/options.json"
start
[ "$(status -H "Authorization: Bearer $ha_token" "$base/api/v1/status")" = 200 ] || fail "options.json token is not accepted"
# The first start migrated the legacy token into /data/vault/.symdesk/users.json
# as a named admin (Go contract), so it keeps working from the volume.
[ "$(status -H "Authorization: Bearer $token" "$base/api/v1/status")" = 200 ] || fail "migrated named-user token was lost"
[ "$(status -H "Authorization: Bearer wrong-token-0123456789abcdefghijkl" "$base/api/v1/status")" = 401 ] || fail "wrong token accepted in options mode"
curl -fsS -o "$data.read" -H "Authorization: Bearer $ha_token" "$base/api/v1/files?path=smoke.md"
printf '%s' "$body" | cmp -s - "$data.read" || fail "options-mode vault differs"

echo "PASS container smoke: $image (version $version, auth, restart persistence, Home Assistant options)"
