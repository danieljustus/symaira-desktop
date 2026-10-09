#!/usr/bin/env python3
"""Fail-closed comparison of retained Go/Rust index-status observations.

The reviewed case inventory lives in this source file, not in a mutable capture
manifest. A successful comparison proves byte-level differential parity for a
recorded diagnostic replay; it does not by itself certify a clean production
build or native execution on another operating system.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
import platform
import re
import stat
import subprocess
import sys
from pathlib import Path
from typing import Any

BASE_CASE_IDS = (
    "aggregate_empty_default_json",
    "aggregate_empty_default_text",
    "aggregate_empty_with_vault_json",
    "aggregate_empty_with_vault_text",
    "documents_empty_json",
    "documents_empty_text",
    "documents_state_empty_json",
    "documents_state_empty_text",
    "aggregate_populated_json",
    "aggregate_populated_text",
    "documents_populated_json",
    "documents_populated_text",
    "documents_state_queued_json",
    "documents_state_indexing_json",
    "documents_state_indexed_json",
    "documents_state_failed_json",
    "documents_state_encrypted_json",
    "documents_state_unsupported_json",
    "aggregate_provider_available_json",
    "aggregate_provider_available_text",
    "provider_retry_one_probe_json",
    "documents_html_unicode_json",
    "documents_lifecycle_invalid_utf8_json",
    "documents_lifecycle_valid_replacement_json",
    "documents_invalid_state_json",
    "documents_missing_vault_json",
    "aggregate_missing_vault_json",
    "timeout_negative_json",
    "timeout_zero_delayed_local_provider_json",
    "timeout_blocked_local_provider_json",
    "aggregate_invalid_retrieval_db_json",
)
BASE_CASE_EXPECTED_EXIT_CODES = {
    case_id: 1 if case_id in {
        "documents_state_empty_json",
        "documents_state_empty_text",
        "documents_invalid_state_json",
        "documents_missing_vault_json",
        "aggregate_missing_vault_json",
        "timeout_negative_json",
        "timeout_blocked_local_provider_json",
        "aggregate_invalid_retrieval_db_json",
    } else 0
    for case_id in BASE_CASE_IDS
}
DURATION_INPUTS = (
    ("invalid_text", "bad", 1),
    ("unitless_number", "1", 1),
    ("unknown_unit", "1foo", 1),
    ("whole_unit_match", "1.sx", 1),
    ("duplicate_decimal", "1..s", 1),
    ("empty", "", 1),
    ("sign_without_value", "+", 1),
    ("negative_duration", "-1ms", 1),
    ("positive_overflow_ns", "9223372036854775808ns", 1),
    ("unicode_suffix", "1é", 1),
    ("newline_control", "1\n", 1),
    ("delete_control", "1\x7f", 1),
    ("long_numeric_then_bad_unit", "999999999999999999999bad", 1),
    ("overflow_then_unknown_unit", "9223372036854775808ns1x", 1),
    ("trailing_punctuation", "1ms!", 1),
    ("signed_compound", "1h+2m", 1),
    ("exponent_notation", "1e3s", 1),
    ("minimum_signed_ns", "-9223372036854775808ns", 1),
    ("backslash_escape", "1\\s", 1),
    ("quote_byte", '1"s', 1),
    ("html_sensitive_bytes", "1<&>", 1),
    ("unicode_line_separator", "1\u2028", 1),
    ("positive_zero", "+0", 0),
    ("negative_zero", "-0", 0),
    ("one_dot_seconds", "1.s", 0),
    ("maximum_signed_ns", "9223372036854775807ns", 0),
    ("subnanosecond_fraction", "0.000000000000000000000000000000000000000000000001s", 0),
)
DURATION_CASES = tuple(
    (f"timeout_parse_{name}_{mode}", mode, value, expected_exit)
    for name, value, expected_exit in DURATION_INPUTS
    for mode in ("json", "text")
)
DURATION_CASE_BY_ID = {
    case_id: (mode, value, expected_exit)
    for case_id, mode, value, expected_exit in DURATION_CASES
}
REQUIRED_CASE_IDS = BASE_CASE_IDS + tuple(case_id for case_id, _, _, _ in DURATION_CASES)
CANONICAL_ORACLE_COMMIT = "191100811b7e61a0b43d21bd80963281c4c9cf8c"
RUST_DIAGNOSTIC_BINARY_SOURCE = "82bbe5074bf02ca198846ed7f95f3587fe74565c"
FULL_REVISION = re.compile(r"^[0-9a-f]{40}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
EMPTY_CASES = {
    "aggregate_empty_default_json",
    "aggregate_empty_default_text",
    "aggregate_empty_with_vault_json",
    "aggregate_empty_with_vault_text",
    "documents_empty_json",
    "documents_empty_text",
    "documents_state_empty_json",
    "documents_state_empty_text",
}
POINTER_FIELD = re.compile(rb"(VaultDocumentCount:)0x[0-9a-fA-F]+")
WORKTREE = Path(__file__).resolve().parents[3]


def sha256(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def inventory_sha256() -> str:
    inventory = {
        "case_ids": list(REQUIRED_CASE_IDS),
        "base_cases": [
            {"case_id": case_id, "expected_exit_code": BASE_CASE_EXPECTED_EXIT_CODES[case_id]}
            for case_id in BASE_CASE_IDS
        ],
        "duration_cases": [
            {"case_id": case_id, "mode": mode, "input": value, "expected_exit_code": expected_exit}
            for case_id, mode, value, expected_exit in DURATION_CASES
        ],
    }
    encoded = json.dumps(inventory, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("ascii")
    return sha256(encoded)


def normalize(data: bytes, replacements: list[tuple[bytes, bytes]]) -> bytes:
    normalized = data
    for original, replacement in replacements:
        if original:
            normalized = normalized.replace(original, replacement)
    return POINTER_FIELD.sub(rb"\1<GO_POINTER>", normalized)


def _unique_object(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON object key: {key}")
        result[key] = value
    return result


def _reject_json_constant(value: str) -> None:
    raise ValueError(f"non-standard JSON numeric constant: {value}")


def _is_int(value: Any) -> bool:
    return type(value) is int


def _is_number(value: Any) -> bool:
    return type(value) in (int, float)


def _number_or_none(value: Any) -> float | None:
    if type(value) in (int, float):
        try:
            result = float(value)
        except OverflowError:
            return None
        return result if math.isfinite(result) else None
    return None


def _platform_identity() -> dict[str, str]:
    system = platform.system().lower()
    if system == "darwin":
        system = "macos"
    elif system.startswith("win"):
        system = "windows"
    elif system.startswith("linux"):
        system = "linux"
    return {"os": system, "architecture": platform.machine().lower()}


def _git_output(*args: str) -> subprocess.CompletedProcess[bytes]:
    return subprocess.run(
        ["git", "-C", str(WORKTREE), *args],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=5,
        check=False,
    )


def _safe_capture_file(root: Path, value: Any, errors: list[str], description: str) -> Path | None:
    if not isinstance(value, str) or not value or "\\" in value:
        errors.append(f"{description} path must be a non-empty relative POSIX path")
        return None
    relative = Path(value)
    if relative.is_absolute() or any(part in ("", ".", "..") for part in relative.parts):
        errors.append(f"{description} path is absolute or traverses outside the capture")
        return None
    if len(relative.parts) != 1 or re.match(r"^[A-Za-z]:", value):
        errors.append(f"{description} path must name a direct child of the capture")
        return None
    candidate = root / relative
    try:
        metadata = candidate.lstat()
        resolved = candidate.resolve(strict=True)
    except OSError as exc:
        errors.append(f"{description} artifact is missing or unreadable: {exc}")
        return None
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        errors.append(f"{description} artifact is not a regular non-symlink file")
        return None
    if os.path.normcase(str(resolved.parent)) != os.path.normcase(str(root)):
        errors.append(f"{description} artifact resolves outside the capture")
        return None
    return resolved


def _checked_file_identity(
    value: Any,
    *,
    description: str,
    errors: list[str],
) -> tuple[Path, str, int] | None:
    if not isinstance(value, dict):
        errors.append(f"{description} identity is missing or not an object")
        return None
    path_value = value.get("path")
    digest = value.get("sha256")
    size = value.get("size_bytes")
    if not isinstance(path_value, str) or not Path(path_value).is_absolute():
        errors.append(f"{description} path must be absolute")
        return None
    if not isinstance(digest, str) or not SHA256.fullmatch(digest):
        errors.append(f"{description} SHA-256 identity is malformed")
        return None
    if type(size) is not int or size < 0:
        errors.append(f"{description} size_bytes must be a non-negative integer")
        return None
    try:
        source = Path(path_value)
        resolved = source.resolve(strict=True)
        metadata = source.lstat()
    except OSError as exc:
        errors.append(f"{description} binary is missing or unreadable: {exc}")
        return None
    if stat.S_ISLNK(metadata.st_mode) or not stat.S_ISREG(metadata.st_mode):
        errors.append(f"{description} binary is not a regular non-symlink file")
        return None
    if os.path.normcase(str(source)) != os.path.normcase(str(resolved)):
        errors.append(f"{description} binary path resolves through a symlink")
        return None
    try:
        actual_size = resolved.stat().st_size
        hasher = hashlib.sha256()
        with resolved.open("rb") as stream:
            for block in iter(lambda: stream.read(1024 * 1024), b""):
                hasher.update(block)
        actual_digest = hasher.hexdigest()
    except OSError as exc:
        errors.append(f"{description} binary cannot be verified: {exc}")
        return None
    if actual_size != size:
        errors.append(f"{description} binary size mismatch")
    if actual_digest != digest:
        errors.append(f"{description} binary SHA-256 mismatch")
    return resolved, digest, int(size)


def _validate_revision(value: Any, description: str, errors: list[str], *, required: bool = True) -> None:
    if value is None and not required:
        return
    if not isinstance(value, str) or not FULL_REVISION.fullmatch(value):
        errors.append(f"{description} must be a full lowercase 40-character Git revision")
        return
    result = _git_output("cat-file", "-e", f"{value}^{{commit}}")
    if result.returncode != 0:
        errors.append(f"{description} does not resolve to a commit in the source repository")


def _report_error(errors: list[str], message: str) -> None:
    errors.append(message)


def compare_manifest(manifest_path: Path) -> dict[str, Any]:
    errors: list[str] = []
    mismatches: list[dict[str, Any]] = []
    compared = 0
    manifest_resolved: Path | None = None
    manifest: Any = None
    try:
        manifest_resolved = manifest_path.resolve(strict=True)
        raw_manifest = manifest_resolved.read_bytes()
        manifest = json.loads(raw_manifest, object_pairs_hook=_unique_object, parse_constant=_reject_json_constant)
    except (OSError, UnicodeDecodeError, json.JSONDecodeError, ValueError) as exc:
        _report_error(errors, f"manifest cannot be read as unique-key UTF-8 JSON: {exc}")

    output_dir = manifest_resolved.parent if manifest_resolved else manifest_path.parent.resolve()
    if not isinstance(manifest, dict):
        _report_error(errors, "manifest root must be a JSON object")
        manifest = {}

    schema_version = manifest.get("schema_version")
    if not _is_int(schema_version) or schema_version != 2:
        errors.append("schema_version must be integer 2")

    declared = manifest.get("declared_case_ids")
    if not isinstance(declared, list) or any(not isinstance(case, str) for case in declared):
        errors.append("declared_case_ids must be a list of strings")
        declared = []
    if len(set(declared)) != len(declared):
        errors.append("declared case IDs are not unique")
    if declared != list(REQUIRED_CASE_IDS):
        errors.append("declared case inventory does not exactly match the reviewed required inventory")
    if manifest.get("case_inventory_sha256") != inventory_sha256():
        errors.append("case inventory digest does not match the reviewed required inventory")

    expected_roles = ["go", "rust"]
    roles = manifest.get("roles")
    if roles != expected_roles:
        errors.append("roles must be exactly ['go', 'rust'] in that order")
    role_binaries: dict[str, tuple[Path, str, int]] = {}
    for role in expected_roles:
        identity = _checked_file_identity(
            manifest.get(f"{role}_binary"), description=f"{role} binary", errors=errors
        )
        if identity is not None:
            role_binaries[role] = identity
        binary_manifest = manifest.get(f"{role}_binary")
        if isinstance(binary_manifest, dict):
            if binary_manifest.get("role") != role:
                errors.append(f"{role} binary role identity is swapped or missing")
            build_revision = binary_manifest.get("build_source_commit")
            _validate_revision(build_revision, f"{role} binary build_source_commit", errors)
            provenance = binary_manifest.get("build_provenance")
            if not isinstance(provenance, str) or not provenance:
                errors.append(f"{role} binary build_provenance is missing")
            if role == "rust" and build_revision != RUST_DIAGNOSTIC_BINARY_SOURCE:
                errors.append("Rust diagnostic binary is not bound to the independently recorded parent source revision")
    if "go" in role_binaries and "rust" in role_binaries:
        go_path, go_hash, _ = role_binaries["go"]
        rust_path, rust_hash, _ = role_binaries["rust"]
        if go_path == rust_path or go_hash == rust_hash:
            errors.append("Go and Rust binary identities must be distinct")
        if isinstance(manifest.get("go_binary"), dict) and manifest["go_binary"].get("build_source_commit") != CANONICAL_ORACLE_COMMIT:
            errors.append("Go binary is not bound to the reviewed canonical oracle revision")

    source_commit = manifest.get("source_commit")
    _validate_revision(source_commit, "source_commit", errors)
    source_worktree = manifest.get("source_worktree")
    if not isinstance(source_worktree, str) or not Path(source_worktree).is_absolute():
        errors.append("source_worktree must be an absolute path")
    elif os.path.normcase(str(Path(source_worktree).resolve())) != os.path.normcase(str(WORKTREE)):
        errors.append("source_worktree does not identify this comparator's repository")
    else:
        head = _git_output("rev-parse", "HEAD")
        if head.returncode != 0 or head.stdout.decode("ascii", errors="replace").strip() != source_commit:
            errors.append("source_commit does not match the source worktree HEAD")
    canonical = manifest.get("canonical_oracle_commit")
    if canonical != CANONICAL_ORACLE_COMMIT:
        errors.append("canonical_oracle_commit does not match the reviewed Go oracle revision")
    _validate_revision(canonical, "canonical_oracle_commit", errors)
    if manifest.get("canonical_oracle_is_ancestor") is not True:
        errors.append("canonical_oracle_is_ancestor must be the boolean true")
    elif isinstance(source_commit, str) and FULL_REVISION.fullmatch(source_commit):
        ancestry = _git_output("merge-base", "--is-ancestor", CANONICAL_ORACLE_COMMIT, source_commit)
        if ancestry.returncode != 0:
            errors.append("canonical Go oracle revision is not an ancestor of source_commit")

    if manifest.get("evidence_class") != "diagnostic-replay":
        errors.append("evidence_class must explicitly identify this as diagnostic-replay")

    captured_platform = manifest.get("platform")
    current_platform = _platform_identity()
    if captured_platform != current_platform:
        errors.append("capture platform does not match the native comparator platform")

    executed = manifest.get("executed_case_ids")
    expected_executed = [case_id for case_id in REQUIRED_CASE_IDS] + [f"rust:{case_id}" for case_id in REQUIRED_CASE_IDS]
    if not isinstance(executed, list) or any(not isinstance(case, str) for case in executed):
        errors.append("executed_case_ids must be a list of strings")
        executed = []
    if len(set(executed)) != len(executed):
        errors.append("executed case IDs contain duplicates")
    if executed != expected_executed:
        errors.append("executed case IDs do not exactly match one Go/Rust run per required case")

    source_status = manifest.get("source_tree_status")
    if source_status != []:
        errors.append("source_tree_status must record an empty clean-tree status")

    worlds = manifest.get("worlds")
    if not isinstance(worlds, dict):
        errors.append("worlds must be an object")
        worlds = {}
    world_paths: dict[str, dict[str, Path]] = {}
    for role in expected_roles:
        for group in ("empty", "populated"):
            key = f"{role}-{group}"
            raw_world = worlds.get(key)
            if not isinstance(raw_world, dict):
                errors.append(f"missing disposable world identity {key}")
                continue
            paths_for_world: dict[str, Path] = {}
            root_value = raw_world.get("world")
            if not isinstance(root_value, str) or not Path(root_value).is_absolute():
                errors.append(f"{key} world path must be absolute")
                continue
            try:
                root = Path(root_value)
                root_lstat = root.lstat()
                root_resolved = root.resolve(strict=True)
                if stat.S_ISLNK(root_lstat.st_mode) or not root_resolved.is_dir():
                    raise OSError("world root is not a regular directory")
                paths_for_world["world"] = root_resolved
            except OSError as exc:
                errors.append(f"{key} world root is missing or unsafe: {exc}")
                continue
            for field in ("home", "xdg_data", "xdg_config", "temp", "vault", "appdata", "local_appdata"):
                value = raw_world.get(field)
                if not isinstance(value, str) or not Path(value).is_absolute():
                    errors.append(f"{key} {field} path must be absolute")
                    continue
                candidate = Path(value)
                try:
                    metadata = candidate.lstat()
                    resolved = candidate.resolve(strict=True)
                    if stat.S_ISLNK(metadata.st_mode) or not resolved.is_dir():
                        raise OSError("path is not a regular directory")
                    if os.path.commonpath((os.path.normcase(str(root_resolved)), os.path.normcase(str(resolved)))) != os.path.normcase(str(root_resolved)):
                        raise OSError("path resolves outside its disposable world")
                    paths_for_world[field] = resolved
                except (OSError, ValueError) as exc:
                    errors.append(f"{key} {field} path is missing or unsafe: {exc}")
            world_paths[key] = paths_for_world

    runs = manifest.get("runs")
    if not isinstance(runs, list):
        errors.append("runs must be a list")
        runs = []
    runs_by_key: dict[tuple[str, str], list[dict[str, Any]]] = {}
    used_artifacts: set[str] = set()
    for index, run in enumerate(runs):
        label = f"run[{index}]"
        if not isinstance(run, dict):
            errors.append(f"{label} must be an object")
            continue
        role = run.get("role")
        case_id = run.get("case_id")
        if role not in expected_roles or not isinstance(case_id, str):
            errors.append(f"{label} has unknown or malformed role/case identity")
            continue
        runs_by_key.setdefault((role, case_id), []).append(run)
        if case_id != "seed_index" and case_id not in REQUIRED_CASE_IDS:
            errors.append(f"{role}:{case_id} is not in the reviewed required inventory")
        if case_id == "seed_index" and run.get("status_case") is not False:
            errors.append(f"{role}:seed_index must be explicitly excluded from status cases")
        elif case_id != "seed_index" and run.get("status_case") is not True:
            errors.append(f"{role}:{case_id} must be explicitly marked as a status case")

        identity = role_binaries.get(role)
        if identity is None:
            continue
        binary_path, binary_hash, _ = identity
        recorded_binary_path = run.get("binary_path")
        if not isinstance(recorded_binary_path, str):
            errors.append(f"{role}:{case_id} binary_path is missing")
        else:
            try:
                if Path(recorded_binary_path).resolve(strict=True) != binary_path:
                    errors.append(f"{role}:{case_id} binary path identity is swapped")
            except OSError:
                errors.append(f"{role}:{case_id} binary path is missing")
        if run.get("binary_sha256") != binary_hash:
            errors.append(f"{role}:{case_id} binary SHA-256 identity is swapped or mismatched")
        argv = run.get("argv")
        if not isinstance(argv, list) or not argv or any(not isinstance(arg, str) for arg in argv):
            errors.append(f"{role}:{case_id} argv must be a non-empty list of strings")
        elif Path(argv[0]).resolve() != binary_path:
            errors.append(f"{role}:{case_id} argv executable does not match its binary identity")

        if run.get("platform") != captured_platform:
            errors.append(f"{role}:{case_id} platform identity differs from the capture")
        if run.get("cwd") != source_worktree:
            errors.append(f"{role}:{case_id} cwd does not match source_worktree")
        expected_exit = run.get("expected_exit_code")
        actual_exit = run.get("exit_code")
        if not _is_int(expected_exit) or not _is_int(actual_exit):
            errors.append(f"{role}:{case_id} exit codes must be integers, not booleans or other types")
        elif actual_exit != expected_exit:
            errors.append(f"unexpected exit code for {role}:{case_id}")
        if run.get("timed_out") is not False:
            errors.append(f"{role}:{case_id} timed_out must be the boolean false")
        if run.get("success") is not True:
            errors.append(f"{role}:{case_id} is missing an explicit true success outcome")
        elapsed = run.get("elapsed_seconds")
        elapsed_number = _number_or_none(elapsed)
        if elapsed_number is None or elapsed_number < 0:
            errors.append(f"{role}:{case_id} elapsed_seconds must be a non-negative number")
        if case_id == "timeout_zero_delayed_local_provider_json" and elapsed_number is not None and elapsed_number < 0.2:
            errors.append(f"{role} zero-timeout delayed-provider control did not wait for the local response")
        if case_id == "timeout_blocked_local_provider_json" and elapsed_number is not None and elapsed_number >= 1.5:
            errors.append(f"{role} blocked-provider deadline exceeded the 1.5s cleanup bound")
        if case_id in BASE_CASE_EXPECTED_EXIT_CODES and expected_exit != BASE_CASE_EXPECTED_EXIT_CODES[case_id]:
            errors.append(f"{role}:{case_id} expected exit does not match the reviewed base case")
        if case_id in DURATION_CASE_BY_ID:
            mode, duration_input, duration_exit = DURATION_CASE_BY_ID[case_id]
            if run.get("duration_mode") != mode or run.get("duration_input") != duration_input:
                errors.append(f"{role}:{case_id} duration input/mode does not match the reviewed corpus")
            if expected_exit != duration_exit:
                errors.append(f"{role}:{case_id} expected exit does not match the reviewed duration case")
            world_key_for_duration = run.get("world_key")
            world_manifest = worlds.get(world_key_for_duration) if isinstance(world_key_for_duration, str) else None
            vault_path = world_manifest.get("vault") if isinstance(world_manifest, dict) else None
            expected_argv = [str(binary_path)]
            if mode == "json":
                expected_argv.append("--json")
            if isinstance(vault_path, str):
                expected_argv.extend(["--vault", vault_path, "index", "status", "--documents", "--timeout", duration_input])
                if argv != expected_argv:
                    errors.append(f"{role}:{case_id} argv does not execute the reviewed duration input")
            else:
                errors.append(f"{role}:{case_id} reviewed duration world has no vault path")
        if case_id == "provider_retry_one_probe_json":
            if not _is_int(run.get("provider_probe_count")) or run.get("provider_probe_count") != 1:
                errors.append(f"{role} retry_count=1 control must record exactly one provider probe")
            if run.get("provider_response_codes") != [503]:
                errors.append(f"{role} retry_count=1 control must record only the first HTTP 503 response")

        world_key = run.get("world_key")
        if not isinstance(world_key, str) or world_key not in world_paths:
            errors.append(f"{role}:{case_id} references an unknown disposable world")
        else:
            paths_for_world = world_paths[world_key]
            environment = run.get("environment")
            if not isinstance(environment, dict):
                errors.append(f"{role}:{case_id} environment must be recorded as an object")
                environment = {}
            env_expected = {
                "HOME": "home",
                "XDG_DATA_HOME": "xdg_data",
                "XDG_CONFIG_HOME": "xdg_config",
                "TMPDIR": "temp",
                "TEMP": "temp",
                "TMP": "temp",
                "USERPROFILE": "home",
                "APPDATA": "appdata",
                "LOCALAPPDATA": "local_appdata",
            }
            for env_key, world_field in env_expected.items():
                expected_path = paths_for_world.get(world_field)
                actual_path = environment.get(env_key)
                if expected_path is not None and actual_path != str(expected_path):
                    errors.append(f"{role}:{case_id} {env_key} is not the explicit isolated {world_field}")
            for env_key, required in (("TZ", "UTC"), ("LANG", "C.UTF-8"), ("LC_ALL", "C.UTF-8")):
                if environment.get(env_key) != required:
                    errors.append(f"{role}:{case_id} {env_key} is not explicitly isolated")
            if not isinstance(environment.get("PATH"), str) or not environment.get("PATH"):
                errors.append(f"{role}:{case_id} PATH is missing from the explicit environment")
            for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
                if environment.get(key) != "":
                    errors.append(f"{role}:{case_id} {key} must be explicitly disabled")
            if environment.get("NO_PROXY") != "*" or environment.get("no_proxy") != "*":
                errors.append(f"{role}:{case_id} NO_PROXY must explicitly keep probes local")

        for stream in ("stdout", "stderr"):
            artifact_key = f"{stream}_file"
            path = _safe_capture_file(output_dir, run.get(artifact_key), errors, f"{role}:{case_id} {stream}")
            size_key = f"{stream}_size_bytes"
            digest_key = f"{stream}_sha256"
            expected_size = run.get(size_key)
            expected_hash = run.get(digest_key)
            if not _is_int(expected_size) or expected_size < 0:
                errors.append(f"{role}:{case_id} {size_key} must be a non-negative integer")
            if not isinstance(expected_hash, str) or not SHA256.fullmatch(expected_hash):
                errors.append(f"{role}:{case_id} {digest_key} is malformed")
            if path is not None and _is_int(expected_size) and expected_size >= 0 and isinstance(expected_hash, str) and SHA256.fullmatch(expected_hash):
                try:
                    raw = path.read_bytes()
                    if len(raw) != expected_size:
                        errors.append(f"raw {stream} size mismatch for {role}:{case_id}")
                    if sha256(raw) != expected_hash:
                        errors.append(f"raw {stream} hash mismatch for {role}:{case_id}")
                except OSError as exc:
                    errors.append(f"raw {stream} unreadable for {role}:{case_id}: {exc}")
            if isinstance(run.get(artifact_key), str):
                artifact_name = run[artifact_key]
                if artifact_name in used_artifacts:
                    errors.append(f"raw output artifact is reused: {artifact_name}")
                used_artifacts.add(artifact_name)

    for role in expected_roles:
        expected_keys = [(role, case_id) for case_id in REQUIRED_CASE_IDS]
        if role == "go":
            seed_rows = runs_by_key.get(("go", "seed_index"), [])
            if len(seed_rows) != 2 or {row.get("world_key") for row in seed_rows} != {"go-populated", "rust-populated"}:
                errors.append("expected one retained Go seed execution for each populated disposable world")
            expected_keys.append(("go", "seed_index"))
        elif runs_by_key.get(("rust", "seed_index")):
            errors.append("Rust status runs must use the independently recorded Go-seeded worlds")
        for key in expected_keys:
            count = len(runs_by_key.get(key, []))
            required_count = 2 if key == ("go", "seed_index") else 1
            if count != required_count:
                errors.append(f"expected exactly {required_count} raw {key[0]}:{key[1]} execution(s), found {count}")
        unknown = [key for key in runs_by_key if key[0] == role and key not in expected_keys]
        for key in unknown:
            errors.append(f"unexpected extra {role}:{key[1]} execution")

    replacements_by_group: dict[str, list[tuple[bytes, bytes]]] = {}
    for group in ("empty", "populated"):
        replacements: list[tuple[bytes, bytes]] = []
        for role in expected_roles:
            world = worlds.get(f"{role}-{group}") if isinstance(worlds, dict) else None
            root = world.get("world") if isinstance(world, dict) else None
            if isinstance(root, str):
                replacements.append((root.encode("utf-8"), f"<WORLD_{group.upper()}>".encode("ascii")))
        replacements_by_group[group] = sorted(replacements, key=lambda item: len(item[0]), reverse=True)

    for case_id in REQUIRED_CASE_IDS:
        case_runs = {role: runs_by_key.get((role, case_id), []) for role in expected_roles}
        if any(len(case_runs[role]) != 1 for role in expected_roles):
            continue
        normalized_runs: dict[str, dict[str, Any]] = {}
        for role in expected_roles:
            run = case_runs[role][0]
            normalized: dict[str, Any] = {"exit_code": run.get("exit_code")}
            for stream in ("stdout", "stderr"):
                artifact = _safe_capture_file(output_dir, run.get(f"{stream}_file"), [], f"{role}:{case_id} {stream}")
                if artifact is None:
                    normalized[stream] = b"<INVALID ARTIFACT>"
                    continue
                try:
                    raw = artifact.read_bytes()
                except OSError:
                    normalized[stream] = b"<UNREADABLE ARTIFACT>"
                    continue
                group = "empty" if case_id in EMPTY_CASES else "populated"
                normalized[stream] = normalize(raw, replacements_by_group[group])
            normalized_runs[role] = normalized
        compared += 1
        if normalized_runs.get("go") != normalized_runs.get("rust"):
            mismatches.append(
                {
                    "case_id": case_id,
                    "go_stdout_sha256": sha256(normalized_runs["go"]["stdout"]),
                    "rust_stdout_sha256": sha256(normalized_runs["rust"]["stdout"]),
                    "go_stderr_sha256": sha256(normalized_runs["go"]["stderr"]),
                    "rust_stderr_sha256": sha256(normalized_runs["rust"]["stderr"]),
                    "go_exit_code": normalized_runs["go"]["exit_code"],
                    "rust_exit_code": normalized_runs["rust"]["exit_code"],
                }
            )

    passed = not errors and not mismatches and compared == len(REQUIRED_CASE_IDS)
    return {
        "manifest": str(manifest_resolved or manifest_path),
        "pass": passed,
        "clean_acceptance": False,
        "evidence_class": manifest.get("evidence_class"),
        "case_inventory_sha256": inventory_sha256(),
        "required_case_ids": len(REQUIRED_CASE_IDS),
        "declared_case_ids": len(declared),
        "executed_case_ids": len(executed),
        "raw_runs_including_seed_commands": len(runs),
        "compared_cases": compared,
        "normalization": "only the exact four recorded disposable world roots and Go VaultDocumentCount pointer values; raw artifacts are unchanged",
        "errors": errors,
        "mismatches": mismatches,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()

    report = compare_manifest(args.manifest)
    rendered = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if args.report:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(rendered, encoding="utf-8")
    print(rendered, end="")
    return 0 if report["pass"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
