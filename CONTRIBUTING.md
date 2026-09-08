# Contributing to multi-dap

Thank you for helping improve `multi-dap`. The adapter controls embedded debug
sessions, so changes must preserve its fail-closed behavior and its explicit
evidence boundaries.

Participation is governed by the repository's
[Code of Conduct](CODE_OF_CONDUCT.md).

## Before you start

1. Read the [architecture](docs/architecture.md), especially the Session Actor,
   lifecycle, network binding, and error-handling sections.
2. Check the [capability matrix](docs/capabilities.md). Do not describe a
   software-only or unverified behavior as hardware-qualified.
3. Keep the repository, user-facing messages, tests, and documentation in
   English.
4. Never commit target connection arguments, control tokens, private ELF paths,
   raw MULTI output, captures, or hardware identifiers.

## Development workflow

The full local workflow is documented in [docs/development.md](docs/development.md).
At minimum, run:

```powershell
gofmt -w <changed-go-files>
go test -mod=readonly ./...
go vet -mod=readonly ./...
go test -mod=readonly -race ./...
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest bridge.test_bridge
uv run --offline --no-python-downloads --no-project --python 3.12 -m unittest discover -s recon/tests
```

If editor or packaging files changed, also run their focused checks and the
Windows release acceptance script described in the developer guide.

## Change requirements

- Add or update tests for observable behavior.
- Keep DAP capabilities synchronized with production wiring. Parsing a request
  is not sufficient reason to advertise support.
- Keep bridge messages bounded and sanitized; raw target diagnostics must not
  cross trust boundaries accidentally.
- Preserve loopback-only listeners and authenticated proxy upgrade behavior.
- Update the capability matrix, architecture, and changelog when behavior or
  evidence status changes.
- Separate host-test evidence from real-target evidence in both code review and
  documentation.

## Pull requests

Explain the user-visible outcome, affected safety boundary, verification run,
and any evidence that remains unavailable. A pull request should not bundle
unrelated refactors with a behavior change.

The repository is licensed under MIT. By submitting a contribution, you agree
that it may be distributed under the repository's MIT License. Do not submit
code or artifacts that you lack permission to contribute.
