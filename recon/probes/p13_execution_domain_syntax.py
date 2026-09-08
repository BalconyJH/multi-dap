# Python 2.7. Warm-only, syntax-only execution-domain evidence.
#
# This probe inventories every configured core before creating a Session.  It
# selects exactly one verified existing window as the warm command endpoint,
# then establishes a strict P/H topology for every configured program.  Its
# only candidate control operations are wrapped in MULTI's documented `sc`
# syntax checker, so it never sends route/halt/continue/step to the target.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


rec = reconlib.Recorder("p13_execution_domain_syntax")
try:
    cfg = reconlib.load_config()
    rec.note("config_loaded", True)

    cores = cfg.get("cores")
    if not isinstance(cores, list) or not cores:
        raise ValueError("configured cores are required")

    # Complete window identity evidence before creating a Session or command.
    inventories = []
    for ordinal, core in enumerate(cores):
        inventory = rec.step(
            "configured_core_%d_window_inventory" % ordinal,
            lambda core=core, ordinal=ordinal:
            reconlib.inspect_configured_core_window(cfg, core, ordinal))
        if inventory is not None:
            inventories.append(inventory)

    verified = [item for item in inventories
                if len(item.verified_windows) == 1]
    rec.note("configured_core_window_inventory_summary", {
        "configured_core_count": len(cores),
        "completed_inventory_count": len(inventories),
        "unique_verified_window_count": len(verified),
        "all_configured_cores_inventoried": len(inventories) == len(cores),
    })
    if len(inventories) != len(cores):
        raise RuntimeError("configured core window inventory was incomplete")
    if len(verified) != 1:
        raise RuntimeError("p13 requires exactly one verified warm command endpoint")

    endpoint = verified[0]
    endpoint_ordinal = endpoint.configured_core_ordinal
    rec.step(
        "command_endpoint_execution_window_api_inventory",
        lambda: reconlib.execution_window_api_inventory(
            endpoint.verified_windows[0]))
    session = reconlib.Session(None, endpoint.verified_windows[0], False, "warm")

    rec.step("command_endpoint_preflight",
             lambda: reconlib.require_stopped_session(session))
    topology = rec.step(
        "frozen_program_topology",
        lambda: reconlib.discover_strict_program_topology(session, cfg))
    if topology is None:
        raise RuntimeError("strict program topology was not established")
    rec.note("execution_domain_candidate_summary", {
        "endpoint_configured_core_ordinal": endpoint_ordinal,
        "configured_route_count": len(topology.component_by_core),
        "strict_program_mapping_confirmed": topology.summary.get(
            "strict_program_component_mapping_confirmed") is True,
        "syntax_check_only": True,
    })

    observation_count = 0
    for route_ordinal in sorted(topology.component_by_core):
        for command_kind, ignored_command in reconlib.execution_domain_syntax_candidates():
            result = rec.step(
                "configured_core_%d_%s_syntax" % (route_ordinal, command_kind),
                lambda session=session, topology=topology,
                route_ordinal=route_ordinal, command_kind=command_kind:
                reconlib.strict_execution_domain_syntax_check(
                    session, topology, route_ordinal, command_kind))
            if result is not None:
                observation_count += 1

    rec.step("command_endpoint_postflight",
             lambda: reconlib.require_stopped_session(session))
    rec.note("execution_domain_syntax_summary", {
        "configured_core_count": len(cores),
        "syntax_observation_count": observation_count,
        "target_control_executed": False,
    })
except Exception:
    rec.fatal()
finally:
    rec.finish()
