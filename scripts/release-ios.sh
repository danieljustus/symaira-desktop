#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage:
  scripts/release-ios.sh [options]
  scripts/release-ios.sh --dry-run
  scripts/release-ios.sh --validate-archive PATH [--version VERSION] [--build-number N]
  scripts/release-ios.sh --validate-export PATH [--version VERSION] [--build-number N]

The default action generates the Xcode project, archives SymDeskMobile for
iphoneos, exports an app-store-connect IPA, and validates the app plus both
extensions. Add --upload to upload the IPA with App Store Connect API
credentials supplied through environment variables.

Options:
  --archive-path PATH   Archive destination (default: build/SymDeskMobile.xcarchive)
  --export-path PATH    Export destination (default: build/ios-export)
  --version VERSION     Marketing version (or IOS_VERSION / MARKETING_VERSION)
  --build-number N      Build number (or IOS_BUILD_NUMBER / CURRENT_PROJECT_VERSION)
  --upload              Upload the validated IPA with xcrun altool
  --allow-unsigned      Skip codesign verification when validating an archive
  --dry-run             Validate source configuration without Xcode or credentials
EOF
}

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROJECT="$ROOT_DIR/SymDesk.xcodeproj"
SCHEME="SymDeskMobile"
ARCHIVE_PATH="$ROOT_DIR/build/SymDeskMobile.xcarchive"
EXPORT_PATH="$ROOT_DIR/build/ios-export"
VERSION="${IOS_VERSION:-${MARKETING_VERSION:-}}"
BUILD_NUMBER="${IOS_BUILD_NUMBER:-${CURRENT_PROJECT_VERSION:-}}"
ACTION="full"
UPLOAD=0
ALLOW_UNSIGNED=0

while (($#)); do
  case "$1" in
    --archive-path) ARCHIVE_PATH="$2"; shift 2 ;;
    --export-path) EXPORT_PATH="$2"; shift 2 ;;
    --version) VERSION="$2"; shift 2 ;;
    --build-number) BUILD_NUMBER="$2"; shift 2 ;;
    --validate-archive) ACTION="validate-archive"; ARCHIVE_PATH="$2"; shift 2 ;;
    --validate-export) ACTION="validate-export"; EXPORT_PATH="$2"; shift 2 ;;
    --upload) UPLOAD=1; shift ;;
    --allow-unsigned) ALLOW_UNSIGNED=1; shift ;;
    --dry-run) ACTION="dry-run"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

require_command() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "error: required command not found: $1" >&2
    exit 1
  }
}

require_version_and_build() {
  [[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || {
    echo "error: provide a SemVer marketing version with --version or IOS_VERSION" >&2
    exit 2
  }
  [[ "$BUILD_NUMBER" =~ ^[1-9][0-9]*$ ]] || {
    echo "error: provide a positive build number with --build-number or IOS_BUILD_NUMBER" >&2
    exit 2
  }
}

validate_source_configuration() {
  local spec="$ROOT_DIR/project.yml"
  local required
  for required in \
    'SymDeskMobile:' \
    'SymDeskMobileShare:' \
    'SymDeskWidget:' \
    'PRODUCT_BUNDLE_IDENTIFIER: com.symaira.desktop.ios' \
    'PRODUCT_BUNDLE_IDENTIFIER: com.symaira.desktop.ios.share' \
    'PRODUCT_BUNDLE_IDENTIFIER: com.symaira.desktop.ios.widget' \
    'Sources/SymDeskMobile/PrivacyInfo.xcprivacy' \
    'Sources/SymDeskMobileShare/PrivacyInfo.xcprivacy' \
    'Sources/SymDeskWidget/PrivacyInfo.xcprivacy'; do
    grep -Fq -- "$required" "$spec" || {
      echo "error: project.yml is missing expected iOS configuration: $required" >&2
      exit 1
    }
  done
  for manifest in \
    "$ROOT_DIR/Sources/SymDeskMobile/PrivacyInfo.xcprivacy" \
    "$ROOT_DIR/Sources/SymDeskMobileShare/PrivacyInfo.xcprivacy" \
    "$ROOT_DIR/Sources/SymDeskWidget/PrivacyInfo.xcprivacy"; do
    [[ -f "$manifest" ]] || { echo "error: privacy manifest not found: $manifest" >&2; exit 1; }
  done
}

plist_value() {
  plutil -extract "$1" raw -o - "$2"
}

validate_bundle() {
  local app="$1"
  local expected_id="$2"
  local label="$3"
  local bundle_id version build
  [[ -d "$app" ]] || { echo "error: $label bundle not found: $app" >&2; exit 1; }
  [[ -f "$app/PrivacyInfo.xcprivacy" ]] || { echo "error: $label privacy manifest missing" >&2; exit 1; }
  bundle_id="$(plist_value CFBundleIdentifier "$app/Info.plist")"
  version="$(plist_value CFBundleShortVersionString "$app/Info.plist")"
  build="$(plist_value CFBundleVersion "$app/Info.plist")"
  [[ "$bundle_id" == "$expected_id" ]] || { echo "error: $label bundle ID is '$bundle_id', expected '$expected_id'" >&2; exit 1; }
  [[ -z "$VERSION" || "$version" == "$VERSION" ]] || { echo "error: $label version is '$version', expected '$VERSION'" >&2; exit 1; }
  [[ -z "$BUILD_NUMBER" || "$build" == "$BUILD_NUMBER" ]] || { echo "error: $label build is '$build', expected '$BUILD_NUMBER'" >&2; exit 1; }
  echo "validated $label: $bundle_id $version ($build)"
}

validate_archive() {
  require_command plutil
  local app="$1/Products/Applications/SymDesk.app"
  validate_bundle "$app" 'com.symaira.desktop.ios' 'main app'
  [[ -d "$app/PlugIns/SymDeskShare.appex" ]] || { echo "error: Share Extension is not embedded" >&2; exit 1; }
  [[ -d "$app/PlugIns/SymDeskWidget.appex" ]] || { echo "error: Widget is not embedded" >&2; exit 1; }
  validate_bundle "$app/PlugIns/SymDeskShare.appex" 'com.symaira.desktop.ios.share' 'Share Extension'
  validate_bundle "$app/PlugIns/SymDeskWidget.appex" 'com.symaira.desktop.ios.widget' 'Widget'
  if [[ "$ALLOW_UNSIGNED" -eq 0 ]] && command -v codesign >/dev/null 2>&1; then
    codesign --verify --deep --strict "$app"
  fi
}

ipa_path() {
  local candidates=()
  shopt -s nullglob
  candidates=("$1"/*.ipa)
  shopt -u nullglob
  [[ ${#candidates[@]} -eq 1 ]] || { echo "error: expected exactly one IPA in $1" >&2; exit 1; }
  printf '%s' "${candidates[0]}"
}

validate_export() {
  require_command plutil
  require_command unzip
  local ipa="$1"
  local listing plist_tmp
  listing="$(unzip -Z1 "$ipa")"
  for path in \
    'Payload/SymDesk.app/Info.plist' \
    'Payload/SymDesk.app/PrivacyInfo.xcprivacy' \
    'Payload/SymDesk.app/PlugIns/SymDeskShare.appex/Info.plist' \
    'Payload/SymDesk.app/PlugIns/SymDeskShare.appex/PrivacyInfo.xcprivacy' \
    'Payload/SymDesk.app/PlugIns/SymDeskWidget.appex/Info.plist' \
    'Payload/SymDesk.app/PlugIns/SymDeskWidget.appex/PrivacyInfo.xcprivacy'; do
    printf '%s\n' "$listing" | grep -Fxq "$path" || { echo "error: exported IPA is missing $path" >&2; exit 1; }
  done
  plist_tmp="$(mktemp)"
  unzip -p "$ipa" 'Payload/SymDesk.app/Info.plist' > "$plist_tmp"
  [[ "$(plist_value CFBundleIdentifier "$plist_tmp")" == 'com.symaira.desktop.ios' ]] || { echo "error: exported app bundle ID mismatch" >&2; exit 1; }
  [[ -z "$VERSION" || "$(plist_value CFBundleShortVersionString "$plist_tmp")" == "$VERSION" ]] || { echo "error: exported app version mismatch" >&2; exit 1; }
  [[ -z "$BUILD_NUMBER" || "$(plist_value CFBundleVersion "$plist_tmp")" == "$BUILD_NUMBER" ]] || { echo "error: exported app build mismatch" >&2; exit 1; }
  rm -f "$plist_tmp"
  echo "validated exported IPA: $ipa"
}

validate_source_configuration

case "$ACTION" in
  dry-run)
    echo "source configuration is valid"
    echo "would archive scheme $SCHEME for generic/platform=iOS"
    echo "would export method app-store-connect to $EXPORT_PATH"
    [[ "$UPLOAD" -eq 0 ]] || echo "would upload the validated IPA using App Store Connect API credentials"
    exit 0
    ;;
  validate-archive)
    require_version_and_build
    validate_archive "$ARCHIVE_PATH"
    ;;
  validate-export)
    require_version_and_build
    validate_export "$(ipa_path "$EXPORT_PATH")"
    ;;
  full)
    require_version_and_build
    require_command xcodegen
    require_command xcodebuild
    team_id="${DEVELOPMENT_TEAM:-${APPLE_TEAM_ID:-}}"
    [[ "$team_id" =~ ^[A-Z0-9]{10}$ ]] || { echo "error: set DEVELOPMENT_TEAM or APPLE_TEAM_ID to the 10-character Apple Team ID" >&2; exit 2; }
    xcodegen generate --spec "$ROOT_DIR/project.yml"
    mkdir -p "$(dirname "$ARCHIVE_PATH")" "$EXPORT_PATH"
    xcodebuild archive \
      -project "$PROJECT" \
      -scheme "$SCHEME" \
      -configuration Release \
      -destination 'generic/platform=iOS' \
      -archivePath "$ARCHIVE_PATH" \
      MARKETING_VERSION="$VERSION" \
      CURRENT_PROJECT_VERSION="$BUILD_NUMBER" \
      DEVELOPMENT_TEAM="$team_id"
    validate_archive "$ARCHIVE_PATH"
    export_options="$(mktemp)"
    trap 'rm -f "$export_options"' EXIT
    printf '%s\n' \
      '<?xml version="1.0" encoding="UTF-8"?>' \
      '<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">' \
      '<plist version="1.0"><dict>' \
      '<key>method</key><string>app-store-connect</string>' \
      '<key>signingStyle</key><string>automatic</string>' \
      "<key>teamID</key><string>$team_id</string>" \
      '</dict></plist>' > "$export_options"
    xcodebuild -exportArchive \
      -archivePath "$ARCHIVE_PATH" \
      -exportPath "$EXPORT_PATH" \
      -exportOptionsPlist "$export_options"
    validate_export "$(ipa_path "$EXPORT_PATH")"
    if [[ "$UPLOAD" -eq 1 ]]; then
      require_command xcrun
      api_key_id="${APP_STORE_CONNECT_API_KEY_ID:-}"
      api_issuer="${APP_STORE_CONNECT_API_ISSUER_ID:-}"
      api_key_path="${APP_STORE_CONNECT_API_PRIVATE_KEY_PATH:-}"
      [[ "$api_key_id" =~ ^[A-Za-z0-9]+$ && -n "$api_issuer" && -f "$api_key_path" ]] || {
        echo "error: set APP_STORE_CONNECT_API_KEY_ID, APP_STORE_CONNECT_API_ISSUER_ID, and APP_STORE_CONNECT_API_PRIVATE_KEY_PATH" >&2
        exit 2
      }
      xcrun altool --upload-app --type ios --file "$(ipa_path "$EXPORT_PATH")" --apiKey "$api_key_id" --apiIssuer "$api_issuer"
    fi
    ;;
esac
