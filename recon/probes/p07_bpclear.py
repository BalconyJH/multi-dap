# Python 2.7. M0-7: do breakpoint placement, toggle, or deletion perturb a
# running target? Uses the same target-progress differential as p06_poll.

import os
import re
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


VALUE_RE = re.compile(r"MULTIDAP_VALUE=(\d+)")
BREAKPOINT_RE = re.compile(r"^\s*(\d+)\s", re.MULTILINE)


rec = reconlib.Recorder("p07_bpclear")
session = None
owned_breakpoint = None
try:
    cfg = rec.step("load_config", reconlib.load_config)
    expression = cfg.get("free_running_symbol", "")
    site = cfg.get("breakpoint_site") or {}
    source = site.get("file", "")
    line = int(site.get("line", 0) or 0)
    if (not expression or expression.startswith("<") or not source or
            source.startswith("<") or line <= 0):
        raise ValueError("progress expression and unreachable breakpoint site are required")
    if "\n" in expression or "\r" in expression or "\n" in source or "\r" in source:
        raise ValueError("probe expressions must be single-line")
    window_seconds = float(cfg.get("poll_window_seconds", 3.0))
    session = rec.step("bind_existing_program_window",
                       lambda: reconlib.bind_existing_program_window(cfg))
    if session is None:
        raise RuntimeError("session could not be opened")

    def command(text):
        result = reconlib.run_command(session, text)
        if not result.get("accepted") or result.get("status") != 1:
            raise RuntimeError("MULTI refused probe command")
        return result

    def read_progress():
        result = command('mprintf("MULTIDAP_VALUE=%u\\n", (unsigned int)(%s))' % expression)
        match = VALUE_RE.search(result.get("raw") or "")
        if match is None:
            raise RuntimeError("could not read progress expression")
        return int(match.group(1)) & 0xFFFFFFFF

    def breakpoint_ids():
        result = command("B")
        return set(int(value) for value in BREAKPOINT_RE.findall(result.get("raw") or ""))

    initial_ids = rec.step("initial_breakpoints", breakpoint_ids)
    if initial_ids is None:
        raise RuntimeError("initial breakpoints could not be listed")

    def rate_window(label, action=None):
        session.Resume(0, 0)
        time.sleep(0.2)
        start_value = read_progress()
        started = time.time()
        time.sleep(window_seconds / 2.0)
        before = session.GetStatus()
        action_result = None
        action_seconds = None
        if action is not None:
            action_started = time.time()
            action_result = action()
            action_seconds = time.time() - action_started
        after = session.GetStatus()
        remaining = window_seconds - (time.time() - started)
        if remaining > 0.0:
            time.sleep(remaining)
        end_value = read_progress()
        elapsed = time.time() - started
        final_status = session.GetStatus()
        session.Halt(1, 0)
        delta = (end_value - start_value) & 0xFFFFFFFF
        return {
            "label": label,
            "delta": delta,
            "elapsed": elapsed,
            "rate": delta / elapsed if elapsed else None,
            "status_before_action": before,
            "status_after_action": after,
            "status_before_halt": final_status,
            "action_seconds": action_seconds,
            "action_result": action_result,
            "valid": before == 3 and after == 3 and final_status == 3 and delta > 0,
        }

    baseline = rec.step("window_baseline", lambda: rate_window("baseline"))
    location = "%s#%d" % (source, line)
    placed = rec.step("window_place_inactive", lambda: rate_window(
        "place_inactive", lambda: command("b /off " + location)))
    current_ids = rec.step("breakpoints_after_place", breakpoint_ids)
    if current_ids is None:
        raise RuntimeError("breakpoints could not be listed after placement")
    created = sorted(current_ids - set(initial_ids or []))
    if len(created) != 1:
        raise RuntimeError("could not identify the probe-owned breakpoint")
    owned_breakpoint = created[0]
    activated = rec.step("window_activate", lambda: rate_window(
        "activate", lambda: command("tog %%%d" % owned_breakpoint)))
    deactivated = rec.step("window_deactivate", lambda: rate_window(
        "deactivate", lambda: command("tog %%%d" % owned_breakpoint)))
    deleted = rec.step("window_delete", lambda: rate_window(
        "delete", lambda: command("d %%%d" % owned_breakpoint)))
    owned_breakpoint = None

    if baseline and baseline.get("rate"):
        for name, result in (("place", placed), ("activate", activated),
                             ("deactivate", deactivated), ("delete", deleted)):
            ratio = result.get("rate") / baseline["rate"] if result and result.get("rate") else None
            rec.note(name + "_to_baseline_ratio", ratio)
except Exception:
    rec.fatal()
finally:
    if session is not None:
        try:
            session.Halt(1, 0)
        except Exception:
            pass
        if owned_breakpoint is not None:
            try:
                reconlib.run_command(session, "d %%%d" % owned_breakpoint)
            except Exception:
                pass
        reconlib.close_session(session)
    rec.finish()
