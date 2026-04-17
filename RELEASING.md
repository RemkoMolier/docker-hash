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
- **`v0.3.0` (first release shipping the `homebrew_casks` migration)** — must skip straight from `v0.2.x` to `v0.3.0`, **not** `v0.2.x → v0.2.(x+1)`.
   The reason is the Homebrew distribution change in #85: we switch from a Homebrew *formula* to a Homebrew *cask*.
   Existing `brew install remkomolier/tap/docker-hash` users must run a one-time `brew uninstall docker-hash && brew install --cask remkomolier/tap/docker-hash` (or rely on the `tap_migrations.json` entry in the tap — see step 8 of the weekly cadence workflow below), and Linux users lose the `brew` install path altogether and must switch to the `.deb`/`.rpm`/tarball channels.
   That is a user-visible install-path break, so the bump needs to be visible.

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
8. **First release after the `brews` → `homebrew_casks` migration only — update the `RemkoMolier/homebrew-tap` repo so existing formula users migrate cleanly.**
   Before (or together with) the first release that ships the cask, add a `tap_migrations.json` at the tap root and delete the old formula file, as [described in the Homebrew docs](https://docs.brew.sh/Taps#tap-migration):

   ```json
   { "docker-hash": "docker-hash" }
   ```

   ```sh
   git rm Formula/docker-hash.rb
   ```

   With the migration file in place, `brew upgrade` for existing `brew install remkomolier/tap/docker-hash` users will switch them to the cask automatically; without it they would see a "no such formula" error on the next upgrade.
   This is a one-time setup — once the cask has shipped and the migration file is in the tap, subsequent releases just overwrite `Casks/docker-hash.rb` via GoReleaser as normal.
9. Once the release is published, the maintainer closes the `release: prepare next release` tracking issue.

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

## Daily auto-release

In addition to the weekly release train (which only opens a tracking issue), a daily auto-release workflow runs at **06:00 UTC** and automatically cuts a **patch** release when container-affecting changes have landed since the last tag.

### What triggers an auto-release

The auto-release fires when **any** of the following is true for the commits since the latest `v*` tag:

| Trigger | Example |
|---|---|
| Changed files that affect the binary or container image: `Dockerfile`, `go.mod`, `go.sum`, `*.go`, `action.yml` | Alpine digest refresh, Go toolchain bump, bug fix |
| A merged PR in the range carries the `security` label | Security-driven dependency bump, auth fix |

If none of these triggers match — for instance, the only changes are CI workflow tweaks, Renovate config, or docs — no release is cut.

### What it does NOT release

| Change type | Changed paths | Auto-release? |
|---|---|---|
| CI workflow updates | `.github/workflows/*` | No |
| Renovate config | `renovate.json` | No |
| Documentation | `*.md` | No |

### Safety guards

- **Patch only** — the workflow never bumps minor or major.
  If a minor or major bump is needed, the maintainer must tag manually.
- **Dry-run log** — the workflow prints the full commit list and computed version before actually tagging.
- **HEAD already tagged** — if HEAD is the same commit as the latest tag, the workflow exits immediately.
- **CI must be green** — the workflow checks the most recent CI run on `main` and skips if it is not `success`.
- **`GITHUB_TOKEN` tag push + `workflow_dispatch`** — because `GITHUB_TOKEN`-created tag pushes do not trigger other workflows, the auto-release explicitly dispatches the release workflow via `gh workflow run release.yml --ref <tag>` after pushing the tag.

### Interaction with the weekly cadence workflow

The two workflows are complementary:

- **`release-cadence`** (weekly, Tuesdays) opens a *tracking issue* for the maintainer to review and decide whether to tag.
  It considers Conventional Commit types (feat/fix/perf/etc.) and can recommend minor or major bumps.
- **`auto-release`** (daily, 06:00 UTC) *automatically tags and releases* when container-affecting paths change.
  It only ever bumps the patch version.

If a daily auto-release fires before the next Tuesday, the weekly cadence run will see no unreleased commits and exit silently.

### Manual trigger

To trigger the auto-release check outside the daily schedule:

```sh
gh workflow run auto-release.yml
```

### Disabling auto-release

To temporarily disable auto-release without removing the workflow file, either comment out or remove the `schedule` block (or its `cron` line) in [.github/workflows/auto-release.yml](.github/workflows/auto-release.yml), or add an `if:` guard in that workflow so scheduled runs are skipped when `github.event_name == 'schedule'`.

## What this policy explicitly does NOT do

- **No automatic minor or major version bumps.**
  The auto-release workflow only bumps patch.
  Minor and major bumps remain a maintainer decision via the weekly cadence workflow and manual tagging.
- **No release-please / Changesets / other release-management framework.**
  The workflow is intentionally a single bash script in YAML; if it grows past that, that's the signal to revisit.

## Adjusting the policy

If you want to change the day or time of the weekly slot, edit the `cron:` line in [.github/workflows/release-cadence.yml](.github/workflows/release-cadence.yml).
If you want to change the time of the daily auto-release, edit the `cron:` line in [.github/workflows/auto-release.yml](.github/workflows/auto-release.yml).
If you want to change which paths trigger an auto-release, edit the `RELEASE_WORTHY_RE` variable in the auto-release workflow.
If you want to change the "releasable" type list for the weekly cadence, edit the `RELEASABLE_TYPES_RE` variable in the cadence workflow and update the list above to match.
If you want to change the security signal from a label to a different mechanism (e.g. PR title prefix), edit the security detection steps in both workflows.

## Supply-chain requirements at release time

The tag-triggered release workflow in [.github/workflows/release.yml](.github/workflows/release.yml) produces signed, attested, SBOM-accompanied artifacts.
That means the release runner depends on a few things being installed and reachable — most are handled automatically, but worth knowing when debugging a failed release:

- **cosign** — installed by `sigstore/cosign-installer` in the workflow.
  Used by GoReleaser's `signs:` hook to emit a `*.sigstore.json` bundle (new cosign bundle format — Fulcio cert, signature, and Rekor entry embedded) next to every archive and `checksums.txt`, and by the `docker_signs:` hook to sign OCI images + manifests in-registry.
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
- Issue [#102](https://github.com/RemkoMolier/docker-hash/issues/102) — daily auto-release proposal.
- [.github/workflows/release.yml](.github/workflows/release.yml) — the tag-triggered release pipeline.
- [.github/workflows/release-cadence.yml](.github/workflows/release-cadence.yml) — the weekly cadence workflow.
- [.github/workflows/auto-release.yml](.github/workflows/auto-release.yml) — the daily auto-release workflow.
- [SECURITY.md](SECURITY.md) — supply-chain posture and vulnerability-reporting policy.
