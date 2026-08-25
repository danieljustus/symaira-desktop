#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

MISMATCHES=0

# 1. Check permitted facades and internal boundaries in the root Go module (cmd/ and internal/).
# Any import of dissolved modules in the root module must go strictly through permitted facades.

while IFS= read -r file; do
  [ -z "$file" ] && continue
  rel_path="${file#"${REPO_ROOT}/"}"

  # Check print imports (dissolved into internal/pdf; no file should import symaira-print)
  if grep -qE '"github\.com/danieljustus/symaira-print' "$file"; then
    echo "::error::Boundary violation: ${rel_path} imports symaira-print (symaira-print is dissolved into internal/pdf)" >&2
    MISMATCHES=$((MISMATCHES + 1))
  fi

  # Check internal/pdf/internal imports (must stay internal to internal/pdf/)
  if grep -qE '"github\.com/danieljustus/symaira-desktop/internal/pdf/internal' "$file"; then
    case "$rel_path" in
      internal/pdf/*)
        ;;
      *)
        echo "::error::Internal import violation: ${rel_path} imports internal/pdf/internal directly (use internal/pdf facade instead)" >&2
        MISMATCHES=$((MISMATCHES + 1))
        ;;
    esac
  fi

  # Check seek imports (dissolved into internal/retrieval; no file should import symaira-seek)
  if grep -qE '"github\.com/danieljustus/symaira-seek' "$file"; then
    echo "::error::Boundary violation: ${rel_path} imports symaira-seek (symaira-seek is dissolved into internal/retrieval)" >&2
    MISMATCHES=$((MISMATCHES + 1))
  fi

  # Check internal/retrieval/internal imports (must stay internal to internal/retrieval/)
  if grep -qE '"github\.com/danieljustus/symaira-desktop/internal/retrieval/internal' "$file"; then
    case "$rel_path" in
      internal/retrieval/*)
        ;;
      *)
        echo "::error::Internal import violation: ${rel_path} imports internal/retrieval/internal directly (use internal/retrieval facade instead)" >&2
        MISMATCHES=$((MISMATCHES + 1))
        ;;
    esac
  fi

  # Check relate imports (dissolved into internal/contacts; no file should import symaira-relate)
  if grep -qE '"github\.com/danieljustus/symaira-relate' "$file"; then
    echo "::error::Boundary violation: ${rel_path} imports symaira-relate (symaira-relate is dissolved into internal/contacts)" >&2
    MISMATCHES=$((MISMATCHES + 1))
  fi

  # Check internal/contacts/internal imports (must stay internal to internal/contacts/)
  if grep -qE '"github\.com/danieljustus/symaira-desktop/internal/contacts/internal' "$file"; then
    case "$rel_path" in
      internal/contacts/*)
        ;;
      *)
        echo "::error::Internal import violation: ${rel_path} imports internal/contacts/internal directly (use internal/contacts facade instead)" >&2
        MISMATCHES=$((MISMATCHES + 1))
        ;;
    esac
  fi

  # Check ingest imports (dissolved into internal/ingest; no file should import symaira-ingest)
  if grep -qE '"github\.com/danieljustus/symaira-ingest' "$file"; then
    echo "::error::Boundary violation: ${rel_path} imports symaira-ingest (symaira-ingest is dissolved into internal/ingest)" >&2
    MISMATCHES=$((MISMATCHES + 1))
  fi

  # Check internal/ingest/internal imports (must stay internal to internal/ingest/)
  if grep -qE '"github\.com/danieljustus/symaira-desktop/internal/ingest/internal' "$file"; then
    case "$rel_path" in
      internal/ingest/*)
        ;;
      *)
        echo "::error::Internal import violation: ${rel_path} imports internal/ingest/internal directly (use internal/ingest facade instead)" >&2
        MISMATCHES=$((MISMATCHES + 1))
        ;;
    esac
  fi

  # Check room imports in root module (dissolved into internal/room and cmd/symroom; no file should import symaira-room)
  if grep -qE '"github\.com/danieljustus/symaira-room' "$file"; then
    echo "::error::Boundary violation: ${rel_path} imports symaira-room (room is dissolved into internal/room and cmd/symroom)" >&2
    MISMATCHES=$((MISMATCHES + 1))
  fi

  # Check internal/room imports (must stay internal to internal/room/ and cmd/symroom/)
  if grep -qE '"github\.com/danieljustus/symaira-desktop/internal/room' "$file"; then
    case "$rel_path" in
      internal/room/*|cmd/symroom/*)
        ;;
      *)
        echo "::error::Internal import violation: ${rel_path} imports internal/room directly (room is a standalone symroom binary, not linked into symdesk)" >&2
        MISMATCHES=$((MISMATCHES + 1))
        ;;
    esac
  fi

  # Check meet imports in root module (meet is a standalone menu-bar app, not linked into symdesk Go core)
  if grep -qE '"github\.com/danieljustus/symaira-meet' "$file"; then
    echo "::error::Boundary violation: ${rel_path} imports symaira-meet (meet is not linked into Go core)" >&2
    MISMATCHES=$((MISMATCHES + 1))
  fi

done < <(find "${REPO_ROOT}/cmd" "${REPO_ROOT}/internal" -name "*.go" -type f 2>/dev/null || true)

if [ "$MISMATCHES" -ne 0 ]; then
  echo "Found ${MISMATCHES} module boundary violation(s)." >&2
  exit 1
fi

echo "All module boundary checks passed."
