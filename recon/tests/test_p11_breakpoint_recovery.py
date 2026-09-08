import importlib.util
import os
import unittest


LIB = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib")
if LIB not in os.sys.path:
    os.sys.path.insert(0, LIB)

import reconlib


def load_probe():
    path = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                        "probes", "p11_m2_owned_breakpoint_recovery.py")
    spec = importlib.util.spec_from_file_location("p11_probe_test", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ExactProductionTokenTests(unittest.TestCase):
    def setUp(self):
        self.probe = load_probe()
        self.original_strict_result = self.probe.reconlib.strict_routed_result
        self.original_revalidate = (
            self.probe.reconlib.revalidate_strict_program_topology)
        self.original_require_stopped = self.probe.reconlib.require_stopped_session

    def tearDown(self):
        self.probe.reconlib.strict_routed_result = self.original_strict_result
        self.probe.reconlib.revalidate_strict_program_topology = (
            self.original_revalidate)
        self.probe.reconlib.require_stopped_session = self.original_require_stopped

    def test_matches_only_exact_uppercase_multidap_hint_shape(self):
        records = [
            reconlib.BreakpointRecord(0, "C:/fixture/main.c", 1,
                                      'mprintf("HIT 0x01020304\\n")'),
            reconlib.BreakpointRecord(1, "C:/fixture/main.c", 2,
                                      'mprintf("HIT 0x0102030a\\n")'),
            reconlib.BreakpointRecord(2, "C:/fixture/main.c", 3,
                                      'mprintf("HIT 0x01020304\\r\\n")'),
            reconlib.BreakpointRecord(3, "C:/fixture/main.c", 4,
                                      'mprintf("OTHER 0x01020304\\n")'),
        ]

        matches = self.probe.exact_production_records(records)

        self.assertEqual([record.handle for record in matches], [0])

    def test_disabled_configuration_does_not_authorize_recovery(self):
        self.assertIsNone(self.probe.approved_section({
            "m2_owned_breakpoint_recovery_probe": {"enabled": False},
        }))

    def test_legacy_recovery_requires_exact_expected_core_count_before_delete(self):
        token = 'mprintf("HIT 0x01020304\\n")'
        topology = type("Topology", (), {"component_by_core": {0: "a", 1: "b"}})()
        snapshots = [
            reconlib.BreakpointSnapshot([
                reconlib.BreakpointRecord(0, "C:/fixture/main.c", 1, token)]),
            reconlib.BreakpointSnapshot([
                reconlib.BreakpointRecord(0, "C:/fixture/main.c", 1, token)]),
            reconlib.BreakpointSnapshot([]),
            reconlib.BreakpointSnapshot([]),
        ]
        events = []
        self.probe.list_breakpoints = (
            lambda unused_session, unused_topology, unused_core: snapshots.pop(0))
        self.probe.reconlib.strict_routed_result = (
            lambda unused_session, unused_topology, unused_core, command:
            events.append(command) or {"accepted": True, "status": 1, "raw": ""})
        self.probe.reconlib.revalidate_strict_program_topology = (
            lambda unused_session, unused_topology: unused_topology)
        self.probe.reconlib.require_stopped_session = lambda unused_session: {}

        outcome = self.probe.recover_legacy_token(object(), topology, {
            "expected_configured_core_ordinal": 1,
            "expected_exact_token_count": 1,
        })

        self.assertTrue(outcome["cleanup_confirmed"])
        self.assertEqual(events, ["d %0"])
        self.assertEqual(snapshots, [])

    def test_legacy_count_mismatch_issues_zero_delete_commands(self):
        topology = type("Topology", (), {"component_by_core": {0: "a", 1: "b"}})()
        events = []
        self.probe.list_breakpoints = (
            lambda unused_session, unused_topology, unused_core:
            reconlib.BreakpointSnapshot([]))
        self.probe.reconlib.strict_routed_result = (
            lambda unused_session, unused_topology, unused_core, command:
            events.append(command) or {"accepted": True, "status": 1, "raw": ""})

        with self.assertRaises(RuntimeError):
            self.probe.recover_legacy_token(object(), topology, {
                "expected_configured_core_ordinal": 1,
                "expected_exact_token_count": 1,
            })

        self.assertEqual(events, [])


if __name__ == "__main__":
    unittest.main()
