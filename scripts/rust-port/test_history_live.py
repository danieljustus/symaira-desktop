"""Failure controls for the history runner, not Go/Rust parity evidence."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("history_live", Path(__file__).with_name("history_live.py"))
assert spec is not None and spec.loader is not None
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class HistoryRunnerControls(unittest.TestCase):
    def exercise(self, failure=None, version="go version go1.26.6 test/test\n", marker=True, drift=False, uppercase_ambient=False):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            output = root / "evidence"
            ambient_gocache = str(root / "ambient-gocache")
            ambient_program_files = str(root / "ProgramFiles")
            ambient_program_files_x86 = str(root / "ProgramFiles(x86)")
            ambient_localappdata = str(root / "AppData" / "Local")
            sentinel_cred_key = "SENTINEL_CREDENTIAL_PATH"
            sentinel_cred_val = str(root / "machine" / "sentinel.store")
            calls = []

            def execute(command, **kwargs):
                index = len(calls)
                calls.append(command)
                self.assertEqual(kwargs["cwd"], root)
                self.assertNotEqual(kwargs["env"]["HOME"], str(Path.home()))
                self.assertEqual(kwargs["env"]["SYMDESK_HISTORY_ORACLE"], str(output / "oracle.json"))
                self.assertGreater(kwargs["timeout"], 0)
                self.assertIn("GOCACHE", kwargs["env"])
                gocache = Path(kwargs["env"]["GOCACHE"])
                self.assertTrue(gocache.exists())
                self.assertTrue(gocache.is_absolute())
                self.assertEqual(gocache.parent, Path(kwargs["env"]["HOME"]).parent)
                self.assertNotEqual(kwargs["env"]["GOCACHE"], kwargs["env"]["HOME"])
                self.assertNotEqual(kwargs["env"]["GOCACHE"], ambient_gocache)
                env_upper = {k.upper(): v for k, v in kwargs["env"].items()}
                self.assertEqual(env_upper.get("PROGRAMFILES"), ambient_program_files)
                self.assertEqual(env_upper.get("PROGRAMFILES(X86)"), ambient_program_files_x86)
                self.assertNotIn("LOCALAPPDATA", env_upper)
                self.assertNotIn(sentinel_cred_key.upper(), env_upper)
                text = {0: version, 1: "rustc 1.98.0 test\n", 3: "test test_history_live_differential_against_go_oracle ... ok\n" if marker else "running 0 tests\n"}.get(index, "")
                kwargs["stdout"].write(text.encode())
                kwargs["stderr"].write(b"control stderr\n")
                if index == 2:
                    (output / "oracle.json").write_bytes(b"test-owned control bytes, not a Go capture")
                if failure == "timeout" and index == 2:
                    raise subprocess.TimeoutExpired(command, 900)
                return subprocess.CompletedProcess(command, 23 if failure == index else 0)

            manifests = [{"source": "before"}, {"source": "after" if drift else "before"}]
            ambient_env = {
                "GOCACHE": ambient_gocache,
                "LOCALAPPDATA": ambient_localappdata,
                sentinel_cred_key: sentinel_cred_val,
            }
            if uppercase_ambient:
                ambient_env["PROGRAMFILES"] = ambient_program_files
                ambient_env["PROGRAMFILES(X86)"] = ambient_program_files_x86
            else:
                ambient_env["ProgramFiles"] = ambient_program_files
                ambient_env["ProgramFiles(x86)"] = ambient_program_files_x86

            with patch.object(runner, "ROOT", root), patch.object(runner, "OUTPUT", output), patch.object(runner, "source_manifest", side_effect=manifests), patch.object(runner.subprocess, "check_output", return_value="a" * 40), patch.object(runner.subprocess, "run", side_effect=execute), patch.dict(runner.os.environ, ambient_env):
                if failure == "timeout":
                    with self.assertRaises(subprocess.TimeoutExpired):
                        runner.run()
                    result = None
                elif drift or not marker or "go1.26.6 " not in version:
                    with self.assertRaises(RuntimeError):
                        runner.run()
                    result = None
                else:
                    result = runner.run()
            report = json.loads((output / "report.json").read_bytes())
            for index in range(len(calls)):
                self.assertEqual((output / f"{index:02d}-stderr.log").read_bytes(), b"control stderr\n")
            return result, report, calls

    def test_success_and_every_nonzero_stage(self):
        result, report, calls = self.exercise()
        self.assertEqual(result, 0)
        self.assertTrue(report["passed"])
        self.assertEqual(len(calls), 4)
        for index in range(4):
            result, report, calls = self.exercise(failure=index)
            self.assertEqual(result, 23)
            self.assertFalse(report["passed"])
            self.assertEqual(len(calls), index + 1)

    def test_missing_execution_wrong_compiler_and_source_drift_fail_closed(self):
        for marker, version, drift in ((False, "go version go1.26.6 test/test\n", False), (True, "go version go1.27.1 test/test\n", False), (True, "go version go1.26.6 test/test\n", True)):
            _, report, _ = self.exercise(marker=marker, version=version, drift=drift)
            self.assertFalse(report["passed"])

    def test_timeout_retains_raw_observations(self):
        _, report, calls = self.exercise(failure="timeout")
        self.assertFalse(report["passed"])
        self.assertEqual(len(calls), 3)
        self.assertIsNone(report["commands"][-1]["exit_code"])
        self.assertIn("oracle_sha256", report)

    def test_canonical_uppercase_installation_roots_and_isolation(self):
        result, report, calls = self.exercise(uppercase_ambient=True)
        self.assertEqual(result, 0)
        self.assertTrue(report["passed"])
        self.assertEqual(len(calls), 4)


if __name__ == "__main__":
    unittest.main()
