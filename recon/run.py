"""Host-side launcher for M0 reconnaissance probes.

mpythonrun.exe writes stdout with WriteConsole and pops a modal error dialog if its
standard handles are redirected, so this launcher gives it a fresh console and reads
the probe's result from its JSON evidence file instead.
"""

import argparse
import json
import os
import subprocess
import sys
import time

RECON = os.path.dirname(os.path.abspath(__file__))
CREATE_NEW_CONSOLE = 0x00000010


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("probe", help="probe stem, e.g. p06_poll")
    ap.add_argument("--timeout", type=float, default=600.0)
    ap.add_argument("rest", nargs=argparse.REMAINDER)
    args = ap.parse_args()

    cfg_path = os.path.join(RECON, "config.local.json")
    if not os.path.exists(cfg_path):
        sys.exit("missing %s - copy config.example.json and fill in real values" % cfg_path)
    with open(cfg_path, encoding="utf-8") as fh:
        cfg = json.load(fh)

    script = os.path.join(RECON, "probes", args.probe + ".py")
    if not os.path.exists(script):
        sys.exit("no such probe: %s" % script)

    out = os.path.join(RECON, "out", args.probe + ".json")
    if os.path.exists(out):
        os.remove(out)

    exe = os.path.join(cfg["multi_root"], "mpythonrun.exe")
    cmd = [exe, "-f", script]
    if args.rest:
        cmd += ["-args"] + [a for a in args.rest if a != "--"]

    started = time.time()
    proc = subprocess.Popen(cmd, cwd=RECON, creationflags=CREATE_NEW_CONSOLE)
    try:
        proc.wait(timeout=args.timeout)
    except subprocess.TimeoutExpired:
        proc.kill()
        print("TIMEOUT after %.1fs - probe killed" % (time.time() - started))

    if not os.path.exists(out):
        sys.exit("probe produced no evidence file: %s" % out)

    with open(out, encoding="utf-8") as fh:
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
