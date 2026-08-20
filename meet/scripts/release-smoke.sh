#!/bin/bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

DIST_DIR="${1:?Usage: $0 <dist-dir> [version]}"
EXPECTED_VERSION="${2:-}"
EXPECTED_VERSION="${EXPECTED_VERSION#v}"

echo "==> Running release smoke tests on ${DIST_DIR}..."

ERRORS=0

# ── Checksums ──
if [ -f "$DIST_DIR/checksums.txt" ]; then
  echo "Verifying checksums..."
  if (cd "$DIST_DIR" && shasum -a 256 -c checksums.txt); then
    echo "  Checksums: PASS"
  else
    echo "  Checksums: FAIL" >&2
    ERRORS=$((ERRORS + 1))
  fi
else
  echo "  No checksums.txt found" >&2
  ERRORS=$((ERRORS + 1))
fi

# ── SBOM ──
if [ -f "$DIST_DIR/sbom.spdx.json" ]; then
  echo "Verifying SBOM..."
  if python3 -c "import json; json.load(open('$DIST_DIR/sbom.spdx.json'))" 2>/dev/null; then
    echo "  SBOM: PASS (valid JSON)"
  else
    echo "  SBOM: FAIL (invalid JSON)" >&2
    ERRORS=$((ERRORS + 1))
  fi
else
  echo "  SBOM: FAIL (missing)" >&2
  ERRORS=$((ERRORS + 1))
fi

# ── Notices archive ──
NOTICES=$(ls "$DIST_DIR"/*_notices.tar.gz 2>/dev/null | head -1 || true)
if [ -n "$NOTICES" ]; then
  echo "  Notices archive: PASS ($(basename "$NOTICES"))"
else
  echo "  Notices archive: FAIL (missing)" >&2
  ERRORS=$((ERRORS + 1))
fi

# ── CLI archive ──
CLI_ARCHIVE=$(ls "$DIST_DIR"/symmeet_v*.tar.gz 2>/dev/null | head -1 || true)
if [ -z "$CLI_ARCHIVE" ]; then
  echo "CLI archive: FAIL (missing)" >&2
  ERRORS=$((ERRORS + 1))
else
  echo "CLI archive: $(basename "$CLI_ARCHIVE")"

  WORK_DIR=$(mktemp -d)
  trap "rm -rf '$WORK_DIR'" EXIT

  tar -xzf "$CLI_ARCHIVE" -C "$WORK_DIR"

  if [ ! -x "$WORK_DIR/symmeet" ]; then
    echo "  CLI binary: FAIL (not executable)" >&2
    ERRORS=$((ERRORS + 1))
  else
    VERSION_OUTPUT=$("$WORK_DIR/symmeet" version --json 2>/dev/null)
    CLI_STDERR=$("$WORK_DIR/symmeet" version --json 2>&1 >/dev/null || true)

    if [ -n "$CLI_STDERR" ]; then
      echo "  CLI stderr: FAIL (non-empty: ${CLI_STDERR})" >&2
      ERRORS=$((ERRORS + 1))
    fi

    CLI_VER=$(echo "$VERSION_OUTPUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['version'])" 2>/dev/null || true)
    SCHEMA_VER=$(echo "$VERSION_OUTPUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['schema_version'])" 2>/dev/null || true)

    if [ "$SCHEMA_VER" != "1" ]; then
      echo "  CLI schema_version: FAIL (got ${SCHEMA_VER}, expected 1)" >&2
      ERRORS=$((ERRORS + 1))
    else
      echo "  CLI schema_version: PASS"
    fi

    if [ -n "$EXPECTED_VERSION" ]; then
      if [ "$CLI_VER" = "$EXPECTED_VERSION" ]; then
        echo "  CLI version: PASS (${CLI_VER})"
      else
        echo "  CLI version: FAIL (got ${CLI_VER}, expected ${EXPECTED_VERSION})" >&2
        ERRORS=$((ERRORS + 1))
      fi
    else
      echo "  CLI version: ${CLI_VER} (no expected version to compare)"
    fi

    "$WORK_DIR/symmeet" --help >/dev/null 2>&1
    echo "  CLI --help: PASS"
  fi
fi

# ── DMG ──
DMG=$(ls "$DIST_DIR"/*.dmg 2>/dev/null | head -1 || true)
if [ -n "$DMG" ]; then
  echo "DMG: $(basename "$DMG")"

  MOUNT_DIR=$(mktemp -d)
  if hdiutil attach -nobrowse -readonly "$DMG" -mountpoint "$MOUNT_DIR" 2>/dev/null; then
    if [ -d "$MOUNT_DIR/SymMeetAgent.app" ]; then
      echo "  DMG contents: PASS (SymMeetAgent.app found)"
    else
      echo "  DMG contents: FAIL (no SymMeetAgent.app)" >&2
      ERRORS=$((ERRORS + 1))
    fi
    hdiutil detach "$MOUNT_DIR" 2>/dev/null || true
  else
    echo "  DMG mount: FAIL" >&2
    ERRORS=$((ERRORS + 1))
  fi
  rm -rf "$MOUNT_DIR"
else
  echo "DMG: FAIL (missing)" >&2
  ERRORS=$((ERRORS + 1))
fi

# ── SymMeetAgent.app bundle ──
APP="$DIST_DIR/SymMeetAgent.app"
if [ -d "$APP" ]; then
  PLIST="$APP/Contents/Info.plist"
  BUNDLE_ID=$(/usr/libexec/PlistBuddy -c "Print :CFBundleIdentifier" "$PLIST" 2>/dev/null || true)
  APP_VER=$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$PLIST" 2>/dev/null || true)

  if [ "$BUNDLE_ID" != "dev.symaira.symmeet.agent" ]; then
    echo "  Agent bundle ID: FAIL (got ${BUNDLE_ID})" >&2
    ERRORS=$((ERRORS + 1))
  else
    echo "  Agent bundle ID: PASS (${BUNDLE_ID})"
  fi

  if [ -n "$EXPECTED_VERSION" ]; then
    if [ "$APP_VER" = "$EXPECTED_VERSION" ]; then
      echo "  Agent version: PASS (${APP_VER})"
    else
      echo "  Agent version: FAIL (got ${APP_VER}, expected ${EXPECTED_VERSION})" >&2
      ERRORS=$((ERRORS + 1))
    fi
  else
    echo "  Agent version: ${APP_VER}"
  fi

  CODESIGN_OUTPUT=$(codesign -dv "$APP" 2>&1 || true)
  if echo "$CODESIGN_OUTPUT" | grep -q "Authority="; then
    echo "  Agent signature: properly signed"
    if codesign --verify --deep --strict "$APP" 2>/dev/null; then
      echo "  codesign --verify: PASS"
    else
      echo "  codesign --verify: FAIL" >&2
      ERRORS=$((ERRORS + 1))
    fi
    if spctl --assess --type execute --verbose "$APP" 2>/dev/null; then
      echo "  spctl --assess: PASS"
    else
      echo "  spctl --assess: FAIL (may require notarization)" >&2
      ERRORS=$((ERRORS + 1))
    fi
    if xcrun stapler validate "$APP" 2>/dev/null; then
      echo "  stapler validate: PASS"
    else
      echo "  stapler validate: FAIL" >&2
      ERRORS=$((ERRORS + 1))
    fi
  else
    echo "  Agent signature: unsigned (dry-run build) -- Gatekeeper checks skipped"
  fi
else
  echo "  SymMeetAgent.app: FAIL (missing)" >&2
  ERRORS=$((ERRORS + 1))
fi

# ── Summary ──
echo ""
if [ "$ERRORS" -eq 0 ]; then
  echo "==> All smoke tests passed."
else
  echo "==> FAILED: ${ERRORS} check(s) failed." >&2
  exit 1
fi
