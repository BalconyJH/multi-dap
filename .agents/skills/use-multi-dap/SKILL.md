---
name: use-multi-dap
description: Configure, start, attach to, and diagnose Green Hills MULTI through the multi-dap adapter, including cold or warm acquisition and CLion or VS Code DAP integration. Use for multi-dap TOML, daemon lifecycle, proxy attachment, and supported debug operations; direct MULTI command-socket download and reset use start-ghs-debugger instead.
---

# Use multi-dap

multi-dap exposes MULTI through a Go DAP daemon and a bridge running in MULTI's
embedded Python. The native MULTI command socket, service router, MBP transport,
and DAP endpoint are distinct interfaces; do not exchange their ports or clients.

This skill is maintained in the multi-dap repository. Relative links below resolve
from this directory to the repository root. If installed elsewhere, locate the
user's multi-dap checkout and read the corresponding files there.

## Establish the supported path

Read [capabilities](../../../docs/capabilities.md) before selecting operations.
Use [getting started](../../../docs/getting-started.md) for installation/build
details and [configuration](../../../docs/configuration.md) when editing TOML.
Treat implemented, software-tested, hardware-observed, and unverified as different
claims; host tests or an open IDE window do not prove hardware execution.

- Production runs on Windows amd64. `multi.installation` is the MULTI IDE directory,
  not the separately installed compiler or probe directory.
- Use MULTI's `mpythonrun.exe`, with embedded Python 3 by default. Select Python 2
  only for an explicitly required legacy installation.
- Copy [the example TOML](../../../multi-dap.example.toml) to a private local path;
  replace placeholders with validated absolute paths and target-specific settings.
  Keep completed configuration and generated connection files out of version control.
- Choose a unique `probe.id` for the exact project and physical probe. Use numeric
  loopback endpoints and one daemon/one active DAP frontend for that identity.
- Multicore numeric-address inspection requires `inspection.default_core`.

## Acquire and attach

Reuse an existing authenticated daemon if its configuration matches. Choose warm
acquisition when the task requires preserving an operator-created MULTI session;
choose cold only when creating a new target connection is intended. Warm failure
must not silently fall back to cold or a different program/window.

Example commands below run from the repository root; replace private paths with
the actual values. Build with `go build -mod=readonly -o .\multi-dap.exe
.\cmd\multi-dap` when a source build is needed. An existing executable may predate
the checked-out source; do not assume newly documented flags exist in a stale binary.

```powershell
.\multi-dap.exe check --config C:\private\project.toml
.\multi-dap.exe status --config C:\private\project.toml
```

For a new cold daemon used by an editor:

```powershell
.\multi-dap.exe ensure --config C:\private\project.toml --bridge-script C:\src\multi-dap\bridge\bridge.py --acquisition cold
```

For an explicitly selected existing MULTI session, the foreground warm path is:

```powershell
.\multi-dap.exe serve --config C:\private\project.toml --bridge-script C:\src\multi-dap\bridge\bridge.py --session-mode warm --service-router-host 127.0.0.1 --service-router-port <runtime-port> --primary-elf C:\absolute\configured-core.elf
```

Obtain router coordinates from the actual session; do not guess them or put them
in TOML. The primary ELF must match a unique, stable full-path binding. Source
runs require the explicit bridge path; intact release bundles discover it.
Use the documented `ensure --acquisition warm` discovery path for editor bootstrap
when appropriate, following [CLion setup](../../../docs/clion.md).

Configure the editor's adapter transport as:

```powershell
multi-dap.exe proxy --config C:\private\project.toml
```

Use the same configuration and, if customized, the same `--control-dir` for
bootstrap, proxy, and control commands. Do not substitute a raw dynamic DAP port
or the unbound legacy `proxy --probe-id` path. CLion uses its native Cidr DAP
profile and synchronous Before Launch `ensure`; read the CLion guide for exact
fields. For VS Code, read [editor integration](../../../docs/m6-packaging-and-editors.md)
and preserve its software-only validation caveat. Zed is currently unsupported.

## Debug within the current contract

- Continue and pause act on the freeze group, not an independently running core.
  Multicore stepping is unsupported; single-core statement stepping is bounded.
- Inspect variables, memory, and disassembly only on a stopped target within the
  capability matrix. Avoid side-effecting MMIO reads.
- Source breakpoints require definitive source presence on every configured core.
  Do not bypass unresolved source mapping with speculative commands.
- Download, reset, step-out, run-to, registers, and memory writes are not currently
  exposed as supported operations. The `require_reset_after_download` policy does
  not implement a download/reset mechanism.
- `connection.preparation = "already_present_no_verify"` only binds an image
  already on the target without downloading, flashing, resetting, or verifying it.
  Use it only when the target really contains the intended program.
- Do not send direct socket or GUI mutations around the actor. If initial image
  preparation is needed, use [start-ghs-debugger](../start-ghs-debugger/SKILL.md)
  as a separate ownership phase before DAP acquisition. It does not solve verified
  socket-only reload of an already-bound application.

## Diagnose and finish

Read [operations](../../../docs/operations.md) for recovery and ownership rules.

```powershell
.\multi-dap.exe diagnose --config C:\private\project.toml
.\multi-dap.exe shutdown --config C:\private\project.toml
```

Run shutdown when the task calls for releasing the daemon, not merely because
inspection is finished. DAP disconnect is not target reset or daemon shutdown.

- On configuration mismatch, restore the previous semantic configuration and shut
  down normally before editing runtime values. Do not delete control records to
  evade binding checks.
- On bridge timeout, treat that generation as poisoned; do not reuse late replies.
- On reconciliation-required state, preserve target state and report the blocked
  operation rather than guessing cleanup or continuing execution.
- Probe contention requires identifying the owning session. Never kill MULTI,
  routers, or target servers by executable name or start competing cold sessions.

Report the acquisition mode, sanitized status/diagnosis, requested operation and
observed result, and remaining validation limits. Keep private connection data,
tokens, target memory, and raw debugger output out of shared artifacts; consult
[security](../../../docs/security.md) before retaining diagnostic evidence.
