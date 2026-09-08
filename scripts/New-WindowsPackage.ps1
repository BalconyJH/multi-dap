[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string]$OutputDirectory,

    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z._-]*$')]
    [string]$Version = '0.0.0-dev',

    [ValidatePattern('^$|^[0-9A-Fa-f]{7,40}$')]
    [string]$Commit = '',

    [ValidateRange(315532800, [long]::MaxValue)]
    [long]$SourceDateEpoch = 315532800
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Copy-PackageFile {
    param(
        [Parameter(Mandatory)] [string]$Source,
        [Parameter(Mandatory)] [string]$Destination,
        [Parameter(Mandatory)] [datetime]$Timestamp
    )

    if (-not (Test-Path -LiteralPath $Source -PathType Leaf)) {
        throw "Required package input is missing: $Source"
    }
    $parent = Split-Path -Parent $Destination
    [System.IO.Directory]::CreateDirectory($parent) | Out-Null
    [System.IO.File]::Copy($Source, $Destination, $false)
    [System.IO.File]::SetCreationTimeUtc($Destination, $Timestamp)
    [System.IO.File]::SetLastWriteTimeUtc($Destination, $Timestamp)
    [System.IO.File]::SetLastAccessTimeUtc($Destination, $Timestamp)
}

function Add-DeterministicZip {
    param(
        [Parameter(Mandatory)] [string]$Stage,
        [Parameter(Mandatory)] [string]$ArchivePath,
        [Parameter(Mandatory)] [datetime]$Timestamp
    )

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $stream = [System.IO.File]::Open($ArchivePath, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write)
    try {
        $archive = [System.IO.Compression.ZipArchive]::new($stream, [System.IO.Compression.ZipArchiveMode]::Create, $false)
        try {
            $files = Get-ChildItem -LiteralPath $Stage -File -Recurse | Sort-Object { $_.FullName.Substring($Stage.Length + 1).Replace('\', '/') }
            foreach ($file in $files) {
                $entryName = $file.FullName.Substring($Stage.Length + 1).Replace('\', '/')
                $entry = $archive.CreateEntry($entryName, [System.IO.Compression.CompressionLevel]::NoCompression)
                $entry.LastWriteTime = [System.DateTimeOffset]$Timestamp
                $input = [System.IO.File]::OpenRead($file.FullName)
                try {
                    $output = $entry.Open()
                    try { $input.CopyTo($output) } finally { $output.Dispose() }
                } finally { $input.Dispose() }
            }
        } finally {
            $archive.Dispose()
        }
    } finally {
        $stream.Dispose()
    }
}

function Remove-OwnedTemporaryDirectory {
    param(
        [Parameter(Mandatory)] [string]$Path,
        [Parameter(Mandatory)] [string]$Prefix
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return }
    $resolvedPath = (Resolve-Path -LiteralPath $Path).Path.TrimEnd('\', '/')
    $temporaryRoot = ([System.IO.Path]::GetTempPath()).TrimEnd('\', '/')
    $expectedPrefix = $temporaryRoot + [System.IO.Path]::DirectorySeparatorChar
    if (-not $resolvedPath.StartsWith($expectedPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not ((Split-Path -Leaf $resolvedPath).StartsWith($Prefix, [System.StringComparison]::Ordinal))) {
        throw "refusing to remove a directory not owned by Windows packaging: $resolvedPath"
    }
    Remove-Item -LiteralPath $resolvedPath -Recurse -Force
}

$repositoryRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$outputParent = (Resolve-Path -LiteralPath $OutputDirectory).Path
$archiveName = "multi-dap-$Version-windows-amd64.zip"
$archivePath = Join-Path $outputParent $archiveName
$checksumPath = "$archivePath.sha256"
foreach ($outputPath in @($archivePath, $checksumPath)) {
    if (Test-Path -LiteralPath $outputPath) {
        throw "Refusing to overwrite existing release output: $outputPath"
    }
}

$timestampOffset = [System.DateTimeOffset]::FromUnixTimeSeconds($SourceDateEpoch)
$minimumZipTimestamp = [System.DateTimeOffset]::new(1980, 1, 1, 0, 0, 0, [System.TimeSpan]::Zero)
if ($timestampOffset -lt $minimumZipTimestamp) {
    throw 'ZIP timestamps must be no earlier than 1980-01-01 UTC.'
}
$timestamp = $timestampOffset.UtcDateTime
$buildDate = $timestampOffset.ToString('yyyy-MM-ddTHH:mm:ssZ', [System.Globalization.CultureInfo]::InvariantCulture)
$normalizedCommit = $Commit.ToLowerInvariant()
$stage = Join-Path ([System.IO.Path]::GetTempPath()) ("multi-dap-package-" + [guid]::NewGuid().ToString('N'))
[System.IO.Directory]::CreateDirectory($stage) | Out-Null

try {
    $previousGoos = $env:GOOS
    $previousGoarch = $env:GOARCH
    $previousCgo = $env:CGO_ENABLED
    $previousProxy = $env:GOPROXY
    try {
        $env:GOOS = 'windows'
        $env:GOARCH = 'amd64'
        $env:CGO_ENABLED = '0'
        $env:GOPROXY = 'off'
        $ldflags = "-s -w -X github.com/Tacrolimus/multi-dap/internal/version.Version=$Version -X github.com/Tacrolimus/multi-dap/internal/version.BuildDate=$buildDate"
        if ($normalizedCommit) {
            $ldflags += " -X github.com/Tacrolimus/multi-dap/internal/version.Commit=$normalizedCommit"
        }
        & go build -mod=readonly -trimpath -buildvcs=false -ldflags $ldflags -o (Join-Path $stage 'multi-dap.exe') ./cmd/multi-dap
        if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
    } finally {
        $env:GOOS = $previousGoos
        $env:GOARCH = $previousGoarch
        $env:CGO_ENABLED = $previousCgo
        $env:GOPROXY = $previousProxy
    }

    Copy-PackageFile -Source (Join-Path $repositoryRoot 'bridge\bridge.py') -Destination (Join-Path $stage 'bridge\bridge.py') -Timestamp $timestamp
    Copy-PackageFile -Source (Join-Path $repositoryRoot 'editors\clion\README.md') -Destination (Join-Path $stage 'editors\clion\README.md') -Timestamp $timestamp
    Copy-PackageFile -Source (Join-Path $repositoryRoot 'editors\clion\profile-contract.json') -Destination (Join-Path $stage 'editors\clion\profile-contract.json') -Timestamp $timestamp
    Copy-PackageFile -Source (Join-Path $repositoryRoot 'multi-dap.example.toml') -Destination (Join-Path $stage 'examples\multi-dap.example.toml') -Timestamp $timestamp
    Copy-PackageFile -Source (Join-Path $repositoryRoot 'packaging\README.md') -Destination (Join-Path $stage 'README.md') -Timestamp $timestamp
    Copy-PackageFile -Source (Join-Path $repositoryRoot 'packaging\LICENSE.txt') -Destination (Join-Path $stage 'LICENSE.txt') -Timestamp $timestamp
    Copy-PackageFile -Source (Join-Path $repositoryRoot 'THIRD_PARTY_NOTICES.md') -Destination (Join-Path $stage 'THIRD_PARTY_NOTICES.md') -Timestamp $timestamp
    Copy-PackageFile -Source (Join-Path $repositoryRoot 'third_party\go\LICENSE') -Destination (Join-Path $stage 'licenses\Go.txt') -Timestamp $timestamp
    Copy-PackageFile -Source (Join-Path $repositoryRoot 'third_party\go-toml\LICENSE') -Destination (Join-Path $stage 'licenses\go-toml.txt') -Timestamp $timestamp

    $binaryPath = Join-Path $stage 'multi-dap.exe'
    $binaryInfo = Get-Item -LiteralPath $binaryPath
    $binaryHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $binaryPath).Hash.ToLowerInvariant()
    $goVersion = (& go env GOVERSION).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($goVersion)) {
        throw 'go env GOVERSION failed while recording release provenance'
    }
    $commitValue = if ([string]::IsNullOrWhiteSpace($normalizedCommit)) { $null } else { $normalizedCommit }
    $provenance = [ordered]@{
        schema = 'multi-dap.release-provenance/v1'
        package = 'multi-dap'
        version = $Version
        commit = $commitValue
        source_date_epoch = $SourceDateEpoch
        build_date = $buildDate
        target = [ordered]@{
            os = 'windows'
            architecture = 'amd64'
        }
        toolchain = [ordered]@{
            go = $goVersion
            cgo_enabled = $false
        }
        binary = [ordered]@{
            path = 'multi-dap.exe'
            size = $binaryInfo.Length
            sha256 = $binaryHash
        }
    }
    $provenancePath = Join-Path $stage 'release-provenance.json'
    $provenanceJSON = ($provenance | ConvertTo-Json -Compress -Depth 4) + "`n"
    [System.IO.File]::WriteAllText($provenancePath, $provenanceJSON, [System.Text.UTF8Encoding]::new($false))
    [System.IO.File]::SetCreationTimeUtc($provenancePath, $timestamp)
    [System.IO.File]::SetLastWriteTimeUtc($provenancePath, $timestamp)
    [System.IO.File]::SetLastAccessTimeUtc($provenancePath, $timestamp)

    $payload = Get-ChildItem -LiteralPath $stage -File -Recurse | Sort-Object { $_.FullName.Substring($stage.Length + 1).Replace('\', '/') }
    $manifestLines = foreach ($file in $payload) {
        $relativePath = $file.FullName.Substring($stage.Length + 1).Replace('\', '/')
        "{0}  {1}" -f (Get-FileHash -Algorithm SHA256 -LiteralPath $file.FullName).Hash.ToLowerInvariant(), $relativePath
    }
    $manifestPath = Join-Path $stage 'SHA256SUMS.txt'
    [System.IO.File]::WriteAllText($manifestPath, (($manifestLines -join "`n") + "`n"), [System.Text.UTF8Encoding]::new($false))
    [System.IO.File]::SetCreationTimeUtc($manifestPath, $timestamp)
    [System.IO.File]::SetLastWriteTimeUtc($manifestPath, $timestamp)
    [System.IO.File]::SetLastAccessTimeUtc($manifestPath, $timestamp)

    Add-DeterministicZip -Stage $stage -ArchivePath $archivePath -Timestamp $timestamp
    $archiveHash = Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath
    $checksumLine = $archiveHash.Hash.ToLowerInvariant() + "  " + $archiveName + "`n"
    [System.IO.File]::WriteAllText($checksumPath, $checksumLine, [System.Text.UTF8Encoding]::new($false))
    [System.IO.File]::SetCreationTimeUtc($checksumPath, $timestamp)
    [System.IO.File]::SetLastWriteTimeUtc($checksumPath, $timestamp)
    [System.IO.File]::SetLastAccessTimeUtc($checksumPath, $timestamp)
    $archiveHash
    Get-Item -LiteralPath $checksumPath
} finally {
    Remove-OwnedTemporaryDirectory -Path $stage -Prefix 'multi-dap-package-'
}
