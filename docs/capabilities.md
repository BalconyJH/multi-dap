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
| MBP v1 bridge transport | `IMPLEMENTED` | Bounded NDJSON over loopback TCP; the bridge contains mechanism, while policy remains in Go. |
| Cold acquisition | `HARDWARE_OBSERVED` | Explicit `DebugProgram` and `ConnectToTarget`; optional already-present preparation does not download, flash, reset, or verify memory. |
| Warm acquisition | `HARDWARE_OBSERVED` | Requires the runtime router address and an exact, unique, stable full-path program binding; no cold fallback. |
| Session Actor and epochs | `IMPLEMENTED` | One actor serializes state; bridge I/O uses one executor; stale bridge/frontend generations and stale stop-scoped handles are rejected. |
| Local control plane | `IMPLEMENTED` | Owner-only record, semantic configuration binding for config-based operations, authenticated `status`, `shutdown`, `diagnose`, and same-socket `proxy` upgrade. The legacy `proxy --probe-id` path is explicitly unbound. |
| VS Code integration | `SOFTWARE_ONLY` | Source extension and host tests exist; a real VS Code GUI session remains unverified. |
| [CLion integration](clion.md) | `HARDWARE_OBSERVED` | Primary supported IDE: native Cidr DAP attach, threads, source stacks, empty locals, evaluation, read-only M5 paths, and breakpoint synchronization were observed within the recorded scope. Actual breakpoint hit and full execution acceptance remain open. |
| Zed integration | `UNSUPPORTED` | No registered adapter exists; the repository does not publish a misleading standalone `debug.json`. |

## DAP request matrix

| DAP operation | Status | Current boundary |
|---|---|---|
| `initialize`, `attach`, `configurationDone` | `IMPLEMENTED` | Strict attach lifecycle and configuration barrier; only wired capabilities are advertised. |
| `threads` | `IMPLEMENTED` | Configured cores are stable DAP threads in one freeze group. |
| `continue`, `pause` | `IMPLEMENTED` | Group semantics only; single-thread execution is rejected. |
| `setBreakpoints` | `PARTIAL` | Path-backed, unmodified, unconditional source breakpoints only. Every configured core needs definitive source presence; uncertainty fails closed. |
| `setExceptionBreakpoints` | `UNSUPPORTED` | An empty clear request succeeds for client compatibility; any configured exception breakpoint is rejected. |
| `stackTrace` | `PARTIAL` | Stopped target, top-level source frames, delayed loading, and local paging of a complete bounded snapshot. |
| `scopes`, `variables` | `PARTIAL` | Stopped target, top-level frame, locals, one aggregate child level, and negotiated type/paging fields. |
| `evaluate` | `PARTIAL` | Stopped snapshot; supported watch/repl/clipboard/variables contexts. Hover, unsupported formatting, and non-top-level frames fail closed. |
| `next`, `stepIn` | `PARTIAL` | Statement granularity on a single configured core. Multicore execution has no production `ExecutionDomain` executor. |
| `stepOut` and run-to | `UNSUPPORTED` | No verified MULTI primitive is wired. |
| `readMemory` | `PARTIAL` | Stopped target, bounded reads, topology routing; numeric multicore addresses require `inspection.default_core`. Avoid side-effecting MMIO. |
| `disassemble` | `PARTIAL` | Stopped-state RH850 forward disassembly; nonzero instruction offsets are rejected. |
| `writeMemory` | `UNSUPPORTED` | Read-only inspection policy. |
| Registers and instruction stepping | `UNSUPPORTED` | Not advertised and not wired. |
| `disconnect` | `PARTIAL` | Detaches without reset, download, resume, halt, or session close. Requests to terminate or suspend the debuggee are rejected. Breakpoint cleanup remains bounded by stopped-state safety. |

## Verification state

The repository currently has automated host coverage for:

- Go unit, integration, race, and build checks;
- pinned workflow linting, action-reference policy, changelog policy, and a
  live Go vulnerability-database scan in CI/release automation;
- Python 3 host tests for the Python 2.7-compatible bridge and reconnaissance
  harness, using fake MULTI APIs;
- VS Code extension tests and package-content checks;
- deterministic Windows archive construction, exact payload allowlisting,
  embedded hashes, and reproducibility.
- semantic configuration-digest coverage, same-ID mismatch rejection, and the
  parent/child `ensure` change-between-read fence.

The full acceptance script treats MULTI Python 2 byte-compilation as `SKIP`
when no interpreter is configured; a release owner must review that result
rather than reading the overall exit code as hardware or runtime qualification.
The sanitized [verification evidence index](verification-evidence.md) records
the latest local run and distinguishes interpreter compatibility from hardware
qualification.

## Open hardware and product work

- Complete M0-2 resident-loop, M0-6 non-perturbing polling, and M0-7 running
  breakpoint-clear measurements.
- Verify an actual source-breakpoint hit and the full intended execution path.
- Complete VS Code GUI validation and implement a real Zed adapter before
  claiming those integrations.
- Implement and verify a multicore execution domain before enabling multicore
  stepping.
- Reduce the Python 2.7 bridge review surface by extracting mechanism-only
  helpers while preserving the contract-tested policy boundary.
- Complete a reviewed tagged build and hosted public-repository release run.

These items do not invalidate the tested host software, but they constrain what
a first release may claim.
