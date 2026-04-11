package main

import (
	"bytes"
	"regexp"
	"testing"
)

func TestPrintVersion(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf)

	got := buf.String()
	want := regexp.MustCompile(`^docker-hash \S+ \(\S+, \S+\)\n$`)
	if !want.MatchString(got) {
		t.Errorf("printVersion() = %q, want match for %v", got, want)
	}
}
