#!/usr/bin/env python3
"""Verify or explicitly regenerate the pinned Go DatasetSync time corpus."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

HERE = Path(__file__).resolve().parent
PROBE = HERE / "go_time_probe.go"
RAW = HERE / "go_time_probe.jsonl"
EXPECTED = HERE / "go_time_observations.json"
PROVENANCE = HERE / "provenance.json"


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def observations_from_raw(raw: bytes, expected_count: int | None = None) -> bytes:
    values = [json.loads(line) for line in raw.split(b"\n") if line]
    if expected_count is not None and len(values) != expected_count:
        raise SystemExit(f"expected {expected_count} time observations, found {len(values)}")
    return (json.dumps(values, indent=2, ensure_ascii=False) + "\n").encode()


def verify_oracle_source(go_repo: Path, provenance: dict[str, object]) -> None:
    production = subprocess.check_output(
        [
            "git",
            "-C",
            str(go_repo),
            "show",
            f"{provenance['oracle_revision']}:{provenance['oracle_production_path']}",
        ]
    )
    actual = sha256(production)
    if actual != provenance["oracle_production_sha256"]:
        raise SystemExit(f"pinned Go production source hash mismatch: {actual}")


def verify_committed(go_repo: Path | None) -> None:
    provenance = json.loads(PROVENANCE.read_bytes())
    for field, path in [
        ("time_probe_sha256", PROBE),
        ("time_raw_jsonl_sha256", RAW),
        ("time_observations_sha256", EXPECTED),
    ]:
        actual = sha256(path.read_bytes())
        if actual != provenance[field]:
            raise SystemExit(f"{path.name}: sha256 {actual} != {provenance[field]}")
    if observations_from_raw(
        RAW.read_bytes(), int(provenance["time_boundary_case_count"])
    ) != EXPECTED.read_bytes():
        raise SystemExit("time observations are not the exact LF-byte replay of Go JSONL")

    mutated = json.loads(EXPECTED.read_bytes())
    mutated[0]["utc_date"] = "mutation-control"
    if (json.dumps(mutated, indent=2, ensure_ascii=False) + "\n").encode() == EXPECTED.read_bytes():
        raise SystemExit("mutation control failed to change the expected bytes")

    if go_repo is not None:
        verify_oracle_source(go_repo, provenance)


def fresh_output(go_repo: Path, go_binary: Path) -> bytes:
    completed = subprocess.run(
        [str(go_binary), "run", str(PROBE)],
        cwd=go_repo,
        env={**os.environ, "GOPROXY": "off"},
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if completed.returncode != 0:
        raise SystemExit(completed.stderr.decode(errors="replace"))
    return completed.stdout


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--go-repo", type=Path)
    parser.add_argument(
        "--go-binary", type=Path, default=Path("/Users/daniel/sdk/go1.26.6/bin/go")
    )
    parser.add_argument("--write", action="store_true")
    args = parser.parse_args()

    go_repo = args.go_repo.resolve() if args.go_repo else None
    if args.write:
        if go_repo is None:
            raise SystemExit("--write requires --go-repo so the pinned source can be verified")
        provenance = json.loads(PROVENANCE.read_bytes())
        verify_oracle_source(go_repo, provenance)
    else:
        verify_committed(go_repo)
    if go_repo is None:
        print("PASS: pinned Go time JSONL replays exactly; mutation control changes expected bytes")
        return

    raw = fresh_output(go_repo, args.go_binary.resolve())
    observations = observations_from_raw(
        raw,
        None if args.write else int(json.loads(PROVENANCE.read_bytes())["time_boundary_case_count"]),
    )
    if args.write:
        RAW.write_bytes(raw)
        EXPECTED.write_bytes(observations)
        provenance = json.loads(PROVENANCE.read_bytes())
        provenance["time_probe_sha256"] = sha256(PROBE.read_bytes())
        provenance["time_raw_jsonl_sha256"] = sha256(raw)
        provenance["time_observations_sha256"] = sha256(observations)
        provenance["time_boundary_case_count"] = len(
            [line for line in raw.split(b"\n") if line]
        )
        PROVENANCE.write_text(json.dumps(provenance, indent=2) + "\n")
        print("WROTE Go time JSONL and observations; review and update provenance hashes")
        return
    if raw != RAW.read_bytes() or observations != EXPECTED.read_bytes():
        raise SystemExit("fresh pinned Go time execution differs from committed evidence")
    print("PASS: fresh pinned Go execution matches 21 time observations")


if __name__ == "__main__":
    main()
