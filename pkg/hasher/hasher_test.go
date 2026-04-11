package hasher_test

import (
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

// ---- Symlink tests ----

func TestCompute_TopLevelSourceSymlink_FollowsTarget(t *testing.T) {
	// COPY mylink /app/ where mylink → real.txt: changing real.txt must change the hash.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY mylink /app/\n",
		"real.txt":   "hello\n",
	})
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	opts := hasher.Options{DockerfilePath: filepath.Join(dir, "Dockerfile"), ContextDir: dir}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "real.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after target change: %v", err)
	}
	if h1 == h2 {
		t.Error("changing the target of a top-level source symlink should change the hash")
	}
}

func TestCompute_TopLevelSourceSymlink_Relink(t *testing.T) {
	// Relinking mylink to a different file with different content must change the hash.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY mylink /app/\n",
		"target1":    "hello\n",
		"target2":    "world\n",
	})
	if err := os.Symlink(filepath.Join(dir, "target1"), filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	opts := hasher.Options{DockerfilePath: filepath.Join(dir, "Dockerfile"), ContextDir: dir}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Relink mylink → target2.
	if err := os.Remove(filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "target2"), filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after relink: %v", err)
	}
	if h1 == h2 {
		t.Error("relinking a top-level source symlink to a file with different content should change the hash")
	}
}

func TestCompute_TopLevelSourceSymlink_EscapesContextErrors(t *testing.T) {
	// COPY mylink /app/ where mylink points outside the context must return an error.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY mylink /app/\n",
	})
	// Point to a file in a completely separate temp directory (outside the context).
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "mylink")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(dir, "Dockerfile"),
		ContextDir:     dir,
	})
	if err == nil {
		t.Error("expected an error when a top-level source symlink escapes the build context, got nil")
	}
}

func TestCompute_InnerSymlink_TargetStringChanges(t *testing.T) {
	// COPY src/ /app/ where src/link → a: relinking src/link → b must change the hash.
	dir := buildTestContext(t, map[string]string{
		"Dockerfile": "FROM ubuntu:22.04\nCOPY src/ /app/\n",
		"src/a":      "file a\n",
		"src/b":      "file b\n",
	})
	if err := os.Symlink("a", filepath.Join(dir, "src", "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	opts := hasher.Options{DockerfilePath: filepath.Join(dir, "Dockerfile"), ContextDir: dir}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Relink src/link → b.
	if err := os.Remove(filepath.Join(dir, "src", "link")); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := os.Symlink("b", filepath.Join(dir, "src", "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after relink: %v", err)
	}
	if h1 == h2 {
		t.Error("relinking an inner symlink (changing the target string) should change the hash")
	}
}

func TestCompute_InnerSymlink_TargetContentDoesNotMatter(t *testing.T) {
	// COPY src/ /app/ where src/link → ../outside: changing ../outside content
	// must NOT change the hash (inner symlinks are hashed by target string only).
	dir := buildTestContext(t, map[string]string{
		"Dockerfile":   "FROM ubuntu:22.04\nCOPY src/ /app/\n",
		"src/real.txt": "unchanged\n",
		"outside":      "original outside\n",
	})
	if err := os.Symlink("../outside", filepath.Join(dir, "src", "link")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	opts := hasher.Options{DockerfilePath: filepath.Join(dir, "Dockerfile"), ContextDir: dir}
	h1, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// Modify outside — the symlink target string is unchanged, so hash must not change.
	if err := os.WriteFile(filepath.Join(dir, "outside"), []byte("changed outside\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	h2, err := hasher.Compute(opts)
	if err != nil {
		t.Fatalf("Compute after outside change: %v", err)
	}
	if h1 != h2 {
		t.Error("changing the content of a file pointed to by an inner symlink (but not separately COPY'd) should not change the hash")
	}
}

func TestCompute_TopLevel_ContextViaSymlinkedParent(t *testing.T) {
	// Regression: when ContextDir itself sits behind a symlinked parent
	// (e.g. /tmp → /private/tmp on macOS), a top-level source symlink
	// that resolves to a sibling file inside the same real directory must
	// NOT be rejected as "escapes build context".
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("MkdirAll real: %v", err)
	}
	// Create a symlink "ctx" → "real" so that ContextDir is accessed via a symlinked path.
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
	// mylink → <ctx>/target.txt (an absolute path through the symlinked parent).
	if err := os.Symlink(filepath.Join(link, "target.txt"), filepath.Join(real, "mylink")); err != nil {
		t.Fatalf("Symlink mylink: %v", err)
	}

	// Compute using the symlinked path as ContextDir.
	h1, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(link, "Dockerfile"),
		ContextDir:     link,
	})
	if err != nil {
		t.Fatalf("Compute via symlinked context: %v", err)
	}

	// Changing the target file must change the hash (sanity-check that the
	// symlink was actually followed and not rejected).
	if err := os.WriteFile(filepath.Join(real, "target.txt"), []byte("world\n"), 0o644); err != nil {
		t.Fatalf("WriteFile target.txt change: %v", err)
	}
	h2, err := hasher.Compute(hasher.Options{
		DockerfilePath: filepath.Join(link, "Dockerfile"),
		ContextDir:     link,
	})
	if err != nil {
		t.Fatalf("Compute via symlinked context after change: %v", err)
	}
	if h1 == h2 {
		t.Error("changing the target file should change the hash even when ContextDir is a symlinked path")
	}
}
