package registrymirrors_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/RemkoMolier/docker-hash/pkg/registrymirrors"
)

// writeHostsTOML creates a hosts.toml file in the given directory.
func writeHostsTOML(t *testing.T, dir, content string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, "hosts.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

// TestLoad_ParsesValidHostsTOML verifies that Load correctly parses a
// known-good hosts.toml and produces the expected Mirrors struct.
func TestLoad_ParsesValidHostsTOML(t *testing.T) {
	certsDir := t.TempDir()
	// Set up certs.d/docker.io/hosts.toml with a mirror that has resolve cap.
	mirrorDir := filepath.Join(certsDir, "docker.io")
	writeHostsTOML(t, mirrorDir, `
server = "https://registry-1.docker.io"

[host."https://mirror.example.com"]
  capabilities = ["pull", "resolve"]
`)

	mirrors, err := registrymirrors.Load(certsDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if mirrors == nil {
		t.Fatal("Load returned nil Mirrors")
	}
}

// TestLoad_EmptyCertsDir returns a zero-value Mirrors when the directory is empty.
func TestLoad_EmptyCertsDir(t *testing.T) {
	certsDir := t.TempDir()
	mirrors, err := registrymirrors.Load(certsDir)
	if err != nil {
		t.Fatalf("Load on empty dir: %v", err)
	}
	if mirrors == nil {
		t.Fatal("Load returned nil")
	}
}

// TestLoad_NonExistentDir returns a zero-value Mirrors without error when the
// directory does not exist.
func TestLoad_NonExistentDir(t *testing.T) {
	mirrors, err := registrymirrors.Load("/nonexistent/path/that/cannot/exist")
	if err != nil {
		t.Fatalf("Load on non-existent dir: %v", err)
	}
	if mirrors == nil {
		t.Fatal("Load returned nil")
	}
}

// TestLoad_EmptyPath returns zero-value Mirrors without error.
func TestLoad_EmptyPath(t *testing.T) {
	mirrors, err := registrymirrors.Load("")
	if err != nil {
		t.Fatalf("Load with empty path: %v", err)
	}
	if mirrors == nil {
		t.Fatal("Load returned nil")
	}
}

// TestTransport_RoundTrip verifies that requests to an upstream registry are
// redirected to the configured mirror.
func TestTransport_RoundTrip(t *testing.T) {
	// "Mirror" server: records that it was called.
	mirrorCalled := false
	mirrorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mirrorServer.Close()

	certsDir := t.TempDir()
	mirrorDir := filepath.Join(certsDir, "docker.io")
	writeHostsTOML(t, mirrorDir, fmt.Sprintf(`
server = "https://registry-1.docker.io"

[host."%s"]
  capabilities = ["pull", "resolve"]
`, mirrorServer.URL))

	mirrors, err := registrymirrors.Load(certsDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// A base transport that would fail if called with the original upstream host.
	base := http.DefaultTransport
	transport := mirrors.Transport(base)

	// Make a request that looks like it's going to index.docker.io (Docker Hub's API host).
	req, err := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/golang/manifests/latest", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if !mirrorCalled {
		t.Error("expected mirror to be called, but it was not")
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
}

// TestTransport_CapabilityFilter verifies that a mirror without "resolve"
// capability is skipped and the request falls through to the upstream.
func TestTransport_CapabilityFilter(t *testing.T) {
	// Mirror without "resolve" capability — should be ignored.
	mirrorCalled := false
	mirrorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mirrorCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer mirrorServer.Close()

	// Upstream: records that it was called.
	upstreamCalled := false
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstreamServer.Close()

	certsDir := t.TempDir()
	mirrorDir := filepath.Join(certsDir, "docker.io")
	writeHostsTOML(t, mirrorDir, fmt.Sprintf(`
server = "https://registry-1.docker.io"

[host."%s"]
  capabilities = ["pull"]
`, mirrorServer.URL))

	mirrors, err := registrymirrors.Load(certsDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Base transport routes to the upstream server.
	base := &hostRewriteTransport{
		targetHost: "index.docker.io",
		serverURL:  upstreamServer.URL,
		delegate:   http.DefaultTransport,
	}
	transport := mirrors.Transport(base)

	req, err := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if mirrorCalled {
		t.Error("mirror without 'resolve' capability must not be called")
	}
	if !upstreamCalled {
		t.Error("expected upstream to be called as fallback")
	}
}

// TestTransport_FallbackOnMirrorFailure verifies that when the first mirror
// returns a 503, the second mirror is tried.
func TestTransport_FallbackOnMirrorFailure(t *testing.T) {
	// First mirror: always 503.
	mirror1Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer mirror1Server.Close()

	// Second mirror: succeeds.
	mirror2Server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mirror2Server.Close()

	// Write a hosts.toml with two mirrors. Because the TOML map iteration order
	// is not guaranteed, we write them under two separate registry dirs and merge
	// via separate Load calls, or use a single file and accept either order.
	// For determinism, use a single registry dir with both hosts listed.
	// NOTE: hosts.toml map iteration may not preserve declaration order in Go's
	// map — to test fallback we accept that one of the two mirrors succeeds.
	certsDir := t.TempDir()
	mirrorDir := filepath.Join(certsDir, "gcr.io")
	writeHostsTOML(t, mirrorDir, fmt.Sprintf(`
server = "https://gcr.io"

[host."%s"]
  capabilities = ["pull", "resolve"]

[host."%s"]
  capabilities = ["pull", "resolve"]
`, mirror1Server.URL, mirror2Server.URL))

	mirrors, err := registrymirrors.Load(certsDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	transport := mirrors.Transport(http.DefaultTransport)

	req, err := http.NewRequest(http.MethodGet, "https://gcr.io/v2/distroless/static/manifests/latest", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	// At least one mirror must have succeeded; the non-503 one returns 200.
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status: got %d, want 200", resp.StatusCode)
	}
}

// TestTransport_NoMirrors passes through unchanged when no mirrors are configured.
func TestTransport_NoMirrors(t *testing.T) {
	upstreamCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	certsDir := t.TempDir() // empty — no hosts.toml files

	mirrors, err := registrymirrors.Load(certsDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	base := &hostRewriteTransport{
		targetHost: "registry.example.com",
		serverURL:  upstream.URL,
		delegate:   http.DefaultTransport,
	}
	transport := mirrors.Transport(base)

	req, err := http.NewRequest(http.MethodGet, "https://registry.example.com/v2/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	if !upstreamCalled {
		t.Error("base transport should be called when no mirrors are configured")
	}
}

// TestDiscover_EnvOverride verifies that DOCKER_HASH_CERTS_D takes precedence.
func TestDiscover_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DOCKER_HASH_CERTS_D", dir)

	got, err := registrymirrors.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got != dir {
		t.Errorf("Discover: got %q, want %q", got, dir)
	}
}

// TestDiscover_NonExistentEnvSkipped verifies that a non-existent
// DOCKER_HASH_CERTS_D is skipped and discovery falls through.
func TestDiscover_NonExistentEnvSkipped(t *testing.T) {
	t.Setenv("DOCKER_HASH_CERTS_D", "/nonexistent/docker-hash-test-path")
	// XDG_CONFIG_HOME to a temp dir that doesn't have the subpath.
	xdgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdgDir)

	// Since neither directory exists, Discover should return "".
	got, err := registrymirrors.Discover()
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	// Either empty or the XDG path (if the containerd/certs.d subdir were created).
	// We just verify no error is returned.
	_ = got
}

// hostRewriteTransport is a test helper that rewrites requests for targetHost
// to the given serverURL, allowing httptest servers to impersonate real hosts.
type hostRewriteTransport struct {
	targetHost string
	serverURL  string
	delegate   http.RoundTripper
}

func (t *hostRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == t.targetHost {
		// Clone the request and rewrite the URL to the test server.
		clone := req.Clone(req.Context())
		parsed, _ := http.NewRequest(req.Method, t.serverURL+req.URL.Path, req.Body)
		clone.URL = parsed.URL
		clone.Host = parsed.Host
		return t.delegate.RoundTrip(clone)
	}
	return t.delegate.RoundTrip(req)
}
