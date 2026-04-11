// docker-hash computes a deterministic SHA-256 hash for a Docker image build,
// based on the Dockerfile, build arguments and all files referenced by
// COPY/ADD instructions within the build context.
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/RemkoMolier/docker-hash/pkg/hasher"
	"github.com/RemkoMolier/docker-hash/pkg/registrymirrors"
	"github.com/google/go-containerregistry/pkg/v1/remote"
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
	var (
		dockerfilePath string
		contextDir     string
		rawBuildArgs   buildArgList
		showVersion    bool
		noResolveFrom  bool
		certsD         string
	)

	flag.StringVar(&dockerfilePath, "file", "Dockerfile", "Path to the Dockerfile")
	flag.StringVar(&dockerfilePath, "f", "Dockerfile", "Path to the Dockerfile (short)")
	flag.StringVar(&contextDir, "context", ".", "Path to the build context directory")
	flag.StringVar(&contextDir, "c", ".", "Path to the build context directory (short)")
	flag.Var(&rawBuildArgs, "build-arg", "Build argument in NAME=VALUE format (may be repeated)")
	flag.BoolVar(&showVersion, "version", false, "Print version information and exit")
	flag.BoolVar(&showVersion, "v", false, "Print version information and exit (short)")
	flag.BoolVar(&noResolveFrom, "no-resolve-from", false, "Disable FROM image digest resolution (skip registry calls)")
	flag.StringVar(&certsD, "certs-d", "", "Path to containerd-style certs.d directory for registry mirrors (overrides auto-discovery)")
	flag.Parse()

	if showVersion {
		printVersion(os.Stdout)
		return
	}

	// Resolve the Dockerfile path relative to context if it is not absolute.
	if !filepath.IsAbs(dockerfilePath) {
		// Check if the Dockerfile sits in the context dir.
		candidate := filepath.Join(contextDir, dockerfilePath)
		if _, err := os.Stat(candidate); err == nil {
			dockerfilePath = candidate
		}
	}

	buildArgs := parseBuildArgs(rawBuildArgs)

	// Set up the registry transport with mirror support.
	transport, err := buildTransport(certsD)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: load registry mirrors: %v\n", err)
		os.Exit(1)
	}

	hash, err := hasher.Compute(hasher.Options{
		DockerfilePath:     dockerfilePath,
		ContextDir:         contextDir,
		BuildArgs:          buildArgs,
		ResolveFromDigests: !noResolveFrom,
		Transport:          transport,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(hash)
}

// buildTransport loads the registry mirror configuration and returns an
// http.RoundTripper that routes requests through any configured mirrors.
// When certsD is empty, auto-discovery is used. When no certs.d directory
// exists, remote.DefaultTransport is returned unchanged.
func buildTransport(certsD string) (http.RoundTripper, error) {
	dir := certsD
	if dir == "" {
		discovered, err := registrymirrors.Discover()
		if err != nil {
			return nil, fmt.Errorf("discover certs.d: %w", err)
		}
		dir = discovered
	}

	mirrors, err := registrymirrors.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("load mirrors from %s: %w", dir, err)
	}

	return mirrors.Transport(remote.DefaultTransport), nil
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
			// No value provided – use empty string.
			m[arg] = ""
		}
	}
	return m
}
