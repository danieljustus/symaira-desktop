import copy
import json
import unittest
from pathlib import Path

import value001
from value001_report import percentage, render


ROOT = Path(__file__).resolve().parents[2]
EVIDENCE = Path("/Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/Repos/docs/intern/rust-resume-evidence")
RETAINED_REPORTS = (
    EVIDENCE / "value001-desktop-20260914-aac14a91.json",
    EVIDENCE / "value001-desktop-20260914-aac14a91-rerun.json",
)


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

    def test_resumption_report_exposes_operations_in_display_percentages(self):
        result = self.load("value001-resume-956bd3e.json")
        before = copy.deepcopy(result)
        report = render(result)
        self.assertIn(
            "http: Go 1.212209 ms; Rust 1.177625 ms; regression -2.85%", report
        )
        self.assertIn(
            "http.file-read: Go 1.099875 ms; Rust 1.296834 ms; regression 17.91%",
            report,
        )
        self.assertIn(
            "http.file-missing: Go 0.711834 ms; Rust 0.801625 ms; regression 12.61%",
            report,
        )
        self.assertIn("Recorded gate passed: true", report)
        self.assertEqual(result, before)

    def test_altered_operation_summary_identity_is_rejected(self):
        for category, operation in (("mcp", "desk_status"), ("http", "file-read")):
            with self.subTest(category=category):
                result = self.load("value001-resume-956bd3e.json")
                result["metrics"][category]["operations"][operation]["rust"]["p95"] *= 0.5
                before = copy.deepcopy(result)
                with self.assertRaisesRegex(ValueError, "p95 differs from retained raw samples"):
                    render(result)
                self.assertEqual(result, before)

    def test_retained_reports_match_explicit_left_fold_and_reject_mean_mutation(self):
        for path in RETAINED_REPORTS:
            with self.subTest(report=path.name):
                result = json.loads(path.read_text(encoding="utf-8"))
                summaries = []
                for metric in result["metrics"].values():
                    summaries.extend((metric["go"], metric["rust"]))
                    summaries.extend(
                        summary
                        for pair in metric.get("operations", {}).values()
                        for summary in (pair["go"], pair["rust"])
                    )
                self.assertGreater(len(summaries), 0)
                summation = value001.summation_of(result)
                for summary in summaries:
                    expected = value001.mean_of(summary["raw"], summation)
                    self.assertEqual(summary["mean"], expected)
                    mutated = copy.deepcopy(result)
                    mutated_summary = mutated["metrics"]["startup"]["go"]
                    mutated_summary["mean"] += 1e-12
                    with self.assertRaisesRegex(value001.HarnessError, "mean does not match raw samples"):
                        value001.validate_result(mutated)

    def test_corrected_second_retained_report_remains_performance_fail(self):
        result = json.loads(RETAINED_REPORTS[1].read_text(encoding="utf-8"))
        before = copy.deepcopy(result)
        # No mean rewriting: the retained report is validated under the
        # summation of its own era.
        value001.validate_result(result)
        failures = {
            name: regression
            for name, regression in value001.latency_regressions(result["metrics"]).items()
            if regression > 0.10
        }
        self.assertEqual(failures, {"http.file-missing": 0.1953396778916543})
        self.assertFalse(result["passed"])
        self.assertEqual(before["passed"], False)

    def test_left_fold_has_exact_ordered_result_for_portability_regression(self):
        values = [1e16, 1.0, -1e16, 3.0]
        self.assertEqual(value001.left_fold_sum(values), 3.0)


if __name__ == "__main__":
    unittest.main()


class SummationProvenanceTests(unittest.TestCase):
    """A report's derived means must be re-derived the way they were produced.

    CPython 3.12 changed the built-in sum() over floats to compensated
    summation. Current reports declare their algorithm; pre-declaration
    reports have it recovered from the interpreter they record.
    """

    RESULTS = ROOT / "docs/rust-port/results"

    def load(self, path):
        return json.loads(path.read_text(encoding="utf-8"))

    def test_interpreter_before_3_12_means_a_plain_left_fold(self):
        for version in ("3.9.6", "3.11.9", "3.9"):
            self.assertEqual(
                value001.legacy_summation_for({"host": {"python": version}}),
                "left_fold", version,
            )

    def test_interpreter_from_3_12_means_compensated_summation(self):
        for version in ("3.12.0", "3.14.2", "4.0.1"):
            self.assertEqual(
                value001.legacy_summation_for({"host": {"python": version}}),
                "fsum", version,
            )

    def test_pre_declaration_report_without_a_recorded_interpreter_is_refused(self):
        # Fail closed: an unrecorded interpreter must not be guessed at.
        for host in ({}, {"python": ""}, {"python": "unknown"}, {"python": "3"}):
            with self.assertRaises(value001.HarnessError):
                value001.legacy_summation_for({"host": host})
        with self.assertRaises(value001.HarnessError):
            value001.legacy_summation_for({})

    def test_pre_declaration_report_may_not_declare_a_summation(self):
        with self.assertRaises(value001.HarnessError):
            value001.summation_of(
                {"schema_version": 2, "summation": "left_fold",
                 "host": {"python": "3.9.6"}}
            )

    def test_current_report_must_declare_a_known_summation(self):
        for declared in (None, "neumaier", "", 7):
            with self.assertRaises(value001.HarnessError):
                value001.summation_of(
                    {"schema_version": value001.SCHEMA_VERSION,
                     "summation": declared, "host": {"python": "3.14.2"}}
                )

    def test_current_report_summation_is_taken_from_the_declaration(self):
        for declared in ("left_fold", "fsum"):
            self.assertEqual(
                value001.summation_of(
                    {"schema_version": value001.SCHEMA_VERSION,
                     "summation": declared, "host": {"python": "3.14.2"}}
                ),
                declared,
            )

    def test_mean_of_rejects_an_unknown_summation(self):
        with self.assertRaises(value001.HarnessError):
            value001.mean_of([1.0, 2.0], "neumaier")

    def test_every_retained_capture_re_derives_under_its_recovered_summation(self):
        """The real regression: each capture must match its own recorded means.

        Both algorithms are wrong for some captures, which is exactly why the
        algorithm has to come from provenance rather than from the interpreter
        that happens to be running the validator.
        """
        captures = sorted(self.RESULTS.glob("value001-*.json")) + list(RETAINED_REPORTS)
        checked = 0
        for path in captures:
            document = self.load(path)
            if "metrics" not in document:
                continue
            summation = value001.summation_of(document)
            summaries = []
            for metric in document["metrics"].values():
                summaries.extend((metric["go"], metric["rust"]))
                for pair in metric.get("operations", {}).values():
                    summaries.extend((pair["go"], pair["rust"]))
            self.assertGreater(len(summaries), 0, path.name)
            for summary in summaries:
                self.assertEqual(
                    summary["mean"],
                    value001.mean_of(summary["raw"], summation),
                    f"{path.name} under {summation}",
                )
            checked += 1
        self.assertGreaterEqual(checked, 6, "expected every retained capture")

    def test_the_two_immutable_value001_reports_use_the_left_fold(self):
        # They were produced on Python 3.9.6, where sum() was a plain fold.
        for path in RETAINED_REPORTS:
            document = self.load(path)
            self.assertEqual(document["host"]["python"], "3.9.6", path.name)
            self.assertEqual(value001.summation_of(document), "left_fold", path.name)
