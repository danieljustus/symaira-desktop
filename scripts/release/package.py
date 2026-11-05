#!/usr/bin/env python3
"""Rust release packaging that replaces GoReleaser (DIST-001/002).

The archive contract is the one GoReleaser shipped up to v0.12.2, read back
from the published assets: one flat `.tar.gz` per target named
`symaira-desktop_<version>_<os>_<arch>.tar.gz` with exactly the members
LICENSE, README.md, VAULT.md (0644) and symdesk, symroom (0755), plus a
`checksums.txt` of `<sha256>  <name>` lines sorted by name, and the Homebrew
formula `symdesk` that installs both binaries.

Subcommands:
  archive   build one archive from two release binaries
  verify    check an archive against the member contract (and run `version`)
  checksums write checksums.txt for every archive in a directory
  formula   render Formula/symdesk.rb from checksums.txt
"""
from __future__ import annotations

import argparse
import gzip
import hashlib
import io
import os
import platform
import subprocess
import sys
import tarfile
import tempfile
from pathlib import Path

PROJECT = "symaira-desktop"
DOCS = ("LICENSE", "README.md", "VAULT.md")
BINARIES = ("symdesk", "symroom")
# Platform scope 2026-10-29 (#1364): macOS arm64 plus Linux amd64/arm64.
TARGETS = {
    ("darwin", "arm64"): "aarch64-apple-darwin",
    ("linux", "amd64"): "x86_64-unknown-linux-musl",
    ("linux", "arm64"): "aarch64-unknown-linux-musl",
}
REPO_URL = "https://github.com/danieljustus/symaira-desktop"


def archive_name(version: str, os_name: str, arch: str) -> str:
    return f"{PROJECT}_{version}_{os_name}_{arch}.tar.gz"


def _member(name: str, data: bytes, mode: int, mtime: int) -> tuple[tarfile.TarInfo, bytes]:
    info = tarfile.TarInfo(name)
    info.size = len(data)
    info.mode = mode
    info.mtime = mtime
    info.uid = info.gid = 0
    info.uname = info.gname = ""
    info.type = tarfile.REGTYPE
    return info, data


def build_archive(root: Path, version: str, os_name: str, arch: str,
                  binaries: dict[str, Path], out_dir: Path, mtime: int) -> Path:
    if (os_name, arch) not in TARGETS:
        raise SystemExit(f"unsupported target {os_name}/{arch}")
    members = [_member(doc, (root / doc).read_bytes(), 0o644, mtime) for doc in DOCS]
    for name in BINARIES:
        members.append(_member(name, binaries[name].read_bytes(), 0o755, mtime))
    raw = io.BytesIO()
    with tarfile.open(fileobj=raw, mode="w", format=tarfile.PAX_FORMAT) as tar:
        for info, data in members:
            tar.addfile(info, io.BytesIO(data))
    out_dir.mkdir(parents=True, exist_ok=True)
    path = out_dir / archive_name(version, os_name, arch)
    # mtime=0 in the gzip header keeps the archive byte-reproducible.
    with open(path, "wb") as handle, gzip.GzipFile(filename="", mode="wb", fileobj=handle, mtime=0) as gz:
        gz.write(raw.getvalue())
    return path


def host_target() -> tuple[str, str] | None:
    system = {"Darwin": "darwin", "Linux": "linux"}.get(platform.system())
    machine = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64", "AMD64": "amd64"}.get(platform.machine())
    return (system, machine) if system and machine else None


def verify_archive(path: Path, version: str, run: bool) -> list[str]:
    errors: list[str] = []
    stem = path.name
    parts = stem.removesuffix(".tar.gz").split("_")
    if not stem.endswith(".tar.gz") or len(parts) != 4 or parts[0] != PROJECT or parts[1] != version:
        return [f"{stem}: name does not match {PROJECT}_{version}_<os>_<arch>.tar.gz"]
    target = (parts[2], parts[3])
    if target not in TARGETS:
        errors.append(f"{stem}: target {target} is outside the supported platform scope")
    with tarfile.open(path) as tar:
        members = tar.getmembers()
        names = [m.name for m in members]
        expected = list(DOCS + BINARIES)
        if sorted(names) != sorted(expected) or len(names) != len(set(names)):
            errors.append(f"{stem}: members {names}, want exactly {expected}")
        for m in members:
            want = 0o755 if m.name in BINARIES else 0o644
            if not m.isreg():
                errors.append(f"{stem}: {m.name} is not a regular file")
            if m.mode & 0o7777 != want:
                errors.append(f"{stem}: {m.name} mode {oct(m.mode)}, want {oct(want)}")
        if run and not errors and target == host_target():
            with tempfile.TemporaryDirectory() as tmp:
                # Members were checked above: exact names, regular files only.
                # The filter argument needs Python >= 3.11.4 / 3.12.
                if hasattr(tarfile, "data_filter"):
                    tar.extractall(tmp, filter="data")
                else:
                    tar.extractall(tmp)  # noqa: S202 - members validated above
                for name in BINARIES:
                    out = subprocess.run([os.path.join(tmp, name), "version"], capture_output=True, text=True, timeout=60)
                    got = out.stdout.strip()
                    if out.returncode != 0 or got != f"{name} {version}":
                        errors.append(f"{stem}: `{name} version` -> exit {out.returncode} {got!r}, want '{name} {version}'")
    return errors


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as handle:
        for block in iter(lambda: handle.read(1 << 20), b""):
            digest.update(block)
    return digest.hexdigest()


def write_checksums(directory: Path) -> Path:
    names = sorted(p.name for p in directory.iterdir() if p.name.endswith(".tar.gz") or p.name.endswith(".dmg") or p.name.endswith(".zip"))
    if not names:
        raise SystemExit(f"no release archives in {directory}")
    path = directory / "checksums.txt"
    path.write_text("".join(f"{sha256(directory / n)}  {n}\n" for n in names))
    return path


def render_formula(version: str, checksums: Path) -> str:
    sums = {}
    for line in checksums.read_text().splitlines():
        digest, _, name = line.partition("  ")
        sums[name] = digest

    def block(os_name: str, arch: str) -> str:
        name = archive_name(version, os_name, arch)
        if name not in sums:
            raise SystemExit(f"checksums.txt has no entry for {name}")
        return (f'      url "{REPO_URL}/releases/download/v{version}/{name}"\n'
                f'      sha256 "{sums[name]}"\n'
                "      define_method(:install) do\n"
                '        bin.install "symdesk"\n'
                '        bin.install "symroom"\n'
                "      end\n")

    return ("# typed: false\n"
            "# frozen_string_literal: true\n\n"
            "# Generated by scripts/release/package.py in danieljustus/symaira-desktop. DO NOT EDIT.\n"
            "class Symdesk < Formula\n"
            '  desc "Local-first markdown vault workspace: CLI and MCP server"\n'
            f'  homepage "{REPO_URL}"\n'
            f'  version "{version}"\n'
            '  license "Apache-2.0"\n\n'
            "  on_macos do\n"
            "    if Hardware::CPU.arm?\n" + block("darwin", "arm64") + "    end\n"
            "  end\n\n"
            "  on_linux do\n"
            "    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?\n" + block("linux", "amd64") + "    end\n"
            "    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?\n" + block("linux", "arm64") + "    end\n"
            "  end\n\n"
            '  conflicts_with "danieljustus/tap/symroom", because: "both install the symroom binary"\n\n'
            "  test do\n"
            '    assert_equal "symdesk #{version}", shell_output("#{bin}/symdesk version").strip\n'
            '    assert_equal "symroom #{version}", shell_output("#{bin}/symroom version").strip\n'
            "  end\n"
            "end\n")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="cmd", required=True)
    a = sub.add_parser("archive")
    a.add_argument("--version", required=True)
    a.add_argument("--os", required=True, choices=sorted({t[0] for t in TARGETS}))
    a.add_argument("--arch", required=True, choices=sorted({t[1] for t in TARGETS}))
    a.add_argument("--symdesk", required=True, type=Path)
    a.add_argument("--symroom", required=True, type=Path)
    a.add_argument("--out", required=True, type=Path)
    a.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    a.add_argument("--mtime", type=int, default=int(os.environ.get("SOURCE_DATE_EPOCH", "0")))
    v = sub.add_parser("verify")
    v.add_argument("--version", required=True)
    v.add_argument("--no-run", action="store_true", help="skip executing host-compatible binaries")
    v.add_argument("archives", nargs="+", type=Path)
    c = sub.add_parser("checksums")
    c.add_argument("directory", type=Path)
    f = sub.add_parser("formula")
    f.add_argument("--version", required=True)
    f.add_argument("--checksums", required=True, type=Path)
    f.add_argument("--out", type=Path)
    t = sub.add_parser("target", help="print the Rust target triple for <os> <arch>")
    t.add_argument("os")
    t.add_argument("arch")
    args = parser.parse_args()

    if args.cmd == "archive":
        path = build_archive(args.root, args.version, args.os, args.arch,
                             {"symdesk": args.symdesk, "symroom": args.symroom}, args.out, args.mtime)
        print(path)
    elif args.cmd == "verify":
        errors = [e for p in args.archives for e in verify_archive(p, args.version, not args.no_run)]
        for error in errors:
            print(f"FAIL {error}", file=sys.stderr)
        if errors:
            return 1
        print(f"PASS {len(args.archives)} archive(s) match the release member contract")
    elif args.cmd == "checksums":
        print(write_checksums(args.directory))
    elif args.cmd == "formula":
        text = render_formula(args.version, args.checksums)
        if args.out:
            args.out.write_text(text)
        else:
            sys.stdout.write(text)
    elif args.cmd == "target":
        triple = TARGETS.get((args.os, args.arch))
        if not triple:
            print(f"unsupported target {args.os}/{args.arch}", file=sys.stderr)
            return 2
        print(triple)
    return 0


if __name__ == "__main__":
    sys.exit(main())
