# Contributing to docker-hash

Thank you for your interest in contributing!

## Commit and PR title convention

This project follows [Conventional Commits](https://www.conventionalcommits.org/).
Because all PRs are squash-merged, **only the PR title needs to conform** — individual
commit messages on a branch are not enforced.

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

Append `!` to the type/scope, or add a `BREAKING CHANGE:` footer:

```
feat(hasher)!: change hash algorithm to SHA-512
```

or

```
refactor: rename Options.ContextDir to Options.BuildContext

BREAKING CHANGE: Options.ContextDir has been renamed to Options.BuildContext.
```

### Examples

| PR title                                      | Valid? |
|-----------------------------------------------|--------|
| `feat(hasher): add --output flag`             | ✅     |
| `fix: handle empty Dockerfile gracefully`     | ✅     |
| `ci: pin golangci-lint to SHA`                | ✅     |
| `docs: document CONTRIBUTING.md policy`       | ✅     |
| `feat(hasher)!: change digest format`         | ✅     |
| `add output flag`                             | ❌ missing type prefix |
| `Fix bug`                                     | ❌ capitalised, missing type prefix |
| `WIP: trying something`                       | ❌ not a recognised type |

A CI check will automatically fail PRs whose title does not conform.
