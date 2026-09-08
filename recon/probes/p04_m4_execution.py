# Python 2.7. M4 capability and transition probe: step-out and run-to.
#
# Phase one is always read-only API introspection.  Phase two is disabled by
# default and accepts only an operator-approved method name that phase one has
# just discovered.  It intentionally never invents a RunCommands spelling.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


ACKNOWLEDGEMENT = "I_UNDERSTAND_TARGET_EXECUTION_CHANGES_STATE"
TERMS = ("step", "next", "run", "finish", "until")
STEP_OUT_NAMES = ("stepout", "stepoutof", "finish")
RUN_TO_NAMES = ("runto", "rununtil", "until")


def has_named_candidate(inventory, names):
    return any(name.lower().replace("_", "") in names for name in inventory)


def approved_section(cfg):
    section = cfg.get("m4_execution_probe")
    if not isinstance(section, dict):
        return None
    if not section.get("enabled"):
        return None
    if section.get("acknowledgement") != ACKNOWLEDGEMENT:
        raise RuntimeError("M4 execution acknowledgement is missing")
    return section


def run_operation(session, inventory, spec):
    """Call once, always restore a stopped execution state, then re-check it."""
    before = reconlib.require_stopped_session(session)
    outcome = {"before": before, "operation_ok": False}
    try:
        method = spec.get("method") if isinstance(spec, dict) else None
        if method not in inventory:
            raise RuntimeError("configured method was not discovered by introspection")
        outcome["call"] = reconlib.configured_api_call(session, spec)
        outcome["after_call"] = reconlib.safe_state_snapshot(session)
        outcome["operation_ok"] = True
    except Exception as exc:
        outcome["call_error_type"] = type(exc).__name__
    finally:
        try:
            outcome["recovery"] = reconlib.recover_stopped_session(session)
        except Exception as exc:
            outcome["recovery_error_type"] = type(exc).__name__
            outcome["operation_ok"] = False
    try:
        outcome["after"] = reconlib.require_stopped_session(session)
    except Exception as exc:
        outcome["after_error_type"] = type(exc).__name__
        outcome["operation_ok"] = False
    return outcome


rec = reconlib.Recorder("p04_m4_execution")
session = None
try:
    # Do not record config.local.json: it contains connection and target data.
    cfg = reconlib.load_config()
    rec.note("config_loaded", True)
    session = rec.step("bind_existing_program_window",
                       lambda: reconlib.bind_existing_program_window(cfg))
    if session is None:
        raise RuntimeError("no uniquely verified existing program window")

    inventory = rec.step("read_only_execution_api_inventory",
                         lambda: reconlib.callable_inventory(session, TERMS))
    if inventory is None:
        raise RuntimeError("could not inspect execution API")
    rec.note("step_out_api_candidate_discovered",
             has_named_candidate(inventory, STEP_OUT_NAMES))
    rec.note("run_to_api_candidate_discovered",
             has_named_candidate(inventory, RUN_TO_NAMES))

    section = approved_section(cfg)
    if section is None:
        rec.note("execution_phase", "disabled_by_local_configuration")
    else:
        for label in ("step_out", "run_to"):
            operation = rec.step(label + "_single_call_with_recovery",
                                 lambda label=label: run_operation(
                                     session, inventory, section.get(label)))
            if operation is None or not operation.get("operation_ok"):
                raise RuntimeError(label + " operation or recovery failed")
except Exception:
    rec.fatal()
finally:
    # Only the armed execution phase can have made the target run.  Preserve
    # stopped state even if a Recorder step failed before its own recovery.
    if session is not None:
        try:
            section = approved_section(cfg) if "cfg" in globals() else None
            if section is not None:
                rec.step("final_execution_recovery",
                         lambda: reconlib.recover_stopped_session(session))
        except Exception:
            rec.fatal()
        reconlib.close_session(session)
    rec.finish()
