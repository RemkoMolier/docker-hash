# Contributing to docker-hash

Thank you for your interest in contributing!

## Commit and PR title convention

This project follows [Conventional Commits](https://www.conventionalcommits.org/).
Because all PRs are squash-merged, **only the PR title needs to conform** — individual commit messages on a branch are not enforced.

### Allowed types

| Type       | When to use                                              |
|------------|----------------------------------------------------------|
| `feat`     | A new feature                                            |
| `fix`      | A bug fix                                                |
| `docs`     | Documentation-only changes                               |
| `style`    | Formatting, whitespace — no logic change                 |
| `refactor` | Code restructuring — no feature or bug-fix               |
| `perf`     | Performance improvements                                 |
| `test`     | Adding or fixing tests                                   |
| `build`    | Changes to the build system or external dependencies     |
| `ci`       | CI configuration changes                                 |
| `chore`    | Miscellaneous tasks (e.g. version bumps, repo hygiene)   |
| `revert`   | Reverts a previous commit                                |

### Scope (optional)

A scope narrows the context of the change, e.g. `feat(hasher):` or `fix(parser):`.
Use the package or component name in lowercase.

### Breaking changes

The CI check validates the **PR title only**, so the supported way to mark a breaking change is the `!` suffix on the type or scope:

```text
feat(hasher)!: change hash algorithm to SHA-512
```

Optionally, you may also document the break with a `BREAKING CHANGE:` footer in the **PR description body** (which becomes the squash-commit body on merge).
The footer is not enforced by CI, but it's picked up by changelog tooling:

```text
refactor: rename Options.ContextDir to Options.BuildContext

BREAKING CHANGE: Options.ContextDir has been renamed to Options.BuildContext.
```

### Examples

| PR title                                  | Valid? |
|-------------------------------------------|--------|
| `feat(hasher): add --output flag`         | ✅ |
| `fix: handle empty Dockerfile gracefully` | ✅ |
| `ci: pin golangci-lint to SHA`            | ✅ |
| `docs: document CONTRIBUTING.md policy`   | ✅ |
| `feat(hasher)!: change digest format`     | ✅ |
| `add output flag`                         | ❌ missing type prefix |
| `Fix bug`                                 | ❌ capitalised, missing type prefix |
| `WIP: trying something`                   | ❌ not a recognised type |

A CI check will automatically fail PRs whose title does not conform.

## Markdown style

Markdown files in this repo use **one sentence per line** (also called *semantic line breaks* or *ventilated prose*).
Don't hard-wrap prose at 80 characters; let lines be as long as the sentence needs.

### Why

When you change a single word in the middle of a hard-wrapped paragraph, the wrap shifts and the diff shows every line as changed.
With one sentence per line, only the modified sentence appears in the diff, so reviews stay focused on what actually changed.

### How

- Each sentence goes on its own line.
- Code blocks, tables, lists of single-item lines, and headings are unaffected — only prose paragraphs and blockquotes get the treatment.
- Blockquotes follow the same rule, with each `>`-prefixed line containing one sentence.
- Long sentences are fine.
- Don't break a single sentence across multiple lines just to keep individual lines short.

### Enforced by CI

The `markdown-lint` job in [.github/workflows/ci.yml](.github/workflows/ci.yml) runs `markdownlint-cli2` against `**/*.md` with the [`markdownlint-rule-max-one-sentence-per-line`](https://github.com/aepfli/markdownlint-rule-max-one-sentence-per-line) custom rule.
The job will fail your PR if a line contains more than one sentence.

To run the same check locally before pushing, install into a temporary directory so no `node_modules/` lands in the repo checkout:

```sh
PREFIX=$(mktemp -d /tmp/mdlint.XXXXXX)
(cp .github/markdownlint/package.json .github/markdownlint/package-lock.json "$PREFIX"/ && \
  cd "$PREFIX" && \
  npm ci --ignore-scripts)
NODE_PATH="$PREFIX/node_modules" \
  npx --prefix "$PREFIX" markdownlint-cli2 "**/*.md"
```

The pinned versions and integrity hashes match what CI uses (see `.github/markdownlint/package-lock.json`), and [.markdownlint-cli2.yaml](.markdownlint-cli2.yaml) contains the rule configuration.
Most editors with a markdownlint integration will also pick up the same rule set automatically (the `MD013: false` and `customRules` settings live in `.markdownlint-cli2.yaml`).

## Dependency updates

Renovate opens pull requests for dependency updates across `gomod`, GitHub Actions, and npm manifests/lockfiles in the repository.

### Automerge policy

Minor, patch, pin, and digest updates are auto-merged via GitHub's native auto-merge (`platformAutomerge: true`, `automergeStrategy: squash` in [renovate.json](renovate.json)).
**Major version bumps still require a human merge** — they are excluded from the automerge rule so you can review changelogs for breaking changes.

### Patch updates wait 3 days

Patch-level updates have `minimumReleaseAge: "3 days"` applied via a `packageRules` entry.
Renovate will not open a PR for a patch release until it has been published for at least three days.
This catches the most common upstream regression pattern (a freshly-published patch turns out to be broken and gets re-released within hours), at the cost of a slightly delayed adoption window.

### Branch protection

`main` is protected by a repository ruleset with `strict` required status checks, which means a Renovate PR cannot merge until its branch has been rebased onto current `main` and CI has re-run successfully on that rebased commit.
This prevents the broken-main cascade scenario where a stale PR gets merged on top of a broken base.

### GitHub Actions are SHA-pinned

Every `uses:` reference in `.github/workflows/` must point at a full 40-character commit SHA, not a moving tag like `@v6` or `@main`.
The readable upstream version goes in a trailing comment so reviewers can still see at a glance which release the SHA corresponds to:

```yaml
- uses: actions/checkout@de0fac2e4500dabe0009e67214ff5f5447ce83dd # v6.0.2
```

The policy is maintained by a `pinDigests: true` rule scoped to the `github-actions` manager's `action` dep type in [renovate.json](renovate.json).
Renovate will open a pinning PR for any unpinned `uses:` line it finds on its next scan and keep already-pinned SHAs current as new releases ship, preserving the trailing version comment.
Note that this is *maintenance*, not a blocking check: nothing in CI today rejects an unpinned `uses:` line at PR time, so Renovate may only normalize it after the fact.

The reason for the policy is supply-chain hardening: a tag like `actions/checkout@v6` is mutable and can be repointed at a different commit by the action's maintainers (or anyone who compromises them), so pinning by SHA is the only way to guarantee the workflow runs the code that was reviewed.

When adding a new action to a workflow, resolve the tag to a SHA up front rather than relying on Renovate to clean it up after merge.

### Triggering Renovate manually

Tick the "Check this box to trigger a request for Renovate to run again on this repository" checkbox in the [Dependency Dashboard issue](../../issues/21) if you want Renovate to re-scan immediately rather than waiting for its next scheduled run.

## Releases and the security label

`docker-hash` follows a weekly release cadence with a security fast track.
The full policy lives in [RELEASING.md](RELEASING.md); the contributor-relevant summary is short:

- Releases are cut on **Tuesdays at 10:00 UTC** when there are releasable changes since the latest tag.
- A scheduled `release-cadence` workflow opens or updates a tracking issue (labelled `release-prep`) when a release is due.
- The maintainer reviews the issue and pushes the tag manually — there is no unattended auto-tagging.

If your PR fixes a security-sensitive issue (CVE-driven dependency bump, auth fix, anything materially affecting the project's security posture), **call it out clearly in the PR description or a follow-up comment**.
External contributors usually can't apply labels to GitHub issues and PRs themselves; a maintainer will add the `security` label on your behalf before merge.
Once the label is in place, the cadence workflow picks the PR up across the unreleased commit range and surfaces it as URGENT in the next release-prep issue, so security fixes do not have to wait for the next weekly slot.

You generally do not need to do anything else.
The maintainer handles tag pushes and the release workflow takes care of the artifacts.
