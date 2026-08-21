#!/bin/bash
# Packaging script for Symprint macOS Application -> DMG
set -euo pipefail

APP_PATH="${1:-client/build/Build/Products/Release/Symprint.app}"
DMG_PATH="${2:-dist/Symprint.dmg}"
VOL_NAME="Symaira Print"

echo "==> Packaging DMG..."
echo "    App:  $APP_PATH"
echo "    Dest: $DMG_PATH"

if [ ! -d "$APP_PATH" ]; then
    echo "Error: App bundle not found at $APP_PATH"
    exit 1
fi

# The Finder "Open With" integration depends on the bundle declaring the
# Markdown document type. Fail loudly when the declaration is missing so a
# broken DMG is never produced silently.
echo "==> Verifying Finder document-type declaration..."
PLIST="$APP_PATH/Contents/Info.plist"
if ! /usr/libexec/PlistBuddy -c "Print :CFBundleDocumentTypes" "$PLIST" >/dev/null 2>&1; then
    echo "Error: $APP_PATH declares no CFBundleDocumentTypes — the Finder 'Open With' integration would be missing. Refusing to package."
    exit 1
fi
if ! /usr/libexec/PlistBuddy -c "Print :CFBundleDocumentTypes:0:LSItemContentTypes" "$PLIST" 2>/dev/null | grep -q "net.daringfireball.markdown"; then
    echo "Error: $APP_PATH does not claim net.daringfireball.markdown — the Finder 'Open With' integration would be missing. Refusing to package."
    exit 1
fi
echo "✓ Finder document-type declaration present."

mkdir -p "$(dirname "$DMG_PATH")"
rm -f "$DMG_PATH"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"${SCRIPT_DIR}/create-symaira-dmg.sh" "$APP_PATH" "$DMG_PATH" "$VOL_NAME"

echo "✓ DMG created successfully at $DMG_PATH"
