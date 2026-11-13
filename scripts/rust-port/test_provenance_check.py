#!/usr/bin/env python3
"""Tests for the Go-free fixture provenance check, with negative controls."""
import copy
import json
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parent))
import provenance_check as pc  # noqa: E402

DOC = json.loads(pc.PROVENANCE.read_text())


def tree_with(doc, files):
    root = Path(tempfile.mkdtemp(prefix="provenance-"))
    for rel in files:
        target = root / rel
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(pc.ROOT / rel, target)
    return root


class ProvenanceCheck(unittest.TestCase):
    def test_committed_provenance_passes(self):
        self.assertEqual(pc.check_identity(DOC), [])
        self.assertEqual(pc.check_bundle(DOC), [])
        self.assertEqual(pc.check_checksums(DOC, pc.ROOT), [])
        self.assertGreaterEqual(len(DOC["fixture_checksums"]), 200)

    def test_main_exits_zero_on_the_committed_tree(self):
        self.assertEqual(pc.main([]), 0)

    def test_altered_fixture_byte_is_rejected(self):
        rel = sorted(DOC["fixture_checksums"])[0]
        root = tree_with(DOC, [rel])
        self.addCleanup(shutil.rmtree, root)
        with (root / rel).open("ab") as handle:
            handle.write(b" ")
        errors = pc.check_checksums({"fixture_checksums": {rel: DOC["fixture_checksums"][rel]}}, root)
        self.assertEqual(len(errors), 1)
        self.assertIn("differs", errors[0])

    def test_missing_fixture_is_rejected(self):
        doc = {"fixture_checksums": {"testdata/port/none.json": "0" * 64}}
        errors = pc.check_checksums(doc, pc.ROOT)
        self.assertEqual(errors, ["testdata/port/none.json: fixture is missing"])

    def test_malformed_checksum_is_rejected(self):
        rel = sorted(DOC["fixture_checksums"])[0]
        errors = pc.check_checksums({"fixture_checksums": {rel: "ABC"}}, pc.ROOT)
        self.assertIn("64 lowercase hex", errors[0])

    def test_empty_checksum_map_is_rejected(self):
        self.assertEqual(pc.check_checksums({"fixture_checksums": {}}, pc.ROOT),
                         ["fixture_checksums is empty"])

    def test_identity_rejects_short_commit_and_blank_release(self):
        doc = copy.deepcopy(DOC)
        doc["oracle"] = {"commit": "abc", "release": " "}
        errors = pc.check_identity(doc)
        self.assertEqual(len(errors), 2)

    def test_identity_rejects_bad_source_digest(self):
        doc = copy.deepcopy(DOC)
        doc["production_source_digest"] = "Z" * 64
        self.assertIn("production_source_digest", pc.check_identity(doc)[0])

    def test_bundle_tampering_is_rejected(self):
        doc = copy.deepcopy(DOC)
        doc["oracle_bundle"]["sha256"] = "0" * 64
        self.assertIn("sha256", pc.check_bundle(doc)[0])
        doc = copy.deepcopy(DOC)
        doc["oracle_bundle"]["anchor"] = "1" * 40
        self.assertTrue(pc.check_bundle(doc))

    def test_fixture_sources_finds_recorded_hashes(self):
        kinds = {kind for _, kind, _ in pc.fixture_sources(pc.ROOT)}
        self.assertEqual(kinds, {"map", "single"})

    def test_history_missing_commit_fails_closed(self):
        root = Path(tempfile.mkdtemp(prefix="provenance-nohistory-"))
        self.addCleanup(shutil.rmtree, root)
        errors, checked = pc.check_history(root)
        self.assertEqual(checked, 0)
        self.assertIn("not in this clone", errors[0])


if __name__ == "__main__":
    unittest.main()
