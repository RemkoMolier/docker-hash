# docker-hash

[![Release](https://img.shields.io/github/v/release/RemkoMolier/docker-hash?logo=github&label=release)](https://github.com/RemkoMolier/docker-hash/releases/latest)
[![License](https://img.shields.io/github/license/RemkoMolier/docker-hash)](https://github.com/RemkoMolier/docker-hash/blob/main/LICENSE)
[![CI](https://img.shields.io/github/actions/workflow/status/RemkoMolier/docker-hash/ci.yml?branch=main&label=CI&logo=github)](https://github.com/RemkoMolier/docker-hash/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/RemkoMolier/docker-hash)](https://goreportcard.com/report/github.com/RemkoMolier/docker-hash)
[![Marketplace](https://img.shields.io/badge/Marketplace-docker--hash-blue?logo=github)](https://github.com/marketplace/actions/docker-hash)

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

## Use as an OCI image (GitLab CI and container-native CI)

`docker-hash` is published as a multi-arch OCI image to the GitHub Container Registry, making it
easy to consume in GitLab CI and any other container-native CI system where jobs already run
inside an image.

Published image: `ghcr.io/remkomolier/docker-hash`

### GitLab CI example

```yaml
compute_hash:
  image: ghcr.io/remkomolier/docker-hash:v0.1.0
  script:
    - docker-hash --file Dockerfile --context .
```

### Generic container usage

```sh
docker run --rm \
  -v "$PWD:/work" \
  -w /work \
  ghcr.io/remkomolier/docker-hash:v0.1.0 \
  docker-hash --file Dockerfile --context .
```

### Package visibility

GHCR packages are **private by default** when first pushed.
Before unauthenticated pulls (as shown in the GitLab CI and `docker run` examples above) will work, the package must be made public once by the repository owner:

1. Go to the [docker-hash package settings](https://github.com/users/RemkoMolier/packages/container/docker-hash/settings) page
   (or navigate via your profile → Packages → `docker-hash` → Package settings).
2. Scroll to **Danger Zone** → **Change package visibility** → set to **Public**.

This is a one-time step.
After the package is public, all tagged images are pullable without authentication.

### Image tag strategy

| Tag | Updates on | Recommended for |
|---|---|---|
| `vX.Y.Z` (e.g. `v0.1.0`) | Never — immutable | **Production / pinned deployments** |
| `vX.Y` (e.g. `v0.1`) | Every patch release in that minor | Patch-level auto-updates |
| `vX` (e.g. `v0`) | Every non-pre-release in that major | Minor-level auto-updates |
| `latest` | Every non-pre-release | Quick local experiments only |

Pre-release tags (e.g. `v0.2.0-rc1`) are published as `vX.Y.Z` only; the floating
`vX.Y`, `vX`, and `latest` tags are not updated for pre-releases.

**Recommendation:** pin to `vX.Y.Z` in CI pipelines to get fully reproducible builds.

### Supported platforms

- `linux/amd64`
- `linux/arm64`

---

## Use as a GitHub Action

`docker-hash` is also published as a reusable composite GitHub Action so you can compute the hash directly inside a workflow without installing the CLI by hand.

### Basic usage

The `file` and `context` inputs are resolved relative to the workflow's checkout, so the calling job needs an `actions/checkout` step before it invokes the action:

```yaml
- uses: actions/checkout@v6

- name: Compute Docker hash
  id: docker_hash
  uses: RemkoMolier/docker-hash@v0.1.1
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
  uses: RemkoMolier/docker-hash@v0.1.1
  with:
    context: ./services/api
    export-env-name: API_IMAGE_HASH

- name: Tag and build
  run: docker build --tag api:$API_IMAGE_HASH ./services/api
```

The action always exposes the digest as a stable `hash` step output.
The optional `export-env-name` is an additional convenience that mirrors the same value into `$GITHUB_ENV` for the rest of the job.

### Tag strategy

The release workflow maintains floating major and minor git tags alongside each immutable `vX.Y.Z` release,
mirroring the [image tag strategy](#image-tag-strategy):

| Ref | Updates on | Recommended for |
|---|---|---|
| `vX.Y.Z` (e.g. `v0.1.1`) | Never — immutable | **Production / pinned workflows** |
| `vX.Y` (e.g. `v0.1`) | Every patch release in that minor | Patch-level auto-updates |
| `vX` (e.g. `v0`) | Every non-pre-release in that major | Minor-level auto-updates |

Pre-release tags (e.g. `v0.2.0-rc1`) are immutable;
the floating `vX.Y` / `vX` refs are not updated for pre-releases.

**Recommendation:** pin to `vX.Y.Z` for reproducible CI runs.

### Resolving FROM image digests in CI

Since v0.2.0, the action resolves every `FROM` reference against its registry by default and folds the digest into the hash, so a workflow's cache key invalidates when an upstream image is repointed under the same tag.
A typical CI job already has the credentials it needs to talk to private registries (via `docker/login-action` or the cloud-provider login actions, both of which write `~/.docker/config.json`), so the action picks them up automatically.

If your workflow runs offline or against an air-gapped runner, set `no-resolve-from: "true"` to skip the registry round-trip.
ARG/ENV expansion still happens and the section-4 base-image contribution is still canonicalized — so `alpine` and `alpine:latest` produce the same *base-image entry*, even though the *overall hash* still differs because section 1 always hashes the raw Dockerfile bytes:

```yaml
- uses: actions/checkout@v6

- name: Compute Docker hash (offline mode)
  uses: RemkoMolier/docker-hash@v0.2.0
  with:
    no-resolve-from: "true"
```

To reproduce the v0.1.x hash format bit-for-bit, set **both** `no-resolve-from` and `no-expand-args`:

```yaml
- name: Compute Docker hash (v0.1.x compat)
  uses: RemkoMolier/docker-hash@v0.2.0
  with:
    no-resolve-from: "true"
    no-expand-args: "true"
```

For a workflow that needs a specific platform's manifest:

```yaml
- uses: actions/checkout@v6

- name: Compute Docker hash for linux/amd64 only
  uses: RemkoMolier/docker-hash@v0.2.0
  with:
    platform: linux/amd64
```

### Inputs

| Input | Default | Description |
|---|---|---|
| `file` | `Dockerfile` | Path to the Dockerfile, relative to the workflow's checkout. |
| `context` | `.` | Build context directory, relative to the workflow's checkout. |
| `build-args` | `""` | Newline-separated `NAME=VALUE` build args. Values may contain `=`. Empty lines and `#`-prefixed comments are ignored. |
| `export-env-name` | `""` | Optional environment variable name. If set, the action also writes the hash to `$GITHUB_ENV` under this name. Must be a valid shell identifier and may not start with `GITHUB_` or `RUNNER_`. |
| `no-resolve-from` | `"false"` | Set to `"true"` to skip resolving `FROM` image digests against the registry. Expansion and canonicalization still run; combine with `no-expand-args` for bit-for-bit v0.1.x output. |
| `no-expand-args` | `"false"` | Set to `"true"` to disable ARG/ENV expansion in `COPY`/`ADD` paths, `--from=` stage names and `FROM` references. Causes `FROM ${VAR}` lines to fail rather than be silently ignored. Combine with `no-resolve-from` for bit-for-bit v0.1.x output. |
| `platform` | `""` | Force a specific platform (e.g. `linux/amd64`) when resolving multi-arch base images. Empty hashes the multi-arch index digest. Per-FROM `--platform=` flags in the Dockerfile take precedence. |
| `auth-file` | `""` | Path to a registry auth file (Docker `config.json` or Podman/Skopeo `auth.json` format). Same semantic as the Skopeo/Podman/Buildah `--authfile` flag. Most workflows will not need this — `actions/checkout` plus `docker/login-action` already populate the default Docker config. |
| `registries-conf` | `""` | Path to a Podman-style `registries.conf` TOML file describing per-registry mirrors. When set (and `no-resolve-from` is not `"true"`), FROM digest resolution is routed through the configured mirrors with fallback to the upstream on connection error or HTTP 5xx. The file uses the same `[[registry]]` / `[[registry.mirror]]` schema Podman, Buildah, Skopeo and CRI-O already consume; an existing `/etc/containers/registries.conf` works as-is. There is no auto-discovery — the path must be provided. |

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
  -f, --file        <path>         Path to the Dockerfile  (default: Dockerfile)
  -c, --context     <dir>          Build context directory (default: .)
      --build-arg   <NAME=VALUE>   Build argument; may be repeated
      --no-resolve-from            Skip the registry round-trip; expansion + canonicalization still run
      --no-expand-args             Disable ARG/ENV expansion in COPY/ADD and FROM; fail on FROM ${VAR}
      --platform    <os/arch>      Force a specific platform when resolving FROM digests
      --auth-file   <path>         Registry auth file path (Docker / Podman / Skopeo format)
      --registries-conf <path>     Podman-style registries.conf for per-registry mirror routing
  -v, --version                    Print version information and exit
```

### Examples

```sh
# Hash using the Dockerfile and context in the current directory.
docker-hash

# Specify paths explicitly.
docker-hash -f path/to/Dockerfile -c path/to/context

# Pass build arguments.
docker-hash --build-arg VERSION=1.2.3 --build-arg ENV=prod

# Skip the registry round-trip — useful for offline runs. ARG/ENV expansion
# and reference canonicalization still happen.
docker-hash --no-resolve-from

# Reproduce a v0.1.x hash bit-for-bit (both flags together).
docker-hash --no-resolve-from --no-expand-args

# Force a specific architecture when resolving multi-arch FROM images.
docker-hash --platform=linux/amd64

# Use a specific registry auth file (matches Skopeo/Podman/Buildah --authfile).
docker-hash --auth-file ~/.config/containers/auth.json

# Route FROM digest resolution through corporate mirrors defined in a
# Podman-style registries.conf. The hash output is unchanged — mirrors
# are an HTTP-layer concern, the canonical upstream image name is what
# feeds the hash.
docker-hash --registries-conf /etc/containers/registries.conf
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

1. The Dockerfile is parsed to extract `FROM` references, `COPY`/`ADD` source paths, and `ARG` declarations.
2. For each `COPY`/`ADD` that references the **build context** (i.e. without `--from=<stage>`), `$VAR` and `${VAR}` references in the source paths are expanded against the running `ARG`/`ENV` state at the COPY position, then all matching files are collected and their contents are hashed.
   If a `.dockerignore` file is present in the context root, it is applied before collecting files — matching the behaviour of `docker build`.
   Symbolic links are handled in two ways that mirror Docker's classic builder behaviour:
   - **Top-level source symlinks** (e.g. `COPY mylink /dest/` where `mylink` is itself a symlink) are followed; the hash covers the resolved target's content.
     A symlink that escapes the build context is an error.
   - **Inner symlinks** found while walking a copied directory (e.g. `COPY src/ /dest/` where `src/foo` is a symlink) are hashed by their target string only, not the target's content.
     This matches what Docker preserves in the resulting image layer.
3. Only build arguments that are **declared** with `ARG` in the Dockerfile **and** explicitly supplied via `--build-arg` are included in the hash.
   Undeclared `--build-arg` values and declared args with no supplied value are both ignored.
4. Each `FROM` reference is expanded against pre-`FROM` `ARG`s and caller args, then resolved to its current registry digest and folded into the hash, so a tag drift in the upstream registry produces a different hash for an otherwise unchanged Dockerfile.
   `FROM scratch`, `FROM <stage-alias>`, and references that already include a `@sha256:...` digest are handled offline; only plain-tag references trigger a network call.
   Use `--no-resolve-from` for offline runs, `--no-expand-args` to enforce expansion-free `FROM` lines, or both flags together to reproduce the v0.1.x behaviour bit-for-bit.
5. All contributions are combined with labelled section separators and a per-file SHA-256 sub-hash into a final SHA-256 digest.

### Resolving FROM image digests

By default, every `FROM` reference is resolved against its registry to capture the current image digest.
This closes the silent-mutable-input gap that v0.1.x had: if `golang:1.25` is repointed at a new build, the docker-hash output changes even though the Dockerfile and context are byte-for-byte identical.

The resolver:

- Skips `FROM scratch`, `FROM <stage-alias>`, and `FROM <repo>@sha256:...` (already pinned).
   None of these need a network call.
- Uses the **multi-arch index digest** by default for plain-tag references, so the same Dockerfile hashes the same on every runner architecture.
- Honours per-FROM `--platform=` flags inside the Dockerfile, falling back to the CLI `--platform` flag, falling back to "use the index digest."
- Caches resolved digests **per invocation**: a Dockerfile with three stages off the same base image makes one network call, not three.
   The cache is never persisted; the whole point of resolving every run is to detect drift.

#### `ARG` and `ENV` expansion

`docker-hash` expands `$VAR`, `${VAR}`, `${VAR:-default}` and `${VAR:+alt}` references in three places:

- **`FROM` image and `--platform=` value** — against pre-`FROM` `ARG` defaults layered with caller-supplied `--build-arg` values.
   Per the Dockerfile spec, only `ARG`s declared *before* any `FROM` are visible there.
- **`COPY`/`ADD` source paths** — against the running `ARG`/`ENV` state at the COPY position in the same stage.
- **`COPY --from=<stage>` stage names** — against the same stage-local state, so `COPY --from=${STAGE}` resolves to the named stage instead of being treated as a context-source copy.

The lookup precedence inside a stage is:

1. **Caller-supplied build args** passed via `--build-arg NAME=VALUE` win over both `ARG` defaults and pre-`FROM` defaults.
2. **In-stage `ARG NAME=value`** declarations contribute their default.
3. **In-stage `ARG NAME`** (no default) inherits from a pre-`FROM` `ARG NAME=value` of the same name — the only way per the Dockerfile spec to see a pre-`FROM` `ARG` from inside a stage.
4. **In-stage `ENV NAME=value`** declarations override anything earlier in the same stage. `ENV` values are themselves expanded against the running state, so an `ENV` can reference an earlier `ARG` or `ENV`.

Substitution is single-pass — values returned by the lookup are not themselves re-scanned for further `$...` references, matching the Dockerfile spec.
A reference whose name has no value is left literal in the result; for `COPY` patterns this means the literal `${VAR}` text gets passed to the filesystem walk and (almost always) trips the "matches no files" guard.

Common patterns now work end-to-end:

```dockerfile
ARG BASE=alpine:3.20
FROM ${BASE}                               # resolves to whatever alpine:3.20 points at
```

```dockerfile
FROM alpine:3.20
ARG VERSION=1.0
COPY app-${VERSION}.tar.gz /opt/           # picks up app-1.0.tar.gz
```

```dockerfile
FROM --platform=$BUILDPLATFORM alpine:3.20  # does not crash
```

`$BUILDPLATFORM` and `$TARGETPLATFORM` are auto-supplied by Docker at build time and reflect the build host's architecture, which is intentionally non-deterministic across runners.
When `docker-hash` finds an unresolved `$...` in a `--platform=` value, it drops the platform to "no platform" so the resolver returns the **multi-arch index digest** — the deterministic choice across runner architectures.
Pass an explicit `--build-arg TARGETPLATFORM=linux/arm64` if you want a per-platform manifest digest.

When an `ARG` resolves to a stage alias declared in the same Dockerfile, the resulting `FROM` is treated as a stage reference and skipped by the resolver, just as if the alias had been written literally.

#### Behaviour modes

Two flags (`--no-resolve-from` and `--no-expand-args`) compose into four modes:

| `--no-resolve-from` | `--no-expand-args` | Mode | What happens |
|---|---|---|---|
| *off* | *off* | **Resolved** (default) | Expand `ARG`/`ENV` everywhere; resolve every plain `FROM` reference against the registry; emit `resolved:<plat>:<repo>@sha256:...` entries. |
| **on** | *off* | **Offline** | Expand `ARG`/`ENV` everywhere; do **not** call the network. Each `FROM` contributes its expanded canonical reference, so `alpine` and `alpine:latest` produce the same section-4 base-image entry (the overall hash still differs between those two Dockerfiles because section 1 always hashes the raw Dockerfile bytes — only the base-image contribution is canonicalized). Hashes differ from the default mode and from v0.1.x by design. |
| *off* | **on** | **Strict** | Do **not** expand `ARG`/`ENV` anywhere. Resolve plain `FROM` references through the registry as in the default mode. A `FROM ${VAR}` line causes `docker-hash` to **fail** rather than silently ignore the variable — this enforces "all `FROM` lines must be expansion-free" in CI. |
| **on** | **on** | **v0.1.x compat** | Skip the entire base-images section. The result is bit-for-bit identical to a v0.1.x hash for the same inputs. Useful for comparing against old hashes during a migration. |

Build-arg sensitivity is preserved in every mode via two paths:

- Section 2 (`build-args`) hashes every declared `ARG` name against its caller-supplied value.
- Section 1 (`dockerfile`) hashes the raw Dockerfile bytes, so `FROM ${BASE}` and `FROM ${OTHER}` produce different hashes regardless of expansion mode.

If a `FROM` line references a `$VAR` that has neither a pre-`FROM` `ARG` default nor a caller-supplied value, the **default** mode falls back to a literal `unexpanded:` contribution rather than crashing.
The full `FROM` text is still in section 1, so the hash still discriminates between different unresolvable expressions.
**Strict** mode rejects such references outright; use it when "every `FROM` must be expansion-free" is a CI policy.

#### Authentication

Authentication flows through `google/go-containerregistry`'s default keychain, which reads, in order:

1. `$HOME/.docker/config.json`
2. `$DOCKER_CONFIG/config.json`
3. `$REGISTRY_AUTH_FILE` (Podman / Skopeo / Buildah convention)
4. `$XDG_RUNTIME_DIR/containers/auth.json` (Podman default path)

Cloud-provider credential helpers (ECR, GCR, ACR) are picked up automatically when the corresponding helper binaries are on `PATH`.
The `--auth-file=<path>` flag is the explicit equivalent of Skopeo's, Podman's, and Buildah's `--authfile` flag — it sets `REGISTRY_AUTH_FILE` for the current run.

#### Registry mirrors

Corporate environments often front public registries with an internal mirror (Artifactory, Harbor, ECR pull-through cache, …).
`docker-hash` can route FROM digest resolution through such mirrors via `--registries-conf=<path>`, which loads a Podman-style `registries.conf` TOML file:

```toml
[[registry]]
prefix = "docker.io"
location = "registry-1.docker.io"

[[registry.mirror]]
location = "artifactory.corp/dockerhub"

[[registry]]
prefix = "ghcr.io"

[[registry.mirror]]
location = "artifactory.corp/ghcr"
```

The schema is the same one Podman, Buildah, Skopeo and CRI-O already consume, so an existing `/etc/containers/registries.conf` can be reused as-is — there is intentionally no second source of truth to maintain.

Routing rules:

- A request to a registry listed in `prefix` (or `location` if `prefix` is omitted) is rewritten to each configured mirror in declaration order.
- If a mirror returns an HTTP 5xx or fails to connect, the next mirror is tried; if every mirror fails the request falls back to the original upstream URL, so a misconfigured mirror degrades gracefully instead of breaking the build.
- The hash output is **unchanged** by mirror routing.
  Mirrors are an HTTP-layer concern: the canonical upstream image name (e.g. `index.docker.io/library/alpine@sha256:...`) is what feeds the hash, regardless of which mirror actually served the manifest.
  Two runners with different mirror configs produce identical hashes for the same image content.
- Per-mirror `insecure = true` enables HTTP and disables TLS verification for that mirror only.
  A WARN line is logged for each request routed through such a mirror so the looser security posture is visible in CI logs.

There is intentionally **no auto-discovery**: the path must be passed explicitly via `--registries-conf=<path>`.
This avoids "why is my build pulling from a mirror I forgot existed" surprises and keeps the resolver behaviour reproducible across machines.

When `--no-resolve-from` is set, `--registries-conf` is silently ignored — there are no registry calls to route in the first place.

The following Podman fields are accepted but currently **ignored**: `unqualified-search-registries`, registry-level `blocked`, and `mirror-by-digest-only`.
Adding support for any of them is a future enhancement; for now they parse without error so an existing Podman config does not need editing.

#### Failure modes

- **Network unreachable**: the run fails with the offending image name in the error message.
   Use `--no-resolve-from` for offline runs.
- **Image not found / 404 / auth missing**: same — fail loud rather than silently fall back, otherwise the determinism story breaks.
- **Behind a corporate registry mirror**: pass a Podman-style `registries.conf` via `--registries-conf=<path>` (see [Registry mirrors](#registry-mirrors) below).
  `HTTPS_PROXY` is also honoured at the connection level if you only need a flat HTTP proxy.

### Known limitations

- **File permissions are not hashed.**
  Two contexts that are byte-for-byte identical but differ only in file modes (e.g. `chmod`) produce the same hash even though Docker may build different images.
- **`ADD <url>` is hashed by URL string, not remote content.**
  Two builds that use the same URL but against different remote content will produce the same hash.
  Use a content-addressed URL (e.g. include a digest or version) to get reliable change detection.
- **`**` glob patterns are not supported.**
  BuildKit supports recursive `**` patterns in `COPY`; `docker-hash` uses `filepath.Glob` which does not.
  Affected patterns will silently match nothing.
- **Inner symlink target content is not hashed.**
  When `COPY src/ /dest/` is used and `src/` contains a symbolic link, the hash covers the symlink's target string (e.g. `../other`).
  If the file the symlink points to changes but the symlink itself is not relinked, the hash does not change.
  This matches Docker's behaviour — Docker preserves the symlink as-is in the resulting layer, so the target file's content is irrelevant.
  Top-level source symlinks (e.g. `COPY mylink /dest/`) are followed: the hash covers the resolved target's content, so changes to the target file are detected.

---

## Project layout

```text
.
├── cmd/docker-hash/   # CLI entry point
├── pkg/dockerfile/    # Dockerfile parser helpers
└── pkg/hasher/        # Core hash computation
```

---

## License

[MIT](LICENSE) — Copyright (c) 2026 Remko Molier.
