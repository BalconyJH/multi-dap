"""Explicit, process-owned TimeMachine API mechanisms for Python 3.8+.

Import and static inspection never load the vendor DLL. Native calls belong to
the thread that explicitly initializes the binding; the owner must pump events
regularly and apply its own process deadline and actor authorization policy.
This module has no command-line, socket, acquisition, or automatic cleanup path.
"""

import contextlib
import ctypes
import hashlib
import os
import re
import stat
import struct
import sys
import threading
from collections import deque


MAX_PACKET_BATCH = 4096
MAX_SESSIONS = 4096
MAX_NOTIFICATIONS = 4096
MAX_LIBRARY_BYTES = 64 << 20
MAX_METADATA_BYTES = 1 << 20
MAX_UINT64 = (1 << 64) - 1
MAX_INT32 = (1 << 31) - 1

_EXPORTS = (
    "tm_init", "tm_next_id", "tm_connect", "tm_disconnect", "tm_get_info",
    "tm_get_num_packets", "tm_get_packets", "tm_register_callback",
    "tm_process_events", "tm_start_trace", "tm_stop_trace",
)
_X86_ARGUMENT_BYTES = {
    "tm_init": 4, "tm_next_id": 4, "tm_connect": 4, "tm_disconnect": 4,
    "tm_get_info": 12, "tm_get_num_packets": 4, "tm_get_packets": 20,
    "tm_register_callback": 12, "tm_process_events": 0,
    "tm_start_trace": 4, "tm_stop_trace": 4,
}
_CALLBACK_NAMES = (
    "trace_start", "trace_stop", "select_packet", "data_clear",
    "new_data_available",
)
_INVALIDATING = frozenset((
    "trace_start", "data_clear", "packet_count_regressed",
))
_PROCESS_LOCK = threading.Lock()
_PROCESS_OWNER = None


class NativeError(Exception):
    """Only a fixed error code crosses the native implementation boundary."""

    def __init__(self, code):
        self.code = code
        super().__init__("timemachine: " + code)


class _Info716(ctypes.Structure):
    _fields_ = [
        ("has_cycle_counts", ctypes.c_uint32),
        ("has_cache_misses", ctypes.c_uint32),
        ("timebase", ctypes.c_uint32),
        ("has_data_addresses", ctypes.c_uint32),
        ("has_data_values", ctypes.c_uint32),
        ("has_unexecuted", ctypes.c_uint32),
        ("has_variable_length_instructions", ctypes.c_uint32),
        ("identifier", ctypes.c_ubyte * 32),
    ]


class _Info814(ctypes.Structure):
    _fields_ = _Info716._fields_[:6] + [
        ("has_64bit_addrs", ctypes.c_uint32),
    ] + _Info716._fields_[6:]


class _Payload(ctypes.Union):
    _fields_ = [("raw", ctypes.c_ubyte * 16), ("alignment", ctypes.c_uint64 * 2)]


class _Packet(ctypes.Structure):
    _fields_ = [
        ("type", ctypes.c_uint8), ("reconstructed", ctypes.c_uint8),
        ("address", ctypes.c_uint64), ("payload", _Payload),
    ]


# API entry points are stdcall on Windows, but the callback typedef is cdecl.
_CALLBACK = ctypes.CFUNCTYPE(None, ctypes.c_void_p, ctypes.c_void_p)
_PROFILES = {
    "7.1.6d": (0x014C, 32, _Info716),
    "8.1.4": (0x8664, 64, _Info814),
}


def _profile(version):
    if not isinstance(version, str) or version not in _PROFILES:
        raise NativeError("unsupported_release")
    return _PROFILES[version]


def _export_symbol(name, machine):
    if machine == 0x014C:
        return "_%s@%d" % (name, _X86_ARGUMENT_BYTES[name])
    return name


def _integer(value, minimum, maximum, code="invalid_argument"):
    if type(value) is not int or value < minimum or value > maximum:
        raise NativeError(code)
    return value


def _root(installation):
    if not isinstance(installation, str) or not os.path.isabs(installation):
        raise NativeError("invalid_installation")
    if "\x00" in installation:
        raise NativeError("invalid_installation")
    if os.name == "nt":
        if installation.startswith(("\\\\", "//")):
            raise NativeError("invalid_installation")
        if ":" in os.path.splitdrive(installation)[1]:
            raise NativeError("invalid_installation")
    try:
        result = os.path.realpath(installation)
        if not os.path.isdir(result):
            raise NativeError("invalid_installation")
        return result
    except (OSError, ValueError):
        raise NativeError("invalid_installation") from None


def _contained_path(root, *parts):
    candidate = os.path.join(root, *parts)
    try:
        resolved = os.path.realpath(candidate)
        if os.path.commonpath((root, resolved)) != root:
            raise NativeError("installation_path_escape")
        return candidate
    except (OSError, ValueError):
        raise NativeError("invalid_installation") from None


def _read_regular(path, limit):
    try:
        before = os.lstat(path)
        if not stat.S_ISREG(before.st_mode) or before.st_size > limit:
            raise NativeError("invalid_installation_file")
        with open(path, "rb") as source:
            opened = os.fstat(source.fileno())
            if not os.path.samestat(before, opened):
                raise NativeError("installation_file_changed")
            data = source.read(limit + 1)
            after = os.fstat(source.fileno())
        if len(data) > limit:
            raise NativeError("installation_file_too_large")
        if (opened.st_size, opened.st_mtime_ns) != (after.st_size, after.st_mtime_ns):
            raise NativeError("installation_file_changed")
        return data
    except (OSError, ValueError):
        raise NativeError("installation_file_unavailable") from None


def _release(root, version):
    patterns = (
        (("defaults", "licensing", "install_info.txt"),
         r"(?im)^[ \t]*Product Release Name[ \t]*:[ \t]*MULTI(?: IDE)?[ \t]+([0-9A-Za-z.+_-]+)[ \t]*$"),
        (("install_summary.txt",),
         r'(?im)^[ \t]*Installing[ \t]+"MULTI(?: IDE)?[ \t]+([0-9A-Za-z.+_-]+)"[ \t]+to[ \t]+"[^"\r\n]*"[ \t]*$'),
    )
    found = False
    for parts, pattern in patterns:
        path = _contained_path(root, *parts)
        if not os.path.lexists(path):
            continue
        try:
            text = _read_regular(path, MAX_METADATA_BYTES).decode("utf-8-sig").replace("\r\n", "\n")
        except UnicodeError:
            raise NativeError("invalid_release_metadata") from None
        releases = re.findall(pattern, text)
        if not releases or any(release != version for release in releases):
            raise NativeError("release_mismatch")
        found = True
    if not found:
        raise NativeError("release_metadata_unavailable")


def _layout(version):
    _, pointer_bits, info = _profile(version)
    fields = {name: getattr(info, name).offset for name, _ in info._fields_}
    expected_size = 64 if version == "8.1.4" else 60
    expected_offsets = {name: index * 4 for index, (name, _) in enumerate(info._fields_)}
    if (sys.byteorder != "little" or ctypes.sizeof(ctypes.c_int) != 4 or
            ctypes.sizeof(ctypes.c_uint64) != 8 or ctypes.sizeof(_Packet) != 32 or
            ctypes.alignment(_Packet) != 8 or ctypes.sizeof(_Payload) != 16 or
            ctypes.sizeof(info) != expected_size or fields != expected_offsets or
            (_Packet.type.offset, _Packet.reconstructed.offset,
             _Packet.address.offset, _Packet.payload.offset) != (0, 1, 8, 16)):
        raise NativeError("unsupported_host_layout")
    return {
        "byte_order": "little", "pointer_bits": pointer_bits,
        "packet_size": 32, "packet_alignment": 8,
        "packet_offsets": {"type": 0, "reconstructed": 1, "address": 8, "payload": 16},
        "info_size": expected_size, "info_offsets": fields,
    }


def _pe_exports(data, expected_machine):
    """Read only the bounded PE headers, sections and named export table."""
    def unpack(fmt, offset):
        size = struct.calcsize(fmt)
        if offset < 0 or offset + size > len(data):
            raise NativeError("invalid_library_image")
        return struct.unpack_from(fmt, data, offset)

    if len(data) < 64 or data[:2] != b"MZ":
        raise NativeError("invalid_library_image")
    pe = unpack("<I", 0x3C)[0]
    if pe < 64 or pe + 24 > len(data) or data[pe:pe + 4] != b"PE\x00\x00":
        raise NativeError("invalid_library_image")
    machine, section_count = unpack("<HH", pe + 4)
    optional_size, characteristics = unpack("<HH", pe + 20)
    if machine != expected_machine:
        raise NativeError("library_architecture_mismatch")
    if not 1 <= section_count <= 96 or not characteristics & 0x2000:
        raise NativeError("invalid_library_image")
    optional = pe + 24
    magic = unpack("<H", optional)[0]
    directory_offset = {0x014C: (0x10B, 96), 0x8664: (0x20B, 112)}[machine]
    if magic != directory_offset[0] or optional_size < directory_offset[1] + 8:
        raise NativeError("invalid_library_image")
    if unpack("<I", optional + directory_offset[1] - 4)[0] < 1:
        raise NativeError("invalid_library_image")
    export_rva, export_size = unpack("<II", optional + directory_offset[1])
    if export_size < 40 or export_size > MAX_LIBRARY_BYTES:
        raise NativeError("invalid_library_exports")
    sections = []
    start = optional + optional_size
    for index in range(section_count):
        entry = start + index * 40
        virtual_size, address, raw_size, raw_offset = unpack("<IIII", entry + 8)
        flags = unpack("<I", entry + 36)[0]
        if raw_offset + raw_size > len(data):
            raise NativeError("invalid_library_image")
        sections.append((address, virtual_size, raw_offset, raw_size, flags))

    def rva_offset(rva, size, executable=False):
        candidates = []
        for address, virtual_size, raw_offset, raw_size, flags in sections:
            relative = rva - address
            if (relative >= 0 and relative + size <= raw_size and
                    relative + size <= max(virtual_size, raw_size) and
                    (not executable or flags & 0x20000000)):
                candidates.append(raw_offset + relative)
        if len(candidates) != 1:
            raise NativeError("invalid_library_image")
        return candidates[0]

    export = rva_offset(export_rva, 40)
    function_count, name_count, functions, names, ordinals = unpack("<IIIII", export + 20)
    if not 1 <= function_count <= 65536 or not 1 <= name_count <= 65536:
        raise NativeError("invalid_library_exports")
    function_table = rva_offset(functions, function_count * 4)
    name_table = rva_offset(names, name_count * 4)
    ordinal_table = rva_offset(ordinals, name_count * 2)
    required = {_export_symbol(name, machine): name for name in _EXPORTS}
    found = set()
    for index in range(name_count):
        name_rva = unpack("<I", name_table + index * 4)[0]
        name_start = rva_offset(name_rva, 1)
        name_end = data.find(b"\x00", name_start, min(len(data), name_start + 256))
        if name_end < 0:
            raise NativeError("invalid_library_exports")
        rva_offset(name_rva, name_end - name_start + 1)
        try:
            name = data[name_start:name_end].decode("ascii")
        except UnicodeError:
            raise NativeError("invalid_library_exports") from None
        if name not in required:
            continue
        canonical = required[name]
        if canonical in found:
            raise NativeError("invalid_library_exports")
        ordinal = unpack("<H", ordinal_table + index * 2)[0]
        if ordinal >= function_count:
            raise NativeError("invalid_library_exports")
        address = unpack("<I", function_table + ordinal * 4)[0]
        if export_rva <= address < export_rva + export_size:
            raise NativeError("forwarded_library_export")
        rva_offset(address, 1, executable=True)
        found.add(canonical)
    if found != set(_EXPORTS):
        raise NativeError("required_export_unavailable")
    return list(_EXPORTS)


def _inspect(installation, version):
    machine, pointer_bits, _ = _profile(version)
    root = _root(installation)
    _release(root, version)
    library = _contained_path(root, "timemachine_api.dll")
    data = _read_regular(library, MAX_LIBRARY_BYTES)
    exports = _pe_exports(data, machine)
    headers = {}
    for name in ("timemachine_api.h", "ts_packet.h"):
        content = _read_regular(_contained_path(root, "timemachine_api", name), MAX_METADATA_BYTES)
        headers[name] = hashlib.sha256(content).hexdigest()
    return root, library, {
        "schema": "multi-dap.timemachine-abi.v1", "multi_version": version,
        "machine": machine, "pointer_bits": pointer_bits,
        "runtime_pointer_bits": ctypes.sizeof(ctypes.c_void_p) * 8,
        "runtime_compatible": ctypes.sizeof(ctypes.c_void_p) * 8 == pointer_bits,
        "dll_sha256": hashlib.sha256(data).hexdigest(),
        "header_sha256": headers, "layout": _layout(version), "exports": exports,
        "export_symbols": {name: _export_symbol(name, machine) for name in _EXPORTS},
        "layout_evidence": "builtin_profile_and_ctypes",
        "dll_loaded": False, "target_contacted": False, "vendor_qualified": False,
    }


def inspect_abi(installation, version):
    """Inspect installation files without DLL loading or session contact."""
    return _inspect(installation, version)[2]


@contextlib.contextmanager
def _pinned_library(path):
    # The Windows handle prevents writing or replacing the image between hash
    # verification and LoadLibrary. A loaded image then remains owned by ctypes.
    if os.name != "nt":
        with open(path, "rb") as source:
            yield source
        return
    import msvcrt
    kernel = ctypes.WinDLL("kernel32.dll", winmode=0x800)
    create = kernel.CreateFileW
    create.argtypes = [ctypes.c_wchar_p, ctypes.c_uint32, ctypes.c_uint32,
                       ctypes.c_void_p, ctypes.c_uint32, ctypes.c_uint32, ctypes.c_void_p]
    create.restype = ctypes.c_void_p
    close = kernel.CloseHandle
    close.argtypes = [ctypes.c_void_p]
    close.restype = ctypes.c_int
    handle = create(path, 0x80000000, 1, None, 3, 0x80, None)
    if handle == ctypes.c_void_p(-1).value or not handle:
        raise NativeError("library_pin_failed")
    try:
        descriptor = msvcrt.open_osfhandle(handle, os.O_RDONLY | os.O_BINARY)
    except BaseException as error:
        close(handle)
        if not isinstance(error, Exception):
            raise
        raise NativeError("library_pin_failed") from None
    with os.fdopen(descriptor, "rb") as source:
        yield source


def _load_library(path):
    # Only the selected DLL's directory and System32 may resolve dependencies.
    return ctypes.WinDLL(path, winmode=0x100 | 0x800)


class NativeTimeMachine:
    """Mechanism-only binding; use one instance in an owned helper process."""

    def __init__(self, installation, version, expected_dll_sha256,
                 notification_limit=256, _loader=None):
        _profile(version)
        if (not isinstance(expected_dll_sha256, str) or
                re.fullmatch(r"[0-9a-fA-F]{64}", expected_dll_sha256) is None):
            raise NativeError("invalid_library_digest")
        _integer(notification_limit, 1, MAX_NOTIFICATIONS)
        self._installation = installation
        self._version = version
        self._expected_digest = expected_dll_sha256.lower()
        self._loader = _loader
        self._notification_limit = notification_limit
        self._notifications = deque()
        self._notification_lock = threading.Lock()
        self._dropped = 0
        self._ignored_callbacks = 0
        self._callback_failures = 0
        self._reported_callback_failures = 0
        self._callback_interrupt = None
        self._generation_invalidated = False
        self._last_packet_count = None
        self._callbacks = []
        self._owner_thread = None
        self._connection = None
        self._initialization_uncertain = False
        self._connection_uncertain = False
        self._disconnect_uncertain = False
        self._trace_control_uncertain = False
        self._library = None
        self._functions = {}
        self._state = "new"

    def initialize(self):
        global _PROCESS_OWNER
        if self._state != "new":
            raise NativeError("already_initialized")
        if sys.version_info < (3, 8):
            raise NativeError("unsupported_python_runtime")
        if os.name != "nt" and self._loader is None:
            raise NativeError("unsupported_platform")
        root, library_path, evidence = _inspect(self._installation, self._version)
        if not evidence["runtime_compatible"]:
            raise NativeError("runtime_architecture_mismatch")
        if evidence["dll_sha256"] != self._expected_digest:
            raise NativeError("library_digest_mismatch")
        try:
            encoded_root = root.encode("ascii")
        except UnicodeError:
            raise NativeError("unsupported_installation_encoding") from None
        with _PROCESS_LOCK:
            if _PROCESS_OWNER is not None:
                raise NativeError("native_owner_exists")
            _PROCESS_OWNER = self
        self._owner_thread = threading.get_ident()
        self._state = "initializing"
        try:
            with _pinned_library(library_path) as source:
                data = source.read(MAX_LIBRARY_BYTES + 1)
                if (len(data) > MAX_LIBRARY_BYTES or
                        hashlib.sha256(data).hexdigest() != self._expected_digest):
                    raise NativeError("library_digest_mismatch")
                self._initialization_uncertain = True
                self._library = (self._loader or _load_library)(library_path)
                self._bind()
            if self._invoke("tm_init", encoded_root) != 0:
                raise NativeError("native_initialization_failed")
            self._state = "ready"
            self._initialization_uncertain = False
        except BaseException as error:
            if not self._initialization_uncertain and self._library is None:
                self._state = "poisoned"
                self._release_owner()
            else:
                # The API has no global deinitialization contract. After a
                # loaded-runtime initialization failure, recycle this helper
                # instead of running a second tm_init in uncertain state.
                self._state = "initialization_failed"
            if not isinstance(error, Exception) or isinstance(error, NativeError):
                raise
            raise NativeError("native_initialization_failed") from None
        evidence["dll_loaded"] = True
        evidence["native_initialized"] = True
        evidence.pop("target_contacted", None)
        evidence["session_connected"] = False
        return evidence

    def _bind(self):
        info = _profile(self._version)[2]
        signatures = {
            "tm_init": ([ctypes.c_char_p], ctypes.c_int),
            "tm_next_id": ([ctypes.c_int], ctypes.c_int),
            "tm_connect": ([ctypes.c_int], ctypes.c_void_p),
            "tm_disconnect": ([ctypes.c_void_p], None),
            "tm_get_info": ([ctypes.c_void_p, ctypes.POINTER(info), ctypes.c_int], ctypes.c_int),
            "tm_get_num_packets": ([ctypes.c_void_p], ctypes.c_uint64),
            "tm_get_packets": ([ctypes.c_void_p, ctypes.c_uint64, ctypes.c_int,
                                ctypes.POINTER(_Packet)], ctypes.c_int),
            "tm_register_callback": ([ctypes.c_void_p, ctypes.c_int, _CALLBACK], None),
            "tm_process_events": ([], None),
            "tm_start_trace": ([ctypes.c_void_p], None),
            "tm_stop_trace": ([ctypes.c_void_p], None),
        }
        for name in _EXPORTS:
            function = getattr(self._library, _export_symbol(name, _profile(self._version)[0]))
            function.argtypes, function.restype = signatures[name]
            self._functions[name] = function

    def _owner(self):
        if self._owner_thread is None:
            raise NativeError("not_initialized")
        if threading.get_ident() != self._owner_thread:
            raise NativeError("wrong_owner_thread")

    def _ready(self):
        self._owner()
        if self._state not in ("ready", "connected") or self._uncertain():
            raise NativeError("native_state_unavailable")

    def _uncertain(self):
        return (self._initialization_uncertain or self._connection_uncertain or
                self._disconnect_uncertain or self._trace_control_uncertain)

    def _open(self):
        self._ready()
        if self._connection is None:
            raise NativeError("not_connected")

    def _invoke(self, name, *args):
        self._owner()
        try:
            self._raise_callback_interrupt()
            result = self._functions[name](*args)
            self._raise_callback_interrupt()
            return result
        except BaseException as error:
            self._state = "poisoned"
            if not isinstance(error, Exception):
                raise
            raise NativeError("native_call_failed") from None

    def _raise_callback_interrupt(self):
        interruption = self._callback_interrupt
        if interruption is not None:
            self._callback_interrupt = None
            raise interruption

    def enumerate_ids(self, limit=256):
        self._ready()
        _integer(limit, 1, MAX_SESSIONS)
        result, previous = [], 0
        for _ in range(limit + 1):
            value = self._invoke("tm_next_id", previous)
            _integer(value, 0, MAX_INT32, "invalid_native_result")
            if value == 0:
                return result
            if value <= previous:
                raise NativeError("invalid_session_inventory")
            if len(result) == limit:
                raise NativeError("session_inventory_limit")
            result.append(value)
            previous = value
        raise NativeError("session_inventory_limit")

    def connect(self, session_id):
        self._ready()
        _integer(session_id, 1, MAX_INT32)
        if self._connection is not None:
            raise NativeError("already_connected")
        if session_id not in self.enumerate_ids():
            raise NativeError("session_unavailable")
        try:
            self._connection_uncertain = True
            connection = self._invoke("tm_connect", session_id)
            if connection is None or connection == 0:
                self._connection_uncertain = False
                raise NativeError("connection_failed")
            _integer(connection, 1, (1 << _profile(self._version)[1]) - 1,
                     "connection_failed")
            self._connection = connection
            self._connection_uncertain = False
            self._state = "connected"
            for index, name in enumerate(_CALLBACK_NAMES):
                callback = _CALLBACK(self._callback(name))
                self._callbacks.append(callback)
                self._invoke("tm_register_callback", connection, index, callback)
        except BaseException as error:
            if self._connection_uncertain or self._connection is not None:
                self._state = "poisoned"
            if not isinstance(error, Exception) or isinstance(error, NativeError):
                raise
            self._state = "poisoned"
            raise NativeError("native_call_failed") from None

    def _callback(self, name):
        def receive(connection, _data):
            # Never dereference the vendor's transient callback data pointer.
            # Callback work is bounded and never invokes another native API.
            try:
                with self._notification_lock:
                    if connection != self._connection or self._connection is None:
                        self._ignored_callbacks += 1
                        self._generation_invalidated = True
                        return
                    if name in ("trace_start", "data_clear"):
                        self._last_packet_count = None
                    self._enqueue_locked(name)
            except BaseException as error:
                # An exception escaping a ctypes callback prints a traceback
                # to stderr. Defer control-flow exits to the native owner.
                self._callback_failures += 1
                if not isinstance(error, Exception) and self._callback_interrupt is None:
                    self._callback_interrupt = error
        return receive

    def _enqueue_locked(self, name):
        if name in _INVALIDATING:
            self._generation_invalidated = True
        if len(self._notifications) == self._notification_limit:
            self._dropped += 1
            self._generation_invalidated = True
        else:
            self._notifications.append(name)

    def drain_notifications(self):
        """Drain fixed notifications without native calls or target payloads."""
        with self._notification_lock:
            callback_failures = self._callback_failures
            callback_failed = callback_failures != self._reported_callback_failures
            result = {
                "events": list(self._notifications), "dropped": self._dropped,
                "ignored_callbacks": self._ignored_callbacks,
                "callback_failed": callback_failed,
                "generation_invalidated": self._generation_invalidated or callback_failed,
            }
            self._notifications.clear()
            self._dropped = 0
            self._ignored_callbacks = 0
            self._reported_callback_failures = callback_failures
            self._generation_invalidated = False
            return result

    def poll_events(self):
        self._open()
        self._invoke("tm_process_events")

    def get_info(self):
        self._open()
        info = _profile(self._version)[2]()
        if self._invoke("tm_get_info", self._connection, ctypes.byref(info), ctypes.sizeof(info)) != 0:
            raise NativeError("info_unavailable")
        return ctypes.string_at(ctypes.addressof(info), ctypes.sizeof(info))

    def packet_count(self):
        self._open()
        count = self._invoke("tm_get_num_packets", self._connection)
        _integer(count, 0, MAX_UINT64 - 1, "packet_count_unavailable")
        with self._notification_lock:
            if self._last_packet_count is not None and count < self._last_packet_count:
                self._enqueue_locked("packet_count_regressed")
            self._last_packet_count = count
        return count

    def get_packets(self, start, count):
        self._open()
        _integer(start, 0, MAX_UINT64)
        _integer(count, 1, MAX_PACKET_BATCH)
        if start > MAX_UINT64 - count:
            raise NativeError("packet_range_overflow")
        packets = (_Packet * count)()
        actual = self._invoke("tm_get_packets", self._connection, start, count, packets)
        _integer(actual, 0, count, "packets_unavailable")
        return ctypes.string_at(ctypes.addressof(packets), actual * ctypes.sizeof(_Packet)), actual

    def start_trace(self, authorized=False):
        self._open()
        if authorized is not True:
            raise NativeError("trace_start_not_authorized")
        self._trace_control_uncertain = True
        self._invoke("tm_start_trace", self._connection)
        self._trace_control_uncertain = False

    def stop_trace(self, authorized=False):
        self._open()
        if authorized is not True:
            raise NativeError("trace_stop_not_authorized")
        self._trace_control_uncertain = True
        self._invoke("tm_stop_trace", self._connection)
        self._trace_control_uncertain = False

    def close(self):
        """Disconnect once, explicitly; never stop trace or change the target."""
        if self._state == "new":
            self._state = "closed"
            return
        if self._state == "closed" and self._owner_thread is None:
            return
        self._owner()
        if self._state in ("close_failed", "initialization_failed") or self._uncertain():
            raise NativeError("reconciliation_required")
        if self._state == "closed":
            self._release_owner()
            return
        if self._connection is not None:
            try:
                # Set before entry: an interruption after native return but
                # before Python state commit must not permit a second close.
                self._disconnect_uncertain = True
                self._invoke("tm_disconnect", self._connection)
                self._connection = None
                self._callbacks = []
                self._state = "closed"
                self._disconnect_uncertain = False
                self._release_owner()
            except BaseException as error:
                self._state = "close_failed"
                # Keep callbacks and the DLL alive: disconnect completion is
                # unknown, and retrying that native pointer would be unsafe.
                if not isinstance(error, Exception) or isinstance(error, NativeError):
                    raise
                raise NativeError("native_call_failed") from None
            return
        self._connection = None
        self._callbacks = []
        self._state = "closed"
        self._release_owner()

    def _release_owner(self):
        global _PROCESS_OWNER
        with _PROCESS_LOCK:
            if _PROCESS_OWNER is self:
                _PROCESS_OWNER = None
