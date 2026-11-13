#!/usr/bin/env python3
"""Go-free fixture provenance check (RUST-019).

`testdata/port/provenance.json` binds every committed Go-recorded fixture to a
SHA-256 and records the oracle commit, the embedded oracle git bundle and the
source digests of the Go generators. The Go portgen that computed the
production/generator digests left the tree with the Go code (last Go commit
`36aad3d4`, tag `v0.12.2`), so those two digests cannot be recomputed from the
working tree any more. They stay as recorded history; this check verifies what
can still be verified without Go:

* the schema and the shape of every recorded identity,
* the oracle bundle bytes against their recorded SHA-256,
* the SHA-256 of every fixture listed in `fixture_checksums` (no fixture bytes
  are normalized or regenerated),
* with `--history` (needs the full git history): every `source_hashes` entry of
  every fixture against the Go blob at the last Go commit.
"""
import argparse
import base64
import hashlib
import json
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PROVENANCE = ROOT / "testdata/port/provenance.json"
LAST_GO_COMMIT = "36aad3d4dabfd2c3b00ad16dbf5a506282e624ca"
HEX64 = re.compile(r"[0-9a-f]{64}")
HEX40 = re.compile(r"[0-9a-f]{40}")
# Fixtures whose `source_hash` is the digest of several concatenated files.
COMBINED_SOURCES = {
    "testdata/port/room/index.json": [
        "internal/room/index/index.go",
        "internal/room/members/members.go",
    ],
    "testdata/port/room/merge-read.json": [
        "internal/room/journal/merge.go",
        "internal/room/journal/journal.go",
    ],
    "testdata/port/room/watch-stream.json": ["internal/room/desk/watch.go"],
    "testdata/port/room/merge.json": ["internal/room/journal/merge.go"],
}


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def check_identity(doc):
    errors = []
    if doc.get("schema_version") != 1:
        errors.append("schema_version must be 1")
    oracle = doc.get("oracle", {})
    if not HEX40.fullmatch(str(oracle.get("commit", ""))):
        errors.append("oracle.commit must be a full lowercase commit")
    if not str(oracle.get("release", "")).strip():
        errors.append("oracle.release must not be blank")
    for key in ("production_source_digest", "generator_source_digest"):
        if not HEX64.fullmatch(str(doc.get(key, ""))):
            errors.append(f"{key} must be 64 lowercase hex characters")
    counts = doc.get("surface_counts", {})
    if not counts or not all(isinstance(v, int) and v > 0 for v in counts.values()):
        errors.append("surface_counts must be positive integers")
    return errors


def check_bundle(doc):
    bundle = doc.get("oracle_bundle")
    if bundle is None:
        return []
    errors = []
    if not HEX40.fullmatch(str(bundle.get("anchor", ""))):
        errors.append("oracle_bundle.anchor must be a full lowercase commit")
    try:
        data = base64.b64decode(bundle.get("data", ""), validate=True)
    except ValueError:
        return errors + ["oracle_bundle.data is not base64"]
    if sha256(data) != bundle.get("sha256"):
        errors.append("oracle_bundle.sha256 does not match the bundle bytes")
    lines = data.split(b"\n", 4)
    if lines[0] != b"# v2 git bundle":
        errors.append("oracle_bundle is not a v2 git bundle")
    elif f"-{bundle.get('anchor')}" not in lines[1].decode(errors="replace"):
        errors.append("oracle_bundle prerequisite is not the recorded anchor")
    elif not lines[2].decode(errors="replace").startswith(doc["oracle"]["commit"]):
        errors.append("oracle_bundle does not advertise the oracle commit")
    return errors


def check_checksums(doc, root):
    errors = []
    checksums = doc.get("fixture_checksums", {})
    if not checksums:
        errors.append("fixture_checksums is empty")
    for rel, expected in sorted(checksums.items()):
        if not HEX64.fullmatch(str(expected)):
            errors.append(f"{rel}: recorded checksum is not 64 lowercase hex")
            continue
        path = root / rel
        if not path.is_file():
            errors.append(f"{rel}: fixture is missing")
        elif sha256(path.read_bytes()) != expected:
            errors.append(f"{rel}: checksum differs from the recorded value")
    return errors


def git_blob(root, commit, rel):
    result = subprocess.run(
        ["git", "--no-replace-objects", "show", f"{commit}:{rel}"],
        cwd=root, capture_output=True, check=False,
    )
    return result.stdout if result.returncode == 0 else None


def tree_paths(root, commit):
    result = subprocess.run(
        ["git", "--no-replace-objects", "ls-tree", "-r", "--name-only", commit],
        cwd=root, capture_output=True, check=False, text=True,
    )
    return result.stdout.splitlines() if result.returncode == 0 else []


def fixture_sources(root):
    """Yield (fixture, kind, mapping) for every recorded source hash."""
    paths = sorted((root / "testdata/port").rglob("*.json"))
    paths += sorted(root.glob("crates/*/tests/data/**/*.json"))
    for path in paths:
        try:
            doc = json.loads(path.read_text())
        except (ValueError, UnicodeDecodeError):
            continue
        if not isinstance(doc, dict):
            continue
        rel = path.relative_to(root).as_posix()
        hashes = doc.get("source_hashes")
        if isinstance(hashes, dict) and hashes:
            yield rel, "map", hashes
        single = doc.get("source_hash")
        if isinstance(single, str):
            yield rel, "single", single


def check_reachable(root, commit):
    """A pinned commit must stay reachable from a published branch or tag.

    A squash merge orphans the commits of its PR branch; once that branch is
    deleted a fresh clone can no longer fetch them, so an object that merely
    exists in a local clone is not enough.
    """
    refs = subprocess.run(
        ["git", "for-each-ref", "--count=1", "--contains", commit,
         "--format=%(refname)", "refs/remotes", "refs/tags"],
        cwd=root, capture_output=True, check=False, text=True,
    )
    if refs.returncode != 0 or not refs.stdout.strip():
        return [f"pinned commit {commit} is not reachable from any remote branch or tag "
                "(anchor it with an oracle-pins/* branch or oracle/* tag that is not an "
                "ancestor of main)"]
    return []


def check_history(root, commit=LAST_GO_COMMIT):
    errors, checked = [], 0
    probe = subprocess.run(["git", "cat-file", "-e", f"{commit}^{{commit}}"],
                           cwd=root, capture_output=True, check=False)
    if probe.returncode != 0:
        return [f"last Go commit {commit} is not in this clone (need full history)"], 0
    errors += check_reachable(root, commit)
    paths = tree_paths(root, commit)
    for rel, kind, value in fixture_sources(root):
        if kind == "map":
            for source, expected in sorted(value.items()):
                blob = git_blob(root, commit, source)
                checked += 1
                if blob is None:
                    # Some fixtures record package-relative names (`store.go`).
                    # Accept them only when a blob with that path suffix has
                    # exactly the recorded hash.
                    suffix = "/" + source
                    candidates = [p for p in paths if p.endswith(suffix)]
                    if any(sha256(git_blob(root, commit, p) or b"") == expected
                           for p in candidates):
                        continue
                    errors.append(f"{rel}: {source} is not a blob at {commit[:8]}")
                elif sha256(blob) != expected:
                    errors.append(f"{rel}: {source} differs from the recorded hash")
        else:
            sources = COMBINED_SOURCES.get(rel)
            if sources is None:
                errors.append(f"{rel}: no source list known for its source_hash")
                continue
            blobs = [git_blob(root, commit, source) for source in sources]
            checked += 1
            if any(blob is None for blob in blobs):
                errors.append(f"{rel}: a source is not a blob at {commit[:8]}")
            elif sha256(b"".join(b for b in blobs if b is not None)) != value:
                errors.append(f"{rel}: source_hash differs from the recorded hash")
    return errors, checked


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument("--history", action="store_true",
                        help="also verify source hashes against the last Go commit")
    args = parser.parse_args(argv)
    doc = json.loads((args.root / "testdata/port/provenance.json").read_text())
    errors = check_identity(doc) + check_bundle(doc) + check_checksums(doc, args.root)
    count = len(doc.get("fixture_checksums", {}))
    if args.history:
        history_errors, checked = check_history(args.root)
        errors += history_errors
        print(f"history: {checked} recorded source hashes checked against {LAST_GO_COMMIT[:8]}")
    for error in errors:
        print(f"FAIL {error}", file=sys.stderr)
    if errors:
        return 1
    print(f"PASS provenance: {count} fixture checksums, oracle {doc['oracle']['commit'][:8]}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
