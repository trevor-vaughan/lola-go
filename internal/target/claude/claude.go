// Package claude renders modules for Claude Code (§8).
package claude

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/untrusted"
)

// Target renders into Claude Code's layout.
type Target struct{}

// ID implements Target.
func (Target) ID() string { return "claude-code" }

// Root implements Target: the project, or $HOME at user scope.
func (Target) Root(scope target.Scope, projectDir string, p paths.Paths) string {
	if scope == target.User {
		return p.Home
	}
	return projectDir
}

// Detect implements Target.
func (Target) Detect(_ target.Scope, root string) bool {
	return target.HasAny(root, ".claude", "CLAUDE.md")
}

// BaseDirs implements Target.
func (Target) BaseDirs(target.Scope) []string {
	return []string{".claude/skills", ".claude/commands", ".claude/agents", ".claude/rules"}
}

// caps: Claude Code takes every artifact kind and keeps every frontmatter key as authored,
// except that an agent's description must be a string. It is the only target with runtime
// hooks (Plan 5b design §7.1).
var caps = target.Capabilities{Kinds: map[string]target.KindSpec{
	"skill":   {Open: true},
	"command": {Open: true},
	"agent": {Open: true, Keys: map[string]target.Value{"description": {Shape: target.ShapeString}},
		Required: []string{"description"}},
	"mcp":                  {},
	"rule":                 {},
	target.KindRuntimeHook: {},
}}

// Capabilities implements Target.
func (Target) Capabilities() target.Capabilities { return caps }

// probe: `claude --version` prints e.g. "2.1.287 (Claude Code)".
var probe = target.Probe{Binaries: []string{"claude"}, VersionArgs: []string{"--version"},
	VersionRE: regexp.MustCompile(`^(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)`)}

// Probe implements Target.
func (Target) Probe() target.Probe { return probe }

// Plan implements Target.
func (c Target) Plan(m *module.Module, ctx target.PlanContext) ([]plan.Intent, []target.Finding, error) {
	id := c.ID()
	var out []plan.Intent
	var finds []target.Finding
	// Runtime hooks go to the per-user settings file (Plan 5b design §1, §7.2); its consent also
	// covers the hooks in a component's frontmatter.
	settings := ".claude/settings.local.json"
	if ctx.Scope == target.User {
		settings = ".claude/settings.json"
	}
	// component admits a skill, command or agent and renders its document, with any
	// frontmatter hooks rewritten and gated by consent, as are an agent's inline MCP servers.
	component := func(kind, name string, d module.Doc, prepareDoc func(module.Doc) module.Doc) (string, *plan.HookConsent, bool, error) {
		doc, f, ok := target.Admit(id, caps, ctx, kind, name, d)
		if finds = append(finds, f...); !ok {
			return "", nil, false, nil
		}
		finds = append(finds, allowedTools(id, kind, name, doc)...)
		if kind == "agent" {
			finds = append(finds, permissionMode(id, name, doc)...)
		}
		text, consent, f, ok, err := componentHooks(id, kind, name, prepareDoc(doc), ctx, settings)
		if finds = append(finds, f...); err != nil || !ok || kind != "agent" {
			return text, consent, ok, err
		}
		consent, f, ok, err = agentServers(id, name, doc, text, consent, ctx, settings)
		finds = append(finds, f...)
		return text, consent, ok, err
	}
	same := func(d module.Doc) module.Doc { return d }
	file := func(kind, path, text string, consent *plan.HookConsent) plan.Intent {
		return plan.Intent{Target: id, Kind: kind, Op: plan.OpWriteFile, Path: path, Data: []byte(text), Mode: 0o644, Consent: consent}
	}
	for _, s := range m.Skills {
		if !module.Supports(s.Targets, id) {
			continue
		}
		text, consent, ok, err := component("skill", s.Name, s.Doc, same)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		dst := ".claude/skills/" + s.Name
		tree := target.SkillTree(id, s, ctx, dst)
		tree.Consent = consent
		out = append(out, tree, file("skill", dst+"/SKILL.md", text, consent))
	}
	for _, cmd := range m.Commands {
		if !module.Supports(cmd.Targets, id) {
			continue
		}
		text, consent, ok, err := component("command", cmd.Name, cmd.Doc, same)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		out = append(out, file("command", ".claude/commands/"+cmd.Name+".md", text, consent))
		for _, sc := range cmd.Sidecars {
			finds = append(finds, allowedTools(id, "command", untrusted.Text(sc.File, untrusted.MaxName), sc.Doc)...)
		}
		if cmd.SidecarDir != "" {
			out = append(out, plan.Intent{Target: id, Kind: "command", Op: plan.OpCopyTree,
				Src: filepath.Join(ctx.ContentDir, cmd.SidecarDir), Path: ".claude/commands/" + cmd.Name, Consent: consent})
		}
	}
	for _, a := range m.Agents {
		if !module.Supports(a.Targets, id) {
			continue
		}
		text, consent, ok, err := component("agent", a.Name, a.Doc, func(doc module.Doc) module.Doc {
			if _, ok := doc.Frontmatter["name"]; !ok {
				doc = doc.Prepend("name", a.Name)
			}
			return doc
		})
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		out = append(out, file("agent", ".claude/agents/"+a.Name+".md", text, consent))
	}
	mcpFile := ".mcp.json"
	if ctx.Scope == target.User {
		mcpFile = ".claude.json"
	}
	for _, s := range m.MCPServers {
		if !module.Supports(s.Targets, id) {
			continue
		}
		_, f, ok := target.Admit(id, caps, ctx, "mcp", s.Name, module.Doc{})
		if finds = append(finds, f...); !ok {
			continue
		}
		v, f := claudeMCP(id, s, ctx)
		finds = append(finds, f...)
		value, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, plan.Intent{Target: id, Kind: "mcp", Op: plan.OpSetJSON, Path: mcpFile, Pointer: "/mcpServers/" + s.Name, Value: value})
	}
	// Claude Code loads every .claude/rules/**/*.md without paths: frontmatter in every
	// session, at project and user scope, so rules never touch CLAUDE.md (§5.6).
	for _, r := range m.Rules {
		if ctx.SkipRules {
			break
		}
		if !module.Supports(r.Targets, id) {
			continue
		}
		_, f, ok := target.Admit(id, caps, ctx, "rule", r.Name, module.Doc{})
		if finds = append(finds, f...); !ok {
			continue
		}
		out = append(out, plan.Intent{Target: id, Kind: "rule", Op: plan.OpWriteFile,
			Path: ".claude/rules/lola/" + m.Name + "/" + r.Name + ".md", Data: []byte(module.Doc{Body: r.Body}.Render()), Mode: 0o644})
	}
	for _, h := range m.RuntimeHooks {
		if !module.Supports(h.Targets, id) {
			continue
		}
		_, f, ok := target.Admit(id, caps, ctx, target.KindRuntimeHook, h.Name(), module.Doc{})
		if finds = append(finds, f...); !ok {
			continue
		}
		ins, f, err := hookSet(id, h, ctx, settings)
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
			finds = append(finds, target.Finding{Severity: target.SeverityWarn, Target: id, Kind: "mcp", Artifact: s.Name,
				Code: "mcp-cwd-dropped", Text: fmt.Sprintf("claude-code has no working-directory setting; cwd %q dropped", s.Cwd)})
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
		out = append(out, target.Finding{Severity: target.SeverityWarn, Target: id, Kind: kind, Artifact: name, Code: "claude-code/allowed-tools",
			Text: "pre-approves tools Claude Code then runs without asking: " + strings.Join(tools, ", ")})
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
		out = append(out, target.Finding{Severity: target.SeverityWarn, Target: id, Kind: "agent", Artifact: name, Code: "claude-code/permission-mode",
			Text: "sets the permission mode it runs in: " + untrusted.Text(fmt.Sprint(v), untrusted.MaxName) +
				" (Claude Code v2.1.267+ ignores bypassPermissions in a subagent's frontmatter)"})
	}
	return out
}
