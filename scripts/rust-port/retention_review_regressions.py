#!/usr/bin/env python3
"""Run focused Go-vs-Rust retention CLI regression cases.

The runner uses only synthetic state, isolated homes and real CLI binaries. It
records raw streams plus before/after retention bytes and modes in a JSON report.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import tempfile
from typing import Any

TIMEOUT_SECONDS = 15
PROPOSAL = {
    "run_id": "probe",
    "rule_name": "test",
    "created": "2026-01-02T03:04:05Z",
    "items": [
        {
            "path": "note.md",
            "title": "Note",
            "reference_date": "2025-01-01",
            "expires_at": "2026-01-01",
            "action": "trash",
        }
    ],
    "status": "pending",
}
HISTORY_WINTER = [
    {
        "timestamp": "2026-01-02T03:04:05Z",
        "rule_name": "winter",
        "action": "trash",
        "path": "winter.md",
        "title": "Winter",
    }
]
HISTORY_SUMMER = [
    {
        "timestamp": "2026-07-02T03:04:05Z",
        "rule_name": "summer",
        "action": "trash",
        "path": "summer.md",
        "title": "Summer",
    }
]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go-binary", type=Path, required=True)
    parser.add_argument("--rust-binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument(
        "--scratch-root",
        type=Path,
        default=Path(os.environ.get("TMPDIR", tempfile.gettempdir())),
    )
    return parser.parse_args()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def encoded(raw: bytes) -> str:
    return base64.b64encode(raw).decode("ascii")


def snapshot(directory: Path) -> dict[str, Any]:
    files: dict[str, Any] = {}
    if directory.exists():
        for path in sorted(directory.iterdir(), key=lambda value: value.name):
            if path.is_file():
                data = path.read_bytes()
                files[path.name] = {
                    "bytes_base64": encoded(data),
                    "sha256": hashlib.sha256(data).hexdigest(),
                    "size": len(data),
                    "mode": oct(stat.S_IMODE(path.stat().st_mode)) if os.name == "posix" else None,
                }
    return {
        "exists": directory.exists(),
        "mode": oct(stat.S_IMODE(directory.stat().st_mode))
        if os.name == "posix" and directory.exists()
        else None,
        "files": files,
    }


def normalize_stream(raw: bytes, sandbox: Path) -> bytes:
    raw = raw.replace(str(sandbox).encode(), b"<SANDBOX>")
    # Only the atomic writer's random name beneath this case's state is noise.
    # Preserve line endings, non-UTF-8 bytes and similarly named user content.
    return re.sub(
        rb"(open <SANDBOX>[/\\]workspace[/\\]vault[/\\]\.symdesk[/\\]retention[/\\]"
        rb"\.symdesk-retention-)(?:[0-9]+|[0-9a-f]+-[0-9]+)(\.tmp: )",
        rb"\1<TEMP>\2",
        raw,
    )


def terminate_process_group(process: subprocess.Popen[bytes]) -> None:
    if os.name == "posix":
        # Descendants can retain output/state handles after their leader exits.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    elif process.poll() is None:
        process.kill()
    try:
        process.wait(timeout=3)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=3)


def execute(binary: Path, case: dict[str, Any], scratch_root: Path) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="retention-regression-", dir=scratch_root) as temp:
        sandbox = Path(temp)
        home = sandbox / "home"
        workspace = sandbox / "workspace"
        runtime = sandbox / "runtime"
        temporary = sandbox / "tmp"
        vault = workspace / "vault"
        state = vault / ".symdesk" / "retention"
        for directory in (home, workspace, runtime, temporary, state):
            directory.mkdir(parents=True, mode=0o700, exist_ok=True)

        payload = case.get("proposal", PROPOSAL)
        proposal_bytes = payload if isinstance(payload, bytes) else json.dumps(
            payload, ensure_ascii=False, separators=(",", ":")
        ).encode()
        (state / "probe.json").write_bytes(proposal_bytes)
        if "history" in case:
            history = case["history"]
            history_bytes = history if isinstance(history, bytes) else json.dumps(
                history, ensure_ascii=False, separators=(",", ":")
            ).encode()
            (state / "history.json").write_bytes(history_bytes)
        if os.name == "posix":
            state.chmod(case.get("mode", 0o700))

        before = snapshot(state)
        environment = {
            key: os.environ[key]
            for key in ("PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT")
            if key in os.environ
        }
        environment.update(
            {
                "HOME": str(home),
                "USERPROFILE": str(home),
                "XDG_CONFIG_HOME": str(home / ".config"),
                "XDG_DATA_HOME": str(home / ".local" / "share"),
                "XDG_CACHE_HOME": str(home / ".cache"),
                "XDG_STATE_HOME": str(home / ".local" / "state"),
                "XDG_RUNTIME_DIR": str(runtime),
                "TMPDIR": str(temporary),
                "TMP": str(temporary),
                "TEMP": str(temporary),
                "LANG": "C",
                "LC_ALL": "C",
                "TZ": case.get("tz", "UTC"),
                "TERM": "dumb",
                "NO_COLOR": "1",
                "SYMDESK_VAULT": "",
                "SYMDESK_SIDECAR": "",
            }
        )
        command = [str(binary), "--vault", str(vault), *case["args"]]
        stdout_path = sandbox / "stdout"
        stderr_path = sandbox / "stderr"
        process: subprocess.Popen[bytes] | None = None
        timed_out = False
        cleanup_attempted = False
        try:
            with stdout_path.open("wb") as stdout, stderr_path.open("wb") as stderr:
                process = subprocess.Popen(
                    command,
                    cwd=workspace,
                    env=environment,
                    stdin=subprocess.DEVNULL,
                    stdout=stdout,
                    stderr=stderr,
                    start_new_session=True,
                )
                try:
                    process.wait(timeout=TIMEOUT_SECONDS)
                except subprocess.TimeoutExpired:
                    timed_out = True
                finally:
                    # Attempt cleanup once; a second kill could hit a reused PGID.
                    cleanup_attempted = True
                    terminate_process_group(process)
            assert process is not None
            stdout = stdout_path.read_bytes()
            stderr = stderr_path.read_bytes()
            after = snapshot(state)
            return {
                "executed": True,
                "command": [str(binary), "--vault", "<VAULT>", *case["args"]],
                "exit": process.returncode,
                "timed_out": timed_out,
                "stdout_base64": encoded(stdout),
                "stderr_base64": encoded(stderr),
                "stdout_sha256": hashlib.sha256(stdout).hexdigest(),
                "stderr_sha256": hashlib.sha256(stderr).hexdigest(),
                "stdout_compare_base64": encoded(normalize_stream(stdout, sandbox)),
                "stderr_compare_base64": encoded(normalize_stream(stderr, sandbox)),
                "before": before,
                "after": after,
            }
        finally:
            if process is not None and not cleanup_attempted:
                terminate_process_group(process)
            if os.name == "posix" and state.exists():
                state.chmod(0o700)


def cases() -> list[dict[str, Any]]:
    missing_items = dict(PROPOSAL)
    missing_items.pop("items")
    null_items = dict(PROPOSAL, items=None)
    empty_items = dict(PROPOSAL, items=[])
    missing_and_null_item = dict(PROPOSAL)
    missing_and_null_item["items"] = [
        {
            "title": None,
            "reference_date": "2025-01-01",
            "expires_at": "2026-01-01",
            "action": "trash",
            "rule_name": None,
        }
    ]
    stored_run = dict(PROPOSAL, run_id="stored")
    duplicate_null = (
        b'{"run_id":"probe","rule_name":"test","rule_name":null,'
        b'"created":"2026-01-02T03:04:05Z","items":[],"items":null,'
        b'"status":"pending"}'
    )
    history_null_strings = [
        {
            "timestamp": "2026-01-02T03:04:05Z",
            "rule_name": None,
            "action": None,
            "path": None,
            "title": None,
        }
    ]
    result = [
        {
            "id": "existing-readonly-directory",
            "args": ["--json", "retention", "reject", "probe"],
            "mode": 0o555,
            "platform": "unix",
        },
        {
            "id": "writable-directory-preservation",
            "args": ["--json", "retention", "reject", "probe"],
            "mode": 0o700,
            "platform": "unix",
        },
        {
            "id": "proposal-missing-items",
            "args": ["--json", "retention", "diff", "probe"],
            "proposal": missing_items,
        },
        {
            "id": "proposal-null-items",
            "args": ["--json", "retention", "diff", "probe"],
            "proposal": null_items,
        },
        {
            "id": "proposal-empty-items-control",
            "args": ["--json", "retention", "diff", "probe"],
            "proposal": empty_items,
        },
        {
            "id": "proposal-item-missing-null-strings",
            "args": ["--json", "retention", "diff", "probe"],
            "proposal": missing_and_null_item,
        },
        {
            "id": "proposal-top-level-null",
            "args": ["--json", "retention", "diff", "probe"],
            "proposal": b"null",
        },
        {
            "id": "proposal-duplicate-null-last-wins",
            "args": ["--json", "retention", "diff", "probe"],
            "proposal": duplicate_null,
        },
        {
            "id": "history-null-strings",
            "args": ["--json", "retention", "history"],
            "history": history_null_strings,
        },
        {
            "id": "text-overrides-json-before",
            "args": ["--output", "text", "--json", "retention", "diff", "probe"],
        },
        {
            "id": "text-overrides-json-after",
            "args": ["retention", "diff", "probe", "--json", "--output", "text"],
        },
        {
            "id": "history-winter-event-offset",
            "args": ["retention", "history"],
            "tz": "Europe/Berlin",
            "history": HISTORY_WINTER,
        },
        {
            "id": "history-summer-event-offset",
            "args": ["retention", "history"],
            "tz": "Europe/Berlin",
            "history": HISTORY_SUMMER,
        },
        {
            "id": "malformed-proposal-eof",
            "args": ["--json", "retention", "diff", "probe"],
            "proposal": b"{",
        },
        {
            "id": "malformed-proposal-invalid-key",
            "args": ["--json", "retention", "diff", "probe"],
            "proposal": b"{ not json",
        },
        {
            "id": "malformed-proposal-type",
            "args": ["--json", "retention", "diff", "probe"],
            "proposal": b'{"run_id":1}',
        },
        {
            "id": "malformed-history-eof",
            "args": ["--json", "retention", "history"],
            "history": b"{",
        },
        {
            "id": "version-text-overrides-json",
            "args": ["--json", "version", "--output", "text"],
        },
        {
            "id": "error-json-go-escaping",
            "args": ["--json", "retention", "diff", "probe<\u2028\u2029"],
        },
        {
            "id": "inherited-stored-run-id-known-defect",
            "args": ["--json", "retention", "reject", "probe"],
            "proposal": stored_run,
            "known_defect": "#1028: both implementations write stored.json and leave probe.json pending",
        },
    ]
    return result


def comparable(observation: dict[str, Any]) -> dict[str, Any]:
    return {
        "exit": observation["exit"],
        "timed_out": observation["timed_out"],
        "stdout_base64": observation["stdout_compare_base64"],
        "stderr_base64": observation["stderr_compare_base64"],
        "after": observation["after"],
    }


def main() -> int:
    args = parse_args()
    go_binary = args.go_binary.expanduser().resolve(strict=True)
    rust_binary = args.rust_binary.expanduser().resolve(strict=True)
    output = args.output.expanduser().resolve()
    scratch_root = args.scratch_root.expanduser().resolve()
    scratch_root.mkdir(parents=True, exist_ok=True)
    output.parent.mkdir(parents=True, exist_ok=True)

    if go_binary == rust_binary:
        raise SystemExit("refusing to compare the same executable path")
    binary_hashes = {
        "go": sha256_file(go_binary),
        "rust": sha256_file(rust_binary),
    }
    if binary_hashes["go"] == binary_hashes["rust"]:
        raise SystemExit("refusing to compare byte-identical executables")

    declared = cases()
    declared_ids = {case["id"] for case in declared}
    if not declared or len(declared_ids) != len(declared):
        raise ValueError("case inventory must be nonempty with unique IDs")
    report: dict[str, Any] = {
        "schema_version": 2,
        "comparison_normalizers": ["case sandbox root", "atomic temporary name within case retention state"],
        "platform": sys.platform,
        "binary_paths": {"go": str(go_binary), "rust": str(rust_binary)},
        "binary_sha256": binary_hashes,
        "script_sha256": sha256_file(Path(__file__).resolve()),
        "cases": [],
    }
    executed_ids: set[str] = set()
    failed_ids: list[str] = []
    skipped_ids: list[str] = []

    for case in declared:
        if case.get("platform") == "unix" and os.name != "posix":
            record = {
                "id": case["id"],
                "platform": "unix",
                "executed": False,
                "passed": False,
                "skip_reason": "Unix permission semantics require a native non-root Unix run",
            }
            skipped_ids.append(case["id"])
        elif case.get("platform") == "unix" and hasattr(os, "geteuid") and os.geteuid() == 0:
            record = {
                "id": case["id"],
                "platform": "unix",
                "executed": False,
                "passed": False,
                "skip_reason": "Unix permission semantics are invalid when executed as root",
            }
            skipped_ids.append(case["id"])
        else:
            observations = {
                "go": execute(go_binary, case, scratch_root),
                "rust": execute(rust_binary, case, scratch_root),
            }
            executed_ids.add(case["id"])
            execution_errors = [
                f"{role}: timed out" if observation["timed_out"] else f"{role}: signal termination"
                for role, observation in observations.items()
                if observation["timed_out"] or observation["exit"] < 0
            ]
            passed = not execution_errors and comparable(observations["go"]) == comparable(observations["rust"])
            differences = [
                key
                for key in ("exit", "timed_out", "stdout_base64", "stderr_base64", "after")
                if comparable(observations["go"])[key] != comparable(observations["rust"])[key]
            ]
            record = {
                "id": case["id"],
                "platform": case.get("platform", "any"),
                "executed": True,
                "passed": passed,
                "execution_errors": execution_errors,
                "differences": differences,
                "observations": observations,
            }
            if "known_defect" in case:
                record["known_defect"] = case["known_defect"]
            if not passed:
                failed_ids.append(case["id"])
        report["cases"].append(record)
        output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        print(
            json.dumps(
                {
                    "id": record["id"],
                    "executed": record["executed"],
                    "passed": record["passed"],
                    "differences": record.get("differences", []),
                    "execution_errors": record.get("execution_errors", []),
                },
                sort_keys=True,
            ),
            flush=True,
        )

    if executed_ids | set(skipped_ids) != declared_ids:
        raise AssertionError("declared/executed/skipped case IDs differ")
    if len(report["cases"]) != len(declared):
        raise AssertionError("not every declared case produced a report row")

    report["declared_case_count"] = len(declared)
    report["executed_case_count"] = len(executed_ids)
    report["skipped_case_ids"] = sorted(skipped_ids)
    report["failed_case_ids"] = sorted(failed_ids)
    report["success"] = not failed_ids and not skipped_ids
    output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(
        f"executed={len(executed_ids)} declared={len(declared)} "
        f"failed={len(failed_ids)} skipped={len(skipped_ids)} report={output}"
    )
    return 0 if report["success"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
