## Outcome

Describe the user-visible or architectural result.

## Safety and evidence boundary

- Which target-control, authentication, path, lifecycle, or packaging boundary changes?
- Is the evidence host-only, protocol-capture, or real-target evidence?
- Which behavior remains unsupported or unverified?

## Verification

- [ ] Changed Go files are formatted.
- [ ] Go tests, vet, race tests, and builds pass.
- [ ] Relevant Python and editor checks pass.
- [ ] Strict documentation builds and remains English-only.
- [ ] Packaging checks pass when release files change.
- [ ] Capability, architecture, and changelog documentation is synchronized.
- [ ] No private target data, token, connection argument, raw capture, or local path is included.
