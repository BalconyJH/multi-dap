# M0 Reconnaissance Harness

This directory answers the nine M0 blockers of `docs/architecture.md` §12 with recorded,
reproducible evidence gathered against real MULTI hardware sessions.

## One hardware owner at a time

The debug probe and the target board are a single shared resource. No two probes, and no
probe and a MULTI GUI, may hold the debug probe simultaneously unless a probe is explicitly
testing coexistence (for example M0-1's "warm" run). Probe *authoring* is parallelizable;
probe *execution* is strictly serial. Run one probe, confirm its evidence file exists and its
`fatal` field is `null`, then confirm it left no *new* `mpythonrun.exe`, `850eserv2.exe`,
`svc_router.exe`, or `multi.exe` process behind. A warm probe must preserve the original session;
process identity is checked, not merely the presence of a process name.

## `config.local.json` is required and gitignored

Every probe and the host runner read board-specific settings from `recon/config.local.json`.
That file is gitignored because it contains real board paths, connection strings, and target
symbol names — none of which may appear in a committed file. Copy `recon/config.example.json`
to `recon/config.local.json` and fill in real values before running anything that touches
hardware. `reconlib.load_config()` returns the real values for in-memory probe use, but the
Recorder always reduces that protected value to a fixed redacted summary, including when it is
nested in a step result, note, or step metadata.

## Probes never print

Probe scripts run under `mpythonrun.exe`, MULTI's embedded Python 2.7 interpreter, and its
standard output is written with `WriteConsole`. Redirecting that handle to a pipe or a file
raises a modal Windows error dialog instead of failing cleanly. Probes therefore never write to
stdout: every result goes to a JSON evidence file via `reconlib.Recorder`, and the host runner
launches `mpythonrun.exe` with a brand new console window and never redirects its standard
handles.

## Running a probe

```
uv run --no-project --python 3.12 recon/run.py <probe_stem> [-- extra args]
```

For example:

```
uv run --no-project --python 3.12 recon/run.py p00_env
```

This launches `recon/probes/<probe_stem>.py` under `mpythonrun.exe`, waits for it to exit
(subject to `--timeout`, default 600s), and prints a step-by-step summary read back from
`recon/out/<probe_stem>.json`.

By default the runner joins `service_router_port` from the ignored local configuration. Joining a
router does **not** authorize reconnecting the target: warm probes use the window register to
find one already-bound program window whose `GetProgram()` exactly matches `primary_elf`. The
diagnostic also counts exact normalized matches to `multicore_project`, because cold startup uses
that `.ghsmc` file; it does not change the binding identity. They never fall back to the first
Debugger window, a basename match, `DebugProgram`, or `ConnectToTarget`. If the registered
windows are ambiguous, stale, or do not match, the probe fails closed without opening an emulator
connection dialog.

Run the read-only binding check before any warm probe:

```
uv run --no-project --python 3.12 recon/run.py p00e_warm_bind
```

Its first evidence step records only aggregate candidate counts (registered Debugger windows,
readable program identities, exact `primary_elf` matches, exact `multicore_project` matches,
stable-state matches, and process-info matches). It does not record program names, paths,
basenames, or extensions.

`p02_socket`, `p06_poll`, and `p07_bpclear` require that same verified warm binding. The runner
rejects `--cold` for every warm-only probe. Use `--cold` only with an explicit cold-capable probe
after confirming that no reusable router exists, since a cold path may create a new licensed
Debugger session. Options may appear before or after the probe stem, for example:

```
uv run --no-project --python 3.12 recon/run.py p00d_attach_steps --cold --timeout 45
```

For a router port discovered from the current process rather than the ignored configuration,
pass it explicitly without rewriting target-specific data:

```
uv run --no-project --python 3.12 recon/run.py p00_env --service-router-port 12345
```

`p08_routed_processes` is also warm-only and read-only. It verifies whether
`route debugger.pid.N P` resolves the route PID to a selected process row. Separately, it
matches each configured ELF with exactly one normalized `debugger.name.<full-path>` component
alias, then reads `P` and `H` through that opaque component. It records only counts, booleans,
and configured-core ordinals plus the `H` halt-cause category. It never sends write commands,
records program paths, component IDs, PIDs, slots, command text, or raw output, or changes the
selected GUI process. Exact-ELF window and process-path relations are evidence only; basename
and suffix counts are explicitly diagnostic-only and cannot establish a production mapping.
Its only strict mapping condition is a complete bijection from every direct-routed program
component's unique stopped `P` selection to a configured ELF through an exact normalized path.
Only after that condition holds does it issue the bounded read-only `calls`, `l`, `B`, and
`print $_STATE` checks; their output remains opaque and is recorded only as command and parser
shape.

`p10_per_core_warm_inventory` diagnoses a stale or `NO PROCESS` primary window without assuming
that it is the only usable command endpoint. It first inventories every configured core against
the complete registered-window set using exact normalized ELF identity. No Session exists and no
command is sent until all configured-core inventories are complete. For each unique verified
window it records the exact public `GetStatus`, `GetProgram`, `Halt`, `Resume`, `Next`, and `Step`
callability/signature surface without invoking any of those methods. For each unique verified
stopped window, it then requires the same strict configured-program topology before reading `B`
and `print $_STATE`; topology discovery and every routed read verify `P` and `H` first. Evidence
contains configured-core ordinals, counts, and booleans only. It never records paths, process IDs,
component IDs, or command output, and never starts, connects, resumes, halts, or mutates a target.

It is warm-only and requires an existing service-router port; invoke it with an explicit current
port when the local configuration does not retain one:

```
uv run --no-project --python 3.12 recon/run.py p10_per_core_warm_inventory --service-router-port <port>
```

When even warm inventory does not return, use `p15_warm_registry_health` once with a short host
timeout. It records only fixed phase names and aggregate counts around Window Register construction,
window enumeration, debugger-window construction, and the three warm-binding reads. It issues no
target-control or state-changing operation. The GHS `GetProgram`, `GetStatus`, and `GetCurPrInfo`
helpers do internally execute the same read-only debugger queries used by the production warm binder.
The host terminates only that exact `mpythonrun` process on timeout; do not repeat it after a phase
blocks until the operator has recovered the existing MULTI session.

```powershell
uv run --no-project --python 3.12 recon/run.py p15_warm_registry_health `
  --service-router-port <port> --timeout 5
```

`p13_execution_domain_syntax` is the next, still warm-only evidence step for a
future per-component execution domain. It does not require every configured
core to expose an exact MULTI-Python debugger window: it requires exactly one
verified existing window as a command endpoint, then establishes the strict
configured-program topology through routed `P` and `H` observations. For each
configured topology component it issues only `sc "route <component> <command>"`.
The documented `sc` command performs syntax checking rather than command
execution; the candidates are fixed to `halt`, `c` (continue), `sl n` (source
step into), and `nl n` (source step over). The probe checks verified stopped
state before and after every syntax check and records only ordinal, boolean,
and status evidence. It never invokes a MULTI-Python control method or routes
an execution command directly. A grammar success is evidence that the command
can be addressed by the component route, not authorization to perform it.

```
uv run --no-project --python 3.12 recon/run.py p13_execution_domain_syntax --service-router-port <port>
```

`p14_execution_domain_experiment` is intentionally disabled and is the first
real control experiment. Its local configuration requires an exact routed-halt
acknowledgement, exclusive stopped-target control acknowledgement, and the
selected configured-core ordinal. It first captures the full routed `P`, `H`,
and `print $_STATE` matrix; routes `halt` to the selected component while all
targets are already stopped; then captures the full matrix again. This phase
can prove that MULTI accepts the control command at the frozen component route
and leaves the observed topology stopped. It cannot prove independent physical
core control because the initial state is already stopped.

The separately disabled `single_step_enabled` phase needs a second exact
acknowledgement. It captures the same full matrix plus a structured
`calls nopar pos notypes` top-call observation for the selected component. That
observation is either a source identity or an explicitly unsourced address
identity, never a fabricated source frame. Before control it requires this
structured top-call form for every configured core. It routes one documented
`sl n`, then performs only a locally bounded number of routed `P` reads until
the selected route and primary window report stopped. It always routes `halt`
through the saved frozen selected component in `finally`, including poll
failure or timeout, before it observes topology again. The post matrix requires
an observable selected-core change and identical P/H/`$_STATE`/top-call
observations for every unselected core; any rejected command, unparseable
observation, failed recovery, topology drift, selected no-change, or
unselected drift is fatal. Its scrubbed evidence reports only booleans,
categories, and the completed poll-attempt count. The finite poll limits
completed iterations, but no verified interface gives an individual
`RunCommands` invocation a hard deadline; p14 therefore does not claim a hard
cancellation guarantee. It can demonstrate a routed command followed by a
stopped recovery and observable selected-process change in that one warm
session. It cannot
prove that only one physical core executed, that a source step has no firmware
side effect, or that the resulting route semantics apply to every future debug
session. Production multi-core run control remains fail-closed regardless of
the result.

```
uv run --no-project --python 3.12 recon/run.py p14_execution_domain_experiment --service-router-port <port>
```

`p11_m2_owned_breakpoint_recovery` is the recovery path for an abandoned M2 probe-owned
breakpoint. It is warm-only and disabled by default. When explicitly armed with its exact local
acknowledgement, exclusive stopped-target-control acknowledgement, and an explicit live router
port, it first consumes the local `p09` recovery journal. The journal is atomically written before
each M2 `b` command and contains the exact token and configured-core ordinal; it is never copied
to recorder evidence or console output and is removed only after the exact token is proven absent.
Without that journal, p11 rejects recovery unless the local configuration explicitly enables the
one-time legacy mode with a non-negative `expected_configured_core_ordinal` and
`expected_exact_token_count: 1`. That legacy mode deletes only an exact
`mprintf("HIT 0x????????\\n")` breakpoint command with eight uppercase hexadecimal digits on that
core, and performs zero `d` commands on any mismatch. It does not read or make claims about
unrelated cores' `B` output. Both modes freeze and revalidate the strict program topology and
stopped state before and after every routed read and deletion, then prove the selected core's token
is absent.

```
uv run --no-project --python 3.12 recon/run.py p11_m2_owned_breakpoint_recovery --service-router-port <port>
```

## M4/M5 capability probes

`p04_m4_execution` and `p05_m5_inspection` bind only through the same strict warm-session gate.
They begin with read-only MULTI-Python callable inventory and record signatures and result *shape*
without serializing target values, paths, addresses, or connection arguments. A missing step-out,
run-to, or disassembly callable is evidence that the corresponding bridge method remains
unavailable; neither probe substitutes a guessed debugger-command string.

Their mutating/read phases are disabled in the committed template. Enabling one requires both an
explicit local acknowledgement and a method name present in that probe's just-recorded inventory.
Execution calls require a stopped session, checkpoint state before and after every call, and always
issue a verified recovery halt. A halt restores execution state only; it cannot undo program-counter
movement or target-side effects, so run them only on an operator-approved disposable test state.
Memory/disassembly calls use the same stopped-state and recovery gates. The sole register command,
`l r`, is already documented and M0-4 verified; its raw text is not written to evidence.

Do not run either probe against the current cold `no_process` session. Until an existing program
window passes the warm-binding gate, both probes should be expected to fail closed before touching
the target.

## M2 per-core source-known evidence probe

`p03_m2_source_known` is the future hardware-evidence path for M2's per-core source-presence
gate. It is warm-only and binds every configured core to a distinct, exactly matching existing
ELF window; it never uses a primary-core fallback. It only inventories public callable names and
signatures matching `browse`, `source`, `file`, `line`, or `module`; inventory never invokes one
of those methods.

The local MULTI 7.1.6 scripting implementation establishes that the apparent symbol helpers are
not command-free source APIs: `CheckSymbol`, `GetSymbolAddress`, and `GetSymbolSize` issue
debugger expressions, while `PrintFile` issues `dbprint f`. `script.pdf` documents `PrintFile`
on page 254, but documents no public Source Files/file-list API. Consequently p03's documented,
read-only, command-free candidate allowlist is empty. Its evidence records the per-core inventory
and zero candidate count; no local configuration can arm a call through this probe.

`p12_m2_source_files` is the separate documented-command evidence path. `debug_cmd.pdf` p.115
defines `l f` as the all-source-file listing, and its optional string as a contains filter. The
probe first completes every exact warm-window inventory; then, only on a stopped window with a
strict `P`/`H` topology, routes `l f` through every configured program component. It keeps only a
bounded grammar signature, never the listed paths. It issues no filter variant: the manual defines
that variant as substring containment, which is incompatible with Presence. Hardware validation of
p12 established the exact grammar: the fixed heading `--------  File names  --------` followed by
contiguous drive-absolute lines. The production resolver therefore rereads each core's complete
list on every Resolve and performs canonical exact membership. This does not replace a CLion plus
hardware smoke test of real source-breakpoint Set/Clear/hit behavior, which remains pending
acceptance.

## `recon/out/` is not committed

`recon/out/` holds the JSON evidence every probe produces. It is deliberately gitignored: the
evidence contains target symbol names, memory addresses, and other consuming-project
identifiers that must never land in this repository. The scrubbed, committed record of what the
evidence showed is `docs/m0-findings.md`.
