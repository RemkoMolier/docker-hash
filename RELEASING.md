# Releasing docker-hash

This document captures the release policy for `docker-hash` and the maintainer workflow that supports it.
The intent is to make the release cadence predictable without giving up human review of every tag.

## Policy

### Weekly release train

Releases are cut on a weekly cadence **when there are releasable changes since the latest tag**.
The fixed weekly slot is **Tuesday at 10:00 UTC**.
Tuesday avoids the Monday morning churn and the Friday afternoon "I won't be around to babysit" anti-pattern, and 10:00 UTC lands during European mornings and US East Coast pre-noon, which is when the maintainer is most likely to be available to push the tag.

### No-op when nothing has changed

If there are no releasable commits since the latest `v*` tag, **no release is cut and no tracking artifact is opened**.
The scheduled workflow exits with a "no release needed" log message.
A docs-only or chore-only week produces no release.

### Security fast track

Security-sensitive changes (CVE-driven dep bumps, auth fixes, anything that materially affects the project's security posture) bypass the weekly schedule and should be released as soon as practical after the fix lands.

The cadence workflow recognises three independent triggers and prepares a release if **any** of them fires:

1. **Releasable type filter** — at least one commit in the range matches `feat`/`fix`/`perf`/`refactor`/`build`/`ci`/`revert`.
2. **`security` label on a merged PR** — any merged PR in the range carries the `security` label, regardless of its commit type.
   This is what makes a security-driven `chore(deps):` bump release-prep-eligible even though `chore(deps):` is otherwise filtered out.
3. **Manual `reason=security`** — a `workflow_dispatch` run with the input `reason: security` forces a release prep even if neither of the above fires (provided there is at least one commit in the range).

In all three cases the workflow opens or updates the same `release-prep` tracking issue.
Triggers 2 and 3 mark the issue body with an `URGENT — security` header so it stands out in the issue list.

The only thing that **cannot** trigger a release prep is "no commits at all since the latest tag" — there is nothing to ship in that case, so the workflow no-ops even with `reason=security`.

#### Where the `security` label comes from

The `security` label is **applied manually** today.
A maintainer adds it to a merged PR (or to an open PR before merge) when the change addresses a security issue.

A planned follow-up (see [#43](https://github.com/RemkoMolier/docker-hash/issues/43)) will have Renovate auto-apply the same label to dependency-update PRs that come from a GHSA / OSV vulnerability advisory, via a top-level `vulnerabilityAlerts.labels` rule in `renovate.json`.
That removes the human-memory step for the most common case (security-driven dep bumps) without changing how the cadence workflow reads the label — the same label name flows through unchanged.

External contributors usually can't apply labels themselves; see the "Releases and the security label" section in [CONTRIBUTING.md](CONTRIBUTING.md) for the contributor side of this flow.

## What counts as a releasable change

A commit since the latest tag is **releasable** if its message starts with one of:

- `feat` — a new feature
- `fix` — a bug fix
- `perf` — a performance improvement
- `refactor` — a code restructure that affects compiled output
- `build` — a build-system change that affects the binary or release artifacts
- `ci` — a CI change that affects how releases are produced (e.g. release.yml)
- `revert` — a revert of a previous change

A commit is **not releasable on its own** if its message starts with:

- `docs` — documentation-only changes
- `test` — test-only changes
- `style` — formatting / whitespace
- `chore` — repo hygiene, version bumps in non-shipping config

The cadence workflow uses the same prefix list to decide whether a release is needed.

> **Edge case:** `chore(deps):` dependency bumps are excluded from the "releasable" check by default, even though they do change the binary's transitive dependencies.
> The reasoning: most weeks of dep bumps are routine and don't warrant a tag.
> When a security-sensitive dep bump *should* trigger a release, label the merging PR with `security` to fast-track it.

## Recommended next version

When the cadence workflow finds releasable commits, it suggests a next version based on the commits' Conventional Commit types:

- **Major bump** (`vX → v(X+1).0.0`) if any commit's type has a `!` breaking-change marker (e.g. `feat(hasher)!:`).
- **Minor bump** (`vX.Y → vX.(Y+1).0`) if any commit is a `feat` or `feat(scope)`.
- **Patch bump** (`vX.Y.Z → vX.Y.(Z+1)`) otherwise.

The maintainer can override the recommendation when actually cutting the tag.
For releases before `v1.0.0`, the maintainer may choose to keep "minor" bumps as `0.X` increments instead of `0.0.X`, since pre-1.0 versioning is intentionally less strict.

## Maintainer workflow

### Weekly cadence (the normal case)

1. The `release-cadence` workflow runs every Tuesday at 10:00 UTC.
2. If there's nothing releasable, it exits silently.
3. If there are releasable commits, it opens or updates a single GitHub issue titled `release: prepare next release` (labelled `release-prep`) with:
   - The latest tag (e.g. `v0.1.0`).
   - The commit range waiting to ship.
   - The list of releasable PRs since the last tag.
   - The recommended next version.
   - Any commits flagged as security-sensitive (PRs with the `security` label).
4. The maintainer reviews the issue, verifies CI is green on the tip of `main`, drafts release notes if desired, and pushes the tag manually:

   ```sh
   git fetch origin main
   git checkout main
   git pull --ff-only
   git tag -a vX.Y.Z -m "docker-hash vX.Y.Z"
   git push origin vX.Y.Z
   ```

5. The release workflow (see [.github/workflows/release.yml](.github/workflows/release.yml)) takes over from there: builds artifacts, generates the changelog, publishes the GitHub Release.
6. Once the release is published, the maintainer closes the `release: prepare next release` tracking issue.

### Triggering a security release manually

If a security-sensitive PR has just landed and the next weekly slot is too far away:

1. Make sure the merged PR has the `security` label.
2. Manually trigger the cadence workflow:

   ```sh
   gh workflow run release-cadence.yml -f reason=security
   ```

3. The workflow opens (or updates) the tracking issue with an `URGENT — security` prefix in the body.
4. Cut the tag immediately following the same `git tag … && git push` flow as the weekly case.

### Manual trigger for any other reason

If you want to inspect what the cadence workflow would say without waiting for Tuesday, run it manually:

```sh
gh workflow run release-cadence.yml
```

The workflow accepts a free-form `reason` input that just ends up in the workflow log so you can find it later.

## What this policy explicitly does NOT do

- **No automatic tag push.**
  Unattended tagging is too aggressive for this project today.
  Every release is a maintainer decision; the workflow only prepares the tracking artifact.
- **No automatic semver calculation beyond the simple type-prefix rule above.**
  If you want a different version than the recommendation, just pick it.
- **No release-please / Changesets / other release-management framework.**
  The workflow is intentionally a single bash script in YAML; if it grows past that, that's the signal to revisit.

## Adjusting the policy

If you want to change the day or time of the weekly slot, edit the `cron:` line in [.github/workflows/release-cadence.yml](.github/workflows/release-cadence.yml).
If you want to change the "releasable" type list, edit the `RELEASABLE_TYPES_RE` variable in the same file and update the list above to match.
If you want to change the security signal from a label to a different mechanism (e.g. PR title prefix), edit the workflow's "security flag detection" step.

## References

- Issue [#38](https://github.com/RemkoMolier/docker-hash/issues/38) — original release-train proposal.
- [.github/workflows/release.yml](.github/workflows/release.yml) — the existing tag-triggered release pipeline.
- [.github/workflows/release-cadence.yml](.github/workflows/release-cadence.yml) — the weekly cadence workflow.
