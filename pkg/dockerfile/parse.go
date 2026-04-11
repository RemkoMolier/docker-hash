// Package dockerfile provides utilities for parsing Dockerfiles and extracting
// information relevant for deterministic hash computation.
package dockerfile

import (
	"bytes"
	"io"
	"os"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/command"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// CopySource represents a source path from a COPY or ADD instruction.
type CopySource struct {
	// Paths is the list of source paths specified in the instruction.
	Paths []string
	// Stage is the name of the build stage the source comes from (--from flag),
	// or empty if the source is from the build context.
	Stage string
}

// ParseResult holds the result of parsing a Dockerfile.
type ParseResult struct {
	// RawContent is the raw Dockerfile content.
	RawContent []byte
	// CopySources contains all COPY and ADD instructions found in the Dockerfile,
	// including those that reference another build stage via --from. Callers
	// should inspect Stage to distinguish build-context sources (Stage == "")
	// from inter-stage copies (Stage != "").
	CopySources []CopySource
	// BuildArgNames are the unique names of ARG instructions found in the Dockerfile.
	BuildArgNames []string
	// FromImages contains the image references from all FROM instructions that
	// reference an external registry image (not a local build stage and not
	// "scratch"). These can be used to resolve digests for deterministic hashing.
	FromImages []string
}

// ParseFile opens and parses a Dockerfile at the given path.
func ParseFile(path string) (*ParseResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// Parse parses a Dockerfile from a reader.
func Parse(r io.Reader) (*ParseResult, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	result, err := parser.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}

	pr := &ParseResult{
		RawContent: raw,
	}

	seenArgs := make(map[string]struct{})
	// stageNames tracks all build-stage names (from AS clauses) so we can
	// distinguish local stage references from real image references in FROM.
	stageNames := make(map[string]struct{})

	for _, node := range result.AST.Children {
		switch strings.ToLower(node.Value) {
		case command.From:
			if node.Next != nil {
				image := node.Next.Value
				// Check for AS <stage> to record stage names.
				if node.Next.Next != nil && strings.ToLower(node.Next.Next.Value) == "as" && node.Next.Next.Next != nil {
					stageNames[strings.ToLower(node.Next.Next.Next.Value)] = struct{}{}
				}
				// Skip scratch; local stage references are filtered below.
				if strings.ToLower(image) != "scratch" {
					pr.FromImages = append(pr.FromImages, image)
				}
			}
		case command.Copy, command.Add:
			cs := parseCopyNode(node)
			pr.CopySources = append(pr.CopySources, cs)
		case command.Arg:
			if node.Next != nil {
				// ARG name or ARG name=default — extract just the name
				argExpr := node.Next.Value
				argName := argExpr
				if idx := strings.Index(argExpr, "="); idx >= 0 {
					argName = argExpr[:idx]
				}
				// Deduplicate: the same ARG name can appear in multiple stages.
				if _, ok := seenArgs[argName]; !ok {
					seenArgs[argName] = struct{}{}
					pr.BuildArgNames = append(pr.BuildArgNames, argName)
				}
			}
		}
	}

	// Remove FROM images that are actually local stage references.
	filtered := make([]string, 0, len(pr.FromImages))
	for _, img := range pr.FromImages {
		if _, isStage := stageNames[strings.ToLower(img)]; !isStage {
			filtered = append(filtered, img)
		}
	}
	pr.FromImages = filtered

	return pr, nil
}

// parseCopyNode extracts CopySource information from a COPY/ADD AST node.
func parseCopyNode(node *parser.Node) CopySource {
	cs := CopySource{}

	// Check flags for --from=<stage>
	for _, flag := range node.Flags {
		if strings.HasPrefix(flag, "--from=") {
			cs.Stage = strings.TrimPrefix(flag, "--from=")
		}
	}

	// Collect all tokens; the last token is the destination, the rest are sources.
	var tokens []string
	for n := node.Next; n != nil; n = n.Next {
		tokens = append(tokens, n.Value)
	}

	// Need at least source + destination
	if len(tokens) < 2 {
		return cs
	}

	// Sources are all tokens except the last (destination)
	cs.Paths = tokens[:len(tokens)-1]
	return cs
}
