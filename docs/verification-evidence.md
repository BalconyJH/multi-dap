# Verification Evidence Index

This index records sanitized, local verification evidence collected on
2026-09-08. It is an audit aid, not a claim that a public release, hosted
workflow, or hardware qualification has occurred.

## Local release-candidate evidence

| Category | Command or check | Result |
|---|---|---|
| Go | `Invoke-WindowsReleaseAcceptance.ps1 -Mode Full` (format, unit tests, vet, build, and race) | Passed. |
| Actor shutdown race | Actor package repeated 50 times plus race-enabled repeated runs | Passed after admission and lifetime-wait fencing; no stranded queued, inflight, or post-close request. |
| Configuration binding | Go unit and race tests for normalized digests, same-ID mismatch rejection, and the `ensure` parent/child fence | Passed. |
| License policy | Identical root/package MIT text, explicit LF checkout attributes, and policy-bound SHA-256 | Passed with public distribution approved. |
| Go vulnerability database | `go run -mod=readonly golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...` | Passed: no known reachable vulnerability was reported. |
| Bridge host tests | Python 3.12 `unittest bridge.test_bridge` | Passed: 37 tests. |
| Recon host tests | Python 3.12 `unittest discover -s recon/tests` | Passed: 71 tests. |
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
.\scripts\Invoke-WindowsReleaseAcceptance.ps1 -Mode Full -Version 0.1.0 -Commit abcdef0123456789abcdef0123456789abcdef01 -SourceDateEpoch 315532800 -MultiPython '<MULTI-python2.exe>'
```

## MULTI interpreter compilation

The full release gate's byte-compilation check was run with the discovered
MULTI embedded Python 2.7.3 interpreter and passed. It compiled the bridge and
recon Python sources only; it did not execute a probe, open a target, or create
bytecode in the worktree. The interpreter location, installation settings, and
target configuration are deliberately omitted from this public index.

This is a compatibility check, not a hardware claim. Host tests and byte
compilation do not demonstrate attach, run control, inspection, breakpoint, or
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
