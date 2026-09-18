#!/usr/bin/env python3
"""Evidence tests for the independently reviewed VALUE-001 aeab7664 capture."""
from __future__ import annotations

import copy
import hashlib
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))

import validate_value001_aeab7664 as validator
import value001
from validate_value001_resume import compare_derivation


class Aeab7664ValidatorTests(unittest.TestCase):
    temp_dir: tempfile.TemporaryDirectory | None = None
    candidate_root: Path

    @classmethod
    def setUpClass(cls):
        cls.temp_dir = tempfile.TemporaryDirectory()
        cls.candidate_root = Path(cls.temp_dir.name) / f"aeab-checkout-{Path(cls.temp_dir.name).name}"
        try:
            subprocess.run(
                [
                    "git",
                    "worktree",
                    "add",
                    "--detach",
                    "--quiet",
                    str(cls.candidate_root),
                    validator.EXPECTED_HEAD,
                ],
                cwd=ROOT,
                check=True,
                capture_output=True,
                text=True,
            )
        except BaseException:
            cls.temp_dir.cleanup()
            raise

    @classmethod
    def tearDownClass(cls):
        try:
            subprocess.run(
                ["git", "worktree", "remove", "--force", str(cls.candidate_root)],
                cwd=ROOT,
                check=False,
                capture_output=True,
            )
        finally:
            if cls.temp_dir is not None:
                cls.temp_dir.cleanup()

    def check_candidate_mutation(self, mutate_fn, *, expected_error: str | None = None):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name

            result = json.loads(validator.ARTIFACT.read_text(encoding="utf-8"))
            mutate_fn(result)

            temp_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
            mutant_digest = hashlib.sha256(temp_path.read_bytes()).hexdigest()

            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.candidate_validator.validate(
                    temp_path,
                    validator.EXPECTED_HEAD,
                    self.candidate_root,
                    mutant_digest,
                )
            if expected_error is not None:
                self.assertIn(expected_error, str(raised.exception))

    def validate_with_metadata(self, metadata):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name
            temp_meta = temp_path.with_suffix(".metadata.json")
            shutil.copyfile(validator.ARTIFACT, temp_path)
            temp_meta.write_text(json.dumps(metadata), encoding="utf-8")
            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(temp_path, self.candidate_root)
            return str(raised.exception)

    def test_positive_retained_capture_passes(self):
        validator.validate(validator.ARTIFACT, self.candidate_root)
        rc = validator.main([str(validator.ARTIFACT), "--root", str(self.candidate_root)])
        self.assertEqual(rc, 0)

    def test_synthetic_raw_derivation_is_rejected_by_fixed_hash_anchor(self):
        derived_text = validator.ARTIFACT.read_text(encoding="utf-8")
        synthetic_original_text = (
            derived_text
            .replace("<redacted-repository>", "/synthetic/repository")
            .replace("<redacted-temporary-directory>", "/synthetic/temporary")
        )
        with tempfile.TemporaryDirectory() as temp_dir:
            raw_path = Path(temp_dir) / "original.json"
            raw_path.write_text(synthetic_original_text, encoding="utf-8")
            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(
                    validator.ARTIFACT,
                    self.candidate_root,
                    raw_path=raw_path,
                )
            self.assertIn("original capture differs from independent review", str(raised.exception))

    def test_tampered_artifact_bytes_rejected_by_digest_anchor(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name
            temp_meta = temp_path.with_suffix(".metadata.json")
            shutil.copyfile(validator.ARTIFACT, temp_path)
            shutil.copyfile(validator.ARTIFACT.with_suffix(".metadata.json"), temp_meta)

            with temp_path.open("r+b") as f:
                f.seek(10)
                f.write(b"X")

            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(temp_path, self.candidate_root)
            self.assertIn("artifact digest differs from trusted SHA256", str(raised.exception))

    def test_wrong_trusted_sha256_arg_rejected(self):
        with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
            validator.validate(
                validator.ARTIFACT,
                self.candidate_root,
                trusted_sha256="0" * 64,
            )
        self.assertIn("trusted SHA256 must match independently reviewed constant", str(raised.exception))

    def test_missing_metadata_sidecar_rejected(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name
            shutil.copyfile(validator.ARTIFACT, temp_path)
            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(temp_path, self.candidate_root)
            self.assertIn("retained provenance sidecar is missing", str(raised.exception))

    def test_wrong_metadata_original_sha256_rejected(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name
            temp_meta = temp_path.with_suffix(".metadata.json")
            shutil.copyfile(validator.ARTIFACT, temp_path)
            metadata = json.loads(validator.ARTIFACT.with_suffix(".metadata.json").read_text(encoding="utf-8"))
            metadata["original_sha256"] = "0" * 64
            temp_meta.write_text(json.dumps(metadata), encoding="utf-8")
            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(temp_path, self.candidate_root)
            self.assertIn("original raw artifact digest is not the verified capture", str(raised.exception))

    def test_wrong_metadata_source_head_rejected(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name
            temp_meta = temp_path.with_suffix(".metadata.json")
            shutil.copyfile(validator.ARTIFACT, temp_path)
            metadata = json.loads(validator.ARTIFACT.with_suffix(".metadata.json").read_text(encoding="utf-8"))
            metadata["source_expected_head"] = "0" * 40
            temp_meta.write_text(json.dumps(metadata), encoding="utf-8")
            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(temp_path, self.candidate_root)
            self.assertIn("metadata source_expected_head mismatch", str(raised.exception))

    def test_wrong_metadata_go_oracle_rejected(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name
            temp_meta = temp_path.with_suffix(".metadata.json")
            shutil.copyfile(validator.ARTIFACT, temp_path)
            metadata = json.loads(validator.ARTIFACT.with_suffix(".metadata.json").read_text(encoding="utf-8"))
            metadata["go_oracle"] = "0" * 40
            temp_meta.write_text(json.dumps(metadata), encoding="utf-8")
            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(temp_path, self.candidate_root)
            self.assertIn("metadata go_oracle mismatch", str(raised.exception))

    def test_wrong_metadata_trusted_ci_run_rejected(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name
            temp_meta = temp_path.with_suffix(".metadata.json")
            shutil.copyfile(validator.ARTIFACT, temp_path)
            metadata = json.loads(validator.ARTIFACT.with_suffix(".metadata.json").read_text(encoding="utf-8"))
            metadata["trusted_ci_run"] = "00000000000"
            temp_meta.write_text(json.dumps(metadata), encoding="utf-8")
            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(temp_path, self.candidate_root)
            self.assertIn("metadata trusted_ci_run mismatch", str(raised.exception))

    def test_wrong_metadata_redacted_paths_rejected(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name
            temp_meta = temp_path.with_suffix(".metadata.json")
            shutil.copyfile(validator.ARTIFACT, temp_path)
            metadata = json.loads(validator.ARTIFACT.with_suffix(".metadata.json").read_text(encoding="utf-8"))
            metadata["redacted_json_paths"] = ["$.repository.root"]
            temp_meta.write_text(json.dumps(metadata), encoding="utf-8")
            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(temp_path, self.candidate_root)
            self.assertIn("metadata redacted_json_paths inventory differs from review", str(raised.exception))

    def test_metadata_nonobject_is_rejected_with_controlled_error(self):
        self.assertIn(
            "provenance metadata must be an object",
            self.validate_with_metadata([]),
        )

    def test_metadata_schema_mismatch_is_rejected_with_controlled_error(self):
        metadata = json.loads(validator.ARTIFACT.with_suffix(".metadata.json").read_text(encoding="utf-8"))
        del metadata["derivation"]
        self.assertIn(
            "provenance metadata schema mismatch",
            self.validate_with_metadata(metadata),
        )

    def test_metadata_path_inventory_types_are_rejected_with_controlled_error(self):
        metadata = json.loads(validator.ARTIFACT.with_suffix(".metadata.json").read_text(encoding="utf-8"))
        metadata["redacted_json_paths"] = "$.repository.root"
        self.assertIn(
            "provenance metadata redacted_json_paths must be a list",
            self.validate_with_metadata(metadata),
        )

        metadata["redacted_json_paths"] = ["$.repository.root", 7]
        self.assertIn(
            "provenance metadata redacted_json_paths must contain only strings",
            self.validate_with_metadata(metadata),
        )

        metadata = json.loads(validator.ARTIFACT.with_suffix(".metadata.json").read_text(encoding="utf-8"))
        metadata["source_artifact"] = 7
        self.assertIn(
            "provenance metadata source_artifact must be a string",
            self.validate_with_metadata(metadata),
        )

    def test_metadata_duplicate_path_inventory_is_rejected_with_controlled_error(self):
        metadata = json.loads(validator.ARTIFACT.with_suffix(".metadata.json").read_text(encoding="utf-8"))
        metadata["redacted_json_paths"] = list(metadata["redacted_json_paths"]) + ["$.repository.root"]
        self.assertIn(
            "provenance metadata redacted_json_paths contains duplicates",
            self.validate_with_metadata(metadata),
        )

    def test_retained_artifact_has_no_private_path(self):
        data = validator.ARTIFACT.read_bytes()
        self.assertNotIn(b"/Users/", data)
        self.assertNotIn(b"/var/folders/", data)
        self.assertNotIn(b":\\\\Users\\\\", data)

    def test_modified_artifact_is_rejected_by_fixed_digest_anchor(self):
        with tempfile.TemporaryDirectory() as temp_dir:
            temp_path = Path(temp_dir) / validator.ARTIFACT.name
            temp_meta = temp_path.with_suffix(".metadata.json")
            result = json.loads(validator.ARTIFACT.read_text(encoding="utf-8"))
            result["contracts"][0]["command"] = "/Users/daniel/repos/symaira-desktop"
            temp_path.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
            shutil.copyfile(validator.ARTIFACT.with_suffix(".metadata.json"), temp_meta)
            with self.assertRaises(validator.candidate_validator.ValidationError) as raised:
                validator.validate(temp_path, self.candidate_root)
            self.assertIn("artifact digest differs from trusted SHA256", str(raised.exception))

    def test_altered_source_head_rejected(self):
        def mutate(result):
            result["repository"]["head"] = "0" * 40
        self.check_candidate_mutation(mutate, expected_error="recorded source head is not --candidate")

    def test_dirty_repository_status_rejected(self):
        def mutate(result):
            result["repository"]["status"] = " M src/main.rs"
        self.check_candidate_mutation(mutate, expected_error="historical capture was not clean")

    def test_false_pass_flag_rejected(self):
        def mutate(result):
            result["passed"] = False
        self.check_candidate_mutation(mutate, expected_error="recorded approval is not passing")

    def test_missing_required_operation_rejected(self):
        def mutate(result):
            del result["metrics"]["http"]["operations"]["file-read"]
        self.check_candidate_mutation(mutate, expected_error="invalid VALUE-001 result: metrics.http.operations are incomplete")

    def test_missing_metric_category_rejected(self):
        def mutate(result):
            del result["metrics"]["rss"]
        self.check_candidate_mutation(mutate, expected_error="invalid VALUE-001 result: 'rss'")

    def test_contract_failure_rejected(self):
        def mutate(result):
            result["contracts"][0]["exit_code"] = 1
        self.check_candidate_mutation(mutate, expected_error="invalid VALUE-001 result: contract record is not strict or passing")

    def test_wrong_rust_binary_source_rejected(self):
        def mutate(result):
            result["binaries"]["rust"]["source"] = "0" * 40
        self.check_candidate_mutation(mutate, expected_error="Rust binary source is not --candidate")

    def test_wrong_go_binary_source_rejected(self):
        def mutate(result):
            result["binaries"]["go"]["source"] = "0" * 40
        self.check_candidate_mutation(mutate, expected_error="Go binary is not the required oracle")

    def test_altered_summary_identity_rejected(self):
        def mutate(result):
            result["metrics"]["http"]["operations"]["file-read"]["rust"]["p95"] = 0.001
        self.check_candidate_mutation(
            mutate,
            expected_error="invalid VALUE-001 result: metrics.http.operations.file-read.rust.p95 does not match raw samples",
        )

    def test_nan_sample_rejected(self):
        def mutate(result):
            result["metrics"]["http"]["operations"]["status"]["rust"]["raw"][0] = float("nan")
        self.check_candidate_mutation(
            mutate,
            expected_error="invalid VALUE-001 result: metrics.http.operations.status.rust contains invalid raw values",
        )

    def test_each_operation_regression_fails_with_coherent_summary_and_ratio(self):
        original = json.loads(validator.ARTIFACT.read_bytes())
        summation = value001.summation_of(original)
        estimator = value001.latency_estimator_of(original)

        for category, operations in validator.candidate_validator.REQUIRED_OPERATIONS.items():
            for operation in sorted(operations):
                with self.subTest(category=category, operation=operation):
                    def mutate(result):
                        metric = result["metrics"][category]
                        go = metric["operations"][operation]["go"]
                        metric["operations"][operation]["rust"] = value001.summary(
                            [sample * 1.101 for sample in go["raw"]],
                            go["unit"],
                            go["warmup_samples"],
                            go["pair_order"],
                            summation,
                        )
                        ratios = value001.latency_regressions(
                            result["metrics"], estimator
                        )
                        result["thresholds"]["latency_regressions"] = ratios
                        self.assertLessEqual(ratios[category], 0.10)
                        self.assertEqual(
                            metric["rust"], original["metrics"][category]["rust"]
                        )

                    self.check_candidate_mutation(
                        mutate,
                        expected_error=f"{category}.{operation} exceeds exact 10% regression limit",
                    )

    def test_each_latency_gate_rejects_synthetic_unpaired_p95_tail_regression(self):
        original = json.loads(validator.ARTIFACT.read_bytes())
        summation = value001.summation_of(original)
        pairs = value001.latency_pairs(original["metrics"])
        self.assertEqual(len(pairs), 16)

        for name in pairs:
            with self.subTest(gate=name):
                def mutate(result, gate=name):
                    pair = value001.latency_pairs(result["metrics"])[gate]
                    go = pair["go"]
                    rust = pair["rust"]
                    rust_raw = list(rust["raw"])
                    tail_value = value001.percentile(go["raw"], 0.95) * 1.25
                    tail_count = max(1, (6 * len(rust_raw) + 99) // 100)
                    paired_ratios = sorted(
                        ((rust_raw[index] / go["raw"][index], index)
                         for index in range(len(rust_raw))),
                        reverse=True,
                    )
                    for _, index in paired_ratios[:tail_count]:
                        rust_raw[index] = tail_value
                    pair["rust"] = value001.summary(
                        rust_raw,
                        rust["unit"],
                        rust["warmup_samples"],
                        rust["pair_order"],
                        summation,
                    )
                    unpaired = value001.latency_regressions(
                        result["metrics"], "unpaired_p95"
                    )
                    paired = value001.latency_regressions(
                        result["metrics"], "paired_median_ratio"
                    )
                    result["thresholds"]["latency_regressions"] = paired
                    self.assertGreater(unpaired[gate], 0.10)
                    self.assertLessEqual(paired[gate], 0.10)

                self.check_candidate_mutation(
                    mutate,
                    expected_error=f"{name} exceeds exact 10% unpaired p95 regression limit",
                )

    def test_cli_missing_root_fails(self):
        command = [
            sys.executable,
            str(Path(validator.__file__).resolve()),
            str(validator.ARTIFACT),
        ]
        completed = subprocess.run(command, capture_output=True, text=True)
        self.assertNotEqual(completed.returncode, 0)
        self.assertIn("--root", completed.stderr)

    def test_synthetic_derivation_control_accepts_only_prefix_substitutions(self):
        derived = json.loads(validator.ARTIFACT.read_bytes())
        synthetic_original_text = (
            validator.ARTIFACT.read_text(encoding="utf-8")
            .replace("<redacted-repository>", "/synthetic/repository")
            .replace("<redacted-temporary-directory>", "/synthetic/temporary")
        )
        synthetic_original = json.loads(synthetic_original_text)
        compare_derivation(synthetic_original, derived)

    def test_synthetic_derivation_control_rejects_altered_samples(self):
        derived = copy.deepcopy(json.loads(validator.ARTIFACT.read_bytes()))
        synthetic_original_text = (
            validator.ARTIFACT.read_text(encoding="utf-8")
            .replace("<redacted-repository>", "/synthetic/repository")
            .replace("<redacted-temporary-directory>", "/synthetic/temporary")
        )
        synthetic_original = json.loads(synthetic_original_text)
        derived["metrics"]["http"]["rust"]["raw"][0] += 1.0
        with self.assertRaises(ValueError):
            compare_derivation(synthetic_original, derived)

    def test_synthetic_derivation_control_rejects_command_argument_changes(self):
        derived = copy.deepcopy(json.loads(validator.ARTIFACT.read_bytes()))
        synthetic_original_text = (
            validator.ARTIFACT.read_text(encoding="utf-8")
            .replace("<redacted-repository>", "/synthetic/repository")
            .replace("<redacted-temporary-directory>", "/synthetic/temporary")
        )
        synthetic_original = json.loads(synthetic_original_text)
        derived["contracts"][1]["command"] += " --different-option"
        with self.assertRaises(ValueError):
            compare_derivation(synthetic_original, derived)


if __name__ == "__main__":
    unittest.main(verbosity=2)
