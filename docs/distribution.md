# Windows Distribution Guide

`multi-dap` is distributed as a self-contained Windows amd64 ZIP. The bundle
contains the Go executable and its required `bridge/bridge.py` companion; it
does not include Green Hills MULTI, a target server, firmware, or a completed
project configuration.

## Supported matrix

| Environment | Production status | Notes |
|---|---|---|
| Windows amd64 workstation with licensed Green Hills MULTI | Supported distribution target | MULTI must provide its Python 2.7 runtime and modules locally. |
| Windows arm64 or x86 | Unsupported | No release archive or hardware qualification is provided. |
| macOS or Linux | Unsupported production runtime | Go host tests may run there, but the MULTI launcher rejects non-Windows execution. |
| Python 3 and `uv` | Development only | Used for host tests and documentation, not for the production bridge. |

No public release has been issued yet. Package-manager commands below describe
the intended post-publication interface, not a currently available package.
Consult the [capability matrix](capabilities.md) before treating a supported
host installation as target or hardware qualification.

## Build from source

Use Go 1.26.6, the version declared by `go.mod`, on Windows amd64:

```powershell
git clone https://github.com/Tacrolimus/multi-dap.git
Set-Location .\multi-dap
go build -mod=readonly -o .\multi-dap.exe .\cmd\multi-dap
.\multi-dap.exe version
```

Source builds must name the bundled bridge explicitly when starting a daemon:

```powershell
.\multi-dap.exe ensure `
  --config "C:\private\multi-dap-project.toml" `
  --bridge-script "$PWD\bridge\bridge.py" `
  --acquisition cold
```

Build and validate the configuration before starting MULTI. The
[getting-started guide](getting-started.md) and the [configuration reference](configuration.md)
define the required paths and lifecycle inputs.

## Verify a release ZIP

Download both `multi-dap-<version>-windows-amd64.zip` and its adjacent
`.zip.sha256` file from the same GitHub release. Verify the downloaded archive
before extracting it, then verify every extracted payload against the archive's
internal `SHA256SUMS.txt` manifest. This is deliberately a two-layer check.

```powershell
$version = '<version>'
$archive = "multi-dap-$version-windows-amd64.zip"
$outer = Get-Content -Raw "$archive.sha256"
$expectedArchiveHash, $expectedArchiveName = $outer.Trim() -split '  ', 2
if ($expectedArchiveName -ne $archive) { throw 'checksum names another archive' }
if ((Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash.ToLowerInvariant() -ne $expectedArchiveHash) {
  throw 'archive checksum mismatch'
}

$destination = "C:\Tools\multi-dap-$version"
Expand-Archive -LiteralPath $archive -DestinationPath $destination
Get-Content -LiteralPath "$destination\SHA256SUMS.txt" | ForEach-Object {
  $expected, $relative = $_ -split '  ', 2
  $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $destination $relative)).Hash.ToLowerInvariant()
  if ($actual -ne $expected) { throw "payload checksum mismatch: $relative" }
}
```

Only extract into a new versioned directory. Do not mix an executable from one
bundle with `bridge/bridge.py` from another bundle.

## Manual install and first smoke test

After verification, key package entries include:

```text
multi-dap-<version>-windows-amd64/
  multi-dap.exe
  bridge/bridge.py
  examples/multi-dap.example.toml
  SHA256SUMS.txt
  release-provenance.json
```

`multi-dap.exe` discovers `bridge/bridge.py` relative to the executable, so it
can be run from any working directory without a production `--bridge-script`
argument. Confirm the installed artifact before configuring an IDE:

```powershell
& "C:\Tools\multi-dap-<version>\multi-dap.exe" version
```

Copy the example TOML outside the release directory, fill in its private local
paths, and validate it. For the supported editor path, use the same absolute
TOML in CLion's `ensure` action and `proxy --config` profile; see
[Configure CLion](clion.md).

## Manual upgrade, rollback, and uninstall

An upgrade never migrates a live target session:

1. Detach the IDE client and run `multi-dap shutdown --config "<current.toml>"`.
2. Download, verify, and extract the new archive into a new versioned folder.
3. Run `multi-dap.exe version`, update any explicit executable paths in editor
   settings, and start a newly selected cold or warm acquisition.
4. Keep the previous verified folder and archive until the new installation
   has completed the needed target validation.

To roll back, shut down the new daemon, restore the previous complete folder
and editor path, then start a new acquisition. Do not copy individual files
between versions. To uninstall a manual installation, first stop its daemon,
remove any PATH or CLion references you added, then delete only its known
versioned installation folder. Keep configurations and evidence separately;
they are not release payloads.

## Intended WinGet lifecycle

After a public GitHub release has been accepted by `microsoft/winget-pkgs`,
the intended commands are:

```powershell
winget install --exact --id Tacrolimus.multi-dap
winget upgrade --exact --id Tacrolimus.multi-dap
winget uninstall --exact --id Tacrolimus.multi-dap
multi-dap version
```

WinGet uses the portable ZIP manifest and keeps the executable with its
bridge. The manifest generator and validation are in this repository, but the
package is **not yet active in the WinGet community repository**: a public
immutable release, exact asset hash, manifest review, and clean-host install,
bridge-discovery, and uninstall evidence are still required for the first
submission. Upgrade evidence is a later two-release gate because no prior
public version exists yet. Do not expect the commands to resolve until the
initial activation boundary is complete.

## Why `uv` and Homebrew are not install channels

`uv` is intentionally limited to Python 3 host tests and the locked
documentation environment. The production bridge must execute with the
proprietary Python 2.7 modules supplied by Green Hills MULTI, so this project
is not a PyPI/`uv tool install` package.

Homebrew is intentionally deferred. Its usual macOS and Linux targets are not
production runtime platforms for `multi-dap`; publishing a formula today would
install a launcher that rejects the host. A supported non-Windows runtime and
its qualification would be required first.

For release construction, provenance, publication safeguards, and the WinGet
submission process, see the [release guide](releasing.md).
