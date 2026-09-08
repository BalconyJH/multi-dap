# M6: Windows Delivery and Editor Integration Baseline

This page defines the currently reproducible Windows deliverable and
editor-integration evidence. The repository provides a VS Code local-proxy
source extension, a field contract for native CLion Cidr DAP CMake Debug
configuration, and CLion 2026.2.1 framing regressions. Host transport evidence
and a limited native CLion hardware smoke test are recorded separately below;
neither constitutes full execution, breakpoint-hit, or release qualification.

## Reproducible Windows package

Run from the repository root:

```powershell
pwsh -File .\scripts\New-WindowsPackage.ps1 -OutputDirectory C:\dist -Version 0.1.0
```

The script follows the existing Go-module workflow and runs `go build ./cmd/multi-dap` with fixed `GOOS=windows`, `GOARCH=amd64`, `CGO_ENABLED=0`, `-mod=readonly`, `-trimpath`, `-buildvcs=false`, stripped-debug-symbol linker options, and `GOPROXY=off` to use only the existing module cache. It does not modify `go.mod`, write to the repository, or overwrite the destination archive. Staging uses a randomly named temporary directory that is removed at completion; before removal, the script verifies that it remains under the system temporary root and has its dedicated random prefix.

The outputs are `multi-dap-<version>-windows-amd64.zip` and a detached
`.zip.sha256` checksum. The archive has fixed ordering, uncompressed ZIP
entries, a fixed UNIX timestamp (default `1980-01-01T00:00:00Z`), and fixed
archive paths. The binary reports the requested version and reproducible build
timestamp through `multi-dap version`; release builds can also embed the source
commit. Repeated packaging with the same Go toolchain, module cache, version,
commit, timestamp, and input sources should yield the same SHA-256. Verify two
independent package builds with:

```powershell
pwsh -File .\scripts\Test-WindowsPackage.ps1 -Version 0.1.0-test
```

## Release-acceptance gate

Before release, run from the repository root (full mode is the default):

```powershell
pwsh -NoProfile -File .\scripts\Invoke-WindowsReleaseAcceptance.ps1 -Version 0.1.0-rc.1
```

Full mode runs `go test ./...`, `go vet ./...`, and `go test -race ./...` under `GOPROXY=off` and `-mod=readonly`, so it uses only the existing Go module cache, downloads no dependencies, and does not rewrite `go.mod` or `go.sum`. It then runs bridge and recon Python 3 host tests using `uv run --offline --no-python-downloads --no-project --python 3.12`. These tests use a fake MULTI API, do not start a GUI, do not connect hardware, and do not automatically download Python.

If passed `-MultiPython <path-to-python.exe>`, or if `MULTI_PYTHON` is set (also inferred from `MULTI_INSTALLATION` / `GHS_MULTI_ROOT`), the script confirms it is Python 2, then compiles the bridge, recon library, runner, notifier, and probes into a temporary directory it creates. Without configured MULTI Python 2, this step is explicitly `skipped` and is never counted as passing. Compilation runs no probe and writes no `.pyc` into the working tree.

Finally, full mode runs `Test-WindowsPackage.ps1`: it independently builds two ZIP files, requires matching archive hashes, verifies each archive's entry allowlist and rejection of runtime artifacts such as configuration/token/capture/log/ready files, and validates in both directions that the manifest has a complete, unique, ordered payload set and SHA-256 for every payload. This is a PowerShell self-test independent of Pester.

Each step emits `PASS`, `FAIL`, or `SKIP` with its duration. The final line is CI-readable `RELEASE_ACCEPTANCE_RESULT` JSON; the process exits with `1` when any required step fails. During development, `-Mode Fast` runs ordinary Go test, vet, and both Python host-test suites. It explicitly skips race tests, MULTI Python 2 compilation, and dual-build package verification, and cannot be used for release.

The package contains only:

- `multi-dap.exe`
- `bridge/bridge.py`
- `examples/multi-dap.example.toml`
- `editors/clion/README.md`
- `editors/clion/profile-contract.json`
- The distribution README, `LICENSE.txt`, `THIRD_PARTY_NOTICES.md`, dependency
  license texts, `release-provenance.json`, and `SHA256SUMS.txt`

It excludes `recon/`, `recon/out/`, local TOML files, credentials, target-connection parameters, daemon control records, bridge ready files, and other temporary files. `notifier.py` is not currently packaged: the daemon does not install its `AFTER_GHS_STARTUP_PYTHON` startup hook, so distributing it would imply a runtime promise that does not exist.

`LICENSE.txt` contains the same MIT License text as the repository root. The
positive release policy binds that exact text by SHA-256, and package tests
reject root/package license drift.

`SHA256SUMS.txt` is an ordered SHA-256 manifest of all payload files, excluding the manifest itself. The package README provides a PowerShell verification command.

`release-provenance.json` binds the requested product version, optional source
commit, reproducible build timestamp, Windows amd64 target, Go version, and the
executable's size and digest. It is part of the payload covered by
`SHA256SUMS.txt`.

The packaged executable first discovers `bridge/bridge.py` relative to its own
path. An explicit `--bridge-script` remains available and is required for
source-tree execution such as `go run`; there is no implicit working-directory
fallback. The audited CLion setup also keeps the path explicit. This makes an
intact ZIP or WinGet installation independent of the caller's working directory
without allowing a project checkout to replace executable code implicitly.

The release workflow can generate a WinGet 1.12 multi-file manifest for the
exact archive URL and digest. It uses a portable ZIP with
`ArchiveBinariesDependOnPath: true` so the executable and companion bridge stay
together. See the [release guide](releasing.md) for publication gates and the
reasons `uv` and Homebrew installation are deferred.

## VS Code: local-proxy extension

[Official VS Code documentation](https://code.visualstudio.com/docs/debugtest/debugging-configuration) requires each `launch.json` configuration's `type` to be provided by an installed debug extension. The repository's `editors/vscode` directory provides the `multi-dap` debug type, a configuration provider, and a `DebugAdapterDescriptorFactory`; it starts only this supported local command:

```text
multi-dap proxy --config <absolute-project.toml> [--control-dir <absolute-directory>]
```

It therefore connects only to a daemon the user has already started and can use the dynamic port produced by `endpoints.dap.port = 0` in TOML. `proxy` first authenticates the private control record. The authentication ACK is the protocol-upgrade boundary; the daemon immediately hands that same control socket to `dap.Server.ServeConnection`, without querying `status` first or dialing `DAPAddress` a second time. This eliminates the TOCTOU where a loopback listener is rebound after daemon exit. The extension does not read tokens and rejects `host`, `port`, and `debugServer`, avoiding a bypass of that authentication boundary. It does not start cold/warm `serve` itself, because those modes require bridge, session, and lifecycle inputs that the user must explicitly provide. The extension is fixed to run in the local UI extension host; a remote workspace is not a supported integration today.

`examples/vscode/launch.json` defines the only supported attach-configuration contract. After variable substitution, `configFile` (and optional `controlDirectory`) must be absolute paths. The extension source directory provides `npm test`, `npm run lint`, and `npm run package-contents`; the last enumerates publish contents with non-writing `npm pack --dry-run`.

## Zed: TCP field exists; adapter registration remains missing

[Zed Debugger documentation](https://zed.dev/docs/debugger) confirms that a `.zed/debug.json` task needs `adapter` and `label` and may have `tcp_connection`; it also states adapters are supplied by built-in support or a language extension. The documentation does not establish that an arbitrary unregistered adapter name can start from `debug.json` alone, so this repository does not fabricate a Zed JSON configuration for `multi-dap`.

Follow-up work is to implement/register a Zed adapter and validate its TCP connection field, `attach` initialization sequence, and stopped-target disconnect behavior with a real daemon. Zed can read `.vscode/launch.json`, but that does not replace adapter registration.

## CLion: Cidr DAP CMake Debug and ensure

The Cidr DAP Debug Profile validated with CLion 2026.2.1 (build
262.9437.136) can be launched from the Debug path of a CMake Application.
Assign the DAP profile named `multi-dap` to each applicable CMake Application
configuration (for example, `<your-cmake-application>`). The profile executable
is `multi-dap.exe`, its arguments are
`proxy --config "<absolute-project.toml>"`,
communication is `stdin/stdout`, and its **Launch**-tab JSON must be
`{"request":"attach"}`; do not use the Attach tab or TCP Remote address.
Use the same absolute private TOML path for this profile and the Before Launch
tool. The proxy binds the owner-only control record to an opaque digest of the
normalized validated configuration and rejects a same-ID daemon with different
target semantics. `proxy --probe-id` remains a legacy unbound CLI path and is
not the supported CLion contract.

Create `multi-dap ensure` in **Settings | Tools | External Tools**, with the distributed `multi-dap.exe` as Program and these Arguments:

```text
ensure --config "<absolute-project.toml>" --bridge-script "<absolute-path-to-bridge.py>" --acquisition cold
```

`--bridge-script` must be the absolute path to `bridge/bridge.py` in the distribution package, because an External Tool's working directory is the target-project root rather than the package root.

In **Run | Edit Configurations**, select each applicable CMake Application configuration and choose the `multi-dap` Debug Profile. Add `multi-dap ensure` to **Before launch** for each configuration. Creating the External Tool does not do this automatically: save the tool, then select **Before launch | + | Run External Tool** in the CMake configuration. After saving, reopen each configuration and confirm Before launch lists the task; the corresponding persisted run-task list must be nonempty, but do not hand-author `.idea` files. Alternatively, do not bind the External Tool and run the same `ensure` command manually before Debug.

Before the first attach, clear every Exception Breakpoint in **Run | View Breakpoints** (`Ctrl+Shift+F8`). When CLion synchronizes an empty list, multi-dap returns a side-effect-free success response. A nonempty exception configuration has no corresponding MULTI semantics, so it fails closed before reaching the backend or bridge and cannot be silently ignored.

`ensure` is synchronous, short-lived, and idempotent bootstrap: it returns `already_ready` when the daemon is ready; otherwise explicit `--acquisition cold` starts a cold daemon under its lifecycle management and returns after the authenticated control record is ready. It does not first perform warm discovery or implicitly fall back from another acquisition. Target-connection parameters come from TOML and must not be copied into the External Tool; the control token remains only in the owner-only control record.

To reuse an operator-established MULTI session, explicitly use:

```text
ensure --config "<absolute-project.toml>" --bridge-script "<absolute-path-to-bridge.py>" --acquisition warm --primary-elf "<configured-exact-primary-elf>"
```

Warm mode strictly discovers only the existing service router and does not cold-connect on failure. Cold mode forbids `--primary-elf`; warm mode requires it. Explicit cold mode reuses only an authenticated, configuration-bound multi-dap daemon; it does not scan for or take over arbitrary operator-owned MULTI. An existing manual session must use warm mode, otherwise a new cold session can contend for licenses or the probe. The private TOML path and real probe ID must not appear in project documentation or release packages.

Select `<your-cmake-application>` and click the **Debug bug icon**, never the green Run button; Run treats the target ELF as a Windows program. The old `multi-dap attach` is a Python configuration and must be disabled or removed. For complete fields and the acceptance checklist, see the dedicated [CLion configuration guide](clion.md). The packaged `editors/clion/profile-contract.json` is an abstract field contract and does not fabricate a cross-version-importable `.idea` file.

Host regressions cover DAP transport/initialization behavior from local CLion 2026.2.1 (build 262.9437.136): it negotiates variable type/paging, observes initialized → empty exception synchronization → `configurationDone` barrier, and sends explicit `terminateDebuggee:false` on detach. multi-dap accepts false and releases only DAP ownership; true continues to fail closed. A native Cidr + hardware smoke test on local CLion 2026.2.1 completed with this CMake Debug configuration: from no daemon, MULTI, service router, or target server, Before Launch cold `ensure` starts the complete process tree and attaches paused within 14 seconds; core0/core4 threads and source stacks are correct, and empty locals, Evaluate, and native source-breakpoint Set/Clear paths are verified. Resume/Step was not run, and an actual breakpoint hit remains outside the evidence scope.

The Cidr Console page is not a DAP evaluator; use `Alt+F8`, Variables, or Live Watches. Manual addresses sent by Cidr Memory View are numeric-only and carry neither core nor stop epoch. A single-core configuration routes them to its sole core; a multi-core configuration must explicitly set `inspection.default_core`, otherwise the request is rejected before bridge I/O. Frame PCs use core+stop-epoch opaque references. Read-only Memory View in stopped state and RH850 forward Disassembly are complete; memory writes, registers, nonzero instruction offsets, automatic memory references for pointer variables, and instruction stepping continue to fail closed.
