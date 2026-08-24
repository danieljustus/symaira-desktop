#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

MANIFESTS=(
  "go.mod"
  "ingest/go.mod"
  "print/go.mod"
  "relate/go.mod"
  "room/go.mod"
  "seek/go.mod"
)

ROOT_PIN=""
MISMATCHES=0

for rel in "${MANIFESTS[@]}"; do
  manifest="${REPO_ROOT}/${rel}"
  if [ ! -f "$manifest" ]; then
    echo "::error::manifest not found: ${rel}" >&2
    MISMATCHES=$((MISMATCHES + 1))
    continue
  fi

  pin=$(awk '/github\.com\/danieljustus\/symaira-corekit/ {print $2}' "$manifest" | head -1)
  if [ -z "$pin" ]; then
    echo "::error::${rel} has no symaira-corekit dependency pin" >&2
    MISMATCHES=$((MISMATCHES + 1))
    continue
  fi

  if [ -z "$ROOT_PIN" ]; then
    ROOT_PIN="$pin"
    echo "Root symaira-corekit pin: ${ROOT_PIN}"
  else
    if [ "$pin" != "$ROOT_PIN" ]; then
      echo "::error::${rel} pin (${pin}) does not match root pin (${ROOT_PIN})" >&2
      MISMATCHES=$((MISMATCHES + 1))
    else
      echo "${rel}: ${pin} (OK)"
    fi
  fi
done

if [ "$MISMATCHES" -ne 0 ]; then
  echo "Found ${MISMATCHES} corekit pin mismatch(es) across modules." >&2
  exit 1
fi

echo "All 6 modules have consistent symaira-corekit pin (${ROOT_PIN})."
