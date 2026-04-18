# Agent bootstrap

Run once per clone:

```sh
mise install && lefthook install
```

Before declaring a change done:

```sh
lefthook run pre-commit --all-files
go test ./...
```

Tool versions are pinned in [.mise.toml](.mise.toml) and `.github/markdownlint/package-lock.json`.
Hooks are defined in [lefthook.yml](lefthook.yml).
