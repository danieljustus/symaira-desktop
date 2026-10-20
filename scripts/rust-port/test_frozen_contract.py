"""Failure controls for the Go-independent frozen contract runner (#1153)."""
import importlib.util
import os
from pathlib import Path
import shutil
import stat
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("frozen_contract", Path(__file__).with_name("frozen_contract.py"))
assert spec is not None and spec.loader is not None
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)

MAKEFILE = """\
a-differential: a-fixtures-check
\t$(PORTGEN_CHECK_ENV) go test ./internal/a
\t$(CARGO) test -p a --locked
b-differential:
\tgo run ./scripts/rust-port/cmd/diffharness
c-differential:
\t$(CARGO) test -p a --locked
"""
OK = "test result: ok. 3 passed; 0 failed; 1 ignored; 0 measured; 0 filtered out\n"


class FrozenContractControls(unittest.TestCase):
    def manifest(self, **replays):
        return {"live_only": {"b-differential": "live"},
                "replays": replays or {"cargo test -p a --locked": {"passed": 3, "ignored": 1}}}

    def test_parses_and_dedupes_cargo_replays(self):
        targets = runner.differential_targets(MAKEFILE)
        self.assertEqual(targets["a-differential"], ["cargo test -p a --locked"])
        self.assertEqual(targets["b-differential"], [])
        commands, errors = runner.plan(targets, self.manifest())
        self.assertEqual((commands, errors), (["cargo test -p a --locked"], []))

    def test_unclassified_live_target_fails(self):
        manifest = self.manifest()
        manifest["live_only"] = {}
        _, errors = runner.plan(runner.differential_targets(MAKEFILE), manifest)
        self.assertIn("b-differential: no Rust replay and not declared live_only", errors)

    def test_replayable_target_cannot_hide_as_live_only(self):
        manifest = self.manifest()
        manifest["live_only"]["a-differential"] = "hidden"
        _, errors = runner.plan(runner.differential_targets(MAKEFILE), manifest)
        self.assertIn("a-differential: has a Rust replay but is declared live_only", errors)

    def test_missing_and_stale_counts_fail(self):
        _, errors = runner.plan(runner.differential_targets(MAKEFILE), self.manifest(**{"cargo test -p gone": {}}))
        self.assertTrue(any("missing recorded counts" in e for e in errors))
        self.assertTrue(any("stale replay entry" in e for e in errors))

    def test_unexpanded_variable_fails(self):
        with self.assertRaises(ValueError):
            runner.differential_targets("x-differential:\n\t$(CARGO) test -p $(PKG)\n")

    def test_counts_require_result_lines_and_ok(self):
        self.assertEqual(runner.counts(OK + OK), {"passed": 6, "failed": 0, "ignored": 2})
        with self.assertRaises(ValueError):
            runner.counts("running 0 tests\n")
        with self.assertRaises(ValueError):
            runner.counts(OK + "test result: FAILED. 1 passed; 1 failed; 0 ignored;")

    def test_go_is_removed_from_path_and_user_roots_are_private(self):
        with tempfile.TemporaryDirectory() as directory:
            bin_dir = Path(directory, "bin")
            bin_dir.mkdir()
            go = bin_dir / "go"
            go.write_text("#!/bin/sh\nexit 0\n")
            go.chmod(go.stat().st_mode | stat.S_IXUSR)
            root = Path(directory, "root")
            root.mkdir()
            saved = os.environ.get("PATH", "")
            os.environ["PATH"] = os.pathsep.join([str(bin_dir), saved])
            try:
                env = runner.go_free_env(root)
            finally:
                os.environ["PATH"] = saved
            self.assertNotIn(str(bin_dir), env["PATH"].split(os.pathsep))
            self.assertEqual(env["HOME"], str(root / "home"))
            self.assertTrue(env["XDG_CONFIG_HOME"].startswith(str(root)))
            self.assertNotIn("GOROOT", env)

    def test_mutations_change_recorded_observations(self):
        raw = b'{"cases": [{"n": 1}, {"n": 5}]}'
        self.assertEqual(runner.mutate(raw, {"append": "\n"}), raw + b"\n")
        mutated = __import__("json").loads(runner.mutate(raw, {"increment": ["cases", -1, "n"]}))
        self.assertEqual(mutated["cases"], [{"n": 1}, {"n": 6}])

    def test_recorded_manifest_covers_the_real_makefile(self):
        manifest = __import__("json").loads(runner.MANIFEST.read_text())
        targets = runner.differential_targets((runner.ROOT / "Makefile").read_text())
        commands, errors = runner.plan(targets, manifest)
        self.assertEqual(errors, [])
        self.assertGreater(len(commands), 50)
        for command in commands:
            self.assertGreater(runner.expected_counts(manifest["replays"][command])[0], 0, command)

    def test_platform_gated_replay_counts_are_per_platform(self):
        entry = {"by_platform": {"linux": {"passed": 2, "ignored": 0}}}
        original = runner.sys.platform
        try:
            runner.sys.platform = "linux"
            self.assertEqual(runner.expected_counts(entry), (2, 0))
            runner.sys.platform = "darwin"
            with self.assertRaises(RuntimeError):
                runner.expected_counts(entry)
        finally:
            runner.sys.platform = original


    @unittest.skipUnless(os.name == "posix", "POSIX ancestor modes")
    def test_replay_parent_requires_owner_only_ancestors(self):
        base = Path(tempfile.mkdtemp(dir=runner.replay_parent())).resolve()
        try:
            shared = base / "group-writable"
            shared.mkdir()
            os.chmod(shared, 0o775)
            self.assertTrue(runner.secure_ancestry(base))
            self.assertFalse(runner.secure_ancestry(shared))
            nested = shared / "replay"
            nested.mkdir()
            self.assertFalse(runner.secure_ancestry(nested), "an insecure ancestor taints every child")
            old = os.environ.get(runner.REPLAY_PARENT_ENV)
            try:
                os.environ[runner.REPLAY_PARENT_ENV] = str(nested)
                with self.assertRaises(RuntimeError):
                    runner.replay_parent()
                os.chmod(shared, 0o700)
                self.assertEqual(runner.replay_parent(), nested)
            finally:
                if old is None:
                    os.environ.pop(runner.REPLAY_PARENT_ENV, None)
                else:
                    os.environ[runner.REPLAY_PARENT_ENV] = old
        finally:
            os.chmod(base / "group-writable", 0o700)
            shutil.rmtree(base)


if __name__ == "__main__":
    unittest.main()
