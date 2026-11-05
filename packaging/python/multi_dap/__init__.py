"""Launch the adjacent native bundle; MULTI owns the bridge interpreter."""

from pathlib import Path
import platform
import struct
import subprocess
import sys


def bundle_directory() -> Path:
    return Path(__file__).resolve().parent / "_bundle"


def main() -> int:
    if (sys.platform != "win32" or struct.calcsize("P") != 8
            or platform.machine().lower() not in ("amd64", "x86_64")):
        print("multi-dap: this wheel requires Windows amd64 and 64-bit Python",
              file=sys.stderr)
        return 126

    executable = bundle_directory() / "multi-dap.exe"
    if not executable.is_file():
        print("multi-dap: bundled executable is missing; reinstall the wheel",
              file=sys.stderr)
        return 126

    try:
        # Inherit the original handles, environment, cwd and console. In
        # particular, never decode/re-encode DAP stdin/stdout or create a shell.
        child = subprocess.Popen([str(executable), *sys.argv[1:]], shell=False)
    except OSError as error:
        print(f"multi-dap: cannot start bundled executable (OS error {error.errno})",
              file=sys.stderr)
        return 126

    while True:
        try:
            code = child.wait()
            # Popen exposes an unsigned Windows DWORD. sys.exit uses a signed
            # C long on Windows; convert without changing the process exit bits.
            return code if code < 0x80000000 else code - 0x100000000
        except KeyboardInterrupt:
            # Windows delivers console Ctrl+C to the native child too. Let Go
            # finish its cleanup; subprocess.call/run would kill it on unwind.
            # Detached `ensure` children remain owned by the native lifecycle.
            continue
