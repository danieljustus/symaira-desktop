"""Mutation controls for the real index-status comparator CLI.

The valid input below is intentionally synthetic and tests structure only; it is
never counted as Go/Rust execution evidence.
"""

from __future__ import annotations

import copy
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
REPO = HERE.parents[2]
sys.path.insert(0, str(HERE))
import compare  # noqa: E402


class CompareNegativeControls(unittest.TestCase):
    def setUp(self) -> None:
        parent = Path(os.environ.get("TMPDIR", tempfile.gettempdir()))
        self.temp_context = tempfile.TemporaryDirectory(prefix="index-status-negative-", dir=parent)
        self.root = Path(self.temp_context.name)
        self.capture = self.root / "capture"
        self.capture.mkdir()
        self.report_path = self.root / "compare-report.json"
        self.manifest_path = self.capture / "manifest.json"
        self.manifest = self._make_valid_structure()
        self.baseline = copy.deepcopy(self.manifest)
        self._write_manifest()

    def tearDown(self) -> None:
        self.temp_context.cleanup()

    def _make_valid_structure(self) -> dict[str, Any]:
        head = subprocess.check_output(["git", "-C", str(REPO), "rev-parse", "HEAD"], text=True).strip()
        bins = self.root / "binaries"
        bins.mkdir()
        binary_paths: dict[str, Path] = {}
        identities: dict[str, dict[str, Any]] = {}
        for role, payload, revision, provenance in (
            ("go", b"synthetic-go-identity", compare.CANONICAL_ORACLE_COMMIT, "structural unit control"),
            ("rust", b"synthetic-rust-identity", head, "structural unit control"),
        ):
            path = bins / f"{role}.bin"
            path.write_bytes(payload)
            binary_paths[role] = path.resolve()
            identities[role] = {
                "role": role,
                "path": str(path.resolve()),
                "sha256": hashlib.sha256(payload).hexdigest(),
                "size_bytes": len(payload),
                "build_source_commit": revision,
                "build_provenance": provenance,
            }

        worlds: dict[str, dict[str, str]] = {}
        resolved_worlds: dict[str, dict[str, Path]] = {}
        for role in ("go", "rust"):
            for group in ("empty", "populated"):
                key = f"{role}-{group}"
                world = self.root / "worlds" / key
                fields = {
                    "world": world,
                    "home": world / "home",
                    "xdg_data": world / "xdg-data",
                    "xdg_config": world / "xdg-config",
                    "temp": world / "temp",
                    "vault": world / "vault",
                    "appdata": world / "appdata",
                    "local_appdata": world / "local-appdata",
                }
                for directory in fields.values():
                    directory.mkdir(parents=True, exist_ok=True)
                resolved_worlds[key] = {name: path.resolve() for name, path in fields.items()}
                worlds[key] = {name: str(path.resolve()) for name, path in fields.items()}

        platform_id = compare._platform_identity()
        runs: list[dict[str, Any]] = []
        executed = list(compare.REQUIRED_CASE_IDS) + [f"rust:{case}" for case in compare.REQUIRED_CASE_IDS]
        for world_key in ("go-populated", "rust-populated"):
            runs.append(self._run_record("go", "seed_index", binary_paths["go"], world_key, resolved_worlds, platform_id, status_case=False))

        error_cases = {
            "documents_invalid_state_json",
            "documents_missing_vault_json",
            "aggregate_missing_vault_json",
            "aggregate_invalid_retrieval_db_json",
            "timeout_negative_json",
            "timeout_blocked_local_provider_json",
        }
        for role in ("go", "rust"):
            for case_id in compare.REQUIRED_CASE_IDS:
                world_key = f"{role}-empty" if case_id in compare.EMPTY_CASES else f"{role}-populated"
                duration = compare.DURATION_CASE_BY_ID.get(case_id)
                expected_exit = duration[2] if duration else (1 if case_id in error_cases else 0)
                extra: dict[str, Any] = {}
                if duration:
                    mode, duration_input, _ = duration
                    extra.update({"duration_mode": mode, "duration_input": duration_input})
                if case_id == "provider_retry_one_probe_json":
                    extra["provider_probe_count"] = 1
                    extra["provider_response_codes"] = [503]
                elapsed = 0.25 if case_id == "timeout_zero_delayed_local_provider_json" else 0.15 if case_id == "timeout_blocked_local_provider_json" else 0.01
                runs.append(
                    self._run_record(
                        role,
                        case_id,
                        binary_paths[role],
                        world_key,
                        resolved_worlds,
                        platform_id,
                        expected_exit=expected_exit,
                        elapsed=elapsed,
                        extra=extra,
                    )
                )

        head = subprocess.check_output(["git", "-C", str(REPO), "rev-parse", "HEAD"], text=True).strip()
        return {
            "schema_version": 2,
            "purpose": "synthetic structural test control only",
            "evidence_class": "diagnostic-replay",
            "source_commit": head,
            "source_worktree": str(REPO.resolve()),
            "source_tree_status": [],
            "canonical_oracle_commit": compare.CANONICAL_ORACLE_COMMIT,
            "canonical_oracle_is_ancestor": True,
            "case_inventory_sha256": compare.inventory_sha256(),
            "platform": platform_id,
            "roles": ["go", "rust"],
            "go_binary": identities["go"],
            "rust_binary": identities["rust"],
            "declared_case_ids": list(compare.REQUIRED_CASE_IDS),
            "executed_case_ids": executed,
            "normalization_policy": "synthetic structure control",
            "runs": runs,
            "worlds": worlds,
        }

    def _run_record(
        self,
        role: str,
        case_id: str,
        binary: Path,
        world_key: str,
        worlds: dict[str, dict[str, Path]],
        platform_id: dict[str, str],
        *,
        status_case: bool = True,
        expected_exit: int = 0,
        elapsed: float = 0.01,
        extra: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        world = worlds[world_key]
        environment = {
            "HOME": str(world["home"]),
            "USERPROFILE": str(world["home"]),
            "XDG_DATA_HOME": str(world["xdg_data"]),
            "XDG_CONFIG_HOME": str(world["xdg_config"]),
            "TMPDIR": str(world["temp"]),
            "TEMP": str(world["temp"]),
            "TMP": str(world["temp"]),
            "APPDATA": str(world["appdata"]),
            "LOCALAPPDATA": str(world["local_appdata"]),
            "LANG": "C.UTF-8",
            "LC_ALL": "C.UTF-8",
            "TZ": "UTC",
            "PATH": str(binary.parent),
            "HTTP_PROXY": "",
            "HTTPS_PROXY": "",
            "ALL_PROXY": "",
            "http_proxy": "",
            "https_proxy": "",
            "all_proxy": "",
            "NO_PROXY": "*",
            "no_proxy": "*",
        }
        argv = compare.case_argv(case_id, str(binary), {key: str(path) for key, path in world.items()})
        stem = f"{role}--{case_id}--{world_key}" if case_id == "seed_index" else f"{role}--{case_id}"
        stdout_name = f"{stem}.stdout.bin"
        stderr_name = f"{stem}.stderr.bin"
        stdout = b'{"synthetic":"structural-only"}\n'
        stderr = b""
        (self.capture / stdout_name).write_bytes(stdout)
        (self.capture / stderr_name).write_bytes(stderr)
        identity = self.manifest_binary_identity(role, binary)
        record: dict[str, Any] = {
            "case_id": case_id,
            "role": role,
            "status_case": status_case,
            "argv": argv,
            "binary_path": str(binary),
            "binary_sha256": identity["sha256"],
            "cwd": str(REPO.resolve()),
            "world_key": world_key,
            "environment": environment,
            "platform": platform_id,
            "exit_code": expected_exit,
            "expected_exit_code": expected_exit,
            "timed_out": False,
            "success": True,
            "elapsed_seconds": elapsed,
            "stdout_file": stdout_name,
            "stdout_size_bytes": len(stdout),
            "stdout_sha256": hashlib.sha256(stdout).hexdigest(),
            "stderr_file": stderr_name,
            "stderr_size_bytes": len(stderr),
            "stderr_sha256": hashlib.sha256(stderr).hexdigest(),
        }
        if extra:
            record.update(extra)
        return record

    def manifest_binary_identity(self, role: str, path: Path) -> dict[str, Any]:
        for binary in self.root.joinpath("binaries").glob("*.bin"):
            if binary.resolve() == path.resolve():
                payload = binary.read_bytes()
                return {"sha256": hashlib.sha256(payload).hexdigest(), "size_bytes": len(payload)}
        self.fail(f"test binary identity missing for {role}")
        raise AssertionError("unreachable")

    def _write_manifest(self) -> None:
        self.manifest_path.write_text(json.dumps(self.manifest, ensure_ascii=True, indent=2) + "\n", encoding="utf-8")

    def _invoke(self) -> tuple[int, dict[str, Any]]:
        result = subprocess.run(
            [
                sys.executable,
                str(HERE / "compare.py"),
                "--manifest", str(self.manifest_path),
                "--report", str(self.report_path),
                "--expected-rust-commit", self.baseline["rust_binary"]["build_source_commit"],
                "--expected-go-sha256", self.baseline["go_binary"]["sha256"],
                "--expected-rust-sha256", self.baseline["rust_binary"]["sha256"],
            ],
            cwd=REPO,
            env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"},
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=30,
            check=False,
        )
        if not self.report_path.is_file():
            self.fail(f"comparator failed to preserve its report: {result.stderr.decode(errors='replace')}")
        return result.returncode, json.loads(self.report_path.read_bytes())

    def _assert_valid_baseline(self) -> None:
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertEqual(exit_code, 0, report["errors"])
        self.assertTrue(report["pass"])

    def test_valid_structural_control_executes_all_required_ids(self) -> None:
        self._assert_valid_baseline()
        self.assertEqual(len(compare.REQUIRED_CASE_IDS), 85)
        self.assertEqual(len(self.manifest["executed_case_ids"]), 170)
        report = json.loads(self.report_path.read_bytes())
        self.assertEqual(report["compared_cases"], len(compare.REQUIRED_CASE_IDS))
        self.assertFalse(report["clean_acceptance"])

    def test_reduced_unknown_and_duplicate_case_inventories_are_rejected(self) -> None:
        self._assert_valid_baseline()
        self.manifest["declared_case_ids"] = list(compare.REQUIRED_CASE_IDS[:2])
        self.manifest["executed_case_ids"] = [
            *compare.REQUIRED_CASE_IDS[:2],
            *(f"rust:{case}" for case in compare.REQUIRED_CASE_IDS[:2]),
        ]
        allowed = set(compare.REQUIRED_CASE_IDS[:2])
        self.manifest["runs"] = [
            run for run in self.manifest["runs"]
            if run["case_id"] == "seed_index" or run["case_id"] in allowed
        ]
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("inventory" in error for error in report["errors"]), report["errors"])
        self.assertTrue(any("expected exactly 1" in error for error in report["errors"]), report["errors"])

        self.manifest = copy.deepcopy(self.baseline)
        self.manifest["declared_case_ids"].append("unknown_case")
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("inventory" in error for error in report["errors"]), report["errors"])

        self.manifest = copy.deepcopy(self.baseline)
        self.manifest["declared_case_ids"].append(self.manifest["declared_case_ids"][0])
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("not unique" in error for error in report["errors"]), report["errors"])

    def test_false_missing_and_wrong_type_outcomes_fail_closed(self) -> None:
        self._assert_valid_baseline()
        original = copy.deepcopy(self.manifest)
        for mutation in ("false", "missing", "numeric_exit", "nonfinite_elapsed", "reviewed_exit_tamper"):
            self.manifest = copy.deepcopy(original)
            target_case = "documents_invalid_state_json"
            run = next(row for row in self.manifest["runs"] if row["case_id"] == target_case and row["role"] == "go")
            if mutation == "false":
                run["success"] = False
            elif mutation == "missing":
                run.pop("success")
            elif mutation == "numeric_exit":
                run["expected_exit_code"] = True
            elif mutation == "nonfinite_elapsed":
                run["elapsed_seconds"] = 10**1000
            else:
                run["exit_code"] = 0
                run["expected_exit_code"] = 0
            self._write_manifest()
            exit_code, report = self._invoke()
            self.assertNotEqual(exit_code, 0)
            if mutation == "numeric_exit":
                reason = "exit codes must be integers"
            elif mutation == "nonfinite_elapsed":
                reason = "elapsed_seconds must be a non-negative number"
            elif mutation == "reviewed_exit_tamper":
                reason = "expected exit does not match the reviewed base case"
            else:
                reason = "success outcome"
            self.assertTrue(any(reason in error for error in report["errors"]), report["errors"])

    def test_binary_platform_and_revision_identity_mutations_fail(self) -> None:
        self._assert_valid_baseline()
        original = copy.deepcopy(self.manifest)

        self.manifest = copy.deepcopy(original)
        rust = self.manifest["rust_binary"]
        go = self.manifest["go_binary"]
        rust.update({"path": go["path"], "sha256": go["sha256"], "size_bytes": go["size_bytes"]})
        for row in self.manifest["runs"]:
            if row["role"] == "rust":
                row["binary_path"] = go["path"]
                row["binary_sha256"] = go["sha256"]
                row["argv"][0] = go["path"]
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("must be distinct" in error for error in report["errors"]), report["errors"])

        self.manifest = copy.deepcopy(original)
        row = next(row for row in self.manifest["runs"] if row["role"] == "rust" and row["case_id"] == "aggregate_populated_json")
        row["binary_sha256"] = self.manifest["go_binary"]["sha256"]
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("binary SHA-256 identity" in error for error in report["errors"]), report["errors"])

        self.manifest = copy.deepcopy(original)
        self.manifest["source_commit"] = self.manifest["source_commit"][:12]
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("full lowercase 40-character" in error for error in report["errors"]), report["errors"])

        self.manifest = copy.deepcopy(original)
        self.manifest["platform"] = {"os": "not-a-native-os", "architecture": "invalid"}
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("capture platform" in error for error in report["errors"]), report["errors"])

    def test_traversal_and_raw_size_hash_tampering_are_rejected(self) -> None:
        self._assert_valid_baseline()
        original = copy.deepcopy(self.manifest)
        outside = self.root / "outside-sentinel.bin"
        sentinel = b"outside sentinel stays byte-exact"
        outside.write_bytes(sentinel)

        self.manifest = copy.deepcopy(original)
        run = next(row for row in self.manifest["runs"] if row["role"] == "go" and row["case_id"] == "documents_populated_json")
        run["stdout_file"] = "../outside-sentinel.bin"
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("traverses outside" in error for error in report["errors"]), report["errors"])
        self.assertEqual(outside.read_bytes(), sentinel)

        self.manifest = copy.deepcopy(original)
        run = next(row for row in self.manifest["runs"] if row["role"] == "go" and row["case_id"] == "documents_populated_json")
        output = self.capture / run["stdout_file"]
        output.write_bytes(output.read_bytes() + b"tamper")
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("raw stdout size mismatch" in error for error in report["errors"]), report["errors"])
        self.assertTrue(any("raw stdout hash mismatch" in error for error in report["errors"]), report["errors"])

    def test_duration_arguments_are_bound_to_all_54_reviewed_oracle_rows(self) -> None:
        self._assert_valid_baseline()
        empty_row = next(row for row in self.manifest["runs"]
                         if row["case_id"] == "documents_state_empty_json" and row["role"] == "go")
        self.assertEqual(empty_row["argv"][-2:], ["--state", ""])
        self.assertEqual(empty_row["expected_exit_code"], 0)
        self.assertEqual(len(compare.DURATION_CASES), 54)
        self.assertTrue(
            {"1.sx", "9223372036854775808ns1x", "1\n", "1\x7f", "1\u2028", "+0", "-0", "1.s"}
            <= {value for _case_id, _mode, value, _exit in compare.DURATION_CASES}
        )
        self.assertTrue(
            {
                "provider_retry_one_probe_json",
                "documents_html_unicode_json",
                "documents_lifecycle_invalid_utf8_json",
                "documents_lifecycle_valid_replacement_json",
                "documents_state_empty_json",
                "documents_state_empty_text",
            }
            <= set(compare.BASE_CASE_IDS)
        )
        original = copy.deepcopy(self.manifest)
        empty_row["argv"][-1] = "empty"
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("reviewed case inputs" in error for error in report["errors"]), report)
        case_id = "timeout_parse_overflow_then_unknown_unit_text"
        self.manifest = copy.deepcopy(original)
        row = next(row for row in self.manifest["runs"] if row["role"] == "go" and row["case_id"] == case_id)
        row["duration_input"] = "1ms"
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("duration input/mode" in error for error in report["errors"]), report["errors"])

        self.manifest = copy.deepcopy(original)
        row = next(row for row in self.manifest["runs"] if row["role"] == "rust" and row["case_id"] == case_id)
        row["argv"][-1] = "1ms"
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("argv does not execute" in error for error in report["errors"]), report["errors"])

        self.manifest = copy.deepcopy(original)
        row = next(row for row in self.manifest["runs"] if row["role"] == "rust" and row["case_id"] == case_id)
        row["world_key"] = []
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("references an unknown disposable world" in error for error in report["errors"]), report["errors"])


    def test_symlink_artifact_is_rejected_without_following_it(self) -> None:
        self._assert_valid_baseline()
        outside = self.root / "outside-sentinel.bin"
        sentinel = b"outside sentinel stays byte-exact"
        outside.write_bytes(sentinel)
        try:
            (self.capture / "outside-link.stdout.bin").symlink_to(outside)
        except (OSError, NotImplementedError) as exc:
            self.skipTest(f"native symlink creation is unavailable: {exc}")
        run = next(row for row in self.manifest["runs"] if row["role"] == "rust" and row["case_id"] == "documents_populated_json")
        run["stdout_file"] = "outside-link.stdout.bin"
        self._write_manifest()
        exit_code, report = self._invoke()
        self.assertNotEqual(exit_code, 0)
        self.assertTrue(any("non-symlink" in error for error in report["errors"]), report["errors"])
        self.assertEqual(outside.read_bytes(), sentinel)


if __name__ == "__main__":
    unittest.main()
