import copy
import json
import unittest
from pathlib import Path

from value001_report import percentage, render


ROOT = Path(__file__).resolve().parents[2]


class PercentageTests(unittest.TestCase):
    def test_ratio_to_percentage(self):
        for ratio, expected in [(0.10, "10.00%"), (3.0, "300.00%"), (316.24612017633007, "31,624.61%")]:
            with self.subTest(ratio=ratio):
                self.assertEqual(percentage(ratio), expected)

    def test_nonfinite_and_boolean_rejected(self):
        for value in [float("nan"), float("inf"), True, "0.1"]:
            with self.assertRaises(ValueError):
                percentage(value)

    def load(self, name):
        return json.loads((ROOT / "docs/rust-port/results" / name).read_text(encoding="utf-8"))

    def test_retained_raw_evidence_for_historical_and_passed_artifacts(self):
        for name in ("value001-latest.json", "value001-retained.json"):
            result = self.load(name)
            before = copy.deepcopy(result)
            report = render(result)
            self.assertIn("Recorded gate passed: " + str(result["passed"]).lower(), report)
            self.assertEqual(result, before)
            self.assertEqual(result["thresholds"]["maximum_p95_regression"], 0.10)

    def test_false_summary_or_ratio_rejected(self):
        result = self.load("value001-latest.json")
        result["metrics"]["http"]["rust"]["p95"] = 1
        with self.assertRaises(ValueError):
            render(result)
        result = self.load("value001-latest.json")
        result["thresholds"]["p95_regressions"]["http"] = 3.1624612017633007
        with self.assertRaises(ValueError):
            render(result)


if __name__ == "__main__":
    unittest.main()
