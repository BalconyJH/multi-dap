[CmdletBinding()]
param(
    [string]$Version = '0.0.0-test',
    [ValidatePattern('^$|^[0-9A-Fa-f]{7,40}$')]
    [string]$Commit = '',
    [long]$SourceDateEpoch = 315532800
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Assert-PackageArchive {
    param(
        [Parameter(Mandatory)] [string]$ArchivePath,
        [Parameter(Mandatory)] [string]$ExpectedVersion,
        [AllowEmptyString()] [string]$ExpectedCommit = '',
        [Parameter(Mandatory)] [long]$ExpectedSourceDateEpoch
    )

    $expectedEntries = @(
        'LICENSE.txt',
        'README.md',
        'SHA256SUMS.txt',
        'THIRD_PARTY_NOTICES.md',
        'bridge/bridge.py',
        'editors/clion/README.md',
        'editors/clion/profile-contract.json',
        'examples/multi-dap.example.toml',
        'licenses/Go.txt',
        'licenses/go-toml.txt',
        'multi-dap.exe',
        'release-provenance.json'
    )
    $expectedPayload = @($expectedEntries | Where-Object { $_ -ne 'SHA256SUMS.txt' })
    $forbiddenEntryPattern = '(?i)(^|/)(?:recon(?:/|$)|.*(?:\.local\.(?:toml|json)|\.token|\.log|\.tmp|\.capture|\.pcap|\.ready)(?:$|/)|(?:config|token|capture|log|ready)(?:/|$))'

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = [System.IO.Compression.ZipFile]::OpenRead($ArchivePath)
    try {
        $entries = @($archive.Entries | ForEach-Object FullName | Sort-Object)
        if (Compare-Object $expectedEntries $entries) {
            throw "package content allowlist mismatch: $($entries -join ', ')"
        }
        if (@($entries | Group-Object | Where-Object Count -gt 1).Count -ne 0) {
            throw 'package contains duplicate ZIP entry names'
        }
        foreach ($entryName in $entries) {
            if ($entryName -match $forbiddenEntryPattern) {
                throw "package contains forbidden release artifact: $entryName"
            }
        }

        $noticeEntry = $archive.GetEntry('THIRD_PARTY_NOTICES.md')
        $goLicenseEntry = $archive.GetEntry('licenses/Go.txt')
        $tomlLicenseEntry = $archive.GetEntry('licenses/go-toml.txt')
        if ($null -eq $noticeEntry -or $null -eq $goLicenseEntry -or $null -eq $tomlLicenseEntry) {
            throw 'package is missing required third-party notices or license texts'
        }

        $readmeEntry = $archive.GetEntry('README.md')
        if ($null -eq $readmeEntry) { throw 'package is missing README.md' }
        $reader = [System.IO.StreamReader]::new($readmeEntry.Open(), [System.Text.UTF8Encoding]::new($false))
        try {
            $readmeText = $reader.ReadToEnd()
        } finally {
            $reader.Dispose()
        }
        foreach ($requiredText in @('Cidr DAP CMake Debug', '<your-cmake-application>', 'proxy --config "<absolute-project.toml>"', '[probe].id', 'Debug (bug icon)', 'multi-dap ensure', '--bridge-script "<absolute-path-to-bridge.py>"', 'Hardware validation checklist')) {
            if ($readmeText.IndexOf($requiredText, [System.StringComparison]::Ordinal) -lt 0) {
                throw "package README is missing CLion integration contract: $requiredText"
            }
        }

        $clionReadmeEntry = $archive.GetEntry('editors/clion/README.md')
        if ($null -eq $clionReadmeEntry) { throw 'package is missing the CLion integration guide' }
        $reader = [System.IO.StreamReader]::new($clionReadmeEntry.Open(), [System.Text.UTF8Encoding]::new($false))
        try {
            $clionReadmeText = $reader.ReadToEnd()
        } finally {
            $reader.Dispose()
        }
        foreach ($requiredText in @('Cidr DAP CMake Debug', '<your-cmake-application>', 'proxy --config "<absolute-project.toml>"', '[probe].id', 'Launch', 'Debug (bug icon)', 'multi-dap ensure', '--bridge-script "<absolute-path-to-bridge.py>"', 'Hardware validation checklist')) {
            if ($clionReadmeText.IndexOf($requiredText, [System.StringComparison]::Ordinal) -lt 0) {
                throw "CLion integration guide is missing required content: $requiredText"
            }
        }

        $clionContractEntry = $archive.GetEntry('editors/clion/profile-contract.json')
        if ($null -eq $clionContractEntry) { throw 'package is missing the CLion profile contract' }
        $reader = [System.IO.StreamReader]::new($clionContractEntry.Open(), [System.Text.UTF8Encoding]::new($false))
        try {
            $clionContract = $reader.ReadToEnd() | ConvertFrom-Json
        } finally {
            $reader.Dispose()
        }
        $expectedEnsureArguments = @(
            'ensure',
            '--config',
            '<absolute-project.toml>',
            '--bridge-script',
            '<absolute-path-to-bridge.py>',
            '--acquisition',
            'cold'
        )
        $expectedProxyArguments = @(
            'proxy',
            '--config',
            '<absolute-project.toml>'
        )
        $expectedSatisfiedBy = @(
            'CMake configuration Before Launch external tool',
            'manual ensure before Debug'
        )
        $expectedCMakeNamePlaceholder = '<your-cmake-application>'
        $expectedForbidden = @(
            'Python Attach to DAP configuration',
            'green Run',
            'DAP profile Attach tab',
            'TCP remote address',
            'dynamic DAP port',
            'unbound proxy --probe-id',
            'target ELF as a Windows program',
            'target connection arguments'
        )
        if ($clionContract.schema_version -ne 8 -or
            $clionContract.client -ne 'CLion' -or
            $clionContract.integration -ne 'cidr-native-dap-cmake-debug' -or
            $clionContract.daemon_prerequisite.program -ne '<absolute-path-to-multi-dap.exe>' -or
            (@($clionContract.daemon_prerequisite.command) -join "`n") -ne ($expectedEnsureArguments -join "`n") -or
            $clionContract.daemon_prerequisite.working_directory -ne '<project-root>' -or
            $clionContract.daemon_prerequisite.lifetime -ne 'synchronous-short-lived-explicit-cold-idempotent' -or
            (@($clionContract.daemon_prerequisite.satisfied_by) -join "`n") -ne ($expectedSatisfiedBy -join "`n") -or
            $clionContract.cmake_configuration.kind -ne 'CMake Application' -or
            $clionContract.cmake_configuration.name_placeholder -ne $expectedCMakeNamePlaceholder -or
            $clionContract.cmake_configuration.debug_profile -ne 'multi-dap' -or
            $clionContract.cmake_configuration.activation -ne 'Debug button only; never green Run' -or
            $clionContract.cidr_debug_profile.kind -ne 'DAP' -or
            $clionContract.cidr_debug_profile.name -ne 'multi-dap' -or
            $clionContract.cidr_debug_profile.adapter_executable -ne '<absolute-path-to-multi-dap.exe>' -or
            (@($clionContract.cidr_debug_profile.adapter_arguments) -join "`n") -ne ($expectedProxyArguments -join "`n") -or
            $clionContract.cidr_debug_profile.configuration_binding -ne 'validated-project-semantics-v1' -or
            $clionContract.cidr_debug_profile.communication -ne 'stdin/stdout' -or
            $clionContract.cidr_debug_profile.launch_tab_json.request -ne 'attach' -or
            $clionContract.cidr_debug_profile.attach_tab -ne 'not-used' -or
            (@($clionContract.forbidden) -join "`n") -ne ($expectedForbidden -join "`n")) {
            throw 'package contains an invalid CLion profile contract'
        }

        $manifestEntry = $archive.GetEntry('SHA256SUMS.txt')
        if ($null -eq $manifestEntry) { throw 'package is missing SHA256SUMS.txt' }
        $reader = [System.IO.StreamReader]::new($manifestEntry.Open(), [System.Text.UTF8Encoding]::new($false))
        try {
            $manifestText = $reader.ReadToEnd()
        } finally {
            $reader.Dispose()
        }
        if (-not $manifestText.EndsWith("`n")) { throw 'manifest must end with a newline' }
        $manifestLines = @($manifestText.TrimEnd("`r", "`n").Split("`n"))
        if ($manifestLines.Count -ne $expectedPayload.Count) {
            throw "manifest entry count mismatch: expected $($expectedPayload.Count), got $($manifestLines.Count)"
        }

        $manifestPaths = @()
        foreach ($line in $manifestLines) {
            if ($line -notmatch '^[0-9a-f]{64}  [^\r\n]+$') {
                throw "malformed manifest entry: $line"
            }
            $digest, $path = $line -split '  ', 2
            $manifestPaths += $path
            $entry = $archive.GetEntry($path)
            if ($null -eq $entry) { throw "manifest names absent entry: $path" }
            $stream = $entry.Open()
            try {
                $hasher = [System.Security.Cryptography.SHA256]::Create()
                try { $actual = [System.BitConverter]::ToString($hasher.ComputeHash($stream)).Replace('-', '').ToLowerInvariant() } finally { $hasher.Dispose() }
            } finally {
                $stream.Dispose()
            }
            if ($actual -ne $digest) { throw "manifest digest mismatch: $path" }
        }
        if (@($manifestPaths | Group-Object | Where-Object Count -gt 1).Count -ne 0) {
            throw 'manifest contains duplicate paths'
        }
        $expectedManifestOrder = @($expectedPayload | Sort-Object)
        if (($manifestPaths -join "`n") -ne ($expectedManifestOrder -join "`n")) {
            throw "manifest paths are not in deterministic order: $($manifestPaths -join ', ')"
        }
        if (Compare-Object ($expectedPayload | Sort-Object) ($manifestPaths | Sort-Object)) {
            throw "manifest payload allowlist mismatch: $($manifestPaths -join ', ')"
        }

        $provenanceEntry = $archive.GetEntry('release-provenance.json')
        if ($null -eq $provenanceEntry) { throw 'package is missing release-provenance.json' }
        $reader = [System.IO.StreamReader]::new($provenanceEntry.Open(), [System.Text.UTF8Encoding]::new($false))
        try {
            $provenanceText = $reader.ReadToEnd()
        } finally {
            $reader.Dispose()
        }
        $provenance = $provenanceText | ConvertFrom-Json
        $binaryEntry = $archive.GetEntry('multi-dap.exe')
        $stream = $binaryEntry.Open()
        try {
            $hasher = [System.Security.Cryptography.SHA256]::Create()
            try { $binaryDigest = [System.BitConverter]::ToString($hasher.ComputeHash($stream)).Replace('-', '').ToLowerInvariant() } finally { $hasher.Dispose() }
        } finally {
            $stream.Dispose()
        }
        $provenanceBuildDate = [System.DateTimeOffset]$provenance.build_date
        $provenanceCommit = if ($null -eq $provenance.commit) { '' } else { [string]$provenance.commit }
        if ($provenance.schema -cne 'multi-dap.release-provenance/v1' -or
            $provenance.package -cne 'multi-dap' -or
            $provenance.version -cne $ExpectedVersion -or
            $provenanceCommit -cne $ExpectedCommit.ToLowerInvariant() -or
            $provenance.source_date_epoch -ne $ExpectedSourceDateEpoch -or
            $provenanceBuildDate.ToUnixTimeSeconds() -ne $ExpectedSourceDateEpoch -or
            $provenance.target.os -cne 'windows' -or
            $provenance.target.architecture -cne 'amd64' -or
            $provenance.toolchain.go -notmatch '^go[0-9]+\.[0-9]+' -or
            $provenance.toolchain.cgo_enabled -ne $false -or
            $provenance.binary.path -cne 'multi-dap.exe' -or
            $provenance.binary.size -ne $binaryEntry.Length -or
            $provenance.binary.sha256 -cne $binaryDigest) {
            throw 'package contains invalid release provenance'
        }
    } finally {
        $archive.Dispose()
    }
}

function Remove-OwnedTemporaryDirectory {
    param([Parameter(Mandatory)] [string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return }
    $resolvedPath = (Resolve-Path -LiteralPath $Path).Path.TrimEnd('\', '/')
    $temporaryRoot = ([System.IO.Path]::GetTempPath()).TrimEnd('\', '/')
    $expectedPrefix = $temporaryRoot + [System.IO.Path]::DirectorySeparatorChar
    if (-not $resolvedPath.StartsWith($expectedPrefix, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not ((Split-Path -Leaf $resolvedPath).StartsWith('multi-dap-package-test-', [System.StringComparison]::Ordinal))) {
        throw "refusing to remove a directory not owned by package self-test: $resolvedPath"
    }
    Remove-Item -LiteralPath $resolvedPath -Recurse -Force
}

$repositoryRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$temporaryRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("multi-dap-package-test-" + [guid]::NewGuid().ToString('N'))
[System.IO.Directory]::CreateDirectory($temporaryRoot) | Out-Null
try {
    $first = Join-Path $temporaryRoot 'first'
    $second = Join-Path $temporaryRoot 'second'
    [System.IO.Directory]::CreateDirectory($first) | Out-Null
    [System.IO.Directory]::CreateDirectory($second) | Out-Null

    & (Join-Path $repositoryRoot 'scripts\New-WindowsPackage.ps1') -OutputDirectory $first -Version $Version -Commit $Commit -SourceDateEpoch $SourceDateEpoch
    & (Join-Path $repositoryRoot 'scripts\New-WindowsPackage.ps1') -OutputDirectory $second -Version $Version -Commit $Commit -SourceDateEpoch $SourceDateEpoch
    if ($LASTEXITCODE -ne 0) { throw "package build failed with exit code $LASTEXITCODE" }

    $archiveName = "multi-dap-$Version-windows-amd64.zip"
    $firstArchive = Join-Path $first $archiveName
    $firstChecksum = "$firstArchive.sha256"
    $secondArchive = Join-Path $second $archiveName
    $secondChecksum = "$secondArchive.sha256"
    $left = (Get-FileHash -Algorithm SHA256 -LiteralPath $firstArchive).Hash
    $right = (Get-FileHash -Algorithm SHA256 -LiteralPath $secondArchive).Hash
    if ($left -ne $right) { throw "package is not reproducible: $left != $right" }

    $expectedChecksum = $left.ToLowerInvariant() + "  " + $archiveName + "`n"
    $firstChecksumText = [System.IO.File]::ReadAllText($firstChecksum, [System.Text.UTF8Encoding]::new($false))
    $secondChecksumText = [System.IO.File]::ReadAllText($secondChecksum, [System.Text.UTF8Encoding]::new($false))
    if ($firstChecksumText -cne $expectedChecksum -or $secondChecksumText -cne $expectedChecksum) {
        throw 'detached archive checksum is missing, malformed, or not reproducible'
    }

    Assert-PackageArchive -ArchivePath $firstArchive -ExpectedVersion $Version -ExpectedCommit $Commit -ExpectedSourceDateEpoch $SourceDateEpoch
    Assert-PackageArchive -ArchivePath $secondArchive -ExpectedVersion $Version -ExpectedCommit $Commit -ExpectedSourceDateEpoch $SourceDateEpoch

    $extractDirectory = Join-Path $temporaryRoot 'version-smoke'
    Expand-Archive -LiteralPath $firstArchive -DestinationPath $extractDirectory
    $versionOutput = & (Join-Path $extractDirectory 'multi-dap.exe') version
    if ($LASTEXITCODE -ne 0) { throw "packaged multi-dap version failed with exit code $LASTEXITCODE" }
    $versionInfo = $versionOutput | ConvertFrom-Json
    $actualBuildDate = [System.DateTimeOffset]$versionInfo.build_date
    if ($versionInfo.version -cne $Version -or $actualBuildDate.ToUnixTimeSeconds() -ne $SourceDateEpoch) {
        throw "packaged version metadata mismatch: $versionOutput"
    }
    $actualCommit = if ($null -eq $versionInfo.PSObject.Properties['commit']) { '' } else { [string]$versionInfo.commit }
    if ($actualCommit -cne $Commit.ToLowerInvariant()) {
        throw "packaged commit metadata mismatch: $versionOutput"
    }

    Write-Output "reproducible SHA256 $left"
} finally {
    Remove-OwnedTemporaryDirectory -Path $temporaryRoot
}
