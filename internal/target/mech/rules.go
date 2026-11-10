package mech

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// wholeArguments is the note that Claude's whole-argument $ARGUMENTS, unescaped and not
// indexed, becomes {{args}}. A $ is escaped only by exactly one backslash; Claude fills it after
// a doubled one.
func wholeArguments(id string) target.ContentRule {
	return target.ContentRule{Kind: "command", Pattern: regexp.MustCompile(`(?:^|[^\\]|\\\\)\$ARGUMENTS(?:$|[^\[])`), Severity: target.SeverityInfo,
		Code: id + "/arguments", Text: "$ARGUMENTS becomes {{args}}"}
}

// positional reports Claude's single-argument placeholders, which a toml-command target cannot
// fill. Its pattern finds $ARGUMENTS[N] and $N; tomlCommands reports declared $name placeholders,
// which depend on the command's own frontmatter, with the same finding.
func positional(id string) target.ContentRule {
	return target.ContentRule{Kind: "command", Pattern: regexp.MustCompile(`(?:^|[^\\]|\\\\)\$(?:ARGUMENTS\[\d+\]|\d)`),
		Severity: target.SeverityWarn, Code: id + "/positional-arguments",
		Text: id + ` has no positional or named arguments; they are left as written and the prompt starts with "Arguments: {{args}}"`,
		Hint: "write $ARGUMENTS in place of $N, $ARGUMENTS[N] and declared names; " + id + " fills it with the whole argument string"}
}

// rulesDir renders m's rules under env.Entry.Path/<module>. With Concat set, the admitted
// bodies join, in name order and verbatim, into one file of that name; without it, each
// admitted rule is its own <name>.md file, as written. With Register set and anything written,
// the directory (or its glob) is added to the JSON list the target reads rule directories from.
func rulesDir(env Env, m *module.Module) ([]plan.Intent, []target.Finding, error) {
	dir := env.Entry.Path + "/" + m.Name
	var finds []target.Finding
	var bodies []string
	var each []plan.Intent
	for _, r := range m.Rules {
		if !module.Supports(r.Targets, env.ID) {
			continue
		}
		_, f, ok := target.Admit(env.ID, env.Caps, env.Ctx, "rule", r.Name, module.Doc{})
		if finds = append(finds, f...); !ok {
			continue
		}
		if env.Entry.Concat != "" {
			bodies = append(bodies, strings.TrimRight(r.Body, "\n")+"\n")
		} else {
			each = append(each, plan.Intent{Target: env.ID, Kind: "rule", Op: plan.OpWriteFile, Path: dir + "/" + r.Name + ".md",
				Data: []byte(module.Doc{Body: r.Body}.Render()), Mode: 0o644})
		}
	}
	out := each
	if len(bodies) > 0 {
		out = append(out, plan.Intent{Target: env.ID, Kind: "rule", Op: plan.OpWriteFile, Path: dir + "/" + env.Entry.Concat,
			Data: []byte(module.Doc{Body: strings.Join(bodies, "\n")}.Render()), Mode: 0o644})
	}
	if len(out) == 0 {
		return nil, finds, nil
	}
	if reg := env.Entry.Register; reg != nil {
		file, err := Resolve(env.ID, reg.File, env.Ctx.Root)
		if err != nil {
			return nil, nil, err
		}
		member := dir
		if reg.Member == "glob" {
			member = dir + "/*.md"
		}
		if env.Ctx.Scope == target.User {
			member = filepath.Join(env.Ctx.Root, member)
		}
		out = append(out, plan.Intent{Target: env.ID, Kind: "rule", Op: plan.OpAddJSONMember, Path: file, Pointer: reg.List, Member: plan.StringMember(member)})
	}
	return out, finds, nil
}
