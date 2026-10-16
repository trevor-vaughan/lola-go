// Package claude renders modules for Claude Code (§8).
package claude

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
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
// except that an agent's description must be a string.
var caps = target.Capabilities{Kinds: map[string]target.KindSpec{
	"skill":   {Open: true},
	"command": {Open: true},
	"agent": {Open: true, Keys: map[string]target.Value{"description": {Shape: target.ShapeString}},
		Required: []string{"description"}},
	"mcp":  {},
	"rule": {},
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
	for _, s := range m.Skills {
		if !module.Supports(s.Targets, id) {
			continue
		}
		doc, f, ok := target.Admit(id, caps, ctx, "skill", s.Name, s.Doc)
		if finds = append(finds, f...); !ok {
			continue
		}
		dst := ".claude/skills/" + s.Name
		out = append(out, target.SkillTree(id, s, ctx, dst), target.WriteText(id, "skill", dst+"/SKILL.md", doc.Render(), ctx))
	}
	for _, cmd := range m.Commands {
		if !module.Supports(cmd.Targets, id) {
			continue
		}
		doc, f, ok := target.Admit(id, caps, ctx, "command", cmd.Name, cmd.Doc)
		if finds = append(finds, f...); !ok {
			continue
		}
		out = append(out, target.WriteText(id, "command", ".claude/commands/"+cmd.Name+".md", doc.Render(), ctx))
		if cmd.SidecarDir != "" {
			out = append(out, plan.Intent{Target: id, Kind: "command", Op: plan.OpCopyTree,
				Src: filepath.Join(ctx.ContentDir, cmd.SidecarDir), Path: ".claude/commands/" + cmd.Name})
		}
	}
	for _, a := range m.Agents {
		if !module.Supports(a.Targets, id) {
			continue
		}
		doc, f, ok := target.Admit(id, caps, ctx, "agent", a.Name, a.Doc)
		if finds = append(finds, f...); !ok {
			continue
		}
		if _, ok := doc.Frontmatter["name"]; !ok {
			doc = doc.Prepend("name", a.Name)
		}
		out = append(out, target.WriteText(id, "agent", ".claude/agents/"+a.Name+".md", doc.Render(), ctx))
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
		_, f, ok := target.Admit(id, caps, ctx, "rule", r.Name, module.Doc{})
		if finds = append(finds, f...); !ok {
			continue
		}
		out = append(out, plan.Intent{Target: id, Kind: "rule", Op: plan.OpWriteFile,
			Path: ".claude/rules/lola/" + m.Name + "/" + r.Name + ".md", Data: []byte(r.Body), Mode: 0o644})
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
