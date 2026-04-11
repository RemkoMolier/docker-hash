// docker-hash computes a deterministic SHA-256 hash for a Docker image build,
// based on the Dockerfile, build arguments and all files referenced by
// COPY/ADD instructions within the build context.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/RemkoMolier/docker-hash/pkg/baseimage"
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
		noResolveFrom  bool
		noExpandArgs   bool
		platform       string
		authFile       string
	)

	flag.StringVar(&dockerfilePath, "file", "Dockerfile", "Path to the Dockerfile")
	flag.StringVar(&dockerfilePath, "f", "Dockerfile", "Path to the Dockerfile (short)")
	flag.StringVar(&contextDir, "context", ".", "Path to the build context directory")
	flag.StringVar(&contextDir, "c", ".", "Path to the build context directory (short)")
	flag.Var(&rawBuildArgs, "build-arg", "Build argument in NAME=VALUE format (may be repeated)")
	flag.BoolVar(&showVersion, "version", false, "Print version information and exit")
	flag.BoolVar(&showVersion, "v", false, "Print version information and exit (short)")
	flag.BoolVar(&noResolveFrom, "no-resolve-from", false,
		"Do not resolve FROM image digests against the upstream registry. "+
			"FROM references are still expanded against ARG/ENV state and "+
			"canonicalized offline (so 'alpine' and 'alpine:latest' hash "+
			"identically), but no network calls are made. Combine with "+
			"--no-expand-args to reproduce the v0.1.x hash shape exactly.")
	flag.BoolVar(&noExpandArgs, "no-expand-args", false,
		"Disable ARG/ENV expansion in COPY/ADD source paths, --from stage "+
			"names and FROM image/platform references. With this flag set, "+
			"a FROM line containing a $VAR reference causes docker-hash to "+
			"fail rather than silently ignore the variable. Combine with "+
			"--no-resolve-from to reproduce the v0.1.x hash shape exactly.")
	flag.StringVar(&platform, "platform", "",
		"Force a specific platform (e.g. linux/amd64) when resolving FROM "+
			"image digests for multi-arch images. Empty (the default) hashes "+
			"the multi-arch index digest, which keeps the hash stable across "+
			"runner architectures. Per-FROM --platform= flags in the Dockerfile "+
			"always take precedence over this value.")
	flag.StringVar(&authFile, "auth-file", "",
		"Path to a registry auth file in Docker config.json or Podman/Skopeo "+
			"auth.json format. When set, this sets REGISTRY_AUTH_FILE for the "+
			"current run; the default keychain still consults the other auth "+
			"sources ($HOME/.docker/config.json, $DOCKER_CONFIG, "+
			"$XDG_RUNTIME_DIR/containers/auth.json) per their normal lookup "+
			"order. Same semantic as the Skopeo/Podman/Buildah --authfile flag.")
	flag.Parse()

	if showVersion {
		printVersion(os.Stdout)
		return
	}

	// --auth-file: set REGISTRY_AUTH_FILE so go-containerregistry's default
	// keychain picks the file up. Setting the env var (rather than building
	// a custom keychain ourselves) keeps the lookup logic in one place and
	// matches the Skopeo/Podman/Buildah convention.
	if authFile != "" {
		if err := os.Setenv("REGISTRY_AUTH_FILE", authFile); err != nil {
			fmt.Fprintf(os.Stderr, "error: set REGISTRY_AUTH_FILE: %v\n", err)
			os.Exit(1)
		}
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

	// Build the base-image resolver. nil means "opt out" and routes plain-
	// tag FROM references through the literal-text fallback in the hasher.
	var resolver baseimage.Resolver
	if !noResolveFrom {
		resolver = baseimage.NewCachingResolver(&baseimage.RemoteResolver{
			PlatformOverride: platform,
		})
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
