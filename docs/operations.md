# Operations Guide

`multi-dap` is a local, single-operator adapter. It is not a remote debugging
service, a shared daemon, or a process supervisor for Green Hills MULTI.

## Deployment baseline

- Run the production daemon on a trusted Windows amd64 workstation.
- Keep the release bundle intact so `multi-dap.exe` and `bridge/bridge.py`
  remain adjacent. Source-tree runs must pass `--bridge-script` explicitly.
- Store the completed project TOML outside the repository with access limited
  to the operator.
- Bind every endpoint to a numeric loopback address. Prefer port `0` unless an
  authenticated integration requires a stable endpoint.
- Give every exact project/physical-probe binding a unique `probe.id`; use one
  daemon per identity and one active DAP frontend per daemon.

## Acquisition choice

Choose acquisition deliberately:

| Mode | Use when | Safety boundary |
|---|---|---|
| Cold | multi-dap should create and own a new target connection | May contend with an existing operator session or license; never selected as a warm fallback. |
| Warm | an operator-created MULTI session must be reused | Requires runtime router coordinates and an exact, unique, stable primary-ELF binding; never creates a cold connection. |

`ensure --acquisition cold` is suitable for an editor Before Launch task. It is
synchronous and idempotent with respect to an already authenticated daemon.
Do not run concurrent manual bootstrap scripts outside this control boundary.
The [CLion configuration guide](clion.md) defines the supported Before Launch
and native Cidr DAP Profile wiring.

## Routine commands

```powershell
multi-dap check --config C:\private\multi-dap-project.toml
multi-dap ensure --config C:\private\multi-dap-project.toml --acquisition cold
multi-dap status --config C:\private\multi-dap-project.toml
multi-dap diagnose --config C:\private\multi-dap-project.toml
multi-dap shutdown --config C:\private\multi-dap-project.toml
```

For a source build, add the explicit absolute `--bridge-script` argument to
`ensure` or `serve`. A release installation discovers its own bundled bridge.

`status` reports the sanitized session state, epochs, and breakpoint ownership
counts. `diagnose` reports the latest terminal phase, classification, and safe
operation category. Neither command exposes the control token or raw MULTI
diagnostics.

These commands and the supported editor proxy bind the owner-only record to an
opaque digest of the normalized validated TOML semantics. Shut down the daemon
before changing any runtime value. If a changed TOML produces
`control: daemon configuration does not match`, restore the previous semantic
configuration and shut the daemon down normally. Do not delete the record or
use legacy `proxy --probe-id` to bypass the mismatch.

## Failure handling

| Observation | Operator action |
|---|---|
| `check` rejects the TOML | Correct the named validation problem before starting any session. |
| `ensure` reports an existing authenticated daemon | Reuse it; do not start another process tree. |
| Warm discovery is ambiguous or empty | Restore the intended MULTI window/router state or correct the exact primary ELF. Do not switch to cold as an automatic recovery step. |
| Bridge RPC times out | Treat that bridge generation as poisoned. Inspect `diagnose`; do not reuse a late reply. |
| Actor reports reconciliation required | Preserve the target state and obtain operator review; do not issue speculative close or breakpoint cleanup. |
| Editor cannot attach | Confirm `status`, the same absolute `--config` and `--control-dir` values in `ensure` and `proxy`, and the single-frontend lease. Do not bypass the proxy with a raw dynamic DAP port. |
| Configuration binding mismatch | A same-ID daemon belongs to different validated semantics, or the TOML changed while running. Restore the prior configuration and use its normal shutdown path. |
| Daemon disappears | Run `diagnose`, preserve its classification, and decide whether the underlying operator-owned MULTI session is still trustworthy. |

The daemon closes only the bridge process and cold target connection that it
owns. It never kills unrelated MULTI, service-router, or target-server
processes by executable name.

## Evidence and retention

Routine operation should not retain raw debugger panes, target memory, or
connection data. Reconnaissance is a separate, single-owner workflow whose
sanitized output belongs only in ignored `recon/out/`. Before sharing a status
or failure record, review it against the sensitive-data list in the
[security model](security.md).

## Upgrade and rollback

Verify the outer release checksum and the archive's internal
`SHA256SUMS.txt` before use. Record `multi-dap version` alongside editor and
MULTI versions when collecting evidence.

Upgrades change only the local adapter bundle. They do not migrate a live
target session. Stop or detach the frontend, shut down the daemon under its
normal control path, replace the complete bundle, and start a new explicitly
chosen acquisition. Keep the previous verified archive for rollback; never mix
an executable from one release with a bridge from another.
