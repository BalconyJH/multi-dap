# Python 2.7. M2 program-component paired breakpoint capability probe.
#
# This is warm-only and disabled by default.  It never opens, reconnects, or
# disconnects a MULTI debugger window.  The only mutating operation is an
# operator-acknowledged `b`, followed immediately by an exact owned `d` and B
# confirmation while the target stays stopped.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


ACKNOWLEDGEMENT = "I_UNDERSTAND_THIS_PROBE_CREATES_AND_DELETES_A_BREAKPOINT"
EXCLUSIVE_CONTROL_ACKNOWLEDGEMENT = "I_CONFIRM_EXCLUSIVE_STOPPED_TARGET_CONTROL"


def approved_section(cfg):
    section = cfg.get("m2_program_breakpoint_probe")
    if not isinstance(section, dict) or not section.get("enabled"):
        return None
    if section.get("acknowledgement") != ACKNOWLEDGEMENT:
        raise RuntimeError("M2 program-breakpoint acknowledgement is missing")
    if (section.get("exclusive_control_acknowledgement") !=
            EXCLUSIVE_CONTROL_ACKNOWLEDGEMENT):
        raise RuntimeError("M2 program-breakpoint exclusive-control acknowledgement is missing")
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


def fresh_token():
    for unused in range(16):
        try:
            value = int(os.urandom(4).encode("hex"), 16)
        except Exception:
            raise RuntimeError("could not create a unique probe breakpoint token")
        if value:
            return 'mprintf("HIT 0x%08X\\n")' % value
    raise RuntimeError("could not create a non-zero probe breakpoint token")


def source_command(frame, token):
    return "b %s#%d {%s}" % (frame.source, frame.line, token)


def list_breakpoints(session, topology, core):
    result = reconlib.strict_routed_command(session, topology, core, "B")
    return reconlib.BreakpointSnapshot(reconlib.parse_breakpoint_listing(result["raw"]))


def calls_candidate(session, topology, core):
    result = reconlib.strict_routed_result(session, topology, core, "calls")
    outcome = {
        "accepted": result.get("accepted") is True,
        "status": result.get("status"),
        "production_shape_parseable": False,
        "top_frame_present": False,
    }
    raw = result.get("raw")
    if not outcome["accepted"] or outcome["status"] != 1:
        outcome["semantic_flags"] = reconlib.inspection_semantic_flags(raw)
        return outcome, None
    try:
        frame = reconlib.top_frame_from_calls(raw)
    except Exception:
        outcome["semantic_flags"] = reconlib.inspection_semantic_flags(raw)
        return outcome, None
    outcome["production_shape_parseable"] = True
    outcome["top_frame_present"] = True
    outcome["top_frame"] = frame
    return outcome, frame


def place_owned_breakpoint(session, topology, core, frame, token):
    before = list_breakpoints(session, topology, core)
    if any(record.command == token for record in before.records):
        raise RuntimeError("probe breakpoint token is already present")
    reconlib.write_m2_breakpoint_recovery_journal(core, token)
    result = reconlib.strict_routed_result(
        session, topology, core, source_command(frame, token))
    if not result.get("accepted") or result.get("status") != 1:
        raise RuntimeError("MULTI rejected program-component breakpoint command")
    after = list_breakpoints(session, topology, core)
    owned = reconlib.identify_probe_breakpoint(
        before.records, after.records, frame.source, token)
    return reconlib.BreakpointMutation(owned, frame.source, frame.line, token)


def delete_owned_breakpoint(session, topology, core, mutation):
    before = list_breakpoints(session, topology, core)
    matches = [record for record in before.records
               if record.command == mutation.token]
    if (len(matches) != 1 or
            matches[0].handle != mutation.record.handle):
        raise RuntimeError("probe-owned breakpoint is not uniquely present before deletion")
    result = reconlib.strict_routed_result(
        session, topology, core, "d %%%d" % mutation.record.handle)
    if not result.get("accepted") or result.get("status") != 1:
        raise RuntimeError("MULTI rejected probe-owned breakpoint deletion")
    after = list_breakpoints(session, topology, core)
    if (any(record.handle == mutation.record.handle for record in after.records) or
            any(record.command == mutation.token for record in after.records)):
        raise RuntimeError("probe-owned breakpoint remains after deletion")
    reconlib.revalidate_strict_program_topology(session, topology)
    return {
        "accepted": True,
        "status": result.get("status"),
        "cleanup_confirmed": True,
    }


def recover_token_owned_breakpoints(session, topology, core, token):
    """Delete every still-listed row bearing this invocation's exact token.

    This recovery runs even when placement failed after MULTI accepted `b` but
    before a BreakpointMutation object could be constructed.  A missing or
    malformed B response is not treated as clean because it cannot prove that
    an owned breakpoint is absent.
    """
    owned_count = 0
    while True:
        before = list_breakpoints(session, topology, core)
        owned = [record for record in before.records if record.command == token]
        if not owned:
            break
        record = owned[0]
        result = reconlib.strict_routed_result(
            session, topology, core, "d %%%d" % record.handle)
        if not result.get("accepted") or result.get("status") != 1:
            raise RuntimeError("MULTI rejected token-owned breakpoint deletion")
        owned_count += 1
        after = list_breakpoints(session, topology, core)
        if any(item.handle == record.handle and item.command == token
               for item in after.records):
            raise RuntimeError("token-owned breakpoint remains after deletion")
        if not any(item.command == token for item in after.records):
            break
    reconlib.revalidate_strict_program_topology(session, topology)
    reconlib.clear_m2_breakpoint_recovery_journal(core, token)
    return {
        "owned_record_count": owned_count,
        "cleanup_confirmed": True,
    }


def paired_breakpoint(session, topology, core, frame):
    token = fresh_token()
    outcome = None
    try:
        mutation = place_owned_breakpoint(session, topology, core, frame, token)
        cleanup = delete_owned_breakpoint(session, topology, core, mutation)
        outcome = {"placement": mutation, "cleanup": cleanup}
    finally:
        recovery = recover_token_owned_breakpoints(session, topology, core, token)
    if outcome is None or not recovery.get("cleanup_confirmed"):
        raise RuntimeError("paired breakpoint cleanup was not confirmed")
    outcome["recovery"] = recovery
    outcome["cleanup_confirmed"] = True
    return outcome


def matrix_breakpoint(session, topology, core, frame):
    token = fresh_token()
    before = list_breakpoints(session, topology, core)
    outcome = None
    try:
        reconlib.write_m2_breakpoint_recovery_journal(core, token)
        result = reconlib.strict_routed_result(
            session, topology, core, source_command(frame, token))
        outcome = {
            "accepted": result.get("accepted") is True,
            "status": result.get("status"),
            "created_count": 0,
        }
        if not outcome["accepted"] or outcome["status"] != 1:
            outcome["semantic_flags"] = reconlib.inspection_semantic_flags(
                result.get("raw"))
            after = list_breakpoints(session, topology, core)
            if set(record.handle for record in before.records) != set(
                    record.handle for record in after.records):
                raise RuntimeError("rejected breakpoint command changed breakpoint handles")
        else:
            after = list_breakpoints(session, topology, core)
            mutation = reconlib.BreakpointMutation(
                reconlib.identify_probe_breakpoint(
                    before.records, after.records, frame.source, token),
                frame.source, frame.line, token)
            outcome["created_count"] = 1
            outcome["placement"] = mutation
            outcome["cleanup"] = delete_owned_breakpoint(
                session, topology, core, mutation)
    finally:
        recovery = recover_token_owned_breakpoints(session, topology, core, token)
    if outcome is None or not recovery.get("cleanup_confirmed"):
        raise RuntimeError("breakpoint matrix cleanup was not confirmed")
    outcome["recovery"] = recovery
    outcome["cleanup_confirmed"] = True
    return outcome


def main():
    rec = reconlib.Recorder("p09_m2_program_breakpoint")
    session = None
    try:
        cfg = reconlib.load_config()
        rec.note("config_loaded", True)
        session = rec.step("bind_existing_program_window",
                           lambda: reconlib.bind_existing_program_window(cfg))
        if session is None:
            raise RuntimeError("no uniquely verified existing program window")

        topology = rec.step("strict_program_component_topology",
                            lambda: reconlib.discover_strict_program_topology(session, cfg))
        if topology is None:
            raise RuntimeError("strict program-component topology could not be established")

        section = approved_section(cfg)
        if section is None:
            rec.note("breakpoint_phase", "disabled_by_local_configuration")
        else:
            explicitly_supplied_router_port()
            frames = []
            for core in sorted(topology.component_by_core):
                outcome, frame = rec.step("configured_program_calls_%d" % core,
                                          lambda core=core: calls_candidate(
                                              session, topology, core)) or ({}, None)
                if frame is not None:
                    frames.append((core, frame))
            rec.note("calls_candidate_summary", {
                "mapped_program_component_count": len(topology.component_by_core),
                "parseable_top_frame_count": len(frames),
            })
            if not frames:
                raise RuntimeError("no mapped program component has a parseable top frame")

            core, frame = frames[0]
            pair = rec.step("single_program_component_paired_breakpoint",
                            lambda: paired_breakpoint(session, topology, core, frame))
            if pair is None or not pair.get("cleanup_confirmed"):
                raise RuntimeError("single program-component paired breakpoint was not cleaned")

            matrix = []
            for mapped_core in sorted(topology.component_by_core):
                result = rec.step("mapped_program_component_breakpoint_%d" % mapped_core,
                                  lambda mapped_core=mapped_core: matrix_breakpoint(
                                      session, topology, mapped_core, frame))
                if result is None or not result.get("cleanup_confirmed", False):
                    if result is None or result.get("accepted"):
                        raise RuntimeError("program-component breakpoint matrix cleanup failed")
                matrix.append(result)
            rec.note("program_component_breakpoint_matrix", {
                "mapped_program_component_count": len(topology.component_by_core),
                "matrix_observation_count": len(matrix),
                "accepted_count": sum(1 for result in matrix
                                      if result and result.get("accepted")),
                "rejected_count": sum(1 for result in matrix
                                      if result and not result.get("accepted")),
                "cleanup_confirmed_count": sum(1 for result in matrix
                                               if result and result.get("cleanup_confirmed")),
            })
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
