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

## How it works

1. The Dockerfile is parsed to extract `COPY`/`ADD` source paths and `ARG`
   declarations.
2. For each `COPY`/`ADD` that references the **build context** (i.e. without
   `--from=<stage>`), all matching files are collected and their contents are
   hashed.
3. Only build arguments that are **declared** with `ARG` in the Dockerfile are
   included in the hash; undeclared `--build-arg` values are ignored.
4. All contributions are combined with section separators into a final
   SHA-256 digest.

---

## Project layout

```
.
├── cmd/docker-hash/   # CLI entry point
├── pkg/dockerfile/    # Dockerfile parser helpers
└── pkg/hasher/        # Core hash computation
```
