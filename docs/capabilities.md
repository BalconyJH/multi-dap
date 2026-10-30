# Capabilities and Development Status

This page is the authoritative release-facing inventory. Detailed invariants
live in the [architecture](architecture.md); hardware observations and open
experiments live in [M0 findings](m0-findings.md).

## Status vocabulary

| Status | Meaning |
|---|---|
| `IMPLEMENTED` | Production wiring and automated host tests exist. |
| `HARDWARE_OBSERVED` | The documented path was exercised on the stated real target, without implying broader qualification. |
| `SOFTWARE_ONLY` | Host tests, fakes, or protocol captures exist; real-target behavior is not established. |
| `PARTIAL` | A deliberately bounded subset is implemented; other cases fail closed. |
| `UNVERIFIED` | A candidate mechanism or probe exists, but the required evidence has not been collected. |
| `UNSUPPORTED` | The adapter rejects the operation and does not guess or emulate it. |

## Product surfaces

| Surface | Status | Current boundary |
|---|---|---|
| Strict TOML configuration | `IMPLEMENTED` | Absolute paths, unique core identities, numeric loopback endpoints, lifecycle policy, and optional inspection core are validated before startup. |
| MULTI version/runtime selection | `IMPLEMENTED` | Configuration loading identifies the selected installation from release metadata; the `multi-version` command can additionally use `gversion.exe` only with an explicit executable-probe opt-in. The result can be constrained with `multi.version` and is configuration-bound. Embedded Python 3 remains the default; `python_version = 2` is an explicit legacy fallback. |
| MBP v1 bridge transport | `IMPLEMENTED` | Bounded NDJSON over loopback TCP; Python 2/3 wire mechanisms live in the required `mbp_wire.py` companion loaded only beside the selected bridge script. Protocol/MULTI dispatch remains in `bridge.py`, while policy remains in Go. |
| MULTI component routing | `IMPLEMENTED` | Comma-separated PID/core aliases are parsed as metadata, never as program identity. Exact routed ELF binding is required; missing programs, ambiguous aliases, and identity drift fail closed. |
| Cold acquisition | `HARDWARE_OBSERVED` | Explicit `DebugProgram` and `ConnectToTarget`; optional already-present preparation does not download, flash, reset, or verify memory. |
| Warm acquisition | `HARDWARE_OBSERVED` | Requires the runtime router address and an exact, unique, stable full-path program binding; no cold fallback. |
| Session Actor and epochs | `IMPLEMENTED` | One actor serializes state; bridge I/O uses one executor; stale bridge/frontend generations and stale stop-scoped handles are rejected. |
| Local control plane | `IMPLEMENTED` | Owner-only records, semantic configuration binding, authenticated `status`/`shutdown` and same-socket `proxy` upgrade. Offline `diagnose` reads a configuration-bound terminal record without contacting the daemon. The legacy `proxy --probe-id` path is explicitly unbound. |
| Host process diagnostics | `IMPLEMENTED` | Explicit Windows `doctor --processes` observes installation-related processes without contacting MULTI or the target. Private paths require `--include-paths`; startup emits only count summaries on stderr. Coverage limits, inaccessible identities, and non-Windows unsupported status remain explicit. No process is killed/adopted, and no ownership or probe-availability inference is made. |
| [Performance timeline](performance.md) | `HARDWARE_OBSERVED` | Scripted config-bound capture on one stopped E2/MULTI 8.1.4 session completed ten requests and private Chrome Trace export without framing loss. A real VS Code Extension Host also captured a synthetic backend through the production proxy. This observes host DAP latency, not target CPU timing. Real-target editor capture and observer overhead remain unverified. |
| [Offline native analysis](native-analysis.md) | `SOFTWARE_ONLY` | Explicitly mapped Profiler CSV import and exact-version TimeMachine packet normalization. Physical-time trace export requires picosecond tags. No automatic vendor parser, snapshot parser, or hardware qualification is claimed. |
| [Native TimeMachine requests](native-analysis.md#experimental-native-dap-requests) | `SOFTWARE_ONLY` | Optional MULTI 8.1.4/Python 3.8+ daemon-owned helper, actor fencing, explicit source and start/stop admission, bounded reads, and local recovery receipts are wired. The CLI and VS Code command read one interval without starting collection; the editor reuses its active frontend and a bounded private-output helper. Source association and actual DLL/session/probe behavior remain unqualified. |
| VS Code integration | `SOFTWARE_ONLY` | Local file workspaces and fully qualified paths are checked before adapter startup. Actual Insiders Extension Host attach, cold/warm daemon reuse, inspection, disconnect, host capture, and native interval capture passed against a synthetic backend through the production authenticated proxy. Visual correctness, actual MULTI acquisition, and real-target acceptance remain unverified. |
| [CLion integration](clion.md) | `HARDWARE_OBSERVED` | Primary supported IDE: native Cidr DAP attach, threads, source stacks, empty locals, evaluation, read-only M5 paths, and breakpoint synchronization were observed within the recorded scope. Actual breakpoint hit and full execution acceptance remain open. |
| [Zed integration](zed.md) | `SOFTWARE_ONLY` | Registered source extension, strict attach schema, config-bound stdio proxy, nine host tests, and a Wasm build with pinned SDK 0.7.0. Local Windows usage only; no automatic acquisition. Actual dev-extension installation, GUI/target acceptance, and remote-workspace behavior remain unverified. |

## DAP request matrix

| DAP operation | Status | Current boundary |
|---|---|---|
| `initialize`, `attach`, `configurationDone` | `IMPLEMENTED` | Strict attach lifecycle and configuration barrier; only wired capabilities are advertised. |
| `threads` | `IMPLEMENTED` | Configured cores are stable DAP threads in one freeze group. |
| `continue`, `pause` | `PARTIAL` | Single-configured-core path only; every multicore execution request fails before bridge I/O until an execution-domain executor is qualified. DAP single-thread execution is rejected. Queued requests recheck their control-lease generation and admission gates before dispatch. |
| `setBreakpoints` | `PARTIAL` | Stopped target, path-backed, unmodified, unconditional source breakpoints only. Mutations recheck control generation and stop epoch before dispatch. Every configured core needs definitive source presence; uncertainty fails closed. |
| `setExceptionBreakpoints` | `UNSUPPORTED` | An empty clear request succeeds for client compatibility; any configured exception breakpoint is rejected. |
| `stackTrace` | `PARTIAL` | Stopped target, complete bounded stack rows including callers, delayed loading, and local paging after whole-snapshot source validation. Ambiguous frame identities are rejected; the live program counter is exposed only for a confirmed selected top frame. |
| `scopes`, `variables` | `PARTIAL` | Stopped target, freshly confirmed selected top frame, locals, one aggregate child level per expansion, and negotiated type/paging fields. Frame selection is never changed implicitly. |
| `evaluate` | `PARTIAL` | Stopped snapshot and freshly confirmed selected top frame; supported watch/repl/clipboard/variables contexts. Hover, unsupported formatting, and non-top-frame evaluation fail closed. |
| `next`, `stepIn` | `PARTIAL` | Statement granularity on a single configured core. Multicore execution has no production `ExecutionDomain` executor. |
| `stepOut` and run-to | `UNSUPPORTED` | No verified MULTI primitive is wired. |
| `readMemory` | `PARTIAL` | Stopped target, bounded reads, topology routing; numeric multicore addresses require `inspection.default_core`. Avoid side-effecting MMIO. |
| `disassemble` | `PARTIAL` | Stopped-state RH850 forward disassembly; nonzero instruction offsets are rejected. |
| `writeMemory` | `UNSUPPORTED` | Read-only inspection policy. |
| Registers and instruction stepping | `UNSUPPORTED` | Not advertised and not wired. |
| `multi-dap/trace/*` | `SOFTWARE_ONLY` | Seven experimental custom requests through the existing frontend owner/configuration barrier. Start and stop each require explicit authorization. Local status remains available after trace faults; no implied target execution, snapshot loading, RTOS routing, or hardware qualification. |
| `disconnect` | `PARTIAL` | Detaches without reset, download, resume, halt, or session close. Requests to terminate or suspend the debuggee are rejected. Breakpoint cleanup rechecks its stopped epoch before dispatch and retains pending records when it becomes unsafe. |

## Verification state

The repository currently has automated host coverage for:

- Go unit, integration, race, and build checks;
- pinned workflow linting, action-reference policy, changelog policy, and a
  live Go vulnerability-database scan in CI/release automation;
- Python 3 host tests for the dual-runtime bridge and reconnaissance harness,
  using fake MULTI APIs;
- VS Code extension tests and package-content checks;
- deterministic Windows archive construction, exact payload allowlisting,
  embedded hashes, and reproducibility;
- semantic configuration-digest coverage, same-ID mismatch rejection, and the
  parent/child `ensure` change-between-read fence.

Execution dispatch invalidates the previous stopped observation. Stopped-only
inspection and breakpoint work require a fresh definitive observation before
their queued epoch guards can pass. Unknown lifecycle samples preserve the
freshness requirement and cannot trigger automatic cleanup retries. This does
not invent a running event from a command acknowledgement or establish
non-perturbing hardware polling.

The [performance timeline](performance.md) has its own host-side framing,
correlation, bounded-storage, redaction, and proxy/file integration tests.
Those checks do not establish native runtime or real-target editor capture
acceptance; the actual Extension Host evidence is explicitly synthetic.
Separate helper/transport/controller/actor/DAP host tests cover
the native request path, without loading a vendor DLL or operating a target.

Separate local no-target observations on MULTI 8.1.4 confirmed debugger-module
imports under embedded Python 3.8.6 and Python 2.7.3. The MBP handshake and bounded
`state` request were observed under Python 3 only. A current-source smoke after
the wire-helper extraction repeated the handshake and `session_not_open`
response, identified the loaded Python 3.8.6 DLL, and verified owned-process
cleanup. Machine-wide process-event auditing was unavailable; snapshots cannot
exclude all transient activity. These are not hardware qualification.

The full acceptance script's MULTI embedded-runtime byte-compilation step is
`SKIP` when no usable standalone Python 3 or Python 2 interpreter is configured;
Python 3 is searched first, with a Python 2 compatibility fallback. A release
owner must review that result rather than reading the overall exit code as
hardware or runtime qualification.
The sanitized [verification evidence index](verification-evidence.md) records
the latest local run and distinguishes interpreter compatibility from hardware
qualification.

## Open hardware and product work

The milestone exit gates remain explicit:

| Milestone | Software state | Evidence or implementation needed to close the milestone |
|---|---|---|
| M1 runtime | Implemented, with host lifecycle and transport coverage. | Complete the dedicated resident-loop and differential polling measurements; recheck selected MULTI/runtime profiles on the intended target. |
| M2 breakpoints | Stopped-only transactions, owner transfer, dispatch fencing, and bounded cleanup implemented. | Observe a real source-breakpoint hit and qualify running clear independently before considering any relaxation of stopped-only mutation. |
| M3 inspection | Complete bounded stack paging and guarded top-frame inspection implemented. | Recheck the selected-frame guards on hardware; capture a supported non-top-frame mechanism before enabling caller locals/evaluation. |
| M4 execution | Single-configured-core continue/pause, statement stepping, and queue admission guards implemented. | Qualify the intended execution path and per-core before/after observations; implement a proven multicore executor for all execution operations. Step-out and run-to need verified primitives. |
| M5 inspection | Read-only memory and RH850 forward disassembly implemented within their bounds. | Qualify register access, instruction offsets, and instruction stepping separately before expanding capabilities. |
| M6 delivery/editors | Deterministic Windows packaging, CLion contract, VS Code optional acquisition and actual synthetic Extension Host acceptance, and a registered Zed source extension implemented. | Complete real-target/visual VS Code acceptance, actual Zed installation/GUI/target acceptance, and reviewed tagged/hosted release acceptance. |
| Native performance | Offline analysis, bounded native requests, CLI capture, and same-session VS Code capture implemented. | Obtain versioned vendor fixtures and verify actual session/source association, capture, clocks, snapshots, and consumer results. |

Host checks close software regressions only. A missing target session, vendor
fixture, execution authorization, or publication approval leaves the relevant
gate open; it is not a reason to advertise an unverified capability.

- Complete M0-2 resident-loop, M0-6 non-perturbing polling, and M0-7 running
  breakpoint-clear measurements.
- Verify an actual source-breakpoint hit and the full intended execution path.
- Complete real-target/visual VS Code validation and actual Zed installation,
  GUI, and target validation before claiming those acceptance levels.
- Implement and verify a multicore execution domain before enabling multicore
  stepping.
- Qualify a versioned native profiling/trace input before adding target
  performance views; see the [MULTI 8 adaptation study](ghs-8-adaptation.md).
- Complete a reviewed tagged build and hosted public-repository release run.

These items do not invalidate the tested host software, but they constrain what
a first release may claim.
