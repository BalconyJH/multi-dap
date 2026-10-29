"""Synthetic host tests; these never load or connect to a vendor runtime."""

import ctypes
from contextlib import contextmanager
import hashlib
import io
import json
import os
from pathlib import Path
import struct
import tempfile
import threading
import unittest
from unittest import mock

from bridge import timemachine_native as native


def synthetic_pe(machine=0x8664, exports=None):
    exports = (list(native._EXPORTS) if exports is None else list(exports))
    exports = [native._export_symbol(name, machine) for name in exports]
    data = bytearray(0x2200)
    data[:2] = b"MZ"
    struct.pack_into("<I", data, 0x3C, 0x80)
    data[0x80:0x84] = b"PE\x00\x00"
    size = 240 if machine == 0x8664 else 224
    struct.pack_into("<HH", data, 0x84, machine, 1)
    struct.pack_into("<HH", data, 0x94, size, 0x2000)
    optional = 0x98
    directory = 112 if machine == 0x8664 else 96
    struct.pack_into("<H", data, optional, 0x20B if machine == 0x8664 else 0x10B)
    struct.pack_into("<I", data, optional + directory - 4, 16)
    struct.pack_into("<II", data, optional + directory, 0x1000, 0x1000)
    section = optional + size
    data[section:section + 8] = b".fixture"
    struct.pack_into("<IIII", data, section + 8, 0x2000, 0x1000, 0x2000, 0x200)
    struct.pack_into("<I", data, section + 36, 0x60000020)
    struct.pack_into("<IIIII", data, 0x200 + 20, len(exports), len(exports),
                     0x1100, 0x1180, 0x1200)
    names = 0x500
    for index, name in enumerate(exports):
        encoded = name.encode("ascii") + b"\x00"
        struct.pack_into("<I", data, 0x300 + index * 4, 0x2800 + index * 4)
        struct.pack_into("<I", data, 0x380 + index * 4, names - 0x200 + 0x1000)
        struct.pack_into("<H", data, 0x400 + index * 2, index)
        data[names:names + len(encoded)] = encoded
        names += len(encoded)
    return bytes(data)


def make_install(root, version="8.1.4", machine=None, exports=None):
    if machine is None:
        machine = native._PROFILES[version][0]
    root = Path(root)
    (root / "timemachine_api").mkdir(parents=True)
    (root / "defaults" / "licensing").mkdir(parents=True)
    (root / "install_summary.txt").write_bytes(
        ('Installing "MULTI IDE %s" to "private-installation-path"\r\n' % version).encode("ascii"))
    (root / "defaults" / "licensing" / "install_info.txt").write_bytes(
        ("Product Release Name: MULTI IDE %s\r\n" % version).encode("ascii"))
    for name in ("timemachine_api.h", "ts_packet.h"):
        (root / "timemachine_api" / name).write_bytes(
            b"Synthetic fixture for metadata hashing; not a vendor header.\n")
    library = synthetic_pe(machine, exports)
    (root / "timemachine_api.dll").write_bytes(library)
    return hashlib.sha256(library).hexdigest()


class FakeFunction:
    def __init__(self, library, name):
        self.library, self.name = library, name
        self.argtypes = None
        self.restype = None

    def __call__(self, *arguments):
        self.library.calls.append((self.name, arguments))
        if self.name in self.library.interrupt_calls:
            raise self.library.interrupt_calls[self.name]
        if self.name in self.library.raise_calls:
            raise RuntimeError("PRIVATE-NATIVE-DETAIL")
        return self.library.invoke(self.name, arguments)


class FakeLibrary:
    def __init__(self):
        self.calls = []
        self.raise_calls = set()
        self.interrupt_calls = {}
        self.loader_interrupt = None
        self.callbacks = {}
        self.ids = [3, 9]
        self.connection = 0x123456
        self.info_status = 0
        self.init_status = 0
        self.packet_counts = [2]
        self.actual_packets = None
        self.poll_callback = None
        self.loaded = []
        self.functions = {name: FakeFunction(self, name) for name in native._EXPORTS}

    def __getattr__(self, name):
        try:
            return self.functions[name]
        except KeyError:
            raise AttributeError(name) from None

    def loader(self, path):
        self.loaded.append(path)
        if self.loader_interrupt is not None:
            raise self.loader_interrupt
        return self

    def invoke(self, name, arguments):
        if name == "tm_init":
            return self.init_status
        if name == "tm_next_id":
            prior = arguments[0]
            for index, value in enumerate([0] + self.ids):
                if value == prior:
                    return self.ids[index] if index < len(self.ids) else 0
            return 0
        if name == "tm_connect":
            return self.connection
        if name == "tm_register_callback":
            self.callbacks[arguments[1]] = arguments[2]
            return None
        if name == "tm_get_info":
            _, pointer, size = arguments
            payload = bytearray(size)
            struct.pack_into("<I", payload, 0, 2)
            struct.pack_into("<I", payload, 8, 3)
            ctypes.memmove(pointer, bytes(payload), size)
            return self.info_status
        if name == "tm_get_num_packets":
            if len(self.packet_counts) > 1:
                return self.packet_counts.pop(0)
            return self.packet_counts[0]
        if name == "tm_get_packets":
            _, start, count, buffer = arguments
            actual = count if self.actual_packets is None else self.actual_packets
            if 0 <= actual <= count:
                payload = bytearray(actual * 32)
                for index in range(actual):
                    struct.pack_into("<B", payload, index * 32, 10)
                    struct.pack_into("<Q", payload, index * 32 + 8, start + index)
                    struct.pack_into("<Q", payload, index * 32 + 24, 1234 + index)
                ctypes.memmove(buffer, bytes(payload), len(payload))
            return actual
        if name == "tm_process_events" and self.poll_callback is not None:
            self.callbacks[self.poll_callback](self.connection, None)
        return None


class NativeBindingTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="multi-dap-native-fixture-")
        self.root = self.temporary.name
        self.digest = make_install(self.root)
        self.fake = FakeLibrary()
        self.instances = []

    def tearDown(self):
        for instance in self.instances:
            try:
                instance.close()
            except native.NativeError:
                pass
        # Failed-disconnect tests intentionally retain an un-retryable fake
        # connection. No DLL was loaded and no native resource exists here.
        native._PROCESS_OWNER = None
        self.temporary.cleanup()

    def binding(self, **kwargs):
        result = native.NativeTimeMachine(
            self.root, "8.1.4", self.digest, _loader=self.fake.loader, **kwargs)
        self.instances.append(result)
        return result

    def opened(self, **kwargs):
        binding = self.binding(**kwargs)
        binding.initialize()
        binding.connect(9)
        return binding

    def assert_native_error(self, code, function, *args, **kwargs):
        with self.assertRaises(native.NativeError) as captured:
            function(*args, **kwargs)
        self.assertEqual(code, captured.exception.code)
        self.assertNotIn("PRIVATE", str(captured.exception))
        self.assertNotIn(self.root, str(captured.exception))

    def test_inspection_and_constructor_never_load_or_initialize(self):
        with mock.patch.object(native, "_load_library", side_effect=AssertionError("must not load")):
            evidence = native.inspect_abi(self.root, "8.1.4")
            self.binding()
        self.assertFalse(evidence["dll_loaded"])
        self.assertFalse(evidence["target_contacted"])
        self.assertFalse(evidence["vendor_qualified"])
        self.assertEqual(self.digest, evidence["dll_sha256"])
        self.assertEqual(64, evidence["layout"]["info_size"])
        self.assertEqual(32, evidence["layout"]["info_offsets"]["identifier"])
        self.assertEqual(32, evidence["layout"]["packet_size"])
        self.assertEqual(8, evidence["layout"]["packet_alignment"])
        self.assertEqual(16, evidence["layout"]["packet_offsets"]["payload"])
        self.assertEqual(list(native._EXPORTS), evidence["exports"])
        self.assertEqual([], self.fake.calls)
        self.assertEqual([], self.fake.loaded)
        encoded = json.dumps(evidence)
        self.assertNotIn(self.root, encoded)
        self.assertNotIn("private-installation-path", encoded)

    def test_legacy_inspection_is_static_and_has_a_distinct_layout(self):
        with tempfile.TemporaryDirectory() as directory:
            make_install(directory, "7.1.6d")
            evidence = native.inspect_abi(directory, "7.1.6d")
            self.assertEqual(32, evidence["pointer_bits"])
            self.assertEqual(60, evidence["layout"]["info_size"])
            self.assertEqual(28, evidence["layout"]["info_offsets"]["identifier"])
            self.assertNotIn("has_64bit_addrs", evidence["layout"]["info_offsets"])
            self.assertEqual("_tm_get_packets@20", evidence["export_symbols"]["tm_get_packets"])
            self.assertEqual("_tm_process_events@0", evidence["export_symbols"]["tm_process_events"])
            if ctypes.sizeof(ctypes.c_void_p) == 8:
                binding = native.NativeTimeMachine(
                    directory, "7.1.6d", evidence["dll_sha256"], _loader=self.fake.loader)
                self.assert_native_error("runtime_architecture_mismatch", binding.initialize)
                self.assertEqual([], self.fake.loaded)

    def test_release_and_digest_are_exact(self):
        for version in ("8", "8.1", "8.1.4a", "7.1.6", "auto", ""):
            self.assert_native_error("unsupported_release", native.inspect_abi, self.root, version)
        binding = native.NativeTimeMachine(self.root, "8.1.4", "0" * 64, _loader=self.fake.loader)
        self.assert_native_error("library_digest_mismatch", binding.initialize)
        self.assertEqual([], self.fake.loaded)
        info = Path(self.root) / "defaults" / "licensing" / "install_info.txt"
        info.write_text("Product Release Name: MULTI IDE 7.1.6d\n", encoding="ascii")
        self.assert_native_error("release_mismatch", native.inspect_abi, self.root, "8.1.4")
        self.assertEqual([], self.fake.loaded)

    def test_invalid_paths_and_containment_fail_before_loading(self):
        self.assert_native_error("invalid_installation", native.inspect_abi, "relative", "8.1.4")
        library = Path(self.root) / "timemachine_api.dll"
        library.unlink()
        library.mkdir()
        self.assert_native_error("invalid_installation_file", self.binding().initialize)
        self.assertEqual([], self.fake.loaded)
        with tempfile.TemporaryDirectory() as outside:
            with mock.patch.object(native.os.path, "realpath", return_value=outside):
                self.assert_native_error("installation_path_escape", native._contained_path,
                                         self.root, "timemachine_api.dll")

    def test_image_architecture_exports_and_forwarders_are_checked(self):
        library = Path(self.root) / "timemachine_api.dll"
        library.write_bytes(synthetic_pe(0x014C))
        self.assert_native_error("library_architecture_mismatch", native.inspect_abi, self.root, "8.1.4")
        library.write_bytes(synthetic_pe(exports=native._EXPORTS[:-1]))
        self.assert_native_error("required_export_unavailable", native.inspect_abi, self.root, "8.1.4")
        forwarded = bytearray(synthetic_pe())
        struct.pack_into("<I", forwarded, 0x300, 0x1500)
        library.write_bytes(forwarded)
        self.assert_native_error("forwarded_library_export", native.inspect_abi, self.root, "8.1.4")
        self.assertEqual([], self.fake.loaded)

    def test_truncated_and_unbounded_image_headers_are_rejected(self):
        for data in (b"", b"MZ" + b"\x00" * 10, synthetic_pe()[:512]):
            self.assert_native_error("invalid_library_image", native._pe_exports, data, 0x8664)
        data = bytearray(synthetic_pe())
        struct.pack_into("<I", data, 0x200 + 24, 1 << 30)
        self.assert_native_error("invalid_library_exports", native._pe_exports, bytes(data), 0x8664)

    def test_initialize_binds_the_documented_types_without_connecting(self):
        binding = self.binding()
        evidence = binding.initialize()
        self.assertTrue(evidence["dll_loaded"])
        self.assertTrue(evidence["native_initialized"])
        self.assertEqual(["tm_init"], [name for name, _ in self.fake.calls])
        self.assertEqual([ctypes.c_char_p], self.fake.tm_init.argtypes)
        self.assertIs(ctypes.c_void_p, self.fake.tm_connect.restype)
        self.assertEqual([ctypes.c_void_p, ctypes.c_uint64, ctypes.c_int,
                          ctypes.POINTER(native._Packet)], self.fake.tm_get_packets.argtypes)
        self.assertIs(ctypes.c_uint64, self.fake.tm_get_num_packets.restype)
        self.assertEqual([], self.fake.tm_process_events.argtypes)
        self.assertEqual([ctypes.c_void_p, ctypes.c_int, native._CALLBACK],
                         self.fake.tm_register_callback.argtypes)
        self.assertEqual(ctypes._FUNCFLAG_CDECL, native._CALLBACK._flags_)
        self.assert_native_error("already_initialized", binding.initialize)

    def test_library_hash_is_rechecked_under_the_load_pin(self):
        original = native._pinned_library

        @contextmanager
        def changed(path):
            with open(path, "ab") as output:
                output.write(b"changed")
            with original(path) as source:
                yield source

        binding = self.binding()
        with mock.patch.object(native, "_pinned_library", changed):
            self.assert_native_error("library_digest_mismatch", binding.initialize)
        self.assertEqual([], self.fake.loaded)

    def test_failed_native_initialization_requires_helper_recycling(self):
        binding = self.binding()
        self.fake.init_status = -1
        self.assert_native_error("native_initialization_failed", binding.initialize)
        self.assert_native_error("reconciliation_required", binding.close)
        self.assert_native_error("native_owner_exists", self.binding().initialize)
        self.assertEqual(1, [name for name, _ in self.fake.calls].count("tm_init"))

    def test_interrupted_initialization_is_sticky_and_preserves_control_flow(self):
        for location in ("loader", "tm_init"):
            for interruption_type in (KeyboardInterrupt, SystemExit):
                with self.subTest(location=location, interruption=interruption_type.__name__):
                    native._PROCESS_OWNER = None
                    self.fake = FakeLibrary()
                    binding = self.binding()
                    interruption = interruption_type(23)
                    if location == "loader":
                        self.fake.loader_interrupt = interruption
                    else:
                        self.fake.interrupt_calls[location] = interruption
                    with self.assertRaises(interruption_type) as captured:
                        binding.initialize()
                    self.assertIs(interruption, captured.exception)
                    self.assertEqual("initialization_failed", binding._state)
                    self.assertTrue(binding._initialization_uncertain)
                    self.assertIs(binding, native._PROCESS_OWNER)
                    self.assert_native_error("already_initialized", binding.initialize)
                    self.assert_native_error("reconciliation_required", binding.close)
                    self.assert_native_error("native_owner_exists", self.binding().initialize)
                    self.assertEqual(1, len(self.fake.loaded))
                    self.assertEqual(0 if location == "loader" else 1,
                                     [name for name, _ in self.fake.calls].count("tm_init"))

    def test_default_loader_uses_only_dll_directory_and_system32(self):
        with mock.patch.object(ctypes, "WinDLL", create=True) as loader:
            native._load_library("fixture.dll")
        loader.assert_called_once_with("fixture.dll", winmode=0x900)

    def test_one_process_owner_and_thread_guard(self):
        binding = self.binding()
        binding.initialize()
        other = self.binding()
        self.assert_native_error("native_owner_exists", other.initialize)
        errors = []

        def wrong_thread():
            try:
                binding.enumerate_ids()
            except native.NativeError as error:
                errors.append(error.code)

        worker = threading.Thread(target=wrong_thread)
        worker.start()
        worker.join(1)
        self.assertFalse(worker.is_alive())
        self.assertEqual(["wrong_owner_thread"], errors)
        self.assertEqual(["tm_init"], [name for name, _ in self.fake.calls])
        binding.close()
        other.initialize()

    def test_session_selection_never_defaults_to_zero_or_first(self):
        binding = self.binding()
        binding.initialize()
        for value in (0, -1, True, "9", native.MAX_INT32 + 1):
            self.assert_native_error("invalid_argument", binding.connect, value)
        self.assert_native_error("session_unavailable", binding.connect, 4)
        self.assertNotIn("tm_connect", [name for name, _ in self.fake.calls])
        self.assertEqual([3, 9], binding.enumerate_ids())
        binding.connect(9)
        connects = [args for name, args in self.fake.calls if name == "tm_connect"]
        self.assertEqual([(9,)], connects)
        self.assertEqual(set(range(5)), set(self.fake.callbacks))
        self.assert_native_error("already_connected", binding.connect, 3)

    def test_unknown_connection_outcome_does_not_guess_cleanup_or_retry(self):
        binding = self.binding()
        binding.initialize()
        self.fake.raise_calls.add("tm_connect")
        self.assert_native_error("native_call_failed", binding.connect, 9)
        self.assert_native_error("native_state_unavailable", binding.connect, 9)
        self.assert_native_error("reconciliation_required", binding.close)
        self.assertIs(binding, native._PROCESS_OWNER)
        self.assertNotIn("tm_disconnect", [name for name, _ in self.fake.calls])

    def test_interrupted_connection_cannot_be_retried_or_release_process_owner(self):
        for interruption_type in (KeyboardInterrupt, SystemExit):
            with self.subTest(interruption=interruption_type.__name__):
                native._PROCESS_OWNER = None
                self.fake = FakeLibrary()
                binding = self.binding()
                binding.initialize()
                interruption = interruption_type(23)
                self.fake.interrupt_calls["tm_connect"] = interruption
                with self.assertRaises(interruption_type) as captured:
                    binding.connect(9)
                self.assertIs(interruption, captured.exception)
                self.assertEqual("poisoned", binding._state)
                self.assertTrue(binding._connection_uncertain)
                self.assert_native_error("native_state_unavailable", binding.connect, 9)
                self.assert_native_error("reconciliation_required", binding.close)
                self.assert_native_error("native_owner_exists", self.binding().initialize)
                self.assertIs(binding, native._PROCESS_OWNER)
                self.assertEqual(1, [name for name, _ in self.fake.calls].count("tm_connect"))
                self.assertNotIn("tm_disconnect", [name for name, _ in self.fake.calls])

    def test_null_connection_is_a_definitive_failed_open(self):
        binding = self.binding()
        binding.initialize()
        self.fake.connection = None
        self.assert_native_error("connection_failed", binding.connect, 9)
        binding.close()
        self.assertNotIn("tm_disconnect", [name for name, _ in self.fake.calls])

    def test_inventory_is_bounded_and_refuses_repeated_or_negative_ids(self):
        binding = self.binding()
        binding.initialize()
        self.assert_native_error("session_inventory_limit", binding.enumerate_ids, 1)
        self.fake.ids = [3, 3]
        self.assert_native_error("invalid_session_inventory", binding.enumerate_ids)
        self.fake.ids = [-1]
        self.assert_native_error("invalid_native_result", binding.enumerate_ids)
        self.assertNotIn("tm_connect", [name for name, _ in self.fake.calls])

    def test_info_is_raw_and_nonzero_feature_flags_are_not_rejected(self):
        binding = self.opened()
        info = binding.get_info()
        self.assertEqual(64, len(info))
        self.assertEqual(2, struct.unpack_from("<I", info, 0)[0])
        info_calls = [args for name, args in self.fake.calls if name == "tm_get_info"]
        self.assertEqual(64, info_calls[-1][2])
        self.fake.info_status = -1
        self.assert_native_error("info_unavailable", binding.get_info)

    def test_packet_batches_preserve_uint64_cursors_and_actual_lengths(self):
        binding = self.opened()
        self.fake.actual_packets = 2
        start = (1 << 63) + 17
        data, count = binding.get_packets(start, 3)
        self.assertEqual(2, count)
        self.assertEqual(64, len(data))
        self.assertEqual(start, struct.unpack_from("<Q", data, 8)[0])
        self.assertEqual(start + 1, struct.unpack_from("<Q", data, 40)[0])
        for start_value, count_value, code in (
            (-1, 1, "invalid_argument"), (True, 1, "invalid_argument"),
            (0, 0, "invalid_argument"), (0, native.MAX_PACKET_BATCH + 1, "invalid_argument"),
            (native.MAX_UINT64, 1, "packet_range_overflow"),
        ):
            self.assert_native_error(code, binding.get_packets, start_value, count_value)
        self.fake.actual_packets = -1
        self.assert_native_error("packets_unavailable", binding.get_packets, 0, 1)
        self.fake.actual_packets = 2
        self.assert_native_error("packets_unavailable", binding.get_packets, 0, 1)
        self.fake.actual_packets = 0
        self.assertEqual((b"", 0), binding.get_packets(0, 1))

    def test_count_regression_and_sentinel_are_explicit(self):
        binding = self.opened()
        self.fake.packet_counts = [50, 49, native.MAX_UINT64]
        self.assertEqual(50, binding.packet_count())
        self.assertEqual(49, binding.packet_count())
        self.assertEqual({
            "events": ["packet_count_regressed"], "dropped": 0,
            "ignored_callbacks": 0, "callback_failed": False,
            "generation_invalidated": True,
        }, binding.drain_notifications())
        self.assert_native_error("packet_count_unavailable", binding.packet_count)

    def test_callback_queue_is_bounded_and_never_dereferences_payload(self):
        binding = self.opened(notification_limit=2)
        for event in (4, 3, 0):
            self.fake.callbacks[event](self.fake.connection, 1)
        self.fake.callbacks[1](self.fake.connection + 1, 1)
        self.assertEqual({
            "events": ["new_data_available", "data_clear"], "dropped": 1,
            "ignored_callbacks": 1, "callback_failed": False,
            "generation_invalidated": True,
        }, binding.drain_notifications())
        self.assertEqual({
            "events": [], "dropped": 0, "ignored_callbacks": 0,
            "callback_failed": False, "generation_invalidated": False,
        }, binding.drain_notifications())
        self.fake.poll_callback = 0
        before = len(self.fake.calls)
        binding.poll_events()
        self.assertEqual(["tm_process_events"], [name for name, _ in self.fake.calls[before:]])
        self.assertEqual(["trace_start"], binding.drain_notifications()["events"])

    def test_notifications_can_be_drained_without_native_owner_thread(self):
        binding = self.opened()
        self.fake.callbacks[4](self.fake.connection, None)
        result = []
        worker = threading.Thread(target=lambda: result.append(binding.drain_notifications()))
        worker.start()
        worker.join(1)
        self.assertEqual(["new_data_available"], result[0]["events"])

    def test_callback_failures_are_loss_evidence_not_stderr_tracebacks(self):
        binding = self.opened()
        stderr = io.StringIO()
        with mock.patch.object(binding, "_enqueue_locked", side_effect=RuntimeError("PRIVATE-CALLBACK")), \
                mock.patch.object(native.sys, "stderr", stderr):
            self.fake.callbacks[4](self.fake.connection, None)
        self.assertEqual("", stderr.getvalue())
        result = binding.drain_notifications()
        self.assertTrue(result["callback_failed"])
        self.assertTrue(result["generation_invalidated"])
        self.assertNotIn("PRIVATE", json.dumps(result))

    def test_callback_interruptions_are_rethrown_by_native_owner_not_swallowed(self):
        for interruption_type in (KeyboardInterrupt, SystemExit):
            with self.subTest(interruption=interruption_type.__name__):
                native._PROCESS_OWNER = None
                self.fake = FakeLibrary()
                binding = self.opened()
                self.fake.poll_callback = 4
                interruption = interruption_type(23)
                stderr = io.StringIO()
                with mock.patch.object(binding, "_enqueue_locked", side_effect=interruption), \
                        mock.patch.object(native.sys, "stderr", stderr):
                    with self.assertRaises(interruption_type) as captured:
                        binding.poll_events()
                self.assertIs(interruption, captured.exception)
                self.assertEqual("", stderr.getvalue())
                self.assertEqual("poisoned", binding._state)
                self.assert_native_error("native_state_unavailable", binding.poll_events)
                result = binding.drain_notifications()
                self.assertTrue(result["callback_failed"])
                self.assertTrue(result["generation_invalidated"])
                binding.close()
                self.assertEqual(1, [name for name, _ in self.fake.calls].count("tm_process_events"))
                self.assertEqual(1, [name for name, _ in self.fake.calls].count("tm_disconnect"))

    def test_trace_control_is_only_explicit_and_never_part_of_close(self):
        binding = self.opened()
        for permission in (False, None, 1, "yes"):
            self.assert_native_error("trace_start_not_authorized", binding.start_trace, permission)
            self.assert_native_error("trace_stop_not_authorized", binding.stop_trace, permission)
        before = len(self.fake.calls)
        binding.get_info()
        binding.packet_count()
        binding.get_packets(0, 1)
        binding.poll_events()
        binding.close()
        names = [name for name, _ in self.fake.calls[before:]]
        self.assertNotIn("tm_start_trace", names)
        self.assertNotIn("tm_stop_trace", names)
        self.assertEqual("tm_disconnect", names[-1])
        binding.close()
        self.assertEqual(1, [name for name, _ in self.fake.calls].count("tm_disconnect"))
        binding = self.opened()
        binding.start_trace(authorized=True)
        binding.stop_trace(authorized=True)
        self.assertEqual(["tm_start_trace", "tm_stop_trace"],
                         [name for name, _ in self.fake.calls[-2:]])

    def test_native_failures_are_sanitized_and_do_not_auto_disconnect_or_stop(self):
        binding = self.opened()
        self.fake.raise_calls.add("tm_get_info")
        before = len(self.fake.calls)
        self.assert_native_error("native_call_failed", binding.get_info)
        self.assertEqual(["tm_get_info"], [name for name, _ in self.fake.calls[before:]])
        self.assert_native_error("native_state_unavailable", binding.packet_count)
        binding.close()
        self.assertEqual("tm_disconnect", self.fake.calls[-1][0])

    def test_uncertain_disconnect_is_not_retried_or_reported_closed(self):
        binding = self.opened()
        self.fake.raise_calls.add("tm_disconnect")
        self.assert_native_error("native_call_failed", binding.close)
        self.assert_native_error("reconciliation_required", binding.close)
        self.assertEqual(1, [name for name, _ in self.fake.calls].count("tm_disconnect"))
        self.assertIs(binding, native._PROCESS_OWNER)
        self.assertTrue(binding._callbacks)

    def test_interrupted_disconnect_never_reuses_the_pointer(self):
        for interruption_type in (KeyboardInterrupt, SystemExit):
            with self.subTest(interruption=interruption_type.__name__):
                native._PROCESS_OWNER = None
                self.fake = FakeLibrary()
                binding = self.opened()
                interruption = interruption_type(23)
                self.fake.interrupt_calls["tm_disconnect"] = interruption
                with self.assertRaises(interruption_type) as captured:
                    binding.close()
                self.assertIs(interruption, captured.exception)
                self.assertEqual("close_failed", binding._state)
                self.assertTrue(binding._disconnect_uncertain)
                self.assertEqual(self.fake.connection, binding._connection)
                self.assertTrue(binding._callbacks)
                self.assert_native_error("reconciliation_required", binding.close)
                self.assert_native_error("native_owner_exists", self.binding().initialize)
                self.assertIs(binding, native._PROCESS_OWNER)
                self.assertEqual(1, [name for name, _ in self.fake.calls].count("tm_disconnect"))

    def test_interruption_between_native_return_and_commit_is_also_sticky(self):
        for call in ("tm_init", "tm_connect", "tm_disconnect"):
            with self.subTest(call=call):
                native._PROCESS_OWNER = None
                self.fake = FakeLibrary()
                binding = self.binding()
                if call != "tm_init":
                    binding.initialize()
                if call == "tm_disconnect":
                    binding.connect(9)
                invoke = binding._invoke
                interruption = KeyboardInterrupt()

                def interrupted(name, *args):
                    result = invoke(name, *args)
                    if name == call:
                        raise interruption
                    return result

                action = {
                    "tm_init": binding.initialize,
                    "tm_connect": lambda: binding.connect(9),
                    "tm_disconnect": binding.close,
                }[call]
                with mock.patch.object(binding, "_invoke", side_effect=interrupted):
                    with self.assertRaises(KeyboardInterrupt) as captured:
                        action()
                self.assertIs(interruption, captured.exception)
                self.assert_native_error("reconciliation_required", binding.close)
                self.assert_native_error("native_owner_exists", self.binding().initialize)
                self.assertIs(binding, native._PROCESS_OWNER)
                self.assertEqual(1, [name for name, _ in self.fake.calls].count(call))

    def test_interrupted_trace_controls_cannot_escape_through_normal_close(self):
        for call in ("tm_start_trace", "tm_stop_trace"):
            for interruption_type in (KeyboardInterrupt, SystemExit):
                with self.subTest(call=call, interruption=interruption_type.__name__):
                    native._PROCESS_OWNER = None
                    self.fake = FakeLibrary()
                    binding = self.opened()
                    interruption = interruption_type(23)
                    self.fake.interrupt_calls[call] = interruption
                    action = binding.start_trace if call == "tm_start_trace" else binding.stop_trace
                    with self.assertRaises(interruption_type) as captured:
                        action(authorized=True)
                    self.assertIs(interruption, captured.exception)
                    self.assertTrue(binding._trace_control_uncertain)
                    self.assert_native_error("native_state_unavailable", action, authorized=True)
                    self.assert_native_error("reconciliation_required", binding.close)
                    self.assert_native_error("native_owner_exists", self.binding().initialize)
                    self.assertIs(binding, native._PROCESS_OWNER)
                    self.assertEqual(1, [name for name, _ in self.fake.calls].count(call))
                    self.assertNotIn("tm_disconnect", [name for name, _ in self.fake.calls])

    def test_callback_registration_failure_keeps_connection_for_explicit_close(self):
        binding = self.binding()
        binding.initialize()
        self.fake.raise_calls.add("tm_register_callback")
        self.assert_native_error("native_call_failed", binding.connect, 9)
        self.assertNotIn("tm_disconnect", [name for name, _ in self.fake.calls])
        binding.close()
        self.assertEqual("tm_disconnect", self.fake.calls[-1][0])

    def test_uninitialized_and_closed_instances_do_not_call_native_functions(self):
        binding = self.binding()
        self.assert_native_error("not_initialized", binding.enumerate_ids)
        binding.close()
        self.assert_native_error("already_initialized", binding.initialize)
        self.assertEqual([], self.fake.calls)


if __name__ == "__main__":
    unittest.main()
