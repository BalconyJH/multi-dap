"""Bounded stdio worker for an actor-owned TimeMachine subprocess.

Only an explicit open request loads the native library. EOF, transport failure,
and process shutdown never issue trace control or native disconnect operations.
Native FFI deadlines belong to the parent process, not Python thread timeouts.
"""

import ctypes
import json
import os
import queue
import re
import sys
import threading
import time
from collections import deque

if __package__:
    from .timemachine_native import NativeTimeMachine
else:
    import importlib.util
    _spec = importlib.util.spec_from_file_location(
        "_multi_dap_timemachine_native",
        os.path.join(os.path.dirname(os.path.abspath(__file__)), "timemachine_native.py"))
    _native_module = importlib.util.module_from_spec(_spec)
    _spec.loader.exec_module(_native_module)
    NativeTimeMachine = _native_module.NativeTimeMachine


PROTOCOL = "multi-dap.timemachine-helper.v1"
MAX_REQUEST_BYTES = 16 << 10
MAX_RESPONSE_BYTES = 1 << 20
MAX_PACKET_BATCH = 4096
MAX_UINT64 = (1 << 64) - 1
QUEUE_LIMIT = 8
NOTIFICATION_LIMIT = 128
PUMP_INTERVAL = 0.020
_NOTIFICATIONS = frozenset((
    "trace_start", "trace_stop", "select_packet", "data_clear",
    "new_data_available", "packet_count_regressed", "decoder_replacement",
))


class ProtocolError(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


class FatalError(ProtocolError):
    def __init__(self, code, exit_code=1):
        self.exit_code = exit_code
        super().__init__(code)


def _object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate key")
        result[key] = value
    return result


def _reject_constant(_value):
    raise ValueError("invalid constant")


def decode_request(line, previous_seq):
    try:
        value = json.loads(line.decode("utf-8"), object_pairs_hook=_object,
                           parse_constant=_reject_constant)
    except (ValueError, UnicodeError, RecursionError):
        raise FatalError("invalid_request") from None
    if (type(value) is not dict or set(value) != {"v", "seq", "op", "args"} or
            type(value["v"]) is not int or value["v"] != 1 or
            type(value["seq"]) is not int or not previous_seq < value["seq"] <= (1 << 31) - 1 or
            type(value["op"]) is not str or type(value["args"]) is not dict):
        raise FatalError("invalid_request")
    return value


def _exact(args, fields):
    if set(args) != set(fields):
        raise ProtocolError("invalid_arguments")


def _integer(value, minimum, maximum):
    if type(value) is not int or not minimum <= value <= maximum:
        raise ProtocolError("invalid_arguments")
    return value


def _decimal(value, minimum=0):
    if type(value) is not str or re.fullmatch(r"0|[1-9][0-9]{0,19}", value) is None:
        raise ProtocolError("invalid_arguments")
    number = int(value)
    if not minimum <= number <= MAX_UINT64:
        raise ProtocolError("invalid_arguments")
    return number


def _native_failure(error):
    if isinstance(error, KeyboardInterrupt):
        return FatalError("native_interrupted", 130)
    if isinstance(error, SystemExit):
        return FatalError("native_interrupted", 1)
    return FatalError("native_failure")


class HelperSession:
    """All methods except notification serialization run on the owner thread."""

    def __init__(self, api_factory=NativeTimeMachine, notification_limit=NOTIFICATION_LIMIT):
        if type(notification_limit) is not int or not 1 <= notification_limit <= 4096:
            raise ValueError("invalid notification limit")
        self._factory = api_factory
        self._api = None
        self.opened = False
        self.terminal = False
        self.faulted = False
        self.generation = 1
        self.trace_state = "unknown"
        self.info = b""
        self._info_size = 0
        self.count = 0
        self._last_count = None
        self._notification_limit = notification_limit
        self._events = deque()
        self._dropped = 0
        self._ignored = 0
        self._callback_failed = False
        self._invalidated = False

    def _call(self, name, *args, **kwargs):
        try:
            return getattr(self._api, name)(*args, **kwargs)
        except BaseException as error:
            self.faulted = True
            raise _native_failure(error) from None

    def _advance(self):
        if self.generation == MAX_UINT64:
            self.faulted = True
            raise FatalError("generation_exhausted")
        self.generation += 1
        self._invalidated = True

    def _collect(self, notification, count_regressed, info_changed):
        fields = {"events", "dropped", "ignored_callbacks", "callback_failed", "generation_invalidated"}
        if (type(notification) is not dict or set(notification) != fields or
                type(notification["events"]) is not list or len(notification["events"]) > 4096 or
                any(type(event) is not str or event not in _NOTIFICATIONS for event in notification["events"]) or
                type(notification["dropped"]) is not int or not 0 <= notification["dropped"] <= MAX_UINT64 or
                type(notification["ignored_callbacks"]) is not int or
                not 0 <= notification["ignored_callbacks"] <= MAX_UINT64 or
                type(notification["callback_failed"]) is not bool or
                type(notification["generation_invalidated"]) is not bool):
            self.faulted = True
            raise FatalError("native_failure")
        events = list(notification["events"])
        if count_regressed and "packet_count_regressed" not in events:
            events.append("packet_count_regressed")
        if info_changed and "decoder_replacement" not in events:
            events.append("decoder_replacement")
        invalidate = notification["generation_invalidated"] or count_regressed or info_changed
        for event in events:
            if event == "trace_start":
                self.trace_state = "running"
                invalidate = True
            elif event == "trace_stop":
                self.trace_state = "stopped"
            elif event in ("data_clear", "packet_count_regressed", "decoder_replacement"):
                self.trace_state = "unknown"
                invalidate = True
            if len(self._events) == self._notification_limit:
                self._dropped = min(MAX_UINT64, self._dropped + 1)
                invalidate = True
                self.trace_state = "unknown"
            else:
                self._events.append(event)
        self._dropped = min(MAX_UINT64, self._dropped + notification["dropped"])
        self._ignored = min(MAX_UINT64, self._ignored + notification["ignored_callbacks"])
        self._callback_failed |= notification["callback_failed"]
        if notification["dropped"] or notification["ignored_callbacks"] or notification["callback_failed"]:
            invalidate = True
            self.trace_state = "unknown"
        if invalidate:
            self._advance()

    def refresh(self):
        if not self.opened or self.faulted:
            return
        self._call("poll_events")
        info = self._call("get_info")
        if type(info) is not bytes or len(info) != self._info_size:
            self.faulted = True
            raise FatalError("native_failure")
        count = self._call("packet_count")
        if type(count) is not int or not 0 <= count < MAX_UINT64:
            self.faulted = True
            raise FatalError("native_failure")
        regressed = self._last_count is not None and count < self._last_count
        self._last_count = count
        self.count = count
        changed = bool(self.info) and self.info != info
        self.info = info
        self._collect(self._call("drain_notifications"), regressed, changed)

    def _notifications(self):
        result = {
            "events": list(self._events), "dropped": self._dropped,
            "ignored_callbacks": self._ignored, "callback_failed": self._callback_failed,
            "generation_invalidated": self._invalidated,
        }
        self._events.clear()
        self._dropped = self._ignored = 0
        self._callback_failed = self._invalidated = False
        return result

    def _status(self):
        return {
            "info_hex": self.info.hex(), "packet_count": str(self.count),
            "generation": str(self.generation), "trace_state": self.trace_state,
            "notifications": self._notifications(),
        }

    def dispatch(self, op, args):
        if self.faulted or self.terminal:
            raise FatalError("native_failure")
        if op == "open":
            _exact(args, ("installation", "multi_version", "dll_sha256", "session_id"))
            if (type(args["installation"]) is not str or not os.path.isabs(args["installation"]) or
                    "\x00" in args["installation"] or len(args["installation"]) > 4096 or
                    type(args["multi_version"]) is not str or args["multi_version"] not in ("8.1.4", "7.1.6d") or
                    type(args["dll_sha256"]) is not str or
                    re.fullmatch(r"[0-9a-fA-F]{64}", args["dll_sha256"]) is None):
                raise ProtocolError("invalid_arguments")
            _integer(args["session_id"], 1, (1 << 31) - 1)
            if self.opened:
                raise ProtocolError("already_open")
            self._info_size = 64 if args["multi_version"] == "8.1.4" else 60
            try:
                self._api = self._factory(args["installation"], args["multi_version"], args["dll_sha256"])
            except BaseException as error:
                self.faulted = True
                raise _native_failure(error) from None
            self._call("initialize")
            self._call("connect", args["session_id"])
            self.opened = True
            self.refresh()
            return self._status()
        if op in ("status", "close"):
            _exact(args, ())
        elif op in ("start", "stop"):
            _exact(args, ("authorized",))
            if args["authorized"] is not True:
                raise ProtocolError("invalid_arguments")
        elif op == "read":
            _exact(args, ("start", "count", "generation"))
            start = _decimal(args["start"])
            count = _integer(args["count"], 1, MAX_PACKET_BATCH)
            generation = _decimal(args["generation"], 1)
            if start > MAX_UINT64 - count:
                raise ProtocolError("invalid_arguments")
        else:
            raise ProtocolError("unsupported_operation")
        if not self.opened:
            raise ProtocolError("not_open")
        if op == "close":
            self._call("close")
            self.opened = False
            self.terminal = True
            return {"closed": True}
        self.refresh()
        if op == "status":
            return self._status()
        if op == "read":
            if generation != self.generation:
                raise ProtocolError("stale_generation")
            if start > self.count or count > self.count - start:
                raise ProtocolError("invalid_range")
            before = self.generation
            batch = self._call("get_packets", start, count)
            if (type(batch) is not tuple or len(batch) != 2 or
                    type(batch[0]) is not bytes or type(batch[1]) is not int or
                    not 0 <= batch[1] <= count or len(batch[0]) != batch[1] * 32):
                self.faulted = True
                raise FatalError("native_failure")
            self.refresh()
            if self.generation != before:
                raise ProtocolError("stale_generation")
            data, actual = batch
            return {
                "generation": str(self.generation), "start": str(start), "count": actual,
                "packets_hex": [data[offset:offset + 32].hex() for offset in range(0, len(data), 32)],
            }
        self.trace_state = op + "_requested"
        self._call("start_trace" if op == "start" else "stop_trace", authorized=True)
        self.refresh()
        return {
            "accepted": True, "confirmed": False, "generation": str(self.generation),
            "trace_state": self.trace_state,
        }


class _Transport:
    def __init__(self, source, destination, queue_limit):
        self.source, self.destination = source, destination
        self.incoming = queue.Queue(queue_limit)
        self.outgoing = queue.Queue(queue_limit)
        self.input_done = threading.Event()
        self.writer_done = threading.Event()
        self.stop_writer = threading.Event()
        self.ready_written = threading.Event()
        self.failure = None
        self._lock = threading.Lock()

    def fail(self, code):
        with self._lock:
            if self.failure is None:
                self.failure = code

    def start(self):
        threading.Thread(target=self._reader, daemon=True).start()
        threading.Thread(target=self._writer, daemon=True).start()

    def _reader(self):
        try:
            while True:
                line = self.source.readline(MAX_REQUEST_BYTES + 3)
                if line == b"":
                    return
                if type(line) is not bytes:
                    self.fail("reader_failed")
                    return
                if len(line) > MAX_REQUEST_BYTES + 2:
                    self.fail("request_too_large")
                    return
                if not line.endswith(b"\n"):
                    self.fail("request_truncated")
                    return
                line = line[:-1]
                if line.endswith(b"\r"):
                    line = line[:-1]
                if len(line) > MAX_REQUEST_BYTES:
                    self.fail("request_too_large")
                    return
                try:
                    self.incoming.put_nowait(line)
                except queue.Full:
                    self.fail("input_overflow")
                    return
        except BaseException:
            self.fail("reader_failed")
        finally:
            self.input_done.set()

    def _writer(self):
        try:
            first = True
            while True:
                try:
                    data = self.outgoing.get(timeout=PUMP_INTERVAL)
                except queue.Empty:
                    if self.stop_writer.is_set():
                        return
                    continue
                if data is None:
                    return
                offset = 0
                while offset < len(data):
                    written = self.destination.write(data[offset:])
                    if type(written) is not int or not 0 < written <= len(data) - offset:
                        raise ValueError("invalid write result")
                    offset += written
                self.destination.flush()
                if first:
                    self.ready_written.set()
                    first = False
        except BaseException:
            self.fail("writer_failed")
        finally:
            self.writer_done.set()

    def offer(self, message):
        try:
            data = json.dumps(message, ensure_ascii=True, separators=(",", ":"),
                              allow_nan=False).encode("ascii") + b"\n"
        except (ValueError, TypeError, RecursionError):
            self.fail("response_too_large")
            return False
        if len(data) > MAX_RESPONSE_BYTES:
            self.fail("response_too_large")
            return False
        try:
            self.outgoing.put_nowait(data)
            return True
        except queue.Full:
            self.fail("output_overflow")
            return False


def serve(source, destination, api_factory=NativeTimeMachine, *,
          clock=time.monotonic, queue_limit=QUEUE_LIMIT, pump_interval=PUMP_INTERVAL,
          startup_timeout=1.0, shutdown_timeout=0.25):
    """Run on the owning thread; return an exit code without implicit cleanup."""
    if (type(queue_limit) is not int or not 1 <= queue_limit <= 64 or
            not 0 < pump_interval <= PUMP_INTERVAL or not 0 < startup_timeout <= 10 or
            not 0 <= shutdown_timeout <= 1):
        raise ValueError("invalid helper limits")
    transport = _Transport(source, destination, queue_limit)
    session = HelperSession(api_factory)
    transport.start()
    transport.offer({
        "v": 1, "event": "ready", "protocol": PROTOCOL,
        "python_major": sys.version_info[0], "python_minor": sys.version_info[1],
        "pointer_bits": ctypes.sizeof(ctypes.c_void_p) * 8,
    })
    code, fatal = 0, None
    last_seq = 0
    try:
        startup_deadline = clock() + startup_timeout
        while not transport.ready_written.wait(pump_interval):
            if transport.failure is not None:
                raise FatalError(transport.failure)
            if clock() >= startup_deadline:
                raise FatalError("writer_timeout")
        next_pump = clock() + pump_interval
        while not session.terminal:
            if transport.failure is not None:
                raise FatalError(transport.failure)
            if clock() >= next_pump:
                session.refresh()
                next_pump = clock() + pump_interval
            try:
                line = transport.incoming.get(timeout=max(0, min(pump_interval, next_pump - clock())))
            except queue.Empty:
                if transport.input_done.is_set():
                    break
                continue
            request = decode_request(line, last_seq)
            last_seq = request["seq"]
            response = {"v": 1, "seq": last_seq}
            try:
                response["result"] = session.dispatch(request["op"], request["args"])
                response["ok"] = True
            except FatalError as error:
                response.update(ok=False, error={"code": error.code})
                transport.offer(response)
                code = error.exit_code
                break
            except ProtocolError as error:
                response.update(ok=False, error={"code": error.code})
            if not transport.offer(response):
                raise FatalError(transport.failure)
    except FatalError as error:
        fatal, code = error.code, error.exit_code
    except KeyboardInterrupt:
        fatal, code = "native_interrupted", 130
    except BaseException:
        fatal, code = "native_failure", 1
    if fatal is not None:
        transport.offer({"v": 1, "event": "fatal", "error": {"code": fatal}})

    # Output is best-effort and bounded. Keep a healthy connection's event
    # pump moving while its writer drains; never call native cleanup here.
    deadline = clock() + shutdown_timeout
    sentinel_sent = False
    while not transport.writer_done.is_set() and clock() < deadline:
        if not sentinel_sent:
            try:
                transport.outgoing.put_nowait(None)
                sentinel_sent = True
            except queue.Full:
                pass
        try:
            session.refresh()
        except FatalError as error:
            session.faulted = True
            code = code or error.exit_code
        except KeyboardInterrupt:
            session.faulted = True
            code = 130
        except BaseException:
            session.faulted = True
            code = code or 1
        transport.writer_done.wait(pump_interval)
    if not transport.writer_done.is_set() or transport.failure is not None:
        code = code or 1
    transport.stop_writer.set()
    return code


def main():
    return serve(sys.stdin.buffer, sys.stdout.buffer)


if __name__ == "__main__":
    # A daemon writer blocked in a buffered pipe must not make interpreter
    # finalization hang. Successful replies have already had a bounded drain.
    try:
        _exit_code = main()
    except KeyboardInterrupt:
        _exit_code = 130
    except BaseException:
        _exit_code = 1
    os._exit(_exit_code)
