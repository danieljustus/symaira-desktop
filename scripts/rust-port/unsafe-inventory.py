#!/usr/bin/env python3
"""Deterministic `unsafe` inventory gate for the Rust port (SEC-004).

cargo-geiger works per package and counts dependency code too; it is run
informationally, not as a gate. This script is the gate: it lexes every
tracked `*.rs` file (comments, string/char literals and raw strings are
skipped, so prose such as "unsafe path" is not counted) and compares the
number of `unsafe` keyword tokens per file with
`scripts/rust-port/unsafe-allowlist.json`.

It fails when

* a file has `unsafe` tokens that the allowlist does not list, or the count
  differs in either direction (new unsafe, or a stale entry);
* an allowlist entry lacks a safety invariant or a focused test that exists;
* a production (`src/`) token has no `SAFETY:` comment within the preceding
  lines (test files document their invariant in the allowlist entry);
* a crate root (`src/lib.rs` / `src/main.rs`) neither denies/forbids
  `unsafe_code` nor is listed in `crate_roots_allowing_unsafe`;
* `allow(unsafe_code)` / `expect(unsafe_code)` appears in a file that is not
  allowlisted.

Usage: unsafe-inventory.py [--root DIR] [--allowlist FILE] [--report]
Exit 0 = policy holds, 1 = violation, 2 = usage/IO error.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

STATS: dict[str, int] = {}
SAFETY_WINDOW = 8  # lines above the token that may hold the SAFETY: comment
ROOT_ATTR = re.compile(r"#!\[\s*(deny|forbid)\s*\(\s*unsafe_code\s*\)\s*\]")
ALLOW_ATTR = re.compile(r"#!?\[\s*(allow|expect)\s*\(\s*unsafe_code\s*\)\s*\]")


_RAW = re.compile(r'(?:b|c)?r(#*)"')
_CHAR = re.compile(r"'(?:\\(?:u\{[0-9a-fA-F_]+\}|x[0-9a-fA-F]{2}|.)|[^\\'\n])'")


def _blank(segment: str) -> str:
    """Replace everything but newlines with spaces (keeps offsets and lines)."""
    return re.sub(r"[^\n]", " ", segment)


def lex(text: str):
    """Return (cleaned, comments): `cleaned` has comments and string/char/raw
    literals blanked (same length, newlines kept); `comments` maps line -> text."""
    out: list[str] = []
    comments: dict[int, str] = {}
    i, n = 0, len(text)

    def word_start(k: int) -> bool:
        return k == 0 or not (text[k - 1].isalnum() or text[k - 1] == "_")

    def add_comment(a: int, b: int) -> None:
        first = text.count("\n", 0, a) + 1
        for k, ln in enumerate(text[a:b].split("\n")):
            comments[first + k] = ln

    while i < n:
        c = text[i]
        if text.startswith("//", i):
            j = text.find("\n", i)
            j = n if j < 0 else j
            add_comment(i, j)
            out.append(_blank(text[i:j]))
            i = j
        elif text.startswith("/*", i):
            depth, j = 1, i + 2
            while j < n and depth:
                if text.startswith("/*", j):
                    depth, j = depth + 1, j + 2
                elif text.startswith("*/", j):
                    depth, j = depth - 1, j + 2
                else:
                    j += 1
            add_comment(i, j)
            out.append(_blank(text[i:j]))
            i = j
        elif c in "rbc" and word_start(i) and (m := _RAW.match(text, i)):
            closer = '"' + m.group(1)
            j = text.find(closer, m.end())
            j = n if j < 0 else j + len(closer)
            out.append(_blank(text[i:j]))
            i = j
        elif c == '"' or (c in "bc" and word_start(i) and text.startswith('"', i + 1)):
            j = i + (1 if c == '"' else 2)
            while j < n and text[j] != '"':
                j += 2 if text[j] == "\\" else 1
            j = min(j + 1, n)
            out.append(_blank(text[i:j]))
            i = j
        elif c == "'" and (m := _CHAR.match(text, i)):
            out.append(_blank(m.group(0)))
            i = m.end()
        else:
            out.append(c)
            i += 1
    return "".join(out), comments


def scan(path: Path):
    """Return (unsafe token lines, root denies unsafe_code, allow-attr lines, comments)."""
    cleaned, comments = lex(path.read_text(encoding="utf-8"))
    tokens = [cleaned.count("\n", 0, m.start()) + 1 for m in re.finditer(r"\bunsafe\b", cleaned)]
    allow_lines = [cleaned.count("\n", 0, m.start()) + 1 for m in ALLOW_ATTR.finditer(cleaned)]
    return tokens, bool(ROOT_ATTR.search(cleaned)), allow_lines, comments


def tracked_rs(root: Path) -> list[str]:
    out = subprocess.run(
        ["git", "-C", str(root), "ls-files", "-z", "--", "*.rs"],
        check=True, capture_output=True,
    ).stdout.decode()
    return sorted(p for p in out.split("\0") if p and (root / p).is_file())


def has_safety_comment(comments: dict[int, str], line: int) -> bool:
    return any(
        "SAFETY:" in comments.get(l, "") for l in range(max(1, line - SAFETY_WINDOW), line + 1)
    )


def check(root: Path, allow_path: Path, report: bool) -> list[str]:
    allow = json.loads(allow_path.read_text(encoding="utf-8"))
    entries = {e["path"]: e for e in allow["unsafe"]}
    roots_ok = set(allow.get("crate_roots_allowing_unsafe", []))
    errors: list[str] = []
    seen: dict[str, int] = {}
    total = 0
    for rel in tracked_rs(root):
        tokens, root_deny, allow_lines, comments = scan(root / rel)
        is_root = bool(re.fullmatch(r"crates/[^/]+/src/(lib|main)\.rs", rel))
        if is_root and not root_deny and rel not in roots_ok:
            errors.append(f"{rel}: crate root lacks #![deny(unsafe_code)]/#![forbid(unsafe_code)] and is not in crate_roots_allowing_unsafe")
        if rel in roots_ok and root_deny:
            errors.append(f"{rel}: listed in crate_roots_allowing_unsafe but denies unsafe_code (stale entry)")
        if tokens:
            seen[rel] = len(tokens)
            total += len(tokens)
            if report:
                print(f"{rel}: {len(tokens)} unsafe token(s) at lines {tokens}")
        entry = entries.get(rel)
        if (tokens or allow_lines) and entry is None:
            what = f"{len(tokens)} unsafe token(s)" if tokens else "allow(unsafe_code)"
            errors.append(f"{rel}: {what} outside the allowlist (add a documented exception or remove the unsafe)")
            continue
        if entry is None:
            continue
        if len(tokens) != entry["count"]:
            errors.append(f"{rel}: {len(tokens)} unsafe token(s), allowlist says {entry['count']}")
        if entry["scope"] == "src":
            for ln in tokens:
                if not has_safety_comment(comments, ln):
                    errors.append(f"{rel}:{ln}: unsafe without a SAFETY: comment in the preceding {SAFETY_WINDOW} lines")
    for rel, entry in entries.items():
        for key in ("invariant", "test", "scope"):
            if not str(entry.get(key, "")).strip():
                errors.append(f"{rel}: allowlist entry has no {key}")
        if entry.get("scope") not in ("src", "test"):
            errors.append(f"{rel}: scope must be src or test")
        if rel not in seen and entry.get("count", 0) != 0:
            errors.append(f"{rel}: allowlisted but contains no unsafe (stale entry)")
        test_file = str(entry.get("test", "")).split("::")[0]
        if test_file and not (root / test_file).is_file():
            errors.append(f"{rel}: focused test file {test_file} does not exist")
    for rel in sorted(roots_ok):
        if not (root / rel).is_file():
            errors.append(f"{rel}: crate_roots_allowing_unsafe names a missing file")
    STATS.update(tokens=total, files=len(seen), allowlisted=len(entries), violations=len(errors))
    return errors


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").split("\n")[0])
    ap.add_argument("--root", default=None)
    ap.add_argument("--allowlist", default=None)
    ap.add_argument("--report", action="store_true")
    args = ap.parse_args()
    root = Path(args.root) if args.root else Path(
        subprocess.run(["git", "rev-parse", "--show-toplevel"], check=True, capture_output=True, text=True).stdout.strip()
    )
    allow = Path(args.allowlist) if args.allowlist else root / "scripts/rust-port/unsafe-allowlist.json"
    try:
        errors = check(root, allow, args.report)
    except (OSError, ValueError, KeyError, subprocess.CalledProcessError) as exc:
        print(f"unsafe-inventory: {exc}", file=sys.stderr)
        return 2
    for e in errors:
        print(f"unsafe-inventory: VIOLATION {e}", file=sys.stderr)
    print("unsafe-inventory: {tokens} unsafe token(s) in {files} file(s); {allowlisted} allowlisted; {violations} violation(s)".format(**STATS))
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
