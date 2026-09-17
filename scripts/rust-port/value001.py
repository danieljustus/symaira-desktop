#!/usr/bin/env python3
"""Run the fail-closed VALUE-001 Go/Rust representative benchmark.

This runner owns the evidence boundary: the Go side is built from an isolated
clean worktree at the frozen current-behaviour oracle, while Rust is measured
from the supplied candidate binary.  The workload is deterministic, uses a
10,000-document generated vault, validates semantic results for every timed
operation, records paired/order samples and maximum RSS, and self-validates
its JSON result using only the Python standard library.
"""
from __future__ import annotations

import argparse
import datetime as dt
import gzip
import hashlib
import json
import math
import os
import platform
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any, Callable

TOKEN = "0123456789abcdef0123456789abcdef"
MIN_SAMPLES = 100
CURRENT_SAMPLES = 100
CURRENT_WARMUPS = 20
# Schema 2 predates explicit summation/estimator declarations. Schema 3 added
# those declarations and is immutable historical evidence. Schema 4 is the
# original order-stratified decision contract. Schema 5 adds the controlled
# HTTP schedule without redefining schema-4 evidence.
SCHEMA4_VERSION = 4
SCHEMA_VERSION = 5
SCHEMA2_VERSION = 2
SCHEMA3_VERSION = 3
ORIGINAL_VALUE_BASELINE = "ae86331930fdfa2b128b68ae5af7437091b9949a"
CURRENT_BEHAVIOUR_ORACLE = "745c08e8144971c61133c5d0e5d61c7ce405aad2"
DOC_COUNT = 10_000
SEARCH_TOKEN = "value001cohort042"
COHORT = 42
MCP_LS_DIR = f"cohort-{COHORT:03d}/"
HTTP_FILE_CONTENT = "---\ntitle: HTTP Probe\ncreated: 2026-01-02T03:04:05Z\n---\nvalue001 http probe\n"
SCHEMA4_PAIRING = "alternating go-rust/rust-go per post-warmup round"
HTTP_OPERATION_NAMES = (
    "healthz",
    "status",
    "snapshot",
    "file-read",
    "file-range",
    "file-missing",
    "file-traversal",
)
HTTP_CONTROL_IDLE_SECONDS = 0.005
HTTP_MEASUREMENT_PAIRING = (
    "rotating HTTP operations; per-operation go-rust/rust-go order by "
    "round plus original operation index; Connection: close per request; "
    "alternating untimed healthz priming per round; 5ms idle after each HTTP request"
)


class HarnessError(RuntimeError):
    pass


def utc_now() -> str:
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def require_executable_regular(path: Path, label: str) -> None:
    """Require an executable file whose identity can be hashed later."""
    if path.is_symlink() or not path.is_file() or not os.access(path, os.X_OK):
        raise HarnessError(f"{label} must be an executable regular file: {path}")


def durable_binary_path(output: Path, name: str) -> Path:
    """Return the local evidence location for a measured binary."""
    return output.parent / ".value001-binaries" / f"{output.stem}.{name}"


def retain_binary(source: Path, destination: Path, label: str) -> Path:
    """Copy a measured executable before its temporary build root is removed."""
    require_executable_regular(source, label)
    destination.parent.mkdir(parents=True, exist_ok=True)
    if destination.is_symlink():
        raise HarnessError(f"{label} evidence path must not be a symlink: {destination}")
    descriptor, temporary_name = tempfile.mkstemp(
        prefix=f".{destination.name}.", suffix=".tmp", dir=destination.parent
    )
    temporary = Path(temporary_name)
    try:
        with source.open("rb") as input_stream, os.fdopen(descriptor, "wb") as output_stream:
            shutil.copyfileobj(input_stream, output_stream)
            output_stream.flush()
            os.fsync(output_stream.fileno())
        os.chmod(temporary, 0o700)
        os.replace(temporary, destination)
    finally:
        temporary.unlink(missing_ok=True)
    require_executable_regular(destination, label)
    return destination


def is_lower_hex(value: Any, length: int) -> bool:
    return (
        isinstance(value, str)
        and len(value) == length
        and all(character in "0123456789abcdef" for character in value)
    )


def command_text(command: list[str]) -> str:
    return subprocess.list2cmdline(command)


def run_checked(
    command: list[str],
    root: Path,
    timeout: float,
    env: dict[str, str] | None = None,
) -> dict[str, Any]:
    started = time.perf_counter()
    try:
        completed = subprocess.run(
            command,
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise HarnessError(f"timed out after {timeout:.1f}s: {command_text(command)}") from exc
    elapsed = (time.perf_counter() - started) * 1000.0
    if completed.returncode != 0:
        raise HarnessError(
            f"command failed ({completed.returncode}): {command_text(command)}\n"
            f"stdout:\n{completed.stdout[-4000:]}\nstderr:\n{completed.stderr[-4000:]}"
        )
    return {
        "command": command_text(command),
        "elapsed_ms": elapsed,
        "exit_code": completed.returncode,
        "stdout": completed.stdout,
        "stderr": completed.stderr,
    }


def git_output(root: Path, args: list[str], check: bool = True) -> str:
    result = subprocess.run(["git", *args], cwd=root, capture_output=True, text=True, check=False)
    if check and result.returncode != 0:
        raise HarnessError(f"git {' '.join(args)} failed: {result.stderr.strip()}")
    return result.stdout.strip()


def git_bytes(root: Path, args: list[str]) -> bytes:
    result = subprocess.run(["git", *args], cwd=root, capture_output=True, check=False)
    if result.returncode != 0:
        raise HarnessError(f"git {' '.join(args)} failed: {result.stderr.decode(errors='replace').strip()}")
    return result.stdout


def tool_version(command: list[str], root: Path, env: dict[str, str] | None = None) -> str:
    result = run_checked(command, root, 30.0, env=env)
    return result["stdout"].strip()


def benchmark_env(home_root: Path, vault: Path, sidecar: Path | None = None) -> dict[str, str]:
    home = home_root / "home"
    tmp = home_root / "tmp"
    for path in (home, tmp, vault):
        path.mkdir(parents=True, exist_ok=True)
    env = {
        "HOME": str(home),
        "USERPROFILE": str(home),
        "XDG_CONFIG_HOME": str(home / ".config"),
        "XDG_DATA_HOME": str(home / ".local" / "share"),
        "XDG_CACHE_HOME": str(home / ".cache"),
        "TMPDIR": str(tmp),
        "TMP": str(tmp),
        "TEMP": str(tmp),
        "LANG": "C",
        "LC_ALL": "C",
        "TZ": "UTC",
        "TERM": "dumb",
        "NO_COLOR": "1",
        "SYMDESK_VAULT": str(vault),
        "SYMDESK_VERSION": "0.12.2",
        "PATH": os.environ.get("PATH", "/usr/bin:/bin"),
    }
    if sidecar is not None:
        env["SYMDESK_SIDECAR"] = str(sidecar)
    return env


def document_path(index: int) -> str:
    return f"cohort-{index % 100:03d}/note-{index:05d}.md"


def document_content(index: int) -> str:
    cohort = index % 100
    anchor = f"{SEARCH_TOKEN} anchor" if cohort == COHORT else "value001 ordinary corpus"
    return (
        "---\n"
        f"title: Value Gate Note {index:05d}\n"
        "created: 2026-01-02T03:04:05Z\n"
        f"status: {'open' if index % 2 == 0 else 'closed'}\n"
        f"---\n{anchor}\n"
        f"deterministic document {index:05d} cohort {cohort:03d}\n"
    )


def write_vault(vault: Path, docs: int = DOC_COUNT, include_http_probe: bool = False) -> dict[str, Any]:
    if docs < 1:
        raise HarnessError("generated vault must contain at least one document")
    vault.mkdir(parents=True, exist_ok=True)
    expected_paths: list[str] = []
    expected_titles: dict[str, str] = {}
    search_paths: list[str] = []
    for index in range(docs):
        relative = document_path(index)
        path = vault / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(document_content(index), encoding="utf-8")
        os.chmod(path, 0o600)
        expected_paths.append(relative)
        expected_titles[relative] = f"Value Gate Note {index:05d}"
        if index % 100 == COHORT:
            search_paths.append(relative)
    if include_http_probe:
        (vault / "HTTP.md").write_text(HTTP_FILE_CONTENT, encoding="utf-8")
        os.chmod(vault / "HTTP.md", 0o600)
        expected_paths.append("HTTP.md")
        expected_titles["HTTP.md"] = "HTTP Probe"
    return {
        "documents": docs,
        "bytes": sum(len(document_content(index).encode()) for index in range(docs)),
        "expected_paths": expected_paths,
        "expected_titles": expected_titles,
        "search_paths": search_paths[:20],
        "search_match_count": len(search_paths),
    }


def expected_vault_semantics(manifest: dict[str, Any], include_http_probe: bool = False) -> dict[str, Any]:
    paths = list(manifest["expected_paths"])
    # Walk/list output is lexical.  HTTP.md sorts before cohort-*.
    paths.sort()
    mcp_ls_paths = [path for path in paths if path.startswith(MCP_LS_DIR)]
    return {
        "paths": paths,
        "mcp_ls_paths": mcp_ls_paths,
        "titles": manifest["expected_titles"],
        "search_paths": sorted(manifest["search_paths"]),
        "search_match_count": manifest["search_match_count"],
        "include_http_probe": include_http_probe,
    }


def parse_json(stdout: str, label: str) -> Any:
    try:
        return json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise HarnessError(f"{label} did not emit JSON: {stdout[:500]!r}") from exc


def validate_version(stdout: str, stderr: str) -> None:
    if stdout != "symdesk 0.12.2\n" or stderr:
        raise HarnessError(f"invalid version contract: stdout={stdout!r} stderr={stderr!r}")


def validate_ls(stdout: str, stderr: str, expected: dict[str, Any], expected_paths: list[str] | None = None) -> None:
    if stderr:
        raise HarnessError(f"unexpected ls stderr: {stderr!r}")
    value = parse_json(stdout, "ls")
    if not isinstance(value, list):
        raise HarnessError(f"ls result is not an array: {type(value).__name__}")
    paths = [item.get("path") for item in value if isinstance(item, dict)]
    expected_paths = expected["paths"] if expected_paths is None else expected_paths
    if len(value) != len(expected_paths) or paths != expected_paths:
        raise HarnessError(f"ls paths mismatch: got {len(paths)} entries, expected {len(expected_paths)}")
    for item in value:
        if not isinstance(item, dict) or set(item) - {"path", "title", "type", "modified"}:
            raise HarnessError(f"invalid ls entry: {item!r}")
        path = item.get("path")
        if path not in expected["titles"] or item.get("title") != expected["titles"][path]:
            raise HarnessError(f"invalid ls semantic entry: {item!r}")
        if not isinstance(item.get("modified"), str) or not item["modified"]:
            raise HarnessError(f"missing ls modified value: {item!r}")


def validate_search(stdout: str, stderr: str, expected: dict[str, Any]) -> None:
    if stderr:
        raise HarnessError(f"unexpected search stderr: {stderr!r}")
    value = parse_json(stdout, "search")
    if not isinstance(value, dict) or set(value) != {"results"} or not isinstance(value["results"], list):
        raise HarnessError(f"invalid search envelope: {value!r}")
    results = value["results"]
    expected_paths = expected["search_paths"]
    actual_paths = [item.get("path") for item in results if isinstance(item, dict)]
    if actual_paths != expected_paths or len(results) != min(20, expected["search_match_count"]):
        raise HarnessError(f"search paths mismatch: got {actual_paths!r}, expected {expected_paths!r}")
    for item in results:
        if set(item) != {"path", "title", "snippet", "score"}:
            raise HarnessError(f"invalid search entry shape: {item!r}")
        if item["title"] != expected["titles"].get(item["path"]):
            raise HarnessError(f"invalid search title: {item!r}")
        if SEARCH_TOKEN not in item["snippet"]:
            raise HarnessError(f"search snippet omitted expected token: {item!r}")
        if item["score"] != 0:
            raise HarnessError(f"search score contract changed: {item!r}")


def mcp_request(method: str, request_id: int, name: str | None = None, arguments: dict[str, Any] | None = None) -> str:
    request: dict[str, Any] = {"jsonrpc": "2.0", "id": request_id, "method": method}
    if name is not None:
        request["params"] = {"name": name, "arguments": arguments or {}}
    return json.dumps(request, separators=(",", ":")) + "\n"


def mcp_frame(stdout: str, stderr: str, label: str) -> dict[str, Any]:
    if stderr:
        raise HarnessError(f"unexpected MCP stderr for {label}: {stderr!r}")
    lines = [line for line in stdout.splitlines() if line.strip()]
    if len(lines) != 1:
        raise HarnessError(f"{label} emitted {len(lines)} MCP frames, expected one: {stdout[:500]!r}")
    value = parse_json(lines[0], f"MCP {label}")
    if not isinstance(value, dict) or value.get("jsonrpc") != "2.0" or "id" not in value:
        raise HarnessError(f"invalid MCP frame for {label}: {value!r}")
    return value


def validate_mcp(stdout: str, stderr: str, label: str, expected: dict[str, Any], vault: Path) -> None:
    frame = mcp_frame(stdout, stderr, label)
    result = frame.get("result")
    if not isinstance(result, dict):
        raise HarnessError(f"MCP {label} has no result: {frame!r}")
    if label == "initialize":
        if result != {
            "protocolVersion": "2024-11-05",
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "symdesk", "version": "0.12.2"},
        }:
            raise HarnessError(f"initialize semantic mismatch: {result!r}")
        return
    if label == "tools-list":
        tools = result.get("tools")
        names = [item.get("name") for item in tools] if isinstance(tools, list) else []
        if names[:3] != ["desk_status", "desk_ls", "desk_search"]:
            raise HarnessError(f"representative MCP tools missing or reordered: {names[:5]!r}")
        return
    if frame.get("id") is None:
        raise HarnessError(f"MCP {label} response id missing")
    content = result.get("content")
    if result.get("isError") is not False or not isinstance(content, list) or len(content) != 1:
        raise HarnessError(f"invalid MCP tool envelope for {label}: {result!r}")
    text = content[0].get("text") if isinstance(content[0], dict) else None
    if not isinstance(text, str):
        raise HarnessError(f"MCP {label} content is not text: {result!r}")
    if label == "desk_status":
        status = parse_json(text, "MCP status")
        if status != {"version": "0.12.2", "vault": str(vault), "capabilities": "read_only"}:
            raise HarnessError(f"MCP status semantic mismatch: {status!r}")
    elif label == "desk_ls":
        validate_ls(text, "", expected, expected["mcp_ls_paths"])
    elif label == "desk_search":
        validate_search(text, "", expected)
    else:
        raise HarnessError(f"unknown timed MCP operation {label}")


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


class RunningServer:
    def __init__(self, binary: Path, home_root: Path, vault: Path, sidecar: Path) -> None:
        self.binary = binary
        self.home_root = home_root
        self.vault = vault
        self.sidecar = sidecar
        self.port = free_port()
        self.base = f"http://127.0.0.1:{self.port}"
        self.log_path = home_root / "server.log"
        self.log_path.parent.mkdir(parents=True, exist_ok=True)
        self.log_file = self.log_path.open("w", encoding="utf-8")
        self.process: subprocess.Popen[str] | None = None
        env = benchmark_env(home_root, vault, sidecar)
        try:
            self.process = subprocess.Popen(
                [str(binary), "serve", "--listen", f"127.0.0.1:{self.port}", "--vault", str(vault), "--token", TOKEN],
                cwd=home_root,
                env=env,
                stdout=subprocess.DEVNULL,
                stderr=self.log_file,
                text=True,
                start_new_session=(os.name != "nt"),
            )
            self.wait_ready()
        except BaseException:
            try:
                self.stop()
            except BaseException:
                pass
            raise

    def wait_ready(self) -> None:
        deadline = time.monotonic() + 15.0
        while time.monotonic() < deadline:
            if self.process is not None and self.process.poll() is not None:
                raise HarnessError(f"{self.binary.name} exited before readiness: {self.log_path.read_text()[-4000:]}")
            try:
                status, body, _ = self.request_raw("GET", "/healthz", auth="none")
                if status == 200 and body == b'{"status":"ok"}':
                    return
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(0.025)
        raise HarnessError(f"timed out waiting for {self.binary.name}: {self.log_path.read_text()[-4000:]}")

    def request_raw(
        self,
        method: str,
        path: str,
        auth: str = "valid",
        headers: dict[str, str] | None = None,
    ) -> tuple[int, bytes, dict[str, str]]:
        request = urllib.request.Request(self.base + path, method=method)
        request.add_header("Connection", "close")
        if auth == "valid":
            request.add_header("Authorization", f"Bearer {TOKEN}")
        elif auth == "wrong":
            request.add_header("Authorization", "Bearer wrong-token")
        elif auth == "raw":
            request.add_header("Authorization", TOKEN)
        for key, value in (headers or {}).items():
            request.add_header(key, value)
        try:
            with urllib.request.urlopen(request, timeout=5.0) as response:
                status = int(response.status)
                body = response.read(16 * 1024 * 1024 + 1)
                response_headers = {key.lower(): value for key, value in response.headers.items()}
        except urllib.error.HTTPError as error:
            status = int(error.code)
            body = error.read(16 * 1024 * 1024 + 1)
            response_headers = {key.lower(): value for key, value in error.headers.items()}
        if len(body) > 16 * 1024 * 1024:
            raise HarnessError(f"{self.binary.name} HTTP response exceeded 16 MiB")
        if response_headers.get("content-encoding") == "gzip" and body:
            body = gzip.decompress(body)
        return status, body, response_headers

    def stop(self) -> None:
        process = self.process
        if process is None:
            if not self.log_file.closed:
                self.log_file.close()
            return
        try:
            if process.poll() is None:
                if os.name == "nt":
                    process.kill()
                else:
                    try:
                        os.killpg(process.pid, signal.SIGTERM)
                    except ProcessLookupError:
                        pass
                try:
                    process.wait(timeout=5.0)
                except subprocess.TimeoutExpired:
                    if os.name == "nt":
                        process.kill()
                    else:
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                    process.wait(timeout=5.0)
            if process.returncode not in (0, -signal.SIGTERM, 143):
                raise HarnessError(f"{self.binary.name} did not shut down cleanly; log={self.log_path}")
        finally:
            if not self.log_file.closed:
                self.log_file.close()
            self.process = None


def http_operation(name: str) -> dict[str, Any]:
    operations = {
        "healthz": {"method": "GET", "path": "/healthz", "auth": "none", "kind": "health"},
        "status": {"method": "GET", "path": "/api/v1/status", "auth": "valid", "kind": "status"},
        "status-unauthorized": {"method": "GET", "path": "/api/v1/status", "auth": "wrong", "kind": "unauthorized"},
        "snapshot": {"method": "GET", "path": "/api/v1/snapshot", "auth": "valid", "kind": "snapshot"},
        "file-read": {"method": "GET", "path": "/api/v1/files?path=HTTP.md", "auth": "valid", "kind": "file"},
        "file-range": {"method": "GET", "path": "/api/v1/files?path=HTTP.md", "auth": "valid", "headers": {"Range": "bytes=0-4"}, "kind": "range"},
        "file-missing": {"method": "GET", "path": "/api/v1/files?path=missing.md", "auth": "valid", "kind": "missing"},
        "file-traversal": {"method": "GET", "path": "/api/v1/files?path=../outside.md", "auth": "valid", "kind": "rejected"},
    }
    if name not in operations:
        raise HarnessError(f"unknown HTTP operation {name}")
    return operations[name]


def expected_content(relative: str) -> str | None:
    if relative == "HTTP.md":
        return HTTP_FILE_CONTENT
    if relative.startswith("cohort-") and relative.endswith(".md"):
        try:
            index = int(relative.rsplit("/note-", 1)[1][:-3])
        except (IndexError, ValueError):
            return None
        if document_path(index) == relative:
            return document_content(index)
    return None


def validate_http(name: str, status: int, body: bytes, headers: dict[str, str], expected: dict[str, Any], vault: Path) -> None:
    operation = http_operation(name)
    kind = operation["kind"]
    if kind == "health":
        if status != 200 or body != b'{"status":"ok"}':
            raise HarnessError(f"HTTP health semantic mismatch: {status} {body!r}")
    elif kind == "status":
        value = parse_json(body.decode(), "HTTP status")
        if status != 200 or value != {"capabilities": ["snapshot", "files", "ingest", "remote_worker", "command"], "mode": "self_hosted", "schema_version": 1, "status": "ok", "version": "0.12.2"}:
            raise HarnessError(f"HTTP status semantic mismatch: {status} {value!r}")
    elif kind == "unauthorized":
        if status != 401:
            raise HarnessError(f"HTTP unauthorized semantic mismatch: {status}")
    elif kind == "snapshot":
        value = parse_json(body.decode(), "HTTP snapshot")
        if status != 200 or not isinstance(value, dict) or not isinstance(value.get("notes"), list):
            raise HarnessError(f"HTTP snapshot envelope mismatch: {status} {value!r}")
        paths: list[str] = []
        for item in value["notes"]:
            if not isinstance(item, dict) or not isinstance(item.get("path"), str):
                raise HarnessError(f"HTTP snapshot entry missing path: {item!r}")
            if not isinstance(item.get("content"), str) or not isinstance(item.get("modified_at"), str):
                raise HarnessError(f"HTTP snapshot entry missing content/time: {item!r}")
            if expected_content(item["path"]) != item["content"]:
                raise HarnessError(f"HTTP snapshot content mismatch for {item['path']}")
            paths.append(item["path"])
        paths.sort()
        if paths != sorted(expected["paths"]):
            raise HarnessError(f"HTTP snapshot paths mismatch: {len(paths)} vs {len(expected['paths'])}")
        if not isinstance(value.get("generated_at"), str) or not value["generated_at"]:
            raise HarnessError("HTTP snapshot omitted generated_at")
    elif kind == "file":
        if status != 200 or body.decode() != HTTP_FILE_CONTENT:
            raise HarnessError(f"HTTP exact file mismatch: {status} {body[:200]!r}")
    elif kind == "range":
        if status != 206 or body != HTTP_FILE_CONTENT.encode()[0:5]:
            raise HarnessError(f"HTTP range mismatch: {status} {body!r}")
    elif kind == "missing":
        if status != 404 or body != b'{"error":"file not found"}\n':
            raise HarnessError(f"HTTP missing-file mismatch: {status} {body!r}")
    elif kind == "rejected":
        if status != 400 or body != b'{"error":"a vault-relative path is required"}\n':
            raise HarnessError(f"HTTP traversal was not rejected exactly: {status} {body!r}")
    if kind in {"status", "snapshot", "file", "range"} and not headers.get("content-type"):
        raise HarnessError(f"HTTP {name} omitted content-type")


def rss_bytes(pid: int) -> int:
    result = subprocess.run(["ps", "-o", "rss=", "-p", str(pid)], capture_output=True, text=True, check=False)
    if result.returncode != 0 or not result.stdout.strip():
        raise HarnessError(f"cannot read RSS for pid {pid}: {result.stderr.strip()}")
    try:
        return int(result.stdout.strip().splitlines()[0]) * 1024
    except ValueError as exc:
        raise HarnessError(f"invalid RSS for pid {pid}: {result.stdout!r}") from exc


def percentile(values: list[float], fraction: float) -> float:
    if not values:
        raise HarnessError("cannot calculate a percentile from zero samples")
    ordered = sorted(values)
    index = max(0, math.ceil(fraction * len(ordered)) - 1)
    return ordered[index]


def left_fold_sum(values: list[float]) -> float:
    """Aggregate samples portably with explicit left-associative addition."""
    total = 0.0
    for sample in values:
        total += sample
    return total


# How a report's derived means were summed.
#
# CPython 3.12+ changed the built-in sum() over floats to compensated
# (Neumaier) summation, so a report's mean cannot be re-derived correctly
# unless the report says which algorithm produced it. Without a declaration a
# validator silently re-derives under whatever the running interpreter does,
# and a retained capture then fails against its own recorded values.
#
# "left_fold" is what this producer emits and declares.
#
# Pre-declaration reports carry no declaration, so their summation has to be
# recovered from provenance they do record: the interpreter that produced
# them. Before 3.12 the built-in sum() over floats was a plain left fold;
# from 3.12 it is compensated, and math.fsum -- exactly rounded and therefore
# interpreter-independent -- reproduces that result for this data.
#
# Verified over every retained pre-declaration capture, 34 summaries each:
#   produced on 3.9.6   -> left_fold 34/34, fsum 9-12/34
#   produced on 3.14.2  -> fsum 34/34,      left_fold 4-11/34
#
# Retained captures are immutable and are never rewritten to carry a
# declaration; they are validated under the algorithm of their era instead.
SUMMATIONS = {
    "left_fold": left_fold_sum,
    "fsum": math.fsum,
}
DEFAULT_SUMMATION = "left_fold"
LEGACY_SCHEMA_VERSIONS = frozenset({SCHEMA2_VERSION})
DECLARED_SCHEMA_VERSIONS = frozenset({SCHEMA3_VERSION, SCHEMA4_VERSION, SCHEMA_VERSION})
ORDER_STRATIFIED_SCHEMA_VERSIONS = frozenset({SCHEMA4_VERSION, SCHEMA_VERSION})
COMPENSATED_SUM_PYTHON = (3, 12)


def mean_of(values: list[float], summation: str) -> float:
    if summation not in SUMMATIONS:
        raise HarnessError(f"unknown summation: {summation!r}")
    return SUMMATIONS[summation](values) / len(values)


def legacy_summation_for(result: Any) -> str:
    """Recover a pre-declaration report's summation from its recorded host.

    Fails closed: a capture that does not record the interpreter that made it
    cannot have its derived means re-derived, and must not be guessed at.
    """
    host = result.get("host") if isinstance(result, dict) else None
    version = (host or {}).get("python")
    if not isinstance(version, str) or not version:
        raise HarnessError(
            "pre-declaration report does not record host.python, so its "
            "summation cannot be determined"
        )
    try:
        parts = tuple(int(piece) for piece in version.split(".")[:2])
    except ValueError as exc:
        raise HarnessError(f"host.python is not a version: {version!r}") from exc
    if len(parts) < 2:
        raise HarnessError(f"host.python is not a version: {version!r}")
    return "fsum" if parts >= COMPENSATED_SUM_PYTHON else "left_fold"


def summation_of(result: Any) -> str:
    """Return the summation a report declares, or recover its era's one."""
    if not isinstance(result, dict):
        raise HarnessError("result is not an object")
    version = result.get("schema_version")
    declared = result.get("summation")
    if version in LEGACY_SCHEMA_VERSIONS:
        if declared is not None:
            raise HarnessError(
                f"schema {version} reports must not declare a summation"
            )
        return legacy_summation_for(result)
    if declared not in SUMMATIONS:
        raise HarnessError(f"unknown or missing declared summation: {declared!r}")
    return str(declared)


def summary(values: list[float], unit: str, warmups: int, pair_orders: list[str], summation: str = DEFAULT_SUMMATION) -> dict[str, Any]:
    if len(values) < MIN_SAMPLES:
        raise HarnessError(f"{unit} has {len(values)} samples; at least {MIN_SAMPLES} are required")
    if len(pair_orders) != len(values):
        raise HarnessError("pair/order sample count does not match raw samples")
    if any(
        isinstance(value, bool)
        or not isinstance(value, (int, float))
        or not math.isfinite(value)
        or value <= 0
        for value in values
    ):
        raise HarnessError(f"{unit} contains a non-positive or non-finite sample")
    return {
        "unit": unit,
        "warmup_samples": warmups,
        "samples": len(values),
        "min": min(values),
        "mean": mean_of(values, summation),
        "p50": percentile(values, 0.50),
        "p95": percentile(values, 0.95),
        "p99": percentile(values, 0.99),
        "max": max(values),
        "raw": values,
        "pair_order": pair_orders,
        "max_observed": max(values),
    }


def measure_process_pair(
    go_binary: Path,
    rust_binary: Path,
    go_env: dict[str, str],
    rust_env: dict[str, str],
    go_args: list[str],
    rust_args: list[str],
    warmups: int,
    samples: int,
    validator: Callable[[str, str], None] | tuple[Callable[[str, str], None], Callable[[str, str], None]],
    input_data: str | None = None,
) -> dict[str, Any]:
    values: dict[str, list[float]] = {"go": [], "rust": []}
    orders: list[str] = []
    for index in range(warmups + samples):
        go_first = index % 2 == 0
        orders.append("go-rust" if go_first else "rust-go")
        order = (("go", go_binary, go_env, go_args), ("rust", rust_binary, rust_env, rust_args)) if go_first else (("rust", rust_binary, rust_env, rust_args), ("go", go_binary, go_env, go_args))
        for name, binary, env, args in order:
            started = time.perf_counter_ns()
            try:
                completed = subprocess.run(
                    [str(binary), *args],
                    input=input_data,
                    env=env,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    text=True,
                    timeout=30.0,
                    check=False,
                )
            except subprocess.TimeoutExpired as exc:
                raise HarnessError(
                    f"{name} {' '.join(args)} timed out after 30.0s"
                ) from exc
            elapsed = (time.perf_counter_ns() - started) / 1_000_000.0
            if completed.returncode != 0:
                raise HarnessError(f"{name} {' '.join(args)} failed: {completed.stderr[-1000:]}")
            active_validator = validator[0 if name == "go" else 1] if isinstance(validator, tuple) else validator
            active_validator(completed.stdout, completed.stderr)
            if index >= warmups:
                values[name].append(elapsed)
    return {"go": summary(values["go"], "milliseconds", warmups, orders[warmups:]), "rust": summary(values["rust"], "milliseconds", warmups, orders[warmups:])}


def measure_mcp(
    go_binary: Path,
    rust_binary: Path,
    go_env: dict[str, str],
    rust_env: dict[str, str],
    expected: dict[str, Any],
    go_vault: Path,
    rust_vault: Path,
    warmups: int,
    samples: int,
) -> dict[str, Any]:
    specs = [
        ("initialize", mcp_request("initialize", 1)),
        ("tools-list", mcp_request("tools/list", 2)),
        ("desk_status", mcp_request("tools/call", 3, "desk_status")),
        ("desk_ls", mcp_request("tools/call", 4, "desk_ls", {"dir": MCP_LS_DIR})),
        ("desk_search", mcp_request("tools/call", 5, "desk_search", {"query": SEARCH_TOKEN})),
    ]
    values = {"go": [], "rust": []}
    orders: list[str] = []
    per_operation: dict[str, Any] = {}
    for label, request in specs:
        def validator_factory(binary_name: str, vault: Path) -> Callable[[str, str], None]:
            return lambda stdout, stderr: validate_mcp(stdout, stderr, label, expected, vault)
        metric = measure_process_pair(
            go_binary,
            rust_binary,
            go_env,
            rust_env,
            ["mcp"],
            ["mcp"],
            warmups,
            samples,
            (lambda stdout, stderr: validate_mcp(stdout, stderr, label, expected, go_vault), lambda stdout, stderr: validate_mcp(stdout, stderr, label, expected, rust_vault)),
            input_data=request,
        )
        # Re-run validation with the Rust vault semantics are path-identical in shape.
        # The process pair already validated each result; this records a strict operation slice.
        per_operation[label] = metric
        values["go"].extend(metric["go"]["raw"])
        values["rust"].extend(metric["rust"]["raw"])
        if not orders:
            orders = metric["go"]["pair_order"]
    return {
        "go": summary(values["go"], "milliseconds", warmups, orders * len(specs)),
        "rust": summary(values["rust"], "milliseconds", warmups, orders * len(specs)),
        "operations": per_operation,
    }


def http_round_schedule(index: int) -> tuple[tuple[str, str], ...]:
    """Return the predeclared operation/order schedule for one HTTP round."""
    if index < 0:
        raise HarnessError("HTTP round index must be non-negative")
    offset = index % len(HTTP_OPERATION_NAMES)
    names = HTTP_OPERATION_NAMES[offset:] + HTTP_OPERATION_NAMES[:offset]
    return tuple(
        (
            name,
            "go-rust"
            if (index + HTTP_OPERATION_NAMES.index(name)) % 2 == 0
            else "rust-go",
        )
        for name in names
    )


def expected_http_pair_orders(warmups: int, samples: int) -> list[str]:
    """Return the exact recorded aggregate order labels for schema-5 HTTP."""
    return [
        order
        for index in range(warmups, warmups + samples)
        for _name, order in http_round_schedule(index)
    ]


def expected_http_operation_order(warmups: int, samples: int) -> list[str]:
    """Return the exact flattened route sequence for schema-5 HTTP."""
    return [
        name
        for index in range(warmups, warmups + samples)
        for name, _order in http_round_schedule(index)
    ]


def expected_http_operation_orders(name: str, warmups: int, samples: int) -> list[str]:
    """Return the exact recorded order labels for one schema-5 HTTP route."""
    if name not in HTTP_OPERATION_NAMES:
        raise HarnessError(f"unknown HTTP operation {name}")
    return [
        order
        for index in range(warmups, warmups + samples)
        for operation_name, order in http_round_schedule(index)
        if operation_name == name
    ]


def measure_http(
    go_server: RunningServer,
    rust_server: RunningServer,
    go_expected: dict[str, Any],
    rust_expected: dict[str, Any],
    warmups: int,
    samples: int,
    rss_interval_ms: float,
) -> tuple[dict[str, Any], dict[str, Any]]:
    values = {"go": [], "rust": []}
    rss_values = {"go": [], "rust": []}
    rss_orders: list[str] = []
    aggregate_orders: list[str] = []
    aggregate_operation_order: list[str] = []
    per_operation: dict[str, Any] = {}
    per_operation_orders: dict[str, list[str]] = {}
    for index in range(warmups + samples):
        priming_servers = (
            (("go", go_server, go_expected), ("rust", rust_server, rust_expected))
            if index % 2 == 0
            else (("rust", rust_server, rust_expected), ("go", go_server, go_expected))
        )
        for name, server, expected in priming_servers:
            operation = http_operation("healthz")
            try:
                status, body, headers = server.request_raw(
                    operation["method"],
                    operation["path"],
                    operation["auth"],
                    operation.get("headers"),
                )
            except (OSError, TimeoutError, urllib.error.URLError) as exc:
                raise HarnessError(f"{name} HTTP healthz request failed or timed out: {exc}") from exc
            validate_http("healthz", status, body, headers, expected, server.vault)
            time.sleep(HTTP_CONTROL_IDLE_SECONDS)
        for operation_name, order in http_round_schedule(index):
            ordered_servers = (
                (("go", go_server, go_expected), ("rust", rust_server, rust_expected))
                if order == "go-rust"
                else (("rust", rust_server, rust_expected), ("go", go_server, go_expected))
            )
            operation = http_operation(operation_name)
            for name, server, expected in ordered_servers:
                started = time.perf_counter_ns()
                try:
                    status, body, headers = server.request_raw(
                        operation["method"],
                        operation["path"],
                        operation["auth"],
                        operation.get("headers"),
                    )
                except (OSError, TimeoutError, urllib.error.URLError) as exc:
                    raise HarnessError(
                        f"{name} HTTP {operation_name} request failed or timed out: {exc}"
                    ) from exc
                elapsed = (time.perf_counter_ns() - started) / 1_000_000.0
                validate_http(operation_name, status, body, headers, expected, server.vault)
                if index >= warmups:
                    values[name].append(elapsed)
                    per_operation.setdefault(operation_name, {"go": [], "rust": []})[name].append(elapsed)
                time.sleep(HTTP_CONTROL_IDLE_SECONDS)
            if index >= warmups:
                aggregate_orders.append(order)
                aggregate_operation_order.append(operation_name)
                per_operation_orders.setdefault(operation_name, []).append(order)
        if index >= warmups:
            if go_server.process is None or rust_server.process is None:
                raise HarnessError("server process disappeared during RSS measurement")
            rss_values["go"].append(float(rss_bytes(go_server.process.pid)))
            rss_values["rust"].append(float(rss_bytes(rust_server.process.pid)))
            rss_orders.append("go-rust" if index % 2 == 0 else "rust-go")
        if rss_interval_ms > 0:
            time.sleep(rss_interval_ms / 1000.0)
    http_metric = {
        "go": summary(values["go"], "milliseconds", warmups, aggregate_orders),
        "rust": summary(values["rust"], "milliseconds", warmups, aggregate_orders),
        "operation_order": aggregate_operation_order,
        "operations": {
            name: {
                "go": summary(data["go"], "milliseconds", warmups, per_operation_orders[name]),
                "rust": summary(data["rust"], "milliseconds", warmups, per_operation_orders[name]),
            }
            for name, data in per_operation.items()
        },
    }
    rss_metric = {
        "go": summary(rss_values["go"], "bytes", warmups, rss_orders),
        "rust": summary(rss_values["rust"], "bytes", warmups, rss_orders),
    }
    return http_metric, rss_metric


def ratio(candidate: float, reference: float) -> float:
    """Return the candidate/reference value as a dimensionless ratio."""
    if (
        isinstance(candidate, bool)
        or isinstance(reference, bool)
        or not isinstance(candidate, (int, float))
        or not isinstance(reference, (int, float))
        or not math.isfinite(candidate)
        or not math.isfinite(reference)
        or candidate <= 0
        or reference <= 0
    ):
        raise HarnessError(f"invalid latency values candidate={candidate!r} reference={reference!r}")
    return candidate / reference


# How a latency regression is estimated from a metric pair.
#
# "unpaired_p95" compares the p95 of each side's marginal distribution. The
# harness, however, collects go and rust as index-aligned PAIRS taken
# back-to-back within one round, with the within-pair order alternated. Both
# VALUE-001 measurement runs drift strongly within the run, in opposite
# directions, so an unpaired p95 over that series largely reports which side's
# samples landed in the slow phase. On the same immutable candidate it
# produced -6.73% and +19.53% for http.file-missing.
#
# "paired_median_ratio" uses the pairing the design already provides: the
# median of the per-pair rust/go ratio. Drift cancels because both members of
# a pair are measured under the same machine state. It remains supported for
# immutable schema-3 evidence, but it pools the two deliberately alternated
# within-pair orders and therefore cannot decide a new candidate.
#
# "order_stratified_paired_median_ratio" keeps the same pairwise ratios but
# computes a median independently for go-rust and rust-go. A new candidate is
# judged by the worse cohort. This is deliberately conservative: a fast first
# invocation must not make a slow second invocation disappear at the pooled
# median boundary.
#
# The estimator is recorded in the report. Schema 2 has no declaration,
# schema 3 is immutable historical evidence, and schemas 4 and 5 require
# order-stratification. Only schema 5 is eligible for a new approval.
SCHEMA3_LATENCY_ESTIMATORS = frozenset({"unpaired_p95", "paired_median_ratio"})
DEFAULT_LATENCY_ESTIMATOR = "order_stratified_paired_median_ratio"
LATENCY_ESTIMATORS = SCHEMA3_LATENCY_ESTIMATORS | {DEFAULT_LATENCY_ESTIMATOR}
LEGACY_LATENCY_ESTIMATOR = "unpaired_p95"
MEDIAN_INTERVAL_CONFIDENCE = 0.95
PAIR_ORDERS = ("go-rust", "rust-go")
MIN_ORDER_COHORT_SAMPLES = MIN_SAMPLES // len(PAIR_ORDERS)


def latency_estimator_of(result: Any) -> str:
    """Return the latency estimator a report declares, or its era's one."""
    if not isinstance(result, dict):
        raise HarnessError("result is not an object")
    version = result.get("schema_version")
    declared = (result.get("thresholds") or {}).get("latency_estimator")
    if version in LEGACY_SCHEMA_VERSIONS:
        if declared is not None:
            raise HarnessError(
                f"schema {version} reports must not declare a latency estimator"
            )
        return LEGACY_LATENCY_ESTIMATOR
    if version == SCHEMA3_VERSION:
        if declared not in SCHEMA3_LATENCY_ESTIMATORS:
            raise HarnessError(f"schema 3 has an invalid latency estimator: {declared!r}")
        return str(declared)
    if version in ORDER_STRATIFIED_SCHEMA_VERSIONS:
        if declared != DEFAULT_LATENCY_ESTIMATOR:
            raise HarnessError(
                f"schema {version} requires the order-stratified latency estimator"
            )
        return DEFAULT_LATENCY_ESTIMATOR
    raise HarnessError(f"unsupported schema_version: {version!r}")


def recorded_regressions(result: Any) -> dict[str, Any]:
    """Read the recorded regressions under the key its schema uses."""
    thresholds = (result or {}).get("thresholds") or {}
    if result.get("schema_version") in LEGACY_SCHEMA_VERSIONS:
        return thresholds.get("p95_regressions") or {}
    return thresholds.get("latency_regressions") or {}


def paired_ratios(pair: dict[str, Any], label: str) -> list[float]:
    """Per-pair rust/go ratios from index-aligned raw samples."""
    go = pair["go"]["raw"]
    rust = pair["rust"]["raw"]
    if len(go) != len(rust):
        raise HarnessError(f"{label} paired samples are not aligned")
    if not go:
        raise HarnessError(f"{label} has no samples")
    return [ratio(r, g) for g, r in zip(go, rust)]


def paired_ratio_cohorts(pair: dict[str, Any], label: str) -> dict[str, list[float]]:
    """Split aligned Rust/Go pair ratios by their recorded execution order.

    The producer deliberately alternates which implementation runs first.
    Both sides must therefore carry the same balanced labels. Missing,
    mismatched, or materially unbalanced labels make an order-stratified gate
    unverifiable and are rejected rather than silently pooled.
    """
    ratios = paired_ratios(pair, label)
    go_orders = pair["go"].get("pair_order")
    rust_orders = pair["rust"].get("pair_order")
    if not isinstance(go_orders, list) or not isinstance(rust_orders, list):
        raise HarnessError(f"{label} pair order labels are missing")
    if len(go_orders) != len(ratios) or len(rust_orders) != len(ratios):
        raise HarnessError(f"{label} pair order labels do not match samples")
    if go_orders != rust_orders:
        raise HarnessError(f"{label} pair order labels are not aligned")

    cohorts = {order: [] for order in PAIR_ORDERS}
    for order, sample in zip(go_orders, ratios):
        if order not in cohorts:
            raise HarnessError(f"{label} has an invalid pair order: {order!r}")
        cohorts[order].append(sample)

    counts = [len(cohorts[order]) for order in PAIR_ORDERS]
    if min(counts) < MIN_ORDER_COHORT_SAMPLES or max(counts) - min(counts) > 1:
        raise HarnessError(
            f"{label} order cohorts are not balanced: "
            f"go-rust={counts[0]}, rust-go={counts[1]}"
        )
    return cohorts


def order_stratified_paired_regression(pair: dict[str, Any], label: str) -> float:
    """Return the worst within-order paired-median regression for one gate."""
    cohorts = paired_ratio_cohorts(pair, label)
    return max(median(values) - 1.0 for values in cohorts.values())


def median(values: list[float]) -> float:
    ordered = sorted(values)
    n = len(ordered)
    middle = n // 2
    if n % 2:
        return ordered[middle]
    return (ordered[middle - 1] + ordered[middle]) / 2.0


def median_interval(values: list[float]) -> tuple[float, float]:
    """Distribution-free confidence interval for the median.

    Uses order statistics under the sign test, so it is exact and
    deterministic: no resampling, no seed, no extra dependency.
    """
    ordered = sorted(values)
    n = len(ordered)
    if n < 2:
        raise HarnessError("a median interval needs at least two samples")
    target = (1.0 - MEDIAN_INTERVAL_CONFIDENCE) / 2.0
    total = float(2 ** n)
    cumulative = 0.0
    k = 0
    for i in range(n + 1):
        term = math.comb(n, i) / total
        if cumulative + term > target:
            break
        cumulative += term
        k = i + 1
    lower = min(max(k - 1, 0), n - 1)
    upper = max(min(n - k, n - 1), 0)
    return ordered[lower], ordered[upper]


def latency_pairs(metrics: dict[str, Any]) -> dict[str, dict[str, Any]]:
    """Every gated latency pair, by gate name."""
    pairs: dict[str, dict[str, Any]] = {}
    for name in ("startup", "search", "mcp", "http"):
        metric = metrics.get(name)
        if not isinstance(metric, dict):
            raise HarnessError(f"missing latency metric {name}")
        pairs[name] = metric
        if name in {"mcp", "http"}:
            operations = metric.get("operations")
            if not isinstance(operations, dict) or not operations:
                raise HarnessError(f"missing operations for {name}")
            for operation, pair in operations.items():
                if not isinstance(operation, str) or not isinstance(pair, dict):
                    raise HarnessError(f"invalid {name} operation metric")
                pairs[f"{name}.{operation}"] = pair
    return pairs


def latency_regressions(metrics: dict[str, Any], estimator: str) -> dict[str, float]:
    """Recompute relative latency changes in stored ratio units.

    Each value is the candidate/reference ratio minus one, so 0.10 means a
    10% regression. Gate comparisons must use these raw ratios; presentation
    code is responsible for multiplying them by 100 for display.
    """
    if estimator not in LATENCY_ESTIMATORS:
        raise HarnessError(f"unknown latency estimator: {estimator!r}")
    regressions: dict[str, float] = {}
    for name, pair in latency_pairs(metrics).items():
        if estimator == "unpaired_p95":
            regressions[name] = ratio(pair["rust"]["p95"], pair["go"]["p95"]) - 1.0
        elif estimator == "paired_median_ratio":
            regressions[name] = median(paired_ratios(pair, name)) - 1.0
        else:
            regressions[name] = order_stratified_paired_regression(pair, name)
    return regressions


def latency_order_regressions(metrics: dict[str, Any]) -> dict[str, dict[str, float]]:
    """Recompute each gate's paired-median regression by execution order."""
    regressions: dict[str, dict[str, float]] = {}
    for name, pair in latency_pairs(metrics).items():
        cohorts = paired_ratio_cohorts(pair, name)
        regressions[name] = {
            order: median(cohorts[order]) - 1.0 for order in PAIR_ORDERS
        }
    return regressions


def latency_regression_intervals(metrics: dict[str, Any]) -> dict[str, list[float]]:
    """Historical pooled paired-median intervals for schema-3 evidence only."""
    intervals: dict[str, list[float]] = {}
    for name, pair in latency_pairs(metrics).items():
        low, high = median_interval(paired_ratios(pair, name))
        intervals[name] = [low - 1.0, high - 1.0]
    return intervals


def latency_order_regression_intervals(metrics: dict[str, Any]) -> dict[str, dict[str, list[float]]]:
    """Per-order descriptive median intervals for schema-4 evidence.

    The two cohort intervals deliberately remain separate. Combining them
    would make a pooled interval look decision-capable again; the schema-4
    gate is the worst *point* median in ``latency_order_regressions``.
    """
    intervals: dict[str, dict[str, list[float]]] = {}
    for name, pair in latency_pairs(metrics).items():
        cohorts = paired_ratio_cohorts(pair, name)
        intervals[name] = {
            order: [low - 1.0, high - 1.0]
            for order, values in cohorts.items()
            for low, high in (median_interval(values),)
        }
    return intervals


def build_go_oracle(root: Path, commit: str, temp_root: Path) -> tuple[Path, dict[str, Any], Path]:
    if commit != CURRENT_BEHAVIOUR_ORACLE:
        raise HarnessError(
            "VALUE-001 requires --go-source-commit to equal "
            f"CURRENT_BEHAVIOUR_ORACLE ({CURRENT_BEHAVIOUR_ORACLE})"
        )
    source = temp_root / "go-oracle-source"
    binary = temp_root / "symdesk-go-oracle"
    added = False
    try:
        run_checked(["git", "worktree", "add", "--detach", "--quiet", str(source), commit], root, 60.0)
        added = True
        status = git_output(source, ["status", "--porcelain=v1", "--untracked-files=all"])
        if status:
            raise HarnessError(f"isolated Go oracle worktree is dirty:\n{status}")
        actual_commit = git_output(source, ["rev-parse", "HEAD"])
        if actual_commit != commit:
            raise HarnessError(f"Go oracle commit mismatch: requested {commit}, got {actual_commit}")
        env = dict(os.environ)
        env["GOTOOLCHAIN"] = "go1.26.6"
        build = ["go", "build", "-trimpath", "-ldflags=-s -w -X main.version=0.12.2", "-o", str(binary), "./cmd/symdesk"]
        result = run_checked(build, source, 300.0, env=env)
        return binary, {
            "commit": actual_commit,
            "status": status,
            "build_command": "GOTOOLCHAIN=go1.26.6 " + command_text(build),
            "build_elapsed_ms": result["elapsed_ms"],
            "go_version": tool_version(["go", "version"], source, env=env),
            "binary_sha256": sha256_file(binary),
            "binary_bytes": binary.stat().st_size,
        }, source
    except BaseException:
        if added:
            subprocess.run(["git", "worktree", "remove", "--force", str(source)], cwd=root, capture_output=True, text=True, check=False)
        raise


def build_result(
    root: Path,
    go_binary: Path,
    rust_binary: Path,
    go_source: dict[str, Any],
    rust_build_command: str,
    contracts: list[dict[str, Any]],
    metrics: dict[str, Any],
    manifest: dict[str, Any],
    samples: int,
    warmups: int,
    index_preparation: dict[str, Any],
    summation: str = DEFAULT_SUMMATION,
    latency_estimator: str = DEFAULT_LATENCY_ESTIMATOR,
    schema_version: int = SCHEMA_VERSION,
) -> dict[str, Any]:
    # The declaration must describe the metrics actually being recorded, not
    # the producer's own preference; a caller replaying pre-declaration
    # metrics has to say so.
    if summation not in SUMMATIONS:
        raise HarnessError(f"unknown summation: {summation!r}")
    if latency_estimator not in LATENCY_ESTIMATORS:
        raise HarnessError(f"unknown latency estimator: {latency_estimator!r}")
    if schema_version == SCHEMA3_VERSION:
        if latency_estimator not in SCHEMA3_LATENCY_ESTIMATORS:
            raise HarnessError("schema 3 cannot emit the order-stratified estimator")
    elif schema_version in ORDER_STRATIFIED_SCHEMA_VERSIONS:
        if latency_estimator != DEFAULT_LATENCY_ESTIMATOR:
            raise HarnessError(
                f"schema {schema_version} requires the order-stratified estimator"
            )
    else:
        raise HarnessError(f"build_result cannot emit schema_version {schema_version!r}")
    if schema_version in ORDER_STRATIFIED_SCHEMA_VERSIONS:
        if go_source.get("commit") != CURRENT_BEHAVIOUR_ORACLE:
            raise HarnessError("current result Go source is not CURRENT_BEHAVIOUR_ORACLE")
        if samples != CURRENT_SAMPLES or warmups != CURRENT_WARMUPS:
            raise HarnessError("current result requires exactly 100 samples and 20 warmups")
        if (
            not isinstance(manifest, dict)
            or manifest.get("documents") != DOC_COUNT
            or manifest.get("search_match_count") != 100
        ):
            raise HarnessError("current result requires the frozen 10,000-document/100-match vault")
        if not isinstance(index_preparation, dict):
            raise HarnessError("current result index preparation is missing")
        for side in ("go", "rust"):
            preparation = index_preparation.get(side)
            if not isinstance(preparation, dict) or preparation.get("documents") != DOC_COUNT:
                raise HarnessError(
                    f"current result requires 10,000 index-preparation documents for {side}"
                )
        require_executable_regular(go_binary, "Go binary")
        require_executable_regular(rust_binary, "Rust binary")
        if go_source.get("binary_bytes") != go_binary.stat().st_size:
            raise HarnessError("current result Go binary size metadata is not verified")
        if go_source.get("binary_sha256") != sha256_file(go_binary):
            raise HarnessError("current result Go binary digest metadata is not verified")
    go_size = go_binary.stat().st_size
    rust_size = rust_binary.stat().st_size
    size_reduction = (go_size - rust_size) / go_size
    rss_go = metrics["rss"]["go"]["max"]
    rss_rust = metrics["rss"]["rust"]["max"]
    rss_reduction = (rss_go - rss_rust) / rss_go
    regressions = latency_regressions(metrics, latency_estimator)
    order_regressions = (
        latency_order_regressions(metrics)
        if schema_version in ORDER_STRATIFIED_SCHEMA_VERSIONS
        else None
    )
    # The stratified estimator's scalar is the worst order by construction,
    # but checking every recorded cohort documents the fail-closed intent and
    # prevents a malformed future implementation from hiding one cohort.
    latency_values = (
        [value for by_order in order_regressions.values() for value in by_order.values()]
        if order_regressions is not None
        else list(regressions.values())
    )
    latency_pass = all(math.isfinite(value) and value <= 0.10 for value in latency_values)
    improvement_pass = size_reduction >= 0.20 or rss_reduction >= 0.20
    contracts_pass = all(item["exit_code"] == 0 for item in contracts)
    thresholds: dict[str, Any] = {
        "minimum_improvement": 0.20,
        # Unchanged ceiling; only the estimator below changed.
        "maximum_latency_regression": 0.10,
        "latency_estimator": latency_estimator,
        "binary_size_reduction": size_reduction,
        "representative_rss_reduction_max": rss_reduction,
        "improvement_pass": improvement_pass,
        "latency_regressions": regressions,
        "latency_pass": latency_pass,
        "contracts_pass": contracts_pass,
        "fail_closed": True,
    }
    if schema_version in ORDER_STRATIFIED_SCHEMA_VERSIONS:
        thresholds["latency_order_regressions"] = order_regressions
        # Intervals are descriptive only and remain stratified: a pooled
        # interval would reintroduce the order bias that schema 4 removes.
        thresholds["latency_order_regression_intervals"] = latency_order_regression_intervals(metrics)
        thresholds["latency_interval_confidence"] = MEDIAN_INTERVAL_CONFIDENCE
    else:
        thresholds["latency_regression_intervals"] = latency_regression_intervals(metrics)
        thresholds["latency_interval_confidence"] = MEDIAN_INTERVAL_CONFIDENCE
    result = {
        "schema_version": schema_version,
        "benchmark": "VALUE-001",
        "summation": summation,
        "captured_at": utc_now(),
        "runner": {
            "path": str(Path(__file__).relative_to(root)),
            "samples": samples,
            "warmups": warmups,
            "pairing": (
                HTTP_MEASUREMENT_PAIRING
                if schema_version == SCHEMA_VERSION
                else SCHEMA4_PAIRING
            ),
        },
        "host": {"os": sys_platform(), "os_version": platform.platform(), "arch": platform.machine(), "machine": platform.machine(), "python": platform.python_version()},
        "repository": {
            "root": str(root),
            "head": git_output(root, ["rev-parse", "HEAD"]),
            "status": git_output(root, ["status", "--porcelain=v1", "--untracked-files=all"]),
            "dirty_allowed": True,
            "candidate_diff_sha256": sha256_bytes(git_bytes(root, ["diff", "--binary", "HEAD"])),
            "original_value_baseline_commit": ORIGINAL_VALUE_BASELINE,
            "current_behaviour_oracle_commit": go_source["commit"],
        },
        "toolchains": {
            "go_oracle": go_source["go_version"],
            "rustc": tool_version(["rustc", "--version", "--verbose"], root),
            "cargo": tool_version(["cargo", "--version"], root),
            "python": platform.python_version(),
        },
        "binaries": {
            "go": {"path": str(go_binary), "bytes": go_size, "sha256": go_source["binary_sha256"], "source": go_source["commit"], "build_command": go_source["build_command"]},
            "rust": {"path": str(rust_binary), "bytes": rust_size, "sha256": sha256_file(rust_binary), "source": git_output(root, ["rev-parse", "HEAD"]), "build_command": rust_build_command},
        },
        "vault": {"documents": manifest["documents"], "generated_bytes": manifest["bytes"], "search_token": SEARCH_TOKEN, "search_matches": manifest["search_match_count"], "generation": "Python stdlib deterministic loop; cohort = index % 100; 10,000 Markdown files"},
        "index_preparation": index_preparation,
        "contracts": contracts,
        "metrics": metrics,
        "thresholds": thresholds,
        "passed": contracts_pass and improvement_pass and latency_pass,
    }
    validate_result(result)
    return result


def sys_platform() -> str:
    return platform.system().lower()


def validate_sample(
    value: Any,
    name: str,
    unit: str,
    summation: str,
    *,
    require_positive: bool,
    expected_samples: int | None = None,
    expected_warmups: int | None = None,
) -> None:
    if not isinstance(value, dict):
        raise HarnessError(f"{name} is not an object")
    required = {"unit", "warmup_samples", "samples", "min", "mean", "p50", "p95", "p99", "max", "raw", "pair_order", "max_observed"}
    if set(value) != required:
        raise HarnessError(f"{name} keys mismatch: {set(value)!r}")
    if value["unit"] != unit or value["warmup_samples"] < 1 or value["samples"] < MIN_SAMPLES:
        raise HarnessError(f"{name} sample metadata invalid")
    if expected_samples is not None and value["samples"] != expected_samples:
        raise HarnessError(f"{name} sample count must be exactly {expected_samples}")
    if expected_warmups is not None and value["warmup_samples"] != expected_warmups:
        raise HarnessError(f"{name} warmup count must be exactly {expected_warmups}")
    if len(value["raw"]) != value["samples"] or len(value["pair_order"]) != value["samples"]:
        raise HarnessError(f"{name} raw/order lengths mismatch")
    if any(
        not isinstance(item, (int, float))
        or isinstance(item, bool)
        or not math.isfinite(item)
        or item < 0
        or (require_positive and item == 0)
        for item in value["raw"]
    ):
        raise HarnessError(f"{name} contains invalid raw values")
    for key in ("min", "mean", "p50", "p95", "p99", "max", "max_observed"):
        if isinstance(value[key], bool) or not isinstance(value[key], (int, float)) or not math.isfinite(value[key]):
            raise HarnessError(f"{name}.{key} is not finite")
    if value["max"] != max(value["raw"]) or value["max_observed"] != value["max"]:
        raise HarnessError(f"{name} maximum is not the maximum raw observation")
    expected = {
        "min": min(value["raw"]),
        "mean": mean_of(value["raw"], summation),
        "p50": percentile(value["raw"], 0.50),
        "p95": percentile(value["raw"], 0.95),
        "p99": percentile(value["raw"], 0.99),
    }
    for key, actual in expected.items():
        if value[key] != actual:
            raise HarnessError(f"{name}.{key} does not match raw samples")
    if any(order not in {"go-rust", "rust-go"} for order in value["pair_order"]):
        raise HarnessError(f"{name} contains an invalid pair order")
    if require_positive:
        first = value["pair_order"].count("go-rust")
        second = value["pair_order"].count("rust-go")
        if first != value["samples"] // 2 or second != value["samples"] // 2:
            raise HarnessError(f"{name} pair cohorts are not balanced")


def validate_paired_metric(
    value: Any,
    name: str,
    unit: str,
    summation: str,
    *,
    require_positive: bool,
    expected_samples: int | None = None,
    expected_warmups: int | None = None,
) -> None:
    if not isinstance(value, dict) or set(value) != {"go", "rust"}:
        raise HarnessError(f"{name} paired metric keys invalid")
    validate_sample(
        value["go"],
        f"{name}.go",
        unit,
        summation,
        require_positive=require_positive,
        expected_samples=expected_samples,
        expected_warmups=expected_warmups,
    )
    validate_sample(
        value["rust"],
        f"{name}.rust",
        unit,
        summation,
        require_positive=require_positive,
        expected_samples=expected_samples,
        expected_warmups=expected_warmups,
    )


def validate_schema5_http_schedule(metric: dict[str, Any]) -> None:
    """Require the exact predeclared route and order sequence."""
    expected_operations = expected_http_operation_order(CURRENT_WARMUPS, CURRENT_SAMPLES)
    if metric.get("operation_order") != expected_operations:
        raise HarnessError("schema 5 HTTP operation order differs from schedule")
    expected_aggregate = expected_http_pair_orders(CURRENT_WARMUPS, CURRENT_SAMPLES)
    for side in ("go", "rust"):
        if metric[side].get("pair_order") != expected_aggregate:
            raise HarnessError(f"schema 5 HTTP aggregate {side} pair order differs from schedule")
    for name in HTTP_OPERATION_NAMES:
        expected = expected_http_operation_orders(name, CURRENT_WARMUPS, CURRENT_SAMPLES)
        pair = metric["operations"][name]
        for side in ("go", "rust"):
            if pair[side].get("pair_order") != expected:
                raise HarnessError(f"schema 5 HTTP {name} {side} pair order differs from schedule")


def validate_result(result: dict[str, Any]) -> None:
    base = {"schema_version", "benchmark", "captured_at", "runner", "host", "repository", "toolchains", "binaries", "vault", "index_preparation", "contracts", "metrics", "thresholds", "passed"}
    if not isinstance(result, dict) or result.get("benchmark") != "VALUE-001":
        raise HarnessError("result top-level schema mismatch")
    version = result.get("schema_version")
    if version in DECLARED_SCHEMA_VERSIONS:
        # Schema 3 and later declare how their means were summed.
        required = base | {"summation"}
    elif version in LEGACY_SCHEMA_VERSIONS:
        # Retained pre-declaration captures are immutable and keep their shape.
        required = base
    else:
        raise HarnessError(f"unsupported schema_version: {version!r}")
    if set(result) != required:
        raise HarnessError("result top-level schema mismatch")
    summation = summation_of(result)
    require_positive = version in ORDER_STRATIFIED_SCHEMA_VERSIONS
    if require_positive:
        repository = result.get("repository")
        expected_repository_keys = {
            "root",
            "head",
            "status",
            "dirty_allowed",
            "candidate_diff_sha256",
            "original_value_baseline_commit",
            "current_behaviour_oracle_commit",
        }
        if (
            not isinstance(repository, dict)
            or set(repository) != expected_repository_keys
            or not isinstance(repository["root"], str)
            or not isinstance(repository["status"], str)
            or repository["dirty_allowed"] is not True
            or not is_lower_hex(repository["head"], 40)
            or not is_lower_hex(repository["candidate_diff_sha256"], 64)
            or not is_lower_hex(repository["original_value_baseline_commit"], 40)
            or not is_lower_hex(repository["current_behaviour_oracle_commit"], 40)
        ):
            raise HarnessError(f"schema {version} repository provenance is invalid")
    if require_positive:
        runner = result.get("runner")
        if (
            not isinstance(runner, dict)
            or runner.get("samples") != CURRENT_SAMPLES
            or runner.get("warmups") != CURRENT_WARMUPS
        ):
            raise HarnessError(
                f"schema {version} runner requires exactly 100 samples and 20 warmups"
            )
        if version == SCHEMA_VERSION and runner.get("pairing") != HTTP_MEASUREMENT_PAIRING:
            raise HarnessError("schema 5 runner requires the controlled HTTP pairing")
        vault = result.get("vault")
        if (
            not isinstance(vault, dict)
            or vault.get("documents") != DOC_COUNT
            or vault.get("search_matches") != 100
        ):
            raise HarnessError("current vault requires exactly 10,000 documents and 100 search matches")
        index_preparation = result.get("index_preparation")
        if not isinstance(index_preparation, dict):
            raise HarnessError("current index preparation is missing")
        for side in ("go", "rust"):
            preparation = index_preparation.get(side)
            if not isinstance(preparation, dict) or preparation.get("documents") != DOC_COUNT:
                raise HarnessError(
                    f"current index_preparation.{side}.documents must be exactly 10000"
                )
    try:
        dt.datetime.fromisoformat(result["captured_at"].replace("Z", "+00:00"))
    except (TypeError, ValueError) as exc:
        raise HarnessError("captured_at is not RFC3339") from exc
    for name in ("startup", "search", "rss"):
        validate_paired_metric(
            result["metrics"][name],
            f"metrics.{name}",
            "milliseconds" if name != "rss" else "bytes",
            summation,
            require_positive=require_positive,
            expected_samples=CURRENT_SAMPLES if require_positive else None,
            expected_warmups=CURRENT_WARMUPS if require_positive else None,
        )
    for name in ("mcp", "http"):
        metric = result["metrics"][name]
        expected_metric_keys = {"go", "rust", "operations"}
        if name == "http" and version == SCHEMA_VERSION:
            expected_metric_keys.add("operation_order")
        if not isinstance(metric, dict) or set(metric) != expected_metric_keys:
            raise HarnessError(f"metrics.{name} operation metric keys invalid")
        required_operations = {
            "mcp": {"initialize", "tools-list", "desk_status", "desk_ls", "desk_search"},
            "http": {"healthz", "status", "snapshot", "file-read", "file-range", "file-missing", "file-traversal"},
        }[name]
        aggregate_samples = (
            CURRENT_SAMPLES * len(required_operations) if require_positive else None
        )
        validate_sample(
            metric["go"],
            f"metrics.{name}.go",
            "milliseconds",
            summation,
            require_positive=require_positive,
            expected_samples=aggregate_samples,
            expected_warmups=CURRENT_WARMUPS if require_positive else None,
        )
        validate_sample(
            metric["rust"],
            f"metrics.{name}.rust",
            "milliseconds",
            summation,
            require_positive=require_positive,
            expected_samples=aggregate_samples,
            expected_warmups=CURRENT_WARMUPS if require_positive else None,
        )
        if not isinstance(metric["operations"], dict) or not metric["operations"]:
            raise HarnessError(f"metrics.{name}.operations is empty")
        if set(metric["operations"]) != required_operations:
            raise HarnessError(f"metrics.{name}.operations are incomplete")
        for operation, pair in metric["operations"].items():
            if not isinstance(operation, str):
                raise HarnessError("operation name is not a string")
            validate_paired_metric(
                pair,
                f"metrics.{name}.operations.{operation}",
                "milliseconds",
                summation,
                require_positive=require_positive,
                expected_samples=CURRENT_SAMPLES if require_positive else None,
                expected_warmups=CURRENT_WARMUPS if require_positive else None,
            )
    if version == SCHEMA_VERSION:
        validate_schema5_http_schedule(result["metrics"]["http"])
    if not isinstance(result["contracts"], list) or len(result["contracts"]) < 4:
        raise HarnessError("contracts are incomplete")
    for contract in result["contracts"]:
        if set(contract) != {"name", "command", "elapsed_ms", "exit_code", "stdout", "stderr"} or contract["exit_code"] != 0:
            raise HarnessError("contract record is not strict or passing")
    thresholds = result["thresholds"]
    if not isinstance(thresholds, dict):
        raise HarnessError("thresholds are not an object")
    estimator = latency_estimator_of(result)
    if version in ORDER_STRATIFIED_SCHEMA_VERSIONS:
        expected_threshold_keys = {
            "minimum_improvement", "maximum_latency_regression", "latency_estimator",
            "binary_size_reduction", "representative_rss_reduction_max",
            "improvement_pass", "latency_regressions", "latency_order_regressions",
            "latency_order_regression_intervals", "latency_interval_confidence",
            "latency_pass", "contracts_pass", "fail_closed",
        }
        if set(thresholds) != expected_threshold_keys:
            raise HarnessError(f"schema {version} threshold keys mismatch")
        if thresholds.get("minimum_improvement") != 0.20:
            raise HarnessError(f"schema {version} minimum improvement threshold changed")
        if thresholds.get("maximum_latency_regression") != 0.10:
            raise HarnessError(f"schema {version} maximum latency threshold changed")
        for key in ("binary_size_reduction", "representative_rss_reduction_max"):
            value = thresholds.get(key)
            if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value):
                raise HarnessError(f"schema {version} {key} is not finite")
        if not isinstance(thresholds.get("improvement_pass"), bool) or not isinstance(thresholds.get("contracts_pass"), bool):
            raise HarnessError(f"schema {version} pass flags are not boolean")
        if thresholds.get("fail_closed") is not True:
            raise HarnessError("order-stratified report is not fail closed")
        expected_order_regressions = latency_order_regressions(result["metrics"])
        recorded_order_regressions = thresholds.get("latency_order_regressions")
        if not isinstance(recorded_order_regressions, dict) or set(recorded_order_regressions) != set(expected_order_regressions):
            raise HarnessError("order-stratified latency regressions are incomplete")
        for name, expected_by_order in expected_order_regressions.items():
            actual_by_order = recorded_order_regressions.get(name)
            if not isinstance(actual_by_order, dict) or set(actual_by_order) != set(PAIR_ORDERS):
                raise HarnessError(f"{name} order-stratified latency regressions are incomplete")
            for order, expected in expected_by_order.items():
                actual = actual_by_order.get(order)
                if isinstance(actual, bool) or not isinstance(actual, (int, float)) or not math.isfinite(actual) or actual != expected:
                    raise HarnessError(f"{name}.{order} order-stratified regression is not recomputed")
        expected_regressions = latency_regressions(result["metrics"], estimator)
        if thresholds.get("latency_regressions") != expected_regressions:
            raise HarnessError("order-stratified latency regressions are not recomputed")
        expected_intervals = latency_order_regression_intervals(result["metrics"])
        recorded_intervals = thresholds.get("latency_order_regression_intervals")
        if not isinstance(recorded_intervals, dict) or set(recorded_intervals) != set(expected_intervals):
            raise HarnessError("order-stratified latency intervals are incomplete")
        for name, expected_by_order in expected_intervals.items():
            actual_by_order = recorded_intervals.get(name)
            if not isinstance(actual_by_order, dict) or set(actual_by_order) != set(PAIR_ORDERS):
                raise HarnessError(f"{name} order-stratified latency intervals are incomplete")
            for order, expected_interval in expected_by_order.items():
                actual_interval = actual_by_order.get(order)
                if not isinstance(actual_interval, list) or len(actual_interval) != 2:
                    raise HarnessError(f"{name}.{order} order-stratified interval is invalid")
                for actual, expected in zip(actual_interval, expected_interval):
                    if isinstance(actual, bool) or not isinstance(actual, (int, float)) or not math.isfinite(actual) or actual != expected:
                        raise HarnessError(f"{name}.{order} order-stratified interval is not recomputed")
        if thresholds.get("latency_interval_confidence") != MEDIAN_INTERVAL_CONFIDENCE:
            raise HarnessError("order-stratified latency interval confidence mismatch")
        expected_latency_pass = all(
            value <= 0.10
            for by_order in expected_order_regressions.values()
            for value in by_order.values()
        )
        if thresholds.get("latency_pass") is not expected_latency_pass:
            raise HarnessError("order-stratified latency pass flag is not recomputed")
    if not isinstance(result["passed"], bool):
        raise HarnessError("passed is not boolean")


def contract_checks(root: Path, go_binary: Path, rust_binary: Path) -> list[dict[str, Any]]:
    checks = [
        ["go", "run", "./scripts/rust-port/cmd/representativegen", "--check"],
        ["go", "run", "./scripts/rust-port/cmd/diffharness", "--symdesk-left", str(go_binary), "--symdesk-right", str(rust_binary), "--cases", "testdata/port/representative/cases.json", "--stage", "representative"],
        ["go", "run", "./scripts/rust-port/cmd/mcpdiff", "--left", str(go_binary), "--right", str(rust_binary), "--fixture", "testdata/port/mcp/representative.json"],
        ["go", "run", "./scripts/rust-port/cmd/httpdiff", "--left", str(go_binary), "--right", str(rust_binary), "--fixture", "testdata/port/http/representative.json"],
    ]
    results: list[dict[str, Any]] = []
    with tempfile.TemporaryDirectory(prefix="symdesk-value001-contract-") as contract_name:
        contract_root = Path(contract_name)
        contract_env = dict(os.environ)
        contract_env.update({"LANG": "C", "LC_ALL": "C", "TZ": "UTC", "NO_COLOR": "1", "GOTOOLCHAIN": "go1.26.6"})
        for command in checks:
            result = run_checked(command, root, 300.0, env=contract_env)
            result["name"] = command[2]
            results.append(result)
    return results


def prepare_index(binary: Path, home_root: Path, vault: Path, sidecar: Path, expected: dict[str, Any]) -> dict[str, Any]:
    env = benchmark_env(home_root, vault, sidecar)
    started = time.perf_counter()
    try:
        completed = subprocess.run(
            [str(binary), "ls", "--vault", str(vault), "--json"],
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=180.0,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise HarnessError(f"{binary.name} index preparation timed out after 180.0s") from exc
    elapsed = (time.perf_counter() - started) * 1000.0
    if completed.returncode != 0:
        raise HarnessError(f"{binary.name} index preparation failed: {completed.stderr[-2000:]}")
    validate_ls(completed.stdout, completed.stderr, expected)
    return {"command": command_text([str(binary), "ls", "--vault", str(vault), "--json"]), "elapsed_ms": elapsed, "exit_code": 0, "stdout_semantics": "exact expected path/title set", "documents": len(expected["paths"])}


def stop_all(servers: list[RunningServer]) -> None:
    errors: list[str] = []
    for server in reversed(servers):
        try:
            server.stop()
        except BaseException as exc:
            errors.append(f"{server.binary.name}: {exc}")
    if errors:
        raise HarnessError("; ".join(errors))


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--go-binary", type=Path, help="optional prebuilt Go binary; normally built from --go-source-commit")
    parser.add_argument("--go-source-commit", default=CURRENT_BEHAVIOUR_ORACLE)
    parser.add_argument("--rust-binary", type=Path, required=True)
    parser.add_argument("--rust-build-command", default="SYMDESK_VERSION=0.12.2 cargo build --release -p symdesk-cli --locked")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--samples", type=int, default=MIN_SAMPLES)
    parser.add_argument("--warmups", type=int, default=20)
    parser.add_argument("--rss-interval-ms", type=float, default=5.0)
    return parser.parse_args()


def incomplete_marker_path(output: Path) -> Path:
    return output.with_name(output.name + ".incomplete")


def mark_output_incomplete(output: Path) -> Path:
    """Make a stale output ineligible for approval until this run completes."""
    marker = incomplete_marker_path(output)
    payload = (json.dumps({"status": "incomplete", "started_at": utc_now(), "pid": os.getpid()}) + "\n").encode("utf-8")
    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(marker, flags, 0o600)
    except FileExistsError as exc:
        raise HarnessError(
            f"output has an incomplete-run marker: {marker}; inspect it and choose a fresh output"
        ) from exc
    except OSError as exc:
        raise HarnessError(f"cannot create incomplete-run marker {marker}: {exc}") from exc
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
    except OSError as exc:
        # Leaving a partial marker is deliberately fail-closed. Do not unlink
        # a path that an attacker could replace after its exclusive creation.
        raise HarnessError(f"cannot persist incomplete-run marker {marker}: {exc}") from exc
    return marker


def write_complete_result(output: Path, result: dict[str, Any]) -> None:
    """Self-validate then atomically replace an output artifact."""
    descriptor, temporary_name = tempfile.mkstemp(
        prefix=f".{output.name}.", suffix=".tmp", dir=output.parent
    )
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            json.dump(result, stream, indent=2)
            stream.write("\n")
        reloaded = json.loads(temporary.read_text(encoding="utf-8"))
        validate_result(reloaded)
        os.replace(temporary, output)
    finally:
        temporary.unlink(missing_ok=True)


def main() -> int:
    args = parse_args()
    if args.go_source_commit != CURRENT_BEHAVIOUR_ORACLE:
        raise HarnessError(
            "--go-source-commit must equal CURRENT_BEHAVIOUR_ORACLE "
            f"({CURRENT_BEHAVIOUR_ORACLE}) before any build"
        )
    root = args.root.resolve()
    rust_binary = args.rust_binary.expanduser().absolute()
    if args.samples != CURRENT_SAMPLES:
        raise HarnessError(f"--samples must equal {CURRENT_SAMPLES} for CURRENT VALUE-001")
    if args.warmups != CURRENT_WARMUPS:
        raise HarnessError(f"--warmups must equal {CURRENT_WARMUPS} for CURRENT VALUE-001")
    require_executable_regular(rust_binary, "Rust binary")
    if shutil.which("ps") is None:
        raise HarnessError("ps is required for long-running RSS measurement")
    if args.output.is_symlink():
        raise HarnessError(f"output must not be a symlink: {args.output}")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    marker = mark_output_incomplete(args.output)
    artifact_complete = False
    try:
        with tempfile.TemporaryDirectory(prefix="symdesk-value001-") as temp_name:
            temp_root = Path(temp_name)
            oracle_source: Path | None = None
            servers: list[RunningServer] = []
            try:
                if args.go_binary is not None:
                    go_binary = args.go_binary.resolve()
                    go_source = {
                        "commit": git_output(root, ["rev-parse", args.go_source_commit]),
                        "status": "prebuilt input; source build not performed",
                        "build_command": "external prebuilt input (not accepted for VALUE-001)",
                        "go_version": tool_version(["go", "version"], root),
                        "binary_sha256": sha256_file(go_binary),
                        "binary_bytes": go_binary.stat().st_size,
                    }
                    raise HarnessError("prebuilt Go input is deliberately rejected; VALUE-001 requires a clean oracle build")
                go_binary, go_source, oracle_source = build_go_oracle(root, args.go_source_commit, temp_root)
                go_binary = retain_binary(
                    go_binary,
                    durable_binary_path(args.output, "go"),
                    "Go oracle binary",
                )
                go_source["binary_sha256"] = sha256_file(go_binary)
                go_source["binary_bytes"] = go_binary.stat().st_size
                original_type = git_output(root, ["cat-file", "-t", ORIGINAL_VALUE_BASELINE])
                if original_type != "commit":
                    raise HarnessError(f"original VALUE baseline is not an immutable commit: {ORIGINAL_VALUE_BASELINE}")
                contracts = contract_checks(root, go_binary, rust_binary)

                go_home = temp_root / "go-search"
                rust_home = temp_root / "rust-search"
                go_vault = go_home / "vault"
                rust_vault = rust_home / "vault"
                go_manifest = write_vault(go_vault)
                rust_manifest = write_vault(rust_vault)
                expected_go = expected_vault_semantics(go_manifest)
                expected_rust = expected_vault_semantics(rust_manifest)
                go_env = benchmark_env(go_home, go_vault, go_home / "sidecar.db")
                rust_env = benchmark_env(rust_home, rust_vault, rust_home / "sidecar.db")
                index_preparation = {
                    "go": prepare_index(go_binary, go_home, go_vault, go_home / "sidecar.db", expected_go),
                    "rust": prepare_index(rust_binary, rust_home, rust_vault, rust_home / "sidecar.db", expected_rust),
                    "vault_justification": "10,000 Markdown documents (~1.6 MiB generated body/frontmatter) exercises recursive walk, 50 SQLite batches of 200, FTS/snippet search and realistic path/title metadata while keeping 100-pair measurement bounded.",
                }
                startup = measure_process_pair(go_binary, rust_binary, go_env, rust_env, ["version"], ["version"], args.warmups, args.samples, validate_version)
                search = measure_process_pair(go_binary, rust_binary, go_env, rust_env, ["search", SEARCH_TOKEN, "--vault", str(go_vault), "--json"], ["search", SEARCH_TOKEN, "--vault", str(rust_vault), "--json"], args.warmups, args.samples, lambda out, err: validate_search(out, err, expected_go))
                mcp = measure_mcp(go_binary, rust_binary, go_env, rust_env, expected_go, go_vault, rust_vault, args.warmups, args.samples)

                go_http_home = temp_root / "go-http"
                rust_http_home = temp_root / "rust-http"
                go_http_vault = go_http_home / "vault"
                rust_http_vault = rust_http_home / "vault"
                go_http_manifest = write_vault(go_http_vault, include_http_probe=True)
                rust_http_manifest = write_vault(rust_http_vault, include_http_probe=True)
                go_http_expected = expected_vault_semantics(go_http_manifest, include_http_probe=True)
                rust_http_expected = expected_vault_semantics(rust_http_manifest, include_http_probe=True)
                prepare_index(go_binary, go_http_home, go_http_vault, go_http_home / "sidecar.db", go_http_expected)
                prepare_index(rust_binary, rust_http_home, rust_http_vault, rust_http_home / "sidecar.db", rust_http_expected)
                go_server = RunningServer(go_binary, go_http_home, go_http_vault, go_http_home / "sidecar.db")
                servers.append(go_server)
                rust_server = RunningServer(rust_binary, rust_http_home, rust_http_vault, rust_http_home / "sidecar.db")
                servers.append(rust_server)
                http, rss = measure_http(go_server, rust_server, go_http_expected, rust_http_expected, args.warmups, args.samples, args.rss_interval_ms)
                metrics = {"startup": startup, "search": search, "mcp": mcp, "http": http, "rss": rss}
                result = build_result(root, go_binary, rust_binary, go_source, args.rust_build_command, contracts, metrics, go_manifest, args.samples, args.warmups, index_preparation)
                write_complete_result(args.output, result)
                artifact_complete = True
                print(json.dumps({"output": str(args.output), "passed": result["passed"], "thresholds": result["thresholds"]}, indent=2))
                if not result["passed"]:
                    raise HarnessError("VALUE-001 failed closed; see measured JSON artifact")
            finally:
                cleanup_error: BaseException | None = None
                try:
                    stop_all(servers)
                except BaseException as exc:
                    cleanup_error = exc
                finally:
                    if oracle_source is not None:
                        subprocess.run(["git", "worktree", "remove", "--force", str(oracle_source)], cwd=root, capture_output=True, text=True, check=False)
                if cleanup_error is not None:
                    raise cleanup_error
    finally:
        if artifact_complete:
            marker.unlink(missing_ok=True)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except HarnessError as error:
        print(f"FAIL {error}", file=sys.stderr)
        raise SystemExit(1)
