#!/usr/bin/env python3
"""Negative and positive controls for the schema-6 acceptance anchor."""
import hashlib
import json
import shutil
import tempfile
import unittest
from pathlib import Path

import validate_value001_5c5e98c5 as validator


class Schema6AcceptanceTests(unittest.TestCase):
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

    def test_reviewed_capture_passes(self):
        result = validator.validate(self.path)
        self.assertIs(result["passed"], True)
        self.assertEqual(result["repository"]["head"], validator.EXPECTED_HEAD)
        self.assertIs(result["thresholds"]["latency_pass"], True)

    def test_derivation_rule_round_trips_and_rejects_unapproved_changes(self):
        derived = json.loads(self.path.read_bytes())
        replacements = {
            validator.REPO_PLACEHOLDER: "/Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/Repos/symaira-desktop/.worktrees/value001-main-5c5e98c5-20260918",
            validator.TEMP_PLACEHOLDER: "/Volumes/1TB_NVMe_SN850X/Dev/Symaira_Dev/BuildTargets/desktop-value001-main-5c5e98c5-T8WZ4R",
        }
        raw = json.loads(self.path.read_bytes())

        def restore(node, path="$"):
            if isinstance(node, dict):
                for key in list(node):
                    node[key] = restore(node[key], f"{path}.{key}")
                return node
            if isinstance(node, list):
                return [restore(item, f"{path}[{index}]") for index, item in enumerate(node)]
            if isinstance(node, str) and path in validator.REDACTED_PATHS:
                for placeholder, private in replacements.items():
                    node = node.replace(placeholder, private)
            return node

        raw = restore(raw)
        assert isinstance(raw, dict)
        validator.compare_derivation(raw, derived)

        def perturb(node, path="$"):
            if isinstance(node, dict):
                for key in node:
                    node[key] = perturb(node[key], f"{path}.{key}")
            elif isinstance(node, list):
                node = [perturb(item, f"{path}[{index}]") for index, item in enumerate(node)]
            elif path == "$.host.machine":
                node = "arm64-mutated"
            return node

        with self.assertRaisesRegex(ValueError, "unapproved derivation change"):
            validator.compare_derivation(perturb(raw), derived)

    def test_operation_regression_is_rejected(self):
        self.mutate(
            lambda r: r["metrics"]["http"]["operations"]["file-read"]["rust"].update(p95=0.1)
        )
        with self.assertRaisesRegex(
            validator.value001.HarnessError,
            "http.operations.file-read.rust.p95 does not match raw samples",
        ):
            validator.validate(self.path)

    def test_interval_above_the_ceiling_is_rejected(self):
        def regress(result):
            metrics = result["metrics"]
            thresholds = result["thresholds"]
            metric = metrics["http"]["operations"]["file-read"]
            go = metric["go"]
            metric["rust"] = validator.value001.summary(
                [sample * 1.2 for sample in go["raw"]],
                go["unit"],
                go["warmup_samples"],
                go["pair_order"],
                validator.value001.summation_of(result),
            )
            thresholds["latency_regressions"] = validator.value001.latency_regressions(
                metrics, validator.value001.latency_estimator_of(result)
            )
            thresholds["latency_order_regressions"] = validator.value001.latency_order_regressions(metrics)
            thresholds["latency_order_regression_intervals"] = (
                validator.value001.latency_order_regression_intervals(metrics)
            )
            thresholds["latency_pass"] = validator.value001.order_stratified_latency_pass(
                thresholds["latency_order_regression_intervals"]
            )
            result["passed"] = bool(
                thresholds["latency_pass"] and thresholds["improvement_pass"] and thresholds["contracts_pass"]
            )

        self.mutate(regress)
        with self.assertRaisesRegex(ValueError, "does not confirm the latency ceiling"):
            validator.validate(self.path)

    def test_private_path_in_the_published_capture_is_rejected(self):
        self.mutate(lambda r: r["host"].update(machine="/Volumes/private/arm64"))
        with self.assertRaisesRegex(ValueError, "private path in the published capture"):
            validator.validate(self.path)

    def test_metadata_inventory_must_match_the_review(self):
        metadata_path = self.path.with_suffix(".metadata.json")
        metadata = json.loads(metadata_path.read_bytes())
        metadata["redacted_json_paths"] = metadata["redacted_json_paths"][:-1]
        metadata_path.write_text(json.dumps(metadata))
        with self.assertRaisesRegex(ValueError, "redaction inventory differs from review"):
            validator.validate(self.path)


if __name__ == "__main__":
    unittest.main(verbosity=2)