#!/usr/bin/env python3
"""Frozen (Go-free) index-status CLI contract.

The Go side was recorded once (canonical oracle commit pinned in
`testdata/port/provenance.json`; the recorder left the tree with the Go code in
RUST-019, last Go commit 075fdf94 / v0.12.2) into
`testdata/port/cli/index-status-recording.json`: each case's exit code and
stdout/stderr with the disposable world root replaced by a token.

* `replay`  - run the reviewed `capture.py` world builder and case runner with a
  Rust binary (the populated world is seeded by the Rust `index`) and compare
  every case with the recording through `compare.normalize` (world roots, Go
  pointer fields); the 150ms deadline control keeps its two admitted phases.
  `crates/symdesk-cli/tests/index_status_frozen.rs` drives it.

Usage:
    frozen.py replay --rust-bin PATH [--recording FILE]
"""
from __future__ import annotations

import argparse
import contextlib
import json
import os
import shutil
import sys
import tempfile
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
sys.dont_write_bytecode = True
sys.path.insert(0, str(HERE))

import capture  # noqa: E402
from compare import (  # noqa: E402
    DEADLINE_CONTROL_CASE,
    DEADLINE_CONTROL_STDOUT,
    EMPTY_CASES,
    REQUIRED_CASE_IDS,
    inventory_sha256,
    normalize,
)

# The status output reports `database_bytes`, and SQLite pages grow with the length of the
# paths stored in the index. Every world therefore lives below a base of exactly this many
# characters (padded with `x`) inside a short directory of /tmp, whatever TMPDIR of the host is.
WORLDS_BASE_LENGTH = 100
DEFAULT_RECORDING = ROOT / "testdata/port/cli/index-status-recording.json"
PROVENANCE = ROOT / "testdata/port/provenance.json"
TOKENS = {"empty": b"<WORLD_EMPTY>", "populated": b"<WORLD_POPULATED>"}


def pinned_oracle() -> dict[str, str]:
    """The production commit P every recording is bound to (portgen verifies it)."""
    oracle = json.loads(PROVENANCE.read_text())["oracle"]
    return {"commit": oracle["commit"], "release": oracle["release"]}


@contextlib.contextmanager
def short_temp():
    """A scratch directory directly below the real /tmp (its path length must not depend on TMPDIR)."""
    path = tempfile.mkdtemp(prefix="sis-", dir=os.path.realpath("/tmp"))
    try:
        yield Path(os.path.realpath(path))
    finally:
        shutil.rmtree(path, ignore_errors=True)


def fixed_length(path: Path, length: int) -> Path:
    text = str(path)
    if len(text) > length:
        raise RuntimeError(f"temporary directory {text} is longer than the {length}-character world base")
    return Path(text + "x" * (length - len(text)))


def run_role(binary: Path, role: str, base: Path) -> dict[str, dict[str, Any]]:
    """Run the full case inventory with one binary; returns case -> observation."""
    out = base / "c"
    out.mkdir(mode=0o700)
    worlds = fixed_length(base / "w", WORLDS_BASE_LENGTH)
    worlds.mkdir(mode=0o700)
    manifest: dict[str, Any] = {"platform": capture.platform_identity(), "executed_case_ids": [], "runs": []}
    roots: dict[str, Path] = {}
    for populated in (False, True):
        group = "populated" if populated else "empty"
        world_key = f"{role}-{group}"
        paths = capture.prepare_world(worlds, group, populated)
        roots[group] = paths["world"]
        if populated:
            capture.seed_go_index(binary, paths, world_key, out, manifest, role=role)
            capture.add_lifecycle_counterexamples(paths)
        capture.run_status_cases(binary, role, paths, world_key, out, manifest, populated=populated)
    executed = [row["case_id"] for row in manifest["runs"] if row["status_case"]]
    if executed != list(REQUIRED_CASE_IDS):
        raise RuntimeError("executed cases differ from the reviewed inventory")
    observed: dict[str, dict[str, Any]] = {}
    for row in manifest["runs"]:
        if not row["status_case"]:
            continue
        group = "empty" if row["case_id"] in EMPTY_CASES else "populated"
        root = str(roots[group])
        replacements = sorted(
            [(root.encode(), TOKENS[group]), (json.dumps(root, ensure_ascii=False)[1:-1].encode(), TOKENS[group])],
            key=lambda item: len(item[0]), reverse=True)
        observed[row["case_id"]] = {
            "exit_code": row["exit_code"],
            "stdout": normalize((out / row["stdout_file"]).read_bytes(), replacements),
            "stderr": normalize((out / row["stderr_file"]).read_bytes(), replacements),
        }
    return observed


def encode_stream(data: bytes) -> dict[str, str]:
    try:
        return {"text": data.decode("utf-8")}
    except UnicodeDecodeError:
        return {"hex": data.hex()}


def decode_stream(entry: dict[str, str]) -> bytes:
    return entry["text"].encode("utf-8") if "text" in entry else bytes.fromhex(entry["hex"])


def replay(args: argparse.Namespace) -> int:
    recording = json.loads(args.recording.read_text())
    if (recording["schema_version"] != 1 or recording["oracle"] != pinned_oracle()
            or recording["case_inventory_sha256"] != inventory_sha256()
            or [case["id"] for case in recording["cases"]] != list(REQUIRED_CASE_IDS)):
        print("recording does not match the reviewed case inventory", file=sys.stderr)
        return 1
    rust = args.rust_bin.resolve(strict=True)
    with short_temp() as base:
        observed = run_role(rust, "rust", base)
    mismatches = []
    for case in recording["cases"]:
        got = observed[case["id"]]
        expected = {"exit_code": case["exit_code"], "stdout": decode_stream(case["stdout"]),
                    "stderr": decode_stream(case["stderr"])}
        if case["id"] == DEADLINE_CONTROL_CASE:
            # The 150ms whole-worker deadline may report either phase on each side (#1257).
            for side, stdout in (("Go recording", expected["stdout"]), ("Rust", got["stdout"])):
                if stdout not in DEADLINE_CONTROL_STDOUT:
                    mismatches.append(f"{case['id']}: {side} stdout is neither admitted phase body: {stdout!r}")
            expected = {**expected, "stdout": b"<DEADLINE CONTROL PHASE CHECKED>"}
            got = {**got, "stdout": b"<DEADLINE CONTROL PHASE CHECKED>"}
        if got != expected:
            mismatches.append(f"{case['id']}: Go recording {expected!r} != Rust {got!r}"[:600])
    if mismatches:
        print("\n".join(mismatches), file=sys.stderr)
        print(f"{len(mismatches)} of {len(recording['cases'])} index status cases differ from the Go recording", file=sys.stderr)
        return 1
    print(f"PASS index status replay: {len(recording['cases'])} cases match the Go recording")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="mode", required=True)
    rep = sub.add_parser("replay")
    rep.add_argument("--rust-bin", type=Path, required=True)
    rep.add_argument("--recording", type=Path, default=DEFAULT_RECORDING)
    args = parser.parse_args()
    return replay(args)


if __name__ == "__main__":
    raise SystemExit(main())
