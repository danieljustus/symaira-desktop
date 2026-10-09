#!/usr/bin/env python3
"""Compare recorded Go/Rust index-status runs without rewriting raw output."""

from __future__ import annotations

import argparse
import hashlib
import json
import re
from pathlib import Path
from typing import Any

EMPTY_CASES = {
    "aggregate_empty_default_json",
    "aggregate_empty_default_text",
    "aggregate_empty_with_vault_json",
    "aggregate_empty_with_vault_text",
    "documents_empty_json",
    "documents_empty_text",
}
POINTER_FIELD = re.compile(rb"(VaultDocumentCount:)0x[0-9a-fA-F]+")


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def normalize(data: bytes, replacements: list[tuple[bytes, bytes]]) -> bytes:
    normalized = data
    for original, replacement in replacements:
        normalized = normalized.replace(original, replacement)
    return POINTER_FIELD.sub(rb"\1<GO_POINTER>", normalized)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()

    manifest_path = args.manifest.resolve(strict=True)
    output_dir = manifest_path.parent
    manifest: dict[str, Any] = json.loads(manifest_path.read_text(encoding="utf-8"))
    declared = manifest["declared_case_ids"]
    executed = manifest["executed_case_ids"]
    roles = [role for role in ("go", "rust") if manifest.get(f"{role}_binary")]
    if roles != ["go", "rust"]:
        raise SystemExit(f"expected Go and Rust binaries, found roles {roles!r}")
    expected_ids = [
        case_id if role == "go" else f"{role}:{case_id}"
        for role in roles
        for case_id in declared
    ]
    errors: list[str] = []
    if len(set(declared)) != len(declared):
        errors.append("declared case IDs are not unique")
    if sorted(executed) != sorted(expected_ids):
        errors.append("executed case IDs do not exactly match the declared Go/Rust matrix")

    runs_by_key: dict[tuple[str, str], list[dict[str, Any]]] = {}
    for run in manifest["runs"]:
        key = (run["role"], run["case_id"])
        runs_by_key.setdefault(key, []).append(run)
    for role in roles:
        for case_id in declared:
            if len(runs_by_key.get((role, case_id), [])) != 1:
                errors.append(f"expected exactly one raw {role}:{case_id} run")

    worlds = manifest["worlds"]
    replacements_by_group: dict[str, list[tuple[bytes, bytes]]] = {}
    for group in ("empty", "populated"):
        replacements: list[tuple[bytes, bytes]] = []
        for role in roles:
            root = worlds[f"{role}-{group}"]["world"].encode("utf-8")
            replacements.append((root, f"<WORLD_{group.upper()}>".encode("ascii")))
        replacements_by_group[group] = sorted(replacements, key=lambda item: len(item[0]), reverse=True)

    mismatches: list[dict[str, Any]] = []
    compared = 0
    for case_id in declared:
        group = "empty" if case_id in EMPTY_CASES else "populated"
        case_runs = {
            role: runs_by_key.get((role, case_id), [None])[0]
            for role in roles
        }
        if any(run is None for run in case_runs.values()):
            continue
        go_run = case_runs["go"]
        rust_run = case_runs["rust"]
        assert go_run is not None and rust_run is not None
        normalized_runs: dict[str, dict[str, Any]] = {}
        for role in roles:
            run = case_runs[role]
            assert run is not None
            stdout_path = output_dir / run["stdout_file"]
            stderr_path = output_dir / run["stderr_file"]
            stdout = stdout_path.read_bytes()
            stderr = stderr_path.read_bytes()
            if sha256(stdout) != run["stdout_sha256"]:
                errors.append(f"raw stdout hash mismatch for {role}:{case_id}")
            if sha256(stderr) != run["stderr_sha256"]:
                errors.append(f"raw stderr hash mismatch for {role}:{case_id}")
            expected_exit = run.get("expected_exit_code")
            if expected_exit is not None and run["exit_code"] != expected_exit:
                errors.append(f"unexpected exit code for {role}:{case_id}")
            expected_binary = manifest[f"{role}_binary"]["sha256"]
            if run["binary_sha256"] != expected_binary:
                errors.append(f"binary identity mismatch for {role}:{case_id}")
            normalized_runs[role] = {
                "exit_code": run["exit_code"],
                "stdout": normalize(stdout, replacements_by_group[group]),
                "stderr": normalize(stderr, replacements_by_group[group]),
            }
        compared += 1
        go = normalized_runs["go"]
        rust = normalized_runs["rust"]
        if go != rust:
            mismatches.append(
                {
                    "case_id": case_id,
                    "go_stdout_file": go_run["stdout_file"],
                    "rust_stdout_file": rust_run["stdout_file"],
                    "go_stderr_file": go_run["stderr_file"],
                    "rust_stderr_file": rust_run["stderr_file"],
                    "go_exit_code": go["exit_code"],
                    "rust_exit_code": rust["exit_code"],
                    "go_normalized_stdout": go["stdout"].decode("utf-8", errors="replace"),
                    "rust_normalized_stdout": rust["stdout"].decode("utf-8", errors="replace"),
                    "go_normalized_stderr": go["stderr"].decode("utf-8", errors="replace"),
                    "rust_normalized_stderr": rust["stderr"].decode("utf-8", errors="replace"),
                }
            )

    for role in roles:
        timeout_rows = [
            run for run in manifest["runs"]
            if run["role"] == role and run["case_id"].startswith("timeout_")
        ]
        zero = next((run for run in timeout_rows if run["case_id"] == "timeout_zero_delayed_local_provider_json"), None)
        blocked = next((run for run in timeout_rows if run["case_id"] == "timeout_blocked_local_provider_json"), None)
        if zero is None or zero["elapsed_seconds"] < 0.2:
            errors.append(f"{role} zero-timeout delayed-provider control did not wait for the local response")
        if blocked is None or blocked["elapsed_seconds"] >= 1.5:
            errors.append(f"{role} blocked-provider deadline exceeded the 1.5s cleanup bound")

    report = {
        "manifest": str(manifest_path),
        "pass": not errors and not mismatches and compared == len(declared),
        "declared_case_ids": len(declared),
        "executed_case_ids": len(executed),
        "raw_runs_including_seed_commands": len(manifest["runs"]),
        "compared_cases": compared,
        "normalization": "exact recorded role world-root substitutions plus Go text pointer-address field only; raw files remain unchanged",
        "errors": errors,
        "mismatches": mismatches,
    }
    rendered = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.report:
        args.report.write_text(rendered, encoding="utf-8")
    print(rendered, end="")
    return 0 if report["pass"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
