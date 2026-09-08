#!/usr/bin/env python3
"""Fail-closed validation for the privacy-reviewed VALUE-001 artifact."""
from __future__ import annotations

import argparse

import hashlib
import json
import math
import re
import sys
from pathlib import Path
from typing import cast

import value001

EXPECTED_HEAD = "655d24869bc0e8974087f0b7fb9e4fefd8f9cc18"
EXPECTED_ORACLE = "745c08e8144971c61133c5d0e5d61c7ce405aad2"
EXPECTED_RETAINED_SHA256 = "bfc4f2274b3d2a2ea8881e014cbb350c243420322c4bd43c5fbf58c260d9988c"
EMPTY_DIFF = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
EXPECTED_CATEGORIES = {"startup", "search", "mcp", "http", "rss"}
HEX40 = re.compile(r"^[0-9a-f]{40}$")
HEX64 = re.compile(r"^[0-9a-f]{64}$")


def fail(message: str) -> None:
    raise ValueError(message)


def finite_number(value: object, label: str) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
        fail(f"{label} is not a finite number")
    return float(cast(int | float, value))


def check_summary(summary: dict, label: str) -> None:
    if not isinstance(summary, dict):
        fail(f"{label} is not an object")
    samples = summary.get("samples")
    raw = summary.get("raw")
    if not isinstance(samples, int) or isinstance(samples, bool) or samples < value001.MIN_SAMPLES:
        fail(f"{label} sample count is invalid")
    if not isinstance(raw, list) or len(raw) != samples:
        fail(f"{label} raw sample count mismatch")
    raw_values = cast(list[object], raw)
    values = [finite_number(v, f"{label}.raw") for v in raw_values]
    if any(v < 0 for v in values):
        fail(f"{label} contains a negative sample")
    n = len(values)
    ordered = sorted(values)
    expected = {
        "min": min(values), "mean": sum(values) / n,
        "p50": ordered[math.ceil(n * .50) - 1],
        "p95": ordered[math.ceil(n * .95) - 1],
        "p99": ordered[math.ceil(n * .99) - 1],
        "max": max(values), "max_observed": max(values),
    }
    for key, actual in expected.items():
        if summary.get(key) != actual:
            fail(f"{label}.{key} does not match retained raw samples")
    orders = cast(list[object], summary.get("pair_order"))
    if not isinstance(orders, list) or len(orders) != n or any(o not in {"go-rust", "rust-go"} for o in orders):
        fail(f"{label} pair order is invalid")


def main(path: Path) -> int:
    result = json.loads(path.read_text(encoding="utf-8"))
    metadata_path = path.with_name("value001-retained.metadata.json")
    if not metadata_path.is_file():
        fail("retained provenance sidecar is missing")
    metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    if metadata.get("original_sha256") != "d35eac7b86d0c00eef68c94388266059cc327e2c9356ea69ac6598e223bc43f5":
        fail("original raw artifact digest is not the verified capture")
    if metadata.get("redacted_sha256") != hashlib.sha256(path.read_bytes()).hexdigest():
        fail("retained artifact digest does not match provenance sidecar")
    if metadata.get("source_expected_head") != EXPECTED_HEAD or metadata.get("trusted_ci_run") != "34238241000":
        fail("trusted source/CI provenance is incomplete")
    try:
        value001.validate_result(result)
    except value001.HarnessError as exc:
        fail(str(exc))
    repo = result["repository"]
    if repo["head"] != EXPECTED_HEAD or repo["current_behaviour_oracle_commit"] != EXPECTED_ORACLE:
        fail("repository source identity is not the accepted snapshot")
    if repo["status"] != "" or repo["candidate_diff_sha256"] != EMPTY_DIFF:
        fail("repository is not clean (empty status and empty diff hash required)")
    if repo.get("dirty_allowed") is not True:
        fail("historical dirty_allowed provenance must remain true")
    for label, binary in result["binaries"].items():
        if not HEX40.fullmatch(binary["source"]):
            fail(f"{label} binary source is not a commit id")
        if not HEX64.fullmatch(binary["sha256"]):
            fail(f"{label} binary digest is invalid")
        if not binary["path"].startswith("<redacted"):
            fail(f"{label} binary path was not privacy redacted")
        if re.search(r"/Users/|/var/folders/|[A-Za-z]:\\\\Users\\\\", binary["build_command"]):
            fail(f"{label} build command contains a private path")
    if set(result["metrics"]) != EXPECTED_CATEGORIES:
        fail("metrics categories are incomplete")
    for name, metric in result["metrics"].items():
        check_summary(metric["go"], f"metrics.{name}.go")
        check_summary(metric["rust"], f"metrics.{name}.rust")
        for operation, pair in metric.get("operations", {}).items():
            check_summary(pair["go"], f"metrics.{name}.operations.{operation}.go")
            check_summary(pair["rust"], f"metrics.{name}.operations.{operation}.rust")
    thresholds = result["thresholds"]
    required_operations = {
        "mcp": {"initialize", "tools-list", "desk_status", "desk_ls", "desk_search"},
        "http": {"healthz", "status", "snapshot", "file-read", "file-range", "file-missing", "file-traversal"},
    }
    for name, expected_operations in required_operations.items():
        actual_operations = result["metrics"][name].get("operations")
        if not isinstance(actual_operations, dict) or set(actual_operations) != expected_operations:
            fail(f"{name} required operation set is incomplete")
    regressions: dict[str, float] = {}
    try:
        regressions = value001.latency_regressions(result["metrics"])
    except (KeyError, TypeError, ZeroDivisionError, value001.HarnessError) as exc:
        fail(f"latency gate cannot be recomputed: {exc}")
    recorded = thresholds.get("p95_regressions")
    if not isinstance(recorded, dict):
        fail("p95 regression thresholds are missing")
    for name, actual in regressions.items():
        if name in recorded and recorded[name] != actual:
            fail(f"threshold ratio for {name} is not recomputed from samples")
        if actual > 0.10:
            fail(f"required latency operation {name} exceeds 10% regression")
    if thresholds["contracts_pass"] is not True or thresholds["improvement_pass"] is not True or thresholds["latency_pass"] is not True or result["passed"] is not True:
        fail("retained artifact is not a passing approval")
    for item in result["contracts"]:
        if item["exit_code"] != 0 or item["stderr"]:
            fail("contract evidence is not clean and passing")
        if re.search(r"/Users/|/var/folders/|[A-Za-z]:\\\\Users\\\\", item["command"]):
            fail("contract command contains a private path")
    for side in ("go", "rust"):
        prep = result["index_preparation"][side]
        if re.search(r"/Users/|/var/folders/|[A-Za-z]:\\\\Users\\\\", prep["command"]):
            fail("index preparation command contains a private path")
    # The sidecar is editable alongside the report. Anchor the independently
    # reviewed capture as well, so coherent sample/provenance rewrites cannot
    # manufacture a different passing experiment by updating that sidecar.
    if hashlib.sha256(path.read_bytes()).hexdigest() != EXPECTED_RETAINED_SHA256:
        fail("retained artifact differs from the independently reviewed capture")
    print(f"PASS retained VALUE-001: {path}")
    return 0


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("artifact", type=Path)
    args = parser.parse_args()
    try:
        raise SystemExit(main(args.artifact))
    except (ValueError, KeyError, json.JSONDecodeError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        raise SystemExit(1)
