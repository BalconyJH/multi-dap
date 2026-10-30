# MULTI 8 Implementation and Acceptance

This is the execution checklist for fully adapting the
[MULTI 8 study](ghs-8-adaptation.md). A checked software implementation is not
a substitute for the source-format, runtime, authorization, or hardware gates
listed below. The overall work remains incomplete until the applicable gates
have direct evidence.

## Required deliverables

| Workstream | Required implementation | Acceptance evidence | Current state |
|---|---|---|---|
| Runtime selection | Version-bound Python 3 defaults and explicit legacy fallback. | Installation metadata, embedded-runtime checks, configuration-binding tests. | Existing implementation; retain its documented qualification limits. |
| Host timeline | Transparent, private, bounded DAP observation and Chrome JSON export. | Framing, correlation, privacy, race, authenticated proxy, and consumer-import checks. | Scripted stopped E2/MULTI 8.1.4 capture passed ten requests and export validation. Synthetic output passed actual Perfetto UI import/SQL checks. Real-editor acceptance and observer overhead remain open. |
| Profiler import | Explicit function/file/source/assembly/task hotspot or coverage import with exact field and unit semantics. | Vendor-produced export fixture, format/version provenance, invalid-input tests, and independent numerical comparison. | Explicit operator-mapped importer implemented; no qualified vendor fixture has been found yet. |
| Native trace input | Typed versioned TimeMachine packet access, not deprecated file-API dependence or GUI scraping. | ABI sizes/offsets, exported functions, address width, packet kinds, timestamp units, and runtime compatibility. | Versioned decoder and native mechanism implemented. Actual DLL initialization was observed, but the standalone inventory was empty; live session/packet acceptance remains open. |
| Target timeline | Normalize native function/context/event records with loss and clock-domain metadata. | Exact integer conversions, generation replacement, task attribution, unknown events, discontinuities, and known-workload comparison. | Offline packet normalization and picosecond Chrome export implemented; known-workload qualification remains open. |
| Snapshot analysis | Explicit operator-selected snapshot workflow using a supported native interface. | License availability, source identity, documented load/save effects, and real snapshot acceptance. | Current 8.1.4 PDF save/load/version/content restrictions are rechecked. Adapter-visible loaded-entry binding, licensing behavior, and real fixtures remain open. |
| Live capture | Actor-owned bounded collection and explicit start/stop authorization with recovery receipts. | Sole ownership, event pumping, cancellation/deadlines, generation fencing, recovery tests, and real target/probe evidence. | Experimental helper and seven requests are wired. The first selected-session open failed; local fault receipts and ordinary DAP isolation were observed. No packets were captured. MULTI-launched context/ownership and source binding remain open. No target mutation is authorized by this checklist. |
| RTOS tasks | Separate typed task inventory with unambiguous core/context routing. | Actual non-GUI response contract, identity/lifecycle tests, and RTOS target evidence. | Do not relabel configured DAP core threads as RTOS tasks. |
| Registers/language | Verify usable read-only register and language-debug primitives before advertising support. | Vendor contract, stopped-state/epoch tests, supported runtime and target acceptance. | `regvalsave` is a documented export candidate, not a qualified per-core reader. Scalar display preservation has synthetic char/integer regressions; real bitfield/GRV fixtures and target evidence remain open. |

## Native trace invariants

- Bind a collector to an explicit MULTI installation and exact selected trace
  session or snapshot. Never choose the first available session.
- Validate actual ABI layout and capabilities in addition to the release
  string. Preserve the 7.1.6d/8.1.4 signedness, address-width, attribute-mask,
  and packet-enumeration differences.
- Keep instruction counts, cycles, and physical time distinct. Wall-clock
  correlation requires explicit synchronization evidence; no guessed CPU
  frequency or assumed host/target shared clock is acceptable.
- Treat trace-buffer replacement and backward re-decoding as new generations,
  not append-only growth. Do not join function spans across replacement,
  undecodable data, overflow, or unknown attribution.
- Preserve ordinary PID and preceding-packet PID semantics separately.
  Unknown/plugin events must remain visible as unsupported or loss evidence.
- Symbolized records require exact image/symbol binding. Missing binding
  cannot become a fabricated function name or source location.
- Keep native callbacks and event pumping outside actor-blocking callbacks.
  Cancellation and timeouts must not reuse stale generations.
- Starting/stopping collection and loading/saving snapshots are explicit
  operations, not side effects of inspection or normalization.

## Profile import invariants

- Do not infer a CSV schema from a few convenient column names. A recognized
  vendor format requires a real versioned fixture and documented semantics.
- An operator-supplied exact mapping may enable explicit offline processing,
  but it is not evidence that an automatic vendor parser is qualified.
- Preserve self versus inclusive metrics, metric units, aggregation scope,
  and the input's provenance. Coverage and percentage rows are not additive
  CPU-time observations.
- Reject ambiguous headers, invalid numbers, non-finite values, overflow,
  unknown units, and conflicting mappings before producing a report.
- Aggregate observations may produce flat hotspots or coverage summaries, not
  invented timestamps, callers, call counts, or flame-graph stacks.
- Retain source hashes and bounded diagnostics without leaking symbols or
  paths into routine CLI output. Explicit analysis artifacts are private.

## Evidence still needed

The existing proxy recorder and its tests are useful foundations, not proof of
the remaining workstreams. No live MULTI/TimeMachine process was present in the
initial read-only process inventory for this continuation. A vendor-produced
Profiler CSV, an applicable snapshot or trace session, and an authorized
known-workload target run may be needed for full acceptance.

Implementation can proceed with clearly labeled synthetic host fixtures, but
those fixtures must never be presented as vendor or hardware evidence. Record
each completed gate in the verification index with its actual scope; leave
unmet gates open rather than weakening their requirements.

The implemented offline commands and their exact boundaries are documented in
[native performance analysis](native-analysis.md). In particular, accepting an
operator mapping or a synthetic native capture does not close the missing
vendor-fixture, snapshot, or live-session gates.

## Integrated host path and remaining work

The host integration now includes a native owner-thread event pump, typed
bounded IPC, an actor-owned controller, request/frontend/control-lease fencing,
explicit collection authorization, and local uncertainty receipts. The helper
is separate from the MBP executor. Windows process Jobs and inherited source
and configured-probe handles cover owned-child lifetime; no helper is spawned
by ordinary optional-capability discovery.

The existing authenticated DAP proxy is the entry point; there is no second
native-control listener. Only an explicitly supplied nonzero session is
connected, with mandatory acknowledgement that source/core matching has not
been verified. An uncertain frontend detach retains the source lease until
daemon shutdown rather than silently enabling another collector.

Remaining integration and qualification:

1. Verify actual source/session/core association and an authorized real native
   session. The 2026-10-29 standalone open failed; init-only inventory found
   no sessions, and a separate saved-argument initialization timed out.
   Investigate the supported MULTI-launched application context with explicit
   owned-process lifetime rather than replaying an unqualified router port.
   File/ABI checks and fake helpers do not establish native behavior.
2. Qualify snapshot loading, provenance and licensing independently. Declaring
   `sourceKind: "snapshot"` does not load or validate a snapshot.
3. Validate the new config-bound `trace capture` CLI against an explicitly
   selected real session. It already uses the documented DAP path with host
   transcript tests; it does not start a daemon or collection. An editor
   capture surface remains a separate product step.
4. Complete real-editor and broader trace-consumer acceptance, including
   observed timings, loss, and independent clock domains. Synthetic host and
   native fixtures have passed actual Perfetto import/SQL checks.
5. Obtain real Profiler, native stream, RTOS, and register fixtures and complete
   each target qualification gate. A stopped target alone does not prove OSA
   freeze mode or non-perturbing register access.

The bundle includes both Python 3.8+ native modules beside the legacy bridge.
Its Python 2 compatibility contract remains limited to the original bridge
and legacy helpers; it does not extend to native trace collection.
