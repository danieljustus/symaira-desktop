#!/usr/bin/env python3
"""Display VALUE-001 latency-regression percentages without changing gate units.

This reads retained raw evidence; it never runs or approves a benchmark.
"""
import argparse
import json
import math
from pathlib import Path

import value001


def ratio_to_percentage(ratio: float) -> str:
    """Render a stored dimensionless ratio as a human-readable percentage.

    VALUE-001 stores reductions and relative regressions as ratios; a ratio of
    0.10 is therefore displayed as 10.00%, while the stored value stays 0.10.
    """
    if isinstance(ratio, bool) or not isinstance(ratio, (int, float)) or not math.isfinite(ratio):
        raise ValueError("ratio must be a finite number")
    return f"{ratio * 100:,.2f}%"


def percentage(ratio: float) -> str:
    """Backward-compatible name for :func:`ratio_to_percentage`."""
    return ratio_to_percentage(ratio)


def verified_p95(summary: dict, *, require_positive: bool = False) -> float:
    raw = summary["raw"]
    if not raw or len(raw) != summary["samples"]:
        raise ValueError("raw sample count mismatch")
    if any(
        isinstance(v, bool)
        or not isinstance(v, (int, float))
        or not math.isfinite(v)
        or v < 0
        or (require_positive and v == 0)
        for v in raw
    ):
        raise ValueError("invalid raw samples")
    result = sorted(raw)[math.ceil(len(raw) * 0.95) - 1]
    if result != summary["p95"]:
        raise ValueError("p95 differs from retained raw samples")
    return result


def render(result: dict) -> str:
    lines = ["Retained measurement: " + result["captured_at"]]
    estimator = value001.latency_estimator_of(result)
    recorded = value001.recorded_regressions(result)
    regressions = value001.latency_regressions(
        result["metrics"], estimator
    )
    order_regressions = (
        value001.latency_order_regressions(result["metrics"])
        if estimator == value001.DEFAULT_LATENCY_ESTIMATOR
        else None
    )
    recorded_by_order = (result.get("thresholds") or {}).get("latency_order_regressions")
    require_positive = result.get("schema_version") == value001.SCHEMA_VERSION
    for name, regression_ratio in regressions.items():
        if name in recorded and not math.isclose(regression_ratio, recorded[name], rel_tol=1e-12, abs_tol=1e-12):
            raise ValueError("recorded regression differs from retained samples")
        metric_name, _, operation = name.partition(".")
        pair = result["metrics"][metric_name]
        if operation:
            pair = pair["operations"][operation]
        go = verified_p95(pair["go"], require_positive=require_positive)
        rust = verified_p95(pair["rust"], require_positive=require_positive)
        line = f"{name}: Go {go:.6f} ms; Rust {rust:.6f} ms; regression {ratio_to_percentage(regression_ratio)}"
        if order_regressions is not None:
            actual_by_order = order_regressions[name]
            if not isinstance(recorded_by_order, dict) or recorded_by_order.get(name) != actual_by_order:
                raise ValueError("recorded order-stratified regression differs from retained samples")
            worst_order = value001.PAIR_ORDERS[0]
            for order in value001.PAIR_ORDERS[1:]:
                if actual_by_order[order] > actual_by_order[worst_order]:
                    worst_order = order
            cohorts = "; ".join(
                f"{order} {ratio_to_percentage(actual_by_order[order])}"
                for order in value001.PAIR_ORDERS
            )
            line += f"; worst order {worst_order}; cohorts {cohorts}"
        lines.append(line)
    lines.append("Recorded gate passed: " + str(result["passed"]).lower())
    return "\n".join(lines)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("result", type=Path)
    args = parser.parse_args()
    print(render(json.loads(args.result.read_text(encoding="utf-8"))))
