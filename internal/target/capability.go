package target

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/module"
)

// Capabilities declares what a target can express. Plan renders through Admit, so the table
// is both what lola checks and what it does.
type Capabilities struct {
	Kinds map[string]KindSpec // a kind missing from the map is unsupported: skipped, not forceable
	Rules []ContentRule       // patterns in artifact bodies the target cannot handle
}

// KindSpec describes one artifact kind (skill, command, agent, mcp or rule).
type KindSpec struct {
	Keys     map[string]Value // known frontmatter keys: checked, or always dropped (Value.Drop)
	Open     bool             // keep keys missing from Keys, unchecked; false drops them with a warn
	Required []string         // keys that must be present, non-empty and valid
	Name     *regexp.Regexp   // a name the target rejects is a forceable skip; nil accepts any
	Bounds                    // the assistant versions that have this kind
}

// Shape is the YAML shape a frontmatter value must have.
type Shape string

// Shapes.
const (
	ShapeString      Shape = "string"
	ShapeStringList  Shape = "string-list"
	ShapeBoolMap     Shape = "bool-map"
	ShapeBool        Shape = "bool"
	ShapeNumber      Shape = "number"
	ShapePositiveInt Shape = "positive-integer" // a whole number above zero, written as an integer or a float
	ShapeMap         Shape = "map"
)

// Value describes one frontmatter key.
type Value struct {
	Shape   Shape
	Pattern *regexp.Regexp // checked against a string, or each element of a string list
	Drop    Severity       // non-empty: always remove the key, with a finding of this severity
	Note    string         // why the key is dropped; used as the finding's text when Drop is set
	// Guard marks a key that restricts the artifact, which the assistant leaves unrestricted
	// without it. A value the target does not keep (any, with Drop, or one it does not accept)
	// is dropped quietly when empty and otherwise skips the artifact, forceably, with Guard's
	// code, text and hint; --force renders the artifact without the key.
	Guard  *Finding
	Bounds // the assistant versions that read this key
}

// ContentRule matches artifact bodies of one kind that the target cannot handle as written.
type ContentRule struct {
	Kind             string
	Pattern          *regexp.Regexp
	Severity         Severity
	Forceable        bool // for a skip: --force renders the artifact anyway
	Code, Text, Hint string
	Bounds           // the assistant versions that have this problem
}

// Admit checks one artifact against caps (§5.1 of the target framework design) and returns its
// document without the keys the target drops, the findings, and whether to render it. MCP
// servers and rules pass an empty Doc: only their kind and name are checked.
func Admit(id string, caps Capabilities, ctx PlanContext, kind, name string, doc module.Doc) (module.Doc, []Finding, bool) {
	at := Finding{Target: id, Kind: kind, Artifact: name}
	spec, ok := caps.Kinds[kind]
	if !ok || !spec.newest() {
		f := at
		f.Code, f.Text = "kind-unsupported", fmt.Sprintf("%s has no place for %ss", id, kind)
		f, _ = Skipped(ctx, f)
		return doc, []Finding{f}, false
	}
	var finds []Finding
	render := true
	if spec.excludes(ctx.Version) {
		finds = append(finds, versionFinding(at, fmt.Sprintf("%s %s has no place for %ss (added in %s)", id, ctx.Version, kind, spec.Since)))
	}
	skip := func(f Finding) {
		f, r := Skipped(ctx, f)
		finds, render = append(finds, f), render && r
	}
	if spec.Name != nil && !spec.Name.MatchString(name) {
		f := at
		f.Code, f.Text, f.Forceable = "name-rejected", fmt.Sprintf("%s needs %s names matching %s", id, kind, spec.Name), true
		skip(f)
	}
	var drop []string
	for _, k := range slices.Sorted(maps.Keys(doc.Frontmatter)) {
		f := at
		f.Severity, f.Code = SeverityWarn, "frontmatter-key-dropped"
		v, known := spec.Keys[k]
		known = known && v.newest()
		switch {
		case known && v.Guard != nil && (v.Drop != "" || !v.accepts(doc.Frontmatter[k])):
			if !blank(doc.Frontmatter[k]) {
				g := at
				g.Code, g.Text, g.Hint, g.Forceable = v.Guard.Code, v.Guard.Text, v.Guard.Hint, true
				skip(g)
			}
			drop = append(drop, k)
			continue
		case known && v.Drop != "":
			f.Severity, f.Text = v.Drop, fmt.Sprintf("%s dropped: %s", k, v.Note)
		case known && !v.accepts(doc.Frontmatter[k]):
			f.Code, f.Text = "frontmatter-value-dropped", fmt.Sprintf("%s dropped: %s needs %s", k, id, v.describe())
		case !known && !spec.Open:
			f.Text = fmt.Sprintf("%s dropped: %s does not support it", k, id)
		case known && v.excludes(ctx.Version):
			finds = append(finds, versionFinding(at, fmt.Sprintf("%s %s ignores %s (added in %s)", id, ctx.Version, k, v.Since)))
			continue
		default:
			continue
		}
		drop = append(drop, k)
		finds = append(finds, f)
	}
	for _, r := range spec.Required {
		if s, isString := doc.Frontmatter[r].(string); doc.Frontmatter[r] == nil || (isString && s == "") || slices.Contains(drop, r) {
			f := at
			f.Code, f.Forceable = "required-key-missing", true
			f.Text = fmt.Sprintf("%s needs a %s in the %s's frontmatter", id, r, kind)
			f.Hint = fmt.Sprintf("add %s: to the %s's frontmatter", r, kind)
			skip(f)
		}
	}
	for _, rule := range caps.Rules {
		if rule.Kind != kind || !rule.Pattern.MatchString(doc.Body) {
			continue
		}
		if !rule.newest() {
			if canonical(ctx.Version) != "" && !rule.excludes(ctx.Version) {
				f := versionFinding(at, fmt.Sprintf("%s in %s %s (fixed in %s)", rule.Text, id, ctx.Version, rule.Until))
				f.Code = "version/limited"
				finds = append(finds, f)
			}
			continue
		}
		f := at
		f.Severity, f.Code, f.Text, f.Hint, f.Forceable = rule.Severity, rule.Code, rule.Text, rule.Hint, rule.Forceable
		if f.Severity == SeveritySkip {
			skip(f)
			continue
		}
		finds = append(finds, f)
	}
	kept, err := doc.Without(drop...)
	if err != nil {
		return doc, append(finds, invalid(at, err)), false
	}
	ambiguous, err := kept.Ambiguous()
	if err != nil {
		return doc, append(finds, invalid(at, err)), false
	}
	for _, k := range ambiguous {
		f := at
		f.Severity, f.Code = SeverityWarn, "frontmatter/yaml-ambiguous"
		f.Text = fmt.Sprintf("%s has an unquoted value that YAML parsers read differently", k)
		f.Hint = "quote it, e.g. \"yes\" or \"0755\""
		finds = append(finds, f)
	}
	return kept, finds, render
}

// versionFinding is a warn about a gap between the newest version, which lola renders for,
// and the installed one.
func versionFinding(at Finding, text string) Finding {
	at.Severity, at.Code, at.Text, at.Hint = SeverityWarn, "version/unsupported", text, "upgrade "+at.Target
	return at
}

// invalid is the finding for frontmatter lola could parse but cannot re-encode: never rendered.
func invalid(at Finding, err error) Finding {
	at.Severity, at.Code, at.Text = SeveritySkip, "frontmatter-invalid", err.Error()
	return at
}

// ToolsGuard is the Guard of an agent's tools key. Gemini CLI and OpenCode give an agent without
// a tool list every tool, so a list the target cannot enforce must not be dropped quietly (user
// decision 2026-10-09). fix is the target's own remedy, shown before the --force alternative.
func ToolsGuard(id, fix string) *Finding {
	return &Finding{Code: "agent-tools-unenforceable",
		Text: id + " cannot enforce this agent's tool list; without it the agent can use every tool",
		Hint: fix + ", or pass --force to install it unrestricted"}
}

// blank reports a frontmatter value that sets nothing: null, "", or an empty list or map.
func blank(x any) bool {
	switch v := x.(type) {
	case nil:
		return true
	case string:
		return v == ""
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

func (v Value) accepts(x any) bool {
	switch v.Shape {
	case ShapeString:
		s, ok := x.(string)
		return ok && (v.Pattern == nil || v.Pattern.MatchString(s))
	case ShapeStringList:
		list, ok := x.([]any)
		if !ok {
			return false
		}
		for _, e := range list {
			if s, ok := e.(string); !ok || (v.Pattern != nil && !v.Pattern.MatchString(s)) {
				return false
			}
		}
		return true
	case ShapeBoolMap:
		m, ok := x.(map[string]any)
		if !ok {
			return false
		}
		for _, e := range m {
			if _, ok := e.(bool); !ok {
				return false
			}
		}
		return true
	case ShapeBool:
		_, ok := x.(bool)
		return ok
	case ShapeNumber:
		switch x.(type) {
		case int, int64, uint64, float64:
			return true
		}
		return false
	case ShapePositiveInt:
		switch n := x.(type) {
		case int:
			return n > 0
		case uint64:
			return n > 0
		case float64:
			return n > 0 && n == math.Trunc(n) && !math.IsInf(n, 1)
		}
		return false
	case ShapeMap:
		_, ok := x.(map[string]any)
		return ok
	}
	return true
}

func (v Value) describe() string {
	d := "a " + string(v.Shape)
	if v.Pattern != nil {
		d += " matching " + v.Pattern.String()
	}
	return d
}
