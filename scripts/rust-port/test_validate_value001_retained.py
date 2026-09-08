import copy
import json
import tempfile
import unittest
from pathlib import Path

import validate_value001_retained as validator


ROOT = Path(__file__).resolve().parents[2]
RETAINED = ROOT / "docs/rust-port/results/value001-retained.json"


class RetainedValidatorMutationTests(unittest.TestCase):
    def setUp(self):
        self.result = json.loads(RETAINED.read_text(encoding="utf-8"))

    def assert_rejected(self, mutate):
        candidate = copy.deepcopy(self.result)
        mutate(candidate)
        with tempfile.TemporaryDirectory() as directory:
            artifact = Path(directory) / "value001-retained.json"
            artifact.write_text(json.dumps(candidate), encoding="utf-8")
            metadata = json.loads((RETAINED.with_name("value001-retained.metadata.json")).read_text())
            metadata["redacted_sha256"] = __import__("hashlib").sha256(artifact.read_bytes()).hexdigest()
            (Path(directory) / "value001-retained.metadata.json").write_text(json.dumps(metadata))
            with self.assertRaises((ValueError, KeyError)):
                validator.main(artifact)

    def test_tampered_raw_sample_rejected(self):
        self.assert_rejected(lambda x: x["metrics"]["search"]["go"]["raw"].__setitem__(0, 999999))

    def test_tampered_summary_ratio_rejected(self):
        self.assert_rejected(lambda x: x["thresholds"]["p95_regressions"].__setitem__("http", 0))

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


if __name__ == "__main__":
    unittest.main()
