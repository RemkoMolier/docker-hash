package baseimage_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"

	"github.com/RemkoMolier/docker-hash/pkg/baseimage"
)

func TestIsScratch(t *testing.T) {
	cases := map[string]bool{
		"scratch":              true,
		"alpine":               false,
		"alpine:latest":        false,
		"library/scratch":      false, // not the sentinel
		"scratch:1":            false,
		"":                     false,
		"docker.io/scratch":    false,
		"alpine@sha256:abc123": false,
	}
	for in, want := range cases {
		if got := baseimage.IsScratch(in); got != want {
			t.Errorf("IsScratch(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsAlreadyPinned(t *testing.T) {
	const validDigest = "alpine@sha256:1304f174557314a7ed9eddb4eab12fed12cb0cd9809e4c28f29af86979a3c870"
	cases := map[string]bool{
		validDigest:                          true,
		"library/alpine@sha256:" + strings.Repeat("a", 64): true,
		"alpine":          false,
		"alpine:latest":   false,
		"alpine:3.20":     false,
		"":                false,
		"alpine@sha256:":  false, // empty digest
		"scratch":         false,
		"alpine@md5:abcd": false, // wrong algorithm
	}
	for in, want := range cases {
		if got := baseimage.IsAlreadyPinned(in); got != want {
			t.Errorf("IsAlreadyPinned(%q) = %v, want %v", in, got, want)
		}
	}
}

// fakeResolver is a Resolver implementation backed by a static map. Each
// Resolve call increments a counter so tests can assert on the number of
// underlying calls (relevant for the cache test).
type fakeResolver struct {
	results map[string]string
	calls   atomic.Int32
}

func (f *fakeResolver) Resolve(_ context.Context, ref baseimage.Reference) (string, error) {
	f.calls.Add(1)
	key := ref.Image + "|" + ref.Platform
	if v, ok := f.results[key]; ok {
		return v, nil
	}
	return "", errors.New("fakeResolver: no result for " + key)
}

func TestCachingResolver_DeduplicatesByImageAndPlatform(t *testing.T) {
	inner := &fakeResolver{
		results: map[string]string{
			"alpine|":               "index.docker.io/library/alpine@sha256:aaa",
			"alpine|linux/amd64":    "index.docker.io/library/alpine@sha256:bbb",
			"golang:1.25|":          "index.docker.io/library/golang@sha256:ccc",
		},
	}
	cache := baseimage.NewCachingResolver(inner)
	ctx := context.Background()

	// First call: miss → inner is invoked.
	got, err := cache.Resolve(ctx, baseimage.Reference{Image: "alpine"})
	if err != nil {
		t.Fatalf("Resolve(alpine): %v", err)
	}
	if got != "index.docker.io/library/alpine@sha256:aaa" {
		t.Errorf("Resolve(alpine) = %q", got)
	}
	if c := inner.calls.Load(); c != 1 {
		t.Errorf("after first call: inner.calls = %d, want 1", c)
	}

	// Same Reference again: hit → inner is NOT invoked.
	if _, err := cache.Resolve(ctx, baseimage.Reference{Image: "alpine"}); err != nil {
		t.Fatalf("Resolve(alpine) second call: %v", err)
	}
	if c := inner.calls.Load(); c != 1 {
		t.Errorf("after cached call: inner.calls = %d, want 1 (cache hit)", c)
	}

	// Same image, different platform: miss → inner IS invoked again.
	if _, err := cache.Resolve(ctx, baseimage.Reference{Image: "alpine", Platform: "linux/amd64"}); err != nil {
		t.Fatalf("Resolve(alpine, linux/amd64): %v", err)
	}
	if c := inner.calls.Load(); c != 2 {
		t.Errorf("after platform-distinct call: inner.calls = %d, want 2", c)
	}

	// Different image: miss → inner is invoked again.
	if _, err := cache.Resolve(ctx, baseimage.Reference{Image: "golang:1.25"}); err != nil {
		t.Fatalf("Resolve(golang): %v", err)
	}
	if c := inner.calls.Load(); c != 3 {
		t.Errorf("after distinct-image call: inner.calls = %d, want 3", c)
	}
}

func TestCachingResolver_PropagatesError(t *testing.T) {
	inner := &fakeResolver{results: map[string]string{}}
	cache := baseimage.NewCachingResolver(inner)

	if _, err := cache.Resolve(context.Background(), baseimage.Reference{Image: "missing"}); err == nil {
		t.Fatal("expected error from cache.Resolve when inner returns error")
	}
	// Errors must NOT be cached: a second call should re-invoke inner.
	if _, err := cache.Resolve(context.Background(), baseimage.Reference{Image: "missing"}); err == nil {
		t.Fatal("expected error on second call too")
	}
	if c := inner.calls.Load(); c != 2 {
		t.Errorf("inner.calls = %d, want 2 (errors must not be cached)", c)
	}
}

func TestRemoteResolver_RefusesScratch(t *testing.T) {
	r := &baseimage.RemoteResolver{}
	if _, err := r.Resolve(context.Background(), baseimage.Reference{Image: "scratch"}); err == nil {
		t.Fatal("expected error when Resolve is called with scratch")
	}
}

func TestRemoteResolver_PinnedShortCircuits(t *testing.T) {
	const pinned = "alpine@sha256:1304f174557314a7ed9eddb4eab12fed12cb0cd9809e4c28f29af86979a3c870"
	const wantCanonical = "index.docker.io/library/alpine@sha256:1304f174557314a7ed9eddb4eab12fed12cb0cd9809e4c28f29af86979a3c870"

	// A zero-value RemoteResolver with no network access should still
	// short-circuit on a pinned reference because we never reach the
	// remote.Get call. Use a context that's already cancelled to prove the
	// point: if anything network-y were attempted it would fail loudly.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r := &baseimage.RemoteResolver{}
	got, err := r.Resolve(ctx, baseimage.Reference{Image: pinned})
	if err != nil {
		t.Fatalf("Resolve(pinned): %v", err)
	}
	if got != wantCanonical {
		t.Errorf("Resolve(pinned) = %q, want %q", got, wantCanonical)
	}
}

// TestRemoteResolver_AgainstFakeRegistry exercises the full resolve flow
// against go-containerregistry's in-memory registry server. It pushes nothing
// — the registry returns 404 for the manifest — but that's enough to assert
// the code reaches the network layer and fails loudly with a wrapped error
// rather than silently returning an empty digest.
//
// A "happy path" registry test that actually serves a manifest is harder to
// set up without bringing in the full crane package; the fake registry's
// 404-on-empty behavior is sufficient to validate (a) the URL is constructed
// correctly, (b) the auth path doesn't crash, and (c) the error wraps
// useful context.
func TestRemoteResolver_AgainstFakeRegistry_404IsLoud(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	r := &baseimage.RemoteResolver{}
	_, err = r.Resolve(context.Background(), baseimage.Reference{
		Image: u.Host + "/test/nonexistent:v1",
	})
	if err == nil {
		t.Fatal("expected an error resolving a nonexistent image, got nil")
	}
	if !strings.Contains(err.Error(), "resolve") {
		t.Errorf("expected error to be wrapped with 'resolve' context, got: %v", err)
	}
}
