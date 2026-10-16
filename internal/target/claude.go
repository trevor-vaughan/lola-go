package target

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
)

// Claude renders into Claude Code's layout (§8).
type Claude struct{}

// ID implements Target.
func (Claude) ID() string { return "claude-code" }

// Root implements Target: the project, or $HOME at user scope.
func (Claude) Root(scope Scope, projectDir string, p paths.Paths) string {
	if scope == User {
		return p.Home
	}
	return projectDir
}

// Detect implements Target.
func (Claude) Detect(root string) bool {
	if fi, err := os.Stat(filepath.Join(root, ".claude")); err == nil && fi.IsDir() {
		return true
	}
	_, err := os.Stat(filepath.Join(root, "CLAUDE.md"))
	return err == nil
}

// BaseDirs implements Target.
func (Claude) BaseDirs(Scope) []string {
	return []string{".claude/skills", ".claude/commands", ".claude/agents", ".claude/lola"}
}

// Plan implements Target.
func (c Claude) Plan(m *module.Module, ctx PlanContext) ([]plan.Intent, []Notice, error) {
	id := c.ID()
	var out []plan.Intent
	var notes []Notice
	text := func(kind, path, body string) plan.Intent {
		return plan.Intent{Target: id, Kind: kind, Op: plan.OpWriteFile, Path: path, Data: []byte(Expand(body, ctx)), Mode: 0o644}
	}
	for _, s := range m.Skills {
		if !module.Supports(s.Targets, id) {
			continue
		}
		dst := ".claude/skills/" + s.Name
		out = append(out,
			plan.Intent{Target: id, Kind: "skill", Op: plan.OpCopyTree, Src: filepath.Join(ctx.ContentDir, s.Dir), Path: dst,
				Exclude: append(slices.Clone(s.Exclude), "SKILL.md")},
			text("skill", dst+"/SKILL.md", s.Doc.Render()))
	}
	for _, cmd := range m.Commands {
		if !module.Supports(cmd.Targets, id) {
			continue
		}
		out = append(out, text("command", ".claude/commands/"+cmd.Name+".md", cmd.Doc.Render()))
		if cmd.SidecarDir != "" {
			out = append(out, plan.Intent{Target: id, Kind: "command", Op: plan.OpCopyTree,
				Src: filepath.Join(ctx.ContentDir, cmd.SidecarDir), Path: ".claude/commands/" + cmd.Name})
		}
	}
	for _, a := range m.Agents {
		if !module.Supports(a.Targets, id) {
			continue
		}
		if a.Doc.String("description") == "" {
			return nil, nil, fmt.Errorf("agent %q: claude-code needs a description in the agent's frontmatter", a.Name)
		}
		doc := a.Doc
		if _, ok := doc.Frontmatter["name"]; !ok {
			doc = doc.Prepend("name", a.Name)
		}
		out = append(out, text("agent", ".claude/agents/"+a.Name+".md", doc.Render()))
	}
	mcpFile := ".mcp.json"
	if ctx.Scope == User {
		mcpFile = ".claude.json"
	}
	for _, s := range m.MCPServers {
		if !module.Supports(s.Targets, id) {
			continue
		}
		v, n := claudeMCP(s, ctx)
		notes = append(notes, n...)
		value, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, plan.Intent{Target: id, Kind: "mcp", Op: plan.OpSetJSON, Path: mcpFile, Pointer: "/mcpServers/" + s.Name, Value: value})
	}
	if m.Instructions != "" {
		if ctx.Scope == User {
			notes = append(notes, Notice{Level: LevelInfo, Text: fmt.Sprintf(
				"%s: instructions are not installed at user scope, because they would apply to every project", m.Name)})
		} else {
			out = append(out, text("instructions", ".claude/lola/"+m.Name+".md", m.Instructions))
		}
	}
	return out, notes, nil
}

func expandAll(in []string, ctx PlanContext) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = Expand(s, ctx)
	}
	return out
}

func expandMap(in map[string]string, ctx PlanContext) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = Expand(v, ctx)
	}
	return out
}

// claudeMCP renders one server in .mcp.json's shape: stdio {command,args,env};
// http {type:"http",url,headers}; sse {type:"sse",url,headers}.
func claudeMCP(s module.MCPServer, ctx PlanContext) (map[string]any, []Notice) {
	var notes []Notice
	v := map[string]any{}
	if s.Transport == module.TransportStdio {
		v["command"] = Expand(s.Command, ctx)
		if len(s.Args) > 0 {
			v["args"] = expandAll(s.Args, ctx)
		}
		if len(s.Env) > 0 {
			v["env"] = expandMap(s.Env, ctx)
		}
		if s.Cwd != "" {
			notes = append(notes, Notice{Level: LevelWarn, Text: fmt.Sprintf(
				"MCP server %s: claude-code has no working-directory setting; cwd %q dropped", s.Name, s.Cwd)})
		}
		return v, notes
	}
	v["type"] = s.Transport
	v["url"] = Expand(s.URL, ctx)
	if len(s.Headers) > 0 {
		v["headers"] = expandMap(s.Headers, ctx)
	}
	return v, notes
}

// Shared implements Target: one CLAUDE.md pointer block at project scope.
func (Claude) Shared(scope Scope, mods []string) []plan.Intent {
	if scope == User || len(mods) == 0 {
		return nil
	}
	lines := make([]string, 0, len(mods))
	for _, m := range slices.Sorted(slices.Values(mods)) {
		lines = append(lines, "@.claude/lola/"+m+".md")
	}
	return []plan.Intent{{Target: "claude-code", Kind: plan.KindPointerBlock, Op: plan.OpPointerBlock, Path: "CLAUDE.md", Lines: lines}}
}
