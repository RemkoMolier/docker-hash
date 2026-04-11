package hasher_test

import (
	"os"
	"path/filepath"
	"strings"
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
