package dockerfile_test

import (
	"strings"
	"testing"

	"github.com/RemkoMolier/docker-hash/pkg/dockerfile"
)

const simpleDockerfile = `FROM ubuntu:22.04
ARG VERSION
ARG BUILD_DATE=unknown
COPY src/ /app/src/
ADD assets/logo.png /app/assets/
RUN apt-get update
`

const multistageDockerfile = `FROM golang:1.21 AS builder
COPY . /src
RUN go build -o /bin/app /src

FROM ubuntu:22.04
ARG APP_VERSION
COPY --from=builder /bin/app /usr/local/bin/app
COPY config/ /etc/app/
`

func TestParse_SimpleDockerfile(t *testing.T) {
	pr, err := dockerfile.Parse(strings.NewReader(simpleDockerfile))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	// Check raw content preserved.
	if string(pr.RawContent) != simpleDockerfile {
		t.Errorf("RawContent mismatch: got %q, want %q", string(pr.RawContent), simpleDockerfile)
	}

	// Check ARG names.
	wantArgs := []string{"VERSION", "BUILD_DATE"}
	if len(pr.BuildArgNames) != len(wantArgs) {
		t.Fatalf("BuildArgNames length: got %d, want %d", len(pr.BuildArgNames), len(wantArgs))
	}
	for i, name := range wantArgs {
		if pr.BuildArgNames[i] != name {
			t.Errorf("BuildArgNames[%d]: got %q, want %q", i, pr.BuildArgNames[i], name)
		}
	}

	// Check COPY/ADD sources (from build context, no --from flag).
	if len(pr.CopySources) != 2 {
		t.Fatalf("CopySources length: got %d, want 2", len(pr.CopySources))
	}
	if pr.CopySources[0].Stage != "" {
		t.Errorf("CopySources[0].Stage: expected empty, got %q", pr.CopySources[0].Stage)
	}
	if len(pr.CopySources[0].Paths) != 1 || pr.CopySources[0].Paths[0] != "src/" {
		t.Errorf("CopySources[0].Paths: got %v, want [src/]", pr.CopySources[0].Paths)
	}
	if len(pr.CopySources[1].Paths) != 1 || pr.CopySources[1].Paths[0] != "assets/logo.png" {
		t.Errorf("CopySources[1].Paths: got %v, want [assets/logo.png]", pr.CopySources[1].Paths)
	}
}

func TestParse_MultistageDockerfile(t *testing.T) {
	pr, err := dockerfile.Parse(strings.NewReader(multistageDockerfile))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}

	// Expect 3 COPY instructions total.
	if len(pr.CopySources) != 3 {
		t.Fatalf("CopySources length: got %d, want 3", len(pr.CopySources))
	}

	// First COPY ". /src" in builder stage — no --from.
	if pr.CopySources[0].Stage != "" {
		t.Errorf("CopySources[0].Stage: expected empty, got %q", pr.CopySources[0].Stage)
	}
	if len(pr.CopySources[0].Paths) != 1 || pr.CopySources[0].Paths[0] != "." {
		t.Errorf("CopySources[0].Paths: got %v, want [.]", pr.CopySources[0].Paths)
	}

	// Second COPY --from=builder /bin/app ...
	if pr.CopySources[1].Stage != "builder" {
		t.Errorf("CopySources[1].Stage: got %q, want builder", pr.CopySources[1].Stage)
	}

	// Third COPY config/ — from build context.
	if pr.CopySources[2].Stage != "" {
		t.Errorf("CopySources[2].Stage: expected empty, got %q", pr.CopySources[2].Stage)
	}
	if len(pr.CopySources[2].Paths) != 1 || pr.CopySources[2].Paths[0] != "config/" {
		t.Errorf("CopySources[2].Paths: got %v, want [config/]", pr.CopySources[2].Paths)
	}

	// ARG
	if len(pr.BuildArgNames) != 1 || pr.BuildArgNames[0] != "APP_VERSION" {
		t.Errorf("BuildArgNames: got %v, want [APP_VERSION]", pr.BuildArgNames)
	}
}

func TestParse_EmptyDockerfile(t *testing.T) {
	pr, err := dockerfile.Parse(strings.NewReader("FROM scratch\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pr.CopySources) != 0 {
		t.Errorf("expected no copy sources, got %d", len(pr.CopySources))
	}
	if len(pr.BuildArgNames) != 0 {
		t.Errorf("expected no build args, got %d", len(pr.BuildArgNames))
	}
	if len(pr.FromRefs) != 1 {
		t.Fatalf("expected one FROM ref, got %d", len(pr.FromRefs))
	}
	if pr.FromRefs[0].Image != "scratch" {
		t.Errorf("FromRefs[0].Image: got %q, want scratch", pr.FromRefs[0].Image)
	}
}

func TestParse_FromRefs_SimpleDockerfile(t *testing.T) {
	pr, err := dockerfile.Parse(strings.NewReader(simpleDockerfile))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.FromRefs) != 1 {
		t.Fatalf("FromRefs length: got %d, want 1", len(pr.FromRefs))
	}
	got := pr.FromRefs[0]
	if got.Image != "ubuntu:22.04" {
		t.Errorf("Image: got %q, want ubuntu:22.04", got.Image)
	}
	if got.Stage != "" {
		t.Errorf("Stage: got %q, want empty", got.Stage)
	}
	if got.Platform != "" {
		t.Errorf("Platform: got %q, want empty", got.Platform)
	}
	if got.IsStageRef {
		t.Errorf("IsStageRef: got true, want false")
	}
}

func TestParse_FromRefs_MultistageDockerfile(t *testing.T) {
	pr, err := dockerfile.Parse(strings.NewReader(multistageDockerfile))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.FromRefs) != 2 {
		t.Fatalf("FromRefs length: got %d, want 2", len(pr.FromRefs))
	}

	// First FROM declares the "builder" stage.
	first := pr.FromRefs[0]
	if first.Image != "golang:1.21" {
		t.Errorf("FromRefs[0].Image: got %q, want golang:1.21", first.Image)
	}
	if first.Stage != "builder" {
		t.Errorf("FromRefs[0].Stage: got %q, want builder", first.Stage)
	}
	if first.IsStageRef {
		t.Errorf("FromRefs[0].IsStageRef: got true, want false")
	}

	// Second FROM is a fresh registry image (not a stage reference).
	second := pr.FromRefs[1]
	if second.Image != "ubuntu:22.04" {
		t.Errorf("FromRefs[1].Image: got %q, want ubuntu:22.04", second.Image)
	}
	if second.Stage != "" {
		t.Errorf("FromRefs[1].Stage: got %q, want empty", second.Stage)
	}
	if second.IsStageRef {
		t.Errorf("FromRefs[1].IsStageRef: got true, want false")
	}
}

func TestParse_FromRefs_StageReference(t *testing.T) {
	// "FROM builder" on the second line is an internal stage reference, not
	// a registry image, because "builder" was declared as an alias on the
	// first FROM line.
	const src = `FROM golang:1.25 AS builder
COPY . /src
RUN go build -o /bin/app /src

FROM builder
RUN echo "extending the same stage"
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.FromRefs) != 2 {
		t.Fatalf("FromRefs length: got %d, want 2", len(pr.FromRefs))
	}

	if pr.FromRefs[0].IsStageRef {
		t.Error("FromRefs[0] (golang:1.25) should not be a stage ref")
	}
	if !pr.FromRefs[1].IsStageRef {
		t.Errorf("FromRefs[1] (builder) should be a stage ref, got IsStageRef=false; image=%q", pr.FromRefs[1].Image)
	}
	if pr.FromRefs[1].Image != "builder" {
		t.Errorf("FromRefs[1].Image: got %q, want builder", pr.FromRefs[1].Image)
	}
}

func TestParse_FromRefs_PlatformFlag(t *testing.T) {
	const src = `FROM --platform=linux/amd64 alpine:3.20
RUN echo hello
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.FromRefs) != 1 {
		t.Fatalf("FromRefs length: got %d, want 1", len(pr.FromRefs))
	}
	got := pr.FromRefs[0]
	if got.Image != "alpine:3.20" {
		t.Errorf("Image: got %q, want alpine:3.20", got.Image)
	}
	if got.Platform != "linux/amd64" {
		t.Errorf("Platform: got %q, want linux/amd64", got.Platform)
	}
}

func TestParse_FromRefs_AlreadyPinnedDigest(t *testing.T) {
	const src = `FROM alpine@sha256:1304f174557314a7ed9eddb4eab12fed12cb0cd9809e4c28f29af86979a3c870
RUN echo hello
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.FromRefs) != 1 {
		t.Fatalf("FromRefs length: got %d, want 1", len(pr.FromRefs))
	}
	got := pr.FromRefs[0]
	if !strings.HasPrefix(got.Image, "alpine@sha256:") {
		t.Errorf("Image should preserve the pinned digest, got %q", got.Image)
	}
	if got.IsStageRef {
		t.Error("a pinned-digest FROM should not be detected as a stage reference")
	}
}

func TestParse_PreFromArgDefaults(t *testing.T) {
	const src = `ARG BASE=alpine:3.20
ARG VERSION
ARG REPO=quay.io
FROM ${REPO}/${BASE}
ARG STAGE_ONLY=ignored
RUN echo hi
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	wantDefaults := map[string]string{
		"BASE": "alpine:3.20",
		"REPO": "quay.io",
	}
	if len(pr.PreFromArgDefaults) != len(wantDefaults) {
		t.Errorf("PreFromArgDefaults length: got %d, want %d (got=%v)", len(pr.PreFromArgDefaults), len(wantDefaults), pr.PreFromArgDefaults)
	}
	for k, v := range wantDefaults {
		if got, ok := pr.PreFromArgDefaults[k]; !ok {
			t.Errorf("PreFromArgDefaults[%q]: missing", k)
		} else if got != v {
			t.Errorf("PreFromArgDefaults[%q] = %q, want %q", k, got, v)
		}
	}
	// VERSION (declared without a default) is in BuildArgNames but NOT
	// in PreFromArgDefaults.
	if _, ok := pr.PreFromArgDefaults["VERSION"]; ok {
		t.Error("VERSION (no default) should not be in PreFromArgDefaults")
	}
	// STAGE_ONLY (declared after the first FROM) is also not in
	// PreFromArgDefaults — it's a stage-scoped ARG and not visible to
	// any FROM line.
	if _, ok := pr.PreFromArgDefaults["STAGE_ONLY"]; ok {
		t.Error("STAGE_ONLY (declared after the first FROM) should not be in PreFromArgDefaults")
	}
}

func TestParse_PreFromArgNames(t *testing.T) {
	// PreFromArgNames must contain every ARG declared before the first
	// FROM, with or without a default. ARGs declared inside a stage must
	// NOT appear (they live in CopySource.Scope, not here).
	const src = `ARG WITH_DEFAULT=value
ARG WITHOUT_DEFAULT
FROM alpine:3.20
ARG STAGE_LOCAL=ignored
RUN echo hi
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[string]struct{}{
		"WITH_DEFAULT":    {},
		"WITHOUT_DEFAULT": {},
	}
	if len(pr.PreFromArgNames) != len(want) {
		t.Errorf("PreFromArgNames length: got %d, want %d (got=%v)", len(pr.PreFromArgNames), len(want), pr.PreFromArgNames)
	}
	for k := range want {
		if _, ok := pr.PreFromArgNames[k]; !ok {
			t.Errorf("PreFromArgNames missing %q", k)
		}
	}
	if _, ok := pr.PreFromArgNames["STAGE_LOCAL"]; ok {
		t.Error("STAGE_LOCAL (declared after FROM) must not appear in PreFromArgNames")
	}
	// Cross-check the existing PreFromArgDefaults invariant: only the
	// ARG that supplied a default is recorded there.
	if v, ok := pr.PreFromArgDefaults["WITH_DEFAULT"]; !ok || v != "value" {
		t.Errorf("PreFromArgDefaults[WITH_DEFAULT] = %q,%v, want value,true", v, ok)
	}
	if _, ok := pr.PreFromArgDefaults["WITHOUT_DEFAULT"]; ok {
		t.Error("PreFromArgDefaults must not contain WITHOUT_DEFAULT (no default supplied)")
	}
}

func TestParse_StageAliases(t *testing.T) {
	const src = `FROM golang:1.25 AS builder
RUN go version

FROM alpine:3.20 AS runtime
COPY --from=builder /bin/app /usr/local/bin/

FROM scratch
COPY --from=runtime /usr/local/bin/app /
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := map[string]struct{}{
		"builder": {},
		"runtime": {},
	}
	if len(pr.StageAliases) != len(want) {
		t.Errorf("StageAliases length: got %d, want %d (got=%v)", len(pr.StageAliases), len(want), pr.StageAliases)
	}
	for k := range want {
		if _, ok := pr.StageAliases[k]; !ok {
			t.Errorf("StageAliases missing %q", k)
		}
	}
}

func TestExpandVars(t *testing.T) {
	defaults := map[string]string{
		"BASE":   "alpine:3.20",
		"REPO":   "quay.io",
		"VARIANT": "stable",
	}
	lookup := func(name string) (string, bool) {
		v, ok := defaults[name]
		return v, ok
	}
	cases := []struct {
		in   string
		want string
	}{
		// No-op cases.
		{"", ""},
		{"alpine:3.20", "alpine:3.20"},
		// Braced and bare forms.
		{"${BASE}", "alpine:3.20"},
		{"$BASE", "alpine:3.20"},
		{"${REPO}/library/${BASE}", "quay.io/library/alpine:3.20"},
		{"$REPO/library/$BASE", "quay.io/library/alpine:3.20"},
		// Mixed.
		{"${REPO}/podman/${VARIANT}", "quay.io/podman/stable"},
		// Unknown variables stay literal.
		{"${UNKNOWN}", "${UNKNOWN}"},
		{"$UNKNOWN", "$UNKNOWN"},
		{"${REPO}/${UNKNOWN}/${BASE}", "quay.io/${UNKNOWN}/alpine:3.20"},
		// Single-pass: expansion outputs are not re-scanned. None of our
		// values contain $, but verify the bare-form terminator behaviour.
		{"$BASE-suffix", "alpine:3.20-suffix"}, // bare $BASE terminates at "-"
		{"${BASE}suffix", "alpine:3.20suffix"}, // braced form is unambiguous
		// Names with invalid characters are left literal.
		{"$1NUMBER", "$1NUMBER"},
		{"${ FOO }", "${ FOO }"},
	}
	for _, tc := range cases {
		got := dockerfile.ExpandVars(tc.in, lookup)
		if got != tc.want {
			t.Errorf("ExpandVars(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandVars_Modifiers(t *testing.T) {
	values := map[string]string{
		"SET":   "value",
		"EMPTY": "",
		"REPO":  "quay.io",
	}
	lookup := func(name string) (string, bool) {
		v, ok := values[name]
		return v, ok
	}
	cases := []struct {
		in   string
		want string
	}{
		// :- (default if unset or empty)
		{"${SET:-fallback}", "value"},
		{"${EMPTY:-fallback}", "fallback"},
		{"${MISSING:-fallback}", "fallback"},
		// :+ (alt if set and non-empty, empty otherwise)
		{"${SET:+alt}", "alt"},
		{"${EMPTY:+alt}", ""},
		{"${MISSING:+alt}", ""},
		// Default values can themselves be variable references.
		{"${MISSING:-${REPO}}", "quay.io"},
		{"${MISSING:-${ALSO_MISSING}}", "${ALSO_MISSING}"},
		// Nested braces survive findMatchingBrace.
		{"${MISSING:-prefix-${REPO}-suffix}", "prefix-quay.io-suffix"},
		// Modifier with empty default.
		{"${MISSING:-}", ""},
		{"${EMPTY:-}", ""},
		// Combined with surrounding text.
		{"img-${SET:-default}-end", "img-value-end"},
		{"img-${MISSING:-default}-end", "img-default-end"},
	}
	for _, tc := range cases {
		got := dockerfile.ExpandVars(tc.in, lookup)
		if got != tc.want {
			t.Errorf("ExpandVars(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestExpandVars_NilLookup(t *testing.T) {
	// nil lookup must not panic; the result should be the input unchanged.
	got := dockerfile.ExpandVars("${BASE}", nil)
	if got != "${BASE}" {
		t.Errorf("ExpandVars(${BASE}, nil) = %q, want %q", got, "${BASE}")
	}
}

func TestParse_CopySource_Scope_StageLocalArg(t *testing.T) {
	// A stage-local ARG with a default should appear in CopySources[i].Scope
	// for any COPY/ADD that follows it in the same stage.
	const src = `FROM alpine:3.20
ARG VERSION=1.0
COPY app-${VERSION}.tar.gz /tmp/
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.CopySources) != 1 {
		t.Fatalf("CopySources length: got %d, want 1", len(pr.CopySources))
	}
	scope := pr.CopySources[0].Scope
	if len(scope) != 1 {
		t.Fatalf("Scope length: got %d, want 1", len(scope))
	}
	d := scope[0]
	if d.Kind != dockerfile.DeclARG {
		t.Errorf("Scope[0].Kind: got %v, want DeclARG", d.Kind)
	}
	if d.Name != "VERSION" || d.Value != "1.0" || !d.HasDefault {
		t.Errorf("Scope[0]: got %+v, want {ARG VERSION=1.0 hasDefault=true}", d)
	}
}

func TestParse_CopySource_Scope_EnvDeclaration(t *testing.T) {
	// ENV declarations in the same stage should also appear in Scope, in
	// declaration order. The deprecated "ENV NAME value" form is treated as
	// a single binding.
	const src = `FROM alpine:3.20
ARG TAG=stable
ENV PATH_PREFIX=/opt
ENV LEGACY value
COPY ${PATH_PREFIX}/${TAG}/ /usr/local/
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.CopySources) != 1 {
		t.Fatalf("CopySources length: got %d", len(pr.CopySources))
	}
	scope := pr.CopySources[0].Scope
	if len(scope) != 3 {
		t.Fatalf("Scope length: got %d, want 3 (ARG TAG, ENV PATH_PREFIX, ENV LEGACY); scope=%+v", len(scope), scope)
	}
	// Order matters.
	if scope[0].Kind != dockerfile.DeclARG || scope[0].Name != "TAG" {
		t.Errorf("Scope[0]: got %+v, want ARG TAG", scope[0])
	}
	if scope[1].Kind != dockerfile.DeclENV || scope[1].Name != "PATH_PREFIX" || scope[1].Value != "/opt" {
		t.Errorf("Scope[1]: got %+v, want ENV PATH_PREFIX=/opt", scope[1])
	}
	if scope[2].Kind != dockerfile.DeclENV || scope[2].Name != "LEGACY" || scope[2].Value != "value" {
		t.Errorf("Scope[2]: got %+v, want ENV LEGACY=value", scope[2])
	}
}

func TestParse_CopySource_Scope_EnvMultiBinding(t *testing.T) {
	// A single ENV instruction can declare multiple variables; all of them
	// must appear in Scope, in declaration order.
	const src = `FROM alpine:3.20
ENV A=1 B=2 C=3
COPY x /
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.CopySources) != 1 {
		t.Fatalf("CopySources length: got %d, want 1", len(pr.CopySources))
	}
	scope := pr.CopySources[0].Scope
	if len(scope) != 3 {
		t.Fatalf("Scope length: got %d, want 3 (one per ENV binding); scope=%+v", len(scope), scope)
	}
	want := []struct{ name, value string }{{"A", "1"}, {"B", "2"}, {"C", "3"}}
	for i, w := range want {
		if scope[i].Kind != dockerfile.DeclENV || scope[i].Name != w.name || scope[i].Value != w.value {
			t.Errorf("Scope[%d]: got %+v, want ENV %s=%s", i, scope[i], w.name, w.value)
		}
	}
}

func TestParse_CopySource_Scope_ResetsAtFrom(t *testing.T) {
	// Stage-local declarations must NOT leak into the next FROM stage.
	const src = `FROM alpine:3.20 AS first
ARG STAGE1=one
COPY a.txt /

FROM alpine:3.20 AS second
COPY b.txt /
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.CopySources) != 2 {
		t.Fatalf("CopySources length: got %d, want 2", len(pr.CopySources))
	}
	if len(pr.CopySources[0].Scope) != 1 {
		t.Errorf("CopySources[0].Scope: got len=%d, want 1", len(pr.CopySources[0].Scope))
	}
	if len(pr.CopySources[1].Scope) != 0 {
		t.Errorf("CopySources[1].Scope should be empty after FROM reset; got %+v", pr.CopySources[1].Scope)
	}
}

func TestParse_CopySource_Scope_PreFromArgsExcluded(t *testing.T) {
	// ARGs declared BEFORE any FROM are pre-FROM ARGs, not stage-local —
	// they must NOT appear in CopySource.Scope (they live in
	// PreFromArgDefaults instead, and are only inherited via in-stage
	// "ARG NAME" redeclaration).
	const src = `ARG BASE=alpine:3.20
FROM ${BASE}
COPY app /
`
	pr, err := dockerfile.Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.CopySources) != 1 {
		t.Fatalf("CopySources length: got %d, want 1", len(pr.CopySources))
	}
	if len(pr.CopySources[0].Scope) != 0 {
		t.Errorf("CopySources[0].Scope should be empty (BASE is pre-FROM); got %+v", pr.CopySources[0].Scope)
	}
	if pr.PreFromArgDefaults["BASE"] != "alpine:3.20" {
		t.Errorf("PreFromArgDefaults[BASE]: got %q, want alpine:3.20", pr.PreFromArgDefaults["BASE"])
	}
}

func TestFromRef_Expand(t *testing.T) {
	lookup := func(name string) (string, bool) {
		switch name {
		case "BASE":
			return "alpine:3.20", true
		case "PLAT":
			return "linux/amd64", true
		}
		return "", false
	}
	in := dockerfile.FromRef{
		Image:    "${BASE}",
		Platform: "${PLAT}",
		Stage:    "runtime",
	}
	got := in.Expand(lookup)
	if got.Image != "alpine:3.20" {
		t.Errorf("Image: got %q, want alpine:3.20", got.Image)
	}
	if got.Platform != "linux/amd64" {
		t.Errorf("Platform: got %q, want linux/amd64", got.Platform)
	}
	if got.Stage != "runtime" {
		t.Errorf("Stage: got %q, want runtime", got.Stage)
	}
}

func TestParseCopyNode_Exclude(t *testing.T) {
	const df = "FROM ubuntu:22.04\nCOPY --exclude=*.log . /app/\n"
	pr, err := dockerfile.Parse(strings.NewReader(df))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.CopySources) != 1 {
		t.Fatalf("CopySources length: got %d, want 1", len(pr.CopySources))
	}
	src := pr.CopySources[0]
	if len(src.Excludes) != 1 || src.Excludes[0] != "*.log" {
		t.Errorf("Excludes: got %v, want [*.log]", src.Excludes)
	}
	if src.Stage != "" {
		t.Errorf("Stage: expected empty, got %q", src.Stage)
	}
}

func TestParseCopyNode_MultipleExcludes(t *testing.T) {
	const df = "FROM ubuntu:22.04\nCOPY --exclude=*.log --exclude=*.tmp . /app/\n"
	pr, err := dockerfile.Parse(strings.NewReader(df))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.CopySources) != 1 {
		t.Fatalf("CopySources length: got %d, want 1", len(pr.CopySources))
	}
	src := pr.CopySources[0]
	want := []string{"*.log", "*.tmp"}
	if len(src.Excludes) != len(want) {
		t.Fatalf("Excludes length: got %d, want %d", len(src.Excludes), len(want))
	}
	for i, v := range want {
		if src.Excludes[i] != v {
			t.Errorf("Excludes[%d]: got %q, want %q", i, src.Excludes[i], v)
		}
	}
}

func TestParseCopyNode_FromAndExclude(t *testing.T) {
	const df = "FROM ubuntu:22.04\nCOPY --from=builder --exclude=*.log /src/ /app/\n"
	pr, err := dockerfile.Parse(strings.NewReader(df))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.CopySources) != 1 {
		t.Fatalf("CopySources length: got %d, want 1", len(pr.CopySources))
	}
	src := pr.CopySources[0]
	if src.Stage != "builder" {
		t.Errorf("Stage: got %q, want builder", src.Stage)
	}
	if len(src.Excludes) != 1 || src.Excludes[0] != "*.log" {
		t.Errorf("Excludes: got %v, want [*.log]", src.Excludes)
	}
}
