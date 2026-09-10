"""Regression gates for native command failures and exact Windows checkouts.

Uses Git's real autocrlf checkout filter; this is not native Windows execution.
No working-tree source or index is modified, and all copies are test-owned.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
CONTRACT_FILES = [
    "go.mod", "go.sum", ".goreleaser.yml", "Dockerfile", "VAULT.md",
    ".github/workflows/release.yml", "home-assistant-addon/symdesk/config.yaml",
]
STEPS = [
    "Verify frozen oracle and differential harness on Windows",
    "Check, lint, and test Rust workspace",
    "Run native Windows representative CLI HTTP and MCP parity",
    "Run native Windows sidecar round-trip suite",
    "Run native Windows version differential",
]


def bash_executable():
    if os.name == "nt":
        # Python's native PATH lookup can select the WSL launcher instead of
        # the Git Bash used by Actions. Locate Bash in the active Git install.
        git = shutil.which("git")
        if git:
            for parent in Path(git).resolve().parents:
                candidate = parent / "bin/bash.exe"
                if candidate.is_file():
                    return str(candidate)
        raise RuntimeError("Git for Windows Bash was not found")
    bash = shutil.which("bash")
    if not bash:
        raise RuntimeError("Bash was not found")
    return bash


class NativeCIContracts(unittest.TestCase):
    def test_native_failures_cannot_be_hidden_by_later_success(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        blocks = re.split(r"^      - name: ", workflow, flags=re.MULTILINE)[1:]
        for name in STEPS:
            with self.subTest(step=name):
                matches = [block for block in blocks if block.splitlines()[0] == name]
                self.assertEqual(len(matches), 1)
                block = matches[0]
                self.assertIn("        shell: bash\n", block)
                self.assertIn("        run: |\n          set -euo pipefail\n", block)
                # Exercise the actual required prologue with a native executable
                # returning failure, followed by a command that would mask it.
                prologue = block.split("        run: |\n", 1)[1].splitlines()[0].strip()
                result = subprocess.run(
                    [bash_executable(), "-c", prologue + "\n\"$BASH\" -c 'exit 23'\nprintf MASKED_SUCCESS"],
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode, 23, result.stderr)
                self.assertNotIn("MASKED_SUCCESS", result.stdout)

    def test_windows_checkout_preserves_production_oracle_bytes(self):
        paths = subprocess.check_output(
            ["git", "ls-files", "-z", "--", "cmd", "internal", *CONTRACT_FILES],
            cwd=ROOT,
        ).decode("utf-8").rstrip("\0").split("\0")
        paths = sorted(p for p in paths if not p.endswith("_test.go") and "/testdata/" not in p)
        self.assertTrue(paths)
        expected = json.loads((ROOT / "testdata/port/provenance.json").read_text())
        with tempfile.TemporaryDirectory(prefix="symdesk-native-checkout-") as temp:
            result = subprocess.run(
                ["git", "-c", "core.autocrlf=true", "checkout-index", "-z", "--prefix=" + Path(temp).as_posix() + "/", "--stdin"],
                # Binary NUL-separated input bypasses Windows text-mode CRLF
                # translation and Git's newline/quoted-path parser.
                cwd=ROOT, input=("\0".join(paths) + "\0").encode("utf-8"),
                capture_output=True, timeout=60,
            )
            self.assertEqual(result.returncode, 0, result.stderr.decode("utf-8", errors="replace"))
            digest = hashlib.sha256()
            for rel in paths:
                content = (Path(temp) / rel).read_bytes()
                self.assertEqual(content, (ROOT / rel).read_bytes(), rel)
                digest.update((rel + "\n").encode())
                digest.update(content)
            self.assertEqual(digest.hexdigest(), expected["production_source_digest"])


if __name__ == "__main__":
    unittest.main()
