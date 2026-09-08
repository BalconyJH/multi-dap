# Python 2.7. M0-6: does state polling perturb a running target?
#
# This is a differential rate experiment, not a call-latency benchmark. The
# configured expression must name an unsigned 32-bit value advanced by target
# execution itself. Each window reads it exactly twice, so read cost cancels.

import os
import re
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


VALUE_RE = re.compile(r"MULTIDAP_VALUE=(\d+)")


def mean(values):
    if not values:
        return None
    return sum(values) / float(len(values))


rec = reconlib.Recorder("p06_poll")
session = None
try:
    cfg = rec.step("load_config", reconlib.load_config)
    expression = cfg.get("free_running_symbol", "")
    if not expression or expression.startswith("<") or "\n" in expression or "\r" in expression:
        raise ValueError("free_running_symbol must be a target progress expression")
    window_seconds = float(cfg.get("poll_window_seconds", 3.0))
    if window_seconds <= 0.0:
        raise ValueError("poll_window_seconds must be positive")
    rec.note("rate_source", "target_expression")
    rec.note("window_seconds", window_seconds)
    session = rec.step("bind_existing_program_window",
                       lambda: reconlib.bind_existing_program_window(cfg))
    if session is None:
        raise RuntimeError("session could not be opened")

    def read_progress():
        command = 'mprintf("MULTIDAP_VALUE=%u\\n", (unsigned int)(%s))' % expression
        result = reconlib.run_command(session, command)
        raw = result.get("raw") or ""
        match = VALUE_RE.search(raw)
        if not result.get("accepted") or result.get("status") != 1 or match is None:
            raise RuntimeError("could not read progress expression")
        return int(match.group(1)) & 0xFFFFFFFF

    validation = rec.step("validate_progress_expression", lambda: (read_progress(), read_progress()))
    if validation is None:
        raise RuntimeError("progress expression could not be read")

    def run_window(mode):
        session.Resume(0, 0)
        time.sleep(0.2)
        start_value = read_progress()
        started = time.time()
        polls = 0
        call_seconds = []
        status_histogram = {}
        last_stop_stamp = None
        while time.time() - started < window_seconds:
            if mode == "quiet":
                time.sleep(0.005)
                continue
            call_started = time.time()
            if mode == "status":
                value = session.GetStatus()
                status_histogram[value] = status_histogram.get(value, 0) + 1
            elif mode == "state":
                value = session.GetCurPrInfo("")
                if isinstance(value, dict):
                    last_stop_stamp = value.get("stopStamp")
            else:
                raise ValueError("unknown polling mode")
            call_seconds.append(time.time() - call_started)
            polls += 1
        end_value = read_progress()
        elapsed = time.time() - started
        status_before_halt = session.GetStatus()
        session.Halt(1, 0)
        delta = (end_value - start_value) & 0xFFFFFFFF
        return {
            "mode": mode,
            "start": start_value,
            "end": end_value,
            "delta": delta,
            "elapsed": elapsed,
            "rate": delta / elapsed if elapsed else None,
            "polls": polls,
            "calls_per_second": polls / elapsed if elapsed else None,
            "call_mean_seconds": mean(call_seconds),
            "call_max_seconds": max(call_seconds) if call_seconds else None,
            "status_histogram": status_histogram,
            "status_before_halt": status_before_halt,
            "last_stop_stamp": last_stop_stamp,
            "valid": status_before_halt == 3 and delta > 0,
        }

    results = []
    for index, mode in enumerate(("quiet", "status", "state", "state", "status", "quiet")):
        result = rec.step("window_%d_%s" % (index, mode),
                          (lambda selected: lambda: run_window(selected))(mode))
        results.append(result)

    rates = {"quiet": [], "status": [], "state": []}
    for result in results:
        if result and result.get("valid") and result.get("rate") is not None:
            rates[result["mode"]].append(result["rate"])
    quiet_rate = mean(rates["quiet"])
    rec.note("mean_rates", dict((key, mean(value)) for key, value in rates.items()))
    rec.note("status_to_quiet_ratio",
             mean(rates["status"]) / quiet_rate if quiet_rate and rates["status"] else None)
    rec.note("state_to_quiet_ratio",
             mean(rates["state"]) / quiet_rate if quiet_rate and rates["state"] else None)
except Exception:
    rec.fatal()
finally:
    if session is not None:
        try:
            session.Halt(1, 0)
        except Exception:
            pass
        reconlib.close_session(session)
    rec.finish()
