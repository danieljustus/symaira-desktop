#!/usr/bin/env python3
"""Focused controls for the native config-paths build and evidence gate."""
from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import unittest
from unittest import mock

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts/rust-port"))
import config_paths_native as gate


def synthetic_capture(host: str = "Darwin") -> dict:
    cases = []
    skipped = 0
    for index in range(gate.CASE_COUNT):
        case_id = f"reviewed-case-{index:02d}"
        unix_only = index < gate.UNIX_CASE_COUNT
        not_applicable = host.lower().startswith("win") and unix_only
        skipped += int(not_applicable)
        cases.append({
            "id": case_id,
            "platform": "unix" if unix_only else "any",
            "input": {"id": case_id},
            "result": "not_applicable" if not_applicable else "passed",
            "go": None if not_applicable else {"exit_code": 0},
            "rust": None if not_applicable else {"exit_code": 0},
        })
    executed = gate.CASE_COUNT - skipped
    return {
        "head": "1" * 40,
        "head_after": "1" * 40,
        "candidate_clean": True,
        "git_status_before": "",
        "git_status_after": "",
        "go_source": {"commit": gate.FOUNDATION},
        "rust_source": {"commit": "1" * 40, "tree": "2" * 40},
        "harness_source": {"commit": "1" * 40, "tree": "3" * 40},
        "source_after": {"commit": "1" * 40, "tree": "4" * 40},
        "go_binary": {"go_version": gate.GO_VERSION, "vcs_revision": gate.FOUNDATION, "vcs_modified": "false"},
        "harness_binary": {"go_version": gate.GO_VERSION, "vcs_revision": "1" * 40, "vcs_modified": "false"},
        "go_corekit_source": {"module_sum": "h1:test"},
        "go_corekit_source_after": {"module_sum": "h1:test"},
        "mutation_control": {"comparator_rejected": True, "retained_input_unchanged": True},
        "declared_case_count": gate.CASE_COUNT,
        "executed_case_count": executed,
        "skipped_platform_case_count": skipped,
        "passed_case_count": executed,
        "cases": cases,
    }


class ConfigPathsNativeTests(unittest.TestCase):
    def test_hosted_oracle_has_real_git_directory_and_exact_foundation(self):
        with tempfile.TemporaryDirectory(prefix="config-paths-oracle-clone-") as temporary:
            parent = Path(temporary).resolve()
            source, oracle = parent / "source", parent / "oracle"
            source.mkdir()
            gate.git(source, "init", "-b", "candidate")
            gate.git(source, "config", "user.name", "Native Gate Test")
            gate.git(source, "config", "user.email", "88160656+danieljustus@users.noreply.github.com")
            gate.git(source, "config", "commit.gpgsign", "false")
            (source / "source.txt").write_bytes(b"immutable foundation\n")
            gate.git(source, "add", "source.txt")
            gate.git(source, "commit", "-m", "foundation fixture")
            foundation = gate.git(source, "rev-parse", "HEAD")
            (source / "source.txt").write_bytes(b"later candidate\n")
            gate.git(source, "commit", "-am", "candidate fixture")
            before = gate.repository_snapshot(source)

            with mock.patch.object(gate, "FOUNDATION", foundation):
                gate.create_oracle_checkout(source, oracle)

            self.assertTrue((oracle / ".git").is_dir())
            self.assertFalse((oracle / ".git/objects/info/alternates").exists())
            self.assertEqual(gate.git(oracle, "rev-parse", "HEAD"), foundation)
            self.assertEqual(gate.git(oracle, "remote", "get-url", "origin"), gate.ORIGIN)
            self.assertEqual(gate.git(oracle, "branch", "--show-current"), "")
            self.assertEqual(gate.git(oracle, "status", "--porcelain=v1"), "")
            self.assertEqual((oracle / "source.txt").read_bytes(), b"immutable foundation\n")
            self.assertEqual(gate.repository_snapshot(source), before)

    def test_operator_windows_cache_root_is_preserved_but_runtime_stays_private(self):
        with tempfile.TemporaryDirectory(prefix="config-paths-cache-root-") as temporary:
            parent = Path(temporary).resolve()
            output = parent / "evidence"
            output.mkdir()
            operator = {"HOME": str(parent / "operator"),
                        "LOCALAPPDATA": str(parent / "operator-cache"),
                        "SYMDESK_SERVER_TOKEN": "not-a-real-token"}
            probe = gate.sanitized_environment(operator)
            self.assertEqual(probe["LOCALAPPDATA"], operator["LOCALAPPDATA"])
            self.assertNotIn("SYMDESK_SERVER_TOKEN", probe)
            runtime, _launch = gate.private_environment(output, operator, None)
            self.assertEqual(Path(runtime["LOCALAPPDATA"]), output / "runtime/localappdata")
            self.assertNotEqual(runtime["LOCALAPPDATA"], operator["LOCALAPPDATA"])
            self.assertNotIn("SYMDESK_SERVER_TOKEN", runtime)

    def test_candidate_and_oracle_origins_accept_only_pinned_https_spellings(self):
        origins = (gate.ORIGIN, gate.ORIGIN.removesuffix(".git"),
                   gate.ORIGIN + "/extra", gate.ORIGIN + "?ref=main",
                   "https://github.com.evil.example/danieljustus/symaira-desktop")
        for role in ("candidate", "oracle"):
            for origin in origins:
                with self.subTest(role=role, origin=origin), tempfile.TemporaryDirectory() as temporary:
                    parent = Path(temporary).resolve()
                    candidate, oracle = parent / "candidate", parent / "oracle"
                    candidate.mkdir()
                    oracle.mkdir()
                    output = parent / "evidence"

                    def observe_git(root, *args):
                        if args == ("rev-parse", "--show-toplevel"):
                            return str(root)
                        if args == ("remote", "get-url", "origin"):
                            return origin if root == (candidate if role == "candidate" else oracle) else gate.ORIGIN
                        if args == ("rev-parse", "HEAD"):
                            return "not-a-commit" if root == (candidate if role == "candidate" else oracle) else "1" * 40
                        if args == ("branch", "--show-current"):
                            return "test-candidate"
                        if args == ("rev-parse", "HEAD^{tree}"):
                            return "2" * 40
                        return ""

                    with mock.patch.object(gate, "ROOT", candidate), \
                         mock.patch.object(sys, "argv", ["native-gate", "--evidence-dir", str(output),
                                                        "--oracle-worktree", str(oracle)]), \
                         mock.patch.object(gate, "private_environment", return_value=({}, {})), \
                         mock.patch.object(gate, "git", side_effect=observe_git), \
                         mock.patch.object(sys, "stderr"):
                        self.assertEqual(gate.main(), 1)
                    receipt = json.loads((output / "gate.json").read_bytes())
                    self.assertFalse(receipt["passed"])
                    if origin in (gate.ORIGIN, gate.ORIGIN.removesuffix(".git")):
                        expected = "HEAD is not a full immutable commit" if role == "candidate" else "not the exact clean immutable foundation"
                    else:
                        expected = "origin does not match the pinned repository"
                    self.assertIn(expected, receipt["failure"])

    def test_output_symlink_parent_cannot_reenter_candidate_before_creation(self):
        with tempfile.TemporaryDirectory(prefix="config-paths-output-boundary-") as temporary:
            parent = Path(temporary).resolve()
            candidate = parent / "candidate"
            candidate.mkdir()
            alias = parent / "external-looking-alias"
            alias.symlink_to(candidate, target_is_directory=True)
            for relative in ("output", "missing/parents/output"):
                with self.subTest(relative=relative):
                    output = alias / relative
                    with mock.patch.object(gate, "ROOT", candidate), \
                         mock.patch.object(sys, "argv", ["native-gate", "--evidence-dir", str(output)]), \
                         mock.patch.object(sys, "stderr") as stderr, \
                         mock.patch.object(gate, "private_environment") as environment:
                        with self.assertRaises(SystemExit) as rejected:
                            gate.main()
                        self.assertEqual(rejected.exception.code, 2)
                        self.assertIn("outside the candidate checkout", "".join(
                            call.args[0] for call in stderr.write.call_args_list
                        ))
                        environment.assert_not_called()
                    self.assertEqual(list(candidate.iterdir()), [])

    def test_failure_keeps_exact_child_streams_and_exit(self):
        with tempfile.TemporaryDirectory(prefix="config-paths-command-") as temporary:
            root = Path(temporary)
            logs = root / "commands"
            logs.mkdir()
            report = {"commands": []}
            script = "import sys; sys.stdout.write('raw stdout\\n'); sys.stderr.write('raw stderr\\n'); sys.exit(23)"
            with self.assertRaisesRegex(gate.GateFailure, "raw logs retained"):
                gate.run_logged_command(
                    report,
                    logs,
                    "controlled-failure",
                    [sys.executable, "-c", script],
                    root,
                    env=dict(os.environ),
                    launch_env=dict(os.environ),
                )
            command = report["commands"][0]
            stdout = root / command["stdout_file"]
            stderr = root / command["stderr_file"]
            self.assertEqual(command["exit_code"], 23)
            self.assertFalse(command["timed_out"])
            self.assertEqual(stdout.read_bytes(), b"raw stdout\n")
            self.assertEqual(stderr.read_bytes(), b"raw stderr\n")
            self.assertEqual(command["stdout_sha256"], hashlib.sha256(b"raw stdout\n").hexdigest())
            self.assertEqual(command["stderr_sha256"], hashlib.sha256(b"raw stderr\n").hexdigest())

    def test_timeout_is_marked_before_process_tree_cleanup(self):
        with tempfile.TemporaryDirectory(prefix="config-paths-timeout-") as temporary:
            root = Path(temporary)
            logs = root / "commands"
            logs.mkdir()
            report = {"commands": []}
            observed_timeout_state = []
            terminate = gate._terminate_process_tree

            def observe_cleanup(process, env):
                observed_timeout_state.append(report["commands"][0]["timed_out"])
                return terminate(process, env)

            with mock.patch.object(gate, "_terminate_process_tree", observe_cleanup):
                with self.assertRaisesRegex(gate.GateFailure, "timed_out=True"):
                    gate.run_logged_command(
                        report,
                        logs,
                        "controlled-timeout",
                        [sys.executable, "-c", "import time; time.sleep(10)"],
                        root,
                        env=dict(os.environ),
                        launch_env=dict(os.environ),
                        timeout=0.05,
                    )
            self.assertEqual(observed_timeout_state, [True])
            command = report["commands"][0]
            self.assertTrue(command["timed_out"])
            self.assertTrue((root / command["stdout_file"]).is_file())
            self.assertTrue((root / command["stderr_file"]).is_file())

    def test_source_identity_and_zero_or_wrong_inventory_fail_closed(self):
        with self.assertRaisesRegex(gate.GateFailure, "source inputs changed"):
            gate.require_sources_unchanged({"sha256": "before"}, {"sha256": "after"})
        capture = synthetic_capture()
        accepted = gate.validate_capture_report(capture, "Darwin", "1" * 40)
        self.assertEqual((accepted["declared"], accepted["executed_pairs"], accepted["not_applicable"]), (75, 75, 0))
        zero = dict(capture, declared_case_count=0, executed_case_count=0, passed_case_count=0, cases=[])
        with self.assertRaisesRegex(gate.GateFailure, "not exactly 75"):
            gate.validate_capture_report(zero, "Darwin", "1" * 40)
        wrong = dict(capture, cases=capture["cases"][:-1])
        with self.assertRaisesRegex(gate.GateFailure, "not exactly 75"):
            gate.validate_capture_report(wrong, "Darwin", "1" * 40)
        duplicate = dict(capture, cases=list(capture["cases"]))
        duplicate["cases"][-1] = dict(
            duplicate["cases"][-1],
            id=duplicate["cases"][0]["id"],
            input={"id": duplicate["cases"][0]["id"]},
        )
        with self.assertRaisesRegex(gate.GateFailure, "duplicate IDs"):
            gate.validate_capture_report(duplicate, "Darwin", "1" * 40)

    def test_go_metadata_only_source_is_rejected_after_package_graph_resolution(self):
        metadata_only = {
            "Path": gate.COREKIT,
            "Version": gate.COREKIT_VERSION,
            "Sum": "h1:corekit-test",
        }
        with self.assertRaisesRegex(gate.GateFailure, "no resolved source directory"):
            gate.go_module_sources([{"Module": metadata_only}])

        with tempfile.TemporaryDirectory(prefix="config-paths-module-source-") as temporary:
            root = Path(temporary)
            corekit_dir = root / "symaira-corekit@v0.18.2"
            corekit_dir.mkdir()
            (corekit_dir / "go.mod").write_text("module github.com/danieljustus/symaira-corekit\\n", encoding="utf-8")
            resolved_module = {
                **metadata_only,
                "Dir": str(corekit_dir),
            }
            calls = []

            def run_query(_report, _logs, name, _command, _cwd, **_kwargs):
                calls.append(name)
                if name in {"resolve-go-cli-build-inputs", "resolve-go-harness-test-inputs"}:
                    value = {"ImportPath": name, "Module": resolved_module}
                else:
                    value = metadata_only
                return json.dumps(value).encode("utf-8")

            arguments = (
                {"commands": []}, root, root, root,
            )
            options = {"env": {}, "launch_env": {}, "launcher": None}
            with mock.patch.object(gate, "run_logged_command", side_effect=run_query):
                with self.assertRaisesRegex(gate.GateFailure, "no resolved source directory"):
                    gate.resolve_go_source_inputs(*arguments, **options)
            self.assertEqual(calls, [
                "resolve-go-cli-build-inputs",
                "resolve-go-harness-test-inputs",
                "resolve-pinned-corekit-source",
            ])

            calls.clear()

            def run_resolved_query(_report, _logs, name, _command, _cwd, **_kwargs):
                calls.append(name)
                if name in {"resolve-go-cli-build-inputs", "resolve-go-harness-test-inputs"}:
                    value = {"ImportPath": name, "Module": resolved_module}
                else:
                    value = resolved_module
                return json.dumps(value).encode("utf-8")

            with mock.patch.object(gate, "run_logged_command", side_effect=run_resolved_query):
                _packages, modules, identity, source_dir = gate.resolve_go_source_inputs(*arguments, **options)
            self.assertEqual(calls, [
                "resolve-go-cli-build-inputs",
                "resolve-go-harness-test-inputs",
                "resolve-pinned-corekit-source",
            ])
            self.assertEqual(len(modules), 1)
            self.assertEqual(identity["directory"], str(corekit_dir.resolve()))
            self.assertEqual(source_dir, corekit_dir.resolve())

    def test_windows_requires_59_real_pairs_and_16_explicit_unix_skips(self):
        capture = synthetic_capture("Windows")
        accepted = gate.validate_capture_report(capture, "Windows", "1" * 40)
        self.assertEqual((accepted["declared"], accepted["executed_pairs"], accepted["not_applicable"]), (75, 59, 16))
        broken = dict(capture, skipped_platform_case_count=15)
        with self.assertRaisesRegex(gate.GateFailure, "skipped_platform_case_count"):
            gate.validate_capture_report(broken, "Windows", "1" * 40)

    def test_native_matrix_wires_early_gate_and_sha_platform_evidence(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8")
        job_match = re.search(r"(?ms)^  rust-native:\n(?P<body>.*?)(?=^  [A-Za-z0-9_-]+:\n|\Z)", workflow)
        if job_match is None:
            self.fail("missing rust-native matrix job")
        job = job_match.group("body")
        self.assertIn("if: github.event_name != 'pull_request'", job)
        self.assertIn("os: [ubuntu-latest, ubuntu-24.04-arm, macos-latest, macos-15-intel, windows-latest, windows-11-arm]", job)
        checkout = job.split("      - uses: actions/checkout@", 1)[1].split("\n      - ", 1)[0]
        self.assertIn("          ref: ${{ github.sha }}\n", checkout,
                      "main push, schedule and dispatch must detach the immutable trigger SHA")
        steps = re.split(r"(?m)^      - name: ", job)[1:]
        gate_index = next(i for i, step in enumerate(steps) if step.startswith("Run native config paths differential"))
        artifact_index = next(i for i, step in enumerate(steps) if step.startswith("Retain native config paths evidence"))
        contacts_index = next(i for i, step in enumerate(steps) if step.startswith("Replay native contacts and ingest path contracts"))
        workspace_index = next(i for i, step in enumerate(steps) if step.startswith("Check, lint, and test Rust workspace"))
        self.assertLess(gate_index, contacts_index)
        self.assertLess(artifact_index, contacts_index)
        self.assertLess(gate_index, workspace_index)
        gate_step = steps[gate_index]
        self.assertIn("        shell: bash\n", gate_step)
        self.assertIn("          set -euo pipefail", gate_step)
        self.assertIn('make config-paths-native CONFIG_PATHS_EVIDENCE_DIR="$CONFIG_PATHS_EVIDENCE_DIR"', gate_step)
        self.assertIn("${{ matrix.os }}", gate_step)
        self.assertIn("${{ github.sha }}", gate_step)
        artifact = steps[artifact_index]
        self.assertIn("        if: always()", artifact)
        self.assertIn("config-paths-native-${{ matrix.os }}-${{ github.sha }}", artifact)
        self.assertIn("/capture/", artifact)
        self.assertIn("/commands/", artifact)
        self.assertIn("if-no-files-found: error", artifact)

        makefile = (ROOT / "Makefile").read_text(encoding="utf-8")
        self.assertRegex(makefile, r"(?m)^\.PHONY:.*\bconfig-paths-native\b")
        self.assertIn("config-paths-native:", makefile)
        self.assertIn("scripts/rust-port/config_paths_native.py", makefile)
        self.assertIn("--evidence-dir", makefile)
        self.assertIn("--build-launcher", makefile)
        self.assertIn("--oracle-worktree", makefile)


if __name__ == "__main__":
    unittest.main()
