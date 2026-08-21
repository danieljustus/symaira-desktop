#!/bin/bash
set -euo pipefail

# Point toolchain to Xcode-beta to resolve SwiftUI macros
export DEVELOPER_DIR="/Applications/Xcode-beta.app/Contents/Developer"

# Ensure we are in the project root directory
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$ROOT_DIR"

# === Single version source: the current git tag (leading "v" stripped),
# === mirroring the CI release workflow (goreleaser -X main.version={{.Version}}).
# === When not on a tag, fall back to a short-sha dev version.
if VERSION="$(git describe --tags --exact-match 2>/dev/null)"; then
    VERSION="${VERSION#v}"
else
    VERSION="dev-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
fi
echo "Version: $VERSION"

echo "=== 1. Building Go Backend ==="
# CGO_ENABLED=0 to stay 100% CGO-free as per guidelines
CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$VERSION" -o symseek ./cmd/symseek
echo "Go binary built at ./symseek"

echo "=== 2. Building Swift GUI Client ==="
# Get build directory dynamically
swift build -c release --package-path client
SWIFT_BIN_DIR="$(swift build -c release --package-path client --show-bin-path)"
echo "Swift binary built at $SWIFT_BIN_DIR/symseek-gui"

echo "=== 3. Assembling macOS App Bundle ==="
APP_DIR="client/build/Symseek.app"
rm -rf "client/build"
mkdir -p "$APP_DIR/Contents/MacOS"
mkdir -p "$APP_DIR/Contents/Resources"

# Copy Info.plist
cp client/Sources/SymseekApp/Info.plist "$APP_DIR/Contents/"

# Inject the derived version into the bundle Info.plist (replaces the
# __VERSION__ template tokens in CFBundleShortVersionString/CFBundleVersion)
plutil -replace CFBundleShortVersionString -string "$VERSION" "$APP_DIR/Contents/Info.plist"
plutil -replace CFBundleVersion -string "$VERSION" "$APP_DIR/Contents/Info.plist"

# Create PkgInfo file (required for macOS WindowServer registration)
echo -n 'APPL????' > "$APP_DIR/Contents/PkgInfo"

# Copy Swift GUI executable
cp "$SWIFT_BIN_DIR/symseek-gui" "$APP_DIR/Contents/MacOS/"

# Embed Go backend binary
cp symseek "$APP_DIR/Contents/Resources/"
chmod +x "$APP_DIR/Contents/Resources/symseek"

# Copy application icon (declared via CFBundleIconFile in Info.plist)
cp client/Sources/SymseekApp/icon/Symseek.icns "$APP_DIR/Contents/Resources/"

echo "macOS App Bundle assembled at $APP_DIR"

echo "=== 4. Packaging into DMG ==="
DMG_STAGE="client/build/dmg"
mkdir -p "$DMG_STAGE"

# Copy App Bundle to DMG staging folder
cp -R "$APP_DIR" "$DMG_STAGE/"

# Create symlink to /Applications for easy drag-and-drop installer
ln -s /Applications "$DMG_STAGE/Applications"

# Create disk image
rm -f client/build/Symseek.dmg
hdiutil create -volname "Symseek" -srcfolder "$DMG_STAGE" -ov -format UDZO client/build/Symseek.dmg

echo "=== DMG Packaging Complete! ==="
echo "DMG is available at: client/build/Symseek.dmg"
