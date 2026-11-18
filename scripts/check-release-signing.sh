#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKFLOW="$ROOT_DIR/.github/workflows/release.yml"
README="$ROOT_DIR/README.md"

require_fixed() {
  local file="$1"
  local pattern="$2"
  local description="$3"
  if ! grep -Fq -- "$pattern" "$file"; then
    printf "missing release-signing contract: %s\n" "$description" >&2
    exit 1
  fi
}

require_fixed_count() {
  local file="$1"
  local pattern="$2"
  local minimum="$3"
  local description="$4"
  local count
  count="$(grep -Fc -- "$pattern" "$file")"
  if (( count < minimum )); then
    printf "missing release-signing contract: %s (found %s, need %s)\n" "$description" "$count" "$minimum" >&2
    exit 1
  fi
}

require_fixed "$WORKFLOW" "id-token: write" "GitHub Actions OIDC permission"
require_fixed "$WORKFLOW" "sigstore/cosign-installer@" "Cosign installation"
require_fixed "$WORKFLOW" "cosign sign-blob" "per-artifact keyless signing step"
require_fixed "$WORKFLOW" "--output-signature" "per-artifact .sig output"
require_fixed "$WORKFLOW" "--output-certificate" "per-artifact .pem output"
if grep -Eq -- "COSIGN_KEY|COSIGN_PASSWORD|--key(=|[[:space:]]|$)" "$WORKFLOW"; then
  printf "long-lived Cosign key material is forbidden in the release workflow\n" >&2
  exit 1
fi
require_fixed "$WORKFLOW" "cosign-release: v2.4.3" "separate signature and certificate support"
# Both Apple signing jobs run on the image whose default Xcode 26.x builds the
# Icon Composer app icon (macos-15 with Xcode 26.0 crashed in actool, v0.13.0)
# and fail closed on any other Xcode major version.
require_fixed_count "$WORKFLOW" "runs-on: macos-26" 2 "macos-26 image for both macOS and iOS release jobs"
require_fixed_count "$WORKFLOW" "Expected Xcode 26.x, got:" 2 "Xcode 26.x check in both Apple release jobs"
require_fixed "$WORKFLOW" 'codesign --force --sign "$CODESIGN_IDENTITY" --timestamp "$DMG_PATH"' "Developer ID signature on the DMG container"
require_fixed "$WORKFLOW" 'xcrun stapler validate "$DMG_PATH"' "DMG stapling validation"
require_fixed "$WORKFLOW" 'spctl --assess --type open --context context:primary-signature' "DMG Gatekeeper assessment"
require_fixed "$WORKFLOW" 'gh release download "$GITHUB_REF_NAME"' "published DMG redownload"
require_fixed "$WORKFLOW" 'test "$PUBLISHED_SHA256" = "$DMG_SHA256"' "published-byte digest verification"
# The app re-sign must keep the entitlements: `codesign --force` without
# --entitlements replaces the ones the archive embedded (microphone capture
# needs com.apple.security.device.audio-input under the hardened runtime).
require_fixed "$WORKFLOW" 'codesign --force --sign "$CODESIGN_IDENTITY" --timestamp --options runtime --entitlements Sources/SymDeskApp/SymDesk.entitlements "$APP_PATH"' "app bundle signed with --entitlements"
require_fixed "$WORKFLOW" 'codesign --force --sign "$CODESIGN_IDENTITY" --timestamp --options runtime --entitlements Sources/SymDeskApp/SymDesk.entitlements "$item"' "main executable signed with --entitlements"
require_fixed "$WORKFLOW" 'codesign -d --entitlements :- "$APP_PATH"' "post-sign entitlement check"
require_fixed "$WORKFLOW" "com.apple.security.device.audio-input" "audio-input entitlement assertion"
require_fixed "$ROOT_DIR/Sources/SymDeskApp/SymDesk.entitlements" "com.apple.security.device.audio-input" "audio-input key in the entitlements file"
if grep -Fq "app-sandbox" "$ROOT_DIR/Sources/SymDeskApp/SymDesk.entitlements"; then
  printf "the Desktop app must not be sandboxed (event taps and Accessibility insertion)\n" >&2
  exit 1
fi
require_fixed "$README" "cosign verify-blob" "consumer verification command"
require_fixed "$README" "https://token.actions.githubusercontent.com" "OIDC issuer"

printf "release-signing contract: ok\n"
