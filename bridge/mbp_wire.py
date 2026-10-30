"""Bounded UTF-8 NDJSON mechanisms shared by the Python 3/2 bridge."""

from __future__ import absolute_import

import json

try:
    text_type = unicode
    integer_type = long
except NameError:
    text_type = str
    integer_type = int
    binary_type = bytes
else:
    binary_type = str

unicode = text_type
long = integer_type


class BridgeError(Exception):
    """A request failure that has a defined MBP error envelope."""

    def __init__(self, kind, message):
        Exception.__init__(self, message)
        self.kind = kind
        self.message = message


def as_unicode(value):
    """Return a JSON string value as unicode, rejecting non-UTF-8 input."""
    if isinstance(value, unicode):
        return value
    if isinstance(value, binary_type):
        return value.decode("utf-8")
    raise BridgeError("invalid_params", "expected a UTF-8 string")


def multi_text(value):
    """Make MULTI output safe for the UTF-8 wire and report lossiness."""
    if value is None:
        return u"", False
    if isinstance(value, unicode):
        return value, False
    if isinstance(value, binary_type):
        try:
            return value.decode("utf-8"), False
        except UnicodeDecodeError:
            return value.decode("utf-8", "replace"), True
    return unicode(value), False


def json_value(value):
    """Recursively convert MULTI values to JSON values with unicode strings."""
    if isinstance(value, unicode):
        return value, False
    if isinstance(value, binary_type):
        return multi_text(value)
    if isinstance(value, dict):
        result = {}
        lossy = False
        for key, item in value.items():
            text_key, key_lossy = multi_text(key)
            text_item, item_lossy = json_value(item)
            result[text_key] = text_item
            lossy = lossy or key_lossy or item_lossy
        return result, lossy
    if isinstance(value, (list, tuple)):
        result = []
        lossy = False
        for item in value:
            text_item, item_lossy = json_value(item)
            result.append(text_item)
            lossy = lossy or item_lossy
        return result, lossy
    if value is None or isinstance(value, (bool, int, long, float)):
        return value, False
    return multi_text(value)


def encode_frame(frame):
    """Encode one frame as a UTF-8 NDJSON line."""
    text = json.dumps(frame, ensure_ascii=False, separators=(",", ":"))
    if not isinstance(text, unicode):
        text = text.decode("utf-8")
    return text.encode("utf-8") + b"\n"


def decode_frame(line):
    """Decode one UTF-8 JSON object frame."""
    try:
        text = line.decode("utf-8")
    except UnicodeDecodeError:
        raise BridgeError("protocol_error", "frame is not valid UTF-8")
    try:
        value = json.loads(text)
    except (TypeError, ValueError):
        raise BridgeError("protocol_error", "frame is not valid JSON")
    if not isinstance(value, dict):
        raise BridgeError("protocol_error", "frame must be a JSON object")
    return value


class BoundedNDJSONReader(object):
    """Read newline-delimited frames without buffering an unbounded line."""

    def __init__(self, connection, max_message_size):
        self.connection = connection
        self.max_message_size = max_message_size
        self.buffer = b""

    def read(self):
        while True:
            newline = self.buffer.find(b"\n")
            if newline >= 0:
                line = self.buffer[:newline]
                self.buffer = self.buffer[newline + 1:]
                if line.endswith(b"\r"):
                    line = line[:-1]
                if len(line) > self.max_message_size:
                    raise BridgeError("protocol_error", "frame exceeds max_message_size")
                return line
            if len(self.buffer) > self.max_message_size:
                raise BridgeError("protocol_error", "frame exceeds max_message_size")
            chunk = self.connection.recv(4096)
            if not chunk:
                if self.buffer:
                    raise BridgeError("protocol_error", "frame is not terminated by newline")
                return None
            self.buffer += chunk
            if len(self.buffer) > self.max_message_size and b"\n" not in self.buffer:
                raise BridgeError("protocol_error", "frame exceeds max_message_size")
