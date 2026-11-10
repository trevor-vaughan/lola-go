package mech

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// The agentskills.io limits: tools reading .agents/skills fail to load, or silently drop, a skill
// past them.
const (
	maxName        = 64
	maxDescription = 1024
)

// skillName is agentskills' name pattern. Plan checks it, with the length and the directory, so a
// skill gets one name-rejected finding for every problem with its name.
var skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// agentSkills renders skills into an agentskills.io directory (env.Entry.Path), held to the
// limits tools reading it enforce: they fail to load, or silently drop, a skill past them.
func agentSkills(env Env, m *module.Module) ([]plan.Intent, []target.Finding, error) {
	var out []plan.Intent
	var finds []target.Finding
	for _, s := range m.Skills {
		if !module.Supports(s.Targets, env.ID) {
			continue
		}
		doc, f, ok := target.Admit(env.ID, env.Caps, env.Ctx, "skill", s.Name, s.Doc)
		// The tools that read .agents/skills reject or drop a skill without a description, so
		// --force cannot render one.
		for i := range f {
			if f[i].Code == "required-key-missing" {
				f[i].Severity, f[i].Forceable, f[i].Forced, ok = target.SeveritySkip, false, false, false
			}
		}
		finds = append(finds, f...)
		at := target.Finding{Target: env.ID, Kind: "skill", Artifact: s.Name, Forceable: true, Class: target.ClassDetail, AuthorHint: true}
		skip := func(code, text, hint string) {
			f := at
			f.Code, f.Text, f.Hint = code, text, hint
			f, render := target.Skipped(env.Ctx, f)
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
			skip("name-rejected", env.ID+" needs the skill's name to "+strings.Join(needs, " and ")+
				"; tools that read "+env.Entry.Path+" may reject the skill if --force installs it anyway", strings.Join(fixes, "; "))
		}
		if n := utf8.RuneCountInString(doc.String("description")); n > maxDescription {
			skip("description-too-long", fmt.Sprintf("%s needs a skill description of at most %d characters (it has %d)", env.ID, maxDescription, n),
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
		dst := env.Entry.Path + "/" + s.Name
		out = append(out, target.SkillTree(env.ID, s, env.Ctx, dst), target.WriteText(env.ID, "skill", dst+"/SKILL.md", doc.Render(), env.Ctx))
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
	at.Severity, at.Code, at.Forceable = target.SeverityWarn, at.Target+"/metadata-not-string", false
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
