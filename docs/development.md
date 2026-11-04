# Developer Guide

## Repository layout

| Path | Responsibility |
|---|---|
| `cmd/multi-dap/` | CLI lifecycle, daemon control, and stdio proxy. |
| `cmd/dwarfspike/` | Source-tree-only DWARF diagnostic; it prints input paths and is excluded from release archives. |
| `internal/core/` | Session Actor, state, handles, source identity, breakpoints, stop arbitration, and inspection models. |
| `internal/daemon/` | Production wiring between the actor, DAP server, bridge, and control plane. |
| `internal/hostprocess/` | Bounded, advisory Windows process snapshots; no ownership or target-control policy. |
| `internal/dap/` | DAP framing, lifecycle, request validation, and response/event types. |
| `internal/mbp/` | MULTI Bridge Protocol v1 messages and codec. |
| `internal/multi/` | Strict parsers and MULTI operation runners. |
| `bridge/` | Python 2.7/3.8-compatible mechanism bridge and Python 3 host tests. |
| `recon/` | Hardware reconnaissance harness; never part of the release package. |
| `editors/` | VS Code and Zed source extensions, synthetic editor acceptance, and CLion configuration contract. |
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

The production bridge runs under MULTI's embedded Python 3 by default. Its
source remains compatible with Python 2.7 for the explicit legacy fallback.
Host tests run under Python 3.12 and fake the Green Hills modules:

```powershell
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest bridge.test_bridge bridge.test_timemachine_native bridge.test_timemachine_helper
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest discover -s recon/tests
```

`bridge/mbp_wire.py` contains the shared Python 2/3 text conversion, MBP frame
encoding/decoding, and bounded reader mechanisms. `bridge.py` loads its exact
sibling source using the script identity supplied by MULTI's script executor.
It does not use the project working directory, import search path, a cached
module, or bytecode as a fallback. Keep both files together; protocol and
MULTI-operation dispatch remain in `bridge.py`, while debugger policy remains
in Go. Architectural policy checks cover both legacy-runtime files.

These tests prove compatibility logic, framing, sanitization, parsing, and
bounded behavior. They do not open MULTI or connect to hardware.

`bridge/timemachine_native.py` and `bridge/timemachine_helper.py` are separate
Python 3.8+ modules. Their tests use fake native functions; importing them,
starting the helper without an open request, or running static ABI inspection
does not load a vendor DLL. They are not part of the Python 2 legacy bridge
contract. Full release acceptance compiles both under the selected Python
3.8+ runtime, or reports a separate skip for the legacy Python 2 runtime.

Go tests for `internal/tracebridge`, `internal/tracecontrol`, actor, and daemon
exercise native request/response handling with owned fake processes. Windows
tests include abrupt parent exit, kill-on-close Jobs, inherited source handles,
and actual child reaping. These do not validate vendor DLL behavior, native
session binding, or a hardware collection workflow.

To byte-compile with an actual MULTI interpreter, pass its standalone
`python.exe` path to release acceptance. Python 3 is preferred; use
`-MultiPython2` only for the explicit legacy fallback. Do not pass
`mpythonrun.exe`; it is a script runner rather than a standalone interpreter:

```powershell
.\scripts\Invoke-WindowsReleaseAcceptance.ps1 -MultiPython3 C:\path\to\multi\python-3.8.6\python.exe
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

For opt-in acceptance in an installed official editor:

```powershell
Push-Location .\editors\vscode
npm run test:editor -- --code "C:\path\to\Code.exe"
Pop-Location
```

This uses an isolated profile and synthetic backend with the production
authenticated proxy and DAP server. It tests actual Extension Host activation,
attach, explicit daemon reuse, inspection, disconnect, durable host capture,
and same-session native interval capture. The native case invokes the production
command and Go helper, verifies one frontend and continued ordinary inspection,
and normalizes the resulting synthetic packet capture.
No vendor runtime or target is used. The command requires an explicit local
editor, does not download it, and is not run by ordinary tests or release
acceptance. Its ignored log/report records the tested editor version and
scope; it is not visual UI or hardware qualification.

`trace capture-editor` is the extension's private bounded NDJSON transport,
not another attach command. It validates configuration locally and receives
only forwarded native requests from the existing DebugSession. Native close
is terminal: the extension ends helper stdin with that response, and the
helper requires EOF before finalizing a successful private artifact. Keep
the nonce, request identity, frame limits, pending-request fences, and
same-session tracker replacement tests aligned when changing either side.

## Zed checks

The Zed source extension pins its Rust SDK and dependency lockfile. Prepare
Rust 1.95.0, its formatting/lint components, the Wasm target, and dependency
cache before offline release acceptance:

```powershell
rustup toolchain install 1.95.0 --profile minimal --component rustfmt --component clippy --target wasm32-wasip2
cargo +1.95.0 fetch --locked --manifest-path editors/zed/Cargo.toml
cargo +1.95.0 fmt --manifest-path editors/zed/Cargo.toml --check
cargo +1.95.0 test --locked --offline --manifest-path editors/zed/Cargo.toml
cargo +1.95.0 clippy --locked --offline --manifest-path editors/zed/Cargo.toml --all-targets -- -D warnings
cargo +1.95.0 build --locked --offline --release --target wasm32-wasip2 --manifest-path editors/zed/Cargo.toml
```

CI checks the source extension on Windows and Linux; its runtime integration
is local Windows only. Full and fast local release acceptance both check the
extension's formatting, host tests, lint, and Wasm build. These commands do not
launch Zed, acquire a daemon, or establish GUI/hardware acceptance. Build
artifacts remain ignored and are not Windows daemon ZIP payloads.

## Documentation

The documentation site uses [Zensical](https://zensical.org/), pinned to
**0.0.73**, with its native TOML configuration. It does not require a separate
MkDocs configuration or a MkDocs plugin pipeline. Use uv and Python 3.12, and
run the commands below from the repository root.

| File or directory | Responsibility |
|---|---|
| `zensical.toml` | Site navigation, modern theme, Markdown extensions, and strict link/anchor validation. |
| `docs/*.md` | Published guides and references; historical plans remain under `docs/superpowers/plans/`. |
| `docs/pyproject.toml` | Exact Zensical dependency and supported host Python version. |
| `docs/uv.lock` | Complete resolved documentation environment and distribution hashes. |
| `docs/.venv/` and `site/` | Ignored local environment and generated site; neither is committed. |

The docs project has `package = false`: it is a build environment, not an
installable `multi-dap` Python package. Its local virtual environment, project
file, and lockfile are excluded from the generated site.

### Preview locally

```powershell
uv run --project docs --locked --python 3.12 zensical serve --dev-addr 127.0.0.1:8000
```

Open `http://127.0.0.1:8000/`. The preview rebuilds when documentation changes;
stop it with Ctrl+C. Keep it bound to loopback when inspecting private local
edits. This starts only the documentation server, not the daemon, MULTI, or a
target connection.

### Build and validate

```powershell
uv run --project docs --locked --python 3.12 zensical build --clean --strict
```

This is the command used by CI and Windows release acceptance. It writes
`site/`; warnings, invalid links, and invalid anchors fail the build. The
`--clean` option clears the build cache. After changing exclusion rules, remove
only the ignored generated `site/` directory before rebuilding: stale output
from an older build must not be carried into publication.

Once the locked Python environment and packages are cached, check without
network access or implicit Python downloads:

```powershell
uv lock --project docs --check --offline --python 3.12 --no-python-downloads
uv run --offline --no-python-downloads --project docs --locked --python 3.12 zensical build --clean --strict
```

An uncached dependency is an offline setup failure, not permission to replace
the locked environment with an unpinned global installation.

### Authoring rules

- Edit Markdown sources, not generated HTML. Keep relative links within the
  documentation tree and add published pages to `project.nav` in
  `zensical.toml`.
- Preserve existing file names and heading anchors when updating a page so
  incoming links remain valid. Navigation groups do not change page URLs.
- Use the configured admonitions, fenced code blocks, tabs, tables, and Mermaid
  diagrams instead of introducing a second documentation framework.
- Preview affected pages and check navigation, search, diagrams, and narrow
  layouts after theme or framework changes.
- Keep all repository text English. Examples may contain non-English Unicode
  only through ASCII escape sequences when a test explicitly verifies encoding
  behavior.

### Upgrade Zensical

Review the [upstream releases](https://github.com/zensical/zensical/releases)
before changing the exact version in `docs/pyproject.toml`. The selected
0.0.73 release was published on 2026-10-26 and checked on 2026-10-30.
Regenerate the lockfile with uv instead of editing distribution hashes:

```powershell
uv lock --project docs --python 3.12 --upgrade-package zensical
uv run --project docs --locked --python 3.12 zensical --version
uv run --project docs --locked --python 3.12 zensical build --clean --strict
```

Review transitive dependency changes, preview the affected pages, and update
the documented version alongside the dependency and lockfile. CI and release
acceptance must continue to use `--locked`; neither should select the latest
upstream version implicitly.

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

Full mode adds the race detector, preferred MULTI Python 3 compilation (with
a Python 2 compatibility fallback), and two
independent package builds whose archive digests and internal manifests must
match. Both modes also verify that every GitHub Action is pinned to the reviewed
commit and version in `.github/actions-lock.json`. Inspect the final
`RELEASE_ACCEPTANCE_RESULT` JSON for skipped checks.

## Hardware work

Read `recon/README.md` before touching the harness. One operator owns the
hardware at a time. Never print or commit raw target data, and never infer a
hardware capability from a host fake. Record only the sanitized evidence
defined by [M0 findings](m0-findings.md).
