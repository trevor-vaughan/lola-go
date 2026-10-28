package conformance_test

import (
	"maps"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// nothingToDo lists, for each registered target, the findings it produces that leave the user
// nothing to do, as "<severity> <code>" with the reason (Plan 10 §6). Every other finding a
// target produces carries a hint; a finding listed here carries none, and the spec below
// produces each entry.
var nothingToDo = map[string]map[string]string{
	"claude-code": {
		"warn mcp-cwd-dropped":             "claude-code has no working-directory setting, and an MCP server has no attribute file: it starts where Claude Code runs",
		"warn claude-code/allowed-tools":   "a review notice: lola installs the key as written and names the tools (user decision, 2026-10-10)",
		"warn claude-code/permission-mode": "a review notice: lola installs the key as written and names the mode",
	},
	"gemini-cli": {
		"skip kind-unsupported":     "gemini-cli has no place for the kind; no change to the module or the run gives it one",
		"info gemini-cli/arguments": "a lossless adaptation: $ARGUMENTS becomes {{args}}",
	},
	"opencode": {
		"skip kind-unsupported":        "opencode has no place for the kind; no change to the module or the run gives it one",
		"info frontmatter-key-dropped": "a lossless adaptation: the file name names an OpenCode agent",
		"warn mcp-cwd-dropped":         "opencode has no working-directory setting, and an MCP server has no attribute file: it starts where OpenCode runs",
		"warn command-sidecar-dropped": "opencode would read the sidecar files as commands; the command installs, and no module change gives opencode the directory",
		"info opencode/env-reference":  "a lossless adaptation: ${VAR} becomes {env:VAR}",
	},
}

// ruleBodies is, for each content rule code a registered target declares, a body the rule
// matches.
var ruleBodies = map[string]string{
	"gemini-cli/shell-injection":      "Run !`git diff`\n",
	"gemini-cli/arguments":            "Ship $ARGUMENTS\n",
	"gemini-cli/positional-arguments": "Fix $1\n",
	"opencode/shell-unchecked":        "Run !`git diff`\n",
	"opencode/shell-fence":            "```!\ngit status\n```\n",
	"skill-shell-dropped":             "Run !`git diff`\n",
	"skill-arguments-dropped":         "Fix $1\n",
}

// misfit is a YAML value that no Value of shape accepts; for a key with no shape, which only a
// Drop or a Guard rejects, a value that sets something.
func misfit(shape target.Shape) string {
	switch shape {
	case target.ShapeString:
		return "[1]"
	case target.ShapePositiveInt:
		return "0"
	}
	return "x"
}

// artifact is a module holding one artifact of kind named name: a skill, command or agent with
// the frontmatter lines fm (none when empty) and body, or an MCP server, rule or runtime hook set.
func artifact(kind, name, fm, body string) *module.Module {
	src := body
	if fm != "" {
		src = "---\n" + fm + "---\n" + body
	}
	m := &module.Module{Name: "example"}
	switch kind {
	case "skill":
		m.Skills = []module.Skill{{Name: name, Dir: "skills/" + name, Doc: doc(src)}}
	case "command":
		m.Commands = []module.Command{{Name: name, Doc: doc(src)}}
	case "agent":
		m.Agents = []module.Agent{{Name: name, Doc: doc(src)}}
	case "mcp":
		m.MCPServers = []module.MCPServer{{Name: name, Transport: module.TransportStdio, Command: "srv"}}
	case "rule":
		m.Rules = []module.Rule{{Name: name, Body: body}}
	case target.KindRuntimeHook:
		m.RuntimeHooks = []module.RuntimeHookSet{{Source: "hooks/hooks.json", Hooks: []byte(`{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}`)}}
	}
	return m
}

// tableModules is a module for each finding caps can produce: an artifact of each IR kind caps
// has no place for; for each skill, command and agent kind it has, one without each required key,
// one with a name it rejects, one with each key it knows set to a value of another shape, one
// with an unknown key and one with an ambiguous value; and one whose body matches each content
// rule.
func tableModules(caps target.Capabilities) []*module.Module {
	var out []*module.Module
	required := func(spec target.KindSpec) string {
		var b strings.Builder
		for _, r := range spec.Required {
			b.WriteString(r + ": d\n")
		}
		return b.String()
	}
	for _, kind := range kinds {
		spec, ok := caps.Kinds[kind]
		if !ok {
			out = append(out, artifact(kind, "a", "", "x\n"))
			continue
		}
		if kind != "skill" && kind != "command" && kind != "agent" {
			continue
		}
		base := required(spec)
		for _, r := range spec.Required {
			out = append(out, artifact(kind, "a", strings.Replace(base, r+": d\n", "", 1), "x\n"))
		}
		if spec.Name != nil {
			out = append(out, artifact(kind, "Not.A Name", base, "x\n"))
		}
		for _, k := range slices.Sorted(maps.Keys(spec.Keys)) {
			out = append(out, artifact(kind, "a", strings.Replace(base, k+": d\n", "", 1)+k+": "+misfit(spec.Keys[k].Shape)+"\n", "x\n"))
		}
		out = append(out, artifact(kind, "a", base+"zz-unknown: x\n", "x\n"), artifact(kind, "a", base+"zz-flag: yes\n", "x\n"))
	}
	for _, rule := range caps.Rules {
		Expect(ruleBodies).To(HaveKey(rule.Code), "ruleBodies needs a body that matches %s", rule.Code)
		out = append(out, artifact(rule.Kind, "a", required(caps.Kinds[rule.Kind]), ruleBodies[rule.Code]))
	}
	return out
}

// coached checks each finding of fs: one that list names carries no hint and is marked in seen,
// and any other carries one. It returns each finding's "<severity> <code>".
func coached(fs []target.Finding, list map[string]string, seen map[string]bool) []string {
	GinkgoHelper()
	var keys []string
	for _, f := range fs {
		key := string(f.Severity) + " " + f.Code
		keys = append(keys, key)
		if _, listed := list[key]; listed {
			Expect(f.Hint).To(BeEmpty(), "%s is listed as nothing to do, but carries a hint: drop it from nothingToDo[%q]", f.Describe(), f.Target)
			seen[key] = true
			continue
		}
		Expect(f.Hint).NotTo(BeEmpty(), "%s needs a hint, or an entry in nothingToDo[%q] saying why there is nothing to do", f.Describe(), f.Target)
	}
	return keys
}

// producer is a module that makes a target produce a finding its capability table does not
// describe: finding is that finding's "<severity> <code>", and with, when set, changes the
// spec's plan context.
type producer struct {
	finding string
	module  *module.Module
	with    func(*target.PlanContext)
}

// hooksOf is a module whose one runtime hook set is events.
func hooksOf(events string) *module.Module {
	return &module.Module{Name: "example", RuntimeHooks: []module.RuntimeHookSet{{Source: "hooks/hooks.json", Hooks: []byte(events)}}}
}

// own is a producer of each finding id's own rendering adds to its table's.
func own(id string) []producer {
	server := func(s module.MCPServer) *module.Module {
		s.Name, s.Transport, s.Command = "s", module.TransportStdio, "srv"
		return &module.Module{Name: "example", MCPServers: []module.MCPServer{s}}
	}
	switch id {
	case "claude-code":
		stop := `{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]}`
		exposed := func(c *target.PlanContext) {
			c.LocalSettingsExposed = ".claude/settings.local.json is not ignored by git"
		}
		spaced := func(c *target.PlanContext) { c.PluginRoot = "/copies/my kit" }
		breaking := func(c *target.PlanContext) { c.PluginRoot = "a: b" } // "description: a: b" no longer parses
		return []producer{
			{"warn mcp-cwd-dropped", server(module.MCPServer{Cwd: "/srv"}), nil},
			{"warn claude-code/allowed-tools", artifact("command", "c", "allowed-tools: Bash\n", "x\n"), nil},
			{"warn claude-code/allowed-tools", &module.Module{Name: "example", Commands: []module.Command{{Name: "c", Doc: doc("x\n"), SidecarDir: "commands/c",
				Sidecars: []module.Sidecar{{File: "commands/c/more.md", Doc: doc("---\nallowed-tools: Bash\n---\nx\n")}}}}}, nil},
			{"warn claude-code/permission-mode", artifact("agent", "a", "description: d\npermissionMode: plan\n", "x\n"), nil},
			{"skip claude-code/settings-local-not-ignored", hooksOf(stop), exposed},
			{"skip claude-code/runtime-hook-event-invalid", hooksOf(`{"not an event":[{"hooks":[{"type":"command","command":"x"}]}]}`), nil},
			{"skip claude-code/runtime-hook-shell-unsupported", hooksOf(`{"Stop":[{"hooks":[{"type":"command","command":"x","shell":"powershell"}]}]}`), nil},
			{"skip claude-code/runtime-hook-path-unsafe", hooksOf(`{"Stop":[{"hooks":[{"type":"command","command":"A=b","args":[]}]}]}`), nil},
			{"skip claude-code/runtime-hook-path-unsafe", hooksOf(stop), spaced},
			{"skip frontmatter-invalid", artifact("skill", "s", "description: ${PLUGIN_ROOT}\nhooks: {Stop: [{hooks: [{type: command, command: x}]}]}\n", "x\n"), breaking},
			{"skip frontmatter-invalid", artifact("agent", "a", "description: ${PLUGIN_ROOT}\nmcpServers: [{s: {command: srv}}]\n", "x\n"), breaking},
		}
	case "gemini-cli":
		return []producer{
			{"warn gemini-cli/positional-arguments", artifact("command", "c", "arguments: [file]\n", "Fix $file\n"), nil},
		}
	case "opencode":
		return []producer{
			{"skip name-rejected", artifact("skill", "s", "description: d\nname: other\n", "x\n"), nil},
			{"warn opencode/arguments-renumbered", artifact("command", "c", "", "Fix $0\n"), nil},
			{"warn opencode/escaped-arguments", artifact("command", "c", "", `Costs \$1`+"\n"), nil},
			{"warn command-sidecar-dropped", &module.Module{Name: "example", Commands: []module.Command{{Name: "c", Doc: doc("x\n"), SidecarDir: "commands/c"}}}, nil},
			{"info opencode/env-reference", server(module.MCPServer{Env: map[string]string{"A": "${X}"}}), nil},
			{"warn opencode/env-default-dropped", server(module.MCPServer{Env: map[string]string{"A": "${X:-d}"}}), nil},
			{"warn mcp-cwd-dropped", server(module.MCPServer{Cwd: "/srv"}), nil},
		}
	}
	Fail("own lists no producers for " + id + ": add a case, empty if its Plan adds no finding of its own")
	return nil
}

var _ = Describe("finding coaching", func() {
	for _, t := range targets.All() {
		It(t.ID()+" gives every finding it produces a hint, or lists it as nothing to do", func() {
			ctx := target.PlanContext{Scope: target.Project, Root: testenv.Isolate(), ContentDir: "/cache/example/content",
				PluginRoot: "/copies/example", PluginData: "/data/example"}
			list, ok := nothingToDo[t.ID()]
			Expect(ok).To(BeTrue(), "nothingToDo has no entry for %s", t.ID())
			seen := map[string]bool{}
			for _, m := range tableModules(t.Capabilities()) {
				_, fs, err := t.Plan(m, ctx)
				Expect(err).NotTo(HaveOccurred())
				coached(fs, list, seen)
			}
			for _, p := range own(t.ID()) {
				c := ctx
				if p.with != nil {
					p.with(&c)
				}
				_, fs, err := t.Plan(p.module, c)
				Expect(err).NotTo(HaveOccurred())
				Expect(coached(fs, list, seen)).To(ContainElement(p.finding), "the producer of %s", p.finding)
			}
			Expect(strings.Join(slices.Sorted(maps.Keys(seen)), "\n")).To(Equal(strings.Join(slices.Sorted(maps.Keys(list)), "\n")),
				"every nothingToDo entry is a finding %s produces", t.ID())
		})
	}
})
