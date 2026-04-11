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
//	prefix   = "docker.io/library"      # registry hostname (optionally with a repo prefix)
//	location = "registry-1.docker.io"   # canonical upstream; used as the
//	                                    # routing key when prefix is empty
//
//	[[registry.mirror]]
//	location = "artifactory.corp/dockerhub"
//	insecure = false                    # accept HTTP and skip TLS verification on this mirror
//
// Routing rules:
//
//   - The match is keyed by the request's authority (host + port). For
//     entries that include a repo prefix the match is also gated on
//     the request URL path: `prefix = "docker.io/library"` matches
//     /v2/library/<repo>/... but does not capture /v2/myorg/<repo>/...,
//     and the longest matching prefix wins (Podman semantics).
//   - Mirrors are tried in declaration order. On connection error or
//     HTTP 5xx the next mirror is tried; if every mirror fails the
//     request falls back to the original upstream URL.
//   - Per-mirror insecure=true is honoured by *cloning the base
//     http.Transport* and flipping InsecureSkipVerify on the clone, so
//     proxy/dial/HTTP-2/keep-alive settings from the base transport are
//     preserved. Mirrors.Transport returns an error if any mirror in
//     the configuration requests insecure=true but the supplied base
//     transport is not an *http.Transport (and therefore cannot be
//     cloned safely).
//
// Unsupported (silently ignored, not an error) fields from the broader
// Podman schema:
//
//   - unqualified-search-registries — docker-hash always sees fully
//     qualified image references coming out of the Dockerfile parser, so
//     short-name expansion is not its job.
//   - registry-level insecure — docker-hash does not rewrite or
//     downgrade upstream requests; per-mirror insecure is honoured
//     instead.
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

// registryEntry mirrors a single [[registry]] block. Only the fields the
// router actually uses are listed; registry-level `insecure`, `blocked`,
// and `mirror-by-digest-only` are dropped on the floor by the lenient
// TOML decoder.
type registryEntry struct {
	Prefix   string        `toml:"prefix"`
	Location string        `toml:"location"`
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

	// insecure means "use plain HTTP and/or skip TLS verification". The
	// router applies this by cloning the base http.Transport and
	// flipping InsecureSkipVerify on the clone, so proxy / dial /
	// keep-alive / HTTP-2 settings carry through.
	insecure bool
}

// registryRule is a single routing rule keyed by host. The rule matches
// when the request's URL.Host equals the rule's host AND the URL path
// belongs to a repository under repoPathPrefix (an empty repoPathPrefix
// matches every v2 request to that host).
type registryRule struct {
	repoPathPrefix string
	mirrors        []mirror
}

// Mirrors holds all parsed mirror configuration. A nil or zero-value
// Mirrors is valid: Transport returns its input unchanged in that case.
type Mirrors struct {
	// m maps the request authority (host[:port], canonicalised through
	// go-containerregistry's name.NewRegistry) to the ordered list of
	// rules that may apply. Each rule may further constrain on a repo
	// path prefix.
	m map[string][]registryRule
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

	m := make(map[string][]registryRule)
	for _, entry := range cfg.Registries {
		// Determine which name this entry matches against. Podman uses
		// `prefix` for the user-facing name and `location` for the
		// canonical upstream; if `prefix` is empty fall back to
		// `location`, matching Podman's own behaviour. Note that
		// docker-hash only consumes `location` for this fallback — it
		// does NOT rewrite the upstream request to point at it, since
		// the resolver already sends to the correct upstream by
		// construction.
		key := entry.Prefix
		if key == "" {
			key = entry.Location
		}
		if key == "" {
			continue // nothing to match against; silently skip.
		}
		host, repoPathPrefix, err := parsePrefix(key)
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
			m[host] = append(m[host], registryRule{
				repoPathPrefix: repoPathPrefix,
				mirrors:        mirrors,
			})
		}
	}
	return &Mirrors{m: m}, nil
}

// parsePrefix splits a Podman prefix (or location) string into a host
// (canonicalised through go-containerregistry, so "docker.io" becomes
// "index.docker.io" and explicit ports are preserved) and an optional
// repository path prefix.
//
// Examples:
//
//	"docker.io"                → ("index.docker.io",          "")
//	"docker.io/library"        → ("index.docker.io",          "library")
//	"docker.io/library/alpine" → ("index.docker.io",          "library/alpine")
//	"registry.example.com:5000" → ("registry.example.com:5000", "")
//	"registry.example.com:5000/my-team" → ("registry.example.com:5000", "my-team")
func parsePrefix(s string) (host, repoPathPrefix string, err error) {
	hostPart := s
	if idx := strings.IndexByte(s, '/'); idx != -1 {
		hostPart = s[:idx]
		repoPathPrefix = strings.TrimRight(s[idx+1:], "/")
	}
	reg, err := name.NewRegistry(hostPart)
	if err != nil {
		return "", "", err
	}
	return reg.RegistryStr(), repoPathPrefix, nil
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
//
// If the configuration includes any mirror with insecure=true, base
// must be an *http.Transport so that the package can clone it for
// insecure use. Passing a wrapping/non-Transport RoundTripper in that
// case is a configuration error and Transport will return a non-nil
// error rather than silently dropping the base transport's behaviour
// (proxy, timeouts, keep-alives, HTTP/2).
func (m *Mirrors) Transport(base http.RoundTripper) (http.RoundTripper, error) {
	if m == nil || len(m.m) == 0 {
		return base, nil
	}
	mt := &mirrorTransport{base: base, mirrors: m.m}
	if m.hasInsecureMirror() {
		t, ok := base.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("registrymirrors: registries.conf requests insecure=true on at least one mirror, but the base transport %T is not *http.Transport and cannot be cloned for insecure use", base)
		}
		mt.baseInsecure = cloneInsecure(t)
	}
	return mt, nil
}

// hasInsecureMirror reports whether any mirror in the configuration is
// flagged insecure=true. Used by Transport to decide whether the
// base-clone-for-insecure path is needed.
func (m *Mirrors) hasInsecureMirror() bool {
	for _, rules := range m.m {
		for _, r := range rules {
			for _, mr := range r.mirrors {
				if mr.insecure {
					return true
				}
			}
		}
	}
	return false
}

// cloneInsecure returns a copy of base that has TLS certificate
// verification disabled. The clone preserves every other field of the
// underlying http.Transport (Proxy, Dialer, MaxIdleConns,
// IdleConnTimeout, TLSHandshakeTimeout, ExpectContinueTimeout,
// ForceAttemptHTTP2, …) so corporate proxy / timeout / keep-alive
// settings configured by the caller still apply to insecure mirror
// requests.
func cloneInsecure(base *http.Transport) *http.Transport {
	cloned := base.Clone()
	if cloned.TLSClientConfig == nil {
		cloned.TLSClientConfig = &tls.Config{} //nolint:gosec // InsecureSkipVerify is set explicitly two lines down
	} else {
		cloned.TLSClientConfig = cloned.TLSClientConfig.Clone()
	}
	cloned.TLSClientConfig.InsecureSkipVerify = true //nolint:gosec // user-explicit opt-in via per-mirror insecure=true
	return cloned
}

// mirrorTransport is the http.RoundTripper implementation that rewrites
// matching requests through configured mirror hosts.
type mirrorTransport struct {
	base    http.RoundTripper
	mirrors map[string][]registryRule

	// baseInsecure is a clone of base with TLS verification disabled,
	// used for mirrors flagged insecure=true. nil when the
	// configuration has no insecure mirrors (the common case), in
	// which case Transport never had to ask whether base was an
	// *http.Transport.
	baseInsecure http.RoundTripper
}

func (t *mirrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Match by full authority (host + port if any). Using URL.Host
	// instead of URL.Hostname() preserves the port so a registry like
	// "registry.example.com:5000" routes to its configured mirrors.
	rules, ok := t.mirrors[req.URL.Host]
	if !ok {
		return t.base.RoundTrip(req)
	}

	mirrors := bestMatchMirrors(rules, req.URL.Path)
	if mirrors == nil {
		return t.base.RoundTrip(req)
	}

	for _, m := range mirrors {
		resp, err := t.tryMirror(req, m)
		if err != nil {
			log.Printf("WARN: mirror %s failed for %s: %v", m.baseURL, req.URL.Host, err)
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

// bestMatchMirrors implements Podman's longest-prefix-wins semantics:
// among the rules registered for the request's host, return the mirror
// list of the rule whose repoPathPrefix is the longest one that still
// covers urlPath. Rules with an empty repoPathPrefix match any v2
// request to the host. Returns nil when no rule matches at all.
func bestMatchMirrors(rules []registryRule, urlPath string) []mirror {
	bestIdx := -1
	bestLen := -1
	for i, rule := range rules {
		if !pathBelongsTo(urlPath, rule.repoPathPrefix) {
			continue
		}
		if len(rule.repoPathPrefix) > bestLen {
			bestIdx = i
			bestLen = len(rule.repoPathPrefix)
		}
	}
	if bestIdx == -1 {
		return nil
	}
	return rules[bestIdx].mirrors
}

// pathBelongsTo reports whether urlPath is a Docker Registry v2 request
// for a repository under repoPathPrefix. An empty repoPathPrefix is the
// host-wide rule and matches any v2 request. Otherwise the URL path
// must equal "/v2/<repoPathPrefix>" exactly or start with
// "/v2/<repoPathPrefix>/" so a configured prefix never accidentally
// matches a sibling repo whose name happens to share a common substring
// (e.g. prefix "lib" must not match "/v2/library/...").
func pathBelongsTo(urlPath, repoPathPrefix string) bool {
	if repoPathPrefix == "" {
		return true
	}
	needle := "/v2/" + repoPathPrefix
	return urlPath == needle || strings.HasPrefix(urlPath, needle+"/")
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
		// baseInsecure is set in Transport() exactly when at least one
		// mirror in the configuration is flagged insecure, so reaching
		// this branch with a nil baseInsecure would be a bug — Transport
		// should have rejected the configuration up front. Guard
		// defensively rather than nil-deref'ing.
		if t.baseInsecure == nil {
			return nil, errors.New("registrymirrors: insecure mirror requested but no insecure transport was prepared (this is a bug; Transport should have rejected the configuration)")
		}
		log.Printf("WARN: insecure=true for mirror %s; TLS verification disabled", m.baseURL.Host)
		transport = t.baseInsecure
	}
	return transport.RoundTrip(mirrorReq)
}
