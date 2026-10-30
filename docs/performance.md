# Performance Timeline

`multi-dap proxy` can optionally record a bounded, payload-free timeline of the
DAP conversation it already forwards. The output is Chrome Trace Event JSON
for a compatible local viewer, including Perfetto.

This is **host-side debugging-path observability**, not a target profiler.
Request spans measure elapsed time at the proxy boundary. They can include
transport, daemon queueing, MULTI work, and waiting for a response, but do not
attribute that time to individual internal components or target functions.

The implementation is software-only: no new hardware profiling capability is
claimed. See [capabilities](capabilities.md) for the wider verification boundary
and [MULTI 8 adaptation](ghs-8-adaptation.md) for native tracing candidates.

## Capture a session

Use an already configured daemon and the same config-bound proxy used by the
editor. Add the following options to the proxy command:

```powershell
multi-dap.exe proxy --config C:\private\project.toml `
  --perf-output C:\private\session-001.trace.json `
  --perf-max-events 10000
```

This command is a stdio DAP adapter, not a standalone workload runner. An editor
or another DAP client must supply its normal DAP conversation over stdin/stdout.
The performance options do not start a daemon, attach an additional client,
change acquisition mode, or execute a target workload.

| Option | Contract |
|---|---|
| `--perf-output` | Absolute path to a **new** JSON file. The parent directory must already exist. Existing files are never overwritten. |
| `--perf-max-events` | Maximum retained request/event records: `1..100000`, default `10000`. Requires `--perf-output`. |
| `--config` | Required when performance recording is enabled. The legacy `--probe-id` proxy cannot enable it. |
| `--control-dir` | If the daemon uses a non-default directory, retain the same option on the proxy. |

Recording is disabled unless `--perf-output` is supplied. No TOML setting or
extra DAP request is needed. The recorder is local to this proxy process and
cannot observe a different frontend or earlier daemon activity.

For CLion, append the performance options to the existing Cidr DAP profile's
adapter arguments. Keep its transport as stdin/stdout, its Launch-tab
`{"request":"attach"}`, and its Before Launch `ensure` action unchanged. Use a
new output filename for each session; reusing a filename deliberately fails
before the proxy starts forwarding. See the [CLion guide](clion.md) for the
base profile.

For the source VS Code extension, the attach configuration can explicitly set
`perfOutput` and optional `perfMaxEvents`:

```json
{
  "name": "Attach with host timeline",
  "type": "multi-dap",
  "request": "attach",
  "configFile": "${workspaceFolder}/project.toml",
  "perfOutput": "C:\\private\\session-001.trace.json",
  "perfMaxEvents": 10000
}
```

The path must be absolute after variable substitution, and `perfMaxEvents`
requires an explicit output. Neither property is inserted by default. The
extension passes separate arguments to the same authenticated proxy; it does
not read tokens, create the file, or issue native trace
commands. The Go proxy retains exclusive/private-file enforcement. These
options passed real VS Code Insiders Extension Host capture with a synthetic
backend. Actual target capture through that editor and observer overhead remain
unverified. Explicit `acquisition` is a separate optional extension setting.

## Completion and file handling

The output file is exclusively created before connecting. On Windows it is
created with an owner-only access control list; on other platforms it is created
with mode `0600`. Choose an existing private directory outside the source tree.
The file is written through the same open handle, not reopened by pathname.

When a complete, correlated, successful DAP disconnect response is observed,
the proxy writes, syncs, and closes the capture before forwarding the response's
final bytes to the editor. This preserves the file when the editor terminates
its adapter immediately after disconnect. Failed, unmatched, mismatched, or
ambiguous responses cannot trigger this boundary. Subsequent protocol bytes
still pass unchanged, but are outside the capture interval.

Otherwise the trace is finalized when the proxy returns after normal stream
completion, an error, or cooperative cancellation. The normal proxy half-close
behavior still drains the other direction. The file is never rewritten after
finalization. Abrupt termination before either boundary, a host crash, or a
filesystem failure can leave an empty or incomplete file; a reserved file
alone is not evidence of a successful capture.

Stdout remains DAP-only. The JSON file is the performance artifact; it is never
mixed into protocol output. A proxy error and an export error remain errors
even if a partial artifact exists.

## Read the timeline

Open the JSON using a Chrome Trace Event compatible viewer. Perfetto documents
its [Chrome JSON import support](https://perfetto.dev/docs/getting-started/other-formats).
Use a locally controlled viewer for private engineering data; do not upload
captures to a third-party service merely to inspect them.

The root object contains `traceEvents`, `displayTimeUnit`, and `otherData`:

| Record | Meaning |
|---|---|
| `ph: "X"` | A matched DAP request and response, with elapsed host round-trip time. |
| `ph: "i"`, `s: "t"` | A standard DAP event observed by the proxy, such as `stopped`, `continued`, or `terminated`. |
| `ph: "M"` | Fixed process and track labels. Concurrent requests use separate bounded lanes rather than implying a call stack. |

`ts` and `dur` are relative **microseconds** from a monotonic host clock.
`displayTimeUnit` only controls presentation; it does not change those units.
Request sequence numbers correlate requests with delayed or out-of-order
responses, including an `attach` response delayed by the configuration barrier.
Recorded names are from a fixed allowlist; unrecognized names become `unknown`.
The seven fixed `multi-dap/trace/*` request names are recognized individually;
their source declarations, status bodies, and raw packet payloads are still
excluded from host observation artifacts.
The start is observed after a complete request frame is read from stdin. The
end is observed after a complete response frame reaches the proxy's output
observer, before that chunk is written to stdout. It does not measure the
editor's rendering latency or time blocked writing that chunk to the editor.

The summary in `otherData` includes:

- `schema: "multi-dap.host-trace.v1"`, `clock: "host-monotonic"`,
  `evidence: "host_observation"`, and `target_cpu_timing: false`;
- retained/dropped event counts, capacity, elapsed capture time, and observed,
  completed, failed, and unfinished request counts;
- bounded-pending, duplicate/out-of-order request, unmatched response, command
  mismatch, framing, and disabled-observer indicators;
- `proxy_failed`, `capture_complete`, and per-command `request_summary`
  latency and outcome totals;
- `finalization_boundary`: `disconnect_response` or `proxy_exit`, and
  `proxy_exit_observed`, which is false for a disconnect-boundary capture.

`capture_complete` describes the **proxy observation**, not target trace
coverage or successful target execution. Inspect the loss and error counters
before using any latency summary. A DAP failure response is different from a
failed proxy transport, and an unmatched or unfinished request has no fabricated
duration. A disconnect-boundary artifact describes observations through that
response, including any pending/loss indicators; it does not claim a later
transport outcome. Later proxy errors are still returned by the command, but
cannot modify the already durable capture.

## Bounds and failure isolation

The recorder retains at most the configured number of request/event records,
plus at most 258 fixed metadata records. It tracks at most 256 outstanding
requests. Full buffers and exhausted pending capacity increment health counters
instead of growing without bound or altering the DAP conversation. Summary
totals can therefore describe more completed requests than the retained slices
show.

Each direction accepts bounded DAP framing: at most 1 MiB per body and 8 KiB
of headers. Malformed or oversized framing disables observation of that
direction and records the condition. The observer still consumes the bytes
successfully from the proxy's perspective; it does not reject, edit, retry, or
inject protocol traffic. The existing DAP endpoint remains responsible for
deciding whether the conversation is valid.

Observation adds bounded host-side parsing and bookkeeping overhead. It is not
an overhead-free measurement, and no claim is made that its cost is negligible
for every workload.

### Host parser microbenchmark

The existing serial request/response benchmark can be run without an editor,
daemon, MULTI, or target:

```powershell
go test -mod=readonly -run '^$' -bench '^BenchmarkRecorderRequestResponse$' -benchmem -count=5 -benchtime=2s ./internal/perf
```

On one Windows amd64 host with Go 1.26.6, five runs measured
3.038-3.171 microseconds per small `threads` request/response pair, with a median
of 3.091 microseconds, 808 bytes allocated, and 20 allocations per pair.
The benchmark uses a fixed clock and reuses one retained event slot. It does
not measure actual clock queries, growing buffers, concurrent forwarding,
large payloads, private-file synchronization/export, editor latency, or target
effects. These local numbers are not an end-to-end overhead budget or a
cross-machine performance guarantee; see the [evidence index](verification-evidence.md).

## What is deliberately excluded

- No request arguments, response bodies, exception messages, console text,
  source paths, symbols, variable values, target addresses, memory, or tokens
  are retained.
- No additional target reads, state polling, breakpoint operations, trace
  start/stop, halt, resume, reset, download, or flash commands are issued.
- A `continued`/`stopped` event timestamp is when the proxy observed it, not a
  hardware timestamp or a measured CPU run interval.
- There are no PC samples, target function entry/exit events, target call trees,
  CPU utilization measurements, or reconstructed target flame graphs.
- Only the DAP boundary is instrumented. A long request does not by itself
  identify whether MULTI, the bridge, the actor, or the transport caused delay.

The timeline is inspired by the event-oriented presentation of
[VizTracer](https://github.com/gaogaotiantian/viztracer) and the separation of
observations from derived views in `t32perf`. It is not feature-equivalent to
either. A future target profiling path needs independently verified source
events, clock units, core/task identity, loss handling, and target-access
authorization before sharing the same viewer.

## Verification scope

Host tests exercise framing fragmentation and coalescing, request correlation,
bounded storage, concurrent forwarding/finalization, redaction, exclusive output
creation, and authenticated proxy integration. These are the verification
targets of the implementation, not hardware qualification or proof of native
trace support.

Synthetic recorder output was imported into the actual Perfetto UI
v59.1-1e9c61e98. Its SQL processor preserved the expected request/event names
and microsecond-to-nanosecond conversion. A separate synthetic native trace
also passed import and raw timestamp-string checks; see
[verification evidence](verification-evidence.md#trace-consumer-import-2026-10-28).
This consumer-import evidence is separate from the synthetic Extension Host
capture described below and does not establish real-target editor capture or
measured observer overhead.

A scripted capture against a stopped E2/MULTI 8.1.4 session was observed on
2026-10-29. After two narrowly scoped MULTI text-parser fixes, all ten requested
DAP operations succeeded and the exported trace reported no framing errors,
dropped events, or unfinished requests. This qualifies that scripted host
capture path only; it adds no target CPU timing or native trace claim.
The [verification evidence](verification-evidence.md) records the failed native
open separately and documents normal test-daemon cleanup.

An isolated VS Code Insiders 1.144.0-insider Extension Host captured twelve completed
requests and eighteen events through the production authenticated proxy against
a synthetic backend. The file was durable before the disconnect reply reached
the editor, with no framing errors or unfinished requests. This is actual
editor/host integration evidence, not a vendor trace parser, on-target workload,
visual UI, or observer-overhead qualification. The remaining native capability
gates are listed in [MULTI 8 adaptation](ghs-8-adaptation.md).
