// docker-hash computes a deterministic SHA-256 hash for a Docker image build,
// based on the Dockerfile, build arguments and all files referenced by
// COPY/ADD instructions within the build context.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/RemkoMolier/docker-hash/pkg/hasher"
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
	)

	flag.StringVar(&dockerfilePath, "file", "Dockerfile", "Path to the Dockerfile")
	flag.StringVar(&dockerfilePath, "f", "Dockerfile", "Path to the Dockerfile (short)")
	flag.StringVar(&contextDir, "context", ".", "Path to the build context directory")
	flag.StringVar(&contextDir, "c", ".", "Path to the build context directory (short)")
	flag.Var(&rawBuildArgs, "build-arg", "Build argument in NAME=VALUE format (may be repeated)")
	flag.BoolVar(&showVersion, "version", false, "Print version information and exit")
	flag.BoolVar(&showVersion, "v", false, "Print version information and exit (short)")
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

	hash, err := hasher.Compute(hasher.Options{
		DockerfilePath: dockerfilePath,
		ContextDir:     contextDir,
		BuildArgs:      buildArgs,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(hash)
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
