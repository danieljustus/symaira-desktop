#!/usr/bin/env bash
# Fails when a symaira-corekit pin was bumped in only some of the files that
# must agree, which is what Dependabot leaves behind:
# - it bumps Package.swift and the tracked Xcode project (project.pbxproj +
#   Package.resolved) but never the XcodeGen spec (project.yml), so the next
#   `xcodegen generate` would silently revert the bump;
# - for a Cargo `tag = "vX.Y.Z"` pin under [workspace.dependencies] it updates
#   only Cargo.lock, so `cargo --locked` fails until Cargo.toml names the tag.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

spec_version() { awk '/symaira-corekit/ && /url:/ {f=6; next} f && /exactVersion:/ {gsub(/["\047]/, "", $2); print $2; exit} f {f--}' "$1"; }
swift_version() { grep -oE 'symaira-corekit(\.git)?"[[:space:]]*,[[:space:]]*exact:[[:space:]]*"[^"]+"' "$1" | grep -oE '"[^"]+"$' | tr -d '"' | head -1 || true; }
pbxproj_version() { awk '/repositoryURL/ && /symaira-corekit/ {f=5; next} f && /[[:space:]]version = / {gsub(/[;"]/, "", $3); print $3; exit} f {f--}' "$1"; }
resolved_version() { awk '/"identity"[[:space:]]*:[[:space:]]*"symaira-corekit"/ {f=8; next} f && /"version"/ {gsub(/[",]/, "", $3); print $3; exit} f {f--}' "$1"; }

status=0
checked=0
while IFS= read -r spec; do
	want="$(spec_version "$spec")"
	[ -n "$want" ] || continue
	dir="$(dirname "$spec")"
	prefix=""
	[ "$dir" = "." ] || prefix="$dir/"
	while IFS= read -r file; do
		[ -n "$file" ] || continue
		case "$file" in
		*/Package.resolved) have="$(resolved_version "$file")" ;;
		*.pbxproj) have="$(pbxproj_version "$file")" ;;
		*) have="$(swift_version "$file")" ;;
		esac
		[ -n "$have" ] || continue
		checked=$((checked + 1))
		if [ "$have" != "$want" ]; then
			echo "::error file=$spec::$spec pins symaira-corekit $want but $file pins $have; update exactVersion in $spec (and regenerate) so both match"
			status=1
		fi
	done < <(git ls-files -- ":(glob)${prefix}Package.swift" ":(glob)${prefix}*.xcodeproj/project.pbxproj" ":(glob)${prefix}*.xcodeproj/project.xcworkspace/xcshareddata/swiftpm/Package.resolved")
done < <(git ls-files -- ':(glob)**/project.yml' | while IFS= read -r f; do grep -q 'symaira-corekit' "$f" && echo "$f"; done)

while IFS= read -r manifest; do
	lock="${manifest%Cargo.toml}Cargo.lock"
	git ls-files --error-unmatch -- "$lock" >/dev/null 2>&1 || continue
	want="$(grep -oE 'symaira-corekit"[^}]*tag[[:space:]]*=[[:space:]]*"v[^"]+"' "$manifest" | grep -oE 'v[^"]+"$' | tr -d '"' | sort -u | tr '\n' ' ' || true)"
	have="$(grep -oE 'symaira-corekit\?tag=v[^#"]+' "$lock" | cut -d= -f2 | sort -u | tr '\n' ' ' || true)"
	[ -n "$want" ] && [ -n "$have" ] || continue
	checked=$((checked + 1))
	if [ "$want" != "$have" ]; then
		echo "::error file=$manifest::$manifest pins symaira-corekit tag ${want% } but $lock resolves ${have% }; set tag = \"${have% }\" in $manifest (Dependabot updates only the lock for [workspace.dependencies])"
		status=1
	fi
done < <(git ls-files -- ':(glob)**/Cargo.toml' | while IFS= read -r f; do grep -q 'symaira-corekit' "$f" && echo "$f"; done)

[ "$status" -eq 0 ] && echo "symaira-corekit pins consistent ($checked manifest(s) checked)"
exit "$status"
