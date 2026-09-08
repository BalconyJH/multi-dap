[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Remove-OwnedTemporaryDirectory {
    param([Parameter(Mandatory)] [string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return }
    $resolvedPath = (Resolve-Path -LiteralPath $Path).Path.TrimEnd('\', '/')
    $temporaryRoot = ([System.IO.Path]::GetTempPath()).TrimEnd('\', '/')
    $expectedPrefix = $temporaryRoot + [System.IO.Path]::DirectorySeparatorChar
    if (-not $resolvedPath.StartsWith($expectedPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not ((Split-Path -Leaf $resolvedPath).StartsWith('multi-dap-winget-test-', [System.StringComparison]::Ordinal))) {
        throw "refusing to remove a directory not owned by the WinGet manifest test: $resolvedPath"
    }
    Remove-Item -LiteralPath $resolvedPath -Recurse -Force
}

$repositoryRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$temporaryRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("multi-dap-winget-test-" + [guid]::NewGuid().ToString('N'))
[System.IO.Directory]::CreateDirectory($temporaryRoot) | Out-Null

try {
    $version = '0.1.0-rc.1'
    $url = 'https://github.com/Tacrolimus/multi-dap/releases/download/v0.1.0-rc.1/multi-dap-0.1.0-rc.1-windows-amd64.zip'
    $hash = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'
    & (Join-Path $repositoryRoot 'scripts\New-WinGetManifest.ps1') `
        -Version $version `
        -InstallerUrl $url `
        -InstallerSha256 $hash `
        -OutputDirectory $temporaryRoot

    $files = @(Get-ChildItem -LiteralPath $temporaryRoot -File | Sort-Object Name)
    $expectedNames = @(
        'Tacrolimus.multi-dap.installer.yaml',
        'Tacrolimus.multi-dap.locale.en-US.yaml',
        'Tacrolimus.multi-dap.yaml'
    )
    if (($files.Name -join "`n") -cne ($expectedNames -join "`n")) {
        throw "unexpected WinGet manifest set: $($files.Name -join ', ')"
    }

    $allText = ($files | ForEach-Object { [System.IO.File]::ReadAllText($_.FullName) }) -join "`n"
    foreach ($required in @(
            'PackageIdentifier: Tacrolimus.multi-dap',
            'PackageVersion: 0.1.0-rc.1',
            'InstallerType: zip',
            'NestedInstallerType: portable',
            'RelativeFilePath: multi-dap.exe',
            'ArchiveBinariesDependOnPath: true',
            'UpgradeBehavior: uninstallPrevious',
            "InstallerUrl: '$url'",
            'InstallerSha256: 0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF',
            'License: MIT',
            'ManifestVersion: 1.12.0')) {
        if ($allText.IndexOf($required, [System.StringComparison]::Ordinal) -lt 0) {
            throw "generated WinGet manifests are missing required text: $required"
        }
    }
    if ($allText -match '[\p{IsCJKUnifiedIdeographs}\p{IsCJKUnifiedIdeographsExtensionA}]') {
        throw 'generated WinGet manifests must contain English text only'
    }
    foreach ($file in $files) {
        $bytes = [System.IO.File]::ReadAllBytes($file.FullName)
        if ($bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF) {
            throw "WinGet manifest contains a UTF-8 BOM: $($file.Name)"
        }
        $text = [System.Text.Encoding]::UTF8.GetString($bytes)
        if ($text.Contains("`r") -or -not $text.EndsWith("`n", [System.StringComparison]::Ordinal)) {
            throw "WinGet manifest does not use normalized LF line endings: $($file.Name)"
        }
    }

    $invalidDirectory = Join-Path $temporaryRoot 'invalid-version'
    $rejectedInvalidVersion = $false
    try {
        & (Join-Path $repositoryRoot 'scripts\New-WinGetManifest.ps1') `
            -Version '1.0.0-01' `
            -InstallerUrl $url `
            -InstallerSha256 $hash `
            -OutputDirectory $invalidDirectory | Out-Null
    } catch {
        $rejectedInvalidVersion = $_.Exception.Message -like '*leading zeroes*'
    }
    if (-not $rejectedInvalidVersion) {
        throw 'WinGet manifest generator accepted an invalid numeric prerelease identifier'
    }

    $rejectedControlCharacter = $false
    try {
        & (Join-Path $repositoryRoot 'scripts\New-WinGetManifest.ps1') `
            -Version $version `
            -InstallerUrl ($url + "`nInjected: true") `
            -InstallerSha256 $hash `
            -OutputDirectory (Join-Path $temporaryRoot 'invalid-url') | Out-Null
    } catch {
        $rejectedControlCharacter = $_.Exception.Message -like '*control characters*'
    }
    if (-not $rejectedControlCharacter) {
        throw 'WinGet manifest generator accepted a control character in InstallerUrl'
    }

    $winget = Get-Command winget -ErrorAction SilentlyContinue
    if ($null -ne $winget) {
        & $winget.Source validate --manifest $temporaryRoot
        if ($LASTEXITCODE -ne 0) {
            throw "winget validate failed with exit code $LASTEXITCODE"
        }
        Write-Output 'winget validate passed.'
    } else {
        Write-Output 'winget validate skipped: winget is not installed.'
    }

    Write-Output 'WinGet manifest generator passed.'
} finally {
    Remove-OwnedTemporaryDirectory -Path $temporaryRoot
}
