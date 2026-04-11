# docker-hash

A Go tool that computes a deterministic SHA-256 hash for a Docker image build,
based on the **Dockerfile content**, any **build arguments** and the **files
referenced by `COPY`/`ADD` instructions** within the build context.

The hash changes whenever:
- The Dockerfile itself is modified.
- A build argument value (declared with `ARG`) changes.
- Any file that is `COPY`'d or `ADD`'d from the build context is modified.

This makes `docker-hash` useful for cache-busting, change detection and
deterministic CI pipelines.

> **Pre-v1 notice:** The hash format is not stable before v1.0.0. Upgrades
> may produce different hashes for unchanged inputs (for example, once
> `.dockerignore` filtering lands). Do not pin downstream tooling on specific
> hash values across upgrades until v1.0.0.

---

## Installation

```sh
go install github.com/RemkoMolier/docker-hash/cmd/docker-hash@latest
```

Or build from source:

```sh
git clone https://github.com/RemkoMolier/docker-hash.git
cd docker-hash
go build -o docker-hash ./cmd/docker-hash/
```

---

## Usage

```
docker-hash [flags]

Flags:
  -f, --file      <path>         Path to the Dockerfile  (default: Dockerfile)
  -c, --context   <dir>          Build context directory (default: .)
      --build-arg <NAME=VALUE>   Build argument; may be repeated
  -v, --version                  Print version information and exit
```

### Examples

```sh
# Hash using the Dockerfile and context in the current directory.
docker-hash

# Specify paths explicitly.
docker-hash -f path/to/Dockerfile -c path/to/context

# Pass build arguments.
docker-hash --build-arg VERSION=1.2.3 --build-arg ENV=prod
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

1. The Dockerfile is parsed to extract `COPY`/`ADD` source paths and `ARG`
   declarations.
2. For each `COPY`/`ADD` that references the **build context** (i.e. without
   `--from=<stage>`), all matching files are collected and their contents are
   hashed. If a `.dockerignore` file is present in the context root, it is
   applied before collecting files — matching the behaviour of `docker build`.
3. Only build arguments that are **declared** with `ARG` in the Dockerfile
   **and** explicitly supplied via `--build-arg` are included in the hash.
   Undeclared `--build-arg` values and declared args with no supplied value are
   both ignored.
4. All contributions are combined with labelled section separators and a
   per-file SHA-256 sub-hash into a final SHA-256 digest.

### Known limitations

- **File permissions are not hashed.** Two contexts that are byte-for-byte
  identical but differ only in file modes (e.g. `chmod`) produce the same
  hash even though Docker may build different images.
- **`ADD <url>` is hashed by URL string, not remote content.** Two builds that
  use the same URL but against different remote content will produce the same
  hash. Use a content-addressed URL (e.g. include a digest or version) to get
  reliable change detection.
- **`**` glob patterns are not supported.** BuildKit supports recursive `**`
  patterns in `COPY`; `docker-hash` uses `filepath.Glob` which does not.
  Affected patterns will silently match nothing.

---

## Project layout

```
.
├── cmd/docker-hash/   # CLI entry point
├── pkg/dockerfile/    # Dockerfile parser helpers
└── pkg/hasher/        # Core hash computation
```

---

## License

[MIT](LICENSE) — Copyright (c) 2026 Remko Molier.
