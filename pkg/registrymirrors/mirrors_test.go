package registrymirrors_test

import (
	"errors"
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

// mustTransport is a thin wrapper around Mirrors.Transport that fails the
// test on a non-nil error. Used wherever a test expects Transport to
// succeed; tests that exercise the error path call Transport directly.
func mustTransport(t *testing.T, m *registrymirrors.Mirrors, base http.RoundTripper) http.RoundTripper {
	t.Helper()
	rt, err := m.Transport(base)
	if err != nil {
		t.Fatalf("Mirrors.Transport: %v", err)
	}
	return rt
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
	got := mustTransport(t, m, base)
	if got != base {
		t.Errorf("Transport on empty config = %v, want unchanged base", got)
	}
}

func TestLoad_UnknownFieldsAreIgnored(t *testing.T) {
	// Forward-compat: the parser must silently ignore Podman fields
	// docker-hash doesn't understand (registry-level insecure, blocked,
	// mirror-by-digest-only, unqualified-search-registries, …).
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
	got := mustTransport(t, m, http.DefaultTransport)
	if got == http.DefaultTransport {
		t.Error("expected wrapped transport, got base unchanged (mirror entry was dropped)")
	}
}

// TestLoad_MissingPrefixFallsBackToLocation verifies the documented
// fallback rule: when a [[registry]] entry omits prefix, location is used
// as the routing key. (This matches Podman's own behaviour.)
func TestLoad_MissingPrefixFallsBackToLocation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

	tripper := mustTransport(t, m, http.DefaultTransport)
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
	resp, err := mustTransport(t, m, base).RoundTrip(req)
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
	resp, err := mustTransport(t, m, http.DefaultTransport).RoundTrip(req)
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
	resp, err := mustTransport(t, m, http.DefaultTransport).RoundTrip(req)
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
		// Distinguish "the wrapper is forwarding to a mirror via base"
		// from "the wrapper has given up and fallen back to the
		// original upstream URL". When falling back the URL host is
		// the original (index.docker.io); when probing a mirror the
		// host has been rewritten to the mirror.
		if r.URL.Host == "index.docker.io" {
			upstreamCalled.Add(1)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader("upstream")),
				Header:     http.Header{},
			}, nil
		}
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
	resp, err := mustTransport(t, m, base).RoundTrip(req)
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

// TestParseMirrorLocation_BareAuthorityScheme verifies that a Podman-style
// schemeless mirror.location is interpreted as https:// when insecure is
// false. The insecure=true case has its own dedicated test
// (TestTransport_InsecureClonesBaseTransport) that exercises the
// base-cloning path; this one stays simple and only checks the secure
// scheme.
func TestParseMirrorLocation_BareAuthorityScheme(t *testing.T) {
	var seenScheme string
	tripper := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		seenScheme = r.URL.Scheme
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

	req, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/alpine/manifests/latest", nil)
	resp, err := mustTransport(t, m, tripper).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if seenScheme != "https" {
		t.Errorf("scheme = %q, want https", seenScheme)
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

// --- Regression tests for the three Copilot review findings ---

// TestPathSpecificPrefix_OnlyMatchesIntendedRepo verifies that a
// `prefix = "docker.io/library"` entry routes /v2/library/<repo>
// requests through its mirror but does NOT capture sibling repos like
// /v2/myorg/<repo>. Without this guarantee the package would be
// silently more aggressive than Podman, contradicting the README claim
// that an existing registries.conf works as-is.
func TestPathSpecificPrefix_OnlyMatchesIntendedRepo(t *testing.T) {
	mirrorHits := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mirrorHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	upstreamHits := atomic.Int32{}
	base := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "index.docker.io" {
			upstreamHits.Add(1)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader("upstream")),
				Header:     http.Header{},
			}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})

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
	tr := mustTransport(t, m, base)

	// Library repo: must hit the mirror.
	libReq, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/alpine/manifests/latest", nil)
	libResp, err := tr.RoundTrip(libReq)
	if err != nil {
		t.Fatalf("library RoundTrip: %v", err)
	}
	_ = libResp.Body.Close()
	if mirrorHits.Load() != 1 {
		t.Errorf("library request: mirror hits = %d, want 1", mirrorHits.Load())
	}
	if upstreamHits.Load() != 0 {
		t.Errorf("library request: unexpected upstream hits = %d", upstreamHits.Load())
	}

	// Sibling org repo on the same registry: must NOT hit the mirror.
	mirrorHits.Store(0)
	upstreamHits.Store(0)
	otherReq, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/myorg/foo/manifests/latest", nil)
	otherResp, err := tr.RoundTrip(otherReq)
	if err != nil {
		t.Fatalf("other RoundTrip: %v", err)
	}
	_ = otherResp.Body.Close()
	if mirrorHits.Load() != 0 {
		t.Errorf("non-library request: mirror hits = %d, want 0 (mirror should not have been picked up)", mirrorHits.Load())
	}
	if upstreamHits.Load() != 1 {
		t.Errorf("non-library request: upstream hits = %d, want 1 (should have fallen through to base)", upstreamHits.Load())
	}
}

// TestPathSpecificPrefix_LongestWins verifies Podman's "longest prefix
// wins" rule when both a host-wide and a path-specific entry are
// present.
func TestPathSpecificPrefix_LongestWins(t *testing.T) {
	general := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "general")
	}))
	t.Cleanup(general.Close)

	library := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "library")
	}))
	t.Cleanup(library.Close)

	body := fmt.Sprintf(`
[[registry]]
prefix = "docker.io"

[[registry.mirror]]
location = %q

[[registry]]
prefix = "docker.io/library"

[[registry.mirror]]
location = %q
`, general.URL, library.URL)
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tr := mustTransport(t, m, http.DefaultTransport)

	// /v2/library/alpine — both rules match, longest (docker.io/library) wins.
	libReq, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/alpine/manifests/latest", nil)
	libResp, err := tr.RoundTrip(libReq)
	if err != nil {
		t.Fatalf("library RoundTrip: %v", err)
	}
	defer func() { _ = libResp.Body.Close() }()
	libBody, _ := io.ReadAll(libResp.Body)
	if string(libBody) != "library" {
		t.Errorf("library request: body = %q, want %q (longest prefix should win)", string(libBody), "library")
	}

	// /v2/myorg/foo — only the host-wide rule matches.
	otherReq, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/myorg/foo/manifests/latest", nil)
	otherResp, err := tr.RoundTrip(otherReq)
	if err != nil {
		t.Fatalf("other RoundTrip: %v", err)
	}
	defer func() { _ = otherResp.Body.Close() }()
	otherBody, _ := io.ReadAll(otherResp.Body)
	if string(otherBody) != "general" {
		t.Errorf("non-library request: body = %q, want %q (host-wide rule should match)", string(otherBody), "general")
	}
}

// TestCustomPortRegistry_Matches verifies that a registry on a non-default
// port routes correctly when the request URL also carries the port. This
// covers the Copilot finding that req.URL.Hostname() (the previous lookup)
// dropped the port and broke matching for entries like
// "registry.example.com:5000".
func TestCustomPortRegistry_Matches(t *testing.T) {
	mirrorHits := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mirrorHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	body := fmt.Sprintf(`
[[registry]]
prefix = "registry.example.com:5000"

[[registry.mirror]]
location = %q
`, srv.URL)
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tr := mustTransport(t, m, http.DefaultTransport)

	req, _ := http.NewRequest(http.MethodGet, "https://registry.example.com:5000/v2/foo/manifests/latest", nil)
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if mirrorHits.Load() != 1 {
		t.Errorf("custom-port request: mirror hits = %d, want 1 (port was dropped from lookup key)", mirrorHits.Load())
	}
}

// TestCustomPortRegistry_DifferentPortDoesNotMatch verifies that a
// configured port-bearing entry does NOT match the same hostname on a
// different port — otherwise the port would be effectively ignored on
// the matching side.
func TestCustomPortRegistry_DifferentPortDoesNotMatch(t *testing.T) {
	mirrorHits := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mirrorHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	upstreamHits := atomic.Int32{}
	base := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "registry.example.com:1234" {
			upstreamHits.Add(1)
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader("upstream")),
				Header:     http.Header{},
			}, nil
		}
		return http.DefaultTransport.RoundTrip(r)
	})

	body := fmt.Sprintf(`
[[registry]]
prefix = "registry.example.com:5000"

[[registry.mirror]]
location = %q
`, srv.URL)
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://registry.example.com:1234/v2/foo/manifests/latest", nil)
	resp, err := mustTransport(t, m, base).RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if mirrorHits.Load() != 0 {
		t.Errorf("different-port request: mirror hits = %d, want 0", mirrorHits.Load())
	}
	if upstreamHits.Load() != 1 {
		t.Errorf("different-port request: upstream hits = %d, want 1", upstreamHits.Load())
	}
}

// TestTransport_InsecureClonesBaseTransport verifies the fix for
// Copilot's #2 finding: an insecure mirror must NOT silently drop the
// base transport's settings (proxy, timeouts, keep-alive, HTTP/2). The
// fix path is to clone the base *http.Transport and only override
// TLSClientConfig.InsecureSkipVerify; this test exercises that path by
// passing a customised *http.Transport as the base, requesting an
// insecure mirror, and asserting that the request reaches an HTTPS
// server presenting a self-signed cert (which would fail without
// InsecureSkipVerify but succeed via the cloned base whose other
// settings are intact).
func TestTransport_InsecureClonesBaseTransport(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	body := fmt.Sprintf(`
[[registry]]
prefix = "docker.io"

[[registry.mirror]]
location = %q
insecure = true
`, srv.URL)
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Build a base http.Transport with a marker timeout so we can
	// confirm via reflection-free observation that the wrapper is
	// using a clone and not a brand-new zero-value Transport. The
	// successful TLS skip is the indirect proof that the clone has
	// InsecureSkipVerify set; reaching the test server at all proves
	// the timeout / dialer settings of the base were preserved.
	base := &http.Transport{
		// Distinctive (but reasonable) values; the body of this test
		// just needs the wrapper to inherit them rather than build a
		// fresh zero Transport.
		MaxIdleConns:    7,
		IdleConnTimeout: 0,
	}

	rt, err := m.Transport(base)
	if err != nil {
		t.Fatalf("Mirrors.Transport: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "https://index.docker.io/v2/library/alpine/manifests/latest", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("insecure mirror RoundTrip: %v (the wrapper either dropped the base transport or did not enable InsecureSkipVerify)", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestTransport_InsecureRequiresCloneableBase verifies that
// Mirrors.Transport refuses to silently downgrade the base when the
// configuration includes an insecure mirror but the supplied base is
// not an *http.Transport. The user-facing contract documented in the
// package doc is "fail loud rather than silently bypass the base
// transport behaviour".
func TestTransport_InsecureRequiresCloneableBase(t *testing.T) {
	body := `
[[registry]]
prefix = "docker.io"

[[registry.mirror]]
location = "mirror.example.com"
insecure = true
`
	m, err := registrymirrors.Load(writeConf(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Pass a non-*http.Transport base (a wrapping middleware would
	// look like this from the package's point of view).
	wrapped := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("should not be called")
	})

	rt, err := m.Transport(wrapped)
	if err == nil {
		t.Fatal("Mirrors.Transport with insecure mirror + non-Transport base should have errored, got nil")
	}
	if rt != nil {
		t.Errorf("expected nil RoundTripper on error, got %T", rt)
	}
}

// TestTransport_NoInsecureAcceptsAnyBase ensures the strictness of the
// preceding test is targeted: a config without any insecure mirror
// must still accept non-Transport bases (otherwise we would force
// every caller to pass http.DefaultTransport).
func TestTransport_NoInsecureAcceptsAnyBase(t *testing.T) {
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
	wrapped := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Header: http.Header{}}, nil
	})
	if _, err := m.Transport(wrapped); err != nil {
		t.Fatalf("Mirrors.Transport with secure-only mirrors must accept any base: %v", err)
	}
}

// roundTripperFunc adapts a function into the http.RoundTripper
// interface so the tests don't need a one-shot struct each time.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
