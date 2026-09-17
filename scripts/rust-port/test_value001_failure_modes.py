"""Fail-closed controls for invalid timing samples and measurement timeouts."""

import os
import socket
import subprocess
import tempfile
import unittest
from pathlib import Path
from typing import Any, cast
from unittest.mock import patch

import value001


class StrictSampleValidationTests(unittest.TestCase):
    def test_summary_rejects_zero_negative_nonfinite_boolean_and_sentinel_values(self):
        orders = ["go-rust", "rust-go"] * 50
        for bad in (0.0, -1.0, float("nan"), float("inf"), float("-inf"), True, "timeout"):
            with self.subTest(bad=repr(bad)):
                values: list[Any] = [1.0] * 100
                values[0] = bad
                with self.assertRaisesRegex(value001.HarnessError, "non-positive or non-finite"):
                    value001.summary(cast(list[float], values), "milliseconds", 1, orders)

    def test_ratio_rejects_zero_boolean_and_sentinel_values(self):
        for candidate, reference in ((0.0, 1.0), (1.0, 0.0), (True, 1.0), (1.0, True), ("timeout", 1.0)):
            with self.subTest(candidate=candidate, reference=reference):
                with self.assertRaisesRegex(value001.HarnessError, "invalid latency values"):
                    value001.ratio(cast(float, candidate), cast(float, reference))


class MeasurementTimeoutTests(unittest.TestCase):
    def test_index_preparation_timeout_becomes_a_controlled_harness_error(self):
        with tempfile.TemporaryDirectory() as temporary, patch.object(
            value001.subprocess,
            "run",
            side_effect=subprocess.TimeoutExpired(["symdesk", "ls"], 180.0),
        ):
            root = Path(temporary)
            with self.assertRaisesRegex(
                value001.HarnessError,
                "synthetic index preparation timed out after 180.0s",
            ):
                value001.prepare_index(
                    Path("/synthetic"),
                    root / "home",
                    root / "vault",
                    root / "sidecar.db",
                    {},
                )

    def test_process_timeout_becomes_a_controlled_harness_error(self):
        with patch.object(
            value001.subprocess,
            "run",
            side_effect=subprocess.TimeoutExpired(["symdesk", "version"], 30.0),
        ):
            with self.assertRaisesRegex(value001.HarnessError, "go version timed out after 30.0s"):
                value001.measure_process_pair(
                    Path("/go"),
                    Path("/rust"),
                    {},
                    {},
                    ["version"],
                    ["version"],
                    warmups=1,
                    samples=100,
                    validator=lambda _stdout, _stderr: None,
                )

    def test_http_timeout_becomes_a_controlled_harness_error(self):
        class TimedOutServer:
            vault = Path("/synthetic-vault")

            @staticmethod
            def request_raw(*_args, **_kwargs):
                raise socket.timeout("simulated timeout")

        with self.assertRaisesRegex(
            value001.HarnessError,
            "go HTTP healthz request failed or timed out",
        ):
            value001.measure_http(
                cast(value001.RunningServer, TimedOutServer()),
                cast(value001.RunningServer, TimedOutServer()),
                {},
                {},
                warmups=1,
                samples=100,
                rss_interval_ms=0,
            )


class OutputBoundaryTests(unittest.TestCase):
    def test_incomplete_marker_cannot_be_reused(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "result.json"
            marker = value001.mark_output_incomplete(output)
            self.assertTrue(marker.is_file())
            with self.assertRaisesRegex(value001.HarnessError, "incomplete-run marker"):
                value001.mark_output_incomplete(output)

    def test_incomplete_marker_symlink_is_refused_without_following_target(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "result.json"
            target = Path(temporary) / "target.txt"
            target.write_text("must stay unchanged\n", encoding="utf-8")
            marker = value001.incomplete_marker_path(output)
            try:
                os.symlink(target, marker)
            except OSError as exc:
                self.skipTest(f"symlink creation unavailable: {exc}")
            with self.assertRaisesRegex(value001.HarnessError, "incomplete-run marker"):
                value001.mark_output_incomplete(output)
            self.assertEqual(target.read_text(encoding="utf-8"), "must stay unchanged\n")

    def test_invalid_result_never_replaces_a_previous_artifact(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "result.json"
            output.write_text("old artifact\n", encoding="utf-8")
            with self.assertRaises(value001.HarnessError):
                value001.write_complete_result(output, {})
            self.assertEqual(output.read_text(encoding="utf-8"), "old artifact\n")
            self.assertEqual(list(Path(temporary).glob(".result.json.*.tmp")), [])


if __name__ == "__main__":
    unittest.main(verbosity=2)
