# MULTI 8 Adaptation Study

This study was prepared on **2026-10-27** from local installation metadata,
vendor headers and examples, the manuals shipped with MULTI IDE 8.1.4, and
public vendor documentation. It identifies useful adapter candidates rather
than enabling undocumented capabilities.

Implementation and unresolved acceptance gates are tracked in
[MULTI 8 implementation and acceptance](ghs-8-implementation.md).

The inspected installations were MULTI IDE **8.1.4** and **7.1.6d**. This
establishes a local comparison, not the newest release available from Green
Hills. The bundled *MULTI: IDE Version 8 Release Notes* is dated October 24,
2022 and describes the MULTI 8 family; it is not an exhaustive 8.1.4-specific
patch changelog.

No debugger, target connection, trace start/stop, or target execution was
performed for this study. Installation presence, documented support, host
implementation, and hardware qualification remain distinct evidence classes.

## Findings and priorities

| Area | Evidence and change | Adapter direction and gate |
|---|---|---|
| Python 3 | MULTI 8 release notes p. 8 describe the runtime selector. The repository already selects Python 3 by default with an explicit Python 2 fallback. | **Already adapted.** Preserve runtime/version binding and test both source compatibility and the selected embedded runtime separately. |
| TimeMachine live packets and callbacks | The installed API exposes session connections, packet counts/reads, information queries, and event callbacks. Much of this API already existed in 7.1.6d; it is not all new in MULTI 8. | **Priority 1 research.** A typed collector can preserve timestamped target events, but requires ABI gating, clock/identity/loss validation, lifecycle isolation, and an authorized trace acquisition workflow. |
| History and hardware trace | Release notes pp. 5-8 describe backward retrieval and History replacing PathAnalyzer, including trace data in History. | **Priority 1 research.** Prefer a documented event/data interface over GUI scraping. Do not recreate deprecated PathAnalyzer integration. Hardware support remains target/probe dependent. |
| Profiler export | *Debugging Command Reference* pp. 162, 164-165 describe `profile csvexport` for task, file, function, coverage, block, and assembly views. The command is marked GUI-only. | **Priority 1 offline candidate.** Obtain a vendor-produced fixture and explicit column/unit semantics before implementing an importer. Socket/bridge automation is unverified, not promised by the existence of the command. |
| Freeze-mode debug snapshots | Release notes p. 5 add freeze-mode snapshots and a trace-only option; p. 24 deprecates trace sessions in favor of snapshots. Snapshot save/load is separately licensed. | **Priority 2 research.** Operator-produced snapshots can separate collection from analysis. Verify supported access APIs, license requirements, included sensitive data, and exact save/load side effects before adapting. |
| RTOS task inspection | Release notes p. 26 add non-GUI support for `osatasklist`. | **Priority 2 research.** A possible task inventory path, but current DAP threads represent configured cores. Task-to-core identity, lifecycle, and inspection routing need a separate design; do not enable attach options implicitly. |
| Profile source/assembly views | Release notes p. 11 describe expanded source-line and assembly profiling views. | **Priority 2 research.** Suitable for flat hotspot/coverage views when the export contract is known; aggregate rows do not establish a timed call graph. |
| Registers and language support | Release notes p. 9 describes register-view changes; p. 22 changes C/C++ `char` bitfield print output to integers; p. 26 describes licensed `dwarf2dbo` conversion. | **Later.** These are not a general language-support addition or a verified core-routed register-value API. Keep unsupported capabilities unadvertised until their actual contracts and target behavior are verified. |

The implemented first step is [the passive proxy timeline](performance.md).
It yields useful host-path latency evidence without adding an unverified
MULTI operation or changing target execution.

## Compared header contracts: 7.1.6d to 8.1.4

The following are observed differences in the installations'
`timemachine_api/timemachine_api.h` and `timemachine_api/ts_packet.h`. Symbol
names identify the vendor contract; proprietary header contents are not
reproduced here.

| Contract difference | Consequence for an adapter |
|---|---|
| Trace information gains `has_64bit_addrs`. | Address width must come from the applicable contract, not a hard-coded 32-bit target assumption. |
| `TSP_WallClockSync` and `TS_Packet_WallClockSync` add a timetag-to-wall-clock mapping, including seconds and microseconds fields. | Preserve clock domains and mapping evidence. A wall-clock synchronization packet does not make all packet timestamps interchangeable with host time. |
| `TSP_PrevPID` attributes task/address-space identity to preceding packets, unlike ordinary PID attribution to following packets. | A collector must distinguish the two directions when reconstructing packet ownership. |
| Several packet timetags change from signed to unsigned 64-bit values. | Decode with the correct versioned types and handle wrap, missing synchronization, and conversion overflow explicitly. |
| Attribute mask value `4` (bit 2) changes from the older `Fetch` meaning to `Failed`. | Reusing the old bit mapping silently changes meaning. Version checks must cover semantic flags, not just structure size. |
| Events include `DataOverflowStart`, `DataOverflowEnd`, `Sleep`, `TaskStarted`, `Marker`, and `UndecodableData`; a plugin event range is defined. | Preserve discontinuities and unknown/plugin records. Never silently bridge a loss interval into a continuous call span. |
| The file API remains present but is documented as deprecated in the newer contract. | Installed `tm_file_*` entry points are not a reason to design a new long-lived file format dependency around them. |

A version string is necessary but insufficient for admission. A future native
collector must verify the actual exported API, structure sizes/alignment,
packet type availability, address width, clock metadata, and supported
target/probe mode. Unknown combinations must fail closed.

The inspected 8.1.4 Python example still names data-access mask `4` as `Fetch`,
despite the newer C header defining it as `Failed`. This is a concrete stale
example, not permission to choose either interpretation. Native decoding uses
the version-matched C contract; examples and host ABI probes are checked
separately.

## Compatibility traps

### Deprecated file and GUI integrations

The MULTI 8 release notes pp. 26-27 deprecate PathAnalyzer, EventAnalyzer trace
integration, tracepoints/passive mode, trace-session files, `tracesave`,
`traceload`, and the TimeMachine API file interface. The API headers may still
contain file-open/create/append/save examples; their presence does not override
the deprecation.

Release notes p. 24 also change command semantics: `tracesave` saves a debug
snapshot rather than the previous trace-session format. A filename extension or
an old script is therefore not a sufficient format contract. New offline work
should first investigate the supported snapshot workflow and current vendor
guidance; loading a snapshot and reading through the live trace API is a
candidate, not an implemented path.

The older public TimeMachine product page still describes PathAnalyzer and
EventAnalyzer. For version-specific adaptation, prefer the release-matched
manual and headers over that general marketing page.

### Snapshot workflow and limits

A direct recheck of the installed 8.1.4 PDFs on 2026-10-28 establishes more
detail than the release-note summary. References here use printed page
numbers, followed by one-based PDF page numbers where useful.

- `debugsnapshot` is documented in *Debugging Command Reference* pp. 23-25
  (PDF 43-45); the snapshot chapter is in *Debugging* pp. 598-607
  (PDF 630-639). Save/load requires a separate license. No license-feature
  identifier or adapter preflight API is specified there.
- Freeze-mode support depends on target/debug server. The documented minimum
  is Probe 6.2.4 for `mpserv`, or Compiler 2020.5 for the simulator and most
  other debug servers. Full-memory snapshots have additional target/OS
  restrictions; an INTEGRITY memory-map/BSP requirement is not a universal
  architecture capability.
- A running freeze-mode target normally yields a trace-only snapshot.
  `-traceonly` excludes final target memory and register state. Only trace
  data already retrieved into MULTI is included; collected-but-not-retrieved
  data is not automatically saved. `-dumpMemWhileRunning` attempts live-memory
  reads only on supported targets and is not a passive fallback.
- Saving writes a host file, can include sensitive source/binaries/memory, and
  blocks subsequent debugger commands until it finishes. Memory reads can
  have side effects. A save operation must not be slipped into inspection or
  cleanup.
- `debugsnapshot -load` creates a new debugger target-list entry. Loading a
  snapshot does not require the original target connection, but the resulting
  entry cannot run/step forward or perform hardware-dependent changes.
  A `.dsnapview` stores host-side view state, not target memory; its companion
  snapshot must represent the same original target data.

This supports a documented operator-driven offline loading workflow, not yet
an adapter-owned snapshot loader. The references do not specify how the
existing bridge or native trace API identifies and binds that new entry.
Actual `.dsnap`/`.dsnapview` fixtures, license behavior, source identity, and
entry selection remain acceptance gates. Declaring `sourceKind: "snapshot"`
to the existing collector does not perform or verify loading.

### RTOS inventory is not a core inventory

The current 8.1.4 *Debugging Command Reference* documents `osatasklist` at
printed p. 241 (PDF 261), without a GUI-only restriction. This agrees with
the release-note announcement; older, unversioned extracted text must not
override the installed reference. The command is still freeze-mode-only.
`-all` includes hidden tasks, while `-attach` attaches to tasks and must not
be included implicitly in an inventory request.

Neither that command entry nor the adjacent `osatask` entry at p. 240
(PDF 260) defines a stable output schema, task-ID reuse/lifetime, or task/core
association. *Debugging* pp. 611-614 (PDF 643-646) also describe OSA object
views whose columns vary with the RTOS. A generic whitespace/table parser or
relabeling configured DAP core threads would invent a contract.

An actual inventory fixture must identify MULTI/RTOS/mode, output delivery,
empty and populated responses, task/address-space identities and lifecycle,
and any supported core routing. Enabling freeze-mode or task attachment is a
separate operation, not a hidden prerequisite of inspection.

### Register export and language display

The current *Debugging Command Reference* p. 204 (PDF 224) documents
`regvalsave` as a register-value export candidate; it is not labeled GUI-only.
By contrast, `regview` at p. 205 (PDF 225) is GUI-only. Do not infer that all
register access requires the GUI, or that a documented export is already a
safe per-core register-reading API.

`regvalsave` writes a `.grv` file; its tab/selection variants depend on GUI
state. `-nomemmapped` excludes memory-mapped registers, but the MULTI 8
register model also includes dynamic and command-based registers. No installed
`.grv` fixture was found, and the references do not establish file grammar,
per-core routing, stop-mode prerequisites, or read perturbation for this
adapter. `regvalload` changes register values and is not a read primitive.
The Python API's documented `GetPc()` and the adapter's routed PC read do not
establish general register enumeration.

For the `char` bitfield display change, the existing scalar parser preserves
the right-hand display string instead of interpreting character codes,
bit width, radix, or signedness. Synthetic character/integer regressions now
protect that behavior; a real MULTI 8 bitfield fixture is still required to
qualify the complete output grammar. This is not a general language-support
expansion or proof of licensed `dwarf2dbo` behavior.

### Retrieval and event-loop ownership

Backward retrieval is not necessarily append-only. Release notes p. 23 state
that the previous decoded data is discarded and the buffer is decoded again
when retrieving backward on supported targets. A collector needs a trace
generation/reset marker and must not append duplicated data to the same epoch.
Disk-space aborts and undecodable regions also need explicit loss metadata.

The TimeMachine live API requires regular `tm_process_events` calls. Its header
warns that omitting this processing can make MULTI appear hung. A future
collector must own a bounded event loop with cancellation and failure handling;
it must not make synchronous callbacks into an actor that is waiting for that
same collector.

`tm_start_trace` and `tm_stop_trace` change collection state. `profile
traceimport` can suspend the debugger while processing. None of these actions
belongs in a supposedly passive observer, and none may bypass the existing
actor/session ownership boundary.

### RH850 software trace is not a generic fallback

The inspected release notes p. 76 limit History from RH850 software trace:
multi-core connections and the instruction-set simulator are not supported.
Function-call information requires compiler-instrumented entry/exit events
with PC trace disabled. Timing is valid only when trace timestamps are enabled,
and data is bounded by the most recent discontinuity.

Consequently, a capability observed on a single-core instrumented program
cannot be generalized to the current multicore DAP configuration. Firmware
instrumentation, build identity, clock setup, and target/probe support must be
verified independently.

## Evidence-aware roadmap

| Stage | Data and useful output | Required verification |
|---|---|---|
| 1: passive proxy observation | DAP request latency, response outcomes, observed state events; Chrome Trace JSON. | Host framing, correlation, redaction, bounds, file security, and transparent forwarding tests. No target performance claim. |
| 2: explicit offline profile import | A supported vendor export fixture; flat function/source hotspots or coverage. | Exact version/format/units, fixture provenance, malformed-input tests, symbol/source privacy, and documented aggregate semantics. No invented timestamps or callers. |
| 3: native trace normalization | Typed TimeMachine packets or a supported snapshot-derived stream; target event timeline. | ABI and feature admission, trace generations, clock calibration/wrap handling, core/task identity, packet loss, symbol/image binding, and known-workload hardware comparison. |
| 4: authorized live collection | Start/stop/capture under the existing ownership policy. | Explicit operation authorization, sole ownership, event-loop deadlines, cleanup/recovery receipts, probe/target qualification, and measured observer overhead. |

An alternative coarse PC sampler could observe data already returned by state
polling. It still needs the open M0-6 polling-perturbation measurement, correct
per-core attribution, and sample-timing evidence. A stopped stack snapshot is
not a representative runtime sample. PC hit counts alone support a histogram,
not a timeline, caller tree, function call count, or CPU-time flame graph.

The common lesson from `t32perf` and
[VizTracer](https://github.com/gaogaotiantian/viztracer) is to keep source
observations and their measurement semantics distinct from the viewer.
VizTracer's Python function tracing and custom-event features are not provided
by the proxy recorder.

## Sources

Public pages were reviewed on 2026-10-27:

- [Green Hills MULTI IDE](https://www.ghs.com/products/MULTI_IDE.html):
  current product-level descriptions of History, TimeMachine, snapshots, and
  profiling; not a release-specific API guarantee.
- [Green Hills TimeMachine](https://www.ghs.com/products/timemachine.html):
  general product background; older naming must be checked against release
  notes.
- [VizTracer project](https://github.com/gaogaotiantian/viztracer):
  function-event timeline and custom-event inspiration, not code or a runtime
  dependency of this feature.
- [Perfetto: other trace formats](https://perfetto.dev/docs/getting-started/other-formats):
  Chrome JSON event format, timestamp units, and viewer import semantics.

Installation-local primary sources, not redistributed:

- *MULTI: IDE Version 8 Release Notes*, October 24, 2022, printed pp. 5-11,
  23-27, and 76.
- *MULTI: Debugging Command Reference*, chapter 12, printed pp. 162, 164-165.
- `timemachine_api.h`, `ts_packet.h`, and the supplied TimeMachine C/Python
  examples from the compared installations.

The 2026-10-28 direct-PDF recheck used these exact source identities from the
inspected 8.1.4 installation:

| Document | SHA-256 |
|---|---|
| `multi_release_notes.pdf` | `2007b6223b6916732c4e7a1d54f95a1c8b2fb395370029b3b9cd207b6450de07` |
| `debug_cmd.pdf` | `4aeaa77fdbd96694fb1838dc4ef86ea3ed5375ebdd90d02e548255ce5f7b92ff` |
| `debug.pdf` | `24b82699c73ef72e0c25e122c01d93f55d5c9233221cbf84221de5b4d5ac5eb1` |

These hashes identify inspected references, not every distribution with the
same release label. Cached text without its source version/hash is not
authoritative. Direct extraction also requires normalizing typographic
ligatures for searches; a failed literal search alone is not absence evidence.

Only sanitized findings belong in this repository. Vendor manuals, raw native
captures, firmware symbols, and installation-specific evidence remain private.
