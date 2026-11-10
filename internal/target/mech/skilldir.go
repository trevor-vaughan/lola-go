package mech

import (
	"fmt"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// skillDir renders skills into a directory of their own (env.Entry.Path). A target that also
// reads .agents/skills (reads_shared_skills) gets one copy: a skill already installed there is
// reported, not rendered again, or the target warns of a conflict.
func skillDir(env Env, m *module.Module) ([]plan.Intent, []target.Finding, error) {
	var out []plan.Intent
	var finds []target.Finding
	for _, s := range m.Skills {
		if !module.Supports(s.Targets, env.ID) {
			continue
		}
		if env.Entry.ReadsSharedSkills && env.Ctx.SharedSkills[s.Name] {
			finds = append(finds, target.SkillViaAgents(env.ID, s.Name))
			continue
		}
		if env.Entry.HookSettings != "" {
			text, consent, f, ok, err := claudeComponent(env, "skill", s.Name, s.Doc, func(d module.Doc) module.Doc { return d })
			if finds = append(finds, f...); err != nil {
				return nil, nil, err
			}
			if !ok {
				continue
			}
			dst := env.Entry.Path + "/" + s.Name
			tree := target.SkillTree(env.ID, s, env.Ctx, dst)
			tree.Consent = consent
			out = append(out, tree, claudeFile(env.ID, "skill", dst+"/SKILL.md", text, consent))
			continue
		}
		doc, f, ok := target.Admit(env.ID, env.Caps, env.Ctx, "skill", s.Name, s.Doc)
		finds = append(finds, f...)
		if name, set := doc.Frontmatter["name"]; set && name != s.Name && env.Entry.NameMatchesDir {
			f, render := target.Skipped(env.Ctx, target.Finding{Target: env.ID, Kind: "skill", Artifact: s.Name, Code: "name-rejected",
				Text: fmt.Sprintf("%s needs the skill's name (%v) to equal its directory name", env.ID, name),
				Hint: fmt.Sprintf("rename the directory to %v, or set name: %s", name, s.Name), Forceable: true, Class: target.ClassDetail, AuthorHint: true})
			finds, ok = append(finds, f), ok && render
		}
		if !ok {
			continue
		}
		doc = withName(doc, s.Name, env)
		dst := env.Entry.Path + "/" + s.Name
		out = append(out, target.SkillTree(env.ID, s, env.Ctx, dst), target.WriteText(env.ID, "skill", dst+"/SKILL.md", doc.Render(), env.Ctx))
	}
	return out, finds, nil
}
