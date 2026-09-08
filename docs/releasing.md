# Release Guide

The first supported distribution target is a deterministic Windows amd64 ZIP.
Release publication is separate from hardware qualification: the release notes
must repeat every relevant boundary from the [capability matrix](capabilities.md).

## Current release prerequisites

The public repository and packaged binary use the MIT License. `LICENSE` and
`packaging/LICENSE.txt` are identical, and
`packaging/release-policy.json` positively approves public MIT distribution
while binding that exact text by SHA-256. The `0.1.0` release scope accepts the
limitations already recorded in the capability matrix: it must not claim an
actual breakpoint hit, complete execution-control qualification, or full
stopped-target disconnect-safety qualification. Those evidence limits do not
block distribution of the explicitly bounded feature set.

The remaining prerequisites are:

1. The release must be built from a reviewed, committed revision. A dirty or
   partially untracked working tree is not a release source.
2. MULTI Python 2 compilation should pass on the release host, or its explicit
   skip must be accepted and recorded by the release owner.
3. The canonical public GitHub repository must record a successful hosted CI
   run with the required rulesets and protected environments in place.

For any future license-text change, update both license copies together,
calculate `Get-FileHash -Algorithm SHA256 .\LICENSE`, and place the lowercase
digest and exact package-manager license name in
`packaging/release-policy.json`. Do not leave public distribution approved
unless the release owner has reviewed those exact files.

## Version policy

- Git tags are the release version source and use `vMAJOR.MINOR.PATCH` with an
  optional Semantic Versioning prerelease suffix.
- Artifact names omit the leading `v`, for example
  `multi-dap-0.1.0-windows-amd64.zip`.
- DAP and MBP protocol versions are independent compatibility contracts and do
  not inherit the product tag.
- Update `CHANGELOG.md` before creating a stable tag. Release acceptance
  requires exactly one matching `## [MAJOR.MINOR.PATCH]` heading and link
  definition; prerelease candidates may use the unique `Unreleased` section.

## Build a local candidate

```powershell
$version = '0.1.0-rc.1'
$epoch = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
.\scripts\Invoke-WindowsReleaseAcceptance.ps1 -Mode Full -Version $version -SourceDateEpoch $epoch
New-Item -ItemType Directory -Path .\dist
.\scripts\New-WindowsPackage.ps1 -OutputDirectory .\dist -Version $version -SourceDateEpoch $epoch
Get-FileHash -Algorithm SHA256 .\dist\multi-dap-$version-windows-amd64.zip
```

Use the commit timestamp as `SourceDateEpoch` in automation. Repeating the
build with the same source, toolchain, module cache, version, and epoch must
produce the same archive digest.

## Automated pipeline

The CI workflow separates Go, Python, editor, documentation, workflow-policy,
live Go vulnerability, and packaging checks. The package job runs only after
all focused checks pass. The same pinned `actionlint` and `govulncheck` tools
run again before a tagged candidate is accepted.

The release workflow is tag-driven and has two security boundaries:

1. A Windows candidate job validates the tag, runs full release acceptance,
   builds the archive, writes a detached archive checksum, and uploads an
   immutable workflow artifact.
2. A separate no-checkout job attests the archive, detached checksum, and all
   three WinGet manifests when the repository is public.
3. A publish job downloads that exact artifact and creates a draft GitHub
   release. Only this job receives `contents: write`.

The draft is intentionally not public. Configure the GitHub `release`
environment for candidate review and the `production` environment with required
reviewers before the first public release. Enable immutable releases before
making any prerelease or stable release public; it must protect the first public
asset, not merely later publication.

Apply the complete [GitHub repository configuration](github-configuration.md),
including default-branch and `v*` tag rulesets, immutable releases, private
vulnerability reporting, and least-privilege Actions settings.

Ordinary CI does not upload its test package. In this public repository, an
approved tag run uploads the candidate for attestation and creates a draft only
while `public_distribution_approved` is true and its license digest matches.
Changing either gate causes the workflow to withhold the Actions artifact and
draft release.

The manually dispatched `Publish release` workflow is the only automated path
that turns a draft public. It requires the tag to be entered twice, checks out
that exact tag, validates the positive MIT distribution policy, requires the
root and packaged licenses to match its approved digest, verifies the detached
archive checksum and GitHub provenance attestation, and then publishes the
existing draft without rebuilding it. Publication also requires a public
repository. A maintainer must still review release notes, hardware claims, and
the WinGet candidate before approving the protected environment.

## Release assets

The expected assets are:

```text
multi-dap-<version>-windows-amd64.zip
multi-dap-<version>-windows-amd64.zip.sha256
Tacrolimus.multi-dap.yaml
Tacrolimus.multi-dap.installer.yaml
Tacrolimus.multi-dap.locale.en-US.yaml
```

The outer checksum protects the downloaded archive. The archive's internal
`SHA256SUMS.txt` independently covers every payload file except itself, and
`release-provenance.json` binds the tagged source, toolchain, target, build
time, and executable digest. The three WinGet files are candidates for review;
they are not submitted automatically.

## Incident response and rollback

Treat a suspected compromised build, signing identity, GitHub environment,
release asset, or published package as a release incident. Stop the affected
draft/publication workflow and pause any pending WinGet submission immediately.
Do not delete, mutate, retag, or reuse the affected tag or immutable release
assets: preserve the release, workflow runs, provenance, checksums, logs, and
the local evidence needed to investigate it.

Publish a corrected patch as a new version from a new reviewed tag after the
cause is contained. If credentials or trust boundaries may be involved, revoke
and rotate the affected credentials; review and remove compromised workload
identities, environment approvals, repository access, and automation tokens
before resuming publication. Coordinate a security advisory or other clear
maintainer communication that identifies affected versions, impact, mitigation,
and the replacement version. Withdraw or supersede the corresponding WinGet
submission through its review process rather than changing the published
artifact in place.

Record the incident decision and the replacement version in `CHANGELOG.md` and
the release notes. The [verification evidence index](verification-evidence.md)
defines what local checks prove and what must be re-run from the new tag.

## WinGet

WinGet is the only appropriate package-manager target for the current runtime.
After the first public GitHub release:

1. Run the manual `Package-manager staging` workflow for the exact stable tag.
   It accepts only a public, published, immutable release; verifies all five
   assets, checksums, provenance, MIT license, and attestations; and regenerates
   the three manifests byte-for-byte.
2. Require its fresh `windows-2025` job to validate and install the local
   manifest, verify `multi-dap version` and the executable-relative bridge, then
   uninstall and confirm PATH removal.
3. Review the staging evidence artifact and submit the verified manifest set to
   `microsoft/winget-pkgs` through an approved pull request. The workflow never
   writes to that repository itself.
4. After two stable versions exist, add or perform the separate clean-host
   upgrade test; one release cannot prove upgrade compatibility.

WinGet publication requires a public stable URL and therefore cannot be fully
validated before a release is published. The repository keeps a manifest
generator, CI validation, and post-release staging path, not a fabricated
version/hash pair.

## Why uv and Homebrew are deferred

`uv` is used to run host tests and the locked, non-package documentation
environment, but this repository is not a Python application package.
The production bridge requires MULTI's Python 2.7 and proprietary modules that
cannot be declared as ordinary PyPI dependencies. A future `uv tool install`
surface would need a separate supported Python 3 launcher with signed binary
download and verification logic.

Homebrew targets macOS and Linux, while the production launcher intentionally
returns unsupported on non-Windows systems. Publishing a formula would install
a binary that cannot start its backend. Homebrew remains deferred until a real
non-Windows runtime is implemented and qualified.
