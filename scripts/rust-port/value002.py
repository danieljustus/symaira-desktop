#!/usr/bin/env python3
"""Run the fail-closed VALUE-002 full-release Go/Rust value gate.

VALUE-002 re-applies the FIXED VALUE-001 thresholds to complete release
candidates (the dual-binary ``symdesk`` + ``symroom`` archive) under fuller
workloads.  Nothing about a threshold or a metric is redefined here:

* improvement: >=20% smaller combined ``symdesk``+``symroom`` release binaries
  OR >=20% lower maximum RSS under the representative workload;
* latency: startup, indexed search, HTTP and MCP p95 may regress by at most
  10%, decided with the schema-6 order-stratified 95% interval rule that
  ``value001.py`` already implements (and reused verbatim from there).

New in VALUE-002, declared before any result existed:

* the VALUE-001 representative slice is re-measured with the real release
  candidates and is validated by ``value001.validate_result`` itself;
* each new deterministic workload is a paired Go/Rust latency pair decided by
  the same interval rule: ``symroom-startup``, ``index-build`` (cold full
  index: fresh sidecar and state per run), ``graph``, ``backlinks``,
  ``relations-inverse`` and ``duplicates`` (offline SimHash retrieval);
* a long-running HTTP server plus a persistent MCP stdio server are driven by
  a fixed mixed request mix and their maximum RSS over the run is reported
  (descriptive; it does not replace the declared RSS criterion);
* release archive sizes are reported next to the binary sizes.

The runner never builds anything: compilers only run as direct commands that
``record-build`` documents.  It fails closed on any contract, validation,
quiet-host or provenance error.
"""
from __future__ import annotations

import argparse
import contextlib
import datetime as dt
import hashlib
import io
import json
import math
import os
import platform
import re
import select
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import urllib.error
from pathlib import Path
from typing import Any, Callable, Iterator

sys.path.insert(0, str(Path(__file__).resolve().parent))
import value001 as v1  # noqa: E402  (shared, unmodified VALUE-001 machinery)

HarnessError = v1.HarnessError

SCHEMA_VERSION = 1
BENCHMARK = "VALUE-002"
FINAL_SAMPLES = 100
FINAL_WARMUPS = 20
QUIET_LOAD1_LIMIT = 6.0
PUBLISHED_GO_ARCHIVE_BYTES = 12_629_014
RELEASE_VERSION = "0.12.2"
RELEASE_OS = "darwin"
RELEASE_ARCH = "arm64"
PAIRING = (
    "VALUE-001 schema-6 pairing for the representative slice; every new "
    "workload alternates go-rust/rust-go per post-warmup round with untimed "
    "per-run setup; sustained load runs each side alone in alternating order"
)
NEW_WORKLOADS = (
    "symroom-startup",
    "index-build",
    "graph",
    "backlinks",
    "relations-inverse",
    "duplicates",
)
LINK_TARGET_INDEX = 42
PROBE_RUNS_FINAL = 5
SUSTAINED_ROUNDS = 2
SUSTAINED_HTTP_REQUESTS_FINAL = 3000
SUSTAINED_MCP_REQUESTS_FINAL = 1500
SUSTAINED_RSS_INTERVAL_S = 0.1
SUSTAINED_HTTP_CYCLE = ("snapshot",) + ("healthz", "status", "file-read", "file-range", "file-missing", "file-traversal") * 4
SUSTAINED_MCP_CYCLE = ("desk_status", "desk_search", "desk_ls")
REQUIRED_BUILDS = (
    "go-symdesk",
    "go-symroom",
    "go-tool-representativegen",
    "go-tool-diffharness",
    "go-tool-mcpdiff",
    "go-tool-httpdiff",
    "rust-release-archive",
    "go-archive",
)
# Files allowed to differ between the measured candidate commit and the commit
# that carries the harness and evidence.  Anything else (crates, cmd, internal,
# go.mod, ...) means the binaries were not built from this tree.
HARNESS_PATH_PREFIXES = ("scripts/rust-port/", "docs/rust-port/", "Makefile")
BUILD_EXECUTABLES = frozenset({"cargo", "rustc", "xcodebuild", "swift-frontend"})
EMPTY_DIFF_SHA256 = hashlib.sha256(b"").hexdigest()


# --------------------------------------------------------------------------
# profile handling (smoke runs exercise the whole pipeline with tiny counts)
# --------------------------------------------------------------------------
_PROFILE_NAMES = ("MIN_SAMPLES", "CURRENT_SAMPLES", "CURRENT_WARMUPS", "MIN_ORDER_COHORT_SAMPLES")


@contextlib.contextmanager
def profile_constants(profile: str, samples: int, warmups: int) -> Iterator[None]:
    """Pin value001's sample constants for the profile and restore them."""
    if profile == "final":
        if samples != FINAL_SAMPLES or warmups != FINAL_WARMUPS:
            raise HarnessError(
                f"final profile requires exactly {FINAL_SAMPLES} samples and {FINAL_WARMUPS} warmups"
            )
        if (v1.MIN_SAMPLES, v1.CURRENT_SAMPLES, v1.CURRENT_WARMUPS) != (100, 100, 20):
            raise HarnessError("value001 sample constants were altered")
        yield
        return
    if profile != "smoke":
        raise HarnessError(f"unknown profile {profile!r}")
    if samples < 4 or samples % 2 or warmups < 2 or warmups % 2:
        raise HarnessError("smoke profile needs an even sample count >= 4 and an even warmup count >= 2")
    saved = {name: getattr(v1, name) for name in _PROFILE_NAMES}
    v1.MIN_SAMPLES = samples
    v1.CURRENT_SAMPLES = samples
    v1.CURRENT_WARMUPS = warmups
    v1.MIN_ORDER_COHORT_SAMPLES = samples // 2
    try:
        yield
    finally:
        for name, value in saved.items():
            setattr(v1, name, value)


# --------------------------------------------------------------------------
# host quietness and load
# --------------------------------------------------------------------------
def parse_build_processes(ps_text: str, own_pids: set[int]) -> list[str]:
    """Return live build processes from ``ps -axo pid=,command=`` output.

    A process counts when its executable is cargo/rustc/xcodebuild/
    swift-frontend or lives under a ``go-build`` tree (the compiler and the
    test binaries ``go`` builds).  Shell wrappers that merely mention those
    words in their command line are not builds.
    """
    found: list[str] = []
    for line in ps_text.splitlines():
        parts = line.strip().split(None, 1)
        if len(parts) != 2 or not parts[0].isdigit():
            continue
        pid, command = int(parts[0]), parts[1]
        if pid in own_pids:
            continue
        executable = command.split(None, 1)[0]
        if os.path.basename(executable) in BUILD_EXECUTABLES or "/go-build" in executable:
            found.append(f"{pid} {command[:200]}")
    return found


def host_snapshot() -> dict[str, Any]:
    load1, load5, load15 = os.getloadavg()
    ps = subprocess.run(["ps", "-axww", "-o", "pid=,command="], capture_output=True, text=True, check=False)
    own = {os.getpid(), os.getppid()}
    builds = parse_build_processes(ps.stdout, own)
    uptime = subprocess.run(["uptime"], capture_output=True, text=True, check=False).stdout.strip()
    return {
        "captured_at": v1.utc_now(),
        "load1": load1,
        "load5": load5,
        "load15": load15,
        "uptime": uptime,
        "build_processes": builds,
        "quiet": load1 < QUIET_LOAD1_LIMIT and not builds,
    }


class LoadTimeline:
    """Sample the 1-minute load average in the background during the run."""

    def __init__(self, interval_s: float = 30.0) -> None:
        self.interval_s = interval_s
        self.samples: list[dict[str, float]] = []
        self._stop = threading.Event()
        self._start = time.monotonic()
        self._thread = threading.Thread(target=self._run, daemon=True)

    def _run(self) -> None:
        while not self._stop.wait(self.interval_s):
            self.samples.append({"t_s": round(time.monotonic() - self._start, 1), "load1": os.getloadavg()[0]})

    def __enter__(self) -> "LoadTimeline":
        self._thread.start()
        return self

    def __exit__(self, *_exc: Any) -> None:
        self._stop.set()
        self._thread.join(timeout=5.0)
        self.samples.append({"t_s": round(time.monotonic() - self._start, 1), "load1": os.getloadavg()[0]})


# --------------------------------------------------------------------------
# build provenance
# --------------------------------------------------------------------------
def file_record(path: Path) -> dict[str, Any]:
    if path.is_symlink() or not path.is_file():
        raise HarnessError(f"artifact must be a regular file: {path}")
    return {"path": str(path), "bytes": path.stat().st_size, "sha256": v1.sha256_file(path)}


def tree_state(root: Path) -> dict[str, Any]:
    status = v1.git_output(root, ["status", "--porcelain=v1", "--untracked-files=all"])
    return {
        "commit": v1.git_output(root, ["rev-parse", "HEAD"]),
        "tree": v1.git_output(root, ["rev-parse", "HEAD^{tree}"]),
        "tracked_diff_sha256": v1.sha256_bytes(v1.git_bytes(root, ["diff", "--binary", "HEAD"])),
        "status": status,
    }


def record_build(
    record_path: Path,
    root: Path,
    name: str,
    command: str,
    exit_code: int,
    artifacts: list[Path],
    env: dict[str, str],
    toolchain: str,
) -> dict[str, Any]:
    """Append one build/packaging step to the provenance record (fail closed)."""
    if exit_code != 0:
        raise HarnessError(f"refusing to record a failed build {name!r} (exit {exit_code})")
    if not artifacts:
        raise HarnessError("a build record needs at least one artifact")
    state = tree_state(root)
    if state["tracked_diff_sha256"] != EMPTY_DIFF_SHA256:
        raise HarnessError("tracked files differ from HEAD; build from a clean candidate tree")
    record: dict[str, Any] = {"schema_version": 1, "candidate": None, "entries": []}
    if record_path.exists():
        record = json.loads(record_path.read_text(encoding="utf-8"))
    candidate = {"commit": state["commit"], "tree": state["tree"], "tracked_diff_sha256": state["tracked_diff_sha256"]}
    if record["candidate"] is None:
        record["candidate"] = candidate
    elif record["candidate"] != candidate:
        raise HarnessError("build record belongs to a different candidate commit/tree")
    record["entries"] = [entry for entry in record["entries"] if entry["name"] != name]
    record["entries"].append(
        {
            "name": name,
            "command": command,
            "cwd": str(root),
            "env": env,
            "toolchain": toolchain,
            "exit_code": exit_code,
            "recorded_at": v1.utc_now(),
            "untracked_at_build": [line[3:] for line in state["status"].splitlines() if line.startswith("?? ")],
            "artifacts": [file_record(path) for path in artifacts],
        }
    )
    record["entries"].sort(key=lambda entry: entry["name"])
    record_path.parent.mkdir(parents=True, exist_ok=True)
    record_path.write_text(json.dumps(record, indent=2) + "\n", encoding="utf-8")
    return record


def validate_build_record(record: Any) -> dict[str, dict[str, Any]]:
    """Validate the provenance record's shape and return entries by name."""
    if not isinstance(record, dict) or set(record) != {"schema_version", "candidate", "entries"}:
        raise HarnessError("build record shape mismatch")
    candidate = record["candidate"]
    if (
        record["schema_version"] != 1
        or not isinstance(candidate, dict)
        or set(candidate) != {"commit", "tree", "tracked_diff_sha256"}
        or not v1.is_lower_hex(candidate["commit"], 40)
        or not v1.is_lower_hex(candidate["tree"], 40)
        or candidate["tracked_diff_sha256"] != EMPTY_DIFF_SHA256
    ):
        raise HarnessError("build record candidate is invalid or was built from a dirty tracked tree")
    entries: dict[str, dict[str, Any]] = {}
    for entry in record["entries"]:
        if not isinstance(entry, dict) or set(entry) != {
            "name", "command", "cwd", "env", "toolchain", "exit_code", "recorded_at", "untracked_at_build", "artifacts",
        }:
            raise HarnessError("build record entry shape mismatch")
        if entry["exit_code"] != 0 or not entry["command"] or not entry["artifacts"]:
            raise HarnessError(f"build record entry {entry.get('name')!r} did not succeed")
        for artifact in entry["artifacts"]:
            if (
                not isinstance(artifact, dict)
                or set(artifact) != {"path", "bytes", "sha256"}
                or not v1.is_lower_hex(artifact["sha256"], 64)
                or not isinstance(artifact["bytes"], int)
                or artifact["bytes"] <= 0
            ):
                raise HarnessError(f"build record artifact invalid in {entry['name']!r}")
        if entry["name"] in entries:
            raise HarnessError(f"duplicate build record entry {entry['name']!r}")
        entries[entry["name"]] = entry
    missing = [name for name in REQUIRED_BUILDS if name not in entries]
    if missing:
        raise HarnessError(f"build record misses required entries: {missing}")
    return entries


def artifact_sha(entry: dict[str, Any], basename: str) -> str:
    matches = [a["sha256"] for a in entry["artifacts"] if os.path.basename(a["path"]) == basename]
    if len(matches) != 1:
        raise HarnessError(f"build record {entry['name']!r} must list exactly one artifact named {basename}")
    return matches[0]


ARCHIVE_MEMBERS = ("LICENSE", "README.md", "VAULT.md", "symdesk", "symroom")


def extract_archive(archive: Path, destination: Path) -> dict[str, dict[str, Any]]:
    """Extract exactly the release members; return their file records."""
    destination.mkdir(parents=True, exist_ok=True)
    records: dict[str, dict[str, Any]] = {}
    with tarfile.open(archive) as tar:
        members = tar.getmembers()
        names = [member.name for member in members]
        if sorted(names) != sorted(ARCHIVE_MEMBERS) or any(not member.isreg() for member in members):
            raise HarnessError(f"{archive.name} members {names} differ from {list(ARCHIVE_MEMBERS)}")
        for member in members:
            stream = tar.extractfile(member)
            if stream is None:
                raise HarnessError(f"cannot read archive member {member.name}")
            target = destination / member.name
            target.write_bytes(stream.read())
            os.chmod(target, 0o755 if member.name in ("symdesk", "symroom") else 0o644)
            records[member.name] = file_record(target)
    return records


# --------------------------------------------------------------------------
# linked vault (graph / backlinks / relations / duplicates / index build)
# --------------------------------------------------------------------------
def link_targets(index: int, count: int = v1.DOC_COUNT) -> tuple[int, int, int]:
    """Deterministic, distinct, non-self link targets: body a,b and frontmatter c."""
    a = (index + 1) % count
    c = (index + 3) % count
    b = (7 * index + 3) % count
    while b in (index, a, c):
        b = (b + 1) % count
    return a, b, c


def note_name(index: int) -> str:
    return f"note-{index:05d}"


def linked_document_content(index: int, count: int = v1.DOC_COUNT) -> str:
    a, b, c = link_targets(index, count)
    content = v1.document_content(index)
    end = content.index("\n---\n", 4)
    related = f'\nrelated: "[[{note_name(c)}]]"'
    return content[:end] + related + content[end:] + f"links [[{note_name(a)}]] [[{note_name(b)}]]\n"


def write_linked_vault(vault: Path, count: int = v1.DOC_COUNT) -> dict[str, Any]:
    vault.mkdir(parents=True, exist_ok=True)
    paths: list[str] = []
    titles: dict[str, str] = {}
    total = 0
    for index in range(count):
        relative = v1.document_path(index)
        path = vault / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        content = linked_document_content(index, count)
        path.write_text(content, encoding="utf-8")
        os.chmod(path, 0o600)
        paths.append(relative)
        titles[relative] = f"Value Gate Note {index:05d}"
        total += len(content.encode())
    return {"documents": count, "bytes": total, "paths": sorted(paths), "titles": titles}


def link_graph(count: int = v1.DOC_COUNT) -> dict[str, Any]:
    """Ground truth derived from the generator (never from either binary)."""
    edges: set[tuple[str, str]] = set()
    body: dict[str, set[str]] = {}
    related: dict[str, set[str]] = {}
    for index in range(count):
        source = v1.document_path(index)
        a, b, c = link_targets(index, count)
        # The links table (graph edges, backlinks) holds body wikilinks only;
        # frontmatter ``related`` is a property, surfaced by relations inverse.
        for target in (a, b):
            edges.add((source, v1.document_path(target)))
            body.setdefault(v1.document_path(target), set()).add(source)
        related.setdefault(v1.document_path(c), set()).add(source)
    return {"edges": edges, "body": body, "related": related}


def expected_linked(count: int = v1.DOC_COUNT) -> dict[str, Any]:
    graph = link_graph(count)
    target = v1.document_path(LINK_TARGET_INDEX)
    sources = sorted(graph["body"].get(target, set()))
    inverse = sorted(
        {(src, "_link") for src in graph["body"].get(target, set())}
        | {(src, "related") for src in graph["related"].get(target, set())}
    )
    return {
        "target": target,
        "edges": graph["edges"],
        "nodes": {v1.document_path(i): f"Value Gate Note {i:05d}" for i in range(count)},
        "backlinks": sources,
        "inverse": inverse,
        "count": count,
    }


def canonical_sha(value: Any) -> str:
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def validate_graph(stdout: str, stderr: str, expected: dict[str, Any]) -> Any:
    if stderr:
        raise HarnessError(f"unexpected graph stderr: {stderr!r}")
    value = v1.parse_json(stdout, "graph")
    if not isinstance(value, dict) or set(value) != {"nodes", "edges"}:
        raise HarnessError("graph envelope mismatch")
    nodes = {}
    for item in value["nodes"]:
        if not isinstance(item, dict) or set(item) != {"id", "label"}:
            raise HarnessError(f"graph node shape mismatch: {item!r}")
        nodes[item["id"]] = item["label"]
    if len(nodes) != len(value["nodes"]) or nodes != expected["nodes"]:
        raise HarnessError("graph nodes differ from the generated vault")
    edges = [(item.get("source"), item.get("target")) for item in value["edges"] if isinstance(item, dict)]
    if len(edges) != len(value["edges"]) or len(set(edges)) != len(edges) or set(edges) != expected["edges"]:
        raise HarnessError(f"graph edges differ from the generated link structure: {len(edges)} vs {len(expected['edges'])}")
    return {"nodes": sorted(nodes), "edges": sorted(edges)}


def validate_backlinks(stdout: str, stderr: str, expected: dict[str, Any]) -> Any:
    if stderr:
        raise HarnessError(f"unexpected backlinks stderr: {stderr!r}")
    value = v1.parse_json(stdout, "backlinks")
    if not isinstance(value, list) or sorted(value) != expected["backlinks"] or len(set(value)) != len(value):
        raise HarnessError(f"backlinks mismatch: got {value!r}, expected {expected['backlinks']!r}")
    return sorted(value)


def validate_inverse(stdout: str, stderr: str, expected: dict[str, Any]) -> Any:
    if stderr:
        raise HarnessError(f"unexpected relations stderr: {stderr!r}")
    value = v1.parse_json(stdout, "relations inverse")
    if not isinstance(value, list):
        raise HarnessError("relations inverse is not an array")
    pairs = []
    for item in value:
        if not isinstance(item, dict) or set(item) != {"source", "title", "property"}:
            raise HarnessError(f"relations entry shape mismatch: {item!r}")
        if item["title"] != expected["nodes"].get(item["source"]):
            raise HarnessError(f"relations title mismatch: {item!r}")
        pairs.append((item["source"], item["property"]))
    if sorted(pairs) != expected["inverse"] or len(set(pairs)) != len(pairs):
        raise HarnessError(f"relations inverse mismatch: got {sorted(pairs)!r}, expected {expected['inverse']!r}")
    return sorted(pairs)


def validate_duplicates(stdout: str, stderr: str, expected: dict[str, Any]) -> Any:
    """Offline SimHash groups: strictly shaped, within the vault, non-empty."""
    if stderr:
        raise HarnessError(f"unexpected duplicates stderr: {stderr!r}")
    value = v1.parse_json(stdout, "duplicates")
    if not isinstance(value, list) or not value:
        raise HarnessError("duplicates produced no groups for a corpus that must contain near-duplicates")
    for group in value:
        if not isinstance(group, dict) or set(group) != {"path", "title", "members"}:
            raise HarnessError(f"duplicate group shape mismatch: {str(group)[:200]}")
        if group["path"] not in expected["nodes"] or not isinstance(group["members"], list) or not group["members"]:
            raise HarnessError(f"duplicate group is not within the vault: {group['path']!r}")
        for member in group["members"]:
            if not isinstance(member, dict) or set(member) != {"path", "title", "similarity"}:
                raise HarnessError(f"duplicate member shape mismatch: {member!r}")
            if member["path"] not in expected["nodes"] or not isinstance(member["similarity"], int):
                raise HarnessError(f"duplicate member invalid: {member!r}")
    return value


def validate_index_build(stdout: str, stderr: str, expected: dict[str, Any]) -> Any:
    shaped = {"paths": sorted(expected["nodes"]), "titles": expected["nodes"]}
    v1.validate_ls(stdout, stderr, shaped)
    value = json.loads(stdout)
    return [[item["path"], item["title"]] for item in value]


def validate_symroom_version(stdout: str, stderr: str) -> Any:
    if stdout != f"symroom {RELEASE_VERSION}\n" or stderr:
        raise HarnessError(f"invalid symroom version contract: stdout={stdout!r} stderr={stderr!r}")
    return stdout


# --------------------------------------------------------------------------
# paired workload measurement with untimed per-run setup
# --------------------------------------------------------------------------
Validator = Callable[[str, str], Any]
Before = Callable[[], None]


def measure_workload(
    go_cmd: list[str],
    rust_cmd: list[str],
    go_env: dict[str, str],
    rust_env: dict[str, str],
    go_validate: Validator,
    rust_validate: Validator,
    go_before: Before | None,
    rust_before: Before | None,
    warmups: int,
    samples: int,
    probe_runs: int,
) -> dict[str, Any]:
    values: dict[str, list[float]] = {"go": [], "rust": []}
    orders: list[str] = []
    digests: dict[str, set[str]] = {"go": set(), "rust": set()}
    prepared = 0
    for index in range(warmups + samples):
        go_first = index % 2 == 0
        orders.append("go-rust" if go_first else "rust-go")
        sides = (
            (("go", go_cmd, go_env, go_validate, go_before), ("rust", rust_cmd, rust_env, rust_validate, rust_before))
            if go_first
            else (("rust", rust_cmd, rust_env, rust_validate, rust_before), ("go", go_cmd, go_env, go_validate, go_before))
        )
        for name, command, env, validate, before in sides:
            if before is not None:
                before()
                prepared += 1
            started = time.perf_counter_ns()
            try:
                completed = subprocess.run(
                    command, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, timeout=300.0, check=False
                )
            except subprocess.TimeoutExpired as exc:
                raise HarnessError(f"{name} {v1.command_text(command)} timed out") from exc
            elapsed = (time.perf_counter_ns() - started) / 1_000_000.0
            if completed.returncode != 0:
                raise HarnessError(f"{name} {v1.command_text(command)} failed: {completed.stderr[-1000:]}")
            canonical = validate(completed.stdout, completed.stderr)
            digests[name].add(canonical_sha(canonical))
            if index >= warmups:
                values[name].append(elapsed)
    for name, seen in digests.items():
        if len(seen) != 1:
            raise HarnessError(f"{name} {v1.command_text(go_cmd if name == 'go' else rust_cmd)} output is not deterministic")
    go_digest, rust_digest = next(iter(digests["go"])), next(iter(digests["rust"]))
    if go_digest != rust_digest:
        raise HarnessError(f"Go and Rust outputs differ for {v1.command_text(go_cmd)}")
    peak: dict[str, list[int]] = {"go": [], "rust": []}
    for _ in range(probe_runs):
        for name, command, env, validate, before in (
            ("go", go_cmd, go_env, go_validate, go_before),
            ("rust", rust_cmd, rust_env, rust_validate, rust_before),
        ):
            if before is not None:
                before()
            probe = subprocess.run(
                ["/usr/bin/time", "-l", *command], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                timeout=300.0, check=False,
            )
            match = re.search(r"^\s*(\d+)\s+maximum resident set size", probe.stderr, re.MULTILINE)
            if probe.returncode != 0 or match is None:
                raise HarnessError(f"{name} RSS probe failed for {v1.command_text(command)}: {probe.stderr[-500:]}")
            peak[name].append(int(match.group(1)))
    return {
        "pair": {
            "go": v1.summary(values["go"], "milliseconds", warmups, orders[warmups:]),
            "rust": v1.summary(values["rust"], "milliseconds", warmups, orders[warmups:]),
        },
        "command": {"go": v1.command_text(go_cmd), "rust": v1.command_text(rust_cmd)},
        "output_sha256": {"go": go_digest, "rust": rust_digest},
        "outputs_equal": True,
        "setup_runs": prepared,
        "peak_rss_bytes": peak,
    }


def cold_reset(home_root: Path, sidecar: Path) -> Before:
    """Return an untimed hook that removes every derived index before a run."""
    state = home_root / "home" / ".local" / "share" / "symdesk"

    def reset() -> None:
        shutil.rmtree(state, ignore_errors=True)
        for suffix in ("", "-wal", "-shm", "-journal"):
            Path(str(sidecar) + suffix).unlink(missing_ok=True)
        if state.exists() or sidecar.exists():
            raise HarnessError("cold index state was not removed")

    return reset


# --------------------------------------------------------------------------
# sustained mixed HTTP + MCP load with long-running RSS
# --------------------------------------------------------------------------
class McpSession:
    """A persistent stdio MCP server driven over many JSON-RPC requests."""

    def __init__(self, binary: Path, home_root: Path, vault: Path, sidecar: Path) -> None:
        self.binary = binary
        home_root.mkdir(parents=True, exist_ok=True)
        self.log_path = home_root / "mcp-session.log"
        self.log = self.log_path.open("w", encoding="utf-8")
        self.buffer = b""
        env = v1.benchmark_env(home_root, vault, sidecar)
        self.process = subprocess.Popen(
            [str(binary), "mcp"], cwd=home_root, env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=self.log, bufsize=0, start_new_session=(os.name != "nt"),
        )

    @property
    def pid(self) -> int:
        return self.process.pid

    def call(self, request: str, timeout: float = 30.0) -> str:
        if self.process.poll() is not None:
            raise HarnessError(f"{self.binary.name} mcp session exited early: {self.log_path.read_text()[-2000:]}")
        assert self.process.stdin is not None and self.process.stdout is not None
        self.process.stdin.write(request.encode())
        deadline = time.monotonic() + timeout
        while b"\n" not in self.buffer:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise HarnessError(f"{self.binary.name} mcp session timed out")
            ready, _, _ = select.select([self.process.stdout], [], [], remaining)
            if not ready:
                continue
            chunk = os.read(self.process.stdout.fileno(), 1 << 20)
            if not chunk:
                raise HarnessError(f"{self.binary.name} mcp session closed stdout")
            self.buffer += chunk
        line, _, self.buffer = self.buffer.partition(b"\n")
        return line.decode() + "\n"

    def stop(self) -> None:
        process = self.process
        try:
            if process.poll() is None:
                if process.stdin is not None:
                    process.stdin.close()
                try:
                    process.wait(timeout=5.0)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGTERM)
                    process.wait(timeout=5.0)
            if process.returncode not in (0, -signal.SIGTERM, 143):
                raise HarnessError(f"{self.binary.name} mcp session did not exit cleanly: {process.returncode}")
        finally:
            if process.stdout is not None:
                process.stdout.close()
            if not self.log.closed:
                self.log.close()


def latency_digest(values: list[float]) -> dict[str, Any]:
    return {
        "count": len(values),
        "p50_ms": v1.percentile(values, 0.50),
        "p95_ms": v1.percentile(values, 0.95),
        "max_ms": max(values),
    }


def sustained_run(
    side: str,
    binary: Path,
    home_root: Path,
    vault: Path,
    sidecar: Path,
    expected: dict[str, Any],
    http_requests: int,
    mcp_requests: int,
) -> dict[str, Any]:
    server = v1.RunningServer(binary, home_root, vault, sidecar)
    session: McpSession | None = None
    errors: list[BaseException] = []
    http_ms: list[float] = []
    mcp_ms: list[float] = []
    series: dict[str, list[int]] = {"server": [], "mcp": []}
    stop_sampling = threading.Event()
    try:
        session = McpSession(binary, home_root, vault, sidecar)
        if server.process is None:
            raise HarnessError("server process is not running")
        server_process = server.process
        idle = {"server": v1.rss_bytes(server_process.pid), "mcp": v1.rss_bytes(session.pid)}
        mcp_specs = {
            "desk_status": v1.mcp_request("tools/call", 3, "desk_status"),
            "desk_search": v1.mcp_request("tools/call", 5, "desk_search", {"query": v1.SEARCH_TOKEN}),
            "desk_ls": v1.mcp_request("tools/call", 4, "desk_ls", {"dir": str(vault / v1.MCP_LS_DIR)}),
        }

        def http_worker() -> None:
            try:
                for i in range(http_requests):
                    name = SUSTAINED_HTTP_CYCLE[i % len(SUSTAINED_HTTP_CYCLE)]
                    operation = v1.http_operation(name)
                    started = time.perf_counter_ns()
                    status, body, headers = server.request_raw(
                        operation["method"], operation["path"], operation["auth"], operation.get("headers")
                    )
                    http_ms.append((time.perf_counter_ns() - started) / 1_000_000.0)
                    v1.validate_http(name, status, body, headers, expected, vault)
            except BaseException as exc:  # noqa: BLE001 - re-raised by the coordinator
                errors.append(exc)

        def mcp_worker() -> None:
            try:
                assert session is not None
                for j in range(mcp_requests):
                    label = SUSTAINED_MCP_CYCLE[j % len(SUSTAINED_MCP_CYCLE)]
                    started = time.perf_counter_ns()
                    line = session.call(mcp_specs[label])
                    mcp_ms.append((time.perf_counter_ns() - started) / 1_000_000.0)
                    v1.validate_mcp(line, "", label, expected, vault)
            except BaseException as exc:  # noqa: BLE001
                errors.append(exc)

        def sampler() -> None:
            assert session is not None
            while not stop_sampling.is_set():
                try:
                    series["server"].append(v1.rss_bytes(server_process.pid))
                    series["mcp"].append(v1.rss_bytes(session.pid))
                except BaseException as exc:  # noqa: BLE001
                    errors.append(exc)
                    return
                stop_sampling.wait(SUSTAINED_RSS_INTERVAL_S)

        started = time.monotonic()
        threads = [threading.Thread(target=http_worker), threading.Thread(target=mcp_worker)]
        sample_thread = threading.Thread(target=sampler)
        sample_thread.start()
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        duration = time.monotonic() - started
        stop_sampling.set()
        sample_thread.join()
        if errors:
            raise HarnessError(f"{side} sustained load failed: {errors[0]!r}")
        final = {"server": v1.rss_bytes(server_process.pid), "mcp": v1.rss_bytes(session.pid)}
        series["server"].append(final["server"])
        series["mcp"].append(final["mcp"])
        if server_process.poll() is not None or session.process.poll() is not None:
            raise HarnessError(f"{side} process died during the sustained load")
    finally:
        stop_sampling.set()
        cleanup: list[str] = []
        if session is not None:
            try:
                session.stop()
            except BaseException as exc:  # noqa: BLE001
                cleanup.append(str(exc))
        try:
            server.stop()
        except BaseException as exc:  # noqa: BLE001
            cleanup.append(str(exc))
        if cleanup:
            raise HarnessError("; ".join(cleanup))
    combined = [s + m for s, m in zip(series["server"], series["mcp"])]
    return {
        "side": side,
        "http_requests": len(http_ms),
        "mcp_requests": len(mcp_ms),
        "errors": 0,
        "duration_s": duration,
        "idle_rss_bytes": idle,
        "final_rss_bytes": final,
        "rss_series_bytes": series,
        "max_rss_bytes": {"server": max(series["server"]), "mcp": max(series["mcp"]), "combined": max(combined)},
        "http_latency": latency_digest(http_ms),
        "mcp_latency": latency_digest(mcp_ms),
        "clean_shutdown": True,
    }


def sustained_summary(runs: list[dict[str, Any]]) -> dict[str, Any]:
    maxima = {
        side: max(run["max_rss_bytes"]["combined"] for run in runs if run["side"] == side) for side in ("go", "rust")
    }
    return {
        "max_rss_bytes": maxima,
        "rss_reduction": (maxima["go"] - maxima["rust"]) / maxima["go"],
    }


def measure_sustained(
    go_binary: Path,
    rust_binary: Path,
    temp_root: Path,
    http_requests: int,
    mcp_requests: int,
    rounds: int,
) -> dict[str, Any]:
    homes: dict[str, tuple[Path, Path, Path, dict[str, Any]]] = {}
    for side, binary in (("go", go_binary), ("rust", rust_binary)):
        home = temp_root / f"sustained-{side}"
        vault = home / "vault"
        manifest = v1.write_vault(vault, include_http_probe=True)
        expected = v1.expected_vault_semantics(manifest, include_http_probe=True)
        v1.prepare_index(binary, home, vault, home / "sidecar.db", expected)
        homes[side] = (home, vault, home / "sidecar.db", expected)
    runs: list[dict[str, Any]] = []
    for round_index in range(rounds):
        order = ("go", "rust") if round_index % 2 == 0 else ("rust", "go")
        for side in order:
            home, vault, sidecar, expected = homes[side]
            binary = go_binary if side == "go" else rust_binary
            run = sustained_run(side, binary, home, vault, sidecar, expected, http_requests, mcp_requests)
            run["round"] = round_index
            run["order"] = "-".join(order)
            runs.append(run)
    return {
        "http_cycle": list(SUSTAINED_HTTP_CYCLE),
        "mcp_cycle": list(SUSTAINED_MCP_CYCLE),
        "rss_interval_s": SUSTAINED_RSS_INTERVAL_S,
        "rounds": rounds,
        "http_requests_per_run": http_requests,
        "mcp_requests_per_run": mcp_requests,
        "runs": runs,
        **sustained_summary(runs),
    }


# --------------------------------------------------------------------------
# gate arithmetic (shared by the producer and the validator)
# --------------------------------------------------------------------------
def combined_size_reduction(binaries: dict[str, Any]) -> tuple[int, int, float]:
    go = binaries["go"]["symdesk"]["bytes"] + binaries["go"]["symroom"]["bytes"]
    rust = binaries["rust"]["symdesk"]["bytes"] + binaries["rust"]["symroom"]["bytes"]
    return go, rust, (go - rust) / go


def workload_intervals(workloads: dict[str, Any]) -> dict[str, dict[str, list[float]]]:
    intervals: dict[str, dict[str, list[float]]] = {}
    for name, entry in workloads.items():
        cohorts = v1.paired_ratio_cohorts(entry["pair"], f"workload.{name}")
        intervals[f"workload.{name}"] = {
            order: [low - 1.0, high - 1.0] for order, values in cohorts.items() for low, high in (v1.median_interval(values),)
        }
    return intervals


def workload_regressions(workloads: dict[str, Any]) -> dict[str, dict[str, float]]:
    regressions: dict[str, dict[str, float]] = {}
    for name, entry in workloads.items():
        cohorts = v1.paired_ratio_cohorts(entry["pair"], f"workload.{name}")
        regressions[f"workload.{name}"] = {order: v1.median(values) - 1.0 for order, values in cohorts.items()}
    return regressions


def compute_thresholds(slice_result: dict[str, Any], binaries: dict[str, Any], workloads: dict[str, Any], sustained: dict[str, Any]) -> dict[str, Any]:
    go_bytes, rust_bytes, size_reduction = combined_size_reduction(binaries)
    rss_reduction = slice_result["thresholds"]["representative_rss_reduction_max"]
    intervals = dict(slice_result["thresholds"]["latency_order_regression_intervals"])
    intervals.update(workload_intervals(workloads))
    order_regressions = dict(slice_result["thresholds"]["latency_order_regressions"])
    order_regressions.update(workload_regressions(workloads))
    latency_failure = v1.order_stratified_latency_failure(intervals)
    sustained_pass = (
        all(run["errors"] == 0 and run["clean_shutdown"] is True for run in sustained["runs"])
        and sustained["max_rss_bytes"]["go"] > 0
        and sustained["max_rss_bytes"]["rust"] > 0
    )
    improvement_basis = [
        basis
        for basis, value in (("combined_binary_size", size_reduction), ("representative_rss", rss_reduction))
        if value >= 0.20
    ]
    return {
        "minimum_improvement": 0.20,
        "maximum_latency_regression": v1.MAXIMUM_LATENCY_REGRESSION,
        "latency_interval_width_limit": v1.MAXIMUM_LATENCY_INTERVAL_WIDTH,
        "latency_interval_confidence": v1.MEDIAN_INTERVAL_CONFIDENCE,
        "latency_estimator": v1.DEFAULT_LATENCY_ESTIMATOR,
        "combined_binary_bytes": {"go": go_bytes, "rust": rust_bytes},
        "combined_binary_size_reduction": size_reduction,
        "representative_rss_reduction_max": rss_reduction,
        "sustained_rss_reduction": sustained["rss_reduction"],
        "improvement_basis": improvement_basis,
        "improvement_pass": bool(improvement_basis),
        "latency_order_regressions": order_regressions,
        "latency_order_regression_intervals": intervals,
        "latency_failure": latency_failure,
        "latency_pass": latency_failure is None,
        "contracts_pass": all(item["exit_code"] == 0 for item in slice_result["contracts"]),
        "sustained_pass": sustained_pass,
        "fail_closed": True,
    }


def overall_pass(thresholds: dict[str, Any]) -> bool:
    return bool(
        thresholds["contracts_pass"]
        and thresholds["improvement_pass"]
        and thresholds["latency_pass"]
        and thresholds["sustained_pass"]
    )


# --------------------------------------------------------------------------
# the VALUE-001-shaped representative slice (validated by value001 itself)
# --------------------------------------------------------------------------
def build_slice(
    root: Path,
    candidate: str,
    go_binary: Path,
    rust_binary: Path,
    go_build: dict[str, Any],
    rust_build: dict[str, Any],
    contracts: list[dict[str, Any]],
    metrics: dict[str, Any],
    manifest: dict[str, Any],
    index_preparation: dict[str, Any],
    samples: int,
    warmups: int,
) -> dict[str, Any]:
    go_size = go_binary.stat().st_size
    rust_size = rust_binary.stat().st_size
    size_reduction = (go_size - rust_size) / go_size
    rss_go = metrics["rss"]["go"]["max"]
    rss_rust = metrics["rss"]["rust"]["max"]
    rss_reduction = (rss_go - rss_rust) / rss_go
    regressions = v1.latency_regressions(metrics, v1.DEFAULT_LATENCY_ESTIMATOR)
    order_regressions = v1.latency_order_regressions(metrics)
    intervals = v1.latency_order_regression_intervals(metrics)
    thresholds = {
        "minimum_improvement": 0.20,
        "maximum_latency_regression": v1.MAXIMUM_LATENCY_REGRESSION,
        "latency_estimator": v1.DEFAULT_LATENCY_ESTIMATOR,
        "binary_size_reduction": size_reduction,
        "representative_rss_reduction_max": rss_reduction,
        "improvement_pass": size_reduction >= 0.20 or rss_reduction >= 0.20,
        "latency_regressions": regressions,
        "latency_order_regressions": order_regressions,
        "latency_order_regression_intervals": intervals,
        "latency_interval_confidence": v1.MEDIAN_INTERVAL_CONFIDENCE,
        "latency_interval_width_limit": v1.MAXIMUM_LATENCY_INTERVAL_WIDTH,
        "latency_pass": v1.order_stratified_latency_pass(intervals),
        "contracts_pass": all(item["exit_code"] == 0 for item in contracts),
        "fail_closed": True,
    }
    head = v1.git_output(root, ["rev-parse", "HEAD"])
    return {
        "schema_version": v1.SCHEMA6_VERSION,
        "benchmark": "VALUE-001",
        "summation": v1.DEFAULT_SUMMATION,
        "captured_at": v1.utc_now(),
        "runner": {
            "path": "scripts/rust-port/value002.py",
            "samples": samples,
            "warmups": warmups,
            "pairing": v1.HTTP_MEASUREMENT_PAIRING,
        },
        "host": {"os": v1.sys_platform(), "os_version": platform.platform(), "arch": platform.machine(), "machine": platform.machine(), "python": platform.python_version()},
        "repository": {
            "root": str(root),
            "head": head,
            "status": v1.git_output(root, ["status", "--porcelain=v1", "--untracked-files=all"]),
            "dirty_allowed": True,
            "candidate_diff_sha256": v1.sha256_bytes(v1.git_bytes(root, ["diff", "--binary", "HEAD"])),
            "original_value_baseline_commit": v1.ORIGINAL_VALUE_BASELINE,
            # VALUE-002 compares the Go release built from the same candidate
            # commit; there is no separate frozen oracle here.
            "current_behaviour_oracle_commit": candidate,
        },
        "toolchains": {"go_oracle": go_build["toolchain"], "rustc": rust_build["toolchain"], "python": platform.python_version()},
        "binaries": {
            "go": {"path": str(go_binary), "bytes": go_size, "sha256": v1.sha256_file(go_binary), "source": candidate, "build_command": go_build["command"]},
            "rust": {"path": str(rust_binary), "bytes": rust_size, "sha256": v1.sha256_file(rust_binary), "source": candidate, "build_command": rust_build["command"]},
        },
        "vault": {
            "documents": manifest["documents"],
            "generated_bytes": manifest["bytes"],
            "search_token": v1.SEARCH_TOKEN,
            "search_matches": manifest["search_match_count"],
            "generation": "Python stdlib deterministic loop; cohort = index % 100; 10,000 Markdown files",
        },
        "index_preparation": index_preparation,
        "contracts": contracts,
        "metrics": metrics,
        "thresholds": thresholds,
        "passed": bool(thresholds["contracts_pass"] and thresholds["improvement_pass"] and thresholds["latency_pass"]),
    }


# --------------------------------------------------------------------------
# validator
# --------------------------------------------------------------------------
TOP_KEYS = {
    "schema_version", "benchmark", "summation", "captured_at", "runner", "host", "repository", "toolchains",
    "build_record", "binaries", "archives", "vaults", "representative", "workloads", "sustained", "thresholds", "passed",
}


def _finite(value: Any, label: str, *, positive: bool = False) -> float:
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or (positive and value <= 0):
        raise HarnessError(f"{label} is not a finite number")
    return float(value)


def validate_workload(name: str, entry: Any, warmups: int, samples: int) -> None:
    if not isinstance(entry, dict) or set(entry) != {"pair", "command", "output_sha256", "outputs_equal", "setup_runs", "peak_rss_bytes"}:
        raise HarnessError(f"workload {name} shape mismatch")
    v1.validate_paired_metric(
        entry["pair"], f"workloads.{name}", "milliseconds", v1.DEFAULT_SUMMATION,
        require_positive=True, expected_samples=samples, expected_warmups=warmups,
    )
    digests = entry["output_sha256"]
    if (
        not isinstance(digests, dict)
        or set(digests) != {"go", "rust"}
        or not v1.is_lower_hex(digests["go"], 64)
        or digests["go"] != digests["rust"]
        or entry["outputs_equal"] is not True
    ):
        raise HarnessError(f"workload {name} Go/Rust outputs are not recorded as identical")
    if name == "index-build" and entry["setup_runs"] != 2 * (warmups + samples):
        raise HarnessError("index-build did not reset the cold index before every run")
    peak = entry["peak_rss_bytes"]
    if (
        not isinstance(peak, dict)
        or set(peak) != {"go", "rust"}
        or not peak["go"]
        or len(peak["go"]) != len(peak["rust"])
        or any(not isinstance(item, int) or item <= 0 for side in peak.values() for item in side)
    ):
        raise HarnessError(f"workload {name} peak RSS probes are invalid")


def validate_sustained(sustained: Any) -> None:
    keys = {"http_cycle", "mcp_cycle", "rss_interval_s", "rounds", "http_requests_per_run", "mcp_requests_per_run", "runs", "max_rss_bytes", "rss_reduction"}
    if not isinstance(sustained, dict) or set(sustained) != keys:
        raise HarnessError("sustained load shape mismatch")
    if sustained["http_cycle"] != list(SUSTAINED_HTTP_CYCLE) or sustained["mcp_cycle"] != list(SUSTAINED_MCP_CYCLE):
        raise HarnessError("sustained request mix differs from the declared mix")
    runs = sustained["runs"]
    if not isinstance(runs, list) or len(runs) != 2 * sustained["rounds"] or sustained["rounds"] < 2:
        raise HarnessError("sustained load needs at least two alternating rounds per side")
    run_keys = {
        "side", "http_requests", "mcp_requests", "errors", "duration_s", "idle_rss_bytes", "final_rss_bytes",
        "rss_series_bytes", "max_rss_bytes", "http_latency", "mcp_latency", "clean_shutdown", "round", "order",
    }
    for run in runs:
        if not isinstance(run, dict) or set(run) != run_keys or run["side"] not in ("go", "rust"):
            raise HarnessError("sustained run shape mismatch")
        if run["http_requests"] != sustained["http_requests_per_run"] or run["mcp_requests"] != sustained["mcp_requests_per_run"]:
            raise HarnessError("sustained run did not complete every declared request")
        if run["errors"] != 0 or run["clean_shutdown"] is not True:
            raise HarnessError("sustained run reported errors or an unclean shutdown")
        _finite(run["duration_s"], "sustained duration", positive=True)
        series = run["rss_series_bytes"]
        if set(series) != {"server", "mcp"} or len(series["server"]) != len(series["mcp"]) or len(series["server"]) < 2:
            raise HarnessError("sustained RSS series are invalid")
        if any(not isinstance(v, int) or v <= 0 for s in series.values() for v in s):
            raise HarnessError("sustained RSS series contain invalid samples")
        combined = [s + m for s, m in zip(series["server"], series["mcp"])]
        if run["max_rss_bytes"] != {"server": max(series["server"]), "mcp": max(series["mcp"]), "combined": max(combined)}:
            raise HarnessError("sustained max RSS is not recomputed from its series")
    for side in ("go", "rust"):
        orders = [run["order"] for run in runs if run["side"] == side]
        if len(orders) != sustained["rounds"] or set(orders) != {"go-rust", "rust-go"}:
            raise HarnessError("sustained rounds do not alternate side order")
    expected = sustained_summary(runs)
    if sustained["max_rss_bytes"] != expected["max_rss_bytes"] or sustained["rss_reduction"] != expected["rss_reduction"]:
        raise HarnessError("sustained RSS summary is not recomputed")


def validate_host(host: Any, profile: str) -> None:
    keys = {"os", "os_version", "arch", "machine", "python", "cpu_count", "quiet_limit_load1", "load_before", "load_after", "load_timeline"}
    if not isinstance(host, dict) or set(host) != keys:
        raise HarnessError("host shape mismatch")
    snapshot_keys = {"captured_at", "load1", "load5", "load15", "uptime", "build_processes", "quiet"}
    for label in ("load_before", "load_after"):
        snap = host[label]
        if not isinstance(snap, dict) or set(snap) != snapshot_keys:
            raise HarnessError(f"host.{label} shape mismatch")
        for key in ("load1", "load5", "load15"):
            _finite(snap[key], f"host.{label}.{key}")
        if not isinstance(snap["build_processes"], list) or not isinstance(snap["quiet"], bool):
            raise HarnessError(f"host.{label} shape mismatch")
    if host["quiet_limit_load1"] != QUIET_LOAD1_LIMIT:
        raise HarnessError("host quiet limit changed")
    if not isinstance(host["load_timeline"], list) or not host["load_timeline"]:
        raise HarnessError("host load timeline is missing")
    if profile == "final":
        before = host["load_before"]
        if before["load1"] >= QUIET_LOAD1_LIMIT or before["build_processes"] or before["quiet"] is not True:
            raise HarnessError("final capture did not start on a quiet host")


def validate_repository(repo: Any, candidate: str) -> None:
    keys = {"root", "head", "candidate_commit", "status", "changed_since_candidate", "non_harness_changes"}
    if not isinstance(repo, dict) or set(repo) != keys:
        raise HarnessError("repository shape mismatch")
    if not v1.is_lower_hex(repo["head"], 40) or repo["candidate_commit"] != candidate:
        raise HarnessError("repository candidate does not match the build record")
    if repo["non_harness_changes"] != [path for path in repo["changed_since_candidate"] if not path.startswith(HARNESS_PATH_PREFIXES)]:
        raise HarnessError("repository non-harness changes are not recomputed")
    if repo["non_harness_changes"]:
        raise HarnessError(f"measured binaries predate non-harness changes: {repo['non_harness_changes']}")


def validate_result(result: Any, *, allow_smoke: bool = False) -> None:
    if not isinstance(result, dict) or set(result) != TOP_KEYS:
        raise HarnessError("VALUE-002 top-level schema mismatch")
    if result["benchmark"] != BENCHMARK or result["schema_version"] != SCHEMA_VERSION:
        raise HarnessError("VALUE-002 benchmark or schema version mismatch")
    if result["summation"] != v1.DEFAULT_SUMMATION:
        raise HarnessError("VALUE-002 requires left-fold summation")
    runner = result["runner"]
    if not isinstance(runner, dict) or set(runner) != {"path", "profile", "samples", "warmups", "probe_runs", "pairing"}:
        raise HarnessError("runner shape mismatch")
    profile, samples, warmups = runner["profile"], runner["samples"], runner["warmups"]
    if runner["pairing"] != PAIRING:
        raise HarnessError("runner pairing changed")
    with profile_constants(profile, samples, warmups):
        validate_host(result["host"], profile)
        entries = validate_build_record(result["build_record"])
        candidate = result["build_record"]["candidate"]["commit"]
        validate_repository(result["repository"], candidate)
        binaries = result["binaries"]
        if not isinstance(binaries, dict) or set(binaries) != {"go", "rust"}:
            raise HarnessError("binaries shape mismatch")
        archives = result["archives"]
        if not isinstance(archives, dict) or set(archives) != {"go", "rust", "published_go_bytes", "rust_vs_go_built", "rust_vs_published"}:
            raise HarnessError("archives shape mismatch")
        if archives["published_go_bytes"] != PUBLISHED_GO_ARCHIVE_BYTES:
            raise HarnessError("published Go archive size changed")
        for side, build_name in (("go", "go-archive"), ("rust", "rust-release-archive")):
            record = archives[side]
            if set(record) != {"path", "bytes", "sha256", "members"}:
                raise HarnessError(f"archives.{side} shape mismatch")
            if record["sha256"] != artifact_sha(entries[build_name], os.path.basename(record["path"])):
                raise HarnessError(f"archives.{side} is not the recorded build artifact")
            for member in ("symdesk", "symroom"):
                if record["members"][member]["sha256"] != binaries[side][member]["sha256"] or record["members"][member]["bytes"] != binaries[side][member]["bytes"]:
                    raise HarnessError(f"measured {side} {member} is not the archive member")
        if archives["rust_vs_go_built"] != (archives["go"]["bytes"] - archives["rust"]["bytes"]) / archives["go"]["bytes"]:
            raise HarnessError("archive reduction is not recomputed")
        if archives["rust_vs_published"] != (PUBLISHED_GO_ARCHIVE_BYTES - archives["rust"]["bytes"]) / PUBLISHED_GO_ARCHIVE_BYTES:
            raise HarnessError("published archive reduction is not recomputed")
        for side, build_name in (("go", "go"), ("rust", "rust")):
            for member in ("symdesk", "symroom"):
                record = binaries[side][member]
                if set(record) != {"path", "bytes", "sha256"} or record["bytes"] <= 0:
                    raise HarnessError(f"binaries.{side}.{member} shape mismatch")
        for member in ("symdesk", "symroom"):
            if binaries["go"][member]["sha256"] != artifact_sha(entries[f"go-{member}"], member):
                raise HarnessError(f"measured Go {member} differs from its recorded build")
        rust_entry = entries["rust-release-archive"]
        for member in ("symdesk", "symroom"):
            if binaries["rust"][member]["sha256"] != artifact_sha(rust_entry, member):
                raise HarnessError(f"measured Rust {member} differs from the recorded release build")
        slice_result = result["representative"]
        v1.validate_result(slice_result)
        if slice_result["repository"]["head"] != result["repository"]["head"]:
            raise HarnessError("representative slice was measured on a different checkout")
        if slice_result["repository"]["current_behaviour_oracle_commit"] != candidate:
            raise HarnessError("representative Go reference is not the candidate Go build")
        for side in ("go", "rust"):
            if slice_result["binaries"][side]["sha256"] != binaries[side]["symdesk"]["sha256"]:
                raise HarnessError(f"representative {side} binary is not the measured symdesk")
            if slice_result["binaries"][side]["source"] != candidate:
                raise HarnessError("representative binary source is not the candidate")
        names = {item["name"] for item in slice_result["contracts"]}
        if len(names) != 4:
            raise HarnessError("representative contracts must be four distinct differential checks")
        vaults = result["vaults"]
        if not isinstance(vaults, dict) or set(vaults) != {"representative", "linked"}:
            raise HarnessError("vaults shape mismatch")
        if vaults["linked"].get("documents") != v1.DOC_COUNT or vaults["linked"].get("target") != v1.document_path(LINK_TARGET_INDEX):
            raise HarnessError("linked vault description invalid")
        workloads = result["workloads"]
        if not isinstance(workloads, dict) or set(workloads) != set(NEW_WORKLOADS):
            raise HarnessError("workload set differs from the declared set")
        for name in NEW_WORKLOADS:
            validate_workload(name, workloads[name], warmups, samples)
        validate_sustained(result["sustained"])
        expected = compute_thresholds(slice_result, binaries, workloads, result["sustained"])
        if result["thresholds"] != expected:
            raise HarnessError("VALUE-002 thresholds are not recomputed from the recorded samples")
        if result["passed"] is not overall_pass(expected):
            raise HarnessError("VALUE-002 passed flag is not recomputed")
        if result["thresholds"]["improvement_pass"] and not result["thresholds"]["improvement_basis"]:
            raise HarnessError("improvement without a basis")
    if profile != "final" and not allow_smoke:
        raise HarnessError("a smoke capture is not evidence; only harness tests may validate one")


def verdict_lines(result: dict[str, Any]) -> list[str]:
    thresholds = result["thresholds"]
    lines = [f"VALUE-002 {'PASS' if result['passed'] else 'FAIL'} candidate {result['build_record']['candidate']['commit'][:8]}"]
    go_b, rust_b = thresholds["combined_binary_bytes"]["go"], thresholds["combined_binary_bytes"]["rust"]
    lines.append(f"combined binaries: Go {go_b:,} B, Rust {rust_b:,} B, reduction {thresholds['combined_binary_size_reduction'] * 100:.2f}%")
    archives = result["archives"]
    lines.append(
        f"archives: Go (built) {archives['go']['bytes']:,} B, Go (published) {archives['published_go_bytes']:,} B, "
        f"Rust {archives['rust']['bytes']:,} B; reduction vs built {archives['rust_vs_go_built'] * 100:.2f}%, "
        f"vs published {archives['rust_vs_published'] * 100:.2f}%"
    )
    lines.append(f"representative RSS reduction: {thresholds['representative_rss_reduction_max'] * 100:.2f}%")
    lines.append(f"sustained mixed-load max RSS: {json.dumps(result['sustained']['max_rss_bytes'])} reduction {thresholds['sustained_rss_reduction'] * 100:.2f}%")
    lines.append(f"improvement basis: {thresholds['improvement_basis']}")
    for name, by_order in thresholds["latency_order_regression_intervals"].items():
        cells = "; ".join(
            f"{order} median {thresholds['latency_order_regressions'][name][order] * 100:+.2f}% 95% [{low * 100:+.2f}%, {high * 100:+.2f}%]"
            for order, (low, high) in by_order.items()
        )
        lines.append(f"  {name}: {cells}")
    lines.append(f"latency: {'pass' if thresholds['latency_pass'] else 'FAIL: ' + str(thresholds['latency_failure'])}")
    lines.append(f"contracts: {thresholds['contracts_pass']}; sustained: {thresholds['sustained_pass']}")
    return lines


# --------------------------------------------------------------------------
# runner
# --------------------------------------------------------------------------
def run_contracts(root: Path, tools: Path, go_binary: Path, rust_binary: Path) -> list[dict[str, Any]]:
    checks = [
        ("./scripts/rust-port/cmd/representativegen", [str(tools / "representativegen"), "--check"]),
        ("./scripts/rust-port/cmd/diffharness", [str(tools / "diffharness"), "--symdesk-left", str(go_binary), "--symdesk-right", str(rust_binary), "--cases", "testdata/port/representative/cases.json", "--stage", "representative"]),
        ("./scripts/rust-port/cmd/mcpdiff", [str(tools / "mcpdiff"), "--left", str(go_binary), "--right", str(rust_binary), "--fixture", "testdata/port/mcp/representative.json"]),
        ("./scripts/rust-port/cmd/httpdiff", [str(tools / "httpdiff"), "--left", str(go_binary), "--right", str(rust_binary), "--fixture", "testdata/port/http/representative.json"]),
    ]
    env = dict(os.environ)
    env.update({"LANG": "C", "LC_ALL": "C", "TZ": "UTC", "NO_COLOR": "1"})
    results = []
    for name, command in checks:
        result = v1.run_checked(command, root, 600.0, env=env)
        result["name"] = name
        results.append(result)
    return results


def run_value002(args: argparse.Namespace) -> dict[str, Any]:
    root = args.root.resolve()
    profile, samples, warmups = args.profile, args.samples, args.warmups
    output = args.output
    record = json.loads(args.build_record.read_text(encoding="utf-8"))
    entries = validate_build_record(record)
    candidate = record["candidate"]["commit"]
    if v1.git_output(root, ["cat-file", "-t", candidate]) != "commit":
        raise HarnessError(f"candidate {candidate} is not a commit")
    changed = [line for line in v1.git_output(root, ["diff", "--name-only", candidate, "HEAD"]).splitlines() if line]
    # name-only listings: ``git status`` output loses its leading column when stripped
    dirty = [
        line
        for args_ in (["diff", "--name-only", "HEAD"], ["ls-files", "--others", "--exclude-standard"])
        for line in v1.git_output(root, args_).splitlines()
        if line
    ]
    non_harness = [path for path in changed + dirty if not path.startswith(HARNESS_PATH_PREFIXES)]
    if non_harness:
        raise HarnessError(f"checkout differs from candidate {candidate[:8]} outside harness paths: {non_harness}")
    with profile_constants(profile, samples, warmups):
        before = host_snapshot()
        if profile == "final" and not before["quiet"]:
            raise HarnessError(
                f"host is not quiet (load1 {before['load1']:.2f} limit {QUIET_LOAD1_LIMIT}, "
                f"builds {before['build_processes'][:3]}); wait and retry"
            )
        output.parent.mkdir(parents=True, exist_ok=True)
        marker = v1.mark_output_incomplete(output)
        done = False
        try:
            # ignore_cleanup_errors: Finder/Spotlight can drop a .DS_Store into the
            # scratch dir after the measurement; a failed rmdir must not discard
            # a completed capture (run 2 of 2026-11-06 was lost that way).
            with tempfile.TemporaryDirectory(prefix="symdesk-value002-", ignore_cleanup_errors=True) as temp_name, LoadTimeline() as timeline:
                temp_root = Path(temp_name)
                binary_dir = args.binary_dir / output.stem
                members: dict[str, dict[str, dict[str, Any]]] = {}
                for side, archive in (("go", args.go_archive), ("rust", args.rust_archive)):
                    members[side] = extract_archive(archive, binary_dir / side)
                go_dir, rust_dir = binary_dir / "go", binary_dir / "rust"
                go_symdesk, go_symroom = go_dir / "symdesk", go_dir / "symroom"
                rust_symdesk, rust_symroom = rust_dir / "symdesk", rust_dir / "symroom"
                for member, path in (("symdesk", go_symdesk), ("symroom", go_symroom)):
                    if members["go"][member]["sha256"] != artifact_sha(entries["go-" + member], member):
                        raise HarnessError(f"Go archive {member} differs from its recorded build")
                for member in ("symdesk", "symroom"):
                    if members["rust"][member]["sha256"] != artifact_sha(entries["rust-release-archive"], member):
                        raise HarnessError(f"Rust archive {member} differs from its recorded release build")
                if members["rust"]["symdesk"]["sha256"] == members["go"]["symdesk"]["sha256"]:
                    raise HarnessError("Go and Rust symdesk are identical files")
                tools = args.contract_tools
                for tool in ("representativegen", "diffharness", "mcpdiff", "httpdiff"):
                    if v1.sha256_file(tools / tool) != artifact_sha(entries["go-tool-" + tool], tool):
                        raise HarnessError(f"contract tool {tool} differs from its recorded build")
                contracts = run_contracts(root, tools, go_symdesk, rust_symdesk)

                # --- representative slice (VALUE-001 machinery, release candidates) ---
                go_home, rust_home = temp_root / "go-search", temp_root / "rust-search"
                go_vault, rust_vault = go_home / "vault", rust_home / "vault"
                go_manifest, rust_manifest = v1.write_vault(go_vault), v1.write_vault(rust_vault)
                expected_go = v1.expected_vault_semantics(go_manifest)
                expected_rust = v1.expected_vault_semantics(rust_manifest)
                go_env = v1.benchmark_env(go_home, go_vault, go_home / "sidecar.db")
                rust_env = v1.benchmark_env(rust_home, rust_vault, rust_home / "sidecar.db")
                index_preparation = {
                    "go": v1.prepare_index(go_symdesk, go_home, go_vault, go_home / "sidecar.db", expected_go),
                    "rust": v1.prepare_index(rust_symdesk, rust_home, rust_vault, rust_home / "sidecar.db", expected_rust),
                    "vault_justification": "10,000 Markdown documents exercise recursive walk, 50 SQLite batches of 200, FTS/snippet search and realistic path/title metadata (VALUE-001 vault, unchanged).",
                }
                startup = v1.measure_process_pair(go_symdesk, rust_symdesk, go_env, rust_env, ["version"], ["version"], warmups, samples, v1.validate_version)
                search = v1.measure_process_pair(
                    go_symdesk, rust_symdesk, go_env, rust_env,
                    ["search", v1.SEARCH_TOKEN, "--vault", str(go_vault), "--json"],
                    ["search", v1.SEARCH_TOKEN, "--vault", str(rust_vault), "--json"],
                    warmups, samples, lambda out, err: v1.validate_search(out, err, expected_go),
                )
                mcp = v1.measure_mcp(go_symdesk, rust_symdesk, go_env, rust_env, expected_go, go_vault, rust_vault, warmups, samples)
                http_go_home, http_rust_home = temp_root / "go-http", temp_root / "rust-http"
                http_go_vault, http_rust_vault = http_go_home / "vault", http_rust_home / "vault"
                http_go_manifest = v1.write_vault(http_go_vault, include_http_probe=True)
                http_rust_manifest = v1.write_vault(http_rust_vault, include_http_probe=True)
                http_go_expected = v1.expected_vault_semantics(http_go_manifest, include_http_probe=True)
                http_rust_expected = v1.expected_vault_semantics(http_rust_manifest, include_http_probe=True)
                v1.prepare_index(go_symdesk, http_go_home, http_go_vault, http_go_home / "sidecar.db", http_go_expected)
                v1.prepare_index(rust_symdesk, http_rust_home, http_rust_vault, http_rust_home / "sidecar.db", http_rust_expected)
                servers: list[v1.RunningServer] = []
                try:
                    go_server = v1.RunningServer(go_symdesk, http_go_home, http_go_vault, http_go_home / "sidecar.db")
                    servers.append(go_server)
                    rust_server = v1.RunningServer(rust_symdesk, http_rust_home, http_rust_vault, http_rust_home / "sidecar.db")
                    servers.append(rust_server)
                    http, rss = v1.measure_http(go_server, rust_server, http_go_expected, http_rust_expected, warmups, samples, args.rss_interval_ms)
                finally:
                    v1.stop_all(servers)
                metrics = {"startup": startup, "search": search, "mcp": mcp, "http": http, "rss": rss}
                go_build = {"command": entries["go-symdesk"]["command"], "toolchain": entries["go-symdesk"]["toolchain"]}
                rust_build = {"command": entries["rust-release-archive"]["command"], "toolchain": entries["rust-release-archive"]["toolchain"]}
                slice_result = build_slice(
                    root, candidate, go_symdesk, rust_symdesk, go_build, rust_build, contracts, metrics, go_manifest,
                    index_preparation, samples, warmups,
                )

                # --- new workloads on the linked 10,000 document vault ---
                linked_expected = expected_linked()
                linked_homes: dict[str, tuple[Path, Path, Path]] = {}
                for side in ("go", "rust"):
                    home = temp_root / f"linked-{side}"
                    vault = home / "vault"
                    write_linked_vault(vault)
                    linked_homes[side] = (home, vault, home / "sidecar.db")
                envs = {side: v1.benchmark_env(h, vlt, sc) for side, (h, vlt, sc) in linked_homes.items()}
                probe_runs = PROBE_RUNS_FINAL if profile == "final" else 2
                bins = {"go": go_symdesk, "rust": rust_symdesk}

                def cmd(side: str, *words: str) -> list[str]:
                    return [str(bins[side]), *words, "--vault", str(linked_homes[side][1]), "--json"]

                workloads: dict[str, Any] = {}
                workloads["symroom-startup"] = measure_workload(
                    [str(go_symroom), "version"], [str(rust_symroom), "version"], envs["go"], envs["rust"],
                    lambda out, err: validate_symroom_version(out, err), lambda out, err: validate_symroom_version(out, err),
                    None, None, warmups, samples, probe_runs,
                )
                workloads["index-build"] = measure_workload(
                    cmd("go", "ls"), cmd("rust", "ls"), envs["go"], envs["rust"],
                    lambda out, err: validate_index_build(out, err, linked_expected),
                    lambda out, err: validate_index_build(out, err, linked_expected),
                    cold_reset(linked_homes["go"][0], linked_homes["go"][2]),
                    cold_reset(linked_homes["rust"][0], linked_homes["rust"][2]),
                    warmups, samples, probe_runs,
                )
                # warm index for the read workloads (untimed, validated like VALUE-001)
                for side in ("go", "rust"):
                    home, vault, sidecar = linked_homes[side]
                    v1.prepare_index(bins[side], home, vault, sidecar, {"paths": linked_expected_paths(linked_expected), "titles": linked_expected["nodes"]})
                target = linked_expected["target"]
                workloads["graph"] = measure_workload(
                    cmd("go", "graph"), cmd("rust", "graph"), envs["go"], envs["rust"],
                    lambda out, err: validate_graph(out, err, linked_expected), lambda out, err: validate_graph(out, err, linked_expected),
                    None, None, warmups, samples, probe_runs,
                )
                workloads["backlinks"] = measure_workload(
                    cmd("go", "backlinks", target), cmd("rust", "backlinks", target), envs["go"], envs["rust"],
                    lambda out, err: validate_backlinks(out, err, linked_expected), lambda out, err: validate_backlinks(out, err, linked_expected),
                    None, None, warmups, samples, probe_runs,
                )
                workloads["relations-inverse"] = measure_workload(
                    cmd("go", "relations", "inverse", target), cmd("rust", "relations", "inverse", target), envs["go"], envs["rust"],
                    lambda out, err: validate_inverse(out, err, linked_expected), lambda out, err: validate_inverse(out, err, linked_expected),
                    None, None, warmups, samples, probe_runs,
                )
                workloads["duplicates"] = measure_workload(
                    cmd("go", "duplicates"), cmd("rust", "duplicates"), envs["go"], envs["rust"],
                    lambda out, err: validate_duplicates(out, err, linked_expected), lambda out, err: validate_duplicates(out, err, linked_expected),
                    None, None, warmups, samples, probe_runs,
                )
                http_requests = SUSTAINED_HTTP_REQUESTS_FINAL if profile == "final" else 100
                mcp_requests = SUSTAINED_MCP_REQUESTS_FINAL if profile == "final" else 60
                sustained = measure_sustained(go_symdesk, rust_symdesk, temp_root, http_requests, mcp_requests, SUSTAINED_ROUNDS)

                # retain the measured binaries beside the artifact, drop the scratch copy
                after = host_snapshot()
            binaries = {
                side: {member: file_record(binary_dir / side / member) for member in ("symdesk", "symroom")}
                for side in ("go", "rust")
            }
            archives = {
                "go": {**file_record(args.go_archive), "members": {m: members["go"][m] for m in ("symdesk", "symroom")}},
                "rust": {**file_record(args.rust_archive), "members": {m: members["rust"][m] for m in ("symdesk", "symroom")}},
                "published_go_bytes": PUBLISHED_GO_ARCHIVE_BYTES,
            }
            archives["rust_vs_go_built"] = (archives["go"]["bytes"] - archives["rust"]["bytes"]) / archives["go"]["bytes"]
            archives["rust_vs_published"] = (PUBLISHED_GO_ARCHIVE_BYTES - archives["rust"]["bytes"]) / PUBLISHED_GO_ARCHIVE_BYTES
            thresholds = compute_thresholds(slice_result, binaries, workloads, sustained)
            head = v1.git_output(root, ["rev-parse", "HEAD"])
            result = {
                "schema_version": SCHEMA_VERSION,
                "benchmark": BENCHMARK,
                "summation": v1.DEFAULT_SUMMATION,
                "captured_at": v1.utc_now(),
                "runner": {
                    "path": "scripts/rust-port/value002.py", "profile": profile, "samples": samples, "warmups": warmups,
                    "probe_runs": probe_runs, "pairing": PAIRING,
                },
                "host": {
                    "os": v1.sys_platform(), "os_version": platform.platform(), "arch": platform.machine(),
                    "machine": platform.machine(), "python": platform.python_version(), "cpu_count": os.cpu_count(),
                    "quiet_limit_load1": QUIET_LOAD1_LIMIT, "load_before": before, "load_after": after,
                    "load_timeline": timeline.samples,
                },
                "repository": {
                    "root": str(root), "head": head, "candidate_commit": candidate,
                    "status": v1.git_output(root, ["status", "--porcelain=v1", "--untracked-files=all"]),
                    "changed_since_candidate": changed, "non_harness_changes": [],
                },
                "toolchains": {
                    "go": entries["go-symdesk"]["toolchain"], "rust": entries["rust-release-archive"]["toolchain"],
                    "python": platform.python_version(),
                },
                "build_record": record,
                "binaries": binaries,
                "archives": archives,
                "vaults": {
                    "representative": {"documents": go_manifest["documents"], "search_matches": go_manifest["search_match_count"], "generation": "value001.write_vault (unchanged)"},
                    "linked": {
                        "documents": v1.DOC_COUNT, "target": linked_expected["target"], "edges": len(linked_expected["edges"]),
                        "generation": "value001 documents plus frontmatter related=[[note-(i+3)]] and body links [[note-(i+1)]] [[note-(7i+3, bumped to stay distinct)]]",
                    },
                },
                "representative": slice_result,
                "workloads": workloads,
                "sustained": sustained,
                "thresholds": thresholds,
                "passed": overall_pass(thresholds),
            }
            write_result(output, result)
            done = True
            return result
        finally:
            if done:
                marker.unlink(missing_ok=True)


def linked_expected_paths(expected: dict[str, Any]) -> list[str]:
    return sorted(expected["nodes"])


def write_result(output: Path, result: dict[str, Any]) -> None:
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{output.name}.", suffix=".tmp", dir=output.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            json.dump(result, stream, indent=2)
            stream.write("\n")
        reloaded = json.loads(temporary.read_text(encoding="utf-8"))
        validate_result(reloaded, allow_smoke=(reloaded.get("runner") or {}).get("profile") == "smoke")
        os.replace(temporary, output)
    finally:
        temporary.unlink(missing_ok=True)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="mode", required=True)
    run = sub.add_parser("run", help="measure the candidate pair and write the VALUE-002 artifact")
    run.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    run.add_argument("--build-record", type=Path, required=True)
    run.add_argument("--go-archive", type=Path, required=True)
    run.add_argument("--rust-archive", type=Path, required=True)
    run.add_argument("--contract-tools", type=Path, required=True)
    run.add_argument("--output", type=Path, required=True)
    run.add_argument("--binary-dir", type=Path, required=True, help="retained, non-repository directory for the measured binaries")
    run.add_argument("--profile", choices=("final", "smoke"), default="final")
    run.add_argument("--samples", type=int, default=FINAL_SAMPLES)
    run.add_argument("--warmups", type=int, default=FINAL_WARMUPS)
    run.add_argument("--rss-interval-ms", type=float, default=5.0)
    rec = sub.add_parser("record-build", help="append one successful build/packaging step to the provenance record")
    rec.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    rec.add_argument("--record", type=Path, required=True)
    rec.add_argument("--name", required=True, choices=REQUIRED_BUILDS)
    rec.add_argument("--command", required=True, help="the exact command line that was run")
    rec.add_argument("--exit-code", type=int, required=True)
    rec.add_argument("--artifact", type=Path, action="append", required=True)
    rec.add_argument("--env", action="append", default=[], help="KEY=VALUE environment used for the command")
    rec.add_argument("--toolchain", required=True, help="toolchain version string")
    val = sub.add_parser("validate", help="fail-closed validation of a VALUE-002 artifact")
    val.add_argument("result", type=Path)
    val.add_argument("--expect-candidate", help="required candidate commit (full 40-hex SHA)")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    if args.mode == "record-build":
        env = dict(item.split("=", 1) for item in args.env)
        record_build(args.record, args.root.resolve(), args.name, args.command, args.exit_code, [p.resolve() for p in args.artifact], env, args.toolchain)
        print(f"recorded {args.name}")
        return 0
    if args.mode == "validate":
        result = json.loads(args.result.read_text(encoding="utf-8"))
        validate_result(result)
        if args.expect_candidate and result["build_record"]["candidate"]["commit"] != args.expect_candidate:
            raise HarnessError("artifact candidate differs from --expect-candidate")
        print("\n".join(verdict_lines(result)))
        return 0
    if args.profile == "final" and shutil.which("ps") is None:
        raise HarnessError("ps is required for RSS measurement")
    if args.output.is_symlink():
        raise HarnessError(f"output must not be a symlink: {args.output}")
    result = run_value002(args)
    print("\n".join(verdict_lines(result)))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except HarnessError as error:
        print(f"FAIL {error}", file=sys.stderr)
        raise SystemExit(1)
