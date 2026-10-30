# multi-dap Zed integration

This source extension registers the `multi-dap` debug adapter with Zed. It uses
the existing authenticated local stdio proxy on Windows:

```text
multi-dap.exe proxy --config <absolute-project.toml>
  [--control-dir <absolute-control-directory>]
  [--perf-output <absolute-new-capture.json> [--perf-max-events <1..100000>]]
```

The daemon must already be running. The extension does not acquire a MULTI
session, download an adapter, choose a target, or start native trace collection.
It does not read control tokens or substitute a raw TCP connection.

The implementation is software-only until a real Zed editor and target session
complete separate acceptance. Host tests and a compiled Wasm extension do not
establish GUI, execution, or hardware behavior. The extension is not published
to the Zed extension registry and is not part of the Windows ZIP payload.

## Build and install for development

Use the checked-in lockfile and Rust 1.95.0. The extension-local
`rust-toolchain.toml` also selects this version when Zed's dev-extension
builder invokes plain `cargo` from this directory:

```powershell
rustup toolchain install 1.95.0 --profile minimal --component rustfmt,clippy
rustup target add wasm32-wasip2 --toolchain 1.95.0
cargo +1.95.0 fetch --locked --manifest-path editors/zed/Cargo.toml
cargo +1.95.0 fmt --check --manifest-path editors/zed/Cargo.toml
cargo +1.95.0 test --locked --manifest-path editors/zed/Cargo.toml
cargo +1.95.0 clippy --locked --all-targets --manifest-path editors/zed/Cargo.toml -- -D warnings
cargo +1.95.0 build --locked --release --target wasm32-wasip2 --manifest-path editors/zed/Cargo.toml
```

The compiled artifact is
`editors/zed/target/wasm32-wasip2/release/multi_dap_zed.wasm` unless
`CARGO_TARGET_DIR` selects another build directory. Build artifacts are ignored.
The extension uses the published `zed_extension_api` 0.7.0; the Wasm build
target follows the extension builder in Zed 1.26.2.

In Zed's command palette, run `zed: install dev extension` and select the
`editors/zed` directory. This registers the adapter declared in `extension.toml`
and its configuration schema. Zed builds the dev extension from its source;
placing a `debug.json` file by itself does not register an adapter.

## Configure an existing daemon

Configure the local executable in Zed settings:

```json
{
  "dap": {
    "multi-dap": {
      "binary": "C:\\tools\\multi-dap\\multi-dap.exe"
    }
  }
}
```

When no binary is specified, the extension asks the selected worktree to
locate `multi-dap.exe` on `PATH`. The result must be a fully qualified Windows
path. An invalid explicit binary is rejected without falling back to `PATH`.
The extension constructs its own proxy arguments; do not configure
`dap.multi-dap.args` or environment overrides as a way to change its transport.

Create `.zed/debug.json` in the intended local Windows project:

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

Omit `controlDirectory` if the daemon uses the default directory. The selected
configuration and control directory must match the existing daemon. Only
`request: "attach"` is supported. The extension passes the transport settings
to the proxy as separate arguments and sends `{}` as the DAP attach body.
No project path, endpoint, or control credential is added to that body.

Paths must be drive-qualified or UNC share paths, with no control characters
or unresolved `${...}` / `$ZED_...` task variables. Relative, drive-relative,
root-relative, and Windows device paths are rejected. The extension does not
interpret paths using a shell or resolve relative paths against a workspace.
Unknown adapter-specific fields, including host/port, `debugServer`,
acquisition, program/PID selection, and arbitrary arguments, are rejected.
Zed TCP connection templates are rejected as well.

This extension supports local Windows use only. The 0.7.0 Worktree API does
not expose a remote-workspace flag; it cannot reliably distinguish a remote
Windows worktree. Remote workspaces are unsupported and are not covered by
the host admission tests.

## Optional host-latency capture

Add `perfOutput` to one debug task to record the proxy's host DAP timing:

```json
[
  {
    "label": "Attach with host timing",
    "adapter": "multi-dap",
    "request": "attach",
    "configFile": "C:\\private\\project.toml",
    "perfOutput": "C:\\private\\zed-001.trace.json",
    "perfMaxEvents": 10000
  }
]
```

Choose a new file in an existing private directory. The proxy creates the
file exclusively with its existing private-file policy; the extension does
not create directories, overwrite output, or retry another path.
`perfMaxEvents` is optional, must be in `1..100000`, and requires `perfOutput`.
Omitting capture settings performs no recording.

This measures observed host DAP latency, not target CPU time. The extension
does not start collection or add target requests for capture. It cannot
qualify native TimeMachine behavior or real-editor overhead.

## Acceptance boundary

Host checks cover strict configuration decoding, duplicate and unknown
fields, Windows paths, exact proxy arguments, stdio-only transport, empty DAP
attach payloads, capture limits, and schema/manifest agreement. The release
Wasm build separately verifies compatibility with the selected SDK and target.

Real-editor acceptance still requires installing the dev extension and
observing adapter registration, normal attach/configuration completion,
configured-core stacks, supported inspection, and non-terminating disconnect.
Capture acceptance additionally requires checking the final private trace
after the proxy exits. Run those checks against an explicitly selected daemon;
hardware operations remain governed by the repository's operation-specific
authorization and evidence rules.

Upstream contracts used for this implementation are Zed's debugger-extension
guide and developing-extensions guide, the Zed 1.26.2 extension builder, and
the published 0.7.0 SDK's `get_dap_binary` / `dap_request_kind` hooks. See
`THIRD_PARTY_NOTICES.md` for the dependency license metadata.
