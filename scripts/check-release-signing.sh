#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GORELEASER_CONFIG="$ROOT_DIR/.goreleaser.yml"
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

require_fixed "$GORELEASER_CONFIG" "signs:" "GoReleaser signs artifacts"
require_fixed "$GORELEASER_CONFIG" "- cmd: cosign" "Cosign signer"
require_fixed "$GORELEASER_CONFIG" "- --output-signature" "per-artifact .sig output"
require_fixed "$GORELEASER_CONFIG" "- --output-certificate" "per-artifact .pem output"
require_fixed "$GORELEASER_CONFIG" '${artifact}.sig' "signature artifact template"
require_fixed "$GORELEASER_CONFIG" '${artifact}.pem' "certificate artifact template"
require_fixed "$GORELEASER_CONFIG" "artifacts: all" "archive and checksum coverage"

if grep -Eq -- "COSIGN_KEY|COSIGN_PASSWORD|--key(=|[[:space:]]|$)" "$GORELEASER_CONFIG"; then
  printf "long-lived Cosign key material is forbidden in GoReleaser config\n" >&2
  exit 1
fi

require_fixed "$WORKFLOW" "id-token: write" "GitHub Actions OIDC permission"
require_fixed "$WORKFLOW" "sigstore/cosign-installer@" "Cosign installation"
require_fixed "$WORKFLOW" "cosign-release: v2.4.3" "separate signature and certificate support"
require_fixed "$WORKFLOW" 'codesign --force --sign "$CODESIGN_IDENTITY" --timestamp "$DMG_PATH"' "Developer ID signature on the DMG container"
require_fixed "$WORKFLOW" 'xcrun stapler validate "$DMG_PATH"' "DMG stapling validation"
require_fixed "$WORKFLOW" 'spctl --assess --type open --context context:primary-signature' "DMG Gatekeeper assessment"
require_fixed "$WORKFLOW" 'gh release download "$GITHUB_REF_NAME"' "published DMG redownload"
require_fixed "$WORKFLOW" 'test "$PUBLISHED_SHA256" = "$DMG_SHA256"' "published-byte digest verification"
require_fixed "$README" "cosign verify-blob" "consumer verification command"
require_fixed "$README" "https://token.actions.githubusercontent.com" "OIDC issuer"

printf "release-signing contract: ok\n"
