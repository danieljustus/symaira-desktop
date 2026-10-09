#!/usr/bin/env python3
"""Capture raw Go/Rust index-status CLI observations in disposable roots.

This is a focused local harness, not the shared Rust-port fixture registry.
All binary stdout/stderr are retained verbatim. A local loopback-only mock
endpoint is used only for deterministic deadline controls; provider credentials
and ambient HOME/XDG state are never inherited.
"""

from __future__ import annotations

import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import threading
import time
from typing import Any

WORKTREE = Path(__file__).resolve().parents[3]
STATES = ("queued", "indexing", "indexed", "failed", "encrypted", "unsupported")
FIXED_TIME = "2026-01-02T03:04:05Z"


class SlowHandler(http.server.BaseHTTPRequestHandler):
    delay = 0.0

    def do_POST(self) -> None:  # noqa: N802 - stdlib handler API
        _ = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        time.sleep(self.delay)
        body = b'{"error":"controlled local timeout probe"}\n'
        self.send_response(503)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def log_message(self, format: str, *args: object) -> None:
        _ = (format, args)
        return


class LoopbackServer:
    def __init__(self, delay: float) -> None:
        handler = type(f"SlowHandler{int(delay * 1000)}", (SlowHandler,), {"delay": delay})
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)

    @property
    def url(self) -> str:
        return f"http://127.0.0.1:{self.server.server_address[1]}/api/embeddings"

    def __enter__(self) -> "LoopbackServer":
        self.thread.start()
        return self

    def __exit__(self, *_exc: object) -> None:
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=2)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def write_config(home: Path, endpoint: str = "http://127.0.0.1:1/api/embeddings") -> None:
    config = home / ".config" / "symseek" / "config.toml"
    config.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    config.write_text(
        f'ollama_url = "{endpoint}"\n'
        'model = "qwen3-embedding:0.6b"\n'
        "embedding_dim = 768\n"
        "timeout_seconds = 2\n"
        "retry_count = 0\n"
        "retry_backoff_ms = 1\n",
        encoding="utf-8",
    )
    config.chmod(0o600)


def prepare_world(base: Path, role: str, populated: bool) -> dict[str, Path]:
    world = base / role
    if world.exists():
        raise RuntimeError(f"refusing to overwrite existing disposable world: {world}")
    home = world / "home"
    xdg_data = world / "xdg-data"
    xdg_config = world / "xdg-config"
    temp = world / "temp"
    vault = world / "vault"
    for directory in (home, xdg_data, xdg_config, temp, vault):
        directory.mkdir(parents=True, mode=0o700)
    write_config(home)
    if populated:
        for state in STATES:
            note = vault / "states" / f"{state}.md"
            note.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            note.write_text(f"# {state}\n\nDeterministic status input for {state}.\n", encoding="utf-8")
            note.chmod(0o600)
        for name, body in (
            ("alpha.md", "# Alpha\n\nA deterministic retrieval status document.\n"),
            ("beta.md", "# Beta\n\nA second deterministic retrieval status document.\n"),
        ):
            path = vault / name
            path.write_text(body, encoding="utf-8")
            path.chmod(0o600)
        unsupported = vault / "opaque.bin"
        unsupported.write_bytes(b"not a document-format fixture; status listing only\n")
        unsupported.chmod(0o600)
    return {"world": world, "home": home, "xdg_data": xdg_data, "xdg_config": xdg_config, "temp": temp, "vault": vault}


def isolated_env(paths: dict[str, Path]) -> dict[str, str]:
    return {
        "PATH": "/opt/homebrew/bin:/usr/bin:/bin",
        "HOME": str(paths["home"]),
        "XDG_DATA_HOME": str(paths["xdg_data"]),
        "XDG_CONFIG_HOME": str(paths["xdg_config"]),
        "TMPDIR": str(paths["temp"]),
        "LANG": "C.UTF-8",
    }


def record_run(
    *,
    case_id: str,
    role: str,
    binary: Path,
    args: list[str],
    env: dict[str, str],
    output_dir: Path,
    manifest: dict[str, Any],
    expected_exit: int | None = None,
    status_case: bool = True,
) -> subprocess.CompletedProcess[bytes]:
    argv = [str(binary), *args]
    started = time.monotonic()
    result = subprocess.run(argv, cwd=WORKTREE, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    elapsed = time.monotonic() - started
    stem = f"{role}--{case_id}"
    stdout_path = output_dir / f"{stem}.stdout.bin"
    stderr_path = output_dir / f"{stem}.stderr.bin"
    stdout_path.write_bytes(result.stdout)
    stderr_path.write_bytes(result.stderr)
    if status_case:
        manifest["executed_case_ids"].append(case_id if role == "go" else f"{role}:{case_id}")
    manifest["runs"].append(
        {
            "case_id": case_id,
            "role": role,
            "argv": argv,
            "binary_path": str(binary),
            "binary_sha256": sha256(binary),
            "cwd": str(WORKTREE),
            "environment_paths": {key: env[key] for key in ("HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "TMPDIR")},
            "exit_code": result.returncode,
            "expected_exit_code": expected_exit,
            "elapsed_seconds": elapsed,
            "stdout_file": stdout_path.name,
            "stderr_file": stderr_path.name,
            "stdout_sha256": hashlib.sha256(result.stdout).hexdigest(),
            "stderr_sha256": hashlib.sha256(result.stderr).hexdigest(),
        }
    )
    if expected_exit is not None and result.returncode != expected_exit:
        raise RuntimeError(f"{role}:{case_id} exited {result.returncode}, expected {expected_exit}; see {stdout_path} and {stderr_path}")
    return result


def seed_go_index(binary: Path, role: str, paths: dict[str, Path], output_dir: Path, manifest: dict[str, Any]) -> None:
    vault = paths["vault"]
    env = isolated_env(paths)
    result = record_run(
        case_id="seed_index",
        role=role,
        binary=binary,
        args=["--json", "--vault", str(vault), "index", str(vault)],
        env=env,
        output_dir=output_dir,
        manifest=manifest,
        expected_exit=0,
        status_case=False,
    )
    if not result.stdout.strip():
        raise RuntimeError(f"{role}: Go index seed emitted no stdout")
    if role != "go" and role != "rust":
        raise RuntimeError(f"unknown role {role}")
    if (vault / "alpha.md").exists():
        sidecars = list((paths["xdg_data"] / "symdesk" / "vaults").glob("*/sidecar.db"))
        if len(sidecars) != 1:
            raise RuntimeError(f"expected exactly one sidecar database, found {sidecars}")
        with sqlite3.connect(sidecars[0]) as connection:
            for state in STATES:
                note = str(vault / "states" / f"{state}.md")
                reason = f"deterministic {state} state input" if state != "indexed" else ""
                connection.execute(
                    "INSERT INTO index_lifecycle(path,state,reason,updated_at) VALUES(?,?,?,?) "
                    "ON CONFLICT(path) DO UPDATE SET state=excluded.state,reason=excluded.reason,updated_at=excluded.updated_at",
                    (note, state, reason, FIXED_TIME),
                )
            connection.execute("UPDATE index_lifecycle SET updated_at = ?", (FIXED_TIME,))
            connection.commit()
        retrieval = paths["xdg_data"] / "symdesk" / "retrieval.db"
        if not retrieval.is_file():
            raise RuntimeError(f"Go index did not create the shared retrieval DB: {retrieval}")
        with sqlite3.connect(retrieval) as connection:
            connection.execute("UPDATE documents SET updated_at = ?", (FIXED_TIME,))
            connection.commit()


def run_status_cases(binary: Path, role: str, paths: dict[str, Path], output_dir: Path, manifest: dict[str, Any], populated: bool) -> None:
    env = isolated_env(paths)
    vault = str(paths["vault"])

    def capture(case_id: str, args: list[str], expected: int = 0) -> None:
        record_run(case_id=case_id, role=role, binary=binary, args=args, env=env, output_dir=output_dir, manifest=manifest, expected_exit=expected)

    if not populated:
        capture("aggregate_empty_default_json", ["--json", "index", "status"])
        capture("aggregate_empty_default_text", ["index", "status"])
        capture("aggregate_empty_with_vault_json", ["--json", "--vault", vault, "index", "status"])
        capture("aggregate_empty_with_vault_text", ["--vault", vault, "index", "status"])
        capture("documents_empty_json", ["--json", "--vault", vault, "index", "status", "--documents"])
        capture("documents_empty_text", ["--vault", vault, "index", "status", "--documents"])
        return

    capture("aggregate_populated_json", ["--json", "--vault", vault, "index", "status"])
    capture("aggregate_populated_text", ["--vault", vault, "index", "status"])
    capture("documents_populated_json", ["--json", "--vault", vault, "index", "status", "--documents"])
    capture("documents_populated_text", ["--vault", vault, "index", "status", "--documents"])
    for state in STATES:
        capture(f"documents_state_{state}_json", ["--json", "--vault", vault, "index", "status", "--documents", "--state", state])

    capture("documents_invalid_state_json", ["--json", "--vault", vault, "index", "status", "--documents", "--state", "bogus"], 1)
    capture("documents_missing_vault_json", ["--json", "index", "status", "--documents"], 1)
    capture("aggregate_missing_vault_json", ["--json", "--vault", str(paths["world"] / "absent-vault"), "index", "status"], 1)
    capture("timeout_negative_json", ["--json", "--vault", vault, "index", "status", "--timeout", "-1ms"], 1)

    # A controlled localhost endpoint makes the zero-deadline case observable
    # without consulting any host provider. The response is delayed then fails,
    # so status succeeds through the real local-hash fallback.
    with LoopbackServer(0.3) as server:
        write_config(paths["home"], server.url)
        before = time.monotonic()
        capture("timeout_zero_delayed_local_provider_json", ["--json", "--vault", vault, "index", "status", "--timeout", "0s"])
        elapsed = time.monotonic() - before
        manifest["runs"][-1]["assertion"] = "explicit zero completed after controlled 300ms loopback delay"
        manifest["runs"][-1]["elapsed_seconds"] = elapsed
        if elapsed < 0.2:
            raise RuntimeError(f"{role}: timeout zero did not wait for the delayed loopback response: {elapsed:.3f}s")

    # The parent deadline must kill the worker while a local-only provider call
    # is blocked. This is independent of the exact provider response.
    with LoopbackServer(2.0) as server:
        write_config(paths["home"], server.url)
        before = time.monotonic()
        capture("timeout_blocked_local_provider_json", ["--json", "--vault", vault, "index", "status", "--timeout", "150ms"], 1)
        elapsed = time.monotonic() - before
        manifest["runs"][-1]["assertion"] = "150ms deadline bounded blocked loopback provider call"
        manifest["runs"][-1]["elapsed_seconds"] = elapsed
        if elapsed >= 1.5:
            raise RuntimeError(f"{role}: blocked provider call exceeded safety bound: {elapsed:.3f}s")

    # An invalid SQLite file exercises the retrieval-open error path. Keep the
    # malformed input strictly inside this disposable HOME and restore config.
    invalid = paths["world"] / "invalid-index.db"
    invalid.write_bytes(b"not a SQLite database\n")
    config = paths["home"] / ".config" / "symseek" / "config.toml"
    config.write_text(
        f'ollama_url = "http://127.0.0.1:1/api/embeddings"\nmodel = "qwen3-embedding:0.6b"\n'
        f'embedding_dim = 768\ntimeout_seconds = 2\nretry_count = 0\nindex_path = "{invalid}"\n',
        encoding="utf-8",
    )
    capture("aggregate_invalid_retrieval_db_json", ["--json", "index", "status"], 1)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--go-bin", required=True, type=Path)
    parser.add_argument("--rust-bin", type=Path)
    parser.add_argument("--world-root", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--label", required=True)
    args = parser.parse_args()

    go_bin = args.go_bin.resolve(strict=True)
    rust_bin = args.rust_bin.resolve(strict=True) if args.rust_bin else None
    if args.output.exists():
        parser.error(f"refusing to overwrite existing evidence directory: {args.output}")
    if args.world_root.exists():
        parser.error(f"refusing to overwrite existing disposable-world directory: {args.world_root}")
    args.output.mkdir(parents=True, mode=0o700)
    args.world_root.mkdir(parents=True, mode=0o700)
    declared = [
        "aggregate_empty_default_json", "aggregate_empty_default_text",
        "aggregate_empty_with_vault_json", "aggregate_empty_with_vault_text",
        "documents_empty_json", "documents_empty_text",
        "aggregate_populated_json", "aggregate_populated_text",
        "documents_populated_json", "documents_populated_text",
        *(f"documents_state_{state}_json" for state in STATES),
        "documents_invalid_state_json", "documents_missing_vault_json",
        "aggregate_missing_vault_json", "timeout_negative_json",
        "timeout_zero_delayed_local_provider_json", "timeout_blocked_local_provider_json",
        "aggregate_invalid_retrieval_db_json",
    ]
    manifest: dict[str, Any] = {
        "schema_version": 1,
        "purpose": "focused index-status raw observation capture; not shared fixture inventory",
        "label": args.label,
        "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=WORKTREE, text=True).strip(),
        "canonical_oracle_commit": "191100811b7e61a0b43d21bd80963281c4c9cf8c",
        "canonical_oracle_is_ancestor": subprocess.run(
            ["git", "merge-base", "--is-ancestor", "191100811b7e61a0b43d21bd80963281c4c9cf8c", "HEAD"], cwd=WORKTREE, check=False
        ).returncode == 0,
        "go_binary": {"path": str(go_bin), "sha256": sha256(go_bin)},
        "rust_binary": {"path": str(rust_bin), "sha256": sha256(rust_bin)} if rust_bin else None,
        "declared_case_ids": declared,
        "executed_case_ids": [],
        "normalization_policy": "raw files are never normalized; any differential comparator may map only exact recorded HOME/XDG/vault path prefixes and Go pointer-address fields",
        "runs": [],
        "worlds": {},
    }

    roles = [("go", go_bin)]
    if rust_bin:
        roles.append(("rust", rust_bin))
    for role, binary in roles:
        for populated in (False, True):
            name = "populated" if populated else "empty"
            paths = prepare_world(args.world_root, f"{role}-{name}", populated)
            manifest["worlds"][f"{role}-{name}"] = {key: str(value) for key, value in paths.items()}
            if populated:
                # Both binaries inspect databases seeded by the actual Go
                # production index command; only status execution differs.
                seed_go_index(go_bin, role, paths, args.output, manifest)
            run_status_cases(binary, role, paths, args.output, manifest, populated)

    executed = manifest["executed_case_ids"]
    expected = len(declared) * len(roles)
    if len(executed) != expected:
        raise RuntimeError(f"case count mismatch: declared {expected} executions, ran {len(executed)}")
    manifest_path = args.output / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    manifest_path.chmod(0o600)
    print(json.dumps({"manifest": str(manifest_path), "declared_case_ids": len(declared), "executed_case_ids": len(executed), "roles": [role for role, _ in roles]}, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
