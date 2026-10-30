# Verification Evidence Index

This index records sanitized, local verification evidence. Each section
defines its own scope. It is an audit aid, not a claim that a public release,
hosted workflow, or hardware qualification has occurred.

## E2 reconnect and scripted DAP acceptance (2026-10-29)

The operator authorized disconnect/reconnect and subsequently authorized
connection-related halt/reset, but not download, erase/program, explicit run,
or trace start/stop. The old E2 connection was disconnected serially; its
target server and trace processes exited. A documented temporary connection
with the selected server arguments restored communication through the
retained MULTI instance. The original ELF was associated using
`prepare_target -verify=none`, without a load request or memory verification.
That association is not an independent image-integrity check.

Two real response shapes exposed software defects and received narrow fixes:

- MULTI 8.1.4 emitted a name-first component alias list. The parser now keeps
  the complete display tail and marks its ambiguity instead of rejecting the
  observed ordering. Suffix PID/core text never establishes identity; exact
  routed `P` validation remains authoritative.
- `print 1 + 1` returned the bare line `2`. Evaluation now accepts bounded
  single-line decimal integers without numeric conversion or inferred
  type/address/children. Locals, named values, and aggregate grammars are not
  relaxed.

Both changes have sanitized fixtures, negative tests, and independent security
review. After the final fix, `go test ./...`, `go vet ./...`, and race tests for
the multi, actor, daemon, DAP, and CLI packages passed.

| Check | Observed result and boundary |
|---|---|
| Scripted warm DAP retest | All ten requests succeeded: initialize, attach, configurationDone, threads, local native status, stackTrace, scopes, variables, evaluate, and non-terminating disconnect. One configured Host core was inspected; empty locals at the reset location are not a general variables acceptance claim. |
| Host timeline | A complete 3614-byte Chrome Trace JSON recorded ten completed requests, zero failed/unfinished requests, zero framing errors, and zero dropped events. Owner-only output ACL was checked. This is host observation, not target CPU profiling, real-editor acceptance, or an observer-overhead benchmark. |
| Native source-selection evidence | A one-shot application launched through the explicitly routed MULTI `trace api` context recorded the vendor-supplied native session argument. It made no native API calls; successful argument delivery does not establish core/source binding. |
| Integrated native capture | The selected-session `open` failed. Its reserved zero-byte file is invalid capture evidence. No packets were obtained and native open was not retried. |
| Native failure isolation | Local status reported `faulted`, `needs_reconciliation:true`, helper abort requested, and native disconnect unconfirmed. Six subsequent local/status/ordinary DAP requests succeeded; no native reconnect or collection control was attempted. |
| Init-only diagnostics | After normal daemon cleanup, a bounded owned subprocess loaded/initialized the real DLL and enumerated an empty session list. A separate saved-argument initialization attempt timed out and was reaped by its owned Job. Neither connected a native session or read packets. Argument replay is not equivalent to testing an actual MULTI-launched native application. |
| Cleanup and target state | Both temporary test daemons were shut down through their authenticated configuration-bound control path. The original MULTI session and replacement E2 server remained. Both physical cores reported stopped at reset locations after the tests; earlier workload results cannot be reused after reconnect/reset. |

The final host timeline SHA-256 is
`cdadae53db035c9f0f74847c2c90b272f4fe3c8b52489f394b769428fd448b46`.
Raw transcripts, connection configuration, native arguments, and artifacts
remain in owner-restricted private storage outside the repository. No real
native stream, target timing, RTOS/register, snapshot, or Profiler format
qualification is claimed. The earlier full release/package acceptance below
predates these two fixes; no new package publication occurred.

## Selected E2 session preflight (2026-10-29, afternoon)

The operator subsequently supplied a reference to an existing Release test
session. Its connection files and primary ELF existed, and the ELF digest
matched the referenced capture/build evidence. A new private warm-only test
TOML passed the fresh executable's static `check`. The original connection
file contains download-mode settings; these were not executed.

Fresh process evidence identified the corresponding MULTI, service router,
E2 target server, and trace server. A read-only command-socket `components`
query confirmed the exact primary-ELF component. This resolves the missing
configuration/session prerequisite from the earlier morning check, but does
not establish a healthy target connection.

The subsequent read-only CPU-status query returned
`0x109: fatal err (USB communication error)` from the existing target server.
Earlier component-state observations were not consistent enough to establish
that both physical cores were stopped; the post-error target state is
unconfirmed. The existing trace context reported collection enabled and no
active retrieval/decoding, not evidence that this test started collection or
that valid target packets were available.

Target access stopped after the error. No warm/cold daemon acquisition,
native DLL initialization, native connection/capture, or DAP feature test was
attempted. No target halt/run/reset/download, reconnect, trace start/stop, or
process termination was issued. The original four session processes remained
present in the final passive inventory. Private configuration and raw
preflight evidence were retained outside the repository; live feature
acceptance is `NOT_RUN`, not a software failure or hardware pass.

## E2 readiness check (2026-10-29)

After the operator reported connecting an E2, two independent read-only
investigations checked the runtime and known configuration locations. No
debugger/control socket was opened and no vendor DLL was initialized.

| Check | Result and scope |
|---|---|
| USB device | Windows enumerated Renesas E2 with status OK, driver service `E2usb`, and configuration-manager error code zero. This establishes host USB recognition, not a target connection or trace capability. |
| Runtime ownership | Process/listener snapshots found no MULTI, TimeMachine, or multi-dap daemon. The default control directory had only historical terminal records, not an active daemon record. |
| Configuration | All three known local scratch TOMLs failed the freshly built `check` command because their project/ELF inputs no longer existed. The current CLion project/settings supplied no GHS configuration. No stale configuration was repaired by guessing another project or image. |
| Source binary | A fresh source build succeeded and matched the previously tested native-integration executable byte for byte. |
| Static native ABI | Installed MULTI 8.1.4 inspection under its standalone Python 3.8.6 found the required exports, compatible 64-bit runtime, 64-byte info structure, and 32-byte packet structure. The result retained `dll_loaded:false` and `target_contacted:false`. |
| Offline export smoke | The fresh executable normalized the existing explicitly synthetic fixture and wrote Chrome Trace output. This was a synthetic CLI check, not an E2 capture. |

The live capture prerequisite was not satisfied: no selected, valid current
project/configuration or attributable native session was available. No cold
acquisition, target halt/run/reset/download, trace start/stop, or native
session connection was attempted. Native and hardware qualification remain
open; USB presence and offline export success do not close those gates.

## Editor entry and remaining-contract recheck (2026-10-28)

The source VS Code extension now accepts opt-in `perfOutput` and
`perfMaxEvents` attach properties. Its original authenticated proxy path and
default no-recording behavior are unchanged. Fourteen Node host tests, lint,
and package-content checks passed. Independent review also checked both
`path.posix` and `path.win32` semantics without claiming a real IDE session.
A Go contract test ties the schema/runtime event ceiling to the actual
recorder limit and rejects default capture settings.

Synthetic scalar tests verify that character and integer displays remain
opaque strings, including signed/hex text and values outside JavaScript's
exact-integer range. They support the existing display-only behavior around
the MULTI 8 char-bitfield change; they are not captured vendor bitfield output.

The current 8.1.4 PDFs were rechecked directly and their source hashes recorded
in the [adaptation study](ghs-8-adaptation.md). An older unversioned text cache
initially suggested an RTOS command-availability contradiction; the direct
PDF check disproved it before that claim was added to the study. The current
reference does document non-GUI freeze-mode `osatasklist`, but not a stable
task output/identity/core-routing contract. Snapshot options, version/license
restrictions, and `regvalsave` were likewise checked against current PDFs,
not inferred from a GUI view or an old extract.

Full local release acceptance was repeated after these changes: all 20 steps
passed, with no skips. The Windows archive hash remained the value recorded
below; the source-only VS Code extension is not a Windows ZIP payload.
No actual IDE capture, vendor DLL initialization, native session connection,
or target operation was performed.

The outstanding qualification work still requires operator-selected inputs:
an authorized TOML/native session for runtime checks, actual Profiler and
snapshot artifacts with provenance, and RTOS/register fixtures from the
intended mode/target. Documentation, synthetic tests, and existing files do
not supply that authorization or replace those observations.

## Native DAP integration (2026-10-28, uncommitted worktree)

The actor/helper/DAP integration adds production wiring to the earlier offline
checkpoint. The full [MULTI 8 checklist](ghs-8-implementation.md) remains open.
All tests in this section use host fakes or synthetic artifacts; no vendor DLL
initialization, native session connection, or target operation was performed.

| Check | Result and scope |
|---|---|
| Native mechanism and helper | 32 native mechanism tests and 22 helper tests passed under host Python 3.12 and the installed standalone Python 3.8.6. The helper startup/EOF smoke made no open request. |
| Native transport | Windows amd64 race/vet and actual Windows 386 host tests passed. Linux amd64 test compilation passed. Strict protocol, bounded IPC, cancellation, poisoning, and actual reaping are covered. |
| Windows helper lifetime | Fake-process tests confirmed abrupt parent death terminates a helper that ignores EOF; Job handles are not inherited. Inherited source handles retain exclusion until actual helper exit. No vendor code ran. |
| Actor integration | Race/vet and ten repeated trace test runs passed. Tests cover ordering, late replies, same-owner control-lease reacquisition, trace-only isolation, and local uncertainty receipts. |
| DAP/backend integration | Strict custom-request DTOs, capability derivation, ownership/configuration barriers, private-error redaction, local status after faults, late-frontend rejection, and 4096-packet response bounds passed host/race/vet checks. |
| Controller/runtime | Targeted race tests passed for static optional discovery, source exclusion, DLL-byte fingerprinting, source-lease retention, factory/handle lifetime, shutdown/reaping barriers, and configured-probe handle pinning. |
| Full process-chain integration | A real DAP socket, backend, actor, controller, and owned fake-helper process returned a decodable three-packet capture and 100 us function interval. A separate blocked-helper case verified bounded failure, actual reaping, local uncertainty, and continued MBP/threads operation. Ten repeated race runs passed. |
| Independent integration reviews | Fixed a typed-nil failed-start panic, retained failed-start process ownership through actual reaping, and corrected concurrent/idempotent Close paths that could report success after poisoning. Reviewers independently reverified the fixes and deterministic regressions. |
| Python/repository regression | 92 bridge/native/helper, 87 reconnaissance, and 23 policy tests passed under Python 3.12; 54 native/helper tests passed under standalone Python 3.8.6. English-language validation and the strict documentation build passed. |
| Windows bundle | Both native modules are explicit payloads with manifest/source checks. Two builds matched; packaged help, profile import, and native normalization/Chrome export passed synthetic smoke tests. No publication or WinGet install was performed. |
| Read-only capture CLI | The new `trace capture` uses an existing config-bound authenticated proxy and complete DAP lifecycle. Repeated race tests cover live/snapshot envelopes, source/generation/index/count validation, occupied frontends, exclusive outputs, bounded incomplete/malformed replies, conservative cleanup, contradictory close receipts, trailing frames, and missing EOF. Independent read-only review found no material security issue. |
| Final full local acceptance | All 20 steps of `Invoke-WindowsReleaseAcceptance.ps1 -Mode Full` passed with the installed standalone Python 3.8.6 selected; no step was skipped. This includes Go tests/vet/build/race, repository/workflow/license checks, Python tests/compilation, VS Code host checks, strict docs, reproducible packages, and WinGet manifest validation. No package install or publication occurred. |

The final synthetic acceptance archives both had SHA-256
`18df79f33445ad4fed3351235a2d4ea0c83c666df45a2e386a6bab45e09d5d72`.
This identifies the tested uncommitted software bundle, not a signed/tagged
release or hardware-qualified distribution.

Source matching remains an operator declaration. A passed software test does
not prove real MULTI native API behavior, target/core identity, snapshot
loading/licensing, buffer replacement atomicity, observer overhead, or correct
target timings. The separate native/runtime/hardware qualification flags remain
false.

## Trace-consumer import (2026-10-28)

Actual consumer import was verified in the official Perfetto UI,
**v59.1-1e9c61e98**, using its built-in WebAssembly trace processor and only
synthetic data. No vendor capture, target identity, or private debugger output
was supplied to the browser.

- A native Chrome trace produced from the synthetic near-`uint64`-maximum
  fixture imported as three slices: one `target.function_lifetime` interval
  and two `target.packet` instants. SQL reported the interval duration as
  `100000` ns, matching the export's `100` us.
- SQL extraction retained the raw timestamp strings
  `18446744073000000000` and `18446744073100000000` exactly. They were not
  rounded through JavaScript numeric identities.
- A second trace generated through the actual host recorder imported a
  `dap.request` interval and a `dap.event` instant. SQL reported the request
  at `526100` ns for `5543400` ns, matching the exported `526.1` us start and
  `5543.4` us duration; the event occurred at `6069500` ns.
- Both imports displayed the intended process/thread names. UI snapshots and
  screenshots were retained only in ignored local browser evidence.

The initial page load was slow; optional accelerator/analytics/internal-plugin
requests failed, but the built-in processor loaded and executed the SQL checks.
This is consumer compatibility evidence for these fixtures, not a real-editor
capture, arbitrary trace-format qualification, picosecond-resolution viewer
guarantee, or target timing validation.

## Earlier offline/native-mechanism checkpoint (uncommitted worktree)

This earlier checkpoint implemented the offline analysis and native mechanism
portions of the [MULTI 8 checklist](ghs-8-implementation.md), before actor-owned
capture was wired. Actual vendor fixtures, snapshot/session binding,
RTOS/register contracts, and hardware acceptance were not established by
these results.

| Check | Result and scope |
|---|---|
| Full Go tests, vet, and race | Passed with the profile, native decoder/normalizer, and CLI packages included. |
| Profile importer fuzz | Synthetic mapping and CSV fuzz completed without failure; exact integer/decimal, missing-value, header, source-range, and resource-boundary regressions also passed. |
| Native packet/capture fuzz | Synthetic packet and capture fuzz completed without failure; versioned ABI, PID attribution, loss, generation, and clock-epoch regressions passed. |
| Python 3.12 bridge tests | Passed: 38 existing bridge tests and 32 native mechanism fake tests. No vendor DLL was loaded by the tests. |
| MULTI standalone Python 3.8.6 | Passed: 32 native mechanism fake tests and native module byte compilation. |
| Existing reconnaissance/policy tests | Passed: 87 reconnaissance and 23 repository-policy tests. |
| Format, language, and documentation | Go formatting, diff checks, repository language validation, and strict documentation build passed. |
| Windows package | Two builds with synthetic verification metadata produced identical payload hashes. At this checkpoint the offline CLI was included; the then-unwired native helper was not a payload. |
| Profile CLI smoke | Imported the explicitly labeled synthetic CSV fixture, preserved three independent rows, and reported `operator_mapped` with `vendor_qualified:false`. |
| Native CLI smoke | Normalized three synthetic packets into one complete function interval and a Chrome trace, retaining near-`uint64`-maximum timestamps without float conversion. |
| Static installation inspection | Both installed release profiles passed metadata/PE/export/layout checks. No `tm_init`, session connection, or native trace operation occurred. |

The 8.1.4 DLL is x64 with unadorned exports. The 7.1.6d DLL is x86 with
stdcall-decorated exports. Static inspection under the 64-bit interpreter
correctly reports the latter as runtime-incompatible; it does not claim an
actual x86 DLL calling-convention test.

A C host probe against the installed 8.1.4 headers confirmed the 64-byte
information structure, 32-byte/alignment-8 packet, and documented field offsets.
The wall-clock sample matched `seconds | (microseconds << 40)`. GCC 13.2.0
required an annotation-only `PRINTF_CHECK(a,b)` fix in an ignored temporary
header copy. No installed header was edited and no vendor library was linked.
This establishes local header layout only, not native runtime or target clock
qualification.

Independent review of the profile/CLI and native normalization paths found no
actionable P1/P2 issue. Native-mechanism review identified an interrupt path
that could repeat an uncertain native operation; sticky uncertainty handling
and `KeyboardInterrupt`/`SystemExit` regression tests were added before further
integration. Independent re-verification closed that finding: six interrupted
init/connect/disconnect cases retained one native invocation and rejected
retry while preserving the interrupt. The later helper/actor integration has
separate host checks recorded above; actual target recovery remains unverified.

Real GHS export/snapshot fixtures were not found in the inspected installation
and project evidence locations. No running MULTI/TimeMachine process was seen
in the initial read-only inventory. Perfetto trace-processor retrieval also
failed on network/TLS-revocation availability; no unverified executable was
run. The later browser-based synthetic consumer acceptance is recorded above;
real-editor and target acceptance remain open.

## Proxy performance capture (2026-10-27)

The [performance timeline](performance.md) and
[MULTI 8 adaptation study](ghs-8-adaptation.md) were checked on Windows amd64
with Go 1.26.6. The worktree was uncommitted; these results are not release
qualification.

| Check | Result |
|---|---|
| `go test -mod=readonly ./...` and `go vet -mod=readonly ./...` | Passed. |
| `go test -mod=readonly -race ./...` | Passed. |
| `go test -mod=readonly ./cmd/multi-dap ./internal/perf -count=10` | Passed. |
| Go formatting, `git diff --check`, and executable build | Passed. |
| Recorder framing fuzz, 5 seconds | Passed: 195,957 executions without a failure in the recorded run. |
| Authenticated proxy integration | Passed: original DAP bytes preserved, delayed responses correlated, failure outcomes retained, and no additional backend actions. |
| Output security | Passed: exclusive creation, no overwrite, Windows protected owner-only DACL, device/alternate-stream rejection, and payload-exclusion tests. |
| Python 3.12 host checks | Passed: 38 bridge, 87 reconnaissance, and 23 repository-policy tests. |
| Repository language policy and strict documentation build | Passed. |
| Windows package tests | Passed: payload allowlist validation and two reproducible builds with synthetic verification metadata. |
| Independent code/security review | No actionable P1/P2 finding; targeted race tests and vet also passed. |
| Metadata-only MULTI release inspection | Identified 8.1.4 and 7.1.6d from installation metadata without executing MULTI or contacting a target. |

A synthetic capture contained six request spans, two observed events, and four
track/process metadata records. One deliberately failed DAP response was
recorded without marking the successful transport/capture incomplete. This
checks the distinction between a request outcome and observation health; it is
not an actual debugger workload or target performance result.

Native Perfetto trace-processor acquisition was attempted from official pinned
sources, but timed out before obtaining a verified executable. No partially
downloaded binary was executed. Actual Perfetto import/UI acceptance and real
editor capture remain unverified. Chrome JSON structure and timestamp semantics
are covered by host tests only.

No MULTI session, target acquisition, trace start/stop, halt, resume, reset,
download, or flash operation was performed. The existing target capability
matrix is unchanged. No new vulnerability-database scan, public release, or
hosted workflow is claimed by this verification.

## MULTI 8 compatibility evidence

The following compatibility evidence was collected on 2026-09-13.

| Environment | Check | Result |
|---|---|---|
| MULTI IDE 8.1.4 + Compiler 2024.1.4 / Probe 6.6.8 | Read-only `p00_env` with `GHS_IDE_USE_PYTHON3=1` | Python 3.8.6 loaded; `ghs_debugger` and `ghs_debugger_api` imported; no target contact. |
| Same installation | Read-only `p00_env` with `GHS_IDE_USE_PYTHON3=0` | Python 2.7.3 loaded; required debugger modules imported; no target contact. |
| Same installation | `bridge.py` under Python 3.8.6, loopback MBP handshake and `state` request | Passed; returned the expected `session_not_open` error without opening a target. |

These checks establish interpreter and bridge-process compatibility only. They do
not qualify C++20, RH850/U2B24, breakpoints, execution, or any other target
operation on the newer toolchain.

## Local release-candidate evidence

The following release-candidate evidence was collected on 2026-09-13; it is
separate from the 2026-10-27 performance checks.

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
