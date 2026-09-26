"""Regression gates for native command failures and exact Windows checkouts.

Uses Git's real autocrlf checkout filter; this is not native Windows execution.
No working-tree source or index is modified, and all copies are test-owned.
"""
import hashlib
import importlib.util
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
    "Run native SymRoom rollback handoff": 1,
    "Run native dataset rollback handoff": 2,
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
        self.env["SYMROOM_ROLLBACK_REPORT"] = str(self.root / "symroom-rollback.json")
        self.env["DATASET_ROLLBACK_REPORT"] = str(self.root / "dataset-rollback.json")

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
    def test_symroom_rollback_handoff_runs_on_native_matrix_without_logging_report(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        job = workflow_job_body(workflow, "rust-native")
        step = re.search(
            r"(?ms)^      - name: Run native SymRoom rollback handoff\n"
            r"(?P<body>.*?)(?=^      - name:|\Z)",
            job,
        )
        self.assertIsNotNone(step)
        body = step.group("body")
        self.assertIn(
            "SYMROOM_ROLLBACK_REPORT: ${{ runner.temp }}/symroom-rollback-${{ matrix.os }}.json",
            body,
        )
        self.assertIn(
            'python3 scripts/rust-port/room-rollback.py --go-ref v0.12.2 '
            '--rust-ref HEAD --report "$SYMROOM_ROLLBACK_REPORT" >/dev/null',
            body,
        )
        harness = (ROOT / "scripts/rust-port/room-rollback.py").read_text()
        safety_check = harness.rindex("ensure_report_safe(report, sensitive_key, failure, cleanup_error)")
        report_output = harness.index('encoded = json.dumps(report, indent=2, sort_keys=True)')
        failure_field = harness.index('report["failure"] = str(failure)')
        self.assertLess(failure_field, safety_check)
        self.assertLess(safety_check, report_output)

    def test_rollback_report_suppresses_private_key_in_failure_and_cleanup_errors(self):
        script = ROOT / "scripts/rust-port/room-rollback.py"
        spec = importlib.util.spec_from_file_location("room_rollback", script)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        ensure_report_safe = module.ensure_report_safe
        key = "private-key-sentinel"
        with self.assertRaisesRegex(RuntimeError, "report suppressed"):
            ensure_report_safe({"failure": "command output " + key}, key, None, None)
        with self.assertRaisesRegex(RuntimeError, "report suppressed"):
            ensure_report_safe({}, key, RuntimeError("failure output " + key), None)
        with self.assertRaisesRegex(RuntimeError, "report suppressed"):
            ensure_report_safe({}, key, None, "cleanup details " + key)

    def test_rollback_selects_msvc_linker_after_git_link(self):
        script = ROOT / "scripts/rust-port/room-rollback.py"
        spec = importlib.util.spec_from_file_location("room_rollback", script)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        paths = (r"C:\Program Files\Git\usr\bin\link.exe" + "\n"
                 + r"C:\Program Files\Microsoft Visual Studio\VC\Tools\MSVC\14.0\bin\Hostx64\x64\link.exe")
        self.assertEqual(module.select_msvc_linker(paths), paths.splitlines()[1])
        self.assertTrue(module.is_git_posix_bin(r"C:\Program Files\Git\usr\bin"))
        self.assertFalse(module.is_git_posix_bin(r"C:\Program Files\Git\cmd"))
        with tempfile.TemporaryDirectory() as root:
            linker = Path(root) / "VC/Tools/MSVC/14.0/bin/HostARM64/arm64/link.exe"
            linker.parent.mkdir(parents=True)
            linker.touch()
            self.assertEqual(
                module.msvc_linker_from_installation(root, "arm64", "HostARM64"),
                str(linker),
            )
        self.assertEqual(
            module.parse_msvc_library_environment("LIB=C:\\sdk;C:\\vc\nTOKEN=secret\n"),
            {"LIB": "C:\\sdk;C:\\vc"},
        )

    def test_rollback_blob_extraction_uses_exact_tree_and_exclusions(self):
        script = ROOT / "scripts/rust-port/room-rollback.py"
        spec = importlib.util.spec_from_file_location("room_rollback", script)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        with tempfile.TemporaryDirectory(prefix="room-blob-test-") as temp:
            repo = Path(temp) / "repo"
            repo.mkdir()
            subprocess.run(["git", "init", "-q", str(repo)], check=True)
            (repo / "go.mod").write_bytes(b"module example.test/rollback\n")
            (repo / "skip").mkdir()
            (repo / "skip" / "fixture.txt").write_bytes(b"excluded")
            (repo / "cmd").mkdir()
            (repo / "cmd" / "binary.dat").write_bytes(b"\0\xff\n")
            subprocess.run(["git", "add", "-A"], cwd=repo, check=True)
            tree = subprocess.run(
                ["git", "write-tree"], cwd=repo, check=True,
                capture_output=True, text=True,
            ).stdout.strip()
            output = Path(temp) / "source"
            module.extract_git_blobs(repo, tree, output, os.environ.copy(), "git", ("skip/",))
            self.assertEqual((output / "go.mod").read_bytes(), b"module example.test/rollback\n")
            self.assertEqual((output / "cmd" / "binary.dat").read_bytes(), b"\0\xff\n")
            self.assertFalse((output / "skip").exists())

    def test_retention_state_differential_runs_on_all_native_targets(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        expected_native_os = (
            "os: [ubuntu-latest, ubuntu-24.04-arm, macos-latest, macos-15-intel, "
            "windows-latest, windows-11-arm]"
        )
        for job_name in ("port-contract", "rust-native"):
            with self.subTest(job=job_name):
                self.assertIn(expected_native_os, workflow_job_body(workflow, job_name))
        job = workflow_job_body(workflow, "rust-native")
        self.assertIn(
            "      - name: Run native authoritative retention state differential\n"
            "        shell: bash\n"
            "        run: make retention-state-differential\n",
            job,
        )
        makefile = (ROOT / "Makefile").read_text()
        self.assertRegex(makefile, r"(?m)^\.PHONY:.*\bretention-state-differential\b")
        self.assertRegex(makefile, r"(?m)^port-contract:.*\bretention-state-differential\b")

    def test_native_platform_steps_cover_architecture_specific_runner_labels(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        for job_name in ("port-contract", "rust-native"):
            with self.subTest(job=job_name):
                job = workflow_job_body(workflow, job_name)
                self.assertIn(
                    "contains(fromJSON('[\"windows-latest\", \"windows-11-arm\"]'), matrix.os)",
                    job,
                )
        native = workflow_job_body(workflow, "rust-native")
        self.assertIn(
            "contains(fromJSON('[\"macos-latest\", \"macos-15-intel\"]'), matrix.os)",
            native,
        )

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
