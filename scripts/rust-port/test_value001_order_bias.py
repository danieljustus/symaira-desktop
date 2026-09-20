"""Regression controls for VALUE-001 alternating-order latency gates."""

import copy
import json
import unittest
from pathlib import Path

import value001
from value001_report import render


ROOT = Path(__file__).resolve().parents[2]


class OrderStratifiedLatencyTests(unittest.TestCase):
    @staticmethod
    def pair(go, rust, orders):
        return {
            "go": {"raw": go, "pair_order": orders},
            "rust": {"raw": rust, "pair_order": list(orders)},
        }

    def test_worst_order_median_rejects_a_pooled_false_pass(self):
        """A fast first invocation must not hide a slow second invocation."""
        orders = ["go-rust", "rust-go"] * 50
        go = [1.0] * len(orders)
        rust = [0.4 if order == "go-rust" else 1.11 for order in orders]
        pair = self.pair(go, rust, orders)

        pooled = value001.median(value001.paired_ratios(pair, "http.healthz")) - 1.0
        self.assertLessEqual(pooled, 0.10)

        cohorts = value001.paired_ratio_cohorts(pair, "http.healthz")
        self.assertAlmostEqual(value001.median(cohorts["go-rust"]) - 1.0, -0.60)
        self.assertAlmostEqual(value001.median(cohorts["rust-go"]) - 1.0, 0.11)
        self.assertAlmostEqual(
            value001.order_stratified_paired_regression(pair, "http.healthz"), 0.11
        )

    def test_worst_order_result_does_not_flip_at_a_pooled_boundary(self):
        orders = ["go-rust", "rust-go"] * 50
        go = [1.0] * len(orders)
        for slow_ratio, pooled_passes in ((1.79, True), (1.81, False)):
            with self.subTest(slow_ratio=slow_ratio):
                rust = [0.4 if order == "go-rust" else slow_ratio for order in orders]
                pair = self.pair(go, rust, orders)
                pooled = value001.median(value001.paired_ratios(pair, "http.healthz")) - 1.0
                self.assertEqual(pooled <= 0.10, pooled_passes)
                self.assertGreater(
                    value001.order_stratified_paired_regression(pair, "http.healthz"),
                    0.10,
                )

    def test_order_cohorts_require_matching_balanced_pair_labels(self):
        orders = ["go-rust", "rust-go"] * 50
        pair = self.pair([1.0] * 100, [1.0] * 100, orders)

        mismatched = self.pair([1.0] * 100, [1.0] * 100, orders)
        mismatched["rust"]["pair_order"][0] = "rust-go"
        with self.assertRaisesRegex(value001.HarnessError, "order labels are not aligned"):
            value001.paired_ratio_cohorts(mismatched, "http.healthz")

        unbalanced = self.pair([1.0] * 100, [1.0] * 100, ["go-rust"] * 51 + ["rust-go"] * 49)
        with self.assertRaisesRegex(value001.HarnessError, "order cohorts are not balanced"):
            value001.paired_ratio_cohorts(unbalanced, "http.healthz")

    def test_invalid_order_label_is_rejected(self):
        orders = ["go-rust"] * 50 + ["invalid-order"] * 50
        pair = self.pair([1.0] * 100, [1.0] * 100, orders)
        with self.assertRaisesRegex(value001.HarnessError, "invalid pair order"):
            value001.paired_ratio_cohorts(pair, "http.healthz")

    def test_fewer_than_50_samples_per_cohort_is_rejected(self):
        orders = ["go-rust", "rust-go"] * 49
        pair = self.pair([1.0] * 98, [1.0] * 98, orders)
        with self.assertRaisesRegex(value001.HarnessError, "order cohorts are not balanced"):
            value001.paired_ratio_cohorts(pair, "http.healthz")

    def test_new_default_estimator_is_order_stratified(self):
        self.assertEqual(
            value001.DEFAULT_LATENCY_ESTIMATOR,
            "order_stratified_paired_median_ratio",
        )

    def test_report_shows_each_order_and_the_worst_cohort(self):
        result = json.loads(
            (ROOT / "docs/rust-port/results/value001-operations-5088972a.json").read_text()
        )
        result["schema_version"] = value001.SCHEMA_VERSION
        result["summation"] = "left_fold"
        result["thresholds"]["latency_estimator"] = value001.DEFAULT_LATENCY_ESTIMATOR
        result["thresholds"]["latency_regressions"] = value001.latency_regressions(
            result["metrics"], value001.DEFAULT_LATENCY_ESTIMATOR
        )
        result["thresholds"]["latency_order_regressions"] = value001.latency_order_regressions(
            result["metrics"]
        )

        report = render(result)
        self.assertIn("worst order rust-go; cohorts go-rust", report)
        self.assertIn("rust-go", report)

        tampered = copy.deepcopy(result)
        tampered["thresholds"]["latency_order_regressions"]["http.healthz"]["rust-go"] += 0.01
        with self.assertRaisesRegex(ValueError, "order-stratified regression differs"):
            render(tampered)


if __name__ == "__main__":
    unittest.main(verbosity=2)
