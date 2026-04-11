package hasher_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/RemkoMolier/docker-hash/pkg/hasher"
)

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
		"Dockerfile":     "FROM ubuntu:22.04\nCOPY src/ /app/\n",
		"src/main.py":    "print('main')\n",
		"src/helper.py":  "def helper(): pass\n",
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
		"Dockerfile":   "FROM ubuntu:22.04\nCOPY *.py /app/\n",
		"main.py":      "print('main')\n",
		"helper.py":    "def helper(): pass\n",
		"ignored.txt":  "not a py file\n",
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
		"Dockerfile":   "FROM ubuntu:22.04\nCOPY . /app/\n",
		"app.py":       "print('hello')\n",
		"build.log":    "some log output\n",
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
		"Dockerfile":       "FROM ubuntu:22.04\nCOPY . /app/\n",
		"subdir/skip.txt":  "skip me\n",
		"subdir/keep.txt":  "keep me\n",
		".dockerignore":    "subdir\n!subdir/keep.txt\n",
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

// --- FROM digest resolution tests ---

// mockManifest is a minimal manifest body returned by mock registry servers.
const mockManifest = `{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{"mediaType":"application/vnd.docker.container.image.v1+json","size":1,"digest":"sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},"layers":[]}`

// newMockRegistryHandler returns an http.Handler that serves minimal OCI
// manifest responses (HEAD and GET) with the given digest.
func newMockRegistryHandler(digest string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", digest)
		// Content-Length is required by go-containerregistry's headManifest.
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(mockManifest)))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			fmt.Fprint(w, mockManifest)
		}
	})
}

// hostRewriteRoundTripper redirects requests for registryHost to serverURL.
type hostRewriteRoundTripper struct {
	registryHost string
	serverURL    string
}

func (t *hostRewriteRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == t.registryHost {
		clone := req.Clone(req.Context())
		clone.URL.Scheme = "http"
		clone.URL.Host = t.serverURL[len("http://"):]
		clone.Host = clone.URL.Host
		return http.DefaultTransport.RoundTrip(clone)
	}
	return http.DefaultTransport.RoundTrip(req)
}

// TestCompute_ResolveFromDigests_DisabledByDefault verifies that without
// ResolveFromDigests the hash does not change when a FROM image would
// hypothetically change its digest (backward compat).
func TestCompute_ResolveFromDigests_DisabledByDefault(t *testing.T) {
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

	// Compute again — should be identical since digest resolution is off.
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute 2nd: %v", err)
	}
	if h1 != h2 {
		t.Error("hash must be deterministic when ResolveFromDigests is false")
	}
}

// TestCompute_ResolveFromDigests_DifferentDigestsDifferentHashes verifies that
// two separate Compute calls with the same Dockerfile but different digests
// returned by the registry produce different hashes.
func TestCompute_ResolveFromDigests_DifferentDigestsDifferentHashes(t *testing.T) {
	digestA := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	serverA := httptest.NewServer(newMockRegistryHandler(digestA))
	defer serverA.Close()

	digestB := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	serverB := httptest.NewServer(newMockRegistryHandler(digestB))
	defer serverB.Close()

	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})

	optsA := hasher.Options{
		DockerfilePath:     filepath.Join(dir, "Dockerfile"),
		ContextDir:         dir,
		ResolveFromDigests: true,
		Transport: &hostRewriteRoundTripper{
			registryHost: "index.docker.io",
			serverURL:    serverA.URL,
		},
	}
	optsB := hasher.Options{
		DockerfilePath:     filepath.Join(dir, "Dockerfile"),
		ContextDir:         dir,
		ResolveFromDigests: true,
		Transport: &hostRewriteRoundTripper{
			registryHost: "index.docker.io",
			serverURL:    serverB.URL,
		},
	}

	hA, err := hasher.Compute(optsA)
	if err != nil {
		t.Fatalf("Compute with digest A: %v", err)
	}
	hB, err := hasher.Compute(optsB)
	if err != nil {
		t.Fatalf("Compute with digest B: %v", err)
	}
	if hA == hB {
		t.Error("different FROM digests must produce different hashes")
	}
}

// TestCompute_ResolveFromDigests_MirrorInvisibleToHash verifies acceptance
// criterion 2: the hash produced via a mirror is identical to the hash
// produced when the "upstream" returns the same digest directly.
// This is the canonical mirror-invisibility test.
func TestCompute_ResolveFromDigests_MirrorInvisibleToHash(t *testing.T) {
	const digest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

	// Both "upstream" and "mirror" return the same digest.
	handler := newMockRegistryHandler(digest)
	upstreamServer := httptest.NewServer(handler)
	defer upstreamServer.Close()
	mirrorServer := httptest.NewServer(handler)
	defer mirrorServer.Close()

	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY app.py /app/\n",
		"app.py":     "print('hello')\n",
	})

	// Direct (no mirror): transport routes index.docker.io → upstreamServer.
	optsNoMirror := hasher.Options{
		DockerfilePath:     filepath.Join(dir, "Dockerfile"),
		ContextDir:         dir,
		ResolveFromDigests: true,
		Transport: &hostRewriteRoundTripper{
			registryHost: "index.docker.io",
			serverURL:    upstreamServer.URL,
		},
	}

	// With mirror: transport routes index.docker.io → mirrorServer.
	optsMirror := hasher.Options{
		DockerfilePath:     filepath.Join(dir, "Dockerfile"),
		ContextDir:         dir,
		ResolveFromDigests: true,
		Transport: &hostRewriteRoundTripper{
			registryHost: "index.docker.io",
			serverURL:    mirrorServer.URL,
		},
	}

	hDirect, err := hasher.Compute(optsNoMirror)
	if err != nil {
		t.Fatalf("Compute (direct): %v", err)
	}
	hMirror, err := hasher.Compute(optsMirror)
	if err != nil {
		t.Fatalf("Compute (mirror): %v", err)
	}

	if hDirect != hMirror {
		t.Errorf("mirror must be invisible to the hash: direct=%s mirror=%s", hDirect, hMirror)
	}
}
