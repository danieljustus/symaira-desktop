from __future__ import annotations

import copy
import hashlib
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("candidate_validator", Path(__file__).with_name("validate_value001_candidate.py"))
assert SPEC and SPEC.loader
validator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(validator)

ROOT = Path(__file__).resolve().parents[2]
ARTIFACT = ROOT / "docs/rust-port/results/value001-operations-5088972a.json"
SCHEMA3_ARTIFACT = ROOT / "docs/rust-port/results/value001-aeab7664.json"
CANDIDATE = "5088972aa7efadfdc7118549354e26d001c1ffad"



class CandidateValidatorTests(unittest.TestCase):
    @staticmethod
    def refresh_current_thresholds(result):
        """Recompute every current-report threshold after a test mutation."""
        metrics = result["metrics"]
        thresholds = result["thresholds"]
        order_regressions = validator.value001.latency_order_regressions(metrics)
        thresholds["latency_regressions"] = validator.value001.latency_regressions(
            metrics, validator.value001.DEFAULT_LATENCY_ESTIMATOR
        )
        thresholds["latency_order_regressions"] = order_regressions
        thresholds["latency_order_regression_intervals"] = (
            validator.value001.latency_order_regression_intervals(metrics)
        )
        thresholds["latency_pass"] = all(
            value <= 0.10
            for by_order in order_regressions.values()
            for value in by_order.values()
        )
        binaries = result["binaries"]
        thresholds["binary_size_reduction"] = (
            binaries["go"]["bytes"] - binaries["rust"]["bytes"]
        ) / binaries["go"]["bytes"]
        rss_go = metrics["rss"]["go"]["max"]
        rss_rust = metrics["rss"]["rust"]["max"]
        thresholds["representative_rss_reduction_max"] = (rss_go - rss_rust) / rss_go
        thresholds["improvement_pass"] = (
            thresholds["binary_size_reduction"] >= 0.20
            or thresholds["representative_rss_reduction_max"] >= 0.20
        )
        thresholds["contracts_pass"] = all(
            item["exit_code"] == 0 for item in result["contracts"]
        )
        result["passed"] = bool(
            thresholds["latency_pass"]
            and thresholds["improvement_pass"]
            and thresholds["contracts_pass"]
        )

    @staticmethod
    def current_candidate_fixture(legacy):
        """Create a current-shape unit fixture from immutable legacy samples.

        The repository fixtures deliberately preserve their historical
        estimators and cannot be used to approve a new candidate. This test
        fixture keeps their provenance shape but makes every synthetic Rust
        sample a uniform 10% win, then records the current order-stratified
        thresholds. It never represents a measured approval.
        """
        result = copy.deepcopy(legacy)
        summation = validator.value001.summation_of(legacy)
        result["schema_version"] = validator.value001.SCHEMA_VERSION
        result["summation"] = summation
        result["repository"]["root"] = str(ROOT.resolve())
        for metric in result["metrics"].values():
            pairs = [metric, *metric.get("operations", {}).values()]
            for pair in pairs:
                go = pair["go"]
                pair["rust"] = validator.value001.summary(
                    [sample * 0.9 for sample in go["raw"]],
                    go["unit"],
                    go["warmup_samples"],
                    go["pair_order"],
                    summation,
                )
        result["thresholds"] = {
            "minimum_improvement": 0.20,
            "maximum_latency_regression": 0.10,
            "latency_estimator": validator.value001.DEFAULT_LATENCY_ESTIMATOR,
            "binary_size_reduction": 0.0,
            "representative_rss_reduction_max": 0.0,
            "improvement_pass": False,
            "latency_regressions": {},
            "latency_order_regressions": {},
            "latency_order_regression_intervals": {},
            "latency_interval_confidence": validator.value001.MEDIAN_INTERVAL_CONFIDENCE,
            "latency_pass": False,
            "contracts_pass": False,
            "fail_closed": True,
        }
        CandidateValidatorTests.refresh_current_thresholds(result)
        return result

    @classmethod
    def setUpClass(cls):
        cls.original = cls.current_candidate_fixture(json.loads(ARTIFACT.read_text()))
        cls.binary_directory = tempfile.TemporaryDirectory(prefix="value001-candidate-binaries-")
        cls.addClassCleanup(cls.binary_directory.cleanup)
        for name, content in (("go", b"g" * 100), ("rust", b"r" * 50)):
            binary_path = Path(cls.binary_directory.name) / name
            binary_path.write_bytes(content)
            binary_path.chmod(0o700)
            cls.original["binaries"][name].update(
                path=str(binary_path),
                bytes=len(content),
                sha256=hashlib.sha256(content).hexdigest(),
            )
        cls.refresh_current_thresholds(cls.original)
        cls.trusted = hashlib.sha256(ARTIFACT.read_bytes()).hexdigest()

    def setUp(self):
        # Isolate Git identity for mutation unit tests; live CLI acceptance
        # is executed separately against the immutable measured checkout.
        def git_identity(root, *args):
            if args in [("rev-parse", "HEAD"), ("rev-parse", CANDIDATE)]:
                return CANDIDATE
            if args == ("cat-file", "-e", CANDIDATE + "^{commit}"):
                return ""
            if args == ("diff", "--binary", "HEAD"):
                return ""
            if args == ("status", "--porcelain=v1", "--untracked-files=all"):
                return ""
            raise AssertionError(f"unexpected Git call: {args}")
        self.git_patch = patch.object(validator, "git", side_effect=git_identity)
        self.git_patch.start()
        self.addCleanup(self.git_patch.stop)

    def check(self, result=None, *, trusted=None, expect=None):
        result = copy.deepcopy(self.original if result is None else result)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "capture.json"
            path.write_text(json.dumps(result, allow_nan=True))
            digest = trusted or hashlib.sha256(path.read_bytes()).hexdigest()
            if expect is None:
                validator.validate(
                    path,
                    CANDIDATE,
                    ROOT,
                    digest,
                    require_order_stratified=True,
                )
            else:
                with self.assertRaises(validator.ValidationError) as raised:
                    validator.validate(
                        path,
                        CANDIDATE,
                        ROOT,
                        digest,
                        require_order_stratified=True,
                    )
                self.assertIn(expect, str(raised.exception))

    def test_current_shape_fixture_passes_with_matching_git_identity(self):
        self.check()

    def test_legacy_pooled_estimator_cannot_approve_a_current_candidate(self):
        legacy = json.loads(ARTIFACT.read_text())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "legacy-capture.json"
            path.write_text(json.dumps(legacy))
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            with self.assertRaisesRegex(
                validator.ValidationError, "schema 4 evidence"
            ):
                validator.validate(
                    path,
                    CANDIDATE,
                    ROOT,
                    digest,
                    require_order_stratified=True,
                )

    def test_cli_requires_order_stratification_for_current_acceptance(self):
        command = [
            "validate_value001_candidate.py",
            str(ARTIFACT),
            "--candidate",
            CANDIDATE,
            "--root",
            str(ROOT),
            "--trusted-sha256",
            self.trusted,
        ]
        with patch.object(sys, "argv", command), patch.object(validator, "validate") as validate:
            self.assertEqual(validator.main(), 0)
        self.assertIs(validate.call_args.kwargs["require_order_stratified"], True)

    def test_order_stratified_threshold_is_recomputed_and_bound(self):
        mutated = copy.deepcopy(self.original)
        mutated["thresholds"]["latency_order_regressions"]["http.healthz"]["rust-go"] += 0.01
        self.check(
            mutated,
            expect="order-stratified regression is not recomputed",
        )

    def test_order_stratified_interval_is_recomputed(self):
        mutated = copy.deepcopy(self.original)
        mutated["thresholds"]["latency_order_regression_intervals"]["http.healthz"]["rust-go"][1] += 0.01
        self.check(
            mutated,
            expect="order-stratified interval is not recomputed",
        )

    def test_untracked_source_in_candidate_checkout_is_rejected(self):
        original_git = validator.git.side_effect
        def dirty_git(root, *args):
            if args == ("status", "--porcelain=v1", "--untracked-files=all"):
                return "?? source.rs"
            return original_git(root, *args)
        validator.git.side_effect = dirty_git
        with self.assertRaisesRegex(validator.ValidationError, "current root is not clean"):
            validator.validate(ARTIFACT, CANDIDATE, ROOT, self.trusted)

    def test_trusted_digest_is_required_and_bound(self):
        with self.assertRaises(validator.ValidationError):
            validator.validate(ARTIFACT, CANDIDATE, ROOT, "0" * 64)

    def test_incomplete_run_marker_blocks_candidate_approval(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "capture.json"
            path.write_text(json.dumps(self.original), encoding="utf-8")
            validator.value001.incomplete_marker_path(path).write_text(
                '{"status":"incomplete"}\n', encoding="utf-8"
            )
            digest = hashlib.sha256(path.read_bytes()).hexdigest()
            with self.assertRaisesRegex(validator.ValidationError, "incomplete-run marker"):
                validator.validate(path, CANDIDATE, ROOT, digest)

    def test_nan_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["metrics"]["http"]["operations"]["status"]["rust"]["raw"][0] = float("nan")
        self.check(mutated, expect="invalid raw values")

    def test_zero_raw_sample_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["metrics"]["http"]["operations"]["status"]["rust"]["raw"][0] = 0.0
        self.check(mutated, expect="invalid raw values")

    def test_schema3_pooled_evidence_cannot_approve_a_current_candidate(self):
        digest = hashlib.sha256(SCHEMA3_ARTIFACT.read_bytes()).hexdigest()
        with self.assertRaisesRegex(validator.ValidationError, "schema 4 evidence"):
            validator.validate(
                SCHEMA3_ARTIFACT,
                CANDIDATE,
                ROOT,
                digest,
                require_order_stratified=True,
            )

    def test_missing_operation_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        del mutated["metrics"]["http"]["operations"]["file-read"]
        self.check(mutated, expect="operations are incomplete")

    def test_altered_recorded_head_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["repository"]["head"] = "0" * 40
        self.check(mutated, expect="recorded source head")

    def test_historical_dirty_capture_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["repository"]["status"] = " M source.rs"
        self.check(mutated, expect="historical capture was not clean")

    def test_schema4_dirty_allowed_must_match_the_published_envelope(self):
        mutated = copy.deepcopy(self.original)
        mutated["repository"]["dirty_allowed"] = False
        self.check(mutated, expect="schema 4 repository provenance is invalid")

    def test_false_contract_outcome_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["contracts"][0]["exit_code"] = 1
        self.check(mutated, expect="contract record")

    def test_wrong_binary_digest_is_rejected_by_trusted_artifact(self):
        mutated = copy.deepcopy(self.original)
        mutated["binaries"]["rust"]["sha256"] = "0" * 64
        self.check(mutated, trusted=self.trusted, expect="artifact digest differs")

    def test_wrong_binary_source_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["binaries"]["rust"]["source"] = "1" * 40
        self.check(mutated, expect="Rust binary source")

    def test_missing_current_binary_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["binaries"]["rust"]["path"] = str(Path(self.binary_directory.name) / "missing")
        self.check(mutated, expect="rust binary must be an executable regular file")

    def test_dangling_current_binary_symlink_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            link = Path(directory) / "rust-link"
            link.symlink_to(Path(directory) / "missing")
            mutated = copy.deepcopy(self.original)
            mutated["binaries"]["rust"]["path"] = str(link)
            self.check(mutated, expect="rust binary must be an executable regular file")

    def test_current_binary_directory_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            mutated = copy.deepcopy(self.original)
            mutated["binaries"]["rust"]["path"] = directory
            self.check(mutated, expect="rust binary must be an executable regular file")

    def test_non_executable_current_binary_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            binary = Path(directory) / "rust"
            binary.write_bytes(b"rust-candidate-fixture")
            binary.chmod(0o600)
            mutated = copy.deepcopy(self.original)
            mutated["binaries"]["rust"]["path"] = str(binary)
            self.check(mutated, expect="rust binary must be an executable regular file")

    def test_current_binary_size_mismatch_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["binaries"]["rust"]["bytes"] += 1
        self.check(mutated, expect="rust binary size mismatch")

    def test_current_binary_digest_mismatch_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["binaries"]["rust"]["sha256"] = "0" * 64
        self.check(mutated, expect="rust binary digest mismatch")

    def test_current_protocol_counts_are_frozen(self):
        mutations = [
            ("runner", "samples", 101, "current runner"),
            ("runner", "warmups", 21, "current runner"),
            ("vault", "documents", 9999, "current vault"),
            ("vault", "search_matches", 99, "current vault"),
        ]
        for side in ("go", "rust"):
            mutations.append((f"index_preparation.{side}", "documents", 9999, f"index_preparation.{side}.documents"))
        for location, field, value, expected in mutations:
            with self.subTest(location=location, field=field):
                mutated = copy.deepcopy(self.original)
                target = mutated
                for part in location.split("."):
                    target = target[part]
                target[field] = value
                self.check(mutated, expect=expected)

    def test_current_summary_counts_and_warmups_are_frozen(self):
        summaries = []
        for category, metric in self.original["metrics"].items():
            summaries.append((f"metrics.{category}.go", metric["go"], 100 if category not in {"mcp", "http"} else (500 if category == "mcp" else 700)))
            summaries.append((f"metrics.{category}.rust", metric["rust"], 100 if category not in {"mcp", "http"} else (500 if category == "mcp" else 700)))
            for operation, pair in metric.get("operations", {}).items():
                summaries.append((f"metrics.{category}.operations.{operation}.go", pair["go"], 100))
                summaries.append((f"metrics.{category}.operations.{operation}.rust", pair["rust"], 100))
        for label, _summary, expected_samples in summaries:
            for field, value in (("samples", expected_samples + 1), ("warmup_samples", 21)):
                with self.subTest(label=label, field=field):
                    mutated = copy.deepcopy(self.original)
                    target = mutated["metrics"]
                    for part in label.split(".")[1:]:
                        target = target[part]
                    target[field] = value
                    self.check(mutated, expect="sample count must be exactly" if field == "samples" else "warmup count must be exactly")

    def test_operation_regression_is_recomputed_and_rejected(self):
        mutated = copy.deepcopy(self.original)
        summary = mutated["metrics"]["http"]["operations"]["status"]["rust"]
        summary["raw"] = [value * 2 for value in summary["raw"]]
        raw_values = summary["raw"]
        values = sorted(raw_values)
        n = len(values)
        summary.update({
            "min": min(values),
            "mean": validator.value001.mean_of(
                raw_values, validator.value001.summation_of(mutated)
            ),
            "p50": values[(n + 1) // 2 - 1], "p95": values[(n * 95 + 99) // 100 - 1],
            "p99": values[(n * 99 + 99) // 100 - 1], "max": max(values), "max_observed": max(values),
        })
        self.refresh_current_thresholds(mutated)
        self.check(mutated, expect="http.status exceeds exact 10% regression limit")

    def test_candidate_is_not_silently_defaulted(self):
        command = ["python3", str(Path(__file__).with_name("validate_value001_candidate.py")), str(ARTIFACT), "--root", str(ROOT), "--trusted-sha256", self.trusted]
        completed = subprocess.run(command, capture_output=True, text=True)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("--candidate", completed.stderr)

    def test_each_operation_regression_fails_with_coherent_summary_and_ratio(self):
        for category, operations in validator.REQUIRED_OPERATIONS.items():
            for operation in sorted(operations):
                with self.subTest(category=category, operation=operation):
                    mutated = copy.deepcopy(self.original)
                    metric = mutated["metrics"][category]
                    go = metric["operations"][operation]["go"]
                    metric["operations"][operation]["rust"] = (
                        validator.value001.summary(
                            [sample * 1.101 for sample in go["raw"]],
                            go["unit"],
                            go["warmup_samples"],
                            go["pair_order"],
                            validator.value001.summation_of(mutated),
                        )
                    )
                    ratios = validator.value001.latency_regressions(
                        mutated["metrics"], validator.value001.latency_estimator_of(mutated)
                    )
                    self.refresh_current_thresholds(mutated)
                    self.assertLessEqual(ratios[category], 0.10)
                    self.assertEqual(
                        metric["rust"], self.original["metrics"][category]["rust"]
                    )
                    self.check(
                        mutated,
                        expect=f"{category}.{operation} exceeds exact 10% regression limit",
                    )

    def test_altered_operation_summary_identity_is_rejected(self):
        for category, operation in (("mcp", "desk_status"), ("http", "file-read")):
            with self.subTest(category=category):
                mutated = copy.deepcopy(self.original)
                mutated["metrics"][category]["operations"][operation]["rust"][
                    "p95"
                ] *= 0.5
                self.check(
                    mutated,
                    expect=f"{category}.operations.{operation}.rust.p95 does not match raw samples",
                )

    def test_extra_operation_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["metrics"]["http"]["operations"]["extra-op"] = copy.deepcopy(
            mutated["metrics"]["http"]["operations"]["status"]
        )
        self.check(mutated, expect="operations are incomplete")

    def test_bad_oracle_identity_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["repository"]["current_behaviour_oracle_commit"] = "0" * 40
        self.check(mutated, expect="current behavior oracle mismatch")

    def test_bad_original_value_baseline_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["repository"]["original_value_baseline_commit"] = "0" * 40
        self.check(mutated, expect="original VALUE baseline mismatch")

    def test_recorded_repository_root_is_bound_to_candidate_root(self):
        mutated = copy.deepcopy(self.original)
        mutated["repository"]["root"] = "/private/tmp/other-candidate"
        self.check(mutated, expect="recorded repository root is not the candidate root")

    def test_threshold_constants_cannot_be_relaxed(self):
        for key in ("minimum_improvement", "maximum_latency_regression"):
            with self.subTest(key=key):
                mutated = copy.deepcopy(self.original)
                mutated["thresholds"][key] = 0.0
                self.check(mutated, expect="threshold changed")

    def test_order_bias_worst_cohort_fails_candidate_validation(self):
        mutated = copy.deepcopy(self.original)
        healthz = mutated["metrics"]["http"]["operations"]["healthz"]
        go = healthz["go"]
        # rust-go regresses by +15%, go-rust is 50% faster (-50%)
        rust_raw = [
            go_sample * (1.15 if order == "rust-go" else 0.50)
            for go_sample, order in zip(go["raw"], go["pair_order"])
        ]
        healthz["rust"] = validator.value001.summary(
            rust_raw,
            go["unit"],
            go["warmup_samples"],
            go["pair_order"],
            validator.value001.summation_of(mutated),
        )
        self.refresh_current_thresholds(mutated)
        # Pooled median regression would pass:
        pooled = (
            validator.value001.median(
                validator.value001.paired_ratios(healthz, "http.healthz")
            )
            - 1.0
        )
        self.assertLessEqual(pooled, 0.10)
        # But candidate validation must reject it because worst cohort is 1.15 - 1 = +15% > 0.10:
        self.check(
            mutated,
            expect="http.healthz exceeds exact 10% regression limit",
        )

    def test_wrong_cohort_label_in_raw_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["metrics"]["http"]["operations"]["healthz"]["go"]["pair_order"][0] = "invalid-order"
        mutated["metrics"]["http"]["operations"]["healthz"]["rust"]["pair_order"][0] = "invalid-order"
        self.check(mutated, expect="invalid pair order")


    def test_unbalanced_current_cohorts_are_rejected(self):
        mutated = copy.deepcopy(self.original)
        for side in ("go", "rust"):
            summary = mutated["metrics"]["startup"][side]
            summary["pair_order"] = ["go-rust"] * summary["samples"]
        self.check(mutated, expect="pair cohorts are not balanced")


if __name__ == "__main__":
    unittest.main(verbosity=2)
