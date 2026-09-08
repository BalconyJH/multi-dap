import os
import tempfile
import unittest


LIB = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib")
if LIB not in os.sys.path:
    os.sys.path.insert(0, LIB)

import reconlib


class M2BreakpointRecoveryJournalTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.original_out = reconlib.OUT_DIR
        self.original_journal = reconlib.M2_BREAKPOINT_RECOVERY_JOURNAL
        reconlib.OUT_DIR = self.directory.name
        reconlib.M2_BREAKPOINT_RECOVERY_JOURNAL = os.path.join(
            self.directory.name, "recovery.json")

    def tearDown(self):
        reconlib.OUT_DIR = self.original_out
        reconlib.M2_BREAKPOINT_RECOVERY_JOURNAL = self.original_journal
        self.directory.cleanup()

    def test_round_trip_requires_exact_same_core_and_token_to_clear(self):
        token = 'mprintf("HIT 0x01020304\\n")'

        reconlib.write_m2_breakpoint_recovery_journal(1, token)

        self.assertEqual(reconlib.read_m2_breakpoint_recovery_journal(), {
            "core": 1,
            "token": token,
        })
        with self.assertRaises(RuntimeError):
            reconlib.clear_m2_breakpoint_recovery_journal(0, token)
        self.assertTrue(reconlib.clear_m2_breakpoint_recovery_journal(1, token))
        self.assertIsNone(reconlib.read_m2_breakpoint_recovery_journal())

    def test_existing_journal_cannot_be_replaced(self):
        reconlib.write_m2_breakpoint_recovery_journal(
            1, 'mprintf("HIT 0x01020304\\n")')

        with self.assertRaises(RuntimeError):
            reconlib.write_m2_breakpoint_recovery_journal(
                1, 'mprintf("HIT 0x01020305\\n")')

    def test_rejects_non_production_token_shape(self):
        with self.assertRaises(ValueError):
            reconlib.write_m2_breakpoint_recovery_journal(
                1, 'mprintf("OTHER 0x01020304\\n")')


if __name__ == "__main__":
    unittest.main()
