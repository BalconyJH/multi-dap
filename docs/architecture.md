# multi-dap architecture

**Status:** The architecture contract and current delivery status are maintained separately.
Hardware evidence and its uncovered items are documented in
[m0-findings.md](m0-findings.md).
**Date:** 2026-08-18
**Target:** Green Hills MULTI 7.1.6d (local installation and a five-core hardware session)
**DAP target:** 1.71.0 — the baseline for any future behavioral dispute

This document records the architecture contract and observed facts; design candidates, host
tests, and future acceptance work must not be described as hardware validation. The actual
delivery boundary is in §12: M2 and M3 top-level inspection are integrated; M4 `next`/`stepIn`
is integrated only for the single-configured-core path, and multi-core `ExecutionDomain` has no
production executor. Every unverified operation fails closed. M0-2, M0-6, and M0-7 remain
unfinished hardware measurements.

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

Verified on hardware in M0 (details and evidence in [m0-findings.md](m0-findings.md)):

| Fact | Where it matters |
|---|---|
| `GHS_Debugger` is a **builtin** in the `mpythonrun` interpreter; no import | driver bring-up |
| The command target is the `GHS_DebuggerWindow` returned by `DebugProgram`, not the `GHS_Debugger`. Connecting rebinds `GHS_Debugger` to the connection window, which has no process | §5, driver |
| `ConnectToTarget(dbserver, setupScript, setupScriptArgs, multiLog, stickToTheDebugger, moreOpts, printOutput)` — `stickToTheDebugger = 1` is required to bind the target to the debugger window | driver |
| `RunCommands(cmds, block, printOutput, keepRawOutput)` returns a **boolean**; the text lands in `cmdExecOutput`, the status in `cmdExecStatus`. With `block = 0` the output field is never populated | §7.2, §7.3 |
| `Resume` / `Halt` / `Step` / `Next` take an explicit `block` parameter, and `simulateBlockingWithNonBlocking = True` — blocking is a client-side poll loop in MULTI-Python, not a blocking call into MULTI | §6.1 |
| `GetCurPrInfo("")` returns 173 fields of MULTI's internal process state in 9–30 ms, including `stopStamp`, `contCount`, `pc`, `file`, `iln`, `proc`, `stackdepth`, the stop-condition flags, and the target memory map. Undocumented; several keys carry trailing spaces | §6.3, §7.3 |
| `stopStamp` increments by exactly one on every confirmed Running → Stopped transition and is unchanged otherwise | §6.2, §6.3, §6.8 |
| `H` reports a textual halt cause and, for a breakpoint with a command list, which list fired | §6.3, §6.5 |
| `GetProcessAttribute` returns `False` for every index and name tried; it is not a usable data route | §7.3 |
| `mpythonrun.exe` writes stdout with `WriteConsole` and raises a modal error dialog if its standard handles are redirected | §7.1 |
| A bare `help`, and `prepare_target` with no action flag, block the interpreter permanently on a GUI prompt | §7.4, §10 |
| Cores are MULTI processes. `switch -component "debugger.pid.N"` selects core N−1; `components` names them; `route` sends one command without changing selection; `P pr=N` is deprecated | §6.7 |
| `$_SYNC_RC = 1` — synchronous freeze-group debugging is on | §6.7 |
| The Green Hills compiler emits **no DWARF** into the ELF; debug information lives in proprietary `.dnm` / `.dla` files | §6.6 |

**Still not verified:** whether a resident socket loop can run inside `mpythonrun` (M0-2),
whether state polling perturbs a running target (M0-6, the one remaining item that can force a
redesign), and whether removing a breakpoint perturbs a running target (M0-7).

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

A line-count ceiling is a review alarm (approximately 500 lines), not an architectural
guarantee. The current bridge is above that alarm after adding strict warm acquisition,
console snapshots, memory, and disassembly mechanisms. Contract tests still enforce the
policy boundary, but future maintenance should split mechanism-focused helpers without moving
Debugger Core policy into Python. If M0 shows that some structured data is only reachable by
normalizing MULTI text output, that parser legitimately belongs in the bridge or the MULTI
Driver; length alone does not make it a layering violation.

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

**M0-8 resolved this in the favourable direction.** `Resume`, `Halt`, `Step`, and `Next` all
take an explicit `block` parameter; `Resume(block=0)` returns in under a millisecond and
`GetStatus()` immediately afterwards reports `running`. MULTI-Python's own "blocking" mode is
`simulateBlockingWithNonBlocking = True` with `checkInterval = 0.5` — a poll loop in Python,
not a blocking call into MULTI. A blocking execution primitive therefore cannot occupy the
transport; it only occupies a caller that chose to wait.

Consequently **the second control channel is not required**, and the "one bridge connection"
premise of §7 holds. The bridge must always issue execution primitives with `block = 0` and
let Debugger Core run the wait loop, so that deadline policy stays in Go as §10 requires.
The actor/executor split remains, justified on its original grounds — responsiveness to
hints, deadlines, and client disconnects — rather than on necessity.

#### Generation fencing

A Go deadline stops the wait; it does not cancel the call. A stalled RPC can therefore
return long after its connection was declared unhealthy and replaced. Every operation is
fenced so that a result from a dead world cannot be accepted:

```go
type BridgeGeneration uint64
type OperationID      uint64

type Completion struct {
    Generation BridgeGeneration
    Operation  OperationID
    Result     any
    Err        error
}
```

The actor discards any completion whose `Generation` is not the current one. The fence
applies to **every** executor operation without exception — polls, state queries, breakpoint
operations, and execution completions alike — because a late poll result is exactly as
capable of corrupting the state machine as a late `Resume` completion.

The same rule applies at the frontend boundary. A `FrontendGeneration` is bumped on every
client attach, so an asynchronous completion belonging to a disconnected client is never
delivered to the client that replaced it.

### 6.2 State machine and epochs

Two monotonic counters, both **session-global**, not per-core (the freeze group starts and
stops as a unit — see §6.7):

```go
type ExecutionEpoch uint64  // incremented on every confirmed Stopped → Running transition
type StopEpoch      uint64  // incremented on every confirmed Running → Stopped transition
```

**Both counters are bound to observed target transitions, not to command acceptance.** The
MULTI GUI can start execution without multi-dap issuing anything (§6.8); binding
`ExecutionEpoch` to "an execution start we accepted" would let a GUI-initiated run pass
through without advancing it, and the invariant below would then suppress a legitimate stop
event. Sources of a confirmed transition are equivalent: a multi-dap resume or step
completing, a poll observing that the target is running, or any future frontend's command.

The state machine is symmetric:

```
                  ExecutionEpoch++
                  drop stop-bound handles
                  DAP continued
   Stopped ─────────────────────────────────▶ Running
      ▲                                          │
      │            StopEpoch++                    │
      │            DAP stopped                    │
      └───────────────────────────────────────────┘
```

`StopEpoch` gates handle validity (§6.4) and deduplicates stop events. A confirmed stop
bumps it and emits exactly one DAP `stopped`; later evidence of the same stop — a redundant
poll result, a late hint — carries no new epoch and produces no event. Timestamps and
debounce windows are not used.

`ExecutionEpoch` carries one invariant: **a stop is canonical only if `ExecutionEpoch` has
advanced since the previous canonical stop.** Without an intervening execution there is
nothing to stop from, so a second `stopped` cannot be emitted. It also tags every transition
and emitted event for logs and replay.

#### Publishing resumption

`Stopped → Running` is published, not merely recorded. When the GUI resumes the target there
is no DAP `continue` request to respond to, so the only way the client learns that execution
resumed is the `continued` event:

```
continued { threadId: <a core in the resumed freeze group>, allThreadsContinued: true }
```

The M1 state sample proves only the freeze-group transition, not which core initiated it.
`GetCurPrInfo().pid` is a MULTI process identifier and is never reinterpreted as a DAP thread.
Because DAP requires `continued.threadId`, M1 names one stable member of the group that did in fact
resume. `stopped.threadId` is optional and remains absent until the stop arbiter has evidence for a
specific core; using the first core there would fabricate causality.

Stop-bound handles are dropped at the same moment (§6.4), before the event is emitted.

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
"stopped" is not by itself enough to emit a correct event. M0-9 established what MULTI can
report: the `H` command returns a textual halt cause (`Halted by user request.`,
`Halted for breakpoint.`, `Process not running.`) and, when the breakpoint carried a command
list, the list itself — which identifies the firing breakpoint and so feeds
`hitBreakpointIds`. `GetCurPrInfo("")` supplements it with `fContFromBp`, `fInStepMode`,
`fPendingHalt`, `fStoppedOnException`, `pc`, `file`, `iln`, `proc`, and `stackdepth`.

**`H` is not fully trustworthy on its own.** In one battery it still reported
`Halted for breakpoint.` immediately after an explicit `halt`, when the honest answer was a
user halt; it appears to report the most recent notable cause rather than the cause of the
current stop. The stop arbiter therefore corroborates `H` against the flag fields and
`$_BREAK`, and reports a conservative reason rather than a confidently wrong one:

```json
{ "execution": "stopped", "reason": "breakpoint", "core": 1, "breakpoint_handle": 17 }
```

**Correctness claim, stated precisely:** polling alone guarantees correct detection of
execution state *for transitions multi-dap initiated*. Stop-reason fidelity may degrade when
the hint channel is unavailable and MULTI cannot report a reason — in that case the adapter
reports a conservative reason rather than fabricating one. The hint channel must never be
required for the adapter to notice that the target stopped.

#### The missed-cycle problem

Polling compares against a cached state, so a complete execution cycle that begins and ends
between two polls is invisible:

```
t=0 ms     cached state = Stopped
t=20 ms    GUI step
t=21 ms    Running
t=25 ms    Stopped at the next line
t=100 ms   poll → Stopped        ← indistinguishable from "never ran"
```

Nothing advances, no `continued` and no `stopped` are emitted, and stale frame handles are
still considered valid while the target is in a different suspended state. Multi-dap's own
commands are unaffected — it knows it resumed — so this is strictly a problem for
externally-initiated execution.

The only sound fix is an observable **stop generation**: a counter, sequence number, or
equivalent field that changes on every stop, so that `Stopped(seq=51) → Stopped(seq=52)` is
recognizable as a new suspended state even though the intervening `Running` was never
sampled.

**M0-9 found one.** `GetCurPrInfo("")` returns a `stopStamp` field that increments by exactly
one on every confirmed Running → Stopped transition — step, halt, or breakpoint alike — and
does not move while the target is running or while it is already stopped. Measured across a
scripted battery it went `0x0c → 0x0d → 0x0e → (running, unchanged) → 0x0f → 0x10 → 0x11 →
0x12 → (halt while stopped, unchanged) → 0x13 → 0x14`.

`StopEpoch` is therefore **derived from `stopStamp`, not counted independently**. Counting
locally would miss exactly the case this field exists to catch: a stop multi-dap never
observed. The adapter records the last observed `stopStamp` and treats any change in it as a
confirmed stop, whatever produced it.

When a transition is discovered only after the fact this way, invalidation and publication
happen at detection time: handles are dropped, `ExecutionEpoch` and `StopEpoch` both advance,
and a `continued` followed by a `stopped` is emitted. The events are late but ordered and
truthful; silently keeping stale handles alive is not an acceptable alternative.

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
invalid handle is answered with a stale-reference error, never with data. When the transition
is only detected later (§6.3, missed cycle) the drop happens at detection time — late, but
never silently skipped.

**IDs are monotonic across the session; they are not reset per stop.** Restarting the
allocator at 1 after each resume would make a stale reference from a racing client resolve
to a *different, live* object at the next stop and return plausible wrong data instead of
failing. Monotonic IDs plus the epoch check make every stale reference fail loudly. Exhaustion
of the `int32` range is not reachable in a realistic session; approaching it is reported as
an error rather than wrapped.

`Core` is independent of invalidation: it exists because cores do not share an address
space, and a variable or memory view must never be resolved against the wrong one.

A generic DAP client preserves that rule with an opaque `memoryReference` containing the core,
stop epoch, and address; multi-dap uses that form for frame PCs. CLion Cidr's manual Memory View
instead sends only a numeric address and byte count. For that path a single-core topology is
unambiguous; a multi-core topology must explicitly configure `inspection.default_core`.
Without either proof the request fails before bridge I/O. The adapter never uses the selected
or last-used thread and never encodes a core into the visible numeric address. Per-address-space
DAP sessions remain the preferred deployment when operators need simultaneous manual numeric
views of several non-shared address spaces.

Registers need no custom DAP extension because a scope can be marked as registers, but the
underlying MULTI register contract remains unverified and is not advertised.

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
    HintToken   uint32   // session-local unique; not a secret
    ActualLine  int
}
```

The DAP side keeps one breakpoint ID regardless of how many physical breakpoints back it;
`hitBreakpointIds` on the stop event reports which logical breakpoint fired.

#### Hint tokens are generated before the breakpoint exists

The breakpoint command list must contain an identity at creation time, but a MULTI handle is
only known after creation. The current identity is therefore a Go-generated opaque token,
never a MULTI handle:

```
Go       token = 0x9F2C41A7   (session-local unique)
bp_set   {file, line, hint_token}
MULTI    b foo#12 {mprintf("HIT 0x9F2C41A7\n")}
bridge   B before/after → MULTIHandle
stop     H command list → strict token parse → {core, MULTIHandle, logicalBreakpoint}
```

Only the byte-exact command generated by multi-dap is accepted; arbitrary command lists remain
opaque. This closes the ordering problem, keeps DAP concepts out of the bridge, and removes any
dependence on MULTI handles being globally unique. UDP/notifier is an optional, currently
unwired wake-up seam (§8), not the source of production breakpoint identity.

#### Partial failure and divergent placement

A logical breakpoint is **atomic**: it exists on every expected core or on none.

| Situation | Result |
|---|---|
| every expected core succeeds, all at the same actual line | `verified = true` |
| any core's `bp_set` fails | successful physical breakpoints are rolled back; `verified = false` + explanatory `message` |
| cores resolve the request to different actual lines | rolled back; `verified = false` + explanatory `message` |

DAP can report only one actual location per breakpoint, so divergent placement cannot be
represented honestly as verified. Atomicity is chosen over best-effort because
`verified = false` while a breakpoint is quietly live on one core is the more damaging
outcome: the user is told nothing is set and the target stops anyway.

**Rollback can itself fail.** The effect of `bp_clear` on a running target has not been verified
on hardware, so the production policy never proactively halts for housekeeping: it first transfers
the physical breakpoint owner to detached/pending, retains the orphan record, and cleans it up at
the next naturally reached `stopped` epoch. A detach occurring in an already stopped epoch may
have one immediate attempt, followed by at most one natural retry per `StopEpoch`; repeated polling
in the same epoch never calls `Clear` indefinitely. The transaction state mutex is used only for
plan/commit and never spans `Set`/`Clear`, while separate transaction serialization preserves the
order of physical mutations; a detach fence prevents a concurrent transaction from recommitting an
old owner as a live record. These are implementation invariants covered by host/race tests, not a
hardware conclusion about `bp_clear` safety. All failed records are retained; `verified = false`
must never make multi-dap forget a breakpoint that it created.

#### Ownership and disconnect

Breakpoints created through multi-dap are DAP-owned and tracked in the store. Breakpoints a
user sets by hand in the MULTI GUI are not, and multi-dap never touches them.

On client `disconnect`, all DAP-owned breakpoints transfer to pending cleanup. If the target has
already stopped naturally, removal is attempted in that epoch's bounded cleanup; otherwise the
running target is not disturbed and cleanup waits for a subsequent natural stop. MULTI is never
reset or re-downloaded for this housekeeping. The next client must still send its complete
breakpoint configuration; retained ownership/pending records prevent duplicate command lists and
keep physical breakpoints that have not yet been cleaned up visible.

Two consequences must be documented rather than assumed:

- After disconnect the target runs past lines that had breakpoints. `status` reports the
  DAP-owned breakpoint count explicitly.
- This behavior is a deliberate DAP compatibility decision, not an internal detail — see
  §9.3.

**Unverified dependency:** whether `bp_clear` perturbs a running target. The current policy
therefore defers work to a naturally reached stopped epoch; “does not alter execution state”
must not be stated as an M0-7-verified fact.

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

**The DWARF plan did not survive contact with the toolchain.** M0 established that the Green
Hills compiler emits no DWARF into the ELF at all: both executables open cleanly with
`debug/elf` (`ELFCLASS32`, `EM_V800`) and then fail with
`decoding dwarf section info at offset 0x0: too short`, because there is no `.debug_info`
section. The section table carries only `.symtab`/`.strtab` plus Green Hills and Renesas
proprietary sections; debug information lives in sibling `.dnm` / `.dla` files in MULTI's own
debug-database format. A cross-compiled control fixture parses correctly, so the Go side is
sound — the data simply is not there.

**Source routing is therefore bounded by the tri-state `Presence` of each configured core.** With
a complete DWARF line table, `Present`/`Absent` can be definitive results; this toolchain has no
DWARF, so the production resolver reads one complete `l f` list for each verified core in every
Resolve. The sole grammar confirmed on p12 hardware is `--------  File names  --------`, followed
by contiguous zero-based decimal indices right-aligned in five columns and
`    N: X:\absolute\path.ext` lines (measured 1/2/3-digit indices have 4/3/2 leading spaces,
respectively). Each line independently passes exact grammar and absolute-path validation before
being added to the canonical membership set; duplicate lines with the same Windows canonical key
are idempotent membership, and neither carry nor select a raw spelling. Only canonical exact
membership can produce `Present`/`Absent`; any parsing, routing, or transport uncertainty is
`Unknown`. The list is never cached across Resolve calls, because an external GUI can reload the
same ELF path. Placement is refused when any core is `Unknown`, rather than treating it as
`Absent`. Direct DAP Set/Clear and native CLion synchronization have been observed on a stopped
target; actual breakpoint hits still await acceptance.

`recon/probes/p03_m2_source_known.py` is a warm-only follow-up evidence tool. By default, it only
performs callable inventory for each strictly bound core and sends no debugger command. After
explicit operator confirmation, it may use one pair of local opaque operands against allowlisted
unary symbol APIs on each core and check stopped recovery. It can demonstrate the presence of a
candidate API and distinguish the two operands; it cannot demonstrate that MULTI can parse a
source path or `line`, much less promote `Presence` from `Unknown` to definitive.

A project therefore still only declares:

```toml
[[cores]]
id = 0
elf = "out/core0.elf"
```

and never maintains a hand-written source-to-core map.

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

v1 policy. M0-9 confirmed that MULTI exposes an observable stop generation (`stopStamp`,
§6.3), so every entry below holds in its supported form. The right-hand column is retained
only as the contingency should `stopStamp` prove unreliable on another MULTI version or
target.

| GUI action while a DAP client is attached | With stop generation (this target) | Without |
|---|---|---|
| setting manual breakpoints | supported; multi-dap never touches them | supported |
| halt | supported; published as a normal transition | supported |
| resume | supported; reconciled by polling | best-effort — a run that ends before the next poll is missed |
| step | supported; each stop is a distinct generation | **unsupported while a DAP client is attached** |
| reset / download | unsupported; detected by reconciliation and reported as a session fault | same |
| deleting a DAP-owned breakpoint | unsupported; detected by reconciliation and reported | same |

Reconciliation is a normal poll outcome, not an error path: a state change multi-dap did not
initiate is still a real transition and is published as such (`continued`, then `stopped`).
Actions marked unsupported invalidate assumptions the adapter cannot repair silently
(downloaded image identity, breakpoint ownership), so they are reported rather than absorbed.

### 6.9 Control lease

One mutating controller at a time:

```go
type ControlLease struct {
    Owner LeaseOwner   // dap | mcp | cli | none
}
```

Mutating operations — resume, halt, step, breakpoint changes, memory writes, reset, download
— require the lease. Read-only operations such as `status` and `doctor` do not.

Acquisition and release are tied to the connection, not to a request:

```
attach accepted        → acquire the DAP lease atomically, before `initialized`
disconnect request     → remove DAP-owned breakpoints → release
socket closed / client crash → same cleanup path → release
```

The unexpected-close path is not optional. Handling only the `disconnect` request would let
an IDE crash strand the lease, leaving the daemon permanently unusable by any other frontend.

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
mpythonrun -f bridge.py -args \
  --rpc-host 127.0.0.1 --rpc-port 0 --ready-file <private-ready-path>
```

`bridge.py` binds its own port and owns the MBP socket. Go never parses the `GHS-Py>` prompt
or the `$` meta commands of the mpythonrun REPL socket. Port zero is the normal startup path:
after `bind()` and before `accept()`, the bridge atomically publishes the numeric loopback host
and kernel-selected port to a private, bounded ready file. Go validates that file, connects once,
and consumes the mandatory MBP handshake. The file is rendezvous metadata only; it carries no
token, target argument, or program identity and is removed with the owned bridge process state.

For recovery, the launcher also carries the live MULTI service-router coordinates:

```text
mpythonrun -sr_connect_servicerouter_host 127.0.0.1 \
  -sr_connect_servicerouter_port <session-port> -f bridge.py -args \
  --rpc-host 127.0.0.1 --rpc-port 0 --ready-file <private-ready-path>
```

The service router is part of the long-lived debugger session, not part of the disposable bridge.
Launching `mpythonrun` without those coordinates creates a private router and a second
`multi.exe`; on the measured installation that second Debugger also requested another CodeMeter
license and failed. The router port is discovered runtime state and is never a fixed project value.

**`mpythonrun` must be launched with its own console and with its standard handles left
alone.** It writes stdout through `WriteConsole`; redirecting stdout to a pipe or a file
raises a modal Windows dialog (`WriteConsole(handle=0x...) for STD_OUTPUT_HANDLE failed`) and
the process stalls. The daemon therefore spawns it with `CREATE_NEW_CONSOLE` and captures
nothing. This is independent support for the decision above: stdout was never a usable
channel, so the bridge had to own a socket regardless.

Fallback if M0-2 shows a resident socket loop cannot coexist with MULTI-Python calls: Go
speaks the mpythonrun REPL socket directly, with all prompt-convergence logic confined to a
single file in the MULTI Driver.

### 7.2 Messages

```
request   {"id":7,"method":"bp_set","params":{…}}
response  {"id":7,"ok":true,"result":{…}}
error     {"id":7,"ok":false,"error":{"kind":"multi_refused","message":"…","raw":""}}
event     {"event":"…","params":{…}}
```

The error envelope retains `raw` as a required MBP v1 compatibility field, but the bridge always
sets it to the empty string. `BridgeError` does not retain or decode target output, unexpected
exceptions are not stringified, and Go, DAP, and CLI surfaces receive only the stable `kind` and
sanitized `message`. Target diagnostics never cross the error boundary.

Successful typed `run_commands` results are the only MBP values that may contain MULTI command
text. They are consumed and validated inside `internal/multi`; no raw success reply is forwarded to
Debugger Core, DAP, or CLI.

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
console    console_read console_reset → bounded Target/I/O snapshots and local cursor reset
escape     run_commands   → typed result carrying internal-only command text
```

**The inspection group is now settled by M0-4.** MULTI exposes no structured inspection API at
all — `GHS_Debugger` has no stack, locals, or evaluate method — so inspection goes through
command text captured from `cmdExecOutput`. The text is rich enough for DAP's lazy paged tree:
one-level expansion exists (`print <path>` on any sub-expression) and array elements are
individually addressable by index. The contract is therefore

```
locals(frame)                     -> e <frame> ; l
children(value_ref, start, count) -> print <path> per element
eval(frame, expr)                 -> print <expr>
```

with **the expression path string as the opaque MULTI value locator**. No handle is invented on
the MULTI side; Debugger Core maps its own `variablesReference` to a path. Failures are uniform:
`Unknown name "<identifier>" in expression.` with `cmdExecStatus = 0`.

This text-to-handle boundary treats input as untrusted: the inspection parser limits each output,
the number of lines, and individual line length; the backend must first return and validate a
**complete snapshot** before applying DAP paging or allocating variable handles, so malformed
off-page values cannot leave partial handles. Structured-locator identifiers accept ASCII grammar
only; command separators and all control characters are rejected. These are parser/service host
test invariants, not an expansion of MULTI text grammar or hardware validation.

The parsers for `calls`, `l`, `l @`, `l g`, `l S`, `l r`, `e`, `print`, `B`, and `H` live in the
MULTI Driver package, which §5 explicitly permits, and are pinned by golden fixtures captured
from hardware.

`state` and `stop_info` collapse into one MBP request. To prevent a torn observation, that request
brackets `GetCurPrInfo("")` with `GetStatus()` before and after it and rejects the sample if the two
status values differ; it never retries in the bridge. The process dictionary supplies the stop
generation and detail. It returns 173 fields in 9–30 ms, including `stopStamp`, `pc`,
`file`, `iln`, `proc`, `stackdepth`, and the stop-condition flags, but it has no reliable canonical
running/stopped field. It is undocumented, so the captured field set is the contract and is
governed by the same discipline §7.4 imposes on `run_commands`.

The implemented bridge allowlist is deliberately narrower than the final table: `open`, `close`,
`state`, `cores`, `resume`, `halt`, `step_in`, `next`, `console_read`, `console_reset`,
`memory_read`, `disassemble`, and the disciplined `run_commands` escape. `console_read` uses
MULTI's `savedebugpane target/io` command to take
bounded snapshots in the bridge's private ready-file directory and returns only the increment
since the previous snapshot. Target-server text and target-program I/O remain separate through
the actor and become DAP `stdout` output events. Adapter warnings use `stderr`; CLion's Cidr
frontend does not surface DAP `category=console` in its process Console. The bridge never
writes either stream to its own stdout, where it would corrupt DAP framing.
`console_reset` changes only the bridge's two local increment cursors. Each new DAP frontend
invokes it through the actor before `configurationDone` releases the polling barrier, so the
next bounded read replays the panes visible at attach time; it sends no MULTI command and does
not change target state. `memory_read` and `disassemble` retain the stopped/core-routed M5 bounds
described in §6.4; no target write method is exposed.
The two stepping methods use the non-blocking M0-8 Python calls. `step_out`, `run_to`, `download`,
and `reset` remain unavailable until their exact MULTI primitives have been verified; a guessed
command spelling is not a protocol contract.

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
- undecodable bytes in a successful command result use `errors="replace"`, and that result is
  flagged `raw_lossy: true` so `internal/multi` knows its command text is not byte-exact evidence;
  the flag does not apply to error envelopes, whose reserved `raw` field is always empty
- Windows paths travel with forward slashes, case preserved, and are **not** normalized —
  MULTI's path comparison behavior is unverified, so multi-dap does not silently rewrite

### 7.6 Network binding is an architectural invariant

The bridge can `resume`, `reset`, and `mem_write`; a DAP client has complete control of the
debugger. Exposure is therefore constrained by design, not by deployment habit:

| Channel | Binding |
|---|---|
| MBP control | random port on `127.0.0.1` / `::1` only |
| hint (optional; not integrated into the runtime) | loopback UDP receiver/protocol seam, optionally with a per-session nonce |
| DAP | loopback by default |
| remote DAP | explicit opt-in, and requires a separate transport security design |

Even if enabled in the future, a forged hint must trigger at most one `state()`; the nonce is the
boundary that prevents cost-free flooding by any local process. The current strict breakpoint
identity does not depend on UDP: it comes from the command list's `mprintf` token and is parsed
from `H` after the target stops.

## 8. Event channels

The control channel is integrated; the hint receiver and notifier remain optional design seams and
are not integrated into the daemon runtime.

**Control — TCP, request/response.** Go → `bridge.py`.

**Hint (optional) — UDP loopback, one-way.** Injected breakpoint code → Go.

The hint channel does not reuse the control connection. When MULTI hits a breakpoint while
the bridge is inside a call such as `Resume()`, re-entering the bridge's own runtime to
deliver a notification is a needless hazard.

UDP is chosen because it natively provides what a hint must be: best-effort, no connect, no
handshake, no back-pressure, and no way to stall the debugger when the listener is gone. A
TCP `connect` can block MULTI's command loop even with a timeout. Loss is acceptable because
polling is the source of truth (§6.3).

**Notifier preloading (unimplemented candidate path).** The debugger's in-process Python and
`mpythonrun` are different contexts, so functions defined in `bridge.py` do not appear
automatically. The repository may contain `notifier.py` and a UDP receiver, but the daemon **does
not** install an `AFTER_GHS_STARTUP_PYTHON` hook, and it must not be described as a working
notification path. The current breakpoint command list writes only a strict `mprintf` token,
which is parsed from `H`'s command list after the target stops:

```
b foo#12 {mprintf("HIT 0x9F2C41A7\n")}
```

If a notifier is integrated in the future, it then sends a token/nonce UDP datagram and swallows
exceptions; exceptions must not contaminate breakpoint-command execution. Whether or not this path
exists, the stop arbiter must remain correct without hints.

## 9. Session lifecycle

The daemon is long-lived and owns the MULTI session and the downloaded image, so that
attaching and detaching an IDE never re-launches MULTI, re-contends for the probe, or
re-downloads.

### 9.1 Daemon

- CLion and VS Code start a local stdio `proxy`; Zed/other clients may use loopback TCP. Every
  supported frontend must send DAP **attach** and must not silently convert `launch` into target
  attachment.
- exactly one DAP client at a time; a second attach is refused with a clear error rather
  than silently taking over
- mutating control requires the lease of §6.9

Commands: `serve`, `status`, `shutdown`, `doctor`, plus `proxy` (stdio front-end that
forwards to the daemon) so an IDE that insists on launching its own adapter process needs no
architectural change.

The supported editor path is `proxy --config <absolute-project.toml>`. It binds
the owner-only record to a versioned SHA-256 digest of every normalized,
validated runtime field before authenticating or forwarding DAP. Record paths
and the physical-probe lock remain keyed by `probe.id`; a same-ID record with a
different semantic digest fails closed and is neither reused nor removed.
`proxy --probe-id <id>` remains only as an explicit legacy identity-only escape
hatch and is not the CLion contract. A stable ID and the semantic digest are not
control credentials.

`proxy` does not first query `status` with a control token and then create a second connection to
the returned `DAPAddress`. After receiving an upgrade ACK on the same authenticated control socket,
it directly calls the daemon's `dap.Server.ServeConnection`. Control authentication and DAP
ownership are therefore bound to the same TCP connection, and a loopback-listener rebind after the
daemon exits cannot insert a TOCTOU race between status and dial.

CLion's Before Launch runs a synchronous, short-lived `ensure`. It first requires the live
control record to match the validated configuration digest and then authenticates it; if present,
it returns `already_ready` directly. Otherwise,
acquisition must be chosen explicitly by the caller: the default and explicit `warm` require the
configured exact primary ELF and bind only one existing window; explicit `cold` forbids a primary
ELF and router arguments and directly derives `serve --session-mode cold`. Neither mode falls back
to the other. Concurrent ensure calls converge on one daemon through an owner-only control
reservation, and the daemon runtime's probe lock then prevents duplicate use that bypasses the
control record. The parent passes its expected digest to the child, which compares it immediately
after loading TOML and before reserving control state or starting the runtime; a configuration
change between the two reads therefore fails before MULTI is touched. The parent terminates only
the serve child it created when readiness fails; on
Windows, the ensure child and its descendants are in a kill-on-close Job, and other MULTI sessions
are never cleaned up by process name.

### 9.2 Attach sequence

The DAP frontend has its own state machine, distinct from the target state machine. Because
the daemon is long-lived, the target may already be stopped when a client attaches, and
configuration must complete before any state is published:

```
initialize request
   ↓
initialize response (capabilities)
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

`disconnect` only detaches: MULTI is not reset or re-downloaded. DAP-owned breakpoints transfer to
stopped-bound cleanup under §6.5; non-perturbation of `bp_clear` while running is unverified, so
they cannot be guaranteed to be removed immediately on disconnect.

This is an explicit DAP compatibility decision. The general DAP model for attach sessions is
that the debuggee continues after disconnect, and the protocol later added `suspendDebuggee`
to express whether it should stay suspended. The local CLion 2026.2.1 implementation sends an
explicit `terminateDebuggee:false` for detach; the frontend accepts explicit false but continues
to fail closed for `terminateDebuggee:true` or `suspendDebuggee:true`. A limited
native CLion GUI-and-hardware smoke test has covered cold attach, stacks,
top-level inspection, read-only memory/disassembly, and breakpoint
synchronization. Actual breakpoint hits, full execution control, and real
disconnect-safety qualification remain open; VS Code and Zed still require
independent GUI/hardware acceptance.

The current policy does not proactively change execution state for cleanup during disconnect; this
does not mean the hardware safety of `bp_clear` has been verified.

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

Cold connection preparation is independently explicit:

```toml
[connection]
preparation = "already_present_no_verify"
```

This mode is restricted to cold acquisition. After `ConnectToTarget` succeeds, the bridge runs
the documented `prepare_target -verify=none` command on the retained Debugger window. It is the
MULTI “Program already present on target / Verify: Not At All” action and does not download,
flash, reset, or verify target memory. Omitted preparation and `"none"` are inert. Warm acquisition
rejects the field, and an input-requiring MULTI response fails the startup instead of opening or
automating a dialog. After a successful cold `ConnectToTarget`, preparation is transactional: a
refusal or exception first issues exactly one `Disconnect(0)` before reporting the sanitized
preparation failure. A disconnect failure is reported as a separate cleanup failure and is never
represented as a successfully cleaned session.

## 10. Error handling and recovery

| Class | Handling |
|---|---|
| Configuration error | `serve` refuses to start; never surfaces as a debug-session failure |
| Startup failure (MULTI, probe, license) | diagnosed and reported by specific cause; never a generic "connection failed" |
| Cold bootstrap after confirmed `open` | If `open` in the same bridge generation has explicitly succeeded and `cores` or initial state subsequently fails, the Actor queues a typed `close` rollback; both bootstrap and rollback errors are retained |
| Warm bootstrap / unconfirmed `open` | **Never** send `close`; they do not prove multi-dap ownership of an existing MULTI session |
| **Daemon duplicate or binding collision** | single-instance lock and record path keyed by the configured probe identity; matching semantics converge, while a same-ID record or startup marker with a different validated digest fails closed without reuse, deletion, authentication, or shutdown. This proves local configuration identity, not hardware serial identity |
| **External probe contention** | a separate error class: the probe is held by a process multi-dap does not manage (a MULTI GUI, a stray target server). Detected from the target server's own failure, reported with evidence |
| Leftover processes | detected at startup and **reported, not killed**; `doctor` prints executable paths and parent/child PIDs so a human decides |
| MULTI refused a command | bridge emits a stable `kind` and sanitized `message`; reserved `raw` remains empty; DAP error response carries no target diagnostic |
| Illegal target state (resume before reset, when required) | refused in Debugger Core; nothing is sent to the bridge |
| Timeout | only Go sets deadlines; the bridge sets none |
| Bridge death | DAP `terminated` is emitted; the daemon does not pretend the session is alive; target state is preserved |
| Unexpected GUI-initiated change | reconciled and published (§6.8); reset/download by GUI is reported as a session fault |

The bridge never retries. Retry and backoff policy live in Debugger Core.

Rollback can also be indeterminate because of transport poison, a stale generation, or MULTI
refusal. In that case, no speculative second close is attempted; the Actor is placed in
fail-closed/reconciliation-required. Lifecycle host tests cover this close policy, which does not
imply that cold or warm hardware sessions are recoverable.

**Poisoned connections.** A Go deadline can stop waiting; it cannot cancel an in-flight
MULTI-Python call. Once an RPC deadline expires, the bridge and its connection are marked
unhealthy, the `BridgeGeneration` is advanced, and the connection must not be reused. Every
completion from the previous generation is discarded on arrival (§6.1) — a stalled call that
returns minutes later must never be mistaken for a current result. What recovery is possible
depends on M0-1:

- while the MULTI service router is healthy **and the exact Window Register program binding has
  been proven for this project**: kill the bridge, restart `mpythonrun` with the router's loopback
  host/port, bind the unique live program window without reconnecting the emulator, resynchronize
  state
- if it cannot: an RPC timeout effectively loses the debug session, which becomes a
  documented product limitation

A dead service router is not the same recoverable case as a dead bridge. Restarting without the
old router starts a second MULTI world, may consume another license, and cannot be treated as
reattachment to preserved state.

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
- **Fencing tests.** A completion from a superseded `BridgeGeneration` or
  `FrontendGeneration` must be discarded, including the case where it arrives after a
  successful reconnect.
- **Transaction/race tests.** The breakpoint state lock does not span `Set`/`Clear`, the detach
  fence does not let an old owner revive after a concurrent transaction, and natural cleanup retry
  is bounded for every `StopEpoch`. These do not replace M0-7.
- **Bootstrap/proxy tests.** Only a confirmed cold `open` may be typed-closed after `cores`/initial
  state fails; warm/unconfirmed open may not be closed. Proxy-upgrade validation does not dial the
  recorded `DAPAddress`, so a listener rebind cannot hijack DAP.
- **Inspection adversarial tests.** Host tests cover parser input limits, prior validation of a
  complete snapshot, locator ASCII/control rejection, and absence of partial handle leakage.
- **Transition tests.** Externally-initiated transitions produce `continued` then `stopped`
  in order; a missed cycle detected by stop generation produces the same pair late rather
  than being dropped.
- **On-board smoke.** One scripted pass: open → download → reset → breakpoint → stop →
  stack → eval → resume.
- **CI invariants.** The layering checks of §5, the `run_commands` call-site restriction of
  §7.4, and the loopback-binding invariant of §7.6.

## 12. Milestones

**M0 — hardware reconnaissance. The status in the table below is authoritative; observed facts,
probes not yet run, and future architecture candidates are strictly distinguished. Full evidence
is in [m0-findings.md](m0-findings.md).**

| # | Question | Status | What it changed |
|---|---|---|---|
| 1 | Can `mpythonrun` attach to an existing MULTI session, or only create its own? | **Answered for acquisition.** Same-router, exact-full-path warm binding has passed; repeating cold connect remains unsafe | automatic recovery remains fail-closed because a blocked window command has no supported cancel/timeout |
| 2 | Can a Python 2.7 script inside `mpythonrun` run a resident socket loop while MULTI-Python calls are made? | **Open.** `socket` and `threading` import cleanly; probe written | §7.1 stands provisionally |
| 3 | `python` is GUI only — can the MULTI window be hidden or minimized in daemon mode? | **Substantially answered.** The whole capture ran minimized with no loss of function; `goaway` / `comeback` are documented for exactly this | daemon presentation |
| 4 | **Finalize the inspection contract.** | **Answered.** No structured API; command text only, but one-level expansion and array indexing both work | §7.3 frozen; expression path is the value locator |
| 5 | Breakpoint Python execution context | **Half answered.** Command lists can be set, listed, and fired, and are reported back by `H`; Python in a command list is untested | The current identity is a strict `mprintf` token plus `H` parsing; UDP/notifier remains an optional seam |
| 6 | `state()` polling: cost, blocking behavior, and **does it perturb the target?** | **Open — highest risk.** `GetStatus()` costs 0.71 ms, `GetCurPrInfo("")` 9–30 ms; the differential experiment now fail-closes unless its configured target progress expression is readable | §6.3 entire event model |
| 7 | Does `bp_clear` perturb a running target? | **Open.** The safe warm-only probe has not run | Transfer owner after disconnect and wait for natural stopped-epoch cleanup |
| 8 | **Execution primitive blocking semantics.** | **Answered.** `block` is an explicit parameter; `Resume(block=0)` returns in <1 ms; MULTI-Python's blocking mode is a client-side poll loop | **§6.1's second-control-channel branch does not fire** |
| 9 | **Stop-reason and stop-generation observability.** | **Answered.** `stopStamp` increments by one on every confirmed stop and never otherwise; `H` plus the flag fields give the reason | **§6.8 holds in its supported form**; `StopEpoch` derives from `stopStamp` |

**M1** — daemon, attach sequence (§9.2), `state`, `resume` / `halt`; a client connects and
sees core state. Early in M1, validate DWARF scanning for the source index (§6.6).

The delivery status below does not change the final architecture and acceptance scope defined in
the preceding sections:

| Milestone | Integrated scope | Explicitly uncommitted scope |
|---|---|---|
| **M2** | breakpoint/stop arbiter, owned breakpoint, stop-epoch/deduplication; state lock does not span `Set`/`Clear`, detach fence, and bounded retry per epoch; p12-validated `l f` source resolver and per-Resolve snapshot; direct DAP Set/Clear and native CLion synchronization on a stopped target | The transaction behavior above is primarily host/race evidence and does not replace M0-7; source breakpoints fail closed when any configured core lacks definitive `Presence`; actual hits await acceptance |
| **M3** | top-level stack frame, one-level aggregate children, service snapshot paging, parser/snapshot/locator failure boundaries | variable type/paging is negotiated by client `initialize`; hover, format, and non-top-level frames fail closed |
| **M4** | `next` and `stepIn` transport/control path for one configured core | multi-core requires an `ExecutionDomain` with complete before/after observation; it has no production executor and is refused before I/O; `stepOut` and run-to are unproven; p04 has not run |
| **M5** | stopped/core-routed `readMemory`; RH850 `disassemble` (actual opcode bytes); stop-bound frame PC reference; CLion hardware acceptance at 8/32/128/256 lines | `writeMemory`, registers, nonzero `instructionOffset`, automatic pointer-variable memory references, and instruction-level stepping fail closed |
| **M6** | config-bound `status`, `shutdown`, `diagnose`, and same-socket `proxy` upgrade; deterministic packaging; VS Code local proxy source extension; CLion 2026.2.1 Cidr DAP CMake Debug/framing/disconnect-false contract; and bounded native CLion hardware smoke | legacy `proxy --probe-id` is identity-only; breakpoint-hit and execution acceptance, VS Code GUI, Zed, and stopped-target hardware disconnect await acceptance |

The warm-session router host/port and primary ELF are runtime inputs to `serve`, not project TOML.
Binding accepts only a window whose full path is unique in the Window Register, whose state is
stable, and whose process information is nonempty; failure of any threshold is refused, with no
fallback to a second router, cold reconnect, basename, or first window. This binding and the
program-component topology of the two configured cores have passed on a stopped target. A read-only
phase probe after the latest native CLion attempt shows that Window Register enumeration still
succeeds, but the first debugger `GetProgram()` blocking `RunCommands` received no reply; the public
interface has no call-level cancel/timeout. The current gate is therefore for an operator to restore
command dispatch in that debugger window before continuing the Cidr smoke test.

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
| Epochs bound to observed transitions, not to accepted commands | The GUI can start execution without multi-dap issuing anything; command-bound epochs would suppress legitimate events | `ExecutionEpoch` incremented on accepted execution start |
| `continued` published symmetrically with `stopped` | A GUI-initiated resume has no DAP request to respond to, so the event is the only way the client learns execution resumed | Recording resumption internally and only publishing stops |
| Every executor completion fenced by `BridgeGeneration` / `OperationID` | A deadline stops the wait but not the call; a stalled RPC returning after a bridge restart would otherwise be accepted as current | Treating a completion as valid because it matches an outstanding request |
| Logical breakpoints are atomic across cores | `verified = false` while a breakpoint is quietly live on one core is the more damaging failure; ownership records survive so orphans can be retried | Best-effort partial breakpoints |
| Handles invalid the moment execution resumes, keyed by session-global `StopEpoch`, with monotonic IDs | Matches DAP's suspended-state reference lifetime; monotonic IDs make stale references fail loudly instead of resolving to a different live object | Validity until the next stop; per-core epochs; resetting the ID allocator after each resume |
| Go-generated opaque hint tokens | The command list needs an identity before MULTI assigns a handle; also keeps DAP concepts out of the bridge and avoids assuming MULTI handles are globally unique | Passing the MULTI breakpoint handle to the notifier |
| UDP loopback for hints, separate from control TCP, with a session nonce (candidate) | If integrated in the future, it can avoid blocking the MULTI command loop; polling covers loss, and the nonce limits local flooding | Reusing the bridge's control connection for notifications |
| Loopback-only binding as an architectural invariant | The bridge can resume, reset, and write memory; a DAP client has full debugger control | Treating listener exposure as a deployment concern |
| Cores as DAP threads, single-thread execution advertised false | Honest about the freeze group instead of simulating per-core control | Pretending per-thread stepping works |
| MCP as a frontend above Debugger Core, gated by a control lease | Preserves the single-owner property; serialization alone does not prevent conflicting intent between two controllers | Hanging an MCP shim beside the DAP server on the same bridge; relying on the actor alone to arbitrate two frontends |
| Inspection method group provisional until M0-4 | DAP's variable model is a lazy paged tree; freezing a flat contract now would lock in the wrong shape | Freezing the full method table before knowing what MULTI exposes |
| `require_reset_after_download` as a configuration capability | The repository is target-agnostic; the requirement is a property of the target, even though the default is conservative | A global hard-coded rule derived from one target's behavior |
| Semantic CI checks over a hard line limit for the bridge | M0 may legitimately require MULTI text normalization in the bridge; terminology and responsibility are what matter | A 300-line hard cap as an architectural guarantee |
