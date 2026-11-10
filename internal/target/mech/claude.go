package mech

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// claudeComponent admits a skill, command or agent and renders its document, with any
// frontmatter hooks rewritten and gated by consent, as are an agent's inline MCP servers. The
// consent names env.Entry.HookSettings, the settings file whose consent covers them.
func claudeComponent(env Env, kind, name string, d module.Doc, prepare func(module.Doc) module.Doc) (string, *plan.HookConsent, []target.Finding, bool, error) {
	id, settings := env.ID, env.Entry.HookSettings
	doc, finds, ok := target.Admit(id, env.Caps, env.Ctx, kind, name, d)
	if !ok {
		return "", nil, finds, false, nil
	}
	finds = append(finds, allowedTools(id, kind, name, doc)...)
	if kind == "agent" {
		finds = append(finds, permissionMode(id, name, doc)...)
	}
	text, consent, f, ok, err := componentHooks(id, kind, name, prepare(doc), env.Ctx, settings)
	if finds = append(finds, f...); err != nil || !ok || kind != "agent" {
		return text, consent, finds, ok, err
	}
	consent, f, ok, err = agentServers(id, name, doc, text, consent, env.Ctx, settings)
	return text, consent, append(finds, f...), ok, err
}

// claudeFile is the intent that writes component name's rendered text, under its consent.
func claudeFile(id, kind, name, path, text string, consent *plan.HookConsent) plan.Intent {
	return plan.Intent{Target: id, Kind: kind, Name: name, Op: plan.OpWriteFile, Path: path, Data: []byte(text), Mode: 0o644, Consent: consent}
}

// claudeHooks renders m's runtime hook sets into the settings file at env.Entry.File.
func claudeHooks(env Env, m *module.Module) ([]plan.Intent, []target.Finding, error) {
	var out []plan.Intent
	var finds []target.Finding
	settings := env.Entry.File.Path
	for _, h := range m.RuntimeHooks {
		if !module.Supports(h.Targets, env.ID) {
			continue
		}
		_, f, ok := target.Admit(env.ID, env.Caps, env.Ctx, target.KindRuntimeHook, h.Name(), module.Doc{})
		if finds = append(finds, f...); !ok {
			continue
		}
		ins, f, err := hookSet(env.ID, h, env.Ctx, settings)
		if err != nil {
			return nil, nil, err
		}
		out, finds = append(out, ins...), append(finds, f...)
	}
	return out, finds, nil
}

// claudeMCP renders one server in .mcp.json's shape: stdio {command,args,env};
// http {type:"http",url,headers}; sse {type:"sse",url,headers}.
func claudeMCP(id string, s module.MCPServer, ctx target.PlanContext) (map[string]any, []target.Finding) {
	var finds []target.Finding
	v := map[string]any{}
	if s.Transport == module.TransportStdio {
		v["command"] = target.Expand(s.Command, ctx)
		if len(s.Args) > 0 {
			v["args"] = target.ExpandAll(s.Args, ctx)
		}
		if len(s.Env) > 0 {
			v["env"] = target.ExpandMap(s.Env, ctx)
		}
		if s.Cwd != "" {
			finds = append(finds, target.Finding{Severity: target.SeverityWarn, Target: id, Kind: "mcp", Artifact: s.Name, Class: target.ClassDetail,
				Code: "mcp-cwd-dropped", Text: fmt.Sprintf("%s has no working-directory setting; cwd %q dropped", id, s.Cwd)})
		}
		return v, finds
	}
	v["type"] = s.Transport
	v["url"] = target.Expand(s.URL, ctx)
	if len(s.Headers) > 0 {
		v["headers"] = target.ExpandMap(s.Headers, ctx)
	}
	return v, finds
}

// allowedTools warns about tools a component pre-approves: Claude Code runs them without asking
// while the component is active. lola installs the key as written (user decision, 2026-10-10)
// and names the tools, so the user can review them.
func allowedTools(id, kind, name string, doc module.Doc) []target.Finding {
	var out []target.Finding
	for _, k := range slices.Sorted(maps.Keys(doc.Frontmatter)) {
		if !strings.EqualFold(k, "allowed-tools") {
			continue
		}
		var tools []string
		seen := map[string]bool{}
		add := func(t any) {
			s := fmt.Sprint(t)
			if s = untrusted.Text(s, len(s)); !seen[s] {
				seen[s] = true
				tools = append(tools, s)
			}
		}
		switch v := doc.Frontmatter[k].(type) {
		case []any:
			for _, t := range v {
				add(t)
			}
		case nil:
		default:
			add(v)
		}
		if len(tools) == 0 {
			continue
		}
		// Never cut: a cut list could hide a tool such as Bash(*) behind padding.
		out = append(out, target.Finding{Severity: target.SeverityWarn, Target: id, Kind: kind, Artifact: name, Code: id + "/allowed-tools",
			Class: target.ClassSafety, Text: "pre-approves tools Claude Code then runs without asking: " + strings.Join(tools, ", ")})
	}
	return out
}

// permissionMode warns about the permission mode an agent sets for itself: Claude Code runs
// the agent in it unless the main conversation's mode prevails. lola installs the key as
// written and names the mode, so the user can review it.
func permissionMode(id, name string, doc module.Doc) []target.Finding {
	var out []target.Finding
	for _, k := range slices.Sorted(maps.Keys(doc.Frontmatter)) {
		v := doc.Frontmatter[k]
		if !strings.EqualFold(k, "permissionMode") || v == nil {
			continue
		}
		out = append(out, target.Finding{Severity: target.SeverityWarn, Target: id, Kind: "agent", Artifact: name, Code: id + "/permission-mode",
			Class: target.ClassSafety, Text: "sets the permission mode it runs in: " + untrusted.Text(fmt.Sprint(v), untrusted.MaxName) +
				" (Claude Code v2.1.267+ ignores bypassPermissions in a subagent's frontmatter)"})
	}
	return out
}
