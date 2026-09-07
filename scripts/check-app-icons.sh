#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
Usage: scripts/check-app-icons.sh [--bundle APP] [--platform macOS|iOS]

Without --bundle, validate the Icon Composer source, legacy fallback catalogs,
and their hashes and dimensions. With --bundle, also prove that Xcode emitted
CFBundleIconName=AppIcon and an AppIcon rendition in the compiled Assets.car.
EOF
}

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUNDLE_PATH=""
PLATFORM=""
while (($#)); do
  case "$1" in
    --bundle)
      [[ $# -ge 2 ]] || { echo "error: --bundle requires a path" >&2; exit 2; }
      BUNDLE_PATH="$2"
      shift 2
      ;;
    --platform)
      [[ $# -ge 2 ]] || { echo "error: --platform requires macOS or iOS" >&2; exit 2; }
      PLATFORM="$2"
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ -n "$BUNDLE_PATH" && "$PLATFORM" != "macOS" && "$PLATFORM" != "iOS" ]]; then
  echo "error: --platform macOS or iOS is required with --bundle" >&2
  exit 2
fi

xcrun_tool() {
  if [[ -n "${DEVELOPER_DIR:-}" ]]; then
    DEVELOPER_DIR="$DEVELOPER_DIR" xcrun "$@"
  else
    xcrun "$@"
  fi
}

CANONICAL="$ROOT_DIR/assets/app-icon"
python3 - "$ROOT_DIR" "$CANONICAL" <<'PY'
from hashlib import sha256
from pathlib import Path
import json
import struct
import sys

root = Path(sys.argv[1])
canonical = Path(sys.argv[2])
icon_json = canonical / "AppIcon.icon" / "icon.json"
icon_assets = canonical / "AppIcon.icon" / "Assets"
iconset = canonical / "AppIcon.iconset"
opaque = canonical / "exports" / "ios-1024-opaque.png"
icns = canonical / "exports" / "AppIcon.icns"
expected_iconset = {
    "icon_16x16.png", "icon_16x16@2x.png",
    "icon_32x32.png", "icon_32x32@2x.png",
    "icon_128x128.png", "icon_128x128@2x.png",
    "icon_256x256.png", "icon_256x256@2x.png",
    "icon_512x512.png", "icon_512x512@2x.png",
}
required = [icon_json, icon_assets / "signet.png", icon_assets / "S.png", opaque, icns]
missing = [str(p.relative_to(root)) for p in required if not p.is_file()]
if missing:
    raise SystemExit("missing canonical icon assets: " + ", ".join(missing))
actual_iconset = {p.name for p in iconset.glob("*.png")}
if actual_iconset != expected_iconset:
    raise SystemExit(
        "canonical iconset files differ: expected "
        + ", ".join(sorted(expected_iconset))
        + "; got " + ", ".join(sorted(actual_iconset))
    )

with icon_json.open() as f:
    document = json.load(f)
serialized = json.dumps(document)
for image_name in ("signet.png", "S.png"):
    if image_name not in serialized:
        raise SystemExit(f"canonical icon.json does not reference {image_name}")
if icns.read_bytes()[:4] != b"icns":
    raise SystemExit("canonical AppIcon.icns is not an icns file")


def png_dimensions(path: Path) -> tuple[int, int]:
    data = path.read_bytes()
    if data[:8] != b"\x89PNG\r\n\x1a\n" or data[12:16] != b"IHDR":
        raise SystemExit(f"{path.relative_to(root)} is not a valid PNG")
    return struct.unpack(">II", data[16:24])


def digest(path: Path) -> str:
    return sha256(path.read_bytes()).hexdigest()

for image in (icon_assets / "signet.png", icon_assets / "S.png", opaque):
    if png_dimensions(image) != (1024, 1024):
        raise SystemExit(f"{image.relative_to(root)} must be 1024x1024")

if (root / "Sources/SymDeskApp").is_dir():
    mac_target = root / "Sources/SymDeskApp/Assets.xcassets/LegacyAppIcon.appiconset"
    ios_target = root / "Sources/SymDeskMobile/Assets.xcassets/LegacyAppIcon.appiconset"
else:
    mac_target = root / "Sources/SymBrainApp/Assets.xcassets/LegacyAppIcon.appiconset"
    ios_target = root / "Sources/SymBrainMobile/Assets.xcassets/LegacyAppIcon.appiconset"
project = root / "project.yml"

mapping = {}
for source_name in sorted(expected_iconset):
    suffix = source_name.removeprefix("icon_")
    for size in ("16x16", "32x32", "128x128", "256x256", "512x512"):
        suffix = suffix.replace(size, size.split("x", 1)[0])
    mapping[mac_target / f"mac-{suffix}"] = iconset / source_name
mapping[ios_target / "ios-1024.png"] = opaque
expected_dimensions = {
    "mac-16.png": (16, 16), "mac-16@2x.png": (32, 32),
    "mac-32.png": (32, 32), "mac-32@2x.png": (64, 64),
    "mac-128.png": (128, 128), "mac-128@2x.png": (256, 256),
    "mac-256.png": (256, 256), "mac-256@2x.png": (512, 512),
    "mac-512.png": (512, 512), "mac-512@2x.png": (1024, 1024),
    "ios-1024.png": (1024, 1024),
}
for target, source in mapping.items():
    if not target.is_file():
        raise SystemExit(f"missing legacy fallback icon: {target.relative_to(root)}")
    target_hash, source_hash = digest(target), digest(source)
    if target_hash != source_hash:
        raise SystemExit(
            f"legacy fallback hash drift for {target.relative_to(root)}: "
            f"target={target_hash} source={source_hash}"
        )
    if png_dimensions(target) != expected_dimensions[target.name]:
        raise SystemExit(
            f"{target.relative_to(root)} has wrong dimensions: "
            f"expected {expected_dimensions[target.name]} got {png_dimensions(target)}"
        )

app_icon_catalogs = sorted(root.glob("Sources/**/AppIcon.appiconset"))
legacy_catalogs = sorted(root.glob("Sources/**/LegacyAppIcon.appiconset"))
if app_icon_catalogs:
    raise SystemExit("same-name AppIcon.appiconset still exists; keep only the Icon Composer .icon named AppIcon")
if set(legacy_catalogs) != {mac_target, ios_target}:
    raise SystemExit("expected exactly one LegacyAppIcon catalog for each root native app")
project_text = project.read_text()
if project_text.count("fileTypes:\n    \"icon\":\n      file: true") != 1:
    raise SystemExit("project.yml must preserve AppIcon.icon as a single wrapper file")
if project_text.count("assets/app-icon/AppIcon.icon") != 2:
    raise SystemExit("project.yml must add AppIcon.icon to both root app targets")
if project_text.count("type: file\n        buildPhase: resources") != 2:
    raise SystemExit("project.yml must add AppIcon.icon as a resource file to both root app targets")
if project_text.count("ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon") != 2:
    raise SystemExit("project.yml must name AppIcon exactly once for macOS and iOS")
if (root / "assets/dmg/.VolumeIcon.icns").is_file() and digest(root / "assets/dmg/.VolumeIcon.icns") != digest(icns):
    raise SystemExit("DMG volume icon drifted from canonical AppIcon.icns")
print(f"Icon Composer source and fallback catalogs are consistent (icon.json sha256={digest(icon_json)})")
PY

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
  test "$icon_name" = AppIcon || {
    echo "error: bundle CFBundleIconName is '$icon_name', expected AppIcon" >&2
    exit 1
  }
  xcrun_tool assetutil --info "$assets_car" | python3 -c '
import json
import sys

def contains_app_icon(value):
    if isinstance(value, dict):
        if value.get("AssetType") == "Icon Image" and value.get("Name") == "AppIcon":
            return True
        return any(contains_app_icon(item) for item in value.values())
    if isinstance(value, list):
        return any(contains_app_icon(item) for item in value)
    return False

if not contains_app_icon(json.load(sys.stdin)):
    raise SystemExit("compiled Assets.car contains no AppIcon rendition")
print("compiled bundle contains AppIcon rendition from the native .icon resource")
'
fi
