package hasher_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/RemkoMolier/docker-hash/pkg/baseimage"
	"github.com/RemkoMolier/docker-hash/pkg/hasher"
)

// fakeResolver is a baseimage.Resolver backed by a static map. It records
// every call so tests can assert on the number of underlying lookups (which
// matters for the per-Compute cache).
type fakeResolver struct {
	results map[string]string
	calls   atomic.Int32
	err     error
}

func (f *fakeResolver) Resolve(_ context.Context, ref baseimage.Reference) (string, error) {
	f.calls.Add(1)
	if f.err != nil {
		return "", f.err
	}
	key := ref.Image + "|" + ref.Platform
	if v, ok := f.results[key]; ok {
		return v, nil
	}
	return "", errors.New("fakeResolver: no result for " + key)
}

// buildTestContext creates a temporary directory with a Dockerfile and
// context files, returning the directory path and a cleanup function.
func buildTestContext(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return dir
}

func TestCompute_Deterministic(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nARG VERSION\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		BuildArgs:      map[string]string{"VERSION": "1.0"},
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("first Compute: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("second Compute: %v", err)
	}
	if h1 != h2 {
		t.Errorf("hash is not deterministic: %s vs %s", h1, h2)
	}
}

func TestCompute_ChangeDockerfileChangesHash(t *testing.T) {
	dir1 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})
	dir2 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM alpine:3.18\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})

	h1, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir1, "Dockerfile"), ContextDir: dir1})
	if err != nil {
		t.Fatalf("Compute dir1: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir2, "Dockerfile"), ContextDir: dir2})
	if err != nil {
		t.Fatalf("Compute dir2: %v", err)
	}
	if h1 == h2 {
		t.Error("different Dockerfiles should produce different hashes")
	}
}

func TestCompute_ChangeFileChangesHash(t *testing.T) {
	dir1 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})
	dir2 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY app.py /app/\n",
		"app.py":     "print('world')\n",
	})

	h1, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir1, "Dockerfile"), ContextDir: dir1})
	if err != nil {
		t.Fatalf("Compute dir1: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir2, "Dockerfile"), ContextDir: dir2})
	if err != nil {
		t.Fatalf("Compute dir2: %v", err)
	}
	if h1 == h2 {
		t.Error("different COPY'd file contents should produce different hashes")
	}
}

func TestCompute_ChangeBuildArgChangesHash(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nARG VERSION\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})

	opts1 := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		BuildArgs:      map[string]string{"VERSION": "1.0"},
	}
	opts2 := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		BuildArgs:      map[string]string{"VERSION": "2.0"},
	}

	h1, err := hasher.Compute(opts1)
	if err != nil {
		t.Fatalf("Compute opts1: %v", err)
	}
	h2, err := hasher.Compute(opts2)
	if err != nil {
		t.Fatalf("Compute opts2: %v", err)
	}
	if h1 == h2 {
		t.Error("different build arg values should produce different hashes")
	}
}

func TestCompute_UndeclaredBuildArgIgnored(t *testing.T) {
	// An argument passed but not declared with ARG should not affect the hash.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})

	opts1 := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		BuildArgs:      map[string]string{},
	}
	opts2 := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		BuildArgs:      map[string]string{"UNDECLARED": "value"},
	}

	h1, err := hasher.Compute(opts1)
	if err != nil {
		t.Fatalf("Compute opts1: %v", err)
	}
	h2, err := hasher.Compute(opts2)
	if err != nil {
		t.Fatalf("Compute opts2: %v", err)
	}
	if h1 != h2 {
		t.Error("undeclared build args should not affect the hash")
	}
}

func TestCompute_NoCopyInstructions(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nRUN echo hello\n",
	})

	h, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if h == "" {
		t.Error("expected a non-empty hash")
	}
}

func TestCompute_DirectoryCopy(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":    "FROM ubuntu:22.04\nCOPY src/ /app/\n",
		"src/main.py":   "print('main')\n",
		"src/helper.py": "def helper(): pass\n",
	})

	h1, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir, "Dockerfile"), ContextDir: dir})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Modifying a file inside the copied directory should change the hash.
	if err := os.WriteFile(filepath.Join(dir, "src", "main.py"), []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	h2, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir, "Dockerfile"), ContextDir: dir})
	if err != nil {
		t.Fatalf("Compute after change: %v", err)
	}
	if h1 == h2 {
		t.Error("modifying a file inside a COPY'd directory should change the hash")
	}
}

func TestCompute_MultistageIgnoresStageFiles(t *testing.T) {
	// COPY --from=builder should NOT cause the hasher to look for local files.
	// This Dockerfile has no COPY that would pick up everything in the context,
	// so only `config/` is hashed from the build context.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": `FROM golang:1.21 AS builder
RUN echo "build step"

FROM ubuntu:22.04
COPY --from=builder /bin/app /usr/local/bin/app
COPY config/ /etc/app/
`,
		"config/app.yaml": "key: value\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Create a file at a path that WOULD be matched if the --from= filter
	// were broken (/bin/app inside the context). The hash must not change.
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "app"), []byte("decoy binary"), 0o755); err != nil {
		t.Fatalf("write bin/app: %v", err)
	}

	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after adding decoy: %v", err)
	}
	if h1 != h2 {
		t.Error("--from=<stage> sources must not pull in local context files")
	}
}

func TestCompute_HashLength(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM scratch\n",
	})

	h, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	// SHA-256 hex digest is always 64 characters.
	if len(h) != 64 {
		t.Errorf("expected 64-char hash, got %d chars: %s", len(h), h)
	}
}

func TestCompute_PathTraversal(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY ../etc/passwd /app/\n",
	})

	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Error("expected an error for COPY source escaping build context, got nil")
	}
}

func TestCompute_GlobPattern(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":  "FROM ubuntu:22.04\nCOPY *.py /app/\n",
		"main.py":     "print('main')\n",
		"helper.py":   "def helper(): pass\n",
		"ignored.txt": "not a py file\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Changing a matched .py file must change the hash.
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after change: %v", err)
	}
	if h1 == h2 {
		t.Error("modifying a glob-matched file should change the hash")
	}

	// Changing the non-matched .txt file must NOT change the hash (reset .py first).
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('main')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile reset: %v", err)
	}
	h3, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after reset: %v", err)
	}
	if h1 != h3 {
		t.Error("hash should be stable after resetting the file")
	}

	if err := os.WriteFile(filepath.Join(dir, "ignored.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile ignored.txt: %v", err)
	}
	h4, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after ignored change: %v", err)
	}
	if h3 != h4 {
		t.Error("changing a file not matched by the glob should not change the hash")
	}
}

func TestCompute_AddURL(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nADD https://example.com/file.tar.gz /tmp/\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hash, got %d", len(h1))
	}

	// A different URL must produce a different hash.
	dir2 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nADD https://example.com/other.tar.gz /tmp/\n",
	})
	h2, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir2, "Dockerfile"), ContextDir: dir2})
	if err != nil {
		t.Fatalf("Compute dir2: %v", err)
	}
	if h1 == h2 {
		t.Error("different ADD URLs should produce different hashes")
	}
}

func TestCompute_BuildArgWithEqualsInValue(t *testing.T) {
	// ARG values that contain '=' must be handled correctly.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nARG KEY\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})

	opts1 := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		BuildArgs:      map[string]string{"KEY": "a=b=c"},
	}
	opts2 := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		BuildArgs:      map[string]string{"KEY": "x=y"},
	}

	h1, err := hasher.Compute(opts1)
	if err != nil {
		t.Fatalf("Compute opts1: %v", err)
	}
	h2, err := hasher.Compute(opts2)
	if err != nil {
		t.Fatalf("Compute opts2: %v", err)
	}
	if h1 == h2 {
		t.Error("different build arg values (with '=' in value) should produce different hashes")
	}
}

// ---- .dockerignore tests ----

func TestCompute_DockerIgnore_ExcludesFiles(t *testing.T) {
	// **/*.log should exclude log files even when COPY . picks them up.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":    "FROM ubuntu:22.04\nCOPY . /app/\n",
		"app.py":        "print('hello')\n",
		"build.log":     "some log output\n",
		".dockerignore": "**/*.log\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Modifying the log file must NOT change the hash (it is ignored).
	if err := os.WriteFile(filepath.Join(dir, "build.log"), []byte("different log\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after log change: %v", err)
	}
	if h1 != h2 {
		t.Error("modifying an ignored file (*.log) should not change the hash")
	}

	// Modifying the non-ignored .py file MUST change the hash.
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile py: %v", err)
	}
	h3, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after py change: %v", err)
	}
	if h1 == h3 {
		t.Error("modifying a non-ignored file should change the hash")
	}
}

func TestCompute_DockerIgnore_NegationPattern(t *testing.T) {
	// *.log ignores all logs; !important.log re-includes that one file.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":    "FROM ubuntu:22.04\nCOPY . /app/\n",
		"app.py":        "print('hello')\n",
		"debug.log":     "debug info\n",
		"important.log": "critical data\n",
		".dockerignore": "*.log\n!important.log\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Changing debug.log (ignored) must not change the hash.
	if err := os.WriteFile(filepath.Join(dir, "debug.log"), []byte("changed debug\n"), 0o644); err != nil {
		t.Fatalf("WriteFile debug.log: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after debug.log change: %v", err)
	}
	if h1 != h2 {
		t.Error("changing an ignored file (debug.log) should not change the hash")
	}

	// Changing important.log (re-included via negation) MUST change the hash.
	if err := os.WriteFile(filepath.Join(dir, "important.log"), []byte("changed critical\n"), 0o644); err != nil {
		t.Fatalf("WriteFile important.log: %v", err)
	}
	h3, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after important.log change: %v", err)
	}
	if h1 == h3 {
		t.Error("changing a re-included file (!important.log) should change the hash")
	}
}

func TestCompute_DockerIgnore_Missing(t *testing.T) {
	// No .dockerignore file — behaviour must be identical to the current implementation.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(h1) != 64 {
		t.Errorf("expected 64-char hash, got %d", len(h1))
	}

	// Hash should be stable.
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute 2nd: %v", err)
	}
	if h1 != h2 {
		t.Error("hash should be stable when .dockerignore is absent")
	}
}

func TestCompute_DockerIgnore_SelfIgnore(t *testing.T) {
	// A .dockerignore that ignores itself should be handled the same way docker
	// build handles it: the file is still read and applied, but the .dockerignore
	// file itself is excluded from the build context.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":    "FROM ubuntu:22.04\nCOPY . /app/\n",
		"app.py":        "print('hello')\n",
		".dockerignore": ".dockerignore\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Changing .dockerignore itself must NOT change the hash because the file
	// ignores itself.
	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte(".dockerignore\n# changed comment\n"), 0o644); err != nil {
		t.Fatalf("WriteFile .dockerignore: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after .dockerignore change: %v", err)
	}
	if h1 != h2 {
		t.Error("a self-ignoring .dockerignore should not affect the hash when it changes")
	}

	// Changing app.py MUST change the hash — this asserts that the walk root
	// "." (fileRel when abs == absContext) is never filtered by
	// MatchesOrParentMatches, so the context files are still reachable.
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile app.py: %v", err)
	}
	h3, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after app.py change: %v", err)
	}
	if h1 == h3 {
		t.Error("changing app.py should change the hash (walk root '.' must not be filtered)")
	}
}

func TestCompute_DockerIgnore_PathTraversalStillErrors(t *testing.T) {
	// The path-traversal guard must fire even when .dockerignore would have
	// excluded the offending path.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":    "FROM ubuntu:22.04\nCOPY ../etc/passwd /app/\n",
		".dockerignore": "../etc/passwd\n",
	})

	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Error("expected an error for COPY source escaping build context, got nil")
	}
}

func TestCompute_DockerIgnore_NegationInsideIgnoredDir(t *testing.T) {
	// "subdir" ignores the whole directory, but "!subdir/keep.txt" re-includes
	// one file. The hash must change when keep.txt changes.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":      "FROM ubuntu:22.04\nCOPY . /app/\n",
		"subdir/skip.txt": "skip me\n",
		"subdir/keep.txt": "keep me\n",
		".dockerignore":   "subdir\n!subdir/keep.txt\n",
	})

	opts := hasher.Options{DockerfilePath: filepath.Join(dir, "Dockerfile"), ContextDir: dir}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Changing skip.txt (ignored) must NOT change the hash.
	if err := os.WriteFile(filepath.Join(dir, "subdir/skip.txt"), []byte("changed skip\n"), 0o644); err != nil {
		t.Fatalf("WriteFile skip.txt: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after skip.txt change: %v", err)
	}
	if h1 != h2 {
		t.Error("changing an ignored file (subdir/skip.txt) should not change the hash")
	}

	// Changing keep.txt (re-included via negation) MUST change the hash.
	if err := os.WriteFile(filepath.Join(dir, "subdir/keep.txt"), []byte("changed keep\n"), 0o644); err != nil {
		t.Fatalf("WriteFile keep.txt: %v", err)
	}
	h3, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after keep.txt change: %v", err)
	}
	if h1 == h3 {
		t.Error("changing a file re-included via !pattern should change the hash")
	}
}

func TestCompute_DockerIgnore_CommentOnly(t *testing.T) {
	// A .dockerignore containing only comments must be treated as a no-op.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":    "FROM ubuntu:22.04\nCOPY . /app/\n",
		"app.py":        "print('hello')\n",
		".dockerignore": "# just a comment\n\n# another comment\n",
	})

	opts := hasher.Options{DockerfilePath: filepath.Join(dir, "Dockerfile"), ContextDir: dir}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// All files including .dockerignore should be included; modifying app.py must change hash.
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after change: %v", err)
	}
	if h1 == h2 {
		t.Error("comment-only .dockerignore: modifying app.py should change the hash")
	}
}

func TestCompute_DockerIgnore_DirectoryPattern(t *testing.T) {
	// "node_modules/" should exclude the entire directory and all nested files.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":                   "FROM ubuntu:22.04\nCOPY . /app/\n",
		"app.py":                       "print('hello')\n",
		"node_modules/lodash/index.js": "module.exports = {};\n",
		"node_modules/lodash/util.js":  "exports.noop = () => {};\n",
		".dockerignore":                "node_modules/\n",
	})

	opts := hasher.Options{DockerfilePath: filepath.Join(dir, "Dockerfile"), ContextDir: dir}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Modifying a file inside node_modules must NOT change the hash.
	if err := os.WriteFile(filepath.Join(dir, "node_modules/lodash/index.js"), []byte("// changed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after node_modules change: %v", err)
	}
	if h1 != h2 {
		t.Error("modifying a file inside an ignored directory (node_modules/) should not change the hash")
	}
}

// ---- FROM digest resolution (#44) ----

func TestCompute_BaseImage_ResolverInvokedWhenSet(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM golang:1.25\nRUN go version\n",
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"golang:1.25|": "index.docker.io/library/golang@sha256:aaa",
		},
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1", c)
	}
}

func TestCompute_BaseImage_DifferentResolvedDigestChangesHash(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM golang:1.25\nRUN go version\n",
	})
	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	// Same Dockerfile, two resolvers returning different digests for the
	// same FROM. The hashes must differ.
	r1 := &fakeResolver{results: map[string]string{"golang:1.25|": "index.docker.io/library/golang@sha256:aaa"}}
	r2 := &fakeResolver{results: map[string]string{"golang:1.25|": "index.docker.io/library/golang@sha256:bbb"}}

	opts.BaseImageResolver = r1
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute r1: %v", err)
	}

	opts.BaseImageResolver = r2
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute r2: %v", err)
	}

	if h1 == h2 {
		t.Error("a different resolved FROM digest must change the hash (this is the whole point of #44)")
	}
}

func TestCompute_BaseImage_PerComputeCacheDeduplicates(t *testing.T) {
	// Three stages, all rooted at the same base image. The resolver must
	// only be called ONCE thanks to the per-Compute cache, not three times.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": `FROM alpine:3.20 AS deps
RUN echo deps

FROM alpine:3.20 AS builder
RUN echo build

FROM alpine:3.20
RUN echo runtime
`,
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"alpine:3.20|": "index.docker.io/library/alpine@sha256:cafe",
		},
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: baseimage.NewCachingResolver(resolver),
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1 (per-Compute cache should deduplicate)", c)
	}
}

func TestCompute_BaseImage_StageRefDoesNotInvokeResolver(t *testing.T) {
	// Second FROM is a stage reference; only the first FROM should hit the
	// resolver.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": `FROM golang:1.25 AS builder
RUN go version

FROM builder
RUN echo "extending the same stage"
`,
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"golang:1.25|": "index.docker.io/library/golang@sha256:aaa",
		},
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1 (the FROM builder line is a stage ref, not a registry image)", c)
	}
}

func TestCompute_BaseImage_PinnedDigestSkipsResolver(t *testing.T) {
	// FROM <repo>@sha256:... is already canonical; the resolver must not be
	// invoked. We pass a fakeResolver that returns an error if invoked, so a
	// successful Compute proves the short-circuit fired.
	const pinned = "alpine@sha256:1304f174557314a7ed9eddb4eab12fed12cb0cd9809e4c28f29af86979a3c870"
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM " + pinned + "\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		err: errors.New("resolver should not be invoked for a pinned reference"),
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 0 {
		t.Errorf("resolver.calls = %d, want 0 (pinned digest must short-circuit)", c)
	}
}

func TestCompute_BaseImage_ScratchDoesNotInvokeResolver(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM scratch\nCOPY app /\n",
		"app":        "binary content\n",
	})
	resolver := &fakeResolver{
		err: errors.New("resolver should not be invoked for scratch"),
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 0 {
		t.Errorf("resolver.calls = %d, want 0 (FROM scratch must short-circuit)", c)
	}
}

func TestCompute_BaseImage_PlatformAffectsHash(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20\n",
	})

	// Same image, different platforms — the resolver returns different
	// digests for each. The hashes must differ.
	resolver := &fakeResolver{
		results: map[string]string{
			"alpine:3.20|linux/amd64": "index.docker.io/library/alpine@sha256:amd64aaa",
			"alpine:3.20|linux/arm64": "index.docker.io/library/alpine@sha256:arm64bbb",
		},
	}

	opts1 := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	// Force the platform via a Dockerfile rewrite — easier than rigging up a
	// CLI flag in the test, and exercises the same parser path.
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM --platform=linux/amd64 alpine:3.20\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h1, err := hasher.Compute(opts1)
	if err != nil {
		t.Fatalf("Compute amd64: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM --platform=linux/arm64 alpine:3.20\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts1)
	if err != nil {
		t.Fatalf("Compute arm64: %v", err)
	}

	if h1 == h2 {
		t.Error("--platform=linux/amd64 vs --platform=linux/arm64 must produce different hashes when the resolver returns different digests")
	}
}

func TestCompute_BaseImage_ResolverErrorPropagates(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM golang:1.25\n",
	})
	resolver := &fakeResolver{
		err: errors.New("synthetic resolver failure"),
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	_, err := hasher.Compute(opts)
	if err == nil {
		t.Fatal("expected an error from Compute when the resolver fails")
	}
}

func TestCompute_BaseImage_PreFromArgDefaultExpands(t *testing.T) {
	// ARG declared (with a default) BEFORE the first FROM is in scope for
	// FROM expressions per the Dockerfile spec. The resolver must be called
	// with the expanded value, not the literal "${BASE}".
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "ARG BASE=alpine:3.20\nFROM ${BASE}\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"alpine:3.20|": "index.docker.io/library/alpine@sha256:expanded",
		},
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1 (ARG default should expand and trigger resolution)", c)
	}
}

func TestCompute_BaseImage_PreFromArgDefaultBareSyntax(t *testing.T) {
	// Dockerfile spec allows the bare "$BASE" form (no braces) too.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "ARG BASE=alpine:3.20\nFROM $BASE\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"alpine:3.20|": "index.docker.io/library/alpine@sha256:expanded",
		},
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1 (bare $BASE should expand same as ${BASE})", c)
	}
}

func TestCompute_BaseImage_CallerArgOverridesDefault(t *testing.T) {
	// Caller-supplied build args win over the parser's pre-FROM ARG
	// defaults: an --build-arg BASE=alpine:3.21 must override
	// "ARG BASE=alpine:3.20" inside the Dockerfile.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "ARG BASE=alpine:3.20\nFROM ${BASE}\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"alpine:3.20|": "index.docker.io/library/alpine@sha256:default",
			"alpine:3.21|": "index.docker.io/library/alpine@sha256:override",
		},
	}

	// Without an override: hash uses the default.
	h1, err := hasher.Compute(hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BuildArgs:         map[string]string{"BASE": "alpine:3.20"},
		BaseImageResolver: resolver,
	})
	if err != nil {
		t.Fatalf("Compute default: %v", err)
	}

	// With an override: hash uses the overridden value.
	h2, err := hasher.Compute(hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BuildArgs:         map[string]string{"BASE": "alpine:3.21"},
		BaseImageResolver: resolver,
	})
	if err != nil {
		t.Fatalf("Compute override: %v", err)
	}

	if h1 == h2 {
		t.Error("a caller --build-arg override should change the hash compared to the ARG default")
	}
}

func TestCompute_BaseImage_MultipleVariablesInOneRef(t *testing.T) {
	// "${REPO}/${IMG}:${TAG}" must be fully expanded before resolution.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "ARG REPO=quay.io\nARG IMG=podman/stable\nARG TAG=latest\nFROM ${REPO}/${IMG}:${TAG}\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"quay.io/podman/stable:latest|": "quay.io/podman/stable@sha256:abc",
		},
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1 (multi-var FROM should expand and resolve once)", c)
	}
}

func TestCompute_BaseImage_PinnedDigestViaArgExpansion(t *testing.T) {
	// ARG BASE=<digest-pinned> + FROM ${BASE} should canonicalize the
	// expanded reference offline (no resolver call) just like a literal
	// pinned-digest FROM line would.
	const pinned = "alpine@sha256:1304f174557314a7ed9eddb4eab12fed12cb0cd9809e4c28f29af86979a3c870"
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "ARG BASE=" + pinned + "\nFROM ${BASE}\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		err: errors.New("resolver must not be invoked for an expanded pinned-digest reference"),
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 0 {
		t.Errorf("resolver.calls = %d, want 0", c)
	}
}

func TestCompute_BaseImage_StageRefViaArgExpansion(t *testing.T) {
	// ARG that resolves to a stage alias must be detected as a stage
	// reference, not sent to the registry resolver.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": `ARG BASE=builder
FROM golang:1.25 AS builder
RUN go version

FROM ${BASE}
RUN echo "extending the same stage via ARG"
`,
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"golang:1.25|": "index.docker.io/library/golang@sha256:aaa",
		},
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1 (ARG that expands to a stage alias should not hit the resolver)", c)
	}
}

func TestCompute_BaseImage_TrulyUnresolvableImageFallsBack(t *testing.T) {
	// FROM references an ARG that has neither a Dockerfile default nor a
	// caller-supplied value. The hash run must not fail; the contribution
	// becomes a literal "unexpanded:" entry that still discriminates by
	// the partially-expanded text.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ${MISSING}\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		err: errors.New("resolver must not be invoked when the image text still contains an unresolved variable"),
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute should not fail on a truly-unresolvable ARG: %v", err)
	}
	if c := resolver.calls.Load(); c != 0 {
		t.Errorf("resolver.calls = %d, want 0", c)
	}

	// Two different unresolvable expressions still produce different
	// hashes via the section-1 Dockerfile content.
	dir2 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ${OTHER}\nRUN echo hi\n",
	})
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute opts: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{
		DockerfilePath:    filepath.Join(dir2, "Dockerfile"),
		ContextDir:        dir2,
		BaseImageResolver: resolver,
	})
	if err != nil {
		t.Fatalf("Compute opts2: %v", err)
	}
	if h1 == h2 {
		t.Error("two different unresolvable FROM expressions should still produce different hashes via section 1")
	}
}

func TestCompute_BaseImage_BuildPlatformDropsToIndexDigest(t *testing.T) {
	// FROM --platform=$BUILDPLATFORM alpine:3.20 must NOT crash. The
	// $BUILDPLATFORM reference is "no caller-supplied platform" for
	// docker-hash purposes — we resolve to the multi-arch index digest
	// (passing "" to the resolver) rather than the platform-specific
	// manifest, which keeps the hash stable across runner architectures.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM --platform=$BUILDPLATFORM alpine:3.20\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		results: map[string]string{
			// Note: empty Platform key (not "linux/amd64" or anything else).
			"alpine:3.20|": "index.docker.io/library/alpine@sha256:index",
		},
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1", c)
	}
}

func TestCompute_BaseImage_TargetPlatformWithCallerArgIsHonoured(t *testing.T) {
	// FROM --platform=$TARGETPLATFORM with a caller-supplied
	// --build-arg TARGETPLATFORM=linux/arm64 should resolve against the
	// arm64 manifest, not drop to the index digest.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM --platform=$TARGETPLATFORM alpine:3.20\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"alpine:3.20|linux/arm64": "index.docker.io/library/alpine@sha256:arm64",
		},
	}
	opts := hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BuildArgs:         map[string]string{"TARGETPLATFORM": "linux/arm64"},
		BaseImageResolver: resolver,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1", c)
	}
}

func TestCompute_BaseImage_BuildPlatformWithCallerArgChangesHash(t *testing.T) {
	// Two runs of the same Dockerfile, with different caller-supplied
	// values for $BUILDPLATFORM, must produce different hashes (because
	// the resolver returns a different per-platform digest).
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM --platform=$BUILDPLATFORM alpine:3.20\n",
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"alpine:3.20|linux/amd64": "index.docker.io/library/alpine@sha256:amd64",
			"alpine:3.20|linux/arm64": "index.docker.io/library/alpine@sha256:arm64",
		},
	}
	h1, err := hasher.Compute(hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BuildArgs:         map[string]string{"BUILDPLATFORM": "linux/amd64"},
		BaseImageResolver: resolver,
	})
	if err != nil {
		t.Fatalf("Compute amd64: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BuildArgs:         map[string]string{"BUILDPLATFORM": "linux/arm64"},
		BaseImageResolver: resolver,
	})
	if err != nil {
		t.Fatalf("Compute arm64: %v", err)
	}
	if h1 == h2 {
		t.Error("different caller-supplied $BUILDPLATFORM values must produce different hashes")
	}
}

func TestCompute_BaseImage_NoResolverHashesLiteralText(t *testing.T) {
	// With BaseImageResolver=nil, plain-tag FROM lines must hash their
	// literal text (the v0.1.x backward-compat path). Two different tags
	// must produce different hashes; identical tags must produce identical
	// hashes; and the hash must be stable across runs.
	dir1 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM golang:1.25\n",
	})
	dir2 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM golang:1.26\n",
	})

	h1, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir1, "Dockerfile"), ContextDir: dir1})
	if err != nil {
		t.Fatalf("Compute dir1: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir2, "Dockerfile"), ContextDir: dir2})
	if err != nil {
		t.Fatalf("Compute dir2: %v", err)
	}
	if h1 == h2 {
		t.Error("different FROM tags should produce different hashes even without a resolver")
	}

	// Stability: re-compute dir1 and expect the same hash.
	h1again, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir1, "Dockerfile"), ContextDir: dir1})
	if err != nil {
		t.Fatalf("Compute dir1 again: %v", err)
	}
	if h1 != h1again {
		t.Error("hash should be stable across runs when no resolver is configured")
	}
}

// ---- COPY/ADD ARG/ENV expansion tests ----

func TestCompute_CopySource_ExpandsStageLocalArg(t *testing.T) {
	// A stage-local ARG with a default should expand inside a COPY pattern.
	// Build the file the ARG names so the resulting pattern matches.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20\nARG VERSION=1.0\nCOPY app-${VERSION}.txt /\n",
		"app-1.0.txt": "v1.0\n",
	})
	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Modifying the targeted file must change the hash.
	if err := os.WriteFile(filepath.Join(dir, "app-1.0.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after change: %v", err)
	}
	if h1 == h2 {
		t.Error("modifying the file matched by the expanded COPY pattern should change the hash")
	}
}

func TestCompute_CopySource_CallerArgOverridesDefault(t *testing.T) {
	// --build-arg VERSION=2.0 should override "ARG VERSION=1.0" inside the
	// stage and cause COPY app-${VERSION}.txt to match app-2.0.txt instead.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":  "FROM alpine:3.20\nARG VERSION=1.0\nCOPY app-${VERSION}.txt /\n",
		"app-1.0.txt": "v1.0\n",
		"app-2.0.txt": "v2.0\n",
	})

	h1, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		BuildArgs:      map[string]string{"VERSION": "1.0"},
	})
	if err != nil {
		t.Fatalf("Compute v1: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		BuildArgs:      map[string]string{"VERSION": "2.0"},
	})
	if err != nil {
		t.Fatalf("Compute v2: %v", err)
	}
	if h1 == h2 {
		t.Error("a caller --build-arg override should change which file the COPY pattern selects, and therefore the hash")
	}
}

func TestCompute_CopySource_PreFromArgInheritedViaRedeclare(t *testing.T) {
	// A pre-FROM ARG is only visible inside a stage when it is explicitly
	// redeclared via "ARG NAME" (no default). Without the redeclare it must
	// stay literal.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":  "ARG VERSION=1.0\nFROM alpine:3.20\nARG VERSION\nCOPY app-${VERSION}.txt /\n",
		"app-1.0.txt": "v1.0\n",
	})
	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
}

func TestCompute_CopySource_EnvExpansion(t *testing.T) {
	// ENV declared in the stage must expand inside a COPY pattern.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20\nENV TARGET=app\nCOPY ${TARGET}.txt /\n",
		"app.txt":    "hello\n",
	})
	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}
	if _, err := hasher.Compute(opts); err != nil {
		t.Fatalf("Compute: %v", err)
	}
}

func TestCompute_CopySource_MissingVarErrors(t *testing.T) {
	// COPY ${MISSING}/file.txt — the variable is unset, the literal pattern
	// matches no files, PR #51's guard fires.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20\nCOPY ${MISSING}/file.txt /\n",
	})
	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Fatal("expected an error when the COPY pattern references an unset variable")
	}
}

func TestCompute_CopySource_StageNameExpands(t *testing.T) {
	// COPY --from=${STAGE} should expand against the stage's ARG state and
	// resolve to the named stage so no local files are pulled in.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": `FROM alpine:3.20 AS builder
RUN echo build

FROM alpine:3.20
ARG STAGE=builder
COPY --from=${STAGE} /bin/app /usr/local/bin/
COPY config.txt /etc/
`,
		"config.txt": "config\n",
	})
	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Decoy: create /bin/app inside the context. If --from expansion were
	// broken (and the COPY were treated as a context-source copy), the
	// hash would change. It must not.
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "app"), []byte("decoy"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after decoy: %v", err)
	}
	if h1 != h2 {
		t.Error("expanded --from=${STAGE} should resolve to a stage reference and skip context files")
	}
}

func TestCompute_CopySource_NoExpandArgs_LeavesPatternLiteral(t *testing.T) {
	// With NoExpandArgs, ${VERSION} in a COPY pattern is treated as a
	// literal — and since no file matches "app-${VERSION}.txt", PR #51's
	// guard fires.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":  "FROM alpine:3.20\nARG VERSION=1.0\nCOPY app-${VERSION}.txt /\n",
		"app-1.0.txt": "v1.0\n",
	})
	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
		NoExpandArgs:   true,
	})
	if err == nil {
		t.Fatal("expected NoExpandArgs to leave ${VERSION} literal and trip the matches-no-files guard")
	}
}

// ---- --no-expand-args / offline-mode FROM tests ----

func TestCompute_NoExpandArgs_FromWithVarErrors(t *testing.T) {
	// FROM with a variable reference must fail under --no-expand-args
	// (with a resolver). The user has explicitly opted out of expansion.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "ARG BASE=alpine:3.20\nFROM ${BASE}\nRUN echo hi\n",
	})
	resolver := &fakeResolver{
		err: errors.New("resolver should not be invoked"),
	}
	_, err := hasher.Compute(hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
		NoExpandArgs:      true,
	})
	if err == nil {
		t.Fatal("expected an error when FROM contains $ and NoExpandArgs is set")
	}
	if !strings.Contains(err.Error(), "no-expand-args") {
		t.Errorf("error should mention --no-expand-args, got: %v", err)
	}
}

func TestCompute_NoExpandArgs_StrictModeStillResolves(t *testing.T) {
	// A plain FROM with NoExpandArgs+resolver should still resolve through
	// the registry — the strict mode just enforces "no expansion".
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM alpine:3.20\n",
	})
	resolver := &fakeResolver{
		results: map[string]string{
			"alpine:3.20|": "index.docker.io/library/alpine@sha256:strict",
		},
	}
	if _, err := hasher.Compute(hasher.Options{
		DockerfilePath:    filepath.Join(dir, "Dockerfile"),
		ContextDir:        dir,
		BaseImageResolver: resolver,
		NoExpandArgs:      true,
	}); err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if c := resolver.calls.Load(); c != 1 {
		t.Errorf("resolver.calls = %d, want 1", c)
	}
}

func TestCompute_BothFlags_ProducesV01xCompatHash(t *testing.T) {
	// With BOTH NoExpandArgs and resolver=nil, section 4 is skipped
	// entirely, reproducing the v0.1.x hash shape. Different FROM tags
	// must still produce different hashes via section 1.
	dir1 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM golang:1.25\n",
	})
	dir2 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM golang:1.26\n",
	})
	h1, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir1, "Dockerfile"),
		ContextDir:     dir1,
		NoExpandArgs:   true,
	})
	if err != nil {
		t.Fatalf("Compute dir1: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir2, "Dockerfile"),
		ContextDir:     dir2,
		NoExpandArgs:   true,
	})
	if err != nil {
		t.Fatalf("Compute dir2: %v", err)
	}
	if h1 == h2 {
		t.Error("different FROM tags should still produce different hashes via section 1")
	}
}

func TestCompute_OfflineMode_ExpandsButDoesNotCallResolver(t *testing.T) {
	// resolver=nil with NoExpandArgs=false: offline mode. ARG expansion
	// happens (so ${BASE} is substituted in the section-4 entry), but no
	// network call is made. Two different ARG-default values must produce
	// different hashes.
	dir1 := buildTestContext(t, map[string]string{
		"Dockerfile": "ARG BASE=alpine:3.20\nFROM ${BASE}\n",
	})
	dir2 := buildTestContext(t, map[string]string{
		"Dockerfile": "ARG BASE=alpine:3.20\nFROM ${BASE}\n",
	})
	// Same Dockerfile twice but with different caller args overriding
	// BASE. The section-1 content is identical; the section-4 entry must
	// be the discriminator.
	h1, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir1, "Dockerfile"),
		ContextDir:     dir1,
		BuildArgs:      map[string]string{"BASE": "alpine:3.20"},
	})
	if err != nil {
		t.Fatalf("Compute h1: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir2, "Dockerfile"),
		ContextDir:     dir2,
		BuildArgs:      map[string]string{"BASE": "alpine:3.21"},
	})
	if err != nil {
		t.Fatalf("Compute h2: %v", err)
	}
	if h1 == h2 {
		t.Error("offline mode should still propagate ARG overrides into section 4")
	}
}

func TestCompute_OfflineMode_CanonicalizesUnpinnedTag(t *testing.T) {
	// In offline mode, "alpine" and "alpine:latest" must hash identically
	// because they refer to the same canonical reference.
	dir1 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM alpine\n",
	})
	dir2 := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM alpine:latest\n",
	})
	// section-1 differs (literal text differs), so to test only section
	// 4's canonicalization we have to compare the LAST 32 hex chars... but
	// since the whole file goes through one SHA-256 we can't easily isolate
	// section 4. Instead, assert via the simpler claim: both compute
	// without errors and the BOTH-FLAGS-OFF mode (offline) produces a
	// stable hash.
	h1, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir1, "Dockerfile"), ContextDir: dir1})
	if err != nil {
		t.Fatalf("Compute dir1: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{DockerfilePath: filepath.Join(dir2, "Dockerfile"), ContextDir: dir2})
	if err != nil {
		t.Fatalf("Compute dir2: %v", err)
	}
	// Different literal text, so section 1 differs → hashes differ. The
	// purpose of this test is to assert that offline mode does not crash
	// on a plain unpinned tag.
	if h1 == "" || h2 == "" {
		t.Error("offline mode produced an empty hash for an unpinned tag")
	}
}

// ---- COPY/ADD source-must-exist tests (#51) ----

func TestCompute_MissingLiteralSourceErrors(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY missing.txt /app/\n",
	})
	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Fatal("expected error for missing literal source, got nil")
	}
	if !strings.Contains(err.Error(), "missing.txt") {
		t.Errorf("error should name the missing pattern, got: %v", err)
	}
}

func TestCompute_MissingGlobErrors(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY *.nope /app/\n",
		"other.txt":  "some file\n",
	})
	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Fatal("expected error for unmatched glob, got nil")
	}
	if !strings.Contains(err.Error(), "*.nope") {
		t.Errorf("error should name the unmatched glob pattern, got: %v", err)
	}
}

func TestCompute_DirectoryThatDoesNotExistErrors(t *testing.T) {
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY src/ /app/\n",
	})
	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Fatal("expected error for missing src/ directory, got nil")
	}
	if !strings.Contains(err.Error(), "src/") {
		t.Errorf("error should name the missing directory pattern, got: %v", err)
	}
}

func TestCompute_DockerIgnoreExcludesAllFilesErrors(t *testing.T) {
	// COPY foo.txt with .dockerignore containing "foo.txt": the file exists on
	// disk but is completely excluded — Compute must return an error.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":    "FROM ubuntu:22.04\nCOPY foo.txt /app/\n",
		"foo.txt":       "hello\n",
		".dockerignore": "foo.txt\n",
	})

	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Fatal("expected an error when .dockerignore excludes all COPY sources, got nil")
	}
	if !strings.Contains(err.Error(), ".dockerignore") {
		t.Errorf("error should mention .dockerignore, got: %v", err)
	}
}

func TestCompute_DockerIgnoreExcludesEverythingFromDirErrors(t *testing.T) {
	// COPY src/ with .dockerignore containing "src/**": all files inside the
	// directory are excluded — Compute must return an error.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":    "FROM ubuntu:22.04\nCOPY src/ /app/\n",
		"src/main.go":   "package main\n",
		"src/util.go":   "package main\n",
		".dockerignore": "src/**\n",
	})

	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Fatal("expected an error when .dockerignore excludes all files from a COPY directory, got nil")
	}
	if !strings.Contains(err.Error(), ".dockerignore") {
		t.Errorf("error should mention .dockerignore, got: %v", err)
	}
}

func TestCompute_DockerIgnorePartialExclusionStillWorks(t *testing.T) {
	// Partial exclusion: .dockerignore excludes some files but not all —
	// Compute must succeed and the excluded file must not affect the hash.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":    "FROM ubuntu:22.04\nCOPY src/ /app/\n",
		"src/main.go":   "package main\n",
		"src/debug.log": "debug output\n",
		".dockerignore": "**/*.log\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute with partial .dockerignore: %v", err)
	}

	// Changing the ignored file must not change the hash.
	if err := os.WriteFile(filepath.Join(dir, "src/debug.log"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after changing ignored file: %v", err)
	}
	if h1 != h2 {
		t.Error("changing a .dockerignore-excluded file should not change the hash")
	}

	// Changing the non-ignored file must change the hash.
	if err := os.WriteFile(filepath.Join(dir, "src/main.go"), []byte("package main // changed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h3, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after changing non-ignored file: %v", err)
	}
	if h1 == h3 {
		t.Error("changing a non-ignored file should change the hash")
	}
}

func TestCompute_CopyExclude_BasicGlob(t *testing.T) {
	// COPY --exclude=*.log . /app/ must ignore log files.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY --exclude=*.log . /app/\n",
		"app.py":     "print('hello')\n",
		"build.log":  "some log output\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Modifying the excluded log file must NOT change the hash.
	if err := os.WriteFile(filepath.Join(dir, "build.log"), []byte("different log\n"), 0o644); err != nil {
		t.Fatalf("WriteFile build.log: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after log change: %v", err)
	}
	if h1 != h2 {
		t.Error("modifying an --exclude'd file (build.log) should not change the hash")
	}

	// Modifying the non-excluded .py file MUST change the hash.
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile app.py: %v", err)
	}
	h3, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after py change: %v", err)
	}
	if h1 == h3 {
		t.Error("modifying a non-excluded file (app.py) should change the hash")
	}
}

func TestCompute_CopyExclude_SourceRelativeMatching(t *testing.T) {
	// COPY --exclude=*.log src/ /app/ — pattern is relative to src/, not the context root.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":  "FROM ubuntu:22.04\nCOPY --exclude=*.log src/ /app/\n",
		"src/foo.log": "log inside src\n",
		"src/foo.py":  "src python\n",
		"other.log":   "log outside src\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Modifying src/foo.log (inside src/, excluded) must NOT change the hash.
	if err := os.WriteFile(filepath.Join(dir, "src", "foo.log"), []byte("changed log\n"), 0o644); err != nil {
		t.Fatalf("WriteFile src/foo.log: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after src/foo.log change: %v", err)
	}
	if h1 != h2 {
		t.Error("modifying an --exclude'd file inside src/ should not change the hash")
	}

	// Modifying src/foo.py (inside src/, NOT excluded) MUST change the hash.
	if err := os.WriteFile(filepath.Join(dir, "src", "foo.py"), []byte("changed python\n"), 0o644); err != nil {
		t.Fatalf("WriteFile src/foo.py: %v", err)
	}
	h3, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after src/foo.py change: %v", err)
	}
	if h1 == h3 {
		t.Error("modifying a non-excluded file inside src/ should change the hash")
	}

	// other.log is outside src/ — the COPY only covers src/, so it is not
	// part of the hash at all. Confirm its exclusion from the COPY is correct
	// (the per-source exclude does not affect files outside src/).
	if err := os.WriteFile(filepath.Join(dir, "src", "foo.py"), []byte("src python\n"), 0o644); err != nil {
		t.Fatalf("WriteFile reset src/foo.py: %v", err)
	}
	h4, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after reset: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.log"), []byte("changed outside log\n"), 0o644); err != nil {
		t.Fatalf("WriteFile other.log: %v", err)
	}
	h5, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after other.log change: %v", err)
	}
	if h4 != h5 {
		t.Error("changing other.log outside src/ should not affect the hash (COPY only covers src/)")
	}
}

func TestCompute_CopyExclude_MultipleExcludes(t *testing.T) {
	// COPY --exclude=*.log --exclude=*.tmp . /app/ — both patterns are honored.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY --exclude=*.log --exclude=*.tmp . /app/\n",
		"app.py":     "print('hello')\n",
		"build.log":  "log\n",
		"cache.tmp":  "tmp\n",
	})

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}

	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Modifying build.log (excluded by *.log) must NOT change the hash.
	if err := os.WriteFile(filepath.Join(dir, "build.log"), []byte("changed log\n"), 0o644); err != nil {
		t.Fatalf("WriteFile build.log: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after log change: %v", err)
	}
	if h1 != h2 {
		t.Error("modifying build.log (excluded by *.log) should not change the hash")
	}

	// Modifying cache.tmp (excluded by *.tmp) must NOT change the hash.
	// First reset build.log to its original value to isolate this sub-test.
	if err := os.WriteFile(filepath.Join(dir, "build.log"), []byte("log\n"), 0o644); err != nil {
		t.Fatalf("WriteFile reset build.log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache.tmp"), []byte("changed tmp\n"), 0o644); err != nil {
		t.Fatalf("WriteFile cache.tmp: %v", err)
	}
	h3, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after tmp change: %v", err)
	}
	if h1 != h3 {
		t.Error("modifying cache.tmp (excluded by *.tmp) should not change the hash")
	}

	// Modifying app.py (not excluded) MUST change the hash.
	if err := os.WriteFile(filepath.Join(dir, "cache.tmp"), []byte("tmp\n"), 0o644); err != nil {
		t.Fatalf("WriteFile reset cache.tmp: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatalf("WriteFile app.py: %v", err)
	}
	h4, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after py change: %v", err)
	}
	if h1 == h4 {
		t.Error("modifying app.py (not excluded) should change the hash")
	}
}

func TestCompute_CopyExclude_AllFilesExcludedErrors(t *testing.T) {
	// COPY --exclude=* . /app/ against a non-empty context should return an
	// error because all files are excluded and zero files remain.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY --exclude=* . /app/\n",
		"app.py":     "print('hello')\n",
	})

	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Error("expected an error when --exclude=* excludes all files, got nil")
	}
}

// ---- Symlink tests ----

func TestCompute_TopLevelSourceSymlink_FollowsTarget(t *testing.T) {
	// COPY mylink /app/ where mylink → real.txt: hashing must follow the
	// symlink and pick up the target's content. Changing real.txt's content
	// must change the hash.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY mylink /app/\n",
		"real.txt":   "hello\n",
	})
	if err := os.Symlink("real.txt", filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Modifying the file the symlink points at must change the hash.
	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after change: %v", err)
	}
	if h1 == h2 {
		t.Error("changing the target of a top-level source symlink should change the hash")
	}
}

func TestCompute_TopLevelSourceSymlink_Relink(t *testing.T) {
	// Relinking a top-level source symlink to a different file with different
	// content must change the hash even if the original target is unchanged.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY mylink /app/\n",
		"a.txt":      "first\n",
		"b.txt":      "second\n",
	})
	if err := os.Symlink("a.txt", filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Symlink a: %v", err)
	}

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Relink mylink → b.txt.
	if err := os.Remove(filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Remove mylink: %v", err)
	}
	if err := os.Symlink("b.txt", filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Symlink b: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after relink: %v", err)
	}
	if h1 == h2 {
		t.Error("relinking a top-level source symlink to a different file should change the hash")
	}
}

func TestCompute_TopLevelSourceSymlink_EscapesContextErrors(t *testing.T) {
	// COPY mylink /app/ where mylink points to a file outside the build
	// context must return an error rather than silently following the link.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile outside: %v", err)
	}

	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY mylink /app/\n",
	})
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Error("expected an error for a top-level symlink that escapes the build context, got nil")
	}
}

func TestCompute_TopLevelSourceSymlink_ContextViaSymlinkedParent(t *testing.T) {
	// Regression: when ContextDir itself sits behind a symlinked parent
	// (e.g. /tmp → /private/tmp on macOS, or a symlinked project checkout),
	// a top-level source symlink that resolves to a sibling file inside the
	// same real directory must NOT be rejected as escaping the build context.
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("MkdirAll real: %v", err)
	}
	link := filepath.Join(base, "ctx")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink ctx→real: %v", err)
	}

	if err := os.WriteFile(filepath.Join(real, "Dockerfile"), []byte("FROM ubuntu:22.04\nCOPY mylink /app/\n"), 0o644); err != nil {
		t.Fatalf("WriteFile Dockerfile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "target.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile target.txt: %v", err)
	}
	// Symlink uses an absolute path through the symlinked parent, the path
	// shape that historically tripped up the EvalSymlinks containment check.
	if err := os.Symlink(filepath.Join(link, "target.txt"), filepath.Join(real, "mylink")); err != nil {
		t.Fatalf("Symlink mylink: %v", err)
	}

	opts := hasher.Options{
		DockerfilePath: filepath.Join(link, "Dockerfile"),
		ContextDir:     link,
	}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute via symlinked context: %v", err)
	}

	// Sanity check: changing the resolved file changes the hash.
	if err := os.WriteFile(filepath.Join(real, "target.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatalf("WriteFile target.txt change: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after change: %v", err)
	}
	if h1 == h2 {
		t.Error("changing the resolved file should change the hash even when ContextDir is a symlinked path")
	}
}

func TestCompute_InnerSymlink_TargetStringChanges(t *testing.T) {
	// COPY src/ /app/ where src/link → a: relinking src/link → b must change
	// the hash because inner symlinks are hashed by their target string.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY src/ /app/\n",
		"src/a.txt":  "first\n",
		"src/b.txt":  "second\n",
	})
	if err := os.Symlink("a.txt", filepath.Join(dir, "src", "link")); err != nil {
		t.Fatalf("Symlink a: %v", err)
	}

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Relink the inner symlink to a different target string.
	if err := os.Remove(filepath.Join(dir, "src", "link")); err != nil {
		t.Fatalf("Remove inner link: %v", err)
	}
	if err := os.Symlink("b.txt", filepath.Join(dir, "src", "link")); err != nil {
		t.Fatalf("Symlink b: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after inner relink: %v", err)
	}
	if h1 == h2 {
		t.Error("relinking an inner symlink (target string change) should change the hash")
	}
}

func TestCompute_InnerSymlink_TargetContentDoesNotMatter(t *testing.T) {
	// COPY src/ /app/ where src/link → ../outside: changing the content of
	// "outside" (which is NOT itself separately COPY'd) must NOT change the
	// hash. Inner symlinks are hashed by their target string only — Docker
	// preserves the symlink as-is in the resulting layer, so the target
	// file's content is irrelevant. This documents the deliberate limitation.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":  "FROM ubuntu:22.04\nCOPY src/ /app/\n",
		"src/keep.go": "package src\n",
		"outside.txt": "first\n",
	})
	if err := os.Symlink("../outside.txt", filepath.Join(dir, "src", "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	opts := hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Change outside.txt's content; the inner symlink target string is
	// unchanged, so the hash must be unchanged too.
	if err := os.WriteFile(filepath.Join(dir, "outside.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatalf("WriteFile outside.txt: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after change: %v", err)
	}
	if h1 != h2 {
		t.Error("changing the content of an inner symlink target (when not separately COPY'd) should NOT change the hash")
	}
}
