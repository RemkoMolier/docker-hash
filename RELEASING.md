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

The `security` label read by the 2nd trigger can arrive on a PR in two ways:

- **Automatically by Renovate** for any PR opened in response to a GHSA / OSV vulnerability advisory.
  This is configured by the `vulnerabilityAlerts.labels` rule in [renovate.json](renovate.json) and requires no human action — the label appears the moment Renovate opens the PR.
- **Manually by a maintainer** for any other security-sensitive PR: an upstream advisory the project picks up before Renovate notices it, an auth or input-validation fix that isn't a dep bump, a fix for a private security report, etc.
  Apply the label via `gh pr edit <num> --add-label security` or in the GitHub UI.

Both paths feed the same label, so the cadence workflow doesn't need to distinguish them and the maintainer doesn't need to remember which kind of fix they're shipping.
If Renovate ever stops applying the label (config drift, broken upstream, etc.) the failure mode is the loud one — the label is missing, the maintainer notices, the config gets fixed — rather than a silent fallback that hides the bug.

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

### Pending overrides

The cadence workflow's automatic recommendation is sometimes wrong on purpose, and the maintainer should override it when cutting the tag.
Track those overrides here so they are not forgotten:

- **`v0.2.0` (next release after v0.1.x)** — must skip straight from `v0.1.x` to `v0.2.0`, **not** `v0.1.x → v0.1.(x+1)`.
   The reason is the hash-format change in #44 (FROM digest resolution): every existing v0.1.x hash will produce a different value under the v0.2.0 default behaviour.
   Anyone who pinned a downstream cache key on a v0.1.x hash will see invalidations on upgrade, so the bump needs to be visible.
   The `--no-resolve-from` escape hatch reproduces v0.1.x hashes bit-for-bit for users who need a soft migration.

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

5. The release workflow (see [.github/workflows/release.yml](.github/workflows/release.yml)) takes over from there: builds artifacts, generates the changelog, publishes the GitHub Release, and pushes the OCI image to GHCR.
6. **First release with the OCI image only:** GHCR packages are private by default.
   After the first image push completes, go to
   [Package settings](https://github.com/users/RemkoMolier/packages/container/docker-hash/settings)
   and change the visibility to **Public** so the image is pullable without authentication.
   This is a one-time step — subsequent releases reuse the same package and inherit its visibility.
7. **First release only — publish the composite action to the GitHub Actions Marketplace.**
   Open the just-published GitHub Release in the browser and tick **Publish this Action to the GitHub Marketplace from this release** at the bottom of the edit form; accept the Marketplace terms and select a category when prompted.
   This is a one-time setup — once the action is listed, every subsequent release is automatically published to the Marketplace listing without further maintainer action.
8. Once the release is published, the maintainer closes the `release: prepare next release` tracking issue.

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

## Supply-chain requirements at release time

The tag-triggered release workflow in [.github/workflows/release.yml](.github/workflows/release.yml) produces signed, attested, SBOM-accompanied artifacts.
That means the release runner depends on a few things being installed and reachable — most are handled automatically, but worth knowing when debugging a failed release:

- **cosign** — installed by `sigstore/cosign-installer` in the workflow.
  Used by GoReleaser's `signs:` / `docker_signs:` hooks to produce `*.sig` / `*.pem` files for archives + `checksums.txt` and to sign OCI images + manifests.
- **syft** — installed by `anchore/sbom-action/download-syft` in the workflow.
  Used by GoReleaser's `sboms:` block to emit `*.spdx.sbom.json` + `*.cyclonedx.sbom.json` next to every archive.
- **Sigstore public-good services** — `fulcio.sigstore.dev` (certificate authority) and `rekor.sigstore.dev` (transparency log) must be reachable from the runner.
  A keyless signing run contacts both; if either is down the release will fail rather than produce unsigned artifacts.
- **GitHub attestations** — `actions/attest-build-provenance` runs twice: once with `subject-checksums: dist/checksums.txt` to attest every artifact listed in the checksum file, and once with `subject-path: dist/checksums.txt` so the checksum file itself also carries provenance.
  Both steps run before the floating-tag update so a tag-push failure doesn't skip provenance.

If any of these fail, the release will halt with artifacts already on the GitHub Release but without signatures or attestations.
In that case, delete the GitHub Release + pushed tags and re-run after fixing the underlying issue rather than leaving a partial release published.

## References

- Issue [#38](https://github.com/RemkoMolier/docker-hash/issues/38) — original release-train proposal.
- [.github/workflows/release.yml](.github/workflows/release.yml) — the existing tag-triggered release pipeline.
- [.github/workflows/release-cadence.yml](.github/workflows/release-cadence.yml) — the weekly cadence workflow.
- [SECURITY.md](SECURITY.md) — supply-chain posture and vulnerability-reporting policy.
