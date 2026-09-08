# Python 2.7. Read-only per-core warm-window and command-endpoint evidence.
#
# Every configured core is inventoried by exact normalized ELF identity before
# this probe constructs a Session or sends a command.  Only a unique verified
# stopped window may become a command endpoint.  The command phase discovers a
# strict frozen program-component topology, whose P/H observations must form a
# complete configured-program bijection, then reads only B and $_STATE through
# routes revalidated against that topology.  No target execution or breakpoint
# mutation command is present in this probe.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


def routed_read_shape(session, topology, endpoint_ordinal, route_ordinal,
                      command):
    result = reconlib.strict_routed_command(
        session, topology, route_ordinal, command)
    observation = {
        "endpoint_configured_core_ordinal": endpoint_ordinal,
        "route_configured_core_ordinal": route_ordinal,
        "accepted": True,
        "production_shape_parseable": reconlib.inspection_output_shape(
            command, result.get("raw")),
    }
    if command == "B":
        observation.update(reconlib.summarize_breakpoint_listing(
            result.get("raw")))
    return observation


rec = reconlib.Recorder("p10_per_core_warm_inventory")
try:
    cfg = reconlib.load_config()
    rec.note("config_loaded", True)

    cores = cfg.get("cores")
    if not isinstance(cores, list) or not cores:
        raise ValueError("configured cores are required")

    # This loop must finish before any Session exists or any command is sent.
    inventories = []
    for ordinal, core in enumerate(cores):
        inventory = rec.step(
            "configured_core_%d_window_inventory" % ordinal,
            lambda core=core, ordinal=ordinal:
            reconlib.inspect_configured_core_window(cfg, core, ordinal))
        if inventory is not None:
            inventories.append(inventory)

    rec.note("configured_core_window_inventory_summary", {
        "configured_core_count": len(cores),
        "completed_inventory_count": len(inventories),
        "unique_verified_window_count": sum(
            1 for item in inventories if len(item.verified_windows) == 1),
        "all_configured_cores_inventoried": len(inventories) == len(cores),
    })
    if len(inventories) != len(cores):
        raise RuntimeError("configured core window inventory was incomplete")

    for inventory in inventories:
        if len(inventory.verified_windows) != 1:
            continue
        ordinal = inventory.configured_core_ordinal
        rec.step(
            "configured_core_%d_execution_window_api_inventory" % ordinal,
            lambda inventory=inventory:
            reconlib.execution_window_api_inventory(inventory.verified_windows[0]))

    endpoint_count = 0
    topology_count = 0
    command_observation_count = 0
    for inventory in inventories:
        if len(inventory.verified_windows) != 1:
            continue
        endpoint_count += 1
        endpoint_ordinal = inventory.configured_core_ordinal
        session = reconlib.Session(
            None, inventory.verified_windows[0], False, "warm")

        # The stop gate runs before components or any routed command.
        preflight = rec.step(
            "configured_core_%d_command_endpoint_preflight" % endpoint_ordinal,
            lambda session=session: reconlib.require_stopped_session(session))
        if preflight is None:
            continue

        topology = rec.step(
            "configured_core_%d_frozen_program_topology" % endpoint_ordinal,
            lambda session=session: reconlib.discover_strict_program_topology(
                session, cfg))
        if topology is None:
            continue
        topology_count += 1
        rec.note("configured_core_%d_frozen_route_summary" % endpoint_ordinal, {
            "endpoint_configured_core_ordinal": endpoint_ordinal,
            "configured_route_count": len(topology.component_by_core),
            "p_h_validated_route_count": len(topology.observations),
            "strict_program_mapping_confirmed": topology.summary.get(
                "strict_program_component_mapping_confirmed") is True,
        })

        for route_ordinal in sorted(topology.component_by_core):
            for label, command in (("breakpoints", "B"),
                                   ("state", "print $_STATE")):
                result = rec.step(
                    "configured_core_%d_route_%d_%s" % (
                        endpoint_ordinal, route_ordinal, label),
                    lambda session=session, topology=topology,
                    endpoint_ordinal=endpoint_ordinal,
                    route_ordinal=route_ordinal, command=command:
                    routed_read_shape(
                        session, topology, endpoint_ordinal, route_ordinal,
                        command))
                if result is not None:
                    command_observation_count += 1

        rec.step(
            "configured_core_%d_command_endpoint_postflight" % endpoint_ordinal,
            lambda session=session: reconlib.require_stopped_session(session))

    rec.note("command_endpoint_summary", {
        "unique_verified_window_count": endpoint_count,
        "strict_topology_endpoint_count": topology_count,
        "read_command_observation_count": command_observation_count,
    })
except Exception:
    rec.fatal()
finally:
    rec.finish()
