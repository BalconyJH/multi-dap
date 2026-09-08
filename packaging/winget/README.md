# WinGet Packaging

`multi-dap` is distributed as a ZIP containing a portable executable and its
required `bridge/bridge.py` companion. The generated WinGet installer manifest
uses `ArchiveBinariesDependOnPath: true`, so WinGet keeps the extracted bundle
together and places its install directory on `PATH`.

Generate a manifest only after the exact release archive and its SHA-256 are
known:

```powershell
.\scripts\New-WinGetManifest.ps1 `
  -Version 0.1.0 `
  -InstallerUrl https://github.com/Tacrolimus/multi-dap/releases/download/v0.1.0/multi-dap-0.1.0-windows-amd64.zip `
  -InstallerSha256 <64-hex-characters> `
  -OutputDirectory .\dist\winget `
  -License MIT
```

MIT is the repository and package-manager license. Never submit placeholder
URLs or hashes.

The generator emits the required multi-file manifest set using WinGet schema
1.12.0. Validate the generated directory with the current Microsoft tooling,
then submit it to `microsoft/winget-pkgs` through a reviewed pull request.

- [Manifest authoring documentation](https://github.com/microsoft/winget-pkgs/tree/master/doc/manifest)
- [WinGet Create](https://github.com/microsoft/winget-create)

Run the repository-owned structural regression test with:

```powershell
.\scripts\Test-WinGetManifest.ps1
```

The test checks deterministic fields and encoding, but it does not replace
Microsoft's repository validation or a clean-host install and uninstall test.

## Post-release staging

After a stable release has been published, repository maintainers manually run
the `Package-manager staging` GitHub workflow with its exact `vX.Y.Z` tag. It
is a review gate only: it never submits a pull request or writes to
`microsoft/winget-pkgs`.

The workflow fails closed unless the repository is public and the selected
release is published, non-draft, non-prerelease, and marked immutable by
GitHub. It resolves and cleanly checks out the exact tag commit, downloads only
the five expected release assets, and verifies:

- GitHub's SHA-256 asset digest and the detached archive checksum;
- build attestations for every release asset, tied to the release tag and
  commit;
- the ZIP payload allowlist, internal checksums, MIT license, and provenance;
- byte-for-byte equality between the published manifests and freshly generated
  manifests from the verified archive URL and digest.

It uploads only review evidence: checksums, provenance, attestation result,
and the published and regenerated YAML manifests. It does not upload a second
copy of release binaries.

A dependent `windows-2025` job treats the runner as a clean host. It requires
`winget`, validates the local downloaded manifest, installs from that local
manifest after explicitly enabling WinGet's `LocalManifestFiles` setting, uses
user-scope and noninteractive flags when the installed WinGet version supports
them, verifies `multi-dap version` reports the exact release version,
verifies the executable-relative `bridge/bridge.py` exists and matches the
released archive, uninstalls the exact package ID, confirms that `multi-dap` no
longer resolves through `PATH`, and disables the temporary local-manifest
setting in an always-run cleanup step.

Do not infer upgrade compatibility from this one-release test. A reliable
upgrade test needs an earlier published `Tacrolimus.multi-dap` version and is a
future two-release gate: install the prior stable package on a fresh runner,
upgrade from the reviewed local manifest, then verify version, PATH, and
uninstall behavior. The staging workflow deliberately does not fabricate that
evidence before two public releases exist.

Before a reviewed submission, specifically confirm that uninstall removes the
package's install directory from `PATH`. This has been reported as an open
upstream edge case for `ArchiveBinariesDependOnPath` packages in
[winget-cli issue 6160](https://github.com/microsoft/winget-cli/issues/6160).
