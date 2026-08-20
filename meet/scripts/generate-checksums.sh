#!/bin/bash
set -euo pipefail

DIST_DIR="${1:?Usage: $0 <dist-dir>}"
CHECKSUM_FILE="${DIST_DIR}/checksums.txt"

echo "Generating deterministic checksums for ${DIST_DIR}..."

> "$CHECKSUM_FILE"

(
  cd "$DIST_DIR"
  find . -maxdepth 1 \( -name "*.tar.gz" -o -name "*.dmg" -o -name "sbom.spdx.json" \) \
    -type f -not -name "checksums.txt" -exec basename {} \; | sort | while read -r f; do
    shasum -a 256 "./$f"
  done
) > "$CHECKSUM_FILE"

echo "Checksums written to ${CHECKSUM_FILE}"
cat "$CHECKSUM_FILE"
