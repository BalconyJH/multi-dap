from __future__ import absolute_import

import json
import base64
import os
import shutil
import socket
import sys
import tempfile
import threading
import types
import unittest

from bridge.bridge import BoundedNDJSONReader, BridgeError, MAX_CONSOLE_OUTPUT_BYTES, MAX_CONSOLE_PANE_BYTES, MAX_DISASSEMBLE_BYTES, MBPServer, MultiBridge, decode_frame, encode_frame, validate_rpc_host


class FakeWindow(object):
    def __init__(self, program=u"C:\\fixture\\app.elf", status=2, process_info=None):
        self.calls = []
        self.cmdExecOutput = b""
        self.cmdExecStatus = 1
        self.command_output = b"command output"
        self.command_status = 1
        self.pane_output = {"target": b"target output\n", "io": b"io output\n"}
        self.memory_output = b""
        self.run_commands_error = None
        self.program = program
        self.process_info = {"snapshot_token": "fixture-stable", "state": "stopped"} if process_info is None else process_info
        self.status = status
        self.status_sequence = []

    def RunCommands(self, command, block, print_output, keep_raw_output=None):
        self.calls.append(("RunCommands", command, block, print_output, keep_raw_output))
        if self.run_commands_error is not None:
            raise self.run_commands_error
        self.cmdExecOutput = self.command_output
        self.cmdExecStatus = self.command_status
        if command.startswith("savedebugpane "):
            parts = command.split("\"")
            if len(parts) != 3:
                raise RuntimeError("invalid savedebugpane fixture command")
            pane = command.split()[1]
            with open(parts[1], "wb") as output:
                output.write(self.pane_output[pane])
        if " memdump -noprogress raw " in command:
            parts = command.split("\"")
            if len(parts) != 3:
                raise RuntimeError("invalid memdump fixture command")
            with open(parts[1], "wb") as output:
                output.write(self.memory_output)
        return True

    def GetCurPrInfo(self, options):
        self.calls.append(("GetCurPrInfo", options))
        return self.process_info

    def GetProgram(self):
        self.calls.append(("GetProgram",))
        return self.program

    def GetStatus(self):
        self.calls.append(("GetStatus",))
        if self.status_sequence:
            return self.status_sequence.pop(0)
        return self.status

    def Resume(self, block, print_output):
        self.calls.append(("Resume", block, print_output))
        return True

    def Halt(self, block, print_output):
        self.calls.append(("Halt", block, print_output))
        return True

    def Step(self, block, print_output, step_into_func):
        self.calls.append(("Step", block, print_output, step_into_func))
        return True

    def Next(self, block, print_output):
        self.calls.append(("Next", block, print_output))
        return True


class FakeDebugger(object):
    def __init__(self, window):
        self.window = window
        self.calls = []
        self.disconnect_error = None

    def DebugProgram(self, project, new_window, block, print_output, expand_filename):
        self.calls.append(("DebugProgram", project, new_window, block, print_output, expand_filename))
        return self.window

    def ConnectToTarget(self, connection, setup_script, setup_script_args, multi_log, stick, more_options, print_output):
        self.calls.append(("ConnectToTarget", connection, setup_script, setup_script_args, multi_log, stick, more_options, print_output))
        return object()

    def Disconnect(self, print_output):
        self.calls.append(("Disconnect", print_output))
        if self.disconnect_error is not None:
            raise self.disconnect_error


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


class BridgeTest(unittest.TestCase):
    def setUp(self):
        self.window = FakeWindow()
        self.debugger = FakeDebugger(self.window)
        self.bridge = MultiBridge(lambda: self.debugger)
        self.old_modules = dict((name, sys.modules.get(name))
                                for name in ("ghs_constants", "ghs_winreg"))
        constants = types.ModuleType("ghs_constants")
        constants.winClassNames = type("Classes", (), {"debugger": "debugger"})()
        winreg = types.ModuleType("ghs_winreg")
        winreg.GHS_WindowRegister = FakeRegister
        sys.modules["ghs_constants"] = constants
        sys.modules["ghs_winreg"] = winreg

    def tearDown(self):
        for name, previous in self.old_modules.items():
            if previous is None:
                del sys.modules[name]
            else:
                sys.modules[name] = previous

    def open_bridge(self):
        return self.bridge.open({"mode": "cold", "project": "project.ghsmc", "connection": "target-server"})

    def test_open_uses_window_and_stick_to_debugger(self):
        self.assertEqual({"opened": True}, self.open_bridge())
        self.assertEqual(
            ("DebugProgram", "project.ghsmc", 0, 1, 0, 1), self.debugger.calls[0]
        )
        self.assertEqual(1, self.debugger.calls[1][5])

    def test_cold_open_prepares_an_already_present_program_after_connecting(self):
        self.assertEqual({"opened": True}, self.bridge.open({
            "mode": "cold", "project": "project.ghsmc", "connection": "target-server",
            "preparation": "already_present_no_verify",
        }))
        self.assertEqual(
            ("ConnectToTarget", "target-server", u"", u"", u"", 1, u"", 0),
            self.debugger.calls[1],
        )
        self.assertEqual(
            [("RunCommands", "prepare_target -verify=none", True, False, None)],
            self.window.calls,
        )

    def test_cold_open_rejects_unknown_or_refused_preparation_without_raw_output(self):
        with self.assertRaises(BridgeError) as raised:
            self.bridge.open({
                "mode": "cold", "project": "project.ghsmc", "connection": "target-server",
                "preparation": "download",
            })
        self.assertEqual("invalid_params", raised.exception.kind)
        self.assertEqual([], self.debugger.calls)

        self.window.command_status = 0
        self.window.RunCommands = lambda command, block, print_output: False
        with self.assertRaises(BridgeError) as raised:
            self.bridge.open({
                "mode": "cold", "project": "project.ghsmc", "connection": "target-server",
                "preparation": "already_present_no_verify",
            })
        self.assertEqual("multi_refused", raised.exception.kind)
        self.assertEqual("MULTI could not prepare target", raised.exception.message)
        self.assertEqual([
            ("DebugProgram", "project.ghsmc", 0, 1, 0, 1),
            ("ConnectToTarget", "target-server", u"", u"", u"", 1, u"", 0),
            ("Disconnect", 0),
        ], self.debugger.calls)
        self.assertIsNone(self.bridge.debugger)
        self.assertIsNone(self.bridge.window)
        self.assertFalse(self.bridge.disconnect_on_close)

    def test_cold_open_disconnects_when_preparation_raises(self):
        self.window.run_commands_error = RuntimeError("fixture preparation failure")
        with self.assertRaises(BridgeError) as raised:
            self.bridge.open({
                "mode": "cold", "project": "project.ghsmc", "connection": "target-server",
                "preparation": "already_present_no_verify",
            })
        self.assertEqual("multi_refused", raised.exception.kind)
        self.assertEqual("MULTI could not prepare target", raised.exception.message)
        self.assertEqual([
            ("DebugProgram", "project.ghsmc", 0, 1, 0, 1),
            ("ConnectToTarget", "target-server", u"", u"", u"", 1, u"", 0),
            ("Disconnect", 0),
        ], self.debugger.calls)
        self.assertEqual(
            [("RunCommands", "prepare_target -verify=none", True, False, None)],
            self.window.calls)
        self.assertIsNone(self.bridge.window)

    def test_cold_open_reports_cleanup_failure_without_publishing_session(self):
        self.window.RunCommands = lambda command, block, print_output: False
        self.debugger.disconnect_error = RuntimeError("fixture disconnect failure")
        with self.assertRaises(BridgeError) as raised:
            self.bridge.open({
                "mode": "cold", "project": "project.ghsmc", "connection": "target-server",
                "preparation": "already_present_no_verify",
            })
        self.assertEqual("cold_cleanup_failed", raised.exception.kind)
        self.assertEqual("MULTI could not prepare target or disconnect", raised.exception.message)
        self.assertEqual([
            ("DebugProgram", "project.ghsmc", 0, 1, 0, 1),
            ("ConnectToTarget", "target-server", u"", u"", u"", 1, u"", 0),
            ("Disconnect", 0),
        ], self.debugger.calls)
        self.assertIsNone(self.bridge.debugger)
        self.assertIsNone(self.bridge.window)
        self.assertFalse(self.bridge.disconnect_on_close)

    def test_cold_open_does_not_disconnect_an_unconfirmed_connection(self):
        self.debugger.ConnectToTarget = lambda *args: False
        with self.assertRaises(BridgeError) as raised:
            self.bridge.open({
                "mode": "cold", "project": "project.ghsmc", "connection": "target-server",
                "preparation": "already_present_no_verify",
            })
        self.assertEqual("multi_refused", raised.exception.kind)
        self.assertEqual("MULTI could not connect to target", raised.exception.message)
        self.assertEqual(
            [("DebugProgram", "project.ghsmc", 0, 1, 0, 1)], self.debugger.calls)
        self.assertIsNone(self.bridge.window)

    def test_warm_open_binds_one_exact_active_window_without_connecting(self):
        matching = FakeWindow(program=u"c:/fixture/app.elf", status=3)
        other = FakeWindow(program=u"C:\\fixture\\other.elf")
        FakeRegister.windows = [other, matching]
        bridge = MultiBridge(lambda: self.debugger, session_mode=u"warm")

        self.assertEqual({"opened": True}, bridge.open({
            "mode": "warm", "primary_elf": u"C:\\fixture\\app.elf",
        }))
        self.assertEqual([], self.debugger.calls)
        self.assertEqual([("GetProgram",)], other.calls)
        self.assertEqual(
            [("GetProgram",), ("GetStatus",), ("GetCurPrInfo", u"")],
            matching.calls,
        )
        self.assertEqual({"closed": True}, bridge.close({}))
        self.assertEqual([], self.debugger.calls)

    def test_warm_open_fails_closed_for_nonunique_or_unstable_candidates(self):
        primary = u"C:\\fixture\\app.elf"
        bridge = MultiBridge(lambda: self.debugger, session_mode=u"warm")
        FakeRegister.windows = [FakeWindow(primary), FakeWindow(primary)]
        with self.assertRaises(BridgeError) as raised:
            bridge.open({"mode": "warm", "primary_elf": primary})
        self.assertEqual("warm_bind_failed", raised.exception.kind)

        FakeRegister.windows = [FakeWindow(primary, status=4), FakeWindow(u"C:\\fixture\\other.elf")]
        with self.assertRaises(BridgeError) as raised:
            bridge.open({"mode": "warm", "primary_elf": primary})
        self.assertEqual("warm_bind_failed", raised.exception.kind)
        self.assertEqual([], self.debugger.calls)

    def test_warm_open_rejects_basename_relative_path_and_cold_fields(self):
        bridge = MultiBridge(lambda: self.debugger, session_mode=u"warm")
        for params in (
                {"mode": "warm", "primary_elf": "primary.elf"},
                {"mode": "warm", "primary_elf": "C:\\fixture\\app.elf", "project": "fixture.ghsmc"},
                {"mode": "warm", "primary_elf": "C:\\fixture\\app.elf", "preparation": "already_present_no_verify"},
                {"mode": "cold", "project": "project.ghsmc", "connection": "target"}):
            with self.assertRaises(BridgeError):
                bridge.open(params)
        self.assertEqual([], self.debugger.calls)

    def test_open_requires_a_matching_explicit_session_mode(self):
        with self.assertRaises(BridgeError):
            self.bridge.open({"project": "project.ghsmc", "connection": "target"})
        with self.assertRaises(BridgeError):
            self.bridge.open({"mode": "warm", "primary_elf": "C:\\fixture\\app.elf"})

    def test_execution_targets_debug_program_window_nonblocking(self):
        self.open_bridge()
        self.assertEqual({"accepted": True}, self.bridge.resume({}))
        self.assertEqual({"accepted": True}, self.bridge.halt({}))
        self.assertEqual(("Resume", 0, 0), self.window.calls[0])
        self.assertEqual(("Halt", 0, 0), self.window.calls[1])

    def test_step_in_and_next_target_debug_program_window_nonblocking(self):
        self.open_bridge()
        self.assertEqual({"accepted": True}, self.bridge.step_in({}))
        self.assertEqual({"accepted": True}, self.bridge.next({}))
        self.assertEqual(("Step", 0, 0, 1), self.window.calls[0])
        self.assertEqual(("Next", 0, 0), self.window.calls[1])

    def test_state_combines_status_and_process_information(self):
        self.open_bridge()
        result = self.bridge.state({})
        self.assertEqual({"status": 2, "process_info": self.window.process_info}, result)
        self.assertEqual([("GetStatus",), ("GetCurPrInfo", u""), ("GetStatus",)], self.window.calls)

    def test_state_rejects_a_torn_snapshot_without_retrying(self):
        self.open_bridge()
        self.window.status_sequence = [2, 3]
        with self.assertRaises(BridgeError) as raised:
            self.bridge.state({})
        self.assertEqual("state_changed", raised.exception.kind)
        self.assertEqual("MULTI state changed during snapshot", raised.exception.message)
        self.assertEqual([("GetStatus",), ("GetCurPrInfo", u""), ("GetStatus",)], self.window.calls)

    def test_rpc_host_accepts_only_numeric_loopback_literals(self):
        self.assertEqual("127.0.0.1", validate_rpc_host("127.0.0.1"))
        self.assertEqual("127.0.0.2", validate_rpc_host("127.0.0.2"))
        self.assertEqual("::1", validate_rpc_host("::1"))
        for host in (
                "localhost", "0.0.0.0", "192.0.2.1", "127.0.0.01",
                "::", "0:0:0:0:0:0:0:1", "::ffff:127.0.0.1"):
            with self.assertRaises(ValueError):
                validate_rpc_host(host)

    def test_cores_and_escape_commands_use_raw_command_output(self):
        self.open_bridge()
        cores = self.bridge.cores({})
        self.assertEqual("command output", cores["components"])
        self.assertEqual(("RunCommands", u"components", 1, 0, 1), self.window.calls[0])
        run = self.bridge.run_commands({"commands": "P"})
        self.assertEqual("command output", run["raw"])
        self.assertEqual(1, run["status"])

    def test_unverified_methods_and_unsafe_commands_are_refused(self):
        self.open_bridge()
        with self.assertRaises(BridgeError):
            self.bridge.dispatch("download", {"image": "image.abs"})
        with self.assertRaises(BridgeError):
            self.bridge.dispatch("reset", {})
        with self.assertRaises(BridgeError):
            self.bridge.run_commands({"commands": "help"})
        with self.assertRaises(BridgeError):
            self.bridge.run_commands({"commands": "prepare_target -verify=none"})

    def test_invalid_multi_bytes_are_marked_lossy(self):
        self.open_bridge()
        self.window.command_output = b"\xff"
        result = self.bridge.run_commands({"commands": "P"})
        self.assertTrue(result["raw_lossy"])
        self.assertEqual(u"\ufffd", result["raw"])

    def test_console_reads_incremental_target_and_io_panes_without_command_output(self):
        self.open_bridge()
        self.window.command_output = b"must not be used"
        first = self.bridge.console_read({})
        self.assertEqual(u"target output\n", first["server"])
        self.assertEqual(u"io output\n", first["io"])
        self.assertFalse(os.path.exists(self.bridge.console_paths["server"]))
        self.assertFalse(os.path.exists(self.bridge.console_paths["io"]))
        self.assertEqual([
            ("RunCommands", self.window.calls[0][1], 1, 0, None),
            ("RunCommands", self.window.calls[1][1], 1, 0, None),
        ], self.window.calls)
        self.assertTrue(self.window.calls[0][1].startswith(u"savedebugpane target \""))
        self.assertTrue(self.window.calls[1][1].startswith(u"savedebugpane io \""))

        self.window.pane_output["target"] += b"more\n"
        self.window.pane_output["io"] = b""
        second = self.bridge.console_read({})
        self.assertEqual(u"more\n", second["server"])
        self.assertEqual(u"", second["io"])
        self.window.pane_output["target"] = b"after rollover\n"
        third = self.bridge.console_read({})
        self.assertEqual(u"after rollover\n", third["server"])
        paths = dict(self.bridge.console_paths)
        self.assertEqual({"closed": True}, self.bridge.close({}))
        self.assertFalse(os.path.exists(paths["server"]))
        self.assertFalse(os.path.exists(paths["io"]))

    def test_console_marks_invalid_pane_bytes_lossy_and_refuses_failed_snapshot(self):
        self.open_bridge()
        self.window.pane_output["target"] = b"\xff"
        result = self.bridge.console_read({})
        self.assertTrue(result["raw_lossy"])
        self.assertEqual(u"\ufffd", result["server"])

        self.window.command_status = 0
        with self.assertRaises(BridgeError) as raised:
            self.bridge.console_read({})
        self.assertEqual("multi_refused", raised.exception.kind)

    def test_console_large_pane_forwards_each_append_once_and_handles_rollover(self):
        self.open_bridge()
        self.window.pane_output["target"] = b"a" * (MAX_CONSOLE_PANE_BYTES * 3)
        self.window.pane_output["io"] = b""
        first = self.bridge.console_read({})
        self.assertEqual(MAX_CONSOLE_PANE_BYTES, len(first["server"].encode("utf-8")))
        self.assertTrue(first["truncated"])
        self.assertLessEqual(
            len(first["server"].encode("utf-8")) + len(first["io"].encode("utf-8")),
            MAX_CONSOLE_OUTPUT_BYTES,
        )

        self.window.pane_output["target"] += b"-one"
        second = self.bridge.console_read({})
        self.assertEqual(u"-one", second["server"])
        self.window.pane_output["target"] += b"-two"
        third = self.bridge.console_read({})
        self.assertEqual(u"-two", third["server"])

        self.window.pane_output["target"] = b""
        self.assertEqual(u"", self.bridge.console_read({})["server"])
        self.window.pane_output["target"] = b"new pane\n"
        self.assertEqual(u"new pane\n", self.bridge.console_read({})["server"])

    def test_console_same_size_same_prefix_with_changed_tail_is_rollover(self):
        self.open_bridge()
        self.window.pane_output["target"] = b"a" * 256 + b"b" * 1024
        self.window.pane_output["io"] = b""
        self.bridge.console_read({})
        self.window.pane_output["target"] = b"a" * 256 + b"c" * 1024
        result = self.bridge.console_read({})
        self.assertEqual(u"a" * 256 + u"c" * 1024, result["server"])

    def test_console_refuses_oversize_snapshot(self):
        self.open_bridge()
        self.window.pane_output["target"] = b"a" * ((1 << 20) + 1)
        with self.assertRaises(BridgeError) as raised:
            self.bridge.console_read({})
        self.assertEqual("console_unavailable", raised.exception.kind)

    def test_console_read_failure_does_not_advance_increment_baseline(self):
        self.open_bridge()
        self.window.pane_output["target"] = b"before\n"
        self.window.pane_output["io"] = b""
        self.assertEqual(u"before\n", self.bridge.console_read({})["server"])
        self.window.pane_output["target"] += b"after\n"
        read_range = self.bridge._read_console_range
        self.bridge._read_console_range = lambda *args: (_ for _ in ()).throw(
            BridgeError("console_unavailable", "console snapshot cannot be read"))
        with self.assertRaises(BridgeError) as raised:
            self.bridge.console_read({})
        self.assertEqual("console_unavailable", raised.exception.kind)
        self.bridge._read_console_range = read_range
        recovered = self.bridge.console_read({})
        self.assertEqual(u"after\n", recovered["server"])

    def test_console_reset_forgets_only_increment_cursors(self):
        self.open_bridge()
        self.window.pane_output["target"] = b"existing target\n"
        self.window.pane_output["io"] = b"existing io\n"
        self.bridge.console_read({})
        calls_before_reset = list(self.window.calls)

        self.assertEqual({"reset": True}, self.bridge.console_reset({}))
        self.assertEqual(calls_before_reset, self.window.calls)
        self.assertEqual({"server": None, "io": None}, self.bridge.console_previous)

        current = self.bridge.console_read({})
        self.assertEqual(u"existing target\n", current["server"])
        self.assertEqual(u"existing io\n", current["io"])

    def test_console_reset_requires_open_session_and_exact_empty_object(self):
        for params in (None, [], {"unexpected": True}):
            with self.assertRaises(BridgeError) as raised:
                self.bridge.console_reset(params)
            self.assertEqual("invalid_params", raised.exception.kind)
        with self.assertRaises(BridgeError) as raised:
            self.bridge.console_reset({})
        self.assertEqual("session_not_open", raised.exception.kind)

    def test_memory_read_uses_numeric_parameters_and_deletes_snapshot(self):
        self.open_bridge()
        self.window.memory_output = b"\x00\x01\xfe\xff"
        result = self.bridge.memory_read({
            "component": "debugger.name.4", "address": 0x10, "byte_count": 4,
        })
        self.assertEqual(base64.b64encode(self.window.memory_output).decode("ascii"), result["data"])
        self.assertEqual(4, result["byte_count"])
        self.assertEqual(1, result["status"])
        command = self.window.calls[-1][1]
        self.assertTrue(command.startswith('route debugger.name.4 memdump -noprogress raw "'))
        self.assertTrue(command.endswith('" 0x10 4'))
        self.assertFalse(os.path.exists(self.bridge._inspection_file()))

    def test_memory_read_rejects_unsafe_or_wrong_length_requests(self):
        self.open_bridge()
        self.window.memory_output = b"\x00"
        for params in (
                {"component": "debugger.name.4; H", "address": 0, "byte_count": 1},
                {"component": "debugger.name.4", "address": "0x0", "byte_count": 1},
                {"component": "debugger.name.4", "address": 0, "byte_count": 0},
                {"component": "debugger.name.4", "address": 0, "byte_count": (64 * 1024) + 1}):
            with self.assertRaises(BridgeError) as raised:
                self.bridge.memory_read(params)
            self.assertEqual("invalid_params", raised.exception.kind)
        with self.assertRaises(BridgeError) as raised:
            self.bridge.memory_read({"component": "debugger.name.4", "address": 0, "byte_count": 2})
        self.assertEqual("memory_unavailable", raised.exception.kind)

    def test_memory_read_reuses_launcher_workspace_without_removing_it(self):
        workspace = tempfile.mkdtemp(prefix="multi-dap-bridge-test-")
        try:
            bridge = MultiBridge(lambda: self.debugger, console_directory=workspace)
            self.assertEqual({"opened": True}, bridge.open({
                "mode": "cold", "project": "project.ghsmc", "connection": "target-server",
            }))
            self.window.memory_output = b"\xaa"
            bridge.memory_read({
                "component": "debugger.name.4", "address": 0, "byte_count": 1,
            })
            self.assertFalse(os.path.exists(os.path.join(workspace, "memory.bin")))
            self.assertEqual({"closed": True}, bridge.close({}))
            self.assertTrue(os.path.isdir(workspace))
        finally:
            shutil.rmtree(workspace, ignore_errors=True)

    def test_disassemble_is_component_routed_and_bounded(self):
        self.open_bridge()
        self.window.command_output = b"0\tjr reset\n0x4\tnop\n"
        result = self.bridge.disassemble({
            "component": "debugger.name.4", "address": 0, "byte_count": 12,
        })
        self.assertEqual(u"0\tjr reset\n0x4\tnop\n", result["raw"])
        self.assertEqual('route debugger.name.4 disassemble 0x0 12', self.window.calls[-1][1])
        with self.assertRaises(BridgeError) as raised:
            self.bridge.disassemble({
                "component": "debugger.name.4", "address": 0, "byte_count": MAX_DISASSEMBLE_BYTES + 1,
            })
        self.assertEqual("invalid_params", raised.exception.kind)

    def test_server_handshake_request_response_and_strict_error_response(self):
        server = MBPServer(self.bridge, max_message_size=4096)
        client, peer = socket.socketpair()
        thread = threading.Thread(target=server.handle_connection, args=(peer,))
        thread.start()
        reader = BoundedNDJSONReader(client, 4096)
        handshake = decode_frame(reader.read())
        self.assertEqual(1, handshake["protocol_version"])
        client.sendall(encode_frame({
            "id": 6,
            "method": "open",
            "params": {"mode": "cold", "project": "project.ghsmc", "connection": "target-server"},
        }))
        opened = decode_frame(reader.read())
        self.assertEqual({"id": 6, "ok": True, "result": {"opened": True}}, opened)
        client.sendall(encode_frame({"id": 7, "method": "unknown", "params": {}}))
        response = decode_frame(reader.read())
        self.assertEqual(7, response["id"])
        self.assertFalse(response["ok"])
        self.assertEqual("unknown_method", response["error"]["kind"])
        self.assertEqual("method is not allowed", response["error"]["message"])
        self.assertEqual("", response["error"]["raw"])
        client.close()
        thread.join(1)
        peer.close()

    def test_reader_bounds_an_unterminated_frame(self):
        left, right = socket.socketpair()
        reader = BoundedNDJSONReader(left, 8)
        right.sendall(b"123456789")
        with self.assertRaises(BridgeError):
            reader.read()
        left.close()
        right.close()

    def test_unexpected_exception_detail_is_not_serialized(self):
        class FailingBridge(object):
            def dispatch(self, method, params):
                raise ValueError("sensitive target detail")

        response = MBPServer(FailingBridge()).process_request({
            "id": 9, "method": "state", "params": {},
        })
        self.assertFalse(response["ok"])
        self.assertEqual("unexpected bridge exception", response["error"]["message"])
        self.assertEqual("", response["error"]["raw"])
        self.assertNotIn("raw_lossy", response["error"])
        self.assertNotIn(b"sensitive target detail", encode_frame(response))

    def test_bridge_error_raw_field_is_always_empty(self):
        self.open_bridge()
        self.window.command_output = b"private command diagnostic"
        self.window.command_status = 0
        response = MBPServer(self.bridge).process_request({
            "id": 10, "method": "run_commands", "params": {"commands": "P"},
        })
        self.assertEqual({
            "kind": "multi_refused",
            "message": "MULTI refused command",
            "raw": "",
        }, response["error"])
        self.assertNotIn(b"private command diagnostic", encode_frame(response))

    def test_exception_length_cannot_expand_error_response(self):
        class FailingBridge(object):
            def __init__(self, detail):
                self.detail = detail

            def dispatch(self, method, params):
                raise ValueError(self.detail)

        request = {"id": 11, "method": "state", "params": {}}
        short_frame = encode_frame(MBPServer(FailingBridge("x")).process_request(request))
        long_frame = encode_frame(MBPServer(FailingBridge("private:" + "x" * (2 << 20))).process_request(request))
        self.assertEqual(len(short_frame), len(long_frame))
        self.assertLess(len(long_frame), 256)
        self.assertNotIn(b"private:", long_frame)

    def test_request_controlled_names_are_not_reflected_in_error_messages(self):
        marker = "private-connection-argument"
        server = MBPServer(self.bridge)
        responses = (
            server.process_request({"id": 12, "method": marker, "params": {}}),
            server.process_request({
                "id": 13,
                "method": "open",
                "params": {
                    "mode": "cold",
                    "project": "fixture.ghsmc",
                    "connection": "fixture-connection",
                    marker: "value",
                },
            }),
        )
        self.assertEqual("method is not allowed", responses[0]["error"]["message"])
        self.assertEqual(
            "request contains an unexpected parameter",
            responses[1]["error"]["message"],
        )
        for response in responses:
            self.assertNotIn(marker.encode("ascii"), encode_frame(response))

    def test_server_publishes_bound_ephemeral_port_before_accept(self):
        server = MBPServer(self.bridge)
        temporary = tempfile.mkdtemp()
        ready_file = os.path.join(temporary, "ready.json")
        thread = threading.Thread(target=server.serve, args=(0, "127.0.0.1", ready_file))
        thread.start()
        for _ in range(100):
            if os.path.isfile(ready_file):
                break
            threading.Event().wait(0.01)
        self.assertTrue(os.path.isfile(ready_file))
        with open(ready_file, "rb") as input_file:
            ready = json.loads(input_file.read().decode("utf-8"))
        self.assertEqual("127.0.0.1", ready["host"])
        self.assertTrue(0 < ready["port"] <= 65535)
        client = socket.create_connection((ready["host"], ready["port"]))
        client.close()
        thread.join(1)
        self.assertFalse(thread.is_alive())
        os.unlink(ready_file)
        os.rmdir(temporary)


if __name__ == "__main__":
    unittest.main()
