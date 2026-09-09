#!/usr/bin/env python3
"""Validate the separately retained, independently reviewed resumption capture."""
import argparse
import hashlib
import json
import sys
from pathlib import Path

import value001
from validate_value001_retained import check_summary

EXPECTED_SHA256 = "85a1f14ad96740b0f3b77b1e44d22db45637c7789b4c0f76087a465d24843a49"
EXPECTED_RAW_SHA256 = "bb94cefc18f633ae9c894e236019a763a7def57a644db3f2b0004048aa5164d8"
EXPECTED_HEAD = "956bd3e008b3fc08e9e3c01f46a577d2f42b4235"
EXPECTED_GO = "745c08e8144971c61133c5d0e5d61c7ce405aad2"
ARTIFACT = Path(__file__).resolve().parents[2] / "docs/rust-port/results/value001-resume-956bd3e.json"

REDACTED_PATHS = {
    "$.repository.root", "$.binaries.go.path", "$.binaries.go.build_command",
    "$.binaries.rust.path", "$.index_preparation.go.command",
    "$.index_preparation.rust.command", "$.contracts[1].command",
    "$.contracts[2].command", "$.contracts[3].command",
}


def compare_derivation(raw: dict, derived: dict) -> None:
    replacements = {
        raw["repository"]["root"]: "<redacted-repository>",
        str(Path(raw["binaries"]["go"]["path"]).parent): "<redacted-temporary-directory>",
    }
    changed = set()

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
            for old, new in sorted(replacements.items(), key=lambda item: -len(item[0])):
                expected = expected.replace(old, new)
            if right != expected:
                raise ValueError(f"unapproved path transformation at {path}")
            changed.add(path)

    compare(raw, derived, "$")
    if changed != REDACTED_PATHS:
        raise ValueError("derivation changed-path inventory differs from review")


def validate(path: Path, raw_path: Path | None = None) -> None:
    if path.is_symlink() or not path.is_file():
        raise ValueError("capture must be a regular file")
    data = path.read_bytes()
    result = json.loads(data)
    value001.validate_result(result)
    for name, metric in result["metrics"].items():
        for side in ("go", "rust"):
            check_summary(metric[side], f"{name}.{side}")
        for operation, pair in metric.get("operations", {}).items():
            for side in ("go", "rust"):
                check_summary(pair[side], f"{name}.{operation}.{side}")
    repo = result["repository"]
    if repo["head"] != EXPECTED_HEAD or repo["status"] or repo["candidate_diff_sha256"] != hashlib.sha256(b"").hexdigest():
        raise ValueError("capture source must be the clean reviewed candidate")
    if result["binaries"]["go"]["source"] != EXPECTED_GO or result["binaries"]["rust"]["source"] != EXPECTED_HEAD:
        raise ValueError("binary source mismatch")
    if result["passed"] is not True or any(c["exit_code"] != 0 or c["stderr"] for c in result["contracts"]):
        raise ValueError("capture must pass contracts and unchanged value gates")
    if b"/Users/" in data or b"/var/folders/" in data:
        raise ValueError("private path in retained capture")
    metadata_path = path.with_suffix(".metadata.json")
    if metadata_path.is_symlink() or not metadata_path.is_file():
        raise ValueError("provenance must be a regular file")
    metadata = json.loads(metadata_path.read_bytes())
    if set(metadata.get("redacted_json_paths", [])) != REDACTED_PATHS:
        raise ValueError("redaction inventory differs from review")
    if (metadata.get("original_sha256") != EXPECTED_RAW_SHA256
            or metadata.get("redacted_sha256") != EXPECTED_SHA256
            or metadata.get("source_expected_head") != EXPECTED_HEAD
            or metadata.get("go_oracle") != EXPECTED_GO
            or metadata.get("trusted_ci_run") != "34254766964"):
        raise ValueError("capture provenance differs from independent review")
    if hashlib.sha256(data).hexdigest() != EXPECTED_SHA256:
        raise ValueError("capture differs from independently reviewed raw-derived evidence")
    if raw_path is not None:
        if raw_path.is_symlink() or not raw_path.is_file():
            raise ValueError("original capture must be a regular file")
        original = raw_path.read_bytes()
        if hashlib.sha256(original).hexdigest() != EXPECTED_RAW_SHA256:
            raise ValueError("original capture differs from independent review")
        compare_derivation(json.loads(original), result)
    # A genuine historical capture is not necessarily a passing approval.
    # Keep provenance checks above, then apply every current operation gate.
    regressions = value001.latency_regressions(result["metrics"])
    failures = [name for name, regression in regressions.items() if regression > 0.10]
    if failures:
        raise ValueError("required latency operations exceed 10% regression: " + ", ".join(failures))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("artifact", type=Path, nargs="?", default=ARTIFACT)
    parser.add_argument("--raw", type=Path, help="Private original for local recursive derivation verification; never required in public CI")
    args = parser.parse_args()
    try:
        validate(args.artifact, args.raw)
    except (ValueError, KeyError, OSError, value001.HarnessError) as error:
        print(f"FAIL {error}", file=sys.stderr)
        raise SystemExit(1)
    print("PASS resumption VALUE-001 capture")
