// Package dockerfile provides utilities for parsing Dockerfiles and extracting
// information relevant for deterministic hash computation.
package dockerfile

import (
	"bytes"
	"io"
	"os"
	"strconv"
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
	// As parsed they may contain $VAR / ${VAR} references; resolve them
	// using a lookup built from Scope before treating them as filesystem
	// patterns.
	Paths []string
	// Stage is the name of the build stage the source comes from (--from flag),
	// or empty if the source is from the build context. As parsed it may
	// contain $VAR / ${VAR} references too.
	Stage string
	// Excludes contains the patterns from --exclude= flags on this instruction,
	// in source order. Each pattern is matched against paths relative to the
	// source path (Docker's --exclude semantics).
	Excludes []string
	// Scope is the ordered list of ARG and ENV declarations visible at this
	// COPY/ADD instruction's position in the Dockerfile. Callers that want to
	// expand variable references in Paths or Stage should walk Scope (in
	// declaration order) to build the variable lookup, then call ExpandVars.
	//
	// Per the Dockerfile spec, ARG visibility is per-stage: pre-FROM ARGs
	// are NOT automatically visible inside a stage. To use a pre-FROM ARG
	// inside a stage, the Dockerfile must redeclare it via "ARG NAME"
	// (without a value) inside the stage. The parser captures both the
	// stage-local declarations and the inheritance points; consumers
	// looking up an "ARG NAME" entry without a default should fall back to
	// ParseResult.PreFromArgDefaults.
	//
	// Scope is empty for COPY/ADD instructions that appear before any FROM
	// (which is invalid per the Dockerfile spec, but the parser does not
	// reject them).
	Scope []Decl
}

// DeclKind identifies whether a Decl is an ARG or an ENV declaration.
type DeclKind int

const (
	// DeclARG marks an ARG declaration. The default value lives in
	// Decl.Value when Decl.HasDefault is true.
	DeclARG DeclKind = iota
	// DeclENV marks an ENV declaration. The raw value (which may contain
	// $VAR references) lives in Decl.Value.
	DeclENV
)

// Decl represents a single ARG or ENV declaration as captured by the parser.
//
// Decls are stored in declaration order on each CopySource.Scope so that
// hashers can replay them at hash time, applying caller-supplied build args
// and ENV expansion against the running variable state.
type Decl struct {
	// Kind is DeclARG or DeclENV.
	Kind DeclKind
	// Name is the variable name.
	Name string
	// Value is the declared default value:
	//   - For DeclARG with HasDefault == true:  the literal default text
	//     after the "=" in "ARG NAME=value".
	//   - For DeclARG with HasDefault == false: empty string.
	//   - For DeclENV:                          the raw value text, which
	//     may itself contain $VAR / ${VAR} references that need to be
	//     expanded against earlier declarations in the same stage.
	Value string
	// HasDefault is meaningful for DeclARG only. true means "ARG NAME=value"
	// (the parser captured an explicit default); false means "ARG NAME"
	// (no default; the value comes from caller --build-arg or, if the
	// pre-FROM ARG of the same name has a default, from that inheritance).
	HasDefault bool
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

// ExpandVars returns s with variable references substituted using lookup.
//
// Supported forms (matching the Dockerfile spec for instruction expansion):
//
//   - $VAR              — bare form, terminated by any non-identifier char
//   - ${VAR}            — braced form
//   - ${VAR:-default}   — value of VAR if set and non-empty, else default
//   - ${VAR:+alt}       — alt if VAR is set and non-empty, else empty
//
// Identifier rules: names must start with a letter or underscore and contain
// only letters, digits, and underscores. Anything else (e.g. "$1", "${ FOO}")
// is left literal in the result.
//
// Modifier arguments (the bit after :- or :+) are recursively expanded
// against the same lookup, so "${VAR:-${OTHER}}" works. Top-level expansion
// is single-pass: a value returned by lookup that itself contains $... is
// NOT re-scanned, matching the Dockerfile spec.
//
// References whose name lookup returns false (with no modifier) are left
// literal in the result so callers can detect and report them.
func ExpandVars(s string, lookup ArgLookup) string {
	if lookup == nil || !strings.ContainsRune(s, '$') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '$' {
			b.WriteByte(s[i])
			i++
			continue
		}
		if i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		next := s[i+1]
		if next == '{' {
			end := findMatchingBrace(s, i+1)
			if end < 0 {
				b.WriteByte(s[i])
				i++
				continue
			}
			inner := s[i+2 : end]
			expanded, ok := expandBraced(inner, lookup)
			if ok {
				b.WriteString(expanded)
			} else {
				b.WriteString(s[i : end+1])
			}
			i = end + 1
		} else if isIdentStart(next) {
			end := i + 2
			for end < len(s) && isIdentChar(s[end]) {
				end++
			}
			name := s[i+1 : end]
			if v, ok := lookup(name); ok {
				b.WriteString(v)
			} else {
				b.WriteString(s[i:end])
			}
			i = end
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// findMatchingBrace returns the index of the } that matches the { at openIdx,
// honouring nested braces. Returns -1 if no match.
func findMatchingBrace(s string, openIdx int) int {
	depth := 0
	for i := openIdx; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// expandBraced parses the content of a ${...} reference and returns the
// expanded value. If the reference is malformed (invalid identifier, etc.) it
// returns ok=false so the caller can leave the whole match literal. An
// unknown name with no modifier returns ok=false; an unknown name with a
// modifier returns the modifier's fallback (default or empty) with ok=true,
// matching Docker's expansion semantics.
func expandBraced(inner string, lookup ArgLookup) (string, bool) {
	var name, modifier, modifierArg string
	if idx := strings.Index(inner, ":-"); idx >= 0 {
		name = inner[:idx]
		modifier = ":-"
		modifierArg = inner[idx+2:]
	} else if idx := strings.Index(inner, ":+"); idx >= 0 {
		name = inner[:idx]
		modifier = ":+"
		modifierArg = inner[idx+2:]
	} else {
		name = inner
	}

	if !isValidIdent(name) {
		return "", false
	}

	value, ok := lookup(name)
	valueIsSet := ok && value != ""

	switch modifier {
	case ":-":
		if valueIsSet {
			return value, true
		}
		// Recursively expand the default so "${VAR:-${OTHER}}" works.
		return ExpandVars(modifierArg, lookup), true
	case ":+":
		if valueIsSet {
			// Recursively expand the alternate too.
			return ExpandVars(modifierArg, lookup), true
		}
		return "", true
	default:
		if ok {
			return value, true
		}
		return "", false
	}
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool {
	return c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isValidIdent(s string) bool {
	if s == "" || !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentChar(s[i]) {
			return false
		}
	}
	return true
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
	// BEFORE the first FROM line. Per the Dockerfile spec, only ARGs declared
	// before the first FROM are visible to FROM expressions; ARGs declared
	// inside a stage (after a FROM) are stage-scoped and cannot template
	// subsequent FROM lines.
	//
	// PreFromArgDefaults only contains entries for the ARGs that supplied an
	// explicit default value (e.g. `ARG BASE=alpine:3.20`). The wider set of
	// names declared as pre-FROM ARGs — including those without a default,
	// e.g. bare `ARG BASE` — lives in PreFromArgNames.
	//
	// Callers that want to expand $VAR / ${VAR} references in FromRef.Image
	// or FromRef.Platform should layer caller-supplied build args over this
	// map (caller args win), gated by PreFromArgNames, and pass the resulting
	// lookup to FromRef.Expand.
	PreFromArgDefaults map[string]string
	// PreFromArgNames is the set of ARG names declared BEFORE the first FROM,
	// regardless of whether they had a default. Per the Dockerfile spec, FROM
	// expression expansion is only allowed to see these names (plus the
	// automatic platform ARGs Docker supplies on its own). Callers MUST gate
	// FROM-expansion lookups by this set so that an arbitrary
	// `--build-arg RANDOM=foo` cannot leak into a `FROM ${RANDOM}` reference
	// when the Dockerfile never declared `ARG RANDOM` before any FROM.
	PreFromArgNames map[string]struct{}
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
		PreFromArgNames:    make(map[string]struct{}),
		StageAliases:       make(map[string]struct{}),
	}

	st := &parseState{
		seenArgs: make(map[string]struct{}),
		// currentStageDecls tracks the ordered list of ARG and ENV
		// declarations that have appeared in the current FROM stage so
		// far. It resets to nil at every FROM and is snapshot-copied
		// onto each CopySource at the COPY/ADD position. Pre-FROM ARGs
		// do NOT enter this list — they live in pr.PreFromArgDefaults
		// and are inherited only via an in-stage "ARG NAME"
		// redeclaration without a default.
	}

	for _, node := range result.AST.Children {
		switch strings.ToLower(node.Value) {
		case command.Copy, command.Add:
			st.handleCopyOrAdd(node, pr)
		case command.Arg:
			st.handleArg(node, pr)
		case command.Env:
			st.handleEnv(node)
		case command.From:
			st.handleFrom(node, pr)
		}
	}

	return pr, nil
}

// parseState is the in-flight bookkeeping for a single Parse call. It
// exists so that the per-instruction handlers can decompose into focused
// methods without each one having to thread the same four pieces of
// state back through its signature.
type parseState struct {
	seenArgs          map[string]struct{}
	seenFirstFrom     bool
	currentStageDecls []Decl
}

// handleCopyOrAdd snapshots the current stage's visible declarations
// onto a parsed COPY/ADD instruction so the hasher can build a
// per-CopySource expansion lookup later.
func (st *parseState) handleCopyOrAdd(node *parser.Node, pr *ParseResult) {
	cs := parseCopyNode(node)
	if len(st.currentStageDecls) > 0 {
		cs.Scope = make([]Decl, len(st.currentStageDecls))
		copy(cs.Scope, st.currentStageDecls)
	}
	pr.CopySources = append(pr.CopySources, cs)
}

// unquoteArgValue strips a single layer of surrounding double or single quotes
// from an ARG default value, mirroring Docker's Dockerfile handling.
// "hello" → hello, 'world' → world, "" → (empty), '' → (empty).
// Values without surrounding quotes are returned unchanged.
func unquoteArgValue(s string) string {
	if unquoted, err := strconv.Unquote(s); err == nil {
		return unquoted
	}
	// strconv.Unquote only handles double-quoted Go strings; handle
	// single-quoted values (no escape-sequence support, matching Docker).
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}

// handleArg processes an `ARG name` or `ARG name=default` instruction.
// Pre-FROM ARGs are recorded in pr.PreFromArgNames /
// pr.PreFromArgDefaults so the hasher can gate FROM-expansion lookups by
// them; in-stage ARGs are appended to the current stage's scope. The
// caller-facing pr.BuildArgNames list is deduplicated across stages.
func (st *parseState) handleArg(node *parser.Node, pr *ParseResult) {
	if node.Next == nil {
		return
	}
	argExpr := node.Next.Value
	name := argExpr
	value := ""
	hasDefault := false
	if idx := strings.Index(argExpr, "="); idx >= 0 {
		name = argExpr[:idx]
		value = argExpr[idx+1:]
		// Strip surrounding quotes (e.g. ARG FOO="" or ARG FOO='bar').
		value = unquoteArgValue(value)
		hasDefault = true
	}
	// Deduplicate: the same ARG name can appear in multiple stages.
	if _, ok := st.seenArgs[name]; !ok {
		st.seenArgs[name] = struct{}{}
		pr.BuildArgNames = append(pr.BuildArgNames, name)
	}
	// Capture pre-FROM ARG declarations: only ARGs declared BEFORE the
	// first FROM line are usable in subsequent FROM expressions per the
	// Dockerfile spec. The first declaration wins; later redeclarations
	// are ignored.
	if !st.seenFirstFrom {
		pr.PreFromArgNames[name] = struct{}{}
		if hasDefault {
			if _, exists := pr.PreFromArgDefaults[name]; !exists {
				pr.PreFromArgDefaults[name] = value
			}
		}
		return
	}
	// Stage-local ARG declarations are appended to the current stage's
	// scope. The Dockerfile spec says these are visible to subsequent
	// instructions (COPY, ADD, RUN, etc.) within the same stage.
	st.currentStageDecls = append(st.currentStageDecls, Decl{
		Kind:       DeclARG,
		Name:       name,
		Value:      value,
		HasDefault: hasDefault,
	})
}

// handleEnv processes an in-stage ENV instruction. ENV NAME=value (kv
// form) and "ENV NAME value" (legacy form) are both supported. The
// buildkit AST encodes the chain as triples: (key, value, separator).
// The separator is "=" for the kv form and "" for the legacy form, and
// is repeated after each pair when an instruction binds multiple
// variables, e.g.
//
//	ENV A=1 B=2  → [A, 1, =, B, 2, =]
//	ENV LEGACY value → [LEGACY, value, ""]
//
// Pre-FROM ENVs are skipped, matching the ARG handling: declarations
// outside any stage cannot be visible to a COPY/ADD inside a later
// stage.
func (st *parseState) handleEnv(node *parser.Node) {
	if !st.seenFirstFrom {
		return
	}
	for n := node.Next; n != nil; {
		if n.Next == nil {
			break // malformed: dangling key with no value/separator
		}
		key := n.Value
		value := n.Next.Value
		// Advance past key and value; the separator (if present) is
		// one more node down the chain.
		next := n.Next.Next
		if next != nil {
			next = next.Next
		}
		if key != "" {
			st.currentStageDecls = append(st.currentStageDecls, Decl{
				Kind:  DeclENV,
				Name:  key,
				Value: value,
			})
		}
		n = next
	}
}

// handleFrom processes a FROM instruction. The current stage's visible
// declarations are cleared because the new stage starts with no
// inherited ARG/ENV state (per the Dockerfile spec the only inheritance
// is via in-stage "ARG NAME" redeclaration of a pre-FROM ARG).
func (st *parseState) handleFrom(node *parser.Node, pr *ParseResult) {
	st.seenFirstFrom = true
	st.currentStageDecls = nil
	ref := parseFromNode(node, pr.StageAliases)
	pr.FromRefs = append(pr.FromRefs, ref)
	// Record this stage alias so any later "FROM <alias>" line is
	// detected as a stage reference rather than a registry image.
	if ref.Stage != "" {
		pr.StageAliases[ref.Stage] = struct{}{}
	}
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
