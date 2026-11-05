package conformance_test

import (
	"fmt"
	"slices"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/trevor-vaughan/lola-go/internal/module"
	"github.com/trevor-vaughan/lola-go/internal/plan"
	"github.com/trevor-vaughan/lola-go/internal/target"
	"github.com/trevor-vaughan/lola-go/internal/targets"
	"github.com/trevor-vaughan/lola-go/internal/testenv"
)

// Verdicts of the executed-key table: the target gates the key under consent, or drops it with
// frontmatter-key-dropped.
const (
	gated   = "gated"
	dropped = "dropped"
)

// executed is Plan 7.5 task 1's table: for each target, component kind and frontmatter key
// spelling, what lola does with a key an assistant executes (hooks it runs, MCP servers it
// starts) or that shares a name with one. A key the assistant does not read ("no" in the
// table) is dropped, so no executing key reaches any assistant ungated under any spelling.
var executed = map[string]map[string]map[string]string{
	"claude-code": {
		// Claude Code runs hooks from skill, command and agent frontmatter and starts an agent's
		// mcpServers (code.claude.com/docs/en/skills, /sub-agents); it reads no mcpServers on a
		// skill or command.
		"skill":   {"hooks": gated, "Hooks": gated, "HOOKS": gated, "mcpServers": dropped, "mcpservers": dropped, "MCPSERVERS": dropped},
		"command": {"hooks": gated, "Hooks": gated, "HOOKS": gated, "mcpServers": dropped, "mcpservers": dropped, "MCPSERVERS": dropped},
		"agent":   {"hooks": gated, "Hooks": gated, "HOOKS": gated, "mcpServers": gated, "mcpservers": gated, "MCPSERVERS": gated},
	},
	"gemini-cli": {
		// Gemini CLI reads only name and description from SKILL.md (skillLoader.ts); a command is
		// TOML lola writes from description alone; an agent's strict schema starts mcp_servers
		// (agentLoader.ts), which lola's closed agent keys drop, and so do skills and commands, in
		// case a future reader starts them there unconsented (R-7-49).
		"skill": {"hooks": dropped, "Hooks": dropped, "mcpServers": dropped, "MCPSERVERS": dropped,
			"mcp_servers": dropped, "MCP_SERVERS": dropped, "Mcp_Servers": dropped},
		"command": {"hooks": dropped, "Hooks": dropped, "mcpServers": dropped, "MCPSERVERS": dropped,
			"mcp_servers": dropped, "MCP_SERVERS": dropped, "Mcp_Servers": dropped},
		"agent": {"hooks": dropped, "Hooks": dropped, "mcpServers": dropped, "MCPSERVERS": dropped, "mcp_servers": dropped},
	},
	"opencode": {
		// OpenCode reads name, description, license, compatibility and metadata from SKILL.md
		// (opencode.ai/docs/skills); its command and agent configs have no hooks or MCP key
		// (opencode.ai/config.json).
		"skill":   {"hooks": dropped, "Hooks": dropped, "mcpServers": dropped, "MCPSERVERS": dropped},
		"command": {"hooks": dropped, "Hooks": dropped, "mcpServers": dropped, "MCPSERVERS": dropped},
		"agent":   {"hooks": dropped, "Hooks": dropped, "mcpServers": dropped, "MCPSERVERS": dropped},
	},
}

// executedValue is a valid value for key: Claude Code's hooks event map, a list of inline MCP
// servers, or Gemini CLI's mcp_servers map.
func executedValue(key string) string {
	switch {
	case strings.EqualFold(key, "hooks"):
		return "{PreToolUse: [{matcher: Bash, hooks: [{type: command, command: echo run}]}]}"
	case strings.EqualFold(key, "mcp_servers"):
		return "{srv: {command: srv}}"
	}
	return "[{srv: {command: srv}}]"
}

// withKey is a module holding one component of kind whose frontmatter sets key.
func withKey(kind, key string) *module.Module {
	d := doc(fmt.Sprintf("---\ndescription: d\n%s: %s\n---\nBody.\n", key, executedValue(key)))
	m := &module.Module{Name: "example"}
	switch kind {
	case "skill":
		m.Skills = []module.Skill{{Name: "review", Dir: "skills/review", Doc: d}}
	case "command":
		m.Commands = []module.Command{{Name: "ship", Doc: d}}
	case "agent":
		m.Agents = []module.Agent{{Name: "critic", Doc: d}}
	}
	return m
}

// verdict is what a target's plan did with key: gated when an intent carries a consent for the
// key's hooks or servers, dropped when a frontmatter-key-dropped finding names the key and no
// intent's written frontmatter holds it, else "written".
func verdict(ins []plan.Intent, fs []target.Finding, key string) string {
	for _, in := range ins {
		if c := in.Consent; c != nil && (strings.EqualFold(key, "hooks") && c.Hooks != nil || !strings.EqualFold(key, "hooks") && c.MCPServers != nil) {
			return gated
		}
	}
	written := slices.ContainsFunc(ins, func(in plan.Intent) bool {
		d, err := module.ParseDoc(in.Data)
		Expect(err).NotTo(HaveOccurred(), "%s", in.Data)
		_, ok := d.Frontmatter[key]
		return ok
	})
	named := slices.ContainsFunc(fs, func(f target.Finding) bool {
		return f.Code == "frontmatter-key-dropped" && strings.HasPrefix(f.Text, key+" dropped")
	})
	if named && !written {
		return dropped
	}
	return "written"
}

var _ = Describe("frontmatter keys an assistant executes", func() {
	ctx := func() target.PlanContext {
		return target.PlanContext{Scope: target.Project, Root: testenv.Isolate(), ContentDir: "/cache/example/content",
			PluginRoot: "/copies/example", PluginData: "/data/example"}
	}

	It("every frontmatter key a target's assistant executes is gated by consent or dropped with a finding, under every accepted spelling", func() {
		Expect(executed).To(HaveLen(len(targets.All())), "every registered target has its row of the table")
		for _, t := range targets.All() {
			rows, ok := executed[t.ID()]
			Expect(ok).To(BeTrue(), t.ID()+" has no row in the executed-key table")
			for _, kind := range []string{"skill", "command", "agent"} {
				Expect(rows).To(HaveKey(kind), t.ID())
				spellings := rows[kind]
				for _, key := range []string{"hooks", "Hooks", "HOOKS", "hOoKs", "mcpServers", "mcpservers", "MCPSERVERS", "McpServers"} {
					ins, fs, err := t.Plan(withKey(kind, key), ctx())
					Expect(err).NotTo(HaveOccurred())
					got := verdict(ins, fs, key)
					Expect(got).To(BeElementOf(gated, dropped), "%s %s %s is written ungated", t.ID(), kind, key)
					if want, ok := spellings[key]; ok {
						Expect(got).To(Equal(want), "%s %s %s", t.ID(), kind, key)
					}
				}
				for key, want := range spellings {
					ins, fs, err := t.Plan(withKey(kind, key), ctx())
					Expect(err).NotTo(HaveOccurred())
					Expect(verdict(ins, fs, key)).To(Equal(want), "%s %s %s", t.ID(), kind, key)
				}
				// The positive control: a key no assistant executes is written as it is. Gemini's
				// commands and agents and OpenCode's commands read a closed key set, which drops
				// every key it does not list, x-note included, so they have no control.
				ins, fs, err := t.Plan(withKey(kind, "x-note"), ctx())
				Expect(err).NotTo(HaveOccurred())
				if kind == "skill" || t.ID() == "claude-code" || t.ID() == "opencode" && kind == "agent" {
					Expect(verdict(ins, fs, "x-note")).To(Equal("written"), "%s %s keeps a key it does not execute", t.ID(), kind)
				}
			}
		}
	})
})
