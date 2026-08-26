#!/bin/sh
# Prints the DEVELOPER_DIR of a full Xcode installation (with XCTest), or
# nothing when none is found. Used by the Makefile to un-block `swift test`
# while xcode-select points at Command Line Tools.
#
# Resolution order:
#   1. an explicitly exported DEVELOPER_DIR (when it is a full Xcode)
#   2. the active xcode-select path (when it is a full Xcode)
#   3. /Applications/Xcode.app, then /Applications/Xcode-beta.app
set -u

is_full_xcode() {
	[ -n "${1:-}" ] && [ -x "$1/usr/bin/xcodebuild" ] && {
		case "$1" in
		*CommandLineTools*) return 1 ;;
		esac
		return 0
	}
	return 1
}

for candidate in \
	"${DEVELOPER_DIR-}" \
	"$(xcode-select -p 2>/dev/null || true)" \
	/Applications/Xcode.app/Contents/Developer \
	/Applications/Xcode-beta.app/Contents/Developer
do
	if is_full_xcode "$candidate"; then
		printf '%s\n' "$candidate"
		exit 0
	fi
done
