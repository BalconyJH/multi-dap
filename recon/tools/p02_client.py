"""Host-side client for the two phases of p02_socket.py."""

import io
import json
import os
import socket
import time


RECON = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(RECON, "out")
PHASE_FILE = os.path.join(OUT, "p02_port.json")
RESULT = os.path.join(OUT, "p02_client.json")
REQUESTS = 64
TIMEOUT = 45.0
MONOTONIC_CLOCK = getattr(time, "perf_counter", time.time)


def wait_for_phase(name, deadline):
    while time.time() < deadline:
        try:
            with io.open(PHASE_FILE, "r", encoding="utf-8") as fh:
                state = json.load(fh)
            if state.get("phase") == name:
                return int(state["port"])
        except (IOError, OSError, ValueError, KeyError):
            pass
        time.sleep(0.05)
    raise RuntimeError("timed out waiting for phase %s" % name)


def run_phase(name, port):
    sock = socket.create_connection(("127.0.0.1", port), timeout=5.0)
    stream = sock.makefile("rb")
    latencies = []
    try:
        for index in range(REQUESTS):
            started = MONOTONIC_CLOCK()
            sock.sendall(("ping %d\n" % index).encode("ascii"))
            reply = stream.readline()
            latencies.append(MONOTONIC_CLOCK() - started)
            if reply != b"pong\n":
                raise RuntimeError("unexpected reply %r" % (reply,))
        sock.sendall(b"done\n")
        if stream.readline() != b"done\n":
            raise RuntimeError("phase did not acknowledge completion")
    finally:
        stream.close()
        sock.close()
    return {
        "phase": name,
        "requests": REQUESTS,
        "latency_seconds": latencies,
        "max_latency_seconds": max(latencies),
        "mean_latency_seconds": sum(latencies) / len(latencies),
    }


def main():
    deadline = time.time() + TIMEOUT
    results = []
    for phase in ("threaded", "select"):
        port = wait_for_phase(phase, deadline)
        results.append(run_phase(phase, port))
    with open(RESULT, "wb") as fh:
        fh.write(json.dumps({"phases": results}, indent=2, sort_keys=True).encode("utf-8"))
    print(RESULT)


if __name__ == "__main__":
    main()
