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
SCHEMA_VERSION = 2
ORIGINAL_VALUE_BASELINE = "ae86331930fdfa2b128b68ae5af7437091b9949a"
CURRENT_BEHAVIOUR_ORACLE = "136f01570944af16c4bc447b7eb63d03125aac3f"
DOC_COUNT = 10_000
SEARCH_TOKEN = "value001cohort042"
COHORT = 42
HTTP_FILE_CONTENT = "---\ntitle: HTTP Probe\ncreated: 2026-01-02T03:04:05Z\n---\nvalue001 http probe\n"


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
    return {
        "paths": paths,
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


def validate_ls(stdout: str, stderr: str, expected: dict[str, Any]) -> None:
    if stderr:
        raise HarnessError(f"unexpected ls stderr: {stderr!r}")
    value = parse_json(stdout, "ls")
    if not isinstance(value, list):
        raise HarnessError(f"ls result is not an array: {type(value).__name__}")
    paths = [item.get("path") for item in value if isinstance(item, dict)]
    if len(value) != len(expected["paths"]) or paths != expected["paths"]:
        raise HarnessError(f"ls paths mismatch: got {len(paths)} entries, expected {len(expected['paths'])}")
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
        validate_ls(text, "", expected)
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


def summary(values: list[float], unit: str, warmups: int, pair_orders: list[str]) -> dict[str, Any]:
    if len(values) < MIN_SAMPLES:
        raise HarnessError(f"{unit} has {len(values)} samples; at least {MIN_SAMPLES} are required")
    if len(pair_orders) != len(values):
        raise HarnessError("pair/order sample count does not match raw samples")
    return {
        "unit": unit,
        "warmup_samples": warmups,
        "samples": len(values),
        "min": min(values),
        "mean": sum(values) / len(values),
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
            completed = subprocess.run([str(binary), *args], input=input_data, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=30.0, check=False)
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
        ("desk_ls", mcp_request("tools/call", 4, "desk_ls")),
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


def measure_http(
    go_server: RunningServer,
    rust_server: RunningServer,
    go_expected: dict[str, Any],
    rust_expected: dict[str, Any],
    warmups: int,
    samples: int,
    rss_interval_ms: float,
) -> tuple[dict[str, Any], dict[str, Any]]:
    names = ["healthz", "status", "snapshot", "file-read", "file-range", "file-missing", "file-traversal"]
    values = {"go": [], "rust": []}
    rss_values = {"go": [], "rust": []}
    orders: list[str] = []
    per_operation: dict[str, Any] = {}
    for index in range(warmups + samples):
        go_first = index % 2 == 0
        if index >= warmups:
            orders.append("go-rust" if go_first else "rust-go")
        ordered_servers = (("go", go_server, go_expected), ("rust", rust_server, rust_expected)) if go_first else (("rust", rust_server, rust_expected), ("go", go_server, go_expected))
        round_values = {"go": [], "rust": []}
        for name, server, expected in ordered_servers:
            for operation_name in names:
                operation = http_operation(operation_name)
                started = time.perf_counter_ns()
                status, body, headers = server.request_raw(operation["method"], operation["path"], operation["auth"], operation.get("headers"))
                elapsed = (time.perf_counter_ns() - started) / 1_000_000.0
                validate_http(operation_name, status, body, headers, expected, server.vault)
                round_values[name].append(elapsed)
                if index >= warmups:
                    values[name].append(elapsed)
                    per_operation.setdefault(operation_name, {"go": [], "rust": []})[name].append(elapsed)
        if index >= warmups:
            if go_server.process is None or rust_server.process is None:
                raise HarnessError("server process disappeared during RSS measurement")
            rss_values["go"].append(float(rss_bytes(go_server.process.pid)))
            rss_values["rust"].append(float(rss_bytes(rust_server.process.pid)))
        if rss_interval_ms > 0:
            time.sleep(rss_interval_ms / 1000.0)
    http_metric = {
        "go": summary(values["go"], "milliseconds", warmups, orders * len(names)),
        "rust": summary(values["rust"], "milliseconds", warmups, orders * len(names)),
        "operations": {
            name: {
                "go": summary(data["go"], "milliseconds", warmups, orders),
                "rust": summary(data["rust"], "milliseconds", warmups, orders),
            }
            for name, data in per_operation.items()
        },
    }
    rss_metric = {
        "go": summary(rss_values["go"], "bytes", warmups, orders),
        "rust": summary(rss_values["rust"], "bytes", warmups, orders),
    }
    return http_metric, rss_metric


def ratio(candidate: float, reference: float) -> float:
    if reference <= 0:
        raise HarnessError(f"non-positive reference value {reference}")
    return candidate / reference


def build_go_oracle(root: Path, commit: str, temp_root: Path) -> tuple[Path, dict[str, Any], Path]:
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
) -> dict[str, Any]:
    go_size = go_binary.stat().st_size
    rust_size = rust_binary.stat().st_size
    size_reduction = (go_size - rust_size) / go_size
    rss_go = metrics["rss"]["go"]["max"]
    rss_rust = metrics["rss"]["rust"]["max"]
    rss_reduction = (rss_go - rss_rust) / rss_go
    regressions = {
        name: ratio(metrics[name]["rust"]["p95"], metrics[name]["go"]["p95"]) - 1.0
        for name in ("startup", "search", "mcp", "http")
    }
    latency_pass = all(value <= 0.10 for value in regressions.values())
    improvement_pass = size_reduction >= 0.20 or rss_reduction >= 0.20
    contracts_pass = all(item["exit_code"] == 0 for item in contracts)
    thresholds = {
        "minimum_improvement": 0.20,
        "maximum_p95_regression": 0.10,
        "binary_size_reduction": size_reduction,
        "representative_rss_reduction_max": rss_reduction,
        "improvement_pass": improvement_pass,
        "p95_regressions": regressions,
        "latency_pass": latency_pass,
        "contracts_pass": contracts_pass,
        "fail_closed": True,
    }
    result = {
        "schema_version": SCHEMA_VERSION,
        "benchmark": "VALUE-001",
        "captured_at": utc_now(),
        "runner": {"path": str(Path(__file__).relative_to(root)), "samples": samples, "warmups": warmups, "pairing": "alternating go-rust/rust-go per post-warmup round"},
        "host": {"os": sys_platform(), "os_version": platform.platform(), "arch": platform.machine(), "machine": platform.machine(), "python": platform.python_version()},
        "repository": {
            "root": str(root),
            "head": git_output(root, ["rev-parse", "HEAD"]),
            "status": git_output(root, ["status", "--porcelain=v1", "--untracked-files=all"]),
            "dirty_allowed": True,
            "candidate_diff_sha256": sha256_bytes(git_bytes(root, ["diff", "--binary", "HEAD"])),
            "original_value_baseline_commit": ORIGINAL_VALUE_BASELINE,
            "current_behaviour_oracle_commit": CURRENT_BEHAVIOUR_ORACLE,
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


def validate_sample(value: Any, name: str, unit: str) -> None:
    if not isinstance(value, dict):
        raise HarnessError(f"{name} is not an object")
    required = {"unit", "warmup_samples", "samples", "min", "mean", "p50", "p95", "p99", "max", "raw", "pair_order", "max_observed"}
    if set(value) != required:
        raise HarnessError(f"{name} keys mismatch: {set(value)!r}")
    if value["unit"] != unit or value["warmup_samples"] < 1 or value["samples"] < MIN_SAMPLES:
        raise HarnessError(f"{name} sample metadata invalid")
    if len(value["raw"]) != value["samples"] or len(value["pair_order"]) != value["samples"]:
        raise HarnessError(f"{name} raw/order lengths mismatch")
    if any(not isinstance(item, (int, float)) or item < 0 for item in value["raw"]):
        raise HarnessError(f"{name} contains invalid raw values")
    if value["max"] != max(value["raw"]) or value["max_observed"] != value["max"]:
        raise HarnessError(f"{name} maximum is not the maximum raw observation")
    if any(order not in {"go-rust", "rust-go"} for order in value["pair_order"]):
        raise HarnessError(f"{name} contains an invalid pair order")


def validate_paired_metric(value: Any, name: str, unit: str) -> None:
    if not isinstance(value, dict) or set(value) != {"go", "rust"}:
        raise HarnessError(f"{name} paired metric keys invalid")
    validate_sample(value["go"], f"{name}.go", unit)
    validate_sample(value["rust"], f"{name}.rust", unit)


def validate_result(result: dict[str, Any]) -> None:
    required = {"schema_version", "benchmark", "captured_at", "runner", "host", "repository", "toolchains", "binaries", "vault", "index_preparation", "contracts", "metrics", "thresholds", "passed"}
    if set(result) != required or result["schema_version"] != SCHEMA_VERSION or result["benchmark"] != "VALUE-001":
        raise HarnessError("result top-level schema mismatch")
    try:
        dt.datetime.fromisoformat(result["captured_at"].replace("Z", "+00:00"))
    except (TypeError, ValueError) as exc:
        raise HarnessError("captured_at is not RFC3339") from exc
    for name in ("startup", "search", "rss"):
        validate_paired_metric(result["metrics"][name], f"metrics.{name}", "milliseconds" if name != "rss" else "bytes")
    for name in ("mcp", "http"):
        metric = result["metrics"][name]
        if not isinstance(metric, dict) or set(metric) != {"go", "rust", "operations"}:
            raise HarnessError(f"metrics.{name} operation metric keys invalid")
        validate_sample(metric["go"], f"metrics.{name}.go", "milliseconds")
        validate_sample(metric["rust"], f"metrics.{name}.rust", "milliseconds")
        if not isinstance(metric["operations"], dict) or not metric["operations"]:
            raise HarnessError(f"metrics.{name}.operations is empty")
        for operation, pair in metric["operations"].items():
            if not isinstance(operation, str):
                raise HarnessError("operation name is not a string")
            validate_paired_metric(pair, f"metrics.{name}.operations.{operation}", "milliseconds")
    if not isinstance(result["contracts"], list) or len(result["contracts"]) < 4:
        raise HarnessError("contracts are incomplete")
    for contract in result["contracts"]:
        if set(contract) != {"name", "command", "elapsed_ms", "exit_code", "stdout", "stderr"} or contract["exit_code"] != 0:
            raise HarnessError("contract record is not strict or passing")
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
    completed = subprocess.run([str(binary), "ls", "--vault", str(vault), "--json"], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=180.0, check=False)
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


def main() -> int:
    args = parse_args()
    root = args.root.resolve()
    rust_binary = args.rust_binary.resolve()
    if args.samples < MIN_SAMPLES:
        raise HarnessError(f"--samples must be at least {MIN_SAMPLES}")
    if args.warmups < 1:
        raise HarnessError("--warmups must be positive")
    if not rust_binary.is_file() or not os.access(rust_binary, os.X_OK):
        raise HarnessError(f"Rust binary is not executable: {rust_binary}")
    if shutil.which("ps") is None:
        raise HarnessError("ps is required for long-running RSS measurement")
    args.output.parent.mkdir(parents=True, exist_ok=True)
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
            args.output.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
            reloaded = json.loads(args.output.read_text(encoding="utf-8"))
            validate_result(reloaded)
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
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except HarnessError as error:
        print(f"FAIL {error}", file=sys.stderr)
        raise SystemExit(1)
