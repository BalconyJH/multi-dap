# Python 2.7. M5 capability probe: memory, registers, and disassembly.
#
# No debugger command is guessed.  Memory and disassembly use only a configured
# method observed in the read-only inventory.  Register listing uses the M0-4
# verified `l r` command and records its shape, never its target-specific text.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


ACKNOWLEDGEMENT = "I_UNDERSTAND_TARGET_READS_REQUIRE_A_STOPPED_SESSION"
TERMS = ("memory", "read", "register", "disass", "asm")


def approved_section(cfg):
    section = cfg.get("m5_read_probe")
    if not isinstance(section, dict):
        return None
    if not section.get("enabled"):
        return None
    if section.get("acknowledgement") != ACKNOWLEDGEMENT:
        raise RuntimeError("M5 read acknowledgement is missing")
    return section


def api_read(session, inventory, spec):
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
        # An incorrectly classified API must not strand the board running.
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


def register_shape(session):
    before = reconlib.require_stopped_session(session)
    result = reconlib.run_command(session, "l r")
    raw = result.get("raw") or ""
    after = reconlib.require_stopped_session(session)
    if not result.get("accepted") or result.get("status") != 1:
        raise RuntimeError("MULTI rejected the verified register-list command")
    if before["status"] != after["status"]:
        raise RuntimeError("register-list command changed execution state")
    return {
        "accepted": True,
        "status": result.get("status"),
        "raw_present": bool(raw),
        "raw_line_count": len(raw.splitlines()),
        "before": before,
        "after": after,
    }


rec = reconlib.Recorder("p05_m5_inspection")
session = None
try:
    cfg = reconlib.load_config()
    rec.note("config_loaded", True)
    session = rec.step("bind_existing_program_window",
                       lambda: reconlib.bind_existing_program_window(cfg))
    if session is None:
        raise RuntimeError("no uniquely verified existing program window")

    inventory = rec.step("read_only_m5_api_inventory",
                         lambda: reconlib.callable_inventory(session, TERMS))
    if inventory is None:
        raise RuntimeError("could not inspect M5 API")
    rec.note("disassembly_api_candidate_discovered", any(
        "disass" in name.lower() or "asm" in name.lower() for name in inventory))

    section = approved_section(cfg)
    if section is None:
        rec.note("read_phase", "disabled_by_local_configuration")
    else:
        memory = rec.step("memory_single_call_with_recovery",
                          lambda: api_read(session, inventory,
                                           section.get("memory")))
        if memory is None or not memory.get("operation_ok"):
            raise RuntimeError("memory read or recovery failed")
        registers = rec.step("register_list_shape", lambda: register_shape(session))
        if registers is None:
            raise RuntimeError("register list failed")
        disassembly = rec.step("disassembly_single_call_with_recovery",
                               lambda: api_read(session, inventory,
                                                section.get("disassembly")))
        if disassembly is None or not disassembly.get("operation_ok"):
            raise RuntimeError("disassembly read or recovery failed")
except Exception:
    rec.fatal()
finally:
    if session is not None:
        try:
            section = approved_section(cfg) if "cfg" in globals() else None
            if section is not None:
                rec.step("final_read_recovery",
                         lambda: reconlib.recover_stopped_session(session))
        except Exception:
            rec.fatal()
        reconlib.close_session(session)
    rec.finish()
