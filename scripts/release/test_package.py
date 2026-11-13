"""Checks for scripts/release/package.py: the produced archive satisfies the
member contract, is byte-reproducible, and each contract violation fails."""
import io
import sys
import tarfile
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import package  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]


def fake_binary(directory: Path, name: str, version: str) -> Path:
    path = directory / name
    path.write_text(f"#!/bin/sh\necho '{name} {version}'\n")
    path.chmod(0o755)
    return path


class PackageTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.dir = Path(self.tmp.name)
        self.bins = {n: fake_binary(self.dir, n, "1.2.3") for n in package.BINARIES}

    def tearDown(self):
        self.tmp.cleanup()

    def build(self, os_name="linux", arch="arm64", out="out"):
        return package.build_archive(ROOT, "1.2.3", os_name, arch, self.bins, self.dir / out, 0)

    def test_archive_matches_contract_and_is_reproducible(self):
        first = self.build()
        second = self.build(out="again")
        self.assertEqual(first.name, "symaira-desktop_1.2.3_linux_arm64.tar.gz")
        self.assertEqual(first.read_bytes(), second.read_bytes())
        self.assertEqual(package.verify_archive(first, "1.2.3", run=False), [])
        with tarfile.open(first) as tar:
            self.assertEqual([m.name for m in tar.getmembers()], ["LICENSE", "README.md", "VAULT.md", "symdesk", "symroom"])

    def test_version_check_runs_host_binaries(self):
        host = package.host_target()
        if host not in package.TARGETS:
            self.skipTest("host is outside the release scope")
        path = self.build(*host)
        self.assertEqual(package.verify_archive(path, "1.2.3", run=True), [])
        self.bins["symroom"].write_text("#!/bin/sh\necho 'symroom 0.0.0'\n")
        bad = self.build(*host, out="bad")
        self.assertTrue(any("symroom version" in e for e in package.verify_archive(bad, "1.2.3", run=True)))

    def rewrite(self, path: Path, mutate) -> Path:
        with tarfile.open(path) as tar:
            members = [(m, tar.extractfile(m).read()) for m in tar.getmembers()]  # type: ignore[union-attr]
        members = mutate(members)
        out = self.dir / "mutated" / path.name
        out.parent.mkdir(exist_ok=True)
        with tarfile.open(out, "w:gz") as tar:
            for info, data in members:
                tar.addfile(info, io.BytesIO(data))
        return out

    def test_violations_fail(self):
        good = self.build()

        def extra(members):
            info = tarfile.TarInfo("symdesk-go")
            info.size, info.mode = 1, 0o755
            return members + [(info, b"x")]

        def missing(members):
            return [m for m in members if m[0].name != "VAULT.md"]

        def mode(members):
            for info, _ in members:
                if info.name == "symdesk":
                    info.mode = 0o644
            return members

        for mutate, needle in ((extra, "members"), (missing, "members"), (mode, "mode")):
            errors = package.verify_archive(self.rewrite(good, mutate), "1.2.3", run=False)
            self.assertTrue(any(needle in e for e in errors), (mutate.__name__, errors))
        renamed = good.with_name("symaira-desktop_1.2.4_linux_arm64.tar.gz")
        good.rename(renamed)
        self.assertTrue(package.verify_archive(renamed, "1.2.3", run=False))
        intel = self.dir / "symaira-desktop_1.2.3_darwin_amd64.tar.gz"
        renamed.rename(intel)
        self.assertTrue(any("platform scope" in e for e in package.verify_archive(intel, "1.2.3", run=False)))

    def test_checksums_and_formula(self):
        out = self.dir / "dist"
        for target in package.TARGETS:
            package.build_archive(ROOT, "1.2.3", *target, self.bins, out, 0)
        sums = package.write_checksums(out)
        lines = sums.read_text().splitlines()
        self.assertEqual(len(lines), 3)
        self.assertEqual([l.split("  ")[1] for l in lines], sorted(l.split("  ")[1] for l in lines))
        formula = package.render_formula("1.2.3", sums)
        for line in lines:
            digest, name = line.split("  ")
            self.assertIn(f'sha256 "{digest}"', formula)
            self.assertIn(f"/releases/download/v1.2.3/{name}", formula)
        self.assertNotIn("darwin_amd64", formula)
        self.assertIn('bin.install "symroom"', formula)


if __name__ == "__main__":
    unittest.main()
