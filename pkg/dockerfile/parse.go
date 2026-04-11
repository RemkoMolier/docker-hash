// Package dockerfile provides utilities for parsing Dockerfiles and extracting
// information relevant for deterministic hash computation.
package dockerfile

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/moby/buildkit/frontend/dockerfile/command"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
)

// ArgLookup is a function that returns the value of a build argument by name,
// and a flag indicating whether it is known. ExpandVars and FromRef.Expand
// call it for every $VAR / ${VAR} reference they find. Callers typically
// layer caller-supplied build args over parser-extracted ARG defaults.
type ArgLookup func(name string) (string, bool)

// CopySource represents a source path from a COPY or ADD instruction.
type CopySource struct {
	// Paths is the list of source paths specified in the instruction.
	Paths []string
	// Stage is the name of the build stage the source comes from (--from flag),
	// or empty if the source is from the build context.
	Stage string
	// Excludes contains the patterns from --exclude= flags on this instruction,
	// in source order. Each pattern is matched against paths relative to the
	// source path (Docker's --exclude semantics).
	Excludes []string
}

// FromRef represents a single FROM instruction in the Dockerfile.
//
// Callers that want to fold the base image's content into a hash should
// resolve every FromRef whose IsStageRef is false and whose Image is not
// "scratch", then ignore the rest. Stage references and the scratch sentinel
// have no upstream registry digest to fetch.
type FromRef struct {
	// Image is the target text after "FROM " and before "AS", without the
	// --platform= flag and without the stage alias. As parsed it may
	// contain ARG references like "${BASE}" or "$BASE"; call Expand to
	// substitute them. Examples after expansion: "golang:1.25", "alpine",
	// "ubuntu@sha256:abc...", "scratch", or — when IsStageRef is true — a
	// stage alias like "builder".
	Image string

	// Stage is the alias declared after "AS" on this FROM line, e.g.
	// "builder" for "FROM golang:1.25 AS builder". Empty when no alias is
	// declared.
	Stage string

	// Platform is the value of "--platform=" on this FROM line, e.g.
	// "linux/amd64". Empty when the flag is absent. As parsed it may
	// contain ARG references like "$BUILDPLATFORM"; call Expand to
	// substitute them.
	Platform string

	// IsStageRef is true when Image matches a stage alias declared on an
	// earlier FROM line in the same Dockerfile. Stage references are not
	// registry images and must not be sent to a network resolver — their
	// underlying image was already extracted as a separate FromRef when the
	// referenced stage was first defined.
	//
	// IsStageRef is set by Parse based on the literal Image text. Callers
	// that apply Expand should re-evaluate this bit against the post-
	// expansion Image, since an ARG can resolve to a stage alias.
	IsStageRef bool
}

// Expand returns a copy of r with $VAR / ${VAR} references in Image and
// Platform substituted using lookup. References that have no value remain
// literal in the result. The substitution is single-pass; expansion outputs
// are not themselves re-scanned for additional references, matching the
// Dockerfile spec for ARG references in FROM lines.
//
// Expand does not re-evaluate IsStageRef — the caller should redo stage
// detection on the returned ref against the parser's StageAliases set if it
// cares.
func (r FromRef) Expand(lookup ArgLookup) FromRef {
	return FromRef{
		Image:      ExpandVars(r.Image, lookup),
		Stage:      r.Stage,
		Platform:   ExpandVars(r.Platform, lookup),
		IsStageRef: r.IsStageRef,
	}
}

// argRefRegex matches a single $VAR or ${VAR} reference. Identifier rules
// follow the Dockerfile spec: ARG names must start with a letter or
// underscore and contain only letters, digits, and underscores. Anything
// else (e.g. "$1", "${ FOO}", "$-X") is left literal.
var argRefRegex = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// ExpandVars returns s with $VAR and ${VAR} references substituted using
// lookup. References whose name lookup returns false are left literal.
//
// The substitution is single-pass: a value returned by lookup that itself
// contains $... is NOT re-scanned. This matches the Dockerfile spec, which
// does not allow recursive ARG expansion in FROM lines.
//
// Only the two basic forms ($VAR and ${VAR}) are supported. Bash extensions
// like "${VAR:-default}" or "${VAR/foo/bar}" are not supported in Dockerfile
// FROM lines and ExpandVars treats them as literal.
func ExpandVars(s string, lookup ArgLookup) string {
	if lookup == nil || !strings.ContainsRune(s, '$') {
		return s
	}
	return argRefRegex.ReplaceAllStringFunc(s, func(match string) string {
		var name string
		if strings.HasPrefix(match, "${") {
			name = match[2 : len(match)-1]
		} else {
			name = match[1:]
		}
		if v, ok := lookup(name); ok {
			return v
		}
		return match
	})
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
	// FromRefs is the ordered list of every FROM instruction in the Dockerfile.
	// Stage detection is done at parse time: a FromRef whose Image matches an
	// earlier "AS <name>" declaration has IsStageRef set to true.
	FromRefs []FromRef
	// PreFromArgDefaults captures `ARG NAME=default` declarations that appear
	// BEFORE the first FROM line. Per the Dockerfile spec, only these ARGs
	// are visible to FROM expressions; ARGs declared inside a stage (after a
	// FROM) are stage-scoped and cannot template subsequent FROM lines.
	//
	// Callers that want to expand $VAR / ${VAR} references in FromRef.Image
	// or FromRef.Platform should layer caller-supplied build args over this
	// map (caller args win) and pass the resulting lookup to FromRef.Expand.
	PreFromArgDefaults map[string]string
	// StageAliases is the set of "AS <name>" identifiers declared by the
	// Dockerfile's FROM instructions, regardless of order. Callers that
	// re-evaluate stage references after ARG expansion (e.g. when an ARG
	// resolves to a stage alias) consult this set instead of re-walking the
	// FROM list.
	StageAliases map[string]struct{}
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
		RawContent:         raw,
		PreFromArgDefaults: make(map[string]string),
		StageAliases:       make(map[string]struct{}),
	}

	seenArgs := make(map[string]struct{})
	seenFirstFrom := false

	for _, node := range result.AST.Children {
		switch strings.ToLower(node.Value) {
		case command.Copy, command.Add:
			cs := parseCopyNode(node)
			pr.CopySources = append(pr.CopySources, cs)
		case command.Arg:
			if node.Next != nil {
				// ARG name or ARG name=default — extract the name and the
				// optional default value.
				argExpr := node.Next.Value
				name := argExpr
				value := ""
				hasDefault := false
				if idx := strings.Index(argExpr, "="); idx >= 0 {
					name = argExpr[:idx]
					value = argExpr[idx+1:]
					hasDefault = true
				}
				// Deduplicate: the same ARG name can appear in multiple stages.
				if _, ok := seenArgs[name]; !ok {
					seenArgs[name] = struct{}{}
					pr.BuildArgNames = append(pr.BuildArgNames, name)
				}
				// Capture pre-FROM ARG defaults: only ARGs declared BEFORE
				// the first FROM line are usable in subsequent FROM
				// expressions per the Dockerfile spec. The first declaration
				// wins; later redeclarations are ignored.
				if !seenFirstFrom && hasDefault {
					if _, exists := pr.PreFromArgDefaults[name]; !exists {
						pr.PreFromArgDefaults[name] = value
					}
				}
			}
		case command.From:
			seenFirstFrom = true
			ref := parseFromNode(node, pr.StageAliases)
			pr.FromRefs = append(pr.FromRefs, ref)
			// Record this stage alias so any later "FROM <alias>" line is
			// detected as a stage reference rather than a registry image.
			if ref.Stage != "" {
				pr.StageAliases[ref.Stage] = struct{}{}
			}
		}
	}

	return pr, nil
}

// parseCopyNode extracts CopySource information from a COPY/ADD AST node.
func parseCopyNode(node *parser.Node) CopySource {
	cs := CopySource{}

	// Check flags for --from=<stage> and --exclude=<pattern>
	for _, flag := range node.Flags {
		switch {
		case strings.HasPrefix(flag, "--from="):
			cs.Stage = strings.TrimPrefix(flag, "--from=")
		case strings.HasPrefix(flag, "--exclude="):
			cs.Excludes = append(cs.Excludes, strings.TrimPrefix(flag, "--exclude="))
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

// parseFromNode extracts FromRef information from a FROM AST node.
//
// The buildkit AST exposes the FROM line as:
//   - node.Flags: any "--platform=<plat>" flag (and historically others)
//   - node.Next:  the image token
//   - node.Next.Next, .Next.Next.Next: the optional "AS <stage>" tokens
//
// stageAliases is the set of "AS <name>" identifiers seen on earlier FROM
// lines in the same Dockerfile; if the parsed image matches one of those
// aliases, we mark the result as a stage reference (not a registry image).
func parseFromNode(node *parser.Node, stageAliases map[string]struct{}) FromRef {
	ref := FromRef{}

	// Pull --platform=<plat> out of the flags slice. Other --foo= flags are
	// ignored: there are no others in the current Dockerfile spec, but the
	// loop is forward-compatible.
	for _, flag := range node.Flags {
		if strings.HasPrefix(flag, "--platform=") {
			ref.Platform = strings.TrimPrefix(flag, "--platform=")
		}
	}

	// Image token (mandatory).
	if node.Next == nil {
		return ref
	}
	ref.Image = node.Next.Value

	// Optional "AS <stage>" tail. The buildkit parser splits this into two
	// extra tokens: node.Next.Next ("as", case-insensitive) and
	// node.Next.Next.Next (the alias). We only consume the alias when the
	// keyword is exactly "as" (or "AS" — strings.EqualFold).
	if as := node.Next.Next; as != nil && strings.EqualFold(as.Value, "as") && as.Next != nil {
		ref.Stage = as.Next.Value
	}

	// Stage detection: if the parsed image matches an alias from an earlier
	// FROM line, this is an internal stage reference, not a registry image.
	if _, ok := stageAliases[ref.Image]; ok {
		ref.IsStageRef = true
	}

	return ref
}
