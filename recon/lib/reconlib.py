# Python 2.7 - runs inside the MULTI embedded interpreter under mpythonrun.
# Support library for M0 reconnaissance probes.
#
# Contract: a probe constructs one Recorder, drives it, and calls finish().
# finish() is also called from an except/finally path, so a crashed probe still
# leaves evidence on disk. Probes never write to stdout: mpythonrun's stdout is a
# real console handle and cannot be redirected.

import os
import sys
import json
import ntpath
import time
import inspect
import re

RECON_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT_DIR = os.path.join(RECON_DIR, "out")
M2_BREAKPOINT_RECOVERY_JOURNAL = os.path.join(
    OUT_DIR, "p09_m2_program_breakpoint_recovery.json")
_MULTI_DAP_HINT_TOKEN = re.compile(
    r'^mprintf\("HIT 0x[0-9A-F]{8}\\n"\)$')

try:
    unicode
except NameError:
    # Keep the support module importable by host-side Python 3 tests.  MULTI
    # itself supplies these Python 2 names.
    unicode = str
    long = int
    _PYTHON3 = True
else:
    _PYTHON3 = False

try:
    basestring
except NameError:
    basestring = str


class RedactedEvidence(object):
    """An in-memory value with an explicit safe representation for evidence."""

    def evidence_summary(self):
        raise NotImplementedError


class LocalConfig(dict, RedactedEvidence):
    """Board-local configuration that remains usable but is never evidence."""
    def evidence_summary(self):
        # Deliberately fixed: even configuration key names and optional section
        # presence can reveal consuming-project details.
        return {"kind": "local_configuration", "redacted": True}


def _valid_m2_breakpoint_recovery_journal(core, token):
    return (isinstance(core, (int, long)) and not isinstance(core, bool) and
            core >= 0 and isinstance(token, basestring) and
            _MULTI_DAP_HINT_TOKEN.match(token) is not None)


def write_m2_breakpoint_recovery_journal(core, token):
    """Atomically persist one local-only token before issuing its `b` command."""
    if not _valid_m2_breakpoint_recovery_journal(core, token):
        raise ValueError("invalid M2 breakpoint recovery journal entry")
    if os.path.exists(M2_BREAKPOINT_RECOVERY_JOURNAL):
        raise RuntimeError("an M2 breakpoint recovery journal already exists")
    if not os.path.isdir(OUT_DIR):
        os.makedirs(OUT_DIR)
    temporary = "%s.tmp.%d" % (M2_BREAKPOINT_RECOVERY_JOURNAL, os.getpid())
    try:
        with open(temporary, "wb") as fh:
            fh.write(json.dumps({"version": 1, "core": int(core),
                                 "token": token}, sort_keys=True).encode("utf-8"))
            fh.flush()
        # The destination was checked above and this process must not issue b
        # until this rename succeeds. A stale journal therefore cannot be
        # silently replaced by a later invocation.
        os.rename(temporary, M2_BREAKPOINT_RECOVERY_JOURNAL)
    except Exception:
        try:
            if os.path.exists(temporary):
                os.remove(temporary)
        except Exception:
            pass
        raise


def read_m2_breakpoint_recovery_journal():
    """Load a locally persisted recovery intent without making it evidence."""
    if not os.path.exists(M2_BREAKPOINT_RECOVERY_JOURNAL):
        return None
    try:
        with open(M2_BREAKPOINT_RECOVERY_JOURNAL, "rb") as fh:
            value = json.loads(fh.read().decode("utf-8"))
    except Exception:
        raise RuntimeError("M2 breakpoint recovery journal is unreadable")
    if (not isinstance(value, dict) or value.get("version") != 1 or
            not _valid_m2_breakpoint_recovery_journal(
                value.get("core"), value.get("token"))):
        raise RuntimeError("M2 breakpoint recovery journal is invalid")
    return {"core": int(value["core"]), "token": value["token"]}


def clear_m2_breakpoint_recovery_journal(core, token):
    """Remove only the exact local intent whose absence was just proven."""
    journal = read_m2_breakpoint_recovery_journal()
    if journal is None:
        return False
    if journal["core"] != core or journal["token"] != token:
        raise RuntimeError("M2 breakpoint recovery journal does not match cleanup")
    os.remove(M2_BREAKPOINT_RECOVERY_JOURNAL)
    return True


def _safe(value, depth=0):
    """Convert a value to JSON-safe, evidence-safe data.

    ``RedactedEvidence`` values retain their operational value in memory,
    while every Recorder entry receives only their summary. Check this before
    container handling so protected dict subclasses remain protected even
    when nested inside a larger result.
    """
    if isinstance(value, RedactedEvidence):
        return _safe(value.evidence_summary())
    if depth > 6:
        return {"type": type(value).__name__, "truncated": True}
    if value is None or isinstance(value, (bool, int, long, float)):
        return value
    if isinstance(value, str):
        if _PYTHON3:
            return value
        return value.decode("utf-8", "replace")
    if isinstance(value, unicode):
        return value
    if isinstance(value, (list, tuple)):
        return [_safe(v, depth + 1) for v in value]
    if isinstance(value, dict):
        return dict((unicode(k), _safe(v, depth + 1)) for k, v in value.items())
    return repr(value)[:500]


def _exception_kind(error):
    """Return a fixed diagnostic category without retaining exception text."""
    if isinstance(error, (IOError, OSError)):
        return "io_error"
    if isinstance(error, (KeyError, IndexError)):
        return "lookup_error"
    if isinstance(error, TypeError):
        return "type_error"
    if isinstance(error, ValueError):
        return "value_error"
    if isinstance(error, RuntimeError):
        return "runtime_error"
    return "exception"


def load_config():
    path = os.path.join(RECON_DIR, "config.local.json")
    f = open(path, "rb")
    try:
        return LocalConfig(json.loads(f.read().decode("utf-8")))
    finally:
        f.close()


def _new_debugger():
    """Construct the GHS_Debugger injected into the probe's main module."""
    import __main__
    factory = getattr(__main__, "GHS_Debugger", None)
    if factory is None:
        try:
            import __builtin__
            factory = getattr(__builtin__, "GHS_Debugger", None)
        except Exception:
            factory = None
    if factory is None:
        raise RuntimeError("MULTI did not inject GHS_Debugger")
    return factory()


class Recorder(object):
    def __init__(self, name):
        self.name = name
        self.started = time.time()
        self.data = {
            "probe": name,
            "started_epoch": self.started,
            "python_version": sys.version,
            "steps": [],
            "notes": {},
            "fatal": None,
        }

    def note(self, key, value):
        self.data["notes"][key] = _safe(value)
        self._write()

    def _write(self):
        if not os.path.isdir(OUT_DIR):
            os.makedirs(OUT_DIR)
        path = os.path.join(OUT_DIR, self.name + ".json")
        tmp = path + ".tmp"
        # Keep the disk boundary authoritative even if a future probe mutates
        # Recorder.data directly instead of using step() or note().
        evidence = _safe(self.data)
        f = open(tmp, "wb")
        try:
            f.write(json.dumps(evidence, indent=2, sort_keys=True,
                               default=repr).encode("utf-8"))
        finally:
            f.close()
        if os.path.exists(path):
            os.remove(path)
        os.rename(tmp, path)
        return path

    def step(self, label, fn, **meta):
        """Time fn(), record its return value or its exception, and return the value.

        Never raises: a failed step is evidence, not a crash.
        """
        entry = {"label": label, "meta": _safe(meta)}
        self.data["steps"].append(entry)
        self._write()
        t0 = time.time()
        try:
            value = fn()
            entry["ok"] = True
            entry["result"] = _safe(value)
        except Exception as error:
            value = None
            entry["ok"] = False
            entry["error_kind"] = _exception_kind(error)
        entry["seconds"] = time.time() - t0
        self._write()
        return value

    def fatal(self):
        self.data["fatal"] = _exception_kind(sys.exc_info()[1])
        self._write()

    def finish(self):
        self.data["total_seconds"] = time.time() - self.started
        return self._write()


class Session(RedactedEvidence):
    """Own the debugger lifetime while exposing the program window API.

    Hardware reconnaissance established that DebugProgram's return value is
    the command target. GHS_Debugger itself is rebound to the connection
    window after ConnectToTarget and then has no active child process.
    """
    def __init__(self, debugger, window, disconnect_on_close, origin):
        self.debugger = debugger
        self.window = window
        self.disconnect_on_close = disconnect_on_close
        self.origin = origin

    def __getattr__(self, name):
        return getattr(self.window, name)

    def evidence_summary(self):
        return {"kind": "session", "origin": self.origin}


class WarmBindError(RuntimeError):
    """No uniquely verified existing program window is available."""


def _normalized_program_name(value):
    """Return a comparison-only program name; never put it in evidence."""
    if value is None:
        return ""
    if isinstance(value, unicode):
        value = value.encode("utf-8") if not _PYTHON3 else value
    value = str(value).strip().strip('"')
    if not value:
        return ""
    # MULTI runs on Windows even when host-side tests do not. Use ntpath so
    # case folding and separator normalization are deterministic everywhere.
    return ntpath.normcase(ntpath.normpath(value))


def open_cold_session(cfg):
    """Create and connect a new program window.

    This is the only API that invokes DebugProgram or ConnectToTarget.  It is
    for an operator-approved cold session, never for reuse of an existing
    router-owned debugger.
    """
    dbg = _new_debugger()
    win = dbg.DebugProgram(cfg["multicore_project"], 0, 1, 0, 1)
    if win is None:
        raise RuntimeError("DebugProgram did not return a program window")
    connection = dbg.ConnectToTarget(
        cfg["connection_args"], "", "", "", 1, "", 0)
    if connection is None or connection is False:
        raise RuntimeError("ConnectToTarget did not establish a connection")
    return Session(dbg, win, True, "cold")


def _existing_debugger_windows():
    """Return the registered Debugger windows in the current service router."""
    try:
        import ghs_constants
        import ghs_winreg
    except ImportError:
        raise WarmBindError("MULTI window-register modules are unavailable")

    registry = ghs_winreg.GHS_WindowRegister()
    if not getattr(registry, "service", None):
        raise WarmBindError("MULTI window register is unavailable")
    window_list = registry.GetWindowList(False)
    if not isinstance(window_list, dict):
        raise WarmBindError("MULTI window register returned an invalid list")
    debugger_class = getattr(ghs_constants.winClassNames, "debugger", "")
    if not debugger_class:
        raise WarmBindError("MULTI debugger window class is unavailable")
    return registry.CheckWindows("", winClass=debugger_class,
                                 fromWinList=window_list)


def _inspect_existing_program_windows(cfg, expected_program=None):
    """Inspect warm candidates and return only non-sensitive aggregate counts."""
    if expected_program is None:
        expected_program = cfg.get("primary_elf", "")
    primary_elf = _normalized_program_name(expected_program)
    multicore_project = _normalized_program_name(
        cfg.get("multicore_project", ""))
    if not primary_elf:
        raise WarmBindError("primary_elf is required for warm binding")

    windows = _existing_debugger_windows()
    summary = {
        "registered_debugger_windows": len(windows),
        "get_program_readable": 0,
        "exact_primary_elf_matches": 0,
        "exact_multicore_project_matches": 0,
        "stable_status_matches": 0,
        "nonempty_process_info_matches": 0,
    }
    verified = []
    for window in windows:
        try:
            program = _normalized_program_name(window.GetProgram())
        except Exception:
            continue
        if not program:
            continue
        summary["get_program_readable"] += 1
        # This is diagnostic-only. DebugProgram opens multicore_project, while
        # the documented GetProgram value is a program name. Keep both exact
        # full-path counts so hardware evidence, rather than an assumption
        # about .ghsmc expansion, decides the warm binding identity.
        if multicore_project and program == multicore_project:
            summary["exact_multicore_project_matches"] += 1
        if program != primary_elf:
            continue
        summary["exact_primary_elf_matches"] += 1

        try:
            status = window.GetStatus()
        except Exception:
            continue
        # Bind only a stable process state.  Dying, forking, zombie, and
        # internal transition states must be sampled again later instead of
        # being treated as a usable warm session.
        if status not in (2, 3):
            continue
        summary["stable_status_matches"] += 1

        try:
            info = window.GetCurPrInfo("")
        except Exception:
            continue
        if not isinstance(info, dict) or not info:
            continue
        summary["nonempty_process_info_matches"] += 1
        verified.append(window)
    return summary, verified


class CoreWindowInventory(RedactedEvidence):
    """Retain exact-ELF window candidates while recording only aggregate shape."""
    def __init__(self, configured_core_ordinal, summary, verified_windows):
        self.configured_core_ordinal = configured_core_ordinal
        self.summary = summary
        self.verified_windows = verified_windows

    def evidence_summary(self):
        return {
            "configured_core_ordinal": self.configured_core_ordinal,
            "registered_debugger_windows": self.summary[
                "registered_debugger_windows"],
            "get_program_readable": self.summary["get_program_readable"],
            "exact_get_program_matches": self.summary[
                "exact_primary_elf_matches"],
            "stable_status_matches": self.summary["stable_status_matches"],
            "nonempty_process_info_matches": self.summary[
                "nonempty_process_info_matches"],
            "has_unique_verified_window": len(self.verified_windows) == 1,
        }


def inspect_configured_core_window(cfg, core, configured_core_ordinal):
    """Inspect one configured ELF without requiring its window to exist."""
    if not isinstance(core, dict):
        raise ValueError("core configuration must be an object")
    elf = core.get("elf")
    if not isinstance(elf, basestring) or not _normalized_program_name(elf):
        raise ValueError("configuration has invalid core ELF")
    if (isinstance(configured_core_ordinal, bool) or
            not isinstance(configured_core_ordinal, (int, long)) or
            configured_core_ordinal < 0):
        raise ValueError("configured core ordinal must be non-negative")
    summary, verified = _inspect_existing_program_windows(cfg, elf)
    return CoreWindowInventory(configured_core_ordinal, summary, verified)


def warm_binding_summary(cfg):
    """Return aggregate warm-binding diagnostics without program identity."""
    summary, unused_verified = _inspect_existing_program_windows(cfg)
    return summary


def _bind_existing_program_window(cfg, expected_program):
    """Bind one exact, already-open program window without connecting."""
    summary, verified = _inspect_existing_program_windows(cfg, expected_program)
    if not verified:
        if not summary["registered_debugger_windows"]:
            raise WarmBindError("no existing Debugger windows were registered")
        raise WarmBindError("no active program window exactly matched expected program")
    if len(verified) != 1:
        raise WarmBindError("multiple matching program windows are active")
    # Constructing a debugger service is not a target connection.  Keep it for
    # the Session ownership model; warm close remains a no-op.
    dbg = _new_debugger()
    return Session(dbg, verified[0], False, "warm")


def bind_existing_program_window(cfg):
    """Return the one existing primary program window in this service router.

    This function is deliberately read-only.  It enumerates registered windows
    and verifies a candidate with GetProgram, GetStatus, and GetCurPrInfo.  It
    never calls DebugProgram, ConnectToTarget, or Disconnect.  The configured
    primary ELF must match exactly after Windows path normalization. The
    multicore-project comparison is diagnostic only until hardware evidence
    establishes what GetProgram returns for a .ghsmc startup. A basename match
    or a first-window fallback would bind the wrong core silently.
    """
    return _bind_existing_program_window(cfg, cfg.get("primary_elf", ""))


def bind_existing_core_program_window(cfg, core):
    """Bind one configured core through its exact ELF identity.

    A source-presence result is per core, so a primary-window fallback would
    make a successful probe ambiguous.  This helper deliberately accepts no
    core selector command and never records the configured ELF.
    """
    if not isinstance(core, dict):
        raise WarmBindError("core configuration must be an object")
    elf = core.get("elf")
    if not isinstance(elf, basestring) or not _normalized_program_name(elf):
        raise WarmBindError("core configuration requires a non-empty ELF")
    return _bind_existing_program_window(cfg, elf)


def close_session(session):
    if not session.disconnect_on_close:
        return
    try:
        session.debugger.Disconnect(0)
    except Exception:
        pass


def callable_inventory(target, terms):
    """Describe matching public callables without invoking any of them.

    This is intentionally an API-shape inventory, not a command discovery
    mechanism.  In particular, probes must not turn a missing API method into
    a guessed RunCommands string.
    """
    normalized_terms = tuple(term.lower() for term in terms)
    inventory = {}
    for name in sorted(dir(target)):
        if name.startswith("_"):
            continue
        if not any(term in name.lower() for term in normalized_terms):
            continue
        try:
            value = getattr(target, name)
        except Exception as exc:
            inventory[name] = {"readable": False,
                               "error_type": type(exc).__name__}
            continue
        if not callable(value):
            continue
        entry = {"readable": True}
        try:
            if hasattr(inspect, "getargspec"):
                argspec = inspect.getargspec(value)
                entry["args"] = list(argspec.args)
                entry["varargs"] = bool(argspec.varargs)
                entry["keywords"] = bool(argspec.keywords)
                entry["defaults_count"] = len(argspec.defaults or ())
            else:
                argspec = inspect.getfullargspec(value)
                entry["args"] = list(argspec.args)
                entry["varargs"] = bool(argspec.varargs)
                entry["keywords"] = bool(argspec.varkw)
                entry["defaults_count"] = len(argspec.defaults or ())
        except Exception as exc:
            entry["signature_error_type"] = type(exc).__name__
        inventory[name] = entry
    return inventory


def execution_window_api_inventory(window):
    """Describe the exact public run-control surface without invoking it."""
    names = ("GetStatus", "GetProgram", "Halt", "Resume", "Next", "Step")
    inventory = {}
    for name in names:
        try:
            value = getattr(window, name)
        except Exception as exc:
            inventory[name] = {"readable": False,
                               "error_type": type(exc).__name__}
            continue
        if not callable(value):
            inventory[name] = {"readable": True, "callable": False}
            continue
        entry = {"readable": True, "callable": True}
        try:
            if hasattr(inspect, "getargspec"):
                argspec = inspect.getargspec(value)
                entry["args"] = list(argspec.args)
                entry["varargs"] = bool(argspec.varargs)
                entry["keywords"] = bool(argspec.keywords)
                entry["defaults_count"] = len(argspec.defaults or ())
            else:
                argspec = inspect.getfullargspec(value)
                entry["args"] = list(argspec.args)
                entry["varargs"] = bool(argspec.varargs)
                entry["keywords"] = bool(argspec.varkw)
                entry["defaults_count"] = len(argspec.defaults or ())
        except Exception as exc:
            entry["signature_error_type"] = type(exc).__name__
        inventory[name] = entry
    return inventory


def stopped_status_value(session):
    """Read MULTI's discovered stopped constant; never infer one by number."""
    value = getattr(session, "status_stopped", None)
    if value is None:
        raise RuntimeError("MULTI object did not expose status_stopped")
    return value


def safe_state_snapshot(session):
    """Return state evidence without target names, paths, addresses, or values."""
    status = session.GetStatus()
    info = session.GetCurPrInfo("")
    return {
        "status": status,
        "process_info_type": type(info).__name__,
        "process_info_present": bool(info),
        "stop_stamp_present": bool(isinstance(info, dict) and
                                    info.get("stopStamp") is not None),
    }


def require_stopped_session(session):
    """Fail closed before a probe performs an operation against a target."""
    snapshot = safe_state_snapshot(session)
    if snapshot["status"] != stopped_status_value(session):
        raise RuntimeError("probe requires a verified stopped target")
    if not snapshot["process_info_present"]:
        raise RuntimeError("probe requires non-empty process information")
    return snapshot


def recover_stopped_session(session):
    """Halt and verify after an execution probe has changed target state."""
    before = session.GetStatus()
    stopped = stopped_status_value(session)
    if before != stopped:
        session.Halt(1, 0)
    after = session.GetStatus()
    if after != stopped:
        raise RuntimeError("recovery halt did not leave target stopped")
    return {"status_before_recovery": before, "status_after_recovery": after}


def configured_api_call(target, spec):
    """Invoke one operator-approved, already-discovered API call.

    The returned evidence deliberately describes the result's shape instead of
    serializing a memory value, source location, address, or other target data.
    """
    if not isinstance(spec, dict):
        raise ValueError("API call specification must be an object")
    name = spec.get("method")
    args = spec.get("args")
    if not isinstance(name, basestring) or not name:
        raise ValueError("API call requires a non-empty method name")
    if not isinstance(args, list):
        raise ValueError("API call args must be a list")
    method = getattr(target, name, None)
    if not callable(method):
        raise RuntimeError("configured API method is not available")
    value = method(*args)
    return {
        "method": name,
        "argument_count": len(args),
        "result_type": type(value).__name__,
        "result_is_none": value is None,
    }


def _opaque_result_status(value):
    """Classify an API result without serializing target-owned content."""
    if value is None:
        return "none"
    if isinstance(value, bool):
        return "boolean_true" if value else "boolean_false"
    if isinstance(value, (int, long, float)):
        return "numeric_nonzero" if value else "numeric_zero"
    if isinstance(value, basestring):
        return "string_nonempty" if value else "string_empty"
    if isinstance(value, dict):
        return "mapping_nonempty" if value else "mapping_empty"
    if isinstance(value, (list, tuple)):
        return "sequence_nonempty" if value else "sequence_empty"
    return "returned"


def configured_unary_api_call(target, method_name, operand):
    """Call a documented unary lookup API and retain only result structure.

    ``operand`` is deliberately absent from the return value: source paths,
    symbol names, and expressions remain in ignored local configuration.  A
    raised exception is intentionally left to the caller, which must redact
    its message before recording evidence.
    """
    if not isinstance(method_name, basestring) or not method_name:
        raise ValueError("API call requires a non-empty method name")
    method = getattr(target, method_name, None)
    if not callable(method):
        raise RuntimeError("configured API method is not available")
    value = method(operand)
    return {
        "method": method_name,
        "argument_count": 1,
        "accepted": True,
        "status": _opaque_result_status(value),
        "result": {
            "type": type(value).__name__,
            "is_none": value is None,
        },
    }


def run_command(session, command, block=1):
    """Run one MULTI command and preserve its boolean, status, and raw text."""
    accepted = session.RunCommands(command, block, 0, 1)
    return {
        "command": command,
        "accepted": accepted,
        "status": getattr(session.window, "cmdExecStatus", None),
        "raw": getattr(session.window, "cmdExecOutput", None),
    }


_ROUTE_PID = re.compile(r"^debugger\.pid\.([1-9][0-9]*)$")
_COMPONENT_ROW = re.compile(r"^\s*([^\s]+)(?: \((.*)\))?\s*$")
_PROCESS_ROW = re.compile(
    r"^\s*(>>)?\s*(\d+)\s+(0[xX][0-9a-fA-F]+)\s+(\S+)\s+"
    r"(.+?)\s+([01]+)\s*(.*?)\s*$")
_PROCESS_STATUSES = frozenset((
    "nil", "no process", "stopped", "running", "dying", "forking",
    "executing", "continuing", "zombie",
))
_HALT_CAUSES = {
    "Halted by user request.": "user_request",
    "Halted for breakpoint.": "breakpoint",
    "Process not running.": "not_running",
}
_PID_TEXT = re.compile(r"^(?:0[xX][0-9a-fA-F]+|[0-9]+)$")
_STACK_LINE = re.compile(
    r"^\s*[0-9]+[_ ]\s+.+?\t\[.*:-?[0-9]+,-?[0-9]+\]\s*$")
_STACK_FRAME = re.compile(
    r"^\s*([0-9]+)([_ ])\s+.+?\t\[(.*):(-?[0-9]+),(-?[0-9]+)\]\s*$")
_BREAKPOINT_LINE = re.compile(
    r"^\s*[0-9]+\s+.+:\s*0[xX][0-9a-fA-F]+\s+AT count:\s*[0-9]+.*$")
_BREAKPOINT_RECORD = re.compile(
    r"^\s*([0-9]+)\s+(.+):\s*(0[xX][0-9a-fA-F]+)\s+AT count:\s*([0-9]+)(.*)$")
_BREAKPOINT_LOCATION = re.compile(r"^(.+)#([1-9][0-9]*)$")
_BREAKPOINT_COMMAND = re.compile(r"<\{(.*)\}>")
_SOURCE_LOCATOR = re.compile(r"^[A-Za-z0-9_./:\\-]+$")
_PATH_PAYLOAD = re.compile(r"(?:[A-Za-z]:[\\/]|[\\/][^\\/\s]+[\\/])")
_ADDRESS_PAYLOAD = re.compile(r"0[xX][0-9a-fA-F]+")
_IDENTIFIER_PAYLOAD = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
_CALLS_FRAME_PREFIX = re.compile(r"^\s*([0-9]+)([_ ])\s+(.+?)\s*$")
_CALLS_ADDRESS_ONLY = re.compile(
    r"^\s*[0-9]+[_ ]\s+(0[xX][0-9a-fA-F]+)\s*$")
_CALLS_SYMBOL_ADDRESS = re.compile(
    r"^\s*[0-9]+[_ ]\s+.+?\s+(?:at\s+)?0[xX][0-9a-fA-F]+\s*$",
    re.IGNORECASE)
_CALLS_FUNCTION_COMMA_ADDRESS = re.compile(
    r"^\s*[0-9]+[_ ]\s+.+?\([^()\r\n]*\)\s*,\s*"
    r"0[xX][0-9a-fA-F]+\s*$")
_CALLS_FUNCTION_AT_ADDRESS = re.compile(
    r"^\s*[0-9]+[_ ]\s+.+?\([^()\r\n]*\)\s+at\s+"
    r"0[xX][0-9a-fA-F]+\s*$", re.IGNORECASE)
_CALLS_FUNCTION_ADDRESS = re.compile(
    r"^\s*[0-9]+[_ ]\s+.+?\([^()\r\n]*\)\s+"
    r"0[xX][0-9a-fA-F]+\s*$")
_CALLS_FIXTURE_HEX = re.compile(r"0[xX][0-9a-fA-F]+")
_CALLS_FIXTURE_DECIMAL = re.compile(r"[0-9]+")
_CALLS_FIXTURE_IDENTIFIER = re.compile(r"[A-Za-z_][A-Za-z0-9_]*")
_SOURCE_FILE_DECIMAL_PREFIX = re.compile(r"^\s*[0-9]+([.:)])(?=\s|$)")
_SOURCE_FILE_BRACKET_PREFIX = re.compile(r"^\s*\[[0-9]+\](?:\s+|$)")
_SOURCE_FILE_EXTENSION = re.compile(
    r"\.[A-Za-z0-9_+\-]+(?=$|[\s,;:\]\)\"'])")
_SOURCE_FILE_BULLET_HEADER = re.compile(r"^\s*[-*+](?:\s+\S+){3}\s*$")
_SOURCE_FILE_INDEXED_WINDOWS_PATH = re.compile(
    r"^\s*([0-9]+):\s+([A-Za-z]:\\[A-Za-z0-9_./\\-]+)\s*$")
# This is the production Go parser's lexical row contract.  It is kept
# separate from the broader grammar-capture expression above: recon records
# only fixed-category deltas and must never retain a target path merely to
# explain why production rejected it.
_SOURCE_FILE_GO_ROW = re.compile(
    r"^((?: {4}[0-9]| {3}[0-9]{2}| {2}[0-9]{3})): "
    r"([A-Za-z]:\\[A-Za-z0-9_.\\-]+\.[A-Za-z0-9_+\-]+)$")
_SOURCE_FILE_BROAD_DRIVE_ROW = re.compile(
    r"^((?: {4}[0-9]| {3}[0-9]{2}| {2}[0-9]{3})): "
    r"([A-Za-z]:\\.*)$")
_SOURCE_FILE_BROAD_INDEX_PREFIX = re.compile(r"^([ \t]*)([0-9]+)(.*)$")
_SOURCE_FILE_LEADING_WHITESPACE = re.compile(r"^[ \t]*")
# `strings.exe` on the locally installed MULTI 7.1.6d binary contains this
# l-f listing title verbatim (next to the rejected singular variant). The
# protocol grammar is intentionally byte-for-byte after splitlines: no
# trimming, field normalization, or variable-width decoration is accepted.
_SOURCE_FILE_LISTING_HEADER = "--------  File names  --------"
_SOURCE_FILE_MARKER_KEYWORDS = (
    "source", "file", "current", "selected", "name", "debug", "info",
    "not", "loaded", "used", "active", "asterisk", "indicates", "path", "full",
)
_KNOWN_INSPECTION_PHRASES = {
    "Process not running.": "process_not_running",
    "No software breakpoints set.": "no_software_breakpoints",
}


def component_route_inventory(components_output):
    """Return component aliases and aggregate shape without serializing names.

    The route suffix is a MULTI process PID, not a core ordinal. Raw component
    names and program paths stay in memory only.
    """
    if not isinstance(components_output, basestring):
        raise ValueError("component listing must be text")
    route_pids = []
    route_components = []
    program_aliases = []
    row_count = 0
    for line in components_output.splitlines():
        line = line.strip()
        if not line or line == "The currently registered components are:":
            continue
        match = _COMPONENT_ROW.match(line)
        if match is None:
            raise ValueError("component listing has malformed row")
        row_count += 1
        component_id = match.group(1)
        descriptor = match.group(2) or ""
        if not descriptor:
            continue
        match = _ROUTE_PID.match(descriptor)
        if match is not None:
            route_pids.append(int(match.group(1)))
            route_components.append(component_id)
        elif descriptor.startswith("debugger.pid."):
            raise ValueError("component listing has invalid route PID")
        elif descriptor.startswith("debugger.name."):
            program_name = _normalized_program_name(
                descriptor[len("debugger.name."):])
            if not program_name:
                raise ValueError("component listing has invalid program alias")
            program_aliases.append({
                "component_id": component_id,
                "program_name": program_name,
            })
    if not route_pids or len(set(route_pids)) != len(route_pids):
        raise ValueError("component listing has invalid route PIDs")
    route_component_ids = set(route_components)
    program_component_ids = set(alias["component_id"]
                                for alias in program_aliases)
    return {
        "route_pids": sorted(route_pids),
        "component_row_count": row_count,
        "route_count": len(route_pids),
        "program_component_count": len(program_component_ids),
        "program_aliases": program_aliases,
        "program_alias_route_shared_component_count": len(
            route_component_ids.intersection(program_component_ids)),
    }


def configured_core_ids(cfg):
    """Read unique configured core slots without exposing them in evidence."""
    cores = cfg.get("cores") if isinstance(cfg, dict) else None
    if not isinstance(cores, list) or not cores:
        raise ValueError("configuration has no cores")
    ids = []
    for core in cores:
        value = core.get("id") if isinstance(core, dict) else None
        if isinstance(value, bool) or not isinstance(value, (int, long)) or value < 0:
            raise ValueError("configuration has invalid core ID")
        ids.append(value)
    if len(set(ids)) != len(ids):
        raise ValueError("configuration has duplicate core IDs")
    return ids


def configured_core_programs(cfg):
    """Return distinct normalized configured ELF paths for in-memory joining."""
    cores = cfg.get("cores") if isinstance(cfg, dict) else None
    if not isinstance(cores, list) or not cores:
        raise ValueError("configuration has no cores")
    programs = []
    for core in cores:
        value = core.get("elf") if isinstance(core, dict) else None
        if not isinstance(value, basestring):
            raise ValueError("configuration has invalid core ELF")
        program_name = _normalized_program_name(value)
        if not program_name:
            raise ValueError("configuration has invalid core ELF")
        programs.append(program_name)
    if len(set(programs)) != len(programs):
        raise ValueError("configuration has duplicate core ELF")
    return programs


def match_configured_program_components(inventory, configured_programs):
    """Join configured ELF identities to unique program components in memory.

    The returned component IDs are opaque routing inputs.  Callers must not put
    them in evidence; the accompanying summary contains only safe counts.
    """
    if not isinstance(inventory, dict):
        raise ValueError("component inventory is required")
    aliases = inventory.get("program_aliases")
    if not isinstance(aliases, list):
        raise ValueError("component inventory has invalid program aliases")
    if not isinstance(configured_programs, list) or not configured_programs:
        raise ValueError("configured core programs are required")

    matches = []
    missing_count = 0
    duplicate_count = 0
    for ordinal, program_name in enumerate(configured_programs):
        components = set(alias.get("component_id") for alias in aliases
                         if alias.get("program_name") == program_name)
        components.discard(None)
        if len(components) == 1:
            matches.append({
                "configured_core_ordinal": ordinal,
                "component_id": next(iter(components)),
            })
        elif not components:
            missing_count += 1
        else:
            duplicate_count += 1
    summary = {
        "configured_core_count": len(configured_programs),
        "program_component_count": inventory.get("program_component_count", 0),
        "configured_cores_with_unique_program_component_count": len(matches),
        "configured_cores_missing_program_component_count": missing_count,
        "configured_cores_with_duplicate_program_component_count": duplicate_count,
        "program_alias_route_shared_component_count": inventory.get(
            "program_alias_route_shared_component_count", 0),
        "all_configured_cores_have_unique_program_component": (
            len(matches) == len(configured_programs)
        ),
    }
    return {"matches": matches, "summary": summary}


class WindowPidAliasObservation(RedactedEvidence):
    """Keep a target PID in memory while exposing only safe routing evidence."""
    def __init__(self, configured_core_ordinal, target_pid, alias_count):
        self.configured_core_ordinal = configured_core_ordinal
        self.target_pid = target_pid
        self.alias_count = alias_count

    def evidence_summary(self):
        return {
            "configured_core_ordinal": self.configured_core_ordinal,
            "target_pid_positive": self.target_pid > 0,
            "matching_pid_alias_count": self.alias_count,
            "target_pid_has_unique_route_alias": self.alias_count == 1,
        }


def window_pid_alias_observation(session, configured_core_ordinal, route_pids):
    """Validate a documented window PID against live PID aliases in memory."""
    if (isinstance(configured_core_ordinal, bool) or
            not isinstance(configured_core_ordinal, (int, long)) or
            configured_core_ordinal < 0):
        raise ValueError("configured core ordinal must be non-negative")
    if not isinstance(route_pids, list):
        raise ValueError("route PIDs are required")
    target_pid = session.GetTargetPid()
    if isinstance(target_pid, bool) or not isinstance(target_pid, (int, long)):
        raise RuntimeError("GetTargetPid returned an invalid result")
    alias_count = route_pids.count(target_pid) if target_pid > 0 else 0
    return WindowPidAliasObservation(configured_core_ordinal, target_pid, alias_count)


class WindowProcessAliasObservation(RedactedEvidence):
    """Keep Window API process identities in memory and expose only relations."""
    def __init__(self, configured_core_ordinal, target_pid, info_pid, proc_relation,
                 target_alias_count, info_alias_count):
        self.configured_core_ordinal = configured_core_ordinal
        self.target_pid = target_pid
        self.info_pid = info_pid
        self.proc_relation = proc_relation
        self.target_alias_count = target_alias_count
        self.info_alias_count = info_alias_count

    def evidence_summary(self):
        return {
            "configured_core_ordinal": self.configured_core_ordinal,
            "target_pid_positive": self.target_pid is not None and self.target_pid > 0,
            "target_pid_matching_alias_count": self.target_alias_count,
            "process_info_pid_positive": self.info_pid is not None and self.info_pid > 0,
            "process_info_pid_matching_alias_count": self.info_alias_count,
            "target_pid_matches_process_info_pid": (
                self.target_pid is not None and self.target_pid == self.info_pid
            ),
            "process_info_proc_relation": self.proc_relation,
        }


def _strict_pid(value):
    """Parse an integer PID without accepting implicit or lossy coercions."""
    if isinstance(value, bool):
        return None
    if isinstance(value, (int, long)):
        return value if value > 0 else None
    if not isinstance(value, basestring):
        return None
    value = value.strip()
    if not _PID_TEXT.match(value):
        return None
    try:
        parsed = int(value, 0)
    except ValueError:
        return None
    return parsed if parsed > 0 else None


def window_process_alias_observation(session, configured_core_ordinal, route_pids):
    """Compare documented Window process fields to PID aliases in memory."""
    if not isinstance(route_pids, list):
        raise ValueError("route PIDs are required")
    target_pid = _strict_pid(session.GetTargetPid())
    info = session.GetCurPrInfo("")
    if not isinstance(info, dict):
        raise RuntimeError("GetCurPrInfo returned an invalid result")
    info_pid = _strict_pid(info.get("pid"))
    if "proc" not in info:
        proc_relation = "absent"
    else:
        proc_pid = _strict_pid(info.get("proc"))
        if proc_pid is None:
            proc_relation = "unparseable"
        elif info_pid is None:
            proc_relation = "pid_unparseable"
        elif proc_pid == info_pid:
            proc_relation = "matches_pid"
        else:
            proc_relation = "differs_from_pid"
    return WindowProcessAliasObservation(
        configured_core_ordinal,
        target_pid,
        info_pid,
        proc_relation,
        route_pids.count(target_pid) if target_pid is not None else 0,
        route_pids.count(info_pid) if info_pid is not None else 0,
    )


def summarize_window_pid_alias_mapping(configured_count, observations):
    """Confirm exact-ELF windows each identify one unique PID route alias."""
    if isinstance(configured_count, bool) or not isinstance(configured_count, (int, long)):
        raise ValueError("configured core count must be numeric")
    if not isinstance(observations, list):
        raise ValueError("window PID observations are required")
    positive = 0
    routed = 0
    target_pids = []
    for observation in observations:
        if not isinstance(observation, WindowPidAliasObservation):
            raise ValueError("window PID observation is invalid")
        if observation.target_pid > 0:
            positive += 1
            target_pids.append(observation.target_pid)
        if observation.alias_count == 1:
            routed += 1
    duplicate_count = len(target_pids) - len(set(target_pids))
    return {
        "configured_core_count": configured_count,
        "window_observation_count": len(observations),
        "positive_target_pid_count": positive,
        "unique_routed_target_pid_count": len(set(target_pids)),
        "duplicate_target_pid_count": duplicate_count,
        "target_pid_with_unique_route_alias_count": routed,
        "strict_window_pid_alias_mapping_confirmed": (
            len(observations) == configured_count and
            positive == configured_count and
            routed == configured_count and
            duplicate_count == 0
        ),
    }


def summarize_window_process_alias_mapping(configured_count, observations):
    """Summarize Window API process identity evidence without selecting a model."""
    if isinstance(configured_count, bool) or not isinstance(configured_count, (int, long)):
        raise ValueError("configured core count must be numeric")
    if not isinstance(observations, list):
        raise ValueError("window process observations are required")
    return {
        "configured_core_count": configured_count,
        "window_observation_count": len(observations),
        "positive_target_pid_count": sum(
            1 for item in observations if item.target_pid is not None),
        "target_pid_with_route_alias_count": sum(
            1 for item in observations if item.target_alias_count == 1),
        "positive_process_info_pid_count": sum(
            1 for item in observations if item.info_pid is not None),
        "process_info_pid_with_route_alias_count": sum(
            1 for item in observations if item.info_alias_count == 1),
        "target_pid_matches_process_info_pid_count": sum(
            1 for item in observations
            if item.target_pid is not None and item.target_pid == item.info_pid),
        "process_info_proc_matches_pid_count": sum(
            1 for item in observations if item.proc_relation == "matches_pid"),
    }


def _parse_process_listing(output):
    """Parse P text into in-memory identities; callers must summarize before evidence."""
    if not isinstance(output, basestring):
        raise ValueError("process listing must be text")
    rows = []
    for line in output.splitlines():
        if not line.strip() or "# PID" in line:
            continue
        match = _PROCESS_ROW.match(line)
        if match is None:
            raise ValueError("process listing has malformed row")
        status = " ".join(match.group(5).lower().split())
        if status not in _PROCESS_STATUSES:
            raise ValueError("process listing has unknown status")
        pid = _strict_pid(match.group(3))
        if pid is None:
            raise ValueError("process listing has invalid PID")
        ppid = _strict_pid(match.group(4))
        if match.group(4) != "N/A" and ppid is None:
            raise ValueError("process listing has invalid PPID")
        rows.append({
            "selected": bool(match.group(1)),
            "slot": int(match.group(2)),
            "pid": pid,
            "ppid": ppid,
            "status": status,
            "program_name": _normalized_program_name(match.group(7)),
        })
    if not rows:
        raise ValueError("process listing has no rows")
    return rows


def summarize_routed_processes(route_pid, output):
    """Reduce one `route debugger.pid.N P` reply without retaining target data."""
    if (isinstance(route_pid, bool) or
            not isinstance(route_pid, (int, long)) or route_pid < 1):
        raise ValueError("route PID must be positive")
    rows = _parse_process_listing(output)
    selected = [row for row in rows if row["selected"]]
    summary = {
        "row_count": len(rows),
        "selected_row_count": len(selected),
        "selected_row_ordinal": -1,
        "selected_pid_matches_route": False,
        "selected_status": "unknown",
        "selected_program_present": False,
    }
    if len(selected) != 1:
        return summary
    row = selected[0]
    summary["selected_row_ordinal"] = rows.index(row)
    summary["selected_pid_matches_route"] = row["pid"] == route_pid
    summary["selected_status"] = row["status"].replace(" ", "_")
    summary["selected_program_present"] = bool(row["program_name"])
    return summary


def summarize_route_pid_mapping(route_summaries):
    """Summarize only the documented route PID -> selected process PID relation."""
    matching_pid_count = 0
    for summary in route_summaries:
        if not isinstance(summary, dict):
            raise ValueError("route summary must be an object")
        if summary.get("selected_pid_matches_route"):
            matching_pid_count += 1
    return {
        "route_count": len(route_summaries),
        "routes_with_selected_pid_match_count": matching_pid_count,
        "routes_without_selected_pid_match_count": len(route_summaries) - matching_pid_count,
        "all_routes_select_their_pid": matching_pid_count == len(route_summaries),
    }


def summarize_selected_process(output):
    """Reduce a routed P reply when no PID alias relation is assumed."""
    summary = summarize_routed_processes(1, output)
    summary.pop("selected_pid_matches_route")
    return summary


def summarize_program_component_processes(component_ordinal, alias_programs, output,
                                          configured_programs):
    """Relate one program component's P rows without exposing identities."""
    if (isinstance(component_ordinal, bool) or
            not isinstance(component_ordinal, (int, long)) or component_ordinal < 0):
        raise ValueError("program component ordinal must be non-negative")
    if not isinstance(alias_programs, list) or not alias_programs:
        raise ValueError("program component aliases are required")
    if not isinstance(configured_programs, list) or not configured_programs:
        raise ValueError("configured core programs are required")
    rows = _parse_process_listing(output)
    selected = [row for row in rows if row["selected"]]
    program_rows = [row for row in rows if row["program_name"]]
    alias_programs = set(alias_programs)
    configured_program_list = configured_programs
    configured_programs = set(configured_program_list)
    selected_row = selected[0] if len(selected) == 1 else None
    selected_program = selected_row["program_name"] if selected_row else ""
    selected_configured_ordinal = (-1 if selected_program not in configured_programs
                                   else configured_program_list.index(selected_program))

    def selected_matches(field, candidate_field):
        if selected_row is None or selected_row[field] is None:
            return 0
        return sum(1 for row in program_rows
                   if row[candidate_field] is not None and
                   selected_row[field] == row[candidate_field])

    return {
        "program_component_ordinal": component_ordinal,
        "row_count": len(rows),
        "program_row_count": len(program_rows),
        "selected_row_count": len(selected),
        "selected_row_ordinal": rows.index(selected_row) if selected_row else -1,
        "selected_status": (selected_row["status"].replace(" ", "_")
                            if selected_row else "unknown"),
        "selected_program_present": bool(selected_row and selected_row["program_name"]),
        "selected_program_matches_configured_elf": selected_configured_ordinal >= 0,
        "selected_configured_core_ordinal": selected_configured_ordinal,
        "alias_program_exact_p_program_match_count": sum(
            1 for row in program_rows if row["program_name"] in alias_programs),
        "configured_elf_exact_p_program_match_count": sum(
            1 for row in program_rows if row["program_name"] in configured_programs),
        "selected_pid_to_program_pid_match_count": selected_matches("pid", "pid"),
        "selected_pid_to_program_ppid_match_count": selected_matches("pid", "ppid"),
        "selected_ppid_to_program_pid_match_count": selected_matches("ppid", "pid"),
        "selected_ppid_to_program_ppid_match_count": selected_matches("ppid", "ppid"),
        "selected_slot_to_program_slot_match_count": (
            sum(1 for row in program_rows if selected_row is not None and
                selected_row["slot"] == row["slot"])
        ),
        "diagnostic_alias_program_basename_p_program_match_count": sum(
            1 for row in program_rows
            if any(ntpath.basename(row["program_name"]) == ntpath.basename(alias)
                   for alias in alias_programs)),
        "diagnostic_alias_program_suffix_p_program_match_count": sum(
            1 for row in program_rows
            if any(row["program_name"].endswith(alias) or alias.endswith(row["program_name"])
                   for alias in alias_programs)),
    }


def summarize_strict_program_component_mapping(configured_programs, observations):
    """Require a complete bijection from routed program components to core ELF IDs."""
    if not isinstance(configured_programs, list) or not configured_programs:
        raise ValueError("configured core programs are required")
    if not isinstance(observations, list):
        raise ValueError("program component observations are required")
    mapped_ordinals = []
    rejected = 0
    selected_unmapped = 0
    selected_not_stopped = 0
    for observation in observations:
        if not isinstance(observation, ProgramComponentObservation):
            raise ValueError("program component observation is invalid")
        process = observation.process_summary
        if not (observation.process_command.get("accepted") and
                observation.process_command.get("status") == 1 and
                observation.halt_command.get("accepted") and
                observation.halt_command.get("status") == 1):
            rejected += 1
            continue
        if not isinstance(process, dict):
            rejected += 1
            continue
        ordinal = process.get("selected_configured_core_ordinal", -1)
        if not process.get("selected_program_matches_configured_elf"):
            selected_unmapped += 1
            continue
        if process.get("selected_row_count") != 1 or process.get("selected_status") != "stopped":
            selected_not_stopped += 1
            continue
        if (isinstance(ordinal, bool) or not isinstance(ordinal, (int, long)) or
                ordinal < 0 or ordinal >= len(configured_programs)):
            selected_unmapped += 1
            continue
        mapped_ordinals.append(ordinal)
    unique = set(mapped_ordinals)
    duplicates = len(mapped_ordinals) - len(unique)
    return {
        "configured_core_count": len(configured_programs),
        "program_component_observation_count": len(observations),
        "accepted_program_component_count": len(observations) - rejected,
        "selected_program_unmapped_component_count": selected_unmapped,
        "selected_program_not_stopped_component_count": selected_not_stopped,
        "mapped_configured_core_count": len(unique),
        "duplicate_configured_core_mapping_count": duplicates,
        "unmapped_configured_core_count": len(configured_programs) - len(unique),
        "extra_program_component_count": max(0, len(observations) - len(configured_programs)),
        "strict_program_component_mapping_confirmed": (
            len(observations) == len(configured_programs) and
            rejected == 0 and selected_unmapped == 0 and selected_not_stopped == 0 and
            len(unique) == len(configured_programs) and duplicates == 0
        ),
    }


def inspection_output_shape(command, raw):
    """Apply the production parser's structural contract without retaining text."""
    if not isinstance(command, basestring) or not isinstance(raw, basestring):
        return False
    if len(raw) > (1 << 20) or len(raw.splitlines()) > 4096:
        return False
    lines = [line for line in raw.splitlines() if line.strip()]
    if any(len(line) > (16 << 10) for line in lines):
        return False
    if command == "calls":
        return bool(lines) and all(_STACK_LINE.match(line) is not None for line in lines)
    if command == "l":
        if not lines:
            return False
        marker = "*** Caution: variable is hidden or out of scope ***"
        return all(" = " in line.replace(marker, "") and
                   bool(line.replace(marker, "").strip().split(" = ", 1)[0])
                   for line in lines)
    if command == "B":
        return (raw.strip() == "No software breakpoints set." or
                bool(lines) and all(_BREAKPOINT_LINE.match(line) is not None
                                    for line in lines))
    if command == "print $_STATE":
        marker = "*** Caution: variable is hidden or out of scope ***"
        if len(lines) != 1:
            return False
        line = lines[0].replace(marker, "").strip()
        return " = " in line and bool(line.split(" = ", 1)[0])
    raise ValueError("unknown inspection command")


def inspection_semantic_flags(raw):
    """Classify opaque non-parser output without retaining any of its text."""
    if not isinstance(raw, basestring):
        return {
            "trimmed_empty": False,
            "exact_known_phrase_category": "non_text",
            "contains_path_payload": False,
            "contains_address_payload": False,
            "contains_digit_payload": False,
            "contains_identifier_like_payload": False,
            "contains_keyword_no": False,
            "contains_keyword_not": False,
            "contains_keyword_call": False,
            "contains_keyword_stack": False,
            "contains_keyword_frame": False,
            "contains_keyword_process": False,
            "contains_keyword_program": False,
            "contains_keyword_running": False,
            "contains_keyword_loaded": False,
            "contains_keyword_debug": False,
            "contains_keyword_info": False,
        }
    trimmed = raw.strip()
    lowered = trimmed.lower()
    return {
        "trimmed_empty": not bool(trimmed),
        "exact_known_phrase_category": _KNOWN_INSPECTION_PHRASES.get(
            trimmed, "unrecognized"),
        "contains_path_payload": _PATH_PAYLOAD.search(trimmed) is not None,
        "contains_address_payload": _ADDRESS_PAYLOAD.search(trimmed) is not None,
        "contains_digit_payload": any(char.isdigit() for char in trimmed),
        "contains_identifier_like_payload": _IDENTIFIER_PAYLOAD.search(trimmed) is not None,
        "contains_keyword_no": "no" in lowered,
        "contains_keyword_not": "not" in lowered,
        "contains_keyword_call": "call" in lowered,
        "contains_keyword_stack": "stack" in lowered,
        "contains_keyword_frame": "frame" in lowered,
        "contains_keyword_process": "process" in lowered,
        "contains_keyword_program": "program" in lowered,
        "contains_keyword_running": "running" in lowered,
        "contains_keyword_loaded": "loaded" in lowered,
        "contains_keyword_debug": "debug" in lowered,
        "contains_keyword_info": "info" in lowered,
    }


def calls_grammar_signature(raw):
    """Classify ``calls`` syntax without retaining any target-owned text.

    The signature intentionally has no raw text, content-derived hash, raw
    length, address, frame index, path, or identifier.  It is sufficient to
    distinguish the already-pinned source-frame grammar from the narrow
    address-only and symbol-plus-address candidates that might be represented
    as DAP stack frames without a source location.
    """
    if not isinstance(raw, basestring):
        return _calls_grammar_summary("non_text", [])
    lines = [line for line in raw.splitlines() if line.strip()]
    if len(lines) > 4096 or any(len(line) > (16 << 10) for line in lines):
        return _calls_grammar_summary("bounded_input_rejected", [])
    return _calls_grammar_summary(
        "text", [_calls_line_grammar_signature(line) for line in lines])


def source_file_listing_commands(_cfg):
    """Return the sole documented read-only source-list command.

    `l f <string>` has contains-filter semantics, so it cannot establish
    exact source membership and is deliberately excluded from recon capture.
    """
    return [("all", "l f")]


def source_file_listing_grammar_signature(raw):
    """Summarize one ``l f`` reply without retaining source paths.

    This is deliberately a bounded grammar-capture parser, not a production
    source-file parser.  The manual establishes command semantics but not a
    machine-readable output grammar.  A production parser can be introduced
    only after a stable, redacted capture demonstrates its exact row format.
    """
    summary = {
        "input_category": "non_text",
        "nonblank_line_count": 0,
        "blank_line_count": 0,
        "source_locator_candidate_count": 0,
        "windows_absolute_candidate_count": 0,
        "slash_rooted_candidate_count": 0,
        "relative_locator_candidate_count": 0,
        "unclassified_line_count": 0,
        "grammar_shape": "",
        "field_count_buckets": {
            "one": 0, "two": 0, "three": 0, "four": 0,
            "five_to_eight": 0, "nine_or_more": 0,
        },
        "index_prefix_counts": {
            "none": 0, "decimal": 0, "bracketed_decimal": 0,
            "bullet": 0,
        },
        "quote_counts": {
            "none": 0, "single": 0, "double": 0, "mixed": 0,
        },
        "whitespace_separator_counts": {
            "none": 0, "space": 0, "tab": 0, "mixed": 0,
        },
        "path_separator_counts": {
            "none": 0, "forward": 0, "backslash": 0, "mixed": 0,
        },
        "field_delimiter_counts": {
            "none": 0, "colon": 0, "comma": 0, "semicolon": 0,
            "pipe": 0, "equals": 0, "multiple": 0,
        },
        "extension_present_line_count": 0,
        "distinct_line_shape_count": 0,
        "repeated_line_shape_count": 0,
        "dominant_line_shape_count": 0,
        "line_shape_counts": {},
        "decimal_prefix_delimiter_counts": {
            "none": 0, "colon": 0, "dot": 0, "close_paren": 0,
        },
        "colon_role_counts": {
            "none": 0, "index_then_drive": 0, "index_only": 0,
            "drive_only": 0, "other": 0,
        },
        "normalized_line_grammar_counts": {
            "bullet_four_field_header": 0,
            "decimal_index_drive_absolute_path": 0,
            "static_source_file_legend": 0,
            "other": 0,
        },
        "first_line_is_bullet_four_field_header": False,
        "row_indices_zero_based_contiguous": False,
        "strict_production_grammar_candidate": False,
        "static_marker_count": 0,
        "static_marker_recognized": False,
        "static_marker_position_bucket": "none",
        "static_marker_relative_to_data_rows": "none",
        "static_marker_keyword_flags": dict(
            (keyword, False) for keyword in _SOURCE_FILE_MARKER_KEYWORDS),
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
        "bullet_nonrow_keyword_flags": dict(
            (keyword, False) for keyword in _SOURCE_FILE_MARKER_KEYWORDS),
        "bullet_nonrow_static_marker_recognized": False,
    }
    if not isinstance(raw, basestring):
        return summary
    lines = raw.splitlines()
    if len(raw) > (1 << 20) or len(lines) > 4096 or any(
            len(line) > (16 << 10) for line in lines):
        summary["input_category"] = "bounded_input_rejected"
        return summary
    summary["input_category"] = "text"
    categories = set()
    shapes = {}
    row_indices = []
    row_ordinals = []
    first_nonblank_seen = False
    special_markers = []
    nonblank_ordinal = 0
    for line in lines:
        value = line.strip()
        if not value:
            summary["blank_line_count"] += 1
            continue
        summary["nonblank_line_count"] += 1
        if _SOURCE_LOCATOR.match(value) is None:
            summary["unclassified_line_count"] += 1
            categories.add("unclassified")
        else:
            summary["source_locator_candidate_count"] += 1
            if re.match(r"^[A-Za-z]:[\\/]", value) is not None or value.startswith("\\\\"):
                summary["windows_absolute_candidate_count"] += 1
                categories.add("windows_absolute")
            elif value.startswith("/"):
                summary["slash_rooted_candidate_count"] += 1
                categories.add("slash_rooted")
            else:
                summary["relative_locator_candidate_count"] += 1
                categories.add("relative_locator")
        signature = _source_file_listing_line_signature(line)
        if not first_nonblank_seen:
            summary["first_line_is_bullet_four_field_header"] = (
                signature["normalized_grammar"] == "bullet_four_field_header")
            first_nonblank_seen = True
        summary["field_count_buckets"][signature["field_count_bucket"]] += 1
        summary["index_prefix_counts"][signature["index_prefix"]] += 1
        summary["quote_counts"][signature["quotes"]] += 1
        summary["whitespace_separator_counts"][signature["whitespace"]] += 1
        summary["path_separator_counts"][signature["path_separator"]] += 1
        summary["field_delimiter_counts"][signature["field_delimiter"]] += 1
        summary["decimal_prefix_delimiter_counts"][
            signature["decimal_prefix_delimiter"]] += 1
        summary["colon_role_counts"][signature["colon_role"]] += 1
        summary["normalized_line_grammar_counts"][
            signature["normalized_grammar"]] += 1
        if signature["row_index"] is not None:
            row_indices.append(signature["row_index"])
            row_ordinals.append(nonblank_ordinal)
        if signature["is_bullet_four_field_line"]:
            special_markers.append({
                "nonblank_ordinal": nonblank_ordinal,
                "recognized": signature["static_marker_recognized"],
                "keywords": signature["marker_keyword_flags"],
                "token_categories": signature["marker_token_categories"],
                "token_length_buckets": signature["marker_token_length_buckets"],
                "second_token_kind": signature["marker_second_token_kind"],
                "third_token_kind": signature["marker_third_token_kind"],
                "legend_pair": signature["marker_legend_pair"],
                "first_decoration": signature["marker_first_decoration"],
                "last_decoration": signature["marker_last_decoration"],
                "decorations_equal": signature["marker_decorations_equal"],
                "finite_legend_match": signature["static_marker_recognized"],
            })
        if signature["extension_present"]:
            summary["extension_present_line_count"] += 1
        shape = signature["shape"]
        if shape not in shapes and len(shapes) == 64:
            summary["input_category"] = "shape_diversity_rejected"
            return summary
        shapes[shape] = shapes.get(shape, 0) + 1
        nonblank_ordinal += 1
    if not categories:
        summary["grammar_shape"] = "empty"
    elif len(categories) == 1:
        summary["grammar_shape"] = next(iter(categories))
    else:
        summary["grammar_shape"] = "multiple_line_categories"
    summary["distinct_line_shape_count"] = len(shapes)
    summary["repeated_line_shape_count"] = sum(
        count for count in shapes.values() if count > 1)
    summary["dominant_line_shape_count"] = max(shapes.values()) if shapes else 0
    summary["line_shape_counts"] = dict(sorted(shapes.items()))
    header_count = summary["normalized_line_grammar_counts"][
        "bullet_four_field_header"]
    row_count = summary["normalized_line_grammar_counts"][
        "decimal_index_drive_absolute_path"]
    summary["row_indices_zero_based_contiguous"] = (
        row_indices == list(range(row_count)))
    summary["static_marker_count"] = len(special_markers)
    summary["bullet_nonrow_count"] = len(special_markers)
    if len(special_markers) == 1:
        marker = special_markers[0]
        summary["static_marker_recognized"] = marker["recognized"]
        summary["static_marker_keyword_flags"] = marker["keywords"]
        summary["bullet_nonrow_static_marker_recognized"] = marker["recognized"]
        summary["bullet_nonrow_keyword_flags"] = marker["keywords"]
        summary["bullet_nonrow_token_categories"] = marker["token_categories"]
        summary["bullet_nonrow_token_length_buckets"] = marker["token_length_buckets"]
        summary["bullet_nonrow_second_token_kind"] = marker["second_token_kind"]
        summary["bullet_nonrow_third_token_kind"] = marker["third_token_kind"]
        summary["bullet_nonrow_legend_pair"] = marker["legend_pair"]
        summary["bullet_nonrow_first_decoration"] = marker["first_decoration"]
        summary["bullet_nonrow_last_decoration"] = marker["last_decoration"]
        summary["bullet_nonrow_decorations_equal"] = marker["decorations_equal"]
        summary["bullet_nonrow_finite_legend_match"] = marker["finite_legend_match"]
        if marker["nonblank_ordinal"] == 0:
            summary["static_marker_position_bucket"] = "first"
        elif marker["nonblank_ordinal"] == summary["nonblank_line_count"] - 1:
            summary["static_marker_position_bucket"] = "last"
        else:
            summary["static_marker_position_bucket"] = "middle"
        preceding_rows = len([ordinal for ordinal in row_ordinals
                              if ordinal < marker["nonblank_ordinal"]])
        if preceding_rows == 0:
            summary["static_marker_relative_to_data_rows"] = "before_all"
        elif preceding_rows == len(row_ordinals):
            summary["static_marker_relative_to_data_rows"] = "after_all"
        else:
            summary["static_marker_relative_to_data_rows"] = "between_rows"
        summary["bullet_nonrow_position_bucket"] = summary[
            "static_marker_position_bucket"]
        summary["bullet_nonrow_relative_to_data_rows"] = summary[
            "static_marker_relative_to_data_rows"]
    summary["strict_production_grammar_candidate"] = (
        summary["blank_line_count"] == 0 and
        not summary["first_line_is_bullet_four_field_header"] and
        header_count == 0 and
        summary["static_marker_count"] == 1 and
        summary["static_marker_recognized"] and
        summary["static_marker_position_bucket"] == "first" and
        summary["static_marker_relative_to_data_rows"] == "before_all" and
        summary["normalized_line_grammar_counts"]["other"] == 0 and
        len(row_indices) == row_count and
        summary["row_indices_zero_based_contiguous"])
    if summary["strict_production_grammar_candidate"]:
        summary["grammar_shape"] = "strict_source_file_listing"
    summary["production_parser_delta"] = (
        _source_file_listing_production_delta(raw))
    return summary


def _source_file_listing_production_delta(raw):
    """Classify divergence from the Go ``l f`` parser without path evidence.

    The returned categories and counts are intentionally finite.  They expose
    whether a grammar capture that looks structurally stable still violates
    the exact production row contract, while retaining neither a path, a
    character value, nor a content-derived identifier.
    """
    delta = {
        "input_within_go_bounds": False,
        "line_count_within_go_bounds": False,
        "terminal_cr_line_count": 0,
        "embedded_cr_line_count": 0,
        "exact_go_header": False,
        "row_count": 0,
        "row_prefix_leading_whitespace_rows": {
            "none": 0, "space": 0, "tab": 0, "mixed": 0,
            "not_indexed": 0,
        },
        "row_prefix_delimiter_rows": {
            "colon": 0, "other": 0, "missing": 0,
        },
        "row_prefix_post_colon_whitespace_rows": {
            "single_space": 0, "multiple_space": 0, "tab": 0,
            "mixed": 0, "none": 0, "not_colon": 0,
        },
        # Keep the same finite prefix evidence at this level. Recorder bounds
        # nested dictionaries at the output boundary, while p12's grammar is
        # itself nested below a step result.
        "prefix_leading_none_count": 0,
        "prefix_leading_space_count": 0,
        "prefix_leading_tab_count": 0,
        "prefix_leading_mixed_count": 0,
        "prefix_not_indexed_count": 0,
        "prefix_leading_space_width_1_count": 0,
        "prefix_leading_space_width_2_count": 0,
        "prefix_leading_space_width_3_count": 0,
        "prefix_leading_space_width_4_count": 0,
        "prefix_leading_space_width_5_to_8_count": 0,
        "prefix_leading_space_width_over_8_count": 0,
        "prefix_leading_space_all_rows_same_width": False,
        "prefix_delimiter_colon_count": 0,
        "prefix_delimiter_other_count": 0,
        "prefix_delimiter_missing_count": 0,
        "prefix_post_colon_single_space_count": 0,
        "prefix_post_colon_multiple_space_count": 0,
        "prefix_post_colon_tab_count": 0,
        "prefix_post_colon_mixed_count": 0,
        "prefix_post_colon_none_count": 0,
        "prefix_post_colon_not_colon_count": 0,
        "drive_absolute_payload_count": 0,
        "broad_drive_row_count": 0,
        "exact_go_row_syntax_count": 0,
        "row_syntax_rejection_count": 0,
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
        "row_index_zero_based_contiguous": False,
        "relative_component_row_count": 0,
        "empty_component_row_count": 0,
        "duplicate_canonical_path_row_count": 0,
        "duplicate_exact_path_row_count": 0,
        "duplicate_case_fold_path_row_count": 0,
        "duplicate_separator_normalization_path_row_count": 0,
        "duplicate_other_canonical_collision_row_count": 0,
        "duplicate_only_exact_path_rows": False,
        "strict_go_parser_candidate": False,
    }
    if not isinstance(raw, basestring) or not raw or len(raw) > (1 << 20):
        return delta
    delta["input_within_go_bounds"] = True
    lines = raw.split("\n")
    if lines and lines[-1] == "":
        lines = lines[:-1]
    if len(lines) < 2 or len(lines) > 4096:
        return delta
    delta["line_count_within_go_bounds"] = True
    normalized = []
    for line in lines:
        if line.endswith("\r"):
            line = line[:-1]
            delta["terminal_cr_line_count"] += 1
        if "\r" in line or len(line) > (16 << 10):
            delta["embedded_cr_line_count"] += 1
        normalized.append(line)
    if delta["embedded_cr_line_count"]:
        return delta
    delta["exact_go_header"] = normalized[0] == _SOURCE_FILE_LISTING_HEADER
    expected_indices = []
    canonical_paths = {}
    leading_space_widths = []
    for line in normalized[1:]:
        delta["row_count"] += 1
        prefix = _SOURCE_FILE_BROAD_INDEX_PREFIX.match(line)
        if prefix is None:
            delta["row_prefix_leading_whitespace_rows"]["not_indexed"] += 1
            delta["row_prefix_delimiter_rows"]["missing"] += 1
            delta["row_prefix_post_colon_whitespace_rows"]["not_colon"] += 1
        else:
            leading, suffix = prefix.group(1), prefix.group(3)
            leading_shape = _source_file_whitespace_shape(leading)
            delta["row_prefix_leading_whitespace_rows"][leading_shape] += 1
            if leading_shape == "space":
                leading_space_widths.append(len(leading))
                delta["prefix_leading_space_width_%s_count" % (
                    _source_file_leading_space_width_bucket(len(leading)))] += 1
            if not suffix:
                delta["row_prefix_delimiter_rows"]["missing"] += 1
                delta["row_prefix_post_colon_whitespace_rows"]["not_colon"] += 1
            elif suffix[0] != ":":
                delta["row_prefix_delimiter_rows"]["other"] += 1
                delta["row_prefix_post_colon_whitespace_rows"]["not_colon"] += 1
            else:
                delta["row_prefix_delimiter_rows"]["colon"] += 1
                post_colon = suffix[1:]
                whitespace = _SOURCE_FILE_LEADING_WHITESPACE.match(post_colon).group(0)
                delta["row_prefix_post_colon_whitespace_rows"][
                    _source_file_post_colon_whitespace_shape(whitespace)] += 1
                payload = post_colon[len(whitespace):]
                if re.match(r"^[A-Za-z]:\\", payload) is not None:
                    delta["drive_absolute_payload_count"] += 1
        broad = _SOURCE_FILE_BROAD_DRIVE_ROW.match(line)
        if broad is None:
            delta["row_syntax_rejection_count"] += 1
            continue
        delta["broad_drive_row_count"] += 1
        # The drive designator and its delimiter are validated by the broad
        # row expression.  Classify only the path tail, where Go's allowlist
        # applies, so the required drive colon is never reported as a delta.
        payload_categories = _source_file_go_payload_categories(broad.group(2)[3:])
        for category in payload_categories:
            delta["row_payload_category_rows"][category] += 1
        match = _SOURCE_FILE_GO_ROW.match(line)
        if match is None:
            delta["row_syntax_rejection_count"] += 1
            if payload_categories == ("allowed_only",):
                delta["allowed_charset_syntax_rejection_count"] += 1
            continue
        delta["exact_go_row_syntax_count"] += 1
        expected_indices.append(int(match.group(1).strip()))
        path = match.group(2)
        parts = path[3:].split("\\")
        if any(part == "" for part in parts):
            delta["empty_component_row_count"] += 1
            continue
        if any(part == "." or part == ".." for part in parts):
            delta["relative_component_row_count"] += 1
            continue
        canonical = path.replace("\\", "/").lower()
        previous_path = canonical_paths.get(canonical)
        if previous_path is not None:
            delta["duplicate_canonical_path_row_count"] += 1
            if previous_path == path:
                delta["duplicate_exact_path_row_count"] += 1
            elif previous_path.replace("\\", "/") == path.replace("\\", "/"):
                delta["duplicate_separator_normalization_path_row_count"] += 1
            elif previous_path.lower() == path.lower():
                delta["duplicate_case_fold_path_row_count"] += 1
            else:
                delta["duplicate_other_canonical_collision_row_count"] += 1
        else:
            canonical_paths[canonical] = path
    delta["row_index_zero_based_contiguous"] = (
        expected_indices == list(range(len(expected_indices))))
    delta["strict_go_parser_candidate"] = (
        delta["input_within_go_bounds"] and
        delta["line_count_within_go_bounds"] and
        delta["embedded_cr_line_count"] == 0 and
        delta["exact_go_header"] and
        delta["row_count"] > 0 and
        delta["exact_go_row_syntax_count"] == delta["row_count"] and
        delta["row_syntax_rejection_count"] == 0 and
        delta["row_index_zero_based_contiguous"] and
        delta["relative_component_row_count"] == 0 and
        delta["empty_component_row_count"] == 0)
    _source_file_flatten_prefix_delta(delta)
    delta["prefix_leading_space_all_rows_same_width"] = (
        bool(leading_space_widths) and
        len(leading_space_widths) == delta["row_count"] and
        len(set(leading_space_widths)) == 1)
    delta["duplicate_only_exact_path_rows"] = (
        delta["duplicate_canonical_path_row_count"] > 0 and
        delta["duplicate_exact_path_row_count"] ==
        delta["duplicate_canonical_path_row_count"])
    return delta


def _source_file_flatten_prefix_delta(delta):
    """Copy nested finite prefix counters into Recorder-safe top-level keys."""
    for category, count in delta["row_prefix_leading_whitespace_rows"].items():
        if category == "not_indexed":
            delta["prefix_not_indexed_count"] = count
        else:
            delta["prefix_leading_%s_count" % category] = count
    for category, count in delta["row_prefix_delimiter_rows"].items():
        delta["prefix_delimiter_%s_count" % category] = count
    for category, count in delta["row_prefix_post_colon_whitespace_rows"].items():
        delta["prefix_post_colon_%s_count" % category] = count


def _source_file_whitespace_shape(value):
    if not value:
        return "none"
    if " " in value and "\t" in value:
        return "mixed"
    if " " in value:
        return "space"
    return "tab"


def _source_file_post_colon_whitespace_shape(value):
    if not value:
        return "none"
    if value == " ":
        return "single_space"
    if " " in value and "\t" in value:
        return "mixed"
    if "\t" in value:
        return "tab"
    return "multiple_space"


def _source_file_leading_space_width_bucket(width):
    if width == 1:
        return "1"
    if width == 2:
        return "2"
    if width == 3:
        return "3"
    if width == 4:
        return "4"
    if width <= 8:
        return "5_to_8"
    return "over_8"


def _source_file_go_payload_categories(payload):
    """Return fixed character-class categories for one private row payload."""
    categories = set()
    for character in payload:
        if ("A" <= character <= "Z" or "a" <= character <= "z" or
                "0" <= character <= "9" or character in "_.\\-"):
            continue
        if character == "/":
            categories.add("forward_slash")
        elif character == " " or character == "\t":
            categories.add("whitespace")
        elif ord(character) < 0x20 or ord(character) == 0x7f:
            categories.add("control")
        elif ord(character) > 0x7f:
            categories.add("non_ascii")
        elif 0x21 <= ord(character) <= 0x7e:
            categories.add("other_ascii_punctuation")
        else:
            categories.add("other_ascii_printable")
    return tuple(sorted(categories)) if categories else ("allowed_only",)


def _source_file_listing_line_signature(line):
    """Classify one opaque source-list row into a fixed text-free shape."""
    value = line.strip()
    tokens = value.split()
    field_count = len(tokens)
    if field_count <= 1:
        field_count_bucket = "one"
    elif field_count == 2:
        field_count_bucket = "two"
    elif field_count == 3:
        field_count_bucket = "three"
    elif field_count == 4:
        field_count_bucket = "four"
    elif field_count <= 8:
        field_count_bucket = "five_to_eight"
    else:
        field_count_bucket = "nine_or_more"
    decimal_prefix = _SOURCE_FILE_DECIMAL_PREFIX.match(line)
    if _SOURCE_FILE_BRACKET_PREFIX.match(line) is not None:
        index_prefix = "bracketed_decimal"
    elif decimal_prefix is not None:
        index_prefix = "decimal"
    elif value.startswith(("-", "*", "+")):
        index_prefix = "bullet"
    else:
        index_prefix = "none"
    has_single_quote = "'" in value
    has_double_quote = '"' in value
    if has_single_quote and has_double_quote:
        quotes = "mixed"
    elif has_single_quote:
        quotes = "single"
    elif has_double_quote:
        quotes = "double"
    else:
        quotes = "none"
    has_space = " " in line
    has_tab = "\t" in line
    if has_space and has_tab:
        whitespace = "mixed"
    elif has_space:
        whitespace = "space"
    elif has_tab:
        whitespace = "tab"
    else:
        whitespace = "none"
    has_forward = "/" in value
    has_backslash = "\\" in value
    if has_forward and has_backslash:
        path_separator = "mixed"
    elif has_forward:
        path_separator = "forward"
    elif has_backslash:
        path_separator = "backslash"
    else:
        path_separator = "none"
    delimiters = []
    for character, category in ((":", "colon"), (",", "comma"),
                                (";", "semicolon"), ("|", "pipe"),
                                ("=", "equals")):
        if character in value:
            delimiters.append(category)
    if not delimiters:
        field_delimiter = "none"
    elif len(delimiters) == 1:
        field_delimiter = delimiters[0]
    else:
        field_delimiter = "multiple"
    extension_present = _SOURCE_FILE_EXTENSION.search(value) is not None
    if decimal_prefix is None:
        decimal_prefix_delimiter = "none"
    else:
        decimal_prefix_delimiter = {
            ":": "colon", ".": "dot", ")": "close_paren",
        }[decimal_prefix.group(1)]
    indexed_path = _SOURCE_FILE_INDEXED_WINDOWS_PATH.match(line)
    normalized_marker = " ".join(value.lower().split())
    # A special row is derived from the same tokens and prefix classification
    # that feed the public structural summary. Do not re-parse it with a
    # second marker-specific regexp: divergent classifiers would conceal the
    # one row production must either recognize exactly or reject.
    marker_tokens = tokens
    is_bullet_four_field_line = (
        index_prefix == "bullet" and field_count == 4)
    marker_words = set(re.findall(r"[a-z]+", normalized_marker))
    marker_keyword_flags = dict(
        (keyword, _source_file_marker_keyword_present(
            keyword, marker_tokens, marker_words))
        for keyword in _SOURCE_FILE_MARKER_KEYWORDS)
    static_marker_recognized = line == _SOURCE_FILE_LISTING_HEADER
    marker_second_token_kind = _source_file_marker_word_kind(
        marker_tokens[1] if is_bullet_four_field_line else "")
    marker_third_token_kind = _source_file_marker_word_kind(
        marker_tokens[2] if is_bullet_four_field_line else "")
    marker_legend_pair = _source_file_marker_legend_pair(
        marker_second_token_kind, marker_third_token_kind)
    marker_first_decoration = _source_file_marker_decoration(
        marker_tokens[0] if is_bullet_four_field_line else "")
    marker_last_decoration = _source_file_marker_decoration(
        marker_tokens[-1] if is_bullet_four_field_line else "")
    marker_decorations_equal = (
        is_bullet_four_field_line and
        marker_tokens[0] == marker_tokens[-1])
    if indexed_path is not None:
        colon_role = "index_then_drive"
        row_index = int(indexed_path.group(1))
        normalized_grammar = "decimal_index_drive_absolute_path"
    else:
        row_index = None
        if is_bullet_four_field_line:
            colon_role = "none" if ":" not in value else "other"
            normalized_grammar = (
                "static_source_file_legend" if static_marker_recognized else "other")
        elif decimal_prefix is not None:
            colon_role = "index_only" if decimal_prefix_delimiter == "colon" else "other"
            normalized_grammar = "other"
        elif re.search(r"[A-Za-z]:\\", value) is not None:
            colon_role = "drive_only"
            normalized_grammar = "other"
        elif ":" in value:
            colon_role = "other"
            normalized_grammar = "other"
        else:
            colon_role = "none"
            normalized_grammar = "other"
    return {
        "field_count_bucket": field_count_bucket,
        "index_prefix": index_prefix,
        "quotes": quotes,
        "whitespace": whitespace,
        "path_separator": path_separator,
        "field_delimiter": field_delimiter,
        "extension_present": extension_present,
        "decimal_prefix_delimiter": decimal_prefix_delimiter,
        "colon_role": colon_role,
        "normalized_grammar": normalized_grammar,
        "row_index": row_index,
        "is_bullet_four_field_line": is_bullet_four_field_line,
        "static_marker_recognized": static_marker_recognized,
        "marker_keyword_flags": marker_keyword_flags,
        "marker_token_categories": [_source_file_marker_token_category(token)
                                    for token in marker_tokens]
        if is_bullet_four_field_line else [],
        "marker_token_length_buckets": [_source_file_marker_token_length_bucket(token)
                                         for token in marker_tokens]
        if is_bullet_four_field_line else [],
        "marker_second_token_kind": marker_second_token_kind,
        "marker_third_token_kind": marker_third_token_kind,
        "marker_legend_pair": marker_legend_pair,
        "marker_first_decoration": marker_first_decoration,
        "marker_last_decoration": marker_last_decoration,
        "marker_decorations_equal": marker_decorations_equal,
        "shape": "|".join((
            "fields_" + field_count_bucket,
            "prefix_" + index_prefix,
            "quotes_" + quotes,
            "whitespace_" + whitespace,
            "path_" + path_separator,
            "delimiter_" + field_delimiter,
            "extension_" + ("present" if extension_present else "absent"),
        )),
    }


def _source_file_marker_token_category(token):
    if re.match(r"^[^A-Za-z0-9]+$", token) is not None:
        return "punctuation"
    if re.match(r"^[A-Za-z]+$", token) is not None:
        return "alpha"
    if re.match(r"^[0-9]+$", token) is not None:
        return "decimal"
    return "other"


def _source_file_marker_token_length_bucket(token):
    length = len(token)
    if length == 1:
        return "one"
    if length <= 4:
        return "two_to_four"
    if length <= 8:
        return "five_to_eight"
    return "nine_or_more"


def _source_file_marker_word_kind(token):
    normalized = token.lower()
    if normalized in ("file", "name", "names", "filename", "listing", "list"):
        return normalized
    return "other" if token else "none"


def _source_file_marker_legend_pair(first, second):
    if first == "file" and second == "names":
        return "file_names"
    return "none"


def _source_file_marker_keyword_present(keyword, tokens, words):
    if keyword == "asterisk":
        return bool(tokens) and tokens[0] == "*"
    if keyword == "file":
        return any(word == "file" or word.startswith("file") for word in words)
    return keyword in words


def _source_file_marker_decoration(token):
    if not token or re.match(r"^[^A-Za-z0-9]+$", token) is None:
        return {
            "repeated_punctuation": False,
            "character_class": "none",
            "count_bucket": "none",
        }
    character = token[0]
    repeated = token == character * len(token)
    if character == "-":
        character_class = "hyphen"
    elif character == "*":
        character_class = "asterisk"
    elif character == "_":
        character_class = "underscore"
    elif character == ".":
        character_class = "period"
    elif character == "=":
        character_class = "equals"
    else:
        character_class = "other_punctuation"
    return {
        "repeated_punctuation": repeated,
        "character_class": character_class,
        "count_bucket": _source_file_marker_token_length_bucket(token),
    }


def _calls_grammar_summary(input_category, lines):
    """Flatten line signatures so Recorder's bounded recursion preserves them."""
    summary = {
        "input_category": input_category,
        "nonblank_line_count": len(lines),
        "indexed_source_frame_count": 0,
        "indexed_address_only_frame_count": 0,
        "indexed_function_comma_address_frame_count": 0,
        "indexed_function_at_address_frame_count": 0,
        "indexed_function_address_frame_count": 0,
        "indexed_symbol_address_frame_count": 0,
        "indexed_unclassified_frame_payload_count": 0,
        "unclassified_count": 0,
        "index_prefix_line_count": 0,
        "selected_marker_line_count": 0,
        "space_separator_line_count": 0,
        "tab_separator_line_count": 0,
        "ascii_letter_or_underscore_line_count": 0,
        "decimal_digit_line_count": 0,
        "hex_address_line_count": 0,
        "bracketed_line_count": 0,
        "parenthesized_line_count": 0,
        "colon_line_count": 0,
        "comma_line_count": 0,
        "path_separator_line_count": 0,
        "dot_line_count": 0,
        "plus_or_minus_line_count": 0,
        "other_punctuation_line_count": 0,
        "minimum_token_count": 0,
        "maximum_token_count": 0,
        "grammar_shape": "",
    }
    if not lines:
        return summary
    summary["minimum_token_count"] = min(item["token_count"] for item in lines)
    summary["maximum_token_count"] = max(item["token_count"] for item in lines)
    for item in lines:
        summary[item["candidate"] + "_count"] += 1
        if item["has_index_prefix"]:
            summary["index_prefix_line_count"] += 1
        if item["selected_marker"]:
            summary["selected_marker_line_count"] += 1
        if item["has_space_separator"]:
            summary["space_separator_line_count"] += 1
        if item["has_tab_separator"]:
            summary["tab_separator_line_count"] += 1
        if item["has_ascii_letter_or_underscore"]:
            summary["ascii_letter_or_underscore_line_count"] += 1
        if item["has_decimal_digit"]:
            summary["decimal_digit_line_count"] += 1
        if item["has_hex_address"]:
            summary["hex_address_line_count"] += 1
        if item["has_open_bracket"] or item["has_close_bracket"]:
            summary["bracketed_line_count"] += 1
        if item["has_open_parenthesis"] or item["has_close_parenthesis"]:
            summary["parenthesized_line_count"] += 1
        if item["has_colon"]:
            summary["colon_line_count"] += 1
        if item["has_comma"]:
            summary["comma_line_count"] += 1
        if item["has_path_separator"]:
            summary["path_separator_line_count"] += 1
        if item["has_dot"]:
            summary["dot_line_count"] += 1
        if item["has_plus_or_minus"]:
            summary["plus_or_minus_line_count"] += 1
        if item["has_other_punctuation"]:
            summary["other_punctuation_line_count"] += 1
    fixtures = sorted(set(item["grammar_shape"] for item in lines))
    if len(fixtures) == 1:
        summary["grammar_shape"] = fixtures[0]
    else:
        summary["grammar_shape"] = "multiple_safe_line_grammars"
    return summary


def _calls_line_grammar_signature(line):
    """Return a fixed-category signature for one opaque stack output line."""
    token_count = len([token for token in re.split(r"\s+", line.strip())
                       if token])
    source_frame = _STACK_LINE.match(line) is not None
    prefix = _CALLS_FRAME_PREFIX.match(line)
    address_only = _CALLS_ADDRESS_ONLY.match(line) is not None
    symbol_address = _CALLS_SYMBOL_ADDRESS.match(line) is not None
    function_comma_address = _CALLS_FUNCTION_COMMA_ADDRESS.match(line) is not None
    function_at_address = _CALLS_FUNCTION_AT_ADDRESS.match(line) is not None
    function_address = _CALLS_FUNCTION_ADDRESS.match(line) is not None
    if source_frame:
        candidate = "indexed_source_frame"
    elif address_only:
        candidate = "indexed_address_only_frame"
    elif function_comma_address:
        candidate = "indexed_function_comma_address_frame"
    elif function_at_address:
        candidate = "indexed_function_at_address_frame"
    elif function_address:
        candidate = "indexed_function_address_frame"
    elif symbol_address:
        candidate = "indexed_symbol_address_frame"
    elif prefix is not None:
        candidate = "indexed_unclassified_frame_payload"
    else:
        candidate = "unclassified"
    return {
        "candidate": candidate,
        "token_count": token_count,
        "has_index_prefix": prefix is not None,
        "selected_marker": (prefix is not None and prefix.group(2) == "_"),
        "has_space_separator": bool(re.search(r"[ ]", line)),
        "has_tab_separator": "\t" in line,
        "has_ascii_letter_or_underscore": bool(re.search(r"[A-Za-z_]", line)),
        "has_decimal_digit": bool(re.search(r"[0-9]", line)),
        "has_hex_address": _ADDRESS_PAYLOAD.search(line) is not None,
        "has_open_bracket": "[" in line,
        "has_close_bracket": "]" in line,
        "has_open_parenthesis": "(" in line,
        "has_close_parenthesis": ")" in line,
        "has_colon": ":" in line,
        "has_comma": "," in line,
        "has_path_separator": ("/" in line or "\\" in line),
        "has_dot": "." in line,
        "has_plus_or_minus": ("+" in line or "-" in line),
        "has_other_punctuation": bool(re.search(r"[^A-Za-z0-9_ \t\[\]\(\):,/.\\+\-]", line)),
        "grammar_shape": _calls_line_grammar_fixture(line, prefix),
    }


def _calls_line_grammar_fixture(line, prefix):
    """Return a target-text-free lexical fixture for an opaque ``calls`` row.

    The fixture keeps only frame selection and token classes.  It deliberately
    collapses every identifier, decimal run, hexadecimal address, whitespace
    run, and unknown punctuation run, so it cannot retain a symbol, address,
    path, PID, or target-specific spelling.  This is evidence for parser
    grammar discovery, never an input accepted by the production parser.
    """
    if prefix is None:
        return "unindexed_" + _calls_payload_fixture(line)
    marker = "selected" if prefix.group(2) == "_" else "unselected"
    return "frame_%s_%s" % (marker, _calls_payload_fixture(prefix.group(3)))


def _calls_payload_fixture(payload):
    tokens = []
    offset = 0
    while offset < len(payload):
        char = payload[offset]
        if char in " \t":
            while offset < len(payload) and payload[offset] in " \t":
                offset += 1
            tokens.append("whitespace")
            continue
        for expression, category in (
                (_CALLS_FIXTURE_HEX, "hex_address"),
                (_CALLS_FIXTURE_DECIMAL, "decimal"),
                (_CALLS_FIXTURE_IDENTIFIER, "identifier")):
            match = expression.match(payload, offset)
            if match is not None:
                tokens.append(category)
                offset = match.end()
                break
        else:
            punctuation = {
                "(": "open_paren", ")": "close_paren", ",": "comma",
                "[": "open_bracket", "]": "close_bracket", ":": "colon",
                "/": "slash", "\\": "backslash", ".": "dot",
                "+": "plus", "-": "minus",
            }
            category = punctuation.get(char, "other_punctuation")
            while (category == "other_punctuation" and offset < len(payload) and
                   payload[offset] not in " \t()[],/:\\.+-" and
                   _CALLS_FIXTURE_HEX.match(payload, offset) is None and
                   _CALLS_FIXTURE_DECIMAL.match(payload, offset) is None and
                   _CALLS_FIXTURE_IDENTIFIER.match(payload, offset) is None):
                offset += 1
            if category != "other_punctuation":
                offset += 1
            tokens.append(category)
    return "_".join(tokens) or "empty"


class ProgramComponentObservation(RedactedEvidence):
    """Keep component IDs, aliases and P rows in memory for aggregate joins."""
    def __init__(self, component_id, alias_programs, process_summary,
                 process_rows, process_command, halt_summary, halt_command):
        self.component_id = component_id
        self.alias_programs = alias_programs
        self.process_summary = process_summary
        self.process_rows = process_rows
        self.process_command = process_command
        self.halt_summary = halt_summary
        self.halt_command = halt_command

    def evidence_summary(self):
        return {
            "process_command": self.process_command,
            "halt_command": self.halt_command,
            "processes": self.process_summary,
            "halt": self.halt_summary,
        }


class StrictProgramTopology(RedactedEvidence):
    """In-memory configured-core to program-component routing relation."""
    def __init__(self, configured_programs, observations, summary):
        self.configured_programs = list(configured_programs)
        self.observations = list(observations)
        self.summary = dict(summary)
        self.component_by_core = {}
        for observation in observations:
            ordinal = observation.process_summary["selected_configured_core_ordinal"]
            self.component_by_core[ordinal] = observation.component_id

    def evidence_summary(self):
        return self.summary


class TopFrame(RedactedEvidence):
    """One source-bearing top stack frame, kept only for an in-memory command."""
    def __init__(self, source, line, selected):
        self.source = source
        self.line = line
        self.selected = selected

    def evidence_summary(self):
        return {
            "source_present": bool(self.source),
            "line_positive": self.line > 0,
            "selected": self.selected,
        }


class ComparableTopCall(RedactedEvidence):
    """One top call identity retained only for a before/after comparison."""
    def __init__(self, location_kind, identity, selected):
        self.location_kind = location_kind
        self.identity = identity
        self.selected = selected

    def evidence_summary(self):
        return {
            "location_kind": self.location_kind,
            "source_present": self.location_kind == "source",
            "selected": self.selected,
        }


class BreakpointRecord(object):
    """A B row retained only until the probe-owned breakpoint is deleted."""
    def __init__(self, handle, source, line, command):
        self.handle = handle
        self.source = source
        self.line = line
        self.command = command


class BreakpointMutation(RedactedEvidence):
    """Redacted result of one probe-owned breakpoint placement."""
    def __init__(self, record, requested_source, requested_line, token):
        self.record = record
        self.requested_source = requested_source
        self.requested_line = requested_line
        self.token = token

    def evidence_summary(self):
        return {
            "created_count": 1,
            "handle_present": self.record.handle >= 0,
            "command_token_matches": self.record.command == self.token,
            "source_matches_requested": (
                _normalized_program_name(self.record.source) ==
                _normalized_program_name(self.requested_source)
            ),
            "actual_line_positive": self.record.line > 0,
            "actual_line_matches_requested": self.record.line == self.requested_line,
        }


class BreakpointSnapshot(RedactedEvidence):
    """A fully parsed B reply whose individual rows never cross evidence."""
    def __init__(self, records):
        self.records = list(records)

    def evidence_summary(self):
        return {"breakpoint_count": len(self.records)}


def _program_component_groups(inventory):
    groups = {}
    for alias in inventory["program_aliases"]:
        groups.setdefault(alias["component_id"], set()).add(alias["program_name"])
    return [(component_id, sorted(programs))
            for component_id, programs in sorted(groups.items())]


def _accepted_command(session, command):
    result = run_command(session, command)
    if not result.get("accepted") or result.get("status") != 1:
        raise RuntimeError("MULTI rejected required routed command")
    if not isinstance(result.get("raw"), basestring):
        raise RuntimeError("MULTI routed command returned non-text output")
    return result


def _program_component_observation(session, component_id, component_ordinal,
                                   aliases, configured_programs):
    before = require_stopped_session(session)
    processes = _accepted_command(session, "route %s P" % component_id)
    process_summary = summarize_program_component_processes(
        component_ordinal, aliases, processes["raw"], configured_programs)
    process_rows = _parse_process_listing(processes["raw"])
    halt = _accepted_command(session, "route %s H" % component_id)
    halt_summary = summarize_halt_info(halt["raw"])
    after = require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("topology observation changed execution state")
    return ProgramComponentObservation(
        component_id, aliases, process_summary, process_rows,
        {"accepted": True, "status": 1}, halt_summary,
        {"accepted": True, "status": 1})


def discover_strict_program_topology(session, cfg):
    """Discover the only accepted configured-core routing relation.

    Component IDs and program names remain in memory.  The caller can record
    the returned object directly because its evidence form contains only the
    strict bijection summary.
    """
    before = require_stopped_session(session)
    components = _accepted_command(session, "components")
    inventory = component_route_inventory(components["raw"])
    configured_programs = configured_core_programs(cfg)
    observations = []
    for ordinal, (component_id, aliases) in enumerate(_program_component_groups(inventory)):
        observations.append(_program_component_observation(
            session, component_id, ordinal, aliases, configured_programs))
    after = require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("component discovery changed execution state")
    summary = summarize_strict_program_component_mapping(
        configured_programs, observations)
    if not summary["strict_program_component_mapping_confirmed"]:
        raise RuntimeError("strict configured program topology is not established")
    return StrictProgramTopology(configured_programs, observations, summary)


def revalidate_strict_program_topology(session, topology):
    """Fail closed when any routed program selection or stop state drifts."""
    if not isinstance(topology, StrictProgramTopology):
        raise ValueError("strict program topology is required")
    observations = []
    for core in sorted(topology.component_by_core):
        component_id = topology.component_by_core[core]
        observations.append(_program_component_observation(
            session, component_id, core, [topology.configured_programs[core]],
            topology.configured_programs))
    summary = summarize_strict_program_component_mapping(
        topology.configured_programs, observations)
    if not summary["strict_program_component_mapping_confirmed"]:
        raise RuntimeError("strict configured program topology drifted")
    return StrictProgramTopology(topology.configured_programs, observations, summary)


def strict_routed_result(session, topology, core, command):
    """Route one command only after the target component is revalidated."""
    topology = revalidate_strict_program_topology(session, topology)
    if core not in topology.component_by_core:
        raise ValueError("configured core is not routed")
    before = require_stopped_session(session)
    result = run_command(session, "route %s %s" % (
        topology.component_by_core[core], command))
    after = require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("routed command changed execution state")
    return result


def strict_routed_command(session, topology, core, command):
    """Run a strict routed command and reject non-success status."""
    result = strict_routed_result(session, topology, core, command)
    if not result.get("accepted") or result.get("status") != 1:
        raise RuntimeError("MULTI rejected required routed command")
    if not isinstance(result.get("raw"), basestring):
        raise RuntimeError("MULTI routed command returned non-text output")
    return result


_EXECUTION_DOMAIN_SYNTAX_CANDIDATES = (
    ("halt", "halt"),
    ("continue", "c"),
    ("step_in_source", "sl n"),
    ("next_source", "nl n"),
)


def execution_domain_syntax_candidates():
    """Return the documented command grammar candidates for one component.

    ``halt`` and ``c`` are the MULTI halt/continue commands. ``sl`` and
    ``nl`` are source-level step-into/step-over, and ``n`` requests their
    documented non-blocking form.  This list is deliberately fixed: a local
    configuration must not inject a command into the syntax-check probe.
    """
    return list(_EXECUTION_DOMAIN_SYNTAX_CANDIDATES)


def strict_execution_domain_syntax_check(session, topology, core, command_kind):
    """Syntax-check one routed control command without executing it.

    MULTI's documented ``sc`` command parses its quoted command rather than
    executing it.  Both strict topology and stopped state are revalidated
    immediately before the check, and stopped state is checked again after it.
    Component identifiers and command/output text remain local to this call.
    """
    candidates = dict(_EXECUTION_DOMAIN_SYNTAX_CANDIDATES)
    if command_kind not in candidates:
        raise ValueError("unknown execution-domain command kind")
    topology = revalidate_strict_program_topology(session, topology)
    if core not in topology.component_by_core:
        raise ValueError("configured core is not routed")
    before = require_stopped_session(session)
    command = 'sc "route %s %s"' % (
        topology.component_by_core[core], candidates[command_kind])
    result = run_command(session, command)
    after = require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("syntax check changed execution state")
    return {
        "command_kind": command_kind,
        "syntax_valid": (result.get("accepted") is True and
                         result.get("status") == 1),
        "accepted": result.get("accepted") is True,
        "status": result.get("status"),
        "stopped_state_preserved": True,
    }


def top_frame_from_calls(output):
    """Parse precisely the selected program's top source frame in memory."""
    if not inspection_output_shape("calls", output):
        raise ValueError("calls output is not production-shape parseable")
    lines = [line for line in output.splitlines() if line.strip()]
    if not lines:
        raise ValueError("calls output has no frames")
    match = _STACK_FRAME.match(lines[0])
    if match is None or int(match.group(1)) != 0:
        raise ValueError("calls output has no exact top frame")
    source = match.group(3)
    line = int(match.group(4))
    if (not source or not _SOURCE_LOCATOR.match(source) or line <= 0 or
            "\r" in source or "\n" in source):
        raise ValueError("top frame cannot be represented by verified breakpoint syntax")
    return TopFrame(source, line, match.group(2) == "_")


def comparable_top_call_from_calls(output):
    """Parse one source-or-address top call for a private equality comparison.

    This deliberately accepts the known unsourced call-stack shape. An address
    is retained only in memory and never emitted as evidence, so an unsourced
    core cannot be represented as though it had source debug data.
    """
    if not isinstance(output, basestring):
        raise ValueError("calls output must be text")
    if len(output) > (1 << 20):
        raise ValueError("calls output exceeds the bounded input size")
    lines = [line for line in output.splitlines() if line.strip()]
    if (not lines or len(lines) > 4096 or
            any(len(line) > (16 << 10) for line in lines)):
        raise ValueError("calls output has no bounded frames")
    top = lines[0]
    prefix = _CALLS_FRAME_PREFIX.match(top)
    if prefix is None or int(prefix.group(1)) != 0:
        raise ValueError("calls output has no exact top frame")
    selected = prefix.group(2) == "_"
    source = _STACK_FRAME.match(top)
    if source is not None:
        path = source.group(3)
        line = int(source.group(4))
        if (not path or not _SOURCE_LOCATOR.match(path) or line <= 0 or
                "\r" in path or "\n" in path):
            raise ValueError("source top frame is not safely comparable")
        return ComparableTopCall("source", (path, line), selected)
    signature = _calls_line_grammar_signature(top)
    if signature["candidate"] not in (
            "indexed_address_only_frame", "indexed_function_comma_address_frame",
            "indexed_function_at_address_frame", "indexed_function_address_frame",
            "indexed_symbol_address_frame"):
        raise ValueError("unsourced top frame has no documented comparable shape")
    addresses = _ADDRESS_PAYLOAD.findall(top)
    if len(addresses) != 1:
        raise ValueError("unsourced top frame has no unique address identity")
    return ComparableTopCall("address", addresses[0], selected)


def parse_breakpoint_listing(output):
    """Parse all B rows before any probe-owned handle is acted upon."""
    if not inspection_output_shape("B", output):
        raise ValueError("breakpoint listing is not production-shape parseable")
    if output.strip() == "No software breakpoints set.":
        return []
    records = []
    seen = set()
    for line in output.splitlines():
        if not line.strip():
            continue
        match = _BREAKPOINT_RECORD.match(line)
        if match is None:
            raise ValueError("breakpoint listing has malformed row")
        handle = int(match.group(1))
        location = _BREAKPOINT_LOCATION.match(match.group(2).strip())
        command = _BREAKPOINT_COMMAND.search(match.group(5))
        if handle < 0 or handle in seen or location is None:
            raise ValueError("breakpoint listing has ambiguous record")
        source = location.group(1).strip()
        line_number = int(location.group(2))
        if (not source or not _SOURCE_LOCATOR.match(source) or line_number <= 0):
            raise ValueError("breakpoint record has unsafe source identity")
        seen.add(handle)
        records.append(BreakpointRecord(
            handle, source, line_number,
            command.group(1) if command is not None else ""))
    return records


def summarize_breakpoint_listing(output):
    """Return only ownership-relevant counts from a complete B listing."""
    records = parse_breakpoint_listing(output)
    production_hint = re.compile(
        r'^mprintf\("HIT 0x[0-9A-F]{8}\\n"\)$')
    return {
        "breakpoint_count": len(records),
        "production_hint_token_count": sum(
            1 for record in records
            if production_hint.match(record.command) is not None),
    }


def identify_probe_breakpoint(before, after, source, token):
    """Accept exactly one new matching token and normalized source identity."""
    old_handles = set(record.handle for record in before)
    created = [record for record in after if record.handle not in old_handles]
    matches = [record for record in created
               if record.command == token and
               _normalized_program_name(record.source) == _normalized_program_name(source)]
    if len(created) != 1 or len(matches) != 1:
        raise RuntimeError("probe-owned breakpoint is ambiguous")
    return matches[0]


def summarize_program_alias_process_mapping(configured_programs, observations):
    """Aggregate exact program-path relations across all routed components."""
    if not isinstance(configured_programs, list) or not configured_programs:
        raise ValueError("configured core programs are required")
    if not isinstance(observations, list):
        raise ValueError("program component observations are required")
    p_programs = set()
    aliases = set()
    for observation in observations:
        if not isinstance(observation, ProgramComponentObservation):
            raise ValueError("program component observation is invalid")
        aliases.update(observation.alias_programs)
        p_programs.update(row["program_name"] for row in observation.process_rows
                          if row["program_name"])
    configured = set(configured_programs)
    exact_alias = aliases.intersection(p_programs)
    exact_configured = configured.intersection(p_programs)
    return {
        "configured_core_count": len(configured),
        "program_component_observation_count": len(observations),
        "program_alias_count": len(aliases),
        "p_program_count": len(p_programs),
        "configured_elf_exact_p_program_match_count": len(exact_configured),
        "configured_elf_without_exact_p_program_count": len(configured - p_programs),
        "program_alias_exact_p_program_match_count": len(exact_alias),
        "program_alias_without_exact_p_program_count": len(aliases - p_programs),
        "diagnostic_program_alias_basename_p_program_match_count": sum(
            1 for alias in aliases
            if any(ntpath.basename(alias) == ntpath.basename(program)
                   for program in p_programs)),
        "diagnostic_program_alias_suffix_p_program_match_count": sum(
            1 for alias in aliases
            if any(alias.endswith(program) or program.endswith(alias)
                   for program in p_programs)),
    }


def summarize_program_component_routing(configured_count, mapping, results):
    """Confirm each unique program component selects one stopped process."""
    if isinstance(configured_count, bool) or not isinstance(configured_count, (int, long)):
        raise ValueError("configured core count must be numeric")
    if not isinstance(mapping, dict) or not isinstance(results, list):
        raise ValueError("program component routing inputs are invalid")
    mapping_summary = mapping.get("summary")
    if not isinstance(mapping_summary, dict):
        raise ValueError("program component mapping summary is required")
    selected_stopped = 0
    halt_observed = 0
    for result in results:
        if not isinstance(result, dict):
            raise ValueError("program component routing result is invalid")
        process = result.get("processes", {})
        halt = result.get("halt", {})
        if (process.get("selected_row_count") == 1 and
                process.get("selected_status") == "stopped"):
            selected_stopped += 1
        if halt.get("cause") in _HALT_CAUSES.values():
            halt_observed += 1
    return {
        "configured_core_count": configured_count,
        "uniquely_mapped_configured_core_count": mapping_summary.get(
            "configured_cores_with_unique_program_component_count", 0),
        "routed_configured_core_count": len(results),
        "selected_stopped_configured_core_count": selected_stopped,
        "halt_observed_configured_core_count": halt_observed,
        "strict_program_component_routing_confirmed": (
            mapping_summary.get("all_configured_cores_have_unique_program_component") is True and
            len(results) == configured_count and
            selected_stopped == configured_count and
            halt_observed == configured_count
        ),
    }


def summarize_halt_info(output):
    """Reduce verified H text to its cause without retaining command-list text."""
    if not isinstance(output, basestring):
        raise ValueError("halt output must be text")
    cause = None
    has_command_list = False
    line_count = 0
    for line in output.splitlines():
        line = line.strip()
        if not line:
            continue
        line_count += 1
        if line in _HALT_CAUSES:
            if cause is not None:
                raise ValueError("halt output has multiple causes")
            cause = _HALT_CAUSES[line]
        elif line.startswith("Command list was:"):
            if has_command_list:
                raise ValueError("halt output has multiple command lists")
            token = line[len("Command list was:"):].strip()
            if len(token) < 2 or token[0] != "{" or token[-1] != "}":
                raise ValueError("halt output has malformed command list")
            has_command_list = True
        else:
            raise ValueError("halt output has unknown line")
    if cause is None:
        raise ValueError("halt output has no cause")
    return {
        "cause": cause,
        "command_list_present": has_command_list,
        "line_count": line_count,
    }
