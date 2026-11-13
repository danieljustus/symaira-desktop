#!/usr/bin/env python3
"""Rust side of the SWIFT-003 Files-mode gate.

Usage: swift_consumer_files.py <symdesk-binary> <isolated-home> <vault-dir> <ios-write-manifest> <read-expected-out>

1. Reads the manifest the iOS write layer produced (``testWriteFilesModeVault``)
   and proves the Rust CLI sees exactly those notes in the shared directory:
   ``ls``, ``index``, ``search``, ``props get`` and ``doctor``.
2. Resolves the iOS "conflicted copy" with ``conflict resolve`` and writes notes
   of its own (``note new``, ``props edit``, ``note daily``).
3. Emits the expectation file for the iOS reader test
   (``testReadRustWrittenVault``).

Every Rust invocation runs with an isolated HOME/XDG tree; no Go is involved.
Any mismatch exits non-zero.
"""
import json
import os
import re
import subprocess
import sys
import unicodedata


def nfc(text: str) -> str:
    return unicodedata.normalize("NFC", text)


def main() -> None:
    binary, home, vault, write_manifest, read_out = sys.argv[1:6]
    env = {
        "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
        "HOME": home,
        "XDG_CONFIG_HOME": os.path.join(home, ".config"),
        "XDG_DATA_HOME": os.path.join(home, ".local/share"),
        "XDG_CACHE_HOME": os.path.join(home, ".cache"),
    }
    failures: list[str] = []

    def check(condition: bool, message: str) -> None:
        print(("ok   " if condition else "FAIL ") + message)
        if not condition:
            failures.append(message)

    def rust(*args: str, ok: bool = True) -> str:
        proc = subprocess.run(
            [binary, *args, "--vault", vault], capture_output=True, text=True, env=env, stdin=subprocess.DEVNULL, timeout=300
        )
        if ok and proc.returncode != 0:
            sys.exit(f"symdesk {' '.join(args)} exited {proc.returncode}: {proc.stderr}{proc.stdout}")
        return proc.stdout

    manifest = json.load(open(write_manifest, encoding="utf-8"))

    # ---- 1. Rust reads what the iOS write layer produced -------------------
    listing = json.loads(rust("ls", "--json"))
    by_path = {nfc(item["path"]): item for item in listing}
    for note in manifest["notes"]:
        item = by_path.get(nfc(note["path"]))
        check(item is not None, f"ls lists iOS note {note['path']}")
        if item:
            check(nfc(item["title"]) == nfc(note["title"]), f"ls title matches for {note['path']}: {item['title']!r}")
    for seeded in ("alpha.md", "projects/beta.md", "documents/invoice.md"):
        check(seeded in by_path, f"ls still lists seed note {seeded}")
    check(manifest["conflict"]["copy"] in by_path, "ls lists the iOS conflicted copy as an ordinary note")

    index = json.loads(rust("index", "--json"))
    check(index.get("status") == "ok", f"index --json status ok: {index}")

    search = json.loads(rust("search", "Quokka", "--json"))
    paths = {nfc(r["path"]) for r in search["results"]}
    check(manifest["conflict"]["original"] in paths, "search finds the iOS-edited note by body text")
    check(manifest["conflict"]["copy"] in paths, "search finds the iOS conflicted copy by body text")
    original = next(r for r in search["results"] if r["path"] == manifest["conflict"]["original"])
    check("EDITED on the phone" in original["snippet"], "search snippet shows the updated iOS body")

    unicode_hits = json.loads(rust("search", "日本語", "--json"))["results"]
    check(any("日本語" in r["snippet"] for r in unicode_hits), "search finds unicode body written by iOS")

    for note in manifest["notes"]:
        props = json.loads(rust("props", "get", note["path"], "--json"))
        check(nfc(props.get("title", "")) == nfc(note["title"]), f"props title round-trips for {note['path']}")
        check(props.get("created") == note["created"], f"props created round-trips for {note['path']}: {props.get('created')}")
        check(props.get("tags") == "[]", f"props tags are the empty list for {note['path']}")

    doctor = json.loads(rust("doctor", "--json", ok=False))
    check(doctor.get("contract", {}).get("status") == "ok", f"doctor contract ok: {doctor.get('contract')}")
    check(doctor.get("vault", {}).get("status") == "ok", "doctor vault ok")
    conflicts = doctor.get("conflicts") or []
    check(any(manifest["conflict"]["copy"] in c for c in conflicts), f"doctor reports the iOS conflicted copy: {conflicts}")

    check(
        os.path.isfile(os.path.join(vault, manifest["upload"])),
        f"consume-folder upload {manifest['upload']} is on disk for the desktop watcher",
    )

    # ---- 2. Rust resolves the conflict and writes its own notes ---------------
    resolved = rust("conflict", "resolve", manifest["conflict"]["copy"], "--action", "keep-theirs")
    check("resolved" in resolved, f"conflict resolve keep-theirs: {resolved.strip()[-120:]}")
    check(not os.path.exists(os.path.join(vault, manifest["conflict"]["copy"])), "conflicted copy removed by Rust")
    promoted = open(os.path.join(vault, manifest["conflict"]["original"]), encoding="utf-8").read()
    check("Quokka LOCAL OFFLINE edit" in promoted, "keep-theirs promoted the iOS local version into the original")

    expected_notes = []
    # No quotes/backslashes in Rust-authored titles: `note new` writes the title
    # unescaped (`title: "%s"`), exactly like Go's Service.NoteNew, so a quote
    # produces invalid YAML in both cores. Escaping is exercised in the iOS->Rust
    # direction above (MobileNoteWriter escapes correctly).
    for title, status in (("Rüst Ünïcode 日本", "done"), ("Rust Plain", None)):
        created = json.loads(rust("note", "new", "--title", title, "--json"))
        check(created.get("status") == "created", f"note new {title!r}: {created}")
        if status:
            rust("props", "edit", created["path"], "status", status)
        expected_notes.append({"path": created["path"], "title": title, "tags": [], "status": status})
    daily = json.loads(rust("note", "daily", "--json"))
    check(re.fullmatch(r"\d{4}-\d{2}-\d{2}\.md", daily["path"]) is not None, f"note daily path {daily['path']}")
    expected_notes.append({"path": daily["path"], "title": daily["path"][:-3], "tags": []})

    # iOS-authored notes must still parse identically after the Rust round trip.
    for note in manifest["notes"]:
        body = note["bodyContains"]
        entry = {"path": note["path"], "title": note["title"], "tags": [], "body_contains": body}
        if note["path"] == manifest["conflict"]["original"]:
            entry["body_contains"] = "Quokka LOCAL OFFLINE edit"
        expected_notes.append(entry)
    for key in ("alpha.md", "projects/beta.md"):
        item = by_path[key]
        expected_notes.append({"path": key, "title": item["title"], "tags": [] if key.startswith("projects") else ["planning", "rust-port"]})
    expected_notes.append({"path": "documents/invoice.md", "title": "Invoice 2026-07", "tags": [], "status": "open"})

    json.dump(
        {"notes": expected_notes, "absent": [manifest["conflict"]["copy"]]},
        open(read_out, "w", encoding="utf-8"),
        ensure_ascii=False,
        indent=2,
    )
    print(f"wrote {read_out} with {len(expected_notes)} expected notes")
    if failures:
        sys.exit(f"{len(failures)} Files-mode check(s) failed")


if __name__ == "__main__":
    main()
