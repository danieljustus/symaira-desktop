import copy
import json
from pathlib import Path
import unittest

from value001_report import percentage, render


class PercentageTests(unittest.TestCase):
    def test_ratio_to_percentage(self):
        for ratio, expected in [(0.10, "10.00%"), (3.0, "300.00%"), (316.24612017633007, "31,624.61%")]:
            with self.subTest(ratio=ratio):
                self.assertEqual(percentage(ratio), expected)

    def test_nonfinite_and_boolean_rejected(self):
        for value in [float("nan"), float("inf"), True, "0.1"]:
            with self.assertRaises(ValueError):
                percentage(value)

    def retained(self):
        root = Path(__file__).resolve().parents[2]
        return json.loads((root / "docs/rust-port/results/value001-latest.json").read_text())

    def test_retained_raw_evidence(self):
        result = self.retained()
        before = copy.deepcopy(result)
        report = render(result)
        self.assertIn("31,624.61%", report)
        self.assertIn("Recorded gate passed: false", report)
        self.assertEqual(result, before)
        self.assertEqual(result["thresholds"]["maximum_p95_regression"], 0.10)

    def test_false_summary_or_ratio_rejected(self):
        result = self.retained()
        result["metrics"]["http"]["rust"]["p95"] = 1
        with self.assertRaises(ValueError):
            render(result)
        result = self.retained()
        result["thresholds"]["p95_regressions"]["http"] = 3.1624612017633007
        with self.assertRaises(ValueError):
            render(result)


if __name__ == "__main__":
    unittest.main()
