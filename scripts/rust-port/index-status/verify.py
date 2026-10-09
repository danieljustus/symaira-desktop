#!/usr/bin/env python3
"""Build immutable Go/Rust sources and retain a native index-status gate."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import platform
import subprocess
import sys

from capture import _terminate_process_tree, sha256_file
from compare import CANONICAL_ORACLE_COMMIT, REQUIRED_CASE_IDS

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
sys.path.insert(0, str(HERE.parent))
from history_live import source_manifest


def native_case_count(comparison: dict) -> int:
    count = comparison.get("compared_cases")
    if (comparison.get("pass") is not True or comparison.get("clean_acceptance") is not True
            or type(count) is not int or count != len(REQUIRED_CASE_IDS)):
        raise RuntimeError("complete clean native comparison was not established")
    return count


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--build-launcher", type=Path, help="local build-policy launcher, if required")
    parser.add_argument("--oracle-worktree", type=Path,
                        help="clean canonical oracle checkout in the local policy's approved source area")
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ)
    env.update(PYTHONDONTWRITEBYTECODE="1", CARGO_TARGET_DIR=str(output / "cargo-target"),
               GOTOOLCHAIN="go1.26.9", GOENV="off", GOWORK="off", GOFLAGS="-mod=readonly", CGO_ENABLED="0")
    report = {"passed": False, "commands": [], "native_os": platform.system(),
              "native_arch": platform.machine(), "sources_before": source_manifest()}

    def git(*args: str, cwd: Path = ROOT) -> str:
        return subprocess.check_output(["git", *args], cwd=cwd, text=True).strip()

    def run(argv: list[str], *, cwd: Path = ROOT, build: bool = False) -> None:
        if build and args.build_launcher:
            argv = [str(args.build_launcher.resolve()), *argv]
        index = len(report["commands"])
        record = {"argv": argv, "cwd": str(cwd), "exit_code": None, "timed_out": False}
        report["commands"].append(record)
        with (output / f"{index:02d}.stdout.log").open("xb") as stdout, (output / f"{index:02d}.stderr.log").open("xb") as stderr:
            process = subprocess.Popen(argv, env=env, cwd=cwd, stdin=subprocess.DEVNULL,
                                       stdout=stdout, stderr=stderr, start_new_session=os.name != "nt",
                                       creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0)
            try:
                record["exit_code"] = process.wait(timeout=900)
            except subprocess.TimeoutExpired:
                record["timed_out"] = True
                raise
            finally:
                _terminate_process_tree(process, env)
        print(f"index-status gate stage {index}: exit={record['exit_code']}", flush=True)
        if record["timed_out"] or record["exit_code"] != 0:
            raise RuntimeError(f"stage {index} failed; raw logs retained in {output}")

    try:
        if git("status", "--porcelain=v1", "--untracked-files=all"):
            raise RuntimeError("native gate requires clean committed source")
        head = git("rev-parse", "HEAD")
        report["source_commit"] = head
        report["oracle_commit"] = CANONICAL_ORACLE_COMMIT
        run(["git", "merge-base", "--is-ancestor", CANONICAL_ORACLE_COMMIT, head])
        oracle = args.oracle_worktree.resolve() if args.oracle_worktree else output / "oracle"
        if not oracle.exists():
            run(["git", "worktree", "add", "--detach", str(oracle), CANONICAL_ORACLE_COMMIT])
        if (Path(git("rev-parse", "--show-toplevel", cwd=oracle)).resolve() != oracle
                or git("rev-parse", "HEAD", cwd=oracle) != CANONICAL_ORACLE_COMMIT
                or git("status", "--porcelain=v1", "--untracked-files=all", cwd=oracle)):
            raise RuntimeError("oracle worktree must be the exact clean canonical checkout")
        suffix = ".exe" if os.name == "nt" else ""
        go_binary = output / f"symdesk-go{suffix}"
        rust_binary = output / "cargo-target" / "debug" / f"symdesk{suffix}"
        run(["go", "build", "-o", str(go_binary), "./cmd/symdesk"], cwd=oracle, build=True)
        run(["cargo", "build", "--manifest-path", str(ROOT / "Cargo.toml"), "--locked",
             "-p", "symdesk-cli", "--bin", "symdesk"], build=True)
        go_hash, rust_hash = sha256_file(go_binary), sha256_file(rust_binary)
        report["binaries"] = {"go": {"path": str(go_binary), "sha256": go_hash},
                              "rust": {"path": str(rust_binary), "sha256": rust_hash}}
        run([sys.executable, "-m", "unittest", "discover", "-s", str(HERE), "-p", "test_*.py"])
        capture = output / "capture"
        run([sys.executable, str(HERE / "capture.py"), "--go-bin", str(go_binary),
             "--rust-bin", str(rust_binary), "--output", str(capture),
             "--world-root", str(output / "worlds"), "--label", f"native-{head}",
             "--rust-build-source-commit", head, "--expected-go-sha256", go_hash,
             "--expected-rust-sha256", rust_hash])
        run([sys.executable, str(HERE / "compare.py"), "--manifest", str(capture / "manifest.json"),
             "--report", str(capture / "comparison.json"), "--expected-rust-commit", head,
             "--expected-go-sha256", go_hash, "--expected-rust-sha256", rust_hash])
        comparison = json.loads((capture / "comparison.json").read_text())
        case_count = native_case_count(comparison)
        if (git("rev-parse", "HEAD", cwd=oracle) != CANONICAL_ORACLE_COMMIT
                or git("status", "--porcelain=v1", "--untracked-files=all", cwd=oracle)):
            raise RuntimeError("oracle source changed during the gate")
        if git("rev-parse", "HEAD") != head or git("status", "--porcelain=v1", "--untracked-files=all"):
            raise RuntimeError("candidate revision or source changed during the gate")
        if sha256_file(go_binary) != go_hash or sha256_file(rust_binary) != rust_hash:
            raise RuntimeError("executables changed during the gate")
        report["case_count"] = case_count
        report["passed"] = True
        return 0
    finally:
        report["sources_after"] = source_manifest()
        report["sources_unchanged"] = report["sources_before"] == report["sources_after"]
        report["passed"] = report["passed"] and report["sources_unchanged"]
        (output / "native-gate.json").write_text(json.dumps(report, indent=2) + "\n")
        if not report["sources_unchanged"]:
            raise RuntimeError("source contents changed; gate cannot pass")


if __name__ == "__main__":
    raise SystemExit(main())
