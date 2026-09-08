"""Best-effort loopback breakpoint hint notifier for MULTI-Python 2.7."""

from __future__ import absolute_import

import json
import os
import socket

try:
    unicode
except NameError:  # Python 3 host tests; MULTI itself supplies this name.
    unicode = str


_HOST = "127.0.0.1"
_PORT = os.environ.get("MULTIDAP_HINT_PORT")
_NONCE = os.environ.get("MULTIDAP_HINT_NONCE")


def configure(port, nonce, host="127.0.0.1"):
    """Set the daemon-provided loopback hint destination for this interpreter."""
    global _HOST, _PORT, _NONCE
    if host not in ("127.0.0.1", "::1"):
        _PORT = None
        _NONCE = None
        return
    _HOST = host
    _PORT = port
    _NONCE = nonce


def notify(token):
    """Send exactly one nonce-bound UDP hint and never disturb MULTI on failure."""
    try:
        if _PORT is None or _NONCE is None:
            return
        packet = json.dumps(
            {"nonce": _NONCE, "token": int(token)},
            ensure_ascii=False,
            separators=(",", ":"),
        )
        if isinstance(packet, unicode):
            packet = packet.encode("utf-8")
        family = socket.AF_INET6 if _HOST == "::1" else socket.AF_INET
        sender = socket.socket(family, socket.SOCK_DGRAM)
        try:
            sender.sendto(packet, (_HOST, int(_PORT)))
        finally:
            sender.close()
    except Exception:
        pass
