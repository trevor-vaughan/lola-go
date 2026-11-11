package mech

import (
	"bytes"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// tomlCommands renders m's commands as TOML files, one per command under env.Entry.Path, whose
// prompt is the expanded body. The target has no positional or named arguments: $ARGUMENTS
// becomes {{args}} and the other placeholders are left as written.
func tomlCommands(env Env, m *module.Module) ([]plan.Intent, []target.Finding, error) {
	var out []plan.Intent
	var finds []target.Finding
	for _, cmd := range m.Commands {
		if !module.Supports(cmd.Targets, env.ID) {
			continue
		}
		doc, f, ok := target.Admit(env.ID, env.Caps, env.Ctx, "command", cmd.Name, cmd.Doc)
		// There is no $ escape, so an escaped placeholder loses its backslash: \$1.00 is $1.00.
		body, indexed := target.Arguments(cmd.Doc, func(p string, i int) string {
			switch {
			case strings.HasPrefix(p, `\`):
				return p[1:]
			case i < 0:
				return "{{args}}"
			}
			return p
		})
		pos := positional(env.ID)
		if indexed && !slices.ContainsFunc(f, func(x target.Finding) bool { return x.Code == pos.Code }) {
			f = append(f, target.Finding{Severity: pos.Severity, Target: env.ID, Kind: "command", Artifact: cmd.Name,
				Code: pos.Code, Text: pos.Text, Hint: pos.Hint, Class: target.ClassDetail, AuthorHint: true})
		}
		if finds = append(finds, f...); !ok {
			continue
		}
		doc.Body = body
		data, err := command(doc, indexed, env.Ctx)
		if err != nil {
			return nil, nil, err
		}
		dst := env.Entry.Path + "/" + cmd.Name
		out = append(out, plan.Intent{Target: env.ID, Kind: "command", Name: cmd.Name, Op: plan.OpWriteFile, Path: dst + env.Entry.Ext, Data: data, Mode: 0o644})
		if cmd.SidecarDir != "" && env.Entry.Sidecar == "copy" {
			out = append(out, target.SidecarTree(env.ID, cmd, env.Ctx, dst, nil))
		}
	}
	return out, finds, nil
}

// command encodes a command as TOML: description from the frontmatter and the expanded body as
// prompt. When the body has single-argument placeholders (indexed), which the target cannot
// fill, the prompt starts with the whole argument string.
func command(doc module.Doc, indexed bool, ctx target.PlanContext) ([]byte, error) {
	prompt := target.Expand(doc.Body, ctx)
	if indexed {
		prompt = "Arguments: {{args}}\n\n" + prompt
	}
	var buf bytes.Buffer
	err := toml.NewEncoder(&buf).Encode(struct {
		Description string `toml:"description,omitempty"`
		Prompt      string `toml:"prompt"`
	}{doc.String("description"), prompt})
	return buf.Bytes(), err
}
