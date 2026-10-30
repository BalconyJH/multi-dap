# Verification Evidence Index

This index records sanitized, local verification evidence. Each section
defines its own scope. It is an audit aid, not a claim that a public release,
hosted workflow, or hardware qualification has occurred.

## Completion audit and bounded host benchmark (2026-10-30)

Two independent read-only audits checked the M0-M5 and native/M6 contracts
against the current implementation and evidence. They found no further safe
host-side feature gap after the process-diagnostic work. They did not find
the overall objective complete: target measurements and verified primitives,
vendor-produced fixtures and source identity, actual GUI acceptance, and
reviewed hosted publication remain unproven.

The retained candidate hash and all 367 files in its local source inventory
matched at audit time. Read-only remote checks still found the same committed
revision and failed hosted CI run, no branch protection/rulesets/environments,
and no releases. No remote settings or Git state were changed. A search of
the selected MULTI installation found no CSV, snapshot, or candidate trace/export
files matching `.csv`, `.dsnap`, `.prof`, `.trace`, or `.tmd`; it was not a
machine-wide search or proof that no such fixture exists elsewhere.

The existing `BenchmarkRecorderRequestResponse` was then run five times with
two-second samples on Windows amd64, Go 1.26.6, and an AMD Ryzen 7 8745H host.
The serial benchmark reported 3038-3171 ns per small request/response pair
(median 3091 ns), 808 B/pair, and 20 allocations/pair. It fixes the clock,
reuses one retained event slot, and measures parser/correlation work only.
It excludes clock-call cost, growth/overflow, concurrent forwarding, artifact
export, editor traffic, and target effects. This does not close real-editor
end-to-end or hardware observer-overhead qualification.

The raw benchmark is `tmp/perf-host-benchmark-20261030.log`. No production
code changed during this audit or measurement, and no vendor runtime or target
was accessed.

## Host process diagnostics and Linux race validation (2026-10-30)

A completion audit found an unimplemented host-side architecture requirement:
possible leftover processes were supposed to be reported, but `doctor` only
performed the static `check` operation. The new explicit diagnostic fills that
gap without replacing existing configuration checks or granting process/target
control.

| Check | Observed result and boundary |
|---|---|
| Windows inventory | Toolhelp snapshots and limited-query process image/timing calls identify installation-path matches and unresolved known-name candidates. Bounds, strict paths/UTF-16, cancellation, inaccessible identities, and snapshot/PID changes have host tests. A real owned harmless test child established its expected PID, parent PID, image path, and EOF cleanup. No vendor process was launched. |
| Production CLI smoke | A freshly built CLI inspected an inert synthetic installation containing itself. Default output omitted image paths; explicit `--include-paths` identified its actual executable. The real machine snapshot remained `partial` because some process identities were inaccessible. Ownership/probe-availability flags remained false. No daemon, native collector, or target was used. |
| CLI and startup integration | Twelve new regression groups cover static-output compatibility, explicit path opt-in, bounded/sanitized output, count-only stderr, configuration conflicts, startup ordering, cancellation, and reuse/child scan exclusion. Default `doctor`/`check` and the strict three-field ready receipt remain unchanged. Windows focused race checks passed twice. |
| Independent review | A read-only security review found no material issue in the limited-query handles, path/coverage policy, privacy opt-in, startup ordering, cancellation, or preserved control-resource cleanup. Independent focused tests, race checks, and vet passed. Real PID reuse, clock jumps, and protected-process failures were not deliberately induced; failure policies use fake-snapshot tests. Stalled kernel-call timing remains unqualified and the deadline remains cooperative. |
| Actual Linux race | WSL Ubuntu 24.04 with Linux Go 1.26.6 passed full uncached `go test -mod=readonly -race -count=1 ./...` and `go vet -mod=readonly ./...`, including the new inventory package and CLI integration. This closes the earlier local Linux race-tooling gap, not the hosted CI gate or Linux production support. |
| Isolated C toolchain | GCC 13.3.0 and 29 official Ubuntu dependency packages were downloaded and extracted into an owned user-cache directory, not installed. Package SHA-256 values matched APT metadata; InRelease signatures and Packages hashes verified against the Ubuntu archive keyring. The system package-state hash was unchanged. Evidence uses the cached signed indexes, not a fresh repository-index update. |
| Final Windows/editor regression | All 21 full Windows acceptance checks passed, with zero failures/skips. The official VS Code Insiders `1.145.0-insider` Extension Host again passed all five synthetic cases with the new CLI; each retained one authenticated proxy. Owned editor/fixture processes and control files were absent after cleanup. |
| Workflow/security checks | actionlint passed and a fresh govulncheck scan found zero reachable vulnerabilities. The same one imported-package and thirteen required-module findings had no called vulnerable symbols. |

Evidence is retained in `tmp/devstatus-acceptance-20261030-08.log`,
`tmp/hostdiag-linux-race-20261030-07.log`,
`tmp/vscode-editor-acceptance-AyotWK/result.json`, and its adjacent
`verification.json`. The isolated-toolchain provenance and scripts are under
`.hwscratch/linux-race/`; the actual CLI scan receipt is
`.hwscratch/host-diagnostics-20261030/receipt.json`.

Both accepted package builds and the retained candidate have SHA-256
`99656c244e920529b3d11a43ca6c2b7c275fc4c57e99bfa63bdce3ce54701592`.
The new retained archive is
`tmp/devstatus-candidate-20261030-03/multi-dap-0.1.0-rc.1-windows-amd64.zip`.
It supersedes the earlier local candidate for these host changes, without
overwriting that candidate or its evidence. The provenance commit remains
null; no commit, push, tag, publication, or remote setting change occurred.

This remains host-software evidence. Process snapshots neither prove an
available probe nor authorize killing, adopting, reconnecting, or operating a
target. Current target configuration/authorization, vendor fixtures, visual
editor acceptance, and reviewed hosted publication remain separate gates.

## Same-session native editor capture and CI corrections (2026-10-30)

This continuation implements the missing native editor capture surface and
repairs the concrete Windows/Linux failures from hosted CI run `40999557048`
at committed revision `f2db9f1fda297af2c7c008133a10c21b469b5b78`.
No source was committed or pushed, and hosted CI has not rerun these changes.

| Check | Observed result and boundary |
|---|---|
| Native editor path | The VS Code command uses only the existing configured DebugSession. A local `trace capture-editor` helper statically validates configuration, reserves exclusive private output, forwards bounded native status/open/poll/read/close requests, validates the capture, and requires confirmed native close plus terminal EOF before successful output. It does not acquire a daemon, create a frontend, start/stop collection, load a snapshot, or execute the target. |
| Actual Extension Host | Official Insiders `1.145.0-insider`, commit `f8c98dc6a2efcf8e86977254e1d4cf56b2542dbc`, passed five synthetic cases: default attach, cold reuse, warm reuse, host capture, and native interval capture. Each case used exactly one authenticated proxy. The native case made no readiness probe or second initialize/attach and completed ordinary threads/evaluate afterward. |
| Native artifact | The production Go normalizer accepted three synthetic packets and one 100-microsecond span. Generation `18446744073709551614` remained an exact decimal string across JavaScript IPC. This is not a vendor-produced packet fixture or source/core qualification. |
| Native failure boundaries | All 48 Node tests passed, including 25 native-capture regressions. Host coverage includes bounded framing, nonce/request identity, exclusive output, source/generation/interval substitution, close/EOF validation, cancellation, session replacement, pending late replies, and retained reconciliation state. The original standalone capture lifecycle remains separately tested. |
| Independent native review | Review reproduced a JS/Go close-receipt disagreement that could forget a reconciliation tombstone. The fix rejects contradictory last-receipt flags and retains uncertainty on any failed native attempt; independent regression review passed and a second capture remained blocked until an explicit clean status check. No other material security finding remained in the reviewed scope. |
| Windows hosted failure | Aggregate-parser fixture mutations now normalize checkout line endings and exercise both LF and CRLF, with an explicit assertion that each malformed mutation changed its input. Parser admission was not relaxed. |
| Linux hosted failures | Dead-reservation recovery uses an owned reaped process rather than an overflowing fake PID. Production liveness checks reject values beyond each OS PID width and retain ambiguous reservations. Full positive warm-start coverage remains Windows-specific; non-Windows tests require rejection of POSIX ELF identity before `open` and cleanup of the owned fake bridge. |
| Independent CI review | A read-only review found no actionable issue in the PID bounds, reservation retention, line-ending fixtures, or split warm-start tests. Linux remains a host-test tier, not a supported production daemon platform. |
| Actual Linux host validation | WSL Ubuntu with Linux Go 1.26.6 passed uncached full `go test -mod=readonly -count=1 ./...` and `go vet -mod=readonly ./...` after the Go editor helper changes. The successful run used the existing module cache without network fetches. Linux race was not run because this environment lacks a C compiler; Windows full acceptance covers race separately. |
| Workflow and vulnerability checks | Locally rebuilt actionlint v1.7.12 passed. A fresh govulncheck v1.1.4 database scan found zero reachable vulnerabilities, with one noncalled imported-package and thirteen noncalled required-module findings. The audit does not cover Rust dependencies. |
| Final Windows acceptance | After the close-receipt correction, all 21 full release-acceptance checks passed with zero failures or skips. This includes full Go/race, 98 bridge/native/helper tests, 87 reconnaissance tests, 48 Node tests, nine Zed tests plus lint/Wasm build, documentation/policy checks, standalone MULTI Python compilation, reproducible ZIPs, and WinGet manifest validation. |

The final five-case editor report is
`tmp/vscode-editor-acceptance-hkXvJP/result.json`. Its isolated acceptance uses
the production proxy/DAP transport with a synthetic backend and inert vendor
sentinel. It does not initialize a vendor native DLL or use hardware. Visual
UI assessment, real native sessions, observer overhead, and target acceptance
remain open.

Linux evidence is `tmp/native-editor-linux-go1266-cached.log`; earlier failed
shell/network attempts were not counted as passes. The workflow/vulnerability
logs and tool provenance are under `tmp/*20261030-04*`. Both audit binaries
reproduced byte-for-byte from their versioned cached source modules, and the
module directory/ZIP hashes matched cached checksum-database records. These
are local source builds, not upstream release executables. Full dependency
graph verification was unavailable offline because unrelated test-dependency
metadata was absent; it is not claimed as a successful check.

The final full log is `tmp/devstatus-acceptance-20261030-06.log`. Both package
builds and the retained local candidate have SHA-256
`46b69b04ca2c175fde5ee3add81ad726dbe29c26264ce9efb41795febb480978`.
The retained archive is
`tmp/devstatus-candidate-20261030/multi-dap-0.1.0-rc.1-windows-amd64.zip`;
its adjacent checksum and internal payload/provenance records identify this
uncommitted candidate. The provenance commit is deliberately null. This is
not a reviewed tagged release, a hosted CI result, a public publication, or
hardware qualification.

### Current bridge loader in the embedded runtime

A separate bounded no-target run used the current `bridge.py` and exact
sibling `mbp_wire.py` through the production MULTI 8.1.4 launch specification.
It validated MBP v1, sent only `state`, received `session_not_open`, and closed
the connection. There was no `open`, debugger-factory request, native collector
request, or target operation in the transcript.

The actual owned Python service loaded the selected installation's
`python38.dll`. Its loaded-module path and file SHA-256 were recorded, and the
file contained the Python `3.8.6` version constant. Six owned Job members were
observed at the checkpoint. `mpythonrun` exited naturally after EOF; remaining
owned services required explicit Job cleanup. Final snapshots found zero
remaining observed owned PIDs or owned listeners and no unattributed related
vendor process. A separate system console-host descendant was outside the
Job and disappeared without a targeted action.

Machine-wide `Win32_ProcessStartTrace` subscription was denied. The scoped
receipt therefore records `bridge_runtime_pass` with
`forensic_audit: unavailable`, `process_event_audit_available: false`, and
`transient_activity_excluded: false`. Process/module snapshots are not proof
that every transient activity was absent. Earlier harness failures and the
original conservative `inconclusive` receipt remain retained; the supplemental
receipt narrows its claim rather than overwriting them.

Private evidence is under `.hwscratch/embedded-python-smoke/`, including
`scoped-receipt-20261030T160321.json`, the process/module audit, and the original
checkpoint/result/audit receipts. The wire companion SHA-256 is
`99a8d4c09fc89294ff4b6ecfb63c168230c6fdfdb158759afa95356f92a803df`.
These observations verify the current loader and pre-open MBP behavior only.
They do not close resident-loop MULTI calls, differential polling, acquisition,
native-session, or hardware qualification.

## Editor integration and bridge decomposition (2026-10-30)

This continuation closes the mechanism-helper extraction and Zed source-adapter
implementation items, and adds actual VS Code Extension Host evidence. It
does not close the remaining target, native-input, Zed GUI, or publication gates.

| Check | Observed result and boundary |
|---|---|
| Legacy bridge helper | The exact sibling `mbp_wire.py` owns Python 2/3 conversion and bounded framing. Loader tests reject missing/corrupt helpers and ignore poisoned cwd/module-cache candidates. Host Python 3.12 ran 98 bridge/native/helper tests; standalone MULTI Python 3.8.6 ran 44 bridge tests and Python 2.7 ran six wire/loader tests. No embedded debugger or target was opened. |
| Frame boundary regression | Exact-limit payloads followed by split CRLF are accepted consistently with a single-read delimiter. Extra payload, incomplete framing, and malformed UTF-8/JSON remain rejected. |
| Companion distribution | Archive copy/allowlists, internal hashes, source equality, publication/staging comparisons, installed companion validation, and embedded-runtime compilation include the new helper. An independent security review found no material issue in this scope. |
| Actual VS Code host | Official VS Code Insiders `1.144.0-insider`, commit `58173d0ebc1838579e3c331e0a47e365904a6456`, loaded the production extension in an isolated profile. Four sessions passed through the real authenticated control server, production proxy, and DAP server with a synthetic backend. |
| Attach and explicit reuse | Default attach opened one authenticated proxy and made zero readiness probes. Cold and warm reuse each opened one proxy and made one readiness probe. Initialize/attach/configurationDone, threads, stack, scopes, variables, evaluate, and disconnect passed. This exercised reuse of a ready synthetic daemon, not actual MULTI acquisition. |
| Capture and cleanup | The fourth real-host session produced twelve completed requests and eighteen trace events, zero framing errors/unfinished requests, and `capture_complete:true`. Its metadata was `finalization_boundary:"disconnect_response"` and `proxy_exit_observed:false`. Cleanup found no fixture control records or acceptance-owned editor/fixture/proxy processes. |
| Editor-discovered correction | The original capture file remained empty when VS Code terminated the adapter after disconnect while stdin stayed open. The proxy now syncs/closes the capture before forwarding the complete matched successful response. A real child-process kill regression confirms the file survives immediate termination; failed/ambiguous responses do not finalize early. Later bytes still forward, and a durable artifact is never rewritten. |
| Zed source extension | The adapter manifest, strict schema, SDK 0.7.0 hooks, Windows stdio admission, and empty DAP attach body are implemented. Nine native tests, clippy, formatting, and a locked offline `wasm32-wasip2` release build passed. Real Zed installation and GUI/target operation were not exercised. |
| Zed dev-builder correction | Review found that installing Rust 1.95.0 did not select it for Zed's plain `cargo` invocation. The extension-local toolchain file now pins 1.95.0 and the Wasm target. `rustup show active-toolchain` confirmed the directory override; plain `cargo build` from that directory passed. |
| Final release gate | The updated full acceptance script passed all 21 checks, with zero failures/skips, including the new offline Zed source/Wasm check. actionlint v1.7.12 passed for the updated CI, release, publication, and package-manager workflows. |
| Go vulnerability audit | A fresh govulncheck v1.1.4 live-database scan of the final Go sources found zero reachable vulnerabilities. One imported-package and thirteen required-module findings had no called vulnerable symbols. This scan does not cover the separate Rust dependency graph. |

Both final Windows package builds had SHA-256
`4a78c765e3106527efebc2ff243e9a63c9bf08674535353204d3294855bbb299`.
The separately compiled Zed Wasm artifact had SHA-256
`4d479367c0fd73b823320df3f49e50252d57309f88b188188a06876ebabe50c6`;
it is not part of the Windows ZIP or a published extension.

Ignored local evidence is in `tmp/devstatus-acceptance-20261030-03.log` and
`tmp/vscode-editor-acceptance-7ybd8l/result.json`. The editor fixture uses
synthetic data and an inert vendor-executable sentinel; no vendor runtime,
native DLL, hardware access, image preparation, or target execution occurred.
The actual editor checks are not a visual UI assessment, an observer-overhead
measurement, or a native trace qualification.

## Development-status completion pass (2026-10-30)

This pass advances the host implementation and reconciles the published status
with actual production wiring. The milestone exit gates in
[capabilities](capabilities.md#open-hardware-and-product-work) remain open where
they require target, native-runtime, editor GUI, or publication evidence.

| Change | Verification and boundary |
|---|---|
| Queued execution ownership | Deterministic actor regressions cover continue, pause, next, and step-in across detach, release, and same-owner reacquisition. Dispatch checks the original control generation and current admission gate. Already dispatched operations retain their existing lifecycle contract. |
| Breakpoint mutation and cleanup | Direct and source-resolved replacements require the original control generation and stopped epoch. Empty replacements are also stopped-only. A newer running/stop observation prevents queued Clear; pending records survive for the next natural stop. |
| Post-execution observations | Stopped-only M3/M5 and breakpoint work refresh state after execution dispatch. Running, changed-epoch, refused, and all five unknown lifecycle states cannot reuse the old stopped snapshot. Unknown states retain the freshness requirement and do not create an automatic cleanup retry loop. Host regressions cover bounded refresh, retained records, and later recovery. |
| M3 frame and snapshot integrity | Read-only selected-frame guards precede locals, evaluation, and aggregate expansion. Stack rows for callers remain available without a falsely attributed live PC. Duplicate indices/multiple selections and invalid off-page source mappings fail before paged handle allocation. |
| VS Code acquisition | All 23 Node host tests, lint, and package-content checks pass. Tests cover local workspace/path admission, explicit cold/warm arguments, success ordering, sanitized failures, and real harmless child-process timeout/output overflow. Default attach remains proxy-only; no GUI or target acquisition was exercised. |
| Architecture scan | Nested Go modules, including private source snapshots, are excluded consistently with `go test ./...`; ordinary adapter sources remain checked. |
| Independent reviews | M3 and VS Code security reviews found no material issue. Actor review identified the unknown-state freshness defect; the fix passed independent repeated race review before final acceptance. |
| Full local release acceptance | The final run after the actor correction passed all 20 checks, with zero failures and zero skips. It includes Go test/vet/build/race, Python host checks, standalone MULTI Python 3.8.6 compilation, editor/policy checks, strict documentation, reproducible packages, and WinGet manifest validation. |
| Additional security/workflow checks | actionlint v1.7.12 passed. govulncheck v1.1.4 completed a live-database scan with zero reachable vulnerabilities; it reported one imported-package and thirteen required-module findings without called vulnerable symbols. The initial module-proxy requests timed out; exact-version sources already in the local module cache were built and used for these checks. |

Both final Windows acceptance builds had SHA-256
`13510dddab901d5816f87f410af4cd850c4be5a1ccca035f12f8a6ae19214ec5`.
This identifies the locally tested uncommitted candidate, not a published or
hardware-qualified release. The ignored full log is
`tmp/devstatus-acceptance-20261030-02.log`.

No target operation, MULTI acquisition, vendor DLL initialization, package
installation, or publication was performed. In particular, the new read-only
frame guards and state refresh ordering still need real MULTI timing and
editor regression. All multicore execution, including continue and pause,
remains disabled until a production execution-domain executor is qualified.

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
