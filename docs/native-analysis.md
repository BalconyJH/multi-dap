# Native Performance Analysis

The offline commands in this page move beyond the
[host proxy timeline](performance.md). They analyze explicitly supplied
performance observations, without launching MULTI, attaching a target,
starting/stopping trace, or changing target execution. The separate,
experimental native DAP requests described later can connect an explicitly
selected TimeMachine source; their start/stop requests are not passive.

These commands are not automatic vendor-format qualification. A real GHS
Profiler export fixture, native runtime acceptance, and known-workload
hardware checks remain required by the
[implementation checklist](ghs-8-implementation.md).

## Explicit Profiler CSV mapping

```powershell
multi-dap.exe profile import `
  --input C:\private\export.csv `
  --mapping C:\private\mapping.json `
  --output C:\private\profile-report-001.json
```

All paths must be absolute. Inputs must be regular files; the output must be a
new file in an existing private directory. The command needs no daemon
configuration. It does not run `profile csvexport`, `profile traceimport`, or
any other debugger operation.

The importer does not guess GHS column names, mode, locale, percentage
denominators, or missing values. The operator supplies the complete ordered
header and its semantics. The following is a **synthetic example**, not a
documented vendor CSV schema:

```json
{
  "schema": "multi-dap.profile-mapping.v1",
  "multi_version": "8.1.4",
  "export_surface": "profile_csvexport",
  "view": "functions",
  "mode": "history_performance",
  "provenance": "synthetic_fixture",
  "filter_scope": "unknown",
  "name_style": "long",
  "list_layout": "flat",
  "headers": ["Label", "Seconds", "Occurrences"],
  "identity": {"label": "Label"},
  "line_basis": "unavailable",
  "missing_tokens": [],
  "missing_policy": "reject",
  "metrics": [
    {
      "name": "time",
      "column": "Seconds",
      "semantic": "time",
      "unit": "s",
      "attribution": "vendor_unspecified"
    },
    {
      "name": "calls",
      "column": "Occurrences",
      "semantic": "observed_calls",
      "unit": "count",
      "attribution": "vendor_unspecified"
    }
  ]
}
```

For an actual operator-provided export use
`provenance: "operator_supplied_export"`, after inspecting the export and its
Profiler settings. This is an operator declaration, not proof of vendor
qualification. No mapping can set `vendor_qualified` to true.

Supported view declarations cover tasks, address spaces, files, functions,
function-relative source, coverage, coverage source, blocks, and assembly.
Supported metric semantics include time, instruction/sample counts, observed
calls, uncovered instructions, percentages, and explicitly mapped coverage
states. The relevant field/unit combinations are validated rather than
interchangeable.

Important interpretation rules:

- Counts and decimals retain exact decimal strings, including counts larger
  than `uint64`. They are not converted through floating point for ranking.
- Rows retain their own identities and source positions. Equal display names
  do not merge functions across address spaces or contexts.
- Missing values are rejected unless exact tokens and `preserve_missing` are
  declared. Preserved missing values are null, never zero.
- Percentages require explicit denominator semantics and values in `0..100`.
  A filter-relative percentage is not a recording-relative percentage.
- Time attribution is `self`, `inclusive`, or `vendor_unspecified`. No
  automatic totals or CPU-time conclusions are derived from inclusive or
  unspecified values.
- History `observed_calls` describes observed call-stack occurrences. It is
  not a guarantee of exact program invocation counts.
- Source lines may be function-relative and may describe ranges. A mapping
  must not present them as absolute source lines without supporting evidence.
- Coverage states remain `covered`, `partial`, or `uncovered`; they are not
  ranked as fabricated numeric scores or presented as branch/MC/DC coverage.

The report contains the explicit mapping, original input SHA-256, canonical
mapping SHA-256, source rows, nullable values, and flat numeric rankings. It
does not manufacture a timeline, callers, or a flame-graph stack.

### Input limits

The accepted CSV dialect is UTF-8, comma-separated, with RFC-style quoted
fields. UTF-8 BOM acceptance must be explicitly enabled in the mapping.
Automatic encoding, delimiter, or locale detection is deliberately absent.
Whitespace, thousands separators, percent suffixes, NaN, and infinity are
not silently reinterpreted as plain decimal numbers.

Mappings are limited to 64 KiB. CSV input is limited to 64 MiB, 100,000 data
rows, 64 columns, 32 metrics, 4 KiB per cell, and one million retained cells.
Numeric precision is limited to 128 digits. Unknown, duplicate, case-aliased,
null, or trailing mapping fields are rejected according to the schema. The
parser enforces cell limits while reading, not only after allocating a record.

## Normalize native TimeMachine packets

```powershell
multi-dap.exe trace normalize `
  --input C:\private\capture.json `
  --output C:\private\normalized-001.json `
  --chrome-output C:\private\target-001.trace.json
```

The input schema, `multi-dap.native-capture.v1`, is **multi-dap's own capture
envelope**, not a parser for `.dsnap`, `.tra`, `.trs`, or arbitrary trace CSV.
Its raw information/packet byte strings are intended for an admitted native
collector, or explicitly labeled synthetic fixtures.

The envelope binds an exact supported release and ABI, explicit source
identity, optional core/image metadata, raw `TM_DATA_INFO`, and a sequence of
generation-delimited packet intervals. A snapshot source requires its digest.
The normalizer does not promote these operator assertions to native-runtime
or hardware qualification.

Current packet profiles distinguish MULTI 8.1.4 from 7.1.6d, including
information-structure layout, signed versus unsigned tags, and data-access
mask meaning. An unknown ABI or release is not guessed from packet size.

The normalized report preserves:

- Raw native timestamps and clock units, without routing `uint64` through
  floating-point JSON numbers.
- Forward PID and preceding-packet PID attribution as different operations.
  Conflicts remain explicit; a task ID is not a core ID.
- Operator-declared initial context separately from context observed in native
  packets.
- Unknown packet/event types, reconstructed observations, discontinuities,
  generation replacements, and unfinished or unmatchable function intervals.
- Raw wall-clock synchronization evidence; it does not silently equate target
  and host clocks.

Only complete, compatible function entry/exit pairs produce intervals.
Generation replacement, overflow, undecodable data, unknown attribution, and
clock regressions must not become uninterrupted function spans.

`--chrome-output` is optional and requires a picosecond timebase. Instruction
and cycle counts can still produce a normalized count-domain report, but
cannot become physical time by assuming a CPU frequency. Chrome timestamps
are relative microseconds, with separate generation/clock-epoch origins. A function
interval is elapsed source time, **not exclusive CPU utilization**.

Synthetic output has been imported into the actual Perfetto UI and checked
with its SQL processor: a 100 us interval became 100000 ns, and raw near-
`uint64`-maximum timestamp strings remained exact. This is a fixture-level
consumer check, not hardware validation or a guarantee that a viewer retains
picosecond display precision. See the [verification evidence](verification-evidence.md).

The output report includes a canonical capture digest. This hashes the
validated capture's canonical JSON, not the original whitespace/layout of the
input file.

## Experimental native DAP requests

The daemon wires a separate Python 3.8+ TimeMachine helper through its session
actor. It does not use the MBP executor for native calls. This integration is
software-tested, **not native-runtime or hardware-qualified**.

Optional availability currently requires Windows, the configuration-bound
MULTI **8.1.4** installation and Python 3 selection, a 64-bit native DLL, a
standalone Python candidate inside that installation, and both helper modules
beside the selected `bridge.py`. Discovery reads host files and fingerprints
the DLL; it does not launch the helper, initialize the DLL, enumerate sessions,
or connect a trace source. Missing optional files do not break ordinary DAP.
The helper's ready handshake additionally verifies Python 3.8+ and pointer
width; native open checks metadata, exports, layouts, and the pinned DLL hash.
Legacy 7.1.6d packet normalization is not a claim of legacy live-runtime support.

The first live E2 acceptance attempt on 2026-10-29 did **not** produce a
native capture: `open` failed and correctly latched reconciliation while
ordinary DAP remained available. A later init-only diagnostic successfully
loaded/initialized the selected DLL but enumerated no sessions. Replaying
captured MULTI API launch arguments in a separate diagnostic timed out during
initialization and was terminated by its owned Job. Neither diagnostic called
native connect, read packets, or controlled collection. These observations do
not establish a safe standalone source-discovery or argument-replay recipe.
The MULTI-launched API application context, helper ownership, and source
association still need qualification; do not treat a copied router port or
an advertised request capability as proof that live capture works. See the
[verification evidence](verification-evidence.md) for the exact scope.

Use the existing config-bound authenticated proxy and ordinary DAP
`initialize`, `attach`, and `configurationDone` sequence. Only one frontend
may own the daemon. `multiDapSupportsNativeTraceRequests` reports that this
optional request path is wired; it does not certify source association,
snapshot support, probe capabilities, or target timing.

| Custom request | Required arguments | Behavior |
|---|---|---|
| `multi-dap/trace/open` | `sourceKind`, nonzero `sessionId`, `sourceId`, `acknowledgeUnverifiedSource: true` | Starts the owned helper and connects exactly the operator-selected session. Optional `coreId`, `imageSha256`, and `snapshotSha256` declare provenance; they are not verified association. |
| `multi-dap/trace/poll` | `handle` | Refreshes native metadata, count, generation, and observed collection state. |
| `multi-dap/trace/read` | `handle`, `start`, `generation`, `count` | Returns one bounded native-capture interval. `start` and `generation` are canonical unsigned decimal **strings**; `count` is `1..4096`. |
| `multi-dap/trace/start` | `handle`, `authorized: true` | Explicitly requests collection start. It never resumes the target. |
| `multi-dap/trace/stop` | `handle`, `authorized: true` | Explicitly requests collection stop. Vendor behavior may retrieve trace data; this is not implicit cleanup. |
| `multi-dap/trace/close` | `handle` | Explicitly disconnects the native API and reaps the helper, without issuing trace stop or target control. |
| `multi-dap/trace/status` | Empty object | Returns a local actor/controller snapshot, including failure receipts; no helper IPC or native call. |

`sourceKind` is `live` or `snapshot`; a snapshot requires `snapshotSha256`.
The operator must already have selected/loaded that source in MULTI. No request
loads a `.dsnap`, saves a snapshot, selects the first session, routes a
`trace api` launch, or proves which core the session represents. An initial
task context cannot be supplied to this production collector and silently
reused across later packet windows.
`sessionId` is a native traceserv session identity, not a DAP thread ID,
operating-system PID, core number, or TCP port. Do not derive it from one of
those unrelated identifiers. Native open checks that the explicitly requested
ID appears in the vendor's session enumeration; membership alone does not
prove the program/core binding.

Successful requests return `{status, batch?, receipt}`; local `status` returns
`{status}`. `read`'s `batch` is the `multi-dap.native-capture.v1` object accepted
by `trace normalize`. A DAP client must store that explicit private artifact
itself; these requests accept no output path or executable path.
There is not yet an editor capture button.

### Capture one existing packet interval

`trace capture` is a config-bound client for the same DAP requests. It does
not create a daemon or compete with an already attached editor. Use an
already running, explicitly selected daemon with no active frontend.
The following session ID is illustrative, not a discovered or recommended ID:

```powershell
multi-dap.exe trace capture `
  --config C:\private\project.toml `
  --session-id 17 `
  --source-id operator-selected-source `
  --acknowledge-unverified-source `
  --start 0 `
  --count 4096 `
  --output C:\private\capture-001.json
```

Use `--control-dir` when the existing daemon uses a non-default private
control directory. `--source-kind` defaults to `live`; `snapshot` additionally
requires `--snapshot-sha256`. Optional `--core-id` and `--image-sha256` retain
explicit operator provenance. None verifies the actual source association.
`--start` is a canonical unsigned decimal value; `--count` is `1..4096` and
defaults to 4096. This is one bounded interval, not an unbounded recorder or
an automatic packet-window merger.

The client performs ordinary DAP initialization/attach/configuration, native
open/poll/read, explicit native close, and non-terminating/non-suspending DAP
disconnect. It never sends trace start/stop, target continue/pause, reset,
download, flash, or daemon shutdown. If no trace was already collected, this
command does not create collection activity to populate the buffer.

The output is reserved as a new private file before attaching. Only a complete,
validated batch followed by confirmed native disconnect and completed DAP
cleanup is written as a successful capture. Failure can leave an empty or
partial private file; stdout never reports a successful capture in that case.
Unknown native state is not repaired with an implicit stop/close or retry.
Errors distinguish confirmed native disconnect from unconfirmed state and
always preserve the possibility that collection continues.

Each protocol stage is deadline-bound; the protocol transaction uses a
one-minute overall context deadline or a shorter caller deadline. Local
configuration/output I/O follows the host filesystem's semantics. The CLI
consumes only a bounded number of response/event frames. A stale generation fails without
silently re-reading a different recording. After a successful capture, run
`trace normalize` with its path; optional Chrome export still requires a
picosecond timebase.

### Ownership, generations, and recovery

- A source lease excludes the same numeric session ID across cooperating
  daemons in the Windows logon session. It is independent of installation
  spelling because no stronger source namespace has been verified. This does
  not exclude an unrelated external tool or prove target/core association.
- The helper inherits separate source and configured-probe lifetime handles.
  Its non-inherited Windows kill-on-close Job terminates owned descendants on
  parent death. The leases remain held until helper exit; a requested abort
  is not confused with actual process reaping.
- Actor request tokens, bridge generations, frontend ownership, and control
  lease epochs fence late replies. Native timeouts isolate the trace
  subsystem; they do not poison MBP or fabricate target running/stopped state.
- Native clear/start notifications, changed information, packet-count
  regressions, and notification loss invalidate packet generations. Read
  refreshes metadata before and after acquisition; stale reads return no
  batch and are never automatically retried. An unobservable same-metadata
  buffer replacement remains an unqualified native consistency boundary.
- `start`/`stop` receipts distinguish API request acceptance from observed
  running/stopped callbacks. A returned void API call is not collection-state
  confirmation. `close` confirms only the native disconnect call returned
  and the helper was reaped, not that target collection stopped.
- Frontend loss, cancellation after dispatch, protocol failure, or helper
  death can leave native state uncertain. The helper is aborted, not sent an
  implicit stop/close command. Reopening is blocked within that daemon and
  the uncertain source lease is retained until daemon shutdown. Inspect local
  `status` and reconcile the actual vendor/target state explicitly; restarting
  a daemon is not evidence that collection stopped.

The actor and helper queues, RPC deadlines, packet counts, and protocol frame
sizes are bounded. Read responses have a 512 KiB body limit inside the normal
1 MiB DAP frame bound. Routine status and errors contain only fixed categories,
not private symbols, source labels, raw packets, helper stderr, or DLL paths.

## Native mechanism and remaining acceptance

`bridge/timemachine_native.py` implements an explicit native mechanism
boundary. Static ABI inspection does not load or initialize the vendor
library. Native initialization and session connection are separate operations,
and session selection requires an explicit nonzero identity.

The mechanism checks installation identity, PE architecture, export symbols,
layout, DLL digest, native-call thread ownership, packet bounds, and bounded
callback notifications. Reading and closing do not implicitly start or stop
trace. Start/stop require separate explicit admission.

`bridge/timemachine_helper.py` owns the native thread and regular event pump.
Go owns process deadlines, admission, generation/ownership fencing, and
recovery receipts. The offline CLI commands above do not acquire a target.
Real DLL/session behavior, source/core association, atomicity under buffer
replacement, snapshot/license workflow, observer overhead, and known-workload
timing still require acceptance. Host fakes cannot close those gates.

`recon/tools/timemachine_abi.c` is a host-only layout probe that links no vendor
library. The local 8.1.4 x64 header check confirmed the expected structure
offsets and wall-clock bitfield packing. GCC required an annotation-only
`PRINTF_CHECK(a,b)` compatibility fix in an ignored temporary header copy;
the installed headers were not changed. That result is local header-layout
evidence, not proof of native DLL behavior or target clock calibration.

## Artifact and verification boundaries

Analysis artifacts may intentionally contain private function labels,
addresses, task identities, and source information. They use the same
exclusive private-file creation as host captures. Terminal status contains
only sanitized operation metadata; rows and symbols stay in the requested
private files.

For two trace outputs, both paths are reserved before either is written.
Failure can leave reserved or partial files, but the CLI does not issue a
success receipt. Existing files are never overwritten. A cancelled import
does not start a target operation.

Host fixtures and fakes verify software contracts. They do not prove the actual
CSV representation of every Profiler mode, the native DLL's behavior against
a live session, task/core routing, sampling overhead, or target clock accuracy.
