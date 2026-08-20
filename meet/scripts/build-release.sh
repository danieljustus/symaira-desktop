#!/bin/bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

DRY_RUN=0
VERSION=""

for arg in "$@"; do
  case "$arg" in
    --dry-run) DRY_RUN=1 ;;
    v*)        VERSION="${arg#v}" ;;
    *)         VERSION="$arg" ;;
  esac
done

if [ -z "$VERSION" ]; then
  echo "Usage: $0 [--dry-run] <version>" >&2
  echo "  e.g. $0 --dry-run v0.1.0" >&2
  exit 1
fi

TAG="v${VERSION}"
DIST_DIR="dist"
EMBEDDED_FILE="Sources/symmeet/Output/EmbeddedRelease.swift"

CLEANUP_DIRS=()

cleanup() {
  git checkout -- "$EMBEDDED_FILE" 2>/dev/null || cat > "$EMBEDDED_FILE" <<'SWIFT'
/// Compile-time version embedding for release builds.
///
/// `scripts/build-release.sh` rewrites this file during release builds,
/// replacing `nil` with the tag version. **Never commit a non-nil value.**
/// The committed default (`nil`) means debug/dev builds fall through to the
/// `SYMMEET_VERSION` environment variable or the hardcoded dev version.
enum EmbeddedRelease {
  static let version: String? = nil
}
SWIFT
  git checkout -- Package.resolved 2>/dev/null || true
  for d in "${CLEANUP_DIRS[@]+"${CLEANUP_DIRS[@]}"}"; do
    rm -rf "$d"
  done
}
trap cleanup EXIT

if [ "$DRY_RUN" -eq 1 ]; then
  echo "==> DRY RUN: building unsigned artifacts for ${TAG}"
else
  echo "==> Building signed release artifacts for ${TAG}"
fi

mkdir -p "$DIST_DIR"

# ── (a) Write EmbeddedRelease.swift with version ──
cat > "$EMBEDDED_FILE" <<SWIFT
enum EmbeddedRelease {
  static let version: String? = "${VERSION}"
}
SWIFT

# ── (b) Build CLI in release mode ──
echo "==> Building symmeet (release)..."
swift build -c release --product symmeet 2>&1
RELEASE_BIN_DIR="$(swift build -c release --show-bin-path)"
RELEASE_BINARY="$RELEASE_BIN_DIR/symmeet"

if [ ! -x "$RELEASE_BINARY" ]; then
  echo "FAIL: release binary not found at ${RELEASE_BINARY}" >&2
  exit 1
fi

# ── (c) Verify embedded version without env ──
echo "==> Verifying embedded version..."
EMBEDDED_OUTPUT=$(SYMMEET_VERSION="" "$RELEASE_BINARY" version --json 2>/dev/null || true)
EMBEDDED_VERSION=$(echo "$EMBEDDED_OUTPUT" | python3 -c "import sys,json; print(json.load(sys.stdin)['version'])" 2>/dev/null || true)

if [ "$EMBEDDED_VERSION" != "$VERSION" ]; then
  echo "FAIL: embedded version '${EMBEDDED_VERSION}' != expected '${VERSION}'" >&2
  echo "  Raw output: ${EMBEDDED_OUTPUT}" >&2
  exit 1
fi
echo "  Embedded version OK: ${EMBEDDED_VERSION}"

# ── (c2) Sign CLI binary (signed path only, before packaging) ──
if [ "$DRY_RUN" -eq 0 ]; then
  echo "==> Signing CLI binary..."
  CODESIGN_ID="${APPLE_SIGNING_IDENTITY:-Developer ID Application}"
  codesign --sign "$CODESIGN_ID" --options runtime --timestamp \
    "$RELEASE_BINARY"
  codesign --verify --strict "$RELEASE_BINARY"
fi

# ── (d) Package CLI ──
echo "==> Packaging CLI..."
SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct HEAD)}"
export SOURCE_DATE_EPOCH
scripts/package-cli.sh "$VERSION"

# ── (e) Build + package agent app (signs the app in the signed path) ──
echo "==> Building agent app..."
PACKAGE_AGENT_ARGS=("$VERSION")
if [ "$DRY_RUN" -eq 1 ]; then
  PACKAGE_AGENT_ARGS=(--dry-run "$VERSION")
fi
scripts/package-agent.sh "${PACKAGE_AGENT_ARGS[@]}"

# ── (e2) Notarize + staple (signed path only; rebuilds the DMG with the stapled app) ──
if [ "$DRY_RUN" -eq 0 ]; then
  echo "==> Notarizing..."
  scripts/notarize-agent.sh "$DIST_DIR"
fi

# ── (f) Generate SBOM ──
echo "==> Generating SBOM..."
scripts/generate-sbom.sh "$VERSION" "$DIST_DIR/sbom.spdx.json"

# ── (g) Generate notices bundle ──
echo "==> Generating notices archive..."
NOTICES_NAME="symmeet_v${VERSION}_notices.tar.gz"
NOTICES_STAGE=$(mktemp -d)
CLEANUP_DIRS+=("$NOTICES_STAGE")

mkdir -p "$NOTICES_STAGE/notices"
cp LICENSE "$NOTICES_STAGE/notices/"
cp Sources/SymMeetWhisperKit/THIRD_PARTY_NOTICES.md "$NOTICES_STAGE/notices/THIRD_PARTY_NOTICES_WhisperKit.md"
cp Sources/SymMeetSpeakerKit/THIRD_PARTY_NOTICES.md "$NOTICES_STAGE/notices/THIRD_PARTY_NOTICES_SpeakerKit.md"

if [ -d .build/checkouts ]; then
  for license_file in $(find .build/checkouts -maxdepth 2 \( -name "LICENSE*" -o -name "LICENCE*" -o -name "NOTICES*" \) -type f | sort); do
    basename_part=$(echo "$license_file" | sed 's|[^/]*/||g; s|/|_|g')
    cp "$license_file" "$NOTICES_STAGE/notices/$basename_part"
  done
fi

find "$NOTICES_STAGE/notices" -type f -exec touch -t "$(date -r "$SOURCE_DATE_EPOCH" +%Y%m%d%H%M.%S)" {} +

COPYFILE_DISABLE=1 tar -czf "$DIST_DIR/$NOTICES_NAME" \
  --uid 0 --gid 0 --numeric-owner \
  -C "$NOTICES_STAGE" notices

# ── (h) Generate checksums (last: after notarization rebuilt the DMG) ──
echo "==> Generating checksums..."
scripts/generate-checksums.sh "$DIST_DIR"

echo ""
echo "==> Release artifacts for ${TAG}:"
ls -lh "$DIST_DIR"/*
echo ""
echo "==> Done."
