import importlib.util
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("dataset-rollback.py")
SPEC = importlib.util.spec_from_file_location("dataset_rollback", SCRIPT)
dataset_rollback = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(dataset_rollback)


class DatasetRollbackTests(unittest.TestCase):
    def test_dataset_identities_reads_query_rows(self):
        self.assertEqual(
            dataset_rollback.dataset_identities(
                '{"dataset":"rollback","rows":[{"identity":"rust-seed"},{"identity":"go-write"}]}'
            ),
            {"rust-seed", "go-write"},
        )

    def test_dataset_identities_rejects_invalid_results(self):
        for output in ('{', '{"rows":[{}]}', '{"rows":null}'):
            with self.subTest(output=output), self.assertRaises(RuntimeError):
                dataset_rollback.dataset_identities(output)


if __name__ == "__main__":
    unittest.main()
