import importlib.util
import hashlib
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("dataset-rollback.py")
SPEC = importlib.util.spec_from_file_location("dataset_rollback", SCRIPT)
dataset_rollback = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(dataset_rollback)


class DatasetRollbackTests(unittest.TestCase):
    def test_dataset_rows_reads_full_query_projection(self):
        self.assertEqual(
            dataset_rollback.dataset_rows(
                '{"dataset":"rollback","rows":[{"identity":"rust-seed","event_id":"rust-seed","amount":1},{"identity":"go-write","event_id":"go-write","amount":2}]}'
            ),
            {"rust-seed": {"event_id": "rust-seed", "amount": 1},
             "go-write": {"event_id": "go-write", "amount": 2}},
        )

    def test_dataset_rows_rejects_invalid_results(self):
        for output in ('{', '{"rows":[{}]}', '{"rows":null}',
                       '{"rows":[{"identity":"one","event_id":"different","amount":1}]}'):
            with self.subTest(output=output), self.assertRaises(RuntimeError):
                dataset_rollback.dataset_rows(output)

    def test_dataset_handle_checks_persisted_fields(self):
        document = ('{"slug":"rollback","path":"datasets/rollback.md","rows":2,'
                    '"identity_field":"event_id","provenance":'
                    '{"source_name":"go-write","source_sha256":"go-write-v1"}}')
        dataset_rollback.dataset_handle(document, 2, "go-write")
        with self.assertRaisesRegex(RuntimeError, "dataset handle fields"):
            dataset_rollback.dataset_handle(document, 3, "go-write")

    def test_source_bound_report_requires_source_and_binary_sha256(self):
        report = {
            "sources": {
                name: {"commit": "a" * 40, "sha256": "b" * 64}
                for name in ("rust", "go_fallback")
            },
            "binaries": {
                "provenance": "built from the reported source commits",
                **{name: {"sha256": "c" * 64} for name in ("rust", "go_fallback")},
            },
            "source_binding": "enforced",
        }
        dataset_rollback.validate_report(report)
        report["sources"]["rust"]["sha256"] = "short"
        with self.assertRaisesRegex(RuntimeError, "source commit/SHA-256"):
            dataset_rollback.validate_report(report)

    def test_caller_binaries_cannot_claim_source_binding(self):
        report = {
            "sources": {
                name: {"commit": "a" * 40}
                for name in ("rust", "go_fallback")
            },
            "binaries": {
                "provenance": "caller-provided binaries; source refs are reported, not enforced",
                **{name: {"sha256": "c" * 64} for name in ("rust", "go_fallback")},
            },
            "source_binding": "declared-only",
        }
        dataset_rollback.validate_report(report)
        report["source_binding"] = "enforced"
        with self.assertRaisesRegex(RuntimeError, "claims source binding"):
            dataset_rollback.validate_report(report)

    def test_sha256_reports_the_actual_binary_or_source_bytes(self):
        with tempfile.NamedTemporaryFile() as artifact:
            artifact.write(b"source or binary bytes")
            artifact.flush()
            self.assertEqual(
                dataset_rollback.sha256(Path(artifact.name)),
                hashlib.sha256(b"source or binary bytes").hexdigest(),
            )

    def test_source_tree_sha256_covers_relative_paths_and_contents(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "nested").mkdir()
            (root / "nested" / "source.txt").write_text("source")
            original = dataset_rollback.source_tree_sha256(root)
            (root / "nested" / "source.txt").write_text("changed")
            self.assertNotEqual(original, dataset_rollback.source_tree_sha256(root))


if __name__ == "__main__":
    unittest.main()
