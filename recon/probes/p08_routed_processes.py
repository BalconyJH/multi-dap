# Python 2.7. Read-only M0 evidence for route-to-process correspondence.
#
# It discovers route PIDs from the live component listing, then reads `P`
# through each route. Evidence contains only parser-checked shape, status, and
# mapping booleans; it never serializes component names, slots, PIDs, program
# names, commands, or raw MULTI output.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


def command_shape(session, command):
    result = reconlib.run_command(session, command)
    raw = result.get("raw")
    return result, {
        "accepted": result.get("accepted") is True,
        "status": result.get("status"),
        "raw_present": bool(raw),
        "raw_line_count": len(raw.splitlines()) if isinstance(raw, basestring) else 0,
    }


def routed_pid_summary(session, route_pid):
    before = reconlib.require_stopped_session(session)
    processes, process_shape = command_shape(
        session, "route debugger.pid.%d P" % route_pid)
    if not process_shape["accepted"] or process_shape["status"] != 1:
        raise RuntimeError("routed process command was rejected")
    process_summary = reconlib.summarize_routed_processes(
        route_pid, processes.get("raw"))
    halt, halt_shape = command_shape(session, "route debugger.pid.%d H" % route_pid)
    if not halt_shape["accepted"] or halt_shape["status"] != 1:
        raise RuntimeError("routed halt-info command was rejected")
    halt_summary = reconlib.summarize_halt_info(halt.get("raw"))
    after = reconlib.require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("routed read command changed execution state")
    return {"processes": process_summary, "halt": halt_summary,
            "process_command": process_shape, "halt_command": halt_shape}


def routed_program_component_summary(session, component_id, component_ordinal,
                                    alias_programs, configured_programs):
    """Read P/H through an in-memory program component ID.

    Rejection is itself evidence here.  A successfully parsed P reply retains
    identities only in the returned redacted observation.
    """
    before = reconlib.require_stopped_session(session)
    processes, process_shape = command_shape(session, "route %s P" % component_id)
    process_summary = None
    process_rows = []
    if process_shape["accepted"] and process_shape["status"] == 1:
        process_summary = reconlib.summarize_program_component_processes(
            component_ordinal, alias_programs, processes.get("raw"), configured_programs)
        process_rows = reconlib._parse_process_listing(processes.get("raw"))
    halt, halt_shape = command_shape(session, "route %s H" % component_id)
    halt_summary = None
    if halt_shape["accepted"] and halt_shape["status"] == 1:
        halt_summary = reconlib.summarize_halt_info(halt.get("raw"))
    after = reconlib.require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("program component read command changed execution state")
    return reconlib.ProgramComponentObservation(
        component_id, alias_programs, process_summary, process_rows,
        process_shape, halt_summary, halt_shape)


def window_pid_alias_summary(window, ordinal, route_pids):
    session = reconlib.Session(None, window, False, "warm")
    before = reconlib.require_stopped_session(session)
    observation = reconlib.window_pid_alias_observation(session, ordinal, route_pids)
    after = reconlib.require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("GetTargetPid changed execution state")
    return observation


def window_process_alias_summary(window, ordinal, route_pids):
    session = reconlib.Session(None, window, False, "warm")
    before = reconlib.require_stopped_session(session)
    observation = reconlib.window_process_alias_observation(session, ordinal, route_pids)
    after = reconlib.require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("Window process query changed execution state")
    return observation


def program_component_groups(inventory):
    groups = {}
    for alias in inventory["program_aliases"]:
        groups.setdefault(alias["component_id"], set()).add(alias["program_name"])
    return [(component_id, sorted(programs))
            for component_id, programs in sorted(groups.items())]


def routed_inspection_summary(session, component_id):
    """Probe only the documented read-only inspection command set."""
    before = reconlib.require_stopped_session(session)
    observations = {}
    for key, command in (
            ("calls", "calls"),
            # Documented calls options normalize parameter and C++ type
            # rendering without changing frame selection or execution state.
            ("calls_nopar_pos_notypes", "calls nopar pos notypes"),
            ("locals", "l"),
            ("breakpoints", "B"),
            ("state", "print $_STATE")):
        result, shape = command_shape(session, "route %s %s" % (component_id, command))
        parseable = False
        if shape["accepted"] and shape["status"] == 1:
            grammar_command = "calls" if key.startswith("calls") else command
            parseable = reconlib.inspection_output_shape(grammar_command,
                                                        result.get("raw"))
        shape["production_shape_parseable"] = parseable
        if key.startswith("calls"):
            shape["grammar_signature"] = reconlib.calls_grammar_signature(
                result.get("raw"))
        if (key.startswith("calls") or key == "locals") and not parseable:
            shape["semantic_flags"] = reconlib.inspection_semantic_flags(
                result.get("raw"))
        observations[key] = shape
    after = reconlib.require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("inspection command changed execution state")
    return observations


rec = reconlib.Recorder("p08_routed_processes")
session = None
try:
    cfg = reconlib.load_config()
    rec.note("config_loaded", True)
    session = rec.step("bind_existing_program_window",
                       lambda: reconlib.bind_existing_program_window(cfg))
    if session is None:
        raise RuntimeError("no uniquely verified existing program window")

    before = reconlib.require_stopped_session(session)
    components, components_shape = command_shape(session, "components")
    if not components_shape["accepted"] or components_shape["status"] != 1:
        raise RuntimeError("component command was rejected")
    configured_programs = reconlib.configured_core_programs(cfg)
    inventory = reconlib.component_route_inventory(components.get("raw"))
    after = reconlib.require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("component command changed execution state")
    rec.note("component_summary", {
        "component_row_count": inventory["component_row_count"],
        "route_count": inventory["route_count"],
        "program_component_count": inventory["program_component_count"],
        "configured_core_count": len(configured_programs),
    })
    rec.note("component_command", components_shape)

    configured_components = reconlib.match_configured_program_components(
        inventory, configured_programs)
    rec.note("program_component_mapping", configured_components["summary"])

    window_inventories = []
    window_pid_observations = []
    window_process_observations = []
    for ordinal, core in enumerate(cfg["cores"]):
        window_inventory = rec.step("configured_core_%d_window_inventory" % ordinal,
                                    lambda core=core, ordinal=ordinal:
                                    reconlib.inspect_configured_core_window(
                                        cfg, core, ordinal))
        if window_inventory is None:
            continue
        window_inventories.append(window_inventory)
        if len(window_inventory.verified_windows) != 1:
            continue
        window = window_inventory.verified_windows[0]
        pid_observation = rec.step("configured_core_%d_window_pid_alias" % ordinal,
                                   lambda window=window, ordinal=ordinal:
                                   window_pid_alias_summary(
                                       window, ordinal, inventory["route_pids"]))
        if pid_observation is not None:
            window_pid_observations.append(pid_observation)
        process_observation = rec.step(
            "configured_core_%d_window_process_alias" % ordinal,
            lambda window=window, ordinal=ordinal: window_process_alias_summary(
                window, ordinal, inventory["route_pids"]))
        if process_observation is not None:
            window_process_observations.append(process_observation)
    rec.note("configured_core_window_summary", {
        "configured_core_count": len(configured_programs),
        "window_inventory_count": len(window_inventories),
        "configured_cores_with_unique_verified_window_count": sum(
            1 for item in window_inventories if len(item.verified_windows) == 1),
    })
    rec.note("window_pid_alias_mapping", reconlib.summarize_window_pid_alias_mapping(
        len(configured_programs), window_pid_observations))
    rec.note("window_process_alias_mapping",
             reconlib.summarize_window_process_alias_mapping(
                 len(configured_programs), window_process_observations))

    route_summaries = []
    for index, route_pid in enumerate(inventory["route_pids"]):
        result = rec.step("routed_pid_summary_%d" % index,
                          lambda route_pid=route_pid: routed_pid_summary(
                              session, route_pid))
        if result is None:
            raise RuntimeError("routed process capture failed")
        route_summaries.append(result["processes"])
    rec.note("route_pid_process_mapping", reconlib.summarize_route_pid_mapping(
        route_summaries))

    program_results = []
    for ordinal, (component_id, aliases) in enumerate(program_component_groups(inventory)):
        result = rec.step("program_component_%d_summary" % ordinal,
                          lambda component_id=component_id, ordinal=ordinal, aliases=aliases:
                          routed_program_component_summary(
                              session, component_id, ordinal, aliases,
                              configured_programs))
        if result is None:
            continue
        program_results.append(result)
    rec.note("program_component_routing", {
        "program_component_count": len(program_component_groups(inventory)),
        "program_component_observation_count": len(program_results),
        "accepted_process_command_count": sum(
            1 for item in program_results if item.process_command["accepted"] and
            item.process_command["status"] == 1),
        "accepted_halt_command_count": sum(
            1 for item in program_results if item.halt_command["accepted"] and
            item.halt_command["status"] == 1),
    })
    rec.note("program_alias_process_mapping",
             reconlib.summarize_program_alias_process_mapping(
                 configured_programs, program_results))
    strict_mapping = reconlib.summarize_strict_program_component_mapping(
        configured_programs, program_results)
    rec.note("strict_program_component_mapping", strict_mapping)
    if strict_mapping["strict_program_component_mapping_confirmed"]:
        inspection_results = []
        for observation in program_results:
            result = rec.step("strict_program_component_%d_inspection" %
                              observation.process_summary[
                                  "selected_configured_core_ordinal"],
                              lambda component_id=observation.component_id:
                              routed_inspection_summary(session, component_id))
            if result is not None:
                inspection_results.append(result)
        rec.note("strict_program_component_inspection", {
            "strict_mapping_used": True,
            "mapped_component_count": len(program_results),
            "inspection_observation_count": len(inspection_results),
            "calls_accepted_count": sum(
                1 for result in inspection_results if result["calls"]["accepted"] and
                result["calls"]["status"] == 1),
            "calls_production_shape_parseable_count": sum(
                1 for result in inspection_results
                if result["calls"]["production_shape_parseable"]),
            "calls_nopar_pos_notypes_accepted_count": sum(
                1 for result in inspection_results
                if result["calls_nopar_pos_notypes"]["accepted"] and
                result["calls_nopar_pos_notypes"]["status"] == 1),
            "calls_nopar_pos_notypes_production_shape_parseable_count": sum(
                1 for result in inspection_results
                if result["calls_nopar_pos_notypes"]["production_shape_parseable"]),
            "locals_accepted_count": sum(
                1 for result in inspection_results if result["locals"]["accepted"] and
                result["locals"]["status"] == 1),
            "locals_production_shape_parseable_count": sum(
                1 for result in inspection_results
                if result["locals"]["production_shape_parseable"]),
            "breakpoints_accepted_count": sum(
                1 for result in inspection_results
                if result["breakpoints"]["accepted"] and
                result["breakpoints"]["status"] == 1),
            "breakpoints_production_shape_parseable_count": sum(
                1 for result in inspection_results
                if result["breakpoints"]["production_shape_parseable"]),
            "state_accepted_count": sum(
                1 for result in inspection_results if result["state"]["accepted"] and
                result["state"]["status"] == 1),
            "state_production_shape_parseable_count": sum(
                1 for result in inspection_results
                if result["state"]["production_shape_parseable"]),
        })
    else:
        rec.note("strict_program_component_inspection", {
            "strict_mapping_used": False,
            "mapped_component_count": 0,
            "inspection_observation_count": 0,
        })
except Exception:
    rec.fatal()
finally:
    if session is not None:
        reconlib.close_session(session)
    rec.finish()
