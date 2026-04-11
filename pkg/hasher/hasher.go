// Package hasher computes a deterministic SHA-256 hash from a Dockerfile,
// its build context files and any supplied build arguments.
package hasher

import (
	"context"
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

	"github.com/RemkoMolier/docker-hash/pkg/baseimage"
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
	// BaseImageResolver, when non-nil, is called for every FROM line whose
	// target is a registry image (i.e. not "scratch", not a stage reference,
	// and not already pinned by digest). The resolved "<repo>@sha256:..."
	// digest string is folded into a dedicated "base-images" section in the
	// hash output. Pass nil to omit the entire base-images section: the FROM
	// text then still affects the hash via the raw-Dockerfile section in
	// section 1, and the resulting digest is bit-for-bit identical to a
	// v0.1.x hash for the same inputs.
	BaseImageResolver baseimage.Resolver
	// Context is the context.Context passed to BaseImageResolver. nil means
	// context.Background(). Cancellation propagates from this context to all
	// in-flight registry calls.
	Context context.Context
}

// contextEntry represents a single entry collected from a COPY/ADD source.
// Regular files have isSymlink == false. Inner symlinks (found while walking
// a copied directory) have isSymlink == true and symlinkTarget set to the raw
// string returned by os.Readlink; they are hashed by that target string only,
// not by the content the link resolves to. isSymlink is the discriminator
// (rather than `symlinkTarget != ""`) so that a symlink whose target is the
// empty string — legal on some POSIX systems though not creatable on Linux —
// is still classified as a symlink and hashed as such, rather than being
// passed to hashFile and silently following the link via os.Open.
type contextEntry struct {
	relPath       string
	symlinkTarget string // raw os.Readlink result; only meaningful when isSymlink
	isSymlink     bool
}

// Compute parses the Dockerfile at opts.DockerfilePath, walks the referenced
// files within opts.ContextDir and returns a hex-encoded deterministic
// SHA-256 hash that covers:
//   - the normalised Dockerfile content
//   - the names and values of all supplied build arguments (sorted)
//   - the path and content of every regular file referenced by COPY/ADD
//   - for top-level source symlinks: the content of the resolved target
//   - for symlinks inside a walked directory: the symlink target string only
//   - the resolved digest of every FROM image, when opts.BaseImageResolver is set
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
		if entry.isSymlink {
			if err := hashSymlink(h, entry.relPath, entry.symlinkTarget); err != nil {
				return "", fmt.Errorf("hash symlink %s: %w", entry.relPath, err)
			}
			continue
		}
		absPath := filepath.Join(opts.ContextDir, entry.relPath)
		if err := hashFile(h, entry.relPath, absPath); err != nil {
			return "", fmt.Errorf("hash file %s: %w", entry.relPath, err)
		}
	}

	// 4. Hash the FROM base images. Each FROM line in the Dockerfile
	// contributes one entry, in declaration order, so the hash captures any
	// drift in the upstream registry. The contribution shape depends on the
	// kind of FROM after $VAR / ${VAR} expansion against pre-FROM ARG
	// defaults plus caller-supplied build args:
	//
	//   - "FROM scratch"        → "scratch"
	//   - "FROM <stage>"        → "stage:<alias>" (no resolution)
	//   - "FROM <repo>@<sha>"   → "<canonical>@<sha>" (no network call)
	//   - "FROM <repo>:<tag>"   → "<canonical>@<resolved-sha>" via the
	//                              resolver
	//   - "FROM ${VAR}" with no
	//     value anywhere         → "unexpanded:..." (literal contribution)
	//
	// Platform variables that are not resolvable (typically `$BUILDPLATFORM`
	// or `$TARGETPLATFORM` when the caller did not supply them) drop to "no
	// platform" so the resolver returns the multi-arch index digest, which
	// is the deterministic choice across runner architectures.
	//
	// This entire section is skipped when opts.BaseImageResolver is nil
	// (the --no-resolve-from path), which makes the resulting hash
	// bit-for-bit identical to a v0.1.x hash for the same inputs. The FROM
	// line text is still part of the Dockerfile content hashed in section 1,
	// so different FROM tags continue to produce different hashes.
	if opts.BaseImageResolver != nil {
		writeSection(h, "base-images")
		lookup := buildArgLookup(opts.BuildArgs, pr.PreFromArgDefaults)
		if err := hashBaseImages(h, pr.FromRefs, pr.StageAliases, opts, lookup); err != nil {
			return "", fmt.Errorf("hash base images: %w", err)
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// buildArgLookup returns a dockerfile.ArgLookup that consults caller-supplied
// build args first, then pre-FROM ARG defaults from the parsed Dockerfile.
// Either map may be nil.
func buildArgLookup(callerArgs, defaults map[string]string) dockerfile.ArgLookup {
	return func(name string) (string, bool) {
		if callerArgs != nil {
			if v, ok := callerArgs[name]; ok {
				return v, true
			}
		}
		if defaults != nil {
			if v, ok := defaults[name]; ok {
				return v, true
			}
		}
		return "", false
	}
}

// hashBaseImages folds every FROM reference in the parsed Dockerfile into h.
// See Compute's section-4 comment for the contribution rules. Caller must
// only invoke this when opts.BaseImageResolver is non-nil; the --no-resolve-from
// path skips the entire section in Compute, not in this helper.
//
// Each FromRef is expanded against `lookup` before any other processing, so
// $VAR / ${VAR} references in either the image or the platform field are
// substituted using caller-supplied build args layered over pre-FROM ARG
// defaults. After expansion, IsStageRef is re-evaluated against
// `stageAliases` because an ARG can resolve to a stage alias.
func hashBaseImages(
	h hash.Hash,
	refs []dockerfile.FromRef,
	stageAliases map[string]struct{},
	opts Options,
	lookup dockerfile.ArgLookup,
) error {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}

	for i, original := range refs {
		// Expand $VAR / ${VAR} references in both Image and Platform.
		ref := original.Expand(lookup)

		// Re-evaluate stage detection: the parser tagged IsStageRef on the
		// literal Image, but if an ARG expanded to a known stage alias the
		// post-expansion ref is also a stage reference and must not hit
		// the resolver.
		if !ref.IsStageRef {
			if _, isStage := stageAliases[ref.Image]; isStage {
				ref.IsStageRef = true
			}
		}

		// Always include a per-line index so two FROM lines with the same
		// resolved image still contribute distinct positions.
		_, _ = fmt.Fprintf(h, "from[%d]:", i)

		switch {
		case ref.IsStageRef:
			// Internal stage reference. The underlying image was already
			// hashed earlier in the slice; record only the alias here so
			// renaming a stage produces a different hash.
			_, _ = fmt.Fprintf(h, "stage:%s\n", ref.Image)

		case baseimage.IsScratch(ref.Image):
			// The Docker-internal sentinel. No registry, no digest, but
			// still distinguishable from any registry image.
			_, _ = fmt.Fprintf(h, "scratch\n")

		case baseimage.IsAlreadyPinned(ref.Image):
			// Already pinned by digest. Canonicalise offline (no network,
			// no resolver invocation) and emit the same "resolved:" shape
			// as a fully-resolved entry, so a "tag-then-digest pin" upgrade
			// produces the same hash as the resolved tag.
			canonical, err := baseimage.Canonicalize(ref.Image)
			if err != nil {
				return fmt.Errorf("canonicalize FROM %q: %w", ref.Image, err)
			}
			_, _ = fmt.Fprintf(h, "resolved:%s:%s\n", platformForResolverHash(ref.Platform), canonical)

		case strings.ContainsRune(ref.Image, '$'):
			// The image still contains an unresolvable variable after
			// expansion: the ARG was referenced but has neither a default
			// in the Dockerfile nor a value in opts.BuildArgs. Fall back
			// to a literal contribution rather than crashing the run.
			//
			// The full FROM line text (including the unresolved ${...})
			// is still in the section-1 Dockerfile content, so changing
			// the templated value still affects the hash via that path.
			_, _ = fmt.Fprintf(h, "unexpanded:%s:%s\n", platformOrDash(ref.Platform), ref.Image)

		default:
			// Plain registry reference — go fetch the digest. Drop any
			// unresolvable platform variable (typically $BUILDPLATFORM /
			// $TARGETPLATFORM with no caller-supplied value) to "no
			// platform" so the resolver returns the multi-arch index
			// digest. The deterministic choice across runner archs.
			platForResolver := ref.Platform
			if strings.ContainsRune(platForResolver, '$') {
				platForResolver = ""
			}
			resolved, err := opts.BaseImageResolver.Resolve(ctx, baseimage.Reference{
				Image:    ref.Image,
				Platform: platForResolver,
			})
			if err != nil {
				return fmt.Errorf("resolve FROM %q: %w", ref.Image, err)
			}
			_, _ = fmt.Fprintf(h, "resolved:%s:%s\n", platformForResolverHash(platForResolver), resolved)
		}
	}
	return nil
}

// platformForResolverHash returns the platform discriminator used in
// "resolved:" hash entries. Empty platforms (or platforms whose value still
// contains a "$" because the variable could not be expanded) collapse to
// "-" so the hash output is stable regardless of runner architecture.
func platformForResolverHash(platform string) string {
	if platform == "" || strings.ContainsRune(platform, '$') {
		return "-"
	}
	return platform
}

// platformOrDash returns the given platform string, or "-" if it is empty.
// Used as a discriminator in the base-image hash contribution so two FROMs
// of the same image with different --platform values hash distinctly.
func platformOrDash(platform string) string {
	if platform == "" {
		return "-"
	}
	return platform
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

		// Build a per-source PatternMatcher from --exclude= flags, if any.
		var excludePM *patternmatcher.PatternMatcher
		if len(src.Excludes) > 0 {
			excludePM, err = patternmatcher.New(src.Excludes)
			if err != nil {
				return nil, fmt.Errorf("build --exclude patterns: %w", err)
			}
		}

		for _, pattern := range src.Paths {
			matched, err := resolvePattern(contextDir, pattern, pm, excludePM)
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
// patterns in pm. Uses MatchesOrParentMatches for both files and directories
// (pm.Matches is deprecated and documented as buggy by the upstream author).
// When pm is nil the function always returns false.
func isIgnored(pm *patternmatcher.PatternMatcher, fileRel string) (bool, error) {
	if pm == nil {
		return false, nil
	}
	return pm.MatchesOrParentMatches(filepath.ToSlash(fileRel))
}

// resolvePattern resolves a COPY/ADD source pattern against contextDir and
// returns context entries (regular files and inner symlinks) that match. All
// resolved paths are verified to remain within contextDir (path traversal
// guard). Files that match the supplied .dockerignore pattern matcher (pm) or
// the per-source --exclude pattern matcher (excludePM) are filtered out;
// excludePM patterns are matched against paths relative to the matched source
// root, following Docker's documented --exclude semantics.
//
// Symlink handling mirrors Docker's classic builder behaviour:
//   - A top-level source that is itself a symlink is followed; the resolved
//     target (file or directory tree) is hashed by content. If the resolved
//     target escapes the build context an error is returned.
//   - Symlinks encountered while walking a copied directory are hashed by
//     their target string only (not the content of the target), matching the
//     layer Docker produces for such entries.
func resolvePattern(contextDir, pattern string, pm, excludePM *patternmatcher.PatternMatcher) ([]contextEntry, error) {
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
	// absContext correctly. Without this, a ContextDir behind a symlinked
	// parent (e.g. /tmp → /private/tmp on macOS, or a symlinked project
	// checkout) would cause every top-level source symlink to be spuriously
	// rejected as escaping the build context. Fall back to the Abs result if
	// the directory cannot be resolved (e.g. it does not yet exist).
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

	// If the pattern resolved to no filesystem entries at all, return an error
	// that names the offending pattern. Docker itself rejects such Dockerfiles
	// with "no such file or directory" or "no source files were specified".
	if len(matches) == 0 {
		return nil, fmt.Errorf("COPY/ADD source %q matches no files in build context", pattern)
	}

	var entries []contextEntry
	var anyIgnored bool
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

		switch {
		case info.Mode()&os.ModeSymlink != 0:
			// Top-level source is a symlink: follow it and hash whatever it
			// resolves to using the same downstream paths as the
			// regular-file and directory branches.
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
				dirEntries, dirIgnored, walkErr := walkDirEntries(absContext, resolvedAbs, pm, excludePM)
				if walkErr != nil {
					return nil, walkErr
				}
				entries = append(entries, dirEntries...)
				if dirIgnored {
					anyIgnored = true
				}
			} else if targetInfo.Mode().IsRegular() {
				ignored, matchErr := isIgnored(pm, resolvedRel)
				if matchErr != nil {
					return nil, matchErr
				}
				if ignored {
					anyIgnored = true
					break
				}
				// For a top-level source the --exclude matching path is the
				// source's own basename, the same convention as the literal
				// regular-file branch below — the user is filtering against
				// the name they typed in the Dockerfile, not the resolved
				// target's name. Use abs (the symlink path), not resolvedAbs.
				excluded, excMatchErr := isIgnored(excludePM, filepath.Base(abs))
				if excMatchErr != nil {
					return nil, excMatchErr
				}
				if excluded {
					break
				}
				// The entry is keyed by resolvedRel (the target's path,
				// e.g. "real.txt"), not by the symlink name ("mylink").
				// This means a separate COPY real.txt /... in the same
				// Dockerfile deduplicates correctly via the seen map, and a
				// symlink to an out-of-tree file would already have errored
				// above on the escapes-context check.
				entries = append(entries, contextEntry{relPath: resolvedRel})
			}
		case info.IsDir():
			dirEntries, dirIgnored, walkErr := walkDirEntries(absContext, abs, pm, excludePM)
			if walkErr != nil {
				return nil, walkErr
			}
			entries = append(entries, dirEntries...)
			if dirIgnored {
				anyIgnored = true
			}
		case info.Mode().IsRegular():
			fileRel, relErr2 := filepath.Rel(absContext, abs)
			if relErr2 != nil {
				return nil, relErr2
			}
			ignored, matchErr := isIgnored(pm, fileRel)
			if matchErr != nil {
				return nil, matchErr
			}
			if ignored {
				anyIgnored = true
				continue
			}
			// For a literal file source, the --exclude matching path is the
			// file's base name (path relative to itself).
			excluded, excMatchErr := isIgnored(excludePM, filepath.Base(abs))
			if excMatchErr != nil {
				return nil, excMatchErr
			}
			if excluded {
				continue
			}
			entries = append(entries, contextEntry{relPath: fileRel})
		}
		// Other entry types (FIFOs, devices) are silently skipped.
	}

	if len(entries) == 0 && anyIgnored {
		return nil, fmt.Errorf("COPY/ADD source %q matches files in the build context, but all of them are excluded by .dockerignore", pattern)
	}
	// A COPY/ADD source that resolves to zero files is always an error: Docker
	// itself rejects such instructions at build time. This also catches the
	// case where every matched file was filtered out by --exclude patterns.
	if len(entries) == 0 {
		return nil, fmt.Errorf("COPY/ADD source %q matches no files in build context", pattern)
	}

	return entries, nil
}

// walkDirEntries walks absDir and returns context entries for all regular
// files and inner symlinks found within it. Paths are expressed relative to
// absContext. Files filtered by pm (.dockerignore) or by excludePM
// (per-source --exclude) are omitted; pm matches use absContext-relative
// paths while excludePM matches use absDir-relative paths, following
// Docker's documented --exclude semantics. The returned bool is true when at
// least one file or symlink was filtered by pm — used by the caller to
// distinguish "all files were dockerignored" from "the source matched
// nothing on disk", which produce different error messages.
func walkDirEntries(absContext, absDir string, pm, excludePM *patternmatcher.PatternMatcher) ([]contextEntry, bool, error) {
	// canPruneIgnoredDirs is constant for the duration of the walk: we can
	// only skip an entire subtree when no negation patterns exist in the
	// matcher (e.g. "subdir" + "!subdir/keep.txt" requires descending into
	// subdir and filtering file-by-file).
	canPruneIgnoredDirs := pm == nil || !pm.Exclusions()
	canPruneExcludedDirs := excludePM == nil || !excludePM.Exclusions()

	var entries []contextEntry
	var anyIgnored bool
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
				anyIgnored = true
				return fs.SkipDir
			}
			// Only count file-like entries we would otherwise have included
			// toward anyIgnored; ignored directories that can't be pruned
			// (because of negation patterns) descend further.
			if d.Type().IsRegular() || d.Type()&fs.ModeSymlink != 0 {
				anyIgnored = true
			}
			return nil
		}
		// Apply per-source --exclude filtering using path relative to the
		// walked source root (Docker's documented --exclude semantics).
		fileRelToSrc, srcRelErr := filepath.Rel(absDir, path)
		if srcRelErr != nil {
			return srcRelErr
		}
		excluded, excMatchErr := isIgnored(excludePM, fileRelToSrc)
		if excMatchErr != nil {
			return excMatchErr
		}
		if excluded {
			if d.IsDir() && canPruneExcludedDirs {
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
			entries = append(entries, contextEntry{relPath: fileRel, symlinkTarget: target, isSymlink: true})
			return nil
		}
		// Regular file.
		if d.Type().IsRegular() {
			entries = append(entries, contextEntry{relPath: fileRel})
		}
		// Other types (FIFOs, devices) are silently skipped.
		return nil
	})
	return entries, anyIgnored, err
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

// hashSymlink hashes a symbolic link entry into the outer hasher h. Only the
// symlink target string is hashed (not the content of whatever the symlink
// points to). This matches Docker's behaviour for inner symlinks found while
// walking a copied directory: Docker preserves the symlink as-is in the
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
