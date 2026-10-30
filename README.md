# multi-dap

`multi-dap` exposes Green Hills MULTI debugging sessions as a local Debug Adapter Protocol (DAP) service. The debugger core and DAP frontend are implemented in Go; `bridge.py` handles only the MULTI API integration and runs under embedded Python 3 by default (with an explicit Python 2.7 fallback).

> **Pre-release:** this public repository is licensed under MIT, and the host
> software and deterministic Windows package are extensively tested. No public
> release has been issued yet, and several hardware behaviors remain explicitly
> unverified. The first supported distribution target is Windows amd64.

## IDE support

**CLion is the primary supported IDE integration.** `multi-dap` uses CLion's
native Cidr DAP CMake Debug Profile, prepares the daemon through an explicit
Before Launch `ensure` action, and connects through an authenticated local
stdio proxy. Attach, configured-core threads and stacks, top-level inspection,
read-only Memory View and Disassembly, and source-breakpoint synchronization
have hardware-observed coverage within the documented limits. Actual
breakpoint hits, full execution-control acceptance, and stopped-target
disconnect-safety qualification remain open.

[Configure CLion step by step](docs/clion.md).

| Editor | Integration | Verified boundary |
|---|---|---|
| CLion | Native Cidr DAP CMake Debug Profile and explicit Before Launch `ensure`. | Hardware-observed attach, inspection, and source-breakpoint synchronization within the limits above. |
| [VS Code](editors/vscode/README.md) | Local source extension with optional cold/warm acquisition, host capture, and same-session native interval capture. | Five actual Extension Host scenarios passed with a synthetic backend. Visual UI, actual MULTI acquisition, and real-target acceptance remain open. |
| [Zed](docs/zed.md) | Local Windows source extension with authenticated stdio proxy and optional host capture. | Nine host tests and a pinned-toolchain Wasm build passed. Actual extension installation and GUI/target acceptance remain open. |

## Documentation

- [Getting started](docs/getting-started.md)
- [CLion configuration](docs/clion.md)
- [Zed source-extension configuration](docs/zed.md)
- [Project configuration reference](docs/configuration.md)
- [Architecture and design](docs/architecture.md)
- [Capabilities and development status](docs/capabilities.md)
- [Performance timeline](docs/performance.md)
- [Native performance analysis](docs/native-analysis.md)
- [MULTI 8 adaptation study](docs/ghs-8-adaptation.md)
- [Operations guide](docs/operations.md)
- [Developer guide](docs/development.md)
- [Windows distribution guide](docs/distribution.md)
- [Release and WinGet guide](docs/releasing.md)
- [Security model](docs/security.md)
- [Verification evidence](docs/verification-evidence.md)
- [GitHub release configuration](docs/github-configuration.md)
- [Hardware findings](docs/m0-findings.md)

The same material builds as a strict Zensical site from
[`zensical.toml`](zensical.toml).

## Distribution

No public package has been published yet. The release pipeline produces a
Windows amd64 ZIP, detached archive checksum, provenance attestation, and a
validated WinGet manifest set. After an approved release is accepted into the
WinGet community repository, the intended command is:

```powershell
winget install --exact --id Tacrolimus.multi-dap
```

Until then, use a locally built or reviewed release archive and verify both its
outer `.sha256` file and internal `SHA256SUMS.txt`. The [Windows distribution
guide](docs/distribution.md) covers source builds, ZIP verification, manual
lifecycle, and the WinGet activation boundary; see the
[release guide](docs/releasing.md) for the licensing and publication gates.

Publication also requires a reviewed repository/package identity decision,
successful hosted CI, and verified release protections. Local package and test
results do not close those gates; see the
[GitHub configuration checklist](docs/github-configuration.md).

## Development status

**Status as of 2026-10-30:** the implemented host paths have passed local
acceptance, but the project is not fully hardware-qualified or publicly
released. Remaining work depends on attributable target sessions and
operation-specific authorization, vendor-produced fixtures, actual editor GUI
acceptance, and reviewed hosted publication. Synthetic tests and historical
hardware observations are not substitutes for those gates.

### Implemented scope

| Milestone | Current implementation | Remaining boundary |
|---|---|---|
| M1 runtime | Strict TOML, MBP v1, bridge generation isolation, Session Actor, DAP transport, and the attach/configuration/thread/disconnect lifecycle. | Base runtime is implemented; resident-loop and differential polling measurements still need the intended target. |
| M2 breakpoints | Source breakpoint transactions, ownership tracking, stop arbitration, epochs, and bounded pending cleanup. | Mutations require a stopped target, the same control lease/stop epoch, and definitive source presence on every configured core. Real breakpoint hits and running-clear behavior remain unqualified. |
| M3 inspection | Complete bounded stack snapshots, service-side paging, top-frame locals/evaluation, and one aggregate level per expansion. | Complete snapshots are validated before handles are allocated. Hover, formatting, and non-top-frame locals/evaluation fail closed; the new frame guards still need target regression. |
| M4 execution | `continue`, `pause`, `next`, and `stepIn` for a single configured core. | Every multicore execution request fails before bridge I/O: no verified production `ExecutionDomain` executor exists. `stepOut` and run-to remain unsupported. |
| M5 memory/disassembly | Stopped, core-routed read-only memory and RH850 forward disassembly with stop-bound references. | Bare numeric multicore addresses require `inspection.default_core`. Memory writes, registers, nonzero instruction offsets, and instruction-level stepping remain unsupported. |
| M6 delivery/editors | Configuration-bound control/proxy, bounded `ensure`, offline `diagnose`, read-only host process diagnostics, reproducible Windows packaging, and CLion/VS Code/Zed integration. | Editor evidence has the limits described above. Reviewed tagged builds, hosted release acceptance, and publication remain open. |
| Native performance | Host DAP timelines, explicitly mapped Profiler CSV import, versioned packet normalization, experimental native requests, CLI capture, and same-session VS Code capture. | Actual native source/core association, vendor-format fixtures, snapshots, clocks, and target behavior remain unqualified. Capture does not issue collection start/stop requests. |

Queued execution and breakpoint work revalidate control-lease generations and
stop epochs before dispatch. Execution invalidates the old stopped observation;
stopped-only work requires a fresh definitive sample. Unknown lifecycle states
cannot revive stale handles or trigger automatic cleanup retries.

Cold acquisition is an explicit choice, not recovery from a failed warm bind.
Only a confirmed successful cold `open` permits typed rollback after a later
bootstrap failure. Warm sessions and unconfirmed opens are never closed.
Historical MULTI window calls have blocked without a supported call-level
cancel mechanism, so automatic session recovery remains fail-closed.

The local compatibility profile is MULTI IDE 8.1.4 with Compiler 2024.1.4 /
Probe 6.6.8. Embedded Python 3 is the default; Python 2.7 is an explicit legacy
fallback. The bridge loads its required `mbp_wire.py` companion only from
beside the selected script, not the project working directory or a cached
same-name module. Keep the release bundle intact.

The [capability matrix](docs/capabilities.md) is the authoritative request-level
inventory. The [architecture](docs/architecture.md) documents the full safety
contract, and [M0 findings](docs/m0-findings.md) distinguish measured hardware
behavior from open experiments.

### Recorded local verification

| Check | Result recorded on 2026-10-30 |
|---|---|
| Windows full release acceptance | 21 checks passed, zero failures or skips, including Go/race, policy, documentation, reproducible ZIPs, and WinGet manifest validation. |
| Linux host validation | Full uncached `go test -race` and `go vet` passed under WSL Ubuntu with Go 1.26.6. Linux is a host-test tier, not a supported MULTI daemon platform. |
| Python host tests | 98 bridge/native/helper tests and 87 reconnaissance tests passed; vendor APIs were faked. |
| Editor source checks | 48 VS Code Node tests and nine Zed tests passed, together with editor lint/package checks and the Zed Wasm build. |
| Actual VS Code Extension Host | Five synthetic scenarios passed on Insiders `1.145.0-insider`: default attach, cold reuse, warm reuse, host capture, and native capture. Each used one authenticated proxy; ordinary inspection remained available after native close. |
| Embedded bridge smoke | The current Python 3.8.6 bridge completed MBP handshake, `state` before open, EOF, and owned-process cleanup. This was not target or native-collector qualification; complete transient-process auditing was unavailable. |
| Workflow and Go security checks | actionlint passed. govulncheck found zero reachable vulnerabilities, with one imported-package and thirteen required-module findings whose vulnerable symbols were not called. The scan does not cover Rust dependencies. |

These are local observations, not a successful hosted CI or public release
claim. Exact versions, source/artifact hashes, independent-review findings,
test scope, and the bounded host-parser benchmark are recorded in the
[verification evidence index](docs/verification-evidence.md).

### What still needs completion

- **Target measurements:** M0 resident-loop, differential polling, and running
  breakpoint-clear experiments; a real source-breakpoint hit; inspection and
  execution regression on the intended configuration.
- **Verified primitives and native inputs:** per-core execution observations,
  step-out/run-to/register mechanisms, and provenance-backed Profiler, native
  trace, snapshot, RTOS, and language fixtures.
- **Editor acceptance:** real-target/visual VS Code checks and actual Zed
  installation, GUI, and target checks. End-to-end observer overhead remains
  separate from the host parser microbenchmark.
- **Release readiness:** a reviewed repository/package identity decision,
  reviewed source, successful hosted CI, release protections, and publication
  approval.

Continuing target work requires a current project TOML, matching ELF/source/core
identity, an explicitly selected session, and authorization for the specific
operations. Missing inputs do not justify enabling an unverified capability.

## Configuration and startup

Copy [multi-dap.example.toml](multi-dap.example.toml), then fill in local absolute paths and target-server parameters. Every TCP/UDP endpoint must use a numeric loopback address; port `0` is assigned by the operating system.

Before completing a project file, a release can be checked without starting
MULTI:

```powershell
multi-dap multi-version --installation C:\path\to\multi_814
```

The standalone command reports the release found by the installation metadata
by default. Add `--allow-executable-probe` when a metadata-free installation
must be inspected with its `gversion.exe` tool; this is an explicit opt-in
because it executes a program from the selected installation. Configuration
loading and `check` use the metadata-only path, so they never execute an
installation tool. Set
`multi.version = "8.1.4"` to make the detected release a configuration
constraint; omission or `"auto"` selects automatic detection.

If Memory View or Disassembly View sends a numeric-only address, a single-core configuration may omit `[inspection]`. A multi-core configuration must explicitly set `inspection.default_core` to a configured `[[cores]].id` so an address is not read from the wrong physical address space. This does not affect DAP requests carrying a core-qualified reference.

If a cold-connected target already contains the program corresponding to `connection.project`, explicitly set `connection.preparation = "already_present_no_verify"`. After successful `ConnectToTarget`, the bridge runs documented `prepare_target -verify=none`: MULTI's “Program already present on target / Verify: Not At All”. It does not download, flash, reset, or read verification memory. Omitting this field, or using `"none"`, performs no preparation. If MULTI still requires input, startup fails closed without GUI automation; the bridge first disconnects this cold connection and reports a separate cleanup-failure category if disconnect fails.

```powershell
Copy-Item multi-dap.example.toml C:\private\multi-dap-project.toml
go run -mod=readonly ./cmd/multi-dap check --config C:\private\multi-dap-project.toml
go run -mod=readonly ./cmd/multi-dap serve --config C:\private\multi-dap-project.toml --bridge-script bridge/bridge.py
```

A packaged executable discovers `bridge/bridge.py` beside itself, so it can be
started from another working directory. Source-tree development must pass
`--bridge-script` explicitly; the adapter never loads executable code from the
current project directory implicitly. Use `multi-dap version` to inspect
release metadata.

The above creates a cold session. To attach to an existing MULTI session, warm mode requires service-router loopback host/port and primary ELF as runtime arguments to this `serve` invocation:

```powershell
go run -mod=readonly ./cmd/multi-dap serve --config C:\private\multi-dap-project.toml `
  --bridge-script bridge/bridge.py --session-mode warm `
  --service-router-host <runtime-loopback-host> `
  --service-router-port <runtime-port> `
  --primary-elf <absolute-primary-elf>
```

Do not put service-router coordinates in TOML: they are runtime session state. If unique full-path binding does not hold, warm mode refuses to start rather than attempting a cold connection.

After startup, `serve` writes only one machine-readable stdout message containing `event`, `dap_address`, and `pid`.

For an advisory Windows host-process snapshot without opening MULTI or
contacting the target:

```powershell
go run -mod=readonly ./cmd/multi-dap doctor --config C:\private\multi-dap-project.toml --processes
```

Executable paths are omitted unless `--include-paths` is also supplied.
Ordinary startup notices contain only counts on stderr and leave ready stdout
unchanged. Inaccessible identities remain explicit, and an empty snapshot does
not prove process ownership or probe availability. Nothing is killed, adopted,
or reconnected by this diagnostic. Plain `doctor` and `check` remain static
configuration checks; see the [operations guide](docs/operations.md#read-only-host-process-diagnostics).

If the daemon terminates unexpectedly during startup or operation, run `diagnose` to read a safe classification of the latest terminal state from the owner-only control directory. `phase` distinguishes `runtime_start` from `runtime_serve`; an actor fail-closed response also reports the verified internal operation category. It reports only probe, PID, lifecycle phase, classification, and time; it never records or emits raw MULTI diagnostics, paths, connection parameters, or tokens:

```powershell
go run -mod=readonly ./cmd/multi-dap diagnose --config C:\private\multi-dap-project.toml
```

The DAP client then connects directly to `dap_address` in attach mode, or through the local authenticated proxy:

```powershell
go run -mod=readonly ./cmd/multi-dap proxy --config C:\private\multi-dap-project.toml
```

CLion uses the native Cidr DAP Debug Profile `multi-dap` for each applicable CMake Application configuration (for example, `<your-cmake-application>`); the profile runs `proxy --config "<absolute-project.toml>"` over stdio and uses `{"request":"attach"}` as Launch-tab JSON. Use the same private TOML for the profile and the Before Launch `multi-dap ensure --config "<absolute-project.toml>" --bridge-script "<absolute-path-to-bridge.py>" --acquisition cold` action. The bridge path must be absolute and must not depend on the project working directory. This does not implicitly attempt warm; to reuse an existing session, explicitly use `--bridge-script "<absolute-path-to-bridge.py>" --acquisition warm --primary-elf "<configured-exact-primary-elf>"`. The validated configuration is bound to the daemon's owner-only record, so a same-ID record created from different target semantics is rejected instead of reused. For field-level configuration, see the [CLion configuration guide](docs/clion.md). Click the Debug bug icon, not green Run. Stopping the daemon closes its own bridge; cold runtime disconnects the target connection it created; Windows Job cleanup reaps descendants when the ensure child exits. The implementation does not clean up unrelated MULTI, service-router, or target-server processes by process name.

Explicit cold mode reuses only an authenticated multi-dap daemon. Its advisory
host-process snapshot does not discover or bind arbitrary operator-owned MULTI
windows. Choose warm mode for an existing manual session; otherwise a new cold
session can contend for licenses or the probe.

## Performance timeline

The config-bound proxy can optionally export a bounded, payload-free Chrome
Trace JSON file while forwarding the editor's normal DAP conversation:

```powershell
multi-dap.exe proxy --config C:\private\project.toml `
  --perf-output C:\private\session-001.trace.json --perf-max-events 10000
```

Use a new absolute output path in an existing private directory. Recording is
off by default and does not issue extra target commands. The trace measures
host-side request round trips and observed DAP events, **not target CPU time,
function calls, or a sampled call tree**. It is finalized before a matched
successful DAP disconnect reply is forwarded, or when the proxy returns if that
boundary was not observed. The artifact can be opened with a compatible local
Perfetto/Chrome Trace viewer.

See [performance capture and interpretation](docs/performance.md) for limits,
health counters, privacy, and CLion usage. The
[MULTI 8 adaptation study](docs/ghs-8-adaptation.md) separates the implemented
host timeline from native TimeMachine and Profiler qualification requirements.

Offline target-data analysis is also available through `profile import` with
an explicit column/semantics mapping, and `trace normalize` for multi-dap's
versioned raw-packet capture envelope. These commands do not acquire a target,
guess a vendor CSV schema, or parse `.dsnap` files. See
[native performance analysis](docs/native-analysis.md) for the exact formats,
clock/privacy boundaries, and remaining native runtime qualification.

An experimental `multi-dap/trace/*` DAP surface also connects an explicitly
selected TimeMachine session through a daemon-owned Python 3.8+ helper.
It uses the existing frontend ownership and configuration barrier, bounded
reads, generation fencing, and local recovery receipts. Start and stop each
require explicit authorization; ordinary DAP startup does not create a native
collector. Source/core association is operator-declared and real native/runtime
acceptance remains open. `trace capture` reads one bounded interval through
this same config-bound DAP path and writes a private capture for
`trace normalize`; it does not start a daemon or collection. The VS Code
**Capture Existing Native Trace** command reads through the active configured
session without opening a second frontend. A bounded local Go helper validates
and privately writes the capture only after confirmed native close and terminal
IPC validation. Cancellation or unresolved requests retain reconciliation state;
the editor never stops collection or disconnects ordinary debugging as recovery.

## Validation

```powershell
go test -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -race ./...
go run -mod=readonly golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
go run -mod=readonly github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest bridge.test_bridge bridge.test_timemachine_native bridge.test_timemachine_helper
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest discover -s recon/tests
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest tools.test_validate_repository_language tools.test_validate_release_changelog
uv run --offline --no-python-downloads --no-project --python 3.12 -m tools.validate_repository_language --repository .
uv run --project docs --locked --python 3.12 zensical build --clean --strict
.\scripts\Invoke-WindowsReleaseAcceptance.ps1 -Mode Full -Version 0.1.0-rc.1
```

Full acceptance also runs the VS Code source checks and the Zed
format/test/lint/Wasm checks. Prepare their Node.js and pinned Rust prerequisites
as described in the [developer guide](docs/development.md). Actual VS Code
Extension Host acceptance is a separate opt-in command requiring an explicitly
selected local editor; it is not run by the full release-acceptance script.

Hardware probes run strictly serially, and raw evidence is written only to ignored `recon/out/`. See [recon/README.md](recon/README.md) for operating instructions.

GitHub Actions run the Go, vulnerability, workflow-policy, Python, editor,
documentation, and package checks. Version tags repeat the security gates,
build an attested Windows archive, and create a draft GitHub release. WinGet
manifest generation is included; `uv` and Homebrew installation are
intentionally deferred because this is not a Python package and the production
MULTI launcher is Windows-only.

## License

`multi-dap` is distributed under the [MIT License](LICENSE). Green Hills MULTI
and its Python modules are separate proprietary products and are not included
in this repository or release archive. See [third-party notices](THIRD_PARTY_NOTICES.md)
for bundled dependency licenses.

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) and the
[Code of Conduct](CODE_OF_CONDUCT.md) before opening a pull request. Report
vulnerabilities through the private process in [SECURITY.md](SECURITY.md), not
through a public issue.
