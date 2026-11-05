"""Install/test/uninstall a local wheel in private uv directories, without MULTI."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import zipfile


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wheel", required=True, type=Path)
    args = parser.parse_args()
    wheel = args.wheel.resolve(strict=True)
    if sys.platform != "win32":
        parser.error("installed-wheel smoke requires Windows amd64")
    uv = shutil.which("uv")
    if uv is None:
        parser.error("uv must be on PATH")

    with tempfile.TemporaryDirectory(prefix="multi-dap-uv-smoke-") as temporary:
        root = Path(temporary).resolve()
        # These variables affect only child processes; the user's tool install,
        # PATH, shell profile and cache are never modified by this acceptance.
        environment = dict(os.environ, UV_TOOL_DIR=str(root / "tools"),
                           UV_TOOL_BIN_DIR=str(root / "bin"),
                           UV_CACHE_DIR=str(root / "cache"))
        project = root / "project space \u4e2d\u6587"
        project.mkdir()
        uv_command = [uv, "--no-config", "--offline", "--no-python-downloads"]

        def run(command):
            return subprocess.run(command, cwd=project, env=environment,
                                  capture_output=True, timeout=60)

        def require(result):
            if result.returncode != 0:
                raise RuntimeError(result.stderr.decode("utf-8", "replace"))
            return result.stdout

        require(run([*uv_command, "tool", "install", "--python", sys.executable, str(wheel)]))
        installed_python = root / "tools" / "multi-dap" / "Scripts" / "python.exe"
        bundle = Path(require(run([
            str(installed_python), "-I", "-c",
            "import multi_dap; print(multi_dap.bundle_directory())",
        ])).decode().strip())
        if not bundle.is_relative_to(root):
            raise RuntimeError("uv installed outside the isolated smoke directory")
        command = root / "bin" / "multi-dap.exe"
        native = bundle / "multi-dap.exe"
        if not command.is_file() or not native.is_file() or command == native:
            raise RuntimeError("installed entry point or native bundle is missing")

        with zipfile.ZipFile(wheel) as archive:
            origin = json.loads(archive.read("multi_dap/bundle-origin.json"))
            prefix = "multi_dap/_bundle/"
            for name in archive.namelist():
                if name.startswith(prefix):
                    installed = bundle / name.removeprefix(prefix)
                    if installed.read_bytes() != archive.read(name):
                        raise RuntimeError("installed bundle differs from wheel: " + name)

        # Compare actual native outputs, including failure exits, from a foreign
        # Unicode working directory. These commands cannot acquire a target.
        for arguments in (["version"], ["help"], ["invalid-wheel-smoke-command"],
                          ["check"], ["trace", "normalize"]):
            wrapped = run([str(command), *arguments])
            direct = run([str(native), *arguments])
            if (wrapped.returncode, wrapped.stdout, wrapped.stderr) != (
                    direct.returncode, direct.stdout, direct.stderr):
                raise RuntimeError("launcher changed native output/exit: " + str(arguments))
        version = json.loads(require(run([str(command), "version"])))
        if version["version"] != origin["native_version"]:
            raise RuntimeError("installed native version differs from wheel origin")
        # Reinstall the local candidate as well: it must not fetch a binary or
        # dependency, and the refreshed console entry point must still work.
        require(run([*uv_command, "tool", "install", "--reinstall", "--python", sys.executable, str(wheel)]))
        if json.loads(require(run([str(command), "version"]))) != version:
            raise RuntimeError("reinstall changed native version metadata")
        require(run([*uv_command, "tool", "uninstall", "multi-dap"]))
        if command.exists() or installed_python.exists():
            raise RuntimeError("uv uninstall left the tool entry point/environment behind")

    print(json.dumps({"event": "wheel_smoke_passed", "wheel": wheel.name,
                      "sha256": hashlib.sha256(wheel.read_bytes()).hexdigest(),
                      "native_version": version["version"],
                      "scope": "offline isolated install, payload, CLI, reinstall, uninstall; no MULTI/target"}))


if __name__ == "__main__":
    main()
