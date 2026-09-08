# multi-dap

`multi-dap` exposes Green Hills MULTI debugging sessions as a local Debug Adapter Protocol (DAP) service. The debugger core and DAP frontend are implemented in Go; `bridge.py` handles only Python 2.7 MULTI API integration.

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

[Configure CLion step by step](docs/clion.md). VS Code support is currently a
software-tested source extension; its GUI path has not reached the same
hardware-observed evidence level. Zed remains unsupported.

## Documentation

- [Getting started](docs/getting-started.md)
- [CLion configuration](docs/clion.md)
- [Project configuration reference](docs/configuration.md)
- [Architecture and design](docs/architecture.md)
- [Capabilities and development status](docs/capabilities.md)
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

## Development status

M1 base runtime is complete: strict TOML configuration, MBP v1, bridge-process startup and generation isolation, MULTI state/core resolution, a Session Actor, DAP transport, and `initialize`, `attach`, `configurationDone`, `threads`, `continue`, `pause`, and `disconnect`.

- **M2** is on the production path: breakpoint creation/maintenance, owned breakpoints, and a stop arbiter with events, epochs, and deduplication. A source breakpoint is installed only after source presence has a definitive result on every configured core; `Unknown` always fails closed. A transaction state mutex is not held across `Set`/`Clear`; a detach fence turns subsequent concurrent-transaction records into pending cleanup; each `StopEpoch` has at most one natural cleanup retry. Read-only p12 hardware evidence shows non-DWARF presence uses a strict complete `l f` listing, canonical exact membership, and a per-core snapshot for every Resolve. Direct DAP Set/Clear and native CLion synchronization have been observed on a stopped target; an actual breakpoint hit remains outstanding. Host/race tests primarily cover these transaction invariants and cannot prove that `bp_clear` is non-disruptive while running.
- **M3** provides `stackTrace` for top-level frames, one level of aggregate children, and service-snapshot paging. Standard DAP `threads` plus paged `stackTrace` (including `totalFrames`) let CLion Parallel Stacks View obtain separate stacks for configured core0/core4, only while stopped. The backend captures a complete stack snapshot and pages it locally by DAP parameters; `variables` type/paging are negotiated during `initialize`. Hover, formatting, and non-top-level frames fail closed. Omitted DAP `evaluate.frameId` deterministically selects the first configured core's top-level frame from the stopped snapshot; explicit frame handles retain core and stop epoch. The MULTI parser has input limits; complete snapshots are validated before paging and variable-handle allocation; structured locators accept only ASCII syntax and reject control characters.
- **M4** wires `next` and `stepIn` only for the single-configured-core path. Multi-core requests require an `ExecutionDomain` that can prove per-core before/after observations; no production executor exists, so they fail closed before bridge I/O. `stepOut` and run-to have no verified MULTI primitive.
- **M5** provides read-only memory and RH850 disassembly. Every operation requires a stopped target and immutable-topology routing; frame PCs use opaque references containing a core and stop epoch. CLion Cidr Memory View numeric-only addresses lack that identity. Multi-core targets must explicitly select an inspection address space with optional `inspection.default_core`; otherwise a bare numeric request fails closed. `writeMemory`, registers, non-zero `instructionOffset`, and instruction-level stepping are not implemented.
- **M6** implements local control (`status`, `shutdown`, `proxy`), deterministic packaging, a VS Code local-proxy source extension, and the CLion Cidr DAP CMake Debug contract. CLion starts the `multi-dap` stdio proxy profile from the Debug (bug icon) for each applicable CMake Application configuration; a Before Launch External Tool or manual command runs short-lived, idempotent `ensure`. The recommended CLion setup explicitly chooses cold acquisition; warm acquisition strictly reuses an operator-owned session. Never use the green Run button for a target ELF. CLion 2026.2.1 has regression coverage for initialize framing, the configuration barrier, and explicit-false disconnect. A native Cidr CLion + hardware smoke test completed: from no daemon/MULTI/router/target-server, Before Launch cold `ensure` starts and attaches; threads/stacks for two configured cores are correct, empty locals return `[]`, Evaluate returns target state, and source-breakpoint Set/Clear synchronize to the daemon without retaining ownership. Actual breakpoint hits, the VS Code GUI, and Zed remain outstanding.

Hardware has demonstrated that cold `DebugProgram` / `ConnectToTarget` returns; the explicit CLion cold Before Launch entry point has completed the native Cidr + hardware loop. Warm remains for strict reuse that does not alter an operator-owned session. Unique full-path binding, program-component topology for two configured cores, strict source-file listing, direct DAP threads/stack/variables/evaluate, and source-breakpoint Set/Clear have all passed on a stopped target. After the latest native CLion launch, MULTI Window Register can enumerate windows, but `GetProgram()` on the first debugger window blocks with no `CommandReply`; this API version has no call-level cancel/timeout. That operator-owned warm session cannot support automatic recovery; explicit cold acquisition is a separate new-session choice, not recovery from a warm failure or an implicit fallback.

Cold bootstrap attempts typed `close` rollback after a subsequent `cores` or initial-state failure only after `open` has returned explicit success from the same bridge generation. Warm sessions and unconfirmed opens are never closed. Uncertain rollback itself fails closed. This is the lifecycle host-test behavioral boundary, not a conclusion about hardware-session recovery.

For remaining hardware boundaries, see [M0 findings](docs/m0-findings.md); for the complete design, see [architecture](docs/architecture.md).

## Configuration and startup

Copy [multi-dap.example.toml](multi-dap.example.toml), then fill in local absolute paths and target-server parameters. Every TCP/UDP endpoint must use a numeric loopback address; port `0` is assigned by the operating system.

If Memory View or Disassembly View sends a numeric-only address, a single-core configuration may omit `[inspection]`. A multi-core configuration must explicitly set `inspection.default_core` to a configured `[[cores]].id` so an address is not read from the wrong physical address space. This does not affect DAP requests carrying a core-qualified reference.

If a cold-connected target already contains the program corresponding to `connection.project`, explicitly set `connection.preparation = "already_present_no_verify"`. After successful `ConnectToTarget`, the bridge runs documented `prepare_target -verify=none`: MULTI's “Program already present on target / Verify: Not At All”. It does not download, flash, reset, or read verification memory. Omitting this field, or using `"none"`, performs no preparation. If MULTI still requires input, startup fails closed without GUI automation; the bridge first disconnects this cold connection and reports a separate cleanup-failure category if disconnect fails.

```powershell
Copy-Item multi-dap.example.toml C:\private\multi-dap-project.toml
go run ./cmd/multi-dap check --config C:\private\multi-dap-project.toml
go run ./cmd/multi-dap serve --config C:\private\multi-dap-project.toml --bridge-script bridge/bridge.py
```

A packaged executable discovers `bridge/bridge.py` beside itself, so it can be
started from another working directory. Source-tree development must pass
`--bridge-script` explicitly; the adapter never loads executable code from the
current project directory implicitly. Use `multi-dap version` to inspect
release metadata.

The above creates a cold session. To attach to an existing MULTI session, warm mode requires service-router loopback host/port and primary ELF as runtime arguments to this `serve` invocation:

```powershell
go run ./cmd/multi-dap serve --config C:\private\multi-dap-project.toml --session-mode warm `
  --service-router-host <runtime-loopback-host> `
  --service-router-port <runtime-port> `
  --primary-elf <absolute-primary-elf>
```

Do not put service-router coordinates in TOML: they are runtime session state. If unique full-path binding does not hold, warm mode refuses to start rather than attempting a cold connection.

After startup, `serve` writes only one machine-readable stdout message containing `event`, `dap_address`, and `pid`.

If the daemon terminates unexpectedly during startup or operation, run `diagnose` to read a safe classification of the latest terminal state from the owner-only control directory. `phase` distinguishes `runtime_start` from `runtime_serve`; an actor fail-closed response also reports the verified internal operation category. It reports only probe, PID, lifecycle phase, classification, and time; it never records or emits raw MULTI diagnostics, paths, connection parameters, or tokens:

```powershell
go run ./cmd/multi-dap diagnose --config C:\private\multi-dap-project.toml
```

The DAP client then connects directly to `dap_address` in attach mode, or through the local authenticated proxy:

```powershell
go run ./cmd/multi-dap proxy --config C:\private\multi-dap-project.toml
```

CLion uses the native Cidr DAP Debug Profile `multi-dap` for each applicable CMake Application configuration (for example, `<your-cmake-application>`); the profile runs `proxy --config "<absolute-project.toml>"` over stdio and uses `{"request":"attach"}` as Launch-tab JSON. Use the same private TOML for the profile and the Before Launch `multi-dap ensure --config "<absolute-project.toml>" --bridge-script "<absolute-path-to-bridge.py>" --acquisition cold` action. The bridge path must be absolute and must not depend on the project working directory. This does not implicitly attempt warm; to reuse an existing session, explicitly use `--bridge-script "<absolute-path-to-bridge.py>" --acquisition warm --primary-elf "<configured-exact-primary-elf>"`. The validated configuration is bound to the daemon's owner-only record, so a same-ID record created from different target semantics is rejected instead of reused. For field-level configuration, see the [CLion configuration guide](docs/clion.md). Click the Debug bug icon, not green Run. Stopping the daemon closes its own bridge; cold runtime disconnects the target connection it created; Windows Job cleanup reaps descendants when the ensure child exits. The implementation does not clean up unrelated MULTI, service-router, or target-server processes by process name.

Explicit cold mode reuses only an authenticated multi-dap daemon and does not scan arbitrary operator-owned MULTI. Choose warm mode for an existing manual session; otherwise a new cold session can contend for licenses or the probe.

## Validation

```powershell
go test -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -race ./...
go run -mod=readonly golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
go run -mod=readonly github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest bridge.test_bridge
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest discover -s recon/tests
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest tools.test_validate_repository_language tools.test_validate_release_changelog
uv run --offline --no-python-downloads --no-project --python 3.12 -m tools.validate_repository_language --repository .
uv run --project docs --locked --python 3.12 zensical build --clean --strict
.\scripts\Invoke-WindowsReleaseAcceptance.ps1 -Mode Full -Version 0.1.0-rc.1
```

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
