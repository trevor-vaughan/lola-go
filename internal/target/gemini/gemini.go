// Package gemini renders modules for Gemini CLI (§8, §8.1).
package gemini

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// Target renders into Gemini CLI's layout.
type Target struct{}

// ID implements Target.
func (Target) ID() string { return "gemini-cli" }

// Root implements Target: the project, or $HOME at user scope.
func (Target) Root(scope target.Scope, projectDir string, p paths.Paths) string {
	if scope == target.User {
		return p.Home
	}
	return projectDir
}

// Detect implements Target.
func (Target) Detect(_ target.Scope, root string) bool {
	return target.HasAny(root, ".gemini", "GEMINI.md")
}

// BaseDirs implements Target. .gemini/lola/ is lola's own and goes once it is empty.
func (Target) BaseDirs(target.Scope) []string {
	return []string{".gemini/skills", ".gemini/commands", ".gemini/agents", ".gemini"}
}

var agentName = regexp.MustCompile(`^[a-z0-9_-]+$`)

// positional reports Claude's single-argument placeholders, which Gemini cannot fill. Its
// pattern finds $ARGUMENTS[N] and $N; Plan reports declared $name placeholders, which depend
// on the command's own frontmatter, with the same finding.
var positional = target.ContentRule{Kind: "command", Pattern: regexp.MustCompile(`(?:^|[^\\]|\\\\)\$(?:ARGUMENTS\[\d+\]|\d)`),
	Severity: target.SeverityWarn, Code: "gemini-cli/positional-arguments",
	Text: `gemini-cli has no positional or named arguments; they are left as written and the prompt starts with "Arguments: {{args}}"`,
	Hint: "use $ARGUMENTS"}

// toolName is Gemini's tool naming: lowercase names joined by single underscores, like its
// built-in (read_file), MCP (mcp_server_tool) and wildcard (*) names. Its strict agent schema
// refuses an agent over one name it does not know, such as Claude's Read or mcp__server__tool.
var toolName = regexp.MustCompile(`^[a-z0-9*-]+(_[a-z0-9*-]+)*$`)

// caps: Gemini CLI reads a closed set of agent and command keys, needs descriptions on skills
// and agents, and runs shell in commands only with its own !{…} syntax. Agent values have the
// shapes of Gemini's localAgentSchema, which rejects the agent on any mismatch.
var caps = target.Capabilities{
	Kinds: map[string]target.KindSpec{
		"skill": {Open: true, Keys: map[string]target.Value{"description": {Shape: target.ShapeString}},
			Required: []string{"description"}},
		"command": {Keys: map[string]target.Value{"description": {Shape: target.ShapeString}}},
		"agent": {Keys: map[string]target.Value{
			"name":         {Shape: target.ShapeString, Pattern: agentName},
			"description":  {Shape: target.ShapeString},
			"kind":         {Shape: target.ShapeString, Pattern: regexp.MustCompile(`^(local|remote)$`)},
			"display_name": {Shape: target.ShapeString},
			"tools":        {Shape: target.ShapeStringList, Pattern: toolName, Guard: target.ToolsGuard("gemini-cli", "list tools by gemini-cli's names")},
			"model":        {Shape: target.ShapeString, Pattern: regexp.MustCompile(`^gemini-`)},
			"temperature":  {Shape: target.ShapeNumber},
			"max_turns":    {Shape: target.ShapePositiveInt},
			"timeout_mins": {Shape: target.ShapePositiveInt},
		}, Required: []string{"description"}, Name: agentName},
		"mcp":  {},
		"rule": {},
	},
	Rules: append([]target.ContentRule{
		{Kind: "command", Pattern: target.Shell, Severity: target.SeveritySkip, Forceable: true,
			Code: "gemini-cli/shell-injection", Text: "gemini-cli does not run !`…`; the command would send it to the model as text",
			Hint: "Gemini runs shell with !{…}"},
		// Claude's whole-argument $ARGUMENTS, unescaped and not indexed, is Gemini's {{args}}. A $
		// is escaped only by exactly one backslash; Claude fills it after a doubled one.
		{Kind: "command", Pattern: regexp.MustCompile(`(?:^|[^\\]|\\\\)\$ARGUMENTS(?:$|[^\[])`), Severity: target.SeverityInfo,
			Code: "gemini-cli/arguments", Text: "$ARGUMENTS becomes {{args}}"},
		positional,
	}, target.SkillRules("gemini-cli")...),
}

// Capabilities implements Target.
func (Target) Capabilities() target.Capabilities { return caps }

// probe: `gemini --version` prints a bare version, e.g. "0.63.0".
var probe = target.Probe{Binaries: []string{"gemini"}, VersionArgs: []string{"--version"},
	VersionRE: regexp.MustCompile(`^(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)`)}

// Probe implements Target.
func (Target) Probe() target.Probe { return probe }

// Plan implements Target.
func (g Target) Plan(m *module.Module, ctx target.PlanContext) ([]plan.Intent, []target.Finding, error) {
	id := g.ID()
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
		if _, ok := doc.Frontmatter["name"]; !ok {
			doc = doc.Prepend("name", s.Name)
		}
		dst := ".gemini/skills/" + s.Name
		out = append(out, target.SkillTree(id, s, ctx, dst), target.WriteText(id, "skill", dst+"/SKILL.md", doc.Render(), ctx))
	}
	for _, cmd := range m.Commands {
		if !module.Supports(cmd.Targets, id) {
			continue
		}
		doc, f, ok := target.Admit(id, caps, ctx, "command", cmd.Name, cmd.Doc)
		// Gemini has no $ escape, so an escaped placeholder loses its backslash: \$1.00 is $1.00.
		body, indexed := target.Arguments(cmd.Doc, func(p string, i int) string {
			switch {
			case strings.HasPrefix(p, `\`):
				return p[1:]
			case i < 0:
				return "{{args}}"
			}
			return p
		})
		if indexed && !slices.ContainsFunc(f, func(x target.Finding) bool { return x.Code == positional.Code }) {
			f = append(f, target.Finding{Severity: positional.Severity, Target: id, Kind: "command", Artifact: cmd.Name,
				Code: positional.Code, Text: positional.Text, Hint: positional.Hint})
		}
		if finds = append(finds, f...); !ok {
			continue
		}
		doc.Body = body
		data, err := command(doc, indexed, ctx)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, plan.Intent{Target: id, Kind: "command", Op: plan.OpWriteFile, Path: ".gemini/commands/" + cmd.Name + ".toml", Data: data, Mode: 0o644})
		if cmd.SidecarDir != "" {
			out = append(out, plan.Intent{Target: id, Kind: "command", Op: plan.OpCopyTree,
				Src: filepath.Join(ctx.ContentDir, cmd.SidecarDir), Path: ".gemini/commands/" + cmd.Name})
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
		out = append(out, target.WriteText(id, "agent", ".gemini/agents/"+a.Name+".md", doc.Render(), ctx))
	}
	for _, s := range m.MCPServers {
		if !module.Supports(s.Targets, id) {
			continue
		}
		_, f, ok := target.Admit(id, caps, ctx, "mcp", s.Name, module.Doc{})
		if finds = append(finds, f...); !ok {
			continue
		}
		value, err := json.Marshal(mcp(s, ctx))
		if err != nil {
			return nil, nil, err
		}
		out = append(out, plan.Intent{Target: id, Kind: "mcp", Op: plan.OpSetJSON, Path: ".gemini/settings.json", Pointer: "/mcpServers/" + s.Name, Value: value})
	}
	// Gemini has no rules directory: a module's rules become one GEMINI.md, in name order,
	// in a directory listed in context.includeDirectories, which Gemini reads in trusted
	// folders (§8). Rules are delivered verbatim.
	var bodies []string
	for _, r := range m.Rules {
		if ctx.SkipRules {
			break
		}
		_, f, ok := target.Admit(id, caps, ctx, "rule", r.Name, module.Doc{})
		if finds = append(finds, f...); ok {
			bodies = append(bodies, strings.TrimRight(r.Body, "\n")+"\n")
		}
	}
	if len(bodies) > 0 {
		dir := ".gemini/lola/" + m.Name
		member := dir
		if ctx.Scope == target.User {
			member = filepath.Join(ctx.Root, dir)
		}
		out = append(out,
			plan.Intent{Target: id, Kind: "rule", Op: plan.OpWriteFile, Path: dir + "/GEMINI.md", Data: []byte(strings.Join(bodies, "\n")), Mode: 0o644},
			plan.Intent{Target: id, Kind: "rule", Op: plan.OpAddJSONMember, Path: ".gemini/settings.json", Pointer: "/context/includeDirectories", Member: member})
	}
	return out, finds, nil
}

// command encodes a command as Gemini TOML (§8.1): description from the frontmatter and the
// expanded body as prompt. When the body has single-argument placeholders (indexed), which
// Gemini cannot fill, the prompt starts with the whole argument string.
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

// mcp renders one server in Gemini's settings.json shape: stdio {command,args,env,cwd};
// http {httpUrl,headers}; sse {url,headers}.
func mcp(s module.MCPServer, ctx target.PlanContext) map[string]any {
	v := map[string]any{}
	switch s.Transport {
	case module.TransportStdio:
		v["command"] = target.Expand(s.Command, ctx)
		if len(s.Args) > 0 {
			v["args"] = target.ExpandAll(s.Args, ctx)
		}
		if len(s.Env) > 0 {
			v["env"] = target.ExpandMap(s.Env, ctx)
		}
		if s.Cwd != "" {
			v["cwd"] = target.Expand(s.Cwd, ctx)
		}
		return v
	case module.TransportHTTP:
		v["httpUrl"] = target.Expand(s.URL, ctx)
	default:
		v["url"] = target.Expand(s.URL, ctx)
	}
	if len(s.Headers) > 0 {
		v["headers"] = target.ExpandMap(s.Headers, ctx)
	}
	return v
}
