#!/usr/bin/env python3
"""Build two source revisions and exercise a disposable Rust/Go SymRoom handoff."""

import argparse
import hashlib
import json
import os
import platform
from pathlib import Path
import shutil
import subprocess
import tempfile
import sys


def run(args, *, cwd, env=None):
    result = subprocess.run(
        [str(arg) for arg in args], cwd=cwd, env=env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if result.returncode:
        raise RuntimeError(
            f"{args[0]} exited {result.returncode}\n{result.stdout}{result.stderr}"
        )
    return result


def source_revision(root, ref):
    return run(["git", "rev-parse", "--verify", f"{ref}^{{commit}}"], cwd=root).stdout.strip()


def sha256(path):
    digest = hashlib.sha256()
    with open(path, "rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def invoke(binary, args, work, room, identity, label, expected_code=0):
    env = {
        "HOME": str(work / "home"),
        "USERPROFILE": str(work / "home"),
        "XDG_DATA_HOME": str(work / "data"),
        "XDG_CONFIG_HOME": str(work / "config"),
        "TMPDIR": str(work / "tmp"),
        "SYMROOM_ROOM_DIR": str(room),
        "TZ": "UTC",
        "LC_ALL": "C",
        "LANG": "C",
        "PATH": str(work / "path"),
    }
    if identity:
        env["SYMROOM_DEFAULT_IDENTITY"] = "rollback"
        env["SYMROOM_IDENTITY_KEY"] = identity
    for name in ("home", "data", "config", "tmp", "path"):
        (work / name).mkdir(parents=True, exist_ok=True)
    result = subprocess.run(
        [str(binary), *args], cwd=work, env=env, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.PIPE,
    )
    if result.returncode != expected_code:
        raise RuntimeError(
            f"{label} exited {result.returncode}, expected {expected_code}\n"
            f"{result.stdout}{result.stderr}"
        )
    return {
        "step": label,
        "args": ["symroom", *args],
        "exit_code": result.returncode,
        "stdout": result.stdout,
        "stderr": result.stderr,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go-ref", default="v0.12.2", help="older Go fallback Git revision")
    parser.add_argument("--rust-ref", default="HEAD", help="Rust candidate Git revision")
    parser.add_argument("--report", type=Path, help="write the source-bound JSON report here")
    options = parser.parse_args()

    root = Path(__file__).resolve().parents[2]
    go_revision = source_revision(root, options.go_ref)
    rust_revision = source_revision(root, options.rust_ref)
    report = {
        "schema_version": 1,
        "result": "PASS",
        "host": {"system": platform.system(), "machine": platform.machine()},
        "sources": {
            "rust": {"ref": options.rust_ref, "commit": rust_revision},
            "go_fallback": {"ref": options.go_ref, "commit": go_revision},
        },
        "steps": [],
    }

    with tempfile.TemporaryDirectory(prefix="symroom-rollback-") as temporary:
        temp = Path(temporary)
        rust_tree, go_tree = temp / "rust-source", temp / "go-source"
        run(["git", "worktree", "add", "--detach", rust_tree, rust_revision], cwd=root)
        run(["git", "worktree", "add", "--detach", go_tree, go_revision], cwd=root)
        try:
            rust_bin, go_bin = temp / "symroom-rust", temp / "symroom-go"
            rust_env = os.environ.copy()
            rust_env["CARGO_TARGET_DIR"] = str(temp / "cargo-target")
            rust_env["CARGO_HOME"] = str(temp / "cargo-home")
            run(["cargo", "build", "--locked", "--release", "-p", "symroom-cli",
                 "--manifest-path", rust_tree / "Cargo.toml"], cwd=rust_tree, env=rust_env)
            shutil.copy2(temp / "cargo-target/release/symroom", rust_bin)
            go_env = os.environ.copy()
            go_env.update({
                "CGO_ENABLED": "0",
                "GOCACHE": str(temp / "go-cache"),
                "GOMODCACHE": str(temp / "go-mod-cache"),
                "GOPATH": str(temp / "go-path"),
                "GOTMPDIR": str(temp / "go-tmp"),
                "HOME": str(temp / "build-home"),
            })
            for name in ("go-cache", "go-mod-cache", "go-path", "go-tmp", "build-home"):
                (temp / name).mkdir()
            report["toolchain"] = {
                "go": run(["go", "version"], cwd=go_tree, env=go_env).stdout.strip(),
                "rustc": run(["rustc", "--version"], cwd=rust_tree).stdout.strip(),
                "cargo": run(["cargo", "--version"], cwd=rust_tree).stdout.strip(),
            }
            run(["go", "build", "-trimpath", "-o", go_bin, "./cmd/symroom"],
                cwd=go_tree, env=go_env)
            report["binaries"] = {
                "rust": {"sha256": sha256(rust_bin)},
                "go_fallback": {
                    "sha256": sha256(go_bin),
                    "provenance": "local build from the selected Go source revision; not a public release artifact",
                },
            }
            room, work = temp / "room", temp / "runtime"

            # Rust creates the identity and signed room state in isolated HOME/XDG paths.
            report["steps"].append(invoke(rust_bin, ["identity", "create", "rollback"],
                                          work, room, None, "rust creates identity"))
            identity_file = work / "data/symroom/identities/rollback.json"
            identity_data = json.loads(identity_file.read_text())
            key = identity_data["private_key"]
            report["steps"].append(invoke(rust_bin, ["init", str(room), "--identity", "rollback"],
                                          work, room, key, "rust initializes room"))
            report["steps"].append(invoke(rust_bin, ["note", "--identity", "rollback", "written by Rust"],
                                          work, room, key, "rust writes signed note"))

            # The older Go binary must verify and read Rust's event before mutating the same copy.
            go_verify = invoke(go_bin, ["verify"], work, room, key, "older Go verifies Rust journal")
            report["steps"].append(go_verify)
            go_log = invoke(go_bin, ["log"], work, room, key, "older Go reads Rust journal")
            report["steps"].append(go_log)
            if "written by Rust" not in go_log["stdout"]:
                raise RuntimeError("older Go fallback did not read the Rust-authored note")

            tampered = temp / "tampered-room"
            shutil.copytree(room, tampered)
            segment = next((tampered / "journal").glob("*.jsonl"))
            lines = segment.read_text().splitlines()
            event = json.loads(lines[-1])
            event["body"]["text"] = "tampered note body"
            lines[-1] = json.dumps(event, separators=(",", ":"))
            segment.write_text("\n".join(lines) + "\n")
            tamper_result = invoke(
                go_bin, ["verify"], work, tampered, key,
                "older Go rejects tampered signature", expected_code=1,
            )
            report["steps"].append(tamper_result)
            if "signature verification failed" not in tamper_result["stdout"]:
                raise RuntimeError("older Go did not reject the tampered signed body")

            report["steps"].append(invoke(go_bin, ["note", "--identity", "rollback", "written by Go fallback"],
                                          work, room, key, "older Go appends signed note"))

            rust_verify = invoke(rust_bin, ["verify"], work, room, key,
                                 "Rust verifies mixed-version journal")
            report["steps"].append(rust_verify)
            rust_log = invoke(rust_bin, ["log"], work, room, key,
                              "Rust reads mixed-version journal")
            report["steps"].append(rust_log)
            if "written by Rust" not in rust_log["stdout"] or "written by Go fallback" not in rust_log["stdout"]:
                raise RuntimeError("Rust did not read both signed notes after Go rollback")

            journal = room / "journal"
            report["room"] = {
                "journal_files": {
                    path.name: sha256(path) for path in sorted(journal.glob("*.jsonl"))
                },
                "journal_sha256": sha256_bytes(b"".join(
                    path.read_bytes() for path in sorted(journal.glob("*.jsonl"))
                )),
            }
        finally:
            run(["git", "worktree", "remove", "--force", rust_tree], cwd=root)
            run(["git", "worktree", "remove", "--force", go_tree], cwd=root)

    encoded = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if options.report:
        options.report.write_text(encoded)
    print(encoded, end="")


def sha256_bytes(value):
    return hashlib.sha256(value).hexdigest()


if __name__ == "__main__":
    main()
