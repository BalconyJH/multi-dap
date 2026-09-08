import importlib.util
import os
import unittest


LIB = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib")
if LIB not in os.sys.path:
    os.sys.path.insert(0, LIB)

import reconlib


def load_probe():
    path = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                        "probes", "p09_m2_program_breakpoint.py")
    spec = importlib.util.spec_from_file_location("p09_probe_test", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class PairedBreakpointRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.probe = load_probe()
        self.token = 'mprintf("HIT 0x01020304\\n")'
        self.frame = reconlib.TopFrame("C:/fixture/main.c", 137, True)
        self.record = reconlib.BreakpointRecord(
            7, self.frame.source, self.frame.line, self.token)
        self.events = []
        self.probe.fresh_token = lambda: self.token
        self.probe.reconlib.revalidate_strict_program_topology = (
            lambda session, topology: topology)
        self.probe.reconlib.write_m2_breakpoint_recovery_journal = (
            lambda core, token: None)
        self.probe.reconlib.clear_m2_breakpoint_recovery_journal = (
            lambda core, token: False)

    def test_after_b_listing_failure_still_recovers_exact_token(self):
        listings = [
            reconlib.BreakpointSnapshot([]),
            RuntimeError("after-b B failed"),
            reconlib.BreakpointSnapshot([self.record]),
            reconlib.BreakpointSnapshot([]),
        ]

        def list_breakpoints(unused_session, unused_topology, unused_core):
            value = listings.pop(0)
            if isinstance(value, Exception):
                raise value
            return value

        def strict_result(unused_session, unused_topology, unused_core, command):
            self.events.append(command)
            return {"accepted": True, "status": 1, "raw": ""}

        self.probe.list_breakpoints = list_breakpoints
        self.probe.reconlib.strict_routed_result = strict_result
        with self.assertRaises(RuntimeError):
            self.probe.paired_breakpoint(object(), object(), 0, self.frame)

        self.assertEqual(len(listings), 0)
        self.assertTrue(any(command.startswith("b ") for command in self.events))
        self.assertEqual([command for command in self.events if command.startswith("d ")],
                         ["d %7"])

    def test_known_zero_handle_uses_explicit_breakpoint_prefix(self):
        record = reconlib.BreakpointRecord(
            0, self.frame.source, self.frame.line, self.token)
        snapshots = [reconlib.BreakpointSnapshot([record]),
                     reconlib.BreakpointSnapshot([])]
        self.probe.list_breakpoints = (
            lambda unused_session, unused_topology, unused_core:
            snapshots.pop(0))
        self.probe.reconlib.strict_routed_result = (
            lambda unused_session, unused_topology, unused_core, command:
            self.events.append(command) or {"accepted": True, "status": 1, "raw": ""})

        outcome = self.probe.delete_owned_breakpoint(
            object(), object(), 0,
            reconlib.BreakpointMutation(record, self.frame.source,
                                        self.frame.line, self.token))

        self.assertTrue(outcome["cleanup_confirmed"])
        self.assertEqual(self.events, ["d %0"])

    def test_known_handle_drift_with_same_token_issues_no_delete(self):
        moved = reconlib.BreakpointRecord(
            8, self.frame.source, self.frame.line, self.token)
        self.probe.list_breakpoints = (
            lambda unused_session, unused_topology, unused_core:
            reconlib.BreakpointSnapshot([moved]))
        self.probe.reconlib.strict_routed_result = (
            lambda unused_session, unused_topology, unused_core, command:
            self.events.append(command) or {"accepted": True, "status": 1, "raw": ""})

        with self.assertRaises(RuntimeError):
            self.probe.delete_owned_breakpoint(
                object(), object(), 0,
                reconlib.BreakpointMutation(
                    self.record, self.frame.source, self.frame.line, self.token))

        self.assertEqual(self.events, [])

    def test_persistent_delete_failure_is_fatal_and_never_reports_clean(self):
        listings = [
            reconlib.BreakpointSnapshot([]),
            RuntimeError("after-b B failed"),
            reconlib.BreakpointSnapshot([self.record]),
        ]

        def list_breakpoints(unused_session, unused_topology, unused_core):
            value = listings.pop(0)
            if isinstance(value, Exception):
                raise value
            return value

        def strict_result(unused_session, unused_topology, unused_core, command):
            if command.startswith("d "):
                return {"accepted": False, "status": 0, "raw": ""}
            return {"accepted": True, "status": 1, "raw": ""}

        self.probe.list_breakpoints = list_breakpoints
        self.probe.reconlib.strict_routed_result = strict_result
        recorder = reconlib.Recorder("unit_p09_persistent_delete")
        recorder._write = lambda: None
        result = recorder.step("paired", lambda: self.probe.paired_breakpoint(
            object(), object(), 0, self.frame))
        if result is None:
            try:
                raise RuntimeError("paired cleanup failed")
            except RuntimeError:
                recorder.fatal()

        self.assertIsNone(result)
        self.assertEqual(recorder.data["steps"][0]["error_kind"], "runtime_error")
        self.assertEqual(recorder.data["fatal"], "runtime_error")
        self.assertNotIn("cleanup_confirmed", recorder.data["steps"][0])

    def test_persistent_listing_failure_is_fatal_and_never_reports_clean(self):
        calls = [0]

        def list_breakpoints(unused_session, unused_topology, unused_core):
            calls[0] += 1
            if calls[0] == 1:
                return reconlib.BreakpointSnapshot([])
            raise RuntimeError("persistent B failure")

        self.probe.list_breakpoints = list_breakpoints
        self.probe.reconlib.strict_routed_result = (
            lambda unused_session, unused_topology, unused_core, command:
            {"accepted": True, "status": 1, "raw": ""})
        recorder = reconlib.Recorder("unit_p09_persistent_listing")
        recorder._write = lambda: None
        result = recorder.step("paired", lambda: self.probe.paired_breakpoint(
            object(), object(), 0, self.frame))
        if result is None:
            try:
                raise RuntimeError("paired cleanup failed")
            except RuntimeError:
                recorder.fatal()

        self.assertIsNone(result)
        self.assertGreaterEqual(calls[0], 3)
        self.assertEqual(recorder.data["fatal"], "runtime_error")
        self.assertNotIn("cleanup_confirmed", recorder.data["steps"][0])
