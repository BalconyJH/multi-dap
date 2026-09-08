# Changelog

All notable changes to `multi-dap` will be documented here. The project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) once its first public
version is tagged.

## Unreleased

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
