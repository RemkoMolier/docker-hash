package main

import (
	"bytes"
	"testing"
)

func TestPrintVersion(t *testing.T) {
	origVersion, origCommit, origDate := version, commit, date
	version, commit, date = "v-test", "commit-test", "date-test"
	defer func() {
		version, commit, date = origVersion, origCommit, origDate
	}()

	var buf bytes.Buffer
	printVersion(&buf)

	got := buf.String()
	want := "docker-hash v-test (commit-test, date-test)\n"
	if got != want {
		t.Errorf("printVersion() = %q, want %q", got, want)
	}
}
