package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPrintVersion(t *testing.T) {
	origVersion, origCommit, origDate := version, commit, date
	version, commit, date = "v-test", "commit-test", "date-test"
	defer func() {
		version, commit, date = origVersion, origCommit, origDate
	}()

	var buf bytes.Buffer
	printVersion(&buf)

	got := buf.String()
	want := "docker-hash v-test (commit-test, date-test)\n"
	if got != want {
		t.Errorf("printVersion() = %q, want %q", got, want)
	}
}

func TestValidateModeFlags(t *testing.T) {
	cases := []struct {
		name          string
		hashFlag      bool
		checkTemplate string
		dotenvPrefix  string
		wantErr       string
	}{
		{name: "no flags"},
		{name: "hash only", hashFlag: true},
		{name: "check with placeholder", checkTemplate: "r/a:{hash}"},
		{name: "hash + check", hashFlag: true, checkTemplate: "r/a:{hash}"},
		{name: "dotenv only", dotenvPrefix: "CI"},
		{name: "dotenv + check", checkTemplate: "r/a:{hash}", dotenvPrefix: "CI"},

		{
			name: "hash + dotenv rejected", hashFlag: true, dotenvPrefix: "CI",
			wantErr: "mutually exclusive",
		},
		{
			name: "check without placeholder", checkTemplate: "r/a:latest",
			wantErr: "{hash} placeholder",
		},
		{
			name: "dotenv with invalid identifier", dotenvPrefix: "1BAD",
			wantErr: "not a valid shell identifier",
		},
		{
			name: "dotenv with dash", dotenvPrefix: "CI-BUILD",
			wantErr: "not a valid shell identifier",
		},
		{
			// Early structural check: an unparseable rendered reference
			// (uppercase in the repo path is not allowed by name.ParseReference)
			// must fail at flag-validation time, not after hashing.
			name:          "check template renders to invalid reference",
			checkTemplate: "BadRepo/APP:build-{hash}",
			wantErr:       "invalid --check template",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateModeFlags(tc.hashFlag, tc.checkTemplate, tc.dotenvPrefix)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestWriteOutput(t *testing.T) {
	const hash = "abc123"
	cases := []struct {
		name         string
		exists       bool
		hashFlag     bool
		checking     bool
		dotenvPrefix string
		want         string
	}{
		{name: "default: bare hash", want: "abc123\n"},
		{name: "--hash: bare hash", hashFlag: true, want: "abc123\n"},
		{name: "--check only: silent", checking: true, want: ""},
		{name: "--hash --check: bare hash", hashFlag: true, checking: true, want: "abc123\n"},
		{name: "--dotenv alone", dotenvPrefix: "CI", want: "CI_HASH=abc123\n"},
		{
			name: "--dotenv --check hit", dotenvPrefix: "CI", checking: true, exists: true,
			want: "CI_HASH=abc123\nCI_EXISTS=yes\n",
		},
		{
			name: "--dotenv --check miss", dotenvPrefix: "CI", checking: true, exists: false,
			want: "CI_HASH=abc123\nCI_EXISTS=no\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			writeOutput(&buf, hash, tc.exists, tc.hashFlag, tc.checking, tc.dotenvPrefix)
			if got := buf.String(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// fixtureDockerfile writes a minimal Dockerfile that hashes offline (no FROM
// resolution needed) into a temp dir and returns the dir and the expected
// 64-hex hash prefix shape.
func fixtureDockerfile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Use FROM scratch so no network resolve is attempted; pair with
	// --no-resolve-from in the CLI args for full determinism.
	content := "FROM scratch\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(content), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	return dir
}

var hexHashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestRun_DefaultPrintsBareHash(t *testing.T) {
	dir := fixtureDockerfile(t)
	var stdout, stderr bytes.Buffer

	code := run([]string{"--no-resolve-from", "--context", dir}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d. stderr: %s", code, exitOK, stderr.String())
	}
	out := strings.TrimRight(stdout.String(), "\n")
	if !hexHashRE.MatchString(out) {
		t.Errorf("stdout is not a 64-hex hash: %q", out)
	}
}

func TestRun_DotenvPrintsHashLine(t *testing.T) {
	dir := fixtureDockerfile(t)
	var stdout, stderr bytes.Buffer

	code := run([]string{"--no-resolve-from", "--context", dir, "--dotenv", "CI"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d. stderr: %s", code, exitOK, stderr.String())
	}
	out := stdout.String()
	if !strings.HasPrefix(out, "CI_HASH=") {
		t.Errorf("expected stdout to start with CI_HASH=, got %q", out)
	}
	if strings.Contains(out, "CI_EXISTS=") {
		t.Errorf("expected no CI_EXISTS without --check, got %q", out)
	}
}

func TestRun_HashAndDotenvMutuallyExclusive(t *testing.T) {
	dir := fixtureDockerfile(t)
	var stdout, stderr bytes.Buffer

	code := run([]string{"--no-resolve-from", "--context", dir, "--hash", "--dotenv", "CI"}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr.String(), "mutually exclusive") {
		t.Errorf("stderr missing mutually-exclusive hint: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("expected empty stdout on usage error, got %q", stdout.String())
	}
}

func TestRun_CheckTemplateMissingPlaceholder(t *testing.T) {
	dir := fixtureDockerfile(t)
	var stdout, stderr bytes.Buffer

	code := run([]string{"--no-resolve-from", "--context", dir, "--check", "r/a:latest"}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	if !strings.Contains(stderr.String(), "{hash}") {
		t.Errorf("stderr should mention {hash}, got %q", stderr.String())
	}
}

func TestRun_UnknownFlagExitsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// Override Go flag's default 2 → make sure an unknown flag yields 1,
	// so exit 2 remains reserved for registry communication errors.
	code := run([]string{"--no-such-flag"}, &stdout, &stderr)
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
}

func TestRun_CheckMissExits3WithDotenv(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	dir := fixtureDockerfile(t)
	var stdout, stderr bytes.Buffer
	// The fake registry is empty, so any lookup hits 404 → exit 3.
	code := run([]string{
		"--no-resolve-from",
		"--context", dir,
		"--check", u.Host + "/test/app:build-{hash}",
		"--dotenv", "CI",
	}, &stdout, &stderr)

	if code != exitImageNotFound {
		t.Fatalf("exit code = %d, want %d. stderr: %s", code, exitImageNotFound, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "CI_HASH=") {
		t.Errorf("expected dotenv to be emitted on exit 3, got %q", out)
	}
	if !strings.Contains(out, "CI_EXISTS=no") {
		t.Errorf("expected CI_EXISTS=no on a 404, got %q", out)
	}
}

// TestRun_HashAndCheckOnMiss pins the combination the GitHub Action uses
// internally: --hash --check together must still print the bare hash on
// stdout even when the registry returns 404, so the Action can populate
// the `hash` output on both hit and miss.
func TestRun_HashAndCheckOnMiss(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	dir := fixtureDockerfile(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--no-resolve-from",
		"--context", dir,
		"--hash",
		"--check", u.Host + "/test/app:build-{hash}",
	}, &stdout, &stderr)

	if code != exitImageNotFound {
		t.Fatalf("exit code = %d, want %d. stderr: %s", code, exitImageNotFound, stderr.String())
	}
	out := strings.TrimRight(stdout.String(), "\n")
	if !hexHashRE.MatchString(out) {
		t.Errorf("stdout is not a 64-hex hash on miss: %q", out)
	}
}

// TestRun_CheckSubstitutesHashIntoRequestPath pins that the {hash}
// placeholder is actually substituted into the HEAD request the checker
// issues — a typo in the placeholder token would otherwise slip past the
// 404-based tests, since an empty registry returns 404 for every path.
func TestRun_CheckSubstitutesHashIntoRequestPath(t *testing.T) {
	var (
		mu    sync.Mutex
		paths []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		// /v2/ is the registry API ping; everything else (manifest HEAD)
		// gets 404 so the CLI exits 3 and we can assert the recorded
		// paths.
		if r.URL.Path == "/v2/" {
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	dir := fixtureDockerfile(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--no-resolve-from",
		"--context", dir,
		"--hash",
		"--check", u.Host + "/test/app:build-{hash}",
	}, &stdout, &stderr)
	if code != exitImageNotFound {
		t.Fatalf("exit code = %d, want %d. stderr: %s", code, exitImageNotFound, stderr.String())
	}
	gotHash := strings.TrimRight(stdout.String(), "\n")
	if !hexHashRE.MatchString(gotHash) {
		t.Fatalf("expected bare hex hash on stdout, got %q", gotHash)
	}

	mu.Lock()
	defer mu.Unlock()
	wantFragment := "build-" + gotHash
	var sawSubstitution bool
	for _, p := range paths {
		if strings.Contains(p, wantFragment) {
			sawSubstitution = true
			break
		}
	}
	if !sawSubstitution {
		t.Fatalf("no recorded request path contained %q; paths seen: %v", wantFragment, paths)
	}
}

func TestRun_CheckNetworkErrorExits2NoStdout(t *testing.T) {
	// Start + close to guarantee a connection failure, which must map
	// to exit 2 (retryable) rather than exit 3 (not found).
	srv := httptest.NewServer(registry.New())
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	srv.Close()

	dir := fixtureDockerfile(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--no-resolve-from",
		"--context", dir,
		"--check", u.Host + "/test/app:build-{hash}",
		"--dotenv", "CI",
	}, &stdout, &stderr)

	if code != exitRegistryError {
		t.Fatalf("exit code = %d, want %d. stderr: %s", code, exitRegistryError, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("expected empty stdout on registry error, got %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "error:") {
		t.Errorf("expected error: prefix on stderr, got %q", stderr.String())
	}
}
