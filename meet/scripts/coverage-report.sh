#!/bin/bash
set -euo pipefail

# Export an lcov coverage report from SwiftPM's code-coverage output and
# enforce a line-coverage floor gate (fails the run when coverage regresses).
#
# Usage: scripts/coverage-report.sh [codecov-dir] [output.lcov]
#
# Expects `swift test --enable-code-coverage` to have run already. Requires
# full Xcode (llvm-profdata / llvm-cov via xcrun); Command Line Tools do not
# include XCTest, so the coverage run itself cannot be produced there.
#
# The gate sums LF/LH records across the whole lcov file and exits 1 when the
# resulting line coverage is below the floor. Default floor is 55%; override
# with the COVERAGE_FLOOR environment variable (e.g. COVERAGE_FLOOR=60 make
# coverage). The 80% target is tracked via the coverage issues; this floor
# only prevents regressions while that gap is closed.

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

CODECOV_DIR="${1:-.build/debug/codecov}"
OUT="${2:-coverage.lcov}"

if [ ! -d "$CODECOV_DIR" ]; then
  echo "error: coverage directory '$CODECOV_DIR' not found." >&2
  echo "       Run 'swift test --enable-code-coverage' (or 'make coverage') first." >&2
  exit 1
fi

# Locate every SwiftPM test-bundle binary. Swift 6 emits one bundle per test
# target rather than a single *PackageTests bundle, so all of them must be
# passed to llvm-cov to retain coverage across the package.
TEST_BINS=()
while IFS= read -r -d '' test_bin; do
  TEST_BINS+=("$test_bin")
done < <(find .build -type f -path "*.xctest/Contents/MacOS/*" ! -path "*.dSYM/*" -print0)

if [ "${#TEST_BINS[@]}" -eq 0 ]; then
  echo "error: no .xctest bundle binaries found under .build." >&2
  echo "       Run 'swift test --enable-code-coverage' (or 'make coverage') first." >&2
  exit 1
fi

# SwiftPM writes raw profiles (*.profraw) into the codecov dir; newer
# toolchains may additionally pre-merge default.profdata. Merge the raw
# profiles when present so both layouts work.
PROFDATA="$CODECOV_DIR/coverage.profdata"
if ls "$CODECOV_DIR"/*.profraw >/dev/null 2>&1; then
  xcrun llvm-profdata merge "$CODECOV_DIR"/*.profraw -o "$PROFDATA"
elif [ -f "$CODECOV_DIR/default.profdata" ]; then
  PROFDATA="$CODECOV_DIR/default.profdata"
else
  echo "error: no .profraw or default.profdata files in '$CODECOV_DIR'." >&2
  exit 1
fi

LLVM_COV_ARGS=("${TEST_BINS[0]}")
for test_bin in "${TEST_BINS[@]:1}"; do
  LLVM_COV_ARGS+=( -object "$test_bin" )
done

# The .xctest bundles do not link the package's main executable, so its own
# sources would be missing from the export. Add the executable binary when it
# exists so CLI code shows up in the report (override with MAIN_EXE=...).
MAIN_EXE="${MAIN_EXE:-.build/debug/symmeet}"
if [ -x "$MAIN_EXE" ]; then
  LLVM_COV_ARGS+=( -object "$MAIN_EXE" )
fi

xcrun llvm-cov export -format=lcov -instr-profile "$PROFDATA" "${LLVM_COV_ARGS[@]}" > "$OUT"
echo "wrote $OUT"

# --- coverage floor gate ---
COVERAGE_FLOOR="${COVERAGE_FLOOR:-55}"
if [[ ! "$COVERAGE_FLOOR" =~ ^[0-9]+(\.[0-9]+)?$ ]]; then
  echo "error: COVERAGE_FLOOR='$COVERAGE_FLOOR' is not a number." >&2
  exit 1
fi

# Sum LF (lines found) and LH (lines hit) across every lcov record; % = LH/LF.
# Only package production sources count toward the gate: records from
# dependency checkouts (.build/checkouts) and generated/derived files would
# otherwise dominate the denominator and hide regressions in our own code.
LF_TOTAL=0
LH_TOTAL=0
CURRENT_SF=""
while IFS=: read -r key value; do
  case "$key" in
    SF)
      CURRENT_SF="$value"
      KEEP=0
      case "$CURRENT_SF" in
        */Sources/*) KEEP=1 ;;
      esac
      case "$CURRENT_SF" in
        */.build/*) KEEP=0 ;;
      esac
      ;;
    LF)
      if [ "$KEEP" -eq 1 ]; then LF_TOTAL=$((LF_TOTAL + ${value//,/})); fi
      ;;
    LH)
      if [ "$KEEP" -eq 1 ]; then LH_TOTAL=$((LH_TOTAL + ${value//,/})); fi
      ;;
  esac
done < "$OUT"

if [ "$LF_TOTAL" -eq 0 ]; then
  echo "error: no LF records found in '$OUT'; cannot compute line coverage." >&2
  exit 1
fi

PERCENT="$(awk -v lf="$LF_TOTAL" -v lh="$LH_TOTAL" 'BEGIN { printf "%.1f", lh * 100 / lf }')"
echo "line coverage: ${PERCENT}% ($LH_TOTAL/$LF_TOTAL lines)"

BELOW_FLOOR="$(awk -v p="$PERCENT" -v f="$COVERAGE_FLOOR" 'BEGIN { print (p < f) ? 1 : 0 }')"
if [ "$BELOW_FLOOR" -eq 1 ]; then
  echo "error: line coverage ${PERCENT}% is below the ${COVERAGE_FLOOR}% floor." >&2
  echo "       Raise coverage toward the 80% target, or override the floor" >&2
  echo "       explicitly with COVERAGE_FLOOR=<pct>." >&2
  exit 1
fi

echo "coverage gate: ${PERCENT}% >= ${COVERAGE_FLOOR}% floor — OK"
