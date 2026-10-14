"""Real Go downloads against a controlled loopback module proxy, no public network."""
import http.server
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch
import zipfile

sys.dont_write_bytecode = True
from download_go_modules import download, retryable
from test_native_ci_contract import NativeStepControl, ROOT


MODULE = b"module example.test/dependency\n\ngo 1.20\n"
archive = io.BytesIO()
with zipfile.ZipFile(archive, "w") as z:
    z.writestr("example.test/dependency@v1.0.0/go.mod", MODULE)
    z.writestr("example.test/dependency@v1.0.0/value.go", "package dependency\nconst Value = 1\n")
ZIP = archive.getvalue()


class Proxy(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        server = self.server
        status = 200
        if self.path.endswith(".zip"):
            server.zip_requests += 1
            server.requested.set()
            if server.delay:
                server.release.wait(10)
            status = server.failure if server.zip_requests <= server.failures else 200
            body = ZIP
        elif self.path.endswith(".mod"):
            body = MODULE
        elif self.path.endswith(".info"):
            body = b'{"Version":"v1.0.0","Time":"2020-01-01T00:00:00Z"}'
        else:
            status, body = 404, b""
        if status != 200:
            body = b""
        self.send_response(status)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            pass

    def log_message(self, *_args):
        pass


class DownloadControls(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="go-download-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / "source"
        self.source.mkdir()
        (self.source / "go.mod").write_bytes(
            b"module example.test/consumer\n\ngo 1.20\n\nrequire example.test/dependency v1.0.0\n"
        )
        self.server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Proxy)
        self.server.zip_requests = self.server.failures = 0
        self.server.failure = 503
        self.server.delay = False
        self.server.release = threading.Event()
        self.server.requested = threading.Event()
        thread = threading.Thread(target=self.server.serve_forever)
        thread.start()
        def cleanup():
            self.server.release.set()
            self.server.shutdown()
            self.server.server_close()
            thread.join(2)
            self.assertFalse(thread.is_alive())
        self.addCleanup(cleanup)
        self.env = {**os.environ, "GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off",
                    "GOFLAGS": "-mod=readonly", "GOPROXY": f"http://127.0.0.1:{self.server.server_port}",
                    "GOSUMDB": "off", "GONOPROXY": "none", "GOPRIVATE": "", "NO_PROXY": "*"}
        for key in ("HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
                    "GOCACHE", "GOMODCACHE", "GOPATH", "TMPDIR", "TMP", "TEMP"):
            path = self.root / key.lower()
            path.mkdir()
            self.env[key] = str(path)
        # Obtain the fixture's real Go checksums, then download into a cold cache.
        subprocess.run(["go", "mod", "download", "example.test/dependency"], cwd=self.source, env=self.env,
                       capture_output=True, check=True, timeout=20)
        self.env["GOMODCACHE"] = str(self.root / "cold-cache")
        self.server.zip_requests = 0
        self.server.requested.clear()
        self.before = {n: (self.source / n).read_bytes() for n in ("go.mod", "go.sum")}

    def run_download(self, **kwargs):
        with patch.dict(os.environ, self.env, clear=True):
            result = download(self.source, self.root / "evidence", budget=3, backoff=0, **kwargs)
        self.assertEqual({n: (self.source / n).read_bytes() for n in self.before}, self.before)
        return result

    def test_real_transient_then_success_and_old_single_attempt_fails(self):
        self.server.failures = 1
        self.assertNotEqual(self.run_download(attempts=1), 0)
        self.assertEqual(self.server.zip_requests, 1)
        self.server.zip_requests = 0
        self.assertEqual(self.run_download(), 0)
        self.assertEqual(self.server.zip_requests, 2)

    def test_real_persistent_and_nontransient_failures_are_bounded(self):
        for status, expected in ((503, 3), (404, 1)):
            with self.subTest(status=status):
                self.server.zip_requests = 0
                self.server.failures = 100
                self.server.failure = status
                started = time.monotonic()
                self.assertNotEqual(self.run_download(), 0)
                self.assertEqual(self.server.zip_requests, expected)
                self.assertLess(time.monotonic() - started, 5)

    def test_real_checksum_failure_is_not_retried(self):
        sums = (self.source / "go.sum").read_text()
        lines = sums.splitlines()
        self.assertNotIn("/go.mod", lines[0])
        lines[0] = lines[0].split(" h1:")[0] + " h1:" + "A" * 43 + "="
        (self.source / "go.sum").write_text("\n".join(lines) + "\n")
        self.before["go.sum"] = (self.source / "go.sum").read_bytes()
        self.assertNotEqual(self.run_download(), 0)
        attempts = list((self.root / "evidence").glob("*/[0-9].stderr.log"))
        self.assertEqual(len(attempts), 1)
        self.assertIn(b"checksum mismatch", attempts[0].read_bytes())

    def test_real_hung_download_hits_total_deadline_without_retry(self):
        self.server.delay = True
        started = time.monotonic()
        self.assertEqual(self.run_download(), 124)
        self.assertTrue(self.server.requested.is_set())
        self.assertEqual(self.server.zip_requests, 1)
        self.assertLess(time.monotonic() - started, 6)

    def test_transport_classification_rejects_mixed_or_unstructured_failures(self):
        error = "read https://proxy.golang.org/a.zip: stream error: stream ID 47; INTERNAL_ERROR; received from peer"
        raw = json.dumps({"Error": error}).encode()
        self.assertTrue(retryable(raw, b""))
        prefixed = {"Path": "example.test/dependency", "Version": "v1.0.0",
                    "Error": "example.test/dependency@v1.0.0: " + error}
        self.assertTrue(retryable(json.dumps(prefixed).encode(), b""))
        for prefix in ("checksum mismatch\n", "unexpected failure: ", "other.module@v1.0.0: "):
            self.assertFalse(retryable(json.dumps({**prefixed, "Error": prefix + error}).encode(), b""))
        for stdout, stderr in ((raw, b"SECURITY ERROR"), (raw + b'{"Error":"checksum mismatch"}', b""),
                               (b'not json', b''), (b'{}', b''), (b'{"Error":true}', b'')):
            self.assertFalse(retryable(stdout, stderr))

    def test_real_cli_retries_transient_without_changing_module_inputs(self):
        self.server.failures = 1
        result = subprocess.run(
            [sys.executable, "-B", str(ROOT / "scripts/rust-port/download_go_modules.py"),
             str(self.root / "cli-evidence")], cwd=self.source, env=self.env,
            capture_output=True, timeout=20,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.server.zip_requests, 2)
        self.assertEqual({n: (self.source / n).read_bytes() for n in self.before}, self.before)
        attempts = list((self.root / "cli-evidence").glob("*/[0-9].stdout.json"))
        self.assertEqual(len(attempts), 2)

    def test_real_make_recipe_stops_once_at_each_failed_stage(self):
        with tempfile.TemporaryDirectory(dir=self.root) as temp:
            control = NativeStepControl(temp)
            stress = Path(temp) / "stress"
            recipe = subprocess.check_output(
                ["make", "-n", "resource-stress", f"RESOURCE_STRESS_ROOT={stress.as_posix()}"],
                cwd=ROOT, text=True, timeout=10,
            )
            # Exercise the actual recipe with labelled command substitutes;
            # this is fail-fast control evidence, not a native product test.
            result, calls = control.run("set -e\n" + recipe)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(len(calls), 5)
            self.assertEqual(calls[0][0:2], ["python3", "scripts/rust-port/download_go_modules.py"])
            self.assertEqual([c[0:2] for c in calls[1:]],
                             [["go", "build"], ["cargo", "build"], ["cargo", "test"], ["go", "run"]])
            for failed in range(1, 6):
                result, failed_calls = control.run("set -e\n" + recipe, fail_at=failed)
                self.assertEqual(result.returncode, 23)
                self.assertEqual(failed_calls, calls[:failed])


if __name__ == "__main__":
    unittest.main()
