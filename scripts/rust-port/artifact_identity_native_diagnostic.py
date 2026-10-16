#!/usr/bin/env python3
"""Disposable #1270 pristine replay collector, not an acceptance gate."""
from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import platform
import sys
import time

sys.dont_write_bytecode = True
SOURCE_SHA = "88e78b2abf0b2983bd5197e4e5d814022960b9f2"
ORIGIN = "https://github.com/danieljustus/symaira-desktop"
MAX_ATTEMPTS = 2  # One earlier full replay already ran on each target.
COMMAND = ["go", "test", "-count=1", "./cmd/symroom", "-run", "^TestPortArtifactIdentityCLIContract$"]


def replay(module, report, logs, root, env):
    for attempt in range(1, MAX_ATTEMPTS + 1):
        started = time.monotonic()
        try:
            module.run_logged_command(
                report, logs, f"pristine-artifact-identity-{attempt}", COMMAND, root,
                env=env, launch_env=env, timeout=300,
            )
        except Exception as error:
            # Raw original failure is retained by the existing runner first.
            report["outcome"] = "original-failure-retained"
            report["failure_class"] = type(error).__name__
            report["failed_attempt"] = attempt
            print(f"Original pristine replay failed at attempt {attempt}: {type(error).__name__}", flush=True)
            return 1
        finally:
            report["attempt_durations_seconds"].append(time.monotonic() - started)
    report["outcome"] = "not-reproduced-within-bound"
    return 0


def main():
    root = Path(sys.argv[1]).resolve(strict=True)
    evidence = Path(sys.argv[2]).resolve()
    evidence.mkdir(parents=True, exist_ok=False)
    logs = evidence / "commands"
    logs.mkdir()
    spec = importlib.util.spec_from_file_location(
        "artifact_diagnostic_existing_helpers", root / "scripts/rust-port/config_paths_native.py"
    )
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load existing process-tree and evidence helpers")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    report = {
        "source_sha": SOURCE_SHA,
        "workflow_sha": os.environ.get("GITHUB_SHA"),
        "host_os": platform.system(),
        "host_arch": platform.machine(),
        "runner_label": os.environ.get("NATIVE_RUNNER_LABEL"),
        "collector_sha256": module.sha256_file(Path(__file__)),
        "maximum_attempts": MAX_ATTEMPTS,
        "previous_full_replays_per_target": 1,
        "experiment": "Focused artifact-identity replay; no complete portgen acceptance",
        "commands": [],
        "attempt_durations_seconds": [],
        "outcome": "preflight-not-completed",
        "full_ci_acceptance": False,
        "historical_cause_established": False,
    }
    result = 1
    before = None
    try:
        if platform.system() != "Windows":
            raise RuntimeError("this experiment requires a genuine native Windows host")
        before = module.repository_snapshot(root)
        module.write_json(evidence / "sources.before.json", before)
        report["source_origin"] = module.git(root, "remote", "get-url", "origin")
        if report["source_origin"] != ORIGIN:
            raise RuntimeError("unexpected source repository")
        if before["commit"] != SOURCE_SHA or before["status"]:
            raise RuntimeError("pristine exact-source preflight failed")
        env = module.sanitized_environment(dict(os.environ))
        env.update({"CGO_ENABLED": "0", "GOTOOLCHAIN": "local", "GOWORK": "off"})
        observed = module.run_logged_command(
            report, logs, "go-execution-identity",
            ["go", "env", "-json", "GOOS", "GOARCH", "GOVERSION", "GOMOD"], root,
            env=env, launch_env=env, timeout=30,
        )
        report["go_identity"] = json.loads(observed)
        identity = report["go_identity"]
        if identity["GOOS"] != "windows" or identity["GOVERSION"] != "go1.26.9" or Path(identity["GOMOD"]).resolve() != root / "go.mod":
            raise RuntimeError("unexpected executed Go identity")
        expected_arch = {"windows-latest": "amd64", "windows-11-arm": "arm64"}.get(report["runner_label"])
        if expected_arch is None or identity["GOARCH"] != expected_arch:
            raise RuntimeError("runner label does not match native Go architecture")
        result = replay(module, report, logs, root, env)
    except Exception as error:
        report["collector_error_class"] = type(error).__name__
        print(f"Diagnostic collector failed: {type(error).__name__}: {error}", file=sys.stderr)
    finally:
        try:
            after = module.repository_snapshot(root)
            module.write_json(evidence / "sources.after.json", after)
            report["source_inventory_unchanged"] = before is not None and before == after
            if not report["source_inventory_unchanged"]:
                result = 1
        except Exception as error:
            report["source_after_error_class"] = type(error).__name__
            result = 1
        module.write_json(evidence / "report.json", report)
    return result


if __name__ == "__main__":
    raise SystemExit(main())
