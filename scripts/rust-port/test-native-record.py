#!/usr/bin/env python3
"""Failure controls for the native recording harness (no historical fixtures)."""

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("native_record", Path(__file__).with_name("native-record.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class NativeRecordTests(unittest.TestCase):
    def setUp(self):
        self.owned = tempfile.TemporaryDirectory(dir=Path.cwd())
        self.addCleanup(self.owned.cleanup)
        self.root = Path(self.owned.name)
        self.binary = self.root / "symdesk"
        self.binary.write_bytes(b"current native binary")
        self.destination = self.root / "evidence"
        self.environ = patch.dict(os.environ, {"SYMDESK_NATIVE_RECORD": str(self.destination)})
        self.environ.start()
        self.addCleanup(self.environ.stop)

    def result(self, stdout=None, stderr=b"", code=0):
        return module.subprocess.CompletedProcess([], code, stdout or b'{"tool":"symdesk","version":"current","schema_version":1}\n', stderr)

    def test_three_invocations_have_distinct_fresh_stores(self):
        roots = []

        def invoke(command, **kwargs):
            root = Path(kwargs["cwd"])
            self.assertEqual(command, [str(self.binary), "version", "--json"])
            self.assertNotIn(root, roots)
            roots.append(root)
            env = kwargs["env"]
            self.assertNotIn("SYMDESK_NATIVE_RECORD", env)
            self.assertNotIn("SYMDESK_VAULT", env)
            for key in ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP"):
                self.assertEqual(list(Path(env[key]).iterdir()), [])
                self.assertEqual(Path(env[key]).parent, root)
            return self.result()

        with patch.object(module.subprocess, "run", side_effect=invoke):
            module.record(self.binary, "current", self.root)
        self.assertEqual(len(roots), 3)
        self.assertTrue(all(not root.exists() for root in roots))
        self.assertEqual(len(json.loads((self.destination / "record.json").read_text())["samples"]), 3)
        with self.assertRaises(FileExistsError):
            module.record(self.binary, "current", self.root)

    def test_bad_outputs_do_not_publish_success_record(self):
        for result in (self.result(stderr=b"diagnostic"), self.result(code=2), self.result(stdout=b"log\n{}"), self.result(stdout=b'[]'), self.result(stdout=b'{"tool":"symdesk","version":"old","schema_version":1}'), self.result(stdout=b'{"tool":"symdesk","version":"current","schema_version":true}')):
            with self.subTest(result=result), tempfile.TemporaryDirectory(dir=self.root) as case:
                destination = Path(case) / "evidence"
                with patch.dict(os.environ, SYMDESK_NATIVE_RECORD=str(destination)), patch.object(module.subprocess, "run", return_value=result):
                    with self.assertRaises(ValueError):
                        module.record(self.binary, "current", self.root)
                self.assertFalse((destination / "record.json").exists())

    def test_changed_binary_does_not_publish_success_record(self):
        def invoke(*args, **kwargs):
            self.binary.write_bytes(b"different binary")
            return self.result()

        with patch.object(module.subprocess, "run", side_effect=invoke):
            with self.assertRaisesRegex(ValueError, "binary changed"):
                module.record(self.binary, "current", self.root)
        self.assertFalse((self.destination / "record.json").exists())

    def test_recording_requires_explicit_destination(self):
        with patch.dict(os.environ, SYMDESK_NATIVE_RECORD=""):
            with self.assertRaisesRegex(ValueError, "fresh evidence directory"):
                module.record(self.binary, "current", self.root)


if __name__ == "__main__":
    unittest.main()
