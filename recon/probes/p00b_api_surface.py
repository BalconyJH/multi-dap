# Python 2.7 - M0 recon probe: MULTI-Python API surface, no target contact.
# Introspects GHS_Debugger and everything reachable from it without connecting,
# so the method table of docs/architecture.md 7.3 can be grounded in real API.
#
# Run: python recon/run.py p00b_api_surface

import os
import sys
import inspect

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib

MAX_DEPTH = 2


def describe_callable(obj):
    info = {"kind": "callable", "doc": (inspect.getdoc(obj) or "")[:4000]}
    try:
        argspec = inspect.getargspec(obj)
        info["args"] = list(argspec.args)
        info["varargs"] = argspec.varargs
        info["keywords"] = argspec.keywords
        info["defaults"] = repr(argspec.defaults)
    except Exception as exc:
        info["args_error"] = "%s: %s" % (type(exc).__name__, exc)
    return info


def describe(obj, depth, seen):
    oid = id(obj)
    if oid in seen:
        return {"kind": "cycle"}
    seen = seen | set([oid])

    entry = {"repr": repr(obj)[:300], "type": type(obj).__name__}
    if inspect.isclass(obj):
        entry["kind"] = "class"
        entry["doc"] = (inspect.getdoc(obj) or "")[:4000]
        entry["mro"] = [c.__name__ for c in inspect.getmro(obj)] if hasattr(obj, "__mro__") else None
    elif callable(obj):
        entry.update(describe_callable(obj))
    else:
        entry["kind"] = "value"

    if depth <= 0:
        return entry

    members = {}
    for name in sorted(dir(obj)):
        if name.startswith("__"):
            continue
        try:
            attr = getattr(obj, name)
        except Exception as exc:
            members[name] = {"kind": "error", "error": "%s: %s" % (type(exc).__name__, exc)}
            continue
        if inspect.isclass(attr) or callable(attr):
            members[name] = describe(attr, depth - 1, seen)
        else:
            members[name] = {"kind": "value", "type": type(attr).__name__, "repr": repr(attr)[:200]}
    entry["members"] = members
    return entry


rec = reconlib.Recorder("p00b_api_surface")
try:
    # GHS_Debugger is a builtin in this interpreter (see p00_env evidence).
    rec.step("describe_GHS_Debugger", lambda: describe(GHS_Debugger, MAX_DEPTH, set()))

    # The debugger factory is documented as GHS_Debugger() -> debugger object.
    # Instantiate it WITHOUT connecting to anything and inspect what it offers.
    rec.step("describe_instance", lambda: describe(GHS_Debugger(), MAX_DEPTH, set()))

    # Everything else the interpreter exposes at module scope.
    for modname in ("ghs_debugger", "ghs_debugger_api", "ghs_constants",
                     "ghs_target_constants", "ide", "service"):
        def do_describe(modname=modname):
            return describe(__import__(modname), 1, set())
        rec.step("describe_module_" + modname, do_describe)
except Exception:
    rec.fatal()
finally:
    rec.finish()
