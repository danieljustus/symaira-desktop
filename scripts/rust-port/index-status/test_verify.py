#!/usr/bin/env python3
"""Unit controls for the native gate's comparator report contract."""
import unittest

from compare import REQUIRED_CASE_IDS
from verify import native_case_count


class NativeReportContract(unittest.TestCase):
    def test_only_complete_clean_native_comparisons_are_accepted(self):
        valid = {"pass": True, "clean_acceptance": True,
                 "compared_cases": len(REQUIRED_CASE_IDS)}
        self.assertEqual(native_case_count(valid), len(REQUIRED_CASE_IDS))
        for invalid in (
            {"pass": True, "clean_acceptance": True, "case_count": len(REQUIRED_CASE_IDS)},
            {**valid, "pass": False},
            {**valid, "clean_acceptance": False},
            {**valid, "compared_cases": len(REQUIRED_CASE_IDS) - 1},
            {**valid, "compared_cases": str(len(REQUIRED_CASE_IDS))},
            {},
        ):
            with self.subTest(report=invalid), self.assertRaises(RuntimeError):
                native_case_count(invalid)


if __name__ == "__main__":
    unittest.main()
