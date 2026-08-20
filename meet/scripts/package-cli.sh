#!/bin/bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

VERSION="${1:?Usage: $0 <version>}"
VERSION="${VERSION#v}"
ARCH="${ARCH:-arm64}"
DIST_DIR="dist"
BINARY_DIR="$(swift build -c release --show-bin-path)"
BINARY="$BINARY_DIR/symmeet"
ARCHIVE_NAME="symmeet_v${VERSION}_darwin_${ARCH}.tar.gz"

if [ ! -f "$BINARY" ]; then
  echo "Error: ${BINARY} not found. Run build-release.sh first." >&2
  exit 1
fi

mkdir -p "$DIST_DIR"

SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct HEAD)}"

STAGE=$(mktemp -d)
trap "rm -rf '$STAGE'" EXIT

cp "$BINARY" "$STAGE/symmeet"
cp LICENSE "$STAGE/"

touch -t "$(date -r "$SOURCE_DATE_EPOCH" +%Y%m%d%H%M.%S)" "$STAGE/symmeet"
touch -t "$(date -r "$SOURCE_DATE_EPOCH" +%Y%m%d%H%M.%S)" "$STAGE/LICENSE"

COPYFILE_DISABLE=1 tar -czf "$DIST_DIR/$ARCHIVE_NAME" \
  --uid 0 --gid 0 --numeric-owner \
  -C "$STAGE" symmeet LICENSE

echo "Created ${DIST_DIR}/${ARCHIVE_NAME}"
