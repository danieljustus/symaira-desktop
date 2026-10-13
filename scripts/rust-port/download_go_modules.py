#!/usr/bin/env python3
"""Bound retries of transient Go module downloads, never builds or tests."""
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time

sys.dont_write_bytecode = True
# Reuse the native harness's bounded, cross-platform process-tree cleanup.
sys.path.insert(0, str(Path(__file__).with_name("index-status")))
from capture import _terminate_process_tree


# Deliberately narrow: unknown errors, TLS trust and checksum failures stop.
TRANSIENT = re.compile(
    r"(?:read https?://\S+: stream error: stream ID [0-9]+; INTERNAL_ERROR; received from peer"
    r"|reading https?://\S+: (?:502 Bad Gateway|503 Service Unavailable|504 Gateway Timeout))\Z"
)


def retryable(stdout, stderr):
    if stderr.strip():
        return False
    decoder = json.JSONDecoder()
    errors = []
    try:
        text = stdout.decode("utf-8").lstrip()
        while text:
            record, end = decoder.raw_decode(text)
            if not isinstance(record, dict):
                return False
            if "Error" in record:
                error = record["Error"]
                if not isinstance(error, str):
                    return False
                # Go may prefix the diagnostic with this exact module identity.
                # Never accept a transient suffix after another failure.
                if isinstance(record.get("Path"), str) and isinstance(record.get("Version"), str):
                    error = error.removeprefix(f"{record['Path']}@{record['Version']}: ")
                if not TRANSIENT.fullmatch(error):
                    return False
                errors.append(error)
            text = text[end:].lstrip()
    except (ValueError, UnicodeError):
        return False
    return bool(errors)


def download(root, output, *, budget=180, attempts=3, backoff=1):
    """Keep the module inputs untouched and use the caller's isolated caches."""
    root, output = Path(root).resolve(), Path(output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    evidence = Path(tempfile.mkdtemp(prefix="go-download-", dir=output))
    # go mod download can update sums even with -mod=readonly. Use exact private
    # copies and reject any update rather than mutating the repository inputs.
    inputs = {name: (root / name).read_bytes() for name in ("go.mod", "go.sum")}
    for name, content in inputs.items():
        (evidence / name).write_bytes(content)
    env = {**os.environ, "GOENV": "off", "GOWORK": "off", "GOFLAGS": "-mod=readonly"}
    executable = shutil.which("go", path=env.get("PATH"))
    if executable is None:
        raise RuntimeError("Go executable not found")
    deadline = time.monotonic() + budget
    for attempt in range(1, attempts + 1):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            return 124
        stdout = evidence / f"{attempt}.stdout.json"
        stderr = evidence / f"{attempt}.stderr.log"
        timed_out = False
        with stdout.open("xb") as out, stderr.open("xb") as err:
            process = subprocess.Popen(
                [executable, "mod", "download", "-json"], cwd=evidence, env=env,
                stdin=subprocess.DEVNULL, stdout=out, stderr=err,
                start_new_session=os.name != "nt",
                creationflags=subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0,
            )
            try:
                code = process.wait(timeout=remaining)
            except subprocess.TimeoutExpired:
                timed_out = True
                code = 124
            finally:
                _terminate_process_tree(process, env)
        print(f"Go dependency download attempt {attempt}/{attempts}: exit={code}; evidence={evidence}", flush=True)
        if any((evidence / name).read_bytes() != content for name, content in inputs.items()):
            print("Go dependency download changed module inputs; refusing unpinned bootstrap", file=sys.stderr)
            return 1
        if timed_out:
            return 124
        if code == 0:
            return 0
        # Bound parsing; keep full raw failures on disk for diagnosis.
        if stdout.stat().st_size > 4 * 1024 * 1024 or stderr.stat().st_size > 4 * 1024 * 1024:
            return 1
        if attempt == attempts or not retryable(stdout.read_bytes(), stderr.read_bytes()):
            return code if code > 0 else 1
        delay = backoff * attempt
        if deadline - time.monotonic() <= delay:
            return 124
        time.sleep(delay)
    return 1


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("usage: download_go_modules.py EVIDENCE_DIRECTORY")
    raise SystemExit(download(Path.cwd(), Path(sys.argv[1])))
