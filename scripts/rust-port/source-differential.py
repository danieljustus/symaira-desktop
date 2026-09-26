#!/usr/bin/env python3
"""Exercise the Go and Rust external source CLIs in isolated vaults."""

import json
import hashlib
import os
from pathlib import Path
from queue import Empty, Queue
import subprocess
import sqlite3
import sys
import tempfile
import threading
import time


GO_ORACLE = "cc3f1db375d819a255651186412d0169a86c2bc8"
GO_SOURCES = (
    "cmd/symdesk/sources.go",
    "internal/retrieval/sources.go",
    "internal/retrieval/retrieval.go",
    "internal/retrieval/internal/engine/sync.go",
    "internal/service/service.go",
)


def isolated_env(home: Path):
    env = os.environ.copy()
    for key in ("HOME", "USERPROFILE", "XDG_DATA_HOME", "XDG_CONFIG_HOME"):
        env[key] = str(home / key.lower())
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    for key in ("SYMDESK_VAULT", "SYMDESK_SIDECAR"):
        env.pop(key, None)
    env.update(LANG="C", LC_ALL="C", TZ="UTC", NO_COLOR="1")
    return env


def run(binary: Path, vault: Path, home: Path, args: list[str]):
    process = subprocess.run(
        [str(binary), "--json", "--vault", str(vault), *args],
        env=isolated_env(home), capture_output=True, text=True, timeout=30, check=False,
    )
    if process.returncode:
        raise AssertionError(f"{binary.name} {args}: {process.stderr or process.stdout}")
    return json.loads(process.stdout)


def exercise(binary: Path, root: Path, source: Path):
    vault = root / "vault"
    vault.mkdir(parents=True)
    home = root / "home"
    home.mkdir()
    call = lambda *args: run(binary, vault, home, list(args))
    empty = call("sources", "list")
    added = call("sources", "add", str(source))
    listed = call("sources", "list")
    again = call("sources", "add", str(source))
    hits = {query: call("search", query)["results"] for query in ("quartzalpha", "quartztext", "quartzgocode")}
    removed = call("sources", "remove", added["source"]["id"])
    after = call("sources", "list")
    missing = {query: call("search", query)["results"] for query in hits}
    registry = json.loads((vault / ".symdesk/search-sources.json").read_text())
    return empty, added, listed, again, hits, removed, after, missing, registry


def watch_exercise(binary: Path, root: Path, source: Path):
    vault, home = root / "vault", root / "home"
    vault.mkdir(parents=True)
    home.mkdir()
    source_id = run(binary, vault, home, ["sources", "add", str(source)])["source"]["id"]
    command = [str(binary), "--vault", str(vault), "sources", "watch", source_id]
    watcher = subprocess.Popen(
        command, env=isolated_env(home), stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE, text=True, bufsize=1,
    )
    lines: Queue[str] = Queue()
    assert watcher.stderr is not None

    def collect_stderr():
        for line in watcher.stderr:
            lines.put(line)

    threading.Thread(target=collect_stderr, daemon=True).start()
    try:
        deadline = time.monotonic() + 20
        while True:
            if watcher.poll() is not None:
                raise AssertionError(f"{binary.name} watch exited before ready: {list(lines.queue)!r}")
            try:
                line = lines.get(timeout=0.2)
            except Empty:
                if time.monotonic() > deadline:
                    raise AssertionError(f"{binary.name} watch never became ready")
                continue
            if line.startswith("Watching ") and str(source) in line:
                break

        nested = source / "nested"
        nested.mkdir()
        time.sleep(0.5)  # Give the new directory event time to register its watcher.
        note = nested / "watched.md"

        def indexed_hash():
            for database in home.rglob("*.db"):
                try:
                    with sqlite3.connect(database, timeout=1) as connection:
                        tables = {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")}
                        if "documents" in tables:
                            row = connection.execute("SELECT hash FROM documents WHERE path=?", (str(note),)).fetchone()
                        elif "files" in tables:
                            row = connection.execute("SELECT sha256 FROM files WHERE path=?", (str(note),)).fetchone()
                        else:
                            continue
                        if row:
                            return row[0]
                except sqlite3.OperationalError:
                    continue
            return None

        def wait_for(expected_hash: str | None):
            end = time.monotonic() + 20
            while time.monotonic() < end:
                if watcher.poll() is not None:
                    raise AssertionError(f"{binary.name} watch exited during index update")
                if indexed_hash() == expected_hash:
                    return
                time.sleep(0.2)
            raise AssertionError(f"{binary.name} watch index hash did not reach {expected_hash}")

        note.write_text("# Watched\n\nwatchnewxyz first state.\n")
        wait_for(hashlib.sha256(note.read_bytes()).hexdigest())
        note.write_text("# Watched\n\nwatchupdatedxyz second state.\n")
        wait_for(hashlib.sha256(note.read_bytes()).hexdigest())
        note.unlink()
        wait_for(None)
        nested.rmdir()
    finally:
        watcher.terminate()
        try:
            watcher.wait(timeout=5)
        except subprocess.TimeoutExpired:
            watcher.kill()
            watcher.wait(timeout=5)
        watcher.stderr.close()


def main() -> None:
    watch = len(sys.argv) == 4 and sys.argv[1] == "--watch"
    if len(sys.argv) != (4 if watch else 3):
        raise SystemExit("usage: source-differential.py [--watch] GO_BINARY RUST_BINARY")
    repo = Path(__file__).resolve().parents[2]
    subprocess.run(
        ["git", "diff", "--quiet", GO_ORACLE, "--", *GO_SOURCES],
        cwd=repo, check=True,
    )
    go, rust = (Path(argument).resolve(strict=True) for argument in sys.argv[2 if watch else 1:])
    with tempfile.TemporaryDirectory(prefix="symdesk-source-diff-") as temporary:
        root = Path(temporary).resolve()
        source = root / "external"
        source.mkdir()
        if watch:
            watch_exercise(go, root / "go", source)
            watch_exercise(rust, root / "rust", source)
            print("Go and Rust recursive source watch lifecycle passed")
            return
        originals = {
            "alpha.md": b"# Alpha\n\nquartzalpha searchable note.\n",
            "plain.txt": b"quartztext searchable plain text.\n",
            "source.go": b"// quartzgocode searchable Go source.\npackage fixture\n",
        }
        for name, contents in originals.items():
            (source / name).write_bytes(contents)
        left = exercise(go, root / "go", source)
        right = exercise(rust, root / "rust", source)
        for index in (0, 1, 2, 3, 5, 6, 7, 8):
            if left[index] != right[index]:
                raise AssertionError(f"source operation {index} differs: Go={left[index]!r} Rust={right[index]!r}")
        expected_files = {
            "quartzalpha": "alpha.md", "quartztext": "plain.txt", "quartzgocode": "source.go"
        }
        for query, filename in expected_files.items():
            matched = []
            for name, result in (("Go", left), ("Rust", right)):
                hits = [hit for hit in result[4][query] if hit["path"] == str(source / filename)]
                if len(hits) != 1:
                    raise AssertionError(f"{name} {query} target search yielded {result[4][query]!r}")
                hit = hits[0]
                if hit["path"] != str(source / filename) or hit["source_type"] != "external" or hit["read_only"] is not True:
                    raise AssertionError(f"{name} {query} search metadata: {hit!r}")
                if query not in hit["snippet"]:
                    raise AssertionError(f"{name} {query} search lost document body: {hit!r}")
                matched.append(hit)
            for field in ("title", "snippet"):
                if matched[0][field].strip() != matched[1][field].strip():
                    raise AssertionError(f"external {query} search {field} differs")
        for filename, contents in originals.items():
            if (source / filename).read_bytes() != contents:
                raise AssertionError(f"external source {filename} was modified")
    print("source registry and external search differential passed (search scoring remains separate)")


if __name__ == "__main__":
    main()
