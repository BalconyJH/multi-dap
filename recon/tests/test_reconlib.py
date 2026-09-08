import json
import os
import sys
import tempfile
import types
import unittest
import importlib.util


LIB = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib")
if LIB not in sys.path:
    sys.path.insert(0, LIB)

import reconlib


def load_p14_module():
    path = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                        "probes", "p14_execution_domain_experiment.py")
    spec = importlib.util.spec_from_file_location("p14_probe_test", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class RecorderSafetyTests(unittest.TestCase):
    def new_recorder(self):
        recorder = reconlib.Recorder("unit_recorder")
        recorder._write = lambda: None
        return recorder

    def test_step_records_only_fixed_exception_category(self):
        recorder = self.new_recorder()
        sensitive = "private target path and connection arguments"

        def fail():
            raise RuntimeError(sensitive)

        self.assertIsNone(recorder.step("failure", fail))

        entry = recorder.data["steps"][0]
        self.assertEqual(entry["error_kind"], "runtime_error")
        serialized = json.dumps(recorder.data)
        self.assertNotIn(sensitive, serialized)
        self.assertNotIn("Traceback", serialized)

    def test_fatal_records_only_fixed_exception_category(self):
        recorder = self.new_recorder()
        sensitive = "private expression operand"

        try:
            raise KeyError(sensitive)
        except KeyError:
            recorder.fatal()

        self.assertEqual(recorder.data["fatal"], "lookup_error")
        self.assertNotIn(sensitive, json.dumps(recorder.data))

    def test_local_config_is_usable_in_memory_but_redacted_everywhere(self):
        sensitive = "C:/private-board/target.elf --connection-secret"
        cfg = reconlib.LocalConfig({
            "primary_elf": sensitive,
            "connection_args": sensitive,
        })
        recorder = self.new_recorder()

        self.assertIs(recorder.step("load_config", lambda: cfg), cfg)
        recorder.note("nested_config", {"config": cfg})
        recorder.step("metadata", lambda: True, config=cfg)

        self.assertEqual(cfg["primary_elf"], sensitive)
        serialized = json.dumps(recorder.data, sort_keys=True)
        self.assertNotIn(sensitive, serialized)
        expected = {"kind": "local_configuration", "redacted": True}
        self.assertEqual(recorder.data["steps"][0]["result"], expected)
        self.assertEqual(recorder.data["notes"]["nested_config"]["config"], expected)
        self.assertEqual(recorder.data["steps"][1]["meta"]["config"], expected)

    def test_load_config_marks_decoded_local_data_as_protected(self):
        sensitive = "C:/private-board/device.dvf --connection-secret"
        original_recon_dir = reconlib.RECON_DIR
        with tempfile.TemporaryDirectory() as tempdir:
            try:
                with open(os.path.join(tempdir, "config.local.json"), "w") as fh:
                    json.dump({"connection_args": sensitive}, fh)
                reconlib.RECON_DIR = tempdir
                cfg = reconlib.load_config()
            finally:
                reconlib.RECON_DIR = original_recon_dir

        recorder = self.new_recorder()
        self.assertIs(recorder.step("load_config", lambda: cfg), cfg)
        self.assertEqual(cfg["connection_args"], sensitive)
        self.assertNotIn(sensitive, json.dumps(recorder.data))

    def test_write_boundary_redacts_direct_recorder_data_mutation(self):
        sensitive = "C:/private-board/target.elf --connection-secret"
        original_out_dir = reconlib.OUT_DIR
        with tempfile.TemporaryDirectory() as tempdir:
            try:
                reconlib.OUT_DIR = tempdir
                recorder = reconlib.Recorder("unit_recorder")
                recorder.data["notes"]["unsafe_direct_write"] = reconlib.LocalConfig({
                    "connection_args": sensitive,
                })
                path = recorder.finish()
                with open(path, "r") as fh:
                    evidence = fh.read()
            finally:
                reconlib.OUT_DIR = original_out_dir

        self.assertNotIn(sensitive, evidence)
        self.assertIn('"local_configuration"', evidence)


class FakeWindow(object):
    def __init__(self, program, status=2, info=None):
        self.program = program
        self.status = status
        self.status_stopped = 2
        self.info = {"pid": "1"} if info is None else info
        self.calls = []

    def GetProgram(self):
        self.calls.append("GetProgram")
        return self.program

    def GetStatus(self):
        self.calls.append("GetStatus")
        return self.status

    def GetCurPrInfo(self, options):
        self.calls.append(("GetCurPrInfo", options))
        return self.info

    def Halt(self, block, print_output):
        self.calls.append(("Halt", block, print_output))
        self.status = self.status_stopped


class CallableTarget(object):
    def __init__(self):
        self.calls = []

    def StepOut(self, block, print_output):
        self.calls.append(("StepOut", block, print_output))
        return 0x1234

    def unrelated(self):
        raise AssertionError("inventory must not invoke callables")


class ExecutionWindowTarget(object):
    def __init__(self):
        self.calls = []

    def GetStatus(self):
        self.calls.append("GetStatus")

    def GetProgram(self):
        self.calls.append("GetProgram")

    def Halt(self, block, print_output):
        self.calls.append("Halt")

    def Resume(self, block, print_output):
        self.calls.append("Resume")

    def Next(self, block, print_output, step_into):
        self.calls.append("Next")

    def Step(self, block, print_output, step_into):
        self.calls.append("Step")


class SourceLookupTarget(object):
    def __init__(self):
        self.operands = []

    def CheckSymbol(self, operand):
        self.operands.append(operand)
        return operand == "known-symbol"


class FakeDebugger(object):
    def __init__(self):
        self.cold_calls = []

    def DebugProgram(self, *args):
        self.cold_calls.append(("DebugProgram", args))
        return object()

    def ConnectToTarget(self, *args):
        self.cold_calls.append(("ConnectToTarget", args))
        return object()


class FakeRegister(object):
    windows = []

    def __init__(self):
        self.service = object()

    def GetWindowList(self, show_it):
        self.show_it = show_it
        return {"winInfo": []}

    def CheckWindows(self, name, winClass=None, fromWinList=None):
        self.arguments = (name, winClass, fromWinList)
        return list(self.windows)


class WarmBindingTests(unittest.TestCase):
    def setUp(self):
        self.old_factory = reconlib._new_debugger
        self.old_modules = dict((name, sys.modules.get(name))
                                for name in ("ghs_constants", "ghs_winreg"))
        self.debugger = FakeDebugger()
        reconlib._new_debugger = lambda: self.debugger

        constants = types.ModuleType("ghs_constants")
        constants.winClassNames = type("Classes", (), {"debugger": "debugger"})()
        winreg = types.ModuleType("ghs_winreg")
        winreg.GHS_WindowRegister = FakeRegister
        sys.modules["ghs_constants"] = constants
        sys.modules["ghs_winreg"] = winreg

    def tearDown(self):
        reconlib._new_debugger = self.old_factory
        for name, previous in self.old_modules.items():
            if previous is None:
                del sys.modules[name]
            else:
                sys.modules[name] = previous

    def test_warm_binding_requires_one_exact_active_program_window(self):
        expected = "C:/project/primary.elf"
        matching = FakeWindow("c:\\project\\primary.elf")
        other = FakeWindow("C:/project/secondary.elf")
        FakeRegister.windows = [other, matching]

        session = reconlib.bind_existing_program_window({"primary_elf": expected})

        self.assertIs(session.window, matching)
        self.assertEqual(session.origin, "warm")
        self.assertFalse(session.disconnect_on_close)
        self.assertEqual(self.debugger.cold_calls, [])
        self.assertEqual(other.calls, ["GetProgram"])
        self.assertIn("GetStatus", matching.calls)
        self.assertIn(("GetCurPrInfo", ""), matching.calls)

    def test_warm_binding_rejects_ambiguous_or_nonmatching_windows(self):
        expected = "C:/project/primary.elf"
        FakeRegister.windows = [FakeWindow(expected), FakeWindow(expected)]
        with self.assertRaises(reconlib.WarmBindError):
            reconlib.bind_existing_program_window({"primary_elf": expected})

        FakeRegister.windows = [FakeWindow("C:/project/other.elf")]
        with self.assertRaises(reconlib.WarmBindError):
            reconlib.bind_existing_program_window({"primary_elf": expected})
        self.assertEqual(self.debugger.cold_calls, [])

    def test_core_warm_binding_uses_its_own_exact_elf(self):
        primary = FakeWindow("C:/project/primary.elf")
        secondary = FakeWindow("C:/project/secondary.elf")
        FakeRegister.windows = [primary, secondary]

        session = reconlib.bind_existing_core_program_window(
            {"primary_elf": "C:/project/primary.elf"},
            {"id": 1, "elf": "C:/project/secondary.elf"})

        self.assertIs(session.window, secondary)
        self.assertEqual(primary.calls, ["GetProgram"])
        self.assertIn("GetStatus", secondary.calls)
        self.assertEqual(self.debugger.cold_calls, [])

    def test_warm_summary_is_aggregate_and_uses_binding_filters(self):
        expected = "C:/project/primary.elf"
        FakeRegister.windows = [
            FakeWindow(""),
            FakeWindow("C:/project/secondary.elf"),
            FakeWindow(expected, status=2, info={}),
            FakeWindow(expected, status=4),
            FakeWindow(expected, status=3, info={"pid": "1"}),
        ]

        summary = reconlib.warm_binding_summary({"primary_elf": expected})

        self.assertEqual(summary, {
            "registered_debugger_windows": 5,
            "get_program_readable": 4,
            "exact_primary_elf_matches": 3,
            "exact_multicore_project_matches": 0,
            "stable_status_matches": 2,
            "nonempty_process_info_matches": 1,
        })
        self.assertEqual(self.debugger.cold_calls, [])

    def test_warm_summary_counts_project_and_primary_without_rebinding(self):
        primary = "C:/project/primary.elf"
        project = "C:/project/target.ghsmc"
        project_window = FakeWindow("c:\\project\\target.ghsmc")
        primary_window = FakeWindow("c:\\project\\primary.elf")
        FakeRegister.windows = [project_window, primary_window]

        cfg = {"primary_elf": primary, "multicore_project": project}
        summary = reconlib.warm_binding_summary(cfg)
        session = reconlib.bind_existing_program_window(cfg)

        self.assertEqual(summary, {
            "registered_debugger_windows": 2,
            "get_program_readable": 2,
            "exact_primary_elf_matches": 1,
            "exact_multicore_project_matches": 1,
            "stable_status_matches": 1,
            "nonempty_process_info_matches": 1,
        })
        self.assertIs(session.window, primary_window)
        self.assertEqual(project_window.calls, ["GetProgram", "GetProgram"])
        self.assertEqual(self.debugger.cold_calls, [])

    def test_cold_session_is_the_only_path_that_connects(self):
        session = reconlib.open_cold_session({
            "multicore_project": "C:/project/config.ghsmc",
            "connection_args": "server arguments",
        })
        self.assertEqual(session.origin, "cold")
        self.assertTrue(session.disconnect_on_close)
        self.assertEqual([call[0] for call in self.debugger.cold_calls],
                         ["DebugProgram", "ConnectToTarget"])


class ProbeSafetyTests(unittest.TestCase):
    def test_inventory_describes_a_callable_without_invoking_it(self):
        target = CallableTarget()

        inventory = reconlib.callable_inventory(target, ("step",))

        self.assertEqual(inventory["StepOut"], {
            "readable": True,
            "args": ["self", "block", "print_output"],
            "varargs": False,
            "keywords": False,
            "defaults_count": 0,
        })
        self.assertEqual(target.calls, [])

    def test_execution_window_inventory_is_exact_and_noninvoking(self):
        target = ExecutionWindowTarget()

        inventory = reconlib.execution_window_api_inventory(target)

        self.assertEqual(sorted(inventory), [
            "GetProgram", "GetStatus", "Halt", "Next", "Resume", "Step"])
        self.assertTrue(inventory["Halt"]["callable"])
        self.assertEqual(inventory["Next"]["args"],
                         ["self", "block", "print_output", "step_into"])
        self.assertEqual(target.calls, [])

    def test_execution_domain_syntax_candidates_are_fixed_documented_grammar(self):
        self.assertEqual(reconlib.execution_domain_syntax_candidates(), [
            ("halt", "halt"),
            ("continue", "c"),
            ("step_in_source", "sl n"),
            ("next_source", "nl n"),
        ])

    def test_configured_api_call_records_shape_not_target_value(self):
        target = CallableTarget()

        result = reconlib.configured_api_call(
            target, {"method": "StepOut", "args": [0, 0]})

        self.assertEqual(target.calls, [("StepOut", 0, 0)])
        self.assertEqual(result, {
            "method": "StepOut",
            "argument_count": 2,
            "result_type": "int",
            "result_is_none": False,
        })
        self.assertNotIn("4660", repr(result))

    def test_configured_unary_api_call_redacts_operand_and_result_value(self):
        target = SourceLookupTarget()

        result = reconlib.configured_unary_api_call(
            target, "CheckSymbol", "known-symbol")

        self.assertEqual(target.operands, ["known-symbol"])
        self.assertEqual(result, {
            "method": "CheckSymbol",
            "argument_count": 1,
            "accepted": True,
            "status": "boolean_true",
            "result": {"type": "bool", "is_none": False},
        })
        self.assertNotIn("known-symbol", repr(result))

    def test_execution_preflight_and_recovery_fail_closed(self):
        window = FakeWindow("C:/project/primary.elf", status=3)
        session = reconlib.Session(None, window, False, "warm")

        with self.assertRaises(RuntimeError):
            reconlib.require_stopped_session(session)

        recovery = reconlib.recover_stopped_session(session)
        self.assertEqual(recovery, {
            "status_before_recovery": 3,
            "status_after_recovery": 2,
        })
        self.assertEqual(reconlib.require_stopped_session(session)["status"], 2)

    def test_preflight_rejects_empty_process_information(self):
        window = FakeWindow("C:/project/primary.elf", info={})
        session = reconlib.Session(None, window, False, "warm")

        with self.assertRaises(RuntimeError):
            reconlib.require_stopped_session(session)


class RoutedProcessTests(unittest.TestCase):
    def test_discovers_component_aliases_without_component_row_order(self):
        components = "\n".join((
            "The currently registered components are:",
            "fixture.core_b (debugger.pid.5)",
            "fixture.program (debugger.name.C:/fixture/app.elf)",
            "fixture.core_a (debugger.pid.1)",
        ))

        inventory = reconlib.component_route_inventory(components)
        self.assertEqual(inventory, {
            "route_pids": [1, 5],
            "component_row_count": 3,
            "route_count": 2,
            "program_component_count": 1,
            "program_aliases": [{
                "component_id": "fixture.program",
                "program_name": "c:\\fixture\\app.elf",
            }],
            "program_alias_route_shared_component_count": 0,
        })

    def test_summarizes_selected_process_against_route_pid(self):
        listing = "\n".join((
            "     # PID        PPID       Status        CBEFITDHR Name and Arguments",
            "     0 0x2a       N/A        Stopped       011111101 C:/fixture/app.elf",
            ">>   4 0x2b       N/A        No Process    011111101",
        ))

        summary = reconlib.summarize_routed_processes(0x2b, listing)

        self.assertEqual(summary, {
            "row_count": 2,
            "selected_row_count": 1,
            "selected_row_ordinal": 1,
            "selected_pid_matches_route": True,
            "selected_status": "no_process",
            "selected_program_present": False,
        })

    def test_refuses_inconclusive_or_malformed_process_listing(self):
        wrong_slot = ">>   1 0x2a N/A No Process 011111101\n"
        summary = reconlib.summarize_routed_processes(5, wrong_slot)
        self.assertFalse(summary["selected_pid_matches_route"])

        with self.assertRaises(ValueError):
            reconlib.summarize_routed_processes(1, "not a process row\n")

    def test_route_pid_mapping_does_not_assign_core_ids(self):
        complete = reconlib.summarize_route_pid_mapping([
            {"selected_pid_matches_route": True},
            {"selected_pid_matches_route": True},
            {"selected_pid_matches_route": True},
        ])
        self.assertEqual(complete, {
            "route_count": 3,
            "routes_with_selected_pid_match_count": 3,
            "routes_without_selected_pid_match_count": 0,
            "all_routes_select_their_pid": True,
        })

        mismatch = reconlib.summarize_route_pid_mapping([
            {"selected_pid_matches_route": True},
            {"selected_pid_matches_route": False},
        ])
        self.assertFalse(mismatch["all_routes_select_their_pid"])

    def test_program_component_mapping_requires_unique_exact_elf_alias(self):
        inventory = reconlib.component_route_inventory("\n".join((
            "fixture.route (debugger.pid.5)",
            "fixture.a (debugger.name.C:/fixture/app-a.elf)",
            "fixture.b (debugger.name.c:/fixture/app-b.elf)",
            "fixture.duplicate (debugger.name.c:/fixture/app-b.elf)",
        )))

        programs = reconlib.configured_core_programs({"cores": [
            {"elf": "c:/fixture/app-a.elf"},
            {"elf": "c:/fixture/app-b.elf"},
            {"elf": "c:/fixture/missing.elf"},
        ]})
        mapping = reconlib.match_configured_program_components(inventory, programs)

        self.assertEqual(mapping["matches"], [{
            "configured_core_ordinal": 0,
            "component_id": "fixture.a",
        }])
        self.assertEqual(mapping["summary"], {
            "configured_core_count": 3,
            "program_component_count": 3,
            "configured_cores_with_unique_program_component_count": 1,
            "configured_cores_missing_program_component_count": 1,
            "configured_cores_with_duplicate_program_component_count": 1,
            "program_alias_route_shared_component_count": 0,
            "all_configured_cores_have_unique_program_component": False,
        })

    def test_program_component_routing_requires_stopped_selected_processes(self):
        mapping = {
            "summary": {
                "configured_cores_with_unique_program_component_count": 2,
                "all_configured_cores_have_unique_program_component": True,
            },
        }
        verified = reconlib.summarize_program_component_routing(2, mapping, [
            {"processes": {"selected_row_count": 1, "selected_status": "stopped"},
             "halt": {"cause": "user_request"}},
            {"processes": {"selected_row_count": 1, "selected_status": "stopped"},
             "halt": {"cause": "breakpoint"}},
        ])
        self.assertTrue(verified["strict_program_component_routing_confirmed"])

        unverified = reconlib.summarize_program_component_routing(2, mapping, [
            {"processes": {"selected_row_count": 1, "selected_status": "stopped"},
             "halt": {"cause": "user_request"}},
            {"processes": {"selected_row_count": 1, "selected_status": "no_process"},
             "halt": {"cause": "not_running"}},
        ])
        self.assertFalse(unverified["strict_program_component_routing_confirmed"])

    def test_window_pid_alias_evidence_redacts_pid_and_requires_bijection(self):
        first = reconlib.WindowPidAliasObservation(0, 41, 1)
        duplicate = reconlib.WindowPidAliasObservation(1, 41, 1)

        self.assertEqual(first.evidence_summary(), {
            "configured_core_ordinal": 0,
            "target_pid_positive": True,
            "matching_pid_alias_count": 1,
            "target_pid_has_unique_route_alias": True,
        })
        self.assertNotIn("41", json.dumps(first.evidence_summary()))

        summary = reconlib.summarize_window_pid_alias_mapping(2, [first, duplicate])
        self.assertFalse(summary["strict_window_pid_alias_mapping_confirmed"])
        self.assertEqual(summary["duplicate_target_pid_count"], 1)

        recorder = reconlib.Recorder("unit_window_pid")
        recorder._write = lambda: None
        # The timestamp is unrelated to the redaction boundary and can contain
        # the same digit sequence as a synthetic PID by coincidence.
        recorder.data["started_epoch"] = 0
        recorder.data["python_version"] = "test"
        observation = recorder.step("window_pid", lambda: first)
        self.assertIs(observation, first)
        self.assertNotIn("41", json.dumps(recorder.data))

    def test_window_process_alias_observation_parses_only_strict_pid_relations(self):
        class Window(object):
            def GetTargetPid(self):
                return 0x2b

            def GetCurPrInfo(self, unused):
                return {"pid": "0x2b", "proc": "43"}

        session = reconlib.Session(None, Window(), False, "warm")
        observation = reconlib.window_process_alias_observation(session, 1, [0x2a, 0x2b])
        self.assertEqual(observation.evidence_summary(), {
            "configured_core_ordinal": 1,
            "target_pid_positive": True,
            "target_pid_matching_alias_count": 1,
            "process_info_pid_positive": True,
            "process_info_pid_matching_alias_count": 1,
            "target_pid_matches_process_info_pid": True,
            "process_info_proc_relation": "matches_pid",
        })

        self.assertIsNone(reconlib._strict_pid("42.0"))
        self.assertIsNone(reconlib._strict_pid(True))

    def test_program_component_process_summary_keeps_relations_without_paths(self):
        listing = "\n".join((
            "     # PID        PPID       Status        CBEFITDHR Name and Arguments",
            ">>   0 0x2a       N/A        Stopped       011111101 C:/fixture/app-a.elf",
            "     1 0x2b       0x2a       Stopped       011111101 C:/fixture/app-b.elf",
        ))
        summary = reconlib.summarize_program_component_processes(
            0, ["c:\\fixture\\app-a.elf"], listing,
            ["c:\\fixture\\app-a.elf", "c:\\fixture\\app-b.elf"])

        self.assertEqual(summary["alias_program_exact_p_program_match_count"], 1)
        self.assertEqual(summary["configured_elf_exact_p_program_match_count"], 2)
        self.assertEqual(summary["selected_pid_to_program_pid_match_count"], 1)
        self.assertEqual(summary["selected_pid_to_program_ppid_match_count"], 1)
        self.assertEqual(summary["selected_ppid_to_program_pid_match_count"], 0)
        self.assertTrue(summary["selected_program_matches_configured_elf"])
        self.assertEqual(summary["selected_configured_core_ordinal"], 0)
        self.assertNotIn("fixture", json.dumps(summary))

    def test_program_alias_process_mapping_uses_exact_paths_as_non_strict_evidence(self):
        process_rows = reconlib._parse_process_listing(
            ">>   0 0x2a N/A Stopped 011111101 C:/fixture/app-a.elf\n")
        observation = reconlib.ProgramComponentObservation(
            "opaque.component", ["c:\\fixture\\app-a.elf"], {}, process_rows,
            {"accepted": True}, {}, {"accepted": True})
        summary = reconlib.summarize_program_alias_process_mapping(
            ["c:\\fixture\\app-a.elf", "c:\\fixture\\app-b.elf"], [observation])

        self.assertEqual(summary["configured_elf_exact_p_program_match_count"], 1)
        self.assertEqual(summary["program_alias_exact_p_program_match_count"], 1)
        self.assertEqual(summary["configured_elf_without_exact_p_program_count"], 1)
        self.assertNotIn("fixture", json.dumps(summary))

    def test_strict_program_component_mapping_requires_selected_program_bijection(self):
        def observation(component, ordinal):
            return reconlib.ProgramComponentObservation(
                component, [], {
                    "selected_program_matches_configured_elf": True,
                    "selected_configured_core_ordinal": ordinal,
                    "selected_row_count": 1,
                    "selected_status": "stopped",
                }, [], {"accepted": True, "status": 1},
                {}, {"accepted": True, "status": 1})

        strict = reconlib.summarize_strict_program_component_mapping(
            ["a", "b"], [observation("opaque-a", 0), observation("opaque-b", 1)])
        self.assertTrue(strict["strict_program_component_mapping_confirmed"])
        self.assertEqual(strict["unmapped_configured_core_count"], 0)

        duplicate = reconlib.summarize_strict_program_component_mapping(
            ["a", "b"], [observation("opaque-a", 0), observation("opaque-b", 0)])
        self.assertFalse(duplicate["strict_program_component_mapping_confirmed"])
        self.assertEqual(duplicate["duplicate_configured_core_mapping_count"], 1)

    def test_inspection_shapes_follow_production_grammar_boundaries(self):
        self.assertTrue(reconlib.inspection_output_shape(
            "calls", "0_ entry\t[C:/fixture/main.c:1,1]\n"))
        self.assertTrue(reconlib.inspection_output_shape("l", "state = 1\n"))
        self.assertTrue(reconlib.inspection_output_shape(
            "B", "No software breakpoints set.\n"))
        self.assertTrue(reconlib.inspection_output_shape(
            "print $_STATE", "$_STATE = stopped\n"))
        self.assertFalse(reconlib.inspection_output_shape("calls", "unexpected\n"))
        self.assertFalse(reconlib.inspection_output_shape("l", "not-a-value\n"))
        self.assertFalse(reconlib.inspection_output_shape("B", "not-a-breakpoint\n"))
        self.assertFalse(reconlib.inspection_output_shape(
            "print $_STATE", "first = 1\nsecond = 2\n"))

    def test_top_frame_and_breakpoint_records_remain_memory_only(self):
        frame = reconlib.top_frame_from_calls(
            "0_ entry\t[C:/fixture/main.c:137,1]\n")
        self.assertEqual(frame.source, "C:/fixture/main.c")
        self.assertEqual(frame.line, 137)
        self.assertTrue(frame.selected)

        before = reconlib.parse_breakpoint_listing(
            "No software breakpoints set.\n")
        token = 'mprintf("HIT 0x00000001\\n")'
        after = reconlib.parse_breakpoint_listing(
            "7 C:/fixture/main.c#137: 0x00c0ffee AT count: 1 "
            "<{mprintf(\"HIT 0x00000001\\n\")}>\n")
        record = reconlib.identify_probe_breakpoint(
            before, after, "C:/fixture/main.c", token)
        mutation = reconlib.BreakpointMutation(
            record, frame.source, frame.line, token)
        safe = mutation.evidence_summary()

        self.assertTrue(safe["handle_present"])
        self.assertTrue(safe["command_token_matches"])
        self.assertTrue(safe["source_matches_requested"])
        self.assertTrue(safe["actual_line_matches_requested"])
        serialized = json.dumps({"frame": frame, "mutation": mutation}, default=lambda v:
                                v.evidence_summary())
        self.assertNotIn("fixture", serialized)
        self.assertNotIn("137", serialized)
        self.assertNotIn("c0ffee", serialized)
        self.assertNotIn("HIT", serialized)

    def test_comparable_top_call_preserves_unsourced_identity_only_in_memory(self):
        unsourced = reconlib.comparable_top_call_from_calls("0_ 0xDEADBEEF\n")
        sourced = reconlib.comparable_top_call_from_calls(
            "0_ entry\t[C:/fixture/main.c:137,1]\n")

        self.assertEqual(unsourced.location_kind, "address")
        self.assertEqual(unsourced.identity, "0xDEADBEEF")
        self.assertTrue(unsourced.selected)
        self.assertEqual(sourced.location_kind, "source")
        self.assertEqual(sourced.identity, ("C:/fixture/main.c", 137))
        serialized = json.dumps({"unsourced": unsourced, "sourced": sourced},
                                default=lambda value: value.evidence_summary())
        self.assertIn('"location_kind": "address"', serialized)
        self.assertIn('"source_present": false', serialized)
        self.assertNotIn("DEADBEEF", serialized)
        self.assertNotIn("fixture", serialized)

    def test_breakpoint_record_parser_rejects_ambiguous_or_unsafe_rows(self):
        zero = reconlib.parse_breakpoint_listing(
            "0 C:/fixture/main.c#137: 0x00c0ffee AT count: 1\n")
        self.assertEqual(len(zero), 1)
        self.assertTrue(reconlib.BreakpointMutation(
            zero[0], "C:/fixture/main.c", 137, "").evidence_summary()[
                "handle_present"])
        with self.assertRaises(ValueError):
            reconlib.parse_breakpoint_listing(
                "7 C:/fixture/main.c#137: 0x00c0ffee AT count: 1\n"
                "7 C:/fixture/main.c#137: 0x00c0ffef AT count: 1\n")
        with self.assertRaises(ValueError):
            reconlib.top_frame_from_calls(
                "0_ entry\t[C:/fixture/main file.c:137,1]\n")

    def test_breakpoint_listing_summary_keeps_only_counts(self):
        summary = reconlib.summarize_breakpoint_listing(
            '0 C:/fixture/main.c#137: 0x00c0ffee AT count: 1 '
            '<{mprintf("HIT 0x00000001\\n")}>\n')
        self.assertEqual(summary, {
            "breakpoint_count": 1,
            "production_hint_token_count": 1,
        })
        serialized = json.dumps(summary)
        self.assertNotIn("fixture", serialized)
        self.assertNotIn("HIT", serialized)
        self.assertNotIn("00000001", serialized)

    def test_inspection_semantic_flags_are_text_free_and_not_an_empty_sentinel(self):
        unknown = "No stack at C:/private/firmware.elf 0xDEADBEEF"
        flags = reconlib.inspection_semantic_flags(unknown)
        self.assertEqual(flags["exact_known_phrase_category"], "unrecognized")
        self.assertFalse(flags["trimmed_empty"])
        self.assertTrue(flags["contains_path_payload"])
        self.assertTrue(flags["contains_address_payload"])
        self.assertTrue(flags["contains_digit_payload"])
        self.assertTrue(flags["contains_identifier_like_payload"])
        self.assertTrue(flags["contains_keyword_no"])
        self.assertTrue(flags["contains_keyword_stack"])
        self.assertNotIn("private", json.dumps(flags))
        self.assertNotIn("DEADBEEF", json.dumps(flags))

        empty = reconlib.inspection_semantic_flags(" \n")
        self.assertTrue(empty["trimmed_empty"])
        self.assertEqual(empty["exact_known_phrase_category"], "unrecognized")

        known = reconlib.inspection_semantic_flags("Process not running.")
        self.assertEqual(known["exact_known_phrase_category"], "process_not_running")

    def test_calls_grammar_signature_distinguishes_source_and_unsourced_frames(self):
        source = reconlib.calls_grammar_signature(
            "0_ fixture_entry()\t[C:/fixture/main.c:1,1]\n")
        self.assertEqual(source["input_category"], "text")
        self.assertEqual(source["nonblank_line_count"], 1)
        self.assertEqual(source["indexed_source_frame_count"], 1)
        self.assertEqual(source["selected_marker_line_count"], 1)
        self.assertEqual(source["tab_separator_line_count"], 1)

        address = reconlib.calls_grammar_signature("7  0xDEADBEEF\n")
        self.assertEqual(address["indexed_address_only_frame_count"], 1)
        self.assertEqual(address["selected_marker_line_count"], 0)
        self.assertEqual(address["hex_address_line_count"], 1)

        symbol_address = reconlib.calls_grammar_signature(
            "1_ fixture_entry at 0xDEADBEEF\n")
        self.assertEqual(symbol_address["indexed_symbol_address_frame_count"], 1)
        self.assertEqual(symbol_address["selected_marker_line_count"], 1)

        function_address = reconlib.calls_grammar_signature(
            "2_ fixture_entry(unsigned int, char), 0xDEADBEEF\n")
        self.assertEqual(function_address[
            "indexed_function_comma_address_frame_count"], 1)
        self.assertEqual(function_address["path_separator_line_count"], 0)

        self.assertEqual(function_address["grammar_shape"],
                         "frame_selected_identifier_open_paren_identifier_"
                         "whitespace_identifier_comma_whitespace_identifier_"
                         "close_paren_comma_whitespace_hex_address")
        self.assertEqual(reconlib.calls_grammar_signature(
            "7  C:/private/firmware.c:4\n")["grammar_shape"],
            "frame_unselected_identifier_colon_slash_identifier_slash_"
            "identifier_dot_identifier_colon_decimal")

        serialized = json.dumps({"source": source, "address": address,
                                 "symbol_address": symbol_address,
                                 "function_address": function_address})
        self.assertNotIn("fixture", serialized)
        self.assertNotIn("DEADBEEF", serialized)
        self.assertNotIn("0x", serialized)
        self.assertNotIn("private", serialized)
        self.assertNotIn("firmware", serialized)

    def test_session_evidence_never_uses_python_object_representation(self):
        session = reconlib.Session(None, object(), False, "warm")
        recorder = reconlib.Recorder("unit_session")
        recorder._write = lambda: None
        recorder.data["started_epoch"] = 0
        recorder.data["python_version"] = "test"
        recorder.step("session", lambda: session)
        serialized = json.dumps(recorder.data)
        self.assertIn('"kind": "session"', serialized)
        self.assertNotIn("object at 0x", serialized)

    def test_summarizes_halt_cause_without_retaining_command_list(self):
        summary = reconlib.summarize_halt_info(
            "Halted for breakpoint.\nCommand list was: {private-target-command}\n")

        self.assertEqual(summary, {
            "cause": "breakpoint",
            "command_list_present": True,
            "line_count": 2,
        })
        self.assertNotIn("private-target-command", repr(summary))

    def test_halt_summary_refuses_unknown_or_ambiguous_text(self):
        with self.assertRaises(ValueError):
            reconlib.summarize_halt_info("Process not running.\nextra\n")
        with self.assertRaises(ValueError):
            reconlib.summarize_halt_info(
                "Process not running.\nHalted by user request.\n")


class P14ProtocolTests(unittest.TestCase):
    def setUp(self):
        self.p14 = load_p14_module()

    def observation(self, identity, state="$_STATE = stopped\n",
                    process="stopped", halt="not_running"):
        return self.p14.RoutedExecutionObservables(
            {"selected_status": process}, {"cause": halt}, state,
            reconlib.ComparableTopCall("address", identity, True))

    def topology(self):
        return types.SimpleNamespace(component_by_core={0: "pid.1", 1: "pid.2"})

    def test_poll_rejects_malformed_routed_process_reply(self):
        class Session(object):
            status_stopped = 2

            def GetStatus(self):
                return 2

        original = self.p14.routed_read
        self.p14.routed_read = lambda *_args: "malformed P output\n"
        try:
            with self.assertRaises(ValueError):
                self.p14.poll_until_selected_stopped(
                    Session(), self.topology(), 0, 1, 0.01, lambda _value: None)
        finally:
            self.p14.routed_read = original

    def test_poll_timeout_is_finite_and_reports_no_completion(self):
        class Session(object):
            status_stopped = 2

            def GetStatus(self):
                return 2

        listing = ">>   0 0x2a N/A Running 011111101 C:/fixture/app.elf\n"
        original = self.p14.routed_read
        sleeps = []
        self.p14.routed_read = lambda *_args: listing
        try:
            outcome = self.p14.poll_until_selected_stopped(
                Session(), self.topology(), 0, 3, 0.25, sleeps.append)
        finally:
            self.p14.routed_read = original
        self.assertEqual(outcome, {
            "attempt_count": 3,
            "selected_stopped": False,
            "primary_stopped": False,
            "completed_before_recovery": False,
        })
        self.assertEqual(sleeps, [0.25, 0.25])

    def test_poll_accepts_selected_and_primary_stopped(self):
        class Session(object):
            status_stopped = 2

            def GetStatus(self):
                return 2

        listing = ">>   0 0x2a N/A Stopped 011111101 C:/fixture/app.elf\n"
        original = self.p14.routed_read
        self.p14.routed_read = lambda *_args: listing
        try:
            outcome = self.p14.poll_until_selected_stopped(
                Session(), self.topology(), 0, 3, 0.25, lambda _value: None)
        finally:
            self.p14.routed_read = original
        self.assertEqual(outcome, {
            "attempt_count": 1,
            "selected_stopped": True,
            "primary_stopped": True,
            "completed_before_recovery": True,
        })

    def test_unselected_drift_fails_the_step_comparison(self):
        before = {
            "configured_core_0": self.observation("0x1000"),
            "configured_core_1": self.observation("0x2000"),
        }
        after = {
            "configured_core_0": self.observation("0x1004"),
            "configured_core_1": self.observation("0x2004"),
        }
        with self.assertRaisesRegex(RuntimeError, "unselected configured core drifted"):
            self.p14.compare_step_matrices(before, after, 0)

    def test_recovery_halt_failure_is_not_masked(self):
        class Session(object):
            pass

        original = self.p14.reconlib.run_command
        self.p14.reconlib.run_command = lambda *_args: {
            "accepted": False, "status": 0, "raw": ""}
        try:
            with self.assertRaisesRegex(RuntimeError, "routed halt recovery was rejected"):
                self.p14.route_recovery_halt(Session(), self.topology(), 0)
        finally:
            self.p14.reconlib.run_command = original


class ProbeSourceContracts(unittest.TestCase):

    def probe_source(self, name):
        probe_path = os.path.join(os.path.dirname(os.path.dirname(
            os.path.abspath(__file__))), "probes", name)
        with open(probe_path, "rb") as fh:
            return fh.read().decode("utf-8")

    def test_m2_source_known_never_guesses_commands_or_cold_connects(self):
        probe_path = os.path.join(os.path.dirname(os.path.dirname(
            os.path.abspath(__file__))), "probes", "p03_m2_source_known.py")
        with open(probe_path, "rb") as fh:
            source = fh.read().decode("utf-8")

        self.assertNotIn("reconlib.run_command(", source)
        self.assertNotIn("RunCommands", source)
        self.assertNotIn("open_cold_session", source)
        self.assertNotIn("DebugProgram", source)
        self.assertNotIn("ConnectToTarget", source)
        self.assertNotIn("Disconnect", source)
        self.assertNotIn("configured_unary_api_call", source)
        self.assertNotIn("recover_stopped_session", source)

    def test_m2_source_known_inventories_source_api_shape_without_callable_candidates(self):
        probe_path = os.path.join(os.path.dirname(os.path.dirname(
            os.path.abspath(__file__))), "probes", "p03_m2_source_known.py")
        with open(probe_path, "rb") as fh:
            source = fh.read().decode("utf-8")

        self.assertIn('"browse", "source", "file", "line", "module"', source)
        self.assertIn("DOCUMENTED_READ_ONLY_SOURCE_METHODS = ()", source)
        self.assertIn("documented_read_only_source_candidate_counts", source)
        self.assertIn("inventory_only_no_documented_read_only_source_api", source)

    def test_m4_never_falls_back_to_a_debugger_command(self):
        source = self.probe_source("p04_m4_execution.py")

        self.assertNotIn("reconlib.run_command(", source)
        self.assertNotIn("open_cold_session", source)
        self.assertNotIn("DebugProgram", source)
        self.assertNotIn("ConnectToTarget", source)
        self.assertIn("reconlib.bind_existing_program_window(cfg)", source)
        self.assertIn('rec.note("execution_phase", "disabled_by_local_configuration")',
                      source)
        self.assertIn("reconlib.recover_stopped_session(session)", source)
        self.assertNotIn('outcome["raw"]', source)
        self.assertNotIn('"raw":', source)
        self.assertNotIn("'raw':", source)

    def test_m5_has_one_verified_register_command_and_no_cold_open(self):
        source = self.probe_source("p05_m5_inspection.py")

        self.assertEqual(source.count('reconlib.run_command(session, "l r")'), 1)
        self.assertNotIn("open_cold_session", source)
        self.assertNotIn("DebugProgram", source)
        self.assertNotIn("ConnectToTarget", source)
        self.assertIn("reconlib.bind_existing_program_window(cfg)", source)
        self.assertIn('rec.note("read_phase", "disabled_by_local_configuration")',
                      source)
        self.assertIn("reconlib.recover_stopped_session(session)", source)
        self.assertNotIn('outcome["raw"]', source)
        self.assertNotIn('"raw":', source)
        self.assertNotIn("'raw':", source)

    def test_m2_program_breakpoint_requires_warm_router_and_records_no_sensitive_values(self):
        source = self.probe_source("p09_m2_program_breakpoint.py")

        self.assertIn("explicitly_supplied_router_port", source)
        self.assertIn("discover_strict_program_topology", source)
        self.assertIn("single_program_component_paired_breakpoint", source)
        self.assertIn("program_component_breakpoint_matrix", source)
        self.assertIn("delete_owned_breakpoint", source)
        self.assertIn("recover_token_owned_breakpoints", source)
        self.assertIn('mprintf("HIT 0x%08X\\\\n")', source)
        self.assertNotIn("open_cold_session", source)
        self.assertNotIn("DebugProgram", source)
        self.assertNotIn("ConnectToTarget", source)
        self.assertNotIn("Disconnect", source)
        self.assertNotIn('"raw":', source)
        self.assertNotIn("'raw':", source)

    def test_m2_owned_breakpoint_recovery_is_warm_gated_and_redacted(self):
        source = self.probe_source("p11_m2_owned_breakpoint_recovery.py")

        self.assertIn("explicitly_supplied_router_port", source)
        self.assertIn("discover_strict_program_topology", source)
        self.assertIn("revalidate_strict_program_topology", source)
        self.assertIn("require_stopped_session", source)
        self.assertIn('"d %%%d"', source)
        self.assertIn("PRODUCTION_HINT_TOKEN", source)
        self.assertNotIn("open_cold_session", source)
        self.assertNotIn("DebugProgram", source)
        self.assertNotIn("ConnectToTarget", source)
        self.assertNotIn("Disconnect", source)
        self.assertNotIn('"raw":', source)
        self.assertNotIn("'raw':", source)

    def test_per_core_warm_inventory_completes_before_any_session_or_command(self):
        source = self.probe_source("p10_per_core_warm_inventory.py")

        inventory_summary = source.index(
            'rec.note("configured_core_window_inventory_summary"')
        session_creation = source.index("session = reconlib.Session(")
        topology_discovery = source.index(
            "reconlib.discover_strict_program_topology(")
        self.assertLess(inventory_summary, session_creation)
        self.assertLess(session_creation, topology_discovery)
        self.assertIn("reconlib.inspect_configured_core_window", source)
        self.assertIn("reconlib.require_stopped_session", source)
        self.assertIn("reconlib.strict_routed_command", source)
        self.assertIn('("breakpoints", "B")', source)
        self.assertIn('(\"state\", \"print $_STATE\")', source)
        self.assertNotIn("bind_existing_program_window", source)
        self.assertNotIn("open_cold_session", source)
        self.assertNotIn("DebugProgram", source)
        self.assertNotIn("ConnectToTarget", source)
        self.assertNotIn("Disconnect", source)
        self.assertNotIn("recover_stopped_session", source)
        self.assertNotIn('"raw":', source)
        self.assertNotIn("'raw':", source)

    def test_warm_only_probes_are_launcher_gated_to_warm_sessions(self):
        root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
        with open(os.path.join(root, "run.py"), "rb") as fh:
            source = fh.read().decode("utf-8")
        self.assertIn('"p00e_warm_bind"', source)
        self.assertIn('"p02_socket"', source)
        self.assertIn('"p03_m2_source_known"', source)
        self.assertIn('"p04_m4_execution"', source)
        self.assertIn('"p05_m5_inspection"', source)
        self.assertIn('"p06_poll"', source)
        self.assertIn('"p07_bpclear"', source)
        self.assertIn('"p08_routed_processes"', source)
        self.assertIn('"p09_m2_program_breakpoint"', source)
        self.assertIn('"p10_per_core_warm_inventory"', source)
        self.assertIn('"p11_m2_owned_breakpoint_recovery"', source)
        self.assertIn('"p12_m2_source_files"', source)
        self.assertIn('"p13_execution_domain_syntax"', source)
        self.assertIn('"p14_execution_domain_experiment"', source)
        self.assertIn('"p15_warm_registry_health"', source)
        self.assertIn('"%s is warm-only and rejects --cold"', source)
        self.assertIn('"%s requires a live service-router port"', source)

    def test_warm_registry_health_is_non_mutating_and_phase_checkpointed(self):
        source = self.probe_source("p15_warm_registry_health.py")

        for checkpoint in (
                "before_window_register_ide_open",
                "after_window_register_ide_open",
                "before_window_register_construct",
                "after_window_register_construct",
                "before_get_window_list",
                "after_get_window_list",
                "before_create_debugger_window",
                "after_create_debugger_window",
                "before_get_program",
                "after_get_program",
                "before_get_status",
                "after_get_status",
                "before_get_process_info",
                "after_get_process_info"):
            self.assertIn(checkpoint, source)
        for forbidden in (
                "open_cold_session", "DebugProgram", "ConnectToTarget",
                "Disconnect", "Resume", "Halt", "Step",
                '"raw":', "'raw':"):
            self.assertNotIn(forbidden, source)

    def test_m2_source_file_listing_is_warm_routed_and_never_mutates(self):
        source = self.probe_source("p12_m2_source_files.py")

        inventory_summary = source.index(
            'rec.note("configured_core_window_inventory_summary"')
        session_creation = source.index("session = reconlib.Session(")
        topology_discovery = source.index(
            "reconlib.discover_strict_program_topology(")
        self.assertLess(inventory_summary, session_creation)
        self.assertLess(session_creation, topology_discovery)
        self.assertIn("reconlib.source_file_listing_commands", source)
        self.assertIn("reconlib.strict_routed_command", source)
        self.assertIn("reconlib.source_file_listing_grammar_signature", source)
        self.assertIn("reconlib.require_stopped_session", source)
        for forbidden in (
                "open_cold_session", "DebugProgram", "ConnectToTarget",
                "Disconnect", "recover_stopped_session", '"b ', '"d ',
                '"raw":', "'raw':"):
            self.assertNotIn(forbidden, source)

    def test_execution_domain_syntax_probe_uses_only_sc_and_strict_topology(self):
        source = self.probe_source("p13_execution_domain_syntax.py")

        inventory_summary = source.index(
            'rec.note("configured_core_window_inventory_summary"')
        session_creation = source.index("session = reconlib.Session(")
        topology_discovery = source.index(
            "reconlib.discover_strict_program_topology(")
        self.assertLess(inventory_summary, session_creation)
        self.assertLess(session_creation, topology_discovery)
        self.assertIn("reconlib.execution_domain_syntax_candidates", source)
        self.assertIn("reconlib.strict_execution_domain_syntax_check", source)
        self.assertIn("reconlib.require_stopped_session", source)
        self.assertIn("target_control_executed", source)
        for forbidden in (
                "open_cold_session", "DebugProgram", "ConnectToTarget",
                "Disconnect", "recover_stopped_session", "strict_routed_command",
                '"raw":', "'raw':"):
            self.assertNotIn(forbidden, source)

    def test_execution_domain_experiment_is_disabled_and_recovers_by_route(self):
        source = self.probe_source("p14_execution_domain_experiment.py")

        self.assertIn("HALT_ACKNOWLEDGEMENT", source)
        self.assertIn("STEP_ACKNOWLEDGEMENT", source)
        self.assertIn("selected_core_ordinal", source)
        self.assertIn("single_step_enabled", source)
        self.assertIn("route_recovery_halt", source)
        self.assertIn("finally:", source)
        self.assertIn('"sl n"', source)
        self.assertIn("full_topology_matrix", source)
        self.assertIn("routed_execution_observables", source)
        self.assertIn("calls nopar pos notypes", source)
        self.assertIn("comparable_top_call_from_calls", source)
        self.assertIn("reconlib.revalidate_strict_program_topology", source)
        self.assertNotIn("reconlib.recover_stopped_session", source)
        self.assertNotIn("open_cold_session", source)
        self.assertNotIn("DebugProgram", source)
        self.assertNotIn("ConnectToTarget", source)
        self.assertNotIn("Disconnect", source)
        self.assertNotIn('"raw":', source)
        self.assertNotIn("'raw':", source)

    def test_source_file_listing_commands_exclude_contains_filters(self):
        self.assertEqual(reconlib.source_file_listing_commands({}), [("all", "l f")])
        self.assertEqual(reconlib.source_file_listing_commands({
            "m2_source_file_probe": {"filters": {
                "known": "src/main.c", "unknown": "src/missing.c"}}}), [
                    ("all", "l f")])

    def test_source_file_listing_grammar_signature_is_bounded_and_redacted(self):
        summary = reconlib.source_file_listing_grammar_signature(
            "C:/fixture/src/main.c\nrelative/file.c\nnot a path\n")
        self.assertEqual(summary, {
            "input_category": "text",
            "nonblank_line_count": 3,
            "blank_line_count": 0,
            "source_locator_candidate_count": 2,
            "windows_absolute_candidate_count": 1,
            "slash_rooted_candidate_count": 0,
            "relative_locator_candidate_count": 1,
            "unclassified_line_count": 1,
            "grammar_shape": "multiple_line_categories",
            "field_count_buckets": {
                "one": 2, "two": 0, "three": 1, "four": 0,
                "five_to_eight": 0, "nine_or_more": 0,
            },
            "index_prefix_counts": {
                "none": 3, "decimal": 0, "bracketed_decimal": 0,
                "bullet": 0,
            },
            "quote_counts": {
                "none": 3, "single": 0, "double": 0, "mixed": 0,
            },
            "whitespace_separator_counts": {
                "none": 2, "space": 1, "tab": 0, "mixed": 0,
            },
            "path_separator_counts": {
                "none": 1, "forward": 2, "backslash": 0, "mixed": 0,
            },
            "field_delimiter_counts": {
                "none": 2, "colon": 1, "comma": 0, "semicolon": 0,
                "pipe": 0, "equals": 0, "multiple": 0,
            },
            "extension_present_line_count": 2,
            "distinct_line_shape_count": 3,
            "repeated_line_shape_count": 0,
            "dominant_line_shape_count": 1,
            "line_shape_counts": {
                "fields_one|prefix_none|quotes_none|whitespace_none|path_forward|delimiter_colon|extension_present": 1,
                "fields_one|prefix_none|quotes_none|whitespace_none|path_forward|delimiter_none|extension_present": 1,
                "fields_three|prefix_none|quotes_none|whitespace_space|path_none|delimiter_none|extension_absent": 1,
            },
            "decimal_prefix_delimiter_counts": {
                "none": 3, "colon": 0, "dot": 0, "close_paren": 0,
            },
            "colon_role_counts": {
                "none": 2, "index_then_drive": 0, "index_only": 0,
                "drive_only": 0, "other": 1,
            },
            "normalized_line_grammar_counts": {
                "bullet_four_field_header": 0,
                "decimal_index_drive_absolute_path": 0,
                "static_source_file_legend": 0,
                "other": 3,
            },
            "first_line_is_bullet_four_field_header": False,
            "row_indices_zero_based_contiguous": True,
            "strict_production_grammar_candidate": False,
            "static_marker_count": 0,
            "static_marker_recognized": False,
            "static_marker_position_bucket": "none",
            "static_marker_relative_to_data_rows": "none",
            "static_marker_keyword_flags": {
                "source": False, "file": False, "current": False,
                "selected": False, "name": False, "debug": False,
                "info": False, "not": False, "loaded": False, "used": False,
                "active": False, "asterisk": False, "indicates": False,
                "path": False, "full": False,
            },
            "bullet_nonrow_count": 0,
            "bullet_nonrow_position_bucket": "none",
            "bullet_nonrow_relative_to_data_rows": "none",
            "bullet_nonrow_token_categories": [],
            "bullet_nonrow_token_length_buckets": [],
            "bullet_nonrow_second_token_kind": "none",
            "bullet_nonrow_third_token_kind": "none",
            "bullet_nonrow_legend_pair": "none",
            "bullet_nonrow_first_decoration": {
                "repeated_punctuation": False,
                "character_class": "none",
                "count_bucket": "none",
            },
            "bullet_nonrow_last_decoration": {
                "repeated_punctuation": False,
                "character_class": "none",
                "count_bucket": "none",
            },
            "bullet_nonrow_decorations_equal": False,
            "bullet_nonrow_finite_legend_match": False,
            "bullet_nonrow_keyword_flags": {
                "source": False, "file": False, "current": False,
                "selected": False, "name": False, "debug": False,
                "info": False, "not": False, "loaded": False, "used": False,
                "active": False, "asterisk": False, "indicates": False,
                "path": False, "full": False,
            },
            "bullet_nonrow_static_marker_recognized": False,
            "production_parser_delta": {
                "input_within_go_bounds": True,
                "line_count_within_go_bounds": True,
                "terminal_cr_line_count": 0,
                "embedded_cr_line_count": 0,
                "exact_go_header": False,
                "row_count": 2,
                "row_prefix_leading_whitespace_rows": {
                    "none": 0, "space": 0, "tab": 0, "mixed": 0,
                    "not_indexed": 2,
                },
                "row_prefix_delimiter_rows": {
                    "colon": 0, "other": 0, "missing": 2,
                },
                "row_prefix_post_colon_whitespace_rows": {
                    "single_space": 0, "multiple_space": 0, "tab": 0,
                    "mixed": 0, "none": 0, "not_colon": 2,
                },
                "prefix_leading_none_count": 0,
                "prefix_leading_space_count": 0,
                "prefix_leading_tab_count": 0,
                "prefix_leading_mixed_count": 0,
                "prefix_not_indexed_count": 2,
                "prefix_leading_space_width_1_count": 0,
                "prefix_leading_space_width_2_count": 0,
                "prefix_leading_space_width_3_count": 0,
                "prefix_leading_space_width_4_count": 0,
                "prefix_leading_space_width_5_to_8_count": 0,
                "prefix_leading_space_width_over_8_count": 0,
                "prefix_leading_space_all_rows_same_width": False,
                "prefix_delimiter_colon_count": 0,
                "prefix_delimiter_other_count": 0,
                "prefix_delimiter_missing_count": 2,
                "prefix_post_colon_single_space_count": 0,
                "prefix_post_colon_multiple_space_count": 0,
                "prefix_post_colon_tab_count": 0,
                "prefix_post_colon_mixed_count": 0,
                "prefix_post_colon_none_count": 0,
                "prefix_post_colon_not_colon_count": 2,
                "drive_absolute_payload_count": 0,
                "broad_drive_row_count": 0,
                "exact_go_row_syntax_count": 0,
                "row_syntax_rejection_count": 2,
                "allowed_charset_syntax_rejection_count": 0,
                "row_payload_category_rows": {
                    "allowed_only": 0,
                    "forward_slash": 0,
                    "whitespace": 0,
                    "other_ascii_punctuation": 0,
                    "other_ascii_printable": 0,
                    "control": 0,
                    "non_ascii": 0,
                },
                "row_index_zero_based_contiguous": True,
                "relative_component_row_count": 0,
                "empty_component_row_count": 0,
                "duplicate_canonical_path_row_count": 0,
                "duplicate_exact_path_row_count": 0,
                "duplicate_case_fold_path_row_count": 0,
                "duplicate_separator_normalization_path_row_count": 0,
                "duplicate_other_canonical_collision_row_count": 0,
                "duplicate_only_exact_path_rows": False,
                "strict_go_parser_candidate": False,
            },
        })
        self.assertNotIn("fixture", repr(summary))
        structural = reconlib.source_file_listing_grammar_signature(
            '01: "C:/fixture/src/main.c"\n[2]\trelative/file.hpp\n')
        self.assertEqual(structural["index_prefix_counts"], {
            "none": 0, "decimal": 1, "bracketed_decimal": 1, "bullet": 0,
        })
        self.assertEqual(structural["quote_counts"]["double"], 1)
        self.assertEqual(structural["whitespace_separator_counts"]["tab"], 1)
        self.assertEqual(structural["extension_present_line_count"], 2)
        self.assertNotIn("fixture", repr(structural))
        strict = reconlib.source_file_listing_grammar_signature(
            "--------  File names  --------\n    0: C:\\fixture\\src\\main.c\n"
            "    1: D:\\fixture\\src\\other.hpp\n")
        self.assertEqual(strict["colon_role_counts"], {
            "none": 1, "index_then_drive": 2, "index_only": 0,
            "drive_only": 0, "other": 0,
        })
        self.assertTrue(strict["row_indices_zero_based_contiguous"])
        self.assertTrue(strict["strict_production_grammar_candidate"])
        self.assertTrue(strict["production_parser_delta"]["strict_go_parser_candidate"])
        self.assertEqual(strict["static_marker_position_bucket"], "first")
        self.assertEqual(strict["static_marker_relative_to_data_rows"], "before_all")
        self.assertEqual(strict["static_marker_keyword_flags"], {
            "source": False, "file": True, "current": False,
            "selected": False, "name": False, "debug": False,
            "info": False, "not": False, "loaded": False, "used": False,
            "active": False, "asterisk": False, "indicates": False,
            "path": False, "full": False,
        })
        self.assertEqual(strict["bullet_nonrow_token_categories"], [
            "punctuation", "alpha", "alpha", "punctuation"])
        self.assertEqual(strict["bullet_nonrow_token_length_buckets"], [
            "five_to_eight", "two_to_four", "five_to_eight", "five_to_eight"])
        self.assertEqual(strict["bullet_nonrow_second_token_kind"], "file")
        self.assertEqual(strict["bullet_nonrow_third_token_kind"], "names")
        self.assertEqual(strict["bullet_nonrow_legend_pair"], "file_names")
        self.assertEqual(strict["bullet_nonrow_first_decoration"], {
            "repeated_punctuation": True,
            "character_class": "hyphen",
            "count_bucket": "five_to_eight",
        })
        self.assertEqual(strict["bullet_nonrow_last_decoration"], {
            "repeated_punctuation": True,
            "character_class": "hyphen",
            "count_bucket": "five_to_eight",
        })
        self.assertTrue(strict["bullet_nonrow_decorations_equal"])
        self.assertTrue(strict["bullet_nonrow_finite_legend_match"])
        singular = reconlib.source_file_listing_grammar_signature(
            "--------  File name  --------\n0: C:\\fixture\\src\\main.c\n")
        self.assertFalse(singular["static_marker_recognized"])
        self.assertFalse(singular["strict_production_grammar_candidate"])
        padded = reconlib.source_file_listing_grammar_signature(
            " --------  File names  --------\n0: C:\\fixture\\src\\main.c\n")
        self.assertFalse(padded["static_marker_recognized"])
        self.assertFalse(padded["strict_production_grammar_candidate"])
        unknown_marker = reconlib.source_file_listing_grammar_signature(
            "0: C:\\fixture\\src\\main.c\n* Debug Info Used\n"
            "1: D:\\fixture\\src\\other.hpp\n")
        self.assertEqual(unknown_marker["bullet_nonrow_count"], 1)
        self.assertFalse(unknown_marker["bullet_nonrow_static_marker_recognized"])
        self.assertEqual(unknown_marker["bullet_nonrow_position_bucket"], "middle")
        self.assertEqual(unknown_marker["bullet_nonrow_relative_to_data_rows"], "between_rows")
        self.assertEqual(unknown_marker["bullet_nonrow_token_categories"], [
            "punctuation", "alpha", "alpha", "alpha"])
        self.assertEqual(unknown_marker["bullet_nonrow_token_length_buckets"], [
            "one", "five_to_eight", "two_to_four", "two_to_four"])
        self.assertEqual(unknown_marker["bullet_nonrow_keyword_flags"], {
            "source": False, "file": False, "current": False,
            "selected": False, "name": False, "debug": True,
            "info": True, "not": False, "loaded": False, "used": True,
            "active": False, "asterisk": True, "indicates": False,
            "path": False, "full": False,
        })
        self.assertFalse(unknown_marker["strict_production_grammar_candidate"])
        duplicate = reconlib.source_file_listing_grammar_signature(
            "0: C:\\fixture\\src\\main.c\n* Current source file\n"
            "0: D:\\fixture\\src\\other.hpp\n")
        self.assertFalse(duplicate["row_indices_zero_based_contiguous"])
        self.assertFalse(duplicate["strict_production_grammar_candidate"])
        production_delta = reconlib.source_file_listing_grammar_signature(
            "--------  File names  --------\n"
            "    0: C:\\fixture\\source code\\main.c\n"
            "    1: D:\\fixture\\src\\other.c\n")["production_parser_delta"]
        self.assertFalse(production_delta["strict_go_parser_candidate"])
        self.assertEqual(production_delta["row_payload_category_rows"], {
            "allowed_only": 1,
            "forward_slash": 0,
            "whitespace": 1,
            "other_ascii_punctuation": 0,
            "other_ascii_printable": 0,
            "control": 0,
            "non_ascii": 0,
        })
        self.assertNotIn("fixture", repr(production_delta))
        prefix_delta = reconlib.source_file_listing_grammar_signature(
            "--------  File names  --------\n"
            "  0: C:\\fixture\\src\\main.c\n")["production_parser_delta"]
        self.assertEqual(prefix_delta["row_prefix_leading_whitespace_rows"], {
            "none": 0, "space": 1, "tab": 0, "mixed": 0,
            "not_indexed": 0,
        })
        self.assertEqual(prefix_delta["row_prefix_delimiter_rows"], {
            "colon": 1, "other": 0, "missing": 0,
        })
        self.assertEqual(prefix_delta["row_prefix_post_colon_whitespace_rows"], {
            "single_space": 1, "multiple_space": 0, "tab": 0,
            "mixed": 0, "none": 0, "not_colon": 0,
        })
        self.assertEqual(prefix_delta["drive_absolute_payload_count"], 1)
        self.assertEqual(prefix_delta["prefix_leading_space_width_2_count"], 1)
        self.assertTrue(prefix_delta["prefix_leading_space_all_rows_same_width"])
        duplicate_delta = reconlib.source_file_listing_grammar_signature(
            "--------  File names  --------\n"
            "    0: C:\\fixture\\src\\main.c\n"
            "    1: C:\\fixture\\src\\main.c\n"
            "    2: c:\\fixture\\src\\MAIN.c\n")["production_parser_delta"]
        self.assertEqual(duplicate_delta["duplicate_canonical_path_row_count"], 2)
        self.assertEqual(duplicate_delta["duplicate_exact_path_row_count"], 1)
        self.assertEqual(duplicate_delta["duplicate_case_fold_path_row_count"], 1)
        self.assertEqual(duplicate_delta[
            "duplicate_separator_normalization_path_row_count"], 0)
        self.assertEqual(duplicate_delta[
            "duplicate_other_canonical_collision_row_count"], 0)
        self.assertFalse(duplicate_delta["duplicate_only_exact_path_rows"])
        self.assertTrue(duplicate_delta["strict_go_parser_candidate"])
        self.assertNotIn("fixture", repr(duplicate_delta))
        self.assertEqual(reconlib.source_file_listing_grammar_signature(
            "x\n" * 4097)["input_category"], "bounded_input_rejected")

    def test_m4_m5_template_phases_are_disabled_by_default(self):
        config_path = os.path.join(os.path.dirname(os.path.dirname(
            os.path.abspath(__file__))), "config.example.json")
        with open(config_path, "rb") as fh:
            config = json.loads(fh.read().decode("utf-8"))

        self.assertFalse(config["m4_execution_probe"]["enabled"])
        self.assertFalse(config["m5_read_probe"]["enabled"])
        self.assertFalse(config["m2_program_breakpoint_probe"]["enabled"])
        self.assertFalse(config["m2_owned_breakpoint_recovery_probe"]["enabled"])


if __name__ == "__main__":
    unittest.main()
