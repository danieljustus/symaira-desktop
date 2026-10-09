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

below_threshold() {
  awk -v total="$1" -v threshold="$2" 'BEGIN { exit !(total < threshold) }'
}

if [[ "${1:-}" == "--self-test" ]]; then
  for invalid in -1 100.1 '' NaN 70bad; do
    if "$0" --self-test "$invalid" >/dev/null 2>&1; then
      echo "::error::Coverage gate accepted an invalid threshold" >&2
      exit 1
    fi
  done
  below="$(awk -v threshold="$threshold" 'BEGIN { printf "%.6f", threshold - 0.1 }')"
  if ! below_threshold "$below" "$threshold"; then
    echo "::error::Coverage gate failed to classify ${below}% as below ${threshold}%" >&2
    exit 1
  fi
  if below_threshold "$threshold" "$threshold"; then
    echo "::error::Coverage gate rejected coverage equal to ${threshold}%" >&2
    exit 1
  fi
  printf 'Coverage gate negative control passed: %s%% rejected; %s%% accepted at threshold %s%%.\n' "$below" "$threshold" "$threshold"
  exit 0
fi

profile="$1"
total="$(go tool cover -func="$profile" | tail -n 1 | grep -oE '[0-9]+\.[0-9]+')"
printf 'Total coverage: %s%% (threshold: %s%%)\n' "$total" "$threshold"
if below_threshold "$total" "$threshold"; then
  echo "::error::Coverage ${total}% is below the ${threshold}% gate" >&2
  exit 1
fi
echo 'Coverage gate passed.'
