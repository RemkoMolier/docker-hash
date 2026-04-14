package dockerfile_test

import (
	"bytes"
	"testing"

	"github.com/RemkoMolier/docker-hash/pkg/dockerfile"
)

// FuzzExpandVars exercises the variable-expansion logic against arbitrary
// input strings and lookup entries. ExpandVars runs on every $VAR reference
// pulled out of a Dockerfile during hash computation, so a panic here would
// be reachable from CLI input.
func FuzzExpandVars(f *testing.F) {
	seeds := []struct{ s, key, value string }{
		{"", "", ""},
		{"plain text", "VAR", "x"},
		{"$VAR", "VAR", "val"},
		{"${VAR}", "VAR", "val"},
		{"${VAR:-default}", "OTHER", ""},
		{"${VAR:+alt}", "VAR", "x"},
		{"${}", "", ""},
		{"${ SPACED }", " SPACED ", ""},
		{"$", "", ""},
		{"$$", "", ""},
		{"${OUTER:-${INNER}}", "INNER", "v"},
		{"${A}${B}", "A", "1"},
		{"${UNCLOSED", "UNCLOSED", "x"},
		{"$1digit", "1digit", "x"},
	}
	for _, s := range seeds {
		f.Add(s.s, s.key, s.value)
	}
	f.Fuzz(func(t *testing.T, s, key, value string) {
		lookup := func(name string) (string, bool) {
			if name == key {
				return value, true
			}
			return "", false
		}
		_ = dockerfile.ExpandVars(s, lookup)
	})
}

// FuzzParse feeds arbitrary byte sequences through the top-level Dockerfile
// parser. The CLI accepts user-supplied files, so any crash here is a
// reachable DoS. Malformed input is allowed to return an error, but never
// to panic.
func FuzzParse(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("FROM scratch\n"))
	f.Add([]byte(simpleDockerfile))
	f.Add([]byte(multistageDockerfile))
	f.Add([]byte("# just a comment\n"))
	f.Add([]byte("FROM a\nCOPY --from=b --exclude=*.md . /out\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = dockerfile.Parse(bytes.NewReader(data))
	})
}
