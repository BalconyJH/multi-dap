# Developer Guide

## Repository layout

| Path | Responsibility |
|---|---|
| `cmd/multi-dap/` | CLI lifecycle, daemon control, and stdio proxy. |
| `cmd/dwarfspike/` | Source-tree-only DWARF diagnostic; it prints input paths and is excluded from release archives. |
| `internal/core/` | Session Actor, state, handles, source identity, breakpoints, stop arbitration, and inspection models. |
| `internal/daemon/` | Production wiring between the actor, DAP server, bridge, and control plane. |
| `internal/dap/` | DAP framing, lifecycle, request validation, and response/event types. |
| `internal/mbp/` | MULTI Bridge Protocol v1 messages and codec. |
| `internal/multi/` | Strict parsers and MULTI operation runners. |
| `bridge/` | Python 2.7-compatible mechanism bridge and Python 3 host tests. |
| `recon/` | Hardware reconnaissance harness; never part of the release package. |
| `editors/` | VS Code source extension and CLion configuration contract. |
| `scripts/` | Deterministic packaging and release acceptance. |
| `tools/` | Repository-language and release-changelog policy validators. |
| `packaging/` | Files copied into the Windows release archive. |

## Platform tiers

Windows amd64 is the production tier because Green Hills MULTI and the bridge
launcher are Windows-specific in this implementation. Non-Windows Go builds
and host tests validate portable packages and explicit unsupported paths; they
do not establish a usable daemon on Linux or macOS.

## Go checks

Use the exact toolchain declared in `go.mod`:

```powershell
$unformatted = gofmt -l .
if ($unformatted) { $unformatted; throw 'Go files are not formatted' }
go test -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -race ./...
go build -mod=readonly -trimpath -buildvcs=false ./...
```

Run `gofmt -w` only on files you intentionally changed.

Scan reachable calls against the current Go vulnerability database with the
pinned tool used by CI and release builds:

```powershell
go run -mod=readonly golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
```

This check requires network access to the Go module proxy and vulnerability
database. A prior successful result becomes stale when either code or the
database changes.

## Python host checks

The production bridge runs under MULTI's Python 2.7. Host tests run under
Python 3.12 and fake the Green Hills modules:

```powershell
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest bridge.test_bridge
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest discover -s recon/tests
```

These tests prove compatibility logic, framing, sanitization, parsing, and
bounded behavior. They do not open MULTI or connect to hardware.

To byte-compile with the actual MULTI interpreter, pass its path to release
acceptance:

```powershell
.\scripts\Invoke-WindowsReleaseAcceptance.ps1 -MultiPython C:\path\to\multi\python\python.exe
```

No probe is executed by this check.

## VS Code checks

The extension has no npm runtime dependencies:

```powershell
Push-Location .\editors\vscode
npm test
npm run lint
npm run package-contents
Pop-Location
```

The extension source is MIT-licensed. Its package remains marked `private`
because VS Code Marketplace publication has not been approved or configured.

## Documentation

The documentation site uses the repository's strict Zensical configuration:

```powershell
uv run --project docs --locked --python 3.12 zensical build --clean --strict
```

`docs/uv.lock` pins the complete documentation environment; the docs project
has `package = false` and is not an installable `multi-dap` Python package.
Warnings, invalid links, and invalid anchors fail the documentation job. All
repository text must remain English; examples may contain non-English Unicode
only through ASCII escape sequences when a test explicitly verifies encoding
behavior.

## Repository policy checks

Run the pinned workflow linter and the host-side repository policy tests with:

```powershell
go run -mod=readonly github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest tools.test_validate_repository_language tools.test_validate_release_changelog
uv run --offline --no-python-downloads --no-project --python 3.12 -m tools.validate_repository_language --repository .
```

The language policy scans tracked and untracked non-ignored repository text,
requires strict UTF-8, rejects non-Latin-script letters, and rejects Unicode
control/format/private-use characters other than tab and normal line endings.
The changelog policy requires a unique version heading and link before a stable
release; prerelease builds may use the unique `Unreleased` section.

## Release acceptance

Fast mode is useful during development:

```powershell
.\scripts\Invoke-WindowsReleaseAcceptance.ps1 -Mode Fast
```

Full mode is the release gate:

```powershell
.\scripts\Invoke-WindowsReleaseAcceptance.ps1 -Mode Full -Version 0.1.0-rc.1
```

Full mode adds the race detector, optional MULTI Python 2 compilation, and two
independent package builds whose archive digests and internal manifests must
match. Both modes also verify that every GitHub Action is pinned to the reviewed
commit and version in `.github/actions-lock.json`. Inspect the final
`RELEASE_ACCEPTANCE_RESULT` JSON for skipped checks.

## Hardware work

Read `recon/README.md` before touching the harness. One operator owns the
hardware at a time. Never print or commit raw target data, and never infer a
hardware capability from a host fake. Record only the sanitized evidence
defined by [M0 findings](m0-findings.md).
