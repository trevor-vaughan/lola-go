package mech

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// escaped is a Claude-escaped placeholder, whose $ follows exactly one backslash, that a target
// with no escape still replaces.
var escaped = regexp.MustCompile(`(?:^|[^\\])\\\$(?:\d|ARGUMENTS)`)

// markdownFiles renders m's artifacts of kind (agents or commands) as one Markdown file each,
// named for the artifact under env.Entry.Path with env.Entry.Ext, admitted as artifacts of
// kind. An agent gets env.Entry.Defaults for the frontmatter keys it leaves unset; a command's
// arguments follow env.Entry.Arguments and its sidecar directory env.Entry.Sidecar.
func markdownFiles(kind string, env Env, m *module.Module) ([]plan.Intent, []target.Finding, error) {
	var out []plan.Intent
	var finds []target.Finding
	if kind == "command" {
		for _, cmd := range m.Commands {
			if !module.Supports(cmd.Targets, env.ID) {
				continue
			}
			if env.Entry.HookSettings != "" {
				text, consent, f, ok, err := claudeComponent(env, kind, cmd.Name, cmd.Doc, func(d module.Doc) module.Doc { return d })
				if finds = append(finds, f...); err != nil {
					return nil, nil, err
				}
				if !ok {
					continue
				}
				out = append(out, claudeFile(env.ID, kind, env.Entry.Path+"/"+cmd.Name+env.Entry.Ext, text, consent))
				for _, sc := range cmd.Sidecars {
					finds = append(finds, allowedTools(env.ID, kind, untrusted.Text(sc.File, untrusted.MaxName), sc.Doc)...)
				}
				if cmd.SidecarDir != "" && env.Entry.Sidecar == "copy" {
					out = append(out, target.SidecarTree(env.ID, cmd, env.Ctx, env.Entry.Path+"/"+cmd.Name, consent))
				}
				continue
			}
			doc, f, ok := target.Admit(env.ID, env.Caps, env.Ctx, kind, cmd.Name, cmd.Doc)
			if finds = append(finds, f...); !ok {
				continue
			}
			if env.Entry.Arguments == "one-based" {
				var af []target.Finding
				doc.Body, af = oneBased(env.ID, cmd)
				finds = append(finds, af...)
			}
			out = append(out, target.WriteText(env.ID, kind, env.Entry.Path+"/"+cmd.Name+env.Entry.Ext, withName(doc, cmd.Name, env).Render(), env.Ctx))
			if cmd.SidecarDir == "" {
				continue
			}
			switch env.Entry.Sidecar {
			case "copy":
				out = append(out, target.SidecarTree(env.ID, cmd, env.Ctx, env.Entry.Path+"/"+cmd.Name, nil))
			case "drop":
				finds = append(finds, target.Finding{Severity: target.SeverityWarn, Target: env.ID, Kind: kind, Artifact: cmd.Name,
					Class: target.ClassDetail, Code: "command-sidecar-dropped", Text: fmt.Sprintf("%s would read the files in %s/ as commands; the sidecar directory is not copied", env.ID, cmd.SidecarDir)})
			}
		}
		return out, finds, nil
	}
	for _, a := range m.Agents {
		if !module.Supports(a.Targets, env.ID) {
			continue
		}
		if env.Entry.HookSettings != "" {
			text, consent, f, ok, err := claudeComponent(env, kind, a.Name, a.Doc, func(d module.Doc) module.Doc { return withName(d, a.Name, env) })
			if finds = append(finds, f...); err != nil {
				return nil, nil, err
			}
			if ok {
				out = append(out, claudeFile(env.ID, kind, env.Entry.Path+"/"+a.Name+env.Entry.Ext, text, consent))
			}
			continue
		}
		doc, f, ok := target.Admit(env.ID, env.Caps, env.Ctx, kind, a.Name, a.Doc)
		if finds = append(finds, f...); !ok {
			continue
		}
		doc = withName(doc, a.Name, env)
		for _, k := range slices.Sorted(maps.Keys(env.Entry.Defaults)) {
			if _, set := doc.Frontmatter[k]; !set {
				doc = doc.Prepend(k, env.Entry.Defaults[k])
			}
		}
		out = append(out, target.WriteText(env.ID, kind, env.Entry.Path+"/"+a.Name+env.Entry.Ext, doc.Render(), env.Ctx))
	}
	return out, finds, nil
}

// withName is doc with its name prepended when the entry says the target needs one and the
// artifact has none.
func withName(doc module.Doc, name string, env Env) module.Doc {
	if _, ok := doc.Frontmatter["name"]; !ok && env.Entry.PrependName {
		return doc.Prepend("name", name)
	}
	return doc
}

// oneBased is cmd's body with Claude's 0-based argument placeholders renumbered to 1-based
// ($0 and $ARGUMENTS[0] become $1), and the findings that say so and that the target has no
// escape for $.
func oneBased(id string, cmd module.Command) (string, []target.Finding) {
	body, _ := target.Arguments(cmd.Doc, func(p string, i int) string {
		if i < 0 {
			return p
		}
		return "$" + strconv.Itoa(i+1)
	})
	var finds []target.Finding
	at := target.Finding{Severity: target.SeverityWarn, Target: id, Kind: "command", Artifact: cmd.Name, Class: target.ClassDetail}
	if body != cmd.Doc.Body {
		f := at
		f.Code, f.Text = id+"/arguments-renumbered", "Claude's 0-based argument placeholders became "+id+"'s 1-based $1, $2, …"
		f.Hint = "check the command with a few arguments; " + id + " may split or join them differently"
		finds = append(finds, f)
	}
	if escaped.MatchString(body) {
		f := at
		f.Code, f.Text = id+"/escaped-arguments", id+` has no escape for $; \$1 and \$ARGUMENTS will still be replaced by arguments`
		f.Hint, f.AuthorHint = "reword the text so it does not contain $ followed by a digit or ARGUMENTS", true
		finds = append(finds, f)
	}
	return body, finds
}
