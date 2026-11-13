#!/bin/sh
# Build, (on macOS optionally sign) and package one Rust release archive.
#
#   scripts/release/build.sh <os> <arch> <version> [out-dir]
#
# Replaces the GoReleaser `builds` + `archives` stanzas (DIST-001/002). Linux
# targets are static musl binaries, matching the CGO_ENABLED=0 Go binaries
# they replace. CODESIGN_IDENTITY signs the darwin binaries with the hardened
# runtime, as the GoReleaser post hook did.
set -eu

os_name="$1"
arch="$2"
version="$3"
out="${4:-dist}"
root=$(CDPATH="" cd -- "$(dirname -- "$0")/../.." && pwd)
triple=$(python3 "$root/scripts/release/package.py" target "$os_name" "$arch")
target_dir="${CARGO_TARGET_DIR:-$root/target}"

rustup target add --toolchain "$(sed -n 's/^channel = "\(.*\)"/\1/p' "$root/rust-toolchain.toml")" "$triple" >/dev/null
SYMDESK_VERSION="$version" SYMROOM_VERSION="$version" \
  cargo build --manifest-path "$root/Cargo.toml" --release --locked --target "$triple" \
  -p symdesk-cli -p symroom-cli --bin symdesk --bin symroom

bin_dir="$target_dir/$triple/release"
if [ "$os_name" = darwin ] && [ -n "${CODESIGN_IDENTITY:-}" ]; then
  for binary in symdesk symroom; do
    codesign -s "$CODESIGN_IDENTITY" --timestamp --options runtime --force "$bin_dir/$binary"
    codesign --verify --strict "$bin_dir/$binary"
    echo "Signed $bin_dir/$binary"
  done
fi

archive=$(python3 "$root/scripts/release/package.py" archive --version "$version" --os "$os_name" --arch "$arch" \
  --symdesk "$bin_dir/symdesk" --symroom "$bin_dir/symroom" --out "$out")
python3 "$root/scripts/release/package.py" verify --version "$version" "$archive"
