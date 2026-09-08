# Python 2.7. Read-only M2 source-file-list grammar capture.
#
# debug_cmd.pdf p.115 documents `l f` as listing all source-file names.  Its
# optional string is only a contains filter, so this probe issues the full
# listing only. It captures the full listing on each verified configured-core
# route and records only a bounded, target-text-free grammar signature. No
# `e`, `b`, or `d` command is present here.

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


def routed_source_file_listing(session, topology, endpoint_ordinal,
                               route_ordinal, label, command):
    result = reconlib.strict_routed_command(
        session, topology, route_ordinal, command)
    grammar = reconlib.source_file_listing_grammar_signature(result.get("raw"))
    return {
        "endpoint_configured_core_ordinal": endpoint_ordinal,
        "route_configured_core_ordinal": route_ordinal,
        "listing_scope": label,
        "accepted": True,
        # The broad grammar capture is useful evidence, but only the mirror
        # of Go's byte-level parser authorizes production source membership.
        "production_shape_parseable": grammar.get(
            "production_parser_delta", {}).get(
                "strict_go_parser_candidate") is True,
        "structural_grammar_parseable": grammar.get(
            "strict_production_grammar_candidate") is True,
        "grammar": grammar,
    }


rec = reconlib.Recorder("p12_m2_source_files")
try:
    cfg = reconlib.load_config()
    rec.note("config_loaded", True)
    commands = reconlib.source_file_listing_commands(cfg)
    rec.note("source_file_listing_command_summary", {
        "unfiltered_listing_requested": commands == [("all", "l f")],
        "filtered_listing_requested": False,
    })

    cores = cfg.get("cores")
    if not isinstance(cores, list) or not cores:
        raise ValueError("configured cores are required")

    # Complete every exact-ELF window inventory before any Session/command.
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

    endpoint_count = 0
    topology_count = 0
    listing_observation_count = 0
    for inventory in inventories:
        if len(inventory.verified_windows) != 1:
            continue
        endpoint_count += 1
        endpoint_ordinal = inventory.configured_core_ordinal
        session = reconlib.Session(
            None, inventory.verified_windows[0], False, "warm")
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
            for label, command in commands:
                result = rec.step(
                    "configured_core_%d_route_%d_source_files_%s" % (
                        endpoint_ordinal, route_ordinal, label),
                    lambda session=session, topology=topology,
                    endpoint_ordinal=endpoint_ordinal,
                    route_ordinal=route_ordinal, label=label, command=command:
                    routed_source_file_listing(
                        session, topology, endpoint_ordinal, route_ordinal,
                        label, command))
                if result is not None:
                    listing_observation_count += 1
        rec.step(
            "configured_core_%d_command_endpoint_postflight" % endpoint_ordinal,
            lambda session=session: reconlib.require_stopped_session(session))

    rec.note("command_endpoint_summary", {
        "unique_verified_window_count": endpoint_count,
        "strict_topology_endpoint_count": topology_count,
        "source_file_listing_observation_count": listing_observation_count,
    })
except Exception:
    rec.fatal()
finally:
    rec.finish()
