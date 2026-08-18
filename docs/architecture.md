# multi-dap architecture

**Status:** architecture accepted; MULTI binding contract pending M0.
No implementation has started.
**Date:** 2026-08-18
**Target:** Green Hills MULTI 7.1.6d (verified locally at `D:/ghs/multi_716d`)

The main structure is settled. What remains uncertain is what execution, stop-reason, and
value-inspection primitives MULTI 7.1.6d can actually provide. M0 exists to establish that
contract; three of its blockers can still change the actor and event architecture.

## 1. Purpose

Expose a Green Hills MULTI debug session as a standard Debug Adapter Protocol backend so
that CLion, VS Code, Zed, and agent tooling can perform full source-level debugging of an
embedded target: breakpoints, stepping, call stacks, variables, memory.

The project is target-agnostic. Everything device-specific — device file, connection title,
target server arguments, core-to-ELF mapping, source path rewriting, lifecycle requirements
— comes from a project configuration file. No consuming project's identifiers appear in this
repository.

## 2. Non-goals

- Replacing the MULTI GUI. MULTI stays available and usable alongside the daemon (§6.8).
- RTOS-aware / task-level debugging. The thread model is deliberately left extensible
  (§6.7) but v1 maps threads to cores only.
- Supporting debuggers other than MULTI.
- Simulating per-core execution control on a synchronous freeze group (§6.7).
- Remote debugging. All listeners are loopback-only by design (§7.6).

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
| Debugger command `python` / `py`: `python [-b\|-nb] -s "stmts" \| -f script [args]`, **GUI only**, `-nb` (non-blocking) is the default | `debug_cmd.pdf` |
| Breakpoints accept command lists: `b main#10 {commands}` | `debug_cmd.pdf` |
| **MULTI-Python exposes no event/callback registration API** (`callback`, `OnStop`, `Notify`, `RegisterCallback` have no hits in `script.pdf`) | manual search |
| A MULTI-Python context runs startup hooks on initialization: `$BEFORE_GHS_STARTUP_PYTHON`, then `before_ghs_startup.py` from the user config dirs and cwd; symmetrically `$AFTER_GHS_STARTUP_PYTHON` and `after_ghs_startup.py` after initialization | `script.pdf`, "Extending the MULTI-Python Environment" |
| The Py pane / Py Window share one Python context per Debugger window; the standalone Python GUI has a separate context | `script.pdf`, "Interface Comparison" |
| `$restart` restarts the underlying interpreter and discards the old context | `script.pdf`, Py pane commands |

The absence of a callback API is why the event model in §8 has to be built rather than
subscribed to.

**Not yet verified:** whether execution primitives block until the next stop, what stop
reason is observable, and how value inspection is structured. These are M0-8, M0-3/M0-9, and
M0-4 respectively, and each can change this document.

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
                        │   ControlLease gates mutating access (§6.9)
                        ▼
              ┌───────────────────┐
              │  Debugger Core    │
              │                   │
              │  Session Actor    │
              │  Bridge Executor  │
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
              MBP v1 — NDJSON over loopback TCP
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
`bp_set` on its own would destroy the single-owner property established in §6.1.

Serializing their requests is not sufficient on its own: two controllers can be perfectly
race-free and still conflict in intent — one client inspecting variables at a stop while
another resumes the target. §6.9 defines the lease that prevents this.

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

### 6.1 Session Actor and Bridge Executor

**The Session Actor is the sole multi-dap command owner. It is not the sole owner of the
physical target** — the MULTI GUI remains an independent controller, and §6.8 defines how
that is reconciled.

One goroutine makes every decision. Everything becomes a message to it:

```
DAP request
poll tick
breakpoint hit hint
bridge death / unhealthy
operation completion
client attach / detach / lease change
```

The actor serializes decisions: what state we are in, whether the operation is legal now,
whether a transition occurred, whether a DAP event must be emitted, which handles are now
invalid.

The actor does **not** perform RPC itself. A separate single-flight Bridge Executor owns the
bridge connection and runs one request at a time:

```
Session Actor ──command──▶ Bridge Executor ──MBP──▶ bridge.py
      ▲                          │
      └────── completion ────────┘
```

This split exists because of an unverified assumption: that execution primitives return
promptly. If `Resume()` or `step_*` block until the target stops again, an actor that
performed its own RPC could neither poll, nor halt, nor confirm a hint — the entire event
model would wedge behind one call. The executor keeps the actor responsive to hints,
deadlines, and client disconnects regardless.

**Consequence if M0-8 shows execution primitives block:** the executor itself is still
occupied, so `halt` cannot be delivered over the same connection. That case requires a
second, independent control channel reserved for interruption and state queries, and the
"one bridge connection" premise in §7 no longer holds. This is a branch in the architecture,
not an implementation detail.

### 6.2 State machine and epochs

Two monotonic counters, both **session-global**, not per-core (the freeze group starts and
stops as a unit — see §6.7):

```go
type ExecutionEpoch uint64  // incremented on every accepted execution start
type StopEpoch      uint64  // incremented on every confirmed transition into Stopped
```

`StopEpoch` gates handle validity (§6.4) and deduplicates stop events. A confirmed stop
bumps it and emits exactly one DAP `stopped`; later evidence of the same stop — a redundant
poll result, a late hint — carries no new epoch and produces no event. Timestamps and
debounce windows are not used.

`ExecutionEpoch` carries one invariant: **a stop is canonical only if `ExecutionEpoch` has
advanced since the previous canonical stop.** Without an intervening execution start there
is nothing to stop from, so a second `stopped` cannot be emitted. It also tags every
transition and emitted event for logs and replay.

### 6.3 Stop arbiter: hints lower latency, state transitions are the truth

A breakpoint hit notification is a **hint**, never an event source:

```
hint{token}
   → Session Actor
   → state() / stop_info()
   → confirm Running → Stopped
   → StopEpoch++
   → canonical stop (reason, core, hit breakpoints)
   → DAP stopped
```

Polling follows the identical path; it merely arrives without a hint. Both sources converge
on one code path and deduplicate through the epoch.

DAP `stopped` requires a `reason` — breakpoint, step, exception, pause — so confirming
"stopped" is not by itself enough to emit a correct event. What MULTI can report about *why*
it stopped is M0-9. Depending on the answer, either `state()` returns a structured stop
description, or a separate `stop_info` primitive provides it:

```json
{ "execution": "stopped", "reason": "breakpoint", "core": 1, "breakpoint_handle": 17 }
```

**Correctness claim, stated precisely:** polling alone guarantees correct detection of
execution state. Stop-reason fidelity may degrade when the hint channel is unavailable and
MULTI cannot report a reason — in that case the adapter reports a conservative reason rather
than fabricating one. The hint channel must never be required for the adapter to notice that
the target stopped.

### 6.4 Handle store

Frame, scope, variable, and memory handles are **suspended-state references**. Per DAP they
are valid only while the debuggee is suspended and become invalid **the moment execution
resumes** — not when the next stop occurs. `variablesReference` values must lie in
`(0, 2^31)`.

```go
type Handle struct {
    ID        int32
    Kind      HandleKind   // frame | scope | variable | memory
    Core      CoreID
    StopEpoch uint64
    Value     any
}

valid := state == Stopped && h.StopEpoch == currentStopEpoch
```

On `Stopped → Running` every stop-bound handle is dropped immediately. A request naming an
invalid handle is answered with a stale-reference error, never with data.

**IDs are monotonic across the session; they are not reset per stop.** Restarting the
allocator at 1 after each resume would make a stale reference from a racing client resolve
to a *different, live* object at the next stop and return plausible wrong data instead of
failing. Monotonic IDs plus the epoch check make every stale reference fail loudly. Exhaustion
of the `int32` range is not reachable in a realistic session; approaching it is reported as
an error rather than wrapped.

`Core` is independent of invalidation: it exists because cores do not share an address
space, and a variable or memory view must never be resolved against the wrong one.

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
    Physical []PhysicalBreakpoint
}

type PhysicalBreakpoint struct {
    Core        CoreID
    MULTIHandle string
    HintToken   uint32   // generated by Go before bp_set
    ActualLine  int
}
```

The DAP side keeps one breakpoint ID regardless of how many physical breakpoints back it;
`hitBreakpointIds` on the stop event reports which logical breakpoint fired.

#### Hint tokens are generated before the breakpoint exists

The breakpoint command list must contain the notification argument at creation time, but a
MULTI handle is only known after creation. The identity carried over the hint channel is
therefore a Go-generated opaque token, never a MULTI handle:

```
Go       token = 0x9F2C41A7   (random, unguessable)
bp_set   {file, line, hint_token}
MULTI    b foo#12 {python -s "notify(0x9F2C41A7)"}
bridge   → returns MULTIHandle
Go       token ↦ {core, MULTIHandle, logicalBreakpoint}
```

The UDP datagram carries only the token. This closes the ordering problem, keeps DAP
concepts out of the bridge, and removes any dependence on MULTI handles being globally
unique.

#### Partial failure and divergent placement

Two cases arise once one logical breakpoint spans multiple cores:

| Situation | Result |
|---|---|
| every expected core succeeds, all at the same actual line | `verified = true` |
| any core's `bp_set` fails | `verified = false` + explanatory `message` |
| cores resolve the request to different actual lines | `verified = false` + explanatory `message` |

DAP can report only one actual location per breakpoint, so divergent placement cannot be
represented honestly as verified.

**Ownership survives an unverified response.** Physical breakpoints that were successfully
created are still recorded, so they can be rolled back or reconciled. A `verified = false`
response must never cause multi-dap to forget breakpoints it created.

#### Ownership and disconnect

Breakpoints created through multi-dap are DAP-owned and tracked in the store. Breakpoints a
user sets by hand in the MULTI GUI are not, and multi-dap never touches them.

On client `disconnect`, all DAP-owned breakpoints are removed; nothing else changes — MULTI
keeps running, the target is neither reset nor re-downloaded, and its execution state is
untouched. Removal is required because the standard DAP startup sequence means the next
client sends its full breakpoint configuration again; leaving the old physical breakpoints in
place would accumulate duplicate command lists on the same line.

Two consequences must be documented rather than assumed:

- After disconnect the target runs past lines that had breakpoints. `status` reports the
  DAP-owned breakpoint count explicitly.
- This behavior is a deliberate DAP compatibility decision, not an internal detail — see
  §9.3.

**Unverified dependency:** whether `bp_clear` perturbs a running target. If removing a
breakpoint requires an implicit halt, then "disconnect does not change execution state" is
false as written. Fallbacks in that case: defer removal until the next natural stop, or
accept and explicitly document one halt at disconnect. Verified in M0-7.

### 6.6 Source identity and the source index

#### Canonical identity

Path comparison must happen in exactly one place. DAP's `initialize` request tells the
adapter whether the client uses `path` or `uri` form, and whether lines and columns are
0-based or 1-based; every conversion happens at the frontend boundary, and Debugger Core
works only in canonical form:

```go
type SourceIdentity struct {
    ClientPath string   // as the client refers to it, in the negotiated form
    DebugPath  string   // as DWARF / MULTI refer to it
    Key        string   // canonical comparison key
}
```

DAP paths or URIs, DWARF `DW_AT_comp_dir` / `DW_AT_name`, and MULTI source paths are all
converted into this type before any comparison. No module compares path strings on its own.

This is defined before M1 rather than left open, because breakpoint routing depends on the
source index, which depends on path identity.

#### Source index

The index answers exactly one question: **which cores contain this source file.** Nothing
more. It does not build a symbol table and does not map lines to addresses — MULTI already
does that, and `bp_set(file#line)` goes through MULTI.

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
the answer. Validated early in M1.

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

### 6.8 Coexistence with the MULTI GUI

The GUI is a debugger controller that multi-dap does not own. A user can resume, halt, step,
reset, download, or delete breakpoints there at any time. The invariant is therefore scoped:
the Session Actor is the sole *multi-dap* command owner, and target state is **observed**,
never assumed.

v1 policy:

| GUI action while a DAP client is attached | Support |
|---|---|
| setting manual breakpoints | supported; multi-dap never touches them |
| halt / resume / step | supported; reconciled by polling, surfaced as ordinary state transitions |
| reset / download | unsupported; detected by reconciliation and reported as a session fault |
| deleting a DAP-owned breakpoint | unsupported; detected by reconciliation and reported |

Reconciliation is a normal poll outcome, not an error path: a state change multi-dap did not
initiate is still a real transition and is published as such. Actions marked unsupported
invalidate assumptions the adapter cannot repair silently (downloaded image identity,
breakpoint ownership), so they are reported rather than absorbed.

### 6.9 Control lease

One mutating controller at a time:

```go
type ControlLease struct {
    Owner LeaseOwner   // dap | mcp | cli | none
}
```

Mutating operations — resume, halt, step, breakpoint changes, memory writes, reset, download
— require the lease. Read-only operations such as `status` and `doctor` do not.

This exists because serialization alone does not prevent conflicting intent: an agent
frontend resuming the target while a developer inspects variables at a stop is race-free and
still wrong. Explicit takeover can be added later; concurrent control by two frontends is
never the default.

## 7. MULTI Bridge Protocol v1

Deliberately **not** JSON-RPC 2.0. Calling it JSON-RPC would invite assumptions about
`jsonrpc: "2.0"`, result/error exclusivity, notification semantics, and batching that this
protocol does not provide. It is: **MULTI Bridge Protocol v1, NDJSON over loopback TCP.**

### 7.1 Transport

```
mpythonrun -f bridge.py -args --rpc-port <port>
```

`bridge.py` binds its own port and owns the MBP socket. Go never parses the `GHS-Py>` prompt
or the `$` meta commands of the mpythonrun REPL socket.

Fallback if M0-2 shows a resident socket loop cannot coexist with MULTI-Python calls: Go
speaks the mpythonrun REPL socket directly, with all prompt-convergence logic confined to a
single file in the MULTI Driver.

### 7.2 Messages

```
request   {"id":7,"method":"bp_set","params":{…}}
response  {"id":7,"ok":true,"result":{…}}
error     {"id":7,"ok":false,"error":{"kind":"multi_refused","message":"…","raw":"…"}}
event     {"event":"…","params":{…}}
```

The error envelope carries `raw`, MULTI's output. **`raw` is semantically uninterpreted by
the bridge, and byte-exact unless `raw_lossy` is true** (§7.5). Debugger Core decides how to
present it. Raw replies are never consumed by an intermediate layer.

Handshake fields: `protocol_version`, `bridge_version`, `max_message_size`,
`encoding = "utf-8"`. A version mismatch is a hard startup failure — no compatibility
guessing.

### 7.3 Method table

Primitives only. No DAP vocabulary appears here.

```
session    open  close  download  reset  state  stop_info  cores
execution  resume  halt  step_over  step_in  step_out  run_to
breakpoint bp_set  bp_clear  bp_list
inspection stack  locals  globals  eval  regs  mem_read  mem_write  disasm     ← provisional
escape     run_commands   → raw text
```

**The inspection group is provisional until M0-4 and must not be frozen by schema contract
tests yet.** DAP's variable model is a lazily expanded tree with paging, so a flat
`locals()` that recursively serializes an entire object graph is not a viable contract. The
likely shape after M0-4 is closer to:

```
locals(frame)
children(value_ref, start, count)
eval(frame, expr)
```

with an opaque MULTI value locator. The deliverable of M0-4 is explicitly "finalize the
inspection contract", not merely "check that locals works".

### 7.4 `run_commands` discipline

`run_commands` is an escape hatch, not a channel. Without discipline it becomes the main
road and the "giant Python script" anti-pattern reappears in a different shape.

- it may only be called from the MULTI Driver package; CI greps for call sites elsewhere
- every call site carries an annotation stating why no structured method exists, e.g.

```go
// MULTI-NOSTRUCT: MULTI 7.1.6d exposes no structured API for X.
// Output contract pinned by golden fixture testdata/x_output.txt.
```

### 7.5 Encoding

Fixed now rather than discovered later, because Python 2.7's `str`/`unicode` split will
otherwise surface as corrupted output at the worst moment:

- the wire is UTF-8 bytes
- every outbound string in the bridge is converted to `unicode` before encoding
- undecodable bytes from MULTI use `errors="replace"`, and the message is flagged
  `raw_lossy: true` so Debugger Core knows that `raw` is not byte-exact evidence
- Windows paths travel with forward slashes, case preserved, and are **not** normalized —
  MULTI's path comparison behavior is unverified, so multi-dap does not silently rewrite

### 7.6 Network binding is an architectural invariant

The bridge can `resume`, `reset`, and `mem_write`; a DAP client has complete control of the
debugger. Exposure is therefore constrained by design, not by deployment habit:

| Channel | Binding |
|---|---|
| MBP control | random port on `127.0.0.1` / `::1` only |
| hint | random UDP port on loopback only, plus a per-session nonce |
| DAP | loopback by default |
| remote DAP | explicit opt-in, and requires a separate transport security design |

A forged hint can at worst provoke one `state()` call, but the session nonce is still
required so that any local process cannot generate hint floods for free. Hint tokens are
random and unguessable for the same reason.

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
polling is the source of truth (§6.3).

**Notifier preloading.** The debugger's in-process Python interpreter is a different context
from the `mpythonrun` process, so a function defined in `bridge.py` does not exist there.
The daemon therefore sets `AFTER_GHS_STARTUP_PYTHON` to a small `notifier.py` when it
launches MULTI, so every MULTI-Python context comes up with `notify` already defined. A
breakpoint command list then contains only the shortest possible call, carrying the
Go-generated hint token (§6.5):

```
b foo#12 {python -s "notify(0x9F2C41A7)"}
```

The notifier sends one UDP datagram carrying the token and the session nonce, and swallows
every exception. An exception raised inside a breakpoint command list would contaminate
MULTI's command execution.

**Known hazard:** `$restart` discards the Python context. If the startup hook does not re-run,
`notify` disappears and the hint channel dies silently. M0-5 must measure this, and
regardless of the result, Debugger Core must remain correct with the hint channel permanently
dead — subject to the stop-reason qualification in §6.3.

## 9. Session lifecycle

The daemon is long-lived and owns the MULTI session and the downloaded image, so that
attaching and detaching an IDE never re-launches MULTI, re-contends for the probe, or
re-downloads.

### 9.1 Daemon

- CLion / VS Code / Zed connect in **attach** mode over loopback TCP
- exactly one DAP client at a time; a second attach is refused with a clear error rather
  than silently taking over
- mutating control requires the lease of §6.9

Commands: `serve`, `status`, `shutdown`, `doctor`, plus `proxy` (stdio front-end that
forwards to the daemon) so an IDE that insists on launching its own adapter process needs no
architectural change.

### 9.2 Attach sequence

The DAP frontend has its own state machine, distinct from the target state machine. Because
the daemon is long-lived, the target may already be stopped when a client attaches, and
configuration must complete before any state is published:

```
initialize
   ↓
attach request (held pending)
   ↓
initialized event
   ↓
setBreakpoints … (client sends its full configuration)
   ↓
configurationDone
   ↓
attach response
   ↓
publish initial target state
```

If the target is already stopped at that barrier, exactly one `stopped` is emitted after it.
Target state changes that occur *during* configuration are folded into the final canonical
state — the internal churn of the attach sequence is never streamed to the client.

### 9.3 Disconnect policy

`disconnect` detaches only: MULTI keeps running, the target is not reset, nothing is
re-downloaded, execution state is unchanged, and DAP-owned breakpoints are removed (§6.5).

This is an explicit DAP compatibility decision. The general DAP model for attach sessions is
that the debuggee continues after disconnect, and the protocol later added `suspendDebuggee`
to express whether it should stay suspended. M6 must verify what VS Code, Zed, and CLion
each expect when disconnecting from a *stopped* target, and the policy is revisited against
those findings rather than assumed correct.

The claim that execution state is unchanged also depends on `bp_clear` not perturbing a
running target (§6.5, verified in M0-7).

### 9.4 Lifecycle capabilities are configuration, not hard-coded policy

Starting execution straight after a download can leave on-chip peripherals, shared memory,
and handshake flags alive from the previous run while each core's C startup re-zeroes the
software-side state; the two views disagree and the target misbehaves wholesale. Whether
that applies is a property of the target, so it is declared:

```toml
[lifecycle]
require_reset_after_download = true   # default
```

When enabled, `resume` after a download is refused in Debugger Core until a `reset` has
happened — enforced mechanically, not documented as advice. The default is `true` because
the failure mode is silent and expensive to diagnose.

## 10. Error handling and recovery

| Class | Handling |
|---|---|
| Configuration error | `serve` refuses to start; never surfaces as a debug-session failure |
| Startup failure (MULTI, probe, license) | diagnosed and reported by specific cause; never a generic "connection failed" |
| **Daemon duplicate** | single-instance lock keyed by the configured probe identity; a second daemon refuses to start and prints the holder's PID. Proves only that another *multi-dap daemon* holds this configuration |
| **External probe contention** | a separate error class: the probe is held by a process multi-dap does not manage (a MULTI GUI, a stray target server). Detected from the target server's own failure, reported with evidence |
| Leftover processes | detected at startup and **reported, not killed**; `doctor` prints executable paths and parent/child PIDs so a human decides |
| MULTI refused a command | `raw` forwarded, semantically uninterpreted (§7.2); DAP error response |
| Illegal target state (resume before reset, when required) | refused in Debugger Core; nothing is sent to the bridge |
| Timeout | only Go sets deadlines; the bridge sets none |
| Bridge death | DAP `terminated` is emitted; the daemon does not pretend the session is alive; target state is preserved |
| Unexpected GUI-initiated change | reconciled and published (§6.8); reset/download by GUI is reported as a session fault |

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
  definition on both the Go and the Python side, so the two cannot drift. The inspection
  group is excluded until M0-4 finalizes it (§7.3).
- **Handle lifetime tests.** Explicit coverage that every stop-bound handle fails after a
  resume, and that a stale ID never resolves to a live object.
- **On-board smoke.** One scripted pass: open → download → reset → breakpoint → stop →
  stack → eval → resume.
- **CI invariants.** The layering checks of §5, the `run_commands` call-site restriction of
  §7.4, and the loopback-binding invariant of §7.6.

## 12. Milestones

**M0 — reconnaissance on hardware. Nine blockers. Nothing is built until these are
answered, because each can invalidate part of the architecture.**

| # | Question | What it can change |
|---|---|---|
| 1 | Can `mpythonrun` attach to an existing MULTI session, or only create its own? | session lifecycle *and* the fault-recovery model (§10) |
| 2 | Can a Python 2.7 script inside `mpythonrun` run a resident socket loop while MULTI-Python calls are made, or is there a threading restriction? | transport (§7.1) |
| 3 | `python` is GUI only — can the MULTI window be hidden or minimized in daemon mode without losing function? | daemon presentation |
| 4 | **Finalize the inspection contract.** How much structured data do stack / locals / eval yield, is the type information sufficient for DAP variables, and how are children expanded and paged? | §7.3 method table |
| 5 | Breakpoint Python execution context: is it the same interpreter as `mpythonrun` (expected: no), does the `AFTER_GHS_STARTUP_PYTHON` preload make `notify` reachable, does executing it block MULTI's command loop, what happens on rapid repeated hits, does `$restart` re-run the hook? | §8 hint channel |
| 6 | `state()` polling: cost, blocking behavior, and — most important — **does it perturb the target?** If reading state requires halting a running core, polling destroys real-time behavior and "polling is the truth" collapses, leaving only the unreliable hint channel. **Highest-risk item.** | §6.3 entire event model |
| 7 | Does `bp_clear` perturb a running target? | §9.3 disconnect contract |
| 8 | **Execution primitive blocking semantics.** Do `Resume` / `step_*` return immediately after starting execution, or block until the target stops? While a `Resume()` is outstanding, can `Halt` / `state` be issued from another MULTI-Python context? | §6.1 actor/executor structure; possibly forces a second control channel |
| 9 | **Stop-reason observability.** Can MULTI report *why* it stopped — reason, stopped core, breakpoint identity or address, exception/fault information — or only that it is stopped? | §6.3 stop arbiter and DAP `stopped` fidelity |

**M1** — daemon, attach sequence (§9.2), `state`, `resume` / `halt`; a client connects and
sees core state. Early in M1, validate DWARF scanning for the source index (§6.6).
**M2** — breakpoints and stop events, both channels, arbiter and epochs.
**M3** — stack, scopes, variables, evaluate. This is the threshold where the IDE crosses
from "can control the target" to "can debug source".
**M4** — stepping: over, in, out, run-to.
**M5** — memory, registers, disassembly.
**M6** — VS Code, Zed, and CLion validation including disconnect expectations (§9.3);
packaging and distribution.

## 13. Open questions

- Breakpoint condition expressions: whether MULTI's expression subset matches what DAP
  clients emit is unverified.
- Source path rewriting rules: build-machine paths versus local paths need a configuration
  form. The canonical identity type is defined (§6.6); the rewriting rule is not.
- Whether hiding the MULTI window (M0-3) has side effects on GUI-only commands beyond
  `python`.
- Whether GUI-initiated reset/download can be detected reliably enough to report, or only
  inferred after the fact (§6.8).

## 14. Decision log

| Decision | Rationale | Rejected alternative |
|---|---|---|
| Bridge is mechanism only; Go holds all semantics | Prevents the "DAP → one giant Python script → MULTI" anti-pattern; the language split makes the boundary self-enforcing | A single Python adapter speaking DAP directly |
| Go for the adapter | Keeps multi-dap a pure DAP adapter and puts a compiler between policy and the bridge | Python 3.11 on both sides — same language removes the friction that keeps policy from sliding into the bridge |
| MULTI-Python via `mpythonrun` as the control substrate | Structured MULTI-Python objects out of process; avoids building a MULTI text-output parser as the core of the project | Injecting a resident agent into MULTI via `python -f` (agent lives inside MULTI, GUI-only); pure command-socket text parsing |
| **Bridge-owned MBP TCP socket as the control channel** | Go never touches the REPL prompt or `$` meta commands; the transport contract is ours | Driving the mpythonrun REPL socket directly and parsing prompts — retained only as the M0-2 fallback |
| NDJSON, not JSON-RPC 2.0 | The protocol is smaller than JSON-RPC and should not imply its semantics | Conforming to JSON-RPC 2.0 for its own sake |
| Session Actor as sole *command* owner, plus a single-flight Bridge Executor | Serializes all decisions in one place while keeping the actor responsive if execution primitives block | An actor that performs its own RPC; parallel `session` and `events` modules both holding state |
| Target state is observed, never assumed; GUI coexistence policy | The MULTI GUI is an independent controller; claiming sole ownership of the target would be false the first time a user presses Resume there | "Session Actor owns the target" as an unqualified invariant |
| Hints are hints; state transitions are the truth; dedup by epoch | Reliable across a silently dead hint channel; more robust than timestamps or debounce | Emitting `stopped` directly from a breakpoint notification |
| Handles invalid the moment execution resumes, keyed by session-global `StopEpoch`, with monotonic IDs | Matches DAP's suspended-state reference lifetime; monotonic IDs make stale references fail loudly instead of resolving to a different live object | Validity until the next stop; per-core epochs; resetting the ID allocator after each resume |
| Go-generated opaque hint tokens | The command list needs an identity before MULTI assigns a handle; also keeps DAP concepts out of the bridge and avoids assuming MULTI handles are globally unique | Passing the MULTI breakpoint handle to the notifier |
| Strict multi-core breakpoint verification | DAP can report one actual location; divergent placement or partial failure cannot honestly be reported as verified | Reporting verified when any core succeeded |
| UDP loopback for hints, separate from control TCP, with a session nonce | Cannot stall MULTI's command loop; loss is acceptable because polling backstops; the nonce makes local hint floods non-free | Reusing the bridge's control connection for notifications |
| Loopback-only binding as an architectural invariant | The bridge can resume, reset, and write memory; a DAP client has full debugger control | Treating listener exposure as a deployment concern |
| Cores as DAP threads, single-thread execution advertised false | Honest about the freeze group instead of simulating per-core control | Pretending per-thread stepping works |
| MCP as a frontend above Debugger Core, gated by a control lease | Preserves the single-owner property; serialization alone does not prevent conflicting intent between two controllers | Hanging an MCP shim beside the DAP server on the same bridge; relying on the actor alone to arbitrate two frontends |
| Inspection method group provisional until M0-4 | DAP's variable model is a lazy paged tree; freezing a flat contract now would lock in the wrong shape | Freezing the full method table before knowing what MULTI exposes |
| `require_reset_after_download` as a configuration capability | The repository is target-agnostic; the requirement is a property of the target, even though the default is conservative | A global hard-coded rule derived from one target's behavior |
| Semantic CI checks over a hard line limit for the bridge | M0 may legitimately require MULTI text normalization in the bridge; terminology and responsibility are what matter | A 300-line hard cap as an architectural guarantee |
