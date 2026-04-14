package baseimage_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"

	"github.com/RemkoMolier/docker-hash/pkg/baseimage"
)

// TestChecker_NotFound exercises the 404 path against the in-memory fake
// registry. The registry starts empty, so any tag lookup returns 404, which
// must surface as ErrImageNotFound (the exit-3 signal for CI callers).
func TestChecker_NotFound(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	c := &baseimage.Checker{}
	err = c.Check(context.Background(), u.Host+"/test/nonexistent:v1")
	if !errors.Is(err, baseimage.ErrImageNotFound) {
		t.Fatalf("expected ErrImageNotFound for a missing tag, got: %v", err)
	}
}

// TestChecker_RegistryError ensures non-404 failures (e.g. the registry
// being unreachable) are NOT conflated with ErrImageNotFound. CI branches
// on this distinction: "not found" means build, "registry error" means
// retry the job.
func TestChecker_RegistryError(t *testing.T) {
	// Start a server then immediately close it, so any request to it
	// fails at the TCP layer. That produces a non-transport.Error which
	// Check must wrap (not re-map to ErrImageNotFound).
	srv := httptest.NewServer(registry.New())
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	srv.Close()

	c := &baseimage.Checker{}
	err = c.Check(context.Background(), u.Host+"/test/app:v1")
	if err == nil {
		t.Fatal("expected an error talking to a closed server, got nil")
	}
	if errors.Is(err, baseimage.ErrImageNotFound) {
		t.Fatalf("network failure must not be mapped to ErrImageNotFound, got: %v", err)
	}
	if !strings.Contains(err.Error(), "check") {
		t.Errorf("expected error to be wrapped with 'check' context, got: %v", err)
	}
}

func TestValidateCheckTemplate(t *testing.T) {
	cases := []struct {
		name     string
		template string
		wantErr  bool
	}{
		{name: "plain tag", template: "my-reg/app:build-{hash}"},
		{name: "with registry port", template: "registry.local:5000/app:build-{hash}"},
		{name: "prefix and suffix around hash", template: "org/app:v1-{hash}-amd64"},

		{name: "uppercase repo rejected", template: "Org/App:build-{hash}", wantErr: true},
		{name: "trailing dash is valid in tags", template: "org/app:{hash}-"},
		{name: "nonsense reference", template: "::::{hash}::::", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := baseimage.ValidateCheckTemplate(tc.template)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q, got nil", tc.template)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.template, err)
			}
		})
	}
}

// TestChecker_HonoursTransport proves the Transport field is threaded
// through to the HEAD request, so the --registries-conf mirror stack
// applies to existence checks too.
func TestChecker_HonoursTransport(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	ct := &countingTransport{inner: http.DefaultTransport}
	c := &baseimage.Checker{Transport: ct}

	_ = c.Check(context.Background(), u.Host+"/test/nonexistent:v1")

	if got := ct.calls.Load(); got == 0 {
		t.Fatal("expected Checker.Transport to receive at least one request, got 0")
	}
}
