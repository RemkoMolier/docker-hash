// docker-hash computes a deterministic SHA-256 hash for a Docker image build,
// based on the Dockerfile, build arguments and all files referenced by
// COPY/ADD instructions within the build context.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/RemkoMolier/docker-hash/pkg/baseimage"
	"github.com/RemkoMolier/docker-hash/pkg/hasher"
	"github.com/RemkoMolier/docker-hash/pkg/registrymirrors"
)

// Exit codes. The 0/1 split matches the pre-check behaviour; 2/3 are new and
// reserved for registry checks so CI pipelines can branch on them.
//
//	0 success — hash computed; with --check, image exists
//	1 hash / Dockerfile / usage error
//	2 registry communication error (network, auth, 5xx) — only from --check
//	3 image not found (manifest 404)            — only from --check
const (
	exitOK            = 0
	exitError         = 1
	exitRegistryError = 2
	exitImageNotFound = 3
)

// Build-time variables injected via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type buildArgList []string

func (b *buildArgList) String() string {
	return strings.Join(*b, ", ")
}

func (b *buildArgList) Set(value string) error {
	*b = append(*b, value)
	return nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the testable entry point. It returns an exit code and writes user-
// visible output to stdout/stderr. Returning an int instead of calling
// os.Exit makes it straightforward to assert on the exit-code matrix in unit
// tests without forking a subprocess.
func run(args []string, stdout, stderr io.Writer) int {
	var (
		dockerfilePath string
		contextDir     string
		rawBuildArgs   buildArgList
		showVersion    bool
		noResolveFrom  bool
		noExpandArgs   bool
		platform       string
		authFile       string
		registriesConf string
		hashFlag       bool
		checkTemplate  string
		dotenvPrefix   string
	)

	fs := flag.NewFlagSet("docker-hash", flag.ContinueOnError)
	fs.SetOutput(stderr)

	fs.StringVar(&dockerfilePath, "file", "Dockerfile", "Path to the Dockerfile")
	fs.StringVar(&dockerfilePath, "f", "Dockerfile", "Path to the Dockerfile (short)")
	fs.StringVar(&contextDir, "context", ".", "Path to the build context directory")
	fs.StringVar(&contextDir, "c", ".", "Path to the build context directory (short)")
	fs.Var(&rawBuildArgs, "build-arg", "Build argument in NAME=VALUE format (may be repeated)")
	fs.BoolVar(&showVersion, "version", false, "Print version information and exit")
	fs.BoolVar(&showVersion, "v", false, "Print version information and exit (short)")
	fs.BoolVar(&noResolveFrom, "no-resolve-from", false,
		"Do not resolve FROM image digests against the upstream registry. "+
			"FROM references are still expanded against ARG/ENV state and the "+
			"base-image hash entry is still canonicalized offline (so 'alpine' "+
			"and 'alpine:latest' produce the same base-image entry), but no "+
			"network calls are made. Combine with --no-expand-args to reproduce "+
			"the v0.1.x hash shape exactly.")
	fs.BoolVar(&noExpandArgs, "no-expand-args", false,
		"Disable ARG/ENV expansion in COPY/ADD source paths, --from stage "+
			"names and FROM image/platform references. With this flag set, "+
			"a FROM line containing a $VAR reference causes docker-hash to "+
			"fail rather than silently ignore the variable. Combine with "+
			"--no-resolve-from to reproduce the v0.1.x hash shape exactly.")
	fs.StringVar(&platform, "platform", "",
		"Force a specific platform (e.g. linux/amd64) when resolving FROM "+
			"image digests for multi-arch images. Empty (the default) hashes "+
			"the multi-arch index digest, which keeps the hash stable across "+
			"runner architectures. Per-FROM --platform= flags in the Dockerfile "+
			"always take precedence over this value.")
	fs.StringVar(&authFile, "auth-file", "",
		"Path to a registry auth file in Docker config.json or Podman/Skopeo "+
			"auth.json format. When set, this sets REGISTRY_AUTH_FILE for the "+
			"current run; the default keychain still consults the other auth "+
			"sources ($HOME/.docker/config.json, $DOCKER_CONFIG, "+
			"$XDG_RUNTIME_DIR/containers/auth.json) per their normal lookup "+
			"order. Same semantic as the Skopeo/Podman/Buildah --authfile flag.")
	fs.StringVar(&registriesConf, "registries-conf", "",
		"Path to a Podman-style registries.conf TOML file describing per-"+
			"registry mirrors. When set (and --no-resolve-from is not set), "+
			"FROM digest resolution is routed through the configured mirrors "+
			"with fallback to the upstream registry on connection error or "+
			"HTTP 5xx. The file uses the same [[registry]] / [[registry.mirror]] "+
			"schema Podman, Buildah, Skopeo and CRI-O already consume, so an "+
			"existing /etc/containers/registries.conf can be reused as-is. "+
			"There is no auto-discovery: the path must be provided explicitly.")
	fs.BoolVar(&hashFlag, "hash", false,
		"Print the computed hash to stdout. This is the default behaviour "+
			"when no other output-selecting flag is set; pass --hash "+
			"explicitly alongside --check to also print the bare hash.")
	fs.StringVar(&checkTemplate, "check", "",
		"Check whether an image exists in the registry. The argument is a "+
			"full image reference containing a literal {hash} placeholder, "+
			"e.g. my-reg/app:build-{hash}-amd64. Exit 0 on hit, 3 on miss, "+
			"2 on network/auth/5xx errors. {hash} expands to the bare "+
			"64-hex digest.")
	fs.StringVar(&dotenvPrefix, "dotenv", "",
		"Emit dotenv lines on stdout instead of the bare hash. Always "+
			"writes PREFIX_HASH=<hash>; when combined with --check, also "+
			"writes PREFIX_EXISTS=yes|no. PREFIX must be a valid shell "+
			"identifier. Mutually exclusive with --hash.")

	if err := fs.Parse(args); err != nil {
		// ContinueOnError already wrote the error + usage to stderr. Map
		// usage errors to exit 1 (reserving 2 for registry communication
		// errors). flag.ErrHelp (-h / --help) is not an error.
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitError
	}

	if showVersion {
		printVersion(stdout)
		return exitOK
	}

	if err := validateModeFlags(hashFlag, checkTemplate, dotenvPrefix); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}

	// --auth-file: set REGISTRY_AUTH_FILE so go-containerregistry's default
	// keychain picks the file up. Setting the env var (rather than building
	// a custom keychain ourselves) keeps the lookup logic in one place and
	// matches the Skopeo/Podman/Buildah convention.
	if authFile != "" {
		if err := os.Setenv("REGISTRY_AUTH_FILE", authFile); err != nil {
			fmt.Fprintf(stderr, "error: set REGISTRY_AUTH_FILE: %v\n", err)
			return exitError
		}
	}

	// Resolve the Dockerfile path relative to context if it is not absolute.
	if !filepath.IsAbs(dockerfilePath) {
		candidate := filepath.Join(contextDir, dockerfilePath)
		if _, err := os.Stat(candidate); err == nil {
			dockerfilePath = candidate
		}
	}

	buildArgs := parseBuildArgs(rawBuildArgs)

	// Build the base-image resolver and, when --check is set, a parallel
	// Checker sharing the same transport + auth stack so mirrors and
	// --auth-file apply uniformly to both flows.
	var (
		resolver baseimage.Resolver
		checker  *baseimage.Checker
		mirrorTr http.RoundTripper
	)
	if registriesConf != "" {
		mirrors, err := registrymirrors.Load(registriesConf)
		if err != nil {
			fmt.Fprintf(stderr, "error: load %s: %v\n", registriesConf, err)
			return exitError
		}
		tr, err := mirrors.Transport(http.DefaultTransport)
		if err != nil {
			fmt.Fprintf(stderr, "error: build mirror transport: %v\n", err)
			return exitError
		}
		mirrorTr = tr
	}
	if !noResolveFrom {
		remote := &baseimage.RemoteResolver{
			PlatformOverride: platform,
			Transport:        mirrorTr,
		}
		resolver = baseimage.NewCachingResolver(remote)
	}
	if checkTemplate != "" {
		checker = &baseimage.Checker{Transport: mirrorTr}
	}

	hash, err := hasher.Compute(hasher.Options{
		DockerfilePath:    dockerfilePath,
		ContextDir:        contextDir,
		BuildArgs:         buildArgs,
		BaseImageResolver: resolver,
		NoExpandArgs:      noExpandArgs,
		Context:           context.Background(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitError
	}

	// Perform registry check, if requested, before emitting any stdout.
	// That way a registry error (exit 2) leaves stdout empty per the
	// design proposal.
	var (
		exists    bool
		checkExit int
	)
	if checker != nil {
		ref := strings.ReplaceAll(checkTemplate, "{hash}", hash)
		err := checker.Check(context.Background(), ref)
		switch {
		case err == nil:
			exists = true
			checkExit = exitOK
		case errors.Is(err, baseimage.ErrImageNotFound):
			exists = false
			checkExit = exitImageNotFound
		default:
			fmt.Fprintf(stderr, "error: %v\n", err)
			return exitRegistryError
		}
	}

	writeOutput(stdout, hash, exists, hashFlag, checker != nil, dotenvPrefix)
	return checkExit
}

// validateModeFlags enforces the cross-flag invariants from the design
// proposal: --hash and --dotenv are mutually exclusive (--dotenv already
// carries the hash), --check templates must contain {hash}, and --dotenv
// prefixes must be valid shell identifiers so downstream `source $dotenv`
// works without surprises.
func validateModeFlags(hashFlag bool, checkTemplate, dotenvPrefix string) error {
	if hashFlag && dotenvPrefix != "" {
		return errors.New("--hash and --dotenv are mutually exclusive (--dotenv already includes the hash)")
	}
	if checkTemplate != "" {
		if !strings.Contains(checkTemplate, "{hash}") {
			return fmt.Errorf("--check template %q must contain a literal {hash} placeholder", checkTemplate)
		}
		if err := baseimage.ValidateCheckTemplate(checkTemplate); err != nil {
			return err
		}
	}
	if dotenvPrefix != "" && !isShellIdentifier(dotenvPrefix) {
		return fmt.Errorf("--dotenv prefix %q is not a valid shell identifier", dotenvPrefix)
	}
	return nil
}

// shellIdentifier matches [A-Za-z_][A-Za-z0-9_]*, the POSIX shell identifier
// shape. Dotenv consumers typically `source` the file, so anything outside
// this set would break the resulting `PREFIX_HASH=...` line.
var shellIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func isShellIdentifier(s string) bool {
	return shellIdentifier.MatchString(s)
}

// writeOutput renders stdout according to the flag combination:
//
//	default / --hash               → bare hash
//	--check T                      → nothing (the exit code carries the signal)
//	--hash --check T               → bare hash
//	--dotenv P                     → PREFIX_HASH=<hash>
//	--dotenv P --check T           → PREFIX_HASH=<hash> + PREFIX_EXISTS=yes|no
func writeOutput(w io.Writer, hash string, exists, hashFlag, checking bool, dotenvPrefix string) {
	if dotenvPrefix != "" {
		fmt.Fprintf(w, "%s_HASH=%s\n", dotenvPrefix, hash)
		if checking {
			yn := "no"
			if exists {
				yn = "yes"
			}
			fmt.Fprintf(w, "%s_EXISTS=%s\n", dotenvPrefix, yn)
		}
		return
	}
	// No dotenv. Print the bare hash unless the user only asked for a
	// registry check (--check with neither --hash nor --dotenv).
	if !checking || hashFlag {
		fmt.Fprintln(w, hash)
	}
}

// printVersion writes the version banner to w. Extracted so it can be unit-tested
// without shelling out to a subprocess.
func printVersion(w io.Writer) {
	_, _ = fmt.Fprintf(w, "docker-hash %s (%s, %s)\n", version, commit, date)
}

// parseBuildArgs converts a slice of "NAME=VALUE" or "NAME" strings into a map.
func parseBuildArgs(args []string) map[string]string {
	m := make(map[string]string, len(args))
	for _, arg := range args {
		if idx := strings.Index(arg, "="); idx >= 0 {
			m[arg[:idx]] = arg[idx+1:]
		} else {
			m[arg] = ""
		}
	}
	return m
}
