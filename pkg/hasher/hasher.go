// Package hasher computes a deterministic SHA-256 hash from a Dockerfile,
// its build context files and any supplied build arguments.
package hasher

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/RemkoMolier/docker-hash/pkg/dockerfile"
	"github.com/moby/patternmatcher"
	"github.com/moby/patternmatcher/ignorefile"
)

// Options holds the inputs for hash computation.
type Options struct {
	// DockerfilePath is the path to the Dockerfile.
	DockerfilePath string
	// ContextDir is the root of the build context.
	ContextDir string
	// BuildArgs is a map of build argument names to values supplied by the caller.
	// Only arguments that are declared with ARG in the Dockerfile are included
	// in the hash; undeclared entries are silently ignored. Arguments present in
	// the Dockerfile ARG list but absent from this map are also omitted.
	BuildArgs map[string]string
}

// contextEntry represents a single entry collected from a COPY/ADD source.
// Regular files have an empty symlinkTarget. Inner symlinks (found while
// walking a copied directory) have a non-empty symlinkTarget holding the raw
// string returned by os.Readlink; they are hashed by that target string only,
// not by the content the link resolves to.
type contextEntry struct {
	relPath       string
	symlinkTarget string // non-empty only for inner symlinks
}

// Compute parses the Dockerfile at opts.DockerfilePath, walks the referenced
// files within opts.ContextDir and returns a hex-encoded deterministic
// SHA-256 hash that covers:
//   - the normalised Dockerfile content
//   - the names and values of all supplied build arguments (sorted)
//   - the path and content of every file referenced by COPY/ADD instructions
//   - for top-level source symlinks: the content of the resolved target
//   - for symlinks inside a walked directory: the symlink target string
func Compute(opts Options) (string, error) {
	pr, err := dockerfile.ParseFile(opts.DockerfilePath)
	if err != nil {
		return "", fmt.Errorf("parse dockerfile: %w", err)
	}

	h := sha256.New()

	// 1. Hash the Dockerfile content.
	writeSection(h, "dockerfile")
	h.Write(pr.RawContent)

	// 2. Hash build arguments (only those declared in the Dockerfile AND
	// explicitly provided by the caller, sorted for determinism).
	writeSection(h, "build-args")
	argNames := make([]string, 0, len(pr.BuildArgNames))
	argNames = append(argNames, pr.BuildArgNames...)
	sort.Strings(argNames)
	for _, name := range argNames {
		val, ok := opts.BuildArgs[name]
		if !ok {
			// Not provided by the caller — omit from hash.
			continue
		}
		_, _ = fmt.Fprintf(h, "%d:%s=%d:%s\n", len(name), name, len(val), val)
	}

	// 3. Hash the build-context files referenced by COPY/ADD.
	writeSection(h, "context-files")

	contextEntries, err := collectContextFiles(opts.ContextDir, pr.CopySources)
	if err != nil {
		return "", fmt.Errorf("collect context files: %w", err)
	}

	// Sort by relPath for determinism.
	sort.Slice(contextEntries, func(i, j int) bool {
		return contextEntries[i].relPath < contextEntries[j].relPath
	})

	for _, entry := range contextEntries {
		if entry.symlinkTarget != "" {
			if err := hashSymlink(h, entry.relPath, entry.symlinkTarget); err != nil {
				return "", fmt.Errorf("hash symlink %s: %w", entry.relPath, err)
			}
		} else {
			absPath := filepath.Join(opts.ContextDir, entry.relPath)
			if err := hashFile(h, entry.relPath, absPath); err != nil {
				return "", fmt.Errorf("hash file %s: %w", entry.relPath, err)
			}
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeSection writes a labelled separator into h so that different sections
// of the hash cannot accidentally collide.
func writeSection(h hash.Hash, label string) {
	_, _ = fmt.Fprintf(h, "\x00[%s]\x00", label)
}

// collectContextFiles returns the context entries (regular files and inner
// symlinks) for all build-context files referenced by the given COPY/ADD
// sources, deduplicated by relative path.
func collectContextFiles(contextDir string, sources []dockerfile.CopySource) ([]contextEntry, error) {
	pm, err := loadDockerIgnore(contextDir)
	if err != nil {
		return nil, fmt.Errorf("load .dockerignore: %w", err)
	}

	seen := make(map[string]struct{})
	var entries []contextEntry

	for _, src := range sources {
		// Skip sources that come from another build stage (--from=<stage>).
		if src.Stage != "" {
			continue
		}
		for _, pattern := range src.Paths {
			matched, err := resolvePattern(contextDir, pattern, pm)
			if err != nil {
				return nil, err
			}
			for _, entry := range matched {
				if _, ok := seen[entry.relPath]; !ok {
					seen[entry.relPath] = struct{}{}
					entries = append(entries, entry)
				}
			}
		}
	}
	return entries, nil
}

// loadDockerIgnore reads .dockerignore from contextDir and returns a
// PatternMatcher built from its patterns. If the file does not exist a nil
// matcher is returned (no-op). Missing file is never an error.
func loadDockerIgnore(contextDir string) (*patternmatcher.PatternMatcher, error) {
	f, err := os.Open(filepath.Join(contextDir, ".dockerignore"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	patterns, err := ignorefile.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if len(patterns) == 0 {
		return nil, nil
	}
	return patternmatcher.New(patterns)
}

// isIgnored returns true when the relative path should be excluded by the
// .dockerignore rules in pm. Uses MatchesOrParentMatches for both files and
// directories (pm.Matches is deprecated and documented as buggy by the upstream
// author). When pm is nil (no .dockerignore) the function always returns false.
func isIgnored(pm *patternmatcher.PatternMatcher, fileRel string) (bool, error) {
	if pm == nil {
		return false, nil
	}
	return pm.MatchesOrParentMatches(filepath.ToSlash(fileRel))
}

// resolvePattern resolves a COPY/ADD source pattern against contextDir and
// returns context entries (regular files and inner symlinks) that match. All
// resolved paths are verified to remain within contextDir (path traversal
// guard). Files that match the supplied .dockerignore pattern matcher (pm) are
// excluded; pass nil for no filtering.
//
// Symlink handling mirrors Docker's classic builder behavior:
//   - A top-level source that is itself a symlink is followed; the resolved
//     target (file or directory tree) is hashed by its content. If the
//     resolved target escapes the build context an error is returned.
//   - Symlinks encountered while walking a copied directory are hashed by
//     their target string only (not the content of the target), matching the
//     layer Docker produces for such entries.
func resolvePattern(contextDir, pattern string, pm *patternmatcher.PatternMatcher) ([]contextEntry, error) {
	// Short-circuit for URLs — return the URL string as a synthetic entry.
	if isURL(pattern) {
		return []contextEntry{{relPath: pattern}}, nil
	}

	absContext, err := filepath.Abs(contextDir)
	if err != nil {
		return nil, err
	}
	// Resolve any symlinks in the context directory path itself so that
	// filepath.EvalSymlinks on resolved target paths can be compared against
	// absContext correctly (e.g. /tmp → /private/tmp on macOS, or a
	// symlinked project checkout).  We fall back to the Abs result if the
	// directory cannot be resolved (e.g. it does not yet exist).
	if resolved, resolveErr := filepath.EvalSymlinks(absContext); resolveErr == nil {
		absContext = resolved
	}

	// Glob relative to context dir.
	absPattern := filepath.Join(absContext, filepath.FromSlash(pattern))

	// Path traversal guard on the pattern itself (before any filesystem access).
	// filepath.Join already collapses ".." segments, so this catches escaping paths.
	patternRel, relErr := filepath.Rel(absContext, absPattern)
	if relErr != nil || patternRel == ".." || strings.HasPrefix(patternRel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("COPY/ADD source %q escapes build context", pattern)
	}

	matches, err := filepath.Glob(absPattern)
	if err != nil {
		return nil, err
	}

	// If no glob matches, check if the literal path itself exists (e.g. plain
	// directory copy without a wildcard).
	if len(matches) == 0 {
		if _, statErr := os.Lstat(absPattern); statErr == nil {
			matches = []string{absPattern}
		}
	}

	var entries []contextEntry
	for _, abs := range matches {
		// Path traversal guard: ensure the resolved path stays within the context.
		absM, err := filepath.Abs(abs)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(absContext, absM)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("COPY/ADD source %q escapes build context", pattern)
		}

		info, err := os.Lstat(abs)
		if err != nil {
			continue
		}

		if info.Mode()&os.ModeSymlink != 0 {
			// Top-level source is a symlink: follow it and hash the target.
			resolvedAbs, resolveErr := filepath.EvalSymlinks(abs)
			if resolveErr != nil {
				return nil, fmt.Errorf("COPY/ADD source %q: follow symlink: %w", pattern, resolveErr)
			}
			resolvedRel, relErr2 := filepath.Rel(absContext, resolvedAbs)
			if relErr2 != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("COPY/ADD source %q: symlink target escapes build context", pattern)
			}
			targetInfo, statErr := os.Stat(resolvedAbs)
			if statErr != nil {
				return nil, statErr
			}
			if targetInfo.IsDir() {
				dirEntries, walkErr := walkDirEntries(absContext, resolvedAbs, pm)
				if walkErr != nil {
					return nil, walkErr
				}
				entries = append(entries, dirEntries...)
			} else if targetInfo.Mode().IsRegular() {
				ignored, matchErr := isIgnored(pm, resolvedRel)
				if matchErr != nil {
					return nil, matchErr
				}
				if !ignored {
					// The entry is keyed by resolvedRel (the target's path,
					// e.g. "real.txt"), not by the symlink name ("mylink").
					// This means a separate COPY real.txt /... in the same
					// Dockerfile deduplicates correctly via the seen map.
					entries = append(entries, contextEntry{relPath: resolvedRel})
				}
			}
		} else if info.IsDir() {
			dirEntries, walkErr := walkDirEntries(absContext, abs, pm)
			if walkErr != nil {
				return nil, walkErr
			}
			entries = append(entries, dirEntries...)
		} else if info.Mode().IsRegular() {
			// Compute the relative path directly from abs for clarity.
			fileRel, relErr2 := filepath.Rel(absContext, abs)
			if relErr2 != nil {
				return nil, relErr2
			}
			// Apply .dockerignore filtering.
			ignored, matchErr := isIgnored(pm, fileRel)
			if matchErr != nil {
				return nil, matchErr
			}
			if ignored {
				continue
			}
			entries = append(entries, contextEntry{relPath: fileRel})
		}
		// Non-regular, non-directory, non-symlink entries (FIFOs, devices) are skipped.
	}

	return entries, nil
}

// walkDirEntries walks absDir and returns context entries for all regular files
// and inner symlinks found within it. Paths are expressed relative to
// absContext. Files excluded by pm are omitted.
func walkDirEntries(absContext, absDir string, pm *patternmatcher.PatternMatcher) ([]contextEntry, error) {
	// canPruneIgnoredDirs is constant for the duration of the walk:
	// we can only skip an entire subtree when no negation patterns
	// exist in the matcher (e.g. "subdir" + "!subdir/keep.txt"
	// requires descending into subdir and filtering file-by-file).
	canPruneIgnoredDirs := pm == nil || !pm.Exclusions()

	var entries []contextEntry
	err := filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fileRel, relErr := filepath.Rel(absContext, path)
		if relErr != nil {
			return relErr
		}
		// Traversal guard inside directory walk.
		if fileRel == ".." || strings.HasPrefix(fileRel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("path %q escapes build context", path)
		}
		// Apply .dockerignore filtering.
		ignored, matchErr := isIgnored(pm, fileRel)
		if matchErr != nil {
			return matchErr
		}
		if ignored {
			if d.IsDir() && canPruneIgnoredDirs {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Inner symlinks: hash the target string, not the target content.
		if d.Type()&fs.ModeSymlink != 0 {
			target, readErr := os.Readlink(path)
			if readErr != nil {
				return readErr
			}
			entries = append(entries, contextEntry{relPath: fileRel, symlinkTarget: target})
			return nil
		}
		// Only include regular files; skip FIFOs, devices, etc.
		if d.Type().IsRegular() {
			entries = append(entries, contextEntry{relPath: fileRel})
		}
		return nil
	})
	return entries, err
}

// hashFile hashes a single context file into the outer hasher h.
// Each file is first hashed independently (SHA-256), then its path,
// byte count and sub-hash are written into h using a length-prefixed
// format to prevent cross-entry collisions.
func hashFile(h hash.Hash, relPath, absPath string) error {
	if isURL(relPath) {
		// URLs are hashed by their string value, not by remote content.
		_, _ = fmt.Fprintf(h, "url:%d:%s\n", len(relPath), relPath)
		return nil
	}

	f, err := os.Open(absPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	fh := sha256.New()
	n, err := io.Copy(fh, f)
	if err != nil {
		return err
	}
	slashPath := filepath.ToSlash(relPath)
	_, _ = fmt.Fprintf(h, "file:%d:%s:%d:%x\n", len(slashPath), slashPath, n, fh.Sum(nil))
	return nil
}

// hashSymlink hashes a symbolic link entry into the outer hasher h.
// Only the symlink target string is hashed (not the content of whatever the
// symlink points to). This matches Docker's behavior for inner symlinks found
// while walking a copied directory: Docker preserves the symlink as-is in the
// resulting layer, so only the target string affects the layer.
func hashSymlink(h hash.Hash, relPath, target string) error {
	slashPath := filepath.ToSlash(relPath)
	_, _ = fmt.Fprintf(h, "symlink:%d:%s:%d:%s\n", len(slashPath), slashPath, len(target), target)
	return nil
}

// isURL returns true when pattern looks like an http or https URL.
func isURL(pattern string) bool {
	return strings.HasPrefix(pattern, "http://") || strings.HasPrefix(pattern, "https://")
}

