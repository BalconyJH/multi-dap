# Python 2.7. M0-2: can a resident socket loop coexist with MULTI calls?

import json
import os
import select
import socket
import sys
import threading
import time

sys.path.insert(0, os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "lib"))
import reconlib


PHASE_FILE = os.path.join(reconlib.OUT_DIR, "p02_port.json")
PHASE_TIMEOUT = 30.0


def publish_phase(name, port):
    if not os.path.isdir(reconlib.OUT_DIR):
        os.makedirs(reconlib.OUT_DIR)
    tmp = PHASE_FILE + ".tmp"
    f = open(tmp, "wb")
    try:
        f.write(json.dumps({"phase": name, "port": port}).encode("ascii"))
    finally:
        f.close()
    if os.path.exists(PHASE_FILE):
        os.remove(PHASE_FILE)
    os.rename(tmp, PHASE_FILE)


def new_listener():
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", 0))
    listener.listen(1)
    return listener


def threaded_phase(session):
    listener = new_listener()
    counts = {"accepts": 0, "requests": 0, "multi_calls": 0}
    done = threading.Event()
    errors = []

    def serve():
        conn = None
        stream = None
        try:
            listener.settimeout(PHASE_TIMEOUT)
            conn, unused_addr = listener.accept()
            counts["accepts"] += 1
            stream = conn.makefile("rb")
            while not done.is_set():
                line = stream.readline()
                if not line:
                    break
                command = line.strip()
                if command == "done":
                    conn.sendall("done\n")
                    done.set()
                    break
                counts["requests"] += 1
                conn.sendall("pong\n")
        except Exception as exc:
            errors.append(repr(exc))
            done.set()
        finally:
            if stream is not None:
                stream.close()
            if conn is not None:
                conn.close()

    worker = threading.Thread(target=serve)
    worker.daemon = True
    worker.start()
    publish_phase("threaded", listener.getsockname()[1])
    deadline = time.time() + PHASE_TIMEOUT
    statuses = {}
    while not done.is_set() and time.time() < deadline:
        status = session.GetStatus()
        statuses[status] = statuses.get(status, 0) + 1
        counts["multi_calls"] += 1
    if not done.is_set():
        errors.append("phase timeout")
        done.set()
    listener.close()
    worker.join(2.0)
    counts["thread_alive_after_join"] = worker.is_alive()
    counts["status_histogram"] = statuses
    counts["errors"] = errors
    return counts


def select_phase(session):
    listener = new_listener()
    listener.setblocking(False)
    publish_phase("select", listener.getsockname()[1])
    deadline = time.time() + PHASE_TIMEOUT
    conn = None
    buffer_ = ""
    done = False
    counts = {"accepts": 0, "requests": 0, "multi_calls": 0, "errors": []}
    statuses = {}
    try:
        while not done and time.time() < deadline:
            readers = [listener]
            if conn is not None:
                readers.append(conn)
            ready, unused_writers, unused_errors = select.select(readers, [], [], 0.005)
            if listener in ready:
                conn, unused_addr = listener.accept()
                conn.setblocking(False)
                counts["accepts"] += 1
            if conn is not None and conn in ready:
                chunk = conn.recv(4096)
                if not chunk:
                    break
                buffer_ += chunk
                while "\n" in buffer_:
                    line, buffer_ = buffer_.split("\n", 1)
                    if line.strip() == "done":
                        conn.sendall("done\n")
                        done = True
                        break
                    counts["requests"] += 1
                    conn.sendall("pong\n")
            status = session.GetStatus()
            statuses[status] = statuses.get(status, 0) + 1
            counts["multi_calls"] += 1
        if not done:
            counts["errors"].append("phase timeout or client disconnect")
    except Exception as exc:
        counts["errors"].append(repr(exc))
    finally:
        if conn is not None:
            conn.close()
        listener.close()
    counts["status_histogram"] = statuses
    return counts


rec = reconlib.Recorder("p02_socket")
session = None
try:
    if os.path.exists(PHASE_FILE):
        os.remove(PHASE_FILE)
    cfg = rec.step("load_config", reconlib.load_config)
    if cfg is None:
        raise RuntimeError("configuration could not be loaded")
    session = rec.step("bind_existing_program_window",
                       lambda: reconlib.bind_existing_program_window(cfg))
    if session is None:
        raise RuntimeError("session could not be opened")
    threaded = rec.step("threaded_socket_and_multi", lambda: threaded_phase(session))
    if (threaded is None or threaded.get("errors") or
            threaded.get("thread_alive_after_join") or
            threaded.get("requests") != 64 or threaded.get("multi_calls", 0) == 0):
        raise RuntimeError("threaded socket phase did not satisfy its contract")
    selected = rec.step("select_socket_and_multi", lambda: select_phase(session))
    if (selected is None or selected.get("errors") or
            selected.get("requests") != 64 or selected.get("multi_calls", 0) == 0):
        raise RuntimeError("select socket phase did not satisfy its contract")
except Exception:
    rec.fatal()
finally:
    if os.path.exists(PHASE_FILE):
        os.remove(PHASE_FILE)
    if session is not None:
        reconlib.close_session(session)
    rec.finish()
