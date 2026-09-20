"""Regression gates for native command failures and exact Windows checkouts.

Uses Git's real autocrlf checkout filter; this is not native Windows execution.
No working-tree source or index is modified, and all copies are test-owned.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
CONTRACT_FILES = [
    "go.mod", "go.sum", ".goreleaser.yml", "Dockerfile", "VAULT.md",
    ".github/workflows/release.yml", "home-assistant-addon/symdesk/config.yaml",
]
STEPS = {
    "Run native history differential": 2,
    "Verify frozen oracle and differential harness on Windows": 5,
    "Check, lint, and test Rust workspace": 8,
    "Run native Windows representative CLI HTTP and MCP parity": 7,
    "Run native Windows sidecar round-trip suite": 1,
    "Run native Windows version differential": 4,
}
ORACLE_SOURCE_FILES = (
    ROOT / "scripts/rust-port/cmd/historygen/main.go",
    ROOT / "scripts/rust-port/cmd/vaultwritegen/main.go",
)
SOURCE_GUARD_JOBS = ("test", "port-contract", "rust-native")



def write_lf(path, text):
    """Write `text` with literal LF endings and no platform translation.

    Path.write_text() only accepts `newline` on Python 3.10+, and these stubs
    are shell and Python sources whose line endings must survive verbatim.
    """
    with path.open("w", encoding="utf-8", newline="\n") as handle:
        handle.write(text)


def native_step_bodies():
    workflow = (ROOT / ".github/workflows/ci.yml").read_text()
    blocks = re.split(r"^      - name: ", workflow, flags=re.MULTILINE)[1:]
    for name, count in STEPS.items():
        matches = [block for block in blocks if block.splitlines()[0] == name]
        if len(matches) != 1:
            raise AssertionError(f"expected one native step: {name}")
        block = matches[0]
        if "        shell: bash\n" not in block:
            raise AssertionError(f"native step must use Bash: {name}")
        lines = block.split("        run: |\n", 1)[1].splitlines()
        body = []
        for line in lines:
            if line and not line.startswith("          "):
                break
            body.append(line[10:])
        if body[0] != "set -euo pipefail":
            raise AssertionError(f"native step must fail fast: {name}")
        yield name, count, "\n".join(body)


def workflow_job_body(workflow, job):
    job_match = re.search(
        rf"(?ms)^  {job}:\n(?P<body>.*?)(?=^  [A-Za-z0-9_-]+:\n|\Z)",
        workflow,
    )
    if job_match is None:
        raise AssertionError(f"expected {job} job")
    return job_match.group("body")


def pinned_source_guard_oracle_commit():
    commits = set()
    pattern = re.compile(r'(?m)^\s*(?:const\s+)?defaultOracleCommit\s*=\s*"([0-9a-f]{40})"$')
    for source in ORACLE_SOURCE_FILES:
        match = pattern.search(source.read_text())
        if match is None:
            raise AssertionError(f"expected pinned oracle commit in {source.relative_to(ROOT)}")
        commits.add(match.group(1))
    if len(commits) != 1:
        raise AssertionError(f"source guards use different oracle commits: {sorted(commits)}")
    return commits.pop()


class NativeStepControl:
    """Run the real step body with test-owned native command substitutes."""

    def __init__(self, root):
        self.root = Path(root)
        self.log = self.root / "calls.jsonl"
        stub_dir = self.root / "stubs"
        stub_dir.mkdir()
        control = stub_dir / "control.py"
        write_lf(
            control,
            "import json, os, pathlib, sys\n"
            "log = pathlib.Path(os.environ['NATIVE_CONTROL_LOG'])\n"
            "calls = log.read_text().splitlines() if log.exists() else []\n"
            "with log.open('a') as output:\n"
            "    output.write(json.dumps(sys.argv[1:]) + '\\n')\n"
            "sys.exit(23 if len(calls) + 1 == int(os.environ['NATIVE_CONTROL_FAIL_AT']) else 0)\n",
        )
        for command in ("go", "cargo", "python3"):
            stub = stub_dir / command
            write_lf(
                stub,
                "#!/usr/bin/env bash\nexec "
                + shlex.join([Path(sys.executable).as_posix(), control.as_posix(), command])
                + ' "$@"\n',
            )
            stub.chmod(0o700)
        self.env = dict(os.environ)
        self.env["PATH"] = str(stub_dir) + os.pathsep + self.env["PATH"]
        self.env["NATIVE_CONTROL_LOG"] = str(self.log)

    def run(self, body, fail_at=0):
        self.log.unlink(missing_ok=True)
        result = subprocess.run(
            [bash_executable(), "--noprofile", "--norc", "-c", body + "\nprintf MASKED_SUCCESS"],
            cwd=self.root,
            env={**self.env, "NATIVE_CONTROL_FAIL_AT": str(fail_at)},
            capture_output=True, text=True, timeout=30,
        )
        calls = [json.loads(line) for line in self.log.read_text().splitlines()]
        return result, calls


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
    def test_rust_historical_evidence_checkouts_have_full_history(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        for job in ("test", "port-contract", "rust", "rust-native"):
            with self.subTest(job=job):
                job_body = workflow_job_body(workflow, job)
                checkout_blocks = re.findall(
                    r"(?m)^      - uses: actions/checkout@[^\n]+\n"
                    r"(?P<tail>(?:        [^\n]*\n|[ \t]*\n)*)",
                    job_body,
                )
                self.assertEqual(len(checkout_blocks), 1)
                with_match = re.search(
                    r"(?m)^        with:\n(?P<options>(?:          [^\n]*\n)*)",
                    checkout_blocks[0],
                )
                if with_match is None:
                    self.fail(f"{job} checkout must define with options")
                self.assertEqual(
                    re.findall(
                        r"^          (fetch-depth: 0)$",
                        with_match.group("options"),
                        re.MULTILINE,
                    ),
                    ["fetch-depth: 0"],
                )

    def test_clone_based_source_guards_materialize_pinned_oracle_branch(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        commit = pinned_source_guard_oracle_commit()
        expected = (
            "git fetch --no-tags origin "
            f"+{commit}:refs/heads/rust-port-oracle-{commit}"
        )
        for job in SOURCE_GUARD_JOBS:
            with self.subTest(job=job):
                runs = re.findall(
                    rf"(?m)^        run: ({re.escape(expected)})$",
                    workflow_job_body(workflow, job),
                )
                self.assertEqual(runs, [expected])

    def test_native_failures_cannot_be_hidden_by_later_success(self):
        for name, count, body in native_step_bodies():
            with self.subTest(step=name), tempfile.TemporaryDirectory(prefix="native-step-") as temp:
                control = NativeStepControl(temp)
                result, expected = control.run(body)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(len(expected), count)
                self.assertIn("MASKED_SUCCESS", result.stdout)
                for fail_at in range(1, count + 1):
                    with self.subTest(command=expected[fail_at - 1]):
                        result, calls = control.run(body, fail_at)
                        self.assertEqual(result.returncode, 23, result.stderr)
                        self.assertEqual(calls, expected[:fail_at])
                        self.assertNotIn("MASKED_SUCCESS", result.stdout)

    def test_failure_control_detects_masking_and_pipeline_failure(self):
        with tempfile.TemporaryDirectory(prefix="native-step-control-") as temp:
            control = NativeStepControl(temp)
            # Deliberately broken controls must expose later success. This
            # proves the per-command test catches both missing errexit and
            # explicit failure suppression in a body that has the prologue.
            for body in ("go test\ncargo test", "set -euo pipefail\ngo test || true\ncargo test"):
                result, calls = control.run(body, fail_at=1)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(len(calls), 2)
                self.assertIn("MASKED_SUCCESS", result.stdout)
            result, calls = control.run("set -euo pipefail\ngo test | cat\ncargo test", fail_at=1)
            self.assertEqual(result.returncode, 23, result.stderr)
            self.assertEqual(len(calls), 1)
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
