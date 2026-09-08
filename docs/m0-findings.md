# M0 hardware findings

**Date:** 2026-08-18
**MULTI:** 7.1.6d, `mpythonrun.exe`, embedded interpreter Python 2.7.3 32-bit (`svc_python.exe`)
**Target:** a multicore device connected through a hardware debug probe; some cores have an
executable, and the project is described by a multicore configuration file. This document does
not record the target, server, connection parameters, binary paths, addresses, command text, or
raw state dumps.
**Evidence:** ten staged captures under `recon/` and `.hwscratch/`, both gitignored because they
contain target symbol names. This document is the scrubbed, committed record.

Every claim below is backed by an observation, not by a manual. Where the shipped manuals and
the hardware disagree, the hardware wins and the disagreement is stated.

---

## 0. Environment constraints discovered before any blocker

These were not in the architecture document and each one constrains the daemon's design.

### 0.1 `mpythonrun.exe` requires a real console and cannot have its handles redirected

Writing its standard output through `WriteConsole`, it fails with a modal Windows dialog when
stdout is a pipe or a file. The capture records only the failure class; handle values, byte
counts, and platform error codes are intentionally omitted.

The daemon must therefore launch the bridge with `CREATE_NEW_CONSOLE` and never capture its
standard streams. This is an argument *for* the §7.1 decision that `bridge.py` owns its own
socket: stdout was never going to be a usable channel. Every probe in this project writes JSON
to a file for the same reason.

### 0.2 A bare `help` wedges the interpreter permanently

A no-argument interactive help action never returns and leaves a viewer process running; its
topic-qualified variant returns promptly. A target-preparation action with omitted mode has the
same GUI-prompt risk. Any command that can raise a dialog is a hazard to a headless daemon, and
the bridge must carry a refusal list rather than discovering these one at a time.

### 0.3 A killed process leaves no evidence

The first attempt at the transition battery was killed on a timeout and its `finally` block never
ran, destroying the whole capture. Every probe now flushes its JSON after each step. The same
reasoning applies to the daemon's own diagnostics.

### 0.4 The command target is a window object, not the debugger object

`GHS_Debugger()` is a builtin in this interpreter — no import. Its command target is whatever
MULTI window it last bound to, exposed as the `cmdExecObj` attribute. A staged open/connect capture
established that subsequent command execution must use the returned debugger-window object, not
the debugger connection object.

Issuing commands on the connection object after connecting binds them to a window without an
active process; subsequent actions fail while a plausible status query can still return. Three
separate captures were lost to this before it was understood. The MULTI
Driver must hold the window object explicitly and must never let a stray call rebind it.

### 0.5 Warm discovery is scoped by the MULTI service router; rebinding remains gated

`mpythonrun` does not rediscover a live Debugger from the target-server argument alone. If it is
started without service-router coordinates, it creates a private `svc_router`, which creates a
second `multi.exe`. On this installation that second Debugger failed before the probe script ran:
the modal license error reported that the required feature was unavailable. The feature identifier
and vendor-specific wording are intentionally omitted.

The live router was observable as a loopback listener owned by `svc_router.exe`. Joining it with
the documented service-router runtime coordinates ran the environment probe inside that router
without creating a second MULTI process or requesting another Debugger license. Those coordinates
are session runtime state, not project configuration.

**Consequence.** Bridge recovery must preserve the service router and rejoin it explicitly. A
bridge restart and a service-router restart are different failure classes: the former can preserve
the Debugger windows; the latter cannot be assumed to do so and may require another license.
Merely joining the router is necessary but not sufficient to prove that the intended program
window was rebound.

The shipped Window Tracking API supplies the correct discovery substrate:
`GHS_WindowRegister.GetWindowList()` plus `CheckWindows(..., winClass=debugger)` reconstructs
registered `GHS_DebuggerWindow` objects whose own registration IDs route commands. The warm probe
now uses that API read-only and requires one unique, full-path `GetProgram()` match in a stable
Stopped/Running state with non-empty process information. It never calls `DebugProgram`,
`ConnectToTarget`, or `Disconnect`, and it has no basename or first-window fallback.

A controlled reproduction exposed why that distinction matters. A cold staged open returned from
the program/load, connection, and status API calls. A subsequent probe joined the same router but
repeated the cold sequence; MULTI displayed a modal external-connection failure before the socket
phase began. Only that probe's new Python service process was stopped; the original Debugger,
router, and target server remained alive. After the warm path was corrected, the read-only binding
probe failed closed because no active registered window exactly matched the configured primary
program identity. That is a configuration/selection boundary, not permission to weaken identity
matching.

A later diagnostic pass recorded only aggregate filter counts: one registered Debugger window and
one readable program identity, but no exact configured-program match. Therefore there were no
stable-state and non-empty-process matches. A stale saved router coordinate failed before the probe
entered Python; resolving the sole current loopback listener owned by the existing `svc_router`
allowed the read-only probe to run. This distinguishes an ephemeral router coordinate from
program-window identity. Neither pass recorded program name, path, basename, extension, listener
port, process identity, or start time.

**Latest status (do not infer a recovery path from this).** The most recent cold startup returned
from the program/connection APIs, but had no usable target process state; those calls returning
does not prove that a usable target session exists. The most recent warm summary contained one
registered, readable window and zero exact/stable/non-empty matches; only the process created by
that probe run was stopped. The remaining hardware gate is the external target/emulator
connection, not evidence that a second service router was created. The router host/port remains a
CLI runtime input and must never be persisted in project TOML.

---

## 1. M0-1 — Can `mpythonrun` attach to an existing MULTI session?

**Answer: partial. Router-scoped window discovery is proven; a safe program-window rebind is not
yet accepted.** A historical capture showed a second process in the same router returning from
`ConnectToTarget` in **0.3 s**, but it did not prove which program window was selected or that the
sequence was safe across a bridge lifetime. The controlled reproduction in §0.5 shows that blindly
repeating the cold connect can instead request the already-owned emulator and raise a modal error.
The Window Register gives a non-connecting rebind mechanism, but the current live session did not
satisfy its exact program-identity gate.

**Consequence.** An RPC timeout must currently terminate the adapter session fail-closed. The daemon
may preserve the service router and Debugger for an operator, but automatic recovery is enabled only
after the exact Window Register binding passes end to end for the configured project. It must never
fall back to calling the cold `open` primitive against that live router.

**Caveat.** Reattaching gives you *a* window, not necessarily the right one — see §0.4. The
resynchronisation path must re-select the intended component explicitly.

---

## 2. M0-2 — Can a resident socket loop run inside `mpythonrun`?

**Answer: not yet measured on hardware.** `socket` and `threading` both import cleanly in the
embedded interpreter and `threading.activeCount()` is 1 at startup, so nothing structural
forbids it. The probe (`recon/probes/p02_socket.py`) tests a background-thread accept loop and a
single-threaded `select` loop, both interleaved with MULTI-Python calls.

The latest attempt did not reach either socket phase: the old warm path repeated the cold target
connection and raised the modal error described in §0.5. The corrected probe first performs the
strict read-only Window Register bind; that bind currently fails its exact identity gate. This
blocker therefore remains open, without negative evidence against resident sockets. §7.1 stands
provisionally.

---

## 3. M0-3 — Can the MULTI window be hidden in daemon mode?

**Answer: yes, with a documented mechanism, not yet exercised.** `DebugProgram` creates a
debugger window whether or not one is wanted, and the whole capture ran with the MULTI process
minimised without any loss of function — every command, every state query, and the download all
worked. The manuals additionally document `goaway` (hides the Debugger and all debug-related
windows) and `comeback`, explicitly intended for "MULTI being externally controlled via a command
script".

The remaining question is narrow: whether commands still work *after* `goaway`. That is what
`recon/probes/p03_gui.py` tests. Note the manual's own warning — after `goaway` there is no
interactive way to issue `comeback`, so the daemon owns that responsibility.

---

## 4. M0-4 — Finalise the inspection contract

**Answer: MULTI exposes no structured inspection API. Everything is text, and the text is rich
enough.**

`GHS_Debugger` has no `GetStack`, `GetLocals`, `Evaluate`, or `GetFrames`. The available structured
read methods did not yield usable inspection values in the capture. Inspection therefore goes
through the raw-output command API and its output field.

The raw-output command API returns a **boolean**, not text; its status field indicates success or
failure, and a separate field holds output. In non-blocking mode the call returns immediately but
the output field is not populated — non-blocking mode cannot be polled through that field.

What the text yields, all captured verbatim as golden fixtures:

| Operation class | Scrubbed, stable output shape |
|---|---|
| Stack listing | one line per frame: index, selected marker, signature, and source location; an extended variant includes indented locals |
| Local listing | name/value with liveness and out-of-scope markers; address/location variants may resolve to a register rather than memory |
| Global and file-static listing | name/location shapes differ by storage class |
| Register and type listing | registers, bitfield members, and typedefs are representable in text |
| Frame selection | a source/function/location record; a numeric variant selects a frame |
| Expression evaluation | scalar output with liveness; aggregate and member evaluation expand exactly one level |

**The two questions that decide the DAP variable model both answer favourably.** One-level
expansion exists (`print <path>` on any sub-expression), and array elements are individually
addressable by index. So the provisional shape §7.3 sketched is viable:

The implemented mapping is: frame-local listing after frame selection; child evaluation from an
opaque expression path plus index; and expression evaluation in the selected frame.

with **the expression path string as the opaque MULTI value locator**. No handle needs to be
invented on the MULTI side; Debugger Core maps its own `variablesReference` to a path.

**Failure form is uniform:** an unknown-identifier diagnostic with command status failure.

**Two cautions.** The breakpoint-output formatter does *not* resolve locals that expression
evaluation resolves, so it is not a substitute. Selecting a non-zero frame then listing locals
produced only out-of-scope variables in the capture, so frame-scoped locals need more work before
M3.

The inspection method group of §7.3 can now be frozen. The parsers live in `internal/multi`,
which §5 explicitly permits.

---

## 5. M0-5 — Breakpoint Python execution context

**Answer: partial. The command-list mechanism works; the Python-inside-it half is untested.**

Breakpoints with command lists set, list, and fire correctly. The emitted list and halt-cause
report include the adapter-owned marker, and the implementation accepts only a byte-exact marker
generated by multi-dap. After a stop the marker resolves the owned logical breakpoint for DAP
`hitBreakpointIds`; UDP is not part of this production path. The committed record deliberately
omits the marker encoding, source location, command text, and all target tokens.

The list output also records per-breakpoint reachability and inactive state.

Still unmeasured: whether a Python notifier can run in that context, whether an
`AFTER_GHS_STARTUP_PYTHON` preload reaches it, timing under rapid repeated hits, and whether
`$restart` re-runs the hook. The repository's notifier/UDP receiver remains an optional seam;
the daemon has not installed this hook or connected it to the runtime.

---

## 6. M0-6 — Does state polling perturb a running target?

**Answer: cost measured, perturbation not yet measured.**

| Primitive | Cost |
|---|---|
| `GetStatus()` while the target is running | **0.71 ms** per call |
| `IsRunning()` while stopped | 1.44 ms per call |
| `GetCurPrInfo("")` | 9–30 ms per call |

`GetStatus()` returned `running` on all 300 consecutive samples with no observed disturbance, and
the target continued to execute across them — but "did not obviously break" is not the same as
"did not steal target time". The differential rate experiment in `recon/probes/p06_poll.py` is
what settles it. An earlier candidate memory address was rejected on re-check because it produced
a constant value or `No Process`; it was never a valid progress counter. The probe now takes a
target-owned progress expression from the ignored local configuration and refuses to measure until
two reads prove that the expression is accessible. On the current target that candidate is the
volatile loop counter in the idle path; its accessibility still has to be confirmed on hardware.

This remains the highest-risk open item. §6.3 stands provisionally.

---

## 7. M0-7 — Does `bp_clear` perturb a running target?

**Answer: not yet measured.** safe warm-only p07 has not run, so neither deletion nor an
alternative command may be called non-perturbing on a running target. The production policy does
not halt for breakpoint housekeeping: transfer detached ownership to pending cleanup, retain any
orphan record, and retry in a bounded pass at a later natural stopped epoch. The eventual probe
must compare all candidate cleanup operations against a running target before this policy is
relaxed.

---

## 8. M0-8 — Execution primitive blocking semantics

**Answer: blocking is the caller's choice, and it is simulated in Python. The second control
channel is not needed.**

Three independent observations:

1. `Resume`, `Halt`, `Step`, and `Next` all take an explicit `block` parameter:
   `Resume(self, block, printOutput)`, `Step(self, block, printOutput, stepIntoFunc)`.
2. `Resume(block=0)` returned in **0.000 s** and `GetStatus()` immediately afterwards reported
   `running`.
3. The debugger object carries `simulateBlockingWithNonBlocking = True` and
   `checkInterval = 0.5`. **Blocking is implemented client-side as a poll loop in MULTI-Python**,
   not as a blocking call into MULTI.

So a blocking execution primitive cannot wedge the transport — it is a Python `while` loop the
caller chose to enter. `Halt` and `GetStatus` remain issuable throughout, and the actor never has
to wait behind an execution call.

**Consequence.** The branch flagged in §6.1 — "if execution primitives block, a second control
channel is required and the one-bridge-connection premise fails" — **does not fire**. §6.1 stands
as written, and the Session Actor / Bridge Executor split remains justified on its original
grounds (responsiveness to hints, deadlines, and disconnects) rather than on necessity.

The daemon should nonetheless always use `block=0` and run its own wait loop, so that the
deadline policy stays in Go as §10 requires.

---

## 9. M0-9 — Stop reason and stop generation

**Answer: both exist. This is the most consequential finding of M0.**

### 9.1 `GetCurPrInfo("")` is a combined state and stop-info primitive

Called on the debugger window it returns a flat dictionary of **173 fields** of MULTI's internal
process state in 9–30 ms. Among them:

| Field | Meaning |
|---|---|
| `stopStamp` | a monotonic stop counter — see below |
| `contCount` | a resume counter |
| `lifeCycleCount` | process generation |
| program-counter fields | current, previous, and nested instruction location |
| `file`, `iln`, `proc` | current source file, line number, function |
| `stackdepth`, `last_stackdepth` | stack depth now and at the previous stop |
| `fStoppedOnException` | stopped on a fault |
| `fContFromBp`, `fInStepMode`, `fPendingHalt`, `fTempHalt`, `fHalt`, `fAnalyzingStop`, `fFakeStop` | stop-condition flags |
| `fDownloaded`, `fLoading`, `fProgrammingFlash`, `fDoingHardwareReset` | lifecycle flags |
| memory-map fields | target ROM/RAM boundaries |
| `pid`, `pidParent`, `exitCode`, `dying` | process identity |

It is undocumented — the shipped manuals do not mention `GetCurPrInfo` or `GetProcessAttribute`
at all — so the field set is a captured contract, governed by the same discipline §7.4 imposes on
`run_commands`. Two encoding traps: some keys carry **trailing spaces in the key itself**, and
values are strings of mixed radix.

`GetProcessAttribute(idx, name)` returned `False` for every index 0–31 and every name tried; it is
not a usable route to this data.

### 9.2 `stopStamp` is the monotonic stop generation §6.3 requires

The scripted state-transition battery recorded aggregate results only: every confirmed
Running → Stopped transition incremented `stopStamp` by one; samples while running and repeated
halt actions while already stopped left it unchanged. This included step, next, explicit halt,
and a breakpoint-induced stop. No program counter, address, command transcript, or raw state
sequence is retained in the committed record.

It increments by exactly one on every confirmed Running → Stopped transition, regardless of
whether the stop came from a step, a halt, or a breakpoint, and it does **not** move when the
target is already stopped or while it is running. That is precisely the semantics §6.3 specifies
for `StopEpoch`.

It also advanced during the download sequence, counting MULTI's internal stops. That is harmless
and arguably correct: those *are* stops.

**Consequences.**
- The missed-cycle problem of §6.3 is solvable. Two consecutive `Stopped` samples with an
  unobserved run between them are distinguishable, because `stopStamp` differs.
- **The GUI coexistence policy of §6.8 resolves to the left-hand column.** GUI-initiated stepping
  is supportable while a DAP client is attached; the degraded right-hand column does not apply.
- `StopEpoch` should be *derived from* `stopStamp` rather than counted independently, so that a
  stop multi-dap never observed still advances it.

### 9.3 Halt reporting yields a textual cause

The halt-report operation yields categories for user halt, breakpoint, and non-running state;
when present, its adapter-owned marker identifies the firing breakpoint. This is enough to
populate DAP's `reason` without retaining raw output or command text.

**Fidelity caveat, and it matters.** In one battery the halt-report operation retained a prior
breakpoint category after an explicit halt, when the honest answer was a user halt. It appears to
report the most recent *notable* cause rather than the cause of the current stop. Until that is
characterised further, the adapter must corroborate it against the flag fields
(`fContFromBp`, `fInStepMode`, `fPendingHalt`, `fStoppedOnException`) and `$_BREAK`, and prefer a
conservative reason over a confidently wrong one — exactly the posture §6.3 already prescribes.

---

## 10. Multicore model

The process listing identifies executable core slots separately from auxiliary slots. The component
listing provides one route per core and supports selecting a component without changing the
current selection. An older process-selection form is deprecated. The exact routing strings,
numeric identifiers, status rows, and command output are omitted because they are target/session
data.

The synchronous-debugging flag was enabled, confirming the §6.7 thread model against hardware
rather than assumption. The selected-core identity is available to per-core breakpoint conditions.

**Consequence for §6.5.** Core-to-thread mapping is available directly from `P` and `components`;
no configuration file needs to declare it. The multicore project already declares which executable
belongs to which core, and MULTI applies `connect`, `prepare_target`, and friends across every
core it lists.

---

## 11. DWARF scanning is not available on this toolchain

The §6.6 source index planned to scan each executable's DWARF line table to learn which cores
contain a given source file. **The compiler output in the capture carries no DWARF.** The binaries
open as ELF, but the DWARF reader reports absent or unusable debug information. The section table
contains symbol tables plus toolchain-proprietary metadata; debug information lives in sibling
proprietary database files. Exact ELF properties, section names, reader errors, and all paths are
intentionally omitted.

A control experiment confirms the Go side is sound: a cross-compiled Go binary used as a test
fixture parses correctly, DWARF version 5, with `DW_AT_comp_dir`, `DW_AT_name`, and the line
table all readable.

**Current conclusion:** when the DWARF fallback is unavailable, the production resolver uses the
read-only full `l f` list confirmed on hardware by p12: the exact heading
`--------  File names  --------` followed by contiguous `N: X:\absolute\path.ext` lines. It then
uses canonical exact membership to produce per-core `Present`/`Absent`. Each Resolve rereads the
list to avoid stale caching after an external GUI reload of an ELF at the same path; any grammar,
routing, or I/O uncertainty remains `Unknown` and rejects the source breakpoint. Direct DAP
Set/Clear and native CLion synchronization have been observed on a stopped target; an actual hit
remains to be accepted.

### 11.1 `p03_m2_source_known.py`: warm-only inventory, not source-resolver evidence

`p03_m2_source_known.py` performs a public callable/signature inventory only for each configured
core that passes the strict warm identity gate; names are matched against `browse`, `source`,
`file`, `line`, and `module`. It sends no debugger commands and does not create, delete, or
enumerate breakpoints.

The local `script.pdf` and the bundled Python implementation eliminate the earlier symbol-API
candidates: `CheckSymbol`, `GetSymbolAddress`, and `GetSymbolSize` all execute expressions
through `RunCommands`; `PrintFile` also executes `dbprint f` (`script.pdf` p.254). `script.pdf`
offers no public Source Files/file-list API, so p03's documented, read-only, command-free
allowlist is currently empty and the probe has no call phase that local configuration can arm.

The inventory cannot prove that MULTI can resolve a source path or `line`, cannot justify
breakpoint placement, and cannot elevate `Presence` from `Unknown` to `Present`/`Absent`; the
current conclusion remains `Unknown`.

### 11.2 `p12_m2_source_files.py`: documented `l f` grammar capture

The local `debug_cmd.pdf` p.115 explicitly says that `l f` lists all source-file names; the
`string` in `l f <string>` is only a contains filter. p12 therefore first performs an exact
warm-window inventory of all configured cores, then runs full `l f` route by route under stopped
state and strict `P`/`H` topology. It records only bounded grammar classes and counts, never
persisting source paths, component IDs, or raw output; it does not run a contains filter and
never determines `Presence` by substring. Hardware results have fixed the production parser's
exact grammar; the probe itself remains read-only, does not create, delete, or enumerate
breakpoints, and is not equivalent to the independently completed direct-DAP Set/Clear smoke or
the later native CLion synchronization observation. An actual hit remains a separate acceptance
item.

---

## 12. Status of the nine blockers

| # | Question | Status |
|---|---|---|
| 1 | `mpythonrun` attach to an existing session | **Answered for acquisition.** Router-scoped exact-full-path warm rebind works; automatic recovery remains fail-closed because blocking window commands have no supported cancel/timeout |
| 2 | Resident socket loop inside `mpythonrun` | **Open.** Nothing structural forbids it; probe written |
| 3 | Hidden / minimised MULTI window | **Substantially answered.** Ran minimised throughout; `goaway`/`comeback` documented; post-`goaway` command survival still to confirm |
| 4 | Finalise the inspection contract | **Answered.** Text only, but rich; one-level expansion and array indexing both work; expression path is the value locator |
| 5 | Breakpoint Python execution context | **Half answered.** Command lists work and identify the firing breakpoint; the Python-in-command-list half is untested |
| 6 | Does state polling perturb the target | **Open — highest risk.** Costs measured; differential experiment written with a fail-closed, target-configured progress expression |
| 7 | Does `bp_clear` perturb a running target | **Open.** The safe warm-only probe has not yet run; the production strategy waits for natural stopped-epoch cleanup after owner transfer rather than actively halting merely to delete. |
| 8 | Execution primitive blocking semantics | **Answered.** Caller's choice; blocking is a client-side poll loop. **§6.1's second-control-channel branch does not fire** |
| 9 | Stop reason and stop generation | **Answered.** `stopStamp` is a true monotonic stop generation; `H` plus the flag fields give the reason. **§6.8 resolves to the supported column** |

M0-2, M0-6, and M0-7 all remain incomplete measurements; M0-4/M0-5 likewise do not prove
completion of the source resolver or notifier runtime. Bridge recovery may be declared only once
strict warm program identity passes end to end.

---

## 13. M4/M5 evidence

The originally planned safe warm-only p04/p05 have not yet run. In an operator-authorized cold
session on 2026-09-07, with the target kept stopped, M5 read-only evidence was instead completed
with the GHS-documented `memdump raw` and `disassemble` commands; state was stopped before and
after the commands, then rechecked through CLion's native DAP path. The following conclusions
cover only RH850 and known Flash addresses.

| Capability | Probe evidence required before implementation | Current result |
|---|---|---|
| Step out | Read-only callable name/signature; one explicitly approved, non-blocking call; pre/post stopped-state snapshots and verified recovery halt | **Unverified — not executed** |
| Run to | Same sequence, with the destination supplied only in ignored local configuration and never copied into the summary | **Unverified — not executed** |
| Memory | Structured read callable signature; result type/absence only; stopped-state preservation | **Answered for bounded reads.** `memdump -noprogress raw` produces exactly the requested bytes; CLion successfully read a contiguous 16 bytes from known HSM Flash, with the target stopped before and after. |
| Registers | The M0-4 verified `l r` output shape, recorded without raw register text; stopped-state preservation | **Unverified — not executed** |
| Disassembly | Read-only callable signature before any call; one approved call only if that callable is actually discovered | **Answered for RH850 forward windows.** Documented command output was sampled and fixed the parser; Host/HSM opcode and 2/4-byte widths, plus CLion 8/32/128/256 lines, passed. Nonzero instruction offsets are not covered. |

The probes write every step atomically to ignored evidence. They do not serialize local connection
arguments, program names, absolute paths, addresses, memory/register values, or raw command text
into the committed findings. A missing API remains a negative result; it is never replaced by a
guessed MULTI command spelling.
