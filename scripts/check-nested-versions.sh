#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

python3 - "$REPO_ROOT" << 'PYEOF'
import sys
import subprocess
import json
from pathlib import Path

repo_root = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(".")

manifests = [
    "go.mod",
    "ingest/go.mod",
    "print/go.mod",
    "relate/go.mod",
    "room/go.mod",
    "seek/go.mod",
]

errors = 0
modules_data = {}

for m in manifests:
    p = repo_root / m
    if not p.is_file():
        print(f"::error::manifest not found: {m}", file=sys.stderr)
        errors += 1
        continue
    try:
        res = subprocess.run(
            ["go", "mod", "edit", "-json", str(p)],
            capture_output=True,
            text=True,
            check=True,
        )
        modules_data[m] = json.loads(res.stdout)
    except Exception as e:
        print(f"::error::failed to parse {m}: {e}", file=sys.stderr)
        errors += 1

if errors > 0:
    sys.exit(1)

# 1. Verify Go version directive alignment across all manifests
root_go = modules_data["go.mod"].get("Go", "")
if not root_go:
    print("::error::root go.mod is missing a Go version directive", file=sys.stderr)
    errors += 1
else:
    print(f"Root Go version: {root_go}")

for m in manifests[1:]:
    mod_go = modules_data[m].get("Go", "")
    if mod_go != root_go:
        print(f"::error::{m} Go version ({mod_go}) does not match root Go version ({root_go})", file=sys.stderr)
        errors += 1
    else:
        print(f"{m}: Go {mod_go} (OK)")

# 2. Verify shared dependency versions alignment across all manifests
dep_map = {}
local_modules = {
    "github.com/danieljustus/symaira-desktop",
    "github.com/danieljustus/symaira-ingest",
    "github.com/danieljustus/symaira-print",
    "github.com/danieljustus/symaira-relate",
    "github.com/danieljustus/symaira-room",
    "github.com/danieljustus/symaira-seek",
}

for m in manifests:
    reqs = modules_data[m].get("Require") or []
    for req in reqs:
        dep_path = req.get("Path")
        dep_ver = req.get("Version")
        if not dep_path or not dep_ver or dep_path in local_modules:
            continue
        if dep_path not in dep_map:
            dep_map[dep_path] = {}
        dep_map[dep_path][m] = dep_ver

shared_deps = 0
for dep_path, occurrences in sorted(dep_map.items()):
    if len(occurrences) > 1:
        shared_deps += 1
        versions = set(occurrences.values())
        if len(versions) > 1:
            print(f"::error::Dependency version mismatch for {dep_path}:", file=sys.stderr)
            for mod_name, ver in occurrences.items():
                print(f"  {mod_name}: {ver}", file=sys.stderr)
            errors += 1
        else:
            ver = next(iter(versions))
            print(f"{dep_path}: {ver} (OK across {len(occurrences)} modules)")

if errors != 0:
    print(f"Found {errors} version mismatch(es) across modules.", file=sys.stderr)
    sys.exit(1)

print(f"All 6 modules have consistent Go version ({root_go}) and {shared_deps} shared dependencies.")
PYEOF
