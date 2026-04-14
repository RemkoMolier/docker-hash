package baseimage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// dummyDigest is a 64-hex placeholder used by ValidateCheckTemplate to
// stand in for the (not-yet-computed) real digest. Its exact value doesn't
// matter — we only need something that parses as a valid digest so the
// reference parser accepts the rendered template.
const dummyDigest = "0000000000000000000000000000000000000000000000000000000000000000"

// ValidateCheckTemplate verifies that the given --check template, once
// {hash} is substituted, parses as a valid image reference. Callers use
// this at flag-parse time so malformed templates fail before the
// (potentially expensive) Dockerfile hash step runs.
func ValidateCheckTemplate(template string) error {
	rendered := strings.ReplaceAll(template, "{hash}", dummyDigest)
	if _, err := name.ParseReference(rendered); err != nil {
		return fmt.Errorf("invalid --check template %q: %w", template, err)
	}
	return nil
}

// ErrImageNotFound is returned by Check when the registry responds with a
// 404 for the manifest. CI callers use this signal to decide "image is not
// cached, kick off the build" — distinct from a network or auth error where
// the right answer is "retry the job."
var ErrImageNotFound = errors.New("baseimage: image not found")

// Checker probes a registry for the existence of a fully-qualified image
// reference. It shares the Transport + Keychain extension points with
// RemoteResolver so the --auth-file and --registries-conf plumbing applies
// to existence checks the same way it applies to FROM digest resolution.
//
// A zero-value Checker is usable; it uses authn.DefaultKeychain and the
// default HTTP transport.
type Checker struct {
	// Keychain controls authentication. nil means authn.DefaultKeychain.
	Keychain authn.Keychain

	// Transport, if non-nil, is the RoundTripper go-containerregistry will
	// use for the HEAD request. Wire the same mirror-aware transport here
	// that RemoteResolver uses so corporate mirrors apply to checks too.
	Transport http.RoundTripper
}

// Check returns nil if a manifest exists at imageRef, ErrImageNotFound if
// the registry responds with 404, or a wrapped error for any other
// registry/network/auth failure.
//
// imageRef must be a full tag or digest reference (e.g. "my-reg/app:build-abc"
// or "alpine@sha256:..."); it is NOT expanded against any resolver state.
func (c *Checker) Check(ctx context.Context, imageRef string) error {
	parsed, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parse reference %q: %w", imageRef, err)
	}

	keychain := c.Keychain
	if keychain == nil {
		keychain = authn.DefaultKeychain
	}

	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(keychain),
	}
	if c.Transport != nil {
		opts = append(opts, remote.WithTransport(c.Transport))
	}

	if _, err := remote.Head(parsed, opts...); err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return ErrImageNotFound
		}
		return fmt.Errorf("check %q: %w", imageRef, err)
	}
	return nil
}
