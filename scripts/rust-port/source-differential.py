#!/usr/bin/env python3
"""Exercise the Go and Rust external source CLIs in isolated vaults."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile


GO_ORACLE = "cc3f1db375d819a255651186412d0169a86c2bc8"
GO_SOURCES = (
    "cmd/symdesk/sources.go",
    "internal/retrieval/sources.go",
    "internal/retrieval/retrieval.go",
    "internal/retrieval/internal/engine/sync.go",
    "internal/service/service.go",
)


def run(binary: Path, vault: Path, home: Path, args: list[str]):
    env = os.environ.copy()
    for key in ("HOME", "USERPROFILE", "XDG_DATA_HOME", "XDG_CONFIG_HOME"):
        env[key] = str(home / key.lower())
        Path(env[key]).mkdir(parents=True, exist_ok=True)
    for key in ("SYMDESK_VAULT", "SYMDESK_SIDECAR"):
        env.pop(key, None)
    env.update(LANG="C", LC_ALL="C", TZ="UTC", NO_COLOR="1")
    process = subprocess.run(
        [str(binary), "--json", "--vault", str(vault), *args],
        env=env, capture_output=True, text=True, timeout=30, check=False,
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


def main() -> None:
    if len(sys.argv) != 3:
        raise SystemExit("usage: source-differential.py GO_BINARY RUST_BINARY")
    repo = Path(__file__).resolve().parents[2]
    subprocess.run(
        ["git", "diff", "--quiet", GO_ORACLE, "--", *GO_SOURCES],
        cwd=repo, check=True,
    )
    go, rust = (Path(argument).resolve(strict=True) for argument in sys.argv[1:])
    with tempfile.TemporaryDirectory(prefix="symdesk-source-diff-") as temporary:
        root = Path(temporary).resolve()
        source = root / "external"
        source.mkdir()
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
