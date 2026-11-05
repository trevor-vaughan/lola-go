// Package agents renders a module's skills into .agents/skills, the agentskills.io directory that
// Codex, Gemini CLI, OpenCode, GitHub Copilot, Cursor and Amp read. Claude Code does not read it.
// It holds skills only: every other kind is unsupported.
package agents

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// Target renders into .agents/skills.
type Target struct{}

// ID implements Target.
func (Target) ID() string { return "agents" }

// Root implements Target: the project, or $HOME at user scope.
func (Target) Root(scope target.Scope, projectDir string, p paths.Paths) string {
	if scope == target.User {
		return p.Home
	}
	return projectDir
}

// skills is the directory every tool reading agentskills looks in. Its parent, .agents, is not
// a marker: other tools keep unrelated things there.
const skills = ".agents/skills"

// Detect implements Target.
func (Target) Detect(_ target.Scope, root string) bool {
	return target.HasAny(root, skills)
}

// Destinations implements Target.
func (Target) Destinations(target.Scope, string) (target.Destinations, error) {
	return target.Destinations{Dirs: []target.DestDir{{Kind: "skill", Path: skills, Shared: true}}}, nil
}

// The agentskills.io limits: tools reading .agents/skills fail to load, or silently drop, a skill
// past them.
const (
	maxName        = 64
	maxDescription = 1024
)

// skillName is agentskills' name pattern. Plan checks it, with the length and the directory, so a
// skill gets one name-rejected finding for every problem with its name.
var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// caps: skills only; keys other than Claude's hooks and MCP servers pass through.
var caps = target.Capabilities{
	Kinds: map[string]target.KindSpec{
		"skill": {Open: true, Keys: map[string]target.Value{"description": {Shape: target.ShapeString},
			"hooks":       {Drop: target.SeverityWarn, Fold: true, Note: "the tools that read .agents/skills run no hooks from a skill's frontmatter"},
			"mcpServers":  {Drop: target.SeverityWarn, Fold: true, Note: "the tools that read .agents/skills start no MCP servers from a skill's frontmatter"},
			"mcp_servers": {Drop: target.SeverityWarn, Fold: true, Note: "the tools that read .agents/skills start no MCP servers from a skill's frontmatter"}},
			Required: []string{"description"}},
	},
	Rules: target.SkillRules("agents"),
}

// Capabilities implements Target.
func (Target) Capabilities() target.Capabilities { return caps }

// Probe implements Target: .agents/skills belongs to no one binary.
func (Target) Probe() target.Probe {
	return target.Probe{Readers: "skills for Codex, Gemini, OpenCode, Copilot, Cursor, Amp"}
}

// Plan implements Target.
func (a Target) Plan(m *module.Module, ctx target.PlanContext) ([]plan.Intent, []target.Finding, error) {
	id := a.ID()
	var out []plan.Intent
	var finds []target.Finding
	for _, s := range m.Skills {
		if !module.Supports(s.Targets, id) {
			continue
		}
		doc, f, ok := target.Admit(id, caps, ctx, "skill", s.Name, s.Doc)
		// The tools that read .agents/skills reject or drop a skill without a description, so
		// --force cannot render one.
		for i := range f {
			if f[i].Code == "required-key-missing" {
				f[i].Severity, f[i].Forceable, f[i].Forced, ok = target.SeveritySkip, false, false, false
			}
		}
		finds = append(finds, f...)
		at := target.Finding{Target: id, Kind: "skill", Artifact: s.Name, Forceable: true, Class: target.ClassDetail, AuthorHint: true}
		skip := func(code, text, hint string) {
			f := at
			f.Code, f.Text, f.Hint = code, text, hint
			f, render := target.Skipped(ctx, f)
			finds, ok = append(finds, f), ok && render
		}
		var needs, fixes []string
		if !skillName.MatchString(s.Name) {
			needs = append(needs, "match "+skillName.String())
		}
		if len(s.Name) > maxName {
			needs = append(needs, fmt.Sprintf("have at most %d characters (it has %d)", maxName, len(s.Name)))
		}
		if len(needs) > 0 {
			fixes = append(fixes, fmt.Sprintf("rename the skill's directory to lowercase letters, digits and single inner hyphens, at most %d characters", maxName))
		}
		if name, set := doc.Frontmatter["name"]; set && name != s.Name {
			needs = append(needs, fmt.Sprintf("equal its directory name (it is %v)", name))
			fixes = append(fixes, fmt.Sprintf("rename the directory to %v, or set name: %s", name, s.Name))
		}
		if len(needs) > 0 {
			skip("name-rejected", "agents needs the skill's name to "+strings.Join(needs, " and ")+
				"; tools that read .agents/skills may reject the skill if --force installs it anyway", strings.Join(fixes, "; "))
		}
		if n := utf8.RuneCountInString(doc.String("description")); n > maxDescription {
			skip("description-too-long", fmt.Sprintf("agents needs a skill description of at most %d characters (it has %d)", maxDescription, n),
				fmt.Sprintf("shorten the skill's description: to %d characters", maxDescription))
		}
		if f, bad := metadata(at, doc.Frontmatter); bad {
			finds = append(finds, f)
		}
		if !ok {
			continue
		}
		if _, set := doc.Frontmatter["name"]; !set {
			doc = doc.Prepend("name", s.Name)
		}
		dst := skills + "/" + s.Name
		out = append(out, target.SkillTree(id, s, ctx, dst), target.WriteText(id, "skill", dst+"/SKILL.md", doc.Render(), ctx))
	}
	unsupported := func(kind, name string, targets []string) {
		if !module.Supports(targets, id) {
			return
		}
		_, f, _ := target.Admit(id, caps, ctx, kind, name, module.Doc{})
		for i := range f {
			if f[i].Code == "kind-unsupported" {
				f[i].Text += ": .agents holds skills only"
			}
		}
		finds = append(finds, f...)
	}
	for _, c := range m.Commands {
		unsupported("command", c.Name, c.Targets)
	}
	for _, ag := range m.Agents {
		unsupported("agent", ag.Name, ag.Targets)
	}
	for _, s := range m.MCPServers {
		unsupported("mcp", s.Name, s.Targets)
	}
	if !ctx.SkipRules {
		for _, r := range m.Rules {
			unsupported("rule", r.Name, r.Targets)
		}
	}
	for _, h := range m.RuntimeHooks {
		unsupported(target.KindRuntimeHook, h.Name(), h.Targets)
	}
	return out, finds, nil
}

// metadata is the warn for a skill's metadata that is not agentskills' map of string keys to
// string values, and whether there is one. The key is rendered as written: the tools differ in
// what they do with it.
func metadata(at target.Finding, fm map[string]any) (target.Finding, bool) {
	v, set := fm["metadata"]
	if !set {
		return target.Finding{}, false
	}
	at.Severity, at.Code, at.Forceable = target.SeverityWarn, "agents/metadata-not-string", false
	m, isMap := v.(map[string]any)
	if !isMap {
		at.Text, at.Hint = "agentskills metadata is a map of string keys to string values", `write metadata: as a map, e.g. metadata: {version: "1"}`
		return at, true
	}
	var bad []string
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if _, ok := m[k].(string); !ok {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return target.Finding{}, false
	}
	verb := "are"
	if len(bad) == 1 {
		verb = "is"
	}
	at.Text = fmt.Sprintf("agentskills metadata values are strings; %s %s not", strings.Join(bad, ", "), verb)
	at.Hint = `quote them in the skill's metadata:, e.g. version: "1"`
	return at, true
}
