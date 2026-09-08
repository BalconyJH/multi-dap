# Project Configuration Reference

`multi-dap` loads one strict TOML file that describes a project, its configured
cores, local protocol endpoints, and target lifecycle policy. The file is
validated before MULTI starts or a listener is created. Use the same file for
`ensure`, the supported CLion or VS Code proxy, and all config-based control
commands.

Start from `multi-dap.example.toml` at the repository root, or
`examples/multi-dap.example.toml` in a release package. Keep the populated copy
outside the repository because it can contain private paths, connection
arguments, probe identity, and firmware information.

## Validate before use

```powershell
multi-dap check --config "C:\private\multi-dap-project.toml"
```

`check` parses and validates the configuration without starting MULTI or
connecting a target. The loader accepts only a regular file of at most 1 MiB,
rejects unknown TOML fields, and reports all independent field-validation
problems that it can collect in one pass. A symbolic link is not accepted as
the configuration file.

## Path rules

The configuration path itself is resolved against the command's working
directory. Within the TOML, these required paths may be absolute or relative to
the directory containing the configuration file:

- `multi.installation`;
- `multi.executable`;
- `connection.project`;
- every `cores[].elf`.

They are cleaned, resolved to absolute paths, and checked on disk. Installation
must be a directory; the other values must be regular files.
`multi.executable` must be inside `multi.installation`.

Both sides of a `source_rewrites` rule must already be absolute. They are
cleaned but are not required to exist during configuration validation.
Windows-style identity checks are case-insensitive.

## `[multi]`

| Key | Required | Contract |
|---|---|---|
| `installation` | Yes | Existing MULTI installation directory. |
| `executable` | Yes | Existing `mpythonrun.exe`-compatible regular file inside `installation`. Runtime startup never searches `PATH`. |

## `[connection]`

| Key | Required | Contract |
|---|---|---|
| `connection.project` | Yes | Existing MULTI project file used by cold acquisition. |
| `connection.arguments` | Yes | Non-empty target-server argument string. It is trimmed but otherwise opaque to `multi-dap`. |
| `connection.preparation` | No | `"none"`, omitted, or `"already_present_no_verify"`. |

`"none"` and an omitted preparation field are inert. Use
`"already_present_no_verify"` only when the program described by the project is
already present on the cold-connected target. It requests MULTI's documented
no-verification preparation and does not download, flash, reset, or read
verification memory. Warm acquisition rejects this preparation mode.

## `[[cores]]`

At least one core is required. Array order is retained.

| Key | Required | Contract |
|---|---|---|
| `cores[].id` | Yes | Non-negative integer unique within the configuration. It becomes the stable DAP thread identity. |
| `cores[].elf` | Yes | Existing regular ELF file. Its cleaned, case-insensitive full-path identity must be unique across configured cores. |

The adapter does not infer cores from MULTI windows and does not accept a
basename-only ELF match.

## `[inspection]`

| Key | Required | Contract |
|---|---|---|
| `inspection.default_core` | No | ID of one configured core. It selects the address space for numeric-only Memory View and Disassembly requests. |

Omitted `default_core` is distinct from core `0`. A single-core configuration
can route an unqualified numeric address to its sole core. A multicore
configuration without `default_core` rejects that request as ambiguous. A
core-qualified opaque reference is not affected by this setting.

## `[[source_rewrites]]`

Source rewrites map an absolute debug-side prefix to an absolute client-side
prefix:

```toml
[[source_rewrites]]
from = "C:/build/source"
to = "C:/workspace/source"
```

Each `from` and `to` value is required and absolute. No two `from` prefixes may
contain or overlap one another, and the same rule applies independently to the
`to` prefixes. Comparisons follow Windows case-insensitive path identity. The
adapter rejects ambiguity instead of selecting the first rule.

## `[endpoints.*]`

Define all three endpoint tables:

| Table | Transport | Purpose |
|---|---|---|
| `[endpoints.mbp]` | TCP | Go-to-bridge MBP v1 control. |
| `[endpoints.dap]` | TCP | Local DAP listener. |
| `[endpoints.hint]` | Reserved UDP | Validated and configuration-bound for a future best-effort hint listener. The production runtime currently relies on polling and does not bind this endpoint. |

Every table has the same keys:

| Key | Required | Contract |
|---|---|---|
| `host` | Yes | Numeric IPv4 or IPv6 loopback literal, such as `127.0.0.1` or `::1`. Hostnames and non-loopback addresses are rejected. |
| `port` | No | Integer from `0` through `65535`; omission decodes as `0`. Use `0` for operating-system allocation. |

MBP and DAP may not use the same non-zero TCP host and port. Dynamic port `0`
is recommended; CLion obtains the selected endpoint through the authenticated
proxy and does not need a fixed DAP port.

## `[timing]`

All timing fields are required, positive Go duration strings:

| Key | Purpose | Example |
|---|---|---|
| `timing.poll_cadence` | Target-state reconciliation cadence. | `"250ms"` |
| `timing.rpc_deadline` | Go-owned deadline for one bridge operation. | `"3s"` |
| `timing.startup_deadline` | Bounded daemon and `ensure` readiness window. | `"30s"` |

Zero, negative, empty, or unparsable durations are rejected. A bridge call that
outlives its deadline is not accepted later as a current-generation result.

## `[lifecycle]`

| Key | Required | Default | Contract |
|---|---|---|---|
| `lifecycle.require_reset_after_download` | No | `true` | When enabled, Debugger Core refuses resume after a confirmed download until a confirmed reset. |

Set this to `false` only for a target whose lifecycle has been reviewed. The
default is conservative because an omitted safety policy must not silently
enable post-download execution.

## `[probe]`

| Key | Required | Contract |
|---|---|---|
| `id` | Yes | Non-empty stable identity containing only letters, digits, `.`, `_`, or `-`. Give every exact project and physical-probe binding a globally unique local value. |

`probe.id` is not a credential. It keys the owner-only daemon record and the
physical-probe lock, so copying it between projects creates a namespace
collision even though config-bound commands reject different semantics.

## Semantic configuration binding

`serve`, `ensure`, `status`, `shutdown`, `diagnose`, and `proxy --config`
derive the same opaque SHA-256 binding from the normalized validated fields on
this page. A control record with the same `probe.id` but another binding is
rejected before authentication, DAP forwarding, shutdown, recovery, or MULTI
startup. The `ensure` parent passes its expected binding to the child, which
checks it before reserving control state or starting the runtime.

The binding is stored only in owner-private runtime metadata. It is absent from
DAP, sanitized status output, ordinary logs, and terminal diagnostics. It is
an integrity selector, not a secret or proof of a hardware serial number.

The binding includes ordered core and source-rewrite entries and every
normalized field described above. It intentionally excludes:

- the TOML file path, comments, formatting, and original bytes;
- the contents of referenced project, ELF, and source files;
- `--bridge-script` and `--control-dir` deployment arguments;
- cold/warm acquisition selection and warm runtime router coordinates.

Two TOML files that normalize to exactly the same validated values therefore
share a binding. Moving a configuration without changing its resolved values
does not create a new identity.

## Change a running configuration safely

1. Detach the IDE frontend.
2. Run `multi-dap shutdown --config "<current-project.toml>"` with the current
   values and wait for the daemon to exit.
3. Edit the TOML, run `check`, and keep `[probe].id` unique.
4. If the path changed, update both CLion's Before Launch `ensure` action and
   its DAP Profile `proxy --config` argument.
5. Start a newly selected cold or warm acquisition.

If an edit was saved first and a command reports
`control: daemon configuration does not match`, restore the previous semantic
values and shut down that daemon through the normal config-bound command. Do
not delete its record, terminate unrelated MULTI processes, or use the legacy identity-only `proxy --probe-id` mode as a bypass.

## Runtime inputs that do not belong in TOML

| Input | CLI location | Reason |
|---|---|---|
| Bridge implementation | `--bridge-script` | Deployment component, not target identity. Packaged binaries discover their adjacent bridge; source runs should pass it explicitly. |
| Control record directory | `--control-dir` | Owner-private local runtime storage. Every cooperating command must use the same value. |
| Acquisition policy | `ensure --acquisition cold|warm` or `serve --session-mode cold|warm` | Operator decision for this start attempt. No implicit fallback is allowed. |
| Warm primary ELF | `ensure --primary-elf` or `serve --primary-elf` | Exact runtime window-binding input. |
| Warm router address | Direct `serve --service-router-host` and `--service-router-port` | Discovered local session state; `ensure` performs bounded discovery instead. |

For the primary IDE workflow, continue with the
[CLion configuration guide](clion.md). For lifecycle and recovery commands, see
the [operations guide](operations.md).
