# multi-dap VS Code integration

This extension connects to a local `multi-dap` daemon. By default the daemon
must already be running; an explicit acquisition choice can prepare it before
attach. It registers
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

After variable substitution, every supplied path must be fully qualified and
contain no control characters. On Windows, use a drive-qualified path or UNC
share path; drive-relative, root-relative, and device paths are rejected.
The extension rejects `launch`, `host`, `port`, and `debugServer` to prevent bypassing the
authenticated local proxy. It always runs in the local UI extension host, so both the TOML file and executable
must be locally accessible. Remote and virtual workspaces are rejected by the
configuration provider and adapter factory. A configuration outside a workspace
can use explicit absolute paths.

If `serve` fails, the daemon is absent, or the control record cannot be authenticated, `proxy` exits and
VS Code reports a startup error. Ordinary attach never attempts acquisition.

## Optional daemon acquisition

To prepare a daemon before attaching, explicitly choose `acquisition` and
provide an absolute `bridgeScript` path after variable substitution. Use a
build whose help includes the `ensure` command and its acquisition options.
For a new cold session:

```json
{
  "name": "Prepare cold session and attach",
  "type": "multi-dap",
  "request": "attach",
  "configFile": "${workspaceFolder}/project.toml",
  "acquisition": "cold",
  "bridgeScript": "C:\\tools\\multi-dap\\bridge\\bridge.py"
}
```

Cold acquisition reuses an authenticated matching daemon when one exists.
Otherwise it creates a new MULTI connection using the selected project
configuration. Choose it only when a new connection is intended.

To reuse an operator-owned MULTI session, select `"acquisition": "warm"` and
add `"primaryElf": "C:\\work\\images\\primary.elf"` with the exact configured
primary ELF identity. Warm acquisition never falls back to cold. `primaryElf`
is required for warm acquisition and forbidden for cold acquisition;
`bridgeScript` and `primaryElf` cannot appear without an acquisition choice.
The extension does not infer these values.

The factory runs:

```text
multi-dap ensure --config <absolute-project.toml>
  --bridge-script <absolute-bridge.py> --acquisition <cold|warm>
  [--primary-elf <absolute-primary.elf>] [--control-dir <absolute-directory>]
```

Each argument is passed separately without a shell. The command has a
120-second host timeout and a 16 KiB limit for each captured output stream;
the daemon also applies the configured startup deadline. A successful exit
and complete `ready` or `already_ready` receipt are required before creating
the authenticated proxy. The same `controlDirectory`, when supplied, is used
for both commands. No endpoint or token is copied from the receipt into the
proxy arguments.

Failure stops this attach attempt without retrying acquisition or exposing
raw diagnostics in the editor error. A timeout terminates the owned `ensure`
process; it does not prove that a daemon or MULTI session was never created.
Inspect `multi-dap diagnose --config <absolute-project.toml>` and
`multi-dap status --config <absolute-project.toml>` with the same control
directory before deciding to retry or shut down. The extension does not
automatically stop a daemon when debugging ends.

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
existing configuration-bound authenticated proxy; selecting host capture does
not itself request daemon acquisition or bypass frontend ownership. The resulting Chrome Trace JSON can be inspected
with a compatible viewer; see the repository's `docs/performance.md`.

## Optional native interval capture

After attaching and completing configuration, run **multi-dap: Capture Existing
Native Trace** from the Command Palette or the debug toolbar. The active
`multi-dap` session must advertise native trace request support during
initialization. The command keeps using that exact debug session if the
active editor changes.

Choose the existing live session or an already-loaded snapshot, and enter
its native session ID (`1..2147483647`) and an explicit source identity.
Optional core identity must name a configured core; optional image identity
is a SHA-256 digest. Snapshot capture also requires the snapshot SHA-256.
These identities are operator declarations and are not inferred from the
active editor, PID, port, image path, or saved settings. The command requires
acknowledgement that source/core association remains unverified.

Choose an unsigned 64-bit decimal first-packet index and a count in `1..4096`,
then select a new local file in an existing private directory. The index and
native generation remain decimal strings across the editor boundary, including
values above JavaScript's safe integer range. Existing files are never
overwritten. The Go helper exclusively reserves and writes the private
capture; the extension itself does not create the artifact.

Use a local `multi-dap` build that provides `trace capture-editor`. The
extension launches the configured executable with separate arguments and
the session's configuration and optional control directory. The helper
validates those inputs without contacting the control endpoint or creating
another proxy. Its bounded protocol requests `status`, `open`, `poll`, `read`,
and `close` through the original session's `customRequest` API. Closing the
native handle leaves ordinary debugging attached.

This command reads one interval of existing packets. It does not start or
stop collection, execute the target, load a snapshot, or acquire a daemon.
Native close confirms disconnection from the selected source; collection
may continue. The resulting capture remains unqualified for actual native
runtime or hardware behavior. It can be validated and normalized using
`multi-dap trace normalize`.

Cancellation, timeouts, and session changes stop only the owned helper.
An already-sent debug request may still settle later. The extension blocks
another capture while such a request or helper remains pending, and retains
uncertainty across replacement trackers for the same session. Run
**multi-dap: Inspect Native Trace Status** for a read-only state check. It
clears the editor's reconciliation flag only after outstanding requests
settle and authoritative local status confirms a clean idle or closed state.
An open, faulted, or uncertain source needs the documented operator
reconciliation workflow; the editor does not attempt automatic cleanup.
Errors use fixed categories and do not echo raw DAP failures or helper
diagnostics.

## Validation and release contents

```powershell
npm test
npm run lint
npm run package-contents
```

The last command lists the four files required for publishing; `.vscodeignore` excludes tests, scripts,
dependency directories, and common temporary packages.

Host tests cover configuration/schema agreement, local workspace admission,
Windows and POSIX path handling, substituted paths, literal arguments,
bootstrap ordering and failure handling, and proxy descriptor admission.
Native capture tests cover source selection and cancellation, lifecycle
admission, literal helper arguments, protocol bounds, request allowlisting,
late responses, same-ID tracker replacement, and reconciliation exclusion.
Owned Node subprocess tests exercise bounded output and timeout handling
without launching MULTI. These checks do not establish real VS Code GUI,
acquisition, capture acceptance, or hardware profiling support.

## Real Extension Host acceptance

An opt-in test opens an installed official VS Code executable with isolated
user data, extensions, settings, and a synthetic workspace:

```powershell
npm run test:editor -- --code "C:\path\to\Code.exe"
```

The runner builds the production `multi-dap` command and a source-only
synthetic backend. It exercises the actual extension, configuration-bound
authenticated control connection, stdio proxy, and production DAP server.
The fixture never constructs the MULTI runtime, executes vendor tools, loads
a target image, or accesses hardware. An intentionally unusable synthetic
`mpythonrun.exe` ensures an accidental acquisition cannot pass.

Five sessions cover default attach, explicit cold and warm reuse of the
already-ready fixture daemon, optional host capture, and native interval
capture. The real editor
issues initialize/attach/configurationDone, threads, stack, scopes, variables,
evaluate, and disconnect. Request trackers retain only commands, event names,
sequence numbers, and success flags. The tests verify that default attach
does not probe readiness, explicit acquisition does, and capture finalization
produces valid, complete host-observation JSON.

The native case uses dependency-injected prompt responses with the production
capture controller; it does not add a production test command or assert visual
dialog behavior. The actual session relays native requests through its one
authenticated proxy, then successfully executes ordinary threads/evaluate
requests after native close. The real Go helper writes the private capture,
and the Go normalizer validates three synthetic packets, one completed
function span, and a 100,000,000-picosecond duration. Large generation and
time-tag values test lossless decimal-string transport. Runtime and hardware
qualification remain false.

The test requires Go and an explicit installed editor executable; it does not
download an editor or modify an existing editor profile. Processes have bounded
lifetimes, and failure cleanup targets only the spawned process trees. Logs and
the sanitized result are retained under ignored `tmp/vscode-editor-acceptance-*`;
synthetic configuration, owner-restricted control records, captures, and editor
data use a separate system temporary directory. Ordinary `npm test`, package
checks, and release acceptance do not launch this editor test.

This is real editor/proxy integration with synthetic target data. It does not
establish visual UI correctness, actual MULTI cold/warm acquisition, probe
behavior, or target execution. The fixture, runner, and tests are excluded from
the extension package.

On 2026-10-30, all five sessions passed in official VS Code Insiders
1.145.0-insider (commit `f8c98dc6a2efcf8e86977254e1d4cf56b2542dbc`).
The capture contained twelve completed requests and eighteen trace events,
with no unfinished requests or framing errors. It was durably finalized at
`disconnect_response`, before the editor could terminate the proxy;
`proxy_exit_observed` remained false. The isolated fixture control record and
all acceptance-owned processes were gone after completion.
