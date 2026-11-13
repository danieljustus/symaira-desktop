#!/usr/bin/env python3
"""Fixed-order control experiment for the VALUE-002 ``http.snapshot`` finding.

Diagnostic only: it never decides VALUE-002 and never writes a gate result.
It reuses value001's vault generator, index preparation and server wrapper
unchanged, then times ``GET /api/v1/snapshot`` against the same measured
binaries in four regimes (see ``docs/rust-port/value002-snapshot-control.md``):

* ``fixed-go-rust`` / ``fixed-rust-go``: snapshot only, order fixed per block;
* ``isolated``: one server alone for a whole block, then the other;
* ``gate-fixed-*``: the gate's seven-operation rotation, order fixed per block.

Each timed request records time-to-headers (server work plus transfer start)
and total time (including the client reading the body), so a client-side cost
can be told apart from server-side work.
"""

from __future__ import annotations

import argparse
import http.client
import json
import os
import statistics
import sys
import tempfile
import time
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
import value001 as v1  # noqa: E402

IDLE = v1.HTTP_CONTROL_IDLE_SECONDS


def timed(server: v1.RunningServer, name: str) -> dict[str, float]:
    op = v1.http_operation(name)
    headers = {"Connection": "close"}
    if op["auth"] == "valid":
        headers["Authorization"] = f"Bearer {v1.TOKEN}"
    headers.update(op.get("headers") or {})
    started = time.perf_counter_ns()
    conn = http.client.HTTPConnection("127.0.0.1", server.port, timeout=5.0)
    conn.request(op["method"], op["path"], headers=headers)
    response = conn.getresponse()
    headed = time.perf_counter_ns()
    body = response.read()
    done = time.perf_counter_ns()
    status = response.status
    conn.close()
    if name == "snapshot" and status != 200:
        raise v1.HarnessError(f"{server.binary} snapshot status {status}")
    return {
        "headers_ms": (headed - started) / 1e6,
        "total_ms": (done - started) / 1e6,
        "bytes": float(len(body)),
    }


def prime(server: v1.RunningServer) -> None:
    status, _, _ = server.request_raw("GET", "/healthz", auth="none")
    if status != 200:
        raise v1.HarnessError("healthz priming failed")
    time.sleep(IDLE)


def block_pair(servers: dict[str, v1.RunningServer], order: tuple[str, str], rounds: int, ops: list[str] | None) -> dict[str, list[dict[str, float]]]:
    out: dict[str, list[dict[str, float]]] = {"go": [], "rust": []}
    for index in range(rounds):
        for side in order:
            prime(servers[side])
        names = ["snapshot"] if ops is None else (ops[index % len(ops):] + ops[: index % len(ops)])
        for name in names:
            for side in order:
                sample = timed(servers[side], name)
                time.sleep(IDLE)
                if name == "snapshot":
                    out[side].append(sample)
    return out


def block_isolated(servers: dict[str, v1.RunningServer], first: str, rounds: int) -> dict[str, list[dict[str, float]]]:
    out: dict[str, list[dict[str, float]]] = {"go": [], "rust": []}
    for side in (first, "rust" if first == "go" else "go"):
        for _ in range(rounds):
            prime(servers[side])
            out[side].append(timed(servers[side], "snapshot"))
            time.sleep(IDLE)
    return out


def summarise(name: str, data: dict[str, list[dict[str, float]]], warmups: int) -> dict[str, Any]:
    result: dict[str, Any] = {"block": name, "load1_after": os.getloadavg()[0]}
    for side in ("go", "rust"):
        samples = data[side][warmups:]
        result[side] = {
            key: statistics.median(s[key] for s in samples) for key in ("headers_ms", "total_ms", "bytes")
        }
        result[side]["n"] = len(samples)
    for key in ("headers_ms", "total_ms"):
        result[f"ratio_{key}"] = result["rust"][key] / result["go"][key]
    paired = [r["total_ms"] / g["total_ms"] for g, r in zip(data["go"][warmups:], data["rust"][warmups:])]
    result["paired_total_ratio_median"] = statistics.median(paired)
    result["paired_total_ratio_above_1_10"] = sum(1 for p in paired if p > 1.10)
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary-dir", type=Path, required=True, help="dir with go/symdesk and rust/symdesk")
    parser.add_argument("--rounds", type=int, default=100)
    parser.add_argument("--warmups", type=int, default=10)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    binaries = {side: args.binary_dir / side / "symdesk" for side in ("go", "rust")}
    for path in binaries.values():
        v1.require_executable_regular(path, str(path))
    rounds = args.rounds + args.warmups
    results: list[dict[str, Any]] = []
    with tempfile.TemporaryDirectory(prefix="symdesk-snapctl-", ignore_cleanup_errors=True) as tmp:
        root = Path(tmp)
        servers: dict[str, v1.RunningServer] = {}
        try:
            for side, binary in binaries.items():
                home = root / f"{side}-http"
                vault = home / "vault"
                manifest = v1.write_vault(vault, include_http_probe=True)
                expected = v1.expected_vault_semantics(manifest, include_http_probe=True)
                v1.prepare_index(binary, home, vault, home / "sidecar.db", expected)
                servers[side] = v1.RunningServer(binary, home, vault, home / "sidecar.db")
                status, body, headers = servers[side].request_raw("GET", "/api/v1/snapshot")
                v1.validate_http("snapshot", status, body, headers, expected, vault)
            ops: list[str] = [str(name) for name in v1.HTTP_OPERATION_NAMES]
            plan = [
                ("fixed-go-rust", lambda: block_pair(servers, ("go", "rust"), rounds, None)),
                ("fixed-rust-go", lambda: block_pair(servers, ("rust", "go"), rounds, None)),
                ("isolated-go-first", lambda: block_isolated(servers, "go", rounds)),
                ("gate-fixed-go-rust", lambda: block_pair(servers, ("go", "rust"), rounds, ops)),
                ("gate-fixed-rust-go", lambda: block_pair(servers, ("rust", "go"), rounds, ops)),
                ("fixed-rust-go#2", lambda: block_pair(servers, ("rust", "go"), rounds, None)),
                ("fixed-go-rust#2", lambda: block_pair(servers, ("go", "rust"), rounds, None)),
                ("isolated-rust-first", lambda: block_isolated(servers, "rust", rounds)),
            ]
            for name, run in plan:
                summary = summarise(name, run(), args.warmups)
                results.append(summary)
                print(json.dumps(summary), flush=True)
        finally:
            v1.stop_all(list(servers.values()))
    args.output.write_text(json.dumps({"schema": 1, "rounds": args.rounds, "warmups": args.warmups, "blocks": results}, indent=2) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
