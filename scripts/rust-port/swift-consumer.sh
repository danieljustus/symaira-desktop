#!/usr/bin/env bash
# Swift consumer gates SWIFT-001/002/003 (docs/rust-port/contract-matrix.md).
#
# Proves that the shipped native apps work unchanged against the Rust
# `symdesk` binary, with no Go involved:
#   SWIFT-001  macOS DeskCore/EventWatcher drive the Rust CLI (xcodebuild, macOS)
#   SWIFT-002  iOS server-mode client/outbox/share inbox/AI client talk HTTP to
#              a Rust `symdesk serve` on loopback (xcodebuild, iOS Simulator)
#   SWIFT-003  iOS Files mode (no CLI) writes a vault directory that the Rust
#              CLI reads, and reads a directory that the Rust CLI wrote
#
# Required environment:
#   SYMDESK_CONSUMER_BINARY   absolute path of the Rust `symdesk` binary
#                             (cargo build -p symdesk-cli --locked)
# Optional environment:
#   SWIFT_CONSUMER_XCODEBUILD command prefix for xcodebuild, e.g.
#                             "/Users/daniel/.local/bin/dev-external xcodebuild"
#   SWIFT_CONSUMER_BUILD_DIR  derived data + xcresult + work root
#                             (must be on the external volume under dev-external)
#   SWIFT_CONSUMER_SIM_ID     simulator UDID; default: device named
#                             symdesk-consumer, else the first available iPhone
#                             on an iOS 18.x runtime
#   SWIFT_CONSUMER_ONLY       space separated subset of: macos server files
#
# Exit status is non-zero when any xcodebuild invocation fails, when an
# xcresult reports a failure or a skip, or when the number of executed tests
# differs from the number of test methods in the gated class (zero executed
# tests is a failure, never a pass).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

if [[ -z "${SYMDESK_CONSUMER_BINARY:-}" ]]; then
  echo "swift-consumer: SYMDESK_CONSUMER_BINARY is required (absolute path of the Rust symdesk binary)" >&2
  exit 2
fi
BIN="$SYMDESK_CONSUMER_BINARY"
if [[ "$BIN" != /* || ! -x "$BIN" ]]; then
  echo "swift-consumer: SYMDESK_CONSUMER_BINARY must be an absolute executable path: $BIN" >&2
  exit 2
fi

BUILD_DIR="${SWIFT_CONSUMER_BUILD_DIR:-${TMPDIR:-/tmp}/symdesk-swift-consumer}"
BUILD_DIR="${BUILD_DIR%/}"
DERIVED="$BUILD_DIR/derived"
RESULTS="$BUILD_DIR/results"
WORK="$BUILD_DIR/work"
LOGS="$BUILD_DIR/logs"
# shellcheck disable=SC2206
XCODEBUILD=(${SWIFT_CONSUMER_XCODEBUILD:-xcodebuild})
ONLY="${SWIFT_CONSUMER_ONLY:-macos server files}"

rm -rf "$RESULTS" "$WORK" "$LOGS"
mkdir -p "$DERIVED" "$RESULTS" "$WORK" "$LOGS"

BIN_VERSION="$("$BIN" version --json)"
echo "swift-consumer: binary $BIN"
echo "swift-consumer: version $BIN_VERSION"
EXPECT_VERSION="$(printf '%s' "$BIN_VERSION" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["version"])')"

# Isolated per-run environment: no operator HOME/XDG state is ever touched.
ISO_HOME="$WORK/home"
mkdir -p "$ISO_HOME"
iso_env() {
  env -i PATH="/usr/bin:/bin:/usr/sbin:/sbin" HOME="$ISO_HOME" \
    XDG_CONFIG_HOME="$ISO_HOME/.config" XDG_DATA_HOME="$ISO_HOME/.local/share" \
    XDG_CACHE_HOME="$ISO_HOME/.cache" "$@"
}

SERVER_PID=""
cleanup() {
  if [[ -n "$SERVER_PID" ]] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

pick_simulator() {
  if [[ -n "${SWIFT_CONSUMER_SIM_ID:-}" ]]; then
    echo "$SWIFT_CONSUMER_SIM_ID"
    return
  fi
  xcrun simctl list devices available -j | python3 -I -c '
import json, sys
devices = json.load(sys.stdin)["devices"]
cands = []
for runtime, items in devices.items():
    if "iOS-18" not in runtime:
        continue
    for d in items:
        if "iPhone" in (d.get("deviceTypeIdentifier") or "") and d.get("isAvailable", True):
            cands.append((0 if d["name"] == "symdesk-consumer" else 1, d["name"], d["udid"]))
cands.sort()
if not cands:
    sys.exit("no available iPhone simulator on an iOS 18.x runtime; create one with: xcrun simctl create symdesk-consumer \"iPhone 16\" com.apple.CoreSimulator.SimRuntime.iOS-18-6")
print(cands[0][2])
'
}

# check_result <label> <xcresult> <expected executed tests>
check_result() {
  local label="$1" bundle="$2" expected="$3"
  xcrun xcresulttool get test-results summary --path "$bundle" > "$RESULTS/$label.summary.json"
  python3 -I - "$label" "$RESULTS/$label.summary.json" "$expected" <<'PY'
import json, sys
label, path, expected = sys.argv[1], sys.argv[2], int(sys.argv[3])
s = json.load(open(path))
passed = s.get("passedTests", 0)
failed = s.get("failedTests", 0)
skipped = s.get("skippedTests", 0)
expected_failures = s.get("expectedFailures", 0)
total = passed + failed + skipped + expected_failures
print(f"RESULT {label}: result={s.get('result')} passed={passed} failed={failed} skipped={skipped} total={total} expected={expected}")
errors = []
if s.get("result") != "Passed":
    errors.append(f"xcresult result is {s.get('result')!r}")
if failed or skipped:
    errors.append(f"failed={failed} skipped={skipped} (a skip never counts as a pass)")
if passed == 0:
    errors.append("zero executed tests")
if passed != expected:
    errors.append(f"executed {passed} tests, expected {expected}")
if errors:
    sys.exit(f"swift-consumer: {label} FAILED: " + "; ".join(errors))
PY
}

count_tests() {
  # number of test methods in a Swift test file
  grep -cE '^\s+func test[A-Za-z0-9_]+\(' "$1"
}

run_xcode_test() {
  # run_xcode_test <label> <project scheme> <destination> <only-testing...> -- <TEST_RUNNER env...>
  local label="$1" scheme="$2" destination="$3"
  shift 3
  local only=() runner_env=()
  while [[ $# -gt 0 && "$1" != "--" ]]; do only+=("-only-testing:$1"); shift; done
  shift || true
  runner_env=("$@")
  rm -rf "$RESULTS/$label.xcresult"
  local status=0
  env "${runner_env[@]}" "${XCODEBUILD[@]}" test \
    -project SymDesk.xcodeproj -scheme "$scheme" -destination "$destination" \
    -derivedDataPath "$DERIVED" -resultBundlePath "$RESULTS/$label.xcresult" \
    -scmProvider system \
    -test-timeouts-enabled YES -default-test-execution-time-allowance 240 -maximum-test-execution-time-allowance 300 \
    "${only[@]}" > "$LOGS/$label.log" 2>&1 || status=$?
  echo "XCODEBUILD $label exit=$status log=$LOGS/$label.log"
  if [[ $status -ne 0 ]]; then
    tail -n 60 "$LOGS/$label.log" >&2 || true
    exit "$status"
  fi
}

echo "swift-consumer: xcodegen generate"
xcodegen generate >/dev/null

# --------------------------------------------------------------------------
# SWIFT-001: macOS consumer
# --------------------------------------------------------------------------
if [[ " $ONLY " == *" macos "* ]]; then
  echo "swift-consumer: SWIFT-001 macOS"
  # RustBackendConsumerTests: the real DeskCore/EventWatcher against the Rust
  # binary. DeskTransportTests: the stream/lifecycle contract of the transport
  # the consumer tests rely on. testEventDecodingRFC3339Timestamp: the
  # `events --json` ts shape.
  run_xcode_test macos SymDeskCore 'platform=macOS' \
    SymDeskCoreTests/RustBackendConsumerTests \
    SymDeskCoreTests/DeskTransportTests \
    SymDeskCoreTests/DeskCoreTests/testEventDecodingRFC3339Timestamp -- \
    "TEST_RUNNER_SYMDESK_CONSUMER_BINARY=$BIN" \
    "TEST_RUNNER_SYMDESK_CONSUMER_EXPECT_VERSION=$EXPECT_VERSION"
  check_result macos "$RESULTS/macos.xcresult" \
    "$(( $(count_tests Tests/SymDeskCoreTests/RustBackendConsumerTests.swift) + $(count_tests Tests/SymDeskCoreTests/DeskTransportTests.swift) + 1 ))"
fi

SIM_ID=""
if [[ " $ONLY " == *" server "* || " $ONLY " == *" files "* ]]; then
  SIM_ID="$(pick_simulator)"
  echo "swift-consumer: simulator $SIM_ID"
  xcrun simctl boot "$SIM_ID" 2>/dev/null || true
  xcrun simctl bootstatus "$SIM_ID" -b >/dev/null
fi
IOS_DEST="platform=iOS Simulator,id=$SIM_ID"

# --------------------------------------------------------------------------
# SWIFT-002: iOS server consumer against `symdesk serve`
# --------------------------------------------------------------------------
if [[ " $ONLY " == *" server "* ]]; then
  echo "swift-consumer: SWIFT-002 iOS server mode"
  SERVER_VAULT="$WORK/server-vault"
  cp -R testdata/swift-consumer/vault "$SERVER_VAULT"
  TOKEN="$(python3 -I -c 'import secrets; print(secrets.token_hex(32))')"
  PORT="$(python3 -I -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()')"
  iso_env SYMDESK_VAULT="$SERVER_VAULT" SYMDESK_SERVER_TOKEN="$TOKEN" \
    "$BIN" serve --listen "127.0.0.1:$PORT" > "$LOGS/server.log" 2>&1 &
  SERVER_PID=$!
  ready=0
  for _ in $(seq 1 100); do
    if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then ready=1; break; fi
    kill -0 "$SERVER_PID" 2>/dev/null || break
    sleep 0.2
  done
  if [[ $ready -ne 1 ]]; then
    echo "swift-consumer: symdesk serve did not become ready" >&2
    cat "$LOGS/server.log" >&2 || true
    exit 1
  fi
  echo "swift-consumer: symdesk serve pid=$SERVER_PID http://127.0.0.1:$PORT"
  run_xcode_test server SymDeskMobile "$IOS_DEST" \
    SymDeskMobileTests/RustServerConsumerTests \
    SymDeskMobileTests/MobileServerDateTests \
    SymDeskMobileTests/MobileVaultParserTests/testDoubleQuotedTitleEscapesReadBackUnescaped -- \
    "TEST_RUNNER_SYMDESK_CONSUMER_SERVER_URL=http://127.0.0.1:$PORT" \
    "TEST_RUNNER_SYMDESK_CONSUMER_SERVER_TOKEN=$TOKEN"
  check_result server "$RESULTS/server.xcresult" \
    "$(( $(count_tests Tests/SymDeskMobileTests/RustServerConsumerTests.swift) + $(count_tests Tests/SymDeskMobileTests/MobileServerDateTests.swift) + 1 ))"
  kill "$SERVER_PID" 2>/dev/null || true
  wait "$SERVER_PID" 2>/dev/null || true
  SERVER_PID=""
fi

# --------------------------------------------------------------------------
# SWIFT-003: iOS Files mode, both directions through a shared directory
# --------------------------------------------------------------------------
if [[ " $ONLY " == *" files "* ]]; then
  echo "swift-consumer: SWIFT-003 iOS Files mode"
  FILES_VAULT="$WORK/files-vault"
  cp -R testdata/swift-consumer/vault "$FILES_VAULT"
  WRITE_MANIFEST="$WORK/files-write-manifest.json"
  READ_MANIFEST="$WORK/files-read-expected.json"

  run_xcode_test files-write SymDeskMobile "$IOS_DEST" \
    SymDeskMobileTests/RustFilesModeConsumerTests/testWriteFilesModeVault -- \
    "TEST_RUNNER_SYMDESK_CONSUMER_FILES_PHASE=write" \
    "TEST_RUNNER_SYMDESK_CONSUMER_FILES_DIR=$FILES_VAULT" \
    "TEST_RUNNER_SYMDESK_CONSUMER_FILES_MANIFEST=$WRITE_MANIFEST"
  check_result files-write "$RESULTS/files-write.xcresult" 1

  # Rust reads what iOS wrote, then writes notes of its own and resolves the
  # iOS conflicted copy; the helper emits what the iOS parser must then see.
  python3 -I scripts/rust-port/swift_consumer_files.py "$BIN" "$ISO_HOME" "$FILES_VAULT" \
    "$WRITE_MANIFEST" "$READ_MANIFEST" | tee "$LOGS/files-rust.log"

  run_xcode_test files-read SymDeskMobile "$IOS_DEST" \
    SymDeskMobileTests/RustFilesModeConsumerTests/testReadRustWrittenVault -- \
    "TEST_RUNNER_SYMDESK_CONSUMER_FILES_PHASE=read" \
    "TEST_RUNNER_SYMDESK_CONSUMER_FILES_DIR=$FILES_VAULT" \
    "TEST_RUNNER_SYMDESK_CONSUMER_FILES_MANIFEST=$READ_MANIFEST"
  check_result files-read "$RESULTS/files-read.xcresult" 1
fi

echo "swift-consumer: OK (binary version $EXPECT_VERSION, results in $RESULTS)"
