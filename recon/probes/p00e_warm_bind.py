# Python 2.7. Read-only verification of warm program-window binding.
#
# This probe deliberately does not call DebugProgram, ConnectToTarget,
# Disconnect, or any target-mutating debugger command.  Each Recorder step is
# flushed before and after execution, so a failed bind still leaves evidence.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


rec = reconlib.Recorder("p00e_warm_bind")
session = None
try:
    cfg = rec.step("load_config", reconlib.load_config)
    if cfg is None:
        raise RuntimeError("configuration could not be loaded")
    summary = rec.step("warm_binding_summary",
                       lambda: reconlib.warm_binding_summary(cfg))
    if summary is None:
        raise RuntimeError("warm binding summary could not be collected")
    session = rec.step("bind_existing_program_window",
                       lambda: reconlib.bind_existing_program_window(cfg))
    if session is None:
        raise RuntimeError("no uniquely verified existing program window")

    def snapshot():
        info = session.GetCurPrInfo("")
        status = session.GetStatus()
        return {
            "origin": session.origin,
            "window_type": type(session.window).__name__,
            "status": status,
            "has_process_info": bool(info),
            "has_pid": bool(info.get("pid")) if isinstance(info, dict) else False,
        }

    state = rec.step("verified_window_snapshot", snapshot)
    if state is None or not state.get("has_process_info"):
        raise RuntimeError("bound window did not retain process information")
except Exception:
    rec.fatal()
finally:
    if session is not None:
        reconlib.close_session(session)
    rec.finish()
