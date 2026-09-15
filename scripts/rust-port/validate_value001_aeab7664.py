#!/usr/bin/env python3
"""Fail-closed validation for the independently reviewed VALUE-001 aeab7664 artifact."""
from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path

import validate_value001_candidate as candidate_validator
from validate_value001_resume import REDACTED_PATHS, compare_derivation

EXPECTED_HEAD = "aeab76640b5e2f1eb935bd3ec2173ed1c608bf06"
EXPECTED_GO_ORACLE = "745c08e8144971c61133c5d0e5d61c7ce405aad2"
EXPECTED_TRUSTED_SHA256 = "b34ec7f8fddbadd3d63ccec3cc9416cb9335e40aa9b456f6d81cd1c5be067f31"
EXPECTED_RAW_SHA256 = "8acdbc8415f4f5ee89d26161913ab31ccdcda87517c9b1cbb10648c7733a14d5"
EXPECTED_TRUSTED_CI_RUN = "34904301453"
ARTIFACT = Path(__file__).resolve().parents[2] / "docs/rust-port/results/value001-aeab7664.json"
METADATA_KEYS = frozenset(
    {
        "original_sha256",
        "redacted_sha256",
        "source_expected_head",
        "go_oracle",
        "trusted_ci_run",
        "source_artifact",
        "redacted_json_paths",
        "derivation",
    }
)
METADATA_STRING_KEYS = METADATA_KEYS - {"redacted_json_paths"}


def validate(
    path: Path,
    root: Path,
    trusted_sha256: str = EXPECTED_TRUSTED_SHA256,
    raw_path: Path | None = None,
) -> None:
    if trusted_sha256 != EXPECTED_TRUSTED_SHA256:
        raise candidate_validator.ValidationError(
            f"trusted SHA256 must match independently reviewed constant {EXPECTED_TRUSTED_SHA256}"
        )
    if path.is_symlink() or not path.is_file():
        raise candidate_validator.ValidationError("artifact must be a regular file")
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != EXPECTED_TRUSTED_SHA256:
        raise candidate_validator.ValidationError("artifact digest differs from trusted SHA256")
    if b"/Users/" in data or b"/var/folders/" in data or b":\\\\Users\\\\" in data:
        raise candidate_validator.ValidationError("retained artifact contains a private path")

    metadata_path = path.with_suffix(".metadata.json")
    if metadata_path.is_symlink() or not metadata_path.is_file():
        raise candidate_validator.ValidationError("retained provenance sidecar is missing or invalid")
    try:
        metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise candidate_validator.ValidationError(f"invalid provenance metadata: {exc}") from exc

    if not isinstance(metadata, dict):
        raise candidate_validator.ValidationError("provenance metadata must be an object")
    if set(metadata) != METADATA_KEYS:
        raise candidate_validator.ValidationError("provenance metadata schema mismatch")
    for key in METADATA_STRING_KEYS:
        if not isinstance(metadata[key], str):
            raise candidate_validator.ValidationError(f"provenance metadata {key} must be a string")
    paths = metadata["redacted_json_paths"]
    if not isinstance(paths, list):
        raise candidate_validator.ValidationError("provenance metadata redacted_json_paths must be a list")
    if any(not isinstance(path, str) for path in paths):
        raise candidate_validator.ValidationError("provenance metadata redacted_json_paths must contain only strings")
    if len(paths) != len(set(paths)):
        raise candidate_validator.ValidationError("provenance metadata redacted_json_paths contains duplicates")

    if metadata["original_sha256"] != EXPECTED_RAW_SHA256:
        raise candidate_validator.ValidationError("original raw artifact digest is not the verified capture")
    if metadata["redacted_sha256"] != EXPECTED_TRUSTED_SHA256:
        raise candidate_validator.ValidationError("retained artifact digest does not match provenance sidecar")
    if metadata["source_expected_head"] != EXPECTED_HEAD:
        raise candidate_validator.ValidationError("metadata source_expected_head mismatch")
    if metadata["go_oracle"] != EXPECTED_GO_ORACLE:
        raise candidate_validator.ValidationError("metadata go_oracle mismatch")
    if metadata["trusted_ci_run"] != EXPECTED_TRUSTED_CI_RUN:
        raise candidate_validator.ValidationError("metadata trusted_ci_run mismatch")
    if set(paths) != REDACTED_PATHS:
        raise candidate_validator.ValidationError("metadata redacted_json_paths inventory differs from review")

    # Reuse REAL candidate_validator.validate unchanged against explicit clean measured root
    candidate_validator.validate(path, EXPECTED_HEAD, root, EXPECTED_TRUSTED_SHA256)

    if raw_path is not None:
        if raw_path.is_symlink() or not raw_path.is_file():
            raise candidate_validator.ValidationError("original raw capture must be a regular file")
        raw_bytes = raw_path.read_bytes()
        if hashlib.sha256(raw_bytes).hexdigest() != EXPECTED_RAW_SHA256:
            raise candidate_validator.ValidationError("original capture differs from independent review")
        try:
            raw_data = json.loads(raw_bytes.decode("utf-8"))
            derived_data = json.loads(data.decode("utf-8"))
            compare_derivation(raw_data, derived_data)
        except (json.JSONDecodeError, ValueError) as exc:
            raise candidate_validator.ValidationError(f"derivation check failed: {exc}") from exc


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("artifact", type=Path, nargs="?", default=ARTIFACT, help="Path to value001-aeab7664.json")
    parser.add_argument("--root", required=True, type=Path, help="Clean candidate checkout used for current approval")
    parser.add_argument("--trusted-sha256", default=EXPECTED_TRUSTED_SHA256, help="Independently reviewed artifact SHA256")
    parser.add_argument("--raw", type=Path, default=None, help="Private original raw capture for derivation verification")
    args = parser.parse_args(argv)
    try:
        validate(args.artifact, args.root, args.trusted_sha256, args.raw)
    except (candidate_validator.ValidationError, OSError, ValueError) as exc:
        print(f"FAIL {exc}", file=sys.stderr)
        return 1
    print(f"PASS VALUE-001 aeab7664 acceptance capture: {args.artifact}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
