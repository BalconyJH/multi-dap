# multi-dap VS Code integration

This extension supports connections only to an **already running** local `multi-dap` daemon. It registers
a configuration provider and `DebugAdapterDescriptorFactory` for the `multi-dap` debug type, and invokes the supported:

```text
multi-dap proxy --config <absolute-project.toml> [--control-dir <absolute-directory>]
  [--perf-output <absolute-new-capture.json> [--perf-max-events <1..100000>]]
```

`proxy` first authenticates the daemon through its private control record, then relays DAP bytes between
standard input and output. The extension does not read, log, or pass control tokens, target-server arguments,
or router connection arguments.

## Usage

1. Start the daemon from a controlled terminal, for example: `multi-dap serve --config C:\\work\\project.toml`.
2. Install this extension and ensure that `multiDap.executable` points to the local `multi-dap` executable
   (the default is `multi-dap`, resolved through `PATH`).
3. Create `.vscode/launch.json`:

```json
{
  "version": "0.2.0",
  "configurations": [
    {
      "name": "Attach to multi-dap",
      "type": "multi-dap",
      "request": "attach",
      "configFile": "${workspaceFolder}/project.toml"
    }
  ]
}
```

After variable substitution, `configFile` must be an absolute path; if supplied, `controlDirectory` must
also be absolute. The extension rejects `launch`, `host`, `port`, and `debugServer` to prevent bypassing the
authenticated local proxy. It always runs in the local UI extension host, so both the TOML file and executable
must be locally accessible; remote workspaces are not currently supported.

If `serve` fails, the daemon is absent, or the control record cannot be authenticated, `proxy` exits and
VS Code reports a startup error. The extension does not attempt to cold- or warm-start `serve` itself because
the current CLI requires the user to explicitly provide session inputs, bridge paths, and lifecycle choices.

## Optional host-latency capture

Host capture is off by default. To record the proxy's observed DAP request
latency and state events, explicitly add `perfOutput` to one attach
configuration. `perfMaxEvents` is optional, must be an integer in `1..100000`,
and requires `perfOutput`; omission uses the CLI's default retained-event limit.
Use a `multi-dap` build whose help lists `--perf-output`; an older executable
will fail startup rather than silently attach without recording.

```json
{
  "name": "Attach with host-latency capture",
  "type": "multi-dap",
  "request": "attach",
  "configFile": "${workspaceFolder}/project.toml",
  "perfOutput": "C:\\private\\host-001.trace.json",
  "perfMaxEvents": 10000
}
```

Replace the illustrative output path with a new file in an existing private
directory. After VS Code variable substitution, `perfOutput` must be an
absolute, non-empty path without control characters. The extension passes it
as a separate argument, not a shell command, and does not read or create the
file itself. The proxy owns exclusive private-file creation. Choose a fresh
filename for each session: existing files are never overwritten, and the
extension does not retry with another name or create directories.

This is **host DAP latency**, not native CPU profiling, target function
tracing, or proof of time spent exclusively in MULTI. Capture adds no target
operations and retains no request arguments, response bodies, source paths,
symbols, target memory, or control tokens. As with ordinary attach, it uses the
existing configuration-bound authenticated proxy; it does not start a daemon
or bypass frontend ownership. The resulting Chrome Trace JSON can be inspected
with a compatible viewer; see the repository's `docs/performance.md`.

## Validation and release contents

```powershell
npm test
npm run lint
npm run package-contents
```

The last command lists the three files required for publishing; `.vscodeignore` excludes tests, scripts,
dependency directories, and common temporary packages.

Host tests cover configuration/schema agreement, substituted paths, bounds,
literal arguments, and descriptor/provider admission. They do not establish
real VS Code GUI capture acceptance or hardware profiling support.
