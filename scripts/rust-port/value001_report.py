#!/usr/bin/env python3
"""Display VALUE-001 p95 regression percentages without changing gate units.

This reads retained raw evidence; it never runs or approves a benchmark.
"""
import argparse
import json
import math
from pathlib import Path

import value001


def percentage(regression: float) -> str:
    """Format a dimensionless regression ratio as a display percentage."""
    if isinstance(regression, bool) or not isinstance(regression, (int, float)) or not math.isfinite(regression):
        raise ValueError("regression must be a finite number")
    return f"{regression * 100:,.2f}%"


def verified_p95(summary: dict) -> float:
    raw = summary["raw"]
    if not raw or len(raw) != summary["samples"]:
        raise ValueError("raw sample count mismatch")
    if any(isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) or v < 0 for v in raw):
        raise ValueError("invalid raw samples")
    result = sorted(raw)[math.ceil(len(raw) * 0.95) - 1]
    if result != summary["p95"]:
        raise ValueError("p95 differs from retained raw samples")
    return result


def render(result: dict) -> str:
    lines = ["Retained measurement: " + result["captured_at"]]
    recorded = result["thresholds"].get("p95_regressions", {})
    regressions = value001.latency_regressions(result["metrics"])
    for name, ratio in regressions.items():
        if name in recorded and not math.isclose(ratio, recorded[name], rel_tol=1e-12, abs_tol=1e-12):
            raise ValueError("recorded regression differs from retained samples")
        metric_name, _, operation = name.partition(".")
        pair = result["metrics"][metric_name]
        if operation:
            pair = pair["operations"][operation]
        go = verified_p95(pair["go"])
        rust = verified_p95(pair["rust"])
        lines.append(f"{name}: Go {go:.6f} ms; Rust {rust:.6f} ms; regression {percentage(ratio)}")
    lines.append("Recorded gate passed: " + str(result["passed"]).lower())
    return "\n".join(lines)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("result", type=Path)
    args = parser.parse_args()
    print(render(json.loads(args.result.read_text(encoding="utf-8"))))
