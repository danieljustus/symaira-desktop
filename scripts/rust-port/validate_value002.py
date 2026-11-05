#!/usr/bin/env python3
"""Fail-closed validator for a VALUE-002 artifact (final profile only).

Re-derives every sample summary, interval, threshold and verdict from the raw
recorded samples, re-checks the build provenance against the recorded
artifacts, and requires the exact candidate commit.  It never measures.

    python3 scripts/rust-port/validate_value002.py RESULT --candidate SHA40 [--require-pass]

Exit 0 means the artifact is internally consistent for that candidate (the
recorded verdict may still be FAIL; pass ``--require-pass`` to also demand
PASS).  Any inconsistency exits 1.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import value002  # noqa: E402


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("result", type=Path)
    parser.add_argument("--candidate", required=True, help="full 40-hex candidate commit the artifact must measure")
    parser.add_argument("--require-pass", action="store_true")
    args = parser.parse_args()
    try:
        if not value002.v1.is_lower_hex(args.candidate, 40):
            raise value002.HarnessError("--candidate must be a full lowercase 40-hex commit")
        result = json.loads(args.result.read_text(encoding="utf-8"))
        value002.validate_result(result)
        if result["build_record"]["candidate"]["commit"] != args.candidate:
            raise value002.HarnessError("artifact candidate differs from --candidate")
        if result["runner"]["profile"] != "final":
            raise value002.HarnessError("only a final-profile capture can be accepted")
        if args.require_pass and not result["passed"]:
            raise value002.HarnessError("recorded VALUE-002 verdict is FAIL")
    except (value002.HarnessError, OSError, ValueError, KeyError) as error:
        print(f"FAIL {error}", file=sys.stderr)
        return 1
    print("\n".join(value002.verdict_lines(result)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
