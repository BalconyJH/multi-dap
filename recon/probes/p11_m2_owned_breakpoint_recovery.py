# Python 2.7. Recover exact multi-dap breakpoint hint tokens from a warm target.
#
# This probe is disabled by default. It never creates a breakpoint and never
# opens, reconnects, disconnects, resumes, halts, downloads, or programs a
# target. When explicitly armed by the operator who has exclusive stopped
# target control, it deletes only B rows with the exact multi-dap mprintf hint
# token shape. Every routed read and deletion revalidates the frozen configured
# program topology and target stopped state before and after the command.

import os
import re
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


ACKNOWLEDGEMENT = "I_UNDERSTAND_THIS_PROBE_DELETES_ONLY_EXACT_MULTI_DAP_BREAKPOINT_TOKENS"
EXCLUSIVE_CONTROL_ACKNOWLEDGEMENT = "I_CONFIRM_EXCLUSIVE_STOPPED_TARGET_CONTROL"
PRODUCTION_HINT_TOKEN = re.compile(r'^mprintf\("HIT 0x[0-9A-F]{8}\\n"\)$')
LEGACY_EXPECTED_TOKEN_COUNT = 1


def approved_section(cfg):
    section = cfg.get("m2_owned_breakpoint_recovery_probe")
    if not isinstance(section, dict) or not section.get("enabled"):
        return None
    if section.get("acknowledgement") != ACKNOWLEDGEMENT:
        raise RuntimeError("M2 owned-breakpoint recovery acknowledgement is missing")
    if (section.get("exclusive_control_acknowledgement") !=
            EXCLUSIVE_CONTROL_ACKNOWLEDGEMENT):
        raise RuntimeError("M2 owned-breakpoint recovery exclusive-control acknowledgement is missing")
    return section


def explicitly_supplied_router_port():
    args = sys.argv[1:]
    if len(args) != 2 or args[0] != "--live-router-port":
        raise RuntimeError("armed probe requires one explicit live router port")
    try:
        port = int(args[1])
    except (TypeError, ValueError):
        raise RuntimeError("armed probe requires a numeric live router port")
    if port < 1 or port > 65535:
        raise RuntimeError("armed probe router port is outside the valid range")
    return port


def exact_production_records(records):
    """Retain only exact mprintf hint-token rows in memory for deletion."""
    return [record for record in records
            if PRODUCTION_HINT_TOKEN.match(record.command) is not None]


def list_breakpoints(session, topology, core):
    result = reconlib.strict_routed_command(session, topology, core, "B")
    return reconlib.BreakpointSnapshot(reconlib.parse_breakpoint_listing(result["raw"]))


def delete_one_exact_token(session, topology, core, token):
    """Re-list, delete one exact token, and re-list before returning."""
    before = list_breakpoints(session, topology, core)
    matches = [record for record in before.records if record.command == token]
    if len(matches) != 1:
        raise RuntimeError("owned breakpoint is not uniquely present before deletion")
    record = matches[0]
    result = reconlib.strict_routed_result(
        session, topology, core, "d %%%d" % record.handle)
    if not result.get("accepted") or result.get("status") != 1:
        raise RuntimeError("MULTI rejected exact owned-breakpoint deletion")
    after = list_breakpoints(session, topology, core)
    if any(item.command == token for item in after.records):
        raise RuntimeError("owned breakpoint remains after deletion")
    reconlib.revalidate_strict_program_topology(session, topology)
    reconlib.require_stopped_session(session)


def recover_journal_token(session, topology, journal):
    core = journal["core"]
    token = journal["token"]
    if core not in topology.component_by_core:
        raise RuntimeError("recovery journal core is not configured")
    initial = list_breakpoints(session, topology, core)
    if len([record for record in initial.records if record.command == token]) != 1:
        raise RuntimeError("recovery journal token is not uniquely present")
    delete_one_exact_token(session, topology, core, token)
    final = list_breakpoints(session, topology, core)
    if any(record.command == token for record in final.records):
        raise RuntimeError("recovery journal token remains after deletion")
    reconlib.clear_m2_breakpoint_recovery_journal(core, token)
    return {
        "recovery_mode": "journal",
        "expected_core_initial_exact_token_count": 1,
        "expected_core_deleted_exact_token_count": 1,
        "expected_core_remaining_exact_token_count": 0,
        "cleanup_confirmed": True,
        "stopped_state_confirmed": True,
        "topology_revalidated": True,
    }


def legacy_recovery_is_explicit(section):
    core = section.get("expected_configured_core_ordinal")
    count = section.get("expected_exact_token_count")
    return (section.get("legacy_production_token_recovery") is True and
            isinstance(core, int) and not isinstance(core, bool) and core >= 0 and
            isinstance(count, int) and not isinstance(count, bool) and
            count == LEGACY_EXPECTED_TOKEN_COUNT)


def recover_legacy_token(session, topology, section):
    expected_core = section["expected_configured_core_ordinal"]
    expected_count = section["expected_exact_token_count"]
    if expected_core not in topology.component_by_core:
        raise RuntimeError("legacy recovery core is not configured")
    initial = exact_production_records(
        list_breakpoints(session, topology, expected_core).records)
    if len(initial) != expected_count:
        raise RuntimeError("legacy recovery expected core/count does not match")
    token = initial[0].command
    delete_one_exact_token(session, topology, expected_core, token)
    final = exact_production_records(
        list_breakpoints(session, topology, expected_core).records)
    if final:
        raise RuntimeError("legacy production token remains after deletion")
    return {
        "recovery_mode": "legacy",
        "expected_core_initial_exact_token_count": expected_count,
        "expected_core_deleted_exact_token_count": expected_count,
        "expected_core_remaining_exact_token_count": 0,
        "cleanup_confirmed": True,
        "stopped_state_confirmed": True,
        "topology_revalidated": True,
    }


def main():
    rec = reconlib.Recorder("p11_m2_owned_breakpoint_recovery")
    session = None
    try:
        cfg = reconlib.load_config()
        rec.note("config_loaded", True)
        section = approved_section(cfg)
        if section is None:
            rec.note("recovery_phase", "disabled_by_local_configuration")
            return

        explicitly_supplied_router_port()
        session = rec.step("bind_existing_program_window",
                           lambda: reconlib.bind_existing_program_window(cfg))
        if session is None:
            raise RuntimeError("no uniquely verified existing program window")
        topology = rec.step("strict_program_component_topology",
                            lambda: reconlib.discover_strict_program_topology(session, cfg))
        if topology is None:
            raise RuntimeError("strict program-component topology could not be established")
        journal = reconlib.read_m2_breakpoint_recovery_journal()
        if journal is not None:
            recovery = lambda: recover_journal_token(session, topology, journal)
        elif legacy_recovery_is_explicit(section):
            recovery = lambda: recover_legacy_token(session, topology, section)
        else:
            raise RuntimeError("recovery requires an owned journal or explicit legacy core/count")
        outcome = rec.step("recover_exact_owned_breakpoint_tokens", recovery)
        if outcome is None or not outcome.get("cleanup_confirmed"):
            raise RuntimeError("exact owned breakpoint cleanup was not confirmed")
    except Exception:
        rec.fatal()
    finally:
        if session is not None:
            try:
                reconlib.require_stopped_session(session)
            except Exception:
                rec.fatal()
            reconlib.close_session(session)
        rec.finish()


if __name__ == "__main__":
    main()
