# Python 2.7 - M0 recon probe: environment sanity check.
# Runs under MULTI-Python (Python 2.7) via mpythonrun. Confirms which interpreter,
# which builtins, and which import surface reconlib's explicit session APIs can
# rely on.
# No target contact.
#
# Run: python recon/run.py p00_env

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib

rec = reconlib.Recorder("p00_env")
try:
    rec.note("executable", sys.executable)
    rec.note("argv", sys.argv)
    rec.note("cwd", os.getcwd())
    rec.note("sys_path", sys.path)
    rec.note("builtin_names",
              sorted(dir(__builtins__)) if not isinstance(__builtins__, dict)
              else sorted(__builtins__.keys()))
    rec.note("modules", sorted(sys.modules.keys()))

    # Which GHS symbols are reachable without an explicit import?
    ghs_globals = {}
    for name in ("GHS_Debugger", "GHS", "ghs", "MULTI", "Debugger"):
        ghs_globals[name] = name in globals() or name in dir(__builtins__)
    rec.note("ghs_globals", ghs_globals)

    # Try importing the documented entry points.
    for modname in ("ghs", "ghs_debugger", "ghs_debugger_api", "MULTI", "multi"):
        def do_import(modname=modname):
            m = __import__(modname)
            return {"file": getattr(m, "__file__", None),
                    "attrs": sorted(a for a in dir(m) if not a.startswith("_"))[:200]}
        rec.step("import_" + modname, do_import)

    # Threading and socket availability decide whether a resident socket loop
    # inside the MULTI interpreter is possible at all (M0-2).
    rec.step("import_threading", lambda: __import__("threading").activeCount())
    rec.step("import_socket", lambda: hasattr(__import__("socket"), "AF_INET"))
except Exception:
    rec.fatal()
finally:
    rec.finish()
