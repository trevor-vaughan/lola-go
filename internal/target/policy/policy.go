// Package policy is lola's reading of a target spec's facts: the severities, guards, texts and
// hints of what a target loses. It turns an era into the capability table target.Admit renders
// through.
package policy

import (
	"fmt"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targetspec"
)

// Capabilities is era's capability table for target id. mechRules, the content rules the
// era's mechanisms contribute, follow the era's behaviors and precede its shared fragments.
func Capabilities(id string, era targetspec.Era, mechRules []target.ContentRule) target.Capabilities {
	caps := target.Capabilities{Kinds: map[string]target.KindSpec{}}
	layout := era.Layout(false) // whether hook_settings is set does not depend on scope
	for kind, entry := range layout {
		caps.Kinds[kind] = kindSpec(id, kind, era.Kinds[kind], entry.HookSettings == "")
	}
	for _, b := range era.Behaviors {
		caps.Rules = append(caps.Rules, rule(id, b))
	}
	caps.Rules = append(caps.Rules, mechRules...)
	for _, f := range era.Shared {
		if f == "skill-rules" {
			caps.Rules = append(caps.Rules, target.SkillRules(id)...)
		}
	}
	return caps
}

// consentKeys are the frontmatter keys whose content Claude Code runs; lola writes them only
// through a mechanism that asks consent (a layout entry with hook_settings).
func consentKeys() []string { return []string{"hooks", "mcpServers"} }

// kindSpec is kind's table. ungated is true when the kind's layout entry has no hook_settings, so
// no consent gates its frontmatter.
func kindSpec(id, kind string, k targetspec.Kind, ungated bool) target.KindSpec {
	s := target.KindSpec{Open: k.UnknownKeys == "keep", Required: k.Required}
	if k.Name != nil {
		s.Name = k.Name.RE()
	}
	for name, key := range k.Keys {
		if s.Keys == nil {
			s.Keys = map[string]target.Value{}
		}
		s.Keys[name] = value(id, kind, name, key)
	}
	if ungated && (kind == "skill" || kind == "command" || kind == "agent") {
		for _, ck := range consentKeys() {
			own, ok := s.Keys[ck]
			stated := ok && own.Drop != "" && own.Fold // the spec states why the tool ignores it
			for name := range s.Keys {
				if strings.EqualFold(name, ck) && (!stated || name != ck) {
					delete(s.Keys, name)
				}
			}
			if stated {
				s.Keys[ck] = own
				continue
			}
			if s.Keys == nil {
				s.Keys = map[string]target.Value{}
			}
			s.Keys[ck] = target.Value{Drop: target.SeverityWarn, Fold: true,
				Note: "lola writes hooks and MCP servers into a component only under consent, which " + id + "'s spec does not set up"}
		}
	}
	return s
}

func value(id, kind, name string, k targetspec.Key) target.Value {
	v := target.Value{Fold: k.CaseInsensitive}
	switch {
	case k.ImpliedBy == "file-name":
		v.Drop, v.Note = target.SeverityInfo, "the file name names the "+kind
	case k.Unsupported != "":
		v.Drop, v.Note = target.SeverityWarn, k.Unsupported
	case k.Accepts != nil:
		v.Shape = target.Shape(k.Accepts.Shape)
		if k.Accepts.Pattern != nil {
			v.Pattern = k.Accepts.Pattern.RE()
		}
	}
	if k.AbsentMeans == "unrestricted" {
		if k.Unsupported != "" {
			v.Drop = target.SeveritySkip
		}
		v.Guard = guard(id, kind, name, k)
	}
	return v
}

// guard is the finding for a key without which the artifact is unrestricted: a value the target
// does not keep skips the artifact, forceably (target.Admit).
func guard(id, kind, key string, k targetspec.Key) *target.Finding {
	hint := fmt.Sprintf("under %s: in the %s's .targets.yaml, set %s: null and restrict the %s with %s:", id, kind, key, kind, k.Alternative)
	if k.Example != "" {
		hint = fmt.Sprintf("under %s: in the %s's .targets.yaml, list its %s by %s's names, such as %s", id, kind, key, id, k.Example)
	}
	return &target.Finding{Code: kind + "-" + key + "-unenforceable",
		Text: fmt.Sprintf("%s cannot enforce this %s's %s; without it the %s is unrestricted", id, kind, key, kind), Hint: hint}
}

func rule(id string, b targetspec.Behavior) target.ContentRule {
	r := target.ContentRule{Kind: b.Kind, Pattern: b.Detect.RE(), Code: id + "/" + b.ID}
	switch b.Effect {
	case "sent-as-text":
		r.Severity, r.Forceable = target.SeveritySkip, true
		r.Text = fmt.Sprintf("%s does not run %s; the %s would send it to the model as text", id, b.What, b.Kind)
		r.Hint = "use " + b.Alternative
		if b.Native != "" {
			r.Hint = fmt.Sprintf(`rewrite %s as %s's %s in the %s's .md.liquid template, inside {%% if target == "%s" %%}…{%% else %%}%s{%% endif %%}`,
				b.What, id, b.Native, b.Kind, id, b.What)
		}
	case "runs-without-permission-check":
		r.Severity = target.SeverityWarn
		r.Text = fmt.Sprintf("%s runs %s without Claude's permission checks (allowed-tools is dropped)", id, b.What)
		r.Hint = fmt.Sprintf(`if the shell needs Claude's permission checks, wrap it in {%% if target != "%s" %%}…{%% endif %%} in the %s's .md.liquid template`, id, b.Kind)
	}
	return r
}
