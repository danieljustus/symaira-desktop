#!/usr/bin/env python3
"""Runner integrity controls; synthetic executables are NOT parity evidence."""

import base64
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

SCRIPT = Path(__file__).with_name("retention_cli_regressions.py")
SPEC = importlib.util.spec_from_file_location("retention_review_runner", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
RUNNER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RUNNER)


@unittest.skipUnless(os.name == "posix", "requires native POSIX executable/process semantics")
class RunnerIntegrityTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="retention-runner-test-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.invocations = 0

    def executable(self, name, body):
        path = self.root / name
        path.write_text(f"#!{sys.executable}\n# synthetic {name}\n{body}\n", encoding="utf-8")
        path.chmod(0o700)
        return path

    def run_pair(self, left, right, declared=None, oracle_stdout=b"", oracle_stderr=b""):
        self.invocations += 1
        output = self.root / f"report-{self.invocations}.json"
        argv = [str(SCRIPT), "--go-binary", str(left), "--rust-binary", str(right),
                "--output", str(output), "--scratch-root", str(self.root),
                "--repo-root", str(Path(__file__).resolve().parents[1])]
        if declared is None:
            declared = [{"id": "integrity-control", "args": ["retention", "diff", "probe"]}]
        for case in declared:
            case.setdefault("oracle", {
                "exit": 0,
                "stdout_base64": base64.b64encode(oracle_stdout).decode("ascii"),
                "stderr_base64": base64.b64encode(oracle_stderr).decode("ascii"),
                "unchanged": True,
            })
        with mock.patch.object(sys, "argv", argv), \
                mock.patch.object(RUNNER, "cases", return_value=declared), \
                mock.patch.object(RUNNER, "TIMEOUT_SECONDS", 0.3), \
                contextlib.redirect_stdout(io.StringIO()):
            status = RUNNER.main()
        report = json.loads(output.read_bytes())
        self.assertEqual(report["success"], status == 0)
        self.assertEqual(report["executed_case_count"], len(declared))
        self.assertEqual(report["declared_case_count"], len(declared))
        for row in report["cases"]:
            for observation in row["observations"].values():
                for stream in ("stdout", "stderr"):
                    raw = base64.b64decode(observation[f"{stream}_base64"], validate=True)
                    self.assertEqual(hashlib.sha256(raw).hexdigest(), observation[f"{stream}_sha256"])
        return status, report

    def test_raw_stream_controls(self):
        controls = [
            (b"same\xff\r\n", b"same\xff\r\n", 0),
            (b"left\n", b"right\n", 1),
            (b"same\r\n", b"same\n", 1),
            (b"\xff\n", b"\xfe\n", 1),
            (b".symdesk-retention-1.tmp\n", b".symdesk-retention-2.tmp\n", 1),
        ]
        for descriptor, stream in ((1, "stdout"), (2, "stderr")):
            for left_bytes, right_bytes, expected in controls:
                with self.subTest(stream=stream, left=left_bytes, right=right_bytes):
                    left = self.executable("left", f"import os; os.write({descriptor}, {left_bytes!r})")
                    right = self.executable("right", f"import os; os.write({descriptor}, {right_bytes!r})")
                    status, report = self.run_pair(
                        left,
                        right,
                        oracle_stdout=left_bytes if descriptor == 1 else b"",
                        oracle_stderr=left_bytes if descriptor == 2 else b"",
                    )
                    self.assertEqual(status, expected)
                    observations = report["cases"][0]["observations"]
                    for role, expected_raw in (("go", left_bytes), ("rust", right_bytes)):
                        self.assertEqual(base64.b64decode(observations[role][f"{stream}_base64"]), expected_raw)

    def test_matching_timeouts_and_signals_are_failures(self):
        for body, timed_out in (
            ("import time; time.sleep(30)", True),
            ("import os, signal; os.kill(os.getpid(), signal.SIGTERM)", False),
        ):
            with self.subTest(timed_out=timed_out):
                left = self.executable("left", body)
                right = self.executable("right", body)
                started = time.monotonic()
                status, report = self.run_pair(left, right)
                self.assertLess(time.monotonic() - started, 8)
                row = report["cases"][0]
                self.assertEqual(status, 1)
                self.assertFalse(row["passed"])
                self.assertTrue(row["execution_errors"])
                for observation in row["observations"].values():
                    self.assertEqual(observation["timed_out"], timed_out)
                    self.assertLess(observation["exit"], 0)

    def test_sandbox_normalization_retains_original_bytes(self):
        body = "import os; os.write(1, os.environ['HOME'].encode() + b'\\r\\n')"
        status, report = self.run_pair(
            self.executable("left", body),
            self.executable("right", body),
            oracle_stdout=b"<SANDBOX>/home\r\n",
        )
        self.assertEqual(status, 0)
        self.assertEqual(report["schema_version"], 2)
        observations = report["cases"][0]["observations"]
        self.assertNotEqual(observations["go"]["stdout_base64"], observations["rust"]["stdout_base64"])
        for observation in observations.values():
            self.assertTrue(base64.b64decode(observation["stdout_base64"]).endswith(b"/home\r\n"))
            self.assertEqual(base64.b64decode(observation["stdout_compare_base64"]), b"<SANDBOX>/home\r\n")

    def test_invalid_case_inventory_and_identical_binaries_are_rejected(self):
        left = self.executable("left", "pass")
        right = self.executable("right", "pass")
        for declared in ([], [{"id": "duplicate", "args": []}] * 2):
            with self.subTest(declared=declared), self.assertRaisesRegex(ValueError, "nonempty.*unique"):
                self.run_pair(left, right, declared)
        with self.assertRaisesRegex(SystemExit, "same executable path"):
            self.run_pair(left, left)
        right.write_bytes(left.read_bytes())
        with self.assertRaisesRegex(SystemExit, "byte-identical"):
            self.run_pair(left, right)

    def test_atomic_name_normalization_is_confined_to_creation_errors(self):
        for ending, expected in ((".tmp: permission denied", 0), (".tmpx: permission denied", 1)):
            with self.subTest(ending=ending):
                programs = []
                for name, suffix in (("left", "1234"), ("right", "abcd-0")):
                    body = (
                        "import os; os.write(2, ('open ' + os.path.dirname(os.environ['HOME']) + "
                        f"'/workspace/vault/.symdesk/retention/.symdesk-retention-{suffix}{ending}').encode())"
                    )
                    programs.append(self.executable(name, body))
                expected_suffix = "<TEMP>.tmp: permission denied" if ending == ".tmp: permission denied" else f"1234{ending}"
                oracle_stderr = (
                    "open <SANDBOX>/workspace/vault/.symdesk/retention/.symdesk-retention-"
                    f"{expected_suffix}"
                ).encode()
                status, _ = self.run_pair(*programs, oracle_stderr=oracle_stderr)
                self.assertEqual(status, expected)

    def test_equal_wrong_output_fails_the_go_oracle_contract(self):
        left = self.executable("left", "import os; os.write(1, b'wrong\\n')")
        right = self.executable("right", "import os; os.write(1, b'wrong\\n')")
        status, report = self.run_pair(left, right, oracle_stdout=b"expected\n")
        self.assertEqual(status, 1)
        self.assertFalse(report["cases"][0]["passed"])
        self.assertIn("Go stdout differs from the explicit contract", report["cases"][0]["oracle_failures"])

    def test_cleanup_after_leader_exit_closes_descendant_pipe(self):
        reader, writer = os.pipe()
        owners = []
        eof = False
        real_popen = subprocess.Popen
        body = """import os, signal, time
ready_reader, ready_writer = os.pipe()
if os.fork() == 0:
    os.close(ready_reader)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    os.write(ready_writer, b'R')
    os.close(ready_writer)
    time.sleep(30)
else:
    os.close(ready_writer)
    assert os.read(ready_reader, 1) == b'R'
    os.close(ready_reader)
"""
        binary = self.executable("descendant", body)

        def spawn(*args, **kwargs):
            process = real_popen(*args, **kwargs, pass_fds=(writer,))
            owners.append(process)
            os.close(writer)
            return process

        try:
            with mock.patch.object(RUNNER.subprocess, "Popen", side_effect=spawn):
                observation = RUNNER.execute(binary, {"args": []}, self.root)
            self.assertEqual(observation["exit"], 0)
            self.assertFalse(observation["timed_out"])
            ready, _, _ = select.select([reader], [], [], 2)
            if ready:
                eof = os.read(reader, 1) == b""
            self.assertTrue(eof, "owned descendant retained its pipe after leader exit")
        finally:
            if not eof:
                for process in owners:
                    with contextlib.suppress(ProcessLookupError):
                        os.killpg(process.pid, signal.SIGKILL)
            os.close(reader)
            if not owners:
                os.close(writer)


if __name__ == "__main__":
    unittest.main()
