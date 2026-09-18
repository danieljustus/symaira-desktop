#!/usr/bin/env python3
"""Validate the redacted public VALUE-001 capture of candidate 5c5e98c5.

The private raw capture stays outside the repository. Pass it with --raw to
re-prove that the public artifact is exactly its reviewed redaction, and that
every other byte is unchanged.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import value001  # noqa: E402
from validate_value001_retained import check_summary  # noqa: E402

EXPECTED_SHA256 = "a67779054c210d88212bf41fd934e4127ed0b5807c8610c1fc70934359619e5d"
EXPECTED_RAW_SHA256 = "40267a5c47cf67579a708e9b0e61425960872d04a1f08796994ef157fd758a63"
EXPECTED_HEAD = "5c5e98c5aab2df54bb2c47ad51fe3d2d8e71f23b"
EXPECTED_GO = "745c08e8144971c61133c5d0e5d61c7ce405aad2"
EXPECTED_CI_RUN = "35354149053"
ARTIFACT = Path(__file__).resolve().parents[2] / "docs/rust-port/results/value001-5c5e98c5.json"
REPO_PLACEHOLDER = "<redacted-repository>"
TEMP_PLACEHOLDER = "<redacted-temporary-directory>"
REDACTED_PATHS = {
    "$.repository.root",
    "$.binaries.go.path",
    "$.binaries.go.build_command",
    "$.binaries.rust.path",
    "$.index_preparation.go.command",
    "$.index_preparation.rust.command",
    "$.contracts[1].command",
    "$.contracts[2].command",
    "$.contracts[3].command",
}


def derivation_replacements(raw: dict) -> list[tuple[str, str]]:
    """Private absolute prefixes the reviewed derivation replaces, longest first.

    The Go oracle binary lives in the run's build root, and the harness temporary
    root (vaults, sidecars) sits below it, so both collapse into the temporary
    placeholder; only the repository worktree keeps its own placeholder.
    """
    build_root = str(Path(raw["binaries"]["go"]["path"]).parent.parent)
    text = json.dumps(raw)
    temp_roots = sorted(
        set(re.findall(re.escape(build_root) + r"/runtime/tmp/symdesk-value001-[A-Za-z0-9_]+", text)),
        key=len,
        reverse=True,
    )
    replacements = [(root, TEMP_PLACEHOLDER) for root in temp_roots]
    replacements.append((build_root, TEMP_PLACEHOLDER))
    replacements.append((raw["repository"]["root"], REPO_PLACEHOLDER))
    return replacements


def compare_derivation(raw: dict, derived: dict) -> None:
    replacements = derivation_replacements(raw)
    changed: set[str] = set()

    def compare(left, right, path):
        if type(left) is not type(right):
            raise ValueError(f"derivation changed type at {path}")
        if isinstance(left, dict):
            if left.keys() != right.keys():
                raise ValueError(f"derivation changed keys at {path}")
            for key in left:
                compare(left[key], right[key], f"{path}.{key}")
        elif isinstance(left, list):
            if len(left) != len(right):
                raise ValueError(f"derivation changed array length at {path}")
            for index, (before, after) in enumerate(zip(left, right)):
                compare(before, after, f"{path}[{index}]")
        elif left != right:
            if path not in REDACTED_PATHS or not isinstance(left, str):
                raise ValueError(f"unapproved derivation change at {path}")
            expected = left
            for old, new in sorted(replacements, key=lambda item: -len(item[0])):
                expected = expected.replace(old, new)
            if right != expected:
                raise ValueError(f"unapproved path transformation at {path}")
            changed.add(path)

    compare(raw, derived, "$")
    if changed != REDACTED_PATHS:
        raise ValueError("derivation changed-path inventory differs from review")


def validate(path: Path, raw_path: Path | None = None) -> dict:
    if path.is_symlink() or not path.is_file():
        raise ValueError("capture must be a regular file")
    data = path.read_bytes()
    result = json.loads(data)
    value001.validate_result(result)
    summation = value001.summation_of(result)
    for name, metric in result["metrics"].items():
        for side in ("go", "rust"):
            check_summary(metric[side], f"{name}.{side}", summation)
        for operation, pair in metric.get("operations", {}).items():
            for side in ("go", "rust"):
                check_summary(pair[side], f"{name}.{operation}.{side}", summation)
    if result.get("schema_version") != value001.SCHEMA_VERSION:
        raise ValueError("capture is not the current schema")
    repo = result["repository"]
    if repo["head"] != EXPECTED_HEAD or repo["status"] or repo["candidate_diff_sha256"] != hashlib.sha256(b"").hexdigest():
        raise ValueError("capture source must be the clean reviewed candidate")
    if result["binaries"]["go"]["source"] != EXPECTED_GO or result["binaries"]["rust"]["source"] != EXPECTED_HEAD:
        raise ValueError("binary source mismatch")
    thresholds = result["thresholds"]
    failure = value001.order_stratified_latency_failure(thresholds["latency_order_regression_intervals"])
    if failure is not None:
        raise ValueError(f"capture does not confirm the latency ceiling: {failure}")
    if thresholds["latency_pass"] is not True or thresholds["contracts_pass"] is not True or thresholds["improvement_pass"] is not True:
        raise ValueError("capture is not a passing approval")
    if result["passed"] is not True or any(c["exit_code"] != 0 or c["stderr"] for c in result["contracts"]):
        raise ValueError("capture must pass contracts and unchanged value gates")
    for private in (b"/Users/", b"/var/folders/", b"/Volumes/"):
        if private in data:
            raise ValueError("private path in the published capture")
    metadata_path = path.with_suffix(".metadata.json")
    if metadata_path.is_symlink() or not metadata_path.is_file():
        raise ValueError("provenance must be a regular file")
    metadata = json.loads(metadata_path.read_bytes())
    if set(metadata.get("redacted_json_paths", [])) != REDACTED_PATHS:
        raise ValueError("redaction inventory differs from review")
    if (
        metadata.get("original_sha256") != EXPECTED_RAW_SHA256
        or metadata.get("redacted_sha256") != EXPECTED_SHA256
        or metadata.get("source_expected_head") != EXPECTED_HEAD
        or metadata.get("candidate") != EXPECTED_HEAD
        or metadata.get("go_oracle") != EXPECTED_GO
        or metadata.get("trusted_ci_run") != EXPECTED_CI_RUN
        or metadata.get("schema_version") != value001.SCHEMA_VERSION
    ):
        raise ValueError("capture provenance differs from independent review")
    if hashlib.sha256(data).hexdigest() != EXPECTED_SHA256:
        raise ValueError("capture differs from the independently reviewed derivation")
    if raw_path is not None:
        if raw_path.is_symlink() or not raw_path.is_file():
            raise ValueError("original capture must be a regular file")
        original = raw_path.read_bytes()
        if hashlib.sha256(original).hexdigest() != EXPECTED_RAW_SHA256:
            raise ValueError("original capture differs from the independent review")
        compare_derivation(json.loads(original), result)
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("artifact", type=Path, nargs="?", default=ARTIFACT)
    parser.add_argument("--raw", type=Path, help="private raw capture to re-derive from")
    args = parser.parse_args()
    try:
        result = validate(args.artifact, args.raw)
    except (OSError, json.JSONDecodeError, KeyError, TypeError, ValueError, value001.HarnessError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        return 1
    raw_note = " and its reviewed derivation proven" if args.raw else ""
    print(f"PASS schema-6 VALUE-001 acceptance {result['repository']['head']}: {args.artifact}{raw_note}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())