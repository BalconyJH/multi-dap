---
name: start-ghs-debugger
description: Inspect and operate Green Hills MULTI through its TCP command socket while preserving target state by default. Use for multi.exe -socket, evaltosocket and routed commands, component inspection, connection diagnosis, or explicitly authorized image preparation and execution.
---

# Driving GHS MULTI from the command socket

Requires a local Green Hills MULTI installation, the target server for your
architecture, and a connected hardware debug probe. Examples use Windows PowerShell.

This is the direct command-socket workflow, separate from multi-dap's DAP daemon.
For TOML configuration, IDE attachment, or daemon diagnosis, use
[use-multi-dap](../use-multi-dap/SKILL.md). Do not run socket mutations while a
multi-dap actor owns the session: external reset/download invalidates its state.
If image preparation is authorized, complete it before DAP acquisition or first release the daemon
through its normal ownership-aware lifecycle. Never create a competing probe owner.

The socket is MULTI's command line over TCP. Its capabilities equal the command line's:
any command that opens a modal dialog or blocks the foreground is a deadlock on the socket.
Read the capability boundary before issuing anything.

## Capability boundary

Default to observation: preserve the current target state for attach, inspection, and
diagnosis. Require explicit user authorization for each download, erase, program, reset,
or run operation. Reset authorization does not authorize flash changes or execution.
Commands that can implicitly perform these operations require the same authorization;
in particular, `prepare_target -load` can download and start execution. Do not infer
permission from a connected probe or from this skill's examples. Halt, disconnect,
session replacement, and process termination also require an authorized task scope.
If the requested observation needs an unauthorized mutation, stop and report the limit.

Works over the socket:

- Enumerate and switch components, `disconnect` an individual target
- Read target memory and symbols (`print`, `print /x`)
- `reset` / `c` / `halt`, sample `$pc` and `_STATE`
- First-time bind and download for the session (`prepare_target -load -onecore -save`)
- `verify -all` to check download integrity

Not available, or actively breaks the session:

| Command | Consequence |
|---|---|
| `prepare_target` again on an already-bound application | May open the `Prepare Target` modal; the socket blocks forever and only killing the GUI recovers it |
| `load <elf>` inside a multicore application | MULTI rejects it explicitly |
| `debug <ghsmc>` | Creates a second set of unbound components while the old ones stay; state gets worse, not reloaded |
| `r` | Blocking foreground run; every later command loses its reply. Use `c` |
| Parallel socket clients | Replies interleave. One port must be driven serially |
| `h` | That is history, not halt. Stop with the full word `halt` |

Known gap: **there is no verified socket-only command to re-download an already-bound
application.** If a task demands fully headless reload, keep the session open and report the
gap. Do not dress up any command in the table above as a successful reload.

## Launching

Reuse an existing authorized session for observation. Launch or regenerate connection
files only when requested and after checking their startup actions against the user's
permissions. A download-mode configuration is not a read-only attach recipe.

```
multi.exe -socket <port> -connectfile <cfg>.con -connect=<title> <cfg>.ghsmc
```

Put `-socket <port>` ahead of the other arguments; port range `1..65535`. The `.con` file
describes the target server and its arguments, the `.ghsmc` file describes which core gets
which ELF. Formats are in [references/connection-files.md](references/connection-files.md) -
read it only when generating or editing connection files.

Before launching: regenerate the connection files with local absolute paths **every time**
and keep them out of version control, and put the target server's directory on `PATH` - MULTI
resolves it from `PATH`.

A returned PID does not mean the debugger started. All four must hold:

- [ ] MULTI is listening on the requested port
- [ ] the target server process is up and attached to the probe
- [ ] the socket answers `components`
- [ ] the component count matches the core count in `.ghsmc`

MULTI alive with neither a listening port nor a target server means it stopped before
connecting to the target. Check in order:

1. leftover MULTI service processes from a previous session (`svc_router`, `svc_statemgr`)
2. licensing - diagnose with `lic_find_licenses.exe` from the install directory
3. `GHS_MULTI_PATH` / `GHS_COMP_PATH` pointing at the current installation
4. whether `.con` / `.ghsmc` were generated for this launch

Never report "debugger connected" from this state.

## Exclusive probe ownership

One MULTI plus one target server may own a probe at a time. To switch sessions:

1. on the old session's socket, `evaltosocket { components }`
2. for each connected target, `route component.N evaltosocket { disconnect }`
3. confirm both `multi.exe` and the target server have exited
4. only then launch the new session

If `multi.exe` has exited but `svc_router.exe` / `svc_statemgr.exe` linger, confirm through
`ExecutablePath` and parent/child PIDs that they belong to the session and installation that
just ended, then kill them by exact PID. Never kill by process name alone.

`Specified emulator is not available` means the probe is taken, not that the firmware is
broken. Do not retry several target servers in parallel.

## Sending commands

Socket messages must end with a newline. Use the bundled client:

```powershell
pwsh -NoProfile -File <skill-directory>/scripts/invoke_multi_socket.ps1 `
  -Port 50001 -Command 'evaltosocket { components }'
```

Parameters: `-Port` (default 50000), `-Command` (required), `-TimeoutSeconds` (default 10,
range 1..120), `-QuietPeriodMilliseconds` (default 300), `-AllowEmptyResponse`.

Completion means EOF or a quiet period, not proof of command success. A deadline before
completion is an error even if some bytes arrived; do not use partial replies as evidence.
With `-AllowEmptyResponse`, an entirely silent quiet period also completes the response.
An empty reply is an error by default. Pass `-AllowEmptyResponse` only for the GUI-state
commands known to stay silent (`switch`, `c`). Never relax it for `components`, `print`, or
`verify` - an empty reply there means the command failed.

```text
evaltosocket { components }
evaltosocket { switch -component component.N }
route component.N evaltosocket { print /x _STATE }
route component.N evaltosocket { print /x $pc }
route component.N evaltosocket { print <symbol> }
route component.N evaltosocket { prepare_target -load -onecore -save }
route component.N evaltosocket { verify -all }
route component.N evaltosocket { reset }
route component.N evaltosocket { c }
route component.N evaltosocket { halt }
route component.N evaltosocket { disconnect }
```

`_STATE`: `1` unbound, `2` Stopped, `3` Running. On `3`, sample `$pc` twice - only a changing
value proves the hardware core is advancing.

## Download and run

This checklist is only for an explicitly authorized download-and-run task, never an
attach or diagnosis prerequisite. Confirm authorization for all operations first,
including any implicit execution during download. If a required step is not authorized,
do not start the sequence or substitute a destructive recovery action.

1. `components` - take this session's actual numbering.
2. On one executable component, `prepare_target -load -onecore -save`. It downloads every ELF
   and binds the `.ghsmc` executables to their cores. **Once per session.**
3. Re-run `components`, then `verify -all` on each; expect
   `verify: no incoherencies detected`.
4. **`reset`.** Reason in the first gotcha below.
5. `c` once. A multicore freeze group starts and stops together, so send `c` to a single
   executable component in the group.
6. When the run window is over, send the full `halt`.
7. `print <result symbol>` to read results.

## Gotchas

- **A download must be followed by `reset` before `c`.** `prepare_target -load` starts
  execution without resetting the chip: on-chip peripherals, shared inter-core memory, and
  handshake flags survive from the previous run while each core's C startup re-zeroes the
  software-side state. The two views disagree and the target misbehaves wholesale. The same
  applies to every re-download inside one session. A round that skipped `reset` is not valid
  on-target evidence.
- **Uniform failure means check `reset` first.** Every case returning the same error code, a
  failure count equal to the case count, an all-ones failure mask - confirm this round was
  reset before suspecting firmware. Real algorithm defects usually hit only a subset of cases.
  Check the recorded reset history, not by issuing an unauthorized reset or rerun.
  Without authorization for another round, report the evidence gap.
- Component numbering changes per session. Never reuse the previous `N`.
- If the socket reports that no program is selected, stop sending run commands. Bind or
  download only if explicitly authorized; otherwise report the missing binding.
- A halted target does not mean the test finished. Confirm the test entry returned before
  reading result symbols.

## Rebuilding without dropping the session

Only for an authorized halt/reload/reset/run task: keep the debugger open, `halt`, then
build. For observation-only tasks, leave the session and target unchanged. Reload on the existing
bound application only through a separately verified download workflow, then `reset`
before `c`. This skill has no verified socket-only reload command; when that is the
only available interface, report the limitation and keep the session intact. Do not
repeat `prepare_target` or relaunch automatically to pretend reload succeeded.

## Evidence standard

An on-target conclusion needs all three kept together: the raw socket replies, the timestamp
of the ELF that was downloaded, and build evidence that the test in question was actually
compiled into that image (build macros, link-product ownership). An all-zero initial result
struct does not prove a pass, and edited source with a stale link product is not evidence
either.

Keep raw replies, ELF paths, timestamps, symbol names/values, and build evidence in
ignored or private storage. Verify that generated connection files and captures are
ignored before writing them in the repository. Shared reports and commits must contain
sanitized summaries only, without target identifiers or private paths.
