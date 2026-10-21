import ast
import io
import json
import os
import shutil
import socket
import tempfile
import threading
import time
import types
import unittest


RECON = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def load_module(name, relative_path, probe=False):
    path = os.path.join(RECON, relative_path)
    with io.open(path, "r", encoding="utf-8") as source:
        tree = ast.parse(source.read(), filename=path)
    if probe:
        tree.body = [node for node in tree.body
                     if isinstance(node, ast.FunctionDef) or
                     (isinstance(node, ast.Import) and
                      all(alias.name != "reconlib" for alias in node.names))]
    module = types.ModuleType(name)
    module.__file__ = path
    eval(compile(tree, path, "exec"), module.__dict__)
    return module


class FakeSession(object):
    def GetStatus(self):
        time.sleep(0.001)
        return "stopped"


class FakeSocket(object):
    def __init__(self, replies):
        self.stream = io.BytesIO(replies)
        self.sent = []
        self.closed = False

    def makefile(self, mode):
        if mode != "rb":
            raise AssertionError("client must read bytes")
        return self.stream

    def sendall(self, data):
        if not isinstance(data, bytes):
            raise AssertionError("client must send bytes")
        self.sent.append(data)

    def close(self):
        self.closed = True


class SocketProbeTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.mkdtemp(prefix="multi-dap-p02-")
        self.probe = load_module("p02_probe_test", "probes/p02_socket.py", probe=True)
        self.client = load_module("p02_client_test", "tools/p02_client.py")
        self.probe.reconlib = types.ModuleType("fake_reconlib")
        self.probe.reconlib.OUT_DIR = self.directory
        self.probe.PHASE_FILE = os.path.join(self.directory, "port.json")
        self.probe.PHASE_TIMEOUT = 5.0
        self.client.PHASE_FILE = self.probe.PHASE_FILE
        self.client.RESULT = os.path.join(self.directory, "result.json")
        self.client.TIMEOUT = 10.0

    def tearDown(self):
        shutil.rmtree(self.directory)

    def assert_phase(self, result, requests):
        self.assertEqual([], result["errors"])
        self.assertEqual(1, result["accepts"])
        self.assertEqual(requests, result["requests"])
        self.assertGreater(result["multi_calls"], 0)
        self.assertEqual({"stopped": result["multi_calls"]}, result["status_histogram"])
        self.assertFalse(result.get("thread_alive_after_join", False))

    def start_phases(self, names):
        results = []
        errors = []

        def run():
            try:
                for name in names:
                    results.append(getattr(self.probe, name + "_phase")(FakeSession()))
            except Exception as error:
                errors.append(error)

        worker = threading.Thread(target=run)
        worker.daemon = True
        worker.start()
        return worker, results, errors

    def test_client_main_completes_both_phases_and_writes_json(self):
        worker, results, errors = self.start_phases(("threaded", "select"))
        try:
            self.client.main()
        finally:
            worker.join(12.0)
        self.assertFalse(worker.is_alive())
        self.assertEqual([], errors)
        self.assertEqual(2, len(results))
        for result in results:
            self.assert_phase(result, self.client.REQUESTS)
        with open(self.client.RESULT, "rb") as output:
            document = json.loads(output.read().decode("utf-8"))
        self.assertEqual(["threaded", "select"],
                         [phase["phase"] for phase in document["phases"]])
        for phase in document["phases"]:
            self.assertEqual(64, phase["requests"])
            self.assertEqual(64, len(phase["latency_seconds"]))
            self.assertGreaterEqual(phase["max_latency_seconds"],
                                    phase["mean_latency_seconds"])

    def check_fragmented_phase(self, name):
        worker, results, errors = self.start_phases((name,))
        try:
            port = self.client.wait_for_phase(name, time.time() + 5.0)
            connection = socket.create_connection(("127.0.0.1", port), timeout=5.0)
            stream = connection.makefile("rb")
            try:
                connection.sendall(b"pi")
                connection.sendall(b"ng 0\nping 1\ndo")
                self.assertEqual(b"pong\n", stream.readline())
                self.assertEqual(b"pong\n", stream.readline())
                connection.sendall(b"ne\n")
                self.assertEqual(b"done\n", stream.readline())
            finally:
                stream.close()
                connection.close()
        finally:
            worker.join(7.0)
        self.assertFalse(worker.is_alive())
        self.assertEqual([], errors)
        self.assertEqual(1, len(results))
        self.assert_phase(results[0], 2)

    def test_threaded_phase_handles_split_and_coalesced_lines(self):
        self.check_fragmented_phase("threaded")

    def test_select_phase_handles_split_and_coalesced_lines(self):
        self.check_fragmented_phase("select")

    def check_invalid_reply(self, replies, sent):
        connection = FakeSocket(replies)
        fake_socket_module = types.ModuleType("fake_socket")
        fake_socket_module.create_connection = lambda address, timeout: connection
        self.client.socket = fake_socket_module
        self.client.REQUESTS = 1
        with self.assertRaises(RuntimeError):
            self.client.run_phase("threaded", 1234)
        self.assertEqual(sent, connection.sent)
        self.assertTrue(connection.closed)
        self.assertTrue(connection.stream.closed)

    def test_client_rejects_unexpected_reply_and_closes_connection(self):
        self.check_invalid_reply(b"unexpected\n", [b"ping 0\n"])

    def test_client_rejects_missing_completion_and_closes_connection(self):
        self.check_invalid_reply(b"pong\n", [b"ping 0\n", b"done\n"])

    def test_wait_for_phase_times_out_without_server(self):
        with self.assertRaises(RuntimeError):
            self.client.wait_for_phase("threaded", time.time() - 1)


if __name__ == "__main__":
    unittest.main()
