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
import time
import traceback

RECON_DIR = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT_DIR = os.path.join(RECON_DIR, "out")


def _safe(value, depth=0):
    """Convert an arbitrary MULTI-Python value into something json can encode."""
    if depth > 6:
        return repr(value)[:500]
    if value is None or isinstance(value, (bool, int, long, float)):
        return value
    if isinstance(value, str):
        return value.decode("utf-8", "replace")
    if isinstance(value, unicode):
        return value
    if isinstance(value, (list, tuple)):
        return [_safe(v, depth + 1) for v in value]
    if isinstance(value, dict):
        return dict((unicode(k), _safe(v, depth + 1)) for k, v in value.items())
    return repr(value)[:500]


def load_config():
    path = os.path.join(RECON_DIR, "config.local.json")
    f = open(path, "rb")
    try:
        return json.loads(f.read().decode("utf-8"))
    finally:
        f.close()


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

    def step(self, label, fn, **meta):
        """Time fn(), record its return value or its exception, and return the value.

        Never raises: a failed step is evidence, not a crash.
        """
        entry = {"label": label, "meta": _safe(meta)}
        t0 = time.time()
        try:
            value = fn()
            entry["ok"] = True
            entry["result"] = _safe(value)
        except Exception:
            value = None
            entry["ok"] = False
            entry["error"] = traceback.format_exc()
        entry["seconds"] = time.time() - t0
        self.data["steps"].append(entry)
        return value

    def fatal(self):
        self.data["fatal"] = traceback.format_exc()

    def finish(self):
        self.data["total_seconds"] = time.time() - self.started
        if not os.path.isdir(OUT_DIR):
            os.makedirs(OUT_DIR)
        path = os.path.join(OUT_DIR, self.name + ".json")
        tmp = path + ".tmp"
        f = open(tmp, "wb")
        try:
            f.write(json.dumps(self.data, indent=2, sort_keys=True,
                               default=repr).encode("utf-8"))
        finally:
            f.close()
        if os.path.exists(path):
            os.remove(path)
        os.rename(tmp, path)
        return path


def open_session(cfg, load_program=True):
    """Bring up a debugger object connected to the configured target.

    Returns the GHS_Debugger instance. Caller is responsible for close_session.
    """
    dbg = GHS_Debugger()                       # builtin; see probe00 evidence
    if load_program:
        dbg.DebugProgram(cfg["multicore_project"], 0, 1, 0, 1)
    dbg.ConnectToTarget(cfg["connection_args"], "", "", "", 0, "", 0)
    return dbg


def close_session(dbg):
    try:
        dbg.Disconnect(0)
    except Exception:
        pass
