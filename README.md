# docker-hash

A Go tool that computes a deterministic SHA-256 hash for a Docker image build, based on the **Dockerfile content**, any **build arguments** and the **files referenced by `COPY`/`ADD` instructions** within the build context.

The hash changes whenever:

- The Dockerfile itself is modified.
- A build argument value (declared with `ARG`) changes.
- Any file that is `COPY`'d or `ADD`'d from the build context is modified.

This makes `docker-hash` useful for cache-busting, change detection and deterministic CI pipelines.

> **Pre-v1 notice:** The hash format is not stable before v1.0.0.
> Upgrades may produce different hashes for unchanged inputs (for example, once `.dockerignore` filtering lands).
> Do not pin downstream tooling on specific hash values across upgrades until v1.0.0.

---

## Installation

### From releases (Linux / macOS)

Pre-built binaries for Linux and macOS (amd64 and arm64) are available on the [GitHub Releases page](https://github.com/RemkoMolier/docker-hash/releases).

```sh
# Replace vX.Y.Z and linux_amd64 with the version and platform you need.
curl -sSL https://github.com/RemkoMolier/docker-hash/releases/download/vX.Y.Z/docker-hash_vX.Y.Z_linux_amd64.tar.gz \
  | tar -xz docker-hash
sudo mv docker-hash /usr/local/bin/
```

> **macOS note:** On first run macOS may block the binary because it is not code-signed.
> Right-click → Open, or run `xattr -d com.apple.quarantine ./docker-hash` to bypass the quarantine.

### Using `go install`

```sh
go install github.com/RemkoMolier/docker-hash/cmd/docker-hash@latest
```

### Build from source

```sh
git clone https://github.com/RemkoMolier/docker-hash.git
cd docker-hash
go build -o docker-hash ./cmd/docker-hash/
```

---

## Use as a GitHub Action

`docker-hash` is also published as a reusable composite GitHub Action so you can compute the hash directly inside a workflow without installing the CLI by hand.

### Basic usage

The `file` and `context` inputs are resolved relative to the workflow's checkout, so the calling job needs an `actions/checkout` step before it invokes the action:

```yaml
- uses: actions/checkout@v6

- name: Compute Docker hash
  id: docker_hash
  uses: RemkoMolier/docker-hash@v0.1.0
  with:
    file: Dockerfile
    context: .
    build-args: |
      VERSION=${{ github.sha }}
      ENV=prod

- name: Use the hash
  run: echo "Image hash is ${{ steps.docker_hash.outputs.hash }}"
```

### Export the hash to an env variable

When you want a later step to consume the hash via a fixed environment variable name (e.g. for templating into a build command), pass `export-env-name`:

```yaml
- uses: actions/checkout@v6

- name: Compute Docker hash
  uses: RemkoMolier/docker-hash@v0.1.0
  with:
    context: ./services/api
    export-env-name: API_IMAGE_HASH

- name: Tag and build
  run: docker build --tag api:$API_IMAGE_HASH ./services/api
```

The action always exposes the digest as a stable `hash` step output.
The optional `export-env-name` is an additional convenience that mirrors the same value into `$GITHUB_ENV` for the rest of the job.

### Inputs

| Input | Default | Description |
|---|---|---|
| `file` | `Dockerfile` | Path to the Dockerfile, relative to the workflow's checkout. |
| `context` | `.` | Build context directory, relative to the workflow's checkout. |
| `build-args` | `""` | Newline-separated `NAME=VALUE` build args. Values may contain `=`. Empty lines and `#`-prefixed comments are ignored. |
| `export-env-name` | `""` | Optional environment variable name. If set, the action also writes the hash to `$GITHUB_ENV` under this name. Must be a valid shell identifier and may not start with `GITHUB_` or `RUNNER_`. |

### Outputs

| Output | Description |
|---|---|
| `hash` | The 64-character hex SHA-256 digest. |

### Notes

- The action builds `docker-hash` from source on each run using `actions/setup-go@v6`.
  This adds a small amount of setup time but keeps the action's behaviour aligned with the source at the selected ref.
  A future revision may switch to downloading release archives for faster cold starts.
- The Linux and macOS runners are the primary supported platforms.
  Windows runners are not yet exercised by the self-test in CI; the composite action uses bash steps and should work on Windows runners that have bash on `PATH`, but this is not yet validated.

---

## Usage

```text
docker-hash [flags]

Flags:
  -f, --file            <path>         Path to the Dockerfile  (default: Dockerfile)
  -c, --context         <dir>          Build context directory (default: .)
      --build-arg       <NAME=VALUE>   Build argument; may be repeated
      --no-resolve-from               Disable FROM image digest resolution (skip registry calls)
      --certs-d         <dir>          Path to a containerd-style certs.d directory for registry mirrors
                                       (overrides auto-discovery; see "Behind a corporate registry mirror?" below)
  -v, --version                        Print version information and exit
```

### Examples

```sh
# Hash using the Dockerfile and context in the current directory.
docker-hash

# Specify paths explicitly.
docker-hash -f path/to/Dockerfile -c path/to/context

# Pass build arguments.
docker-hash --build-arg VERSION=1.2.3 --build-arg ENV=prod

# Skip registry calls (no FROM digest resolution).
docker-hash --no-resolve-from

# Use a specific certs.d directory for registry mirrors.
docker-hash --certs-d /etc/containerd/certs.d
```

The tool prints a single 64-character hex-encoded SHA-256 digest to stdout.

---

## Checking your version

```sh
docker-hash --version
# docker-hash dev (none, unknown)
```

When built with version metadata injected via ldflags (e.g. by a release pipeline):

```sh
go build \
  -ldflags "-X main.version=v0.1.0 -X main.commit=abc1234 -X main.date=2026-01-01T00:00:00Z" \
  ./cmd/docker-hash/
./docker-hash --version
# docker-hash v0.1.0 (abc1234, 2026-01-01T00:00:00Z)
```

---

## How it works

1. The Dockerfile is parsed to extract `COPY`/`ADD` source paths, `ARG` declarations, and `FROM` image references.
2. For each `COPY`/`ADD` that references the **build context** (i.e. without `--from=<stage>`), all matching files are collected and their contents are hashed.
   If a `.dockerignore` file is present in the context root, it is applied before collecting files — matching the behaviour of `docker build`.
3. Only build arguments that are **declared** with `ARG` in the Dockerfile **and** explicitly supplied via `--build-arg` are included in the hash.
   Undeclared `--build-arg` values and declared args with no supplied value are both ignored.
4. Each `FROM` image is resolved to its current registry digest (e.g. `sha256:abc123…`).
   This means the hash changes automatically when an upstream image is updated, even if the tag stays the same.
   Use `--no-resolve-from` to skip this step (e.g. in air-gapped environments or when registry calls are undesirable).
5. All contributions are combined with labelled section separators and a per-file SHA-256 sub-hash into a final SHA-256 digest.

### Known limitations

- **File permissions are not hashed.**
  Two contexts that are byte-for-byte identical but differ only in file modes (e.g. `chmod`) produce the same hash even though Docker may build different images.
- **`ADD <url>` is hashed by URL string, not remote content.**
  Two builds that use the same URL but against different remote content will produce the same hash.
  Use a content-addressed URL (e.g. include a digest or version) to get reliable change detection.
- **`**` glob patterns are not supported.**
  BuildKit supports recursive `**` patterns in `COPY`; `docker-hash` uses `filepath.Glob` which does not.
  Affected patterns will silently match nothing.

---

## Behind a corporate registry mirror?

Users in corporate environments that route Docker traffic through a private registry mirror (Artifactory, Harbor, ECR pull-through cache, etc.) can configure `docker-hash` to use those mirrors via the standard [containerd `hosts.toml` format](https://github.com/containerd/containerd/blob/main/docs/hosts.md).

### File layout

Create a `hosts.toml` file for each registry you want to mirror.
The directory structure matches containerd's:

```text
<certs-d>/<upstream-registry>/hosts.toml
```

For example, to mirror `docker.io` through `artifactory.corp/dockerhub`:

```text
~/.config/containerd/certs.d/docker.io/hosts.toml
```

```toml
server = "https://registry-1.docker.io"

[host."https://artifactory.corp/dockerhub"]
  capabilities = ["pull", "resolve"]
```

Only hosts that include `"resolve"` in their `capabilities` list are used for digest resolution.

### Auto-discovery

`docker-hash` searches for a `certs.d` directory in this order (first existing directory wins):

1. `$DOCKER_HASH_CERTS_D`
2. `$XDG_CONFIG_HOME/containerd/certs.d`
3. `~/.config/containerd/certs.d`
4. `/etc/containerd/certs.d`

If the node already runs `containerd` or `nerdctl`, `docker-hash` will automatically pick up the same mirror configuration — no duplication needed.

### CLI override

Use `--certs-d=<path>` to point at a specific directory, bypassing auto-discovery entirely:

```sh
docker-hash --certs-d /etc/containerd/certs.d
```

This is especially useful in CI runners where the mirrors are at a non-default path.

### Hash stability

The **mirror is invisible to the hash output**.
A `FROM golang:1.25` resolved via `artifactory.corp/dockerhub` produces the **same hash** as one resolved directly from Docker Hub — provided the image content is the same.
This ensures that CI runners with different mirror configurations produce identical hashes for the same Dockerfile and image content, which is the whole point of a deterministic hash.

### TLS / skip_verify

Adding `skip_verify = true` to a `[host."..."]` block disables TLS certificate verification for that mirror:

```toml
[host."https://insecure-mirror.corp"]
  capabilities = ["pull", "resolve"]
  skip_verify = true
```

A `WARN` line is printed to stderr when `skip_verify` is active so the setting is always visible.

### Auth

Credentials for both the upstream registry and mirrors are read from `$HOME/.docker/config.json` (or the path in `$DOCKER_CONFIG`) via the standard Docker credential store.
`hosts.toml` itself never contains credentials — this matches containerd's design.

### Multiple mirrors / fallback

If multiple `[host."..."]` blocks are present, `docker-hash` tries them in the order they appear in the file.
When a mirror fails (connection error or HTTP 5xx), the next mirror is tried.
If all mirrors fail, `docker-hash` falls back to the upstream registry.
A `WARN` line is printed to stderr for each skipped mirror.
If the upstream also fails, `docker-hash` exits with an error — use `--no-resolve-from` as the escape hatch.

---

## Project layout

```text
.
├── cmd/docker-hash/          # CLI entry point
├── pkg/dockerfile/           # Dockerfile parser helpers
├── pkg/hasher/               # Core hash computation (includes FROM digest resolution)
└── pkg/registrymirrors/      # containerd hosts.toml parser and mirror transport
```

---

## License

[MIT](LICENSE) — Copyright (c) 2026 Remko Molier.
