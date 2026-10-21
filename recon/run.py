"""Host-side launcher for M0 reconnaissance probes.

mpythonrun.exe writes stdout with WriteConsole and pops a modal error dialog if its
standard handles are redirected, so this launcher gives it a fresh console and reads
the probe's result from its JSON evidence file instead.
"""

import argparse
import io
import json
import os
import subprocess
import sys
import time

RECON = os.path.dirname(os.path.abspath(__file__))
CREATE_NEW_CONSOLE = 0x00000010
PYTHON_VERSION_2 = 2
PYTHON_VERSION_3 = 3
DEFAULT_PYTHON_VERSION = PYTHON_VERSION_3
WARM_ONLY_PROBES = ("p00e_warm_bind",
                    "p02_socket",
                    "p03_m2_source_known",
                    "p04_m4_execution",
                    "p05_m5_inspection",
                    "p06_poll",
                    "p07_bpclear",
                    "p08_routed_processes",
                    "p09_m2_program_breakpoint",
                    "p10_per_core_warm_inventory",
                    "p11_m2_owned_breakpoint_recovery",
                    "p12_m2_source_files",
                    "p13_execution_domain_syntax",
                    "p14_execution_domain_experiment",
                    "p15_warm_registry_health")


def wait_for_process(proc, timeout, poll_interval=0.05):
    """Wait for a child on both Python 2.7 and Python 3.

    ``Popen.wait(timeout=...)`` and ``subprocess.TimeoutExpired`` were added
    after the Python 2.7 runtime shipped with older MULTI installations.  A
    small poll loop keeps the explicit Python 2 fallback usable without
    changing the child process or redirecting its console handles.
    """
    deadline = time.time() + max(0.0, float(timeout))
    while True:
        return_code = proc.poll()
        if return_code is not None:
            return return_code, False
        if time.time() >= deadline:
            return None, True
        time.sleep(min(poll_interval, max(0.0, deadline - time.time())))


def kill_timed_out_process(proc, timeout=5.0):
    """Kill a timed-out child without an unbounded cleanup wait."""
    try:
        proc.kill()
    except OSError:
        return_code = proc.poll()
        if return_code is None:
            raise
        return return_code
    return_code, timed_out = wait_for_process(proc, timeout)
    if timed_out:
        raise RuntimeError("probe did not exit after kill; it may still be running")
    return return_code


def resolve_python_version(config, override=None):
    """Resolve the embedded MULTI interpreter without an implicit fallback."""
    raw = config.get("python_version", DEFAULT_PYTHON_VERSION) if override is None else override
    if not isinstance(raw, int) or isinstance(raw, bool):
        raise ValueError("python_version must be 2 or 3")
    if raw not in (PYTHON_VERSION_2, PYTHON_VERSION_3):
        raise ValueError("python_version must be 2 or 3")
    return raw


def embedded_python_environment(version, base=None):
    """Pin mpythonrun's interpreter selector while preserving other variables."""
    if version not in (PYTHON_VERSION_2, PYTHON_VERSION_3):
        raise ValueError("python_version must be 2 or 3")
    environment = dict(os.environ if base is None else base)
    for name in list(environment):
        if name.lower() == "ghs_ide_use_python3":
            del environment[name]
    environment["GHS_IDE_USE_PYTHON3"] = "1" if version == PYTHON_VERSION_3 else "0"
    return environment


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("probe", help="probe stem, e.g. p06_poll")
    ap.add_argument("--timeout", type=float, default=600.0)
    ap.add_argument(
        "--service-router-port",
        type=int,
        default=None,
        help="override the ignored configuration's live service-router port",
    )
    ap.add_argument(
        "--cold",
        action="store_true",
        help="do not join the configured live service router",
    )
    ap.add_argument(
        "--python-version",
        type=int,
        choices=(PYTHON_VERSION_2, PYTHON_VERSION_3),
        default=None,
        help="embedded MULTI Python runtime (default: config value or 3)",
    )
    args, rest = ap.parse_known_args()
    if args.cold and args.service_router_port is not None:
        ap.error("--cold and --service-router-port are mutually exclusive")
    if args.probe in WARM_ONLY_PROBES and args.cold:
        ap.error("%s is warm-only and rejects --cold" % args.probe)
    if (args.probe in ("p09_m2_program_breakpoint",
                       "p11_m2_owned_breakpoint_recovery",
                       "p14_execution_domain_experiment") and
            args.service_router_port is None):
        ap.error("%s requires explicit --service-router-port" % args.probe)
    if args.service_router_port is not None and not 1 <= args.service_router_port <= 65535:
        ap.error("--service-router-port must be in 1..65535")
    if rest and rest[0] == "--":
        rest = rest[1:]

    cfg_path = os.path.join(RECON, "config.local.json")
    if not os.path.exists(cfg_path):
        sys.exit("missing %s - copy config.example.json and fill in real values" % cfg_path)
    with io.open(cfg_path, "r", encoding="utf-8") as fh:
        cfg = json.load(fh)
    try:
        python_version = resolve_python_version(cfg, args.python_version)
    except ValueError as exc:
        ap.error(str(exc))

    script = os.path.join(RECON, "probes", args.probe + ".py")
    if not os.path.exists(script):
        sys.exit("no such probe: %s" % script)

    out = os.path.join(RECON, "out", args.probe + ".json")
    if os.path.exists(out):
        os.remove(out)

    exe = os.path.join(cfg["multi_root"], "mpythonrun.exe")
    cmd = [exe]
    configured_router_port = int(cfg.get("service_router_port", 0) or 0)
    service_router_port = (
        0
        if args.cold
        else args.service_router_port
        if args.service_router_port is not None
        else configured_router_port
    )
    if args.probe in WARM_ONLY_PROBES and not service_router_port:
        ap.error("%s requires a live service-router port" % args.probe)
    if service_router_port:
        cmd += ["-sr_connect_servicerouter_host", "127.0.0.1",
                "-sr_connect_servicerouter_port", str(service_router_port)]
    cmd += ["-f", script]
    probe_args = list(rest)
    if args.probe in ("p09_m2_program_breakpoint",
                      "p11_m2_owned_breakpoint_recovery",
                      "p14_execution_domain_experiment"):
        probe_args += ["--live-router-port", str(args.service_router_port)]
    if probe_args:
        cmd += ["-args"] + probe_args

    started = time.time()
    proc = subprocess.Popen(
        cmd,
        cwd=RECON,
        creationflags=CREATE_NEW_CONSOLE,
        env=embedded_python_environment(python_version),
    )
    _, timed_out = wait_for_process(proc, args.timeout)
    if timed_out:
        try:
            kill_timed_out_process(proc)
        except (OSError, RuntimeError) as exc:
            sys.exit("TIMEOUT after %.1fs - cleanup failed: %s" %
                     (time.time() - started, exc))
        print("TIMEOUT after %.1fs - probe exited" % (time.time() - started))

    if not os.path.exists(out):
        sys.exit("probe produced no evidence file: %s" % out)

    with io.open(out, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    print("probe   :", data.get("probe"))
    print("seconds :", round(data.get("total_seconds", 0.0), 3))
    print("fatal   :", data.get("fatal"))
    for step in data.get("steps", []):
        flag = "ok " if step.get("ok") else "ERR"
        print("  [%s] %-40s %8.3fs" % (flag, step.get("label"), step.get("seconds", 0.0)))
    print("evidence:", out)


if __name__ == "__main__":
    main()
