"""Schema-2/3 evidence stays interpretable after schema-4 hardening."""

import copy
import json
import math
import unittest
from pathlib import Path

import value001


ROOT = Path(__file__).resolve().parents[2]
HISTORICAL_ARTIFACTS = (
    "value001-latest.json",
    "value001-retained.json",
    "value001-aeab7664.json",
)


def refresh_summary(summary: dict, summation: str) -> None:
    values = summary["raw"]
    ordered = sorted(values)
    count = len(values)
    summary.update(
        {
            "min": min(values),
            "mean": value001.mean_of(values, summation),
            "p50": ordered[math.ceil(count * 0.50) - 1],
            "p95": ordered[math.ceil(count * 0.95) - 1],
            "p99": ordered[math.ceil(count * 0.99) - 1],
            "max": max(values),
            "max_observed": max(values),
        }
    )


class HistoricalSchemaCompatibilityTests(unittest.TestCase):
    def test_schema2_and_schema3_artifacts_remain_valid_without_reinterpretation(self):
        versions = set()
        for name in HISTORICAL_ARTIFACTS:
            with self.subTest(name=name):
                result = json.loads((ROOT / "docs/rust-port/results" / name).read_text())
                versions.add(result["schema_version"])
                before = copy.deepcopy(result)
                value001.validate_result(result)
                self.assertEqual(result, before)
        self.assertEqual(versions, {2, 3})

    def test_schema2_and_schema3_allow_a_nonnegative_historical_observation(self):
        for name in HISTORICAL_ARTIFACTS:
            with self.subTest(name=name):
                result = json.loads((ROOT / "docs/rust-port/results" / name).read_text())
                summary = result["metrics"]["rss"]["go"]
                summary["raw"][0] = 0.0
                refresh_summary(summary, value001.summation_of(result))
                value001.validate_result(result)

    def test_schema4_rejects_a_zero_observation(self):
        summary = value001.summary(
            [1.0] * value001.MIN_SAMPLES,
            "milliseconds",
            warmups=1,
            pair_orders=["go-rust", "rust-go"] * (value001.MIN_SAMPLES // 2),
        )
        summary["raw"][0] = 0.0
        with self.assertRaisesRegex(value001.HarnessError, "invalid raw values"):
            value001.validate_sample(
                summary,
                "schema4.synthetic",
                "milliseconds",
                value001.DEFAULT_SUMMATION,
                require_positive=True,
            )


if __name__ == "__main__":
    unittest.main(verbosity=2)
