# Verification Evidence Index

This index records sanitized, local verification evidence collected on
2026-09-13. It is an audit aid, not a claim that a public release, hosted
workflow, or hardware qualification has occurred.

## MULTI 8 compatibility evidence

| Environment | Check | Result |
|---|---|---|
| MULTI IDE 8.1.4 + Compiler 2024.1.4 / Probe 6.6.8 | Read-only `p00_env` with `GHS_IDE_USE_PYTHON3=1` | Python 3.8.6 loaded; `ghs_debugger` and `ghs_debugger_api` imported; no target contact. |
| Same installation | Read-only `p00_env` with `GHS_IDE_USE_PYTHON3=0` | Python 2.7.3 loaded; required debugger modules imported; no target contact. |
| Same installation | `bridge.py` under Python 3.8.6, loopback MBP handshake and `state` request | Passed; returned the expected `session_not_open` error without opening a target. |

These checks establish interpreter and bridge-process compatibility only. They do
not qualify C++20, RH850/U2B24, breakpoints, execution, or any other target
operation on the newer toolchain.

## Local release-candidate evidence

| Category | Command or check | Result |
|---|---|---|
| Go | `Invoke-WindowsReleaseAcceptance.ps1 -Mode Full` (format, unit tests, vet, build, and race) | Passed. |
| Actor shutdown race | Actor package repeated 50 times plus race-enabled repeated runs | Passed after admission and lifetime-wait fencing; no stranded queued, inflight, or post-close request. |
| Configuration binding | Go unit and race tests for normalized digests, same-ID mismatch rejection, and the `ensure` parent/child fence | Passed. |
| License policy | Identical root/package MIT text, explicit LF checkout attributes, and policy-bound SHA-256 | Passed with public distribution approved. |
| Go vulnerability database | `go run -mod=readonly golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...` | Passed: no known reachable vulnerability was reported. |
| Bridge host tests | Python 3.12 `unittest bridge.test_bridge` | Passed: 38 tests. |
| Recon host tests | Python 3.12 `unittest discover -s recon/tests` | Passed: 77 tests. |
| VS Code extension | `npm test`, `npm run lint`, `npm run package-contents` | Passed: 8 tests, lint, and package-content checks. |
| Documentation | `uv run --project docs --locked --python 3.12 zensical build --clean --strict` | Passed with no issues. |
| Repository policies | Language and release-changelog validator unit tests and enforcement | Passed: 23 tests and both policies. |
| Workflow syntax | `actionlint` v1.7.12 | Passed. |
| Windows archive | Two deterministic package builds with fixed version, commit, and source-date epoch | Passed: archive digests and internal manifests matched. |
| WinGet candidate | Generated 1.12 manifest set and local `winget validate` | Passed structural and tool validation. |

The latest full acceptance invocation used synthetic release metadata: version
`0.1.0`, a synthetic 40-hex commit identifier, and a fixed source-date epoch.
All 19 steps passed. These values prove stable-version changelog validation,
packaging determinism, and validation wiring only; they do not identify a
source commit suitable for publication.

```powershell
.\scripts\Invoke-WindowsReleaseAcceptance.ps1 -Mode Full -Version 0.1.0 -Commit abcdef0123456789abcdef0123456789abcdef01 -SourceDateEpoch 315532800 -MultiPython3 'C:\path\to\multi_814\python-3.8.6\python.exe'
```

## MULTI interpreter compilation

The recorded full release-gate byte-compilation check was run with the
discovered MULTI standalone Python 3.8.6 interpreter and passed. The retained
`-MultiPython` argument in earlier runs is the Python 2 compatibility alias;
current acceptance searches Python 3 first. It compiled the bridge and
recon Python sources only; it did not execute a probe, open a target, or create
bytecode in the worktree. The interpreter location, installation settings, and
target configuration are deliberately omitted from this public index.

This is a compatibility check, not a hardware claim. Host tests, interpreter
smoke checks, and byte compilation do not demonstrate attach, run control, inspection, breakpoint, or
multicore behavior against a live target.

## CLion support evidence

- The versioned profile contract records CLion 2026.2.1 build 262.9437.136,
  native Cidr DAP CMake Debug, config-bound stdio proxy arguments, and exact
  Launch-tab attach JSON. Go contract tests and Windows package tests keep that
  contract synchronized with the shipped offline guide.
- Host regressions cover CLion initialization framing, the configuration
  barrier, empty exception-breakpoint synchronization, variables paging/type
  negotiation, and `disconnect` with `terminateDebuggee:false`.
- Sanitized limited hardware observations cover cold attach, configured-core
  threads and source stacks, top-level inspection, read-only Memory View and
  Disassembly, and source-breakpoint synchronization. They do not establish an
  actual breakpoint hit or full execution-control acceptance.

Follow the [CLion configuration guide](clion.md) for the supported setup and
its current UI-level capability boundaries.

## Evidence boundaries and next evidence

- The source tree used for local checks was uncommitted. Re-run the complete
  acceptance gate from the reviewed, committed tag before publication.
- No hosted GitHub workflow, public release, artifact attestation verification,
  protected-environment approval, or repository rule configuration is claimed
  here. Those require the canonical repository and maintainer access.
- The post-release WinGet staging and clean-host install/uninstall workflow is
  implemented but has not run against a published immutable release. No
  clean-host WinGet lifecycle result is claimed; upgrade requires two stable
  public versions.
- No blanket hardware qualification is claimed. The limited CLion observations
  above do not expand operation-level support. Consult the sanitized
  [hardware findings](m0-findings.md) and [capability matrix](capabilities.md)
  for what has and has not been demonstrated.

For the repeatable release procedure, see the [release guide](releasing.md).
