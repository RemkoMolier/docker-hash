FROM alpine:3.24@sha256:a2d49ea686c2adfe3c992e47dc3b5e7fa6e6b5055609400dc2acaeb241c829f4

# Pull in any package updates that have been published to the 3.23 apk
# repo since the base image was last rebuilt on Docker Hub (e.g. musl,
# zlib, libcrypto3 patch releases). `--no-cache` keeps the index out of
# the final layer so the image doesn't grow the apk database.
#
# NOTE: `apk upgrade` trades bit-exact reproducibility for fresh CVE
# coverage at build time — two builds of the same commit on different
# days may produce different final image digests because the apk repo
# content changes underneath. The FROM @sha256: pin keeps the base
# deterministic; package upgrades on top are intentional.
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/docker-hash /usr/local/bin/docker-hash
