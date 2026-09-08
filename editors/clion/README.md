# multi-dap CLion integration

CLion debugs targets through the native **Cidr DAP CMake Debug** path: select each applicable
CMake Application configuration (for example, `<your-cmake-application>`) and assign it the `multi-dap` DAP Debug Profile. Cidr starts
`multi-dap.exe proxy --config "<absolute-project.toml>"` through `stdin/stdout`; its Launch tab JSON must be
`{"request":"attach"}`. This makes the proxy authenticate and reuse a ready local daemon; it does not start a
second MULTI instance, service router, or target connection.

The validated baseline is CLion 2026.2.1 (build 262.9437.136). A later limited
hardware observation used local CLion 2026.3; this is not blanket qualification
of every later IDE version. This integration is not Python's **Attach to DAP**
configuration: do not use the Profile Attach tab or run the target ELF as a native Windows program.

[`profile-contract.json`](profile-contract.json) is an abstract field contract for validation and documentation
synchronization, not a CLion-importable file. The storage formats for Run/Debug Configurations, CMake Profiles,
and Debug Profiles vary by IDE version and user settings, so do not hand-author or version-control `.idea` files.

## Prerequisites

1. `multi-dap.exe`, the project TOML, and the absolute path of the configured primary ELF must be accessible on
   the Windows host running CLion. The primary ELF must exactly match the full path of a core in the TOML.
2. The recommended Before Launch tool uses explicit cold acquisition: when no authenticated daemon exists, it
   starts a new MULTI target connection from the TOML. To reuse an operator-established session, use explicit
   warm acquisition instead; failed warm discovery exits clearly and never falls back to cold.
3. Before debugging, the daemon must be prepared by the Before Launch External Tool below or by manually running
   `ensure` in a terminal. Both satisfy the same prerequisite; do not depend on a hidden resident adapter as well.
4. In **Run | View Breakpoints** (`Ctrl+Shift+F8`), clear every Exception Breakpoint checkbox. Entries may remain,
   but they must not be enabled. multi-dap does not currently implement MULTI exception-stop semantics, so it does
   not pretend that a non-empty exception-breakpoint configuration succeeded.

## CLion configuration

### 1. Prepare the daemon

Under **Settings | Tools | External Tools**, create a local tool such as `multi-dap ensure`:

| Field | Value |
|---|---|
| Program | `<absolute-path-to-multi-dap.exe>` |
| Arguments | `ensure --config "<absolute-project.toml>" --bridge-script "<absolute-path-to-bridge.py>" --acquisition cold` |
| Working directory | `$PROJECT_DIR$` or the absolute project root. |

`--bridge-script` must be the absolute path to `bridge/bridge.py` in the unpacked archive. The External Tool's
working directory is the target project root, so it cannot rely on a default relative path to find the packaged bridge.

When the daemon is authenticated and ready, `ensure` returns `already_ready`; otherwise it starts one cold daemon
that is governed by its own lifecycle and returns after the control record is authenticated and ready.
`--acquisition cold` is an explicit choice: it does not try warm first or implicitly fall back from warm. It is a
synchronous, short-lived, idempotent Before Launch action, not a debug adapter that must remain resident in a terminal.
When the TOML uses `connection.preparation = "already_present_no_verify"`, the cold connection performs that documented
preparation action; startup fails closed if additional GUI input is required.

Do not add a service-router address, target connection arguments, or a control token to this External Tool. Cold
acquisition gets the connection contract from the TOML, and the control token remains private runtime information.

To reuse an existing MULTI session, change the External Tool Arguments to:

```text
ensure --config "<absolute-project.toml>" --bridge-script "<absolute-path-to-bridge.py>" --acquisition warm --primary-elf "<configured-exact-primary-elf>"
```

Warm acquisition only discovers and strictly validates an existing service router; it never creates a cold connection.
Cold mode forbids `--primary-elf`, while warm mode requires it, so configuration errors fail before any child starts.
Explicit cold reuses only an authenticated multi-dap daemon and never scans for or takes over arbitrary operator-owned
MULTI sessions. If a session was established manually, use warm; otherwise the new cold session can contend for licenses
or probes.

If you do not use Before Launch, you can run the same `ensure` command manually in a terminal before debugging. Confirm
that it completes successfully before starting CLion Debug.

### 2. Create the Cidr DAP Debug Profile

Under **Settings | Build, Execution, Deployment | Debugger | Debug Profiles**, create a DAP profile with:

| Field | Value |
|---|---|
| Name | `multi-dap` |
| Executable | `<absolute-path-to-multi-dap.exe>` |
| Arguments | `proxy --config "<absolute-project.toml>"` |
| Communicate with the debugger via | `stdin/stdout` |

Use the same absolute private TOML path as the Before Launch `ensure` action.
The proxy validates that file and compares an opaque digest of its normalized
runtime semantics with the owner-only daemon record before forwarding DAP. A
same-ID record created for different target semantics is rejected rather than
reused. The digest, raw paths, and connection arguments are not exposed through
DAP, `status`, logs, or terminal diagnostics.

`[probe].id` still must be globally unique for the exact project and
physical-probe binding because it keys the daemon record and physical-probe
lock. Shut down the daemon with the current TOML before editing runtime values.
If an edit causes `control: daemon configuration does not match`, restore the
previous semantic configuration and shut that daemon down normally. The CLI's
`--probe-id` proxy mode is legacy and identity-only; it is not the supported
CLion configuration and must not be used to bypass a mismatch.

The default control directory is under `%APPDATA%\multi-dap\control`. If a
custom private directory is required, append the same
`--control-dir "<absolute-private-directory>"` to both the Before Launch
`ensure` arguments and these proxy arguments. The profile contract otherwise
assumes the default; never copy the control record or token into the project.

Enter the following in that profile's **Launch** tab:

```json
{"request":"attach"}
```

Do not use the **Attach** tab. `proxy` authenticates the existing daemon and
binds it to the validated project configuration; do not configure a TCP Remote
address and do not require a fixed DAP port.

### 3. Connect CMake configurations to the Profile

Under **Run | Edit Configurations**, select each applicable CMake Application configuration and choose the
`multi-dap` Debug Profile. These CMake configurations provide target/build identity; the Cidr profile
provides DAP adapter startup and the attach request.

When using the External Tool, click `+ | Run External Tool` under **Before launch** in each CMake configuration and
select `multi-dap ensure`. The External Tool and CMake configuration are separate IDE entities. After saving, reopen
each configured CMake Application and confirm that the Before launch list still contains the tool. Do not hand-author or modify
`.idea` files to achieve this state.

The old `multi-dap attach` is a Python `Attach to DAP` configuration and must be disabled or removed. It is neither a
substitute for this integration nor valid user guidance.

### 4. Use it

Select `<your-cmake-application>` from the toolbar and click its **Debug (bug icon)**. Never click the green **Run**:
Windows `CreateProcess` would attempt to execute the target-architecture ELF directly, which fails and never enters DAP.

On the first launch, first confirm that Before Launch `ensure` completed without errors, or that manual `ensure` succeeded.
CLion then starts the proxy over stdio and sends DAP `initialize` and `attach` from the Launch tab. It does not send an
adapter `launch` request.

## Locked client contract

- CLion's `initialize` negotiates variable types and paging; multi-dap advertises only adapter capabilities that are
  actually wired.
- When CLion synchronizes an empty exception-breakpoint list, multi-dap returns a successful response with no side
  effects. Any non-empty configuration fails closed before entering the backend or bridge. Disable all Exception
  Breakpoints in CLion before the first Debug session.
- The order is stdio proxy startup → initialize response → attach → initialized event → breakpoint configuration →
  configurationDone response → attach response; target churn during the configuration window does not leak to the IDE.
- CLion detach sends `disconnect {"terminateDebuggee": false}`. This message releases only DAP ownership; it does not
  reset, download, resume, halt, or close the MULTI session. `true` continues to fail closed.
- The daemon permits only one active DAP frontend. A second CLion/VS Code client receives an explicit rejection.
- Current capability boundaries remain those in the main repository documentation: M4 supports only `next`/`stepIn`;
  M5 supplies stopped-state, read-only Memory View and RH850 Disassembly. Writing memory, registers, non-zero instruction
  offsets, and instruction-level stepping remain fail-closed; CLion UI buttons do not expand these boundaries.

### Parallel Stacks, Console, and Memory View

CLion **Parallel Stacks View** uses standard DAP `threads` and paged `stackTrace` requests for each thread, not a private
protocol ([JetBrains documentation](https://www.jetbrains.com/help/clion/parallel-stacks-view.html)). multi-dap preserves
stable thread identities for configured core0/core4 and advertises delayed stack loading. The target must be stopped to
read each core's stack; requests while running remain fail-closed. The current MULTI path still takes a complete stack
snapshot, then pages it locally by `startFrame`/`levels` while returning the complete `totalFrames`.

The **Console** page in CLion Cidr DAP is not a DAP expression evaluator. That backend registers no console language,
does not enter prompt mode, and explicitly does not support the interpreter-command path; green text in the page is only
input echo. multi-dap's standard DAP `evaluate` is available: submit expressions through **Evaluate Expression** (`Alt+F8`),
Variables, or Live Watches. The adapter cannot turn this Console page into an interactive evaluator by changing
`EvaluateResponse`.

Console still displays debugging output: multi-dap polls the GHS Debugger Target and I/O panes with bounded polling and
sends both as DAP `stdout` events (adapter warnings use `stderr`). CLion Cidr does not display DAP `category=console` on
this page, so that category is not used. The Target pane contains target-server diagnostics such as `850eserv2`; the I/O
pane contains target-program host I/O/`printf` output. Neither is mixed with raw adapter stdio bytes. The implementation
uses MULTI `savedebugpane` GUI-pane snapshots, so forwarding is bounded and near-real-time rather than unbounded log
storage. Before every new DAP frontend completes configuration, an actor serially resets the incremental cursors; the next
poll replays bounded current content from both panes. This prevents Console from becoming empty after detach/reattach within
one daemon merely because a previous generation consumed the history.

Manual-address requests in CLion Cidr **Memory View** do not include a thread, core, or stop epoch. A single-core
configuration routes to its only core; a multi-core configuration must explicitly set `inspection.default_core`, otherwise
the request fails as ambiguous before bridge I/O. A frame PC uses an opaque reference containing core and stop epoch and
can directly enter Disassembly. Both read types require a stopped target, with a 64 KiB memory limit per request; do not
read MMIO that may have side effects. Variables do not yet produce `memoryReference`, so the accepted path is Memory View
**Go To Address**, not the Ctrl+Enter shortcut for every pointer variable.

## Hardware validation checklist

Real GUI validation must use a connected, recoverable target and record the CLion version/build, `ensure` return event,
daemon `status`, and the following results. Do not retain target connection arguments, control tokens, or unsanitized
MULTI output:

1. With the target stopped, start with the Debug (bug icon) for `<your-cmake-application>`. Confirm that cold `ensure`, when
   no daemon exists, starts only one daemon/MULTI/target connection; concurrent or repeated Debug returns `already_ready`
   and does not start a second set. When using warm instead, confirm that it reuses only the existing session. If Before
   Launch is used, reopen each configured CMake Application and confirm its task list still contains `multi-dap ensure`.
2. Confirm that Threads shows the configured cores. Validate a line breakpoint only when definitive source mapping exists;
   otherwise expect a clear unverified diagnostic rather than guessed mapping.
3. A single-configured-core setup can validate continue, pause, next, and stepIn. Current multi-core execution control must
   reject before bridge I/O until ExecutionDomain can provide complete before/after observation. stepOut and registers remain
   explicitly unsupported. On a stopped target, validate known Flash/RAM addresses in Memory View and frame/explicit-address
   Disassembly; `writeMemory`, non-zero `instructionOffset`, and instruction stepping must continue to fail closed.
4. At the top-level frame, validate stack, scopes, locals/evaluate; when evaluate omits frameId, validate the deterministic
   first-core top-level frame. Non-top-level frames, hover, expressions with uncertain side effects, and unsupported formatting
   must fail closed.
5. Detach from CLion. Confirm that the daemon still responds to `status`, disconnect does not alter target execution state,
   the existing MULTI session remains, and the same CMake Debug configuration can attach again.
6. For a negative test, temporarily remove `multi-dap ensure` from Before Launch: without a ready daemon, Debug must fail
   explicitly; `proxy` itself must not trigger cold open, reset, download, or target control.

Separate from the versioned CLion 2026.2.1 transport fixture and profile
contract, a later limited hardware run used local CLion 2026.3. That run
completed items 1, 2, and 4 plus the M5 read-only path: starting
from no related processes, Debug for a validated CMake application triggered cold ensure and reached paused within 14 seconds; core0/core4 each had
the correct source stack; empty locals encoded as an array; a 16-byte HSM Flash Memory View read succeeded; Host/HSM
Disassembly passed 2/4-byte widths, opcodes, and 8/32/128/256-line windows, while the target remained stopped before and
after every operation. The current result-line source breakpoint is still reported by CLion as having no executable code;
execution control and actual breakpoint hits are outside this M5 validation conclusion.
