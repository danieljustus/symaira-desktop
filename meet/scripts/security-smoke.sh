#!/bin/bash
set -euo pipefail

echo "Running security smoke tests..."

ERRORS=0

# Check that no source files contain hardcoded secrets
echo "Checking for hardcoded secrets..."
if grep -rn "ghp_\|sk_live_\|AKIA" Sources/ Tests/ --include="*.swift" 2>/dev/null; then
  echo "FAIL: Hardcoded secrets found in source"
  ERRORS=$((ERRORS + 1))
else
  echo "PASS: No hardcoded secrets"
fi

# Check that artifact files are not world-readable (if any exist)
echo "Checking artifact permissions..."
DATA_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/symaira-meet/meetings"
if [ -d "$DATA_DIR" ]; then
  WORLD_READABLE=$(find "$DATA_DIR" -perm +o=r -type f 2>/dev/null | head -5)
  if [ -n "$WORLD_READABLE" ]; then
    echo "FAIL: World-readable artifact files found:"
    echo "$WORLD_READABLE"
    ERRORS=$((ERRORS + 1))
  else
    echo "PASS: No world-readable artifacts"
  fi
else
  echo "SKIP: No meeting data directory found"
fi

# Check that transcript content is not in log files
echo "Checking for transcript content in logs..."
if grep -rn "transcript\|participant\|audio" Sources/ --glob '*Log*' 2>/dev/null; then
  echo "FAIL: Sensitive content found in log-related files"
  ERRORS=$((ERRORS + 1))
else
  echo "PASS: No sensitive content in logs"
fi

# Verify JSON output goes to stdout only
echo "Checking stdout/stderr separation..."
if grep -rn "FileHandle.standardOutput" Sources/ --include="*.swift" 2>/dev/null | grep -v "writeJSON\|writeLine\|writeRaw\|JSONRPCWriter" | head -5; then
  echo "WARN: Non-standard stdout writes found (review manually)"
else
  echo "PASS: Stdout writes use standard output functions"
fi

# Summary
echo ""
if [ $ERRORS -eq 0 ]; then
  echo "All security smoke tests passed."
else
  echo "FAILED: ${ERRORS} security check(s) failed."
  exit 1
fi
