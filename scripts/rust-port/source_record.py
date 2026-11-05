#!/usr/bin/env python3
"""Record the real Go `symdesk sources` observations for a Rust frozen replay.

`source-differential.py` compared a live Go and a live Rust binary. After the
Go deletion (RUST-019) nothing may need Go, so this script runs the *Go*
binary once through the same two scenarios and writes what it observed:

* registry: `sources list|add|remove` plus external search, as in
  `source-differential.py exercise()`;
* watch: the `sources watch` lifecycle (ready line, then the indexed content
  hash after create, update and delete), as in `watch_exercise()`.

`crates/symdesk-cli/tests/sources_frozen.rs` replays both against the Rust
binary and compares with the recording. Variable text is normalized on both
sides by `normalize()` below and its Rust twin: the canonical and literal
source/vault roots become `<SOURCE>`/`<VAULT>` and the path-derived source ID
`folder-<sha256 of canonical path>` becomes `<ID>`.

Usage: source_record.py [--check] [--go-binary PATH] OUTPUT_DIR_OR_PREFIX
Without --go-binary the Go oracle is built from the working tree with
`-X main.version=devel`.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
from queue import Empty, Queue
import re
import sqlite3
import subprocess
import sys
import tempfile
import threading
import time

ROOT = Path(__file__).resolve().parents[2]
# Reviewed functional source with #1129 canonical identity and native vault
# reference repairs, anchored to the actual merged functional P (the pin the
# live source-differential certified).
GO_ORACLE = "5c666cbf6051eba7b8e659d7d3f16d051c3404b9"
GO_SOURCES = (
    "cmd/symdesk/sources.go",
    "internal/retrieval/sources.go",
    "internal/retrieval/retrieval.go",
    "internal/retrieval/internal/engine/sync.go",
    "internal/service/service.go",
)
ORIGINALS = {
    "alpha.md": "# Alpha\n\nquartzalpha searchable note.\n",
    "plain.txt": "quartztext searchable plain text.\n",
    "source.go": "// quartzgocode searchable Go source.\npackage fixture\n",
}
QUERIES = {"quartzalpha": "alpha.md", "quartztext": "plain.txt", "quartzgocode": "source.go"}
WATCH_STATES = (
    ("write", "# Watched\n\nwatchnewxyz first state.\n"),
    ("write", "# Watched\n\nwatchupdatedxyz second state.\n"),
    ("delete", None),
)
ID_PATTERN = re.compile(r"folder-[0-9a-f]{64}")


def isolated_env(home: Path):
    env = os.environ.copy()
    for key in ("HOME", "USERPROFILE", "XDG_DATA_HOME", "XDG_CONFIG_HOME"):
        env[key] = str(home / key.lower())
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    for key in ("SYMDESK_VAULT", "SYMDESK_SIDECAR"):
        env.pop(key, None)
    env.update(LANG="C", LC_ALL="C", TZ="UTC", NO_COLOR="1")
    return env


def normalizer(source: Path, vault: Path):
    """Return normalize(text) for one scenario's roots (canonical first: on
    macOS the literal /var/... root is a suffix of /private/var/...)."""
    pairs = []
    for path, token in ((source, "<SOURCE>"), (vault, "<VAULT>")):
        pairs.append((str(path.resolve()), token))
        pairs.append((str(path), token))
    pairs.sort(key=lambda pair: -len(pair[0]))

    def normalize(text: str) -> str:
        for needle, token in pairs:
            text = text.replace(needle, token)
        return ID_PATTERN.sub("<ID>", text)

    return normalize


def run_json(binary: Path, vault: Path, home: Path, args: list[str]):
    process = subprocess.run(
        [str(binary), "--json", "--vault", str(vault), *args],
        env=isolated_env(home), capture_output=True, text=True, timeout=60, check=False,
    )
    if process.returncode:
        raise AssertionError(f"{binary.name} {args}: {process.stderr or process.stdout}")
    return process.stdout


def registry_scenario(binary: Path, root: Path, source: Path):
    vault, home = root / "vault", root / "home"
    vault.mkdir(parents=True)
    home.mkdir()
    normalize = normalizer(source, vault)
    steps = []

    def call(*args):
        text = run_json(binary, vault, home, list(args))
        document = json.loads(normalize(text))
        steps.append({"args": [normalize(a) for a in args], "output": document})
        return json.loads(text)

    call("sources", "list")
    added = call("sources", "add", str(source))
    call("sources", "list")
    call("sources", "add", str(source))
    for query, filename in QUERIES.items():
        results = call("search", query)["results"]
        target = [hit for hit in results if hit["path"] == str(source.resolve() / filename)
                  or hit["path"] == str(source / filename)]
        if len(target) != 1:
            raise AssertionError(f"{query}: expected one target hit, got {results!r}")
        hit = target[0]
        if hit["source_type"] != "external" or hit["read_only"] is not True or query not in hit["snippet"]:
            raise AssertionError(f"{query}: unexpected metadata {hit!r}")
        steps[-1]["output"] = {
            "target_hit": {
                "path": normalize(hit["path"]), "title": hit["title"].strip(),
                "snippet": hit["snippet"].strip(), "source_type": hit["source_type"],
                "read_only": hit["read_only"],
            },
        }
    call("sources", "remove", added["source"]["id"])
    call("sources", "list")
    for query in QUERIES:
        call("search", query)
    registry = json.loads(normalize((vault / ".symdesk/search-sources.json").read_text()))
    return {"steps": steps, "registry": registry}


def watch_scenario(binary: Path, root: Path, source: Path):
    vault, home = root / "vault", root / "home"
    vault.mkdir(parents=True)
    home.mkdir()
    normalize = normalizer(source, vault)
    source_id = json.loads(run_json(binary, vault, home, ["sources", "add", str(source)]))["source"]["id"]
    watcher = subprocess.Popen(
        [str(binary), "--vault", str(vault), "sources", "watch", source_id],
        env=isolated_env(home), stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, text=True, bufsize=1,
    )
    lines: Queue[str] = Queue()
    stream = watcher.stderr
    assert stream is not None

    def collect():
        for line in stream:
            lines.put(line)

    threading.Thread(target=collect, daemon=True).start()
    observations = []
    try:
        deadline = time.monotonic() + 30
        ready = None
        while ready is None:
            if watcher.poll() is not None:
                raise AssertionError(f"watch exited before ready: {list(lines.queue)!r}")
            try:
                line = lines.get(timeout=0.2)
            except Empty:
                if time.monotonic() > deadline:
                    raise AssertionError("watch never became ready")
                continue
            if line.startswith("Watching ") and str(source) in line:
                ready = normalize(line.rstrip("\n"))
        nested = source / "nested"
        nested.mkdir()
        time.sleep(0.5)  # Let the new directory's watcher register.
        note = nested / "watched.md"

        def indexed_hash():
            for database in home.rglob("*.db"):
                try:
                    connection = sqlite3.connect(database, timeout=1)
                    try:
                        tables = {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table'")}
                        if "documents" in tables:
                            row = connection.execute("SELECT hash FROM documents WHERE path=?", (str(note),)).fetchone()
                        elif "files" in tables:
                            row = connection.execute("SELECT sha256 FROM files WHERE path=?", (str(note),)).fetchone()
                        else:
                            continue
                        if row:
                            return row[0]
                    finally:
                        connection.close()
                except sqlite3.OperationalError:
                    continue
            return None

        for action, content in WATCH_STATES:
            if action == "write":
                assert content is not None
                note.write_text(content)
                expected = hashlib.sha256(note.read_bytes()).hexdigest()
            else:
                note.unlink()
                expected = None
            end = time.monotonic() + 30
            while indexed_hash() != expected:
                if watcher.poll() is not None or time.monotonic() > end:
                    raise AssertionError(f"watch index hash did not reach {expected}")
                time.sleep(0.2)
            observations.append({"action": action, "content": content, "indexed_sha256": expected})
        nested.rmdir()
    finally:
        watcher.terminate()
        try:
            watcher.wait(timeout=5)
        except subprocess.TimeoutExpired:
            watcher.kill()
            watcher.wait(timeout=5)
        stream.close()
    return {"ready_line": ready, "observations": observations}


def build_go(output: Path) -> Path:
    binary = output / "symdesk-go"
    subprocess.run(["go", "build", "-ldflags=-X main.version=devel", "-o", str(binary), "./cmd/symdesk"],
                   cwd=ROOT, check=True, env=dict(os.environ, GOTOOLCHAIN="go1.26.10"))
    return binary


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--check", action="store_true", help="fail when the committed recordings differ")
    parser.add_argument("--go-binary", type=Path)
    parser.add_argument("--registry", default="testdata/port/cli/source-registry.json")
    parser.add_argument("--watch", default="testdata/port/cli/source-watch.json")
    args = parser.parse_args()
    # The Go oracle source must be exactly the pinned one the recording claims.
    subprocess.run(["git", "diff", "--quiet", GO_ORACLE, "--", *GO_SOURCES], cwd=ROOT, check=True)
    with tempfile.TemporaryDirectory(prefix="symdesk-source-record-") as temporary:
        base = Path(temporary).resolve()
        binary = (args.go_binary or build_go(base)).resolve(strict=True)
        source = base / "external"
        source.mkdir()
        for name, text in ORIGINALS.items():
            (source / name).write_text(text)
        header = {
            "schema_version": 1,
            "oracle": {"commit": GO_ORACLE, "sources": list(GO_SOURCES)},
            "originals": ORIGINALS,
        }
        recordings = {
            args.registry: dict(header, kind="registry", queries=QUERIES,
                                **registry_scenario(binary, base / "registry", source)),
            args.watch: dict(header, kind="watch", **watch_scenario(binary, base / "watch", source)),
        }
        for name, text in ORIGINALS.items():
            assert (source / name).read_text() == text, f"external source {name} was modified"
    failures = []
    for relative, document in recordings.items():
        encoded = (json.dumps(document, indent=2, ensure_ascii=False) + "\n").encode()
        path = ROOT / relative
        if args.check:
            if not path.is_file() or path.read_bytes() != encoded:
                failures.append(relative)
        else:
            path.write_bytes(encoded)
            print(f"recorded {relative}")
    if failures:
        print("drifted from the live Go oracle: " + ", ".join(failures), file=sys.stderr)
        return 1
    if args.check:
        print("PASS source recordings match the live Go oracle")
    return 0


if __name__ == "__main__":
    sys.exit(main())
