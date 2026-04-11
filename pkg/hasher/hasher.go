// Package hasher computes a deterministic SHA-256 hash from a Dockerfile,
// its build context files and any supplied build arguments.
package hasher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RemkoMolier/docker-hash/pkg/dockerfile"
)

// Options holds the inputs for hash computation.
type Options struct {
	// DockerfilePath is the path to the Dockerfile.
	DockerfilePath string
	// ContextDir is the root of the build context.
	ContextDir string
	// BuildArgs is a map of build argument names to values supplied by the caller.
	BuildArgs map[string]string
}

// Compute parses the Dockerfile at opts.DockerfilePath, walks the referenced
// files within opts.ContextDir and returns a hex-encoded deterministic
// SHA-256 hash that covers:
//   - the normalised Dockerfile content
//   - the names and values of all supplied build arguments (sorted)
//   - the path and content of every file referenced by COPY/ADD instructions
func Compute(opts Options) (string, error) {
	pr, err := dockerfile.ParseFile(opts.DockerfilePath)
	if err != nil {
		return "", fmt.Errorf("parse dockerfile: %w", err)
	}

	h := sha256.New()

	// 1. Hash the Dockerfile content.
	writeSection(h, "dockerfile")
	h.Write(pr.RawContent)

	// 2. Hash build arguments (only those declared in the Dockerfile, sorted).
	writeSection(h, "build-args")
	argNames := make([]string, 0, len(pr.BuildArgNames))
	argNames = append(argNames, pr.BuildArgNames...)
	sort.Strings(argNames)
	for _, name := range argNames {
		val := opts.BuildArgs[name]
		fmt.Fprintf(h, "%s=%s\n", name, val)
	}

	// 3. Hash the build-context files referenced by COPY/ADD.
	writeSection(h, "context-files")

	contextFiles, err := collectContextFiles(opts.ContextDir, pr.CopySources)
	if err != nil {
		return "", fmt.Errorf("collect context files: %w", err)
	}

	// Sort paths for determinism.
	sort.Strings(contextFiles)

	for _, relPath := range contextFiles {
		absPath := filepath.Join(opts.ContextDir, relPath)
		if err := hashFile(h, relPath, absPath); err != nil {
			return "", fmt.Errorf("hash file %s: %w", relPath, err)
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeSection writes a labelled separator into h so that different sections
// of the hash cannot accidentally collide.
func writeSection(h hash.Hash, label string) {
	fmt.Fprintf(h, "\x00[%s]\x00", label)
}

// collectContextFiles returns the relative paths of all build-context files
// referenced by the given COPY/ADD sources, deduplicated.
func collectContextFiles(contextDir string, sources []dockerfile.CopySource) ([]string, error) {
	seen := make(map[string]struct{})
	var files []string

	for _, src := range sources {
		// Skip sources that come from another build stage (--from=<stage>).
		if src.Stage != "" {
			continue
		}
		for _, pattern := range src.Paths {
			matched, err := resolvePattern(contextDir, pattern)
			if err != nil {
				return nil, err
			}
			for _, rel := range matched {
				if _, ok := seen[rel]; !ok {
					seen[rel] = struct{}{}
					files = append(files, rel)
				}
			}
		}
	}
	return files, nil
}

// resolvePattern resolves a COPY/ADD source pattern against contextDir and
// returns relative file paths (not directories) that match.
func resolvePattern(contextDir, pattern string) ([]string, error) {
	// Glob relative to context dir.
	absPattern := filepath.Join(contextDir, filepath.FromSlash(pattern))
	matches, err := filepath.Glob(absPattern)
	if err != nil {
		return nil, err
	}

	// If no glob matches, check if the path itself exists (e.g. plain dir copy).
	if len(matches) == 0 {
		if _, statErr := os.Stat(absPattern); statErr == nil {
			matches = []string{absPattern}
		}
	}

	var files []string
	for _, abs := range matches {
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if info.IsDir() {
			// Walk the directory and collect all regular files.
			err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					rel, relErr := filepath.Rel(contextDir, path)
					if relErr != nil {
						return relErr
					}
					files = append(files, rel)
				}
				return nil
			})
			if err != nil {
				return nil, err
			}
		} else {
			rel, err := filepath.Rel(contextDir, abs)
			if err != nil {
				return nil, err
			}
			files = append(files, rel)
		}
	}

	// Also handle URLs (http/https) in ADD — include them as a literal string
	// contribution so the hash changes if the URL changes.
	if isURL(pattern) {
		files = append(files, pattern)
	}

	return files, nil
}

// hashFile writes the relative path and content of a file into h.
func hashFile(h hash.Hash, relPath, absPath string) error {
	// Write path separator for clarity.
	fmt.Fprintf(h, "\nfile:%s\n", filepath.ToSlash(relPath))

	// If this is a URL (added by resolvePattern), just hash the URL string.
	if isURL(relPath) {
		return nil
	}

	f, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	return nil
}

// isURL returns true when pattern looks like an http or https URL.
func isURL(pattern string) bool {
	return strings.HasPrefix(pattern, "http://") || strings.HasPrefix(pattern, "https://")
}
