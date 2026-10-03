#!/usr/bin/env python3
"""Record three fresh version invocations of a just-built native Rust binary."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import sys
import tempfile


def record(binary: Path, expected_version: str, temp_root: Path) -> dict:
    destination = os.environ.get("SYMDESK_NATIVE_RECORD", "")
    if not destination:
        raise ValueError("SYMDESK_NATIVE_RECORD must name a fresh evidence directory")
    binary = binary.resolve(strict=True)
    temp_root = temp_root.resolve(strict=True)
    if temp_root == Path("/private/tmp") or Path("/private/tmp") in temp_root.parents:
        raise ValueError("native samples must use a temp root outside /private/tmp")
    output = Path(destination)
    output.mkdir(parents=True, exist_ok=False)
    binary_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
    samples = []
    for number in range(1, 4):
        with tempfile.TemporaryDirectory(prefix="symdesk-native-", dir=temp_root) as owned:
            root = Path(owned)
            env = os.environ.copy()
            # The binary never consumes the recording switch. Only this harness
            # writes evidence; each CLI invocation reads fresh, isolated stores.
            env.pop("SYMDESK_NATIVE_RECORD", None)
            for key in ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP"):
                directory = root / key.lower()
                directory.mkdir()
                env[key] = str(directory)
            for key in tuple(env):
                if key.startswith("SYMDESK_"):
                    env.pop(key)
            env.update(LANG="C", LC_ALL="C", TZ="UTC", NO_COLOR="1")
            result = subprocess.run(
                [str(binary), "version", "--json"], cwd=root, env=env,
                capture_output=True, timeout=30, check=False,
            )
            (output / f"sample-{number}.stdout.json").write_bytes(result.stdout)
            (output / f"sample-{number}.stderr.txt").write_bytes(result.stderr)
            if result.returncode != 0 or result.stderr:
                raise ValueError(f"sample {number}: exit {result.returncode}, stderr {result.stderr!r}")
            value = json.loads(result.stdout)
            if not isinstance(value, dict) or type(value.get("schema_version")) is not int or value != {"tool": "symdesk", "version": expected_version, "schema_version": 1}:
                raise ValueError(f"sample {number}: unexpected current-build version document {value!r}")
            samples.append({"sample": number, "exit_code": result.returncode, "document": value})
    if hashlib.sha256(binary.read_bytes()).hexdigest() != binary_hash:
        raise ValueError("binary changed while native samples were running")
    evidence = {
        "record_schema_version": 1,
        "recorded_at": datetime.now(timezone.utc).isoformat(),
        "platform": platform.system(), "architecture": platform.machine(),
        "binary_sha256": binary_hash, "expected_build_version": expected_version,
        "samples": samples,
    }
    (output / "record.json").write_text(json.dumps(evidence, indent=2) + "\n")
    return evidence


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--expected-version", required=True)
    parser.add_argument("--temp-root", type=Path, required=True)
    args = parser.parse_args()
    try:
        evidence = record(args.binary, args.expected_version, args.temp_root)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"native record failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(evidence))
    return 0


if __name__ == "__main__":
    sys.exit(main())
