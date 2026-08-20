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
  exit 1
fi

DIST_DIR="dist"
APP_NAME="SymMeetAgent"
XCODEPROJ="SymMeetAgent.xcodeproj"

echo "==> Generating Xcode project..."
xcodegen generate

BUILD_DIR=$(mktemp -d)
trap "rm -rf '$BUILD_DIR'" EXIT

BUILD_FLAGS=(-project "$XCODEPROJ" -scheme "$APP_NAME" -configuration Release -derivedDataPath "$BUILD_DIR")

if [ "$DRY_RUN" -eq 1 ]; then
  echo "==> Building ${APP_NAME} (unsigned)..."
  xcodebuild "${BUILD_FLAGS[@]}" \
    CODE_SIGNING_ALLOWED=NO \
    CODE_SIGNING_REQUIRED=NO \
    CODE_SIGN_IDENTITY="" \
    2>&1 | tail -10
else
  TEAM_ID="${APPLE_TEAM_ID:?APPLE_TEAM_ID is required for signed builds}"
  SIGNING_ID="${APPLE_SIGNING_IDENTITY:-Developer ID Application}"

  echo "==> Building ${APP_NAME} (signed)..."
  xcodebuild "${BUILD_FLAGS[@]}" \
    DEVELOPMENT_TEAM="$TEAM_ID" \
    CODE_SIGN_STYLE=Manual \
    CODE_SIGN_IDENTITY="$SIGNING_ID" \
    2>&1 | tail -10
fi

# ── Locate built .app in the run-local DerivedData ──
BUILT_APP="$BUILD_DIR/Build/Products/Release/${APP_NAME}.app"
if [ ! -d "$BUILT_APP" ]; then
  echo "FAIL: built ${APP_NAME}.app not found at ${BUILT_APP}" >&2
  exit 1
fi

mkdir -p "$DIST_DIR"
rm -rf "$DIST_DIR/${APP_NAME}.app"
cp -R "$BUILT_APP" "$DIST_DIR/${APP_NAME}.app"

# ── Set version strings in Info.plist ──
PLIST="$DIST_DIR/${APP_NAME}.app/Contents/Info.plist"
/usr/libexec/PlistBuddy -c "Set :CFBundleShortVersionString ${VERSION}" "$PLIST"
/usr/libexec/PlistBuddy -c "Set :CFBundleVersion ${VERSION}" "$PLIST"

# ── Verify bundle id and version ──
BUNDLE_ID=$(/usr/libexec/PlistBuddy -c "Print :CFBundleIdentifier" "$PLIST")
if [ "$BUNDLE_ID" != "dev.symaira.symmeet.agent" ]; then
  echo "FAIL: unexpected bundle ID: ${BUNDLE_ID}" >&2
  exit 1
fi

SVSS=$(/usr/libexec/PlistBuddy -c "Print :CFBundleShortVersionString" "$PLIST")
BV=$(/usr/libexec/PlistBuddy -c "Print :CFBundleVersion" "$PLIST")
if [ "$SVSS" != "$VERSION" ] || [ "$BV" != "$VERSION" ]; then
  echo "FAIL: version mismatch: CFBundleShortVersionString=${SVSS}, CFBundleVersion=${BV}" >&2
  exit 1
fi
echo "  Bundle ID: ${BUNDLE_ID}, Version: ${SVSS}"

# ── Sign (signed path) ──
if [ "$DRY_RUN" -eq 0 ]; then
  echo "==> Signing ${APP_NAME}.app..."
  SIGNING_ID="${APPLE_SIGNING_IDENTITY:-Developer ID Application}"
  if codesign --verify --deep --strict "$DIST_DIR/${APP_NAME}.app" 2>/dev/null; then
    echo "  App is already signed by xcodebuild; keeping the verified signature."
  else
    codesign --force --sign "$SIGNING_ID" --options runtime --timestamp --deep --strict \
      "$DIST_DIR/${APP_NAME}.app"
  fi
fi

# ── Create DMG ──
echo "==> Creating DMG..."
DMG_NAME="${APP_NAME}_v${VERSION}.dmg"
rm -f "$DIST_DIR/$DMG_NAME"
scripts/create-symaira-dmg.sh \
  "$DIST_DIR/${APP_NAME}.app" \
  "$DIST_DIR/$DMG_NAME" \
  "Symaira Meet"
echo "Created ${DIST_DIR}/${DMG_NAME}"
