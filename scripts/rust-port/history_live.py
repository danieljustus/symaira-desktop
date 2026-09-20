#!/usr/bin/env python3
"""Capture production Go history and replay it on this native Rust target.

This is diagnostic until its source commit and native CI result are accepted.
The raw oracle and command logs are retained even when a comparison fails.
"""
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
OUTPUT = ROOT / "target/history-live"


def source_manifest():
    paths = subprocess.check_output(
        ["git", "ls-files", "-c", "-o", "--exclude-standard", "-z"], cwd=ROOT,
    ).decode().split("\0")
    return {
        name: hashlib.sha256((ROOT / name).read_bytes()).hexdigest()
        for name in sorted(set(paths))
        if name and (ROOT / name).is_file()
        and (Path(name).suffix in {".rs", ".go", ".toml", ".lock", ".mod", ".sum", ".sql", ".json", ".py"}
             or name in {"Makefile", ".gitattributes", ".github/workflows/ci.yml"})
    }


def run():
    OUTPUT.mkdir(parents=True, exist_ok=True)
    oracle = OUTPUT / "oracle.json"
    report = {
        "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
        "native_os": platform.system(), "native_arch": platform.machine(),
        "sources_before": source_manifest(), "commands": [], "passed": False,
    }
    home = Path.home()
    allowed = {
        "PATH", "SystemRoot", "SYSTEMROOT", "WINDIR", "windir", "COMSPEC", "ComSpec",
        "PATHEXT", "SystemDrive", "DEVELOPER_DIR", "SDKROOT",
        "ProgramFiles", "ProgramFiles(x86)",
    }
    allowed_upper = {key.upper() for key in allowed}
    env = {key: value for key, value in os.environ.items() if key.upper() in allowed_upper}
    env.update(
        CARGO_HOME=os.environ.get("CARGO_HOME", str(home / ".cargo")),
        RUSTUP_HOME=os.environ.get("RUSTUP_HOME", str(home / ".rustup")),
        CARGO_TARGET_DIR=str(ROOT / "target"), CARGO_BUILD_JOBS="2",
        GOTOOLCHAIN="local", GOENV="off", GOWORK="off", CGO_ENABLED="0",
        PYTHONDONTWRITEBYTECODE="1", SYMDESK_HISTORY_ORACLE=str(oracle),
    )
    try:
        with tempfile.TemporaryDirectory(prefix="history-live-") as private:
            for key in ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP", "GOCACHE"):
                directory = Path(private) / key
                directory.mkdir()
                env[key] = str(directory)
            commands = [
                ["go", "version"], ["rustc", "--version"],
                ["go", "run", "./scripts/rust-port/cmd/historygen", "--output", str(oracle)],
                ["cargo", "test", "-p", "symdesk-vault", "--test", "history_contracts", "--locked", "--", "--include-ignored", "--nocapture"],
            ]
            for index, command in enumerate(commands):
                record = {"argv": command, "exit_code": None}
                report["commands"].append(record)
                with (OUTPUT / f"{index:02d}-stdout.log").open("wb") as stdout, (OUTPUT / f"{index:02d}-stderr.log").open("wb") as stderr:
                    result = subprocess.run(command, cwd=ROOT, env=env, stdout=stdout, stderr=stderr, timeout=900)
                record["exit_code"] = result.returncode
                print(f"history-live: stage {index} exit {result.returncode}", flush=True)
                if result.returncode:
                    return result.returncode
                text = (OUTPUT / f"{index:02d}-stdout.log").read_text()
                if index == 0 and not text.startswith("go version go1.26.6 "):
                    raise RuntimeError("history oracle requires Go 1.26.6")
                if index == 1 and not text.startswith("rustc 1.98.0 "):
                    raise RuntimeError("history replay requires Rust 1.98.0")
                if index == 3 and "test test_history_live_differential_against_go_oracle ... ok" not in text:
                    raise RuntimeError("live history differential did not execute successfully")
            report["passed"] = True
            return 0
    finally:
        report["sources_after"] = source_manifest()
        unchanged = report["sources_before"] == report["sources_after"]
        report["sources_unchanged"] = unchanged
        report["passed"] = report["passed"] and unchanged
        if oracle.is_file():
            report["oracle_sha256"] = hashlib.sha256(oracle.read_bytes()).hexdigest()
        (OUTPUT / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        if not unchanged:
            raise RuntimeError("history-live source changed during execution")


if __name__ == "__main__":
    raise SystemExit(run())
