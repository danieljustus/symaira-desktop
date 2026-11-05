#!/usr/bin/env python3
"""Record the native Go contacts/ingest store-path capture for this OS.

`crates/symdesk-core/tests/store_paths.rs` used to run
`go run ./scripts/rust-port/cmd/configgen --store-paths-root ...` on every OS.
After the Go deletion nothing may need Go, so the real Go capture is recorded
once per OS and committed:

    testdata/port/storage/store-paths-darwin.json   (macOS arm64, this script)
    testdata/port/storage/store-paths-linux.json    (Linux arm64, this script
                                                     inside symdesk-local-gate)

The capture is produced under a fixed private root so a regeneration is
byte-identical; the Rust replay rewrites that root to its own before it
materializes the recorded seed state.

Usage: store_paths_record.py [--check] [--output FILE]
Only the host OS is recorded or checked; the other OS's file is left alone.
"""
import argparse
import os
from pathlib import Path
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
CAPTURE_PARENT = "/tmp/symdesk-store-paths-oracle"


def host_goos() -> str:
    if sys.platform == "darwin":
        return "darwin"
    if sys.platform.startswith("linux"):
        return "linux"
    raise SystemExit(f"store-path captures are recorded for macOS and Linux only, not {sys.platform}")


def capture(destination: Path) -> bytes:
    """Run the real Go producer; return the raw capture document."""
    base = Path(os.path.realpath("/tmp")) / Path(CAPTURE_PARENT).name
    if base.exists():
        shutil.rmtree(base)
    base.mkdir(mode=0o700)
    try:
        root = base / "capture"
        output = root / "native-go.json"
        env = {key: value for key, value in os.environ.items() if not key.startswith("GO") or key in {"GOCACHE", "GOMODCACHE"}}
        env.update(GOTOOLCHAIN="go1.26.10", CGO_ENABLED="0")
        subprocess.run(
            ["go", "run", "./scripts/rust-port/cmd/configgen", "--store-paths-root", str(root), "--output", str(output)],
            cwd=ROOT, env=env, check=True, stdin=subprocess.DEVNULL,
        )
        return output.read_bytes()
    finally:
        shutil.rmtree(base, ignore_errors=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--check", action="store_true", help="fail when the committed capture differs from a fresh Go capture")
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    goos = host_goos()
    destination = args.output or ROOT / f"testdata/port/storage/store-paths-{goos}.json"
    raw = capture(destination)
    if args.check:
        if not destination.is_file() or destination.read_bytes() != raw:
            print(f"{destination.name} drifted from the native Go capture on {goos}", file=sys.stderr)
            return 1
        print(f"PASS {destination.name} matches the native Go capture on {goos}")
        return 0
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_bytes(raw)
    print(f"recorded {destination}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
