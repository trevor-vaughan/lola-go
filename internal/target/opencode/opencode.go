// Package opencode renders modules for OpenCode (§8). At user scope its root is
// $XDG_CONFIG_HOME/opencode and artifact directories sit directly under it.
package opencode

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/paths"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
)

// Target renders into OpenCode's layout.
type Target struct{}

// ID implements Target.
func (Target) ID() string { return "opencode" }

// Root implements Target: the project, or $XDG_CONFIG_HOME/opencode at user scope.
func (Target) Root(scope target.Scope, projectDir string, p paths.Paths) string {
	if scope == target.User {
		return filepath.Join(p.ConfigHome, "opencode")
	}
	return projectDir
}

// Detect implements Target. At user scope root is OpenCode's own config directory, which
// exists once OpenCode is set up even without an opencode.json.
func (Target) Detect(scope target.Scope, root string) bool {
	if scope == target.User {
		fi, err := os.Stat(root)
		return err == nil && fi.IsDir()
	}
	return target.HasAny(root, ".opencode", "opencode.json", "opencode.jsonc")
}

// dir is the root-relative directory that holds OpenCode's artifact directories.
func dir(scope target.Scope) string {
	if scope == target.User {
		return ""
	}
	return ".opencode/"
}

// BaseDirs implements Target. At project scope .opencode/lola/ is lola's own and goes once
// it is empty; at user scope lola/ sits beside the artifact directories and stays.
func (Target) BaseDirs(scope target.Scope) []string {
	if scope == target.User {
		return []string{"skills", "commands", "agents", "lola"}
	}
	return []string{".opencode/skills", ".opencode/commands", ".opencode/agents", ".opencode"}
}

// escaped is a Claude-escaped placeholder, whose $ follows exactly one backslash, that OpenCode,
// which has no escape, still replaces.
var escaped = regexp.MustCompile(`(?:^|[^\\])\\\$(?:\d|ARGUMENTS)`)

// model is OpenCode's provider/model form.
var model = target.Value{Shape: target.ShapeString, Pattern: regexp.MustCompile(`^[^/]+/.+$`)}

// color is a hex color, the one form every OpenCode version accepts: 1.18 also takes theme
// names such as accent, but OpenCode V2 rejects them and drops the agent (anomalyco/opencode
// #54057, #38796), and rendered output must not depend on the installed version.
var color = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// steps is a positive integer, as OpenCode's agent schema requires of steps and maxSteps.
var steps = target.Value{Shape: target.ShapePositiveInt}

// caps: OpenCode needs descriptions on skills and agents and kebab-case skill names, reads a
// closed set of command keys, names an agent by its file and configures its tools with
// permissions. It refuses to start on an agent value that does not match its config schema
// (https://opencode.ai/config.json, AgentConfig), so each key it checks has the shape it needs;
// permission's nested form has no Value shape and passes unchecked.
var caps = target.Capabilities{Kinds: map[string]target.KindSpec{
	"skill": {Open: true, Keys: map[string]target.Value{"description": {Shape: target.ShapeString}},
		Required: []string{"description"}, Name: regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)},
	"command": {Keys: map[string]target.Value{
		"description": {Shape: target.ShapeString},
		"agent":       {Shape: target.ShapeString},
		"model":       model,
		"subtask":     {Shape: target.ShapeBool},
	}},
	"agent": {Open: true, Keys: map[string]target.Value{
		"description": {Shape: target.ShapeString},
		"name":        {Drop: target.SeverityInfo, Note: "the file name names the agent"},
		"tools":       {Drop: target.SeveritySkip, Guard: target.ToolsGuard("opencode", "remove tools: (OpenCode restricts an agent with permission:)")}, // opencode configures tools with permissions
		"model":       model,
		"variant":     {Shape: target.ShapeString},
		"mode":        {Shape: target.ShapeString, Pattern: regexp.MustCompile(`^(subagent|primary|all)$`)},
		"color":       {Shape: target.ShapeString, Pattern: color},
		"temperature": {Shape: target.ShapeNumber},
		"top_p":       {Shape: target.ShapeNumber},
		"steps":       steps,
		"maxSteps":    steps,
		"hidden":      {Shape: target.ShapeBool},
		"disable":     {Shape: target.ShapeBool},
		"options":     {Shape: target.ShapeMap},
	}, Required: []string{"description"}},
	"mcp":  {},
	"rule": {},
}, Rules: append([]target.ContentRule{
	// OpenCode's prompt.ts runs every !`…` in a command (SHELL_REGEX /!`([^`]+)`/g), wherever it
	// sits, with no permission check; Claude's ```! fence it never runs.
	{Kind: "command", Pattern: regexp.MustCompile("!`[^`]+`"), Severity: target.SeverityWarn,
		Code: "opencode/shell-unchecked", Text: "OpenCode runs !`…` without Claude's permission checks (allowed-tools is dropped)",
		Hint: "review the command before installing"},
	{Kind: "command", Pattern: regexp.MustCompile("(?m)^[ \\t]*`{3,}!"), Severity: target.SeveritySkip, Forceable: true,
		Code: "opencode/shell-fence", Text: "OpenCode does not run fenced shell; the command would send it to the model as text",
		Hint: "use inline !`…`"},
}, target.SkillRules("opencode")...)}

// Capabilities implements Target.
func (Target) Capabilities() target.Capabilities { return caps }

// probe: `opencode --version` prints a bare version, e.g. "1.0.12".
var probe = target.Probe{Binaries: []string{"opencode"}, VersionArgs: []string{"--version"},
	VersionRE: regexp.MustCompile(`^v?(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)`)}

// Probe implements Target.
func (Target) Probe() target.Probe { return probe }

// Plan implements Target.
func (o Target) Plan(m *module.Module, ctx target.PlanContext) ([]plan.Intent, []target.Finding, error) {
	id, d := o.ID(), dir(ctx.Scope)
	var out []plan.Intent
	var finds []target.Finding
	for _, s := range m.Skills {
		if !module.Supports(s.Targets, id) {
			continue
		}
		doc, f, ok := target.Admit(id, caps, ctx, "skill", s.Name, s.Doc)
		finds = append(finds, f...)
		if name, set := doc.Frontmatter["name"]; set && name != s.Name {
			f, render := target.Skipped(ctx, target.Finding{Target: id, Kind: "skill", Artifact: s.Name, Code: "name-rejected",
				Text: fmt.Sprintf("opencode needs the skill's name (%v) to equal its directory name", name),
				Hint: fmt.Sprintf("rename the directory to %v, or set name: %s", name, s.Name), Forceable: true})
			finds, ok = append(finds, f), ok && render
		}
		if !ok {
			continue
		}
		if _, set := doc.Frontmatter["name"]; !set {
			doc = doc.Prepend("name", s.Name)
		}
		dst := d + "skills/" + s.Name
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
		// Claude's placeholders are 0-based, OpenCode's 1-based: $0 and $ARGUMENTS[0] become $1.
		body, _ := target.Arguments(cmd.Doc, func(p string, i int) string {
			if i < 0 {
				return p
			}
			return "$" + strconv.Itoa(i+1)
		})
		at := target.Finding{Severity: target.SeverityWarn, Target: id, Kind: "command", Artifact: cmd.Name}
		if body != cmd.Doc.Body {
			f := at
			f.Code, f.Text = "opencode/arguments-renumbered", "Claude's 0-based argument placeholders became OpenCode's 1-based $1, $2, …"
			f.Hint = "check the command with a few arguments; OpenCode may split or join them differently"
			finds = append(finds, f)
		}
		if escaped.MatchString(body) {
			f := at
			f.Code, f.Text = "opencode/escaped-arguments", `OpenCode has no escape for $; \$1 and \$ARGUMENTS will still be replaced by arguments`
			f.Hint = "reword the text so it does not contain $ followed by a digit or ARGUMENTS"
			finds = append(finds, f)
		}
		doc.Body = body
		out = append(out, target.WriteText(id, "command", d+"commands/"+cmd.Name+".md", doc.Render(), ctx))
		if cmd.SidecarDir != "" {
			finds = append(finds, target.Finding{Severity: target.SeverityWarn, Target: id, Kind: "command", Artifact: cmd.Name,
				Code: "command-sidecar-dropped", Text: fmt.Sprintf("opencode would read the files in %s/ as commands; the sidecar directory is not copied", cmd.SidecarDir)})
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
		if _, set := doc.Frontmatter["mode"]; !set {
			doc = doc.Prepend("mode", "subagent")
		}
		out = append(out, target.WriteText(id, "agent", d+"agents/"+a.Name+".md", doc.Render(), ctx))
	}
	// Only MCP servers and rules use the config file, so only they may fail on reading it.
	var cfg string
	rulesHere := !ctx.SkipRules && slices.ContainsFunc(m.Rules, func(r module.Rule) bool { return module.Supports(r.Targets, id) })
	if rulesHere || slices.ContainsFunc(m.MCPServers, func(s module.MCPServer) bool { return module.Supports(s.Targets, id) }) {
		var err error
		if cfg, err = config(ctx.Root); err != nil {
			return nil, nil, err
		}
	}
	for _, s := range m.MCPServers {
		if !module.Supports(s.Targets, id) {
			continue
		}
		_, f, ok := target.Admit(id, caps, ctx, "mcp", s.Name, module.Doc{})
		if finds = append(finds, f...); !ok {
			continue
		}
		v, f := mcp(id, s, ctx)
		finds = append(finds, f...)
		value, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, plan.Intent{Target: id, Kind: "mcp", Op: plan.OpSetJSON, Path: cfg, Pointer: "/mcp/" + s.Name, Value: value})
	}
	// Rules are delivered verbatim, with one instructions member per module: OpenCode expands
	// a wildcard only in the last component of an absolute entry (§8).
	rulesDir := d + "lola/" + m.Name
	rules := false
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
		out = append(out, plan.Intent{Target: id, Kind: "rule", Op: plan.OpWriteFile, Path: rulesDir + "/" + r.Name + ".md", Data: []byte(module.Doc{Body: r.Body}.Render()), Mode: 0o644})
		rules = true
	}
	if rules {
		member := rulesDir + "/*.md"
		if ctx.Scope == target.User {
			member = filepath.Join(ctx.Root, rulesDir, "*.md")
		}
		out = append(out, plan.Intent{Target: id, Kind: "rule", Op: plan.OpAddJSONMember, Path: cfg, Pointer: "/instructions", Member: plan.StringMember(member)})
	}
	// Runtime hooks (Plan 5b design §7.1) are never rendered here: the kind is missing from caps,
	// since mapping Claude Code's events and tool names would be a cross-target value conversion.
	for _, h := range m.RuntimeHooks {
		if module.Supports(h.Targets, id) {
			_, f, _ := target.Admit(id, caps, ctx, target.KindRuntimeHook, h.Name(), module.Doc{})
			finds = append(finds, f...)
		}
	}
	return out, finds, nil
}

// config names OpenCode's config file under root: opencode.jsonc when it exists, else
// opencode.json.
func config(root string) (string, error) {
	_, err := os.Stat(filepath.Join(root, "opencode.jsonc"))
	switch {
	case err == nil:
		return "opencode.jsonc", nil
	case errors.Is(err, fs.ErrNotExist):
		return "opencode.json", nil
	default:
		return "", fmt.Errorf("opencode: cannot read %s: %w", filepath.Join(root, "opencode.jsonc"), err)
	}
}

// envRef is a ${VAR} or ${VAR:-default} environment reference.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-[^}]*)?\}`)

// mcp renders one server in opencode.json's shape: local
// {type:"local",command:[cmd,…args],environment}; remote {type:"remote",url,headers}.
// ${VAR} becomes OpenCode's {env:VAR}; ${VAR:-default} loses its default.
func mcp(id string, s module.MCPServer, ctx target.PlanContext) (map[string]any, []target.Finding) {
	var finds []target.Finding
	at := target.Finding{Target: id, Kind: "mcp", Artifact: s.Name}
	env := func(v string) string {
		return envRef.ReplaceAllStringFunc(target.Expand(v, ctx), func(ref string) string {
			sub := envRef.FindStringSubmatch(ref)
			to := "{env:" + sub[1] + "}"
			f := at
			if sub[2] == "" {
				f.Severity, f.Code, f.Text = target.SeverityInfo, "opencode/env-reference", ref+" becomes "+to
			} else {
				f.Severity, f.Code = target.SeverityWarn, "opencode/env-default-dropped"
				f.Text = fmt.Sprintf("opencode has no defaults for environment references; %s becomes %s, dropping %q", ref, to, sub[2][2:])
				f.Hint = "set " + sub[1] + " in the environment opencode runs in"
			}
			finds = append(finds, f)
			return to
		})
	}
	envMap := func(in map[string]string) map[string]string {
		out := make(map[string]string, len(in))
		for _, k := range slices.Sorted(maps.Keys(in)) {
			out[k] = env(in[k])
		}
		return out
	}
	v := map[string]any{}
	if s.Transport == module.TransportStdio {
		command := []string{env(s.Command)}
		for _, a := range s.Args {
			command = append(command, env(a))
		}
		v["type"], v["command"] = "local", command
		if len(s.Env) > 0 {
			v["environment"] = envMap(s.Env)
		}
		if s.Cwd != "" {
			f := at
			f.Severity, f.Code, f.Text = target.SeverityWarn, "mcp-cwd-dropped", fmt.Sprintf("opencode has no working-directory setting; cwd %q dropped", s.Cwd)
			finds = append(finds, f)
		}
		return v, finds
	}
	v["type"], v["url"] = "remote", env(s.URL)
	if len(s.Headers) > 0 {
		v["headers"] = envMap(s.Headers)
	}
	return v, finds
}
