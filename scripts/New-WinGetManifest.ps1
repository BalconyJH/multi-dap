[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidatePattern('^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$')]
    [string]$Version,

    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string]$InstallerUrl,

    [Parameter(Mandatory)]
    [ValidatePattern('^[0-9A-Fa-f]{64}$')]
    [string]$InstallerSha256,

    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string]$OutputDirectory,

    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9.-]+/[A-Za-z0-9._-]+$')]
    [string]$Repository = 'Tacrolimus/multi-dap',

    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]+$')]
    [string]$PackageIdentifier = 'Tacrolimus.multi-dap',

    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9 .()+/_-]{1,127}$')]
    [string]$License = 'MIT'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

if ($Version.Contains('-')) {
    foreach ($identifier in $Version.Split('-', 2)[1].Split('.')) {
        if ($identifier -match '^0[0-9]+$') {
            throw "Numeric prerelease identifiers must not contain leading zeroes: $Version"
        }
    }
}

function Write-ManifestFile {
    param(
        [Parameter(Mandatory)] [string]$Path,
        [Parameter(Mandatory)] [string]$Content
    )

    if (Test-Path -LiteralPath $Path) {
        throw "Refusing to overwrite existing WinGet manifest: $Path"
    }
    $normalized = ($Content -replace "`r`n", "`n").TrimStart("`n")
    if (-not $normalized.EndsWith("`n", [System.StringComparison]::Ordinal)) {
        $normalized += "`n"
    }
    [System.IO.File]::WriteAllText($Path, $normalized, [System.Text.UTF8Encoding]::new($false))
}

$uri = $null
if ($InstallerUrl -match '[\x00-\x1F\x7F]') {
    throw 'InstallerUrl must not contain control characters.'
}
if (-not [System.Uri]::TryCreate($InstallerUrl, [System.UriKind]::Absolute, [ref]$uri) -or
    $uri.Scheme -cne 'https') {
    throw 'InstallerUrl must be an absolute HTTPS URL.'
}
$canonicalInstallerUrl = $uri.AbsoluteUri

if (-not (Test-Path -LiteralPath $OutputDirectory)) {
    [System.IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
}
$output = (Resolve-Path -LiteralPath $OutputDirectory).Path
if (-not (Test-Path -LiteralPath $output -PathType Container)) {
    throw "OutputDirectory is not a directory: $output"
}

$repositoryUrl = "https://github.com/$Repository"
$releaseUrl = "$repositoryUrl/releases/tag/v$Version"
$licenseUrl = "$repositoryUrl/blob/v$Version/LICENSE"
$hash = $InstallerSha256.ToUpperInvariant()
$baseName = $PackageIdentifier

$versionManifest = @"
# yaml-language-server: `$schema=https://aka.ms/winget-manifest.version.1.12.0.schema.json
PackageIdentifier: $PackageIdentifier
PackageVersion: $Version
DefaultLocale: en-US
ManifestType: version
ManifestVersion: 1.12.0
"@

$installerManifest = @"
# yaml-language-server: `$schema=https://aka.ms/winget-manifest.installer.1.12.0.schema.json
PackageIdentifier: $PackageIdentifier
PackageVersion: $Version
InstallerType: zip
NestedInstallerType: portable
NestedInstallerFiles:
  - RelativeFilePath: multi-dap.exe
ArchiveBinariesDependOnPath: true
UpgradeBehavior: uninstallPrevious
Installers:
  - Architecture: x64
    InstallerUrl: '$canonicalInstallerUrl'
    InstallerSha256: $hash
ManifestType: installer
ManifestVersion: 1.12.0
"@

$localeManifest = @"
# yaml-language-server: `$schema=https://aka.ms/winget-manifest.defaultLocale.1.12.0.schema.json
PackageIdentifier: $PackageIdentifier
PackageVersion: $Version
PackageLocale: en-US
Publisher: Tacrolimus
PublisherUrl: https://github.com/Tacrolimus
PublisherSupportUrl: $repositoryUrl/issues
PackageName: multi-dap
PackageUrl: $repositoryUrl
License: $License
LicenseUrl: $licenseUrl
Copyright: Copyright (c) 2026 multi-dap contributors
ShortDescription: Local Debug Adapter Protocol backend for Green Hills MULTI
Description: >-
  multi-dap exposes a local Green Hills MULTI debug session through the Debug
  Adapter Protocol. It requires a separately licensed and configured Green Hills
  MULTI installation and supported debug hardware.
Moniker: multi-dap
Tags:
  - dap
  - debugger
  - embedded
  - green-hills
  - multi
ReleaseNotesUrl: $releaseUrl
InstallationNotes: >-
  Configure a private project TOML and a licensed Green Hills MULTI installation
  before starting the daemon. The package includes bridge/bridge.py, which the
  installed executable discovers relative to its own location.
ManifestType: defaultLocale
ManifestVersion: 1.12.0
"@

Write-ManifestFile -Path (Join-Path $output "$baseName.yaml") -Content $versionManifest
Write-ManifestFile -Path (Join-Path $output "$baseName.installer.yaml") -Content $installerManifest
Write-ManifestFile -Path (Join-Path $output "$baseName.locale.en-US.yaml") -Content $localeManifest

Get-ChildItem -LiteralPath $output -Filter "$baseName*.yaml" -File | Sort-Object Name
