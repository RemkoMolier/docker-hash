// Package registrymirrors loads a Podman-style registries.conf TOML file
// and exposes an http.RoundTripper that transparently routes registry
// requests through the configured mirrors.
//
// The file format is the same one Podman, Buildah, Skopeo and CRI-O already
// consume, so a corporate environment that has already configured
// /etc/containers/registries.conf for those tools can point docker-hash at
// the same file via --registries-conf=<path> instead of maintaining a
// second source of truth.
//
// There is intentionally no auto-discovery: the path must be provided
// explicitly, otherwise no mirror routing happens. This avoids "why is my
// build pulling from a mirror I forgot existed" surprises.
//
// Format (the subset docker-hash actually understands):
//
//	[[registry]]
//	prefix   = "docker.io"            # registry hostname this entry applies to
//	location = "registry-1.docker.io" # canonical upstream (used as fallback target)
//	insecure = false                  # accept HTTP and skip TLS verification on the upstream
//
//	[[registry.mirror]]
//	location = "artifactory.corp/dockerhub"
//	insecure = false                  # accept HTTP and skip TLS verification on this mirror
//
// Routing rule: a registry request is matched against the entries by the
// request URL hostname. If a match is found, mirrors are tried in
// declaration order; on connection error or HTTP 5xx the next mirror is
// tried; if all mirrors fail the request falls back to the original
// upstream URL.
//
// The "prefix" is normalised through go-containerregistry's
// name.NewRegistry, so "docker.io" and "index.docker.io" both route Docker
// Hub requests, matching what go-containerregistry uses internally as the
// API hostname.
//
// Unsupported (ignored, not an error) fields from the Podman schema:
//
//   - unqualified-search-registries — docker-hash always sees fully
//     qualified image references coming out of the Dockerfile parser, so
//     short-name expansion is not its job.
//   - blocked — docker-hash will not refuse to resolve a blocked
//     registry; the underlying network call will fail naturally if the
//     registry is unreachable.
//   - mirror-by-digest-only — docker-hash always resolves to a digest,
//     so this is effectively always-on.
//
// These can be added later if a real use case appears; ignoring them now
// keeps the parser surface small.
package registrymirrors

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/google/go-containerregistry/pkg/name"
)

// registriesConfig is the top-level Podman registries.conf schema. Only
// the fields docker-hash actually consumes are declared; everything else
// is silently ignored to keep us forward-compatible with future Podman
// schema additions.
type registriesConfig struct {
	Registries []registryEntry `toml:"registry"`
}

// registryEntry mirrors a single [[registry]] block.
type registryEntry struct {
	Prefix   string        `toml:"prefix"`
	Location string        `toml:"location"`
	Insecure bool          `toml:"insecure"`
	Mirrors  []mirrorEntry `toml:"mirror"`
}

// mirrorEntry mirrors a single [[registry.mirror]] sub-table.
type mirrorEntry struct {
	Location string `toml:"location"`
	Insecure bool   `toml:"insecure"`
}

// mirror is the parsed, runtime-ready form of a single mirror.
type mirror struct {
	// baseURL is the mirror's resolved base URL: scheme + host + any
	// path prefix from the configured location.
	baseURL *url.URL

	// insecure means "use plain HTTP and skip TLS verification". This
	// is the union of Podman's two cases (the location had no scheme +
	// insecure=true picks http://, or the location was https:// and
	// insecure=true asks the client to skip cert verification on the
	// outer dial).
	insecure bool
}

// Mirrors holds all parsed mirror configuration. A nil or zero-value
// Mirrors is valid: Transport returns its input unchanged in that case.
type Mirrors struct {
	// m maps the normalised upstream registry API hostname (as used by
	// go-containerregistry, e.g. "index.docker.io") to an ordered list
	// of mirrors to try.
	m map[string][]mirror
}

// Load reads a Podman-style registries.conf file from path and returns
// the parsed Mirrors. An empty path is rejected: the caller is expected
// to gate the call so that --registries-conf was provided.
//
// Returns an error if the file cannot be opened or fails to parse.
// Unknown TOML fields are ignored.
func Load(path string) (*Mirrors, error) {
	if path == "" {
		return nil, errors.New("registrymirrors: Load called with empty path")
	}

	f, err := os.Open(path) //nolint:gosec // path is an explicit user-supplied flag
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var cfg registriesConfig
	if _, err := toml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	m := make(map[string][]mirror)
	for _, entry := range cfg.Registries {
		// Determine which name this entry matches against. Podman uses
		// `prefix` for the user-facing name and `location` for the
		// canonical upstream; if `prefix` is empty fall back to
		// `location`, matching Podman's own behaviour.
		key := entry.Prefix
		if key == "" {
			key = entry.Location
		}
		if key == "" {
			continue // nothing to match against; silently skip.
		}
		host, err := normaliseHost(key)
		if err != nil {
			return nil, fmt.Errorf("registries.conf: invalid registry key %q: %w", key, err)
		}

		mirrors := make([]mirror, 0, len(entry.Mirrors))
		for _, me := range entry.Mirrors {
			parsed, err := parseMirrorLocation(me.Location, me.Insecure)
			if err != nil {
				return nil, fmt.Errorf("registries.conf: registry %q: %w", key, err)
			}
			mirrors = append(mirrors, parsed)
		}
		if len(mirrors) > 0 {
			m[host] = append(m[host], mirrors...)
		}
	}
	return &Mirrors{m: m}, nil
}

// normaliseHost runs a registry name through go-containerregistry's
// name.NewRegistry, which canonicalises shorthand forms (e.g. "docker.io"
// → "index.docker.io") so that the lookup keys match the hostnames
// go-containerregistry actually uses on the wire.
//
// The input may be a bare hostname ("docker.io"), an authority
// ("registry.example.com:5000") or a hostname-prefixed path
// ("docker.io/library"); only the host part is significant.
func normaliseHost(s string) (string, error) {
	// Strip any path component (Podman allows "docker.io/library" as
	// prefix; we only route at hostname granularity).
	if idx := strings.IndexByte(s, '/'); idx != -1 {
		s = s[:idx]
	}
	reg, err := name.NewRegistry(s)
	if err != nil {
		return "", err
	}
	return reg.RegistryStr(), nil
}

// parseMirrorLocation turns a Podman mirror.location string into a
// runtime mirror struct. The location may carry an explicit scheme
// (https://...) or be a bare authority — in the bare-authority case the
// scheme is derived from the insecure bit.
func parseMirrorLocation(loc string, insecure bool) (mirror, error) {
	loc = strings.TrimSpace(loc)
	if loc == "" {
		return mirror{}, errors.New("mirror.location is empty")
	}

	// If the location does not start with a scheme, prepend one based
	// on insecure. Podman's own docs describe locations as bare
	// authorities by default.
	if !strings.Contains(loc, "://") {
		scheme := "https"
		if insecure {
			scheme = "http"
		}
		loc = scheme + "://" + loc
	}

	u, err := url.Parse(loc)
	if err != nil {
		return mirror{}, fmt.Errorf("invalid mirror location %q: %w", loc, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return mirror{}, fmt.Errorf("mirror location %q must use http or https scheme", loc)
	}
	// Strip any trailing slash so concatenation with the upstream
	// request path is consistent.
	u.Path = strings.TrimRight(u.Path, "/")

	return mirror{baseURL: u, insecure: insecure}, nil
}

// Transport returns an http.RoundTripper that, for requests targeting a
// configured registry, tries each mirror in declaration order before
// falling back to the upstream. When m is nil or has no mirrors the
// returned transport is base unchanged — there is no overhead.
func (m *Mirrors) Transport(base http.RoundTripper) http.RoundTripper {
	if m == nil || len(m.m) == 0 {
		return base
	}
	return &mirrorTransport{base: base, mirrors: m.m}
}

// mirrorTransport is the http.RoundTripper implementation that rewrites
// matching requests through configured mirror hosts.
type mirrorTransport struct {
	base    http.RoundTripper
	mirrors map[string][]mirror
}

func (t *mirrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	mirrors, ok := t.mirrors[host]
	if !ok {
		return t.base.RoundTrip(req)
	}

	for _, m := range mirrors {
		resp, err := t.tryMirror(req, m)
		if err != nil {
			log.Printf("WARN: mirror %s failed for %s: %v", m.baseURL, host, err)
			continue
		}
		if resp.StatusCode >= 500 {
			_ = resp.Body.Close()
			log.Printf("WARN: mirror %s returned %d for %s", m.baseURL, resp.StatusCode, req.URL.Path)
			continue
		}
		return resp, nil
	}

	// All mirrors failed; fall back to the original upstream request.
	return t.base.RoundTrip(req)
}

// tryMirror sends req to the given mirror, rewriting the URL to the
// mirror's scheme + host + path prefix and preserving the original
// request path and query string.
func (t *mirrorTransport) tryMirror(req *http.Request, m mirror) (*http.Response, error) {
	mirrorReq := req.Clone(req.Context())

	rewritten := *m.baseURL
	rewritten.Path = m.baseURL.Path + req.URL.Path
	rewritten.RawQuery = req.URL.RawQuery
	mirrorReq.URL = &rewritten
	mirrorReq.Host = m.baseURL.Host

	transport := t.base
	if m.insecure {
		log.Printf("WARN: insecure=true for mirror %s; TLS verification disabled", m.baseURL.Host)
		transport = insecureTransport()
	}
	return transport.RoundTrip(mirrorReq)
}

// insecureTransport returns an http.RoundTripper that skips TLS
// verification. Used only for mirrors that explicitly opt in via
// insecure=true in registries.conf.
func insecureTransport() http.RoundTripper {
	return &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // user-explicit opt-in via insecure
	}
}
