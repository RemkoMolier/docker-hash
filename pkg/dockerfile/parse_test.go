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

	// FromImages: "builder" is a local stage and must not appear; both real images must.
	wantImages := []string{"golang:1.21", "ubuntu:22.04"}
	if len(pr.FromImages) != len(wantImages) {
		t.Fatalf("FromImages length: got %d, want %d (%v)", len(pr.FromImages), len(wantImages), pr.FromImages)
	}
	for i, w := range wantImages {
		if pr.FromImages[i] != w {
			t.Errorf("FromImages[%d]: got %q, want %q", i, pr.FromImages[i], w)
		}
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
	// "scratch" must not appear in FromImages.
	if len(pr.FromImages) != 0 {
		t.Errorf("expected no FromImages for scratch-only Dockerfile, got %v", pr.FromImages)
	}
}

func TestParse_FromImages_Simple(t *testing.T) {
	df := "FROM ubuntu:22.04\nRUN echo hello\n"
	pr, err := dockerfile.Parse(strings.NewReader(df))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.FromImages) != 1 || pr.FromImages[0] != "ubuntu:22.04" {
		t.Errorf("FromImages: got %v, want [ubuntu:22.04]", pr.FromImages)
	}
}

func TestParse_FromImages_Multistage(t *testing.T) {
	// Local stage references must be excluded; only real image names remain.
	df := `FROM golang:1.21 AS builder
RUN go build
FROM ubuntu:22.04
COPY --from=builder /bin/app /app
`
	pr, err := dockerfile.Parse(strings.NewReader(df))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// "builder" is a local stage — must not appear.
	want := []string{"golang:1.21", "ubuntu:22.04"}
	if len(pr.FromImages) != len(want) {
		t.Fatalf("FromImages length: got %d, want %d (%v)", len(pr.FromImages), len(want), pr.FromImages)
	}
	for i, w := range want {
		if pr.FromImages[i] != w {
			t.Errorf("FromImages[%d]: got %q, want %q", i, pr.FromImages[i], w)
		}
	}
}

func TestParse_FromImages_ScratchExcluded(t *testing.T) {
	df := "FROM scratch\nCOPY bin /bin\n"
	pr, err := dockerfile.Parse(strings.NewReader(df))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(pr.FromImages) != 0 {
		t.Errorf("FromImages: expected empty, got %v", pr.FromImages)
	}
}

func TestParse_FromImages_StageReferenceExcluded(t *testing.T) {
	// "builder" is a local stage name; the second FROM references it.
	df := `FROM alpine:3.19 AS builder
FROM builder AS final
`
	pr, err := dockerfile.Parse(strings.NewReader(df))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Only "alpine:3.19" should appear; "builder" is a local stage.
	if len(pr.FromImages) != 1 || pr.FromImages[0] != "alpine:3.19" {
		t.Errorf("FromImages: got %v, want [alpine:3.19]", pr.FromImages)
	}
}
