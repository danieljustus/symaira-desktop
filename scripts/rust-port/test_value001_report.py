import copy
import json
import os
import unittest
from pathlib import Path

import value001
from value001_report import percentage, ratio_to_percentage, render


ROOT = Path(__file__).resolve().parents[2]

# The two immutable VALUE-001 measurement reports are workspace evidence, not
# repository content: they live beside the repository, so a clean checkout --
# CI included -- legitimately does not have them. Locate them by environment
# override, then by the workspace layout, and skip rather than fail when they
# are absent. Hardcoding one machine's absolute path made every checkout
# without it report a false failure.
RETAINED_NAMES = (
    "value001-desktop-20260914-aac14a91.json",
    "value001-desktop-20260914-aac14a91-rerun.json",
)


def _locate_evidence():
    """Find the workspace evidence directory, or return where we looked.

    Searched upward rather than at a fixed depth, because this repository is
    normally checked out inside a worktree (.worktrees/<branch>) and the
    number of levels to the workspace root differs between layouts.
    """
    override = os.environ.get("SYMDESK_VALUE001_EVIDENCE")
    if override:
        return Path(override)
    for parent in [ROOT, *ROOT.parents]:
        candidate = parent / "docs/intern/rust-resume-evidence"
        if all((candidate / name).is_file() for name in RETAINED_NAMES):
            return candidate
    return ROOT / "docs/intern/rust-resume-evidence"


EVIDENCE = _locate_evidence()
RETAINED_REPORTS = tuple(EVIDENCE / name for name in RETAINED_NAMES)
RETAINED_AVAILABLE = all(path.is_file() for path in RETAINED_REPORTS)
REQUIRES_RETAINED = unittest.skipUnless(
    RETAINED_AVAILABLE,
    f"retained VALUE-001 reports are not present under {EVIDENCE}; "
    "set SYMDESK_VALUE001_EVIDENCE to the directory holding them",
)


class PercentageTests(unittest.TestCase):
    def test_ratio_to_percentage(self):
        for ratio, expected in [(0.10, "10.00%"), (3.0, "300.00%"), (316.24612017633007, "31,624.61%")]:
            with self.subTest(ratio=ratio):
                self.assertEqual(ratio_to_percentage(ratio), expected)
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

    def test_retained_latest_http_ratio_is_rendered_as_percentage(self):
        result = self.load("value001-latest.json")
        before = copy.deepcopy(result)
        report = render(result)
        self.assertIn(
            "http: Go 1.122667 ms; Rust 356.161750 ms; regression 31,624.61%",
            report,
        )
        self.assertIn("Recorded gate passed: false", report)
        self.assertEqual(result, before)

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

    @REQUIRES_RETAINED
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

    @REQUIRES_RETAINED
    def test_corrected_second_retained_report_remains_performance_fail(self):
        result = json.loads(RETAINED_REPORTS[1].read_text(encoding="utf-8"))
        before = copy.deepcopy(result)
        # No mean rewriting: the retained report is validated under the
        # summation of its own era.
        value001.validate_result(result)
        failures = {
            name: regression
            for name, regression in value001.latency_regressions(
                result["metrics"], value001.latency_estimator_of(result)
            ).items()
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
        # The repository's own captures are always checked; the workspace-level
        # measurement reports are added only where they are present.
        captures = sorted(self.RESULTS.glob("value001-*.json"))
        if RETAINED_AVAILABLE:
            captures += list(RETAINED_REPORTS)
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
        expected = 6 if RETAINED_AVAILABLE else 4
        self.assertGreaterEqual(checked, expected, "expected every retained capture")

    @REQUIRES_RETAINED
    def test_the_two_immutable_value001_reports_use_the_left_fold(self):
        # They were produced on Python 3.9.6, where sum() was a plain fold.
        for path in RETAINED_REPORTS:
            document = self.load(path)
            self.assertEqual(document["host"]["python"], "3.9.6", path.name)
            self.assertEqual(value001.summation_of(document), "left_fold", path.name)


class PairedLatencyEstimatorTests(unittest.TestCase):
    """The gate statistic must use the pairing the harness already collects.

    measure_http runs both servers inside one round, alternating which goes
    first, so go[i] and rust[i] are taken milliseconds apart under the same
    machine state. Comparing the two marginal p95 values discards that.
    """

    def load(self, path):
        return json.loads(path.read_text(encoding="utf-8"))

    def test_a_constant_slowdown_is_reported_exactly(self):
        go = [1.0, 2.0, 3.0, 4.0, 10.0]
        pair = {
            "go": {"raw": go, "p95": max(go)},
            "rust": {"raw": [v * 1.25 for v in go], "p95": max(go) * 1.25},
        }
        self.assertAlmostEqual(
            value001.median(value001.paired_ratios(pair, "t")) - 1.0, 0.25
        )

    def test_pairing_requires_aligned_samples(self):
        with self.assertRaises(value001.HarnessError):
            value001.paired_ratios(
                {"go": {"raw": [1.0, 2.0]}, "rust": {"raw": [1.0]}}, "t"
            )
        with self.assertRaises(value001.HarnessError):
            value001.paired_ratios({"go": {"raw": []}, "rust": {"raw": []}}, "t")

    def test_tail_contamination_defeats_the_unpaired_statistic(self):
        """Rust is uniformly 10% faster, but three of its rounds got unlucky.

        This is the failure mode the real runs show: the p95 is a single
        order statistic, so a handful of environment hiccups on one side move
        it arbitrarily far while the actual per-pair relationship is
        unchanged. The paired estimator is unaffected by them.
        """
        n = 100
        go = [1.0] * n
        rust = [0.9] * n
        # The nearest-rank p95 of 100 samples is index 94, so it takes six
        # unlucky rounds -- not a majority, not even close -- to move it.
        for index in range(94, 100):
            rust[index] = 5.0  # unrelated hiccups, not a Rust regression

        paired = value001.median(
            value001.paired_ratios(
                {"go": {"raw": go}, "rust": {"raw": rust}}, "t"
            )
        ) - 1.0
        self.assertAlmostEqual(paired, -0.10)

        go_p95 = value001.percentile(go, 0.95)
        rust_p95 = value001.percentile(rust, 0.95)
        unpaired = value001.ratio(rust_p95, go_p95) - 1.0
        # The unpaired view turns a 10% win into a catastrophic regression.
        self.assertGreater(unpaired, 1.0)
        self.assertTrue(paired <= 0.10 < unpaired)

    def test_median_interval_is_deterministic_and_contains_the_median(self):
        values = [1.0 + n / 100.0 for n in range(100)]
        first = value001.median_interval(values)
        self.assertEqual(first, value001.median_interval(list(reversed(values))))
        low, high = first
        self.assertLessEqual(low, value001.median(values))
        self.assertGreaterEqual(high, value001.median(values))

    def test_median_interval_needs_at_least_two_samples(self):
        with self.assertRaises(value001.HarnessError):
            value001.median_interval([1.0])

    def test_estimator_must_be_declared_on_current_reports(self):
        for declared in (None, "p95", ""):
            with self.assertRaises(value001.HarnessError):
                value001.latency_estimator_of(
                    {"schema_version": value001.SCHEMA_VERSION,
                     "thresholds": {"latency_estimator": declared}}
                )

    def test_pre_declaration_reports_keep_the_unpaired_estimator(self):
        self.assertEqual(
            value001.latency_estimator_of({"schema_version": 2, "thresholds": {}}),
            "unpaired_p95",
        )
        with self.assertRaises(value001.HarnessError):
            value001.latency_estimator_of(
                {"schema_version": 2,
                 "thresholds": {"latency_estimator": "paired_median_ratio"}}
            )

    def test_unknown_estimator_is_refused(self):
        with self.assertRaises(value001.HarnessError):
            value001.latency_regressions({}, "neumaier")

    @REQUIRES_RETAINED
    def test_the_two_immutable_runs_disagree_unpaired_and_agree_paired(self):
        """The real defect, pinned to the real measurement data.

        The same immutable candidate produced -6.73% and +19.53% for
        http.file-missing under the unpaired statistic. Under the pairing the
        harness itself recorded, both runs agree that Rust is faster.
        """
        unpaired, paired = [], []
        for path in RETAINED_REPORTS:
            document = self.load(path)
            metrics = document["metrics"]
            unpaired.append(
                value001.latency_regressions(metrics, "unpaired_p95")["http.file-missing"]
            )
            paired.append(
                value001.latency_regressions(metrics, "paired_median_ratio")["http.file-missing"]
            )

        # Unpaired: one run passes the 10% ceiling, the other blows through it.
        self.assertLess(unpaired[0], 0.10)
        self.assertGreater(unpaired[1], 0.10)
        self.assertGreater(abs(unpaired[1] - unpaired[0]), 0.25)

        # Paired: both runs agree, and both say Rust is faster.
        for value in paired:
            self.assertLess(value, 0.0)
        self.assertLess(abs(paired[1] - paired[0]), 0.10)

    @REQUIRES_RETAINED
    def test_no_operation_regresses_under_pairing_in_either_run(self):
        for path in RETAINED_REPORTS:
            document = self.load(path)
            regressions = value001.latency_regressions(
                document["metrics"], "paired_median_ratio"
            )
            worst = max(regressions.items(), key=lambda item: item[1])
            self.assertLessEqual(
                worst[1], 0.10, f"{path.name}: {worst[0]} regresses {worst[1]:.2%}"
            )
