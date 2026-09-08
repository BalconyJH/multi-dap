# GitHub Repository Configuration

Repository files define the build and publication logic, but GitHub-side access
controls complete the release trust boundary. The repository is public; apply
and verify this checklist before publishing any release.

## Repository identity and visibility

- Use the canonical repository identity `Tacrolimus/multi-dap`, matching the Go
  module and generated WinGet package metadata. If ownership changes, update all
  three together in a reviewed change.
- Keep repository visibility public. The positive release policy is approved
  for MIT distribution and binds the exact license text by SHA-256.
- Enable private vulnerability reporting before inviting external users.
- Enable immutable releases before creating any public prerelease or stable
  release. Do not rely on a later settings change to protect an already-public
  asset.

Record the final repository URL as a Git remote and run CI from a pushed branch
before creating a release tag. Local success is not evidence that hosted-runner
permissions, caches, or platform images are configured correctly.

## Default branch ruleset

Protect the default branch with:

- pull requests required for changes;
- at least one approving review and CODEOWNERS review for owned paths;
- dismissal of stale approvals after new commits;
- required resolution of review conversations;
- required status checks with branches up to date;
- blocked force pushes and deletion;
- a narrowly controlled bypass list.

Require these nine CI checks after their first successful hosted run records
the exact names:

- `Go (ubuntu-24.04)`;
- `Go (windows-2025)`;
- `Python host tests (ubuntu-24.04)`;
- `Python host tests (windows-2025)`;
- `VS Code extension`;
- `Documentation`;
- `Go vulnerability audit`;
- `GitHub Actions policy`;
- `Windows release candidate`.

Do not enable merge queue unless CI is extended with a `merge_group` trigger
and the resulting merge-queue checks have been observed and added to this
ruleset. A `pull_request`-only required check cannot validate the temporary
merge-group commit.

## Release tag ruleset

Create a tag ruleset targeting `v*` that:

- restricts tag creation to release maintainers;
- blocks tag updates and deletion;
- uses no broad bypass permission;
- requires a reviewed default-branch commit as the release source;
- requires signed tags if the organization has a managed signing policy.

The workflows verify the tag ref, resolved commit, archive provenance, and
attestation at one point in time. Preventing later tag mutation is a repository
setting and cannot be implemented by workflow YAML alone.

## Environments

Create two protected environments with no deployment secrets:

| Environment | Used by | Required configuration |
|---|---|---|
| `release` | Tag workflow creates a draft release | Required reviewer, prevent self-review, deployments limited to protected `v*` tags. |
| `production` | Manual workflow publishes the existing draft | Independent required reviewer, prevent self-review, deployments limited to the protected default branch (`master`), and the three release-protection variables below. |

In the **`production` environment's Variables** settings (not repository-level
variables and not secrets), set all three values to the literal lowercase
`true` only after an administrator has independently verified the matching
GitHub setting:

| Variable | Required value | Administrator acknowledgement |
|---|---|---|
| `RELEASE_IMMUTABILITY_ENABLED` | `true` | GitHub immutable releases are enabled. |
| `RELEASE_TAG_RULESET_ENABLED` | `true` | A `v*` tag ruleset restricts creation to release maintainers and forbids tag updates and deletion. |
| `RELEASE_DEFAULT_BRANCH_PROTECTED` | `true` | The default-branch rules require pull requests, review, and CI; forbid force pushes and deletion; and tightly limit bypasses. |

The publish workflow fails closed before it changes a draft when any variable
is missing or differs from `true`. It also makes its own live REST check that
the dynamic default branch reports as protected; the acknowledgement does not
replace that check. After publication it performs a bounded, read-only retry
and requires the release to report the requested tag, the requested tag to
resolve to the verified commit, the expected prerelease state, `draft: false`,
and `immutable: true`. It also rechecks the exact five-asset allowlist and each
immutable asset's name, byte size, and SHA-256 digest against the already
attested local copy. It does not rebuild, replace, or mutate assets during that
verification.

The environments must approve the exact workflow run; publication never
rebuilds the candidate. Do not store Green Hills credentials, target connection
arguments, or a broad GitHub personal access token in either environment.

Environment rules do not create a release role. A person who can dispatch the
manual workflow or change workflows can influence publication, and a required
reviewer only approves a deployment after the workflow has reached that gate.
Create a small, separately managed release-maintainers team and grant workflow
dispatch, tag creation, environment-review, and release-editing authority only
to that team. Keep ordinary contributors outside that role. Configure at least
two trusted people or teams as reviewers: `@Tacrolimus` alone cannot provide
independent review or continuity. Environment reviewers should not be treated
as a substitute for default-branch protection, tag protection, or restrictive
repository roles.

## Actions policy

- Keep the default `GITHUB_TOKEN` permission read-only at repository level.
- Allow only GitHub-owned actions plus the reviewed `astral-sh/setup-uv` action,
  or use an equally narrow organization policy.
- Enforce full 40-character commit-SHA references for every third-party and
  GitHub-owned action. The repository's native policy checks every external
  `uses:` reference against `.github/actions-lock.json`; do not weaken it to
  tag-only pinning or trust GitHub-owned actions by name alone.
- Keep the pinned `actionlint` job required. It is installed as an exact Go
  module version and verified through the Go checksum database before checking
  workflow schema and expressions.
- Review `.github/actions-lock.json` whenever Dependabot proposes an Actions
  update. Confirm the upstream tag and commit before changing both the workflow
  and lock file.
- Do not permit pull-request workflows from forks to access secrets or write
  tokens. This repository intentionally does not use `pull_request_target`.
- Keep artifact retention bounded. Ordinary CI uploads no distributable
  package; tag artifacts are withheld in a public repository until the positive
  distribution policy passes.
- Keep the scheduled `Security audit` workflow enabled. It repeats the pinned
  `govulncheck` v1.1.4 scan weekly and can also be dispatched manually; it has
  read-only permissions and is not a replacement for the required pull-request
  `Go vulnerability audit` check. Its `Scheduled Go vulnerability audit` job
  deliberately uses a different check name so a scheduled run cannot satisfy
  the pull-request requirement by name collision.

## Release settings and first hosted run

1. Push a review branch and confirm every CI matrix job passes on GitHub-hosted
   runners.
2. Review the job permissions in the run UI. Only draft creation and final
   publication receive `contents: write`; attestation is isolated in a
   no-checkout job.
3. Confirm that the canonical repository reports public visibility and that
   release asset URLs are anonymously downloadable.
4. Enable immutable releases and set the three protected production-environment
   acknowledgement variables only after their external controls are verified.
5. Confirm the positive MIT release policy, create a fresh protected
   prerelease tag, and inspect all five draft assets and attestations.
6. Exercise the protected manual publish workflow. Verify that a prerelease is
   not marked Latest.
7. Perform the clean-host WinGet lifecycle test before submitting to
   `microsoft/winget-pkgs`.

Keep screenshots or exported settings outside the source repository if they
contain organization, membership, or security-policy details.
