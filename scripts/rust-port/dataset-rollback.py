#!/usr/bin/env python3
"""Prove an older Go SymDesk can read and extend a Rust-written dataset."""

import argparse
import hashlib
import importlib.util
import json
import os
import platform
import re
import shutil
import sys
import subprocess
import tempfile
from pathlib import Path

sys.dont_write_bytecode = True

_ROOM_SPEC = importlib.util.spec_from_file_location(
    "room_rollback", Path(__file__).with_name("room-rollback.py")
)
room_rollback = importlib.util.module_from_spec(_ROOM_SPEC)
_ROOM_SPEC.loader.exec_module(room_rollback)


def run(binary, vault, args, label, env):
    command = [str(binary), "--json", "--vault", str(vault), *args]
    result = subprocess.run(command, capture_output=True, text=True, check=False, env=env)
    if result.returncode:
        raise RuntimeError(
            f"{label} failed ({result.returncode})\n"
            f"stdout: {result.stdout.strip()}\nstderr: {result.stderr.strip()}"
        )
    return result.stdout


def dataset_identities(output):
    try:
        document = json.loads(output)
        rows = document["rows"]
        identities = {row["identity"] for row in rows}
    except (json.JSONDecodeError, KeyError, TypeError) as error:
        raise RuntimeError(f"dataset query returned an invalid result: {error}") from error
    if len(identities) != len(rows):
        raise RuntimeError("dataset query returned duplicate identities")
    return identities


def source_revision(root, ref):
    return room_rollback.run(["git", "rev-parse", "--verify", f"{ref}^{{commit}}"], cwd=root).stdout.strip()


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def source_tree_sha256(root):
    digest = hashlib.sha256()
    files = sorted(
        path for path in root.rglob("*")
        if path.is_file() or path.is_symlink()
        if ".git" not in path.relative_to(root).parts
    )
    for path in files:
        relative = path.relative_to(root).as_posix().encode("utf-8")
        digest.update(len(relative).to_bytes(8, "big"))
        digest.update(relative)
        if path.is_symlink():
            target = os.readlink(path).encode("utf-8")
            digest.update(b"L")
            digest.update(len(target).to_bytes(8, "big"))
            digest.update(target)
        else:
            digest.update(b"F")
            digest.update(path.stat().st_size.to_bytes(8, "big"))
            with path.open("rb") as source:
                for block in iter(lambda: source.read(1024 * 1024), b""):
                    digest.update(block)
    return digest.hexdigest()


def build_binaries(root, temp, go_revision, rust_revision, env, git, go, cargo, worktrees):
    rust_tree, go_tree = temp / "rust-source", temp / "go-source"
    room_rollback.run([git, "worktree", "add", "--detach", rust_tree, rust_revision], cwd=root, env=env)
    worktrees.append(rust_tree)
    exclusions = []
    if os.name == "nt":
        exclusions.append("internal/ingest/internal/notionimport/testdata/fixture/")
        room_rollback.extract_git_blobs(root, go_revision, go_tree, env, git, tuple(exclusions))
    else:
        room_rollback.run([git, "worktree", "add", "--detach", go_tree, go_revision], cwd=root, env=env)
        worktrees.append(go_tree)

    suffix = ".exe" if os.name == "nt" else ""
    rust_output = temp / "cargo-target" / "release" / f"symdesk{suffix}"
    go_binary = temp / f"symdesk-go{suffix}"
    room_rollback.run([
        cargo, "build", "--locked", "--release", "-p", "symdesk-cli",
        "--manifest-path", rust_tree / "Cargo.toml",
    ], cwd=rust_tree, env=env)
    room_rollback.run([
        go, "build", "-trimpath", "-buildvcs=false", "-o", go_binary, "./cmd/symdesk",
    ], cwd=go_tree, env=env)
    return rust_output, go_binary, {
        "rust": {"sha256": source_tree_sha256(rust_tree), "exclusions": []},
        "go_fallback": {"sha256": source_tree_sha256(go_tree), "exclusions": exclusions},
    }


def validate_report(report):
    sources = report.get("sources", {})
    binaries = report.get("binaries", {})
    source_binding = report.get("source_binding")
    if source_binding == "enforced" and binaries.get("provenance") != "built from the reported source commits":
        raise RuntimeError("report claims source binding for caller-provided binaries")
    for name in ("rust", "go_fallback"):
        source = sources.get(name, {})
        binary = binaries.get(name, {})
        if not re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", source.get("commit", "")):
            raise RuntimeError(f"report is missing a valid source commit for {name}")
        if source_binding == "enforced" and not re.fullmatch(r"[0-9a-f]{64}", source.get("sha256", "")):
            raise RuntimeError(f"report is missing source commit/SHA-256 for {name}")
        if not re.fullmatch(r"[0-9a-f]{64}", binary.get("sha256", "")):
            raise RuntimeError(f"report is missing binary SHA-256 for {name}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--rust-binary", type=Path, help="use a prebuilt Rust binary")
    parser.add_argument("--go-binary", type=Path, help="use a prebuilt historical Go binary")
    parser.add_argument("--go-ref", default="v0.12.2", help="historical Go source revision represented by the binary")
    parser.add_argument("--rust-ref", default="HEAD", help="Rust candidate Git revision to build")
    parser.add_argument("--report", type=Path, help="write the source-bound JSON report")
    options = parser.parse_args()
    if bool(options.rust_binary) != bool(options.go_binary):
        parser.error("--rust-binary and --go-binary must be supplied together")

    root = Path(__file__).resolve().parents[2]
    go_revision = source_revision(root, options.go_ref)
    rust_revision = source_revision(root, options.rust_ref)
    report = {
        "schema_version": 1,
        "result": "FAIL",
        "host": {"system": platform.system(), "machine": platform.machine()},
        "sources": {
            "rust": {"ref": options.rust_ref, "commit": rust_revision},
            "go_fallback": {"ref": options.go_ref, "commit": go_revision},
        },
        "steps": [],
    }
    temp = Path(tempfile.mkdtemp(prefix="symdesk-dataset-rollback-"))
    worktrees = []
    build_env = None
    git = shutil.which("git")
    failure = None
    try:
        if not git:
            raise RuntimeError("git must be available on PATH")
        report["source_binding"] = "declared-only"
        if options.rust_binary:
            rust_binary = options.rust_binary.resolve(strict=True)
            go_binary = options.go_binary.resolve(strict=True)
            provenance = "caller-provided binaries; source refs are reported, not enforced"
        else:
            cargo, rustc, go, git = (shutil.which(tool) for tool in ("cargo", "rustc", "go", "git"))
            if not all((cargo, rustc, go, git)):
                raise RuntimeError("cargo, rustc, go, and git must be available on PATH")
            build_env = room_rollback.build_environment(temp, Path(rustc))
            rust_binary, go_binary, sources = build_binaries(
                root, temp, go_revision, rust_revision, build_env, git, go, cargo, worktrees
            )
            provenance = "built from the reported source commits"
            report["source_binding"] = "enforced"
            report["sources"]["rust"]["sha256"] = sources["rust"]["sha256"]
            report["sources"]["go_fallback"]["sha256"] = sources["go_fallback"]["sha256"]
            report["sources"]["go_fallback"]["excluded_paths"] = sources["go_fallback"]["exclusions"]
        report["binaries"] = {
            "provenance": provenance,
            "rust": {"sha256": sha256(rust_binary)},
            "go_fallback": {"sha256": sha256(go_binary)},
        }

        vault = temp / "vault"
        runtime_home = temp / "runtime-home"
        runtime_env = {
            "HOME": str(runtime_home),
            "USERPROFILE": str(runtime_home),
            "XDG_DATA_HOME": str(temp / "runtime-data"),
            "XDG_CONFIG_HOME": str(temp / "runtime-config"),
            "XDG_CACHE_HOME": str(temp / "runtime-cache"),
            "TMPDIR": str(temp / "runtime-tmp"),
            "TEMP": str(temp / "runtime-tmp"),
            "TMP": str(temp / "runtime-tmp"),
            "PATH": os.environ.get("PATH", ""),
            "TZ": "UTC",
        }
        if os.name == "nt":
            runtime_env["SystemRoot"] = os.environ.get("SystemRoot", r"C:\Windows")
            runtime_env["WINDIR"] = runtime_env["SystemRoot"]
        for directory in (runtime_home, Path(runtime_env["XDG_DATA_HOME"]),
                          Path(runtime_env["XDG_CONFIG_HOME"]), Path(runtime_env["XDG_CACHE_HOME"]),
                          Path(runtime_env["TMPDIR"])):
            directory.mkdir(parents=True, exist_ok=True)
        vault.mkdir()
        query = ["dataset", "query", "rollback", "--columns", "identity,event_id,amount"]

        run(rust_binary, vault, [
            "dataset", "sync", "rollback", "--identity-field", "event_id",
            "--rows", '[{"identity":"rust-seed","values":{"event_id":"rust-seed","amount":1}}]',
            "--provenance", '{"source_name":"rust-seed","source_sha256":"rust-seed-v1","imported_at":"2026-01-01T00:00:00Z"}',
        ], "Rust seed write", runtime_env)
        report["steps"].append({"name": "Rust seed write", "result": "PASS"})
        go_seed = dataset_identities(run(go_binary, vault, query, "historical Go reads Rust dataset", runtime_env))
        if go_seed != {"rust-seed"}:
            raise RuntimeError(f"historical Go saw identities {sorted(go_seed)}, want ['rust-seed']")
        report["steps"].append({"name": "historical Go reads Rust dataset", "result": "PASS"})

        run(go_binary, vault, [
            "dataset", "sync", "rollback", "--identity-field", "event_id",
            "--rows", '[{"identity":"go-write","values":{"event_id":"go-write","amount":2}}]',
            "--source-name", "go-write", "--source-sha256", "go-write-v1",
            "--imported-at", "2026-01-02T00:00:00Z",
        ], "historical Go mutation", runtime_env)
        report["steps"].append({"name": "historical Go mutation", "result": "PASS"})

        run(rust_binary, vault, [
            "dataset", "sync", "rollback", "--identity-field", "event_id",
            "--rows", '[{"identity":"rust-return","values":{"event_id":"rust-return","amount":3}}]',
            "--provenance", '{"source_name":"rust-return","source_sha256":"rust-return-v1","imported_at":"2026-01-03T00:00:00Z"}',
        ], "Rust reopen and mutation", runtime_env)
        report["steps"].append({"name": "Rust reopens and mutates Go dataset", "result": "PASS"})
        got = dataset_identities(run(rust_binary, vault, query, "Rust reads mixed-version dataset", runtime_env))
        want = {"rust-seed", "go-write", "rust-return"}
        if got != want:
            raise RuntimeError(f"Rust saw identities {sorted(got)}, want {sorted(want)}")
        report["steps"].append({"name": "Rust reads mixed-version dataset", "result": "PASS", "rows": len(got)})
        report["result"] = "PASS"
    except BaseException as error:
        failure = error
        report["failure"] = str(error)
    finally:
        cleanup = room_rollback.cleanup_worktrees(root, worktrees, build_env or os.environ.copy(), git) if worktrees and git else []
        if cleanup:
            report["cleanup"] = {"result": "FAIL", "workspace": str(temp), "details": cleanup}
            failure = failure or RuntimeError("temporary source cleanup failed")
            report["result"] = "FAIL"
        else:
            try:
                room_rollback.remove_temp_tree(temp)
                report["cleanup"] = {"result": "PASS"}
            except OSError as error:
                report["cleanup"] = {"result": "FAIL", "workspace": str(temp), "details": [str(error)]}
                failure = failure or error
                report["result"] = "FAIL"

    encoded = json.dumps(report, indent=2, sort_keys=True) + "\n"
    if report["result"] == "PASS":
        validate_report(report)
    if options.report:
        options.report.write_text(encoded)
    print(encoded, end="")
    if failure:
        raise RuntimeError(str(failure))


if __name__ == "__main__":
    main()
