import os
from pathlib import Path
import shutil
import socket
import subprocess
import threading
import unittest


SCRIPT = Path(__file__).resolve().parents[1] / "scripts" / "invoke_multi_socket.ps1"
POWERSHELL = os.environ.get("SOCKET_TEST_POWERSHELL") or shutil.which("pwsh") or shutil.which("powershell")


@unittest.skipUnless(POWERSHELL, "PowerShell is required")
class InvokeMultiSocketTests(unittest.TestCase):
    def invoke(self, reply=b"complete\r\n", close=False, continuous=False,
               allow_empty=False, quiet_period=200):
        stopped = threading.Event()
        commands = []
        failures = []
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            listener.listen(1)
            listener.settimeout(10)

            def serve():
                try:
                    connection, _ = listener.accept()
                    with connection:
                        connection.settimeout(10)
                        command = b""
                        while not command.endswith(b"\n"):
                            chunk = connection.recv(4096)
                            if not chunk:
                                raise AssertionError("client closed before command newline")
                            command += chunk
                        commands.append(command)
                        if reply:
                            connection.sendall(reply)
                        if close:
                            return
                        while not stopped.wait(0.025):
                            if continuous:
                                try:
                                    connection.sendall(b"partial\n")
                                except (BrokenPipeError, ConnectionResetError, ConnectionAbortedError):
                                    return
                except Exception as error:
                    failures.append(error)

            server = threading.Thread(target=serve, daemon=True)
            server.start()
            arguments = [
                POWERSHELL, "-NoLogo", "-NoProfile", "-NonInteractive",
                "-ExecutionPolicy", "Bypass", "-File", str(SCRIPT),
                "-Port", str(listener.getsockname()[1]),
                "-Command", "evaltosocket { components }",
                "-TimeoutSeconds", "1",
                "-QuietPeriodMilliseconds", str(quiet_period),
            ]
            if allow_empty:
                arguments.append("-AllowEmptyResponse")
            try:
                result = subprocess.run(arguments, capture_output=True, text=True, timeout=15)
            finally:
                stopped.set()
                server.join(timeout=11)
            self.assertFalse(server.is_alive(), "fake server did not stop")
            self.assertEqual(failures, [])
            self.assertEqual(commands, [b"evaltosocket { components }\n"])
            return result

    def test_quiet_period_completes_reply(self):
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "complete")

    def test_eof_completes_before_long_quiet_period(self):
        result = self.invoke(close=True, quiet_period=5000)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "complete")

    def test_deadline_rejects_partial_reply_even_when_empty_allowed(self):
        for allow_empty in (False, True):
            with self.subTest(allow_empty=allow_empty):
                result = self.invoke(continuous=True, allow_empty=allow_empty, quiet_period=500)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("response incomplete", result.stderr)
                self.assertEqual(result.stdout, "")

    def test_empty_eof_is_rejected_by_default(self):
        result = self.invoke(reply=b"", close=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("returned no data", result.stderr)

    def test_silence_without_permission_times_out(self):
        result = self.invoke(reply=b"")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("response incomplete", result.stderr)

    def test_explicit_empty_response_allows_eof_or_quiet(self):
        for close in (False, True):
            with self.subTest(close=close):
                result = self.invoke(reply=b"", close=close, allow_empty=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
