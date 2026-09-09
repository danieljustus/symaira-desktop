import copy
import json
import tempfile
import unittest
from pathlib import Path

import validate_value001_retained as validator
import value001


ROOT = Path(__file__).resolve().parents[2]
RETAINED = ROOT / "docs/rust-port/results/value001-retained.json"


class RetainedValidatorMutationTests(unittest.TestCase):
    def setUp(self):
        self.result = json.loads(RETAINED.read_text(encoding="utf-8"))

    def assert_rejected(self, mutate, expected=None):
        candidate = copy.deepcopy(self.result)
        mutate(candidate)
        with tempfile.TemporaryDirectory() as directory:
            artifact = Path(directory) / "value001-retained.json"
            artifact.write_text(json.dumps(candidate), encoding="utf-8")
            metadata = json.loads((RETAINED.with_name("value001-retained.metadata.json")).read_text())
            metadata["redacted_sha256"] = __import__("hashlib").sha256(artifact.read_bytes()).hexdigest()
            (Path(directory) / "value001-retained.metadata.json").write_text(json.dumps(metadata))
            with self.assertRaises((ValueError, KeyError)) as raised:
                validator.main(artifact)
            if expected is not None:
                self.assertIn(expected, str(raised.exception))

    def test_old_failing_evidence_is_not_approved_by_operation_gate(self):
        result = json.loads((ROOT / "docs/rust-port/results" / "value001-latest.json").read_text(encoding="utf-8"))
        regressions = value001.latency_regressions(result["metrics"])
        self.assertGreater(regressions["http.snapshot"], 0.10)
        self.assertFalse(all(value <= 0.10 for value in regressions.values()))

    def test_tampered_raw_sample_rejected(self):
        self.assert_rejected(lambda x: x["metrics"]["search"]["go"]["raw"].__setitem__(0, 999999))

    def test_tampered_summary_ratio_rejected(self):
        self.assert_rejected(lambda x: x["thresholds"]["p95_regressions"].__setitem__("http", 0))

    def test_nonfinite_latency_rejected(self):
        self.assert_rejected(lambda x: x["metrics"]["http"]["operations"]["snapshot"]["rust"].__setitem__("p95", float("nan")))

    def test_missing_required_operation_rejected(self):
        self.assert_rejected(lambda x: x["metrics"]["http"]["operations"].pop("snapshot"))

    def test_source_identity_rejected(self):
        self.assert_rejected(lambda x: x["repository"].__setitem__("head", "0" * 40))

    def test_dirty_status_rejected(self):
        self.assert_rejected(lambda x: x["repository"].__setitem__("status", " M Makefile"))

    def test_false_pass_flag_rejected(self):
        self.assert_rejected(lambda x: x.__setitem__("passed", False))

    def test_missing_metric_category_rejected(self):
        self.assert_rejected(lambda x: x["metrics"].pop("rss"))

    def test_rebound_binary_hash_rejected(self):
        self.assert_rejected(lambda x: x["binaries"]["rust"].__setitem__("sha256", "0" * 64))

    def test_rebound_binary_source_rejected(self):
        self.assert_rejected(lambda x: x["binaries"]["rust"].__setitem__("source", "0" * 40))

    def test_reviewed_capture_accepted(self):
        self.assertEqual(validator.main(RETAINED), 0)

    def test_each_operation_regression_fails_before_capture_digest_check(self):
        for category in ("mcp", "http"):
            for operation in self.result["metrics"][category]["operations"]:
                with self.subTest(category=category, operation=operation):

                    def mutate(result):
                        metric = result["metrics"][category]
                        go = metric["operations"][operation]["go"]
                        metric["operations"][operation]["rust"] = value001.summary(
                            [sample * 1.101 for sample in go["raw"]],
                            go["unit"],
                            go["warmup_samples"],
                            go["pair_order"],
                        )
                        ratios = value001.latency_regressions(result["metrics"])
                        result["thresholds"]["p95_regressions"] = ratios
                        self.assertLessEqual(ratios[category], 0.10)
                        self.assertEqual(
                            metric["rust"], self.result["metrics"][category]["rust"]
                        )

                    self.assert_rejected(
                        mutate,
                        f"required latency operation {category}.{operation} exceeds 10% regression",
                    )

    def test_altered_operation_summary_identity_is_rejected(self):
        self.assert_rejected(
            lambda result: result["metrics"]["http"]["operations"]["file-read"][
                "rust"
            ].update(p95=0.1),
            "http.operations.file-read.rust.p95 does not match raw samples",
        )


if __name__ == "__main__":
    unittest.main()
