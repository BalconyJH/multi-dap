# multi-dap architecture

**Status:** design frozen for M0. No implementation has started.
**Date:** 2026-08-18
**Target:** Green Hills MULTI 7.1.6d (verified locally at `D:/ghs/multi_716d`)

## 1. Purpose

Expose a Green Hills MULTI debug session as a standard Debug Adapter Protocol backend so
that CLion, VS Code, Zed, and agent tooling can perform full source-level debugging of an
embedded target: breakpoints, stepping, call stacks, variables, memory.

The project is target-agnostic. Everything device-specific — device file, connection title,
target server arguments, core-to-ELF mapping, source path rewriting — comes from a project
configuration file. No consuming project's identifiers appear in this repository.

## 2. Non-goals

- Replacing the MULTI GUI. MULTI stays available and usable alongside the daemon.
- RTOS-aware / task-level debugging. The thread model is deliberately left extensible
  (§6.7) but v1 maps threads to cores only.
- Supporting debuggers other than MULTI.
- Simulating per-core execution control on a synchronous freeze group (§6.7).

## 3. Verified facts about MULTI 7.1.6d

These were confirmed against the local installation and its shipped manuals
(`manuals/script.pdf`, `manuals/debug_cmd.pdf`). They are load-bearing: if any turns out to
be wrong on hardware, the affected section must be revisited.

| Fact | Source |
|---|---|
| MULTI-Python exists; embedded interpreter is **Python 2.7** (`python27.dll`, PSF license for 2.7.3) | install tree + manual |
| `mpythonrun` runs Python scripts/statements out of process and can serve a plain socket: `mpythonrun -socket <port>`, `mpythonrun -f script.py -args ...` | `script.pdf`, "The mpythonrun Utility Program" |
| API shape: `GHS_Debugger()` → `DebugProgram()` → `ConnectToTarget()` → `cmdExecObj` → `Resume()` / `Halt()` / `RunCmd()` | `script.pdf` telnet example |
| `RunCommands()` exists on several classes; `RunCommandsViaDebugServer()` also exists | `script.pdf` API reference |
| Debugger command `python` / `py`: `python [-b|-nb] -s "stmts" \| -f script [args]`, **GUI only**, `-nb` (non-blocking) is the default | `debug_cmd.pdf` |
| Breakpoints accept command lists: `b main#10 {commands}` | `debug_cmd.pdf` |
| **MULTI-Python exposes no event/callback registration API** (`callback`, `OnStop`, `Notify`, `RegisterCallback` have no hits in `script.pdf`) | manual search |
| A MULTI-Python context runs startup hooks on initialization: `$BEFORE_GHS_STARTUP_PYTHON`, then `before_ghs_startup.py` from the user config dirs and cwd; symmetrically `$AFTER_GHS_STARTUP_PYTHON` and `after_ghs_startup.py` after initialization | `script.pdf`, "Extending the MULTI-Python Environment" |
| The Py pane / Py Window share one Python context per Debugger window; the standalone Python GUI has a separate context | `script.pdf`, "Interface Comparison" |
| `$restart` restarts the underlying interpreter and discards the old context | `script.pdf`, Py pane commands |

The absence of a callback API is why the event model in §8 has to be built rather than
subscribed to.

## 4. Architecture

```
              CLion / VS Code / Zed
                        │
                       DAP
                        ▼
              ┌───────────────────┐
              │   DAP frontend    │
              └─────────┬─────────┘
                        │
   MCP frontend ────────┤        (later; a frontend, never a second engine)
   CLI / doctor  ───────┤
                        ▼
              ┌───────────────────┐
              │  Debugger Core    │
              │                   │
              │  Session Actor    │
              │  State machine    │
              │  Stop arbiter     │
              │  Breakpoint store │
              │  Handle store     │
              │  Source index     │
              └─────────┬─────────┘
                        ▼
                  MULTI Driver
                        │
                  bridge client
                        │
              MBP v1 — NDJSON over TCP
                        ▼
              ┌───────────────────┐
              │ bridge.py (Py2.7) │
              │  mechanism only   │
              └─────────┬─────────┘
                        │
                  MULTI-Python
                        ▼
                     MULTI
                        │
                target server / probe
                        │
                     target
```

Written in Go, except `bridge.py` and the notifier snippet, which must be Python 2.7
because that is what MULTI embeds.

### Frontends are presentation only

DAP, MCP, and the CLI are peers. All three sit **above** Debugger Core and none of them may
reach the MULTI Driver or the bridge directly. A frontend that could issue `resume` or
`bp_set` on its own would destroy the single-owner property established in §6.1, which is
the invariant the rest of this design rests on.

## 5. Layering discipline

`bridge.py` implements mechanism. It has no notion of the debugger's semantics: it does not
allocate handles, does not map breakpoint identities, does not decide which event to emit,
does not deduplicate, does not retry, does not set timeouts, does not rewrite source paths,
and holds no session state machine. Each bridge function performs one MULTI operation and
serializes the result.

Debugger Core owns every piece of identity and state: handle allocation and invalidation,
breakpoint identity mapping, stop arbitration, event deduplication, core-to-thread mapping,
source path rewriting, polling cadence and backoff, retry and deadline policy.

The language split is the first line of defense — leaking policy into the bridge means
writing it in Python 2.7. CI enforces the rest:

- `bridge.py` must not contain DAP terminology (`dap`, `thread`, `variablesReference`,
  `frameId`, `stackFrame`, `scopes`)
- bridge method names must match an allowlist checked against the Go-side method table
- schema contract tests run on both sides from one shared definition
- `bridge.py` must contain no retry loop, no state machine, no source mapping, no handle
  allocator

A line-count ceiling is a review alarm (≈500 lines), not an architectural guarantee. If M0
shows that some structured data is only reachable by normalizing MULTI text output, that
parser legitimately belongs in the bridge or the MULTI Driver; length alone does not make it
a layering violation.

## 6. Debugger Core

### 6.1 Session Actor

One goroutine owns the MULTI target. Everything becomes a message to it:

```
DAP request
poll tick
breakpoint hit hint
bridge death / unhealthy
operation completion
client attach / detach
```

The actor serializes every decision: what state we are in, whether the operation is legal
now, whether a transition occurred, whether a DAP event must be emitted, and which handles
are now invalid.

Nothing else may touch the bridge client. The bridge client is single-flight by
construction: one in-flight request at a time, driven by the actor. Concurrent polling,
stepping, and hint handling therefore cannot interleave on the wire.

### 6.2 State machine and epochs

Two monotonic counters, both **session-global**, not per-core (the freeze group starts and
stops as a unit — see §6.7):

```go
type ExecutionEpoch uint64  // incremented on every resume/step that starts execution
type StopEpoch      uint64  // incremented on every confirmed transition into Stopped
```

A confirmed stop bumps `StopEpoch` and emits exactly one DAP `stopped`. Any later evidence
of the same stop — a redundant poll result, a late hint — carries no new epoch and produces
no event. This is the deduplication mechanism; timestamps and debounce windows are not used.

### 6.3 Stop arbiter: hints lower latency, state transitions are the truth

A breakpoint hit notification is a **hint**, never an event source:

```
hint{source: breakpoint, core, physicalHandle}
   → Session Actor
   → state()
   → confirm Running → Stopped
   → StopEpoch++
   → canonical stop
   → DAP stopped
```

Polling follows the identical path; it merely arrives without a hint. Both sources converge
on one code path, so they deduplicate naturally through the epoch.

**Correctness must not depend on hints.** The poller alone has to produce correct behavior,
because the hint channel can silently die (§8, `$restart`). Hints only reduce latency.

### 6.4 Handle store

Handles are not a bare `map[int]any`. Every handle carries the epoch it was minted under and
the core it belongs to:

```go
type Handle struct {
    ID        int
    Kind      HandleKind   // frame | scope | variable | memory
    Core      CoreID
    StopEpoch uint64
    Value     any
}
```

A request naming a handle from an older `StopEpoch` is answered with a stale-reference
error, never with data from the current stop. `Core` is separate from invalidation: it exists
because cores do not share an address space, and a variable or memory view must never be
resolved against the wrong one.

Memory follows the same rule through DAP's opaque `memoryReference`: the core and address
space are encoded into the reference itself, so `readMemory` and `disassemble` never depend
on which core happens to be "selected". Registers need no custom extension — DAP already
allows a scope to be marked as registers.

### 6.5 Breakpoint store

**`setBreakpoints` is replacement semantics, not incremental.** Each request carries the
complete set of breakpoints for that source. The store diffs against current state:

```
requested   foo.c = {12, 30}
current     foo.c = {12, 20}
diff        +30  -20  keep 12
```

A DAP breakpoint is logical and may map to several physical breakpoints, because one source
file can be compiled into more than one ELF and therefore exist on more than one core:

```go
type LogicalBreakpoint struct {
    DAPID    int
    Source   SourceIdentity
    Line     int
    Physical []PhysicalBreakpoint   // {Core, MULTIHandle}
}
```

The DAP side keeps one breakpoint ID regardless of how many physical breakpoints back it;
`hitBreakpointIds` on the stop event reports which logical breakpoint fired.

**Ownership.** Breakpoints created through multi-dap are DAP-owned and tracked in the store.
Breakpoints a user sets by hand in the MULTI GUI are not, and multi-dap never touches them.

On client `disconnect`, all DAP-owned breakpoints are removed; nothing else changes — MULTI
keeps running, the target is neither reset nor re-downloaded, and its execution state is
untouched. Removal is required because the standard DAP startup sequence
(`initialized → setBreakpoints… → configurationDone`) means the next client sends its full
breakpoint configuration again; leaving the old physical breakpoints in place would
accumulate duplicate command lists on the same line.

Consequence to document for users: after disconnect the target will run past lines that had
breakpoints. `status` reports the DAP-owned breakpoint count explicitly so this is visible
rather than assumed.

### 6.6 Source index

The source index answers exactly one question: **which cores contain this source file.**
Nothing more. It does not build a symbol table and does not map lines to addresses — MULTI
already does that, and `bp_set(file#line)` goes through MULTI.

Built at session open by scanning each configured ELF's DWARF line-table file names
(`debug/elf` + `debug/dwarf`). A project therefore only declares:

```toml
[[cores]]
id = 0
elf = "out/core0.elf"
```

and never maintains a hand-written source-to-core map.

Risk: the DWARF version emitted by the GHS compiler, the form of `DW_AT_comp_dir` /
`DW_AT_name`, and any architecture-specific encoding may not be handled by the Go standard
library. Fallback if scanning fails: ask MULTI per core whether the file is known, and cache
the answer.

### 6.7 Thread model: freeze group

Cores map to DAP threads:

```
core 0 → threadId 1
core 1 → threadId 2
```

The freeze group starts and stops as a unit, so multi-dap must not pretend to support
single-thread execution:

- `supportsSingleThreadExecutionRequests` is advertised as **false**
- `stopped` carries `allThreadsStopped: true`
- `continue` responses carry `allThreadsContinued: true`
- DAP `pause` maps to a group halt; per-thread pause is not accepted

The model is kept extensible for later RTOS-aware work — a thread is conceptually a
`{Core, Task}` context of which v1 only ever populates `Core`. Nothing in the core assumes
`thread == core` permanently.

## 7. MULTI Bridge Protocol v1

Deliberately **not** JSON-RPC 2.0. Calling it JSON-RPC would invite assumptions about
`jsonrpc: "2.0"`, result/error exclusivity, notification semantics, and batching that this
protocol does not provide. It is: **MULTI Bridge Protocol v1, NDJSON over TCP.**

### Transport

```
mpythonrun -f bridge.py -args --rpc-port <port>
```

`bridge.py` binds its own port. Go never parses the `GHS-Py>` prompt or the `$` meta
commands of the mpythonrun REPL socket.

Fallback if M0 shows a resident socket loop cannot coexist with MULTI-Python calls: Go
speaks the mpythonrun REPL socket directly, with all prompt-convergence logic confined to a
single file in the MULTI Driver.

### Messages

```
request   {"id":7,"method":"bp_set","params":{…}}
response  {"id":7,"ok":true,"result":{…}}
error     {"id":7,"ok":false,"error":{"kind":"multi_refused","message":"…","raw":"…"}}
event     {"event":"…","params":{…}}
```

The error envelope carries `raw`, MULTI's unmodified output. The bridge does not interpret
what MULTI said; Debugger Core decides how to present it. This also preserves evidence: raw
replies are never consumed by an intermediate layer.

Handshake fields: `protocol_version`, `bridge_version`, `max_message_size`,
`encoding = "utf-8"`. A version mismatch is a hard startup failure — no compatibility
guessing.

### Method table

Primitives only. No DAP vocabulary appears here.

```
session    open  close  download  reset  state  cores
execution  resume  halt  step_over  step_in  step_out  run_to
breakpoint bp_set  bp_clear  bp_list
inspection stack  locals  globals  eval  regs  mem_read  mem_write  disasm
escape     run_commands   → raw text
```

### `run_commands` discipline

`run_commands` is an escape hatch, not a channel. Without discipline it becomes the main
road and the "giant Python script" anti-pattern reappears in a different shape.

- it may only be called from the MULTI Driver package; CI greps for call sites elsewhere
- every call site carries an annotation stating why no structured method exists, e.g.

```go
// MULTI-NOSTRUCT: MULTI 7.1.6d exposes no structured API for X.
// Output contract pinned by golden fixture testdata/x_output.txt.
```

### Encoding

Fixed now rather than discovered later, because Python 2.7's `str`/`unicode` split will
otherwise surface as corrupted output at the worst moment:

- the wire is UTF-8 bytes
- every outbound string in the bridge is converted to `unicode` before encoding
- undecodable bytes from MULTI use `errors="replace"`, and the message is flagged
  `raw_lossy: true` so Debugger Core knows that `raw` is not byte-exact evidence
- Windows paths travel with forward slashes, case preserved, and are **not** normalized —
  MULTI's path comparison behavior is unverified, so multi-dap does not silently rewrite

## 8. Event channels

Two channels, deliberately separate.

**Control — TCP, request/response.** Go → `bridge.py`.

**Hint — UDP loopback, one-way.** Injected breakpoint code → Go.

The hint channel does not reuse the control connection. When MULTI hits a breakpoint while
the bridge is inside a call such as `Resume()`, re-entering the bridge's own runtime to
deliver a notification is a needless hazard.

UDP is chosen because it natively provides what a hint must be: best-effort, no connect, no
handshake, no back-pressure, and no way to stall the debugger when the listener is gone. A
TCP `connect` can block MULTI's command loop even with a timeout. Loss is acceptable because
polling is the source of truth.

**Notifier preloading.** The debugger's in-process Python interpreter is a different context
from the `mpythonrun` process, so a function defined in `bridge.py` does not exist there.
The daemon therefore sets `AFTER_GHS_STARTUP_PYTHON` to a small `notifier.py` when it
launches MULTI, so every MULTI-Python context comes up with `notify` already defined. A
breakpoint command list then contains only the shortest possible call:

```
b foo#12 {python -s "notify(3)"}
```

The notifier sends one UDP datagram and swallows every exception. An exception raised inside
a breakpoint command list would contaminate MULTI's command execution.

**Known hazard:** `$restart` discards the Python context. If the startup hook does not re-run,
`notify` disappears and the hint channel dies silently. M0 must measure this, and regardless
of the result, Debugger Core must remain correct with the hint channel permanently dead.

## 9. Session lifecycle

The daemon is long-lived and owns the MULTI session and the downloaded image, so that
attaching and detaching an IDE never re-launches MULTI, re-contends for the probe, or
re-downloads.

- CLion / VS Code / Zed connect in **attach** mode over TCP
- exactly one DAP client at a time; a second attach is refused with a clear error rather
  than silently taking over
- `disconnect` detaches only: MULTI keeps running, the target is not reset, nothing is
  re-downloaded, execution state is unchanged, DAP-owned breakpoints are removed (§6.5)
- on re-attach the daemon reports the true current state (running or stopped) and the client
  re-sends its breakpoint configuration

Commands: `serve`, `status`, `shutdown`, `doctor`, plus `proxy` (stdio front-end that
forwards to the daemon) so an IDE that insists on launching its own adapter process needs no
architectural change.

**Mechanical invariant:** after a download, `resume` is refused until a `reset` has happened.
Starting execution straight after a download leaves on-chip peripherals, shared memory, and
handshake flags alive from the previous run while each core's C startup re-zeroes the
software-side state; the two views disagree and the target misbehaves wholesale. This is
enforced in code, not documented as advice.

## 10. Error handling and recovery

| Class | Handling |
|---|---|
| Configuration error | `serve` refuses to start; never surfaces as a debug-session failure |
| Startup failure (MULTI, probe, license) | diagnosed and reported by specific cause; never a generic "connection failed" |
| Probe already owned | single-instance lock keyed by the configured probe identity; a second daemon refuses to start and prints the holder's PID |
| Leftover processes | detected at startup and **reported, not killed**; `doctor` prints executable paths and parent/child PIDs so a human decides |
| MULTI refused a command | `raw` forwarded unmodified; DAP error response |
| Illegal target state (resume before reset) | refused in Debugger Core; nothing is sent to the bridge |
| Timeout | only Go sets deadlines; the bridge sets none |
| Bridge death | DAP `terminated` is emitted; the daemon does not pretend the session is alive; target state is preserved |

The bridge never retries. Retry and backoff policy live in Debugger Core.

**Poisoned connections.** A Go deadline can stop waiting; it cannot cancel an in-flight
MULTI-Python call. Once an RPC deadline expires, the bridge and its connection are marked
unhealthy and must not be reused. What recovery is possible depends on M0-1:

- if `mpythonrun` can attach to an existing MULTI session: kill the bridge, restart
  `mpythonrun`, reconnect to the live MULTI session, resynchronize state
- if it cannot: an RPC timeout effectively loses the debug session, which becomes a
  documented product limitation

This makes M0-1 a fault-recovery question, not merely a lifecycle question.

## 11. Testing strategy

- **Protocol layer, no hardware.** An in-process fake bridge drives the full DAP
  request/response/event surface. This is the primary test surface.
- **Replay layer.** Real bridge replies recorded on hardware become golden files that drive
  Debugger Core offline.
- **Contract tests.** The method table and message schemas are checked from one shared
  definition on both the Go and the Python side, so the two cannot drift.
- **On-board smoke.** One scripted pass: open → download → reset → breakpoint → stop →
  stack → eval → resume.
- **CI invariants.** The layering checks of §5 and the `run_commands` call-site restriction
  of §7.

## 12. Milestones

**M0 — reconnaissance on hardware. Seven blockers. Nothing is built until these are
answered, because each can invalidate the architecture.**

1. Can `mpythonrun` attach to an existing MULTI session, or only create its own?
   (Also determines the fault-recovery model — §10.)
2. Can a Python 2.7 script inside `mpythonrun` run a resident socket loop while MULTI-Python
   calls are made, or is there a threading restriction?
3. `python` is GUI only — can the MULTI window be hidden or minimized in daemon mode without
   losing function?
4. How much structured data do `stack` / `locals` / `eval` actually yield? Is the type
   information sufficient to populate DAP variables?
5. Breakpoint Python execution context: is it the same interpreter as `mpythonrun` (expected:
   no), does the `AFTER_GHS_STARTUP_PYTHON` preload make `notify` reachable, does executing
   it block MULTI's command loop, what happens on rapid repeated hits, and does `$restart`
   re-run the hook?
6. `state()` polling: cost, blocking behavior, and — most important — **does it perturb the
   target?** If reading state requires halting a running core, polling destroys real-time
   behavior and the "polling is the truth" premise collapses, leaving only the unreliable
   hint channel. Highest-risk item in M0.
7. Can the bridge be restarted on its own and recover an existing session after a permanent
   MULTI API stall?

**M1** — daemon, attach, `state`, `resume` / `halt`; a client connects and sees core state.
Early in M1, validate DWARF scanning for the source index (§6.6).
**M2** — breakpoints and stop events, both channels, arbiter and epochs.
**M3** — stack, scopes, variables, evaluate. This is the threshold where the IDE crosses
from "can control the target" to "can debug source".
**M4** — stepping: over, in, out, run-to.
**M5** — memory, registers, disassembly.
**M6** — VS Code and Zed validation, packaging, distribution.

## 13. Open questions

- Breakpoint condition expressions: whether MULTI's expression subset matches what DAP
  clients emit is unverified.
- Source path mapping rules: build-machine paths versus local paths need a configuration
  form; the rewriting rule is undecided.
- Whether hiding the MULTI window (M0-3) has side effects on GUI-only commands beyond
  `python`.

## 14. Decision log

| Decision | Rationale | Rejected alternative |
|---|---|---|
| Bridge is mechanism only; Go holds all semantics | Prevents the "DAP → one giant Python script → MULTI" anti-pattern; the language split makes the boundary self-enforcing | A single Python adapter speaking DAP directly |
| Go for the adapter | Keeps multi-dap a pure DAP adapter and puts a compiler between policy and the bridge | Python 3.11 on both sides — same language removes the friction that keeps policy from sliding into the bridge |
| `mpythonrun` socket as the control channel | Structured MULTI-Python objects out of process; avoids building a MULTI text-output parser as the core of the project | Injecting a resident agent into MULTI via `python -f` (agent lives inside MULTI, GUI-only); or pure command-socket text parsing |
| NDJSON, not JSON-RPC 2.0 | The protocol is smaller than JSON-RPC and should not imply its semantics | Conforming to JSON-RPC 2.0 for its own sake |
| Single Session Actor owning all state | Serializes DAP requests, poll ticks, hints, and failures into one decision point; removes a class of races | Parallel `session` and `events` modules both holding state |
| Hints are hints; `state()` is the truth; dedup by epoch | Reliable across a silently dead hint channel; more robust than timestamps or debounce | Emitting `stopped` directly from a breakpoint notification |
| Handles bound to a session-global `StopEpoch` | Stale references fail loudly instead of returning data from a previous stop; freeze-group semantics make per-core epochs meaningless | Per-core epochs; plain handle maps |
| UDP loopback for hints, separate from control TCP | Cannot stall MULTI's command loop; loss is acceptable because polling backstops | Reusing the bridge's control connection for notifications |
| Cores as DAP threads, single-thread execution advertised false | Honest about the freeze group instead of simulating per-core control | Pretending per-thread stepping works |
| MCP as a frontend above Debugger Core | Preserves the single-owner property | Hanging an MCP shim beside the DAP server on the same bridge — two orchestrators, both able to resume and set breakpoints |
| Semantic CI checks over a hard line limit for the bridge | M0 may legitimately require MULTI text normalization in the bridge; terminology and responsibility are what matter | A 300-line hard cap as an architectural guarantee |
