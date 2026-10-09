#!/usr/bin/env python3
"""Capture raw Go/Rust index-status CLI observations in disposable roots.

Every run writes stdout/stderr bytes and its manifest row before evaluating an
exit or case assertion. Captures produced here are diagnostic replays unless a
separate clean-build gate explicitly upgrades them.
"""

from __future__ import annotations

import argparse
import hashlib
import http.server
import json
import os
import platform
import signal
import sqlite3
import subprocess
import threading
import time
from contextlib import closing
from pathlib import Path
from typing import Any

from compare import (
    CANONICAL_ORACLE_COMMIT,
    DURATION_CASES,
    TIMESTAMP_CASES,
    WORKER_CASE_IDS,
    case_argv,
    REQUIRED_CASE_IDS,
    FULL_REVISION,
    SHA256,
    inventory_sha256,
)

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


class RetryProbeHandler(http.server.BaseHTTPRequestHandler):
    def do_POST(self) -> None:  # noqa: N802 - stdlib handler API
        _ = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.server.probe_count += 1  # type: ignore[attr-defined]
        if self.server.probe_count == 1:  # type: ignore[attr-defined]
            status = 503
            body = b'{"error":"first controlled response is unavailable"}\n'
        else:
            status = 200
            body = json.dumps({"data": [{"embedding": [0.0] * 768}]}).encode("utf-8")
        self.server.response_codes.append(status)  # type: ignore[attr-defined]
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def log_message(self, format: str, *args: object) -> None:
        _ = (format, args)


class EmbeddingSuccessHandler(http.server.BaseHTTPRequestHandler):
    def do_POST(self) -> None:  # noqa: N802 - stdlib handler API
        _ = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        body = json.dumps({"data": [{"embedding": [0.0] * 768}]}).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, format: str, *args: object) -> None:
        _ = (format, args)


class LoopbackServer:
    def __init__(self, handler: type[http.server.BaseHTTPRequestHandler], *, delay: float = 0.0, probe_count: int = 0) -> None:
        selected = type(f"{handler.__name__}_{int(delay * 1000)}", (handler,), {"delay": delay})
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), selected)
        self.server.daemon_threads = True
        self.server.probe_count = probe_count  # type: ignore[attr-defined]
        self.server.response_codes = []  # type: ignore[attr-defined]
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
        if self.thread.is_alive():
            raise RuntimeError("loopback server thread did not stop within two seconds")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def file_identity(path: Path, *, role: str, source_commit: str, provenance: str) -> dict[str, Any]:
    resolved = path.resolve(strict=True)
    metadata = resolved.stat()
    if not resolved.is_file() or metadata.st_size <= 0 or not os.access(resolved, os.X_OK):
        raise RuntimeError(f"{role} binary is not a non-empty executable regular file: {resolved}")
    return {
        "role": role,
        "path": str(resolved),
        "sha256": sha256_file(resolved),
        "size_bytes": metadata.st_size,
        "build_source_commit": source_commit,
        "build_provenance": provenance,
    }


def write_config(
    home: Path,
    endpoint: str = "http://127.0.0.1:1/api/embeddings",
    *,
    retry_count: int = 0,
) -> None:
    config = home / ".config" / "symseek" / "config.toml"
    config.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    config.write_text(
        f'ollama_url = "{endpoint}"\n'
        'model = "qwen3-embedding:0.6b"\n'
        "embedding_dim = 768\n"
        "timeout_seconds = 2\n"
        f"retry_count = {retry_count}\n"
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
    appdata = world / "appdata"
    local_appdata = world / "local-appdata"
    for directory in (home, xdg_data, xdg_config, temp, vault, appdata, local_appdata):
        directory.mkdir(parents=True, mode=0o700)
    (world / "no-providers").mkdir(mode=0o700)
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
    return {
        "world": world,
        "home": home,
        "xdg_data": xdg_data,
        "xdg_config": xdg_config,
        "temp": temp,
        "vault": vault,
        "appdata": appdata,
        "local_appdata": local_appdata,
    }


def isolated_env(paths: dict[str, Path], binary: Path) -> dict[str, str]:
    system = platform.system().lower()
    path_entries = [str(paths["world"] / "no-providers")]
    env: dict[str, str] = {
        "HOME": str(paths["home"]),
        "USERPROFILE": str(paths["home"]),
        "XDG_DATA_HOME": str(paths["xdg_data"]),
        "XDG_CONFIG_HOME": str(paths["xdg_config"]),
        "TMPDIR": str(paths["temp"]),
        "TEMP": str(paths["temp"]),
        "TMP": str(paths["temp"]),
        "APPDATA": str(paths["appdata"]),
        "LOCALAPPDATA": str(paths["local_appdata"]),
        "LANG": "C.UTF-8",
        "LC_ALL": "C.UTF-8",
        "TZ": "UTC",
        "HTTP_PROXY": "",
        "HTTPS_PROXY": "",
        "ALL_PROXY": "",
        "http_proxy": "",
        "https_proxy": "",
        "all_proxy": "",
        "NO_PROXY": "*",
        "no_proxy": "*",
    }
    if system.startswith("win"):
        system_root = os.environ.get("SystemRoot", r"C:\Windows")
        env["SystemRoot"] = system_root
    env["PATH"] = os.pathsep.join(dict.fromkeys(path_entries))
    return env


def _terminate_process_tree(process: subprocess.Popen[bytes], env: dict[str, str]) -> str:
    """Bound cleanup of the isolated child process tree on supported hosts."""
    if os.name == "nt":
        try:
            subprocess.run(
                [str(Path(env.get("SystemRoot", env.get("SYSTEMROOT", r"C:\Windows"))) / "System32/taskkill.exe"), "/PID", str(process.pid), "/T", "/F"],
                env=env,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=2,
                check=False,
            )
        except (OSError, subprocess.TimeoutExpired):
            if process.poll() is None:
                process.kill()
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=2)
        return "windows-taskkill-tree"

    try:
        os.killpg(process.pid, 0)
    except ProcessLookupError:
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=2)
        return "posix-process-group"
    except OSError:
        pass
    try:
        os.killpg(process.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    except OSError:
        if process.poll() is None:
            process.terminate()
    time.sleep(0.05)
    try:
        os.killpg(process.pid, signal.SIGKILL)
    except ProcessLookupError:
        pass
    except OSError:
        if process.poll() is None:
            process.kill()
    try:
        process.wait(timeout=2)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=2)
    return "posix-process-group"


def persist_manifest(output_dir: Path, manifest: dict[str, Any]) -> None:
    destination = output_dir / "manifest.json"
    temporary = output_dir / ".manifest.json.tmp"
    encoded = (json.dumps(manifest, indent=2, sort_keys=True) + "\n").encode("utf-8")
    with temporary.open("wb") as stream:
        stream.write(encoded)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, destination)
    try:
        destination.chmod(0o600)
    except OSError:
        pass


def record_run(
    *,
    case_id: str,
    role: str,
    binary: Path,
    args: list[str],
    paths: dict[str, Path],
    world_key: str,
    output_dir: Path,
    manifest: dict[str, Any],
    expected_exit: int | None = 0,
    status_case: bool = True,
    timeout_seconds: float = 10.0,
    extra_metadata: dict[str, Any] | None = None,
) -> dict[str, Any]:
    argv = [str(binary), *args]
    env = isolated_env(paths, binary)
    started = time.monotonic()
    stem = f"{role}--{case_id}--{world_key}" if case_id == "seed_index" else f"{role}--{case_id}"
    stdout_path = output_dir / f"{stem}.stdout.bin"
    stderr_path = output_dir / f"{stem}.stderr.bin"
    timed_out = False
    launch_error: str | None = None
    return_code: int | None = None
    cleanup_method = "not-started"
    try:
        with stdout_path.open("xb") as stdout_stream, stderr_path.open("xb") as stderr_stream:
            creationflags = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0) if os.name == "nt" else 0
            try:
                process = subprocess.Popen(
                    argv,
                    cwd=WORKTREE,
                    env=env,
                    stdin=subprocess.DEVNULL,
                    stdout=stdout_stream,
                    stderr=stderr_stream,
                    start_new_session=(os.name != "nt"),
                    creationflags=creationflags,
                    close_fds=True,
                )
            except OSError as exc:
                launch_error = f"{type(exc).__name__}: {exc}"
            else:
                try:
                    return_code = process.wait(timeout=timeout_seconds)
                except subprocess.TimeoutExpired:
                    timed_out = True
                    cleanup_method = _terminate_process_tree(process, env)
                    return_code = process.returncode
                else:
                    cleanup_method = _terminate_process_tree(process, env)
            stdout_stream.flush()
            stderr_stream.flush()
    except BaseException:
        # File handles are also closed by the context manager; keep already
        # created raw observations rather than deleting evidence on failure.
        raise
    elapsed = time.monotonic() - started
    stdout = stdout_path.read_bytes() if stdout_path.exists() else b""
    stderr = stderr_path.read_bytes() if stderr_path.exists() else b""
    expected_ok = expected_exit is not None and return_code == expected_exit
    success = launch_error is None and not timed_out and expected_ok
    run: dict[str, Any] = {
        "case_id": case_id,
        "role": role,
        "status_case": status_case,
        "argv": argv,
        "binary_path": str(binary.resolve()),
        "binary_sha256": sha256_file(binary),
        "cwd": str(WORKTREE),
        "world_key": world_key,
        "environment": env,
        "platform": manifest["platform"],
        "exit_code": return_code,
        "expected_exit_code": expected_exit,
        "timed_out": timed_out,
        "success": success,
        "launch_error": launch_error,
        "elapsed_seconds": elapsed,
        "process_cleanup": cleanup_method,
        "stdout_file": stdout_path.name,
        "stdout_size_bytes": len(stdout),
        "stdout_sha256": hashlib.sha256(stdout).hexdigest(),
        "stderr_file": stderr_path.name,
        "stderr_size_bytes": len(stderr),
        "stderr_sha256": hashlib.sha256(stderr).hexdigest(),
    }
    if extra_metadata:
        run.update(extra_metadata)
    if status_case:
        manifest["executed_case_ids"].append(case_id if role == "go" else f"rust:{case_id}")
    manifest["runs"].append(run)
    persist_manifest(output_dir, manifest)
    if not success:
        reason = launch_error or ("process timed out" if timed_out else f"exit {return_code}, expected {expected_exit}")
        raise RuntimeError(f"{role}:{case_id} failed ({reason}); raw output retained at {stdout_path} and {stderr_path}")
    return run


def seed_go_index(
    go_binary: Path,
    paths: dict[str, Path],
    world_key: str,
    output_dir: Path,
    manifest: dict[str, Any],
) -> None:
    vault = paths["vault"]
    run = record_run(
        case_id="seed_index",
        role="go",
        binary=go_binary,
        args=["--json", "--vault", str(vault), "index", str(vault)],
        paths=paths,
        world_key=world_key,
        output_dir=output_dir,
        manifest=manifest,
        expected_exit=0,
        status_case=False,
    )
    stdout_path = output_dir / run["stdout_file"]
    if not stdout_path.read_bytes().strip():
        run["success"] = False
        persist_manifest(output_dir, manifest)
        raise RuntimeError(f"Go index seed emitted no stdout; raw output retained at {stdout_path}")

    sidecars = list((paths["xdg_data"] / "symdesk" / "vaults").glob("*/sidecar.db"))
    if len(sidecars) != 1:
        raise RuntimeError(f"expected exactly one sidecar database, found {sidecars}")
    with closing(sqlite3.connect(sidecars[0])) as connection:
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
    with closing(sqlite3.connect(retrieval)) as connection:
        connection.execute("UPDATE documents SET updated_at = ?", (FIXED_TIME,))
        connection.commit()


def add_lifecycle_counterexamples(paths: dict[str, Path]) -> None:
    vault = paths["vault"]
    sidecars = list((paths["xdg_data"] / "symdesk" / "vaults").glob("*/sidecar.db"))
    if len(sidecars) != 1:
        raise RuntimeError(f"expected one sidecar before lifecycle probes, found {sidecars}")
    html_name = "html-unicode.md"
    records = (
        ("html", html_name, "indexed", "HTML <>& plus \u2028 and \u2029"),
        ("invalid_utf8", "invalid-utf8.md", "failed", b"truncated-utf8-\xf0\x9f"),
        ("valid_replacement", "valid-replacement.md", "queued", "valid-utf8-\ufffd".encode("utf-8")),
    )
    with closing(sqlite3.connect(sidecars[0])) as connection:
        for name, filename, state, reason in records:
            note = vault / "states" / filename
            note.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            note.write_text(f"# lifecycle {name}\n", encoding="utf-8")
            note.chmod(0o600)
            if name == "html":
                connection.execute(
                    "INSERT INTO index_lifecycle(path,state,reason,updated_at) VALUES(?,?,?,?) "
                    "ON CONFLICT(path) DO UPDATE SET state=excluded.state,reason=excluded.reason,updated_at=excluded.updated_at",
                    (str(note), state, reason, FIXED_TIME),
                )
            else:
                if not isinstance(reason, bytes):
                    raise TypeError(f"binary lifecycle reason is not bytes: {name}")
                # CAST keeps these test bytes in SQLite TEXT storage; BLOB
                # storage would exercise a different JSON boundary.
                connection.execute(
                    "INSERT INTO index_lifecycle(path,state,reason,updated_at) VALUES(?,?,CAST(? AS TEXT),?) "
                    "ON CONFLICT(path) DO UPDATE SET state=excluded.state,reason=excluded.reason,updated_at=excluded.updated_at",
                    (str(note), state, sqlite3.Binary(reason), FIXED_TIME),
                )
        connection.commit()


def run_duration_cases(
    binary: Path,
    role: str,
    paths: dict[str, Path],
    world_key: str,
    output_dir: Path,
    manifest: dict[str, Any],
) -> None:
    vault = str(paths["vault"])
    for case_id, mode, duration, expected_exit in DURATION_CASES:
        args = (["--json"] if mode == "json" else []) + [
            "--vault", vault, "index", "status", "--documents", "--timeout", duration
        ]
        record_run(
            case_id=case_id,
            role=role,
            binary=binary,
            args=args,
            paths=paths,
            world_key=world_key,
            output_dir=output_dir,
            manifest=manifest,
            expected_exit=expected_exit,
            extra_metadata={"duration_mode": mode, "duration_input": duration},
        )


def run_timestamp_and_worker_cases(binary: Path, role: str, paths: dict[str, Path], world_key: str,
                                   output_dir: Path, manifest: dict[str, Any]) -> None:
    retrieval = paths["xdg_data"] / "symdesk" / "retrieval.db"
    for case_id, mode, value, storage in TIMESTAMP_CASES:
        with closing(sqlite3.connect(retrieval)) as connection:
            if storage == "hex-text":
                connection.execute("UPDATE documents SET updated_at = CAST(? AS TEXT)",
                                   (sqlite3.Binary(bytes.fromhex(value)),))
            else:
                bound = sqlite3.Binary(value.encode("utf-8")) if storage == "blob" else value
                connection.execute("UPDATE documents SET updated_at = ?", (bound,))
            connection.commit()
            observed = connection.execute("SELECT DISTINCT hex(CAST(updated_at AS BLOB)) FROM documents").fetchall()
        if len(observed) != 1:
            raise RuntimeError(f"timestamp input was not stored uniformly: {case_id}: {observed}")
        record_run(case_id=case_id, role=role, binary=binary,
                   args=case_argv(case_id, str(binary), {key: str(path) for key, path in paths.items()})[1:],
                   paths=paths, world_key=world_key, output_dir=output_dir, manifest=manifest,
                   extra_metadata={"timestamp_mode": mode, "timestamp_input": value,
                                   "timestamp_storage": storage, "timestamp_observed_hex": observed[0][0]})
    with closing(sqlite3.connect(retrieval)) as connection:
        connection.execute("UPDATE documents SET updated_at = ?", (FIXED_TIME,))
        connection.commit()
    for case_id in WORKER_CASE_IDS:
        record_run(case_id=case_id, role=role, binary=binary,
                   args=case_argv(case_id, str(binary), {key: str(path) for key, path in paths.items()})[1:],
                   paths=paths, world_key=world_key, output_dir=output_dir, manifest=manifest)


def run_status_cases(
    binary: Path,
    role: str,
    paths: dict[str, Path],
    world_key: str,
    output_dir: Path,
    manifest: dict[str, Any],
    *,
    populated: bool,
) -> None:
    vault = str(paths["vault"])

    def capture(case_id: str, args: list[str], expected: int = 0) -> dict[str, Any]:
        return record_run(
            case_id=case_id,
            role=role,
            binary=binary,
            args=args,
            paths=paths,
            world_key=world_key,
            output_dir=output_dir,
            manifest=manifest,
            expected_exit=expected,
        )

    if not populated:
        capture("aggregate_empty_default_json", ["--json", "index", "status"])
        capture("aggregate_empty_default_text", ["index", "status"])
        capture("aggregate_empty_with_vault_json", ["--json", "--vault", vault, "index", "status"])
        capture("aggregate_empty_with_vault_text", ["--vault", vault, "index", "status"])
        capture("documents_empty_json", ["--json", "--vault", vault, "index", "status", "--documents"])
        capture("documents_empty_text", ["--vault", vault, "index", "status", "--documents"])
        capture("documents_state_empty_json", ["--json", "--vault", vault, "index", "status", "--documents", "--state", ""])
        capture("documents_state_empty_text", ["--vault", vault, "index", "status", "--documents", "--state", ""])
        return

    capture("aggregate_populated_json", ["--json", "--vault", vault, "index", "status"])
    capture("aggregate_populated_text", ["--vault", vault, "index", "status"])
    capture("documents_populated_json", ["--json", "--vault", vault, "index", "status", "--documents"])
    capture("documents_populated_text", ["--vault", vault, "index", "status", "--documents"])
    for state in STATES:
        capture(f"documents_state_{state}_json", ["--json", "--vault", vault, "index", "status", "--documents", "--state", state])
    run_timestamp_and_worker_cases(binary, role, paths, world_key, output_dir, manifest)

    sidecars = list((paths["xdg_data"] / "symdesk" / "vaults").glob("*/sidecar.db"))
    if len(sidecars) != 1:
        raise RuntimeError(f"expected exactly one sidecar for the deadline control: {sidecars}")
    with closing(sqlite3.connect(sidecars[0])) as blocker:
        blocker.execute("PRAGMA journal_mode=DELETE")
        blocker.execute("BEGIN EXCLUSIVE")
        try:
            run = capture("timeout_blocked_sidecar_json", ["--json", "--vault", vault, "index", "status",
                          "--documents", "--timeout", "500ms"], 1)
            diagnostic = json.loads((output_dir / run["stdout_file"]).read_bytes())
            run["success"] = (run["success"] and run["elapsed_seconds"] < 1.5
                              and diagnostic.get("error") == "index status timed out during open sidecar database after 500ms: context deadline exceeded")
            persist_manifest(output_dir, manifest)
            if not run["success"]:
                raise RuntimeError(f"{role}: locked sidecar did not establish the bounded open-sidecar phase; raw output retained")
        finally:
            blocker.rollback()

    with LoopbackServer(EmbeddingSuccessHandler) as server:
        write_config(paths["home"], server.url)
        capture("aggregate_provider_available_json", ["--json", "--vault", vault, "index", "status"])
        capture("aggregate_provider_available_text", ["--vault", vault, "index", "status"])

    with LoopbackServer(RetryProbeHandler) as server:
        write_config(paths["home"], server.url, retry_count=1)
        run = capture("provider_retry_one_probe_json", ["--json", "--vault", vault, "index", "status"])
        run["provider_probe_count"] = server.server.probe_count  # type: ignore[attr-defined]
        run["provider_response_codes"] = list(server.server.response_codes)  # type: ignore[attr-defined]
        run["success"] = run["success"] and run["provider_probe_count"] == 1 and run["provider_response_codes"] == [503]
        persist_manifest(output_dir, manifest)
        if run["provider_probe_count"] != 1 or run["provider_response_codes"] != [503]:
            raise RuntimeError(
                f"{role} retry_count=1 expected exactly one HTTP 503 probe, observed "
                f"{run['provider_probe_count']} probe(s) with codes {run['provider_response_codes']}; raw output retained"
            )

    capture("documents_html_unicode_json", ["--json", "--vault", vault, "index", "status", "--documents"])
    capture("documents_lifecycle_invalid_utf8_json", ["--json", "--vault", vault, "index", "status", "--documents", "--state", "failed"])
    capture("documents_lifecycle_valid_replacement_json", ["--json", "--vault", vault, "index", "status", "--documents", "--state", "queued"])

    capture("documents_invalid_state_json", ["--json", "--vault", vault, "index", "status", "--documents", "--state", "bogus"], 1)
    capture("documents_missing_vault_json", ["--json", "index", "status", "--documents"], 1)
    capture("aggregate_missing_vault_json", ["--json", "--vault", str(paths["world"] / "absent-vault"), "index", "status"], 1)
    capture("timeout_negative_json", ["--json", "--vault", vault, "index", "status", "--timeout", "-1ms"], 1)

    with LoopbackServer(SlowHandler, delay=0.3) as server:
        write_config(paths["home"], server.url)
        before = time.monotonic()
        run = capture("timeout_zero_delayed_local_provider_json", ["--json", "--vault", vault, "index", "status", "--timeout", "0s"])
        elapsed = time.monotonic() - before
        run["assertion"] = "explicit zero completed after controlled 300ms loopback delay"
        run["elapsed_seconds"] = elapsed
        run["success"] = run["success"] and elapsed >= 0.2
        persist_manifest(output_dir, manifest)
        if elapsed < 0.2:
            raise RuntimeError(f"{role}: timeout zero did not wait for delayed loopback response ({elapsed:.3f}s); raw output retained")

    with LoopbackServer(SlowHandler, delay=2.0) as server:
        write_config(paths["home"], server.url)
        before = time.monotonic()
        run = capture(
            "timeout_blocked_local_provider_json",
            ["--json", "--vault", vault, "index", "status", "--timeout", "150ms"],
            1,
        )
        elapsed = time.monotonic() - before
        run["assertion"] = "150ms deadline bounded blocked loopback provider call"
        run["elapsed_seconds"] = elapsed
        run["success"] = run["success"] and elapsed < 1.5
        persist_manifest(output_dir, manifest)
        if elapsed >= 1.5:
            raise RuntimeError(f"{role}: blocked provider exceeded safety bound ({elapsed:.3f}s); raw output retained")

    invalid = paths["world"] / "invalid-index.db"
    invalid.write_bytes(b"not a SQLite database\n")
    config = paths["home"] / ".config" / "symseek" / "config.toml"
    invalid_path = json.dumps(str(invalid), ensure_ascii=False)
    config.write_text(
        f'ollama_url = "http://127.0.0.1:1/api/embeddings"\nmodel = "qwen3-embedding:0.6b"\n'
        f'embedding_dim = 768\ntimeout_seconds = 2\nretry_count = 0\nindex_path = {invalid_path}\n',
        encoding="utf-8",
    )
    capture("aggregate_invalid_retrieval_db_json", ["--json", "index", "status"], 1)
    run_duration_cases(binary, role, paths, world_key, output_dir, manifest)


def git_text(*args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(WORKTREE), *args],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=5,
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed: {result.stderr.decode('utf-8', errors='replace')}")
    return result.stdout.decode("utf-8").strip()


def platform_identity() -> dict[str, str]:
    system = platform.system().lower()
    if system == "darwin":
        system = "macos"
    elif system.startswith("win"):
        system = "windows"
    elif system.startswith("linux"):
        system = "linux"
    return {"os": system, "architecture": platform.machine().lower()}


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--go-bin", required=True, type=Path)
    parser.add_argument("--rust-bin", required=True, type=Path)
    parser.add_argument("--rust-build-source-commit", required=True)
    parser.add_argument("--expected-go-sha256", required=True)
    parser.add_argument("--expected-rust-sha256", required=True)
    parser.add_argument("--world-root", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--label", required=True)
    args = parser.parse_args()

    go_bin = args.go_bin.resolve(strict=True)
    rust_bin = args.rust_bin.resolve(strict=True)
    if go_bin == rust_bin or sha256_file(go_bin) == sha256_file(rust_bin):
        parser.error("Go and Rust binary paths and content identities must be distinct")
    if not FULL_REVISION.fullmatch(args.rust_build_source_commit):
        parser.error("Rust source revision must be a full lowercase Git revision")
    if not SHA256.fullmatch(args.expected_go_sha256) or not SHA256.fullmatch(args.expected_rust_sha256):
        parser.error("expected binary digests must be lowercase SHA-256 values")
    if sha256_file(go_bin) != args.expected_go_sha256:
        parser.error("Go binary hash does not match the independently recorded oracle build")
    if sha256_file(rust_bin) != args.expected_rust_sha256:
        parser.error("Rust binary hash does not match the independently recorded parent build")

    source_commit = git_text("rev-parse", "HEAD")
    if source_commit != args.rust_build_source_commit:
        parser.error("native differential requires a Rust build from the captured source revision")
    source_status = git_text("status", "--porcelain=v1", "--untracked-files=all")
    if source_status:
        raise RuntimeError("capture requires a clean source worktree; commit only after tests and rerun")
    git_text("cat-file", "-e", f"{args.rust_build_source_commit}^{{commit}}")
    ancestor = subprocess.run(
        ["git", "-C", str(WORKTREE), "merge-base", "--is-ancestor", CANONICAL_ORACLE_COMMIT, source_commit],
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        timeout=5,
        check=False,
    ).returncode == 0
    if not ancestor:
        raise RuntimeError("canonical Go oracle revision is not an ancestor of the captured source revision")

    for path in (args.output, args.world_root):
        if path.exists():
            parser.error(f"refusing to overwrite existing evidence or disposable-world path: {path}")
    args.output.mkdir(parents=True, mode=0o700)
    args.world_root.mkdir(parents=True, mode=0o700)

    manifest: dict[str, Any] = {
        "schema_version": 2,
        "purpose": "focused index-status native differential on clean immutable source",
        "evidence_class": "native-differential",
        "label": args.label,
        "source_commit": source_commit,
        "source_worktree": str(WORKTREE),
        "source_tree_status": [],
        "canonical_oracle_commit": CANONICAL_ORACLE_COMMIT,
        "canonical_oracle_is_ancestor": ancestor,
        "case_inventory_sha256": inventory_sha256(),
        "platform": platform_identity(),
        "roles": ["go", "rust"],
        "go_binary": file_identity(
            go_bin,
            role="go",
            source_commit=CANONICAL_ORACLE_COMMIT,
            provenance="reviewed canonical Go oracle executable",
        ),
        "rust_binary": file_identity(
            rust_bin,
            role="rust",
            source_commit=args.rust_build_source_commit,
            provenance="independently built executable, source revision and binary digest supplied by the owning build gate",
        ),
        "declared_case_ids": list(REQUIRED_CASE_IDS),
        "executed_case_ids": [],
        "normalization_policy": "raw files are retained; compare only maps exact recorded disposable world roots and Go VaultDocumentCount pointer-address fields",
        "runs": [],
        "worlds": {},
    }
    persist_manifest(args.output, manifest)

    binaries = {"go": go_bin, "rust": rust_bin}
    for role in ("go", "rust"):
        for populated in (False, True):
            name = "populated" if populated else "empty"
            world_key = f"{role}-{name}"
            paths = prepare_world(args.world_root, world_key, populated)
            manifest["worlds"][world_key] = {key: str(value) for key, value in paths.items()}
            persist_manifest(args.output, manifest)
            if populated:
                seed_go_index(go_bin, paths, world_key, args.output, manifest)
                add_lifecycle_counterexamples(paths)
            run_status_cases(
                binaries[role],
                role,
                paths,
                world_key,
                args.output,
                manifest,
                populated=populated,
            )

    if manifest["executed_case_ids"] != (
        list(REQUIRED_CASE_IDS) + [f"rust:{case_id}" for case_id in REQUIRED_CASE_IDS]
    ):
        raise RuntimeError("executed case order/count differs from the immutable reviewed inventory")
    if git_text("rev-parse", "HEAD") != source_commit or git_text("status", "--porcelain=v1", "--untracked-files=all"):
        raise RuntimeError("source changed during native capture; retained evidence cannot be accepted")
    if sha256_file(go_bin) != args.expected_go_sha256 or sha256_file(rust_bin) != args.expected_rust_sha256:
        raise RuntimeError("binary changed during native capture; retained evidence cannot be accepted")
    persist_manifest(args.output, manifest)
    print(json.dumps({
        "manifest": str(args.output / "manifest.json"),
        "evidence_class": manifest["evidence_class"],
        "declared_case_ids": len(manifest["declared_case_ids"]),
        "executed_case_ids": len(manifest["executed_case_ids"]),
        "roles": manifest["roles"],
    }, indent=2))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
