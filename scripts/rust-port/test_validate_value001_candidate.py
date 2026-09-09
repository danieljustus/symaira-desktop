from __future__ import annotations

import copy
import hashlib
import importlib.util
import json
import subprocess
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("candidate_validator", Path(__file__).with_name("validate_value001_candidate.py"))
assert SPEC and SPEC.loader
validator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(validator)

ARTIFACT = Path("/tmp/symdesk-value001-5088972a.json")
ROOT = Path("/Users/daniel/Dev/Symaira Dev/symaira-desktop/.worktrees/value-operation-gates")
CANDIDATE = "5088972aa7efadfdc7118549354e26d001c1ffad"


class CandidateValidatorTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if not ARTIFACT.is_file():
            raise unittest.SkipTest("real VALUE-001 capture is unavailable")
        cls.original = json.loads(ARTIFACT.read_text())
        cls.trusted = hashlib.sha256(ARTIFACT.read_bytes()).hexdigest()

    def check(self, result=None, *, trusted=None, expect=None):
        result = copy.deepcopy(self.original if result is None else result)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "capture.json"
            path.write_text(json.dumps(result, allow_nan=True))
            digest = trusted or hashlib.sha256(path.read_bytes()).hexdigest()
            if expect is None:
                validator.validate(path, CANDIDATE, ROOT, digest)
            else:
                with self.assertRaises(validator.ValidationError) as raised:
                    validator.validate(path, CANDIDATE, ROOT, digest)
                self.assertIn(expect, str(raised.exception))

    def test_real_capture_revalidates_for_exact_current_candidate(self):
        validator.validate(ARTIFACT, CANDIDATE, ROOT, self.trusted)

    def test_trusted_digest_is_required_and_bound(self):
        with self.assertRaises(validator.ValidationError):
            validator.validate(ARTIFACT, CANDIDATE, ROOT, "0" * 64)

    def test_nan_is_rejected(self):
        mutated = copy.deepcopy(self.original)
        mutated["metrics"]["http"]["operations"]["status"]["rust"]["raw"][0] = float("nan")
        self.check(mutated, expect="invalid raw values")

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

    def test_operation_regression_is_recomputed_and_rejected(self):
        mutated = copy.deepcopy(self.original)
        summary = mutated["metrics"]["http"]["operations"]["status"]["rust"]
        summary["raw"] = [value * 2 for value in summary["raw"]]
        values = sorted(summary["raw"])
        n = len(values)
        summary.update({
            "min": min(values), "mean": sum(values) / n,
            "p50": values[(n + 1) // 2 - 1], "p95": values[(n * 95 + 99) // 100 - 1],
            "p99": values[(n * 99 + 99) // 100 - 1], "max": max(values), "max_observed": max(values),
        })
        self.check(mutated, expect="threshold ratio for http.status")

    def test_candidate_is_not_silently_defaulted(self):
        command = ["python3", str(Path(__file__).with_name("validate_value001_candidate.py")), str(ARTIFACT), "--root", str(ROOT), "--trusted-sha256", self.trusted]
        completed = subprocess.run(command, capture_output=True, text=True)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("--candidate", completed.stderr)


if __name__ == "__main__":
    unittest.main(verbosity=2)
