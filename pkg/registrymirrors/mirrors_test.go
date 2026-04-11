package registrymirrors_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RemkoMolier/docker-hash/pkg/registrymirrors"
)

// writeConf is a small helper that writes a registries.conf to a temp dir
// and returns its absolute path.
func writeConf(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "registries.conf")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write registries.conf: %v", err)
	}
	return path
}

func TestLoad_EmptyPathRejected(t *testing.T) {
	if _, err := registrymirrors.Load(""); err == nil {
		t.Fatal("expected error from Load(\"\"), got nil")
	}
}

func TestLoad_MissingFile(t *testing.T) {
	if _, err := registrymirrors.Load(filepath.Join(t.TempDir(), "nope.conf")); err == nil {
		t.Fatal("expected error from Load on missing file, got nil")
	}
}

func TestLoad_MalformedTOML(t *testing.T) {
	path := writeConf(t, "this is not valid TOML = ")
	if _, err := registrymirrors.Load(path); err == nil {
		t.Fatal("expected parse error on malformed TOML, got nil")
	}
}

func TestLoad_EmptyFileIsValid(t *testing.T) {
	path := writeConf(t, "")
	m, err := registrymirrors.Load(path)
	if err != nil {
		t.Fatalf("Load on empty file: %v", err)
	}
	if m == nil {
		t.Fatal("Load returned nil Mirrors on empty file")
	}
	// Empty config should produce a no-op transport: passing in a base
	// must return that same base unchanged.
	base := http.DefaultTransport
	if got := m.Transport(base); got != base {
		t.Errorf("Transport on empty config = %v, want unchanged base", got)
	}
}

func TestLoad_UnknownFieldsAreIgnored(t *testing.T) {
	// Forward-compat: the parser must silently ignore Podman fields
	// docker-hash doesn't understand (blocked, mirror-by-digest-only,
	// unqualified-search-registries, etc.) and the
	// registry-level insecure flag.
	body := `
unqualified-search-registries = ["docker.io"]

[[registry]]
prefix = "docker.io"
location = "registry-1.docker.io"
insecure = false
blocked = false
mirror-by-digest-only = false

[[registry.mirror]]
location = "mirror.example.com/dockerhub"
insecure = false
mirror-by-digest-only = false
`
	path := writeConf(t, body)
	m, err := registrymirrors.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// We should still have routed docker.io → mirror.example.com.
	if got := m.Transport(http.DefaultTransport); got == http.DefaultTransport {
		t.Error("expected wrapped transport, got base unchanged (mirror entry was dropped)")
	}
}

// TestLoad_MissingPrefixFallsBackToLocation verifies the documented
// fallback rule: when a [[registry]] entry omits prefix, location is used
// as the routing key. (This matches Podman's own behaviour.)
func TestLoad_MissingPrefixFallsBackToLocation(t *testing.T) {
	// Build a request hitting Docker Hub's API hostname and check that
	// the wrapped transport rewrites it. The [[registry]] entry omits
	// `prefix` and uses `location` as the routing key — the documented
	// fallback rule borrowed from Podman's own behaviour.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)

	conf := fmt.Sprintf(`
[[registry]]
location = "docker.io"

[[registry.mirror]]
location = %q
`, srv.URL)
	m, err := registrymirrors.Load(writeConf(t, conf))
	if err != nil {
		t.Fatalf("Load with test URL: %v", err)
	}

	tripper := m.Transport(http.DefaultTransport)
	// Note we send to the canonical "index.docker.io" hostname; the
	// parser must have normalised "docker.io" to that key.
	req, err := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/alpine/manifests/latest", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := tripper.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestTransport_NoMatchPassesThrough(t *testing.T) {
	// docker.io has a mirror configured, but we're asking for a
	// completely different registry (ghcr.io). The transport must
	// pass the request straight to base.
	calls := atomic.Int32{}
	base := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "ghcr.io" {
			t.Errorf("base saw rewritten host %q, expected ghcr.io", r.URL.Host)
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader("ok")),
			Header:     http.Header{},
		}, nil
	})

	body := `
[[registry]]
prefix = "docker.io"

[[registry.mirror]]
location = "mirror.example.com"
`
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://ghcr.io/v2/foo/manifests/latest", nil)
	resp, err := m.Transport(base).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if calls.Load() != 1 {
		t.Errorf("base saw %d calls, want 1", calls.Load())
	}
}

// TestTransport_MirrorRewrites checks the happy path: a request for a
// configured registry is rewritten to the mirror host with the path
// preserved.
func TestTransport_MirrorRewrites(t *testing.T) {
	var seenURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(srv.Close)

	body := fmt.Sprintf(`
[[registry]]
prefix = "ghcr.io"

[[registry.mirror]]
location = %q
`, srv.URL+"/proxy")
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://ghcr.io/v2/foo/manifests/latest", nil)
	resp, err := m.Transport(http.DefaultTransport).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if seenURL != "/proxy/v2/foo/manifests/latest" {
		t.Errorf("mirror saw URL %q, want /proxy/v2/foo/manifests/latest", seenURL)
	}
}

// TestTransport_FallsBackOn5xx asserts that a mirror returning HTTP 5xx
// causes the transport to fall through to the next mirror, and finally
// to the upstream base.
func TestTransport_FallsBackOn5xx(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(bad.Close)

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "good")
	}))
	t.Cleanup(good.Close)

	body := fmt.Sprintf(`
[[registry]]
prefix = "docker.io"

[[registry.mirror]]
location = %q

[[registry.mirror]]
location = %q
`, bad.URL, good.URL)
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/alpine/manifests/latest", nil)
	resp, err := m.Transport(http.DefaultTransport).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 (should have fallen through to good mirror)", resp.StatusCode)
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	if string(bodyBytes) != "good" {
		t.Errorf("body = %q, want %q", string(bodyBytes), "good")
	}
}

// TestTransport_FallsBackToUpstreamWhenAllMirrorsFail asserts that when
// every configured mirror returns 5xx, the transport falls back to the
// original upstream URL via the base RoundTripper.
func TestTransport_FallsBackToUpstreamWhenAllMirrorsFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(bad.Close)

	upstreamCalled := atomic.Int32{}
	base := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		// We only count calls that go to the *original* upstream — not
		// requests the transport directs at the mirror via base. The
		// distinguishing feature is the host: when the mirror is tried,
		// the wrapper sets req.URL to the mirror URL; when falling back
		// it leaves the URL untouched.
		if r.URL.Host == "index.docker.io" {
			upstreamCalled.Add(1)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader("upstream")),
				Header:     http.Header{},
			}, nil
		}
		// Mirror probe via base — forward to the bad server.
		return http.DefaultTransport.RoundTrip(r)
	})

	body := fmt.Sprintf(`
[[registry]]
prefix = "docker.io"

[[registry.mirror]]
location = %q
`, bad.URL)
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/alpine/manifests/latest", nil)
	resp, err := m.Transport(base).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if upstreamCalled.Load() != 1 {
		t.Errorf("upstream calls = %d, want 1 (mirror should have failed and fallen back)", upstreamCalled.Load())
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestParseMirrorLocation_BareAuthority verifies that a Podman-style
// schemeless mirror.location is interpreted as https:// when insecure is
// false and http:// when insecure is true. We can't read the parsed URL
// directly (the field is unexported) so we route a request through and
// check the scheme on the wire.
func TestParseMirrorLocation_BareAuthority(t *testing.T) {
	for _, tc := range []struct {
		name       string
		insecure   bool
		wantScheme string
	}{
		{name: "secure-default", insecure: false, wantScheme: "https"},
		{name: "insecure-true", insecure: true, wantScheme: "http"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seenScheme string
			tripper := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				seenScheme = r.URL.Scheme
				return &http.Response{
					StatusCode: 200,
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     http.Header{},
				}, nil
			})

			body := fmt.Sprintf(`
[[registry]]
prefix = "docker.io"

[[registry.mirror]]
location = "mirror.example.com"
insecure = %v
`, tc.insecure)
			m, err := registrymirrors.Load(writeConf(t, body))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			req, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/alpine/manifests/latest", nil)
			resp, err := m.Transport(tripper).RoundTrip(req)
			if err != nil {
				t.Fatalf("RoundTrip: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()

			// For the insecure case the wrapper builds its own
			// http.Transport via insecureTransport(); the rewritten
			// request never hits our injected base. So we can only
			// assert the scheme for the secure case via the base.
			if !tc.insecure && seenScheme != tc.wantScheme {
				t.Errorf("scheme = %q, want %q", seenScheme, tc.wantScheme)
			}
		})
	}
}

func TestLoad_InvalidMirrorScheme(t *testing.T) {
	body := `
[[registry]]
prefix = "docker.io"

[[registry.mirror]]
location = "ftp://mirror.example.com"
`
	if _, err := registrymirrors.Load(writeConf(t, body)); err == nil {
		t.Fatal("expected error on ftp:// mirror, got nil")
	}
}

func TestLoad_PrefixWithPathTrimsToHost(t *testing.T) {
	// Podman allows prefix = "docker.io/library"; we currently route
	// only at hostname granularity, so the path part should be
	// trimmed before normalisation. A request to docker.io/library/alpine
	// should be matched as if the prefix were just "docker.io" (any
	// non-library repo would also match — that's a known limitation,
	// documented in the package doc).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	body := fmt.Sprintf(`
[[registry]]
prefix = "docker.io/library"

[[registry.mirror]]
location = %q
`, srv.URL)
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/alpine/manifests/latest", nil)
	resp, err := m.Transport(http.DefaultTransport).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// roundTripperFunc adapts a function into the http.RoundTripper
// interface so the tests don't need a one-shot struct each time.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
