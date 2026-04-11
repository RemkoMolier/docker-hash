// Package baseimage resolves Dockerfile FROM references to canonical
// "<repo>@sha256:..." digest strings, so the digest can be folded into the
// docker-hash output and a tag drift in the upstream registry produces a
// different hash for an otherwise unchanged Dockerfile.
//
// Resolution rules (see RELEASING.md / issue #44 for the full design):
//
//   - "FROM scratch" is the Docker-internal sentinel and is never resolved.
//     Callers should detect it via IsScratch and hash a literal "scratch"
//     contribution instead of calling a Resolver.
//   - "FROM <stage>" (where <stage> is a name declared earlier via
//     "FROM ... AS <stage>") is an internal multi-stage reference, not a
//     registry image. Callers should detect this via the parser's IsStageRef
//     bit and skip resolution.
//   - "FROM <repo>@sha256:..." is already pinned. Callers can pass it
//     through Resolve unchanged; the implementation in this package will
//     short-circuit and return the canonical form without a network call.
//   - "FROM <repo>:<tag>" is the case that actually needs resolution.
//     RemoteResolver fetches the manifest digest from the upstream registry.
//   - "FROM --platform=<plat> <repo>:<tag>" is resolved to that platform's
//     manifest digest, not the multi-arch index digest.
//
// Authentication flows through google/go-containerregistry's default
// keychain, which natively reads (in order):
//
//  1. $HOME/.docker/config.json
//  2. $DOCKER_CONFIG/config.json
//  3. $REGISTRY_AUTH_FILE (Podman / Skopeo / Buildah convention)
//  4. $XDG_RUNTIME_DIR/containers/auth.json (Podman default path)
//
// Cloud-provider credential helpers (ECR, GCR, ACR) are supported transparently
// via the same keychain when their helper binaries are installed.
package baseimage

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Reference is a parsed Dockerfile FROM target plus any per-line modifiers
// the resolver needs to know about. Constructing a Reference is the caller's
// job; this package does not parse Dockerfiles.
type Reference struct {
	// Image is the literal target text after "FROM " and before "AS",
	// without the --platform= flag and without the stage alias. Examples:
	// "golang:1.25", "alpine", "ubuntu@sha256:abc...", "scratch".
	Image string

	// Platform is the value of "--platform=" on the FROM line, if any.
	// Empty for FROM lines without the flag.
	Platform string
}

// Resolver turns a Reference into a canonical "<repo>@sha256:..." digest
// string suitable for folding into a hash. Implementations may make network
// calls or may be entirely in-memory (for tests).
type Resolver interface {
	Resolve(ctx context.Context, ref Reference) (string, error)
}

// IsScratch reports whether the given image string is the special
// "FROM scratch" sentinel. Scratch is not a registry image and must not be
// sent to a network resolver.
func IsScratch(image string) bool {
	return image == "scratch"
}

// IsAlreadyPinned reports whether the given image string already includes a
// digest, e.g. "alpine@sha256:abc...". When true, no network call is needed
// to compute the canonical form — Canonicalize handles it offline.
func IsAlreadyPinned(image string) bool {
	_, err := name.NewDigest(image)
	return err == nil
}

// Canonicalize takes a pinned image reference (one that already includes a
// "@sha256:..." digest) and returns its canonical "<registry>/<repo>@sha256:..."
// form. It performs no network access and is the offline equivalent of what
// a Resolver would return for the same input.
//
// Returns an error if image is not a valid pinned digest reference. Callers
// should typically gate this behind IsAlreadyPinned.
func Canonicalize(image string) (string, error) {
	pinned, err := name.NewDigest(image)
	if err != nil {
		return "", fmt.Errorf("baseimage: canonicalize %q: %w", image, err)
	}
	return pinned.Name(), nil
}

// CanonicalName returns the fully-qualified canonical form of an image
// reference WITHOUT performing any network access. For an unpinned reference
// like "alpine" it returns "index.docker.io/library/alpine:latest"; for a
// pinned reference it returns the same thing Canonicalize would. The function
// is the offline-mode equivalent of asking a Resolver to canonicalize the
// reference: it folds in the implicit default registry, library namespace and
// "latest" tag so that "alpine", "alpine:latest" and "library/alpine" all
// hash to the same canonical text.
func CanonicalName(image string) (string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", fmt.Errorf("baseimage: canonical name %q: %w", image, err)
	}
	return ref.Name(), nil
}

// RemoteResolver is the production Resolver implementation. It uses
// google/go-containerregistry to fetch image manifests from the upstream
// registry and returns the canonical "<repo>@<digest>" form.
//
// A zero-value RemoteResolver is usable: it falls back to authn.DefaultKeychain
// for authentication and emits the multi-arch index digest for images that do
// not specify a per-line --platform.
type RemoteResolver struct {
	// PlatformOverride forces all resolutions to use this platform when the
	// Reference itself does not specify one. The expected format is
	// "<os>/<arch>" or "<os>/<arch>/<variant>" (e.g. "linux/amd64",
	// "linux/arm/v7"). Empty means "use the index digest for multi-arch
	// images" — the default and the right choice for cross-platform CI
	// determinism.
	PlatformOverride string

	// Keychain controls authentication. nil means authn.DefaultKeychain
	// (which reads $HOME/.docker/config.json + $DOCKER_CONFIG +
	// $REGISTRY_AUTH_FILE + $XDG_RUNTIME_DIR/containers/auth.json + cloud
	// helpers).
	Keychain authn.Keychain
}

// Resolve fetches the digest for the given reference and returns the
// canonical "<repo>@<digest>" string. Pinned references short-circuit
// without network access.
func (r *RemoteResolver) Resolve(ctx context.Context, ref Reference) (string, error) {
	if IsScratch(ref.Image) {
		return "", fmt.Errorf("baseimage: refusing to resolve %q (callers should detect scratch and skip)", ref.Image)
	}

	// Already pinned: return the canonical form without network access.
	// name.NewDigest validates the digest format and yields a Reference
	// whose Name() is "<registry>/<repo>@sha256:...".
	if pinned, err := name.NewDigest(ref.Image); err == nil {
		return pinned.Name(), nil
	}

	parsed, err := name.ParseReference(ref.Image)
	if err != nil {
		return "", fmt.Errorf("parse reference %q: %w", ref.Image, err)
	}

	keychain := r.Keychain
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}

	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(keychain),
	}

	// Platform: explicit on the FROM line wins; otherwise CLI override;
	// otherwise unset (multi-arch index digest).
	platform := ref.Platform
	if platform == "" {
		platform = r.PlatformOverride
	}
	if platform != "" {
		plat, err := parsePlatform(platform)
		if err != nil {
			return "", fmt.Errorf("parse platform %q: %w", platform, err)
		}
		opts = append(opts, remote.WithPlatform(plat))
	}

	desc, err := remote.Get(parsed, opts...)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", ref.Image, err)
	}

	// Use the parsed Context().Name() (= "<registry>/<repo>") rather than
	// the original literal so that "alpine", "alpine:latest" and
	// "library/alpine" all hash as the same canonical
	// "index.docker.io/library/alpine@<digest>" — they refer to the same
	// image content and should therefore produce the same docker-hash digest.
	return fmt.Sprintf("%s@%s", parsed.Context().Name(), desc.Digest.String()), nil
}

// CachingResolver wraps another Resolver and memoises results by
// (Image, Platform) pair. Create a new CachingResolver per Compute() call so
// the cache stays scoped to a single hash computation — the whole point of
// resolving every run is to detect when registry tags drift, and a long-lived
// cache would defeat that.
type CachingResolver struct {
	inner Resolver

	mu    sync.Mutex
	cache map[string]string
}

// NewCachingResolver returns a CachingResolver wrapping inner.
func NewCachingResolver(inner Resolver) *CachingResolver {
	return &CachingResolver{inner: inner, cache: make(map[string]string)}
}

// Resolve checks the in-process cache first; on miss it delegates to the
// wrapped resolver and stores the result.
func (r *CachingResolver) Resolve(ctx context.Context, ref Reference) (string, error) {
	key := ref.Image + "|" + ref.Platform

	r.mu.Lock()
	if cached, ok := r.cache[key]; ok {
		r.mu.Unlock()
		return cached, nil
	}
	r.mu.Unlock()

	resolved, err := r.inner.Resolve(ctx, ref)
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	r.cache[key] = resolved
	r.mu.Unlock()
	return resolved, nil
}

// parsePlatform parses an "<os>/<arch>" or "<os>/<arch>/<variant>" string
// into a v1.Platform. Returns an error if the input does not match either
// shape.
func parsePlatform(s string) (v1.Platform, error) {
	parts := strings.Split(s, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return v1.Platform{}, fmt.Errorf("expected <os>/<arch> or <os>/<arch>/<variant>, got %q", s)
	}
	p := v1.Platform{OS: parts[0], Architecture: parts[1]}
	if len(parts) == 3 {
		p.Variant = parts[2]
	}
	return p, nil
}
