"""Regression gates for native command failures and exact Windows checkouts.

Uses Git's real autocrlf checkout filter; this is not native Windows execution.
No working-tree source or index is modified, and all copies are test-owned.
"""
import hashlib
import json
from pathlib import Path
import re
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
                    ["bash", "-c", prologue + "\nbash -c 'exit 23'\nprintf MASKED_SUCCESS"],
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode, 23)
                self.assertNotIn("MASKED_SUCCESS", result.stdout)

    def test_windows_checkout_preserves_production_oracle_bytes(self):
        paths = subprocess.check_output(
            ["git", "ls-files", "--", "cmd", "internal", *CONTRACT_FILES],
            cwd=ROOT, text=True,
        ).splitlines()
        paths = sorted(p for p in paths if not p.endswith("_test.go") and "/testdata/" not in p)
        self.assertTrue(paths)
        expected = json.loads((ROOT / "testdata/port/provenance.json").read_text())
        with tempfile.TemporaryDirectory(prefix="symdesk-native-checkout-") as temp:
            subprocess.run(
                ["git", "-c", "core.autocrlf=true", "checkout-index", "--prefix=" + Path(temp).as_posix() + "/", "--stdin"],
                cwd=ROOT, input="\n".join(paths) + "\n", text=True,
                capture_output=True, check=True, timeout=60,
            )
            digest = hashlib.sha256()
            for rel in paths:
                content = (Path(temp) / rel).read_bytes()
                self.assertEqual(content, (ROOT / rel).read_bytes(), rel)
                digest.update((rel + "\n").encode())
                digest.update(content)
            self.assertEqual(digest.hexdigest(), expected["production_source_digest"])


if __name__ == "__main__":
    unittest.main()
