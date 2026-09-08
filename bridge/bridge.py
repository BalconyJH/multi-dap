"""Loopback-only MULTI Bridge Protocol v1 server for MULTI-Python 2.7."""

from __future__ import absolute_import

import argparse
import base64
import json
import ntpath
import os
import re
import socket
import shutil
import tempfile

try:
    unicode
except NameError:  # Python 3 host tests; MULTI itself supplies these names.
    unicode = str
    long = int
    binary_type = bytes
else:
    binary_type = str


PROTOCOL_VERSION = 1
BRIDGE_VERSION = "0.1.0"
DEFAULT_MAX_MESSAGE_SIZE = 1 << 20
LOOPBACK_HOST = "127.0.0.1"
METHODS = frozenset((
    "open", "close", "state", "cores", "resume", "halt", "step_in", "next", "run_commands",
    "console_read", "console_reset",
    "memory_read", "disassemble",
))
SESSION_MODES = frozenset((u"cold", u"warm"))
COLD_PREPARATIONS = frozenset((u"", u"already_present_no_verify"))
PREPARATION_ALREADY_PRESENT_NO_VERIFY = u"already_present_no_verify"
MAX_CONSOLE_OUTPUT_BYTES = 128 * 1024
# A replacement character can require three UTF-8 bytes.  Keep the capture
# substantially below the response limit even for completely invalid input
# and leave room for the JSON envelope.
MAX_CONSOLE_PANE_BYTES = 64 * 1024
MAX_CONSOLE_FILE_BYTES = 1 << 20
CONSOLE_PREFIX_BYTES = 256
MAX_MEMORY_READ_BYTES = 64 * 1024
MAX_DISASSEMBLE_BYTES = 48 * 1024 + 6
MAX_DISASSEMBLE_OUTPUT_BYTES = 1 << 20
COMPONENT_ID_PATTERN = re.compile(r"^[A-Za-z_][A-Za-z0-9_.-]*\.[0-9]+$")


class BridgeError(Exception):
    """A request failure that has a defined MBP error envelope."""

    def __init__(self, kind, message):
        Exception.__init__(self, message)
        self.kind = kind
        self.message = message


def _ascii_host(value):
    """Return an ASCII host literal, rejecting names and non-ASCII input."""
    if isinstance(value, unicode):
        try:
            encoded = value.encode("ascii")
        except UnicodeEncodeError:
            raise ValueError("rpc host must be an ASCII IP literal")
    elif isinstance(value, binary_type):
        encoded = value
    else:
        raise ValueError("rpc host must be an IP literal")
    try:
        return encoded.decode("ascii")
    except UnicodeDecodeError:
        raise ValueError("rpc host must be an ASCII IP literal")


def _is_decimal_ipv4_literal(host):
    parts = host.split(u".")
    if len(parts) != 4:
        return False
    for part in parts:
        if not part or any(character < u"0" or character > u"9" for character in part):
            return False
        if len(part) > 3 or len(part) > 1 and part.startswith(u"0"):
            return False
        if int(part) > 255:
            return False
    return True


def validate_rpc_host(host):
    """Accept only numeric loopback IPv4/IPv6 literals for the MBP listener."""
    text = _ascii_host(host)
    if _is_decimal_ipv4_literal(text):
        if int(text.split(u".")[0]) == 127:
            return text
    if text == u"::1":
        return text
    raise ValueError("rpc host must be a numeric loopback IP literal")


def as_unicode(value):
    """Return a JSON string value as unicode, rejecting non-UTF-8 input."""
    if isinstance(value, unicode):
        return value
    if isinstance(value, binary_type):
        return value.decode("utf-8")
    raise BridgeError("invalid_params", "expected a UTF-8 string")


def multi_text(value):
    """Make MULTI output safe for the UTF-8 wire and report lossiness."""
    if value is None:
        return u"", False
    if isinstance(value, unicode):
        return value, False
    if isinstance(value, binary_type):
        try:
            return value.decode("utf-8"), False
        except UnicodeDecodeError:
            return value.decode("utf-8", "replace"), True
    return unicode(value), False


def json_value(value):
    """Recursively convert MULTI values to JSON values with unicode strings."""
    if isinstance(value, unicode):
        return value, False
    if isinstance(value, binary_type):
        return multi_text(value)
    if isinstance(value, dict):
        result = {}
        lossy = False
        for key, item in value.items():
            text_key, key_lossy = multi_text(key)
            text_item, item_lossy = json_value(item)
            result[text_key] = text_item
            lossy = lossy or key_lossy or item_lossy
        return result, lossy
    if isinstance(value, (list, tuple)):
        result = []
        lossy = False
        for item in value:
            text_item, item_lossy = json_value(item)
            result.append(text_item)
            lossy = lossy or item_lossy
        return result, lossy
    if value is None or isinstance(value, (bool, int, long, float)):
        return value, False
    return multi_text(value)


def encode_frame(frame):
    """Encode one frame as a UTF-8 NDJSON line."""
    text = json.dumps(frame, ensure_ascii=False, separators=(",", ":"))
    if not isinstance(text, unicode):
        text = text.decode("utf-8")
    return text.encode("utf-8") + b"\n"


def decode_frame(line):
    """Decode one UTF-8 JSON object frame."""
    try:
        text = line.decode("utf-8")
    except UnicodeDecodeError:
        raise BridgeError("protocol_error", "frame is not valid UTF-8")
    try:
        value = json.loads(text)
    except (TypeError, ValueError):
        raise BridgeError("protocol_error", "frame is not valid JSON")
    if not isinstance(value, dict):
        raise BridgeError("protocol_error", "frame must be a JSON object")
    return value


class BoundedNDJSONReader(object):
    """Read newline-delimited frames without buffering an unbounded line."""

    def __init__(self, connection, max_message_size):
        self.connection = connection
        self.max_message_size = max_message_size
        self.buffer = b""

    def read(self):
        while True:
            newline = self.buffer.find(b"\n")
            if newline >= 0:
                line = self.buffer[:newline]
                self.buffer = self.buffer[newline + 1:]
                if line.endswith(b"\r"):
                    line = line[:-1]
                if len(line) > self.max_message_size:
                    raise BridgeError("protocol_error", "frame exceeds max_message_size")
                return line
            if len(self.buffer) > self.max_message_size:
                raise BridgeError("protocol_error", "frame exceeds max_message_size")
            chunk = self.connection.recv(4096)
            if not chunk:
                if self.buffer:
                    raise BridgeError("protocol_error", "frame is not terminated by newline")
                return None
            self.buffer += chunk
            if len(self.buffer) > self.max_message_size and b"\n" not in self.buffer:
                raise BridgeError("protocol_error", "frame exceeds max_message_size")


class MultiBridge(object):
    """Dispatch the deliberately small M1 method set onto one MULTI window."""

    def __init__(self, debugger_factory=None, session_mode=u"cold", console_directory=None):
        if session_mode not in SESSION_MODES:
            raise ValueError("session mode must be cold or warm")
        self.debugger_factory = debugger_factory
        self.debugger = None
        self.window = None
        self.session_mode = session_mode
        self.disconnect_on_close = False
        self.console_directory = console_directory
        self.console_owned_directory = False
        self.console_paths = None
        self.console_previous = {"server": None, "io": None}
        self.inspection_directory = None
        self.inspection_owned_directory = False

    def _inspection_file(self):
        """Return one bridge-owned, fixed snapshot path for binary reads."""
        if self.inspection_directory is None:
            if self.console_directory is None:
                self.inspection_directory = tempfile.mkdtemp(prefix="multi-dap-inspection-")
                self.inspection_owned_directory = True
            else:
                self.inspection_directory = os.path.abspath(self.console_directory)
                self.inspection_owned_directory = False
        if not os.path.isdir(self.inspection_directory):
            raise BridgeError("memory_unavailable", "memory workspace is unavailable")
        path = os.path.abspath(os.path.join(self.inspection_directory, "memory.bin"))
        if (os.path.dirname(path) != os.path.abspath(self.inspection_directory) or
                any(character in path for character in u"\"\r\n;")):
            raise BridgeError("memory_unavailable", "memory workspace path is unsafe")
        return path

    def _cleanup_inspection(self):
        directory = self.inspection_directory
        owned = self.inspection_owned_directory
        self.inspection_directory = None
        self.inspection_owned_directory = False
        if directory and owned:
            shutil.rmtree(directory, ignore_errors=True)

    def _console_files(self):
        if self.console_paths is not None:
            return self.console_paths
        directory = self.console_directory
        if directory is None:
            directory = tempfile.mkdtemp(prefix="multi-dap-console-")
            self.console_owned_directory = True
        directory = os.path.abspath(directory)
        if not os.path.isdir(directory):
            raise BridgeError("console_unavailable", "console workspace is unavailable")
        paths = {
            "server": os.path.join(directory, "target-pane.txt"),
            "io": os.path.join(directory, "io-pane.txt"),
        }
        for path in paths.values():
            # The names are bridge constants, but retain this boundary check so
            # an unexpected launcher path can never become MULTI command text.
            if (os.path.dirname(os.path.abspath(path)) != directory or
                    any(character in path for character in u"\"\r\n;")):
                raise BridgeError("console_unavailable", "console workspace path is unsafe")
        self.console_paths = paths
        return paths

    def _cleanup_console(self):
        paths = self.console_paths
        self.console_paths = None
        self.console_previous = {"server": None, "io": None}
        if paths is not None:
            for path in paths.values():
                try:
                    os.unlink(path)
                except OSError:
                    pass
        if self.console_owned_directory:
            directory = self.console_directory
            # directory is only set for caller-owned paths; a temporary path is
            # captured from the files when no ready-file workspace was supplied.
            if paths:
                directory = os.path.dirname(paths["server"])
            if directory:
                shutil.rmtree(directory, ignore_errors=True)
            self.console_owned_directory = False

    def cleanup(self):
        """Release bridge-owned snapshots on transport termination."""
        self._cleanup_console()
        self._cleanup_inspection()

    def _inspection_params(self, params, count_name, maximum):
        """Validate numeric-only M5 inspection inputs before command assembly."""
        if not isinstance(params, dict) or set(params) != set(("component", "address", count_name)):
            raise BridgeError("invalid_params", "inspection request has invalid parameters")
        component = params["component"]
        address = params["address"]
        count = params[count_name]
        if not isinstance(component, unicode) or not COMPONENT_ID_PATTERN.match(component):
            raise BridgeError("invalid_params", "component is invalid")
        for value, label, limit in ((address, "address", None), (count, count_name, maximum)):
            if isinstance(value, bool) or not isinstance(value, (int, long)) or value < 0:
                raise BridgeError("invalid_params", "%s must be a non-negative integer" % label)
            if limit is not None and (value == 0 or value > limit):
                raise BridgeError("invalid_params", "%s is out of range" % label)
        if address > 0xffffffffffffffff:
            raise BridgeError("invalid_params", "address is out of range")
        return component, address, count

    def _console_file_state(self, path):
        try:
            size = os.path.getsize(path)
            if size > MAX_CONSOLE_FILE_BYTES:
                raise BridgeError("console_unavailable", "console snapshot exceeds size limit")
            with open(path, "rb") as input_file:
                prefix = input_file.read(CONSOLE_PREFIX_BYTES)
                input_file.seek(max(0, size - CONSOLE_PREFIX_BYTES))
                suffix = input_file.read(CONSOLE_PREFIX_BYTES)
        except IOError:
            raise BridgeError("console_unavailable", "console snapshot cannot be read")
        except OSError:
            raise BridgeError("console_unavailable", "console snapshot cannot be read")
        return (size, prefix, suffix)

    def _read_console_range(self, path, offset, size):
        """Read an output range, retaining only its newest bounded portion."""
        if size <= offset:
            return b"", False
        start = offset
        truncated = False
        if size - start > MAX_CONSOLE_PANE_BYTES:
            start = size - MAX_CONSOLE_PANE_BYTES
            truncated = True
        try:
            with open(path, "rb") as input_file:
                input_file.seek(start)
                return input_file.read(MAX_CONSOLE_PANE_BYTES), truncated
        except IOError:
            raise BridgeError("console_unavailable", "console snapshot cannot be read")

    def _console_increment(self, key, path):
        size, prefix, suffix = self._console_file_state(path)
        previous = self.console_previous[key]
        try:
            if previous is None:
                # The first capture may already exceed the output budget. Preserve
                # its newest bytes, which are where the active console ends.
                increment, truncated = self._read_console_range(path, 0, size)
            else:
                previous_size, previous_prefix, previous_suffix = previous
                if (size > previous_size and prefix.startswith(previous_prefix)):
                    # Same pane image with an appended suffix. Reading from the old
                    # length avoids duplicate tails even after the pane exceeded cap.
                    increment, truncated = self._read_console_range(path, previous_size, size)
                elif size == previous_size and prefix == previous_prefix and suffix == previous_suffix:
                    increment, truncated = b"", False
                elif size < previous_size and not prefix:
                    # An empty pane is a clear, not a replay of preceding text.
                    increment, truncated = b"", False
                else:
                    # Changed prefix/suffix or shrink-with-content: pane rollover.
                    increment, truncated = self._read_console_range(path, 0, size)
        except Exception:
            raise
        try:
            os.unlink(path)
        except OSError:
            raise BridgeError("console_unavailable", "console snapshot cannot be removed")
        # The caller commits both pane baselines only after all snapshot I/O
        # succeeded. Windows may temporarily deny one read while MULTI replaces
        # a pane file; that must not discard either pane's unsent increment.
        return increment, (size, prefix, suffix), truncated

    def _new_debugger(self):
        if self.debugger_factory is not None:
            return self.debugger_factory()
        return GHS_Debugger()

    def _require_window(self):
        if self.window is None:
            raise BridgeError("session_not_open", "MULTI session is not open")
        return self.window

    def _params(self, params, required, optional=()):
        if params is None:
            params = {}
        if not isinstance(params, dict):
            raise BridgeError("invalid_params", "params must be an object")
        allowed = set(required) | set(optional)
        unexpected = set(params) - allowed
        if unexpected:
            raise BridgeError("invalid_params", "request contains an unexpected parameter")
        for key in required:
            if key not in params:
                raise BridgeError("invalid_params", "request is missing a required parameter")
        result = {}
        for key in allowed:
            if key in params:
                result[key] = as_unicode(params[key])
        return result

    def _command_result(self, command):
        window = self._require_window()
        accepted = window.RunCommands(command, 1, 0, 1)
        status = getattr(window, "cmdExecStatus", None)
        if not accepted or status != 1:
            raise BridgeError("multi_refused", "MULTI refused command")
        raw, raw_lossy = multi_text(getattr(window, "cmdExecOutput", u""))
        result = {"raw": raw, "status": status}
        if raw_lossy:
            result["raw_lossy"] = True
        return result

    def _execution(self, method):
        window = self._require_window()
        accepted = getattr(window, method)(0, 0)
        if accepted is not True:
            raise BridgeError("multi_refused", "MULTI refused execution request")
        return {"accepted": True}

    def _normalized_primary_elf(self, value):
        """Return an absolute Windows program identity for exact matching."""
        try:
            text = as_unicode(value).strip().strip(u'"')
        except (BridgeError, UnicodeDecodeError):
            return u""
        if not text:
            return u""
        normalized = ntpath.normcase(ntpath.normpath(text))
        if not ntpath.isabs(normalized):
            return u""
        return normalized

    def _existing_debugger_windows(self):
        """Enumerate Debugger windows in the launcher-selected router."""
        try:
            import ghs_constants
            import ghs_winreg
        except ImportError:
            raise BridgeError("warm_bind_failed", "MULTI window-register modules are unavailable")
        registry = ghs_winreg.GHS_WindowRegister()
        if not getattr(registry, "service", None):
            raise BridgeError("warm_bind_failed", "MULTI window register is unavailable")
        window_list = registry.GetWindowList(False)
        if not isinstance(window_list, dict):
            raise BridgeError("warm_bind_failed", "MULTI window register returned an invalid list")
        debugger_class = getattr(ghs_constants.winClassNames, "debugger", u"")
        if not debugger_class:
            raise BridgeError("warm_bind_failed", "MULTI debugger window class is unavailable")
        windows = registry.CheckWindows(u"", winClass=debugger_class,
                                        fromWinList=window_list)
        if not isinstance(windows, (list, tuple)):
            raise BridgeError("warm_bind_failed", "MULTI window register returned invalid windows")
        return windows

    def _bind_existing_program_window(self, primary_elf):
        """Bind exactly one active full-path primary ELF match, read-only."""
        expected = self._normalized_primary_elf(primary_elf)
        if not expected:
            raise BridgeError("invalid_params", "warm primary_elf must be an absolute Windows path")
        matches = []
        for window in self._existing_debugger_windows():
            try:
                program = self._normalized_primary_elf(window.GetProgram())
            except Exception:
                continue
            if program != expected:
                continue
            try:
                status = window.GetStatus()
            except Exception:
                continue
            if isinstance(status, bool) or status not in (2, 3):
                continue
            try:
                info = window.GetCurPrInfo(u"")
            except Exception:
                continue
            if not isinstance(info, dict) or not info:
                continue
            matches.append(window)
        if len(matches) != 1:
            raise BridgeError("warm_bind_failed", "expected exactly one active primary ELF window")
        return matches[0]

    def open(self, params):
        if not isinstance(params, dict):
            raise BridgeError("invalid_params", "params must be an object")
        mode = as_unicode(params.get("mode", u""))
        if mode not in SESSION_MODES:
            raise BridgeError("invalid_params", "open mode must be cold or warm")
        if mode != self.session_mode:
            raise BridgeError("invalid_params", "open mode does not match bridge session mode")
        values = self._params(
            params,
            ("mode", "project", "connection") if mode == u"cold" else ("mode", "primary_elf"),
            ("setup_script", "setup_script_args", "multi_log", "preparation") if mode == u"cold" else (),
        )
        if self.window is not None:
            raise BridgeError("session_open", "MULTI session is already open")
        if mode == u"warm":
            self.window = self._bind_existing_program_window(values["primary_elf"])
            self.disconnect_on_close = False
            return {"opened": True}
        preparation = values.get("preparation", u"")
        if preparation not in COLD_PREPARATIONS:
            raise BridgeError("invalid_params", "unknown cold preparation")
        debugger = self._new_debugger()
        window = debugger.DebugProgram(values["project"], 0, 1, 0, 1)
        if window is None:
            raise BridgeError("multi_refused", "MULTI could not open debug program")
        connection = debugger.ConnectToTarget(
            values["connection"],
            values.get("setup_script", u""),
            values.get("setup_script_args", u""),
            values.get("multi_log", u""),
            1,
            "",
            0,
        )
        if connection is None or connection is False:
            raise BridgeError("multi_refused", "MULTI could not connect to target")
        if preparation == PREPARATION_ALREADY_PRESENT_NO_VERIFY:
            # This connection is bridge-owned before open is confirmed to Go.
            # Keep preparation transactional so a refusal cannot leave it behind.
            try:
                if window.RunCommands("prepare_target -verify=none", True, False) is not True:
                    raise BridgeError("multi_refused", "MULTI could not prepare target")
            except Exception:
                try:
                    debugger.Disconnect(0)
                except Exception:
                    raise BridgeError(
                        "cold_cleanup_failed",
                        "MULTI could not prepare target or disconnect")
                raise BridgeError("multi_refused", "MULTI could not prepare target")
        # ConnectToTarget rebinds debugger state. Keep the DebugProgram window as the command
        # target; the connection window has no active process (M0 finding 0.4).
        self.debugger = debugger
        self.window = window
        self.disconnect_on_close = True
        return {"opened": True}

    def close(self, params):
        self._params(params, ())
        self._require_window()
        try:
            if self.disconnect_on_close:
                self.debugger.Disconnect(0)
        finally:
            self.window = None
            self.debugger = None
            self.disconnect_on_close = False
            self._cleanup_console()
            self._cleanup_inspection()
        return {"closed": True}

    def state(self, params):
        self._params(params, ())
        window = self._require_window()
        status_before = window.GetStatus()
        if isinstance(status_before, bool) or not isinstance(status_before, (int, long)):
            raise BridgeError("multi_refused", "MULTI returned an invalid process status")
        process_info, lossy = json_value(window.GetCurPrInfo(u""))
        if not isinstance(process_info, dict):
            raise BridgeError("multi_refused", "MULTI returned invalid process information")
        status_after = window.GetStatus()
        if isinstance(status_after, bool) or not isinstance(status_after, (int, long)):
            raise BridgeError("multi_refused", "MULTI returned an invalid process status")
        if status_before != status_after:
            raise BridgeError("state_changed", "MULTI state changed during snapshot")
        result = {"status": status_before, "process_info": process_info}
        if lossy:
            result["raw_lossy"] = True
        return result

    def cores(self, params):
        self._params(params, ())
        result = self._command_result(u"components")
        components = result.pop("raw")
        result["components"] = components
        return result

    def resume(self, params):
        self._params(params, ())
        return self._execution("Resume")

    def halt(self, params):
        self._params(params, ())
        return self._execution("Halt")

    def step_in(self, params):
        self._params(params, ())
        window = self._require_window()
        accepted = window.Step(0, 0, 1)
        if accepted is not True:
            raise BridgeError("multi_refused", "MULTI refused execution request")
        return {"accepted": True}

    def next(self, params):
        self._params(params, ())
        return self._execution("Next")

    def run_commands(self, params):
        values = self._params(params, ("commands",))
        commands = values["commands"]
        for command in commands.replace(u"\r", u"\n").replace(u";", u"\n").split(u"\n"):
            normalized = command.strip().lower()
            if normalized == u"help" or normalized.startswith(u"prepare_target"):
                raise BridgeError("unsafe_command", "command is unsafe in a headless bridge")
        return self._command_result(commands)

    def console_read(self, params):
        self._params(params, ())
        self._require_window()
        paths = self._console_files()
        window = self.window
        for pane in (u"target", u"io"):
            path = paths["server" if pane == u"target" else "io"]
            accepted = window.RunCommands(u'savedebugpane %s "%s"' % (pane, path), 1, 0)
            status = getattr(window, "cmdExecStatus", None)
            # savedebugpane intentionally has no cmdExecOutput on MULTI 7.1.6.
            if not accepted or status != 1:
                raise BridgeError("multi_refused", "MULTI refused console snapshot")
        server, server_state, server_truncated = self._console_increment("server", paths["server"])
        io, io_state, io_truncated = self._console_increment("io", paths["io"])
        self.console_previous["server"] = server_state
        self.console_previous["io"] = io_state
        server_text, server_lossy = multi_text(server)
        io_text, io_lossy = multi_text(io)
        result = {"server": server_text, "io": io_text}
        if server_lossy or io_lossy:
            result["raw_lossy"] = True
        if server_truncated or io_truncated:
            result["truncated"] = True
        return result

    def console_reset(self, params):
        """Forget local console cursors without touching MULTI or the target."""
        # This operation deliberately does not use _params: None is not an
        # object on the wire, so accepting it would weaken the reset contract.
        if not isinstance(params, dict) or params:
            raise BridgeError("invalid_params", "console_reset params must be an empty object")
        self._require_window()
        self.console_previous = {"server": None, "io": None}
        return {"reset": True}

    def memory_read(self, params):
        component, address, byte_count = self._inspection_params(
            params, "byte_count", MAX_MEMORY_READ_BYTES)
        path = self._inspection_file()
        try:
            try:
                os.unlink(path)
            except OSError:
                pass
            result = self._command_result(
                u'route %s memdump -noprogress raw "%s" 0x%x %d' %
                (component, path, address, byte_count))
            try:
                with open(path, "rb") as input_file:
                    data = input_file.read(MAX_MEMORY_READ_BYTES + 1)
            except (IOError, OSError):
                raise BridgeError("memory_unavailable", "memory snapshot cannot be read")
            if len(data) != byte_count:
                raise BridgeError("memory_unavailable", "memory snapshot length is invalid")
            encoded = base64.b64encode(data)
            if not isinstance(encoded, unicode):
                encoded = encoded.decode("ascii")
            return {"data": encoded, "byte_count": byte_count, "status": result["status"]}
        finally:
            try:
                os.unlink(path)
            except OSError:
                pass

    def disassemble(self, params):
        component, address, byte_count = self._inspection_params(
            params, "byte_count", MAX_DISASSEMBLE_BYTES)
        result = self._command_result(
            u"route %s disassemble 0x%x %d" % (component, address, byte_count))
        raw = result["raw"]
        if len(raw.encode("utf-8")) > MAX_DISASSEMBLE_OUTPUT_BYTES:
            raise BridgeError("multi_refused", "disassembly output exceeds size limit")
        return result

    def dispatch(self, method, params):
        if method not in METHODS:
            raise BridgeError("unknown_method", "method is not allowed")
        return getattr(self, method)(params)


class MBPServer(object):
    """Single-client, serial MBP server. It intentionally emits no events in M1."""

    def __init__(self, bridge=None, max_message_size=DEFAULT_MAX_MESSAGE_SIZE):
        if max_message_size <= 0:
            raise ValueError("max_message_size must be positive")
        self.bridge = bridge if bridge is not None else MultiBridge()
        self.max_message_size = max_message_size

    def handshake(self):
        return {
            "protocol_version": PROTOCOL_VERSION,
            "bridge_version": BRIDGE_VERSION,
            "max_message_size": self.max_message_size,
            "encoding": "utf-8",
        }

    def response(self, request_id, result):
        return {"id": request_id, "ok": True, "result": result}

    def error(self, request_id, error):
        # The required raw field is intentionally empty. MULTI diagnostics and
        # unexpected exceptions can contain paths, connection arguments, or
        # unbounded text; none of them cross the MBP trust boundary.
        envelope = {"kind": error.kind, "message": error.message, "raw": u""}
        return {"id": request_id, "ok": False, "error": envelope}

    def process_request(self, request):
        request_id = request.get("id", 0)
        try:
            if set(request) - set(("id", "method", "params")):
                raise BridgeError("protocol_error", "request has unknown fields")
            if "id" not in request or isinstance(request_id, bool) or not isinstance(request_id, (int, long)):
                raise BridgeError("protocol_error", "request id must be an integer")
            if "method" not in request:
                raise BridgeError("protocol_error", "request method is required")
            method = as_unicode(request["method"])
            return self.response(request_id, self.bridge.dispatch(method, request.get("params")))
        except BridgeError as error:
            return self.error(request_id, error)
        except Exception:
            # Do not stringify an unexpected exception: it can embed sensitive
            # arguments, produce unbounded text, or execute hostile __str__ code.
            return self.error(request_id, BridgeError(
                "internal_error", "unexpected bridge exception"))

    def handle_connection(self, connection):
        connection.sendall(encode_frame(self.handshake()))
        reader = BoundedNDJSONReader(connection, self.max_message_size)
        try:
            while True:
                try:
                    line = reader.read()
                    if line is None:
                        return
                    request = decode_frame(line)
                except BridgeError:
                    # A malformed frame has no trustworthy request id. Closing is the only strict
                    # response shape available; inventing an id would violate request/response.
                    return
                except socket.error:
                    # A peer may close immediately after the handshake. This is a
                    # normal transport termination, not bridge output.
                    return
                connection.sendall(encode_frame(self.process_request(request)))
        finally:
            self.bridge.cleanup()

    def _write_ready_file(self, path, host, port):
        """Atomically publish the listener selected by bind(), without stdout."""
        if path is None:
            return
        if not isinstance(path, (unicode, binary_type)):
            raise ValueError("ready file must be a path string")
        payload = json.dumps({"host": host, "port": port}, separators=(",", ":"))
        if isinstance(payload, unicode):
            payload = payload.encode("utf-8")
        temporary = path + ".tmp." + str(os.getpid())
        try:
            with open(temporary, "wb") as output:
                output.write(payload)
                output.flush()
                os.fsync(output.fileno())
            # The Go launcher creates a private, initially absent destination.
            # Windows rename is atomic for this no-replace case.
            os.rename(temporary, path)
        except Exception:
            try:
                os.unlink(temporary)
            except OSError:
                pass
            raise

    def serve(self, port, host=LOOPBACK_HOST, ready_file=None):
        if not isinstance(port, (int, long)) or isinstance(port, bool) or port < 0 or port > 65535:
            raise ValueError("port must be in 0..65535")
        host = validate_rpc_host(host)
        family = socket.AF_INET6 if u":" in host else socket.AF_INET
        listener = socket.socket(family, socket.SOCK_STREAM)
        try:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind((host, port))
            listener.listen(1)
            bound_host, bound_port = listener.getsockname()[:2]
            bound_host = validate_rpc_host(bound_host)
            self._write_ready_file(ready_file, bound_host, bound_port)
            connection, address = listener.accept()
        finally:
            listener.close()
        try:
            try:
                validate_rpc_host(address[0])
            except ValueError:
                return
            self.handle_connection(connection)
        finally:
            connection.close()


def main(argv=None):
    parser = argparse.ArgumentParser()
    parser.add_argument("--rpc-port", type=int, required=True)
    parser.add_argument("--rpc-host", default=LOOPBACK_HOST, type=validate_rpc_host)
    parser.add_argument("--ready-file")
    parser.add_argument("--session-mode", default="cold", choices=("cold", "warm"))
    args = parser.parse_args(argv)
    workspace = os.path.dirname(os.path.abspath(args.ready_file)) if args.ready_file else None
    MBPServer(MultiBridge(session_mode=args.session_mode, console_directory=workspace)).serve(args.rpc_port, args.rpc_host, args.ready_file)


if __name__ == "__main__":
    main()
