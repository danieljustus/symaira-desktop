#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: scripts/check-app-icons.sh [--bundle APP] [--platform macOS|iOS]

With no --bundle, validate the canonical icon sources and compile both native
asset catalogs with actool. With --bundle, also prove that the built app has
CFBundleIconName=AppIcon and an AppIcon rendition in its compiled Assets.car.
EOF
}

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUNDLE_PATH=""
PLATFORM=""
while (($#)); do
  case "$1" in
    --bundle) BUNDLE_PATH="$2"; shift 2 ;;
    --platform) PLATFORM="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ -n "$BUNDLE_PATH" && "$PLATFORM" != "macOS" && "$PLATFORM" != "iOS" ]]; then
  echo "error: --platform macOS or iOS is required with --bundle" >&2
  exit 2
fi

xcrun_tool() {
  local developer_dir="${DEVELOPER_DIR:-}"
  if [[ -z "$developer_dir" && -d "/Applications/Xcode-beta.app/Contents/Developer" ]]; then
    developer_dir="/Applications/Xcode-beta.app/Contents/Developer"
  fi
  if [[ -n "$developer_dir" ]]; then
    DEVELOPER_DIR="$developer_dir" xcrun "$@"
  else
    xcrun "$@"
  fi
}

CANONICAL="$ROOT_DIR/assets/app-icon"
python3 - "$ROOT_DIR" "$CANONICAL" <<'PY'
from pathlib import Path
import json
import sys

root = Path(sys.argv[1])
canonical = Path(sys.argv[2])
icon_json = canonical / "AppIcon.icon" / "icon.json"
icon_assets = canonical / "AppIcon.icon" / "Assets"
iconset = canonical / "AppIcon.iconset"
opaque = canonical / "exports" / "ios-1024-opaque.png"
icns = canonical / "exports" / "AppIcon.icns"
required = [icon_json, icon_assets / "signet.png", icon_assets / "S.png", opaque, icns]
required += sorted(iconset.glob("*.png"))
missing = [str(p.relative_to(root)) for p in required if not p.is_file()]
if missing:
    raise SystemExit("missing canonical icon assets: " + ", ".join(missing))

with icon_json.open() as f:
    document = json.load(f)
serialized = json.dumps(document)
for image_name in ("signet.png", "S.png"):
    if image_name not in serialized:
        raise SystemExit(f"canonical icon.json does not reference {image_name}")
if icns.read_bytes()[:4] != b"icns":
    raise SystemExit("canonical AppIcon.icns is not an icns file")
if opaque.read_bytes()[:8] != b"\x89PNG\r\n\x1a\n":
    raise SystemExit("canonical iOS fallback is not a PNG")

if (root / "Sources/SymDeskApp").is_dir():
    mac_target = root / "Sources/SymDeskApp/Assets.xcassets/AppIcon.appiconset"
    ios_target = root / "Sources/SymDeskMobile/Assets.xcassets/AppIcon.appiconset"
    project = root / "project.yml"
else:
    mac_target = root / "Sources/SymBrainApp/Assets.xcassets/AppIcon.appiconset"
    ios_target = root / "Sources/SymBrainMobile/Assets.xcassets/AppIcon.appiconset"
    project = root / "project.yml"

mapping = {}
for source in sorted(iconset.glob("*.png")):
    suffix = source.stem.removeprefix("icon_")
    for size in ("16x16", "32x32", "128x128", "256x256", "512x512"):
        suffix = suffix.replace(size, size.split("x", 1)[0])
    mapping[mac_target / f"mac-{suffix}.png"] = source
mapping[ios_target / "ios-1024.png"] = opaque
mismatched = [str(target.relative_to(root)) for target, source in mapping.items()
              if not target.is_file() or target.read_bytes() != source.read_bytes()]
if mismatched:
    raise SystemExit("native app icon catalog drift: " + ", ".join(mismatched))

catalogs = sorted(root.glob("Sources/**/AppIcon.appiconset"))
if len(catalogs) != 2 or set(catalogs) != {mac_target, ios_target}:
    raise SystemExit("expected exactly one AppIcon catalog for each root native app")
project_text = project.read_text()
if project_text.count("ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon") != 2:
    raise SystemExit("project.yml must wire AppIcon exactly once for macOS and iOS")
if (root / "assets/dmg/.VolumeIcon.icns").is_file() and (root / "assets/dmg/.VolumeIcon.icns").read_bytes() != icns.read_bytes():
    raise SystemExit("DMG volume icon drifted from canonical AppIcon.icns")
print("canonical icon sources and native asset catalogs are consistent")
PY

compile_catalog() {
  local platform="$1" deployment="$2" catalog="$3" output
  output="$(mktemp -d)"
  trap 'rm -rf "$output"' RETURN
  mkdir -p "$output/compiled"
  xcrun_tool actool \
      --compile "$output/compiled" \
      --platform "$platform" \
      --minimum-deployment-target "$deployment" \
      --app-icon AppIcon \
      --output-partial-info-plist "$output/partial.plist" \
      "$catalog" >/dev/null
  test -s "$output/compiled/Assets.car"
  echo "actool compiled AppIcon for $platform: $catalog"
}

compile_catalog macosx 14.0 "$ROOT_DIR/$(if [[ -d "$ROOT_DIR/Sources/SymDeskApp" ]]; then printf '%s' Sources/SymDeskApp; else printf '%s' Sources/SymBrainApp; fi)/Assets.xcassets"
if [[ -d "$ROOT_DIR/Sources/SymDeskMobile" ]]; then
  compile_catalog iphoneos 18.0 "$ROOT_DIR/Sources/SymDeskMobile/Assets.xcassets"
else
  compile_catalog iphoneos 17.0 "$ROOT_DIR/Sources/SymBrainMobile/Assets.xcassets"
fi

if [[ -n "$BUNDLE_PATH" ]]; then
  if [[ "$PLATFORM" == "macOS" ]]; then
    plist="$BUNDLE_PATH/Contents/Info.plist"
    assets_car="$BUNDLE_PATH/Contents/Resources/Assets.car"
  else
    plist="$BUNDLE_PATH/Info.plist"
    assets_car="$BUNDLE_PATH/Assets.car"
  fi
  test -f "$plist" || { echo "error: bundle Info.plist not found: $plist" >&2; exit 1; }
  test -f "$assets_car" || { echo "error: compiled Assets.car not found: $assets_car" >&2; exit 1; }
  icon_name="$(plutil -extract CFBundleIconName raw -o - "$plist")"
  test "$icon_name" = AppIcon || { echo "error: bundle CFBundleIconName is '$icon_name', expected AppIcon" >&2; exit 1; }
  asset_info="$(mktemp)"
  trap 'rm -f "$asset_info"' EXIT
  xcrun_tool assetutil --info "$assets_car" > "$asset_info"
  python3 - "$asset_info" <<'PY'
import json
import sys
from pathlib import Path
entries = json.loads(Path(sys.argv[1]).read_text())
if not any(item.get("AssetType") == "Icon Image" and item.get("Name") == "AppIcon" for item in entries):
    raise SystemExit("compiled Assets.car contains no AppIcon rendition")
print("release bundle contains compiled AppIcon assets")
PY
fi
