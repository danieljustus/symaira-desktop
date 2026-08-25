#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

MISMATCHES=0

# 1. Check that absorbed library internal packages are never imported outside their own module.
ABSORBED_MODULES=("ingest" "room")

for mod in "${ABSORBED_MODULES[@]}"; do
  import_pattern="github.com/danieljustus/symaira-${mod}/internal"
  
  # Search all .go files in repo excluding the module's own directory and vendor/.
  while IFS= read -r match; do
    [ -z "$match" ] && continue
    echo "::error::Internal import violation: ${match} imports ${import_pattern}" >&2
    MISMATCHES=$((MISMATCHES + 1))
  done < <(grep -rnE "\"${import_pattern}(/|\")" "${REPO_ROOT}" \
    --include="*.go" \
    --exclude-dir="vendor" \
    --exclude-dir=".build" \
    --exclude-dir="${mod}" 2>/dev/null || true)
done

# 2. Check permitted facades in the root Go module (cmd/ and internal/).
# Any import of absorbed modules in the root module must go strictly through permitted facades and api/ packages.

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

  # Check ingest imports
  if grep -qE '"github\.com/danieljustus/symaira-ingest' "$file"; then
    case "$rel_path" in
      internal/ingest/*|internal/mail/*|internal/selfhost/*|internal/testsupport/*|cmd/symdesk/doctor.go|cmd/symdesk/paperless_migrate.go)
        # Permitted facade, must only import api
        if grep -qE '"github\.com/danieljustus/symaira-ingest([^/"]|/[^a]|/a[^p]|/ap[^i]|/api/)' "$file"; then
          echo "::error::Boundary violation: ${rel_path} must only import symaira-ingest/api" >&2
          MISMATCHES=$((MISMATCHES + 1))
        fi
        ;;
      *)
        echo "::error::Facade violation: ${rel_path} imports symaira-ingest directly outside permitted facades" >&2
        MISMATCHES=$((MISMATCHES + 1))
        ;;
    esac
  fi

  # Check room imports in root module (room is a standalone binary symroom, not linked into symdesk)
  if grep -qE '"github\.com/danieljustus/symaira-room' "$file"; then
    echo "::error::Boundary violation: ${rel_path} imports symaira-room (room ships as standalone symroom binary)" >&2
    MISMATCHES=$((MISMATCHES + 1))
  fi

  # Check meet imports in root module (meet is a standalone menu-bar app, not linked into symdesk Go core)
  if grep -qE '"github\.com/danieljustus/symaira-meet' "$file"; then
    echo "::error::Boundary violation: ${rel_path} imports symaira-meet (meet is not linked into Go core)" >&2
    MISMATCHES=$((MISMATCHES + 1))
  fi

done < <(find "${REPO_ROOT}/cmd" "${REPO_ROOT}/internal" -name "*.go" -type f 2>/dev/null || true)

# 3. Check that absorbed library modules do not keep absorbed cmd/ surfaces
for mod in ingest; do
  if [ -d "${REPO_ROOT}/${mod}/cmd" ]; then
    echo "::error::Absorbed tool cmd/ entry point found: ${mod}/cmd (absorbed modules are consumed via api/)" >&2
    MISMATCHES=$((MISMATCHES + 1))
  fi
done

if [ "$MISMATCHES" -ne 0 ]; then
  echo "Found ${MISMATCHES} module boundary violation(s)." >&2
  exit 1
fi

echo "All module boundary checks passed."
