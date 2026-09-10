#!/usr/bin/env python3
"""Negative controls for the fixed resumption evidence anchor."""
import hashlib
import json
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import validate_value001_resume as validator


class ResumeEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name) / validator.ARTIFACT.name
        shutil.copyfile(validator.ARTIFACT, self.path)
        shutil.copyfile(validator.ARTIFACT.with_suffix(".metadata.json"), self.path.with_suffix(".metadata.json"))

    def mutate(self, change):
        result = json.loads(self.path.read_bytes())
        change(result)
        self.path.write_text(json.dumps(result, indent=2) + "\n")
        metadata_path = self.path.with_suffix(".metadata.json")
        metadata = json.loads(metadata_path.read_bytes())
        metadata["redacted_sha256"] = hashlib.sha256(self.path.read_bytes()).hexdigest()
        metadata_path.write_text(json.dumps(metadata))

    def test_genuine_capture_fails_operation_acceptance(self):
        result = json.loads(self.path.read_bytes())
        regressions = validator.value001.latency_regressions(result["metrics"])
        self.assertLessEqual(regressions["http"], 0.10)
        self.assertEqual(
            {name for name, ratio in regressions.items() if ratio > 0.10},
            {"http.file-read", "http.file-missing"},
        )
        with self.assertRaisesRegex(ValueError, "http.file-read, http.file-missing"):
            validator.validate(self.path)

    def test_altered_operation_summary_identity_is_rejected(self):
        self.mutate(
            lambda r: r["metrics"]["http"]["operations"]["file-read"]["rust"].update(
                p95=0.1
            )
        )
        with self.assertRaisesRegex(
            validator.value001.HarnessError,
            "http.operations.file-read.rust.p95 does not match raw samples",
        ):
            validator.validate(self.path)

    def test_each_operation_regression_fails_with_isolated_digest_anchor(self):
        # Only test copies receive a synthetic digest anchor; production pins
        # and retained evidence remain unchanged. Assert the latency reason.
        original = json.loads(validator.ARTIFACT.read_bytes())
        for category in ("mcp", "http"):
            for operation in original["metrics"][category]["operations"]:
                with self.subTest(category=category, operation=operation):
                    shutil.copyfile(validator.ARTIFACT, self.path)

                    def change(result):
                        go = result["metrics"][category]["operations"][operation]["go"]
                        result["metrics"][category]["operations"][operation]["rust"] = (
                            validator.value001.summary(
                                [sample * 1.101 for sample in go["raw"]],
                                go["unit"],
                                go["warmup_samples"],
                                go["pair_order"],
                            )
                        )
                        ratios = validator.value001.latency_regressions(
                            result["metrics"]
                        )
                        self.assertLessEqual(ratios[category], 0.10)
                        self.assertEqual(
                            result["metrics"][category]["rust"],
                            original["metrics"][category]["rust"],
                        )

                    self.mutate(change)
                    digest = hashlib.sha256(self.path.read_bytes()).hexdigest()
                    with patch.object(validator, "EXPECTED_SHA256", digest):
                        with self.assertRaisesRegex(
                            ValueError,
                            f"required latency operations exceed 10% regression: .*{category}\\.{operation}",
                        ):
                            validator.validate(self.path)

    def test_recursive_derivation_accepts_only_prefix_substitutions(self):
        # Synthetic private prefixes test the transformation, not measurements.
        derived = json.loads(self.path.read_bytes())
        original_text = self.path.read_text().replace("<redacted-repository>", "/synthetic/repository").replace("<redacted-temporary-directory>", "/synthetic/temporary")
        original = json.loads(original_text)
        validator.compare_derivation(original, derived)
        derived["metrics"]["http"]["rust"]["raw"][0] += 1
        with self.assertRaises(ValueError):
            validator.compare_derivation(original, derived)

    def test_recursive_derivation_rejects_command_argument_changes(self):
        derived = json.loads(self.path.read_bytes())
        original_text = self.path.read_text().replace("<redacted-repository>", "/synthetic/repository").replace("<redacted-temporary-directory>", "/synthetic/temporary")
        original = json.loads(original_text)
        derived["contracts"][1]["command"] += " --different-option"
        with self.assertRaises(ValueError):
            validator.compare_derivation(original, derived)

    def test_dirty_source_rejected(self):
        self.mutate(lambda r: r["repository"].update(status=" M source.rs"))
        with self.assertRaises(ValueError):
            validator.validate(self.path)

    def test_altered_raw_sample_rejected(self):
        self.mutate(lambda r: r["metrics"]["http"]["rust"]["raw"].pop())
        with self.assertRaises((ValueError, validator.value001.HarnessError)):
            validator.validate(self.path)

    def test_coherent_sidecar_rewrite_cannot_replace_capture(self):
        self.mutate(lambda r: r["binaries"]["rust"].update(sha256="0" * 64))
        with self.assertRaises(ValueError):
            validator.validate(self.path)

    def test_changed_ci_anchor_rejected(self):
        metadata_path = self.path.with_suffix(".metadata.json")
        metadata = json.loads(metadata_path.read_bytes())
        metadata["trusted_ci_run"] = "0"
        metadata_path.write_text(json.dumps(metadata))
        with self.assertRaises(ValueError):
            validator.validate(self.path)


if __name__ == "__main__":
    unittest.main()
