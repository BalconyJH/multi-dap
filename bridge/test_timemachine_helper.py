"""Host-only worker protocol tests; all native operations use fakes."""

import io
import json
import os
from pathlib import Path
import queue
import subprocess
import sys
import tempfile
import threading
import time
import unittest

from bridge import timemachine_helper as helper
from bridge.timemachine_native import NativeError


def request(seq, op, args=None):
    return (json.dumps({"v": 1, "seq": seq, "op": op, "args": {} if args is None else args},
                       separators=(",", ":")) + "\n").encode("utf-8")


def open_args():
    return {
        "installation": os.path.abspath("synthetic-installation"),
        "multi_version": "8.1.4", "dll_sha256": "0" * 64, "session_id": 7,
    }


def messages(destination):
    data = destination.getvalue()
    return [json.loads(line) for line in data.splitlines() if line]


def wait_for(predicate, timeout=2):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.005)
    raise AssertionError("bounded test wait expired")


class FakeAPI:
    def __init__(self, *_args):
        self.owner = threading.get_ident()
        self.calls = []
        self.count = 8
        self.pending = []
        self.dropped = 0
        self.ignored = 0
        self.callback_failed = False
        self.invalidated = False
        self.read_hook = None
        self.start_callback = False
        self.stop_callback = False
        self.fail_op = None
        self.fail_after = 0
        self.failure = NativeError("PRIVATE-NATIVE-ERROR")
        self.info_size = 64
        self.info_byte = 0

    def record(self, op):
        if threading.get_ident() != self.owner:
            raise AssertionError("native owner thread changed")
        self.calls.append((op, threading.get_ident()))
        if op == self.fail_op and sum(name == op for name, _ in self.calls) > self.fail_after:
            raise self.failure

    def initialize(self):
        self.record("initialize")

    def connect(self, session):
        self.record("connect")
        if session != 7:
            raise AssertionError("unexpected fake session")

    def get_info(self):
        self.record("get_info")
        return bytes([self.info_byte]) + bytes(self.info_size - 1)

    def packet_count(self):
        self.record("packet_count")
        return self.count

    def get_packets(self, start, count):
        self.record("get_packets")
        if self.read_hook is not None:
            self.read_hook(self)
        return b"\x0a" + bytes(31) + bytes((count - 1) * 32), count

    def poll_events(self):
        self.record("poll_events")

    def drain_notifications(self):
        self.record("drain_notifications")
        result = {
            "events": self.pending, "dropped": self.dropped,
            "ignored_callbacks": self.ignored, "callback_failed": self.callback_failed,
            "generation_invalidated": self.invalidated,
        }
        self.pending = []
        self.dropped = self.ignored = 0
        self.callback_failed = self.invalidated = False
        return result

    def start_trace(self, authorized=False):
        self.record("start_trace")
        if authorized is not True:
            raise AssertionError("missing authorization")
        if self.start_callback:
            self.pending.append("trace_start")

    def stop_trace(self, authorized=False):
        self.record("stop_trace")
        if authorized is not True:
            raise AssertionError("missing authorization")
        if self.stop_callback:
            self.pending.append("trace_stop")

    def close(self):
        self.record("close")


class Factory:
    def __init__(self, configure=None):
        self.instances = []
        self.configure = configure

    def __call__(self, *args):
        api = FakeAPI(*args)
        if self.configure is not None:
            self.configure(api)
        self.instances.append(api)
        return api


class BlockingInput:
    def __init__(self):
        self.lines = queue.Queue()

    def readline(self, _limit):
        return self.lines.get()

    def send(self, value):
        self.lines.put(value)


class SynchronizedOutput:
    def __init__(self, partial=False):
        self.data = bytearray()
        self.lock = threading.Lock()
        self.partial = partial

    def write(self, data):
        if self.partial:
            data = data[:3]
        with self.lock:
            self.data.extend(data)
        return len(data)

    def flush(self):
        pass

    def getvalue(self):
        with self.lock:
            return bytes(self.data)


class SlowOutput(SynchronizedOutput):
    def __init__(self):
        super().__init__()
        self.block = threading.Event()
        self.release = threading.Event()
        self.waiting = threading.Event()

    def write(self, data):
        if self.block.is_set():
            self.waiting.set()
            self.release.wait(3)
        return super().write(data)


class HelperProtocolTests(unittest.TestCase):
    def test_startup_and_empty_eof_do_not_create_a_native_api(self):
        factory, output = Factory(), io.BytesIO()
        code = helper.serve(io.BytesIO(), output, factory)
        self.assertEqual(0, code)
        self.assertEqual([], factory.instances)
        wire = messages(output)
        self.assertEqual(1, len(wire))
        self.assertEqual("ready", wire[0]["event"])
        self.assertEqual(helper.PROTOCOL, wire[0]["protocol"])
        self.assertEqual({"v", "event", "protocol", "python_major", "python_minor", "pointer_bits"},
                         set(wire[0]))

    def test_direct_script_runs_isolated_from_an_unrelated_directory(self):
        script = Path(helper.__file__).resolve()
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run(
                [sys.executable, "-I", "-S", "-B", "-u", str(script)],
                cwd=directory, input=b"", stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=5)
        self.assertEqual(0, result.returncode)
        self.assertEqual(b"", result.stderr)
        self.assertEqual("ready", json.loads(result.stdout)["event"])

    def test_ordered_responses_and_raw_packet_batch(self):
        source = io.BytesIO(
            request(1, "open", open_args()) + request(2, "status") +
            request(3, "read", {"start": "0", "count": 2, "generation": "1"}) +
            request(4, "close"))
        factory, output = Factory(), SynchronizedOutput(partial=True)
        code = helper.serve(source, output, factory)
        self.assertEqual(0, code)
        wire = messages(output)
        self.assertEqual([1, 2, 3, 4], [entry["seq"] for entry in wire[1:]])
        self.assertTrue(all(entry["ok"] for entry in wire[1:]))
        self.assertEqual("1", wire[1]["result"]["generation"])
        self.assertEqual("8", wire[1]["result"]["packet_count"])
        self.assertEqual(128, len(wire[1]["result"]["info_hex"]))
        self.assertEqual("unknown", wire[1]["result"]["trace_state"])
        self.assertEqual({
            "generation": "1", "start": "0", "count": 2,
            "packets_hex": ["0a" + "00" * 31, "00" * 32],
        }, wire[3]["result"])
        self.assertEqual({"closed": True}, wire[4]["result"])
        names = [name for name, _ in factory.instances[0].calls]
        self.assertEqual(1, names.count("close"))
        self.assertNotIn("start_trace", names)
        self.assertNotIn("stop_trace", names)

    def test_no_implicit_disconnect_or_trace_stop_on_parent_eof(self):
        factory, output = Factory(), io.BytesIO()
        self.assertEqual(0, helper.serve(io.BytesIO(request(1, "open", open_args())), output, factory))
        names = [name for name, _ in factory.instances[0].calls]
        self.assertNotIn("close", names)
        self.assertNotIn("stop_trace", names)

    def test_invalid_envelopes_are_terminal_and_never_reflect_private_input(self):
        bodies = (
            b'{"v":1,"seq":1,"op":"status","args":{},"PRIVATE":1}\n',
            b'{"v":1,"seq":1,"op":"status","args":{},"seq":2}\n',
            b'{"v":1,"seq":1,"op":"status","args":{"PRIVATE":1,"PRIVATE":2}}\n',
            b'{"v":true,"seq":1,"op":"status","args":{}}\n',
            b'{"v":1,"Seq":1,"op":"status","args":{}}\n',
            b'{"v":1,"seq":1,"op":"status","args":null}\n',
            b'{"v":1,"seq":1,"op":"status","args":{}} null\n',
            b'{"v":1,"seq":1,"op":"status","args":{"PRIVATE":NaN}}\n',
            b'{"v":1,"seq":1,"op":"\xff","args":{}}\n',
            b'null\n',
        )
        for data in bodies:
            with self.subTest(data=data[:20]):
                factory, output = Factory(), io.BytesIO()
                self.assertEqual(1, helper.serve(io.BytesIO(data), output, factory))
                self.assertEqual([], factory.instances)
                self.assertEqual({"v": 1, "event": "fatal", "error": {"code": "invalid_request"}},
                                 messages(output)[-1])
                self.assertNotIn(b"PRIVATE", output.getvalue())

    def test_input_sequence_is_monotonic_and_no_boolean_aliases_are_accepted(self):
        for sequence in (0, True, 1.0, 1 << 31):
            output = io.BytesIO()
            self.assertEqual(1, helper.serve(io.BytesIO(request(sequence, "status")), output, Factory()))
            self.assertEqual("invalid_request", messages(output)[-1]["error"]["code"])
        output = io.BytesIO()
        self.assertEqual(1, helper.serve(io.BytesIO(request(1, "status") + request(1, "status")), output, Factory()))
        self.assertEqual("not_open", messages(output)[1]["error"]["code"])
        self.assertEqual("invalid_request", messages(output)[2]["error"]["code"])

    def test_bad_frames_are_bounded_and_partial_eof_is_not_valid_json(self):
        for data, expected in (
            (b"x" * (helper.MAX_REQUEST_BYTES + 1) + b"\n", "request_too_large"),
            (request(1, "status")[:-1], "request_truncated"),
        ):
            factory, output = Factory(), io.BytesIO()
            self.assertEqual(1, helper.serve(io.BytesIO(data), output, factory))
            self.assertEqual(expected, messages(output)[-1]["error"]["code"])
            self.assertEqual([], factory.instances)

    def test_exact_body_limit_accepts_crlf(self):
        line = request(1, "status")[:-1]
        line += b" " * (helper.MAX_REQUEST_BYTES - len(line)) + b"\r\n"
        output = io.BytesIO()
        self.assertEqual(0, helper.serve(io.BytesIO(line), output, Factory()))
        self.assertEqual("not_open", messages(output)[1]["error"]["code"])

    def test_invalid_operation_arguments_never_reach_native_control(self):
        factory = Factory()
        session = helper.HelperSession(factory)
        session.dispatch("open", open_args())
        before = len(factory.instances[0].calls)
        for op, args, code in (
            ("start", {}, "invalid_arguments"),
            ("start", {"authorized": 1}, "invalid_arguments"),
            ("stop", {"authorized": False}, "invalid_arguments"),
            ("status", {"PRIVATE": True}, "invalid_arguments"),
            ("read", {"start": "01", "count": 1, "generation": "1"}, "invalid_arguments"),
            ("read", {"start": "0", "count": True, "generation": "1"}, "invalid_arguments"),
            ("read", {"start": "0", "count": 4097, "generation": "1"}, "invalid_arguments"),
            ("read", {"start": str(helper.MAX_UINT64), "count": 1, "generation": "1"}, "invalid_arguments"),
            ("PRIVATE", {}, "unsupported_operation"),
        ):
            with self.subTest(op=op, code=code):
                with self.assertRaises(helper.ProtocolError) as captured:
                    session.dispatch(op, args)
                self.assertEqual(code, captured.exception.code)
        self.assertEqual(before, len(factory.instances[0].calls))

    def test_open_requires_exact_release_digest_session_and_absolute_path(self):
        for field, value in (("multi_version", "latest"), ("dll_sha256", "PRIVATE"),
                             ("session_id", 0), ("session_id", True), ("installation", "relative")):
            args = open_args()
            args[field] = value
            factory = Factory()
            with self.assertRaises(helper.ProtocolError):
                helper.HelperSession(factory).dispatch("open", args)
            self.assertEqual([], factory.instances)

    def test_start_stop_acknowledgements_do_not_invent_observed_state(self):
        factory, session = Factory(), None
        session = helper.HelperSession(factory)
        session.dispatch("open", open_args())
        result = session.dispatch("start", {"authorized": True})
        self.assertEqual({"accepted": True, "confirmed": False, "generation": "1",
                          "trace_state": "start_requested"}, result)
        factory.instances[0].stop_callback = True
        result = session.dispatch("stop", {"authorized": True})
        self.assertFalse(result["confirmed"])
        self.assertEqual("stopped", result["trace_state"])
        factory.instances[0].start_callback = True
        result = session.dispatch("start", {"authorized": True})
        self.assertEqual("running", result["trace_state"])
        self.assertEqual("2", result["generation"])
        session.dispatch("close", {})
        self.assertEqual(1, [name for name, _ in factory.instances[0].calls].count("close"))

    def test_read_discards_batches_if_generation_changes_before_or_after_read(self):
        for scenario in ("before", "after", "regression", "info_before", "info_after"):
            with self.subTest(scenario=scenario):
                factory, session = Factory(), None
                session = helper.HelperSession(factory)
                session.dispatch("open", open_args())
                api = factory.instances[0]
                if scenario == "before":
                    api.pending.append("data_clear")
                elif scenario == "after":
                    api.read_hook = lambda current: current.pending.append("data_clear")
                elif scenario == "info_before":
                    api.info_byte = 1
                elif scenario == "info_after":
                    api.read_hook = lambda current: setattr(current, "info_byte", 1)
                else:
                    api.read_hook = lambda current: setattr(current, "count", 1)
                with self.assertRaises(helper.ProtocolError) as captured:
                    session.dispatch("read", {"start": "0", "count": 2, "generation": "1"})
                self.assertEqual("stale_generation", captured.exception.code)
                self.assertEqual(2, session.generation)
                self.assertEqual(0 if scenario in ("before", "info_before") else 1,
                                 [name for name, _ in api.calls].count("get_packets"))
                if scenario in ("info_before", "info_after"):
                    status = session.dispatch("status", {})
                    self.assertEqual("01" + "00" * 63, status["info_hex"])
                    self.assertIn("decoder_replacement", status["notifications"]["events"])

    def test_pending_notification_loss_invalidates_generation_and_is_reported(self):
        factory = Factory()
        session = helper.HelperSession(factory, notification_limit=2)
        session.dispatch("open", open_args())
        api = factory.instances[0]
        api.pending = ["new_data_available"] * 3
        api.ignored = 1
        api.callback_failed = True
        result = session.dispatch("status", {})
        self.assertEqual("2", result["generation"])
        self.assertEqual({
            "events": ["new_data_available"] * 2, "dropped": 1,
            "ignored_callbacks": 1, "callback_failed": True, "generation_invalidated": True,
        }, result["notifications"])
        self.assertFalse(session.dispatch("status", {})["notifications"]["generation_invalidated"])
        session.generation = helper.MAX_UINT64
        api.pending.append("trace_start")
        with self.assertRaises(helper.FatalError) as captured:
            session.refresh()
        self.assertEqual("generation_exhausted", captured.exception.code)

    def test_native_failures_and_interruptions_are_fixed_terminal_errors(self):
        for interruption, exit_code, expected in (
            (NativeError("PRIVATE"), 1, "native_failure"),
            (KeyboardInterrupt(), 130, "native_interrupted"),
            (SystemExit("PRIVATE"), 1, "native_interrupted"),
        ):
            def configure(api):
                api.fail_op, api.failure = "initialize", interruption
            factory, output = Factory(configure), io.BytesIO()
            code = helper.serve(io.BytesIO(request(1, "open", open_args()) + request(2, "close")),
                                output, factory)
            self.assertEqual(exit_code, code)
            self.assertEqual({"v": 1, "seq": 1, "ok": False, "error": {"code": expected}},
                             messages(output)[1])
            self.assertEqual(2, len(messages(output)))
            self.assertNotIn(b"PRIVATE", output.getvalue())
            self.assertEqual(["initialize"], [name for name, _ in factory.instances[0].calls])

    def test_failed_lifecycle_operations_are_terminal_without_retry_or_cleanup(self):
        operations = (
            ("start", {"authorized": True}, "start_trace"),
            ("stop", {"authorized": True}, "stop_trace"),
            ("close", {}, "close"),
            ("read", {"start": "0", "count": 1, "generation": "1"}, "get_packets"),
            ("status", {}, "get_info"),
        )
        for op, args, native_op in operations:
            with self.subTest(op=op):
                def configure(api):
                    api.fail_op = native_op
                    api.fail_after = 1 if native_op == "get_info" else 0
                factory, output = Factory(configure), io.BytesIO()
                source = io.BytesIO(request(1, "open", open_args()) +
                                    request(2, op, args) + request(3, op, args))
                self.assertEqual(1, helper.serve(source, output, factory))
                wire = messages(output)
                self.assertEqual(3, len(wire))
                self.assertEqual({"v": 1, "seq": 2, "ok": False, "error": {"code": "native_failure"}},
                                 wire[-1])
                names = [name for name, _ in factory.instances[0].calls]
                self.assertEqual(2 if native_op == "get_info" else 1, names.count(native_op))
                if native_op != "close":
                    self.assertNotIn("close", names)
                if native_op != "stop_trace":
                    self.assertNotIn("stop_trace", names)
                self.assertNotIn(b"PRIVATE", output.getvalue())

    def test_maximum_batch_stays_under_the_response_frame_limit(self):
        def configure(api):
            api.count = helper.MAX_PACKET_BATCH
        factory, output = Factory(configure), io.BytesIO()
        source = io.BytesIO(request(1, "open", open_args()) + request(
            2, "read", {"start": "0", "count": helper.MAX_PACKET_BATCH, "generation": "1"}))
        self.assertEqual(0, helper.serve(source, output, factory))
        lines = output.getvalue().splitlines()
        self.assertTrue(all(len(line) + 1 <= helper.MAX_RESPONSE_BYTES for line in lines))
        result = json.loads(lines[-1])["result"]
        self.assertEqual(helper.MAX_PACKET_BATCH, result["count"])
        self.assertEqual(helper.MAX_PACKET_BATCH, len(result["packets_hex"]))

    def test_idle_input_does_not_stop_pumping_or_change_native_owner_thread(self):
        source, output, factory = BlockingInput(), SynchronizedOutput(), Factory()
        result = []
        owner = threading.Thread(target=lambda: result.append(helper.serve(source, output, factory)))
        owner.start()
        source.send(request(1, "open", open_args()))
        wait_for(lambda: len(messages(output)) >= 2)
        api = factory.instances[0]
        before = len([name for name, _ in api.calls if name == "poll_events"])
        wait_for(lambda: len([name for name, _ in api.calls if name == "poll_events"]) >= before + 3)
        source.send(request(2, "close"))
        owner.join(2)
        source.send(b"")
        self.assertFalse(owner.is_alive())
        self.assertEqual([0], result)
        self.assertEqual({owner.ident}, {thread for _, thread in api.calls})

    def test_partial_pipe_line_leaves_owner_free_to_pump(self):
        read_fd, write_fd = os.pipe()
        with os.fdopen(read_fd, "rb") as source, os.fdopen(write_fd, "wb", buffering=0) as sender:
            factory, output = Factory(), SynchronizedOutput()
            result = []
            owner = threading.Thread(target=lambda: result.append(helper.serve(source, output, factory)))
            owner.start()
            sender.write(request(1, "open", open_args()))
            wait_for(lambda: len(messages(output)) >= 2)
            api = factory.instances[0]
            data = request(2, "status")
            sender.write(data[:10])
            before = len([name for name, _ in api.calls if name == "poll_events"])
            wait_for(lambda: len([name for name, _ in api.calls if name == "poll_events"]) >= before + 2)
            self.assertEqual(2, len(messages(output)))
            sender.write(data[10:])
            wait_for(lambda: len(messages(output)) >= 3)
            sender.write(request(3, "close"))
            sender.close()
            owner.join(2)
            self.assertFalse(owner.is_alive())
            self.assertEqual([0], result)

    def test_slow_stdout_is_bounded_without_implicit_native_cleanup(self):
        source, output, factory = BlockingInput(), SlowOutput(), Factory()
        result = []
        owner = threading.Thread(target=lambda: result.append(helper.serve(
            source, output, factory, queue_limit=2, shutdown_timeout=0.08)))
        owner.start()
        source.send(request(1, "open", open_args()))
        wait_for(lambda: len(messages(output)) >= 2)
        api = factory.instances[0]
        output.block.set()
        source.send(request(2, "status"))
        self.assertTrue(output.waiting.wait(1))
        before = len([name for name, _ in api.calls if name == "poll_events"])
        wait_for(lambda: len([name for name, _ in api.calls if name == "poll_events"]) >= before + 2)
        for seq in (3, 4, 5):
            source.send(request(seq, "status"))
            time.sleep(0.03)
        owner.join(1)
        output.release.set()
        source.send(b"")
        self.assertFalse(owner.is_alive())
        self.assertEqual([1], result)
        names = [name for name, _ in api.calls]
        self.assertNotIn("close", names)
        self.assertNotIn("stop_trace", names)

    def test_transport_failures_never_print_exception_details(self):
        class BadInput:
            def readline(self, _limit):
                raise RuntimeError("PRIVATE-INPUT")
        class BadOutput:
            def write(self, _data):
                raise RuntimeError("PRIVATE-OUTPUT")
        factory, output = Factory(), io.BytesIO()
        self.assertEqual(1, helper.serve(BadInput(), output, factory))
        self.assertEqual([], factory.instances)
        self.assertNotIn(b"PRIVATE", output.getvalue())
        self.assertEqual(1, helper.serve(io.BytesIO(), BadOutput(), factory, shutdown_timeout=0.01))

    def test_blocked_ready_writer_has_a_bounded_injectable_clock_deadline(self):
        output, factory = SlowOutput(), Factory()
        output.block.set()
        current = [0]

        def clock():
            current[0] += 0.05
            return current[0]

        code = helper.serve(io.BytesIO(request(1, "open", open_args())), output, factory,
                            clock=clock, startup_timeout=0.01, shutdown_timeout=0.02)
        output.release.set()
        self.assertEqual(1, code)
        self.assertEqual([], factory.instances)

    def test_queue_and_response_limits_are_enforced(self):
        output, factory = io.BytesIO(), Factory()
        source = io.BytesIO(b"".join(request(index, "status") for index in range(1, 100)))
        self.assertEqual(1, helper.serve(source, output, factory, queue_limit=1))
        self.assertEqual([], factory.instances)
        self.assertIn("input_overflow", [message.get("error", {}).get("code") for message in messages(output)])
        transport = helper._Transport(io.BytesIO(), io.BytesIO(), 1)
        self.assertFalse(transport.offer({"PRIVATE": "x" * helper.MAX_RESPONSE_BYTES}))
        self.assertEqual("response_too_large", transport.failure)


if __name__ == "__main__":
    unittest.main()
