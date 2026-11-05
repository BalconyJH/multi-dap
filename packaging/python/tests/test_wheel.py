"""Host tests for packaging and transparent native process launch."""

import base64
from contextlib import redirect_stderr
import csv
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import struct
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch, Mock
import warnings
import zipfile


ROOT = Path(__file__).resolve().parents[1]


def load_module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


backend = load_module("wheel_backend", ROOT / "wheel_backend.py")
launcher = load_module("multi_dap", ROOT / "multi_dap" / "__init__.py")


class WheelTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="multi-dap-wheel-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.archive = self.root / "multi-dap-0.1.0-rc.1-windows-amd64.zip"
        self.files = {name: b"fixture\n" for name in backend.PAYLOAD}
        binary = bytearray(128)
        binary[:2] = b"MZ"
        struct.pack_into("<I", binary, 60, 64)
        binary[64:70] = b"PE\0\0\x64\x86"
        self.files["multi-dap.exe"] = bytes(binary)
        self.provenance = {
            "schema": "multi-dap.release-provenance/v1", "package": "multi-dap",
            "version": "0.1.0-rc.1", "source_date_epoch": 315532800,
            "target": {"os": "windows", "architecture": "amd64"},
            "binary": {"path": "multi-dap.exe", "size": len(binary),
                       "sha256": hashlib.sha256(binary).hexdigest()},
        }

    def write_archive(self, *, payload_hashes=True, extra=None):
        self.files["release-provenance.json"] = json.dumps(self.provenance).encode()
        if payload_hashes:
            self.files["SHA256SUMS.txt"] = "".join(
                f"{hashlib.sha256(data).hexdigest()}  {name}\n"
                for name, data in sorted(self.files.items()) if name != "SHA256SUMS.txt"
            ).encode()
        with zipfile.ZipFile(self.archive, "w") as archive:
            for name, data in self.files.items():
                archive.writestr(name, data)
            if extra:
                with warnings.catch_warnings():
                    warnings.simplefilter("ignore", UserWarning)
                    archive.writestr(extra, b"extra")
        digest = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        Path(str(self.archive) + ".sha256").write_text(
            f"{digest}  {self.archive.name}\n", encoding="utf-8")

    def build(self, out=None):
        return backend.build_wheel(str(out or self.root), {"archive": str(self.archive)})

    def test_reproducible_wheel_preserves_payload_and_has_complete_record(self):
        self.write_archive()
        first = self.root / self.build()
        other = self.root / "second"
        other.mkdir()
        second = other / self.build(other)
        self.assertEqual(first.read_bytes(), second.read_bytes())
        self.assertEqual(first.name, "multi_dap-0.1.0rc1-py3-none-win_amd64.whl")
        with zipfile.ZipFile(first) as wheel:
            info = "multi_dap-0.1.0rc1.dist-info"
            self.assertIn(b"Root-Is-Purelib: false", wheel.read(info + "/WHEEL"))
            self.assertIn(b"Tag: py3-none-win_amd64", wheel.read(info + "/WHEEL"))
            metadata = wheel.read(info + "/METADATA")
            self.assertIn(b"Requires-Python: >=3.10", metadata)
            self.assertNotIn(b"Requires-Dist:", metadata)
            self.assertEqual(wheel.read(info + "/entry_points.txt"),
                             b"[console_scripts]\nmulti-dap = multi_dap:main\n")
            for name, data in self.files.items():
                self.assertEqual(wheel.read("multi_dap/_bundle/" + name), data)
            rows = list(csv.reader(io.StringIO(wheel.read(info + "/RECORD").decode())))
            self.assertEqual({row[0] for row in rows}, set(wheel.namelist()))
            for name, digest, size in rows:
                if name.endswith("/RECORD"):
                    self.assertEqual((digest, size), ("", ""))
                    continue
                data = wheel.read(name)
                expected = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
                self.assertEqual(digest, "sha256=" + expected)
                self.assertEqual(int(size), len(data))

    def test_outer_and_inner_checksums_fail_closed(self):
        self.write_archive()
        with self.archive.open("ab") as stream:
            stream.write(b"corruption")
        with self.assertRaisesRegex(ValueError, "archive checksum"):
            self.build()
        self.files["bridge/bridge.py"] = b"changed"
        self.write_archive(payload_hashes=False)
        with self.assertRaisesRegex(ValueError, "payload checksum"):
            self.build()

    def test_missing_extra_traversal_and_duplicate_entries_are_rejected(self):
        for extra in ("../escape.py", "bridge/notifier.py", "multi-dap.exe"):
            with self.subTest(extra=extra):
                self.write_archive(extra=extra)
                with self.assertRaisesRegex(ValueError, "allowlist"):
                    self.build()
        del self.files["bridge/mbp_wire.py"]
        self.write_archive()
        with self.assertRaisesRegex(ValueError, "allowlist"):
            self.build()

    def test_provenance_and_actual_architecture_are_both_checked(self):
        self.provenance["target"]["architecture"] = "arm64"
        self.write_archive()
        with self.assertRaisesRegex(ValueError, "Windows amd64"):
            self.build()
        self.provenance["target"]["architecture"] = "amd64"
        binary = self.files["multi-dap.exe"].replace(b"\x64\x86", b"\x64\xaa")
        self.files["multi-dap.exe"] = binary
        self.provenance["binary"]["sha256"] = hashlib.sha256(binary).hexdigest()
        self.write_archive()
        with self.assertRaisesRegex(ValueError, "AMD64 PE"):
            self.build()

    def test_native_binary_binding_and_version_binding(self):
        self.provenance["binary"]["sha256"] = "0" * 64
        self.write_archive()
        with self.assertRaisesRegex(ValueError, "executable does not match"):
            self.build()
        self.provenance["binary"]["sha256"] = hashlib.sha256(self.files["multi-dap.exe"]).hexdigest()
        self.provenance["version"] = "0.2.0"
        self.write_archive()
        with self.assertRaisesRegex(ValueError, "filename"):
            self.build()

    def test_output_is_not_overwritten(self):
        self.write_archive()
        path = self.root / self.build()
        original = path.read_bytes()
        with self.assertRaises(FileExistsError):
            self.build()
        self.assertEqual(path.read_bytes(), original)

    def test_build_requires_one_explicit_absolute_archive(self):
        for settings in (None, {}, {"archive": "relative.zip"},
                         {"archive": [str(self.archive)]},
                         {"archive": str(self.archive), "version": "1.0.0"}):
            with self.subTest(settings=settings), self.assertRaises(ValueError):
                backend.build_wheel(str(self.root), settings)
        with self.assertRaises(backend.UnsupportedOperation):
            backend.build_sdist(str(self.root))

    def test_version_mapping_is_explicit(self):
        for native, expected in {"1.2.3": "1.2.3", "0.0.0-dev": "0.0.0.dev0",
                                 "0.0.0-ci.123": "0.0.0.dev123+ci",
                                 "1.2.3-dev.4": "1.2.3.dev4", "1.2.3-alpha.1": "1.2.3a1",
                                 "1.2.3-beta.2": "1.2.3b2", "1.2.3-rc.3": "1.2.3rc3"}.items():
            self.assertEqual(backend.python_version(native), expected)
        for value in ("v1.2.3", "1.2", "01.2.3", "1.2.3-preview.1", "1.2.3+local", "1.2.3-rc.01"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                backend.python_version(value)


class LauncherTests(unittest.TestCase):
    def setUp(self):
        for target, value in (("sys.platform", "win32"),
                              ("platform.machine", lambda: "AMD64"),
                              ("struct.calcsize", lambda _: 8)):
            patcher = patch.object(getattr(launcher, target.split(".")[0]),
                                   target.split(".")[1], value)
            patcher.start()
            self.addCleanup(patcher.stop)
        patcher = patch.object(sys, "argv", list(sys.argv))
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_arguments_handles_cwd_exit_and_interrupt_are_preserved(self):
        launcher.sys.argv = ["multi-dap", "proxy", "--config", "C:\\space dir\\project.toml", "", 'a"b']
        child = Mock()
        child.wait.side_effect = [KeyboardInterrupt(), 37]
        with patch.object(launcher.Path, "is_file", return_value=True), \
                patch.object(launcher.subprocess, "Popen", return_value=child) as popen:
            self.assertEqual(launcher.main(), 37)
        popen.assert_called_once_with(
            [str(launcher.bundle_directory() / "multi-dap.exe"), *launcher.sys.argv[1:]], shell=False)
        child.kill.assert_not_called()
        child.terminate.assert_not_called()

    def test_missing_payload_and_spawn_error_only_use_stderr(self):
        for missing in (True, False):
            stderr = io.StringIO()
            with self.subTest(missing=missing), redirect_stderr(stderr), \
                    patch.object(launcher.Path, "is_file", return_value=not missing), \
                    patch.object(launcher.subprocess, "Popen", side_effect=OSError(5, "private path")):
                self.assertEqual(launcher.main(), 126)
            self.assertIn("multi-dap:", stderr.getvalue())
            self.assertNotIn("private path", stderr.getvalue())

    def test_windows_status_survives_console_entry_point_conversion(self):
        with patch.object(launcher.Path, "is_file", return_value=True), \
                patch.object(launcher.subprocess, "Popen") as popen:
            popen.return_value.wait.return_value = 0xC000013A
            self.assertEqual(launcher.main(), -1073741510)

    def test_unsupported_platform_never_launches(self):
        for system, machine, size in (("linux", "x86_64", 8), ("win32", "ARM64", 8),
                                      ("win32", "AMD64", 4)):
            launcher.sys.platform = system
            launcher.platform.machine = lambda: machine
            launcher.struct.calcsize = lambda _: size
            with redirect_stderr(io.StringIO()), patch.object(launcher.subprocess, "Popen") as popen:
                self.assertEqual(launcher.main(), 126)
                popen.assert_not_called()

    def test_real_child_preserves_binary_stdio_unicode_arguments_and_project_cwd(self):
        # Use a real child with no vendor/runtime dependency. The platform and
        # executable lookup alone are replaced, so the same test runs on Linux.
        code = (
            "import importlib.util,pathlib,sys; "
            "spec=importlib.util.spec_from_file_location('launcher',sys.argv[1]); "
            "m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m); "
            "m.sys.platform='win32'; m.platform.machine=lambda:'AMD64'; "
            "m.struct.calcsize=lambda _:8; "
            "m.bundle_directory=lambda:type('Bundle',(),{'__truediv__':lambda self,name:pathlib.Path(sys.executable)})(); "
            "sys.argv=[sys.executable,*sys.argv[2:]]; sys.exit(m.main())"
        )
        child = (
            "import json,os,sys; "
            "sys.stdout.buffer.write(sys.stdin.buffer.read()); "
            "sys.stderr.write(json.dumps([sys.argv[1:],os.getcwd()])); sys.exit(23)"
        )
        args = ["", 'a"b', "space dir\\", "\u4e2d\u6587"]
        data = b"Content-Length: 9\r\n\r\n\x00\xff\x80\n\r\x1aabc"
        statuses = (23, -1073741510) if os.name == "nt" else (23,)
        for status in statuses:
            with self.subTest(status=status), tempfile.TemporaryDirectory(prefix="multi-dap-project-") as project:
                result = subprocess.run([sys.executable, "-c", code,
                                         str(ROOT / "multi_dap" / "__init__.py"),
                                         "-c", child.replace("sys.exit(23)", f"sys.exit({status})"),
                                         *args], cwd=project, input=data, capture_output=True)
                self.assertEqual(result.returncode, status & 0xFFFFFFFF, result.stderr)
                self.assertEqual(result.stdout, data)
                actual_args, cwd = json.loads(result.stderr)
                self.assertEqual(actual_args, args)
                self.assertEqual(Path(cwd).resolve(), Path(project).resolve())


if __name__ == "__main__":
    unittest.main()
