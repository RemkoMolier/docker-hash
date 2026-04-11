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

To run the same check locally before pushing:

```sh
npm init -y >/dev/null
npm install --no-save \
  markdownlint-cli2@0.22.0 \
  markdownlint-rule-max-one-sentence-per-line@0.0.2
npx markdownlint-cli2 "**/*.md"
```

The pinned versions match what CI uses; see [.markdownlint-cli2.yaml](.markdownlint-cli2.yaml) for the rule configuration.
