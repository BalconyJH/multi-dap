[CmdletBinding()]
param(
    [ValidateSet('Fast', 'Full')]
    [string]$Mode = 'Full',

    [ValidatePattern('^[0-9A-Za-z][0-9A-Za-z._-]*$')]
    [string]$Version = '0.0.0-acceptance',

    [ValidateRange(315532800, [long]::MaxValue)]
    [long]$SourceDateEpoch = 315532800,

    [ValidatePattern('^$|^[0-9A-Fa-f]{7,40}$')]
    [string]$Commit = '',

    [string]$MultiPython
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Add-ReleaseResult {
    param(
        [Parameter(Mandatory)] [AllowEmptyCollection()] [System.Collections.Generic.List[object]]$Results,
        [Parameter(Mandatory)] [string]$Name,
        [Parameter(Mandatory)] [ValidateSet('passed', 'failed', 'skipped')] [string]$Status,
        [Parameter(Mandatory)] [System.Diagnostics.Stopwatch]$Timer,
        [string]$Detail = ''
    )

    $Results.Add([pscustomobject]@{
            name = $Name
            status = $Status
            duration_seconds = [Math]::Round($Timer.Elapsed.TotalSeconds, 3)
            detail = $Detail
        })
}

function Invoke-ReleaseStep {
    param(
        [Parameter(Mandatory)] [AllowEmptyCollection()] [System.Collections.Generic.List[object]]$Results,
        [Parameter(Mandatory)] [string]$Name,
        [Parameter(Mandatory)] [scriptblock]$Action
    )

    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        & $Action
        $timer.Stop()
        Add-ReleaseResult -Results $Results -Name $Name -Status passed -Timer $timer
        Write-Output "PASS $Name ($([Math]::Round($timer.Elapsed.TotalSeconds, 3))s)"
    } catch {
        $timer.Stop()
        $detail = $_.Exception.Message
        Add-ReleaseResult -Results $Results -Name $Name -Status failed -Timer $timer -Detail $detail
        Write-Output "FAIL $Name ($([Math]::Round($timer.Elapsed.TotalSeconds, 3))s): $detail"
        throw
    }
}

function Skip-ReleaseStep {
    param(
        [Parameter(Mandatory)] [AllowEmptyCollection()] [System.Collections.Generic.List[object]]$Results,
        [Parameter(Mandatory)] [string]$Name,
        [Parameter(Mandatory)] [string]$Reason
    )

    $timer = [System.Diagnostics.Stopwatch]::StartNew()
    $timer.Stop()
    Add-ReleaseResult -Results $Results -Name $Name -Status skipped -Timer $timer -Detail $Reason
    Write-Output "SKIP ${Name}: $Reason"
}

function Invoke-NativeCommand {
    param(
        [Parameter(Mandatory)] [string]$FilePath,
        [Parameter(Mandatory)] [string[]]$Arguments,
        [Parameter(Mandatory)] [string]$Description
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "$Description failed with exit code $LASTEXITCODE"
    }
}

function Resolve-MultiPython {
    param([string]$RequestedPath)

    $candidates = @($RequestedPath, $env:MULTI_PYTHON)
    foreach ($installationVariable in 'MULTI_INSTALLATION', 'GHS_MULTI_ROOT') {
        $installation = [Environment]::GetEnvironmentVariable($installationVariable)
        if (-not [string]::IsNullOrWhiteSpace($installation)) {
            $candidates += (Join-Path $installation 'python\python.exe')
        }
    }
    foreach ($candidate in $candidates) {
        if (-not [string]::IsNullOrWhiteSpace($candidate) -and (Test-Path -LiteralPath $candidate -PathType Leaf)) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }
    return $null
}

function Get-GitHubActionUseDeclaration {
    param(
        [Parameter(Mandatory)] [AllowEmptyString()] [string]$Line,
        [Parameter(Mandatory)] [string]$Source
    )

    # YAML permits quoted mapping keys and whitespace before the colon.  Do not
    # silently skip a declaration merely because it uses one of those forms.
    $keyPattern = '^\s*(?:-\s*)?(?:uses|"uses"|''uses'')\s*:'
    if ($Line -notmatch $keyPattern) { return $null }

    $value = $Line.Substring($Matches[0].Length).TrimStart()
    if ($value -notmatch '^(?<reference>[^#\s]+)(?:\s+#\s*(?<version>v[^\s#]+))?\s*$') {
        throw "invalid uses declaration in ${Source}: $Line"
    }

    $reference = $Matches.reference
    # Action references may be YAML-quoted.  The supported reference grammar
    # has no quote characters, so remove only a matching outer pair and leave
    # all other input for the pin validator to reject.
    if (($reference.StartsWith('"') -and $reference.EndsWith('"')) -or
        ($reference.StartsWith("'") -and $reference.EndsWith("'"))) {
        $reference = $reference.Substring(1, $reference.Length - 2)
    }

    return [pscustomobject]@{
        reference = $reference
        version = if ($null -eq $Matches.version) { '' } else { $Matches.version }
    }
}

function Get-GitHubActionsYamlUsage {
    param(
        [Parameter(Mandatory)] [AllowEmptyCollection()] [string[]]$WorkflowPaths,
        [Parameter(Mandatory)] [AllowEmptyCollection()] [string[]]$ActionManifestPaths
    )

    # PyYAML is already locked as a transitive dependency of the documentation
    # toolchain.  Its parsed representation closes bypasses such as an escaped
    # quoted key (for example "u\\u0073es"), which cannot be recognized safely
    # by a line-oriented expression alone.
    $inspector = @'
import json
import sys
import yaml

workflow_count = int(sys.argv[1])
workflow_paths = sys.argv[2 : 2 + workflow_count]
action_paths = sys.argv[2 + workflow_count :]

def string_uses(value, path):
    if not isinstance(value, str):
        raise ValueError(f"uses must be a string in {path}")
    return value

def reject_pull_request_target(document, path):
    # PyYAML's YAML 1.1 resolver turns an unquoted `on` key into True, while
    # GitHub Actions treats it as the workflow trigger key.  Check both forms
    # so quoted, escaped, and ordinary YAML spellings have identical policy.
    if not isinstance(document, dict):
        raise ValueError(f"workflow root must be a mapping: {path}")
    trigger = document.get("on")
    if trigger is None and True in document:
        trigger = document[True]
    if isinstance(trigger, str):
        targets = [trigger]
    elif isinstance(trigger, list):
        targets = trigger
    elif isinstance(trigger, dict):
        targets = trigger.keys()
    elif trigger is None:
        targets = []
    else:
        raise ValueError(f"workflow trigger must be a string, sequence, or mapping: {path}")
    if any(target == "pull_request_target" for target in targets):
        raise ValueError(f"pull_request_target is prohibited: {path}")

def workflow_uses(document, path):
    reject_pull_request_target(document, path)
    jobs = document.get("jobs", {})
    if not isinstance(jobs, dict):
        raise ValueError(f"workflow jobs must be a mapping: {path}")
    result = []
    for job in jobs.values():
        if not isinstance(job, dict):
            continue
        if "uses" in job:
            result.append(string_uses(job["uses"], path))
        steps = job.get("steps", [])
        if not isinstance(steps, list):
            continue
        for step in steps:
            if isinstance(step, dict) and "uses" in step:
                result.append(string_uses(step["uses"], path))
    return result

def self_test_trigger_policy():
    # Keep adversarial forms in the inspector itself: they exercise the
    # current parser without adding an artificial workflow to the repository.
    for fixture in (
        "on: pull_request_target\njobs: {}\n",
        "on: [pull_request_target]\njobs: {}\n",
        "on: {pull_request_target: {types: [opened]}}\njobs: {}\n",
        '"o\\u006e": [pull_request_target]\njobs: {}\n',
    ):
        try:
            reject_pull_request_target(yaml.safe_load(fixture), "trigger-policy-self-test")
        except ValueError:
            continue
        raise RuntimeError("trigger-policy self-test did not reject pull_request_target")

self_test_trigger_policy()

def composite_uses(document, path):
    if not isinstance(document, dict):
        raise ValueError(f"action root must be a mapping: {path}")
    runs = document.get("runs", {})
    if not isinstance(runs, dict) or runs.get("using") != "composite":
        return None
    steps = runs.get("steps", [])
    if not isinstance(steps, list):
        raise ValueError(f"composite action runs.steps must be a sequence: {path}")
    return [string_uses(step["uses"], path) for step in steps if isinstance(step, dict) and "uses" in step]

for path in workflow_paths:
    with open(path, encoding="utf-8") as handle:
        print(json.dumps({"path": path, "uses": workflow_uses(yaml.safe_load(handle), path)}))
for path in action_paths:
    with open(path, encoding="utf-8") as handle:
        uses = composite_uses(yaml.safe_load(handle), path)
    if uses is not None:
        print(json.dumps({"path": path, "uses": uses}))
'@
    $docsProject = Join-Path $repositoryRoot 'docs'
    $output = @(& uv run --no-python-downloads --project $docsProject --locked --python 3.12 python -c $inspector $WorkflowPaths.Count @($WorkflowPaths + $ActionManifestPaths))
    if ($LASTEXITCODE -ne 0) { throw "unable to inspect GitHub Actions YAML with the locked documentation environment (exit code $LASTEXITCODE)" }
    return @($output | ForEach-Object { $_ | ConvertFrom-Json })
}

function Remove-OwnedTemporaryDirectory {
    param([Parameter(Mandatory)] [string]$Path)

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) { return }
    $resolvedPath = (Resolve-Path -LiteralPath $Path).Path.TrimEnd('\', '/')
    $temporaryRoot = ([System.IO.Path]::GetTempPath()).TrimEnd('\', '/')
    $prefix = $temporaryRoot + [System.IO.Path]::DirectorySeparatorChar
    if (-not $resolvedPath.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase) -or
        -not ((Split-Path -Leaf $resolvedPath).StartsWith('multi-dap-release-acceptance-', [System.StringComparison]::Ordinal))) {
        throw "refusing to remove a directory not owned by release acceptance: $resolvedPath"
    }
    Remove-Item -LiteralPath $resolvedPath -Recurse -Force
}

$repositoryRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$results = [System.Collections.Generic.List[object]]::new()
$overallTimer = [System.Diagnostics.Stopwatch]::StartNew()
$success = $false
$failure = $null
$previousProxy = $env:GOPROXY
$previousPythonDontWriteBytecode = $env:PYTHONDONTWRITEBYTECODE
$previousLocation = Get-Location
$compileDirectory = $null

try {
    Set-Location -LiteralPath $repositoryRoot
    # Release acceptance must use only the existing module cache and must not edit go.mod/go.sum.
    $env:GOPROXY = 'off'
    $env:PYTHONDONTWRITEBYTECODE = '1'

    Invoke-ReleaseStep -Results $results -Name 'go-format' -Action {
        $unformatted = @(& gofmt -l .)
        if ($LASTEXITCODE -ne 0) { throw "gofmt failed with exit code $LASTEXITCODE" }
        if ($unformatted.Count -ne 0) {
            throw "Go files are not formatted: $($unformatted -join ', ')"
        }
    }
    Invoke-ReleaseStep -Results $results -Name 'go-test' -Action {
        Invoke-NativeCommand -FilePath 'go' -Arguments @('test', '-mod=readonly', './...') -Description 'go test'
    }
    Invoke-ReleaseStep -Results $results -Name 'go-vet' -Action {
        Invoke-NativeCommand -FilePath 'go' -Arguments @('vet', '-mod=readonly', './...') -Description 'go vet'
    }
    Invoke-ReleaseStep -Results $results -Name 'go-build' -Action {
        Invoke-NativeCommand -FilePath 'go' -Arguments @('build', '-mod=readonly', '-trimpath', '-buildvcs=false', './...') -Description 'go build'
    }
    Invoke-ReleaseStep -Results $results -Name 'github-actions-policy' -Action {
        $lockPath = Join-Path $repositoryRoot '.github\actions-lock.json'
        if (-not (Test-Path -LiteralPath $lockPath -PathType Leaf)) {
            throw '.github/actions-lock.json is missing'
        }
        $lock = Get-Content -LiteralPath $lockPath -Raw | ConvertFrom-Json
        if ($lock.schema -cne 'multi-dap.github-actions-lock/v1' -or $null -eq $lock.actions) {
            throw '.github/actions-lock.json has an invalid schema'
        }
        $locked = @{}
        foreach ($property in $lock.actions.PSObject.Properties) {
            $entry = $property.Value
            if ($property.Name -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*$' -or
                $entry.version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$' -or
                $entry.sha -notmatch '^[0-9a-f]{40}$') {
                throw "invalid action lock entry: $($property.Name)"
            }
            $locked[$property.Name] = $entry
        }
        $seen = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
        $workflowFiles = @(Get-ChildItem -LiteralPath (Join-Path $repositoryRoot '.github\workflows') -File -Include '*.yml', '*.yaml')
        if ($workflowFiles.Count -eq 0) { throw 'no GitHub Actions workflows were found' }
        # Workflow trigger policy is checked by the semantic YAML inspector
        # below, including YAML 1.1's unquoted `on` behavior.
        $actionManifestPaths = [System.Collections.Generic.List[string]]::new()

        # Composite actions can invoke remote actions too.  Discover action
        # manifests from tracked and unignored files so a newly added local
        # action is validated before it is committed.
        $trackedFiles = @(& git ls-files --cached --others --exclude-standard)
        if ($LASTEXITCODE -ne 0) { throw "git ls-files failed with exit code $LASTEXITCODE" }
        foreach ($relativePath in $trackedFiles) {
            $leafName = [System.IO.Path]::GetFileName($relativePath)
            if ($leafName -cne 'action.yml' -and $leafName -cne 'action.yaml') { continue }
            $manifestPath = Join-Path $repositoryRoot $relativePath
            if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { continue }
            $actionManifestPaths.Add($manifestPath)
        }

        $sources = @(Get-GitHubActionsYamlUsage -WorkflowPaths @($workflowFiles.FullName) -ActionManifestPaths @($actionManifestPaths))
        if ($sources.Count -lt $workflowFiles.Count) { throw 'GitHub Actions YAML inspection omitted a workflow' }

        foreach ($source in $sources) {
            $expectedReferences = @($source.uses)
            $displayPath = [System.IO.Path]::GetRelativePath($repositoryRoot, $source.path)
            $declarations = [System.Collections.Generic.List[object]]::new()
            $lineNumber = 0
            foreach ($line in [System.IO.File]::ReadLines($source.path)) {
                $lineNumber++
                $declaration = Get-GitHubActionUseDeclaration -Line $line -Source "${displayPath}:$lineNumber"
                if ($null -eq $declaration) { continue }
                $declarations.Add([pscustomobject]@{ declaration = $declaration; line = $lineNumber })
            }
            $actualReferences = @($declarations | ForEach-Object { $_.declaration.reference } | Sort-Object)
            if ($actualReferences.Count -ne $expectedReferences.Count -or
                (Compare-Object -CaseSensitive $actualReferences @($expectedReferences | Sort-Object))) {
                throw "uses declarations do not match parsed GitHub Actions YAML: $displayPath"
            }
            foreach ($item in $declarations) {
                $declaration = $item.declaration
                $lineNumber = $item.line
                $reference = $declaration.reference
                if ($reference.StartsWith('./', [System.StringComparison]::Ordinal)) { continue }
                if ($reference -notmatch '^(?<name>[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*)@(?<sha>[0-9a-f]{40})$') {
                    throw "action is not pinned to a full lowercase commit SHA in ${displayPath}:${lineNumber}: $reference"
                }
                $name = $Matches.name
                $sha = $Matches.sha
                if (-not $locked.ContainsKey($name)) {
                    throw "action is absent from .github/actions-lock.json: $name"
                }
                if ($locked[$name].sha -cne $sha) {
                    throw "action SHA differs from .github/actions-lock.json: $name"
                }
                if ($declaration.version -cne $locked[$name].version) {
                    throw "action version comment differs from .github/actions-lock.json: $name"
                }
                [void]$seen.Add($name)
            }
        }
        $unused = @($locked.Keys | Where-Object { -not $seen.Contains($_) } | Sort-Object)
        if ($unused.Count -ne 0) {
            throw "unused action lock entries: $($unused -join ', ')"
        }
    }
    Invoke-ReleaseStep -Results $results -Name 'third-party-licenses' -Action {
        $goRoot = (& go env GOROOT).Trim()
        $moduleCache = (& go env GOMODCACHE).Trim()
        if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($goRoot) -or [string]::IsNullOrWhiteSpace($moduleCache)) {
            throw 'unable to resolve Go license locations'
        }
        $licensePairs = @(
            @((Join-Path $repositoryRoot 'third_party\go\LICENSE'), (Join-Path $goRoot 'LICENSE')),
            @((Join-Path $repositoryRoot 'third_party\go-toml\LICENSE'), (Join-Path $moduleCache 'github.com\pelletier\go-toml\v2@v2.4.3\LICENSE'))
        )
        foreach ($pair in $licensePairs) {
            if (-not (Test-Path -LiteralPath $pair[0] -PathType Leaf) -or -not (Test-Path -LiteralPath $pair[1] -PathType Leaf)) {
                throw "required third-party license is missing: $($pair -join ', ')"
            }
            $checkedIn = ([System.IO.File]::ReadAllText($pair[0]) -replace "`r`n", "`n").TrimEnd("`n")
            $upstream = ([System.IO.File]::ReadAllText($pair[1]) -replace "`r`n", "`n").TrimEnd("`n")
            if ($checkedIn -cne $upstream) {
                throw "third-party license text drifted from the pinned dependency: $($pair[0])"
            }
        }
    }
    Invoke-ReleaseStep -Results $results -Name 'release-policy-structure' -Action {
        $rootLicense = Join-Path $repositoryRoot 'LICENSE'
        $packageLicense = Join-Path $repositoryRoot 'packaging\LICENSE.txt'
        $policyPath = Join-Path $repositoryRoot 'packaging\release-policy.json'
        if (-not (Test-Path -LiteralPath $rootLicense -PathType Leaf) -or
            -not (Test-Path -LiteralPath $packageLicense -PathType Leaf) -or
            -not (Test-Path -LiteralPath $policyPath -PathType Leaf)) {
            throw 'release license or policy file is missing'
        }
        $policy = Get-Content -LiteralPath $policyPath -Raw | ConvertFrom-Json
        if ($policy.schema -cne 'multi-dap.release-policy/v1' -or
            $policy.public_distribution_approved -isnot [bool] -or
            $policy.license -notmatch '^[A-Za-z0-9][A-Za-z0-9 .()+/_-]{1,127}$') {
            throw 'packaging/release-policy.json is invalid'
        }
        $rootText = ([System.IO.File]::ReadAllText($rootLicense) -replace "`r`n", "`n")
        $packageText = ([System.IO.File]::ReadAllText($packageLicense) -replace "`r`n", "`n")
        if ($rootText -cne $packageText) {
            throw 'LICENSE and packaging/LICENSE.txt must be identical'
        }
        if ($policy.public_distribution_approved) {
            if ($policy.license_file_sha256 -notmatch '^[0-9A-Fa-f]{64}$') {
                throw 'approved public distribution requires a license_file_sha256'
            }
            $actualLicenseHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $rootLicense).Hash
            if ($actualLicenseHash -cne $policy.license_file_sha256.ToUpperInvariant()) {
                throw 'approved release-policy digest does not match LICENSE'
            }
        }
        Write-Output "release policy public_distribution_approved=$($policy.public_distribution_approved.ToString().ToLowerInvariant())"
    }
    Invoke-ReleaseStep -Results $results -Name 'bridge-host-tests' -Action {
        Invoke-NativeCommand -FilePath 'uv' -Arguments @('run', '--offline', '--no-python-downloads', '--no-project', '--python', '3.12', '-m', 'unittest', 'bridge.test_bridge') -Description 'bridge host tests'
    }
    Invoke-ReleaseStep -Results $results -Name 'recon-host-tests' -Action {
        Invoke-NativeCommand -FilePath 'uv' -Arguments @('run', '--offline', '--no-python-downloads', '--no-project', '--python', '3.12', '-m', 'unittest', 'discover', '-s', 'recon/tests') -Description 'recon host tests'
    }
    Invoke-ReleaseStep -Results $results -Name 'vscode-extension' -Action {
        Push-Location -LiteralPath (Join-Path $repositoryRoot 'editors\vscode')
        try {
            Invoke-NativeCommand -FilePath 'npm' -Arguments @('test') -Description 'VS Code extension tests'
            Invoke-NativeCommand -FilePath 'npm' -Arguments @('run', 'lint') -Description 'VS Code extension lint'
            Invoke-NativeCommand -FilePath 'npm' -Arguments @('run', 'package-contents') -Description 'VS Code extension package contents'
        } finally {
            Pop-Location
        }
    }
    Invoke-ReleaseStep -Results $results -Name 'repository-language-tests' -Action {
        Invoke-NativeCommand -FilePath 'uv' -Arguments @('run', '--offline', '--no-python-downloads', '--no-project', '--python', '3.12', '-m', 'unittest', 'tools.test_validate_repository_language') -Description 'repository language validator tests'
    }
    Invoke-ReleaseStep -Results $results -Name 'release-changelog-tests' -Action {
        Invoke-NativeCommand -FilePath 'uv' -Arguments @('run', '--offline', '--no-python-downloads', '--no-project', '--python', '3.12', '-m', 'unittest', 'tools.test_validate_release_changelog') -Description 'release changelog validator tests'
    }
    Invoke-ReleaseStep -Results $results -Name 'english-repository' -Action {
        Invoke-NativeCommand -FilePath 'uv' -Arguments @('run', '--offline', '--no-python-downloads', '--no-project', '--python', '3.12', '-m', 'tools.validate_repository_language', '--repository', $repositoryRoot) -Description 'repository language validation'
    }
    Invoke-ReleaseStep -Results $results -Name 'release-changelog' -Action {
        Invoke-NativeCommand -FilePath 'uv' -Arguments @('run', '--offline', '--no-python-downloads', '--no-project', '--python', '3.12', '-m', 'tools.validate_release_changelog', '--version', $Version, '--changelog', (Join-Path $repositoryRoot 'CHANGELOG.md')) -Description 'release changelog validation'
    }
    Invoke-ReleaseStep -Results $results -Name 'documentation' -Action {
        Invoke-NativeCommand -FilePath 'uv' -Arguments @('run', '--project', 'docs', '--locked', '--python', '3.12', 'zensical', 'build', '--clean', '--strict') -Description 'strict Zensical documentation build'
    }

    if ($Mode -eq 'Full') {
        Invoke-ReleaseStep -Results $results -Name 'go-test-race' -Action {
            Invoke-NativeCommand -FilePath 'go' -Arguments @('test', '-mod=readonly', '-race', './...') -Description 'go test -race'
        }

        $multiPythonPath = Resolve-MultiPython -RequestedPath $MultiPython
        if ($null -eq $multiPythonPath) {
            Skip-ReleaseStep -Results $results -Name 'multi-python2-py-compile' -Reason 'MULTI Python 2 was not configured; pass -MultiPython or set MULTI_PYTHON'
        } else {
            Invoke-ReleaseStep -Results $results -Name 'multi-python2-py-compile' -Action {
                $version = & $multiPythonPath --version 2>&1
                if ($LASTEXITCODE -ne 0 -or ($version -notmatch 'Python 2\.')) {
                    throw "MULTI Python interpreter is not usable as Python 2: $multiPythonPath ($version)"
                }
                $compileDirectory = Join-Path ([System.IO.Path]::GetTempPath()) ("multi-dap-release-acceptance-" + [guid]::NewGuid().ToString('N'))
                [System.IO.Directory]::CreateDirectory($compileDirectory) | Out-Null
                $sources = @(
                    (Join-Path $repositoryRoot 'bridge\bridge.py'),
                    (Join-Path $repositoryRoot 'recon\lib\reconlib.py'),
                    (Join-Path $repositoryRoot 'recon\run.py'),
                    (Join-Path $repositoryRoot 'recon\notifier.py')
                ) + @(Get-ChildItem -LiteralPath (Join-Path $repositoryRoot 'recon\probes') -Filter '*.py' -File | Sort-Object FullName | ForEach-Object FullName)
                $compiler = 'import os,py_compile,sys; out=sys.argv[1]; [py_compile.compile(path, os.path.join(out, str(index) + ".pyc"), doraise=True) for index,path in enumerate(sys.argv[2:])]'
                Invoke-NativeCommand -FilePath $multiPythonPath -Arguments (@('-B', '-c', $compiler, $compileDirectory) + $sources) -Description 'MULTI Python 2 py_compile'
            }
        }

        Invoke-ReleaseStep -Results $results -Name 'windows-package-reproducibility' -Action {
            & (Join-Path $repositoryRoot 'scripts\Test-WindowsPackage.ps1') -Version $Version -Commit $Commit -SourceDateEpoch $SourceDateEpoch
        }
        Invoke-ReleaseStep -Results $results -Name 'winget-manifest' -Action {
            & (Join-Path $repositoryRoot 'scripts\Test-WinGetManifest.ps1')
        }
    } else {
        Skip-ReleaseStep -Results $results -Name 'go-test-race' -Reason 'Fast mode omits race instrumentation'
        Skip-ReleaseStep -Results $results -Name 'multi-python2-py-compile' -Reason 'Fast mode omits MULTI Python 2 compilation'
        Skip-ReleaseStep -Results $results -Name 'windows-package-reproducibility' -Reason 'Fast mode omits the two-build package check'
        Skip-ReleaseStep -Results $results -Name 'winget-manifest' -Reason 'Fast mode omits WinGet manifest validation'
    }

    $success = $true
} catch {
    $failure = $_.Exception.Message
} finally {
    if ($null -ne $compileDirectory) {
        Remove-OwnedTemporaryDirectory -Path $compileDirectory
    }
    $env:GOPROXY = $previousProxy
    $env:PYTHONDONTWRITEBYTECODE = $previousPythonDontWriteBytecode
    Set-Location -LiteralPath $previousLocation
    $overallTimer.Stop()
    $result = [pscustomobject]@{
        schema_version = 1
        mode = $Mode.ToLowerInvariant()
        success = $success
        duration_seconds = [Math]::Round($overallTimer.Elapsed.TotalSeconds, 3)
        failure = $failure
        steps = @($results)
    }
    Write-Output ('RELEASE_ACCEPTANCE_RESULT ' + ($result | ConvertTo-Json -Compress -Depth 4))
}

if (-not $success) { exit 1 }
