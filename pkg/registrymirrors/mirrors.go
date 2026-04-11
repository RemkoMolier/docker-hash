// Package registrymirrors implements containerd-style hosts.toml mirror
// configuration loading and an http.RoundTripper that redirects registry
// requests through the configured mirrors.
//
// The file layout follows the containerd spec:
//
//	<certsDir>/<registry-host>/hosts.toml
//
// For example:
//
//	~/.config/containerd/certs.d/docker.io/hosts.toml
//
// Discovery order (first existing directory wins):
//  1. $DOCKER_HASH_CERTS_D
//  2. $XDG_CONFIG_HOME/containerd/certs.d
//  3. ~/.config/containerd/certs.d
//  4. /etc/containerd/certs.d
package registrymirrors

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/google/go-containerregistry/pkg/name"
)

// hostsConfig is the top-level structure of a hosts.toml file.
// Unknown fields are silently ignored to be forward-compatible with
// future containerd schema additions.
type hostsConfig struct {
	Server string                `toml:"server"`
	Host   map[string]hostEntry `toml:"host"`
}

// hostEntry represents a single [host."<url>"] block in hosts.toml.
type hostEntry struct {
	Capabilities []string `toml:"capabilities"`
	SkipVerify   bool     `toml:"skip_verify"`
}

// mirror holds the parsed information for a single mirror.
type mirror struct {
	// baseURL is the mirror's base URL (scheme + host + optional path prefix).
	baseURL    *url.URL
	skipVerify bool
}

// Mirrors holds all parsed mirror configuration. A zero-value Mirrors
// (or nil pointer) is valid and causes all requests to pass through unmodified.
type Mirrors struct {
	// m maps the normalized upstream registry API hostname (as used by
	// go-containerregistry, e.g. "index.docker.io") to an ordered list
	// of mirrors to try.
	m map[string][]mirror
}

// Discover returns the first mirror configuration directory that exists,
// following the discovery order defined in the package doc.
// Returns ("", nil) when no directory is found (mirrors disabled).
func Discover() (string, error) {
	candidates := configDirCandidates()
	for _, dir := range candidates {
		info, err := os.Stat(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("stat %s: %w", dir, err)
		}
		if info.IsDir() {
			return dir, nil
		}
	}
	return "", nil
}

// configDirCandidates returns the ordered list of candidate directories.
func configDirCandidates() []string {
	var candidates []string

	if v := os.Getenv("DOCKER_HASH_CERTS_D"); v != "" {
		candidates = append(candidates, v)
	}

	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "containerd", "certs.d"))
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "containerd", "certs.d"))
	}

	candidates = append(candidates, "/etc/containerd/certs.d")
	return candidates
}

// Load reads every <registry-host>/hosts.toml file under certsDir and returns
// a Mirrors struct. If certsDir is empty or does not exist, a zero-value
// Mirrors is returned without error.
func Load(certsDir string) (*Mirrors, error) {
	if certsDir == "" {
		return &Mirrors{}, nil
	}

	info, err := os.Stat(certsDir)
	if errors.Is(err, os.ErrNotExist) {
		return &Mirrors{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("stat certsDir %s: %w", certsDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("certsDir %s is not a directory", certsDir)
	}

	m := make(map[string][]mirror)

	entries, err := os.ReadDir(certsDir)
	if err != nil {
		return nil, fmt.Errorf("read certsDir %s: %w", certsDir, err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		registryName := entry.Name()
		hostsFile := filepath.Join(certsDir, registryName, "hosts.toml")

		if _, err := os.Stat(hostsFile); errors.Is(err, os.ErrNotExist) {
			continue
		}

		cfg, err := parseHostsFile(hostsFile)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", hostsFile, err)
		}

		// Normalise the directory name to the go-containerregistry API hostname.
		// E.g. "docker.io" → "index.docker.io".
		apiHost, err := normalizeRegistryHost(registryName)
		if err != nil {
			// If the directory name is not a valid registry (e.g. a stray dir),
			// skip it silently.
			continue
		}

		mirrors, err := mirrorsFromConfig(cfg)
		if err != nil {
			return nil, fmt.Errorf("load mirrors from %s: %w", hostsFile, err)
		}
		if len(mirrors) > 0 {
			m[apiHost] = append(m[apiHost], mirrors...)
		}
	}

	return &Mirrors{m: m}, nil
}

// parseHostsFile reads and decodes a single hosts.toml file.
func parseHostsFile(path string) (*hostsConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var cfg hostsConfig
	if _, err := toml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// mirrorsFromConfig extracts the ordered mirror list from a parsed hostsConfig.
// Only hosts with the "resolve" capability are included.
func mirrorsFromConfig(cfg *hostsConfig) ([]mirror, error) {
	var mirrors []mirror
	for rawURL, entry := range cfg.Host {
		if !hasCapability(entry.Capabilities, "resolve") {
			continue
		}
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("invalid mirror URL %q: %w", rawURL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("mirror URL %q must use http or https scheme", rawURL)
		}
		// Strip any trailing slash from the path for consistent concatenation.
		u.Path = strings.TrimRight(u.Path, "/")
		mirrors = append(mirrors, mirror{baseURL: u, skipVerify: entry.SkipVerify})
	}
	return mirrors, nil
}

// hasCapability reports whether caps contains the given capability name.
func hasCapability(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}

// normalizeRegistryHost returns the API hostname that go-containerregistry
// uses for the given registry name. For example "docker.io" → "index.docker.io".
func normalizeRegistryHost(registryName string) (string, error) {
	reg, err := name.NewRegistry(registryName)
	if err != nil {
		return "", err
	}
	return reg.RegistryStr(), nil
}

// Transport returns an http.RoundTripper that, for requests targeting a
// registry with configured mirrors, tries each mirror in order before falling
// back to the upstream. When m is nil or has no mirrors the returned transport
// is equivalent to base.
func (m *Mirrors) Transport(base http.RoundTripper) http.RoundTripper {
	if m == nil || len(m.m) == 0 {
		return base
	}
	return &mirrorTransport{base: base, mirrors: m.m}
}

// mirrorTransport is the http.RoundTripper implementation that rewrites
// requests to configured mirror hosts.
type mirrorTransport struct {
	base    http.RoundTripper
	mirrors map[string][]mirror
}

func (t *mirrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// The request host might include a port; strip it for lookup.
	host := req.URL.Hostname()

	mirrors, ok := t.mirrors[host]
	if !ok {
		return t.base.RoundTrip(req)
	}

	// Try each mirror in declaration order.
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

// tryMirror sends req to the given mirror, rewriting the URL.
func (t *mirrorTransport) tryMirror(req *http.Request, m mirror) (*http.Response, error) {
	mirrorReq := req.Clone(req.Context())

	// Build the rewritten URL: mirror scheme+host+path prefix + original path.
	rewritten := *m.baseURL
	rewritten.Path = m.baseURL.Path + req.URL.Path
	rewritten.RawQuery = req.URL.RawQuery
	mirrorReq.URL = &rewritten
	mirrorReq.Host = m.baseURL.Host

	transport := t.base
	if m.skipVerify {
		log.Printf("WARN: skip_verify=true for mirror %s; TLS verification disabled", m.baseURL.Host)
		transport = insecureTransport()
	}

	return transport.RoundTrip(mirrorReq)
}

// insecureTransport returns an http.RoundTripper that skips TLS verification.
func insecureTransport() http.RoundTripper {
	return &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // user-explicit opt-in via skip_verify
	}
}
