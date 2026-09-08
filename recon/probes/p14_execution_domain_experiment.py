# Python 2.7. Minimal, operator-gated execution-domain experiment.
#
# The disabled phase establishes a read-only frozen program topology.  Armed
# phase one routes `halt` to an already stopped selected component.  The
# separately acknowledged phase two routes exactly one non-blocking `sl n`,
# then always routes `halt` to the same frozen component before any further
# topology observation.  There is deliberately no primary-window recovery
# fallback: after routed execution, that would risk halting a different core.

import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib

try:
    long
except NameError:
    long = int


HALT_ACKNOWLEDGEMENT = "I_UNDERSTAND_ROUTED_HALT_ON_STOPPED_TARGET"
STEP_ACKNOWLEDGEMENT = "I_UNDERSTAND_ROUTED_STEP_MOVES_THE_SELECTED_CORE"
EXCLUSIVE_CONTROL_ACKNOWLEDGEMENT = "I_CONFIRM_EXCLUSIVE_STOPPED_TARGET_CONTROL"


class RoutedExecutionObservables(reconlib.RedactedEvidence):
    """In-memory routed observations with a scrubbed evidence representation."""
    def __init__(self, process, halt, state_raw, top_call):
        self.process = process
        self.halt = halt
        self.state_raw = state_raw
        self.top_call = top_call

    def evidence_summary(self):
        return {
            "process": self.process,
            "halt": self.halt,
            "state_output_parseable": reconlib.inspection_output_shape(
                "print $_STATE", self.state_raw),
            "top_call": self.top_call,
        }


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


def approved_section(cfg, topology):
    section = cfg.get("p14_execution_domain_probe")
    if not isinstance(section, dict) or not section.get("enabled"):
        return None
    if section.get("acknowledgement") != HALT_ACKNOWLEDGEMENT:
        raise RuntimeError("P14 routed-halt acknowledgement is missing")
    if (section.get("exclusive_control_acknowledgement") !=
            EXCLUSIVE_CONTROL_ACKNOWLEDGEMENT):
        raise RuntimeError("P14 exclusive-control acknowledgement is missing")
    ordinal = section.get("selected_core_ordinal")
    if (isinstance(ordinal, bool) or not isinstance(ordinal, (int, long)) or
            ordinal not in topology.component_by_core):
        raise RuntimeError("P14 selected configured core ordinal is not mapped")
    if section.get("single_step_enabled"):
        if section.get("single_step_acknowledgement") != STEP_ACKNOWLEDGEMENT:
            raise RuntimeError("P14 routed-step acknowledgement is missing")
    return section


def accepted_routed_command(session, topology, core, command):
    """Route a fixed command after a stopped-state topology revalidation."""
    topology = reconlib.revalidate_strict_program_topology(session, topology)
    if core not in topology.component_by_core:
        raise RuntimeError("selected configured core is not routed")
    reconlib.require_stopped_session(session)
    result = reconlib.run_command(session, "route %s %s" % (
        topology.component_by_core[core], command))
    if not result.get("accepted") or result.get("status") != 1:
        raise RuntimeError("MULTI rejected routed execution-domain command")
    return topology


def route_recovery_halt(session, topology, core):
    """Recover through the frozen selected route; never fall back to primary."""
    if core not in topology.component_by_core:
        raise RuntimeError("selected configured core is not routed for recovery")
    result = reconlib.run_command(session, "route %s halt" % (
        topology.component_by_core[core],))
    if not result.get("accepted") or result.get("status") != 1:
        raise RuntimeError("routed halt recovery was rejected")
    return reconlib.require_stopped_session(session)


def routed_read(session, topology, core, command):
    if core not in topology.component_by_core:
        raise RuntimeError("configured core is not routed")
    result = reconlib.run_command(session, "route %s %s" % (
        topology.component_by_core[core], command))
    if (not result.get("accepted") or result.get("status") != 1 or
            not isinstance(result.get("raw"), basestring)):
        raise RuntimeError("routed observable command was rejected")
    return result["raw"]


def routed_execution_observables(session, topology, core, include_top_call):
    """Capture P/H/$_STATE and an optional structured selected top call."""
    topology = reconlib.revalidate_strict_program_topology(session, topology)
    before = reconlib.require_stopped_session(session)
    process = reconlib.summarize_selected_process(
        routed_read(session, topology, core, "P"))
    halt = reconlib.summarize_halt_info(
        routed_read(session, topology, core, "H"))
    state_raw = routed_read(session, topology, core, "print $_STATE")
    if not reconlib.inspection_output_shape("print $_STATE", state_raw):
        raise RuntimeError("routed state output is not production-shape parseable")
    top_call = None
    if include_top_call:
        calls_raw = routed_read(session, topology, core,
                                "calls nopar pos notypes")
        top_call = reconlib.comparable_top_call_from_calls(calls_raw)
    after = reconlib.require_stopped_session(session)
    if before["status"] != after["status"]:
        raise RuntimeError("routed observable capture changed execution state")
    return RoutedExecutionObservables(process, halt, state_raw, top_call)


def full_topology_matrix(session, topology, include_all_top_calls):
    matrix = {}
    for core in sorted(topology.component_by_core):
        matrix["configured_core_%d" % core] = routed_execution_observables(
            session, topology, core, include_all_top_calls)
    return matrix


def observable_change(before, after):
    """Compare one structured observation in memory only."""
    if before.top_call is None or after.top_call is None:
        raise RuntimeError("structured top-call observation is missing")
    return {
        "process_status_changed": (
            before.process.get("selected_status") !=
            after.process.get("selected_status")),
        "halt_cause_changed": before.halt.get("cause") != after.halt.get("cause"),
        "state_output_changed": before.state_raw != after.state_raw,
        "top_call_location_changed": (
            before.top_call.location_kind != after.top_call.location_kind or
            before.top_call.identity != after.top_call.identity),
    }


def compare_step_matrices(before, after, selected_core):
    """Require exactly the selected route to leave an observable footprint."""
    if set(before) != set(after):
        raise RuntimeError("post-step configured core matrix differs from pre-step matrix")
    selected_key = "configured_core_%d" % selected_core
    if selected_key not in before:
        raise RuntimeError("selected configured core is missing from the observable matrix")
    selected = observable_change(before[selected_key], after[selected_key])
    if not any(selected.values()):
        raise RuntimeError("routed source step left no observable selected-core change")
    unselected_drift_count = 0
    for key in sorted(before):
        if key == selected_key:
            continue
        if any(observable_change(before[key], after[key]).values()):
            unselected_drift_count += 1
    if unselected_drift_count:
        raise RuntimeError("unselected configured core drifted during routed source step")
    return {
        "selected_observable_change": True,
        "unselected_unchanged_count": len(before) - 1,
        "unselected_drift_count": 0,
    }


def step_poll_configuration(section):
    """Require a finite, explicitly configured observation window."""
    attempts = section.get("step_poll_max_attempts")
    interval = section.get("step_poll_interval_seconds")
    if (isinstance(attempts, bool) or not isinstance(attempts, (int, long)) or
            attempts < 1 or attempts > 100):
        raise RuntimeError("P14 step poll attempts must be in 1..100")
    if (isinstance(interval, bool) or not isinstance(interval, (int, long, float)) or
            interval < 0.01 or interval > 1.0):
        raise RuntimeError("P14 step poll interval must be in 0.01..1.0 seconds")
    return attempts, float(interval)


def poll_until_selected_stopped(session, topology, selected_core, max_attempts,
                                interval_seconds, sleep=time.sleep):
    """Poll only routed P until selected and primary window are stopped.

    The attempt bound limits completed read-only polling iterations. It cannot
    place a hard deadline around an individual MULTI RunCommands invocation;
    the experiment records that limitation rather than claiming cancellation.
    """
    if selected_core not in topology.component_by_core:
        raise RuntimeError("selected configured core is not routed for polling")
    stopped = reconlib.stopped_status_value(session)
    for attempt in range(1, max_attempts + 1):
        selected_status = None
        for core in sorted(topology.component_by_core):
            summary = reconlib.summarize_selected_process(
                routed_read(session, topology, core, "P"))
            if core == selected_core:
                selected_status = summary.get("selected_status")
        primary_status = session.GetStatus()
        if isinstance(primary_status, bool) or not isinstance(primary_status, (int, long)):
            raise RuntimeError("primary status is invalid during routed step poll")
        if selected_status == "stopped" and primary_status == stopped:
            return {
                "attempt_count": attempt,
                "selected_stopped": True,
                "primary_stopped": True,
                "completed_before_recovery": True,
            }
        if attempt != max_attempts:
            sleep(interval_seconds)
    return {
        "attempt_count": max_attempts,
        "selected_stopped": False,
        "primary_stopped": False,
        "completed_before_recovery": False,
    }


def main():
    rec = reconlib.Recorder("p14_execution_domain_experiment")
    try:
        cfg = reconlib.load_config()
        rec.note("config_loaded", True)
        cores = cfg.get("cores")
        if not isinstance(cores, list) or not cores:
            raise ValueError("configured cores are required")

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
        if len(inventories) != len(cores) or len(verified) != 1:
            raise RuntimeError("P14 requires one verified warm command endpoint")

        endpoint = verified[0]
        session = reconlib.Session(None, endpoint.verified_windows[0], False, "warm")
        rec.step("command_endpoint_preflight",
                 lambda: reconlib.require_stopped_session(session))
        topology = rec.step(
            "frozen_program_topology",
            lambda: reconlib.discover_strict_program_topology(session, cfg))
        if topology is None:
            raise RuntimeError("strict program topology was not established")

        section = approved_section(cfg, topology)
        if section is None:
            rec.note("execution_phase", "disabled_by_local_configuration")
            return

        explicitly_supplied_router_port()
        selected_core = section["selected_core_ordinal"]
        before_halt = rec.step(
            "all_core_observables_before_idempotent_halt",
            lambda: full_topology_matrix(session, topology, False))
        topology = rec.step(
            "selected_core_idempotent_routed_halt",
            lambda: accepted_routed_command(session, topology, selected_core, "halt"))
        if topology is None:
            raise RuntimeError("idempotent routed halt was not accepted")
        after_halt = rec.step(
            "all_core_observables_after_idempotent_halt",
            lambda: full_topology_matrix(session, topology, False))
        if before_halt is None or after_halt is None:
            raise RuntimeError("idempotent halt observable matrix was incomplete")
        rec.note("idempotent_halt_summary", {
            "selected_configured_core_ordinal": selected_core,
            "before_matrix_count": len(before_halt),
            "after_matrix_count": len(after_halt),
            "all_targets_stopped_after": True,
        })

        if not section.get("single_step_enabled"):
            rec.note("single_step_phase", "disabled_by_local_configuration")
            return

        max_attempts, interval_seconds = step_poll_configuration(section)
        before_step = rec.step(
            "all_core_observables_before_single_step",
            lambda: full_topology_matrix(session, topology, True))
        if before_step is None:
            raise RuntimeError("single-step preflight matrix was incomplete")

        # Keep this route object even if poll/revalidation fails after `sl n`.
        recovery_topology = topology
        step_started = False
        poll = None
        recovery = None
        try:
            topology = rec.step(
                "selected_core_single_source_step",
                lambda: accepted_routed_command(
                    session, topology, selected_core, "sl n"))
            if topology is None:
                raise RuntimeError("routed source step was not accepted")
            step_started = True
            poll = rec.step(
                "selected_core_bounded_route_poll",
                lambda: poll_until_selected_stopped(
                    session, recovery_topology, selected_core, max_attempts,
                    interval_seconds))
        finally:
            recovery = rec.step(
                "selected_core_routed_halt_recovery",
                lambda: route_recovery_halt(
                    session, recovery_topology, selected_core))
        if not step_started or recovery is None:
            raise RuntimeError("routed source step or halt recovery failed")
        if poll is None or not poll.get("completed_before_recovery"):
            raise RuntimeError("routed source step did not complete within bounded polling")
        topology = rec.step(
            "strict_topology_after_single_step_recovery",
            lambda: reconlib.revalidate_strict_program_topology(
                session, recovery_topology))
        if topology is None:
            raise RuntimeError("strict topology drifted after routed step recovery")
        after_step = rec.step(
            "all_core_observables_after_single_step_recovery",
            lambda: full_topology_matrix(session, topology, True))
        if after_step is None:
            raise RuntimeError("single-step postflight matrix was incomplete")
        comparison = compare_step_matrices(before_step, after_step, selected_core)
        rec.note("single_step_protocol_summary", {
            "selected_configured_core_ordinal": selected_core,
            "poll_attempt_count": poll["attempt_count"],
            "selected_stopped_before_recovery": poll["selected_stopped"],
            "primary_stopped_before_recovery": poll["primary_stopped"],
            "individual_runcommands_hard_deadline_available": False,
            "comparison": comparison,
            "routed_halt_recovery_confirmed": True,
        })
    except Exception:
        rec.fatal()
    finally:
        rec.finish()


if __name__ == "__main__":
    main()
