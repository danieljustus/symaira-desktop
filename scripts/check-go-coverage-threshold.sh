#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 2 ]]; then
  echo "usage: $0 <coverage-profile|--self-test> <threshold>" >&2
  exit 2
fi
threshold="$2"
if [[ ! "$threshold" =~ ^[0-9]+([.][0-9]+)?$ ]] ||
    ! awk -v threshold="$threshold" 'BEGIN { exit !(threshold >= 0 && threshold <= 100) }'; then
  echo "::error::Coverage threshold must be a number from 0 to 100" >&2
  exit 2
fi

if [[ "${1:-}" == "--self-test" ]]; then
  for invalid in -1 100.1 '' NaN 70bad; do
    if "$0" --self-test "$invalid" >/dev/null 2>&1; then
      echo "::error::Coverage gate accepted an invalid threshold" >&2
      exit 1
    fi
  done
  temporary="$(mktemp -d "${TMPDIR:-${RUNNER_TEMP:-.}}/coverage-gate.XXXXXX")"
  trap 'rm -rf "$temporary"' EXIT
  source_file="$temporary/source.go"
  profile="$temporary/control.cover"
  printf 'package coveragefixture\nfunc covered() { println(1) }\nfunc uncovered() { println(0) }\n' > "$source_file"
  # Structural profile controls, not measurements of production coverage.
  for control in 6999:1 7000:0 7001:0; do
    covered="${control%:*}"
    expected="${control#*:}"
    printf 'mode: atomic\n%s:2.18,2.28 %s 0\n%s:2.18,2.28 %s 1\n%s:3.20,3.30 %s 0\n' \
      "$source_file" "$covered" "$source_file" "$covered" "$source_file" "$((10000 - covered))" > "$profile"
    actual=0
    "$0" "$profile" 70 > "$temporary/result.log" 2>&1 || actual="$?"
    if [[ "$actual" -ne "$expected" ]]; then
      printf '::error::Coverage profile control %s/10000 returned %s, expected %s\n' "$covered" "$actual" "$expected" >&2
      exit 1
    fi
  done
  for invalid_profile in '' 'mode: atomic' 'mode: invalid' 'mode: atomic\ninvalid'; do
    printf '%b\n' "$invalid_profile" > "$profile"
    if "$0" "$profile" 70 > /dev/null 2>&1; then
      echo '::error::Coverage gate accepted an empty or malformed profile' >&2
      exit 1
    fi
  done
  printf 'Coverage gate controls passed: below/equal/above profile boundary, invalid profiles and threshold %s%%.\n' "$threshold"
  exit 0
fi

profile="$1"
# Keep Go's native profile validation, but never gate on its rounded summary.
go tool cover -func="$profile" > /dev/null
awk -v threshold="$threshold" '
  NR > 1 {
    location = $0
    sub(/[[:space:]]+[0-9]+[[:space:]]+[0-9]+[[:space:]]*$/, "", location)
    statements[location] = $(NF - 1)
    if ($NF > 0) hits[location] = 1
  }
  END {
    for (location in statements) {
      total += statements[location]
      if (hits[location]) covered += statements[location]
    }
    if (total <= 0) {
      print "::error::Coverage profile has no statements" > "/dev/stderr"
      exit 2
    }
    printf "Total coverage: %.6f%% (%d/%d statements; threshold: %s%%)\n", 100 * covered / total, covered, total, threshold
    if (100 * covered < threshold * total) {
      printf "::error::Coverage %d/%d statements is below the %s%% gate\n", covered, total, threshold > "/dev/stderr"
      exit 1
    }
  }
' "$profile"
echo 'Coverage gate passed.'
