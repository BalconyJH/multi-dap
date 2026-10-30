# Changelog

All notable changes to `multi-dap` will be documented here. The project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) once its first public
version is tagged.

## Unreleased

### Added

- Bounded, read-only Windows host-process diagnostics through
  `doctor --processes`, with explicit private-path opt-in and count-only startup
  notices. Process observations never establish ownership/probe availability
  or trigger termination, adoption, or target recovery; static checks and
  editor ready receipts remain unchanged.
- Experimental VS Code native-buffer capture through the existing configured
  debug session. A bounded Go editor transport reuses typed capture validation,
  private output creation, and conservative native cleanup; cancellation and
  late replies retain reconciliation state instead of starting a second
  frontend or implicitly stopping collection.
- A registered local Windows Zed source extension with a strict attach schema,
  authenticated stdio proxy, optional host capture, pinned Rust SDK/lockfile,
  host tests, and Wasm build checks. Real Zed GUI/target acceptance remains open.
- A shared Python 2/3 `mbp_wire.py` mechanism helper for text conversion,
  frame encoding/decoding, and bounded reading. The selected bridge loads only
  its exact sibling source; deterministic packages and publication/install
  checks bind the required companion.
- Opt-in real VS Code Extension Host acceptance with isolated editor data,
  the production authenticated proxy and DAP server, and a synthetic backend.
  It exercises attach, explicit daemon reuse, inspection, disconnect, and
  private host and native interval capture without starting a vendor runtime.
- Optional explicit cold/warm daemon acquisition in the VS Code source
  extension. Bounded `ensure` completion precedes the authenticated proxy;
  default attach remains proxy-only and warm never falls back to cold.
- Explicitly mapped, bounded Profiler CSV analysis with exact decimal/count
  ranking, source identity and line ranges, missing-value preservation, and
  input/mapping hashes. Real vendor-format qualification remains open.
- Versioned TimeMachine packet decoding and offline normalization, including
  PID/previous-PID attribution, discontinuities, generation/clock-epoch
  separation, and picosecond-only Chrome trace export.
- A separate Python 3.8+ native TimeMachine mechanism with static ABI/export
  inspection, pinned DLL loading, explicit session selection, bounded
  callbacks, and guarded native operations.
- Experimental actor-owned native trace collection through seven custom DAP
  requests, with a bounded helper event loop, process deadlines, frontend and
  source-generation fencing, explicit start/stop admission, local recovery
  receipts, and Windows Job/inherited-lease lifetime protection. Source
  association is operator-declared; native-runtime and hardware qualification
  remain open.
- A read-only `trace capture` client for an already running config-bound daemon.
  It captures one validated packet interval through the existing authenticated
  DAP path, explicitly disconnects, and writes a private native-capture file
  without starting collection or performing target execution operations.
- Opt-in, config-bound proxy performance capture with bounded Chrome Trace JSON,
  request latency summaries, loss/health counters, payload redaction, and
  exclusive private output files. This observes the host DAP path, not target
  CPU execution.
- Optional VS Code attach properties `perfOutput` and `perfMaxEvents` for the
  existing host-timeline proxy, with explicit private-output selection,
  variable-substitution validation, and no default recording.
- A MULTI 8 adaptation study covering the 7.1.6d-to-8.1.4 TimeMachine header
  contract differences, native profiling candidates, and deprecated trace
  interfaces.
- MULTI IDE 8.1.4 / Compiler 2024.1.4 compatibility documentation and a
  configuration-bound embedded Python runtime selector. Python 3 is now the
  preferred `mpythonrun` runtime; Python 2.7 remains an explicit fallback.
- Metadata-only MULTI IDE release detection, optional `multi.version`
  constraints, and configuration-bound observed versions. Executing the
  installation's `gversion.exe` requires an explicit executable-probe opt-in.
- Windows launcher environment pinning and no-target smoke evidence for both
  MULTI embedded interpreters.
- Dual-runtime reconnaissance helpers with byte-oriented socket framing and
  explicit embedded-interpreter selection.
- Repository-local DAP and native MULTI socket workflows with operation-specific
  authorization, bounded response waits, and private evidence handling.

### Fixed

- Process-liveness checks reject IDs that exceed the operating system's PID
  width before invoking system APIs, retaining ambiguous startup reservations
  instead of probing a truncated PID or Unix process group.
- Hosted CI regressions now exercise malformed aggregate fixtures under both
  LF and CRLF and separate portable acquisition-identity checks from the
  Windows-only positive warm-runtime path.
- Host captures finalize durably before a matched successful disconnect reply
  reaches an editor that may immediately terminate its adapter. Explicit
  finalization metadata distinguishes that boundary from observed proxy exit;
  later bytes are still forwarded and the durable file is never rewritten.
- Maximum-size MBP frames with CRLF now preserve their acceptance boundary
  when CR and LF arrive in separate socket reads.
- Queued execution and source-breakpoint mutations recheck their original
  control-lease generation before dispatch. Source mutation and pending
  cleanup require the original stopped epoch; stopped-only work refreshes
  state after execution invalidates the previous observation.
- VS Code rejects remote/virtual workspaces, control characters in paths,
  and Windows paths that depend on the current drive before starting an
  adapter or acquisition process.
- Top-frame inspection verifies MULTI's selected frame before reading locals,
  evaluating expressions, or expanding children. Caller frames remain visible
  in stack snapshots without receiving the live program counter.
- Stack parsing rejects duplicate indices and multiple selected frames.
  Complete source mapping is validated before paging and handle allocation,
  including invalid sources outside the requested page.
- The Go architectural contract scan respects nested module boundaries, so
  private source snapshots do not produce false ownership violations.
- Development status now explicitly includes continue/pause in the existing
  multicore execution gate; the disabled domain has never qualified these
  operations merely because group semantics are described.
- MULTI component inventories accept comma-separated PID/core aliases without
  treating them as program bindings, including observed 8.1.4 name-first
  ordering. Ambiguous display tails remain intact and explicitly marked;
  exact routed ELF identity checks remain mandatory.
- Evaluation accepts the observed single-line bare decimal integer response
  from MULTI 8.1.4 without numeric conversion, inferred type/address, or
  broader locals parsing. Diagnostics and unsupported text still fail closed.
- Bridge inspection accepts Python 2 byte-string component names while
  preserving strict route validation.
- Direct Windows `serve` repairs invalid stdout and stderr console handles
  before starting the MULTI bridge.
- Executable containment checks resolve ancestor directory links before
  accepting a configured path inside the MULTI installation.
- Reconnaissance timeout cleanup remains bounded, and the socket client writes
  UTF-8 JSON under both supported Python runtimes.

## [0.1.0] - 2026-09-08

### Added

- A Go daemon and Debug Adapter Protocol frontend for Green Hills MULTI.
- A Python 2.7 bridge that confines Green Hills API calls to the MULTI runtime.
- Cold and strict warm session acquisition, an authenticated local control
  plane, semantic configuration binding, and a stdio proxy for editor
  integrations.
- Source breakpoints, multicore thread and stack inspection, variables,
  evaluation, read-only memory, and RH850 disassembly within the documented
  capability boundaries.
- A first-class CLion Cidr DAP configuration guide and packaged profile
  contract using the config-bound proxy, plus a software-tested VS Code source
  extension.
- Deterministic Windows amd64 release archives with payload checksums.
- Host-side Go, Python, editor, and package acceptance tests.
- An English documentation site covering architecture, capabilities,
  development, operations, security, verification evidence, and release
  governance.
- A Windows distribution guide covering verified archives, manual lifecycle,
  WinGet activation, and the explicit uv/Homebrew boundary.
- MIT licensing for the public source repository and packaged binary.
- SHA-pinned CI, scheduled vulnerability auditing, protected candidate and
  publication workflows, provenance attestations, immutable-release state and
  asset revalidation, and release incident procedures.
- WinGet 1.12 candidate-manifest generation and validation for the portable
  Windows archive.
- A post-release WinGet staging workflow that verifies an immutable release,
  regenerates manifests, and performs clean-host install and uninstall checks
  without submitting to a package registry.

### Fixed

- Actor shutdown now closes command admission before queuing the close command,
  and public request waits observe actor termination so queued or inflight
  calls cannot remain stranded after the loop exits.

### Release constraints

- The remaining hardware qualification items are documented in
  [docs/capabilities.md](docs/capabilities.md) and
  [docs/m0-findings.md](docs/m0-findings.md).

[Unreleased]: https://github.com/Tacrolimus/multi-dap/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/Tacrolimus/multi-dap/releases/tag/v0.1.0
