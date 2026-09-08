# Python 2.7. Read-only inspection of the object bound by an existing service router.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


rec = reconlib.Recorder("p00c_existing")
try:
    dbg = rec.step("construct", reconlib._new_debugger)
    rec.step("debugger_type", lambda: type(dbg).__name__)
    bound = rec.step("bound_object", lambda: getattr(dbg, "cmdExecObj", None))
    rec.step("bound_type", lambda: type(bound).__name__ if bound is not None else None)

    def snapshot(target):
        info = target.GetCurPrInfo("")
        return {
            "status": target.GetStatus(),
            "has_process_info": bool(info),
            "pid": info.get("pid") if isinstance(info, dict) else None,
            "file_present": bool(info.get("file")) if isinstance(info, dict) else False,
            "function_present": bool(info.get("proc")) if isinstance(info, dict) else False,
        }

    rec.step("debugger_snapshot", lambda: snapshot(dbg))
    if bound is not None:
        rec.step("bound_snapshot", lambda: snapshot(bound))
except Exception:
    rec.fatal()
finally:
    rec.finish()
