# Zed Configuration

The repository provides a registered `multi-dap` source extension for local
Windows Zed projects. It launches the existing authenticated stdio proxy and
attaches to a daemon that is already running. The extension is not published
to the Zed registry or included in the Windows daemon ZIP.

This integration is `SOFTWARE_ONLY`: host contract tests and a compiled Wasm
extension establish the implementation boundary. Actual Zed registration,
GUI behavior, and target acceptance remain separate gates.

## Install the source extension

The source is in `editors/zed`. Prepare the pinned build environment:

```powershell
rustup toolchain install 1.95.0 --profile minimal --component rustfmt --component clippy --target wasm32-wasip2
cargo +1.95.0 fetch --locked --manifest-path editors/zed/Cargo.toml
cargo +1.95.0 build --locked --release --target wasm32-wasip2 --manifest-path editors/zed/Cargo.toml
```

In Zed, run `zed: install dev extension` and select `editors/zed`. Its
`extension.toml` registers the adapter and its JSON schema; a standalone
`debug.json` cannot provide that registration. The implementation pins the
published extension API 0.7.0 and the Wasm target used by the Zed 1.26.2
extension builder. The extension-local `rust-toolchain.toml` pins Rust 1.95.0
for Zed's plain `cargo` invocation as well as the explicit commands above.

The source extension README contains the complete host-test commands and
dependency notices. It does not download or install a daemon executable.

## Select the executable and daemon

Set the local executable in Zed settings:

```json
{
  "dap": {
    "multi-dap": {
      "binary": "C:\\tools\\multi-dap\\multi-dap.exe"
    }
  }
}
```

If no binary is specified, the extension asks the worktree to find
`multi-dap.exe` on `PATH`. An invalid explicit binary fails without a fallback.
The extension owns its proxy arguments; do not rely on arbitrary adapter
argument or environment overrides.

Prepare the daemon separately using the intended acquisition mode from the
[operations guide](operations.md). Then create `.zed/debug.json`:

```json
[
  {
    "label": "Attach to multi-dap",
    "adapter": "multi-dap",
    "request": "attach",
    "configFile": "C:\\private\\project.toml",
    "controlDirectory": "C:\\private\\multi-dap-control"
  }
]
```

Use the same TOML and control directory as the daemon. Omit
`controlDirectory` when the daemon uses the default directory. The extension
launches `proxy --config ...`, authenticates through that configuration-bound
control path, and sends an empty DAP attach body. It never reads tokens or
replaces the proxy with a raw DAP port.

Only attach is supported. Unknown adapter fields, TCP templates, host/port
overrides, launch, implicit acquisition, and program/PID selection are
rejected. All paths must be fully qualified Windows drive or UNC paths without
control characters or unresolved task variables. The extension does not
resolve relative paths or interpret arguments through a shell.

Use a local Windows workspace. API 0.7.0 does not expose a remote-workspace
flag, so the extension cannot reliably identify a remote Windows worktree;
remote usage is unsupported and has no acceptance evidence.

## Optional host capture

Add `perfOutput` with a fresh absolute output filename to the same debug task.
Optional `perfMaxEvents` must be in `1..100000` and requires `perfOutput`.
The existing directory must be private, and an existing output file is never
overwritten. The proxy creates and owns the capture; the extension performs
no native trace collection. See [performance capture](performance.md) for
finalization and interpretation.

## Remaining acceptance

After development installation, verify adapter registration, the attach
configuration barrier, configured-core stacks, supported inspection,
non-terminating disconnect, and any requested capture in the real editor.
Software fixtures do not replace those observations or authorize target
execution. The [capability matrix](capabilities.md) remains authoritative for
the daemon's supported operations.
