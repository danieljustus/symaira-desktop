"""Producer negative controls only; these tests never measure a benchmark."""

import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import value001


ROOT = Path(__file__).resolve().parents[2]
RESULTS = ROOT / "docs/rust-port/results"
REQUIRED_GATES = (
    "startup",
    "search",
    "mcp",
    "http",
    "mcp.initialize",
    "mcp.tools-list",
    "mcp.desk_status",
    "mcp.desk_ls",
    "mcp.desk_search",
    "http.healthz",
    "http.status",
    "http.snapshot",
    "http.file-read",
    "http.file-range",
    "http.file-missing",
    "http.file-traversal",
)


def metric_pair(metrics, name):
    category, _, operation = name.partition(".")
    metric = metrics[category]
    return metric["operations"][operation] if operation else metric


class ProducerGateTests(unittest.TestCase):
    def setUp(self):
        self.original = json.loads(
            (RESULTS / "value001-operations-5088972a.json").read_text()
        )
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.go = Path(temporary.name) / "go-size-only"
        self.rust = Path(temporary.name) / "rust-size-only"
        # Inert size inputs, never executables. All build/Git queries are mocked.
        self.go.write_bytes(b"g" * 100)
        self.rust.write_bytes(b"r" * 50)

    def build(self, metrics):
        source = {
            "commit": self.original["binaries"]["go"]["source"],
            "go_version": "unit-test metadata",
            "binary_sha256": value001.sha256_file(self.go),
            "build_command": "unit-test size input; no build",
            "build_elapsed_ms": 10**12,
        }
        with (
            patch.object(
                value001, "git_output", return_value=self.original["repository"]["head"]
            ),
            patch.object(value001, "git_bytes", return_value=b""),
            patch.object(value001, "tool_version", return_value="unit-test metadata"),
        ):
            return value001.build_result(
                ROOT,
                self.go,
                self.rust,
                source,
                "unit-test size input; no build",
                self.original["contracts"],
                metrics,
                {
                    "documents": 10000,
                    "bytes": self.original["vault"]["generated_bytes"],
                    "search_match_count": 100,
                },
                100,
                20,
                self.original["index_preparation"],
            )

    def test_each_required_gate_enforces_unchanged_ceiling(self):
        for name in REQUIRED_GATES:
            for factor, passes in ((1.099, True), (1.101, False)):
                with self.subTest(gate=name, factor=factor):
                    metrics = copy.deepcopy(self.original["metrics"])
                    pair = metric_pair(metrics, name)
                    go = pair["go"]
                    pair["rust"] = value001.summary(
                        [sample * factor for sample in go["raw"]],
                        go["unit"],
                        go["warmup_samples"],
                        go["pair_order"],
                    )
                    result = self.build(metrics)
                    thresholds = result["thresholds"]
                    self.assertEqual(
                        set(thresholds["p95_regressions"]), set(REQUIRED_GATES)
                    )
                    self.assertEqual(thresholds["maximum_p95_regression"], 0.10)
                    self.assertAlmostEqual(
                        thresholds["p95_regressions"][name], factor - 1
                    )
                    self.assertTrue(thresholds["improvement_pass"])
                    self.assertTrue(thresholds["contracts_pass"])
                    self.assertIs(thresholds["latency_pass"], passes)
                    self.assertIs(result["passed"], passes)
                    if "." in name:
                        category = name.split(".")[0]
                        self.assertLessEqual(
                            thresholds["p95_regressions"][category], 0.10
                        )
                        self.assertEqual(
                            metrics[category]["rust"],
                            self.original["metrics"][category]["rust"],
                        )

    def test_altered_summary_identity_is_rejected_without_normalization(self):
        for name in REQUIRED_GATES:
            for side in ("go", "rust"):
                for field in ("min", "mean", "p50", "p95", "p99"):
                    with self.subTest(gate=name, side=side, field=field):
                        metrics = copy.deepcopy(self.original["metrics"])
                        metric_pair(metrics, name)[side][field] *= 0.5
                        before = copy.deepcopy(metrics)
                        with self.assertRaisesRegex(
                            value001.HarnessError,
                            f"{side}\\.{field} does not match raw samples",
                        ):
                            self.build(metrics)
                        self.assertEqual(metrics, before)

    def test_each_missing_required_operation_is_rejected(self):
        for name in REQUIRED_GATES:
            if "." not in name:
                continue
            with self.subTest(gate=name):
                metrics = copy.deepcopy(self.original["metrics"])
                category, operation = name.split(".")
                del metrics[category]["operations"][operation]
                with self.assertRaisesRegex(
                    value001.HarnessError, f"{category}\\.operations are incomplete"
                ):
                    self.build(metrics)

    def test_historical_resumption_raw_metrics_still_fail(self):
        capture = json.loads((RESULTS / "value001-resume-956bd3e.json").read_text())
        before = copy.deepcopy(capture)
        result = self.build(capture["metrics"])
        regressions = result["thresholds"]["p95_regressions"]
        self.assertLessEqual(regressions["http"], 0.10)
        self.assertEqual(
            {name for name, ratio in regressions.items() if ratio > 0.10},
            {"http.file-read", "http.file-missing"},
        )
        self.assertFalse(result["thresholds"]["latency_pass"])
        self.assertFalse(result["passed"])
        self.assertEqual(capture, before)


if __name__ == "__main__":
    unittest.main(verbosity=2)
