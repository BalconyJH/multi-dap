"""Dependency-free PEP 517 backend for repackaging a verified Windows ZIP.

No compilation, downloads, vendor execution, or source-distribution fallback.
The input must be explicitly selected with -C archive=<absolute ZIP path>.
"""

import base64
import csv
from datetime import datetime, timezone
import hashlib
import io
import json
from pathlib import Path
import re
import struct
import zipfile


ROOT = Path(__file__).resolve().parent
TAG = "py3-none-win_amd64"
PAYLOAD = frozenset({
    "multi-dap.exe", "bridge/bridge.py", "bridge/mbp_wire.py",
    "bridge/timemachine_helper.py", "bridge/timemachine_native.py",
    "editors/clion/README.md", "editors/clion/profile-contract.json",
    "examples/multi-dap.example.toml", "README.md", "LICENSE.txt",
    "SHA256SUMS.txt", "release-provenance.json", "THIRD_PARTY_NOTICES.md",
    "licenses/Go.txt", "licenses/go-toml.txt",
})
MAX_ARCHIVE_BYTES = 128 * 1024 * 1024


def python_version(native_version):
    """Map a deliberately limited, unambiguous subset of SemVer to PEP 440."""
    number = r"(?:0|[1-9][0-9]*)"
    match = re.fullmatch(
        rf"({number}\.{number}\.{number})(?:-(dev|ci|alpha|beta|rc)(?:\.({number}))?)?",
        native_version,
    )
    if not match:
        raise ValueError("unsupported release version; use X.Y.Z[-dev|ci|alpha|beta|rc[.N]]")
    release, phase, serial = match.groups()
    if phase is None:
        return release
    if phase == "ci":
        return release + ".dev" + (serial or "0") + "+ci"
    return release + {"dev": ".dev", "alpha": "a", "beta": "b", "rc": "rc"}[phase] + (serial or "0")


def _checksum_manifest(data):
    checksums = {}
    for line in data.decode("utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  (.+)", line)
        if not match or match[2] in checksums:
            raise ValueError("invalid or duplicate checksum manifest entry")
        checksums[match[2]] = match[1]
    return checksums


def _read_bundle(config_settings):
    settings = config_settings or {}
    if set(settings) != {"archive"} or not isinstance(settings["archive"], str):
        raise ValueError("pass exactly one build setting: -C archive=<absolute ZIP path>")
    archive_path = Path(settings["archive"])
    if not archive_path.is_absolute() or not archive_path.is_file():
        raise ValueError("archive must be an existing absolute ZIP path")
    if archive_path.stat().st_size > MAX_ARCHIVE_BYTES:
        raise ValueError("archive exceeds the wheel build size limit")
    raw = archive_path.read_bytes()
    digest = hashlib.sha256(raw).hexdigest()
    outer = _checksum_manifest(Path(str(archive_path) + ".sha256").read_bytes())
    if outer != {archive_path.name: digest}:
        raise ValueError("archive checksum mismatch")
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        entries = archive.infolist()
        if len(entries) != len(PAYLOAD) or {entry.filename for entry in entries} != PAYLOAD:
            raise ValueError("archive contents do not match the Windows bundle allowlist")
        if sum(entry.file_size for entry in entries) > MAX_ARCHIVE_BYTES:
            raise ValueError("expanded archive exceeds the wheel build size limit")
        # No archive paths are extracted. The exact allowlist also rejects
        # duplicates, directory entries, traversal, and unexpected code/data.
        payload = {entry.filename: archive.read(entry) for entry in entries}
    expected = _checksum_manifest(payload["SHA256SUMS.txt"])
    actual = {name: hashlib.sha256(data).hexdigest()
              for name, data in payload.items() if name != "SHA256SUMS.txt"}
    if expected != actual:
        raise ValueError("bundle payload checksum mismatch")
    provenance = json.loads(payload["release-provenance.json"])
    if (provenance.get("schema") != "multi-dap.release-provenance/v1"
            or provenance.get("package") != "multi-dap"
            or provenance.get("target") != {"os": "windows", "architecture": "amd64"}):
        raise ValueError("bundle provenance must describe multi-dap for Windows amd64")
    binary = payload["multi-dap.exe"]
    if provenance.get("binary") != {
        "path": "multi-dap.exe", "size": len(binary), "sha256": actual["multi-dap.exe"],
    }:
        raise ValueError("executable does not match bundle provenance")
    if len(binary) < 64 or binary[:2] != b"MZ":
        raise ValueError("bundled executable has no DOS header")
    pe_offset = struct.unpack_from("<I", binary, 60)[0]
    if binary[pe_offset:pe_offset + 6] != b"PE\0\0\x64\x86":
        raise ValueError("bundled executable is not an AMD64 PE image")
    version = python_version(provenance["version"])
    if archive_path.name != f"multi-dap-{provenance['version']}-windows-amd64.zip":
        raise ValueError("archive filename does not match bundle provenance")
    epoch = provenance.get("source_date_epoch")
    if type(epoch) is not int or not 315532800 <= epoch <= 4354819199:
        raise ValueError("bundle source_date_epoch must fit the ZIP timestamp range")
    return payload, provenance, version, digest


def build_wheel(wheel_directory, config_settings=None, metadata_directory=None):
    payload, provenance, version, digest = _read_bundle(config_settings)
    info = f"multi_dap-{version}.dist-info"
    files = {f"multi_dap/_bundle/{name}": data for name, data in payload.items()}
    # Normalize only our launcher source. Every original ZIP byte is preserved.
    files["multi_dap/__init__.py"] = (ROOT / "multi_dap" / "__init__.py").read_bytes().replace(b"\r\n", b"\n")
    files["multi_dap/bundle-origin.json"] = (json.dumps({
        "schema": "multi-dap.wheel-origin/v1",
        "archive_sha256": digest,
        "native_version": provenance["version"],
        "wheel_version": version,
    }, sort_keys=True, separators=(",", ":")) + "\n").encode()
    files[f"{info}/METADATA"] = (
        "Metadata-Version: 2.4\nName: multi-dap\n"
        f"Version: {version}\n"
        "Summary: Windows launcher and bundled Green Hills MULTI DAP adapter\n"
        "Requires-Python: >=3.10\nLicense-Expression: MIT\n"
        "License-File: LICENSE.txt\n"
        "Project-URL: Source, https://github.com/Tacrolimus/multi-dap\n\n"
        "Includes the Windows amd64 adapter and its bridge companions.\n"
        "Green Hills MULTI is a separately installed prerequisite.\n"
    ).encode()
    files[f"{info}/licenses/LICENSE.txt"] = payload["LICENSE.txt"]
    files[f"{info}/WHEEL"] = (
        "Wheel-Version: 1.0\nGenerator: multi-dap-wheel\n"
        f"Root-Is-Purelib: false\nTag: {TAG}\n"
    ).encode()
    files[f"{info}/entry_points.txt"] = b"[console_scripts]\nmulti-dap = multi_dap:main\n"
    record = io.StringIO(newline="")
    writer = csv.writer(record, lineterminator="\n")
    for name, data in sorted(files.items()):
        hashed = base64.urlsafe_b64encode(hashlib.sha256(data).digest()).rstrip(b"=").decode()
        writer.writerow([name, "sha256=" + hashed, len(data)])
    writer.writerow([f"{info}/RECORD", "", ""])
    files[f"{info}/RECORD"] = record.getvalue().encode()
    filename = f"multi_dap-{version}-{TAG}.whl"
    timestamp = datetime.fromtimestamp(provenance["source_date_epoch"], timezone.utc).timetuple()[:6]
    # Stored entries avoid compressor-version differences. Exclusive creation
    # prevents silently replacing a candidate already selected for review.
    with (Path(wheel_directory) / filename).open("xb") as stream:
        with zipfile.ZipFile(stream, "w", compression=zipfile.ZIP_STORED) as wheel:
            for name, data in sorted(files.items()):
                entry = zipfile.ZipInfo(name, timestamp)
                entry.create_system = 3
                entry.external_attr = 0o100644 << 16
                wheel.writestr(entry, data)
    return filename


class UnsupportedOperation(Exception):
    pass


def build_sdist(sdist_directory, config_settings=None):
    raise UnsupportedOperation("only wheels from an explicit release ZIP are supported; use uv build --wheel")
