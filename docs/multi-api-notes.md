# MULTI 7.1.6d API and Command Surface — Mined from Shipped Manuals

Mined for Task 2 of `docs/superpowers/plans/2026-08-18-m0-recon-and-m1-skeleton.md`. This
file is the command/attribute reference that Tasks 4–10 draw `RunCommands` call forms and
`GHS_Debugger` method signatures from. It is **not** hardware-verified; it records only what
the shipped documentation says. Every item the manuals do not document is marked
`NOT DOCUMENTED - must be probed` rather than omitted.

## Step 1: Extraction method

Text was extracted with `pdftotext -layout` (Poppler, invoked from Git Bash) into per-manual
`.txt` files, then re-split on form-feed characters and re-joined with a `P####|` page-index
prefix per line (a small Python 3 script) so that grep hits could be traced back to a page.
Page numbers cited below are the **printed page numbers** that appear in each manual's own
running header/footer (e.g. "Green Hills Software 125" / "125 MULTI: Debugging Command
Reference") — confirmed by direct inspection of the extracted text around every citation, not
computed from a fixed front-matter offset. A handful of searches were cross-checked with a
second, non-`-layout` extraction (`pdftotext` with no flags) to rule out `-layout` inserting
spurious spaces inside identifiers; no discrepancies were found.

**Manuals read** (all under `D:/ghs/multi_716d/manuals/`):

| File | Title | Pages | Relevance |
|---|---|---|---|
| `script.pdf` | MULTI: Scripting | 396 | Primary — `GHS_Debugger` / `GHS_DebuggerApi` Python API |
| `debug_cmd.pdf` | MULTI: Debugging Command Reference | 344 | Primary — the Debugger command language passed to `RunCommands` |
| `debug.pdf` | MULTI: Debugging | 1010 | Secondary — the debugger user guide; status-column vocabulary, system variables, multicore/freeze-mode chapter |
| `start.pdf` | MULTI: Getting Started | 38 | Secondary — target-connection tutorial; checked, added nothing beyond `debug_cmd.pdf` |

Also present in the manuals directory but not mined (out of scope for the MULTI command
surface): `debug.pdf`'s sibling `edit.pdf` (MULTI Editor), `license.pdf`, `multi_release_notes.pdf`.
`multi_release_notes.pdf` was spot-checked for RH850/850eserv2 release notes and found to
contain none relevant to the scripting/command surface.

**Target-specific gap.** None of the four manuals mention `850eserv2` or `RH850` by name.
Every `connect`/`target`/`xmit` command description defers processor- and debug-server-specific
argument syntax to "the MULTI: Configuring Connections book for your processor family" or the
"Green Hills Debug Probes User's Guide" — neither of which is present in
`D:/ghs/multi_716d/manuals/`. The generic command forms below are documented; the exact
`dbserver_arguments` accepted by `850eserv2` are **NOT DOCUMENTED in the mined set — must be
probed** (or read from the saved MULTI connection / `850eserv2 -help`).

---

## Step 2 / Item 1: Commands reporting execution status, halt reason, or signal/exception info

| Command | Signature | What it reports | Page | Manual |
|---|---|---|---|---|
| `info` | `info` | Prints, as free text: debugging status, current target connection, (Linux/Solaris) core file status, child program status, output recording status, command recording status, case sensitivity status | 125 | debug_cmd.pdf |
| `H` | `H` | "Prints the cause of a halt." No documented output format or enumerated reason strings — one-line description only, no example | 170 | debug_cmd.pdf |
| `halt` | `halt [{commands}]` | Halts the current process without an interrupt; accepts a command list to run on halt | 170 | debug_cmd.pdf |
| `k` | `k [force]` | Kills the current process | 171 | debug_cmd.pdf |
| `signal` | `signal signal [pr=num]` | **Linux/Solaris targets only** — sends a signal | 176 | debug_cmd.pdf |
| `zignal` | `zignal signal [s][i][r][b][C][Q]` | **Linux/Solaris targets only** — configures signal stop/ignore/report/bell handling | 176 | debug_cmd.pdf |
| `P` | `P [[pr=num] subcommands]` | Lists per-process info; "used exclusively for multiprocess debugging, and most of the subcommands are used exclusively for native debugging" | 33–34 | debug_cmd.pdf |
| `l` (no arg) | `l` | Lists locals/params of the current function (see Item 3) | 115 | debug_cmd.pdf |

**Unix-only vs. embedded/multicore, explicitly marked by the manual itself:** `signal` and
`zignal` are stated to be "Linux/Solaris targets only" (page 176) — do not use them against
the RH850/`850eserv2` target. The `P` command's subcommand set (`b`,`c`,`e`,`f`,`i`) is
described as native-debugging-only fork/exec bookkeeping (page 34); it is not expected to be
meaningful for a freeze-mode embedded connection, though `P` with no subcommands ("lists
information... about all processes") may still be useful since MULTI represents each core as
a process slot (see Item 4).

**Read-only system variables acting as status fields** (MULTI: Debugging, pages 330–336,
queried in the command pane as `$_STATE` etc., or via `l s _` to list all of them):

| Variable | Meaning | Page |
|---|---|---|
| `_STATE` | Process state: 1=No child, 2=Stopped, 3=Running, 4=Dying, 5=Just forked, 6=Just exec'ed, 7=About to resume, 8=Zombied | 336 |
| `_BREAK` | The current breakpoint number (closest documented "why did we stop" field, but only meaningful for breakpoint stops) | 333 |
| `_LAST_COMMAND_STATUS` | 0/1 — whether the last MULTI command execution failed/succeeded | 334 |
| `_PID` | Process ID of the **selected core**, as reported by the debug server | 335 |
| `_PROCESS` | MULTI's internal slot number for the current process | 335 |
| `_SYNC_RC` | −1 no connection / 0 sync debugging off / 1 sync debugging on | 336 |
| `_CURRENT_TASKID` | Task ID of the currently executing OSA task | 333 |
| `_RTSERV_VER` | 2=rtserv2, 1=rtserv, 0=non-rtserv connection | 335 |

`_STATE`'s eight values line up exactly with the `status_*` int constants already recovered
from the live `GHS_Debugger.GetStatus()` object in `recon/out/probe01.json` (nil/no_process/
stopped/running/dying/forking/executing/continuing/zombie) — strong circumstantial evidence
that `GetStatus()` is a thin wrapper over the same internal state as `$_STATE`, though the
manual never states this connection explicitly.

**GUI status-column vocabulary** (MULTI: Debugging, "The Status Column", pages 20–22) — the
human-readable strings shown in the Debugger's target-list Status column, not confirmed to be
retrievable via any scriptable call: `Created`, `DebugBrk`, `Execing`, `Exited`, `GrpHalt`,
`Halted`, `HostIO`, `No TimeMachine Data`, `Not loaded`, `<OS Running>`, `Paused by
halted_core`, `Paused by halted_task`, `Paused by the debugger`, `Pended`, `Ready`, `Running`,
`Stopped`, `SysHalt`, `Waiting`, `Zombied`. For a synchronous multi-core halt specifically
(pages 687–688): the core that caused the halt (user halt, breakpoint, single-step completion,
hardware reset, or hardware exception) is listed as **Stopped**; every other core is listed as
**Paused**. This Stopped-vs-Paused distinction is the only documented halt/no-halt-cause signal
for multicore and is a strong candidate for how M0-9 should interpret a per-core stop, but
**whether it is exposed through any of `GetStatus()` / `GetCurPrInfo()` / `RunCommands("info")`
rather than only the GUI is NOT DOCUMENTED — must be probed.**

`NOT DOCUMENTED - must be probed`: a dedicated `status`, `mode`, `why`, or `reason` command;
an exception/fault-cause command distinct from `H`'s one-line description; whether `H`'s output
is a fixed enumerated set of strings or free text; whether any of the above is exposed as a
structured (non-text) value through `GHS_Debugger`.

---

## Step 2 / Item 2: Breakpoint commands — set with command list, list, delete, halted-target requirement

| Command | Signature | Notes | Page |
|---|---|---|---|
| `b` | `b [%bp_label] [@bp_count] [&] [/s] [/off] [/at\|/tt\|/type_gt...] [address_expression] [{commands}]` | Sets a breakpoint; `{commands}` is a command list executed when the breakpoint is hit (this is the mechanism `architecture.md` §8's notifier relies on). `/off` sets it inactive so it can be placed **on a running target without halting it** | 46–48 |
| `B` | `B [address_expression \| breakpoint_list]` | Lists breakpoints. Output line format: `ID bp_label location address count: flags commands` | 48 |
| `d` | `d [[/force] address_expression \| breakpoint_list \| %bp_label]` | Deletes one or more **software** breakpoints. With no args, deletes all breakpoints at the current line | 55 |
| `D` | `D [/at \| /tt]` | Deletes software breakpoints in bulk (scope depends on what's selected in the target list) | 56 |
| `dz` | `dz [soft\|hard\|sobp] [bp_ID_list]` (+ `-gui`, `-line`, `-list`, `-clear` forms) | Restores/lists/permanently clears previously *deleted* breakpoints | 56–57 |
| `hardbrk` | `hardbrk [at]` / `hardbrk [at] delete=num\|*\|expr` / `hardbrk [enabled\|disabled][rolling]...expr[{commands}]` | Lists / deletes / sets **hardware** breakpoints. `at` on a synchronous multi-core target means "any-core hardware breakpoint" — hit by any core | 59–61 |
| `watchpoint` | `watchpoint expr` / `watchpoint -delete` | Data write watchpoint; hardware-backed where available, else a single software watchpoint | 71–72 |
| `install_bp_on_request` | `install_bp_on_request -enable \| -install` | Freeze-mode setup-script command deferring breakpoint installation until the target signals it's safe (MMU-dependent scenarios) | 64 |
| `tog` / `Tog` | `tog [address_expression\|breakpoint_list]` / `Tog` | Toggles active/inactive without deleting | 70–71 |

**Halted-target requirement for deletion:** `NOT DOCUMENTED - must be probed`. Neither `d`,
`D`, nor `hardbrk ... delete=` states whether the target must be halted first. The only
explicit halted/running distinction found anywhere in the breakpoint chapter is `b`'s `/off`
flag, which exists specifically to let you *set* (not delete) a breakpoint on a running target
— its existence implies plain `b` (and by extension, deletion) may otherwise expect a halted
target, but this is an inference, not a documented statement. This gates M0-7 (Task 6).

**Multicore relevance:** `hardbrk at` and the "any-core hardware breakpoint" language (page 60)
is the only breakpoint-chapter text that is explicitly multicore-aware; ordinary software
breakpoints are stated elsewhere (MULTI: Debugging, page 686) to apply to all cores by default
when synchronous debugging is enabled.

---

## Step 2 / Item 3: Stack and variable commands and their output shape

| Command | Signature | Shape / notes | Page | GUI-only? |
|---|---|---|---|---|
| `calls` | `calls [maxdepth] [par\|nopar] [pos\|nopos] [local\|nolocal] [types\|notypes] [templatetypes\|notemplatetypes] [showallframes\|noshowallframes]` | Prints the current call stack as text to the command pane; default `maxdepth`=20, max 32768 | 78 | No |
| `callsview` | `callsview [%name] [maxdepth] ...` | Same data in a dedicated Call Stack window | 79 | **Yes** |
| `e` | `e [address_expression]` | Navigation/frame-selection command. `e` alone prints `file:func#line: address` (e.g. `test.c:PrintLine#28: 0x411c`); `e num_` selects call-stack frame number `num` (this is MULTI's "frame" command — no command literally named `frame` exists) | 147–148 | No |
| `l` (no arg) | `l [object [string]]` | **No parameter**: lists locals and parameters of the current function (the `this` pointer too, for a C++ method); function must be on the stack | 115 | No |
| `l @` | `l @ [string]` | Lists **addresses** of local variables instead of values | 115 | No |
| `l g` | `l g [string]` | Lists names/addresses of globals + in-scope file statics | 115–116 | No |
| `l S` | `l S [string]` | Lists static variables | 116 | No |
| `l func` | `l func` | Lists locals/params of a specific stack function `func` (not just the current one); `l @func` for addresses | 116 | No |
| `l r` / `l R` | `l r` / `l R` | Lists registers / register synonyms | 116 | No |
| `l t` | `l t` | Lists type definitions | 116 | No |
| `p`, `print` | `p [/format] expr` / `print [/format] expr` | Evaluates and prints an expression in the current source language, in a chosen display format | 119 | No |
| `eval` | `eval expr` | Like `print` but does not echo; use for expressions with I/O side effects (avoids a double memory read) | 113 | No |
| `examine` | `examine [/format] expr` / `examine address_expression` / `examine numberb` | Three forms: same as `print`, or navigate-then-print an address, or jump to a breakpoint's location | 114 | No |
| `mprintf` | `mprintf(format_string, ...)` | **`printf`-equivalent** — C `printf` syntax (all conversions except `%n`), prints to the command pane | 117 | No |
| `printline` | `printline [count [line]]` | Prints `count` source lines starting at `line` | 119 | No |
| `printwindow` | `printwindow [line [num]]` | Prints `num` lines centered on `line` | 120 | No |
| `localsview` | `localsview` | Opens a Data Explorer on current-function locals; documented as equivalent to `view $locals$` | 298 | **Yes** |
| `memview` | `memview [[@count] address_expression]` | Opens a Memory View window | 299 | **Yes** |
| `l b` | `l b` | Lists breakpoints — identical to `B` | 115 | No |
| `l P` | `l P` | Lists processes (see Item 4) | 116 | No |

**`GHS_Debugger` structured (non-text) reads**, from `script.pdf`'s Memory Access Functions
(page 236–238) and Symbol Functions (page 244–245): `ReadIndirectValue()`,
`ReadIntegerFromMemory()` (alias family includes `ReadInt`), `ReadStringFromMemory()` (alias
family includes `ReadStr`), `WriteIntegerToMemory()`, `WriteStringToMemory()`,
`GetSymbolAddress()`, `GetSymbolSize()`, `CheckSymbol()`. These are genuinely structured
(non-text) calls, consistent with the verified-facts row noting they cover part of the variable
surface — but none of them walk a struct, array, or pointer chain; they read a scalar/string at
a resolved address or symbol.

**`NOT DOCUMENTED - must be probed`**: `whatis`, `ptype`, `info locals`, `info args` — none of
these GDB-style commands exist anywhere in `debug_cmd.pdf` (confirmed absent by exhaustive
case-insensitive search across the whole manual, both `-layout` and raw extraction). There is
no dedicated `stack` or `frame` command either — `calls`/`e num_` are the closest equivalents.
Whether `print`/`l`/`view` can expand exactly one level of a struct without recursing into
children, and whether array printing can be bounded to a range (`arr[a..b]` style), is **not
stated** in the command reference text read; `view` (referenced by `localsview` but itself
GUI-oriented, page 301, not read in full) may carry this and should be checked by probe p04 at
the keyword `view` if the hardware probe needs it — recorded here as unresolved rather than
silently dropped.

---

## Step 2 / Item 4: Multicore / connection commands

| Command | Signature | Notes | Page | GUI-only? |
|---|---|---|---|---|
| `connect` | `connect connection_method_name` / `connect [setup=filename [setupargs=...]] [log[=filename]] [-temp] debug_server [dbserver_arguments]` / `connect -restart_runmode` / `connect log[=filename]\|nolog` / `connect` | Connects/reconnects to a target. `dbserver_arguments` (i.e. `850eserv2`'s own flags) are deferred to the per-processor-family "MULTI: Configuring Connections" book, **not present in the mined manuals** | 247–249 | No (bare `connect` opens the GUI Connection Chooser) |
| `disconnect` | `disconnect` | Closes the current target connection | 250 | No |
| `target`, `xmit` | `target [/NoRmtMsg] string` | Transmits a command string directly to the target debug server (i.e. raw `850eserv2` commands) with the current task/core context | 258 | No |
| `targetinput`, `xmitio` | `targetinput [input_string]` | Feeds target stdin | 259 | No |
| `prepare_target` | `prepare_target [-ask\|-flash\|-load...\|-verify=...] [-allcores\|-onecore] [-save\|-nosave]` | `-allcores`/`-onecore` are explicitly multicore-aware download/flash/verify scoping flags (`-allcores` noted deprecated in favor of an unspecified newer mechanism) | 253–255 | Partial (`-ask` opens a dialog) |
| `change_binding` | `change_binding bind\|unbind` | Associates/disassociates the selected executable with a connection | 247 | Partial |
| `connectionview` | `connectionview [connection_file]` | Opens the Connection Organizer window | 250 | **Yes** |
| `route` | `route destination_component command` | Routes one command to a specific named **component** (e.g. `debugger.pid.543`, or the unique suffix `pid.543`), overriding the current target-list selection | 201 | No |
| `components` | `components [component_name]` | Lists components (`component.number`) and their aliases; use to discover the names `route` accepts | 110 | No |
| `l P` | `l P` | Lists processes (MULTI's per-core/per-task slots) | 116 | No |
| `P` | `P [[pr=num] subcommands]` | With no args, lists all processes including slot numbers; with `pr=num`, targets process slot `num` | 33 | No |
| `addhook -core N` | `addhook [-order n] [-board\|-core number] -before/-after action {commands}` | Registers a setup-script hook scoped to one core (vs. `-board` for all cores) — used for e.g. per-core register init after reset | 237–241 | No |

**How core selection actually works (synthesized from `debug.pdf`, not a single command):**
MULTI does not expose a single documented "select core" command in the command reference.
Instead, each core of a multicore target appears as its own entry (a "process") in the
Debugger's target list (`debug.pdf` pages 20–22, 685–688), and commands act on whichever entry
is "current" — either the GUI selection, or explicitly overridden per-command via
`route destination_component command`, or via `P pr=num subcommands` for the native-debugging
subcommand set. The read-only system variable `$_PID` (page 335) returns the process ID of the
**selected core**, and conditional breakpoints can be scoped to one core with `_PID==N` (page
687) or via the `stopif` command. A `*.ghsmc` multicore configuration file, once associated
with a program, causes `change_binding`, `connect`, `prepare_target`, `k`, `quit`, `debug`,
`new`, and `detach` to apply to **every** executable/core listed in it at once (page 685).

**Multicore synchronous-debugging semantics** (`debug.pdf` pages 686–688, "Synchronous
Debugging on a Multi-Core Target"): when enabled, all cores run and halt together and software
breakpoints apply across all cores by default; a synchronous halt is triggered by a user halt,
a breakpoint, completion of a single instruction step, a hardware reset, or a hardware
exception. The core that caused the halt shows status **Stopped**; every other core shows
**Paused**. Non-synchronous (independent) halting/resuming of cores is explicitly called out as
liable to cause missed breakpoints — a strong signal for how `architecture.md` §6.1's
per-core `Resume`/`Halt` semantics should be validated against hardware.

**Python-level connection API** (`script.pdf`, Chapter 10 "Connection Functions", pages
213–221): `GHS_DebuggerApi.ConnectToRtserv()`, `ConnectToRtserv2()`, `ConnectToTarget()`,
`Disconnect()`, `IsConnected()`. `GHS_TargetIds` (page 257) is a class of ~500 numeric CPU
family/coprocessor-ID constants (e.g. `targetIds.XSCALE_IXP2350`), used with
`GetCpuFamily()`/`GetTargetCoProcessor()` — not a core-selection mechanism despite the name.

`NOT DOCUMENTED - must be probed`: the exact `850eserv2` `dbserver_arguments` grammar; whether
`GHS_Debugger.ConnectToTarget()`'s `dbserver` string argument (per the verified-facts table
signature `ConnectToTarget(dbserver, setupScript, setupScriptArgs, multiLog,
stickToTheDebugger, moreOpts, printOutput)`) accepts the same syntax as the `connect` command's
second form; how a Python probe selects which of several already-connected cores a given
`GHS_Debugger`/`RunCommands` call applies to (i.e., whether `route`/`P pr=`/target-list
selection is reachable from `GHS_Debugger` at all, or only from `RunCommands` text).

---

## Step 2 / Item 5: Monotonically increasing stop counter / stop sequence / run number

**`NOT DOCUMENTED - must be probed.`** An exhaustive case-insensitive search (both `-layout`
and raw-text extraction) of `script.pdf`, `debug_cmd.pdf`, and `debug.pdf` for `stop count`,
`stopcount`, `hit count`/`hitcount`, `run number`, `sequence number`, `generation`, `epoch`,
`stop sequence`, `stop id`/`stopid`, and `halt count` turned up nothing that is a
monotonically-increasing counter over stops in general. The only "count"-shaped things
documented are narrower and do not qualify:

- **Tracepoint hit count** (`tpreset`, pages 287–288) — counts hits of one specific tracepoint,
  reset independently; not global, not monotonic across stop *kinds*.
- **`_BREAK`** (page 333) — "the current breakpoint number"; identifies *which* breakpoint was
  last hit, not a running count of stops, and is meaningless for a `Halt`- or `Step`-induced
  stop.
- **`CONTINUECOUNT`** (page 330) — a *countdown* set by the user (`c @3`) to skip N-1
  breakpoints before actually stopping; not an observed/output value.
- **`_LAST_COMMAND_STATUS`** (page 334) — boolean, not a counter.

No system variable, `GHS_Debugger` method, or Debugger command in the three manuals reports a
value that increments on every stop regardless of stop kind. This must be established
empirically by probe p09 (Task 7) — most plausibly by re-reading `_BREAK`, `GetPc()`, or a
`GetCurPrInfo()`/`GetProcessAttribute()` field across consecutive stops of different kinds, per
the plan's Step 3.

---

## Step 2 / Item 6: `GetProcessAttribute` attribute index table and `GetCurPrInfo` options

**`NOT DOCUMENTED - must be probed.` This is a hard negative, searched aggressively:**

- Direct string search for `GetCurPrInfo`, `GetProcessAttribute`, `CurPrInfo`, `ProcessAttribute`,
  `attributeIdx` — **zero matches** in `script.pdf` (both `-layout` and raw, non-layout
  `pdftotext` extraction, to rule out a mid-identifier space artifact), **zero matches** in
  `debug_cmd.pdf`, **zero matches** in `debug.pdf`.
- Broader search for the standalone word `Attribute` across all three manuals turned up only
  unrelated hits: window/manipulation-function chapter titles, `cmdExecOutput`/`cmdExecObj`
  window-object attributes, breakpoint `rolling`-attribute prose, and OSA Explorer's per-object
  attribute display — none reference a `GHS_Debugger` process/attribute index.
- `script.pdf`'s own "GHS_Debugger Functions" section (page 225) lists **exactly two** members
  of class `GHS_Debugger` itself: `__init__()` and `RunCommands()`. Every other method the
  verified-facts table observed on the live object (`Resume`, `Halt`, `Step`, `Next`,
  `GetStatus`, `GetPc`, `WaitToStop`, `ConnectToTarget`, `DebugProgram`, `GetCurPrInfo`,
  `GetProcessAttribute`, etc.) is either inherited from `GHS_DebuggerApi` (documented, e.g.
  `GetStatus()` page 240, `WaitToStop()` page 244) or — for `GetCurPrInfo`/`GetProcessAttribute`
  specifically — **not documented under any class in the manual at all**, inherited or
  otherwise. These two methods exist on the live 7.1.6d object but ship with no written
  documentation in this manual set.

**Consequence for M0-9 (`architecture.md` §12):** the manuals supply no constraint on the valid
`options` domain for `GetCurPrInfo(options)` or the `attributeIdx` domain for
`GetProcessAttribute(attributeIdx, attributeString)`. Task 7's brute-force sweep (`opt` in
`0..15`, `attributeIdx` in `0..63`) is therefore the only source of truth and must run exactly
as specified — there is no manual cross-check available to narrow the range in advance or to
validate the sweep's results against a documented table.

**Adjacent-but-not-matching finding**: `P` (the multiprocess command, page 33) and `_PID`
(page 335, "process ID... of the selected core") both use "process" (`Pr`) to mean "one core's
debug context" in MULTI's model. `GetCurPrInfo` almost certainly expands to "Get Current
Process/Processor Info" under the same naming convention, which is circumstantial support for
guessing it returns per-core status/attribute data — but this is inference, not documentation,
and does not reduce the size of the sweep needed.

---

## Step 2 / Item 7: Window commands for hiding or minimizing the Debugger window

| Command | Signature | Notes | Page |
|---|---|---|---|
| `goaway` | `goaway` | **Hides** the Debugger and all debug-related windows (Data Explorer, Register View, Memory View, MULTI Editor windows opened from the Debugger). GUI only. Explicitly: "The `goaway` and `comeback` commands are only useful when MULTI is being externally controlled via a command script because there is no way to interactively issue `comeback` after `goaway` has hidden the Debugger." | 114 |
| `comeback` | `comeback` | Restores the windows `goaway` hid. GUI only | 109 |

This is a direct, documented answer to part of M0-3: MULTI ships a scripted hide/show pair
specifically designed for headless/externally-controlled operation, with an explicit warning
that it is one-way without a second control channel to issue `comeback` (relevant to
`architecture.md` §6.1's control-channel question — Task 6/M0-8). Two adjacent, related but
distinct commands:

- `debugpane [cmd|target|io|serial|python|traffic|next|prev]` (page 110/111) — switches which
  *pane* is visible inside the Debugger window; does not hide the window itself.
  Also `python`/`py` and `pywin` (pages 222–223) are themselves GUI-only, matching the
  verified-environment note that the `python` debugger command cannot be used headlessly —
  `RunCommands` from `GHS_Debugger` is the non-GUI path.
- `DebugProgram(fileName, newWin, block, printOutput, expandFileName)`'s `newWin` argument
  (`script.pdf`, GHS_DebuggerApi Run Control Attributes and Functions, page 239) controls
  whether a new Debugger window is created — this is a startup-time choice, not a
  hide/show-after-the-fact toggle, and is a candidate `p03_gui` variant per the plan.

`NOT DOCUMENTED - must be probed`: whether the process's *taskbar-level* window (as opposed to
MULTI's internal notion of "the Debugger window" that `goaway` hides) can be started minimized
or hidden from Windows entirely (e.g. `mpythonrun.exe`'s own console/window, or a
`DebugProgram(newWin=1)`-created window) — `goaway`/`comeback` operate on MULTI's internal
window model, not necessarily on OS-level window visibility; whether `goaway` has any effect
when no window was ever shown (`newWin=0` / non-GUI launch to begin with); whether issuing
`goaway`/`comeback` perturbs a running target the way Task 5/6 measure for polling and
breakpoint-clearing.

---

## Additional finding: documented vs. observed `RunCommands` signature

`script.pdf` (page 225) documents `GHS_Debugger.RunCommands()` as:

```
RunCommands(cmds, block=True, printOutput=True)
```

— three parameters, with aliases `RunCommand()`, `RunCmd()`, `RunCmds()`, `ExecuteCmd()`,
`ExecuteCmds()`, `ExecCmds()`, `ExecCmd()`. The verified-environment-facts table (from actual
introspection of the live 7.1.6d interpreter, `recon/out/probe01.json`) records a **fourth**
parameter, `keepRawOutput`, not present in this manual's signature. This is a second confirmed
documentation/reality gap in the same class as Item 6 (`GetCurPrInfo`/`GetProcessAttribute`):
the shipped `script.pdf` under-documents the live `GHS_Debugger` surface. Treat any signature
in this file as a **floor**, not a ceiling, on what the real object accepts — probes should
continue to introspect (`dir()`, docstrings if any) rather than trust the manual alone for
argument counts.

---

## Summary of `NOT DOCUMENTED - must be probed` items

1. Whether `H`'s halt-cause output is a fixed enumerated string set or free text; any
   structured (non-text) exposure of halt/stop reason from `GHS_Debugger`; whether the
   Stopped-vs-Paused per-core distinction (multicore sync halt) is queryable outside the GUI.
2. Whether breakpoint deletion (`d`, `D`, `hardbrk ... delete=`) requires a halted target.
3. Whether any command/method expands exactly one level of a struct/array without recursing,
   or bounds array printing to a range (`one_level_expansion_command` / `array_range_command`
   per the Task 8 probe contract).
4. The `850eserv2` `dbserver_arguments` grammar; whether `ConnectToTarget()`'s argument syntax
   matches the `connect` command's; the concrete mechanism (if any) by which a `GHS_Debugger`
   Python call — as opposed to a `route`/`P pr=`-prefixed `RunCommands` string — targets one
   specific core of an already-connected multicore session.
5. Any monotonically increasing stop counter/sequence/generation/run number — none found.
6. `GetCurPrInfo(options)`'s valid `options` domain and `GetProcessAttribute(attributeIdx, ...)`'s
   `attributeIdx` table — undocumented entirely; the Task 7 brute-force sweep is authoritative.
7. Whether `goaway`/`comeback` affect OS-level (as opposed to MULTI-internal) window
   visibility, whether `goaway` perturbs a running target, and whether `goaway` is meaningful
   when no window was ever created (non-GUI launch).
