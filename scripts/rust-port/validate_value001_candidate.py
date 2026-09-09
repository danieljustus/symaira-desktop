#!/usr/bin/env python3
"""Fail-closed VALUE-001 approval for one explicit immutable candidate."""
from __future__ import annotations

import argparse
import hashlib
import json
import math
import re
import subprocess
import sys
from pathlib import Path
from typing import Any

import value001

ORACLE = "745c08e8144971c61133c5d0e5d61c7ce405aad2"
EMPTY_SHA256 = hashlib.sha256(b"").hexdigest()
HEX40 = re.compile(r"^[0-9a-f]{40}$")
HEX64 = re.compile(r"^[0-9a-f]{64}$")
REQUIRED_METRICS = {"startup", "search", "mcp", "http", "rss"}
REQUIRED_OPERATIONS = {
    "mcp": {"initialize", "tools-list", "desk_status", "desk_ls", "desk_search"},
    "http": {"healthz", "status", "snapshot", "file-read", "file-range", "file-missing", "file-traversal"},
}
REQUIRED_CONTRACTS = {
    "./scripts/rust-port/cmd/representativegen",
    "./scripts/rust-port/cmd/diffharness",
    "./scripts/rust-port/cmd/mcpdiff",
    "./scripts/rust-port/cmd/httpdiff",
}



class ValidationError(ValueError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValidationError(message)


def finite(value: Any, label: str) -> float:
    require(isinstance(value, (int, float)) and not isinstance(value, bool) and math.isfinite(value), f"{label} is not finite")
    return float(value)


def check_summary(summary: Any, label: str, unit: str) -> None:
    require(isinstance(summary, dict), f"{label} is not an object")
    required = {"unit", "warmup_samples", "samples", "min", "mean", "p50", "p95", "p99", "max", "raw", "pair_order", "max_observed"}
    require(set(summary) == required, f"{label} keys mismatch")
    require(summary["unit"] == unit, f"{label} unit mismatch")
    samples = summary["samples"]
    require(isinstance(samples, int) and not isinstance(samples, bool) and samples >= value001.MIN_SAMPLES, f"{label} sample count invalid")
    require(isinstance(summary["warmup_samples"], int) and summary["warmup_samples"] >= 1, f"{label} warmup count invalid")
    raw = summary["raw"]
    require(isinstance(raw, list) and len(raw) == samples, f"{label} raw sample count mismatch")
    values = [finite(v, f"{label}.raw") for v in raw]
    require(all(v >= 0 for v in values), f"{label} contains a negative sample")
    ordered = sorted(values)
    expected = {
        "min": min(values), "mean": sum(values) / samples,
        "p50": ordered[math.ceil(samples * .50) - 1],
        "p95": ordered[math.ceil(samples * .95) - 1],
        "p99": ordered[math.ceil(samples * .99) - 1],
        "max": max(values), "max_observed": max(values),
    }
    for key, expected_value in expected.items():
        require(summary[key] == expected_value, f"{label}.{key} does not match raw samples")
    order = summary["pair_order"]
    require(isinstance(order, list) and len(order) == samples and all(v in {"go-rust", "rust-go"} for v in order), f"{label} pair order invalid")


def git(root: Path, *args: str) -> str:
    try:
        return subprocess.run(["git", *args], cwd=root, text=True, capture_output=True, check=True, timeout=30).stdout.strip()
    except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as exc:
        raise ValidationError(f"git verification failed: {exc}") from exc


def regular(path: Path, label: str) -> None:
    require(not path.is_symlink() and path.is_file(), f"{label} must be a regular file")


def validate(path: Path, candidate: str, root: Path, trusted_sha256: str) -> None:
    require(HEX40.fullmatch(candidate) is not None, "--candidate must be a literal 40-character lowercase commit")
    require(HEX64.fullmatch(trusted_sha256) is not None, "--trusted-sha256 must be a 64-character lowercase digest")
    regular(path, "artifact")
    require(hashlib.sha256(path.read_bytes()).hexdigest() == trusted_sha256, "artifact digest differs from trusted SHA256")
    require(root.is_dir() and not root.is_symlink(), "--root must be a real directory")
    require(git(root, "cat-file", "-e", f"{candidate}^{{commit}}") == "", "candidate commit does not exist")
    require(git(root, "rev-parse", candidate) == candidate, "candidate commit identity mismatch")
    require(git(root, "rev-parse", "HEAD") == candidate, "current root HEAD is not the explicit candidate")
    # The capture's recorded status/diff are the historical integrity gate.
    # The candidate checkout may contain separately retained evidence files;
    # source applicability is anchored to its immutable HEAD and tracked diff.
    require(git(root, "diff", "--binary", "HEAD") == "", "current root tracked diff is not empty")

    try:
        result = json.loads(path.read_text(encoding="utf-8"))
        value001.validate_result(result)
    except (OSError, json.JSONDecodeError, KeyError, TypeError, value001.HarnessError) as exc:
        raise ValidationError(f"invalid VALUE-001 result: {exc}") from exc
    repo = result["repository"]
    require(repo["head"] == candidate, "recorded source head is not --candidate")
    require(repo["current_behaviour_oracle_commit"] == ORACLE, "current behavior oracle mismatch")
    require(repo["status"] == "" and repo["candidate_diff_sha256"] == EMPTY_SHA256, "historical capture was not clean")
    require(isinstance(repo.get("dirty_allowed"), bool), "historical dirty_allowed provenance missing")

    binaries = result["binaries"]
    require(set(binaries) == {"go", "rust"}, "binary inventory incomplete")
    require(binaries["go"]["source"] == ORACLE, "Go binary is not the required oracle")
    require(binaries["rust"]["source"] == candidate, "Rust binary source is not --candidate")
    for name, binary in binaries.items():
        require(HEX40.fullmatch(binary["source"]) is not None and HEX64.fullmatch(binary["sha256"]) is not None, f"{name} binary identity invalid")
        require(isinstance(binary.get("bytes"), int) and binary["bytes"] > 0, f"{name} binary size invalid")
        binary_path = Path(binary.get("path", ""))
        if binary_path.exists():
            require(not binary_path.is_symlink() and binary_path.is_file(), f"{name} binary must be a regular file")
            require(hashlib.sha256(binary_path.read_bytes()).hexdigest() == binary["sha256"], f"{name} binary digest mismatch")

    metrics = result["metrics"]
    require(set(metrics) == REQUIRED_METRICS, "metric categories are incomplete")
    for name, metric in metrics.items():
        unit = "bytes" if name == "rss" else "milliseconds"
        check_summary(metric["go"], f"metrics.{name}.go", unit)
        check_summary(metric["rust"], f"metrics.{name}.rust", unit)
        if name in REQUIRED_OPERATIONS:
            require(set(metric["operations"]) == REQUIRED_OPERATIONS[name], f"{name} operation set is incomplete")
            for operation, pair in metric["operations"].items():
                check_summary(pair["go"], f"metrics.{name}.{operation}.go", "milliseconds")
                check_summary(pair["rust"], f"metrics.{name}.{operation}.rust", "milliseconds")

    try:
        regressions = value001.latency_regressions(metrics)
    except (KeyError, TypeError, ZeroDivisionError, value001.HarnessError) as exc:
        raise ValidationError(f"latency gate cannot be recomputed: {exc}") from exc
    thresholds = result["thresholds"]
    recorded = thresholds.get("p95_regressions")
    require(isinstance(recorded, dict) and set(recorded) == set(regressions), "regression inventory is incomplete")
    for name, actual in regressions.items():
        require(finite(actual, name) == recorded[name], f"threshold ratio for {name} is not recomputed")
        require(actual <= 0.10, f"{name} exceeds exact 10% regression limit")
    go_bytes = binaries["go"]["bytes"]
    rust_bytes = binaries["rust"]["bytes"]
    size_reduction = (go_bytes - rust_bytes) / go_bytes
    rss_go = metrics["rss"]["go"]["max"]
    rss_rust = metrics["rss"]["rust"]["max"]
    rss_reduction = (rss_go - rss_rust) / rss_go
    require(finite(thresholds.get("binary_size_reduction"), "binary size reduction") == size_reduction, "binary size reduction is not recomputed")
    require(finite(thresholds.get("representative_rss_reduction_max"), "RSS reduction") == rss_reduction, "RSS reduction is not recomputed")
    require(size_reduction >= 0.20 or rss_reduction >= 0.20, "neither exact 20% improvement criterion passes")
    require(thresholds.get("contracts_pass") is True and thresholds.get("latency_pass") is True and thresholds.get("improvement_pass") is True and result["passed"] is True, "recorded approval is not passing")
    require(isinstance(result["contracts"], list) and {c.get("name") for c in result["contracts"]} == REQUIRED_CONTRACTS, "required contract inventory is incomplete")
    for contract in result["contracts"]:
        require(contract["exit_code"] == 0 and contract["stderr"] == "", "contract outcome is not clean exit 0")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("artifact", type=Path)
    parser.add_argument("--candidate", required=True, help="exact lowercase 40-hex immutable Git commit")
    parser.add_argument("--root", required=True, type=Path, help="candidate checkout used for current approval")
    parser.add_argument("--trusted-sha256", required=True, help="independently supplied artifact SHA256")
    args = parser.parse_args()
    try:
        validate(args.artifact, args.candidate, args.root, args.trusted_sha256)
    except (ValidationError, OSError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        return 1
    print(f"PASS VALUE-001 exact candidate {args.candidate}: {args.artifact}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
