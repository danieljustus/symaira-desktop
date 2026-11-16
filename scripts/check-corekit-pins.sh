#!/usr/bin/env bash
# Fails when an XcodeGen spec (project.yml) and the manifests next to it pin
# different symaira-corekit versions. Dependabot bumps Package.swift and the
# tracked Xcode project (project.pbxproj + Package.resolved) but never
# project.yml, so a CoreKit bump must also edit the spec before it merges;
# otherwise the next `xcodegen generate` silently reverts it.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

spec_version() { awk '/symaira-corekit/ && /url:/ {f=6; next} f && /exactVersion:/ {gsub(/["\047]/, "", $2); print $2; exit} f {f--}' "$1"; }
swift_version() { grep -oE 'symaira-corekit(\.git)?"[[:space:]]*,[[:space:]]*exact:[[:space:]]*"[^"]+"' "$1" | grep -oE '"[^"]+"$' | tr -d '"' | head -1; }
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

[ "$status" -eq 0 ] && echo "symaira-corekit pins consistent ($checked manifest(s) checked against their project.yml)"
exit "$status"
