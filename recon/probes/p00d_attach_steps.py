# Python 2.7. Checkpoint each call in the warm attach sequence.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


rec = reconlib.Recorder("p00d_attach_steps")
try:
    cfg = rec.step("load_config", reconlib.load_config)
    dbg = rec.step("construct", reconlib._new_debugger)
    win = rec.step("DebugProgram", lambda: dbg.DebugProgram(
        cfg["multicore_project"], 0, 1, 0, 1))
    rec.step("window_type", lambda: type(win).__name__ if win is not None else None)
    rec.step("ConnectToTarget", lambda: dbg.ConnectToTarget(
        cfg["connection_args"], "", "", "", 1, "", 0))
    rec.step("window_status", lambda: win.GetStatus())
except Exception:
    rec.fatal()
finally:
    rec.finish()
